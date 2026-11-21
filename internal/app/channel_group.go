package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

const maxChannelGroupNameRunes = 40

func validateChannelGroupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxChannelGroupNameRunes {
		return "", domain.NewError(domain.CodeInvalidArgument, "group name must be 1-40 characters")
	}
	return name, nil
}

// ListChannelGroups returns the viewer's sidebar channel groups in display order.
func (s *Service) ListChannelGroups(ctx context.Context, viewer domain.Member) ([]domain.ChannelGroup, error) {
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT id, space_id, name, position, collapsed, created_at
		 FROM channel_groups
		 WHERE space_id = ? AND member_id = ?
		 ORDER BY position ASC, created_at ASC`,
		viewer.SpaceID, viewer.ID,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list channel groups", err)
	}
	defer rows.Close()

	groups := make([]domain.ChannelGroup, 0)
	byID := make(map[string]int)
	for rows.Next() {
		var g domain.ChannelGroup
		var collapsed int
		var createdAt string
		if err := rows.Scan(&g.ID, &g.SpaceID, &g.Name, &g.Position, &collapsed, &createdAt); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "scan channel group", err)
		}
		g.Collapsed = collapsed == 1
		g.CreatedAt = store.ParseTime(createdAt)
		g.ConversationIDs = make([]string, 0)
		byID[g.ID] = len(groups)
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return groups, nil
	}

	itemRows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT cgi.group_id, cgi.conversation_id
		 FROM channel_group_items cgi
		 JOIN conversations c ON c.id = cgi.conversation_id
		 WHERE cgi.member_id = ? AND c.space_id = ? AND c.archived_at IS NULL
		 ORDER BY cgi.position ASC, lower(COALESCE(c.slug, '')) ASC`,
		viewer.ID, viewer.SpaceID,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list channel group items", err)
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var groupID, convID string
		if err := itemRows.Scan(&groupID, &convID); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "scan channel group item", err)
		}
		if idx, ok := byID[groupID]; ok {
			groups[idx].ConversationIDs = append(groups[idx].ConversationIDs, convID)
		}
	}
	return groups, itemRows.Err()
}

func (s *Service) CreateChannelGroup(ctx context.Context, actor domain.Member, name string) (domain.ChannelGroup, error) {
	clean, err := validateChannelGroupName(name)
	if err != nil {
		return domain.ChannelGroup{}, err
	}
	now := time.Now().UTC()
	var g domain.ChannelGroup
	var events []domain.SpaceEvent
	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var nextPos int
		_ = tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(position), -1) + 1 FROM channel_groups WHERE space_id = ? AND member_id = ?`,
			actor.SpaceID, actor.ID,
		).Scan(&nextPos)

		g = domain.ChannelGroup{
			ID:              uuid.NewString(),
			SpaceID:         actor.SpaceID,
			Name:            clean,
			Position:        nextPos,
			Collapsed:       false,
			ConversationIDs: []string{},
			CreatedAt:       now,
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO channel_groups (id, space_id, member_id, name, position, collapsed, created_at)
			 VALUES (?, ?, ?, ?, ?, 0, ?)`,
			g.ID, g.SpaceID, actor.ID, g.Name, g.Position, store.FormatTime(now),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "insert channel group", err)
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "channel_groups.changed", map[string]any{
			"member_id": actor.ID,
			"group_id":  g.ID,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return domain.ChannelGroup{}, err
	}
	s.dispatchPostCommit(events, nil)
	return g, nil
}

type UpdateChannelGroupInput struct {
	Name      *string
	Collapsed *bool
	Position  *int
}

func (s *Service) UpdateChannelGroup(ctx context.Context, actor domain.Member, groupID string, in UpdateChannelGroupInput) (domain.ChannelGroup, error) {
	var updated domain.ChannelGroup
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var name, createdAt string
		var pos, collapsed int
		err := tx.QueryRowContext(ctx,
			`SELECT name, position, collapsed, created_at FROM channel_groups WHERE id = ? AND space_id = ? AND member_id = ?`,
			groupID, actor.SpaceID, actor.ID,
		).Scan(&name, &pos, &collapsed, &createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewError(domain.CodeNotFound, "channel group not found")
		}
		if err != nil {
			return domain.WrapError(domain.CodeInternal, "query channel group", err)
		}
		if in.Name != nil {
			clean, err := validateChannelGroupName(*in.Name)
			if err != nil {
				return err
			}
			name = clean
		}
		if in.Collapsed != nil {
			collapsed = boolToInt(*in.Collapsed)
		}
		if in.Position != nil && *in.Position >= 0 {
			pos = *in.Position
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE channel_groups SET name = ?, position = ?, collapsed = ? WHERE id = ?`,
			name, pos, collapsed, groupID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "update channel group", err)
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "channel_groups.changed", map[string]any{
			"member_id": actor.ID,
			"group_id":  groupID,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		updated = domain.ChannelGroup{
			ID:              groupID,
			SpaceID:         actor.SpaceID,
			Name:            name,
			Position:        pos,
			Collapsed:       collapsed == 1,
			ConversationIDs: []string{},
			CreatedAt:       store.ParseTime(createdAt),
		}
		return nil
	})
	if err != nil {
		return domain.ChannelGroup{}, err
	}
	s.dispatchPostCommit(events, nil)
	return updated, nil
}

func (s *Service) DeleteChannelGroup(ctx context.Context, actor domain.Member, groupID string) error {
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM channel_groups WHERE id = ? AND space_id = ? AND member_id = ?`,
			groupID, actor.SpaceID, actor.ID,
		)
		if err != nil {
			return domain.WrapError(domain.CodeInternal, "delete channel group", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return domain.NewError(domain.CodeNotFound, "channel group not found")
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "channel_groups.changed", map[string]any{
			"member_id": actor.ID,
			"group_id":  groupID,
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

// SetConversationGroup moves a channel into groupID (or back to ungrouped when groupID is empty).
func (s *Service) SetConversationGroup(ctx context.Context, actor domain.Member, convID, groupID string) error {
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := canAccessConversation(ctx, tx, actor, convID)
		if err != nil {
			return err
		}
		if c.Kind != domain.ConversationPublic && c.Kind != domain.ConversationPrivate {
			return domain.NewError(domain.CodeInvalidArgument, "only channels can be placed in a group")
		}
		if groupID == "" {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM channel_group_items WHERE member_id = ? AND conversation_id = ?`,
				actor.ID, c.ID,
			); err != nil {
				return domain.WrapError(domain.CodeInternal, "ungroup channel", err)
			}
		} else {
			var exists int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM channel_groups WHERE id = ? AND space_id = ? AND member_id = ?`,
				groupID, actor.SpaceID, actor.ID,
			).Scan(&exists); err != nil {
				return domain.WrapError(domain.CodeInternal, "check channel group", err)
			}
			if exists == 0 {
				return domain.NewError(domain.CodeNotFound, "channel group not found")
			}
			var nextPos int
			_ = tx.QueryRowContext(ctx,
				`SELECT COALESCE(MAX(position), -1) + 1 FROM channel_group_items WHERE group_id = ?`,
				groupID,
			).Scan(&nextPos)
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO channel_group_items (group_id, member_id, conversation_id, position)
				 VALUES (?, ?, ?, ?)
				 ON CONFLICT(member_id, conversation_id) DO UPDATE SET
				   group_id = excluded.group_id,
				   position = excluded.position`,
				groupID, actor.ID, c.ID, nextPos,
			); err != nil {
				return domain.WrapError(domain.CodeInternal, "move channel to group", err)
			}
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "channel_groups.changed", map[string]any{
			"member_id":       actor.ID,
			"conversation_id": c.ID,
			"group_id":        groupID,
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
