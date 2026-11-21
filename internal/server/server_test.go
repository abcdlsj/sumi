package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
	"github.com/abcdlsj/sumi/internal/store"
)

func doJSON(t *testing.T, client *http.Client, method, url string, body any, bearer string) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestEndToEndHTTPComputerWSAndSSE(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := app.NewService(st, app.NewEventHub(), nil)
	srv := New(svc, Config{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	jar, _ := cookiejar.New(nil)
	human := &http.Client{Jar: jar}

	// 1. Register + Create Space + Pair Computer
	resp, _ := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/auth/register", map[string]any{
		"email":        "lisi@sumi.dev",
		"password":     "password123",
		"display_name": "Lisi",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status=%d", resp.StatusCode)
	}

	resp, _ = doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces", map[string]any{
		"slug": "studio",
		"name": "Sumi Studio",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create space status=%d", resp.StatusCode)
	}

	resp, pairOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/computers/pair", map[string]any{
		"name":     "mac-studio",
		"hostname": "mac.local",
		"os":       "macos",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair computer status=%d", resp.StatusCode)
	}
	compToken := pairOut["token"].(string)
	compID := pairOut["computer"].(map[string]any)["id"].(string)

	// 2. Connect Computer WebSocket and complete Hello handshake
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/computer"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsConn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + compToken}},
	})
	if err != nil {
		t.Fatalf("dial computer ws: %v", err)
	}
	defer wsConn.CloseNow()

	helloRaw, _ := json.Marshal(protocol.Envelope{
		Type: protocol.TypeHello,
		Hello: &protocol.HelloFrame{
			ProtocolVersion: protocol.Version,
			DaemonSessionID: "sess-1",
			DaemonVersion:   "2.0.0",
			Hostname:        "mac.local",
			OS:              "macos",
		},
	})
	if err := wsConn.Write(ctx, websocket.MessageText, helloRaw); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	_, ackBytes, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatalf("read hello_ack: %v", err)
	}
	var ack protocol.Envelope
	if err := json.Unmarshal(ackBytes, &ack); err != nil || ack.Type != protocol.TypeHelloAck {
		t.Fatalf("unexpected ack: %s", string(ackBytes))
	}
	if !protocol.Has(ack.HelloAck.Capabilities, protocol.CapAgentActivity) {
		t.Fatalf("hello_ack must advertise server capabilities: %v", ack.HelloAck.Capabilities)
	}

	// 3. Create Agent Lin -> Computer receives provision control frame
	resp, agentOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/agents", map[string]any{
		"display_name":  "Lin",
		"role_text":     "Engineer",
		"computer_id":   compID,
		"driver_kind":   domain.DriverBuiltin,
		"driver_config": map[string]any{"base_url": "http://127.0.0.1:1/v1", "model": "m"},
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create agent status=%d", resp.StatusCode)
	}
	agentID := agentOut["agent"].(map[string]any)["id"].(string)

	_, ctrlBytes, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatalf("read provision frame: %v", err)
	}
	var ctrl protocol.Envelope
	if err := json.Unmarshal(ctrlBytes, &ctrl); err != nil || ctrl.Type != protocol.TypeAgentControl || ctrl.AgentControl.Action != "provision" {
		t.Fatalf("expected provision control frame, got %s", string(ctrlBytes))
	}

	// 4. Open SSE stream and verify live message.created delivery + Computer Wake frame
	sseReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/spaces/studio/events", nil)
	sseResp, err := human.Do(sseReq)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	defer sseResp.Body.Close()

	_, bootOut := doJSON(t, human, http.MethodGet, ts.URL+"/api/v1/spaces/studio/bootstrap", nil, "")
	convs := bootOut["conversations"].([]any)
	generalID := convs[0].(map[string]any)["id"].(string)

	resp, _ = doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/conversations/"+generalID+"/messages", map[string]any{
		"body_markdown": "@Lin check this out",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("send message status=%d", resp.StatusCode)
	}

	// Computer should receive Wake frame for Lin
	_, wakeBytes, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatalf("read wake frame: %v", err)
	}
	var wake protocol.Envelope
	if err := json.Unmarshal(wakeBytes, &wake); err != nil || wake.Type != protocol.TypeWake || wake.Wake.AgentMemberID != agentID {
		t.Fatalf("expected wake for Lin, got %s", string(wakeBytes))
	}

	// Browser SSE should receive message.created
	scanner := bufio.NewScanner(sseResp.Body)
	foundMsgEvent := false
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "event: message.created") {
			foundMsgEvent = true
			break
		}
	}
	if !foundMsgEvent {
		t.Fatalf("did not receive message.created on SSE")
	}

	// 5. Agent attempts to send before reading -> HTTP 409 Held with unread messages
	compClient := &http.Client{}
	resp, heldOut := doJSON(t, compClient, http.MethodPost, ts.URL+"/api/v1/computer/agents/"+agentID+"/conversations/"+generalID+"/messages", map[string]any{
		"body_markdown": "Blind reply",
	}, compToken)
	if resp.StatusCode != http.StatusConflict || heldOut["held"] != true {
		t.Fatalf("expected 409 held, got status=%d body=%+v", resp.StatusCode, heldOut)
	}

	// Resending after hold succeeds
	resp, _ = doJSON(t, compClient, http.MethodPost, ts.URL+"/api/v1/computer/agents/"+agentID+"/conversations/"+generalID+"/messages", map[string]any{
		"body_markdown": "Read and acknowledged",
	}, compToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 after hold, got %d", resp.StatusCode)
	}
}

func TestSSEFiltersPrivateChannelsAndPersonalGroups(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := app.NewService(st, app.NewEventHub(), nil)
	srv := New(svc, Config{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ownerJar, _ := cookiejar.New(nil)
	ownerClient := &http.Client{Jar: ownerJar}
	resp, _ := doJSON(t, ownerClient, http.MethodPost, ts.URL+"/api/v1/auth/register", map[string]any{
		"email": "owner@sumi.dev", "password": "password123", "display_name": "Owner",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register owner status=%d", resp.StatusCode)
	}
	resp, spaceOut := doJSON(t, ownerClient, http.MethodPost, ts.URL+"/api/v1/spaces", map[string]any{
		"slug": "studio", "name": "Studio",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create space status=%d", resp.StatusCode)
	}
	spaceID := spaceOut["space"].(map[string]any)["id"].(string)

	// Register a second human member and add them to the space.
	otherJar, _ := cookiejar.New(nil)
	otherClient := &http.Client{Jar: otherJar}
	resp, otherUserOut := doJSON(t, otherClient, http.MethodPost, ts.URL+"/api/v1/auth/register", map[string]any{
		"email": "other@sumi.dev", "password": "password123", "display_name": "Other",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register other status=%d", resp.StatusCode)
	}
	otherUserID := otherUserOut["user"].(map[string]any)["id"].(string)
	if err := st.WithWriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO members (id, space_id, user_id, kind, display_name, access_level, created_at) VALUES ('m-other', ?, ?, 'human', 'Other', 'member', ?)`,
			spaceID, otherUserID, store.FormatTime(time.Now().UTC()),
		)
		return err
	}); err != nil {
		t.Fatalf("insert other member: %v", err)
	}

	// Owner creates a private channel without Other, plus a personal channel group.
	resp, privOut := doJSON(t, ownerClient, http.MethodPost, ts.URL+"/api/v1/spaces/studio/channels", map[string]any{
		"slug": "secret-room", "kind": "private",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create private channel status=%d", resp.StatusCode)
	}
	privID := privOut["conversation"].(map[string]any)["id"].(string)

	_, bootOut := doJSON(t, ownerClient, http.MethodGet, ts.URL+"/api/v1/spaces/studio/bootstrap", nil, "")
	var generalID string
	for _, raw := range bootOut["conversations"].([]any) {
		c := raw.(map[string]any)
		if c["slug"] == "general" {
			generalID = c["id"].(string)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sseReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/spaces/studio/events", nil)
	sseResp, err := otherClient.Do(sseReq)
	if err != nil {
		t.Fatalf("open other SSE: %v", err)
	}
	defer sseResp.Body.Close()

	// Owner posts to private channel, creates personal group, then posts to #general.
	doJSON(t, ownerClient, http.MethodPost, ts.URL+"/api/v1/spaces/studio/conversations/"+privID+"/messages", map[string]any{
		"body_markdown": "private note",
	}, "")
	doJSON(t, ownerClient, http.MethodPost, ts.URL+"/api/v1/spaces/studio/channel-groups", map[string]any{
		"name": "Owner Private Group",
	}, "")
	doJSON(t, ownerClient, http.MethodPost, ts.URL+"/api/v1/spaces/studio/conversations/"+generalID+"/messages", map[string]any{
		"body_markdown": "public hello",
	}, "")

	scanner := bufio.NewScanner(sseResp.Body)
	var curEvent string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			curEvent = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if strings.Contains(data, privID) {
				t.Fatalf("leaked private channel event to non-member: %s %s", curEvent, data)
			}
			if curEvent == "channel_groups.changed" {
				t.Fatalf("leaked personal channel_groups.changed to another member: %s", data)
			}
			if curEvent == "message.created" && strings.Contains(data, generalID) {
				return
			}
		}
	}
	t.Fatalf("never received expected public channel SSE event")
}
