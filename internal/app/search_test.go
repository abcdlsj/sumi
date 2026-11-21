package app

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

func addHumanMemberForTest(t *testing.T, svc *Service, spaceID, email, name string) domain.Member {
	t.Helper()
	ctx := context.Background()
	auth, err := svc.Register(ctx, email, "password123", name)
	if err != nil {
		t.Fatalf("register human: %v", err)
	}
	id := uuid.NewString()
	now := time.Now().UTC()
	if err := svc.Store().WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO members (id, space_id, user_id, kind, display_name, access_level, created_at) VALUES (?, ?, ?, 'human', ?, 'member', ?)`,
			id, spaceID, auth.User.ID, name, store.FormatTime(now),
		)
		return err
	}); err != nil {
		t.Fatalf("insert human member: %v", err)
	}
	return domain.Member{
		ID:          id,
		SpaceID:     spaceID,
		Kind:        domain.MemberKindHuman,
		DisplayName: name,
		AccessLevel: domain.AccessMember,
		CreatedAt:   now,
	}
}

func TestSearchMessagesVisibilityBoundaries(t *testing.T) {
	svc, _, space, owner, comp := setupTestService(t)
	ctx := context.Background()

	bob := addHumanMemberForTest(t, svc, space.ID, "bob@example.com", "Bob")
	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Lin",
		RoleText:     "Engineer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create lin: %v", err)
	}

	convs, err := svc.ListConversations(ctx, owner)
	if err != nil || len(convs) == 0 {
		t.Fatalf("list conversations: %v", err)
	}
	generalID := convs[0].ID

	priv, err := svc.CreateChannel(ctx, owner, CreateChannelInput{
		Slug: "secret-ops",
		Kind: domain.ConversationPrivate,
	})
	if err != nil {
		t.Fatalf("create private channel: %v", err)
	}

	dm, err := svc.GetOrCreateDM(ctx, owner, lin.ID)
	if err != nil {
		t.Fatalf("create dm: %v", err)
	}

	pubMsg, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: generalID,
		BodyMarkdown:   "Deploy checklist for AlphaRelease 中文检索",
	})
	if err != nil {
		t.Fatalf("send public message: %v", err)
	}
	privMsg, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: priv.ID,
		BodyMarkdown:   "Private AlphaRelease token rotation",
	})
	if err != nil {
		t.Fatalf("send private message: %v", err)
	}
	privThreadReply, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID:   priv.ID,
		ReplyToMessageID: privMsg.Message.ID,
		BodyMarkdown:     "Thread detail for AlphaRelease",
	})
	if err != nil {
		t.Fatalf("send private thread reply: %v", err)
	}
	dmMsg, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: dm.ID,
		BodyMarkdown:   "DM note about AlphaRelease",
	})
	if err != nil {
		t.Fatalf("send dm message: %v", err)
	}

	// Owner sees all four hits across public, private, private thread, and DM.
	ownerHits, err := svc.SearchMessages(ctx, owner, SearchMessagesInput{Query: "alpharelease"})
	if err != nil {
		t.Fatalf("owner search: %v", err)
	}
	if len(ownerHits) != 4 {
		t.Fatalf("expected 4 hits for owner, got %d", len(ownerHits))
	}

	// Bob (human not in private channel or owner-lin DM) sees only the public hit.
	bobHits, err := svc.SearchMessages(ctx, bob, SearchMessagesInput{Query: "alpharelease"})
	if err != nil {
		t.Fatalf("bob search: %v", err)
	}
	if len(bobHits) != 1 || bobHits[0].MessageID != pubMsg.Message.ID {
		t.Fatalf("expected only public hit for bob, got %d hits", len(bobHits))
	}

	// CJK substring match works as expected.
	cjkHits, err := svc.SearchMessages(ctx, bob, SearchMessagesInput{Query: "中文检索"})
	if err != nil {
		t.Fatalf("cjk search: %v", err)
	}
	if len(cjkHits) != 1 || cjkHits[0].MessageID != pubMsg.Message.ID {
		t.Fatalf("expected 1 cjk hit, got %d", len(cjkHits))
	}

	// Scoping search to an inaccessible private channel fails with permission_denied or not_found.
	if _, err := svc.SearchMessages(ctx, bob, SearchMessagesInput{Query: "AlphaRelease", ConversationID: priv.ID}); err == nil {
		t.Fatalf("expected error when bob searches private channel")
	} else {
		var de *domain.Error
		if !errors.As(err, &de) || (de.Code != domain.CodePermissionDenied && de.Code != domain.CodeNotFound) {
			t.Fatalf("unexpected error code: %v", err)
		}
	}

	// Verify hit IDs for owner include private thread and DM.
	seen := map[string]bool{}
	for _, h := range ownerHits {
		seen[h.MessageID] = true
	}
	if !seen[privThreadReply.Message.ID] || !seen[dmMsg.Message.ID] {
		t.Fatalf("owner hits missing private thread or dm message")
	}
}

func TestAgentReadProgressAndReadEvent(t *testing.T) {
	svc, _, space, owner, comp := setupTestService(t)
	ctx := context.Background()

	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Lin",
		RoleText:     "Engineer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create lin: %v", err)
	}

	convs, err := svc.ListConversations(ctx, owner)
	if err != nil || len(convs) == 0 {
		t.Fatalf("list conversations: %v", err)
	}
	generalID := convs[0].ID

	if _, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: generalID, BodyMarkdown: "first"}); err != nil {
		t.Fatalf("send first: %v", err)
	}
	m2, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: generalID, BodyMarkdown: "second"})
	if err != nil {
		t.Fatalf("send second: %v", err)
	}

	// Before Lin reads, agent_reads is empty.
	page, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: generalID})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(page.AgentReads) != 0 {
		t.Fatalf("expected 0 agent reads initially, got %d", len(page.AgentReads))
	}

	subCh, unsub := svc.Hub().Subscribe(space.ID)
	defer unsub()

	// Lin reads messages with MarkRead=true; agent_reads reports Lin's watermark and SSE event fires.
	if _, err := svc.ListMessages(ctx, lin, ListMessagesInput{ConversationID: generalID, MarkRead: true}); err != nil {
		t.Fatalf("lin list messages: %v", err)
	}

	select {
	case ev := <-subCh:
		if ev.Kind != "conversation.read_updated" {
			t.Fatalf("expected conversation.read_updated event, got %s", ev.Kind)
		}
	default:
		t.Fatalf("expected conversation.read_updated event on agent read")
	}

	page, err = svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: generalID})
	if err != nil {
		t.Fatalf("list messages after lin read: %v", err)
	}
	if len(page.AgentReads) != 1 || page.AgentReads[0].MemberID != lin.ID || page.AgentReads[0].ReadSeq != m2.Message.Seq {
		t.Fatalf("unexpected agent reads: %+v", page.AgentReads)
	}

	// Reading again at the same seq must not emit duplicate events.
	if err := svc.MarkRead(ctx, lin, generalID, m2.Message.Seq); err != nil {
		t.Fatalf("repeat mark read: %v", err)
	}
	select {
	case ev := <-subCh:
		t.Fatalf("expected no duplicate event on unchanged read_seq, got %s", ev.Kind)
	default:
	}
}
