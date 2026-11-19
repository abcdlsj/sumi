package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
	"github.com/abcdlsj/sumi/internal/store"
)

type AuthResult struct {
	User         domain.User `json:"user"`
	SessionToken string      `json:"-"`
}

func (s *Service) Register(ctx context.Context, email, password, displayName string) (AuthResult, error) {
	normEmail := normalizeEmail(email)
	if normEmail == "" || !strings.Contains(normEmail, "@") {
		return AuthResult{}, domain.NewError(domain.CodeInvalidArgument, "valid email is required")
	}
	if len(password) < 8 {
		return AuthResult{}, domain.NewError(domain.CodeInvalidArgument, "password must be at least 8 characters")
	}
	if err := domain.ValidateDisplayName(displayName); err != nil {
		return AuthResult{}, err
	}
	pwHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return AuthResult{}, domain.WrapError(domain.CodeInternal, "hash password", err)
	}
	rawToken, err := randomToken(32)
	if err != nil {
		return AuthResult{}, domain.WrapError(domain.CodeInternal, "generate session token", err)
	}

	now := time.Now().UTC()
	user := domain.User{
		ID:              uuid.NewString(),
		EmailNormalized: normEmail,
		DisplayName:     displayName,
		CreatedAt:       now,
	}
	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO users (id, email_normalized, password_hash, display_name, created_at) VALUES (?, ?, ?, ?, ?)`,
			user.ID, user.EmailNormalized, string(pwHash), user.DisplayName, store.FormatTime(now),
		); err != nil {
			if store.IsUniqueViolation(err) {
				return domain.NewError(domain.CodeConflict, "email already registered")
			}
			return domain.WrapError(domain.CodeInternal, "insert user", err)
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO browser_sessions (id, user_id, token_hash, expires_at, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), user.ID, hashToken(rawToken),
			store.FormatTime(now.Add(30*24*time.Hour)), store.FormatTime(now), store.FormatTime(now),
		)
		return err
	})
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{User: user, SessionToken: rawToken}, nil
}

func (s *Service) Login(ctx context.Context, email, password string) (AuthResult, error) {
	normEmail := normalizeEmail(email)
	var user domain.User
	var pwHash, createdAt string
	err := s.store.ReadDB().QueryRowContext(ctx,
		`SELECT id, email_normalized, password_hash, display_name, created_at FROM users WHERE email_normalized = ?`,
		normEmail,
	).Scan(&user.ID, &user.EmailNormalized, &pwHash, &user.DisplayName, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthResult{}, domain.NewError(domain.CodeUnauthenticated, "invalid email or password")
	}
	if err != nil {
		return AuthResult{}, domain.WrapError(domain.CodeInternal, "query user", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(pwHash), []byte(password)); err != nil {
		return AuthResult{}, domain.NewError(domain.CodeUnauthenticated, "invalid email or password")
	}
	user.CreatedAt = store.ParseTime(createdAt)

	rawToken, err := randomToken(32)
	if err != nil {
		return AuthResult{}, domain.WrapError(domain.CodeInternal, "generate session token", err)
	}
	now := time.Now().UTC()
	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO browser_sessions (id, user_id, token_hash, expires_at, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), user.ID, hashToken(rawToken),
			store.FormatTime(now.Add(30*24*time.Hour)), store.FormatTime(now), store.FormatTime(now),
		)
		return err
	})
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{User: user, SessionToken: rawToken}, nil
}

func (s *Service) Logout(ctx context.Context, sessionToken string) error {
	if sessionToken == "" {
		return nil
	}
	return s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM browser_sessions WHERE token_hash = ?`, hashToken(sessionToken))
		return err
	})
}

func (s *Service) AuthenticateBrowserSession(ctx context.Context, rawToken string) (domain.User, error) {
	if rawToken == "" {
		return domain.User{}, domain.NewError(domain.CodeUnauthenticated, "missing session")
	}
	var u domain.User
	var createdAt, expiresAt string
	err := s.store.ReadDB().QueryRowContext(ctx,
		`SELECT u.id, u.email_normalized, u.display_name, u.created_at, s.expires_at
		 FROM browser_sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ?`,
		hashToken(rawToken),
	).Scan(&u.ID, &u.EmailNormalized, &u.DisplayName, &createdAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, domain.NewError(domain.CodeUnauthenticated, "invalid session")
	}
	if err != nil {
		return domain.User{}, domain.WrapError(domain.CodeInternal, "query session", err)
	}
	if time.Now().UTC().After(store.ParseTime(expiresAt)) {
		return domain.User{}, domain.NewError(domain.CodeUnauthenticated, "session expired")
	}
	u.CreatedAt = store.ParseTime(createdAt)
	return u, nil
}

func (s *Service) CreateSpace(ctx context.Context, user domain.User, slug, name string) (domain.Space, domain.Member, error) {
	if err := domain.ValidateSlug(slug); err != nil {
		return domain.Space{}, domain.Member{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.Space{}, domain.Member{}, domain.NewError(domain.CodeInvalidArgument, "space name is required")
	}
	now := time.Now().UTC()
	spaceID := uuid.NewString()
	ownerMemberID := uuid.NewString()
	generalID := uuid.NewString()

	space := domain.Space{
		ID:            spaceID,
		Slug:          slug,
		Name:          name,
		OwnerMemberID: ownerMemberID,
		CreatedAt:     now,
	}
	member := domain.Member{
		ID:          ownerMemberID,
		SpaceID:     spaceID,
		Kind:        domain.MemberKindHuman,
		DisplayName: user.DisplayName,
		AccessLevel: domain.AccessOwner,
		CreatedAt:   now,
	}

	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO spaces (id, slug, name, owner_member_id, created_at) VALUES (?, ?, ?, ?, ?)`,
			space.ID, space.Slug, space.Name, space.OwnerMemberID, store.FormatTime(now),
		); err != nil {
			if store.IsUniqueViolation(err) {
				return domain.NewError(domain.CodeConflict, "space slug already exists")
			}
			return domain.WrapError(domain.CodeInternal, "insert space", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO members (id, space_id, user_id, kind, display_name, access_level, created_at) VALUES (?, ?, ?, 'human', ?, 'owner', ?)`,
			member.ID, space.ID, user.ID, member.DisplayName, store.FormatTime(now),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "insert owner member", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO conversations (id, space_id, kind, slug, topic, created_at) VALUES (?, ?, 'public', 'general', 'Space-wide discussion', ?)`,
			generalID, space.ID, store.FormatTime(now),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "insert general channel", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
			generalID, space.ID, member.ID, store.FormatTime(now),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "join general channel", err)
		}
		return nil
	})
	if err != nil {
		return domain.Space{}, domain.Member{}, err
	}
	return space, member, nil
}

func (s *Service) ListUserSpaces(ctx context.Context, userID string) ([]domain.Space, error) {
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT s.id, s.slug, s.name, s.owner_member_id, s.event_seq, s.created_at
		 FROM spaces s JOIN members m ON m.space_id = s.id
		 WHERE m.user_id = ? AND m.retired_at IS NULL
		 ORDER BY s.created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list spaces", err)
	}
	defer rows.Close()
	out := make([]domain.Space, 0)
	for rows.Next() {
		var sp domain.Space
		var createdAt string
		if err := rows.Scan(&sp.ID, &sp.Slug, &sp.Name, &sp.OwnerMemberID, &sp.EventSeq, &createdAt); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "scan space", err)
		}
		sp.CreatedAt = store.ParseTime(createdAt)
		out = append(out, sp)
	}
	return out, rows.Err()
}

func (s *Service) ResolveHumanMemberInSpace(ctx context.Context, userID, spaceSlug string) (domain.Space, domain.Member, error) {
	var sp domain.Space
	var spCreated string
	err := s.store.ReadDB().QueryRowContext(ctx,
		`SELECT id, slug, name, owner_member_id, event_seq, created_at FROM spaces WHERE slug = ?`,
		spaceSlug,
	).Scan(&sp.ID, &sp.Slug, &sp.Name, &sp.OwnerMemberID, &sp.EventSeq, &spCreated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Space{}, domain.Member{}, domain.NewError(domain.CodeNotFound, "space not found")
	}
	if err != nil {
		return domain.Space{}, domain.Member{}, domain.WrapError(domain.CodeInternal, "query space", err)
	}
	sp.CreatedAt = store.ParseTime(spCreated)

	var memberID string
	err = s.store.ReadDB().QueryRowContext(ctx,
		`SELECT id FROM members WHERE space_id = ? AND user_id = ? AND retired_at IS NULL`,
		sp.ID, userID,
	).Scan(&memberID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Space{}, domain.Member{}, domain.NewError(domain.CodePermissionDenied, "not a member of this space")
	}
	if err != nil {
		return domain.Space{}, domain.Member{}, domain.WrapError(domain.CodeInternal, "query space member", err)
	}
	m, err := loadMember(ctx, s.store.ReadDB(), memberID)
	if err != nil {
		return domain.Space{}, domain.Member{}, err
	}
	return sp, m, nil
}

func (s *Service) ListMembers(ctx context.Context, spaceID string) ([]domain.Member, error) {
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT m.id, m.space_id, m.kind, m.display_name, m.access_level, m.created_at, m.retired_at,
		        a.computer_id, a.role_text, a.driver_kind, a.driver_config_json, a.runtime_state,
		        a.focus_conversation_id, a.last_error_code, a.runtime_state_updated_at
		 FROM members m
		 LEFT JOIN agents a ON a.member_id = m.id
		 WHERE m.space_id = ? AND m.retired_at IS NULL
		 ORDER BY m.kind ASC, lower(m.display_name) ASC`,
		spaceID,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list members", err)
	}
	defer rows.Close()

	members := make([]domain.Member, 0)
	byID := make(map[string]int)
	for rows.Next() {
		var m domain.Member
		var kind, access, createdAt string
		var retiredAt, compID, roleText, driverKind, driverCfg, rtState, focusConv, errCode, rtUpdated sql.NullString
		if err := rows.Scan(
			&m.ID, &m.SpaceID, &kind, &m.DisplayName, &access, &createdAt, &retiredAt,
			&compID, &roleText, &driverKind, &driverCfg, &rtState, &focusConv, &errCode, &rtUpdated,
		); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "scan member", err)
		}
		m.Kind = domain.MemberKind(kind)
		m.AccessLevel = domain.AccessLevel(access)
		m.CreatedAt = store.ParseTime(createdAt)
		if m.Kind == domain.MemberKindAgent {
			info := &domain.AgentInfo{
				ComputerID:          compID.String,
				RoleText:            roleText.String,
				DriverKind:          domain.DriverKind(driverKind.String),
				DriverConfigJSON:    driverCfg.String,
				RuntimeState:        domain.RuntimeState(rtState.String),
				FocusConversationID: focusConv.String,
				LastErrorCode:       errCode.String,
			}
			if rtUpdated.Valid {
				t := store.ParseTime(rtUpdated.String)
				info.RuntimeStateUpdatedAt = &t
			}
			m.Agent = info
		}
		byID[m.ID] = len(members)
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	permRows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT member_id, action_code FROM member_permissions WHERE space_id = ?`, spaceID,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list permissions", err)
	}
	defer permRows.Close()
	for permRows.Next() {
		var mid, code string
		if err := permRows.Scan(&mid, &code); err != nil {
			return nil, err
		}
		if idx, ok := byID[mid]; ok {
			members[idx].Permissions = append(members[idx].Permissions, domain.Permission(code))
		}
	}
	return members, permRows.Err()
}

func (s *Service) SetMemberPermission(ctx context.Context, actor domain.Member, targetMemberID string, perm domain.Permission, grant bool) error {
	if actor.Kind != domain.MemberKindHuman || (actor.AccessLevel != domain.AccessOwner && actor.AccessLevel != domain.AccessAdmin) {
		return domain.NewError(domain.CodePermissionDenied, "only human owner or admin can manage permissions")
	}
	if err := domain.ValidatePermission(perm); err != nil {
		return err
	}
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		target, err := loadMember(ctx, tx, targetMemberID)
		if err != nil {
			return err
		}
		if target.SpaceID != actor.SpaceID {
			return domain.NewError(domain.CodeNotFound, "member not found")
		}
		if grant {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO member_permissions (member_id, space_id, action_code, granted_by_member_id, created_at)
				 VALUES (?, ?, ?, ?, ?)`,
				target.ID, actor.SpaceID, string(perm), actor.ID, store.FormatTime(time.Now().UTC()),
			); err != nil {
				return domain.WrapError(domain.CodeInternal, "grant permission", err)
			}
		} else {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM member_permissions WHERE member_id = ? AND action_code = ?`,
				target.ID, string(perm),
			); err != nil {
				return domain.WrapError(domain.CodeInternal, "revoke permission", err)
			}
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "member.permissions_changed", map[string]any{
			"member_id": target.ID,
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
	s.dispatchPostCommit(events, nil)
	return nil
}

// PairComputerDirect registers a Computer directly in a Space (used by CLI/tests and confirmed pairing).
func (s *Service) PairComputerDirect(ctx context.Context, actor domain.Member, name, hostname, osName string) (domain.Computer, string, error) {
	if actor.AccessLevel != domain.AccessOwner && actor.AccessLevel != domain.AccessAdmin {
		return domain.Computer{}, "", domain.NewError(domain.CodePermissionDenied, "only owner or admin can pair a computer")
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
		SpaceID:          actor.SpaceID,
		Name:             name,
		Hostname:         hostname,
		OS:               osName,
		ConnectionStatus: "offline",
		CreatedAt:        now,
	}
	var events []domain.SpaceEvent
	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		ev, err := insertComputerTx(ctx, tx, comp, hashToken(rawToken))
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return domain.Computer{}, "", err
	}
	s.dispatchPostCommit(events, nil)
	return comp, rawToken, nil
}

func insertComputerTx(ctx context.Context, tx *sql.Tx, comp domain.Computer, tokenHash string) (domain.SpaceEvent, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO computers (id, space_id, name, hostname, os, token_hash, connection_status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'offline', ?)`,
		comp.ID, comp.SpaceID, comp.Name, comp.Hostname, comp.OS, tokenHash, store.FormatTime(comp.CreatedAt),
	); err != nil {
		return domain.SpaceEvent{}, domain.WrapError(domain.CodeInternal, "insert computer", err)
	}
	return store.AppendEvent(ctx, tx, comp.SpaceID, "computer.changed", map[string]any{
		"computer_id": comp.ID,
		"status":      "offline",
	})
}

func (s *Service) AuthenticateComputer(ctx context.Context, rawToken string) (domain.Computer, error) {
	if rawToken == "" {
		return domain.Computer{}, domain.NewError(domain.CodeUnauthenticated, "missing computer token")
	}
	var c domain.Computer
	var createdAt string
	var lastSeen sql.NullString
	err := s.store.ReadDB().QueryRowContext(ctx,
		`SELECT id, space_id, name, hostname, os, connection_status, daemon_version, last_seen_at, created_at
		 FROM computers WHERE token_hash = ? AND deleted_at IS NULL`,
		hashToken(rawToken),
	).Scan(&c.ID, &c.SpaceID, &c.Name, &c.Hostname, &c.OS, &c.ConnectionStatus, &c.DaemonVersion, &lastSeen, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Computer{}, domain.NewError(domain.CodeUnauthenticated, "invalid computer token")
	}
	if err != nil {
		return domain.Computer{}, domain.WrapError(domain.CodeInternal, "query computer", err)
	}
	c.CreatedAt = store.ParseTime(createdAt)
	if lastSeen.Valid {
		t := store.ParseTime(lastSeen.String)
		c.LastSeenAt = &t
	}
	return c, nil
}

func (s *Service) ListComputers(ctx context.Context, spaceID string) ([]domain.Computer, error) {
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT id, space_id, name, hostname, os, connection_status, daemon_version, last_seen_at, created_at
		 FROM computers WHERE space_id = ? AND deleted_at IS NULL ORDER BY created_at ASC`,
		spaceID,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list computers", err)
	}
	defer rows.Close()
	out := make([]domain.Computer, 0)
	for rows.Next() {
		var c domain.Computer
		var createdAt string
		var lastSeen sql.NullString
		if err := rows.Scan(&c.ID, &c.SpaceID, &c.Name, &c.Hostname, &c.OS, &c.ConnectionStatus, &c.DaemonVersion, &lastSeen, &createdAt); err != nil {
			return nil, err
		}
		c.CreatedAt = store.ParseTime(createdAt)
		if lastSeen.Valid {
			t := store.ParseTime(lastSeen.String)
			c.LastSeenAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) SetComputerStatus(ctx context.Context, computerID, status, daemonVersion string) ([]protocol.AssignedAgentSpec, error) {
	now := time.Now().UTC()
	var events []domain.SpaceEvent
	var specs []protocol.AssignedAgentSpec
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var spaceID, currentStatus string
		if err := tx.QueryRowContext(ctx, `SELECT space_id, connection_status FROM computers WHERE id = ?`, computerID).Scan(&spaceID, &currentStatus); err != nil {
			return domain.WrapError(domain.CodeNotFound, "computer not found", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE computers SET connection_status = ?, daemon_version = CASE WHEN ? <> '' THEN ? ELSE daemon_version END, last_seen_at = ? WHERE id = ?`,
			status, daemonVersion, daemonVersion, store.FormatTime(now), computerID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "update computer status", err)
		}
		if status == "offline" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE agents SET runtime_state = 'offline', focus_conversation_id = NULL, runtime_state_updated_at = ? WHERE computer_id = ?`,
				store.FormatTime(now), computerID,
			); err != nil {
				return domain.WrapError(domain.CodeInternal, "mark agents offline", err)
			}
		}
		if status != currentStatus {
			ev, err := store.AppendEvent(ctx, tx, spaceID, "computer.changed", map[string]any{
				"computer_id": computerID,
				"status":      status,
			})
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		if status == "online" {
			rows, err := tx.QueryContext(ctx,
				`SELECT m.id, m.space_id, m.display_name, a.role_text, a.driver_kind, a.driver_config_json
				 FROM agents a JOIN members m ON m.id = a.member_id
				 WHERE a.computer_id = ? AND m.retired_at IS NULL`,
				computerID,
			)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var sp protocol.AssignedAgentSpec
				var dk string
				if err := rows.Scan(&sp.MemberID, &sp.SpaceID, &sp.DisplayName, &sp.RoleText, &dk, &sp.DriverConfigJSON); err != nil {
					return err
				}
				sp.DriverKind = domain.DriverKind(dk)
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

type CreateAgentInput struct {
	DisplayName          string
	RoleText             string
	ComputerID           string
	DriverKind           domain.DriverKind
	DriverConfigJSON     string
	SourceConversationID string // Optional: emits an agent_created Action Message in this conversation.
}

func (s *Service) CreateAgent(ctx context.Context, actor domain.Member, in CreateAgentInput) (domain.Member, error) {
	ok, err := hasPermission(ctx, s.store.ReadDB(), actor, domain.PermAgentCreate)
	if err != nil {
		return domain.Member{}, err
	}
	if !ok {
		return domain.Member{}, domain.NewError(domain.CodePermissionDenied, "missing agent.create permission")
	}
	if err := domain.ValidateDisplayName(in.DisplayName); err != nil {
		return domain.Member{}, err
	}
	if in.DriverKind != domain.DriverACP && in.DriverKind != domain.DriverBuiltin {
		return domain.Member{}, domain.NewError(domain.CodeInvalidArgument, "driver_kind must be acp or builtin")
	}
	cfg := strings.TrimSpace(in.DriverConfigJSON)
	if cfg == "" {
		cfg = "{}"
	}
	if !json.Valid([]byte(cfg)) {
		return domain.Member{}, domain.NewError(domain.CodeInvalidArgument, "driver_config_json must be valid JSON")
	}

	now := time.Now().UTC()
	memberID := uuid.NewString()
	var created domain.Member
	var events []domain.SpaceEvent
	var compOnline bool

	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var compStatus string
		if err := tx.QueryRowContext(ctx,
			`SELECT connection_status FROM computers WHERE id = ? AND space_id = ? AND deleted_at IS NULL`,
			in.ComputerID, actor.SpaceID,
		).Scan(&compStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.NewError(domain.CodeNotFound, "computer not found in space")
			}
			return domain.WrapError(domain.CodeInternal, "query computer", err)
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
			memberID, actor.SpaceID, in.ComputerID, in.RoleText, string(in.DriverKind), cfg, string(rtState), store.FormatTime(now), store.FormatTime(now),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "insert agent", err)
		}

		// Auto-join #general if present so the agent is reachable in the default space channel.
		var generalID string
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM conversations WHERE space_id = ? AND kind = 'public' AND slug = 'general'`,
			actor.SpaceID,
		).Scan(&generalID); err == nil {
			_, _ = tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
				generalID, actor.SpaceID, memberID, store.FormatTime(now),
			)
		}

		if in.SourceConversationID != "" {
			if _, err := canAccessConversation(ctx, tx, actor, in.SourceConversationID); err != nil {
				return err
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
				ComputerID:       in.ComputerID,
				RoleText:         in.RoleText,
				DriverKind:       in.DriverKind,
				DriverConfigJSON: cfg,
				RuntimeState:     rtState,
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
				MemberID:         memberID,
				SpaceID:          actor.SpaceID,
				DisplayName:      in.DisplayName,
				RoleText:         in.RoleText,
				DriverKind:       in.DriverKind,
				DriverConfigJSON: cfg,
			},
		})
	}
	return created, nil
}

func (s *Service) UpdateAgentRuntimeState(ctx context.Context, computerID, agentMemberID string, state domain.RuntimeState, focusConvID, errorCode string) error {
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
			`UPDATE agents SET runtime_state = ?, focus_conversation_id = ?, last_error_code = ?, runtime_state_updated_at = ? WHERE member_id = ?`,
			string(state), focus, errorCode, store.FormatTime(now), agentMemberID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "update agent runtime state", err)
		}
		ev, err := store.AppendEvent(ctx, tx, spaceID, "agent.state_changed", map[string]any{
			"member_id":             agentMemberID,
			"runtime_state":         state,
			"focus_conversation_id": focusConvID,
			"error_code":            errorCode,
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
