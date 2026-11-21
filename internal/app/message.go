package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

var mentionTokenRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])@([A-Za-z0-9_]{1,32})\b`)

func parseMentionNames(body string) (mentionAll bool, names []string) {
	matches := mentionTokenRe.FindAllStringSubmatch(body, -1)
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		name := m[1]
		low := strings.ToLower(name)
		if low == "all" {
			mentionAll = true
			continue
		}
		if _, ok := seen[low]; !ok {
			seen[low] = struct{}{}
			names = append(names, low)
		}
	}
	return mentionAll, names
}

type SendMessageInput struct {
	ConversationID   string
	ReplyToMessageID string // If set on a channel/dm message, routes into (or lazily creates) that message's thread conversation.
	BodyMarkdown     string
	AttachmentIDs    []string // Previously uploaded attachments owned by the actor; bound in the same write transaction.
	EnforceFreshness bool     // True for Agent writes: refuses if unread messages from others exist after read_seq.
	// IdempotencyKey makes a retried send return the original message instead of posting twice.
	IdempotencyKey string
}

type SendMessageResult struct {
	Message        domain.Message   `json:"message,omitempty"`
	Held           bool             `json:"held,omitempty"`
	UnreadMessages []domain.Message `json:"unread_messages,omitempty"`
	// OmittedUnreadCount counts older unread messages not included in UnreadMessages.
	OmittedUnreadCount int64 `json:"omitted_unread_count,omitempty"`
	// Replayed marks a result returned for a repeated Idempotency-Key; nothing new was posted.
	Replayed bool `json:"replayed,omitempty"`
	// WokenCount is the number of Agents woken by this message (0 on held/replayed or unwoken channel posts).
	WokenCount int `json:"woken_count"`
}

func (s *Service) SendMessage(ctx context.Context, actor domain.Member, in SendMessageInput) (SendMessageResult, error) {
	body := strings.TrimSpace(in.BodyMarkdown)
	if body == "" {
		return SendMessageResult{}, domain.NewError(domain.CodeInvalidArgument, "body_markdown is required")
	}
	if len(in.AttachmentIDs) > domain.MaxAttachmentsPerMessage {
		return SendMessageResult{}, domain.NewError(domain.CodeInvalidArgument, "at most 10 attachments per message")
	}
	if len(in.IdempotencyKey) > maxIdempotencyKey {
		return SendMessageResult{}, domain.NewError(domain.CodeInvalidArgument, "Idempotency-Key is longer than 128 characters")
	}
	requestHash := ""
	if in.IdempotencyKey != "" {
		requestHash = messageRequestHash(in)
	}

	var result SendMessageResult
	var events []domain.SpaceEvent
	var wakes []wakeTarget

	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		targetMeta, err := canAccessConversation(ctx, tx, actor, in.ConversationID)
		if err != nil {
			return err
		}
		if err := checkNotArchived(ctx, tx, targetMeta); err != nil {
			return err
		}
		if in.IdempotencyKey != "" {
			prev, err := replayMessageTx(ctx, tx, actor.ID, in.IdempotencyKey, requestHash)
			if err != nil {
				return err
			}
			if prev != nil {
				result = SendMessageResult{Message: *prev, Replayed: true}
				return nil
			}
		}

		// Lazily create or resolve Thread conversation when ReplyToMessageID is provided.
		if in.ReplyToMessageID != "" {
			if targetMeta.Kind == domain.ConversationThread || targetMeta.Kind == domain.ConversationTask {
				return domain.NewError(domain.CodeInvalidArgument, "cannot nest threads inside a thread or task conversation")
			}
			var msgConvID string
			if err := tx.QueryRowContext(ctx, `SELECT conversation_id FROM messages WHERE id = ?`, in.ReplyToMessageID).Scan(&msgConvID); err != nil {
				return domain.NewError(domain.CodeNotFound, "reply_to_message_id not found")
			}
			if msgConvID != targetMeta.ID {
				return domain.NewError(domain.CodeInvalidArgument, "reply_to_message_id does not belong to target conversation")
			}
			threadMeta, created, err := ensureThreadConversationTx(ctx, tx, targetMeta, in.ReplyToMessageID)
			if err != nil {
				return err
			}
			targetMeta = threadMeta
			if created {
				ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.created", map[string]any{
					"conversation_id":        threadMeta.ID,
					"kind":                   "thread",
					"parent_conversation_id": threadMeta.ParentConversationID,
					"root_message_id":        threadMeta.RootMessageID,
				})
				if err != nil {
					return err
				}
				events = append(events, ev)
			}
		}

		// Freshness Guard: hold Agent writes if unread messages from other members exist.
		if in.EnforceFreshness {
			held, err := checkFreshnessTx(ctx, tx, targetMeta.ID, actor.ID)
			if err != nil {
				return err
			}
			if held != nil {
				result = SendMessageResult{Held: true, UnreadMessages: held.Messages, OmittedUnreadCount: held.Omitted}
				return nil
			}
		}

		mentionAll, mentionNames := parseMentionNames(body)
		mentionIDs, err := resolveMentionTargetsTx(ctx, tx, actor.SpaceID, mentionNames)
		if err != nil {
			return err
		}

		msg, err := insertMessageTx(ctx, tx, targetMeta.ID, actor, domain.MessageKindText, body, "", "{}", mentionAll, mentionIDs)
		if err != nil {
			return err
		}
		if in.IdempotencyKey != "" {
			if err := recordIdempotencyTx(ctx, tx, actor.ID, in.IdempotencyKey, requestHash, msg.ID); err != nil {
				return err
			}
		}
		if len(in.AttachmentIDs) > 0 {
			if err := bindAttachmentsTx(ctx, tx, actor, msg.ID, targetMeta.SpaceID, in.AttachmentIDs); err != nil {
				return err
			}
			msg.Attachments = loadBoundAttachmentsTx(ctx, tx, msg.ID)
		}

		// A human posting in a public channel joins its explicit member roster.
		if actor.Kind == domain.MemberKindHuman {
			if boundary, berr := resolveVisibilityBoundary(ctx, tx, targetMeta); berr == nil && boundary.Kind == domain.ConversationPublic {
				_, _ = tx.ExecContext(ctx,
					`INSERT OR IGNORE INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
					boundary.ID, actor.SpaceID, actor.ID, store.FormatTime(time.Now().UTC()),
				)
			}
		}

		// Author has seen up to their own message; auto-follow thread/task on reply.
		if err := advanceReadSeqTx(ctx, tx, targetMeta.ID, actor.ID, msg.Seq); err != nil {
			return err
		}
		if targetMeta.Kind == domain.ConversationThread || targetMeta.Kind == domain.ConversationTask {
			if err := setFollowingTx(ctx, tx, targetMeta.ID, actor.ID, true); err != nil {
				return err
			}
			// Auto-follow mentioned members as well.
			for _, mid := range mentionIDs {
				if err := setFollowingTx(ctx, tx, targetMeta.ID, mid, true); err != nil {
					return err
				}
			}
			// For threads, also auto-follow the root message author.
			if targetMeta.Kind == domain.ConversationThread && targetMeta.RootMessageID != "" {
				var rootAuthor string
				if err := tx.QueryRowContext(ctx, `SELECT author_member_id FROM messages WHERE id = ?`, targetMeta.RootMessageID).Scan(&rootAuthor); err == nil && rootAuthor != "" {
					_ = setFollowingTx(ctx, tx, targetMeta.ID, rootAuthor, true)
				}
			}
		}

		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "message.created", map[string]any{
			"message_id":             msg.ID,
			"conversation_id":        targetMeta.ID,
			"parent_conversation_id": targetMeta.ParentConversationID,
			"root_message_id":        targetMeta.RootMessageID,
			"task_id":                targetMeta.TaskID,
			"seq":                    msg.Seq,
			"author_member_id":       actor.ID,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)

		// Compute agent wake targets (DM, @mention, followed thread/task, or notify_level=all).
		wakes, err = computeAgentWakesTx(ctx, tx, targetMeta, actor.ID, mentionAll, mentionIDs)
		if err != nil {
			return err
		}
		result = SendMessageResult{Message: msg, WokenCount: len(wakes)}
		return nil
	})
	if err != nil {
		return SendMessageResult{}, err
	}
	if !result.Held && !result.Replayed {
		s.dispatchPostCommit(events, wakes)
	}
	if result.Replayed {
		s.loadMessageAttachments(ctx, []domain.Message{result.Message})
	}
	return result, nil
}

func ensureThreadConversationTx(ctx context.Context, tx *sql.Tx, parent convMeta, rootMsgID string) (convMeta, bool, error) {
	var existingID string
	var nextSeq int64
	err := tx.QueryRowContext(ctx,
		`SELECT id, next_seq FROM conversations WHERE kind = 'thread' AND root_message_id = ?`,
		rootMsgID,
	).Scan(&existingID, &nextSeq)
	if err == nil {
		return convMeta{
			ID:                   existingID,
			SpaceID:              parent.SpaceID,
			Kind:                 domain.ConversationThread,
			ParentConversationID: parent.ID,
			RootMessageID:        rootMsgID,
			NextSeq:              nextSeq,
		}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return convMeta{}, false, domain.WrapError(domain.CodeInternal, "query thread", err)
	}
	now := time.Now().UTC()
	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO conversations (id, space_id, kind, parent_conversation_id, root_message_id, created_at)
		 VALUES (?, ?, 'thread', ?, ?, ?)`,
		id, parent.SpaceID, parent.ID, rootMsgID, store.FormatTime(now),
	); err != nil {
		return convMeta{}, false, domain.WrapError(domain.CodeInternal, "create thread conversation", err)
	}
	return convMeta{
		ID:                   id,
		SpaceID:              parent.SpaceID,
		Kind:                 domain.ConversationThread,
		ParentConversationID: parent.ID,
		RootMessageID:        rootMsgID,
		NextSeq:              1,
	}, true, nil
}

func resolveMentionTargetsTx(ctx context.Context, tx *sql.Tx, spaceID string, lowerNames []string) ([]string, error) {
	if len(lowerNames) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(lowerNames))
	for _, low := range lowerNames {
		var id string
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM members WHERE space_id = ? AND lower(display_name) = ? AND retired_at IS NULL`,
			spaceID, low,
		).Scan(&id)
		if err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func insertMessageTx(
	ctx context.Context,
	tx *sql.Tx,
	convID string,
	author domain.Member,
	kind domain.MessageKind,
	body string,
	actionKind domain.ActionKind,
	payloadJSON string,
	mentionAll bool,
	mentionIDs []string,
) (domain.Message, error) {
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`UPDATE conversations SET next_seq = next_seq + 1 WHERE id = ? RETURNING next_seq - 1`,
		convID,
	).Scan(&seq); err != nil {
		return domain.Message{}, domain.WrapError(domain.CodeInternal, "allocate conversation seq", err)
	}
	now := time.Now().UTC()
	msgID := uuid.NewString()
	mAllInt := 0
	if mentionAll {
		mAllInt = 1
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (id, space_id, conversation_id, seq, author_member_id, kind, body_markdown, action_kind, payload_json, mention_all, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msgID, author.SpaceID, convID, seq, author.ID, string(kind), body, string(actionKind), payloadJSON, mAllInt, store.FormatTime(now),
	); err != nil {
		return domain.Message{}, domain.WrapError(domain.CodeInternal, "insert message", err)
	}
	for _, mid := range mentionIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO message_mentions (message_id, conversation_id, seq, member_id) VALUES (?, ?, ?, ?)`,
			msgID, convID, seq, mid,
		); err != nil {
			return domain.Message{}, domain.WrapError(domain.CodeInternal, "insert mention", err)
		}
	}
	return domain.Message{
		ID:               msgID,
		SpaceID:          author.SpaceID,
		ConversationID:   convID,
		Seq:              seq,
		AuthorMemberID:   author.ID,
		AuthorName:       author.DisplayName,
		AuthorKind:       author.Kind,
		Kind:             kind,
		BodyMarkdown:     body,
		ActionKind:       actionKind,
		PayloadJSON:      payloadJSON,
		MentionAll:       mentionAll,
		MentionMemberIDs: mentionIDs,
		CreatedAt:        now,
	}, nil
}

func insertActionMessageTx(
	ctx context.Context,
	tx *sql.Tx,
	actor domain.Member,
	convID string,
	actionKind domain.ActionKind,
	payload map[string]any,
) (domain.Message, domain.SpaceEvent, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return domain.Message{}, domain.SpaceEvent{}, domain.WrapError(domain.CodeInternal, "marshal action payload", err)
	}
	msg, err := insertMessageTx(ctx, tx, convID, actor, domain.MessageKindAction, "", actionKind, string(raw), false, nil)
	if err != nil {
		return domain.Message{}, domain.SpaceEvent{}, err
	}
	ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "message.created", map[string]any{
		"message_id":      msg.ID,
		"conversation_id": convID,
		"seq":             msg.Seq,
		"kind":            "action",
		"action_kind":     actionKind,
	})
	if err != nil {
		return domain.Message{}, domain.SpaceEvent{}, err
	}
	return msg, ev, nil
}

const heldUnreadLimit = 10

type heldUnread struct {
	Messages []domain.Message
	Omitted  int64
}

// checkFreshnessTx implements the Freshness Guard. If others posted after the actor's read_seq it
// returns the newest heldUnreadLimit of those messages, counts the rest, and advances read_seq to
// the newest one so a deliberate retry is not held again by messages the actor was already shown.
func checkFreshnessTx(ctx context.Context, tx *sql.Tx, convID, actorID string) (*heldUnread, error) {
	var readSeq int64
	_ = tx.QueryRowContext(ctx,
		`SELECT read_seq FROM conversation_states WHERE conversation_id = ? AND member_id = ?`,
		convID, actorID,
	).Scan(&readSeq)
	var total, maxSeq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(seq), 0) FROM messages
		 WHERE conversation_id = ? AND seq > ? AND author_member_id <> ? AND deleted_at IS NULL`,
		convID, readSeq, actorID,
	).Scan(&total, &maxSeq); err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "count unread", err)
	}
	if total == 0 {
		return nil, nil
	}
	fromSeq := readSeq
	if total > heldUnreadLimit {
		// Start right before the heldUnreadLimit-th newest unread message from others.
		if err := tx.QueryRowContext(ctx,
			`SELECT seq FROM messages
			 WHERE conversation_id = ? AND seq > ? AND author_member_id <> ? AND deleted_at IS NULL
			 ORDER BY seq DESC LIMIT 1 OFFSET ?`,
			convID, readSeq, actorID, heldUnreadLimit-1,
		).Scan(&fromSeq); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "locate unread window", err)
		}
		fromSeq--
	}
	msgs, err := queryMessages(ctx, tx, convID, fromSeq, heldUnreadLimit, actorID)
	if err != nil {
		return nil, err
	}
	if err := advanceReadSeqTx(ctx, tx, convID, actorID, maxSeq); err != nil {
		return nil, err
	}
	return &heldUnread{Messages: msgs, Omitted: total - int64(len(msgs))}, nil
}

func queryMessages(ctx context.Context, q queryer, convID string, afterSeq int64, limit int, excludeAuthorID string) ([]domain.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if excludeAuthorID != "" {
		rows, err = q.QueryContext(ctx,
			`SELECT msg.id, msg.space_id, msg.conversation_id, msg.seq, msg.author_member_id, m.display_name, m.kind,
			        msg.kind, msg.body_markdown, msg.action_kind, msg.payload_json, msg.mention_all, msg.created_at
			 FROM messages msg
			 JOIN members m ON m.id = msg.author_member_id
			 WHERE msg.conversation_id = ? AND msg.seq > ? AND msg.author_member_id <> ? AND msg.deleted_at IS NULL
			 ORDER BY msg.seq ASC LIMIT ?`,
			convID, afterSeq, excludeAuthorID, limit,
		)
	} else {
		rows, err = q.QueryContext(ctx,
			`SELECT msg.id, msg.space_id, msg.conversation_id, msg.seq, msg.author_member_id, m.display_name, m.kind,
			        msg.kind, msg.body_markdown, msg.action_kind, msg.payload_json, msg.mention_all, msg.created_at
			 FROM messages msg
			 JOIN members m ON m.id = msg.author_member_id
			 WHERE msg.conversation_id = ? AND msg.seq > ? AND msg.deleted_at IS NULL
			 ORDER BY msg.seq ASC LIMIT ?`,
			convID, afterSeq, limit,
		)
	}
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "query messages", err)
	}
	defer rows.Close()

	msgs := make([]domain.Message, 0)
	for rows.Next() {
		var msg domain.Message
		var authorKind, kind, actionKind, createdAt string
		var mentionAll int
		if err := rows.Scan(
			&msg.ID, &msg.SpaceID, &msg.ConversationID, &msg.Seq, &msg.AuthorMemberID, &msg.AuthorName, &authorKind,
			&kind, &msg.BodyMarkdown, &actionKind, &msg.PayloadJSON, &mentionAll, &createdAt,
		); err != nil {
			return nil, err
		}
		msg.AuthorKind = domain.MemberKind(authorKind)
		msg.Kind = domain.MessageKind(kind)
		msg.ActionKind = domain.ActionKind(actionKind)
		msg.MentionAll = mentionAll == 1
		msg.CreatedAt = store.ParseTime(createdAt)
		msgs = append(msgs, msg)
	}
	return msgs, rows.Err()
}

// queryMessagesTail fetches the newest `limit` messages (or those strictly before `beforeSeq`),
// returning them in ascending seq order along with whether older messages remain.
func queryMessagesTail(ctx context.Context, q queryer, convID string, beforeSeq int64, limit int) ([]domain.Message, bool, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if beforeSeq > 0 {
		rows, err = q.QueryContext(ctx,
			`SELECT msg.id, msg.space_id, msg.conversation_id, msg.seq, msg.author_member_id, m.display_name, m.kind,
			        msg.kind, msg.body_markdown, msg.action_kind, msg.payload_json, msg.mention_all, msg.created_at
			 FROM messages msg
			 JOIN members m ON m.id = msg.author_member_id
			 WHERE msg.conversation_id = ? AND msg.seq < ? AND msg.deleted_at IS NULL
			 ORDER BY msg.seq DESC LIMIT ?`,
			convID, beforeSeq, limit+1,
		)
	} else {
		rows, err = q.QueryContext(ctx,
			`SELECT msg.id, msg.space_id, msg.conversation_id, msg.seq, msg.author_member_id, m.display_name, m.kind,
			        msg.kind, msg.body_markdown, msg.action_kind, msg.payload_json, msg.mention_all, msg.created_at
			 FROM messages msg
			 JOIN members m ON m.id = msg.author_member_id
			 WHERE msg.conversation_id = ? AND msg.deleted_at IS NULL
			 ORDER BY msg.seq DESC LIMIT ?`,
			convID, limit+1,
		)
	}
	if err != nil {
		return nil, false, domain.WrapError(domain.CodeInternal, "query messages tail", err)
	}
	defer rows.Close()

	msgs := make([]domain.Message, 0)
	for rows.Next() {
		var msg domain.Message
		var authorKind, kind, actionKind, createdAt string
		var mentionAll int
		if err := rows.Scan(
			&msg.ID, &msg.SpaceID, &msg.ConversationID, &msg.Seq, &msg.AuthorMemberID, &msg.AuthorName, &authorKind,
			&kind, &msg.BodyMarkdown, &actionKind, &msg.PayloadJSON, &mentionAll, &createdAt,
		); err != nil {
			return nil, false, err
		}
		msg.AuthorKind = domain.MemberKind(authorKind)
		msg.Kind = domain.MessageKind(kind)
		msg.ActionKind = domain.ActionKind(actionKind)
		msg.MentionAll = mentionAll == 1
		msg.CreatedAt = store.ParseTime(createdAt)
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := false
	if len(msgs) > limit {
		hasMore = true
		msgs = msgs[:limit]
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, hasMore, nil
}

// loadBoundAttachmentsTx reads attachments just bound inside the same transaction.
func loadBoundAttachmentsTx(ctx context.Context, tx *sql.Tx, messageID string) []domain.Attachment {
	rows, err := tx.QueryContext(ctx,
		`SELECT a.id, a.space_id, a.uploader_member_id, a.name, a.media_type, a.size_bytes, a.created_at
		 FROM message_attachments ma
		 JOIN attachments a ON a.id = ma.attachment_id
		 WHERE ma.message_id = ?
		 ORDER BY ma.position ASC`,
		messageID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []domain.Attachment
	for rows.Next() {
		var att domain.Attachment
		var createdAt string
		if err := rows.Scan(&att.ID, &att.SpaceID, &att.UploaderMemberID, &att.Name, &att.MediaType, &att.SizeBytes, &createdAt); err != nil {
			return nil
		}
		att.MessageID = messageID
		att.CreatedAt = store.ParseTime(createdAt)
		out = append(out, att)
	}
	return out
}

type ListMessagesInput struct {
	ConversationID string
	AfterSeq       int64
	BeforeSeq      int64
	Latest         bool
	UnreadOnly     bool
	Limit          int
	MarkRead       bool
}

// MessagePage is one read of a Conversation together with the viewer's attention state.
type MessagePage struct {
	Messages  []domain.Message `json:"messages"`
	ReadSeq   int64            `json:"read_seq"`
	Following bool             `json:"following"`
	HasMore   bool             `json:"has_more,omitempty"`
}

func (s *Service) ListMessages(ctx context.Context, viewer domain.Member, in ListMessagesInput) (MessagePage, error) {
	c, err := canAccessConversation(ctx, s.store.ReadDB(), viewer, in.ConversationID)
	if err != nil {
		return MessagePage{}, err
	}
	var currentReadSeq int64
	var following bool
	_ = s.store.ReadDB().QueryRowContext(ctx,
		`SELECT read_seq, following FROM conversation_states WHERE conversation_id = ? AND member_id = ?`,
		c.ID, viewer.ID,
	).Scan(&currentReadSeq, &following)

	var msgs []domain.Message
	var hasMore bool
	if in.BeforeSeq > 0 || (in.Latest && in.AfterSeq == 0 && !in.UnreadOnly) {
		msgs, hasMore, err = queryMessagesTail(ctx, s.store.ReadDB(), c.ID, in.BeforeSeq, in.Limit)
	} else {
		afterSeq := in.AfterSeq
		if in.UnreadOnly && currentReadSeq > afterSeq {
			afterSeq = currentReadSeq
		}
		msgs, err = queryMessages(ctx, s.store.ReadDB(), c.ID, afterSeq, in.Limit, "")
	}
	if err != nil {
		return MessagePage{}, err
	}
	s.loadMessageAttachments(ctx, msgs)
	s.loadMessageReactions(ctx, viewer.ID, msgs)

	// Enrich channel/DM root messages with thread reply summaries and attached tasks.
	if c.Kind != domain.ConversationThread && c.Kind != domain.ConversationTask && len(msgs) > 0 {
		s.loadThreadSummaries(ctx, msgs)
		for i := range msgs {
			taskRows, err := s.store.ReadDB().QueryContext(ctx,
				`SELECT t.id, t.space_id, t.number, t.channel_id, COALESCE(t.source_message_id, ''), COALESCE(t.conversation_id, ''),
				        t.title, t.description, t.status, t.creator_member_id, COALESCE(t.assignee_member_id, ''),
				        COALESCE(am.display_name, ''), COALESCE(am.kind, ''), t.revision,
				        COALESCE((SELECT COUNT(*) FROM messages tm WHERE tm.conversation_id = t.conversation_id AND tm.deleted_at IS NULL), 0),
				        t.created_at, t.updated_at
				 FROM tasks t
				 LEFT JOIN members am ON am.id = t.assignee_member_id
				 WHERE t.source_message_id = ?
				 ORDER BY t.number ASC`,
				msgs[i].ID,
			)
			if err == nil {
				for taskRows.Next() {
					var tk domain.Task
					var status, aKind, createdAt, updatedAt string
					if err := taskRows.Scan(
						&tk.ID, &tk.SpaceID, &tk.Number, &tk.ChannelID, &tk.SourceMessageID, &tk.ConversationID,
						&tk.Title, &tk.Description, &status, &tk.CreatorMemberID, &tk.AssigneeMemberID,
						&tk.AssigneeName, &aKind, &tk.Revision, &tk.MessageCount, &createdAt, &updatedAt,
					); err == nil {
						tk.Status = domain.TaskStatus(status)
						tk.AssigneeKind = domain.MemberKind(aKind)
						tk.CreatedAt = store.ParseTime(createdAt)
						tk.UpdatedAt = store.ParseTime(updatedAt)
						msgs[i].AttachedTasks = append(msgs[i].AttachedTasks, tk)
					}
				}
				_ = taskRows.Close()
			}
		}
	}

	if in.MarkRead && len(msgs) > 0 {
		lastSeq := msgs[len(msgs)-1].Seq
		if lastSeq > currentReadSeq {
			_ = s.MarkRead(ctx, viewer, c.ID, lastSeq)
		}
	}
	return MessagePage{Messages: msgs, ReadSeq: currentReadSeq, Following: following, HasMore: hasMore}, nil
}

// ArchiveChannel marks a public or private channel read-only; no messages, reactions, task changes,
// or notifications can happen while archived.
