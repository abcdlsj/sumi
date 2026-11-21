package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/abcdlsj/sumi/internal/domain"
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
