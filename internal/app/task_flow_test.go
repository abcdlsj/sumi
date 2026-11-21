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
