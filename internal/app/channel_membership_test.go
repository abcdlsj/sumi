package app

import (
	"context"
	"errors"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestChannelMembershipIsExplicitForAgents(t *testing.T) {
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

	// New public channel with only Lin invited must NOT include Kira.
	ch, err := svc.CreateChannel(ctx, owner, CreateChannelInput{
		Slug:      "design",
		Kind:      domain.ConversationPublic,
		MemberIDs: []string{lin.ID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	members, err := svc.ListConversationMembers(ctx, owner, ch.ID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members (owner + Lin), got %d: %+v", len(members), members)
	}

	// Kira cannot list, read, or wake from #design before being invited.
	kiraConvs, err := svc.ListConversations(ctx, kira)
	if err != nil {
		t.Fatalf("kira list convs: %v", err)
	}
	for _, c := range kiraConvs {
		if c.ID == ch.ID {
			t.Fatalf("uninvited agent Kira should not see #design in conversations")
		}
	}
	if _, err := svc.ListMessages(ctx, kira, ListMessagesInput{ConversationID: ch.ID}); err == nil {
		t.Fatalf("uninvited agent Kira should not read #design")
	} else {
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodePermissionDenied {
			t.Fatalf("want permission_denied, got %v", err)
		}
	}

	// @all and @Kira in #design must wake only Lin, not Kira.
	spy.mu.Lock()
	spy.wakes = nil
	spy.mu.Unlock()
	res, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: ch.ID,
		BodyMarkdown:   "Hey @all and @Kira",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res.WokenCount != 1 {
		t.Fatalf("expected woken_count=1 (only Lin), got %d", res.WokenCount)
	}
	spy.mu.Lock()
	if len(spy.wakes) != 1 || spy.wakes[0].AgentMemberID != lin.ID {
		t.Fatalf("expected only Lin woken, got %+v", spy.wakes)
	}
	spy.mu.Unlock()

	// Inviting Kira grants access and enables wakes.
	if err := svc.InviteToChannel(ctx, owner, ch.ID, kira.ID); err != nil {
		t.Fatalf("invite Kira: %v", err)
	}
	if _, err := svc.ListMessages(ctx, kira, ListMessagesInput{ConversationID: ch.ID}); err != nil {
		t.Fatalf("invited Kira should read #design: %v", err)
	}
	res2, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: ch.ID,
		BodyMarkdown:   "Welcome @Kira",
	})
	if err != nil || res2.WokenCount != 1 {
		t.Fatalf("expected Kira woken after invite: res=%+v err=%v", res2, err)
	}

	// Removing Kira revokes access again.
	if err := svc.RemoveFromChannel(ctx, owner, ch.ID, kira.ID); err != nil {
		t.Fatalf("remove Kira: %v", err)
	}
	if _, err := svc.ListMessages(ctx, kira, ListMessagesInput{ConversationID: ch.ID}); err == nil {
		t.Fatalf("removed agent Kira should lose access to #design")
	}
}
