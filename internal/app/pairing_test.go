package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

func requireCode(t *testing.T, err error, want domain.Code) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != want {
		t.Fatalf("want error code %s, got %v", want, err)
	}
}

func TestPairingCreateConfirmPoll(t *testing.T) {
	svc, _, space, owner, _ := setupTestService(t)
	ctx := context.Background()

	ticket, err := svc.CreatePairing(ctx, "lin.local", "linux", "2.0.0")
	if err != nil {
		t.Fatalf("create pairing: %v", err)
	}
	if len(ticket.Code) != pairingCodeLen || strings.ContainsAny(ticket.Code, "0O1I") {
		t.Fatalf("unexpected code shape (len=%d)", len(ticket.Code))
	}
	if ticket.ExpiresAt.Sub(time.Now()) > pairingTTL {
		t.Fatalf("ttl exceeds %s", pairingTTL)
	}

	var stored int
	if err := svc.store.ReadDB().QueryRow(
		`SELECT COUNT(*) FROM computer_pairings WHERE code_hash = ? OR token_hash = ?`, ticket.Code, ticket.Secret,
	).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("plaintext code or secret stored (n=%d err=%v)", stored, err)
	}

	if _, err := svc.AuthenticateComputer(ctx, ticket.Secret); err == nil {
		t.Fatal("secret must not authenticate before confirmation")
	}
	poll, err := svc.PollPairing(ctx, ticket.ID, ticket.Secret)
	if err != nil || poll.Status != "pending" || poll.ComputerID != "" {
		t.Fatalf("pending poll = %+v err=%v", poll, err)
	}

	view, err := svc.ViewPairing(ctx, ticket.ID, strings.ToLower(ticket.Code))
	if err != nil || view.Hostname != "lin.local" || view.OS != "linux" || view.Status != "pending" {
		t.Fatalf("view = %+v err=%v", view, err)
	}

	comp, err := svc.ConfirmPairing(ctx, owner, ticket.ID, ticket.Code, "")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if comp.Name != "lin.local" || comp.SpaceID != space.ID {
		t.Fatalf("computer = %+v", comp)
	}

	poll, err = svc.PollPairing(ctx, ticket.ID, ticket.Secret)
	if err != nil || poll.Status != "confirmed" || poll.ComputerID != comp.ID || poll.SpaceSlug != space.Slug {
		t.Fatalf("confirmed poll = %+v err=%v", poll, err)
	}
	authed, err := svc.AuthenticateComputer(ctx, ticket.Secret)
	if err != nil || authed.ID != comp.ID {
		t.Fatalf("secret should authenticate as computer %s: got %s err=%v", comp.ID, authed.ID, err)
	}

	_, err = svc.ConfirmPairing(ctx, owner, ticket.ID, ticket.Code, "again")
	requireCode(t, err, domain.CodeConflict)
	comps, _ := svc.ListComputers(ctx, space.ID)
	if len(comps) != 2 {
		t.Fatalf("double confirm created computers: %d", len(comps))
	}
}

func TestPairingRejectsWrongCodeSecretAndMissing(t *testing.T) {
	svc, _, _, owner, _ := setupTestService(t)
	ctx := context.Background()
	ticket, err := svc.CreatePairing(ctx, "mac.local", "macos", "")
	if err != nil {
		t.Fatalf("create pairing: %v", err)
	}
	wrong := "ZZZZZZ"
	if wrong == ticket.Code {
		wrong = "YYYYYY"
	}

	_, err = svc.ViewPairing(ctx, ticket.ID, wrong)
	requireCode(t, err, domain.CodeNotFound)
	_, err = svc.ViewPairing(ctx, ticket.ID, "")
	requireCode(t, err, domain.CodeNotFound)
	_, err = svc.ViewPairing(ctx, "missing", ticket.Code)
	requireCode(t, err, domain.CodeNotFound)

	_, err = svc.ConfirmPairing(ctx, owner, ticket.ID, wrong, "")
	requireCode(t, err, domain.CodeNotFound)
	_, err = svc.ConfirmPairing(ctx, owner, "missing", ticket.Code, "")
	requireCode(t, err, domain.CodeNotFound)

	_, err = svc.PollPairing(ctx, ticket.ID, "not-the-secret")
	requireCode(t, err, domain.CodeNotFound)
	_, err = svc.PollPairing(ctx, ticket.ID, "")
	requireCode(t, err, domain.CodeNotFound)
}

func TestPairingExpired(t *testing.T) {
	svc, _, _, owner, _ := setupTestService(t)
	ctx := context.Background()
	ticket, err := svc.CreatePairing(ctx, "mac.local", "macos", "")
	if err != nil {
		t.Fatalf("create pairing: %v", err)
	}
	past := store.FormatTime(time.Now().UTC().Add(-time.Second))
	if err := svc.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE computer_pairings SET expires_at = ? WHERE id = ?`, past, ticket.ID)
		return err
	}); err != nil {
		t.Fatalf("expire pairing: %v", err)
	}

	_, err = svc.ConfirmPairing(ctx, owner, ticket.ID, ticket.Code, "")
	requireCode(t, err, domain.CodeExpired)
	poll, err := svc.PollPairing(ctx, ticket.ID, ticket.Secret)
	if err != nil || poll.Status != "expired" {
		t.Fatalf("poll = %+v err=%v", poll, err)
	}
	if _, err := svc.AuthenticateComputer(ctx, ticket.Secret); err == nil {
		t.Fatal("expired secret must not authenticate")
	}
}

func TestPairingConfirmRequiresOwnerOrAdmin(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()
	ticket, err := svc.CreatePairing(ctx, "mac.local", "macos", "")
	if err != nil {
		t.Fatalf("create pairing: %v", err)
	}
	agent, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", ComputerID: comp.ID, DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	_, err = svc.ConfirmPairing(ctx, agent, ticket.ID, ticket.Code, "")
	requireCode(t, err, domain.CodePermissionDenied)

	plain := owner
	plain.AccessLevel = domain.AccessMember
	_, err = svc.ConfirmPairing(ctx, plain, ticket.ID, ticket.Code, "")
	requireCode(t, err, domain.CodePermissionDenied)

	poll, err := svc.PollPairing(ctx, ticket.ID, ticket.Secret)
	if err != nil || poll.Status != "pending" {
		t.Fatalf("poll = %+v err=%v", poll, err)
	}
}

func TestCreatePairingValidatesInput(t *testing.T) {
	svc, _, _, _, _ := setupTestService(t)
	ctx := context.Background()
	for _, tc := range []struct{ host, os string }{
		{"mac.local", "windows"},
		{"", "macos"},
		{strings.Repeat("h", maxHostnameLen+1), "linux"},
	} {
		_, err := svc.CreatePairing(ctx, tc.host, tc.os, "")
		requireCode(t, err, domain.CodeInvalidArgument)
	}
}
