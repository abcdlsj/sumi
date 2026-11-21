package app

import (
	"context"
	"errors"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestChannelArchiveBlocksWritesAndNotificationsUntilUnarchived(t *testing.T) {
	svc, spy, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Lin",
		RoleText:     "Engineer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Lin: %v", err)
	}

	ch, err := svc.CreateChannel(ctx, owner, CreateChannelInput{
		Slug:      "legacy",
		Kind:      domain.ConversationPublic,
		MemberIDs: []string{lin.ID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	root, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: ch.ID,
		BodyMarkdown:   "Hey @Lin check this",
	})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	reply, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID:   ch.ID,
		ReplyToMessageID: root.Message.ID,
		BodyMarkdown:     "thread detail @Lin",
	})
	if err != nil {
		t.Fatalf("send thread reply: %v", err)
	}
	threadID := reply.Message.ConversationID

	task, err := svc.CreateTask(ctx, owner, CreateTaskInput{
		ChannelID:        ch.ID,
		Title:            "Old task",
		AssigneeMemberID: lin.ID,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Non-admin agent cannot archive.
	if _, err := svc.ArchiveChannel(ctx, lin, ch.ID); err == nil {
		t.Fatalf("agent must not be able to archive channel")
	}

	// Archive channel.
	spy.mu.Lock()
	spy.wakes = nil
	spy.mu.Unlock()

	archived, err := svc.ArchiveChannel(ctx, owner, ch.ID)
	if err != nil || archived.ArchivedAt == nil {
		t.Fatalf("archive channel: %+v err=%v", archived, err)
	}

	// No wakes on archive.
	spy.mu.Lock()
	if len(spy.wakes) != 0 {
		t.Fatalf("archive must not wake agents, got %+v", spy.wakes)
	}
	spy.mu.Unlock()

	// Hidden from active conversations & inbox (including threads and tasks under it).
	convs, err := svc.ListConversations(ctx, owner)
	if err != nil {
		t.Fatalf("list convs: %v", err)
	}
	for _, c := range convs {
		if c.ID == ch.ID {
			t.Fatalf("archived channel must not appear in ListConversations")
		}
	}
	inbox, err := svc.ListInbox(ctx, lin, "all")
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	for _, it := range inbox {
		if it.ConversationID == ch.ID || it.ConversationID == threadID || it.ConversationID == task.ConversationID {
			t.Fatalf("archived channel/thread/task must not appear in inbox: %+v", it)
		}
	}

	// Appears in archived channels list.
	archList, err := svc.ListArchivedChannels(ctx, owner)
	if err != nil || len(archList) != 1 || archList[0].ID != ch.ID {
		t.Fatalf("list archived: %+v err=%v", archList, err)
	}

	// Both human and agent are blocked from posting in channel, thread, or task.
	var de *domain.Error
	for _, tc := range []struct {
		name   string
		actor  domain.Member
		convID string
	}{
		{"human channel", owner, ch.ID},
		{"agent channel", lin, ch.ID},
		{"human thread", owner, threadID},
		{"agent task", lin, task.ConversationID},
	} {
		if _, err := svc.SendMessage(ctx, tc.actor, SendMessageInput{ConversationID: tc.convID, BodyMarkdown: "blocked"}); !errors.As(err, &de) || de.Code != domain.CodeConflict {
			t.Fatalf("%s: want conflict on archived send, got %v", tc.name, err)
		}
	}

	// Reactions and task mutations are also blocked.
	if _, err := svc.AddReaction(ctx, owner, root.Message.ID, "thumbsup"); !errors.As(err, &de) || de.Code != domain.CodeConflict {
		t.Fatalf("reaction on archived channel: want conflict, got %v", err)
	}
	if _, err := svc.UpdateTaskStatus(ctx, owner, task.ID, domain.TaskDone); !errors.As(err, &de) || de.Code != domain.CodeConflict {
		t.Fatalf("task update in archived channel: want conflict, got %v", err)
	}

	// History remains readable.
	page, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: ch.ID})
	if err != nil || len(page.Messages) == 0 {
		t.Fatalf("archived history should remain readable: err=%v", err)
	}

	// Unarchive restores listing and messaging.
	restored, err := svc.UnarchiveChannel(ctx, owner, ch.ID)
	if err != nil || restored.ArchivedAt != nil {
		t.Fatalf("unarchive: %+v err=%v", restored, err)
	}
	if _, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: ch.ID, BodyMarkdown: "back online @Lin"}); err != nil {
		t.Fatalf("send after unarchive: %v", err)
	}
	archList, _ = svc.ListArchivedChannels(ctx, owner)
	if len(archList) != 0 {
		t.Fatalf("expected 0 archived channels after unarchive, got %d", len(archList))
	}
}
