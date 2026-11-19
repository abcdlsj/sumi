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

type CreateChannelInput struct {
	Slug                 string
	Kind                 domain.ConversationKind // public or private
	Topic                string
	MemberIDs            []string
	SourceConversationID string
}

func (s *Service) CreateChannel(ctx context.Context, actor domain.Member, in CreateChannelInput) (domain.Conversation, error) {
	ok, err := hasPermission(ctx, s.store.ReadDB(), actor, domain.PermChannelCreate)
	if err != nil {
		return domain.Conversation{}, err
	}
	if !ok {
		return domain.Conversation{}, domain.NewError(domain.CodePermissionDenied, "missing channel.create permission")
	}
	if err := domain.ValidateSlug(in.Slug); err != nil {
		return domain.Conversation{}, err
	}
	if in.Kind != domain.ConversationPublic && in.Kind != domain.ConversationPrivate {
		return domain.Conversation{}, domain.NewError(domain.CodeInvalidArgument, "channel kind must be public or private")
	}

	now := time.Now().UTC()
	conv := domain.Conversation{
		ID:          uuid.NewString(),
		SpaceID:     actor.SpaceID,
		Kind:        in.Kind,
		Slug:        in.Slug,
		Topic:       strings.TrimSpace(in.Topic),
		NextSeq:     1,
		CreatedAt:   now,
		NotifyLevel: domain.NotifyMentions,
	}
	var events []domain.SpaceEvent

	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO conversations (id, space_id, kind, slug, topic, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			conv.ID, conv.SpaceID, string(conv.Kind), conv.Slug, conv.Topic, store.FormatTime(now),
		); err != nil {
			if store.IsUniqueViolation(err) {
				return domain.NewError(domain.CodeConflict, "channel slug already exists")
			}
			return domain.WrapError(domain.CodeInternal, "insert channel", err)
		}

		membersToAdd := make(map[string]struct{})
		membersToAdd[actor.ID] = struct{}{}
		for _, mid := range in.MemberIDs {
			if mid != "" {
				membersToAdd[mid] = struct{}{}
			}
		}
		for mid := range membersToAdd {
			m, err := loadMember(ctx, tx, mid)
			if err != nil || m.SpaceID != actor.SpaceID || m.RetiredAt != nil {
				return domain.NewError(domain.CodeInvalidArgument, "invalid channel member")
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
				conv.ID, actor.SpaceID, mid, store.FormatTime(now),
			); err != nil {
				return domain.WrapError(domain.CodeInternal, "add channel member", err)
			}
		}

		if in.SourceConversationID != "" {
			if _, err := canAccessConversation(ctx, tx, actor, in.SourceConversationID); err != nil {
				return err
			}
			_, ev, err := insertActionMessageTx(ctx, tx, actor, in.SourceConversationID, domain.ActionChannelCreated, map[string]any{
				"channel_id": conv.ID,
				"slug":       conv.Slug,
				"kind":       conv.Kind,
			})
			if err != nil {
				return err
			}
			events = append(events, ev)
		}

		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.created", map[string]any{
			"conversation_id": conv.ID,
			"kind":            conv.Kind,
			"slug":            conv.Slug,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return domain.Conversation{}, err
	}
	s.dispatchPostCommit(events, nil)
	return conv, nil
}

func (s *Service) InviteToChannel(ctx context.Context, actor domain.Member, channelID, targetMemberID string) error {
	ok, err := hasPermission(ctx, s.store.ReadDB(), actor, domain.PermChannelManageMembers)
	if err != nil {
		return err
	}
	if !ok {
		return domain.NewError(domain.CodePermissionDenied, "missing channel.manage_members permission")
	}
	var events []domain.SpaceEvent
	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := canAccessConversation(ctx, tx, actor, channelID)
		if err != nil {
			return err
		}
		if c.Kind != domain.ConversationPublic && c.Kind != domain.ConversationPrivate {
			return domain.NewError(domain.CodeInvalidArgument, "can only invite to public or private channels")
		}
		target, err := loadMember(ctx, tx, targetMemberID)
		if err != nil || target.SpaceID != actor.SpaceID || target.RetiredAt != nil {
			return domain.NewError(domain.CodeNotFound, "target member not found")
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
			c.ID, actor.SpaceID, target.ID, store.FormatTime(time.Now().UTC()),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "invite member", err)
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.members_changed", map[string]any{
			"conversation_id": c.ID,
			"member_id":       target.ID,
			"action":          "joined",
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return err
	}
	s.dispatchPostCommit(events, nil)
	return nil
}

func (s *Service) RemoveFromChannel(ctx context.Context, actor domain.Member, channelID, targetMemberID string) error {
	if actor.ID != targetMemberID {
		ok, err := hasPermission(ctx, s.store.ReadDB(), actor, domain.PermChannelManageMembers)
		if err != nil {
			return err
		}
		if !ok {
			return domain.NewError(domain.CodePermissionDenied, "missing channel.manage_members permission")
		}
	}
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := canAccessConversation(ctx, tx, actor, channelID)
		if err != nil {
			return err
		}
		if c.Kind != domain.ConversationPublic && c.Kind != domain.ConversationPrivate {
			return domain.NewError(domain.CodeInvalidArgument, "can only remove members from channels")
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM conversation_members WHERE conversation_id = ? AND member_id = ?`,
			c.ID, targetMemberID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "remove member", err)
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.members_changed", map[string]any{
			"conversation_id": c.ID,
			"member_id":       targetMemberID,
			"action":          "removed",
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return err
	}
	s.dispatchPostCommit(events, nil)
	return nil
}

func (s *Service) GetOrCreateDM(ctx context.Context, actor domain.Member, peerMemberID string) (domain.Conversation, error) {
	if peerMemberID == "" || peerMemberID == actor.ID {
		return domain.Conversation{}, domain.NewError(domain.CodeInvalidArgument, "peer_member_id must be another member")
	}
	var conv domain.Conversation
	var events []domain.SpaceEvent

	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		peer, err := loadMember(ctx, tx, peerMemberID)
		if err != nil {
			return err
		}
		if peer.SpaceID != actor.SpaceID || peer.RetiredAt != nil {
			return domain.NewError(domain.CodeNotFound, "peer member not found")
		}

		// Find existing DM shared by exactly actor and peer.
		var existingID, createdAt string
		var nextSeq int64
		err = tx.QueryRowContext(ctx,
			`SELECT c.id, c.next_seq, c.created_at
			 FROM conversations c
			 JOIN conversation_members m1 ON m1.conversation_id = c.id AND m1.member_id = ?
			 JOIN conversation_members m2 ON m2.conversation_id = c.id AND m2.member_id = ?
			 WHERE c.space_id = ? AND c.kind = 'dm'
			 LIMIT 1`,
			actor.ID, peer.ID, actor.SpaceID,
		).Scan(&existingID, &nextSeq, &createdAt)
		if err == nil {
			conv = domain.Conversation{
				ID:              existingID,
				SpaceID:         actor.SpaceID,
				Kind:            domain.ConversationDM,
				NextSeq:         nextSeq,
				CreatedAt:       store.ParseTime(createdAt),
				PeerMemberID:    peer.ID,
				PeerDisplayName: peer.DisplayName,
				PeerKind:        peer.Kind,
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.WrapError(domain.CodeInternal, "query dm", err)
		}

		now := time.Now().UTC()
		dmID := uuid.NewString()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO conversations (id, space_id, kind, created_at) VALUES (?, ?, 'dm', ?)`,
			dmID, actor.SpaceID, store.FormatTime(now),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "insert dm", err)
		}
		for _, mid := range []string{actor.ID, peer.ID} {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
				dmID, actor.SpaceID, mid, store.FormatTime(now),
			); err != nil {
				return domain.WrapError(domain.CodeInternal, "insert dm member", err)
			}
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.created", map[string]any{
			"conversation_id": dmID,
			"kind":            "dm",
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		conv = domain.Conversation{
			ID:              dmID,
			SpaceID:         actor.SpaceID,
			Kind:            domain.ConversationDM,
			NextSeq:         1,
			CreatedAt:       now,
			PeerMemberID:    peer.ID,
			PeerDisplayName: peer.DisplayName,
			PeerKind:        peer.Kind,
		}
		return nil
	})
	if err != nil {
		return domain.Conversation{}, err
	}
	s.dispatchPostCommit(events, nil)
	return conv, nil
}

func (s *Service) ListConversations(ctx context.Context, viewer domain.Member) ([]domain.Conversation, error) {
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT c.id, c.space_id, c.kind, COALESCE(c.slug, ''), c.topic, c.next_seq, c.created_at,
		        COALESCE(cs.read_seq, 0), COALESCE(cs.notify_level, 'mentions'), COALESCE(cs.following, 0),
		        COALESCE((SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id AND m.seq > COALESCE(cs.read_seq, 0) AND m.author_member_id <> ? AND m.deleted_at IS NULL), 0),
		        COALESCE((SELECT COUNT(*) FROM message_mentions mm WHERE mm.conversation_id = c.id AND mm.member_id = ? AND mm.seq > COALESCE(cs.read_seq, 0)), 0),
		        COALESCE(peer.id, ''), COALESCE(peer.display_name, ''), COALESCE(peer.kind, ''), COALESCE(pa.runtime_state, '')
		 FROM conversations c
		 LEFT JOIN conversation_members cm ON cm.conversation_id = c.id AND cm.member_id = ?
		 LEFT JOIN conversation_states cs ON cs.conversation_id = c.id AND cs.member_id = ?
		 LEFT JOIN conversation_members pcm ON c.kind = 'dm' AND pcm.conversation_id = c.id AND pcm.member_id <> ?
		 LEFT JOIN members peer ON peer.id = pcm.member_id
		 LEFT JOIN agents pa ON pa.member_id = peer.id
		 WHERE c.space_id = ?
		   AND c.archived_at IS NULL
		   AND (
		       c.kind = 'public'
		       OR (c.kind IN ('private', 'dm') AND cm.member_id IS NOT NULL)
		   )
		 ORDER BY CASE c.kind WHEN 'public' THEN 0 WHEN 'private' THEN 1 ELSE 2 END, lower(COALESCE(c.slug, peer.display_name, '')) ASC`,
		viewer.ID, viewer.ID, viewer.ID, viewer.ID, viewer.ID, viewer.SpaceID,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list conversations", err)
	}
	defer rows.Close()

	out := make([]domain.Conversation, 0)
	for rows.Next() {
		var c domain.Conversation
		var kind, createdAt, notify, peerKind, peerRT string
		var following int
		if err := rows.Scan(
			&c.ID, &c.SpaceID, &kind, &c.Slug, &c.Topic, &c.NextSeq, &createdAt,
			&c.ReadSeq, &notify, &following, &c.UnreadCount, &c.MentionCount,
			&c.PeerMemberID, &c.PeerDisplayName, &peerKind, &peerRT,
		); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "scan conversation", err)
		}
		c.Kind = domain.ConversationKind(kind)
		c.CreatedAt = store.ParseTime(createdAt)
		c.NotifyLevel = domain.NotifyLevel(notify)
		c.Following = following == 1
		c.PeerKind = domain.MemberKind(peerKind)
		c.PeerRuntime = domain.RuntimeState(peerRT)
		out = append(out, c)
	}
	return out, rows.Err()
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
		result = SendMessageResult{Message: msg}
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

func computeAgentWakesTx(ctx context.Context, tx *sql.Tx, target convMeta, authorID string, mentionAll bool, mentionIDs []string) ([]wakeTarget, error) {
	boundary, err := resolveVisibilityBoundary(ctx, tx, target)
	if err != nil {
		return nil, err
	}
	mentionedSet := make(map[string]struct{}, len(mentionIDs))
	for _, id := range mentionIDs {
		mentionedSet[id] = struct{}{}
	}

	// Candidate agents that can access this conversation and have an online computer.
	var rows *sql.Rows
	if boundary.Kind == domain.ConversationPublic {
		rows, err = tx.QueryContext(ctx,
			`SELECT a.member_id, COALESCE(a.computer_id, ''), COALESCE(cs.notify_level, 'mentions'), COALESCE(cs.following, 0)
			 FROM agents a
			 JOIN members m ON m.id = a.member_id
			 JOIN computers c ON c.id = a.computer_id AND c.connection_status = 'online'
			 LEFT JOIN conversation_states cs ON cs.conversation_id = ? AND cs.member_id = a.member_id
			 WHERE a.space_id = ? AND m.retired_at IS NULL AND a.member_id <> ?`,
			target.ID, target.SpaceID, authorID,
		)
	} else {
		rows, err = tx.QueryContext(ctx,
			`SELECT a.member_id, COALESCE(a.computer_id, ''), COALESCE(cs.notify_level, 'mentions'), COALESCE(cs.following, 0)
			 FROM agents a
			 JOIN members m ON m.id = a.member_id
			 JOIN conversation_members cm ON cm.conversation_id = ? AND cm.member_id = a.member_id
			 JOIN computers c ON c.id = a.computer_id AND c.connection_status = 'online'
			 LEFT JOIN conversation_states cs ON cs.conversation_id = ? AND cs.member_id = a.member_id
			 WHERE a.space_id = ? AND m.retired_at IS NULL AND a.member_id <> ?`,
			boundary.ID, target.ID, target.SpaceID, authorID,
		)
	}
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "query wake agents", err)
	}
	defer rows.Close()

	var wakes []wakeTarget
	for rows.Next() {
		var agentID, compID, notify string
		var following int
		if err := rows.Scan(&agentID, &compID, &notify, &following); err != nil {
			return nil, err
		}
		_, directlyMentioned := mentionedSet[agentID]
		var reason string
		switch {
		case target.Kind == domain.ConversationDM:
			reason = "dm"
		case directlyMentioned || mentionAll:
			reason = "mention"
		case (target.Kind == domain.ConversationThread || target.Kind == domain.ConversationTask) && following == 1:
			reason = "following"
		case (target.Kind == domain.ConversationPublic || target.Kind == domain.ConversationPrivate) && notify == string(domain.NotifyAll):
			reason = "channel_all"
		}
		if reason != "" {
			wakes = append(wakes, wakeTarget{
				computerID:     compID,
				agentMemberID:  agentID,
				conversationID: target.ID,
				reason:         reason,
			})
		}
	}
	return wakes, rows.Err()
}

// heldUnreadLimit bounds how many unread messages a held write returns; older ones are counted only.
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
	UnreadOnly     bool
	Limit          int
	MarkRead       bool
}

// MessagePage is one read of a Conversation together with the viewer's attention state.
type MessagePage struct {
	Messages  []domain.Message `json:"messages"`
	ReadSeq   int64            `json:"read_seq"`
	Following bool             `json:"following"`
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

	afterSeq := in.AfterSeq
	if in.UnreadOnly && currentReadSeq > afterSeq {
		afterSeq = currentReadSeq
	}

	msgs, err := queryMessages(ctx, s.store.ReadDB(), c.ID, afterSeq, in.Limit, "")
	if err != nil {
		return MessagePage{}, err
	}
	s.loadMessageAttachments(ctx, msgs)

	// Enrich channel/DM root messages with compact thread reply counts and attached tasks.
	if c.Kind != domain.ConversationThread && c.Kind != domain.ConversationTask && len(msgs) > 0 {
		for i := range msgs {
			var threadID string
			var replyCount int64
			var lastReply sql.NullString
			if err := s.store.ReadDB().QueryRowContext(ctx,
				`SELECT th.id, COUNT(rm.id), MAX(rm.created_at)
				 FROM conversations th
				 LEFT JOIN messages rm ON rm.conversation_id = th.id AND rm.deleted_at IS NULL
				 WHERE th.kind = 'thread' AND th.root_message_id = ?
				 GROUP BY th.id`,
				msgs[i].ID,
			).Scan(&threadID, &replyCount, &lastReply); err == nil {
				msgs[i].ThreadConvID = threadID
				msgs[i].ReplyCount = replyCount
				if lastReply.Valid {
					t := store.ParseTime(lastReply.String)
					msgs[i].LastReplyAt = &t
				}
			}

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
	return MessagePage{Messages: msgs, ReadSeq: currentReadSeq, Following: following}, nil
}
