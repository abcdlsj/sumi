package app

import (
	"context"
	"errors"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestUpdateSpaceAccentIsGovernedAndValidated(t *testing.T) {
	svc, _, space, owner, comp := setupTestService(t)
	ctx := context.Background()

	if space.Accent != domain.DefaultSpaceAccent {
		t.Fatalf("new space accent = %q, want %q", space.Accent, domain.DefaultSpaceAccent)
	}

	var domErr *domain.Error
	if _, err := svc.UpdateSpace(ctx, owner, "", "neon"); !errors.As(err, &domErr) || domErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("unknown accent: want invalid_argument, got %v", err)
	}

	sp, err := svc.UpdateSpace(ctx, owner, "", "iris")
	if err != nil {
		t.Fatalf("update accent: %v", err)
	}
	if sp.Accent != "iris" || sp.Name != space.Name {
		t.Fatalf("update kept name %q and set accent %q", sp.Name, sp.Accent)
	}

	agent, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", RoleText: "Helper", ComputerID: comp.ID,
		DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := svc.UpdateSpace(ctx, agent, "Hijacked", ""); !errors.As(err, &domErr) || domErr.Code != domain.CodePermissionDenied {
		t.Fatalf("agent update: want permission_denied, got %v", err)
	}

	spaces, err := svc.ListUserSpaces(ctx, mustOwnerUserID(t, svc, owner))
	if err != nil || len(spaces) != 1 || spaces[0].Accent != "iris" {
		t.Fatalf("list spaces after update: %+v %v", spaces, err)
	}
}

func mustOwnerUserID(t *testing.T, svc *Service, owner domain.Member) string {
	t.Helper()
	var userID string
	if err := svc.store.ReadDB().QueryRow(`SELECT user_id FROM members WHERE id = ?`, owner.ID).Scan(&userID); err != nil {
		t.Fatalf("owner user id: %v", err)
	}
	return userID
}

func TestAgentConversationsListFollowedThreadsWithTargets(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()
	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", RoleText: "Helper", ComputerID: comp.ID,
		DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	dm, err := svc.GetOrCreateDM(ctx, owner, lin.ID)
	if err != nil {
		t.Fatalf("dm: %v", err)
	}
	root, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: dm.ID, BodyMarkdown: "your role is seer"})
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if _, err := svc.ListMessages(ctx, lin, ListMessagesInput{ConversationID: dm.ID}); err != nil {
		t.Fatalf("lin reads dm: %v", err)
	}
	reply, err := svc.SendMessage(ctx, lin, SendMessageInput{ConversationID: dm.ID, ReplyToMessageID: root.Message.ID, BodyMarkdown: "check bram", EnforceFreshness: true})
	if err != nil {
		t.Fatalf("thread reply: %v", err)
	}
	threadID := reply.Message.ConversationID

	byID := func(m domain.Member) map[string]domain.Conversation {
		convs, err := svc.ListAgentConversations(ctx, m)
		if err != nil {
			t.Fatalf("list agent conversations: %v", err)
		}
		out := map[string]domain.Conversation{}
		for _, c := range convs {
			out[c.ID] = c
		}
		return out
	}
	// The root author follows the thread too, so both sides can find it without the Inbox.
	for _, m := range []domain.Member{lin, owner} {
		got := byID(m)
		th, ok := got[threadID]
		if !ok {
			t.Fatalf("%s: followed thread missing from listing", m.DisplayName)
		}
		if th.Target != "thread:"+threadID || th.ParentTarget != got[dm.ID].Target || th.RootMessageID != root.Message.ID {
			t.Fatalf("%s: thread entry %+v, dm target %q", m.DisplayName, th, got[dm.ID].Target)
		}
	}
	if got := byID(lin); got[dm.ID].Target != "dm:@"+owner.DisplayName {
		t.Fatalf("dm target = %q", got[dm.ID].Target)
	}

	if err := svc.SetFollowing(ctx, lin, threadID, false); err != nil {
		t.Fatalf("unfollow: %v", err)
	}
	if _, ok := byID(lin)[threadID]; ok {
		t.Fatal("unfollowed thread must drop out of the listing")
	}
}
