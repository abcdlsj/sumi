package app

import (
	"context"
	"fmt"
	"testing"

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
