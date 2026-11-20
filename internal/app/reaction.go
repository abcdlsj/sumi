package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

// AddReaction idempotently adds actor's emoji reaction to a message visible to actor.
// Reactions are low-noise signals: they never wake Agents or advance read_seq.
func (s *Service) AddReaction(ctx context.Context, actor domain.Member, messageID, emoji string) ([]domain.ReactionGroup, error) {
	return s.setReaction(ctx, actor, messageID, emoji, true)
}

// RemoveReaction idempotently removes actor's emoji reaction from a message visible to actor.
func (s *Service) RemoveReaction(ctx context.Context, actor domain.Member, messageID, emoji string) ([]domain.ReactionGroup, error) {
	return s.setReaction(ctx, actor, messageID, emoji, false)
}

func (s *Service) setReaction(ctx context.Context, actor domain.Member, messageID, emoji string, add bool) ([]domain.ReactionGroup, error) {
	if actor.RetiredAt != nil {
		return nil, domain.NewError(domain.CodePermissionDenied, "retired member cannot react to messages")
	}
	if err := domain.ValidateReactionEmoji(emoji); err != nil {
		return nil, err
	}
	if strings.TrimSpace(messageID) == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "message_id is required")
	}

	var reactions []domain.ReactionGroup
	var events []domain.SpaceEvent

	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var convID string
		var deletedAt sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT conversation_id, deleted_at FROM messages WHERE id = ?`,
			messageID,
		).Scan(&convID, &deletedAt)
		if errors.Is(err, sql.ErrNoRows) || deletedAt.Valid {
			return domain.NewError(domain.CodeNotFound, "message not found")
		}
		if err != nil {
			return domain.WrapError(domain.CodeInternal, "query message for reaction", err)
		}
		if _, err := canAccessConversation(ctx, tx, actor, convID); err != nil {
			return err
		}

		var res sql.Result
		action := "added"
		if add {
			res, err = tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO message_reactions (message_id, emoji, member_id, created_at) VALUES (?, ?, ?, ?)`,
				messageID, emoji, actor.ID, store.FormatTime(time.Now().UTC()),
			)
		} else {
			action = "removed"
			res, err = tx.ExecContext(ctx,
				`DELETE FROM message_reactions WHERE message_id = ? AND emoji = ? AND member_id = ?`,
				messageID, emoji, actor.ID,
			)
		}
		if err != nil {
			return domain.WrapError(domain.CodeInternal, "update message reaction", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "message.reaction_changed", map[string]any{
				"message_id":      messageID,
				"conversation_id": convID,
				"emoji":           emoji,
				"member_id":       actor.ID,
				"action":          action,
			})
			if err != nil {
				return err
			}
			events = append(events, ev)
		}

		byMsg, err := queryReactionsByMessages(ctx, tx, actor.ID, []string{messageID})
		if err != nil {
			return err
		}
		reactions = byMsg[messageID]
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(events) > 0 {
		s.dispatchPostCommit(events, nil)
	}
	if reactions == nil {
		reactions = []domain.ReactionGroup{}
	}
	return reactions, nil
}

// loadMessageReactions populates Message.Reactions in one batch query.
func (s *Service) loadMessageReactions(ctx context.Context, viewerID string, msgs []domain.Message) {
	if len(msgs) == 0 {
		return
	}
	ids := make([]string, len(msgs))
	for i := range msgs {
		ids[i] = msgs[i].ID
	}
	byMsg, err := queryReactionsByMessages(ctx, s.store.ReadDB(), viewerID, ids)
	if err != nil {
		return
	}
	for i := range msgs {
		if groups := byMsg[msgs[i].ID]; len(groups) > 0 {
			msgs[i].Reactions = groups
		}
	}
}

func queryReactionsByMessages(ctx context.Context, q queryer, viewerID string, messageIDs []string) (map[string][]domain.ReactionGroup, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(messageIDs))
	args := make([]any, len(messageIDs))
	for i, id := range messageIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := q.QueryContext(ctx,
		`SELECT message_id, emoji, member_id
		 FROM message_reactions
		 WHERE message_id IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY created_at ASC, emoji ASC, member_id ASC`,
		args...,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "query message reactions", err)
	}
	defer rows.Close()

	// Preserve each emoji's first-reaction order per message so chips do not jump on count changes.
	type msgAcc struct {
		order  []string
		groups map[string]*domain.ReactionGroup
	}
	acc := make(map[string]*msgAcc)
	for rows.Next() {
		var msgID, emoji, memberID string
		if err := rows.Scan(&msgID, &emoji, &memberID); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "scan message reaction", err)
		}
		m := acc[msgID]
		if m == nil {
			m = &msgAcc{groups: make(map[string]*domain.ReactionGroup)}
			acc[msgID] = m
		}
		g := m.groups[emoji]
		if g == nil {
			g = &domain.ReactionGroup{Emoji: emoji}
			m.groups[emoji] = g
			m.order = append(m.order, emoji)
		}
		g.Count++
		g.MemberIDs = append(g.MemberIDs, memberID)
		if memberID == viewerID {
			g.Reacted = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make(map[string][]domain.ReactionGroup, len(acc))
	for msgID, m := range acc {
		list := make([]domain.ReactionGroup, 0, len(m.order))
		for _, emoji := range m.order {
			list = append(list, *m.groups[emoji])
		}
		out[msgID] = list
	}
	return out, nil
}
