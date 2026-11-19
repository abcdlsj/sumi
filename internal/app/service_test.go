package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
	"github.com/abcdlsj/sumi/internal/store"
)

type spyNotifier struct {
	mu       sync.Mutex
	wakes    []protocol.WakeFrame
	controls []protocol.AgentControlFrame
}

func (n *spyNotifier) SendWake(_ string, f protocol.WakeFrame) {
	n.mu.Lock()
	n.wakes = append(n.wakes, f)
	n.mu.Unlock()
}

func (n *spyNotifier) SendControl(_ string, f protocol.AgentControlFrame) {
	n.mu.Lock()
	n.controls = append(n.controls, f)
	n.mu.Unlock()
}

func setupTestService(t *testing.T) (*Service, *spyNotifier, domain.Space, domain.Member, domain.Computer) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	spy := &spyNotifier{}
	svc := NewService(st, NewEventHub(), spy)
	ctx := context.Background()

	auth, err := svc.Register(ctx, "owner@sumi.dev", "secret1234", "Lisi")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	space, owner, err := svc.CreateSpace(ctx, auth.User, "studio", "Sumi Studio")
	if err != nil {
		t.Fatalf("create space: %v", err)
	}
	comp, _, err := svc.PairComputerDirect(ctx, owner, "mac-studio", "mac.local", "macos")
	if err != nil {
		t.Fatalf("pair computer: %v", err)
	}
	if _, err := svc.SetComputerStatus(ctx, comp.ID, "online", &ComputerHello{DaemonVersion: "2.0.0"}); err != nil {
		t.Fatalf("set computer online: %v", err)
	}
	return svc, spy, space, owner, comp
}

func TestTaskDecouplingLazyConversationAndFreshnessGuard(t *testing.T) {
	svc, spy, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	// Create two agents: Lin and Kira.
	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin",
		RoleText:    "Backend engineer",
		ComputerID:  comp.ID,
		DriverKind:  domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Lin: %v", err)
	}
	kira, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Kira",
		RoleText:    "Reviewer",
		ComputerID:  comp.ID,
		DriverKind:  domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Kira: %v", err)
	}

	convs, err := svc.ListConversations(ctx, owner)
	if err != nil || len(convs) != 1 {
		t.Fatalf("expected #general channel, got %v err=%v", convs, err)
	}
	generalID := convs[0].ID

	// Post one root message and attach TWO tasks to it (1:N).
	rootRes, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: generalID,
		BodyMarkdown:   "Split this work into two tasks @Lin",
	})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	if len(spy.wakes) != 1 || spy.wakes[0].AgentMemberID != lin.ID {
		t.Fatalf("expected wake for Lin on mention, got %+v", spy.wakes)
	}

	t1, err := svc.CreateTask(ctx, owner, CreateTaskInput{
		ChannelID:       generalID,
		SourceMessageID: rootRes.Message.ID,
		Title:           "Unified watermark",
	})
	if err != nil {
		t.Fatalf("create task 1: %v", err)
	}
	t2, err := svc.CreateTask(ctx, owner, CreateTaskInput{
		ChannelID:       generalID,
		SourceMessageID: rootRes.Message.ID,
		Title:           "Lazy task conversation",
	})
	if err != nil {
		t.Fatalf("create task 2: %v", err)
	}
	if t1.ConversationID != "" || t2.ConversationID != "" {
		t.Fatalf("expected lazy task conversations to start empty")
	}

	// Lin claims t1 -> lazily creates t1 conversation and moves todo -> in_progress.
	claimRes, err := svc.ClaimTask(ctx, lin, t1.ID, true)
	if err != nil || claimRes.Held {
		t.Fatalf("Lin claim t1: res=%+v err=%v", claimRes, err)
	}
	if claimRes.Task.Status != domain.TaskInProgress || claimRes.Task.ConversationID == "" {
		t.Fatalf("expected in_progress and materialized conversation, got %+v", claimRes.Task)
	}

	// Kira attempts to claim t1 -> conflict.
	if _, err := svc.ClaimTask(ctx, kira, t1.ID, true); err == nil {
		t.Fatalf("expected conflict when Kira claims already-claimed task")
	}

	// Human owner can also claim t2 (Human/Agent parity).
	ownerClaim, err := svc.ClaimTask(ctx, owner, t2.ID, false)
	if err != nil || ownerClaim.Task.AssigneeMemberID != owner.ID {
		t.Fatalf("owner claim t2 failed: %+v err=%v", ownerClaim, err)
	}

	// Owner posts in t1's independent conversation -> wakes Lin (who follows t1).
	if _, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: claimRes.Task.ConversationID,
		BodyMarkdown:   "Keep done_seq separate from following",
	}); err != nil {
		t.Fatalf("owner reply in task: %v", err)
	}

	// Lin tries to send in t1 WITHOUT reading owner's message -> Freshness Guard holds it!
	heldRes, err := svc.SendMessage(ctx, lin, SendMessageInput{
		ConversationID:   claimRes.Task.ConversationID,
		BodyMarkdown:     "Stale reply",
		EnforceFreshness: true,
	})
	if err != nil {
		t.Fatalf("freshness send err: %v", err)
	}
	if !heldRes.Held || len(heldRes.UnreadMessages) != 1 {
		t.Fatalf("expected held with 1 unread message, got %+v", heldRes)
	}

	// Now that Freshness Guard advanced Lin's read_seq, resending succeeds.
	okRes, err := svc.SendMessage(ctx, lin, SendMessageInput{
		ConversationID:   claimRes.Task.ConversationID,
		BodyMarkdown:     "Acknowledged, keeping done_seq separate",
		EnforceFreshness: true,
	})
	if err != nil || okRes.Held {
		t.Fatalf("expected resend after hold to succeed, got %+v err=%v", okRes, err)
	}

	// Agent assigning to another member without reason must fail.
	if _, err := svc.AssignTask(ctx, lin, AssignTaskInput{
		TaskID:           t1.ID,
		AssigneeMemberID: kira.ID,
	}); err == nil {
		t.Fatalf("expected error when agent assigns without reason")
	}

	// Agent assigning with reason succeeds, keeps status unchanged, and wakes Kira.
	assignRes, err := svc.AssignTask(ctx, lin, AssignTaskInput{
		TaskID:           t1.ID,
		AssigneeMemberID: kira.ID,
		Reason:           "Please review the schema",
	})
	if err != nil || assignRes.Task.AssigneeMemberID != kira.ID || assignRes.Task.Status != domain.TaskInProgress {
		t.Fatalf("assign failed: %+v err=%v", assignRes, err)
	}

	// Explicit status update to done and reopen back to in_progress.
	doneTask, err := svc.UpdateTaskStatus(ctx, kira, t1.ID, domain.TaskDone)
	if err != nil || doneTask.Status != domain.TaskDone {
		t.Fatalf("mark done failed: %+v err=%v", doneTask, err)
	}
	reopened, err := svc.UpdateTaskStatus(ctx, owner, t1.ID, domain.TaskInProgress)
	if err != nil || reopened.Status != domain.TaskInProgress {
		t.Fatalf("reopen failed: %+v err=%v", reopened, err)
	}
}

func TestPermissionsAndAttentionNoiseControl(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin",
		RoleText:    "Coordinator",
		ComputerID:  comp.ID,
		DriverKind:  domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Lin: %v", err)
	}

	// Without agent.create permission, Lin cannot create child agents.
	_, err = svc.CreateAgent(ctx, lin, CreateAgentInput{
		DisplayName: "Sora",
		RoleText:    "Helper",
		ComputerID:  comp.ID,
		DriverKind:  domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	var domErr *domain.Error
	if !errors.As(err, &domErr) || domErr.Code != domain.CodePermissionDenied {
		t.Fatalf("expected permission_denied, got %v", err)
	}

	// Grant agent.create -> Lin creates Sora and emits an Action Message in #general.
	if err := svc.SetMemberPermission(ctx, owner, lin.ID, domain.PermAgentCreate, true); err != nil {
		t.Fatalf("grant permission: %v", err)
	}
	convs, _ := svc.ListConversations(ctx, owner)
	generalID := convs[0].ID

	sora, err := svc.CreateAgent(ctx, lin, CreateAgentInput{
		DisplayName: "Sora",
		RoleText:    "Helper",
		ComputerID:  comp.ID,
		DriverKind:  domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
		SourceConversationID: generalID,
	})
	if err != nil || sora.DisplayName != "Sora" {
		t.Fatalf("expected Lin to create Sora, err=%v", err)
	}

	// Verify channel noise control: by default channel notify_level = mentions.
	// Plain message from Lin without mentioning owner does NOT enter owner's needs_me inbox.
	if _, err := svc.SendMessage(ctx, lin, SendMessageInput{
		ConversationID: generalID,
		BodyMarkdown:   "Routine background update",
	}); err != nil {
		t.Fatalf("send plain message: %v", err)
	}
	needsMe, err := svc.ListInbox(ctx, owner, "needs_me")
	if err != nil {
		t.Fatalf("list inbox needs_me: %v", err)
	}
	if len(needsMe) != 0 {
		t.Fatalf("expected 0 needs_me items for unmentioned channel message, got %+v", needsMe)
	}

	// Mentioning @Lisi enters needs_me inbox; MarkDone clears it cleanly.
	if _, err := svc.SendMessage(ctx, lin, SendMessageInput{
		ConversationID: generalID,
		BodyMarkdown:   "@Lisi ready for sign-off",
	}); err != nil {
		t.Fatalf("send mention: %v", err)
	}
	needsMe, err = svc.ListInbox(ctx, owner, "needs_me")
	if err != nil || len(needsMe) != 1 || needsMe[0].Reason != "mention" {
		t.Fatalf("expected 1 mention inbox item, got %+v err=%v", needsMe, err)
	}
	if want := "#" + convs[0].Slug; needsMe[0].Target != want {
		t.Fatalf("inbox target = %q, want %q", needsMe[0].Target, want)
	}
	if err := svc.MarkDone(ctx, owner, generalID); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	needsMe, err = svc.ListInbox(ctx, owner, "needs_me")
	if err != nil || len(needsMe) != 0 {
		t.Fatalf("expected 0 inbox items after MarkDone, got %+v err=%v", needsMe, err)
	}
}

var testBuiltinConfig = domain.DriverConfig{BaseURL: "http://127.0.0.1:1/v1", Model: "test-model"}

func TestListMessagesReportsViewerFollowing(t *testing.T) {
	svc, _, _, owner, _ := setupTestService(t)
	ctx := context.Background()
	convs, err := svc.ListConversations(ctx, owner)
	if err != nil || len(convs) == 0 {
		t.Fatalf("list conversations: %v", err)
	}
	root, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: convs[0].ID, BodyMarkdown: "root"})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	reply, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: convs[0].ID, ReplyToMessageID: root.Message.ID, BodyMarkdown: "reply"})
	if err != nil {
		t.Fatalf("send reply: %v", err)
	}
	threadID := reply.Message.ConversationID

	page, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: threadID})
	if err != nil || !page.Following {
		t.Fatalf("replying must follow the thread: page.Following=%v err=%v", page.Following, err)
	}
	if err := svc.SetFollowing(ctx, owner, threadID, false); err != nil {
		t.Fatalf("unfollow: %v", err)
	}
	page, err = svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: threadID})
	if err != nil || page.Following {
		t.Fatalf("unfollow must be reflected: page.Following=%v err=%v", page.Following, err)
	}
}
