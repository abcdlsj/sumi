package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestMessageReactionsInvariantsAndBoundaries(t *testing.T) {
	svc, spy, space, owner, comp := setupTestService(t)
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
	kira, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Kira",
		RoleText:     "Reviewer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Kira: %v", err)
	}

	convs, err := svc.ListConversations(ctx, owner)
	if err != nil || len(convs) == 0 {
		t.Fatalf("list conversations: %v", err)
	}
	generalID := convs[0].ID

	msgRes, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: generalID,
		BodyMarkdown:   "Shipped the v2 API",
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	msgID := msgRes.Message.ID
	wakesBefore := len(spy.wakes)

	// Invalid emoji must be rejected.
	if _, err := svc.AddReaction(ctx, owner, msgID, "invalid_emoji"); err == nil {
		t.Fatalf("expected invalid_argument for unknown emoji")
	} else {
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeInvalidArgument {
			t.Fatalf("expected invalid_argument, got %v", err)
		}
	}

	// Owner reacts with rocket, Lin reacts with eyes then rocket; order follows first appearance.
	groups, err := svc.AddReaction(ctx, owner, msgID, "rocket")
	if err != nil {
		t.Fatalf("owner add rocket: %v", err)
	}
	if len(groups) != 1 || groups[0].Emoji != "rocket" || groups[0].Count != 1 || !groups[0].Reacted {
		t.Fatalf("unexpected groups after owner rocket: %+v", groups)
	}

	// Idempotent add does not duplicate count or emit duplicate events.
	groups, err = svc.AddReaction(ctx, owner, msgID, "rocket")
	if err != nil || len(groups) != 1 || groups[0].Count != 1 {
		t.Fatalf("expected idempotent rocket add, got %+v err=%v", groups, err)
	}

	if _, err := svc.AddReaction(ctx, lin, msgID, "eyes"); err != nil {
		t.Fatalf("lin add eyes: %v", err)
	}
	groups, err = svc.AddReaction(ctx, lin, msgID, "rocket")
	if err != nil {
		t.Fatalf("lin add rocket: %v", err)
	}
	if len(groups) != 2 || groups[0].Emoji != "rocket" || groups[0].Count != 2 || groups[1].Emoji != "eyes" || groups[1].Count != 1 {
		t.Fatalf("expected [rocket(2), eyes(1)], got %+v", groups)
	}

	// Reactions must not wake agents or advance read_seq.
	if len(spy.wakes) != wakesBefore {
		t.Fatalf("reactions must never wake agents, got %d new wakes", len(spy.wakes)-wakesBefore)
	}
	pageForOwner, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: generalID})
	if err != nil || len(pageForOwner.Messages) != 1 {
		t.Fatalf("list messages: %+v err=%v", pageForOwner, err)
	}
	gotOwner := pageForOwner.Messages[0].Reactions
	if len(gotOwner) != 2 || !gotOwner[0].Reacted || gotOwner[1].Reacted {
		t.Fatalf("owner viewer reacted flags wrong: %+v", gotOwner)
	}

	pageForKira, err := svc.ListMessages(ctx, kira, ListMessagesInput{ConversationID: generalID})
	if err != nil || len(pageForKira.Messages) != 1 {
		t.Fatalf("list messages for kira: %v", err)
	}
	if pageForKira.ReadSeq != 0 {
		t.Fatalf("expected kira read_seq to remain 0, got %d", pageForKira.ReadSeq)
	}
	for _, g := range pageForKira.Messages[0].Reactions {
		if g.Reacted {
			t.Fatalf("kira has not reacted, got reacted=true on %+v", g)
		}
	}

	// Removing reaction updates count and removes empty group idempotently.
	groups, err = svc.RemoveReaction(ctx, lin, msgID, "eyes")
	if err != nil {
		t.Fatalf("remove eyes: %v", err)
	}
	if len(groups) != 1 || groups[0].Emoji != "rocket" || groups[0].Count != 2 {
		t.Fatalf("expected only rocket(2) after removing eyes, got %+v", groups)
	}
	groups, err = svc.RemoveReaction(ctx, lin, msgID, "eyes")
	if err != nil || len(groups) != 1 {
		t.Fatalf("idempotent remove failed: %+v err=%v", groups, err)
	}

	// Visibility boundary: private channel message cannot be reacted to by non-members.
	priv, err := svc.CreateChannel(ctx, owner, CreateChannelInput{
		Slug:      "secret",
		Kind:      domain.ConversationPrivate,
		MemberIDs: []string{lin.ID},
	})
	if err != nil {
		t.Fatalf("create private channel: %v", err)
	}
	privMsg, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: priv.ID,
		BodyMarkdown:   "Private note",
	})
	if err != nil {
		t.Fatalf("send private msg: %v", err)
	}
	if _, err := svc.AddReaction(ctx, kira, privMsg.Message.ID, "thumbsup"); err == nil {
		t.Fatalf("expected permission_denied for non-member reacting in private channel")
	} else {
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodePermissionDenied {
			t.Fatalf("expected permission_denied, got %v", err)
		}
	}
	if _, err := svc.AddReaction(ctx, lin, privMsg.Message.ID, "thumbsup"); err != nil {
		t.Fatalf("private channel member should be able to react: %v", err)
	}

	// Cross-space isolation: member in another space cannot react.
	otherAuth, err := svc.Register(ctx, "other@sumi.dev", "secret1234", "Other")
	if err != nil {
		t.Fatalf("register other: %v", err)
	}
	_, otherOwner, err := svc.CreateSpace(ctx, otherAuth.User, "other-space", "Other Space")
	if err != nil {
		t.Fatalf("create other space: %v", err)
	}
	if otherOwner.SpaceID == space.ID {
		t.Fatalf("expected distinct spaces")
	}
	if _, err := svc.AddReaction(ctx, otherOwner, msgID, "thumbsup"); err == nil {
		t.Fatalf("expected permission_denied across spaces")
	} else {
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodePermissionDenied {
			t.Fatalf("expected permission_denied across spaces, got %v", err)
		}
	}

	// Concurrent reactions on the same message must all commit cleanly.
	var wg sync.WaitGroup
	errCh := make(chan error, len(domain.ReactionEmojis)*2)
	for _, emoji := range domain.ReactionEmojis {
		for _, actor := range []domain.Member{owner, lin} {
			wg.Add(1)
			go func(a domain.Member, e string) {
				defer wg.Done()
				if _, err := svc.AddReaction(ctx, a, msgID, e); err != nil {
					errCh <- err
				}
			}(actor, emoji)
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent add reaction failed: %v", err)
	}
	finalPage, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: generalID})
	if err != nil || len(finalPage.Messages[0].Reactions) != len(domain.ReactionEmojis) {
		t.Fatalf("expected all %d reaction emojis, got %+v err=%v", len(domain.ReactionEmojis), finalPage.Messages[0].Reactions, err)
	}
}
