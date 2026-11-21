package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migration is a single versioned forward SQL migration.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

func loadEmbeddedMigrations() ([]Migration, error) {
	return loadMigrations(migrationsFS, "migrations")
}

func loadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	seen := make(map[int]string, len(entries))
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		version, name, err := parseMigrationFilename(filename)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("duplicate migration version %d (%q and %q)", version, prev, filename)
		}
		seen[version] = filename

		raw, err := fs.ReadFile(fsys, path.Join(dir, filename))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", filename, err)
		}
		sqlText := strings.TrimSpace(string(raw))
		if sqlText == "" {
			return nil, fmt.Errorf("migration %q is empty", filename)
		}
		migrations = append(migrations, Migration{
			Version: version,
			Name:    name,
			SQL:     sqlText,
		})
	}
	if len(migrations) == 0 {
		return nil, errors.New("no SQL migrations found")
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	return migrations, nil
}

func parseMigrationFilename(filename string) (int, string, error) {
	if !strings.HasSuffix(filename, ".sql") {
		return 0, "", fmt.Errorf("invalid migration filename %q: must end with .sql", filename)
	}
	stem := strings.TrimSuffix(filename, ".sql")
	prefix, name, ok := strings.Cut(stem, "_")
	if !ok || prefix == "" || name == "" {
		return 0, "", fmt.Errorf("invalid migration filename %q: expected <version>_<name>.sql", filename)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("invalid migration version in %q: must be a positive integer", filename)
	}
	return version, name, nil
}

// applyMigrations upgrades db forward through any pending migrations.
// Each migration runs atomically inside its own transaction and records its version in schema_meta.
func applyMigrations(db *sql.DB, migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("no SQL migrations provided")
	}
	baseline := migrations[0].Version
	latest := migrations[len(migrations)-1].Version

	current, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if current > latest {
		return fmt.Errorf("database schema version %d is newer than supported (%d); upgrade binary", current, latest)
	}
	if current > 0 && current < baseline {
		return fmt.Errorf("unsupported pre-release schema version %d (minimum supported baseline is %d); rebuild empty database", current, baseline)
	}

	applied := 0
	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		if err := applyOneMigration(db, m); err != nil {
			return fmt.Errorf("apply migration %04d_%s: %w", m.Version, m.Name, err)
		}
		applied++
	}
	if applied > 0 {
		if err := verifyForeignKeys(db); err != nil {
			return err
		}
	}
	return nil
}

func readSchemaVersion(db *sql.DB) (int, error) {
	var tableName string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='schema_meta'`).Scan(&tableName)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("check schema_meta: %w", err)
	}

	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_meta`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

func applyOneMigration(db *sql.DB, m Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_meta (
		version INTEGER PRIMARY KEY CHECK (version > 0),
		applied_at TEXT NOT NULL
	);`); err != nil {
		return fmt.Errorf("ensure schema_meta: %w", err)
	}
	if _, err := tx.Exec(m.SQL); err != nil {
		return fmt.Errorf("exec sql: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_meta (version, applied_at) VALUES (?, ?)`,
		m.Version, FormatTime(time.Now().UTC()),
	); err != nil {
		return fmt.Errorf("record schema_meta: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func verifyForeignKeys(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA foreign_key_check;`)
	if err != nil {
		return fmt.Errorf("foreign key check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var tbl, parent string
		var rowid sql.NullInt64
		var fkid int
		_ = rows.Scan(&tbl, &rowid, &parent, &fkid)
		return fmt.Errorf("foreign key violation after migration in table %q (parent %q)", tbl, parent)
	}
	return rows.Err()
}
