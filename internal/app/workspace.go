package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/abcdlsj/sumi/internal/domain"
)

func (s *Service) resolveAgentWorkspaceTarget(ctx context.Context, actor domain.Member, agentID string) (string, error) {
	if actor.RetiredAt != nil || actor.Kind != domain.MemberKindHuman {
		return "", domain.NewError(domain.CodePermissionDenied, "only active human members can view agent workspaces")
	}
	if s.workspace == nil {
		return "", domain.NewError(domain.CodeUnavailable, "workspace viewer is unavailable")
	}
	var computerID sql.NullString
	err := s.store.ReadDB().QueryRowContext(ctx,
		`SELECT a.computer_id
		 FROM agents a JOIN members m ON m.id = a.member_id
		 WHERE a.member_id = ? AND m.space_id = ? AND m.retired_at IS NULL`,
		agentID, actor.SpaceID,
	).Scan(&computerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.NewError(domain.CodeNotFound, "agent not found")
	}
	if err != nil {
		return "", domain.WrapError(domain.CodeInternal, "query agent computer", err)
	}
	if !computerID.Valid || computerID.String == "" {
		return "", domain.NewError(domain.CodeUnavailable, "agent is not assigned to a computer")
	}
	return computerID.String, nil
}

func (s *Service) ListAgentWorkspace(ctx context.Context, actor domain.Member, agentID, relPath string, includeHidden bool) (*domain.WorkspaceListing, error) {
	computerID, err := s.resolveAgentWorkspaceTarget(ctx, actor, agentID)
	if err != nil {
		return nil, err
	}
	return s.workspace.ListWorkspace(ctx, computerID, agentID, relPath, includeHidden)
}

func (s *Service) ReadAgentWorkspaceFile(ctx context.Context, actor domain.Member, agentID, relPath string) (*domain.WorkspaceFileContent, error) {
	computerID, err := s.resolveAgentWorkspaceTarget(ctx, actor, agentID)
	if err != nil {
		return nil, err
	}
	return s.workspace.ReadWorkspaceFile(ctx, computerID, agentID, relPath)
}
