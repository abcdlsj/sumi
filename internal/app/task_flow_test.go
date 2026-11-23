package app

import (
	"context"
	"errors"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func assertTaskErrorCode(t *testing.T, err error, want domain.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", want)
	}
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("expected domain.Error %s, got %T: %v", want, err, err)
	}
	if de.Code != want {
		t.Fatalf("expected error code %s, got %s (%s)", want, de.Code, de.Message)
	}
}

func TestTaskLifecycleGuards(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	agentA, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Alice",
		RoleText:     "Engineer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent A: %v", err)
	}
	agentB, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Bob",
		RoleText:     "Reviewer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent B: %v", err)
	}

	// Create a channel with only agentA as initial member (agentB is NOT a member).
	ch, err := svc.CreateChannel(ctx, owner, CreateChannelInput{
		Slug:      "eng",
		Kind:      domain.ConversationPublic,
		Topic:     "Engineering",
		MemberIDs: []string{agentA.ID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	// Assigning a task on creation to an agent without channel access must fail.
	_, err = svc.CreateTask(ctx, owner, CreateTaskInput{
		ChannelID:        ch.ID,
		Title:            "Blocked task",
		AssigneeMemberID: agentB.ID,
	})
	assertTaskErrorCode(t, err, domain.CodeInvalidArgument)

	// Create an unassigned task.
	task, err := svc.CreateTask(ctx, owner, CreateTaskInput{
		ChannelID: ch.ID,
		Title:     "Fix pipeline",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Unclaiming an unassigned task fails with conflict.
	_, err = svc.UnclaimTask(ctx, agentA, task.ID)
	assertTaskErrorCode(t, err, domain.CodeConflict)

	// agentA claims the task -> transitions todo -> in_progress.
	claimRes, err := svc.ClaimTask(ctx, agentA, task.ID, false)
	if err != nil {
		t.Fatalf("agentA claim: %v", err)
	}
	if claimRes.Task.Status != domain.TaskInProgress || claimRes.Task.AssigneeMemberID != agentA.ID {
		t.Fatalf("expected in_progress assigned to agentA, got %+v", claimRes.Task)
	}

	// Self-reclaiming an already in_progress task fails with conflict.
	_, err = svc.ClaimTask(ctx, agentA, task.ID, false)
	assertTaskErrorCode(t, err, domain.CodeConflict)

	// Owner cannot unclaim agentA's task directly (must use AssignTask).
	_, err = svc.UnclaimTask(ctx, owner, task.ID)
	assertTaskErrorCode(t, err, domain.CodePermissionDenied)

	// agentA unclaims own task -> returns to todo and clears assignee.
	unclaimed, err := svc.UnclaimTask(ctx, agentA, task.ID)
	if err != nil {
		t.Fatalf("agentA unclaim: %v", err)
	}
	if unclaimed.Status != domain.TaskTodo || unclaimed.AssigneeMemberID != "" {
		t.Fatalf("expected todo unassigned after unclaim, got %+v", unclaimed)
	}

	// Assigning to agentB (not in channel) fails with invalid argument.
	_, err = svc.AssignTask(ctx, owner, AssignTaskInput{
		TaskID:           task.ID,
		AssigneeMemberID: agentB.ID,
	})
	assertTaskErrorCode(t, err, domain.CodeInvalidArgument)

	// Pre-assigning to agentA keeps status todo; agentA can then claim it to start work.
	assignRes, err := svc.AssignTask(ctx, owner, AssignTaskInput{
		TaskID:           task.ID,
		AssigneeMemberID: agentA.ID,
	})
	if err != nil {
		t.Fatalf("assign to agentA: %v", err)
	}
	if assignRes.Task.Status != domain.TaskTodo || assignRes.Task.AssigneeMemberID != agentA.ID {
		t.Fatalf("expected todo pre-assigned to agentA, got %+v", assignRes.Task)
	}
	revAfterAssign := assignRes.Task.Revision

	// No-op reassign to the same assignee does not bump revision.
	noopRes, err := svc.AssignTask(ctx, owner, AssignTaskInput{
		TaskID:           task.ID,
		AssigneeMemberID: agentA.ID,
	})
	if err != nil {
		t.Fatalf("noop assign: %v", err)
	}
	if noopRes.Task.Revision != revAfterAssign {
		t.Fatalf("expected revision unchanged (%d), got %d", revAfterAssign, noopRes.Task.Revision)
	}

	// Pre-assigned agentA can claim a todo task to move it to in_progress.
	startedRes, err := svc.ClaimTask(ctx, agentA, task.ID, false)
	if err != nil {
		t.Fatalf("claim pre-assigned todo task: %v", err)
	}
	if startedRes.Task.Status != domain.TaskInProgress {
		t.Fatalf("expected in_progress after claiming pre-assigned todo, got %s", startedRes.Task.Status)
	}

	// Mark done; assign and unclaim must be rejected while done.
	if _, err := svc.UpdateTaskStatus(ctx, agentA, task.ID, domain.TaskDone); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	_, err = svc.AssignTask(ctx, owner, AssignTaskInput{TaskID: task.ID, AssigneeMemberID: owner.ID})
	assertTaskErrorCode(t, err, domain.CodeConflict)
	_, err = svc.UnclaimTask(ctx, agentA, task.ID)
	assertTaskErrorCode(t, err, domain.CodeConflict)
}

func TestTerminalTaskSuppressesFollowingWakesAndInboxNeedsMe(t *testing.T) {
	svc, spy, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	agentA, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Cato",
		RoleText:     "Engineer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent A: %v", err)
	}
	agentB, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Iris",
		RoleText:     "Reviewer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent B: %v", err)
	}
	agentC, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Piper",
		RoleText:     "QA",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent C: %v", err)
	}

	ch, err := svc.CreateChannel(ctx, owner, CreateChannelInput{
		Slug:      "game-dev",
		Kind:      domain.ConversationPrivate,
		MemberIDs: []string{agentA.ID, agentB.ID, agentC.ID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	task, err := svc.CreateTask(ctx, owner, CreateTaskInput{
		ChannelID:        ch.ID,
		Title:            "Wire loop",
		AssigneeMemberID: agentA.ID,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	taskConvID := task.ConversationID
	if err := svc.SetFollowing(ctx, agentB, taskConvID, true); err != nil {
		t.Fatalf("follow task: %v", err)
	}

	clearWakes := func() {
		spy.mu.Lock()
		spy.wakes = nil
		spy.mu.Unlock()
	}
	getWakes := func() int {
		spy.mu.Lock()
		defer spy.mu.Unlock()
		return len(spy.wakes)
	}

	// Active task: plain reply from agentA wakes follower agentB and enters needs_me.
	clearWakes()
	res, err := svc.SendMessage(ctx, agentA, SendMessageInput{ConversationID: taskConvID, BodyMarkdown: "Active update"})
	if err != nil || res.WokenCount != 1 || getWakes() != 1 {
		t.Fatalf("active task must wake follower: woken=%d spy=%d err=%v", res.WokenCount, getWakes(), err)
	}
	items, err := svc.ListInbox(ctx, agentB, "needs_me")
	if err != nil || len(items) != 1 || items[0].Reason != "task" {
		t.Fatalf("active task must enter follower needs_me: items=%+v err=%v", items, err)
	}
	if _, err := svc.ListMessages(ctx, agentB, ListMessagesInput{ConversationID: taskConvID, UnreadOnly: true, MarkRead: true}); err != nil {
		t.Fatalf("mark read agentB: %v", err)
	}

	// Mark task done -> plain replies must NOT wake followers or enter needs_me.
	if _, err := svc.UpdateTaskStatus(ctx, agentA, task.ID, domain.TaskDone); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	clearWakes()
	res, err = svc.SendMessage(ctx, agentA, SendMessageInput{ConversationID: taskConvID, BodyMarkdown: "Closing remark after done"})
	if err != nil || res.WokenCount != 0 || getWakes() != 0 {
		t.Fatalf("done task must not wake followers: woken=%d spy=%d err=%v", res.WokenCount, getWakes(), err)
	}
	items, err = svc.ListInbox(ctx, agentB, "needs_me")
	if err != nil || len(items) != 0 {
		t.Fatalf("done task must not enter agent needs_me via following: items=%+v err=%v", items, err)
	}
	ownerNeeds, err := svc.ListInbox(ctx, owner, "needs_me")
	if err != nil || len(ownerNeeds) != 0 {
		t.Fatalf("done task must not enter human needs_me via following: items=%+v err=%v", ownerNeeds, err)
	}
	// Still visible under unread filter.
	unreadItems, err := svc.ListInbox(ctx, agentB, "unread")
	if err != nil || len(unreadItems) != 1 {
		t.Fatalf("done task unread must still appear in unread filter: items=%+v err=%v", unreadItems, err)
	}
	// Terminal task is excluded from agent's active followed conversations list.
	agentConvs, err := svc.ListAgentConversations(ctx, agentB)
	if err != nil {
		t.Fatalf("list agent convs: %v", err)
	}
	for _, c := range agentConvs {
		if c.ID == taskConvID {
			t.Fatalf("done task must not appear in followed active conversations")
		}
	}

	// Posting in a terminal task does NOT auto-follow a non-follower (agentC).
	if _, err := svc.SendMessage(ctx, agentC, SendMessageInput{ConversationID: taskConvID, BodyMarkdown: "Post-done note"}); err != nil {
		t.Fatalf("agentC post in done task: %v", err)
	}
	pageC, err := svc.ListMessages(ctx, agentC, ListMessagesInput{ConversationID: taskConvID})
	if err != nil || pageC.Following {
		t.Fatalf("posting in done task must not auto-follow: following=%v err=%v", pageC.Following, err)
	}

	// Explicit @mention in a done task STILL wakes the mentioned agent and enters needs_me.
	clearWakes()
	res, err = svc.SendMessage(ctx, agentA, SendMessageInput{ConversationID: taskConvID, BodyMarkdown: "@Iris please double check"})
	if err != nil || res.WokenCount != 1 || getWakes() != 1 {
		t.Fatalf("explicit mention in done task must wake target: woken=%d spy=%d err=%v", res.WokenCount, getWakes(), err)
	}
	items, err = svc.ListInbox(ctx, agentB, "needs_me")
	if err != nil || len(items) != 1 || items[0].Reason != "mention" {
		t.Fatalf("explicit mention in done task must enter needs_me with reason=mention: items=%+v err=%v", items, err)
	}
	if _, err := svc.ListMessages(ctx, agentB, ListMessagesInput{ConversationID: taskConvID, UnreadOnly: true, MarkRead: true}); err != nil {
		t.Fatalf("mark read agentB after mention: %v", err)
	}

	// Closed task also suppresses following wakes.
	if _, err := svc.UpdateTaskStatus(ctx, owner, task.ID, domain.TaskClosed); err != nil {
		t.Fatalf("mark closed: %v", err)
	}
	clearWakes()
	res, err = svc.SendMessage(ctx, agentA, SendMessageInput{ConversationID: taskConvID, BodyMarkdown: "Note on closed task"})
	if err != nil || res.WokenCount != 0 || getWakes() != 0 {
		t.Fatalf("closed task must not wake followers: woken=%d spy=%d err=%v", res.WokenCount, getWakes(), err)
	}

	// Reopening the task restores following wakes for existing followers.
	if _, err := svc.UpdateTaskStatus(ctx, owner, task.ID, domain.TaskInProgress); err != nil {
		t.Fatalf("reopen task: %v", err)
	}
	clearWakes()
	res, err = svc.SendMessage(ctx, agentA, SendMessageInput{ConversationID: taskConvID, BodyMarkdown: "Resumed work"})
	if err != nil || res.WokenCount != 1 || getWakes() != 1 {
		t.Fatalf("reopened task must restore following wake: woken=%d spy=%d err=%v", res.WokenCount, getWakes(), err)
	}
	items, err = svc.ListInbox(ctx, agentB, "needs_me")
	if err != nil || len(items) != 1 || items[0].Reason != "task" {
		t.Fatalf("reopened task must restore needs_me reason=task: items=%+v err=%v", items, err)
	}
}
