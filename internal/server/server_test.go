package server

import (
	"bufio"
	"bytes"
	"context"
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

	resp, pairOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/computers/pair", map[string]any{
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

	// 3. Create Agent Lin -> Computer receives provision control frame
	resp, agentOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/agents", map[string]any{
		"display_name": "Lin",
		"role_text":    "Engineer",
		"computer_id":  compID,
		"driver_kind":  domain.DriverBuiltin,
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
