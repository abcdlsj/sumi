package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveDraftSweepsExpiredDrafts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SUMI_DRAFT_DIR", dir)

	stale := draft{
		ConversationID: "conv-stale",
		BodyMarkdown:   "old",
		IdempotencyKey: "k-stale",
		SavedAt:        time.Now().UTC().Add(-draftTTL - time.Minute),
	}
	if _, err := saveDraft(stale); err != nil {
		t.Fatalf("save stale: %v", err)
	}

	fresh := draft{
		ConversationID: "conv-fresh",
		BodyMarkdown:   "new",
		IdempotencyKey: "k-fresh",
		SavedAt:        time.Now().UTC(),
	}
	if _, err := saveDraft(fresh); err != nil {
		t.Fatalf("save fresh: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "conv-stale.json")); !os.IsNotExist(err) {
		t.Fatalf("expected expired draft to be swept, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "conv-fresh.json")); err != nil {
		t.Fatalf("expected fresh draft to remain, err=%v", err)
	}
}
