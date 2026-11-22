package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
	"github.com/abcdlsj/sumi/internal/store"
)

type CreateAgentInput struct {
	DisplayName          string // Empty means auto-assign from the Agent name pool.
	RoleText             string
	ComputerID           string
	DriverKind           domain.DriverKind
	DriverConfig         domain.DriverConfig
	SourceConversationID string // Optional: emits an agent_created Action Message in this conversation.
	ActionCardMessageID  string // Optional: atomically marks this pending agent.create Action Card as executed.
}

func (s *Service) CreateAgent(ctx context.Context, actor domain.Member, in CreateAgentInput) (domain.Member, error) {
	ok, err := hasPermission(ctx, s.store.ReadDB(), actor, domain.PermAgentCreate)
	if err != nil {
		return domain.Member{}, err
	}
	if !ok {
		return domain.Member{}, domain.NewError(domain.CodePermissionDenied, "missing agent.create permission")
	}
	// An empty display name is auto-assigned inside the write transaction so the
	// pick sees every member of the Space.
	if in.DisplayName != "" {
		if err := domain.ValidateDisplayName(in.DisplayName); err != nil {
			return domain.Member{}, err
		}
	}
	cfg, err := domain.NormalizeDriverConfig(in.DriverKind, in.DriverConfig)
	if err != nil {
		return domain.Member{}, err
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return domain.Member{}, domain.WrapError(domain.CodeInternal, "encode driver config", err)
	}

	now := time.Now().UTC()
	memberID := uuid.NewString()
	var created domain.Member
	var events []domain.SpaceEvent
	var compOnline bool

	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if in.DisplayName == "" {
			name, err := pickAgentDisplayNameTx(ctx, tx, actor.SpaceID)
			if err != nil {
				return err
			}
			in.DisplayName = name
		}
		compStatus, err := placeableComputerStatusTx(ctx, tx, actor, in.ComputerID)
		if err != nil {
			return err
		}
		compOnline = compStatus == "online"
		rtState := domain.RuntimeOffline
		if compOnline {
			rtState = domain.RuntimeIdle
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO members (id, space_id, user_id, kind, display_name, access_level, created_at)
			 VALUES (?, ?, NULL, 'agent', ?, 'member', ?)`,
			memberID, actor.SpaceID, in.DisplayName, store.FormatTime(now),
		); err != nil {
			if store.IsUniqueViolation(err) {
				return domain.NewError(domain.CodeConflict, "display_name already taken in this space")
			}
			return domain.WrapError(domain.CodeInternal, "insert agent member", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agents (member_id, space_id, computer_id, role_text, driver_kind, driver_config_json, runtime_state, runtime_state_updated_at, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			memberID, actor.SpaceID, in.ComputerID, in.RoleText, string(in.DriverKind), string(cfgJSON), string(rtState), store.FormatTime(now), store.FormatTime(now),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "insert agent", err)
		}

		// Auto-join #general if present and active so the agent is reachable in the default space channel.
		var generalID string
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM conversations WHERE space_id = ? AND kind = 'public' AND slug = 'general' AND archived_at IS NULL`,
			actor.SpaceID,
		).Scan(&generalID); err == nil {
			_, _ = tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
				generalID, actor.SpaceID, memberID, store.FormatTime(now),
			)
		}

		if in.ActionCardMessageID != "" {
			cardConvID, cardEv, err := markActionCardExecutedTx(
				ctx,
				tx,
				actor,
				in.ActionCardMessageID,
				domain.ActionCardAgentCreate,
				domain.AgentCreateCardResult{AgentID: memberID, DisplayName: in.DisplayName},
				now,
			)
			if err != nil {
				return err
			}
			events = append(events, cardEv)
			// Auto-join the newly created agent to the channel hosting the proposal card.
			if cardMeta, err := canAccessConversation(ctx, tx, actor, cardConvID); err == nil {
				if boundary, err := resolveVisibilityBoundary(ctx, tx, cardMeta); err == nil &&
					(boundary.Kind == domain.ConversationPublic || boundary.Kind == domain.ConversationPrivate) {
					_, _ = tx.ExecContext(ctx,
						`INSERT OR IGNORE INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
						boundary.ID, actor.SpaceID, memberID, store.FormatTime(now),
					)
				}
			}
		} else if in.SourceConversationID != "" {
			srcMeta, err := canAccessConversation(ctx, tx, actor, in.SourceConversationID)
			if err != nil {
				return err
			}
			if err := checkNotArchived(ctx, tx, srcMeta); err != nil {
				return err
			}
			// Add the newly created agent to the source channel so the creator can immediately collaborate with it there.
			if boundary, err := resolveVisibilityBoundary(ctx, tx, srcMeta); err == nil &&
				(boundary.Kind == domain.ConversationPublic || boundary.Kind == domain.ConversationPrivate) {
				_, _ = tx.ExecContext(ctx,
					`INSERT OR IGNORE INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
					boundary.ID, actor.SpaceID, memberID, store.FormatTime(now),
				)
			}
			_, ev, err := insertActionMessageTx(ctx, tx, actor, in.SourceConversationID, domain.ActionAgentCreated, map[string]any{
				"agent_member_id": memberID,
				"display_name":    in.DisplayName,
			})
			if err != nil {
				return err
			}
			events = append(events, ev)
		}

		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "agent.created", map[string]any{
			"member_id":   memberID,
			"computer_id": in.ComputerID,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)

		created = domain.Member{
			ID:          memberID,
			SpaceID:     actor.SpaceID,
			Kind:        domain.MemberKindAgent,
			DisplayName: in.DisplayName,
			AccessLevel: domain.AccessMember,
			CreatedAt:   now,
			Agent: &domain.AgentInfo{
				ComputerID:   in.ComputerID,
				RoleText:     in.RoleText,
				DriverKind:   in.DriverKind,
				DriverConfig: cfg.Redact(in.DriverKind),
				RuntimeState: rtState,
			},
		}
		return nil
	})
	if err != nil {
		return domain.Member{}, err
	}

	s.dispatchPostCommit(events, nil)
	if compOnline {
		s.notifier.SendControl(in.ComputerID, protocol.AgentControlFrame{
			AgentMemberID: memberID,
			Action:        "provision",
			Spec: &protocol.AssignedAgentSpec{
				MemberID:     memberID,
				SpaceID:      actor.SpaceID,
				DisplayName:  in.DisplayName,
				RoleText:     in.RoleText,
				DriverKind:   in.DriverKind,
				DriverConfig: cfg,
			},
		})
	}
	return created, nil
}

// UpdateAgentDriver replaces an Agent's driver config; an empty api_key keeps the stored one.
// The hosting Computer receives a fresh provision so it rebuilds the driver.
func (s *Service) UpdateAgentDriver(ctx context.Context, actor domain.Member, agentMemberID string, kind domain.DriverKind, in domain.DriverConfig) (domain.Member, error) {
	ok, err := hasPermission(ctx, s.store.ReadDB(), actor, domain.PermAgentCreate)
	if err != nil {
		return domain.Member{}, err
	}
	if !ok {
		return domain.Member{}, domain.NewError(domain.CodePermissionDenied, "missing agent.create permission")
	}
	var spec protocol.AssignedAgentSpec
	var computerID, compStatus string
	var events []domain.SpaceEvent
	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var compID, status sql.NullString
		row := tx.QueryRowContext(ctx,
			`SELECT `+agentSpecColumns+`, a.computer_id, c.connection_status
			 FROM agents a JOIN members m ON m.id = a.member_id
			 LEFT JOIN computers c ON c.id = a.computer_id
			 WHERE a.member_id = ? AND a.space_id = ? AND m.retired_at IS NULL`,
			agentMemberID, actor.SpaceID,
		)
		var dk, cfgJSON string
		if err := row.Scan(&spec.MemberID, &spec.SpaceID, &spec.DisplayName, &spec.RoleText, &dk, &cfgJSON, &compID, &status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.NewError(domain.CodeNotFound, "agent not found")
			}
			return domain.WrapError(domain.CodeInternal, "query agent", err)
		}
		computerID, compStatus = compID.String, status.String
		stored := domain.ParseDriverConfig(cfgJSON)
		if strings.TrimSpace(in.APIKey) == "" && domain.DriverKind(dk) == kind {
			in.APIKey = stored.APIKey
		}
		cfg, err := domain.NormalizeDriverConfig(kind, in)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return domain.WrapError(domain.CodeInternal, "encode driver config", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE agents SET driver_kind = ?, driver_config_json = ?, last_error_code = '', last_error_detail = '' WHERE member_id = ?`,
			string(kind), string(raw), agentMemberID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "update agent driver", err)
		}
		spec.DriverKind = kind
		spec.DriverConfig = cfg
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "agent.updated", map[string]any{
			"member_id": agentMemberID,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return domain.Member{}, err
	}
	s.dispatchPostCommit(events, nil)
	if computerID != "" && compStatus == "online" {
		s.notifier.SendControl(computerID, protocol.AgentControlFrame{
			AgentMemberID: agentMemberID,
			Action:        "provision",
			Spec:          &spec,
		})
	}
	return s.GetMember(ctx, actor.SpaceID, agentMemberID)
}

// GetMember returns one non-retired member with its Browser-safe agent projection.

func (s *Service) UpdateAgentRuntimeState(ctx context.Context, computerID, agentMemberID string, state domain.RuntimeState, focusConvID, errorCode, errorDetail string, activity *domain.AgentActivity) error {
	activity = domain.SanitizeActivity(activity)
	if state != domain.RuntimeWorking {
		activity = nil
	}
	if state == domain.RuntimeError {
		if errorCode == "" {
			errorCode = domain.AgentErrTurnFailed
		}
		errorDetail = domain.BoundErrorDetail(errorDetail)
	} else {
		errorCode, errorDetail = "", ""
	}
	now := time.Now().UTC()
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var spaceID string
		if err := tx.QueryRowContext(ctx,
			`SELECT space_id FROM agents WHERE member_id = ? AND computer_id = ?`,
			agentMemberID, computerID,
		).Scan(&spaceID); err != nil {
			return domain.NewError(domain.CodeNotFound, "agent not assigned to computer")
		}
		var focus any
		if focusConvID != "" {
			focus = focusConvID
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE agents SET runtime_state = ?, focus_conversation_id = ?, last_error_code = ?, last_error_detail = ?, runtime_state_updated_at = ? WHERE member_id = ?`,
			string(state), focus, errorCode, errorDetail, store.FormatTime(now), agentMemberID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "update agent runtime state", err)
		}
		ev, err := store.AppendEvent(ctx, tx, spaceID, "agent.state_changed", map[string]any{
			"member_id":             agentMemberID,
			"runtime_state":         state,
			"focus_conversation_id": focusConvID,
			"error_code":            errorCode,
			"error_detail":          errorDetail,
			"activity":              activity,
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
	s.activityMu.Lock()
	if activity != nil {
		s.activity[agentMemberID] = *activity
	} else {
		delete(s.activity, agentMemberID)
	}
	s.activityMu.Unlock()
	s.dispatchPostCommit(events, nil)
	return nil
}

func (s *Service) ControlAgent(ctx context.Context, actor domain.Member, agentMemberID, action string) error {
	if actor.AccessLevel != domain.AccessOwner && actor.AccessLevel != domain.AccessAdmin {
		return domain.NewError(domain.CodePermissionDenied, "only owner or admin can control agent runtime")
	}
	if action != "restart" && action != "reset_session" {
		return domain.NewError(domain.CodeInvalidArgument, "action must be restart or reset_session")
	}
	var compID string
	err := s.store.ReadDB().QueryRowContext(ctx,
		`SELECT computer_id FROM agents WHERE member_id = ? AND space_id = ?`,
		agentMemberID, actor.SpaceID,
	).Scan(&compID)
	if errors.Is(err, sql.ErrNoRows) || compID == "" {
		return domain.NewError(domain.CodeNotFound, "agent not found")
	}
	if err != nil {
		return domain.WrapError(domain.CodeInternal, "query agent computer", err)
	}
	s.notifier.SendControl(compID, protocol.AgentControlFrame{
		AgentMemberID: agentMemberID,
		Action:        action,
	})
	return nil
}

// UpdateSpace renames a Space and/or changes its accent; empty fields keep their value.

func placeableComputerStatusTx(ctx context.Context, tx *sql.Tx, actor domain.Member, computerID string) (string, error) {
	var status, ownerUserID string
	err := tx.QueryRowContext(ctx,
		`SELECT connection_status, owner_user_id FROM computers WHERE id = ? AND deleted_at IS NULL`, computerID,
	).Scan(&status, &ownerUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.NewError(domain.CodeNotFound, "computer not found")
	}
	if err != nil {
		return "", domain.WrapError(domain.CodeInternal, "query computer", err)
	}
	var allowed bool
	switch actor.Kind {
	case domain.MemberKindHuman:
		var userID string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(user_id, '') FROM members WHERE id = ?`, actor.ID).Scan(&userID); err != nil {
			return "", domain.WrapError(domain.CodeInternal, "query actor account", err)
		}
		allowed = userID != "" && userID == ownerUserID
	case domain.MemberKindAgent:
		var hostID string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(computer_id, '') FROM agents WHERE member_id = ?`, actor.ID).Scan(&hostID); err != nil {
			return "", domain.WrapError(domain.CodeInternal, "query actor computer", err)
		}
		allowed = hostID != "" && hostID == computerID
	}
	if !allowed {
		return "", domain.NewError(domain.CodePermissionDenied, "only the computer's owner can place agents on it")
	}
	return status, nil
}
