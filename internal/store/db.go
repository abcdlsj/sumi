package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/abcdlsj/sumi/internal/domain"
)

//go:embed schema.sql
var schemaSQL string

const ExpectedSchemaVersion = 1

const timeFormat = "2006-01-02T15:04:05.000000Z"

func FormatTime(t time.Time) string {
	return t.UTC().Format(timeFormat)
}

func ParseTime(s string) time.Time {
	t, err := time.Parse(timeFormat, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339Nano, s)
	}
	return t.UTC()
}

// Store owns the SQLite connection pools with strict write serialization (MaxOpenConns=1 + mutex)
// and concurrent WAL reads.
type Store struct {
	writeDB *sql.DB
	readDB  *sql.DB
	writeMu sync.Mutex
	objects *ObjectStore
}

// Open initializes a SQLite database at path (or ":memory:" for isolated tests).
func Open(path string) (*Store, error) {
	var dsn string
	if path == ":memory:" {
		// Unique shared in-memory DB so writeDB and readDB observe the same tables.
		dsn = fmt.Sprintf("file:sumi_mem_%s?mode=memory&cache=shared", strings.ReplaceAll(uuid.NewString(), "-", ""))
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
		dsn = fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	}

	writeDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open write sqlite: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)

	readDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("open read sqlite: %w", err)
	}
	readDB.SetMaxOpenConns(8)
	readDB.SetMaxIdleConns(4)

	objectRoot := filepath.Join(filepath.Dir(path), "objects")
	if path == ":memory:" {
		tmpDir, err := os.MkdirTemp("", "sumi-mem-objects-")
		if err != nil {
			_ = writeDB.Close()
			_ = readDB.Close()
			return nil, fmt.Errorf("create temp object root: %w", err)
		}
		objectRoot = tmpDir
	}
	objects, err := newObjectStore(objectRoot)
	if err != nil {
		_ = writeDB.Close()
		_ = readDB.Close()
		return nil, err
	}

	s := &Store{writeDB: writeDB, readDB: readDB, objects: objects}
	if err := s.initPragmasAndSchema(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initPragmasAndSchema() error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
	}
	for _, p := range pragmas {
		if _, err := s.writeDB.Exec(p); err != nil {
			return fmt.Errorf("apply write pragma %q: %w", p, err)
		}
		if _, err := s.readDB.Exec(p); err != nil {
			return fmt.Errorf("apply read pragma %q: %w", p, err)
		}
	}

	var tableName string
	err := s.writeDB.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='schema_meta'`).Scan(&tableName)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.writeDB.Exec(schemaSQL); err != nil {
			return fmt.Errorf("apply baseline schema: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("check schema_meta: %w", err)
	}

	var version int
	if err := s.writeDB.QueryRow(`SELECT MAX(version) FROM schema_meta`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version != ExpectedSchemaVersion {
		return fmt.Errorf("unsupported schema version %d (expected %d); rebuild empty database or upgrade binary", version, ExpectedSchemaVersion)
	}
	return nil
}

func (s *Store) Close() error {
	var firstErr error
	if s.readDB != nil {
		if err := s.readDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.writeDB != nil {
		if err := s.writeDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ReadDB returns the concurrent read pool.
func (s *Store) ReadDB() *sql.DB {
	return s.readDB
}

// Objects returns the attachment blob store that sits next to the SQLite file.
func (s *Store) Objects() *ObjectStore {
	return s.objects
}

// WithWriteTx runs fn inside a serialized SQLite transaction.
func (s *Store) WithWriteTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return domain.WrapError(domain.CodeInternal, "begin write tx", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return domain.WrapError(domain.CodeInternal, "commit write tx", err)
	}
	return nil
}

// AppendEvent allocates the next commit-ordered sequence for spaceID and writes an event row inside tx.
func AppendEvent(ctx context.Context, tx *sql.Tx, spaceID, kind string, payload any) (domain.SpaceEvent, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return domain.SpaceEvent{}, domain.WrapError(domain.CodeInternal, "marshal event payload", err)
	}
	now := time.Now().UTC()
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`UPDATE spaces SET event_seq = event_seq + 1 WHERE id = ? RETURNING event_seq`,
		spaceID,
	).Scan(&seq); err != nil {
		return domain.SpaceEvent{}, domain.WrapError(domain.CodeInternal, "advance space event_seq", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events (space_id, sequence, kind, payload_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		spaceID, seq, kind, string(raw), FormatTime(now),
	); err != nil {
		return domain.SpaceEvent{}, domain.WrapError(domain.CodeInternal, "insert event", err)
	}
	return domain.SpaceEvent{
		Sequence:    seq,
		SpaceID:     spaceID,
		Kind:        kind,
		PayloadJSON: string(raw),
		CreatedAt:   now,
	}, nil
}

// IsUniqueViolation reports whether err is a SQLite UNIQUE constraint error.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "PRIMARY KEY constraint failed")
}
