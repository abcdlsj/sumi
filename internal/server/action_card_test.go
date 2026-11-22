package server

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

func TestAgentCreateConfirmationGateAndSilentEnv(t *testing.T) {
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

	if resp, _ := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/auth/register", map[string]any{
		"email": "owner@sumi.dev", "password": "password123", "display_name": "Owner",
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status=%d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces", map[string]any{
		"slug": "studio", "name": "Studio",
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create space status=%d", resp.StatusCode)
	}
	resp, pairOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/computers/pair", map[string]any{
		"name": "node", "hostname": "node.local", "os": "macos",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair status=%d", resp.StatusCode)
	}
	compToken := pairOut["token"].(string)
	compID := pairOut["computer"].(map[string]any)["id"].(string)

	_, agentOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/agents", map[string]any{
		"display_name":  "Lin",
		"role_text":     "Engineer",
		"computer_id":   compID,
		"driver_kind":   domain.DriverBuiltin,
		"driver_config": map[string]any{"base_url": "http://127.0.0.1:1/v1", "model": "m"},
	}, "")
	linID := agentOut["agent"].(map[string]any)["id"].(string)

	agentClient := &http.Client{}
	agentCreateURL := ts.URL + "/api/v1/computer/agents/" + linID + "/agents"

	// 1. Without agent.create permission, even proposing a confirmation card is denied.
	resp, denied := doJSON(t, agentClient, http.MethodPost, agentCreateURL, map[string]any{
		"display_name": "Helper",
		"role_text":    "Reviewer",
	}, compToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without permission, got %d (%v)", resp.StatusCode, denied)
	}

	// 2. Grant agent.create -> default mode (no env) posts a confirmation card (202) and does not create the agent.
	if resp, _ := doJSON(t, human, http.MethodPut, ts.URL+"/api/v1/spaces/studio/members/"+linID+"/permissions/agent.create", map[string]any{
		"granted": true,
	}, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("grant permission status=%d", resp.StatusCode)
	}

	t.Setenv("SUMI_ALLOW_AGENT_SILENT_CREATE", "")
	resp, proposed := doJSON(t, agentClient, http.MethodPost, agentCreateURL, map[string]any{
		"display_name": "Helper",
		"role_text":    "Reviewer",
	}, compToken)
	if resp.StatusCode != http.StatusAccepted || proposed["status"] != "pending_confirmation" {
		t.Fatalf("expected 202 pending_confirmation, got %d (%v)", resp.StatusCode, proposed)
	}
	cardID := proposed["action_card_message_id"].(string)
	if cardID == "" {
		t.Fatalf("missing action_card_message_id")
	}

	// Human confirms the card via the standard human create-agent endpoint.
	resp, confirmed := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/agents", map[string]any{
		"display_name":           "Helper",
		"role_text":              "Reviewer edited by human",
		"computer_id":            compID,
		"driver_kind":            domain.DriverBuiltin,
		"driver_config":          map[string]any{"base_url": "http://127.0.0.1:1/v1", "model": "m"},
		"action_card_message_id": cardID,
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 confirming card, got %d (%v)", resp.StatusCode, confirmed)
	}

	// 3. Another proposal dismissed by human via dismiss endpoint.
	resp, proposed2 := doJSON(t, agentClient, http.MethodPost, agentCreateURL, map[string]any{
		"display_name": "TempBot",
		"role_text":    "Temporary",
	}, compToken)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 for second proposal, got %d", resp.StatusCode)
	}
	card2ID := proposed2["action_card_message_id"].(string)
	resp, dismissed := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/action-cards/"+card2ID+"/dismiss", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 dismissing card, got %d (%v)", resp.StatusCode, dismissed)
	}

	// 4. With SUMI_ALLOW_AGENT_SILENT_CREATE=1, privileged agent creates silently (201).
	t.Setenv("SUMI_ALLOW_AGENT_SILENT_CREATE", "1")
	resp, silent := doJSON(t, agentClient, http.MethodPost, agentCreateURL, map[string]any{
		"display_name":  "SilentBot",
		"role_text":     "Created in brain-in-vat",
		"driver_kind":   domain.DriverBuiltin,
		"driver_config": map[string]any{"base_url": "http://127.0.0.1:1/v1", "model": "m"},
	}, compToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 with silent env, got %d (%v)", resp.StatusCode, silent)
	}
	if silent["agent"].(map[string]any)["display_name"] != "SilentBot" {
		t.Fatalf("unexpected silent agent: %v", silent)
	}
}
