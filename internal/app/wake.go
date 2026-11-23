package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

const (
	// DefaultAgentChainLimit caps consecutive agent-authored text messages without human input
	// in a shared conversation before passive wakes (following, channel_all) pause.
	DefaultAgentChainLimit = 10

	// DefaultAgentHardChainLimit escalates an unbroken agent-only streak in a shared conversation
	// to pause all agent-authored wakes including explicit @mentions until a human posts.
	DefaultAgentHardChainLimit = 20

	// DefaultAgentDMWindow and DefaultAgentDMWindowLimit throttle agent-to-agent DM ping-pong
	// via a sliding window because no human participates in an agent-to-agent DM to reset a streak.
	DefaultAgentDMWindow      = 15 * time.Minute
	DefaultAgentDMWindowLimit = 10

	wakePauseTierPassive     = "passive"
	wakePauseTierAll         = "all"
	wakePauseTierDMRateLimit = "dm_rate_limit"
)

type wakePolicy struct {
	taskTerminal bool
	softPaused   bool
	hardPaused   bool
	dmLimited    bool
}

type wakeOutcome struct {
	wakes             []wakeTarget
	suppressedPassive bool
	suppressedHard    bool
	suppressedDM      bool
}

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

// consecutiveAgentTextCount counts leading agent-authored text messages from the newest message
// backwards until a human text message is encountered, bounded at maxScan.
func consecutiveAgentTextCount(ctx context.Context, q queryer, convID string, maxScan int) (int, error) {
	if maxScan <= 0 {
		return 0, nil
	}
	rows, err := q.QueryContext(ctx,
		`SELECT m.kind
		 FROM messages msg
		 JOIN members m ON m.id = msg.author_member_id
		 WHERE msg.conversation_id = ? AND msg.kind = 'text' AND msg.deleted_at IS NULL
		 ORDER BY msg.seq DESC
		 LIMIT ?`,
		convID, maxScan,
	)
	if err != nil {
		return 0, domain.WrapError(domain.CodeInternal, "query recent text authors", err)
	}
	defer rows.Close()
	streak := 0
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return 0, err
		}
		if domain.MemberKind(kind) != domain.MemberKindAgent {
			break
		}
		streak++
	}
	return streak, rows.Err()
}

func lastHumanTextSeq(ctx context.Context, q queryer, convID string) (int64, error) {
	var seq int64
	if err := q.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(msg.seq), 0)
		 FROM messages msg
		 JOIN members m ON m.id = msg.author_member_id
		 WHERE msg.conversation_id = ? AND msg.kind = 'text' AND msg.deleted_at IS NULL AND m.kind <> 'agent'`,
		convID,
	).Scan(&seq); err != nil {
		return 0, domain.WrapError(domain.CodeInternal, "query last human text seq", err)
	}
	return seq, nil
}

func hasWakePausedNoticeSinceSeq(ctx context.Context, q queryer, convID string, sinceSeq int64, tier string) (bool, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT payload_json
		 FROM messages
		 WHERE conversation_id = ? AND kind = 'system' AND action_kind = ? AND seq > ? AND deleted_at IS NULL`,
		convID, string(domain.ActionWakePaused), sinceSeq,
	)
	if err != nil {
		return false, domain.WrapError(domain.CodeInternal, "query wake pause notices", err)
	}
	defer rows.Close()
	return matchWakePauseTier(rows, tier)
}

func hasWakePausedNoticeSinceTime(ctx context.Context, q queryer, convID string, cutoff time.Time, tier string) (bool, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT payload_json
		 FROM messages
		 WHERE conversation_id = ? AND kind = 'system' AND action_kind = ? AND created_at >= ? AND deleted_at IS NULL`,
		convID, string(domain.ActionWakePaused), store.FormatTime(cutoff),
	)
	if err != nil {
		return false, domain.WrapError(domain.CodeInternal, "query recent dm wake pause notices", err)
	}
	defer rows.Close()
	return matchWakePauseTier(rows, tier)
}

func matchWakePauseTier(rows *sql.Rows, tier string) (bool, error) {
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var payload struct {
			Tier string `json:"tier"`
		}
		if json.Unmarshal([]byte(raw), &payload) == nil && payload.Tier == tier {
			return true, nil
		}
	}
	return false, rows.Err()
}

// isAgentToAgentDM reports whether all members of a DM conversation are agents.
// Human-involved DMs are never throttled.
func isAgentToAgentDM(ctx context.Context, q queryer, convID string) (bool, error) {
	var total, agents int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN m.kind = 'agent' THEN 1 ELSE 0 END), 0)
		 FROM conversation_members cm
		 JOIN members m ON m.id = cm.member_id
		 WHERE cm.conversation_id = ?`,
		convID,
	).Scan(&total, &agents); err != nil {
		return false, domain.WrapError(domain.CodeInternal, "query dm member kinds", err)
	}
	return total >= 2 && total == agents, nil
}

func recentDMTextCount(ctx context.Context, q queryer, convID string, cutoff time.Time) (int, error) {
	var count int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*)
		 FROM messages
		 WHERE conversation_id = ? AND kind = 'text' AND deleted_at IS NULL AND created_at >= ?`,
		convID, store.FormatTime(cutoff),
	).Scan(&count); err != nil {
		return 0, domain.WrapError(domain.CodeInternal, "count recent dm text messages", err)
	}
	return count, nil
}

func isAgentDMWindowLimited(ctx context.Context, q queryer, convID string, now time.Time) (bool, error) {
	isA2A, err := isAgentToAgentDM(ctx, q, convID)
	if err != nil || !isA2A {
		return false, err
	}
	cnt, err := recentDMTextCount(ctx, q, convID, now.UTC().Add(-DefaultAgentDMWindow))
	if err != nil {
		return false, err
	}
	return cnt >= DefaultAgentDMWindowLimit, nil
}

func hasUnreadHumanMention(ctx context.Context, q queryer, convID, memberID string, readSeq int64) (bool, error) {
	var found int
	err := q.QueryRowContext(ctx,
		`SELECT 1
		 FROM messages m
		 JOIN members a ON a.id = m.author_member_id
		 LEFT JOIN message_mentions mm ON mm.message_id = m.id AND mm.member_id = ?
		 WHERE m.conversation_id = ? AND m.seq > ? AND m.deleted_at IS NULL
		   AND a.kind = 'human' AND (m.mention_all = 1 OR mm.member_id IS NOT NULL)
		 LIMIT 1`,
		memberID, convID, readSeq,
	).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, domain.WrapError(domain.CodeInternal, "query unread human mentions", err)
	}
	return found == 1, nil
}

func computeAgentWakesTx(ctx context.Context, tx *sql.Tx, target convMeta, authorID string, mentionAll bool, mentionIDs []string, policy wakePolicy) (wakeOutcome, error) {
	var out wakeOutcome
	boundary, err := resolveVisibilityBoundary(ctx, tx, target)
	if err != nil {
		return out, err
	}
	if target.ArchivedAt != nil || boundary.ArchivedAt != nil {
		return out, nil
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
		return out, domain.WrapError(domain.CodeInternal, "query wake agents", err)
	}
	defer rows.Close()

	for rows.Next() {
		var agentID, compID, notify string
		var following int
		if err := rows.Scan(&agentID, &compID, &notify, &following); err != nil {
			return out, err
		}
		_, directlyMentioned := mentionedSet[agentID]
		var reason string
		switch {
		case target.Kind == domain.ConversationDM:
			if policy.dmLimited {
				out.suppressedDM = true
			} else {
				reason = "dm"
			}
		case directlyMentioned || mentionAll:
			if policy.hardPaused {
				out.suppressedHard = true
			} else {
				reason = "mention"
			}
		case (target.Kind == domain.ConversationThread || (target.Kind == domain.ConversationTask && !policy.taskTerminal)) && following == 1:
			if policy.softPaused || policy.hardPaused {
				out.suppressedPassive = true
			} else {
				reason = "following"
			}
		case (target.Kind == domain.ConversationPublic || target.Kind == domain.ConversationPrivate) && notify == string(domain.NotifyAll):
			if policy.softPaused || policy.hardPaused {
				out.suppressedPassive = true
			} else {
				reason = "channel_all"
			}
		}
		if reason != "" {
			out.wakes = append(out.wakes, wakeTarget{
				computerID:     compID,
				agentMemberID:  agentID,
				conversationID: target.ID,
				reason:         reason,
			})
		}
	}
	return out, rows.Err()
}

// heldUnreadLimit bounds how many unread messages a held write returns; older ones are counted only.
