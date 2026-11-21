package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMigrationsUpgradeFromBaselineAndIdempotency(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sumi.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open fresh db: %v", err)
	}

	ver, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if ver != 6 {
		t.Fatalf("expected baseline schema version 6, got %d", ver)
	}

	var appliedAt string
	if err := s.ReadDB().QueryRow(`SELECT applied_at FROM schema_meta WHERE version = 6`).Scan(&appliedAt); err != nil {
		t.Fatalf("read applied_at: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Re-opening must be a no-op and preserve the original schema_meta row.
	s, err = Open(dbPath)
	if err != nil {
		t.Fatalf("re-open db: %v", err)
	}
	var appliedAt2 string
	if err := s.ReadDB().QueryRow(`SELECT applied_at FROM schema_meta WHERE version = 6`).Scan(&appliedAt2); err != nil {
		t.Fatalf("read applied_at after reopen: %v", err)
	}
	if appliedAt != appliedAt2 {
		t.Fatalf("expected baseline migration to be idempotent, applied_at changed %q -> %q", appliedAt, appliedAt2)
	}

	// Seed a user row on v6 to verify incremental forward migrations preserve data.
	if _, err := s.writeDB.Exec(
		`INSERT INTO users (id, email_normalized, password_hash, display_name, created_at) VALUES ('u1', 'a@b.com', 'h', 'Alice', ?)`,
		appliedAt,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	base, err := loadEmbeddedMigrations()
	if err != nil {
		t.Fatalf("loadEmbeddedMigrations: %v", err)
	}
	extended := append(append([]Migration(nil), base...),
		Migration{
			Version: 7,
			Name:    "add_user_bio",
			SQL:     `ALTER TABLE users ADD COLUMN bio TEXT NOT NULL DEFAULT '';`,
		},
		Migration{
			Version: 8,
			Name:    "backfill_user_bio",
			SQL:     `UPDATE users SET bio = 'migrated' WHERE id = 'u1';`,
		},
	)

	if err := applyMigrations(s.writeDB, extended); err != nil {
		t.Fatalf("apply incremental migrations: %v", err)
	}

	ver, err = s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion after upgrade: %v", err)
	}
	if ver != 8 {
		t.Fatalf("expected schema version 8, got %d", ver)
	}

	var bio string
	if err := s.ReadDB().QueryRow(`SELECT bio FROM users WHERE id = 'u1'`).Scan(&bio); err != nil {
		t.Fatalf("read migrated column: %v", err)
	}
	if bio != "migrated" {
		t.Fatalf("expected bio='migrated', got %q", bio)
	}

	// Running the same migration set again must be idempotent.
	if err := applyMigrations(s.writeDB, extended); err != nil {
		t.Fatalf("re-apply incremental migrations: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestMigrationFailureRollsBackAtomically(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	base, err := loadEmbeddedMigrations()
	if err != nil {
		t.Fatalf("loadEmbeddedMigrations: %v", err)
	}
	broken := append(append([]Migration(nil), base...),
		Migration{
			Version: 7,
			Name:    "partial_then_fail",
			SQL: `
				CREATE TABLE rollback_probe (id TEXT PRIMARY KEY);
				INSERT INTO nonexistent_table (id) VALUES ('boom');
			`,
		},
	)

	if err := applyMigrations(s.writeDB, broken); err == nil {
		t.Fatal("expected broken migration to fail")
	}

	ver, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if ver != 6 {
		t.Fatalf("expected schema version to stay at 6 after rollback, got %d", ver)
	}

	var probe string
	err = s.ReadDB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='rollback_probe'`).Scan(&probe)
	if err != sql.ErrNoRows {
		t.Fatalf("expected rollback_probe table to be rolled back, got name=%q err=%v", probe, err)
	}
}

func TestMigrationsRejectPreReleaseAndFutureVersions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sumi.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.writeDB.Exec(`UPDATE schema_meta SET version = 5`); err != nil {
		t.Fatalf("downgrade version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := Open(dbPath); err == nil || !strings.Contains(err.Error(), "pre-release") {
		t.Fatalf("expected pre-release version 5 to be rejected, got %v", err)
	}

	// Open raw SQLite to simulate a DB written by a newer binary (version 99).
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := rawDB.Exec(`UPDATE schema_meta SET version = 99`); err != nil {
		_ = rawDB.Close()
		t.Fatalf("set future version: %v", err)
	}
	_ = rawDB.Close()

	if _, err := Open(dbPath); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("expected future version 99 to be rejected, got %v", err)
	}
}

func TestLoadMigrationsValidation(t *testing.T) {
	cases := []struct {
		name  string
		files fstest.MapFS
	}{
		{
			name:  "empty dir",
			files: fstest.MapFS{},
		},
		{
			name: "missing separator",
			files: fstest.MapFS{
				"migrations/0001.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			},
		},
		{
			name: "non-positive version",
			files: fstest.MapFS{
				"migrations/0000_init.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			},
		},
		{
			name: "duplicate version",
			files: fstest.MapFS{
				"migrations/0001_a.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
				"migrations/1_b.sql":    &fstest.MapFile{Data: []byte("SELECT 2;")},
			},
		},
		{
			name: "empty sql",
			files: fstest.MapFS{
				"migrations/0001_empty.sql": &fstest.MapFile{Data: []byte("   \n\t  ")},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadMigrations(tc.files, "migrations"); err == nil {
				t.Fatalf("expected loadMigrations to fail for %s", tc.name)
			}
		})
	}
}

func TestMemoryStoreCloseRemovesTempObjects(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	root := s.Objects().Root()
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("expected temp object root to exist: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("expected temp object root to be removed on Close, got err=%v", err)
	}
}
