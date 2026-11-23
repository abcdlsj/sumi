package app

import (
	"context"
	"database/sql"

	"github.com/abcdlsj/sumi/internal/domain"
)

func isTaskTerminalTx(ctx context.Context, q queryer, target convMeta) (bool, error) {
	if target.Kind != domain.ConversationTask || target.TaskID == "" {
		return false, nil
	}
	var status string
	if err := q.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, target.TaskID).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, domain.WrapError(domain.CodeInternal, "query task status", err)
	}
	return domain.TaskStatus(status).IsTerminal(), nil
}

func computeAgentWakesTx(ctx context.Context, tx *sql.Tx, target convMeta, authorID string, mentionAll bool, mentionIDs []string) ([]wakeTarget, error) {
	boundary, err := resolveVisibilityBoundary(ctx, tx, target)
	if err != nil {
		return nil, err
	}
	if target.ArchivedAt != nil || boundary.ArchivedAt != nil {
		return nil, nil
	}
	taskTerminal, err := isTaskTerminalTx(ctx, tx, target)
	if err != nil {
		return nil, err
	}
	mentionedSet := make(map[string]struct{}, len(mentionIDs))
	for _, id := range mentionIDs {
		mentionedSet[id] = struct{}{}
	}

	// Candidate agents that are explicit members of this conversation boundary and have an online computer.
	rows, err := tx.QueryContext(ctx,
		`SELECT a.member_id, COALESCE(a.computer_id, ''), COALESCE(cs.notify_level, 'mentions'), COALESCE(cs.following, 0)
		 FROM agents a
		 JOIN members m ON m.id = a.member_id
		 JOIN conversation_members cm ON cm.conversation_id = ? AND cm.member_id = a.member_id
		 JOIN computers c ON c.id = a.computer_id AND c.connection_status = 'online'
		 LEFT JOIN conversation_states cs ON cs.conversation_id = ? AND cs.member_id = a.member_id
		 WHERE a.space_id = ? AND m.retired_at IS NULL AND a.member_id <> ?`,
		boundary.ID, target.ID, target.SpaceID, authorID,
	)
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
		case (target.Kind == domain.ConversationThread || (target.Kind == domain.ConversationTask && !taskTerminal)) && following == 1:
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
