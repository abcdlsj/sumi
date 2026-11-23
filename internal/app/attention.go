package app

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
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
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := canAccessConversation(ctx, tx, actor, convID)
		if err != nil {
			return err
		}
		if seq <= 0 {
			seq = c.NextSeq - 1
		}
		var prevSeq int64
		_ = tx.QueryRowContext(ctx,
			`SELECT read_seq FROM conversation_states WHERE conversation_id = ? AND member_id = ?`,
			c.ID, actor.ID,
		).Scan(&prevSeq)
		if err := advanceReadSeqTx(ctx, tx, c.ID, actor.ID, seq); err != nil {
			return err
		}
		// Broadcast agent read watermark advances so the UI updates agent read faces live.
		if actor.Kind == domain.MemberKindAgent && seq > prevSeq {
			ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.read_updated", map[string]any{
				"conversation_id": c.ID,
				"member_id":       actor.ID,
				"read_seq":        seq,
			})
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(events) > 0 {
		s.dispatchPostCommit(events, nil)
	}
	return nil
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
// inboxTarget renders the CLI target an Agent can pass straight to `sumi read` / `sumi send`.
func inboxTarget(kind domain.ConversationKind, convID, slug, peerName string, taskNumber int64) string {
	switch kind {
	case domain.ConversationPublic, domain.ConversationPrivate:
		return "#" + slug
	case domain.ConversationDM:
		return "dm:@" + peerName
	case domain.ConversationTask:
		return "task:#" + strconv.FormatInt(taskNumber, 10)
	}
	return "thread:" + convID
}

func (s *Service) ListInbox(ctx context.Context, viewer domain.Member, filter string) ([]domain.InboxItem, error) {
	if filter == "" {
		filter = "needs_me"
	}
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT c.id, c.kind, COALESCE(c.slug, ''), COALESCE(pc.slug, ''), COALESCE(t.number, 0), COALESCE(t.id, ''), COALESCE(t.title, ''), COALESCE(t.status, ''),
		        c.next_seq - 1 AS latest_seq,
		        COALESCE(cs.read_seq, 0), COALESCE(cs.done_seq, 0), COALESCE(cs.notify_level, 'mentions'), COALESCE(cs.following, 0),
		        COALESCE(peer.display_name, ''),
		        COALESCE((SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id AND m.seq > COALESCE(cs.read_seq, 0) AND m.author_member_id <> ? AND m.deleted_at IS NULL), 0) AS unread_cnt,
		        COALESCE((SELECT COUNT(*) FROM message_mentions mm WHERE mm.conversation_id = c.id AND mm.member_id = ? AND mm.seq > COALESCE(cs.read_seq, 0)), 0) AS mention_cnt,
		        COALESCE((SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id AND m.mention_all = 1 AND m.seq > COALESCE(cs.read_seq, 0) AND m.author_member_id <> ? AND m.deleted_at IS NULL), 0) AS mention_all_cnt,
		        COALESCE(lm.display_name, ''), COALESCE(latest.body_markdown, ''), COALESCE(latest.action_kind, ''), COALESCE(latest.created_at, '')
		 FROM conversations c
		 LEFT JOIN conversations pc ON pc.id = c.parent_conversation_id
		 LEFT JOIN conversation_members cm ON cm.conversation_id = COALESCE(c.parent_conversation_id, c.id) AND cm.member_id = ?
		 LEFT JOIN conversation_states cs ON cs.conversation_id = c.id AND cs.member_id = ?
		 LEFT JOIN tasks t ON t.id = c.task_id
		 LEFT JOIN conversation_members pcm ON c.kind = 'dm' AND pcm.conversation_id = c.id AND pcm.member_id <> ?
		 LEFT JOIN members peer ON peer.id = pcm.member_id
		 LEFT JOIN messages latest ON latest.conversation_id = c.id
		   AND latest.seq = (SELECT MAX(m2.seq) FROM messages m2 WHERE m2.conversation_id = c.id AND m2.deleted_at IS NULL)
		 LEFT JOIN members lm ON lm.id = latest.author_member_id
		 WHERE c.space_id = ?
		   AND c.archived_at IS NULL
		   AND pc.archived_at IS NULL
		   AND c.next_seq > 1
		   AND (
		       (COALESCE(pc.kind, c.kind) = 'public' AND ? = 'human')
		       OR cm.member_id IS NOT NULL
		   )`,
		viewer.ID, viewer.ID, viewer.ID, viewer.ID, viewer.ID, viewer.ID, viewer.SpaceID, string(viewer.Kind),
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list inbox", err)
	}
	defer rows.Close()

	type inboxCandidate struct {
		item    domain.InboxItem
		readSeq int64
	}
	candidates := make([]inboxCandidate, 0)
	for rows.Next() {
		var convID, kindStr, slug, parentSlug, taskID, taskTitle, taskStatusStr, notifyStr, peerName string
		var latestAuthor, latestBody, latestAction, latestAtStr string
		var taskNumber, latestSeq, readSeq, doneSeq, unreadCnt, mentionCnt, mentionAllCnt int64
		var followingInt int
		if err := rows.Scan(
			&convID, &kindStr, &slug, &parentSlug, &taskNumber, &taskID, &taskTitle, &taskStatusStr,
			&latestSeq, &readSeq, &doneSeq, &notifyStr, &followingInt, &peerName,
			&unreadCnt, &mentionCnt, &mentionAllCnt,
			&latestAuthor, &latestBody, &latestAction, &latestAtStr,
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
		taskTerminal := kind == domain.ConversationTask && domain.TaskStatus(taskStatusStr).IsTerminal()
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
		case (kind == domain.ConversationThread || (kind == domain.ConversationTask && !taskTerminal)) && following && unreadCnt > 0:
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

		preview := previewText(latestBody)
		if preview == "" && latestAction != "" {
			preview = "[" + latestAction + "]"
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

		candidates = append(candidates, inboxCandidate{
			readSeq: readSeq,
			item: domain.InboxItem{
				ConversationID:   convID,
				Target:           inboxTarget(kind, convID, slug, peerName, taskNumber),
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
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()

	// Apply agent-to-agent wake governance to agent inbox attention (checked after closing rows
	// to avoid nested connection-pool grabs). Human inbox is never suppressed.
	items := make([]domain.InboxItem, 0, len(candidates))
	for _, cand := range candidates {
		it := cand.item
		if viewer.Kind == domain.MemberKindAgent && it.NeedsAttention {
			suppressed := false
			switch it.Reason {
			case "task", "thread", "channel":
				if streak, serr := consecutiveAgentTextCount(ctx, s.store.ReadDB(), it.ConversationID, DefaultAgentChainLimit); serr == nil && streak >= DefaultAgentChainLimit {
					suppressed = true
				}
			case "mention":
				if it.Kind != domain.ConversationDM {
					if streak, serr := consecutiveAgentTextCount(ctx, s.store.ReadDB(), it.ConversationID, DefaultAgentHardChainLimit); serr == nil && streak >= DefaultAgentHardChainLimit {
						if hasHuman, herr := hasUnreadHumanMention(ctx, s.store.ReadDB(), it.ConversationID, viewer.ID, cand.readSeq); herr == nil && !hasHuman {
							suppressed = true
						}
					}
				}
			case "dm":
				if limited, lerr := isAgentDMWindowLimited(ctx, s.store.ReadDB(), it.ConversationID, s.now()); lerr == nil && limited {
					suppressed = true
				}
			}
			if suppressed {
				if filter == "needs_me" {
					continue
				}
				it.NeedsAttention = false
				it.Reason = "unread"
			}
		}
		items = append(items, it)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].NeedsAttention != items[j].NeedsAttention {
			return items[i].NeedsAttention
		}
		return items[i].LatestAt.After(items[j].LatestAt)
	})
	return items, nil
}
