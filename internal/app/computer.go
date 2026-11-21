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

func (s *Service) PairComputerDirect(ctx context.Context, ownerUserID, name, hostname, osName string) (domain.Computer, string, error) {
	if ownerUserID == "" {
		return domain.Computer{}, "", domain.NewError(domain.CodeUnauthenticated, "sign in to pair a computer")
	}
	if osName != "macos" && osName != "linux" {
		return domain.Computer{}, "", domain.NewError(domain.CodeInvalidArgument, "os must be macos or linux")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = hostname
	}
	rawToken, err := randomToken(32)
	if err != nil {
		return domain.Computer{}, "", domain.WrapError(domain.CodeInternal, "generate computer token", err)
	}
	now := time.Now().UTC()
	comp := domain.Computer{
		ID:               uuid.NewString(),
		OwnerUserID:      ownerUserID,
		Name:             name,
		Hostname:         hostname,
		OS:               osName,
		ConnectionStatus: "offline",
		ACPProviders:     []string{},
		CreatedAt:        now,
	}
	var events []domain.SpaceEvent
	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		evs, err := insertComputerTx(ctx, tx, comp, hashToken(rawToken))
		if err != nil {
			return err
		}
		events = append(events, evs...)
		return nil
	})
	if err != nil {
		return domain.Computer{}, "", err
	}
	s.dispatchPostCommit(events, nil)
	return comp, rawToken, nil
}

func insertComputerTx(ctx context.Context, tx *sql.Tx, comp domain.Computer, tokenHash string) ([]domain.SpaceEvent, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO computers (id, owner_user_id, name, hostname, os, token_hash, connection_status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'offline', ?)`,
		comp.ID, comp.OwnerUserID, comp.Name, comp.Hostname, comp.OS, tokenHash, store.FormatTime(comp.CreatedAt),
	); err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "insert computer", err)
	}
	return computerChangedTx(ctx, tx, comp.ID, "offline")
}

// computerVisibleSpacesSQL selects the Spaces a Computer appears in: those where its owner is an
// active member, plus those with an active Agent hosted on it. Bind the computer id twice.
const computerVisibleSpacesSQL = `
	SELECT m.space_id FROM members m JOIN computers c ON c.owner_user_id = m.user_id
	 WHERE c.id = ? AND m.retired_at IS NULL
	UNION
	SELECT a.space_id FROM agents a JOIN members m ON m.id = a.member_id
	 WHERE a.computer_id = ? AND m.retired_at IS NULL`

// computerChangedTx appends computer.changed to every Space the Computer is visible in.
func computerChangedTx(ctx context.Context, tx *sql.Tx, computerID, status string) ([]domain.SpaceEvent, error) {
	rows, err := tx.QueryContext(ctx, computerVisibleSpacesSQL, computerID, computerID)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list computer spaces", err)
	}
	var spaces []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, domain.WrapError(domain.CodeInternal, "scan computer space", err)
		}
		spaces = append(spaces, id)
	}
	if err := rows.Close(); err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list computer spaces", err)
	}
	events := make([]domain.SpaceEvent, 0, len(spaces))
	for _, sp := range spaces {
		ev, err := store.AppendEvent(ctx, tx, sp, "computer.changed", map[string]any{"computer_id": computerID, "status": status})
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, nil
}

func (s *Service) AuthenticateComputer(ctx context.Context, rawToken string) (domain.Computer, error) {
	if rawToken == "" {
		return domain.Computer{}, domain.NewError(domain.CodeUnauthenticated, "missing computer token")
	}
	c, err := scanComputer(s.store.ReadDB().QueryRowContext(ctx,
		`SELECT `+computerColumns+` FROM computers WHERE token_hash = ? AND deleted_at IS NULL`,
		hashToken(rawToken),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Computer{}, domain.NewError(domain.CodeUnauthenticated, "invalid computer token")
	}
	if err != nil {
		return domain.Computer{}, domain.WrapError(domain.CodeInternal, "query computer", err)
	}
	return c, nil
}

const computerColumns = `id, owner_user_id, name, hostname, os, connection_status, daemon_version, capabilities_json, last_seen_at, created_at`

type rowScanner interface {
	Scan(dest ...any) error
}

// computerCapabilities is the computers.capabilities_json document reported in hello.
type computerCapabilities struct {
	ACPProviders []string `json:"acp_providers"`
}

func scanComputer(row rowScanner) (domain.Computer, error) {
	var c domain.Computer
	var capsJSON, createdAt string
	var lastSeen sql.NullString
	if err := row.Scan(&c.ID, &c.OwnerUserID, &c.Name, &c.Hostname, &c.OS, &c.ConnectionStatus, &c.DaemonVersion, &capsJSON, &lastSeen, &createdAt); err != nil {
		return domain.Computer{}, err
	}
	var caps computerCapabilities
	_ = json.Unmarshal([]byte(capsJSON), &caps)
	c.ACPProviders = sanitizeACPProviders(caps.ACPProviders)
	c.CreatedAt = store.ParseTime(createdAt)
	if lastSeen.Valid {
		t := store.ParseTime(lastSeen.String)
		c.LastSeenAt = &t
	}
	return c, nil
}

// sanitizeACPProviders keeps known providers only, in canonical order, never nil.
func sanitizeACPProviders(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range domain.ACPProviders {
		for _, got := range in {
			if got == p {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// ListComputers returns the Computers visible in a Space (owner is an active member, or it hosts
// one of the Space's Agents), each with the owner's member id in this Space when present.
func (s *Service) ListComputers(ctx context.Context, spaceID string) ([]domain.Computer, error) {
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT `+prefixed("c.", computerColumns)+`, COALESCE(om.id, '')
		 FROM computers c
		 LEFT JOIN members om ON om.space_id = ? AND om.user_id = c.owner_user_id AND om.retired_at IS NULL
		 WHERE c.deleted_at IS NULL
		   AND (om.id IS NOT NULL OR EXISTS (
		       SELECT 1 FROM agents a JOIN members am ON am.id = a.member_id
		        WHERE a.computer_id = c.id AND a.space_id = ? AND am.retired_at IS NULL))
		 ORDER BY c.created_at ASC`,
		spaceID, spaceID,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list computers", err)
	}
	defer rows.Close()
	out := make([]domain.Computer, 0)
	for rows.Next() {
		var ownerMember string
		c, err := scanComputer(scanWithTail{rows, &ownerMember})
		if err != nil {
			return nil, err
		}
		c.OwnerMemberID = ownerMember
		out = append(out, c)
	}
	return out, rows.Err()
}

// scanWithTail scans computerColumns followed by extra trailing columns.
type scanWithTail struct {
	row  rowScanner
	tail any
}

func (s scanWithTail) Scan(dest ...any) error { return s.row.Scan(append(dest, s.tail)...) }

func prefixed(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// ComputerHello carries the capability fields a Computer reports when it connects.
type ComputerHello struct {
	DaemonVersion string
	ACPProviders  []string
}

// SetComputerStatus records connection status; hello is non-nil only for a fresh hello frame.
func (s *Service) SetComputerStatus(ctx context.Context, computerID, status string, hello *ComputerHello) ([]protocol.AssignedAgentSpec, error) {
	now := time.Now().UTC()
	var events []domain.SpaceEvent
	var specs []protocol.AssignedAgentSpec
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var currentStatus string
		if err := tx.QueryRowContext(ctx, `SELECT connection_status FROM computers WHERE id = ?`, computerID).Scan(&currentStatus); err != nil {
			return domain.WrapError(domain.CodeNotFound, "computer not found", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE computers SET connection_status = ?, last_seen_at = ? WHERE id = ?`,
			status, store.FormatTime(now), computerID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "update computer status", err)
		}
		if hello != nil {
			caps, _ := json.Marshal(computerCapabilities{ACPProviders: sanitizeACPProviders(hello.ACPProviders)})
			if _, err := tx.ExecContext(ctx,
				`UPDATE computers SET daemon_version = ?, capabilities_json = ? WHERE id = ?`,
				hello.DaemonVersion, string(caps), computerID,
			); err != nil {
				return domain.WrapError(domain.CodeInternal, "update computer capabilities", err)
			}
		}
		if status == "offline" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE agents SET runtime_state = 'offline', focus_conversation_id = NULL, runtime_state_updated_at = ? WHERE computer_id = ?`,
				store.FormatTime(now), computerID,
			); err != nil {
				return domain.WrapError(domain.CodeInternal, "mark agents offline", err)
			}
		}
		if status != currentStatus || hello != nil {
			evs, err := computerChangedTx(ctx, tx, computerID, status)
			if err != nil {
				return err
			}
			events = append(events, evs...)
		}
		if status == "online" {
			rows, err := tx.QueryContext(ctx,
				`SELECT `+agentSpecColumns+`
				 FROM agents a JOIN members m ON m.id = a.member_id
				 WHERE a.computer_id = ? AND m.retired_at IS NULL`,
				computerID,
			)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				sp, err := scanAgentSpec(rows)
				if err != nil {
					return err
				}
				specs = append(specs, sp)
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.dispatchPostCommit(events, nil)
	return specs, nil
}

const agentSpecColumns = `m.id, m.space_id, m.display_name, a.role_text, a.driver_kind, a.driver_config_json`

func scanAgentSpec(row rowScanner) (protocol.AssignedAgentSpec, error) {
	var sp protocol.AssignedAgentSpec
	var dk, cfg string
	if err := row.Scan(&sp.MemberID, &sp.SpaceID, &sp.DisplayName, &sp.RoleText, &dk, &cfg); err != nil {
		return protocol.AssignedAgentSpec{}, err
	}
	sp.DriverKind = domain.DriverKind(dk)
	sp.DriverConfig = domain.ParseDriverConfig(cfg)
	return sp, nil
}
