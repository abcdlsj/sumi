package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOpenSchemaVersionCheckAndWALConcurrency(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sumi.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	ctx := context.Background()
	spaceID := uuid.NewString()
	ownerID := uuid.NewString()
	userID := uuid.NewString()
	now := FormatTime(time.Now())

	err = s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, email_normalized, password_hash, display_name, created_at) VALUES (?, ?, ?, ?, ?)`,
			userID, "owner@example.com", "hash", "Owner", now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO spaces (id, slug, name, owner_member_id, created_at) VALUES (?, ?, ?, ?, ?)`,
			spaceID, "studio", "Studio", ownerID, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO members (id, space_id, user_id, kind, display_name, access_level, created_at) VALUES (?, ?, ?, 'human', 'Owner', 'owner', ?)`,
			ownerID, spaceID, userID, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed space: %v", err)
	}

	// Concurrent writers and readers must succeed cleanly without SQLITE_BUSY.
	var wg sync.WaitGroup
	errCh := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := s.WithWriteTx(ctx, func(tx *sql.Tx) error {
				_, err := AppendEvent(ctx, tx, spaceID, "test.tick", map[string]int{"i": idx})
				return err
			})
			if err != nil {
				errCh <- err
			}
		}(i)
	}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var count int
			if err := s.ReadDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE space_id = ?`, spaceID).Scan(&count); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent access failed: %v", err)
	}

	var eventCount int
	if err := s.ReadDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE space_id = ?`, spaceID).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount != 16 {
		t.Fatalf("expected 16 events, got %d", eventCount)
	}

	// Bump schema version to simulate incompatible DB and verify Open rejects it.
	if err := s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE schema_meta SET version = 99`)
		return err
	}); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := Open(dbPath); err == nil {
		t.Fatalf("expected Open to reject unsupported schema version 99")
	}
}

func TestTaskMultiplePerSourceMessageAndLazyConversation(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := FormatTime(time.Now())
	userID, spaceID, memberID, chanID, msgID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()

	err = s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO users (id, email_normalized, password_hash, display_name, created_at) VALUES (?, 'a@b.com', 'h', 'Alice', ?)`, userID, now)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO spaces (id, slug, name, owner_member_id, created_at) VALUES (?, 's', 'S', ?, ?)`, spaceID, memberID, now)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO members (id, space_id, user_id, kind, display_name, access_level, created_at) VALUES (?, ?, ?, 'human', 'Alice', 'owner', ?)`, memberID, spaceID, userID, now)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO conversations (id, space_id, kind, slug, created_at) VALUES (?, ?, 'public', 'general', ?)`, chanID, spaceID, now)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO messages (id, space_id, conversation_id, seq, author_member_id, kind, body_markdown, created_at) VALUES (?, ?, ?, 1, ?, 'text', 'Split this work', ?)`, msgID, spaceID, chanID, memberID, now)
		if err != nil {
			return err
		}
		// Two tasks anchored to the same source_message_id with NULL conversation_id (lazy).
		for i := 1; i <= 2; i++ {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO tasks (id, space_id, number, channel_id, source_message_id, title, status, creator_member_id, assignee_member_id, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, 'Work item', 'todo', ?, ?, ?, ?)`,
				uuid.NewString(), spaceID, i, chanID, msgID, memberID, memberID, now, now)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert multiple tasks for one message: %v", err)
	}
}
