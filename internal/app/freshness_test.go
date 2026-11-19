package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestFreshnessGuardHoldsWithNewestUnreadWindow(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()
	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", RoleText: "r", ComputerID: comp.ID,
		DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	dm, err := svc.GetOrCreateDM(ctx, owner, lin.ID)
	if err != nil {
		t.Fatal(err)
	}
	const backlog = 25
	var lastSeq int64
	for i := 0; i < backlog; i++ {
		res, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: dm.ID, BodyMarkdown: fmt.Sprintf("m%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		lastSeq = res.Message.Seq
	}

	held, err := svc.SendMessage(ctx, lin, SendMessageInput{ConversationID: dm.ID, BodyMarkdown: "reply", EnforceFreshness: true})
	if err != nil {
		t.Fatal(err)
	}
	if !held.Held || len(held.UnreadMessages) != heldUnreadLimit || held.OmittedUnreadCount != backlog-heldUnreadLimit {
		t.Fatalf("held=%v unread=%d omitted=%d", held.Held, len(held.UnreadMessages), held.OmittedUnreadCount)
	}
	if got := held.UnreadMessages[len(held.UnreadMessages)-1].Seq; got != lastSeq {
		t.Fatalf("held window must end at the newest message: got seq %d want %d", got, lastSeq)
	}
	if got := held.UnreadMessages[0].Seq; got != lastSeq-heldUnreadLimit+1 {
		t.Fatalf("held window must be the newest %d messages in order: first seq %d", heldUnreadLimit, got)
	}

	// One hold covers the whole backlog: the deliberate retry goes through.
	ok, err := svc.SendMessage(ctx, lin, SendMessageInput{ConversationID: dm.ID, BodyMarkdown: "reply", EnforceFreshness: true})
	if err != nil || ok.Held {
		t.Fatalf("retry after hold must send: held=%v err=%v", ok.Held, err)
	}
}

func TestSendMessageIdempotencyKey(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()
	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", RoleText: "r", ComputerID: comp.ID,
		DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	dm, err := svc.GetOrCreateDM(ctx, owner, lin.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := SendMessageInput{ConversationID: dm.ID, BodyMarkdown: "result", EnforceFreshness: true, IdempotencyKey: "k1"}
	first, err := svc.SendMessage(ctx, lin, in)
	if err != nil || first.Held || first.Replayed {
		t.Fatalf("first send: %+v err=%v", first, err)
	}
	again, err := svc.SendMessage(ctx, lin, in)
	if err != nil || !again.Replayed || again.Message.ID != first.Message.ID {
		t.Fatalf("retry must replay the original message: %+v err=%v", again, err)
	}
	page, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: dm.ID, Limit: 50})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("retry posted a duplicate: n=%d err=%v", len(page.Messages), err)
	}

	in.BodyMarkdown = "different"
	var de *domain.Error
	if _, err := svc.SendMessage(ctx, lin, in); !errors.As(err, &de) || de.Code != domain.CodeConflict {
		t.Fatalf("reused key with another body must conflict, got %v", err)
	}

	// Keys are scoped per actor.
	other, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: dm.ID, BodyMarkdown: "result", IdempotencyKey: "k1"})
	if err != nil || other.Replayed {
		t.Fatalf("another actor's key must not replay: %+v err=%v", other, err)
	}

	// After the window the same key sends anew.
	if err := svc.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE idempotency_records SET created_at = '2000-01-01T00:00:00Z'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	in.BodyMarkdown = "result"
	late, err := svc.SendMessage(ctx, lin, in)
	if err != nil || late.Replayed || late.Message.ID == first.Message.ID {
		t.Fatalf("expired key must send a new message: %+v err=%v", late, err)
	}
}

func TestInboxPreviewCutsOnRuneBoundary(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()
	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", RoleText: "r", ComputerID: comp.ID,
		DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	dm, err := svc.GetOrCreateDM(ctx, owner, lin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: dm.ID, BodyMarkdown: strings.Repeat("部署", 100)}); err != nil {
		t.Fatal(err)
	}
	items, err := svc.ListInbox(ctx, lin, "needs_me")
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox: %v %d", err, len(items))
	}
	if p := items[0].LatestPreview; !utf8.ValidString(p) || utf8.RuneCountInString(p) != 121 {
		t.Fatalf("preview must be 120 runes plus ellipsis, got %d runes valid=%v", utf8.RuneCountInString(p), utf8.ValidString(p))
	}
}
