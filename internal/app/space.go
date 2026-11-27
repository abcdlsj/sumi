package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

func (s *Service) CreateSpace(ctx context.Context, user domain.User, slug, name string) (domain.Space, domain.Member, error) {
	if err := domain.ValidateSlug(slug); err != nil {
		return domain.Space{}, domain.Member{}, err
	}
	name = strings.TrimSpace(name)
	if err := domain.ValidateSpaceName(name); err != nil {
		return domain.Space{}, domain.Member{}, err
	}
	now := time.Now().UTC()
	spaceID := uuid.NewString()
	ownerMemberID := uuid.NewString()
	generalID := uuid.NewString()

	space := domain.Space{
		ID:            spaceID,
		Slug:          slug,
		Name:          name,
		Accent:        domain.DefaultSpaceAccent,
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
			`INSERT INTO spaces (id, slug, name, accent, owner_member_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			space.ID, space.Slug, space.Name, space.Accent, space.OwnerMemberID, store.FormatTime(now),
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
		`SELECT s.id, s.slug, s.name, s.accent, s.avatar_sha256, s.owner_member_id, s.event_seq, s.created_at
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
		if err := rows.Scan(&sp.ID, &sp.Slug, &sp.Name, &sp.Accent, &sp.AvatarHash, &sp.OwnerMemberID, &sp.EventSeq, &createdAt); err != nil {
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
		`SELECT id, slug, name, accent, avatar_sha256, owner_member_id, event_seq, created_at FROM spaces WHERE slug = ?`,
		spaceSlug,
	).Scan(&sp.ID, &sp.Slug, &sp.Name, &sp.Accent, &sp.AvatarHash, &sp.OwnerMemberID, &sp.EventSeq, &spCreated)
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
		        a.focus_conversation_id, a.last_error_code, a.last_error_detail, a.runtime_state_updated_at
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
		var retiredAt, compID, roleText, driverKind, driverCfg, rtState, focusConv, errCode, errDetail, rtUpdated sql.NullString
		if err := rows.Scan(
			&m.ID, &m.SpaceID, &kind, &m.DisplayName, &access, &createdAt, &retiredAt,
			&compID, &roleText, &driverKind, &driverCfg, &rtState, &focusConv, &errCode, &errDetail, &rtUpdated,
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
				DriverConfig:        domain.ParseDriverConfig(driverCfg.String).Redact(domain.DriverKind(driverKind.String)),
				RuntimeState:        domain.RuntimeState(rtState.String),
				FocusConversationID: focusConv.String,
				LastErrorCode:       errCode.String,
				LastErrorDetail:     errDetail.String,
			}
			if rtUpdated.Valid {
				t := store.ParseTime(rtUpdated.String)
				info.RuntimeStateUpdatedAt = &t
			}
			if info.RuntimeState == domain.RuntimeWorking {
				s.activityMu.Lock()
				if a, ok := s.activity[m.ID]; ok {
					info.Activity = &a
				}
				s.activityMu.Unlock()
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

// PairComputerDirect registers a Computer owned by the user's account and returns its token once.

func (s *Service) GetMember(ctx context.Context, spaceID, memberID string) (domain.Member, error) {
	members, err := s.ListMembers(ctx, spaceID)
	if err != nil {
		return domain.Member{}, err
	}
	for _, m := range members {
		if m.ID == memberID {
			return m, nil
		}
	}
	return domain.Member{}, domain.NewError(domain.CodeNotFound, "member not found")
}

func (s *Service) UpdateSpace(ctx context.Context, actor domain.Member, name, accent string) (domain.Space, error) {
	if actor.Kind != domain.MemberKindHuman || (actor.AccessLevel != domain.AccessOwner && actor.AccessLevel != domain.AccessAdmin) {
		return domain.Space{}, domain.NewError(domain.CodePermissionDenied, "only owners and admins can change space settings")
	}
	name = strings.TrimSpace(name)
	if name != "" {
		if err := domain.ValidateSpaceName(name); err != nil {
			return domain.Space{}, err
		}
	}
	if accent != "" {
		if err := domain.ValidateSpaceAccent(accent); err != nil {
			return domain.Space{}, err
		}
	}
	var sp domain.Space
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var created string
		if err := tx.QueryRowContext(ctx,
			`UPDATE spaces SET name = COALESCE(NULLIF(?, ''), name), accent = COALESCE(NULLIF(?, ''), accent)
			 WHERE id = ? RETURNING id, slug, name, accent, avatar_sha256, owner_member_id, event_seq, created_at`,
			name, accent, actor.SpaceID,
		).Scan(&sp.ID, &sp.Slug, &sp.Name, &sp.Accent, &sp.AvatarHash, &sp.OwnerMemberID, &sp.EventSeq, &created); err != nil {
			return domain.WrapError(domain.CodeInternal, "update space", err)
		}
		sp.CreatedAt = store.ParseTime(created)
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "space.updated", map[string]any{"space_id": sp.ID})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return domain.Space{}, err
	}
	s.dispatchPostCommit(events, nil)
	return sp, nil
}

// sniffSpaceAvatarMediaType detects safe raster image formats strictly from magic bytes,
// rejecting active web types (SVG, HTML, scripts) regardless of client-supplied headers.
func sniffSpaceAvatarMediaType(head []byte) (string, bool) {
	if len(head) == 0 {
		return "", false
	}
	detected := http.DetectContentType(head)
	switch detected {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return detected, true
	}
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		brand := string(head[8:12])
		if brand == "avif" || brand == "avis" {
			return "image/avif", true
		}
	}
	return "", false
}

// SetSpaceAvatar stores a verified raster image in the content-addressed object store and
// binds it as the Space's custom avatar.
func (s *Service) SetSpaceAvatar(ctx context.Context, actor domain.Member, content io.Reader) (domain.Space, error) {
	if actor.Kind != domain.MemberKindHuman || (actor.AccessLevel != domain.AccessOwner && actor.AccessLevel != domain.AccessAdmin) {
		return domain.Space{}, domain.NewError(domain.CodePermissionDenied, "only owners and admins can change space settings")
	}
	var headBuf [512]byte
	n, readErr := io.ReadFull(content, headBuf[:])
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return domain.Space{}, domain.WrapError(domain.CodeInvalidArgument, "read avatar upload", readErr)
	}
	head := headBuf[:n]
	if len(head) == 0 {
		return domain.Space{}, domain.NewError(domain.CodeInvalidArgument, "avatar image is empty")
	}
	mediaType, ok := sniffSpaceAvatarMediaType(head)
	if !ok {
		return domain.Space{}, domain.NewError(domain.CodeInvalidArgument, "avatar must be a PNG, JPEG, WebP, GIF, or AVIF image")
	}

	combined := io.MultiReader(bytes.NewReader(head), content)
	sha, _, err := s.store.Objects().Put(combined, domain.MaxSpaceAvatarBytes)
	if err != nil {
		if errors.Is(err, store.ErrObjectTooLarge) {
			return domain.Space{}, domain.NewError(domain.CodeInvalidArgument, "avatar image exceeds 4 MiB limit")
		}
		return domain.Space{}, domain.WrapError(domain.CodeInternal, "store space avatar", err)
	}

	var sp domain.Space
	var events []domain.SpaceEvent
	// Objects are content-addressed and may be shared across attachments or spaces, so previous
	// avatars are left untouched in the object store.
	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var created string
		if err := tx.QueryRowContext(ctx,
			`UPDATE spaces SET avatar_sha256 = ?, avatar_media_type = ?
			 WHERE id = ? RETURNING id, slug, name, accent, avatar_sha256, owner_member_id, event_seq, created_at`,
			sha, mediaType, actor.SpaceID,
		).Scan(&sp.ID, &sp.Slug, &sp.Name, &sp.Accent, &sp.AvatarHash, &sp.OwnerMemberID, &sp.EventSeq, &created); err != nil {
			return domain.WrapError(domain.CodeInternal, "update space avatar", err)
		}
		sp.CreatedAt = store.ParseTime(created)
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "space.updated", map[string]any{"space_id": sp.ID})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return domain.Space{}, err
	}
	s.dispatchPostCommit(events, nil)
	return sp, nil
}

// RemoveSpaceAvatar clears a Space's custom avatar so it falls back to the default acronym avatar.
func (s *Service) RemoveSpaceAvatar(ctx context.Context, actor domain.Member) (domain.Space, error) {
	if actor.Kind != domain.MemberKindHuman || (actor.AccessLevel != domain.AccessOwner && actor.AccessLevel != domain.AccessAdmin) {
		return domain.Space{}, domain.NewError(domain.CodePermissionDenied, "only owners and admins can change space settings")
	}
	var sp domain.Space
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var created string
		if err := tx.QueryRowContext(ctx,
			`UPDATE spaces SET avatar_sha256 = '', avatar_media_type = ''
			 WHERE id = ? RETURNING id, slug, name, accent, avatar_sha256, owner_member_id, event_seq, created_at`,
			actor.SpaceID,
		).Scan(&sp.ID, &sp.Slug, &sp.Name, &sp.Accent, &sp.AvatarHash, &sp.OwnerMemberID, &sp.EventSeq, &created); err != nil {
			return domain.WrapError(domain.CodeInternal, "remove space avatar", err)
		}
		sp.CreatedAt = store.ParseTime(created)
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "space.updated", map[string]any{"space_id": sp.ID})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return domain.Space{}, err
	}
	s.dispatchPostCommit(events, nil)
	return sp, nil
}

type SpaceAvatarContent struct {
	SHA256    string
	MediaType string
	File      *os.File
}

// OpenSpaceAvatar opens the Space's custom avatar for an authorized Space member.
func (s *Service) OpenSpaceAvatar(ctx context.Context, viewer domain.Member) (SpaceAvatarContent, error) {
	var sha, mediaType string
	err := s.store.ReadDB().QueryRowContext(ctx,
		`SELECT avatar_sha256, avatar_media_type FROM spaces WHERE id = ?`,
		viewer.SpaceID,
	).Scan(&sha, &mediaType)
	if errors.Is(err, sql.ErrNoRows) || sha == "" {
		return SpaceAvatarContent{}, domain.NewError(domain.CodeNotFound, "space avatar not found")
	}
	if err != nil {
		return SpaceAvatarContent{}, domain.WrapError(domain.CodeInternal, "query space avatar", err)
	}
	file, err := s.store.Objects().Open(sha)
	if err != nil {
		return SpaceAvatarContent{}, domain.WrapError(domain.CodeNotFound, "space avatar content missing", err)
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return SpaceAvatarContent{SHA256: sha, MediaType: mediaType, File: file}, nil
}

// placeableComputerStatusTx enforces who may place an Agent on a Computer: a Human only on a
// Computer their own account owns; an Agent only on the Computer that hosts it.
