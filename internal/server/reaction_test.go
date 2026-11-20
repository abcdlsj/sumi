package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

func TestHTTPReactionsHumanAgentAndSSE(t *testing.T) {
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
		"name":     "mac",
		"hostname": "mac.local",
		"os":       "macos",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair computer status=%d", resp.StatusCode)
	}
	compToken := pairOut["token"].(string)
	compID := pairOut["computer"].(map[string]any)["id"].(string)

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

	_, bootOut := doJSON(t, human, http.MethodGet, ts.URL+"/api/v1/spaces/studio/bootstrap", nil, "")
	generalID := bootOut["conversations"].([]any)[0].(map[string]any)["id"].(string)

	resp, sendOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/conversations/"+generalID+"/messages", map[string]any{
		"body_markdown": "Ready to merge",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("send status=%d", resp.StatusCode)
	}
	msgID := sendOut["message"].(map[string]any)["id"].(string)

	// Open SSE stream to confirm message.reaction_changed delivery.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sseReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/spaces/studio/events", nil)
	sseResp, err := human.Do(sseReq)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	defer sseResp.Body.Close()

	// Human adds thumbsup via HTTP.
	resp, reactOut := doJSON(t, human, http.MethodPut, ts.URL+"/api/v1/spaces/studio/messages/"+msgID+"/reactions/thumbsup", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("human add reaction status=%d body=%+v", resp.StatusCode, reactOut)
	}
	if len(reactOut["reactions"].([]any)) != 1 {
		t.Fatalf("expected 1 reaction group, got %+v", reactOut)
	}

	// Verify SSE event emitted.
	scanner := bufio.NewScanner(sseResp.Body)
	found := false
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "event: message.reaction_changed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected message.reaction_changed SSE event")
	}

	// Agent adds thumbsup and eyes via computer-brokered route.
	compClient := &http.Client{}
	resp, reactOut = doJSON(t, compClient, http.MethodPut, ts.URL+"/api/v1/computer/agents/"+agentID+"/messages/"+msgID+"/reactions/thumbsup", nil, compToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent add thumbsup status=%d", resp.StatusCode)
	}
	groups := reactOut["reactions"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["count"].(float64) != 2 {
		t.Fatalf("expected thumbsup count=2, got %+v", groups)
	}

	// Invalid emoji returns 400.
	resp, _ = doJSON(t, human, http.MethodPut, ts.URL+"/api/v1/spaces/studio/messages/"+msgID+"/reactions/nope", nil, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid emoji, got %d", resp.StatusCode)
	}

	// Human removes thumbsup -> count drops to 1.
	resp, reactOut = doJSON(t, human, http.MethodDelete, ts.URL+"/api/v1/spaces/studio/messages/"+msgID+"/reactions/thumbsup", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("human remove reaction status=%d", resp.StatusCode)
	}
	groups = reactOut["reactions"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["count"].(float64) != 1 || groups[0].(map[string]any)["reacted"].(bool) {
		t.Fatalf("expected count=1 reacted=false after removal, got %+v", groups)
	}
}
