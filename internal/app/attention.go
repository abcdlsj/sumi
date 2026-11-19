package app

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

func advanceReadSeqTx(ctx context.Context, tx *sql.Tx, convID, memberID string, seq int64) error {
	now := store.FormatTime(time.Now().UTC())
	_, err := tx.ExecContext(ctx,
		`INSERT INTO conversation_states (conversation_id, member_id, read_seq, notify_level, following, done_seq, updated_at)
		 VALUES (?, ?, ?, 'mentions', 0, 0, ?)
		 ON CONFLICT(conversation_id, member_id) DO UPDATE SET
		   read_seq = CASE WHEN excluded.read_seq > conversation_states.read_seq THEN excluded.read_seq ELSE conversation_states.read_seq END,
		   updated_at = excluded.updated_at`,
		convID, memberID, seq, now,
	)
	if err != nil {
		return domain.WrapError(domain.CodeInternal, "advance read_seq", err)
	}
	return nil
}

func setFollowingTx(ctx context.Context, tx *sql.Tx, convID, memberID string, following bool) error {
	now := store.FormatTime(time.Now().UTC())
	f := 0
	if following {
		f = 1
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO conversation_states (conversation_id, member_id, read_seq, notify_level, following, done_seq, updated_at)
		 VALUES (?, ?, 0, 'mentions', ?, 0, ?)
		 ON CONFLICT(conversation_id, member_id) DO UPDATE SET
		   following = excluded.following,
		   updated_at = excluded.updated_at`,
		convID, memberID, f, now,
	)
	if err != nil {
		return domain.WrapError(domain.CodeInternal, "set following", err)
	}
	return nil
}

func (s *Service) MarkRead(ctx context.Context, actor domain.Member, convID string, seq int64) error {
	return s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := canAccessConversation(ctx, tx, actor, convID)
		if err != nil {
			return err
		}
		if seq <= 0 {
			seq = c.NextSeq - 1
		}
		return advanceReadSeqTx(ctx, tx, c.ID, actor.ID, seq)
	})
}

func (s *Service) MarkDone(ctx context.Context, actor domain.Member, convID string) error {
	return s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := canAccessConversation(ctx, tx, actor, convID)
		if err != nil {
			return err
		}
		latestSeq := c.NextSeq - 1
		now := store.FormatTime(time.Now().UTC())
		_, err = tx.ExecContext(ctx,
			`INSERT INTO conversation_states (conversation_id, member_id, read_seq, notify_level, following, done_seq, updated_at)
			 VALUES (?, ?, ?, 'mentions', 0, ?, ?)
			 ON CONFLICT(conversation_id, member_id) DO UPDATE SET
			   read_seq = CASE WHEN excluded.read_seq > conversation_states.read_seq THEN excluded.read_seq ELSE conversation_states.read_seq END,
			   done_seq = CASE WHEN excluded.done_seq > conversation_states.done_seq THEN excluded.done_seq ELSE conversation_states.done_seq END,
			   updated_at = excluded.updated_at`,
			c.ID, actor.ID, latestSeq, latestSeq, now,
		)
		return err
	})
}

func (s *Service) SetNotifyLevel(ctx context.Context, actor domain.Member, convID string, level domain.NotifyLevel) error {
	if err := domain.ValidateNotifyLevel(level); err != nil {
		return err
	}
	return s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := canAccessConversation(ctx, tx, actor, convID)
		if err != nil {
			return err
		}
		now := store.FormatTime(time.Now().UTC())
		_, err = tx.ExecContext(ctx,
			`INSERT INTO conversation_states (conversation_id, member_id, read_seq, notify_level, following, done_seq, updated_at)
			 VALUES (?, ?, 0, ?, 0, 0, ?)
			 ON CONFLICT(conversation_id, member_id) DO UPDATE SET
			   notify_level = excluded.notify_level,
			   updated_at = excluded.updated_at`,
			c.ID, actor.ID, string(level), now,
		)
		return err
	})
}

func (s *Service) SetFollowing(ctx context.Context, actor domain.Member, convID string, following bool) error {
	return s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := canAccessConversation(ctx, tx, actor, convID)
		if err != nil {
			return err
		}
		return setFollowingTx(ctx, tx, c.ID, actor.ID, following)
	})
}

// ListInbox computes the unified attention view for both Humans and Agents from conversation_states.
// filter: "needs_me" (default) | "unread" | "all".
func (s *Service) ListInbox(ctx context.Context, viewer domain.Member, filter string) ([]domain.InboxItem, error) {
	if filter == "" {
		filter = "needs_me"
	}
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT c.id, c.kind, COALESCE(c.slug, ''), COALESCE(pc.slug, ''), COALESCE(t.number, 0), COALESCE(t.id, ''), COALESCE(t.title, ''),
		        c.next_seq - 1 AS latest_seq,
		        COALESCE(cs.read_seq, 0), COALESCE(cs.done_seq, 0), COALESCE(cs.notify_level, 'mentions'), COALESCE(cs.following, 0),
		        COALESCE(peer.display_name, ''),
		        COALESCE((SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id AND m.seq > COALESCE(cs.read_seq, 0) AND m.author_member_id <> ? AND m.deleted_at IS NULL), 0) AS unread_cnt,
		        COALESCE((SELECT COUNT(*) FROM message_mentions mm WHERE mm.conversation_id = c.id AND mm.member_id = ? AND mm.seq > COALESCE(cs.read_seq, 0)), 0) AS mention_cnt,
		        COALESCE((SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id AND m.mention_all = 1 AND m.seq > COALESCE(cs.read_seq, 0) AND m.author_member_id <> ? AND m.deleted_at IS NULL), 0) AS mention_all_cnt
		 FROM conversations c
		 LEFT JOIN conversations pc ON pc.id = c.parent_conversation_id
		 LEFT JOIN conversation_members cm ON cm.conversation_id = COALESCE(c.parent_conversation_id, c.id) AND cm.member_id = ?
		 LEFT JOIN conversation_states cs ON cs.conversation_id = c.id AND cs.member_id = ?
		 LEFT JOIN tasks t ON t.id = c.task_id
		 LEFT JOIN conversation_members pcm ON c.kind = 'dm' AND pcm.conversation_id = c.id AND pcm.member_id <> ?
		 LEFT JOIN members peer ON peer.id = pcm.member_id
		 WHERE c.space_id = ?
		   AND c.archived_at IS NULL
		   AND c.next_seq > 1
		   AND (
		       COALESCE(pc.kind, c.kind) = 'public'
		       OR cm.member_id IS NOT NULL
		   )`,
		viewer.ID, viewer.ID, viewer.ID, viewer.ID, viewer.ID, viewer.ID, viewer.SpaceID,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list inbox", err)
	}
	defer rows.Close()

	items := make([]domain.InboxItem, 0)
	for rows.Next() {
		var convID, kindStr, slug, parentSlug, taskID, taskTitle, notifyStr, peerName string
		var taskNumber, latestSeq, readSeq, doneSeq, unreadCnt, mentionCnt, mentionAllCnt int64
		var followingInt int
		if err := rows.Scan(
			&convID, &kindStr, &slug, &parentSlug, &taskNumber, &taskID, &taskTitle,
			&latestSeq, &readSeq, &doneSeq, &notifyStr, &followingInt, &peerName,
			&unreadCnt, &mentionCnt, &mentionAllCnt,
		); err != nil {
			return nil, err
		}
		if unreadCnt == 0 && filter != "all" {
			continue
		}
		if latestSeq <= doneSeq && filter != "all" {
			continue
		}

		kind := domain.ConversationKind(kindStr)
		notify := domain.NotifyLevel(notifyStr)
		following := followingInt == 1
		totalMentions := mentionCnt + mentionAllCnt

		needsAttention := false
		reason := "unread"
		switch {
		case kind == domain.ConversationDM && unreadCnt > 0:
			needsAttention = true
			reason = "dm"
		case totalMentions > 0 && notify != domain.NotifyMute:
			needsAttention = true
			reason = "mention"
		case (kind == domain.ConversationThread || kind == domain.ConversationTask) && following && unreadCnt > 0:
			needsAttention = true
			if kind == domain.ConversationTask {
				reason = "task"
			} else {
				reason = "thread"
			}
		case (kind == domain.ConversationPublic || kind == domain.ConversationPrivate) && notify == domain.NotifyAll && unreadCnt > 0:
			needsAttention = true
			reason = "channel"
		}

		if filter == "needs_me" && !needsAttention {
			continue
		}

		var latestAuthor, latestBody, latestAction, latestAtStr string
		_ = s.store.ReadDB().QueryRowContext(ctx,
			`SELECT m.display_name, msg.body_markdown, msg.action_kind, msg.created_at
			 FROM messages msg JOIN members m ON m.id = msg.author_member_id
			 WHERE msg.conversation_id = ? AND msg.deleted_at IS NULL
			 ORDER BY msg.seq DESC LIMIT 1`,
			convID,
		).Scan(&latestAuthor, &latestBody, &latestAction, &latestAtStr)

		preview := strings.TrimSpace(latestBody)
		if preview == "" && latestAction != "" {
			preview = "[" + latestAction + "]"
		}
		if len(preview) > 120 {
			preview = preview[:120] + "…"
		}

		title := slug
		switch kind {
		case domain.ConversationPublic, domain.ConversationPrivate:
			title = "#" + slug
		case domain.ConversationDM:
			title = "@" + peerName
		case domain.ConversationThread:
			title = "Thread in #" + parentSlug
		case domain.ConversationTask:
			title = fmt.Sprintf("#%d %s", taskNumber, taskTitle)
		}

		items = append(items, domain.InboxItem{
			ConversationID:   convID,
			Kind:             kind,
			Title:            title,
			ParentSlug:       parentSlug,
			TaskNumber:       taskNumber,
			TaskID:           taskID,
			UnreadCount:      unreadCnt,
			MentionCount:     totalMentions,
			NeedsAttention:   needsAttention,
			Reason:           reason,
			LatestSeq:        latestSeq,
			LatestAuthorName: latestAuthor,
			LatestPreview:    preview,
			LatestAt:         store.ParseTime(latestAtStr),
			Following:        following,
			NotifyLevel:      notify,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].NeedsAttention != items[j].NeedsAttention {
			return items[i].NeedsAttention
		}
		return items[i].LatestAt.After(items[j].LatestAt)
	})
	return items, nil
}
