package app

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

func TestCreateAgentAutoAssignsPoolNames(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	first, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		RoleText:   "Generalist",
		ComputerID: comp.ID,
		DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent without name: %v", err)
	}
	if first.DisplayName != agentNamePool[0] {
		t.Fatalf("expected first pool name %q, got %q", agentNamePool[0], first.DisplayName)
	}

	// An explicitly named agent takes its name, so the auto picker skips it.
	if _, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: agentNamePool[1],
		RoleText:    "Reviewer",
		ComputerID:  comp.ID,
		DriverKind:  domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	}); err != nil {
		t.Fatalf("create named agent: %v", err)
	}

	third, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		RoleText:   "Writer",
		ComputerID: comp.ID,
		DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create third agent: %v", err)
	}
	if third.DisplayName != agentNamePool[2] {
		t.Fatalf("expected %q after skipping taken names, got %q", agentNamePool[2], third.DisplayName)
	}

	// The Space owner alone must not hide the owner's own member name.
	names := map[string]bool{owner.DisplayName: true}
	for _, m := range []domain.Member{first, third} {
		if names[m.DisplayName] {
			t.Fatalf("duplicate display name %q", m.DisplayName)
		}
		names[m.DisplayName] = true
	}
	if err := domain.ValidateDisplayName(first.DisplayName); err != nil {
		t.Fatalf("auto name %q invalid: %v", first.DisplayName, err)
	}
}

func TestCreateAgentFallsBackToNumberedNamesWhenPoolExhausted(t *testing.T) {
	svc, _, space, owner, comp := setupTestService(t)
	ctx := context.Background()

	// Occupy every pool name, including a retired member to prove it still counts.
	created := store.FormatTime(time.Now().UTC())
	if err := svc.Store().WithWriteTx(ctx, func(tx *sql.Tx) error {
		for i, name := range agentNamePool {
			var retired any
			if i == 0 {
				retired = created
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO members (id, space_id, user_id, kind, display_name, access_level, created_at, retired_at)
				 VALUES (?, ?, NULL, 'agent', ?, 'member', ?, ?)`,
				fmt.Sprintf("filler-%d", i), space.ID, name, created, retired,
			)
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("fill pool: %v", err)
	}

	for _, want := range []string{"Agent1", "Agent2"} {
		created, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
			RoleText:   "Generalist",
			ComputerID: comp.ID,
			DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
		})
		if err != nil {
			t.Fatalf("create %s: %v", want, err)
		}
		if created.DisplayName != want {
			t.Fatalf("expected %q, got %q", want, created.DisplayName)
		}
	}
}
