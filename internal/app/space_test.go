package app

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
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

func TestComputerIsAccountOwnedAndSharedAcrossSpaces(t *testing.T) {
	svc, _, studio, owner, comp := setupTestService(t)
	ctx := context.Background()
	ownerUser := domain.User{ID: mustOwnerUserID(t, svc, owner)}

	// The same account opens a second Space: its Computer is usable there without re-pairing.
	lab, labOwner, err := svc.CreateSpace(ctx, ownerUser, "lab", "Lab")
	if err != nil {
		t.Fatalf("create lab: %v", err)
	}
	for _, sp := range []domain.Space{studio, lab} {
		comps, err := svc.ListComputers(ctx, sp.ID)
		if err != nil || len(comps) != 1 || comps[0].ID != comp.ID {
			t.Fatalf("%s computers = %+v err=%v", sp.Slug, comps, err)
		}
	}
	mk := func(actor domain.Member, name string) (domain.Member, error) {
		return svc.CreateAgent(ctx, actor, CreateAgentInput{DisplayName: name, RoleText: "r", ComputerID: comp.ID, DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig})
	}
	studioAgent, err := mk(owner, "Lin")
	if err != nil {
		t.Fatalf("agent in studio: %v", err)
	}
	labAgent, err := mk(labOwner, "Sora")
	if err != nil {
		t.Fatalf("agent in lab: %v", err)
	}

	// Reconnecting provisions every Agent the Computer hosts, each carrying its own Space.
	specs, err := svc.SetComputerStatus(ctx, comp.ID, "online", nil)
	if err != nil {
		t.Fatalf("online: %v", err)
	}
	spaceOf := map[string]string{}
	for _, sp := range specs {
		spaceOf[sp.MemberID] = sp.SpaceID
	}
	if spaceOf[studioAgent.ID] != studio.ID || spaceOf[labAgent.ID] != lab.ID {
		t.Fatalf("specs carry wrong spaces: %v", spaceOf)
	}

	// Another account's admin in the same Space can see the Computer but not place Agents on it.
	other, err := svc.Register(ctx, "mallory@sumi.dev", "secret1234", "Mallory")
	if err != nil {
		t.Fatalf("register other: %v", err)
	}
	if err := svc.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO members (id, space_id, user_id, kind, display_name, access_level, created_at) VALUES ('m-mal', ?, ?, 'human', 'Mallory', 'admin', ?)`,
			studio.ID, other.User.ID, store.FormatTime(time.Now().UTC()),
		)
		return err
	}); err != nil {
		t.Fatalf("add admin: %v", err)
	}
	mallory, err := loadMember(ctx, svc.store.ReadDB(), "m-mal")
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	var domErr *domain.Error
	if _, err := mk(mallory, "Intruder"); !errors.As(err, &domErr) || domErr.Code != domain.CodePermissionDenied {
		t.Fatalf("admin on someone else's computer: want permission_denied, got %v", err)
	}

	// An Agent may only place children on the Computer that hosts it.
	if err := svc.SetMemberPermission(ctx, owner, studioAgent.ID, domain.PermAgentCreate, true); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := mk(studioAgent, "Child"); err != nil {
		t.Fatalf("agent child on own computer: %v", err)
	}
	second, _, err := svc.PairComputerDirect(ctx, ownerUser.ID, "mini", "mini.local", "macos")
	if err != nil {
		t.Fatalf("pair second: %v", err)
	}
	if _, err := svc.CreateAgent(ctx, studioAgent, CreateAgentInput{DisplayName: "Far", RoleText: "r", ComputerID: second.ID, DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig}); !errors.As(err, &domErr) || domErr.Code != domain.CodePermissionDenied {
		t.Fatalf("agent child on another computer: want permission_denied, got %v", err)
	}
}
