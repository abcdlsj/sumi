package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureWorkDir(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureWorkDir(dir); err != nil {
		t.Fatalf("existing dir must pass: %v", err)
	}

	missing := filepath.Join(dir, "gone")
	err := EnsureWorkDir(missing)
	if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing dir must be named in the error, got %v", err)
	}

	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureWorkDir(file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("regular file must be rejected, got %v", err)
	}
}
