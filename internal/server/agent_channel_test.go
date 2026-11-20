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

// TestAgentChannelMembersAndTaskShow covers the capability-socket routes behind
// `sumi channel members|invite|remove` and `sumi task show`.
func TestAgentChannelMembersAndTaskShow(t *testing.T) {
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
		t.Fatalf("pair computer status=%d", resp.StatusCode)
	}
	compToken := pairOut["token"].(string)
	compID := pairOut["computer"].(map[string]any)["id"].(string)

	_, agentsOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/agents", map[string]any{
		"display_name":  "Lin",
		"role_text":     "Engineer",
		"computer_id":   compID,
		"driver_kind":   domain.DriverBuiltin,
		"driver_config": map[string]any{"base_url": "http://127.0.0.1:1/v1", "model": "m"},
	}, "")
	agentID := agentsOut["agent"].(map[string]any)["id"].(string)

	_, membersOut := doJSON(t, human, http.MethodGet, ts.URL+"/api/v1/spaces/studio/members", nil, "")
	var ownerID string
	for _, raw := range membersOut["members"].([]any) {
		if m := raw.(map[string]any); m["kind"] == string(domain.MemberKindHuman) {
			ownerID = m["id"].(string)
		}
	}
	if ownerID == "" {
		t.Fatal("owner member id not found")
	}

	// Agent needs channel.manage_members to invite/remove.
	resp, _ = doJSON(t, human, http.MethodPut,
		ts.URL+"/api/v1/spaces/studio/members/"+agentID+"/permissions/"+string(domain.PermChannelManageMembers),
		map[string]any{"granted": true}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grant permission status=%d", resp.StatusCode)
	}

	resp, chOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/channels", map[string]any{
		"slug": "ops", "kind": "private", "member_ids": []string{agentID},
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create channel status=%d", resp.StatusCode)
	}
	opsID := chOut["conversation"].(map[string]any)["id"].(string)

	compClient := &http.Client{}
	agentURL := ts.URL + "/api/v1/computer/agents/" + agentID + "/conversations/" + opsID + "/members"

	resp, listOut := doJSON(t, compClient, http.MethodGet, agentURL, nil, compToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent list members status=%d", resp.StatusCode)
	}
	if got := len(listOut["members"].([]any)); got != 2 {
		t.Fatalf("private channel must list its 2 members, got %d", got)
	}

	resp, _ = doJSON(t, compClient, http.MethodDelete, agentURL+"/"+ownerID, nil, compToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent remove member status=%d", resp.StatusCode)
	}
	resp, listOut = doJSON(t, compClient, http.MethodGet, agentURL, nil, compToken)
	if resp.StatusCode != http.StatusOK || len(listOut["members"].([]any)) != 1 {
		t.Fatalf("after removal list status=%d members=%v", resp.StatusCode, listOut["members"])
	}

	resp, _ = doJSON(t, compClient, http.MethodPost, agentURL, map[string]any{"member_id": ownerID}, compToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent invite member status=%d", resp.StatusCode)
	}
	resp, listOut = doJSON(t, compClient, http.MethodGet, agentURL, nil, compToken)
	if resp.StatusCode != http.StatusOK || len(listOut["members"].([]any)) != 2 {
		t.Fatalf("after invite list status=%d members=%v", resp.StatusCode, listOut["members"])
	}

	// Task show is readable through the agent API.
	resp, taskOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/tasks", map[string]any{
		"channel_id": opsID, "title": "ship it",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task status=%d", resp.StatusCode)
	}
	taskID := taskOut["task"].(map[string]any)["id"].(string)
	resp, showOut := doJSON(t, compClient, http.MethodGet, ts.URL+"/api/v1/computer/agents/"+agentID+"/tasks/"+taskID, nil, compToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent task show status=%d", resp.StatusCode)
	}
	if got := showOut["task"].(map[string]any)["title"]; got != "ship it" {
		t.Fatalf("task show title = %v", got)
	}

	// A private channel the Agent is absent from is not readable.
	resp, otherOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/channels", map[string]any{
		"slug": "secret", "kind": "private",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create second channel status=%d", resp.StatusCode)
	}
	secretID := otherOut["conversation"].(map[string]any)["id"].(string)
	resp, _ = doJSON(t, compClient, http.MethodGet,
		ts.URL+"/api/v1/computer/agents/"+agentID+"/conversations/"+secretID+"/members", nil, compToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member must be forbidden, got %d", resp.StatusCode)
	}
}
