package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

// TestBrainInVat verifies the full Agent capability loop through real Server, SQLite,
// Computer WebSocket, Unix capability socket, and the compiled `sumi` CLI binary:
//   - Wake delivery and inbox read
//   - Lazy task conversation creation via claim
//   - Workspace file write
//   - Freshness Guard hold + self-correction
//   - Permission-gated child agent creation (and denial for unprivileged child)
func TestBrainInVat(t *testing.T) {
	// Brain-in-a-vat runs unattended end-to-end, so opt into silent CLI child-agent creation.
	t.Setenv("SUMI_ALLOW_AGENT_SILENT_CREATE", "1")
	var binPath string
	var childDeniedChecked atomic.Bool
	var parentCompleted atomic.Bool

	factory := func(spec protocol.AssignedAgentSpec, _ string) (driver.SessionDriver, error) {
		return &funcDriver{
			fn: func(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error) {
				if spec.DisplayName == "XiaoZhu" {
					// Child agent without agent.create permission must be refused.
					res, code, err := runSumiCLI(ctx, binPath, in, "agent", "create", "--name", "Unauthorized", "--role", "Should fail")
					if err != nil {
						return driver.TurnResult{}, err
					}
					if res.OK || code == 0 || res.Error == nil || res.Error.Code != "permission_denied" {
						return driver.TurnResult{}, fmt.Errorf("expected unprivileged child agent.create to fail with permission_denied")
					}
					childDeniedChecked.Store(true)
					return driver.TurnResult{FinalText: "permission check verified"}, nil
				}

				// Parent agent LinChe executes the end-to-end checklist via real CLI calls.
				inboxRes, _, err := runSumiCLI(ctx, binPath, in, "inbox")
				if err != nil || !inboxRes.OK {
					return driver.TurnResult{}, fmt.Errorf("inbox failed: %v %+v", err, inboxRes)
				}
				var inboxPayload struct {
					Items []domain.InboxItem `json:"items"`
				}
				if err := json.Unmarshal(inboxRes.Data, &inboxPayload); err != nil || len(inboxPayload.Items) == 0 {
					return driver.TurnResult{}, fmt.Errorf("expected unread inbox item")
				}
				convID := inboxPayload.Items[0].ConversationID

				// 1. Attempt blind send before reading -> must be held by Freshness Guard!
				heldRes, code, err := runSumiCLI(ctx, binPath, in, "send", "--body", "Blind reply", convID)
				if err != nil {
					return driver.TurnResult{}, err
				}
				if heldRes.OK || code == 0 || heldRes.Error == nil || heldRes.Error.Code != "held" {
					return driver.TurnResult{}, fmt.Errorf("expected Freshness Guard to hold unread send")
				}

				// 2. Write role file in agent workspace.
				roleFile := filepath.Join(in.WorkDir, "xiao-zhu.md")
				if err := os.WriteFile(roleFile, []byte("# XiaoZhu\nAssistant engineer.\n"), 0o644); err != nil {
					return driver.TurnResult{}, err
				}

				// 3. List tasks and claim #1 (materializes its dedicated task conversation).
				taskListRes, _, err := runSumiCLI(ctx, binPath, in, "task", "list")
				if err != nil || !taskListRes.OK {
					return driver.TurnResult{}, fmt.Errorf("task list failed: %v", err)
				}
				var tasksPayload struct {
					Tasks []domain.Task `json:"tasks"`
				}
				_ = json.Unmarshal(taskListRes.Data, &tasksPayload)
				if len(tasksPayload.Tasks) == 0 {
					return driver.TurnResult{}, fmt.Errorf("no tasks found")
				}
				taskID := tasksPayload.Tasks[0].ID

				claimRes, _, err := runSumiCLI(ctx, binPath, in, "task", "claim", taskID)
				if err != nil || !claimRes.OK {
					return driver.TurnResult{}, fmt.Errorf("task claim failed: %v %+v", err, claimRes)
				}
				var claimPayload struct {
					Task domain.Task `json:"task"`
				}
				_ = json.Unmarshal(claimRes.Data, &claimPayload)
				if claimPayload.Task.ConversationID == "" {
					return driver.TurnResult{}, fmt.Errorf("task conversation was not lazily created on claim")
				}

				// 4. Create child agent XiaoZhu using granted agent.create permission.
				createRes, _, err := runSumiCLI(ctx, binPath, in,
					"agent", "create",
					"--name", "XiaoZhu",
					"--role", "Helper created by LinChe",
					"--base-url", "http://127.0.0.1:1/v1",
					"--model", "test-model",
					"--source-conversation", convID,
				)
				if err != nil || !createRes.OK {
					return driver.TurnResult{}, fmt.Errorf("agent create failed: %v %+v", err, createRes)
				}

				// 5. Post conclusion inside the task's dedicated conversation and mark task done.
				taskMsgRes, _, err := runSumiCLI(ctx, binPath, in, "send", "--body", "Created @XiaoZhu and saved xiao-zhu.md", claimPayload.Task.ConversationID)
				if err != nil || !taskMsgRes.OK {
					return driver.TurnResult{}, fmt.Errorf("send to task conversation failed: %v %+v", err, taskMsgRes)
				}
				statusRes, _, err := runSumiCLI(ctx, binPath, in, "task", "status", taskID, "done")
				if err != nil || !statusRes.OK {
					return driver.TurnResult{}, fmt.Errorf("task status done failed: %v", err)
				}

				// 6. Reply in source channel mentioning @XiaoZhu so child wakes and runs permission check.
				sendRes, _, err := runSumiCLI(ctx, binPath, in, "send", "--body", "Completed checklist and onboarded @XiaoZhu.", convID)
				if err != nil || !sendRes.OK {
					return driver.TurnResult{}, fmt.Errorf("channel reply failed: %v %+v", err, sendRes)
				}
				parentCompleted.Store(true)
				return driver.TurnResult{FinalText: "checklist done"}, nil
			},
		}, nil
	}

	env := startScenarioEnv(t, factory)
	binPath = env.binPath
	ctx := context.Background()

	linChe, err := env.svc.CreateAgent(ctx, env.owner, app.CreateAgentInput{
		DisplayName: "LinChe",
		RoleText:    "Lead engineer",
		ComputerID:  env.comp.ID,
		DriverKind:  domain.DriverBuiltin, DriverConfig: scenarioBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create LinChe: %v", err)
	}
	if err := env.svc.SetMemberPermission(ctx, env.owner, linChe.ID, domain.PermAgentCreate, true); err != nil {
		t.Fatalf("grant agent.create: %v", err)
	}

	ch, err := env.svc.CreateChannel(ctx, env.owner, app.CreateChannelInput{
		Slug:      "lin-che",
		Kind:      domain.ConversationPublic,
		MemberIDs: []string{linChe.ID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	rootRes, err := env.svc.SendMessage(ctx, env.owner, app.SendMessageInput{
		ConversationID: ch.ID,
		BodyMarkdown:   "Onboarding request: prepare xiao-zhu.md and create child agent XiaoZhu.",
	})
	if err != nil {
		t.Fatalf("post root message: %v", err)
	}
	task, err := env.svc.CreateTask(ctx, env.owner, app.CreateTaskInput{
		ChannelID:       ch.ID,
		SourceMessageID: rootRes.Message.ID,
		Title:           "Onboard XiaoZhu",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := env.svc.SendMessage(ctx, env.owner, app.SendMessageInput{
		ConversationID: ch.ID,
		BodyMarkdown:   "@LinChe please claim #1 and complete the onboarding request.",
	}); err != nil {
		t.Fatalf("mention LinChe: %v", err)
	}

	waitUntil(t, 15*time.Second, "brain-in-vat checklist and child permission denial", func() bool {
		return parentCompleted.Load() && childDeniedChecked.Load()
	})

	// Verify persisted outcomes in SQLite.
	finalTask, err := env.svc.GetTask(ctx, env.owner, task.ID)
	if err != nil || finalTask.Status != domain.TaskDone || finalTask.ConversationID == "" {
		t.Fatalf("expected task done with dedicated conversation, got %+v err=%v", finalTask, err)
	}
	members, err := env.svc.ListMembers(ctx, env.space.ID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	foundChild := false
	for _, m := range members {
		if m.DisplayName == "XiaoZhu" {
			foundChild = true
		}
	}
	if !foundChild {
		t.Fatalf("expected child agent XiaoZhu to exist in Space")
	}
}
