package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

const (
	pairingTTL          = 10 * time.Minute
	pairingCodeLen      = 6
	pairingCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	maxHostnameLen      = 253
	maxDaemonVersionLen = 64
)

// PairingTicket is returned once to the requesting Computer; Code and Secret are never stored in plaintext.
type PairingTicket struct {
	ID        string
	Code      string
	Secret    string
	ExpiresAt time.Time
}

type PairingView struct {
	ID        string    `json:"id"`
	Hostname  string    `json:"hostname"`
	OS        string    `json:"os"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
}

type PairingPoll struct {
	Status     string `json:"status"`
	SpaceSlug  string `json:"space_slug,omitempty"`
	ComputerID string `json:"computer_id,omitempty"`
}

type pairingRow struct {
	id, codeHash, tokenHash, hostname, os, daemonVersion, status string
	expiresAt                                                    time.Time
	computerID, spaceID                                          sql.NullString
}

func (p pairingRow) effectiveStatus(now time.Time) string {
	if p.status == "pending" && !now.Before(p.expiresAt) {
		return "expired"
	}
	return p.status
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func loadPairing(ctx context.Context, q rowQuerier, id string) (pairingRow, error) {
	var p pairingRow
	var expiresAt string
	err := q.QueryRowContext(ctx,
		`SELECT id, code_hash, token_hash, hostname, os, daemon_version, status, expires_at, computer_id, space_id
		 FROM computer_pairings WHERE id = ?`, id,
	).Scan(&p.id, &p.codeHash, &p.tokenHash, &p.hostname, &p.os, &p.daemonVersion, &p.status, &expiresAt, &p.computerID, &p.spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return pairingRow{}, pairingNotFound()
	}
	if err != nil {
		return pairingRow{}, domain.WrapError(domain.CodeInternal, "query pairing", err)
	}
	p.expiresAt = store.ParseTime(expiresAt)
	return p, nil
}

func pairingNotFound() error {
	return domain.NewError(domain.CodeNotFound, "pairing not found")
}

func hashMatches(stored, raw string) bool {
	return raw != "" && subtle.ConstantTimeCompare([]byte(stored), []byte(hashToken(raw))) == 1
}

func normalizePairingCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func randomPairingCode() (string, error) {
	max := big.NewInt(int64(len(pairingCodeAlphabet)))
	var b strings.Builder
	for i := 0; i < pairingCodeLen; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(pairingCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// CreatePairing is unauthenticated: the caller proves ownership later only by holding Secret.
func (s *Service) CreatePairing(ctx context.Context, hostname, osName, daemonVersion string) (PairingTicket, error) {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" || len(hostname) > maxHostnameLen {
		return PairingTicket{}, domain.NewError(domain.CodeInvalidArgument, "hostname is required and must be at most 253 characters")
	}
	if osName != "macos" && osName != "linux" {
		return PairingTicket{}, domain.NewError(domain.CodeInvalidArgument, "os must be macos or linux")
	}
	daemonVersion = strings.TrimSpace(daemonVersion)
	if len(daemonVersion) > maxDaemonVersionLen {
		return PairingTicket{}, domain.NewError(domain.CodeInvalidArgument, "daemon_version is too long")
	}
	code, err := randomPairingCode()
	if err != nil {
		return PairingTicket{}, domain.WrapError(domain.CodeInternal, "generate pairing code", err)
	}
	secret, err := randomToken(32)
	if err != nil {
		return PairingTicket{}, domain.WrapError(domain.CodeInternal, "generate pairing secret", err)
	}
	now := time.Now().UTC()
	t := PairingTicket{ID: uuid.NewString(), Code: code, Secret: secret, ExpiresAt: now.Add(pairingTTL)}
	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO computer_pairings (id, code_hash, token_hash, hostname, os, daemon_version, status, expires_at, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
			t.ID, hashToken(code), hashToken(secret), hostname, osName, daemonVersion,
			store.FormatTime(t.ExpiresAt), store.FormatTime(now),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "insert pairing", err)
		}
		return nil
	})
	if err != nil {
		return PairingTicket{}, err
	}
	return t, nil
}

// ViewPairing requires the pairing code; a mismatched code is indistinguishable from a missing pairing.
func (s *Service) ViewPairing(ctx context.Context, id, code string) (PairingView, error) {
	p, err := loadPairing(ctx, s.store.ReadDB(), id)
	if err != nil {
		return PairingView{}, err
	}
	if !hashMatches(p.codeHash, normalizePairingCode(code)) {
		return PairingView{}, pairingNotFound()
	}
	return PairingView{
		ID:        p.id,
		Hostname:  p.hostname,
		OS:        p.os,
		Status:    p.effectiveStatus(time.Now().UTC()),
		ExpiresAt: p.expiresAt,
	}, nil
}

// ConfirmPairing binds a pending pairing to the actor's Space. The new Computer reuses the
// pairing's token_hash so the Secret held by the requester becomes its Computer token.
func (s *Service) ConfirmPairing(ctx context.Context, actor domain.Member, id, code, name string) (domain.Computer, error) {
	if actor.Kind != domain.MemberKindHuman || (actor.AccessLevel != domain.AccessOwner && actor.AccessLevel != domain.AccessAdmin) {
		return domain.Computer{}, domain.NewError(domain.CodePermissionDenied, "only owner or admin can confirm a pairing")
	}
	code = normalizePairingCode(code)
	var comp domain.Computer
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		p, err := loadPairing(ctx, tx, id)
		if err != nil {
			return err
		}
		if !hashMatches(p.codeHash, code) {
			return pairingNotFound()
		}
		now := time.Now().UTC()
		switch p.effectiveStatus(now) {
		case "pending":
		case "expired":
			return domain.NewError(domain.CodeExpired, "pairing expired")
		default:
			return domain.NewError(domain.CodeConflict, "pairing already confirmed")
		}
		compName := strings.TrimSpace(name)
		if compName == "" {
			compName = p.hostname
		}
		comp = domain.Computer{
			ID:               uuid.NewString(),
			SpaceID:          actor.SpaceID,
			Name:             compName,
			Hostname:         p.hostname,
			OS:               p.os,
			ConnectionStatus: "offline",
			DaemonVersion:    p.daemonVersion,
			ACPProviders:     []string{},
			CreatedAt:        now,
		}
		ev, err := insertComputerTx(ctx, tx, comp, p.tokenHash)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE computer_pairings SET status = 'confirmed', computer_id = ?, space_id = ?, confirmed_at = ?
			 WHERE id = ? AND status = 'pending'`,
			comp.ID, actor.SpaceID, store.FormatTime(now), p.id,
		)
		if err != nil {
			return domain.WrapError(domain.CodeInternal, "confirm pairing", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return domain.NewError(domain.CodeConflict, "pairing already confirmed")
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return domain.Computer{}, err
	}
	s.dispatchPostCommit(events, nil)
	return comp, nil
}

// PollPairing authenticates by the pairing Secret; a wrong Secret is indistinguishable from a missing pairing.
func (s *Service) PollPairing(ctx context.Context, id, secret string) (PairingPoll, error) {
	p, err := loadPairing(ctx, s.store.ReadDB(), id)
	if err != nil {
		return PairingPoll{}, err
	}
	if !hashMatches(p.tokenHash, secret) {
		return PairingPoll{}, pairingNotFound()
	}
	out := PairingPoll{Status: p.effectiveStatus(time.Now().UTC())}
	if out.Status == "confirmed" {
		out.ComputerID = p.computerID.String
		if err := s.store.ReadDB().QueryRowContext(ctx,
			`SELECT slug FROM spaces WHERE id = ?`, p.spaceID.String,
		).Scan(&out.SpaceSlug); err != nil {
			return PairingPoll{}, domain.WrapError(domain.CodeInternal, "query pairing space", err)
		}
	}
	return out, nil
}
