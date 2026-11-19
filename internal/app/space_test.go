package app

import (
	"context"
	"errors"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestUpdateSpaceAccentIsGovernedAndValidated(t *testing.T) {
	svc, _, space, owner, comp := setupTestService(t)
	ctx := context.Background()

	if space.Accent != domain.DefaultSpaceAccent {
		t.Fatalf("new space accent = %q, want %q", space.Accent, domain.DefaultSpaceAccent)
	}

	var domErr *domain.Error
	if _, err := svc.UpdateSpace(ctx, owner, "", "neon"); !errors.As(err, &domErr) || domErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("unknown accent: want invalid_argument, got %v", err)
	}

	sp, err := svc.UpdateSpace(ctx, owner, "", "iris")
	if err != nil {
		t.Fatalf("update accent: %v", err)
	}
	if sp.Accent != "iris" || sp.Name != space.Name {
		t.Fatalf("update kept name %q and set accent %q", sp.Name, sp.Accent)
	}

	agent, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", RoleText: "Helper", ComputerID: comp.ID,
		DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := svc.UpdateSpace(ctx, agent, "Hijacked", ""); !errors.As(err, &domErr) || domErr.Code != domain.CodePermissionDenied {
		t.Fatalf("agent update: want permission_denied, got %v", err)
	}

	spaces, err := svc.ListUserSpaces(ctx, mustOwnerUserID(t, svc, owner))
	if err != nil || len(spaces) != 1 || spaces[0].Accent != "iris" {
		t.Fatalf("list spaces after update: %+v %v", spaces, err)
	}
}

func mustOwnerUserID(t *testing.T, svc *Service, owner domain.Member) string {
	t.Helper()
	var userID string
	if err := svc.store.ReadDB().QueryRow(`SELECT user_id FROM members WHERE id = ?`, owner.ID).Scan(&userID); err != nil {
		t.Fatalf("owner user id: %v", err)
	}
	return userID
}
