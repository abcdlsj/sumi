package scenarios

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
)

// TestPiACPEndToEnd drives a real `pi` coding agent through the native Pi RPC driver: the agent
// must read its DM and reply via the `sumi` CLI that the daemon puts on PATH.
// Opt in with SUMI_E2E_PI=1; requires `pi` on PATH and pi's own login.
func TestPiACPEndToEnd(t *testing.T) {
	if os.Getenv("SUMI_E2E_PI") != "1" {
		t.Skip("set SUMI_E2E_PI=1 to run the real pi end-to-end check")
	}
	if _, err := exec.LookPath("pi"); err != nil {
		t.Fatalf("pi not on PATH: %v", err)
	}
	env := startScenarioEnv(t, nil)
	ctx := context.Background()

	comps, err := env.svc.ListComputers(ctx, env.space.ID)
	if err != nil || len(comps) != 1 || !strings.Contains(strings.Join(comps[0].ACPProviders, ","), "pi") {
		t.Fatalf("computer must report pi in acp_providers, got %+v err=%v", comps, err)
	}

	agent, err := env.svc.CreateAgent(ctx, env.owner, app.CreateAgentInput{
		DisplayName:  "PiPong",
		RoleText:     "Test agent. Follow instructions exactly.",
		ComputerID:   env.comp.ID,
		DriverKind:   domain.DriverACP,
		DriverConfig: domain.DriverConfig{Provider: domain.ACPProviderPi},
	})
	if err != nil {
		t.Fatalf("create pi agent: %v", err)
	}
	dm, err := env.svc.GetOrCreateDM(ctx, env.owner, agent.ID)
	if err != nil {
		t.Fatalf("open dm: %v", err)
	}
	if _, err := env.svc.SendMessage(ctx, env.owner, app.SendMessageInput{
		ConversationID: dm.ID,
		BodyMarkdown:   "Reply with exactly: pong",
	}); err != nil {
		t.Fatalf("send dm: %v", err)
	}

	agentState := func() *domain.AgentInfo {
		m, err := env.svc.GetMember(ctx, env.space.ID, agent.ID)
		if err != nil {
			return nil
		}
		return m.Agent
	}
	var reply string
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		page, err := env.svc.ListMessages(ctx, env.owner, app.ListMessagesInput{ConversationID: dm.ID, Limit: 50})
		if err == nil {
			for _, m := range page.Messages {
				if m.AuthorMemberID == agent.ID && m.Kind == domain.MessageKindText {
					reply = m.BodyMarkdown
				}
			}
		}
		info := agentState()
		if reply != "" && info != nil && info.RuntimeState == domain.RuntimeIdle {
			break
		}
		if info != nil && info.RuntimeState == domain.RuntimeError {
			t.Fatalf("agent entered error state: code=%s detail=%s", info.LastErrorCode, info.LastErrorDetail)
		}
		time.Sleep(500 * time.Millisecond)
	}
	info := agentState()
	if reply == "" {
		if info != nil {
			t.Fatalf("no reply from pi agent within 3m: state=%s code=%s detail=%s", info.RuntimeState, info.LastErrorCode, info.LastErrorDetail)
		}
		t.Fatalf("no reply from pi agent within 3m")
	}
	if info == nil || info.RuntimeState != domain.RuntimeIdle {
		t.Fatalf("agent did not return to idle: %+v", info)
	}
	t.Logf("pi agent replied (%d chars, contains pong=%v)", len(reply), strings.Contains(strings.ToLower(reply), "pong"))
	if !strings.Contains(strings.ToLower(reply), "pong") {
		t.Fatalf("reply does not contain pong")
	}
}
