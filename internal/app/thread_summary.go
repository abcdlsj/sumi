package app

import (
	"context"
	"database/sql"
	"strings"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

// inlineReplyLimit is how many of a thread's newest replies ride along with the
// root message. The count stays authoritative; the previews are display only.
const inlineReplyLimit = 3

// loadThreadSummaries fills ThreadConvID, ReplyCount, LastReplyAt and the newest
// replies on every root message of a page, in two batch reads for the whole page.
func (s *Service) loadThreadSummaries(ctx context.Context, msgs []domain.Message) {
	if len(msgs) == 0 {
		return
	}
	placeholders := make([]string, len(msgs))
	args := make([]any, len(msgs))
	index := make(map[string]int, len(msgs))
	for i := range msgs {
		placeholders[i] = "?"
		args[i] = msgs[i].ID
		index[msgs[i].ID] = i
	}
	in := strings.Join(placeholders, ",")

	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT th.root_message_id, th.id, COUNT(rm.id), MAX(rm.created_at)
		 FROM conversations th
		 LEFT JOIN messages rm ON rm.conversation_id = th.id AND rm.deleted_at IS NULL
		 WHERE th.kind = 'thread' AND th.root_message_id IN (`+in+`)
		 GROUP BY th.root_message_id, th.id`,
		args...,
	)
	if err != nil {
		return
	}
	for rows.Next() {
		var rootID, threadID string
		var replyCount int64
		var lastReply sql.NullString
		if err := rows.Scan(&rootID, &threadID, &replyCount, &lastReply); err != nil {
			break
		}
		i, ok := index[rootID]
		if !ok {
			continue
		}
		msgs[i].ThreadConvID = threadID
		msgs[i].ReplyCount = replyCount
		if lastReply.Valid {
			t := store.ParseTime(lastReply.String)
			msgs[i].LastReplyAt = &t
		}
	}
	_ = rows.Close()

	replyArgs := make([]any, 0, len(args)+1)
	replyArgs = append(replyArgs, args...)
	replyArgs = append(replyArgs, inlineReplyLimit)
	rows, err = s.store.ReadDB().QueryContext(ctx,
		`SELECT root_message_id, id, seq, author_member_id, author_name, author_kind, body_markdown, created_at
		 FROM (
		   SELECT th.root_message_id AS root_message_id, rm.id AS id, rm.seq AS seq,
		          rm.author_member_id AS author_member_id,
		          COALESCE(m.display_name, '') AS author_name, COALESCE(m.kind, '') AS author_kind,
		          rm.body_markdown AS body_markdown, rm.created_at AS created_at,
		          ROW_NUMBER() OVER (PARTITION BY th.root_message_id ORDER BY rm.seq DESC) AS rn
		   FROM conversations th
		   JOIN messages rm ON rm.conversation_id = th.id AND rm.deleted_at IS NULL AND rm.kind = 'text'
		   LEFT JOIN members m ON m.id = rm.author_member_id
		   WHERE th.kind = 'thread' AND th.root_message_id IN (`+in+`)
		 )
		 WHERE rn <= ?
		 ORDER BY root_message_id ASC, seq ASC`,
		replyArgs...,
	)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var rootID string
		var reply domain.ThreadReplyPreview
		var authorKind, body, createdAt string
		if err := rows.Scan(&rootID, &reply.MessageID, &reply.Seq, &reply.AuthorID, &reply.AuthorName, &authorKind, &body, &createdAt); err != nil {
			return
		}
		i, ok := index[rootID]
		if !ok {
			continue
		}
		reply.AuthorKind = domain.MemberKind(authorKind)
		reply.Preview = previewText(body)
		reply.CreatedAt = store.ParseTime(createdAt)
		msgs[i].LatestReplies = append(msgs[i].LatestReplies, reply)
	}
}
