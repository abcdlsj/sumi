package computer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

func newTestAgentHome(t *testing.T) *hostedAgent {
	t.Helper()
	home := t.TempDir()
	return &hostedAgent{
		homeDir:   home,
		workDir:   filepath.Join(home, "workspace"),
		memoryDir: filepath.Join(home, "memory"),
	}
}

func TestEnsureAgentHomeRebuildsDeletedWorkspace(t *testing.T) {
	ag := newTestAgentHome(t)
	if _, err := ensureAgentHome(ag); err != nil {
		t.Fatalf("fresh home: %v", err)
	}
	memFile := filepath.Join(ag.memoryDir, "MEMORY.md")
	if _, err := os.Stat(memFile); err != nil {
		t.Fatalf("MEMORY.md must be seeded: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ag.workDir, "notes.md"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	// An externally removed workspace must come back with the documented 0700 mode.
	if err := os.RemoveAll(ag.workDir); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := ensureAgentHome(ag)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !rebuilt {
		t.Fatal("a missing work dir must report a rebuild")
	}
	fi, err := os.Stat(ag.workDir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("work dir must exist again: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("work dir mode = %o, want 700", perm)
	}

	// Steady state must not report a rebuild nor clobber existing memory.
	if err := os.WriteFile(memFile, []byte("remember"), 0o600); err != nil {
		t.Fatal(err)
	}
	rebuilt, err = ensureAgentHome(ag)
	if err != nil || rebuilt {
		t.Fatalf("intact home must be a no-op, rebuilt=%v err=%v", rebuilt, err)
	}
	if b, _ := os.ReadFile(memFile); string(b) != "remember" {
		t.Fatalf("MEMORY.md must be left alone, got %q", b)
	}
}

func TestProvisionRebuildsMissingWorkspaceBeforeDriverStart(t *testing.T) {
	d := NewDaemon(Config{
		DataDir: t.TempDir(),
		DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			return &countingDriver{}, nil
		},
	}, nil)
	ag := d.provisionAgent(builtinSpec("m1"))
	if err := os.RemoveAll(ag.workDir); err != nil {
		t.Fatal(err)
	}
	d.provisionAgent(builtinSpec("m2"))
	if fi, err := os.Stat(ag.workDir); err != nil || !fi.IsDir() {
		t.Fatalf("re-provision must recreate the work dir: %v", err)
	}
}
