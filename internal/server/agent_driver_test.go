package server

import (
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
	"github.com/abcdlsj/sumi/internal/protocol"
	"github.com/abcdlsj/sumi/internal/store"
)

func TestAgentDriverConfigAPIRedactsSecrets(t *testing.T) {
	const secret = "sk-http-secret-123"
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	svc := app.NewService(st, app.NewEventHub(), nil)
	ts := httptest.NewServer(New(svc, Config{}, nil).Handler())
	defer ts.Close()

	jar, _ := cookiejar.New(nil)
	human := &http.Client{Jar: jar}
	mustStatus := func(resp *http.Response, want int, what string) {
		t.Helper()
		if resp.StatusCode != want {
			t.Fatalf("%s: status=%d want %d", what, resp.StatusCode, want)
		}
	}
	noSecret := func(v any, what string) string {
		t.Helper()
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%s leaked api_key: %s", what, raw)
		}
		return string(raw)
	}

	resp, _ := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/auth/register", map[string]any{
		"email": "owner@sumi.dev", "password": "password123", "display_name": "Owner",
	}, "")
	mustStatus(resp, http.StatusCreated, "register")
	resp, _ = doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces", map[string]any{"slug": "studio", "name": "Studio"}, "")
	mustStatus(resp, http.StatusCreated, "create space")
	resp, pairOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/computers/pair", map[string]any{
		"name": "mac", "hostname": "mac.local", "os": "macos",
	}, "")
	mustStatus(resp, http.StatusCreated, "pair")
	compToken := pairOut["token"].(string)
	compID := pairOut["computer"].(map[string]any)["id"].(string)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/computer"
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + compToken}},
	})
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer ws.CloseNow()
	hello, _ := json.Marshal(protocol.Envelope{Type: protocol.TypeHello, Hello: &protocol.HelloFrame{
		ProtocolVersion: protocol.Version, DaemonSessionID: "s1", DaemonVersion: "2.0.0",
		Hostname: "mac.local", OS: "macos", ACPProviders: []string{"pi", "codex"},
	}})
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	if _, _, err := ws.Read(ctx); err != nil {
		t.Fatalf("read ack: %v", err)
	}
	readControl := func() protocol.AgentControlFrame {
		t.Helper()
		_, raw, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("read control: %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil || env.AgentControl == nil {
			t.Fatalf("expected control frame, got %s", raw)
		}
		return *env.AgentControl
	}

	_, compsOut := doJSON(t, human, http.MethodGet, ts.URL+"/api/v1/spaces/studio/computers", nil, "")
	if raw := noSecret(compsOut, "computers"); !strings.Contains(raw, `"acp_providers":["pi","codex"]`) {
		t.Fatalf("computers missing acp_providers: %s", raw)
	}

	resp, bad := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/agents", map[string]any{
		"display_name": "Bad", "role_text": "x", "computer_id": compID, "driver_kind": "builtin",
		"driver_config": map[string]any{"base_url": "localhost", "model": "m"},
	}, "")
	mustStatus(resp, http.StatusBadRequest, "invalid builtin config")
	if raw, _ := json.Marshal(bad); !strings.Contains(string(raw), "driver_config.base_url must be an http(s) URL") {
		t.Fatalf("error must name field: %s", raw)
	}

	resp, created := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/agents", map[string]any{
		"display_name": "Lin", "role_text": "Engineer", "computer_id": compID, "driver_kind": "builtin",
		"driver_config": map[string]any{"base_url": "https://api.example.com/v1", "model": "gpt", "api_key": secret},
	}, "")
	mustStatus(resp, http.StatusCreated, "create agent")
	noSecret(created, "create")
	assertDriverView(t, created, map[string]any{"base_url": "https://api.example.com/v1", "model": "gpt", "api_key_set": true})
	agentID := created["agent"].(map[string]any)["id"].(string)
	if ctrl := readControl(); ctrl.Action != "provision" || ctrl.Spec.DriverConfig.APIKey != secret {
		t.Fatalf("provision must carry full config, got %+v", ctrl)
	}

	_, boot := doJSON(t, human, http.MethodGet, ts.URL+"/api/v1/spaces/studio/bootstrap", nil, "")
	if raw := noSecret(boot, "bootstrap"); !strings.Contains(raw, `"api_key_set":true`) {
		t.Fatalf("bootstrap missing redacted view: %s", raw)
	}
	_, members := doJSON(t, human, http.MethodGet, ts.URL+"/api/v1/spaces/studio/members", nil, "")
	noSecret(members, "members")
	_, agentMembers := doJSON(t, &http.Client{}, http.MethodGet, ts.URL+"/api/v1/computer/agents/"+agentID+"/members", nil, compToken)
	noSecret(agentMembers, "agent-facing members")

	resp, updated := doJSON(t, human, http.MethodPut, ts.URL+"/api/v1/spaces/studio/agents/"+agentID+"/driver", map[string]any{
		"driver_kind": "builtin", "driver_config": map[string]any{"base_url": "https://other.example/v1", "model": "gpt-2", "api_key": ""},
	}, "")
	mustStatus(resp, http.StatusOK, "update driver")
	noSecret(updated, "update")
	assertDriverView(t, updated, map[string]any{"base_url": "https://other.example/v1", "model": "gpt-2", "api_key_set": true})
	if ctrl := readControl(); ctrl.Action != "provision" || ctrl.Spec.DriverConfig.APIKey != secret || ctrl.Spec.DriverConfig.Model != "gpt-2" {
		t.Fatalf("re-provision must carry kept key and new model, got %+v", ctrl)
	}

	resp, _ = doJSON(t, human, http.MethodPut, ts.URL+"/api/v1/spaces/studio/agents/"+agentID+"/driver", map[string]any{
		"driver_kind": "acp", "driver_config": map[string]any{"provider": "gemini"},
	}, "")
	mustStatus(resp, http.StatusBadRequest, "invalid acp provider")
}

// assertDriverView checks resp.agent.agent.driver_config has exactly the wanted keys and values.
func assertDriverView(t *testing.T, resp map[string]any, want map[string]any) {
	t.Helper()
	got := resp["agent"].(map[string]any)["agent"].(map[string]any)["driver_config"].(map[string]any)
	if len(got) != len(want) {
		t.Fatalf("driver_config=%v want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("driver_config=%v want %v", got, want)
		}
	}
}
