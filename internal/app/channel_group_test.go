package app

import (
	"context"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestChannelGroupsCRUDAndMove(t *testing.T) {
	svc, _, _, owner, _ := setupTestService(t)
	ctx := context.Background()

	ch1, err := svc.CreateChannel(ctx, owner, CreateChannelInput{Slug: "eng", Kind: domain.ConversationPublic})
	if err != nil {
		t.Fatalf("create ch1: %v", err)
	}
	ch2, err := svc.CreateChannel(ctx, owner, CreateChannelInput{Slug: "ops", Kind: domain.ConversationPrivate})
	if err != nil {
		t.Fatalf("create ch2: %v", err)
	}

	g1, err := svc.CreateChannelGroup(ctx, owner, "Core")
	if err != nil || g1.Name != "Core" || g1.Collapsed {
		t.Fatalf("create group 1: %+v err=%v", g1, err)
	}
	g2, err := svc.CreateChannelGroup(ctx, owner, "Projects")
	if err != nil {
		t.Fatalf("create group 2: %v", err)
	}

	// Move ch1 and ch2 into g1, then move ch2 to g2.
	if err := svc.SetConversationGroup(ctx, owner, ch1.ID, g1.ID); err != nil {
		t.Fatalf("move ch1 to g1: %v", err)
	}
	if err := svc.SetConversationGroup(ctx, owner, ch2.ID, g1.ID); err != nil {
		t.Fatalf("move ch2 to g1: %v", err)
	}
	if err := svc.SetConversationGroup(ctx, owner, ch2.ID, g2.ID); err != nil {
		t.Fatalf("move ch2 to g2: %v", err)
	}

	collapsed := true
	renamed := "Platform"
	if _, err := svc.UpdateChannelGroup(ctx, owner, g1.ID, UpdateChannelGroupInput{Name: &renamed, Collapsed: &collapsed}); err != nil {
		t.Fatalf("update g1: %v", err)
	}

	groups, err := svc.ListChannelGroups(ctx, owner)
	if err != nil || len(groups) != 2 {
		t.Fatalf("list groups: %+v err=%v", groups, err)
	}
	if groups[0].Name != "Platform" || !groups[0].Collapsed || len(groups[0].ConversationIDs) != 1 || groups[0].ConversationIDs[0] != ch1.ID {
		t.Fatalf("unexpected g1 state: %+v", groups[0])
	}
	if len(groups[1].ConversationIDs) != 1 || groups[1].ConversationIDs[0] != ch2.ID {
		t.Fatalf("unexpected g2 state: %+v", groups[1])
	}

	// Ungroup ch2 and delete g1 -> ch1 returns to ungrouped automatically.
	if err := svc.SetConversationGroup(ctx, owner, ch2.ID, ""); err != nil {
		t.Fatalf("ungroup ch2: %v", err)
	}
	if err := svc.DeleteChannelGroup(ctx, owner, g1.ID); err != nil {
		t.Fatalf("delete g1: %v", err)
	}
	groups, err = svc.ListChannelGroups(ctx, owner)
	if err != nil || len(groups) != 1 || len(groups[0].ConversationIDs) != 0 {
		t.Fatalf("after delete/ungroup: %+v err=%v", groups, err)
	}
}
