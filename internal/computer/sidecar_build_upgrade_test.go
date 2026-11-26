package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/agenthost"
	"github.com/abcdlsj/sumi/internal/driver"
)

// gatedSidecarDriver holds every turn open until the gate file exists, standing in for a
// long turn that is still running when Sumi is hot-upgraded.
type gatedSidecarDriver struct{ gate string }

func (g gatedSidecarDriver) RunTurn(ctx context.Context, _ driver.TurnInput) (driver.TurnResult, error) {
	for {
		if _, err := os.Stat(g.gate); err == nil {
			return driver.TurnResult{FinalText: "ok"}, nil
		}
		select {
		case <-ctx.Done():
			return driver.TurnResult{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func (gatedSidecarDriver) ResetSession() error { return nil }
func (gatedSidecarDriver) Close() error        { return nil }

// writeGatedSumiBinary writes the fake `sumi` wrapper at path. Changing build changes the file's
// bytes, and so the build hash, the way `make upgrade` replaces the real binary in place.
func writeGatedSumiBinary(t *testing.T, path, gate, build string) {
	t.Helper()
	body := fmt.Sprintf("#!/bin/sh\n# build %s\nSUMI_TEST_AGENTHOST=1 SUMI_TEST_AGENTHOST_BIN=%q SUMI_TEST_AGENTHOST_GATE=%q exec %q -test.run=^TestAgentHostHelperProcess$ -- \"$@\"\n", build, path, gate, os.Args[0])
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	// Bump mtime explicitly so the size+mtime hash cache can never serve the old build's hash.
	future := time.Now().Add(time.Duration(len(build)) * time.Second)
	_ = os.Chtimes(path, future, future)
}

// TestSidecarBusyAtUpgradeIsReplacedAfterItsTurnEnds covers an Agent mid-turn during a hot
// upgrade: the new Computer re-attaches without interrupting the turn, then the next loop
// iteration replaces the sidecar with one spawned from the new binary.
func TestSidecarBusyAtUpgradeIsReplacedAfterItsTurnEnds(t *testing.T) {
	dataDir := t.TempDir()
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "sumi")
	gate := filepath.Join(binDir, "gate")
	spec := builtinSpec("m1")
	agentHome := filepath.Join(dataDir, "agents", spec.MemberID)

	// Old Computer: spawn a sidecar from build "old" and start a long turn on it.
	writeGatedSumiBinary(t, bin, gate, "old")
	oldHash, err := agenthost.BinaryHash(bin)
	if err != nil {
		t.Fatal(err)
	}
	oldHC, oldSt, err := agenthost.AttachOrSpawn(context.Background(), agenthost.AttachOptions{
		BinaryPath: bin, AgentHome: agentHome, Spec: spec,
	})
	if err != nil {
		t.Fatalf("spawn old sidecar: %v", err)
	}
	if oldSt.BuildHash != oldHash {
		t.Fatalf("old sidecar build hash = %q, want %q", oldSt.BuildHash, oldHash)
	}
	oldPID := oldSt.PID
	t.Cleanup(func() { _ = syscall.Kill(oldPID, syscall.SIGKILL) })
	turnDone := make(chan error, 1)
	go func() {
		_, err := oldHC.RunTurnWithID(context.Background(), "turn-at-upgrade", "conv-1", driver.TurnInput{UserPrompt: "long"})
		turnDone <- err
	}()
	waitFor(t, func() bool {
		st, err := oldHC.Status(context.Background())
		return err == nil && st.Busy
	})

	// Upgrade replaces the binary in place; a new Computer starts and provisions the Agent.
	writeGatedSumiBinary(t, bin, gate, "new-build")
	newHash, err := agenthost.BinaryHash(bin)
	if err != nil {
		t.Fatal(err)
	}
	if newHash == oldHash {
		t.Fatal("test setup: new build hash equals old")
	}

	var inboxCalls atomic.Int32
	inbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		inboxCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	defer inbox.Close()
	d := NewDaemon(Config{DataDir: dataDir, UseSidecar: true, SumiBinary: bin, ServerURL: inbox.URL}, nil)
	t.Cleanup(d.Stop)

	d.provisionAndWake(context.Background(), spec, "provisioned")
	ag := d.agents[spec.MemberID]
	ag.mu.Lock()
	hc, _ := ag.drv.(*agenthost.Client)
	stale := ag.staleBuild
	ag.mu.Unlock()
	if hc == nil {
		t.Fatalf("expected sidecar client, got %T (code=%q)", ag.drv, ag.failCode)
	}
	if !stale {
		t.Fatal("busy sidecar on the old build was not flagged stale")
	}
	st, err := hc.Status(context.Background())
	if err != nil || st.PID != oldPID || !st.Busy || st.TurnID != "turn-at-upgrade" {
		t.Fatalf("running turn was disturbed by the upgrade: st=%+v err=%v", st, err)
	}

	// The turn finishes normally on the old sidecar.
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatalf("turn running across the upgrade failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn running across the upgrade never finished")
	}

	// The next loop iteration replaces the sidecar with one on the new build.
	var newSt agenthost.StatusResponse
	waitFor(t, func() bool {
		ag.mu.Lock()
		cur, _ := ag.drv.(*agenthost.Client)
		busy := ag.busy
		ag.mu.Unlock()
		if cur == nil || busy {
			return false
		}
		s, err := cur.Status(context.Background())
		newSt = s
		return err == nil && s.PID != oldPID
	})
	if newSt.BuildHash != newHash {
		t.Fatalf("replacement sidecar build hash = %q, want new build %q", newSt.BuildHash, newHash)
	}
	waitFor(t, func() bool { return syscall.Kill(oldPID, 0) != nil })
	ag.mu.Lock()
	stale = ag.staleBuild
	ag.mu.Unlock()
	if stale {
		t.Fatal("staleBuild still set after replacement")
	}

	// A later wake on the same build keeps the sidecar.
	calls := inboxCalls.Load()
	d.scheduleWake(context.Background(), spec.MemberID, "", "dm")
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy && inboxCalls.Load() > calls
	})
	ag.mu.Lock()
	cur, _ := ag.drv.(*agenthost.Client)
	ag.mu.Unlock()
	again, err := cur.Status(context.Background())
	if err != nil || again.PID != newSt.PID {
		t.Fatalf("same-build sidecar restarted on a later wake: st=%+v err=%v (want pid %d)", again, err, newSt.PID)
	}
	t.Cleanup(func() { _ = syscall.Kill(newSt.PID, syscall.SIGKILL) })
}
