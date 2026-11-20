package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestThreadReplySummaryRidesOnRootMessage(t *testing.T) {
	svc, _, _, owner, _ := setupTestService(t)
	ctx := context.Background()
	conv, err := svc.CreateChannel(ctx, owner, CreateChannelInput{Slug: "eng", Kind: domain.ConversationPublic})
	if err != nil {
		t.Fatal(err)
	}
	root, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: conv.ID, BodyMarkdown: "ship it"})
	if err != nil {
		t.Fatal(err)
	}

	// Five replies, oldest first, so the summary must keep the newest three.
	var bodies []string
	for i := 0; i < 5; i++ {
		body := fmt.Sprintf("reply %d", i)
		bodies = append(bodies, body)
		if _, err := svc.SendMessage(ctx, owner, SendMessageInput{
			ConversationID:   conv.ID,
			ReplyToMessageID: root.Message.ID,
			BodyMarkdown:     body,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// A system event in the same thread counts as a reply but never as a preview.
	thread := threadConvID(t, svc, root.Message.ID)
	if err := svc.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO messages (id, space_id, conversation_id, seq, author_member_id, kind, body_markdown, created_at)
			 VALUES ('sys-1', ?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE conversation_id = ?), ?, 'system', 'thread created', ?)`,
			root.Message.SpaceID, thread, thread, owner.ID, "2026-01-01T00:00:05Z",
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	page, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: conv.ID, Latest: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("expected only the root message in the channel page, got %d", len(page.Messages))
	}
	got := page.Messages[0]
	if got.ThreadConvID != thread {
		t.Fatalf("thread id = %q, want %q", got.ThreadConvID, thread)
	}
	if got.ReplyCount != 6 {
		t.Fatalf("reply count = %d, want 6 (system events count)", got.ReplyCount)
	}
	if got.LastReplyAt == nil {
		t.Fatal("last reply time is not set")
	}
	if len(got.LatestReplies) != inlineReplyLimit {
		t.Fatalf("latest replies = %d, want %d", len(got.LatestReplies), inlineReplyLimit)
	}
	var previews []string
	for _, reply := range got.LatestReplies {
		previews = append(previews, reply.Preview)
		if reply.AuthorName != owner.DisplayName {
			t.Fatalf("author name = %q, want %q", reply.AuthorName, owner.DisplayName)
		}
		if reply.AuthorKind != domain.MemberKindHuman {
			t.Fatalf("author kind = %q, want human", reply.AuthorKind)
		}
		if reply.CreatedAt.IsZero() || reply.MessageID == "" || reply.Seq == 0 {
			t.Fatalf("incomplete preview: %+v", reply)
		}
	}
	if want := bodies[2:]; strings.Join(previews, "|") != strings.Join(want, "|") {
		t.Fatalf("previews = %v, want newest three in order %v", previews, want)
	}
}

func TestThreadReplyPreviewTruncatesLongBodies(t *testing.T) {
	svc, _, _, owner, _ := setupTestService(t)
	ctx := context.Background()
	conv, err := svc.CreateChannel(ctx, owner, CreateChannelInput{Slug: "eng", Kind: domain.ConversationPublic})
	if err != nil {
		t.Fatal(err)
	}
	root, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: conv.ID, BodyMarkdown: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID:   conv.ID,
		ReplyToMessageID: root.Message.ID,
		BodyMarkdown:     strings.Repeat("长", previewRuneLimit+5),
	}); err != nil {
		t.Fatal(err)
	}

	page, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: conv.ID, Latest: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	preview := []rune(page.Messages[0].LatestReplies[0].Preview)
	if len(preview) != previewRuneLimit+1 || preview[previewRuneLimit] != '…' {
		t.Fatalf("preview length = %d, last rune = %q", len(preview), preview[len(preview)-1])
	}
}

func threadConvID(t *testing.T, svc *Service, rootMessageID string) string {
	t.Helper()
	var threadID string
	if err := svc.store.ReadDB().QueryRowContext(context.Background(),
		`SELECT id FROM conversations WHERE kind = 'thread' AND root_message_id = ?`, rootMessageID,
	).Scan(&threadID); err != nil {
		t.Fatal(err)
	}
	return threadID
}
