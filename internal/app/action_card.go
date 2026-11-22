package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

// ProposeAgentCreate posts an interactive agent.create Action Card in the target conversation
// instead of silently provisioning the agent. Still requires agent.create permission.
func (s *Service) ProposeAgentCreate(ctx context.Context, actor domain.Member, in CreateAgentInput) (domain.Message, error) {
	ok, err := hasPermission(ctx, s.store.ReadDB(), actor, domain.PermAgentCreate)
	if err != nil {
		return domain.Message{}, err
	}
	if !ok {
		return domain.Message{}, domain.NewError(domain.CodePermissionDenied, "missing agent.create permission")
	}
	name := strings.TrimSpace(in.DisplayName)
	if name != "" {
		if err := domain.ValidateDisplayName(name); err != nil {
			return domain.Message{}, err
		}
	}

	params := domain.AgentCreateCardParams{
		DisplayName: name,
		RoleText:    strings.TrimSpace(in.RoleText),
		ComputerID:  strings.TrimSpace(in.ComputerID),
	}
	// Preserve explicit driver suggestions without failing on incomplete fields or ever storing api_key.
	switch in.DriverKind {
	case domain.DriverACP:
		params.DriverKind = domain.DriverACP
		params.DriverConfig = in.DriverConfig.Redact(domain.DriverACP)
	case domain.DriverBuiltin:
		if strings.TrimSpace(in.DriverConfig.BaseURL) != "" || strings.TrimSpace(in.DriverConfig.Model) != "" {
			params.DriverKind = domain.DriverBuiltin
			redacted := in.DriverConfig.Redact(domain.DriverBuiltin)
			redacted.APIKeySet = nil
			params.DriverConfig = redacted
		}
	}
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return domain.Message{}, domain.WrapError(domain.CodeInternal, "encode action card params", err)
	}
	payload := domain.ActionCardPayload{
		Version:  1,
		CardType: domain.ActionCardAgentCreate,
		Status:   domain.ActionCardPending,
		Title:    "Create agent",
		Params:   paramsRaw,
	}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return domain.Message{}, domain.WrapError(domain.CodeInternal, "encode action card payload", err)
	}

	var msg domain.Message
	var events []domain.SpaceEvent

	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		targetConvID, err := resolveProposalConversationTx(ctx, tx, actor, in.SourceConversationID)
		if err != nil {
			return err
		}
		inserted, err := insertMessageTx(ctx, tx, targetConvID, actor, domain.MessageKindAction, "", domain.ActionActionCard, string(payloadRaw), false, nil)
		if err != nil {
			return err
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "message.created", map[string]any{
			"message_id":      inserted.ID,
			"conversation_id": targetConvID,
			"seq":             inserted.Seq,
			"kind":            "action",
			"action_kind":     domain.ActionActionCard,
		})
		if err != nil {
			return err
		}
		msg = inserted
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return domain.Message{}, err
	}
	s.dispatchPostCommit(events, nil)
	return msg, nil
}

// resolveProposalConversationTx selects where an agent's confirmation card should appear:
// explicit source_conversation_id, then the agent's current focus conversation, then active #general.
func resolveProposalConversationTx(ctx context.Context, tx *sql.Tx, actor domain.Member, explicitConvID string) (string, error) {
	if explicitConvID != "" {
		meta, err := canAccessConversation(ctx, tx, actor, explicitConvID)
		if err != nil {
			return "", err
		}
		if err := checkNotArchived(ctx, tx, meta); err != nil {
			return "", err
		}
		return explicitConvID, nil
	}
	if actor.Kind == domain.MemberKindAgent {
		var focusID sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT focus_conversation_id FROM agents WHERE member_id = ?`, actor.ID).Scan(&focusID); err == nil && focusID.String != "" {
			if meta, err := canAccessConversation(ctx, tx, actor, focusID.String); err == nil {
				if err := checkNotArchived(ctx, tx, meta); err == nil {
					return focusID.String, nil
				}
			}
		}
	}
	var generalID string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM conversations WHERE space_id = ? AND kind = 'public' AND slug = 'general' AND archived_at IS NULL`,
		actor.SpaceID,
	).Scan(&generalID); err == nil && generalID != "" {
		if _, err := canAccessConversation(ctx, tx, actor, generalID); err == nil {
			return generalID, nil
		}
	}
	return "", domain.NewError(domain.CodeInvalidArgument, "source_conversation_id is required to post a confirmation card")
}

// markActionCardExecutedTx atomically transitions a pending Action Card to executed inside the
// caller's write transaction, returning the card's conversation_id and a message.updated event.
func markActionCardExecutedTx(
	ctx context.Context,
	tx *sql.Tx,
	actor domain.Member,
	messageID string,
	expectedType domain.ActionCardType,
	result any,
	now time.Time,
) (string, domain.SpaceEvent, error) {
	var convID, payloadRaw string
	err := tx.QueryRowContext(ctx,
		`SELECT conversation_id, payload_json
		 FROM messages
		 WHERE id = ? AND space_id = ? AND kind = 'action' AND action_kind = ? AND deleted_at IS NULL`,
		messageID, actor.SpaceID, string(domain.ActionActionCard),
	).Scan(&convID, &payloadRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.SpaceEvent{}, domain.NewError(domain.CodeNotFound, "action card not found")
	}
	if err != nil {
		return "", domain.SpaceEvent{}, domain.WrapError(domain.CodeInternal, "load action card", err)
	}
	if _, err := canAccessConversation(ctx, tx, actor, convID); err != nil {
		return "", domain.SpaceEvent{}, err
	}
	var payload domain.ActionCardPayload
	if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
		return "", domain.SpaceEvent{}, domain.NewError(domain.CodeInternal, "corrupt action card payload")
	}
	if payload.CardType != expectedType {
		return "", domain.SpaceEvent{}, domain.NewError(domain.CodeInvalidArgument, "action card type mismatch")
	}
	if payload.Status != domain.ActionCardPending {
		return "", domain.SpaceEvent{}, domain.NewError(domain.CodeConflict, "action card is no longer pending")
	}
	resRaw, err := json.Marshal(result)
	if err != nil {
		return "", domain.SpaceEvent{}, domain.WrapError(domain.CodeInternal, "encode action card result", err)
	}
	payload.Status = domain.ActionCardExecuted
	payload.Result = resRaw
	payload.ExecutedByMemberID = actor.ID
	payload.ExecutedByName = actor.DisplayName
	payload.ExecutedAt = &now

	nextRaw, err := json.Marshal(payload)
	if err != nil {
		return "", domain.SpaceEvent{}, domain.WrapError(domain.CodeInternal, "encode updated action card", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET payload_json = ?, edited_at = ? WHERE id = ?`,
		string(nextRaw), store.FormatTime(now), messageID,
	); err != nil {
		return "", domain.SpaceEvent{}, domain.WrapError(domain.CodeInternal, "update action card", err)
	}
	ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "message.updated", map[string]any{
		"message_id":      messageID,
		"conversation_id": convID,
		"action_kind":     domain.ActionActionCard,
	})
	if err != nil {
		return "", domain.SpaceEvent{}, err
	}
	return convID, ev, nil
}

// DismissActionCard marks a pending Action Card as dismissed by a human owner/admin.
func (s *Service) DismissActionCard(ctx context.Context, actor domain.Member, messageID string) (domain.ActionCardPayload, error) {
	if actor.Kind != domain.MemberKindHuman || actor.AccessLevel == domain.AccessMember {
		return domain.ActionCardPayload{}, domain.NewError(domain.CodePermissionDenied, "owner or admin access required")
	}
	now := time.Now().UTC()
	var updated domain.ActionCardPayload
	var events []domain.SpaceEvent

	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var convID, payloadRaw string
		err := tx.QueryRowContext(ctx,
			`SELECT conversation_id, payload_json
			 FROM messages
			 WHERE id = ? AND space_id = ? AND kind = 'action' AND action_kind = ? AND deleted_at IS NULL`,
			messageID, actor.SpaceID, string(domain.ActionActionCard),
		).Scan(&convID, &payloadRaw)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewError(domain.CodeNotFound, "action card not found")
		}
		if err != nil {
			return domain.WrapError(domain.CodeInternal, "load action card", err)
		}
		if _, err := canAccessConversation(ctx, tx, actor, convID); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(payloadRaw), &updated); err != nil {
			return domain.NewError(domain.CodeInternal, "corrupt action card payload")
		}
		if updated.Status != domain.ActionCardPending {
			return domain.NewError(domain.CodeConflict, "action card is no longer pending")
		}
		updated.Status = domain.ActionCardDismissed
		updated.ExecutedByMemberID = actor.ID
		updated.ExecutedByName = actor.DisplayName
		updated.ExecutedAt = &now

		nextRaw, err := json.Marshal(updated)
		if err != nil {
			return domain.WrapError(domain.CodeInternal, "encode dismissed action card", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE messages SET payload_json = ?, edited_at = ? WHERE id = ?`,
			string(nextRaw), store.FormatTime(now), messageID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "dismiss action card", err)
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "message.updated", map[string]any{
			"message_id":      messageID,
			"conversation_id": convID,
			"action_kind":     domain.ActionActionCard,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return domain.ActionCardPayload{}, err
	}
	s.dispatchPostCommit(events, nil)
	return updated, nil
}
