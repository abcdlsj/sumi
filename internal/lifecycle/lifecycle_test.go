package lifecycle

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/agenthost"
	"github.com/abcdlsj/sumi/internal/datadir"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
)

func buildTestSumiBinary(t *testing.T, ver string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "sumi-"+ver)
	cmd := exec.Command("go", "build", "-ldflags", "-X github.com/abcdlsj/sumi/internal/version.Version="+ver, "-o", out, "../../cmd/sumi")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sumi binary (%s): %v\n%s", ver, err, string(b))
	}
	return out
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// requireGone guarantees a detached child never outlives the test that spawned it, even when
// the graceful control-socket stop never landed (failed test, removed temp dir, dead socket).
func requireGone(t *testing.T, pid int) {
	t.Helper()
	killCandidate(pid)
	deadline := time.Now().Add(2 * time.Second)
	for ProcessAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ProcessAlive(pid) {
		t.Errorf("detached process %d leaked past cleanup", pid)
	}
}

func TestStageBinaryAndServerHandoffWithBackup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary handoff test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	root := t.TempDir()
	binV1 := buildTestSumiBinary(t, "2.0.1")
	binV2 := buildTestSumiBinary(t, "2.0.2")

	staged1, info1, err := StageBinary(ctx, root, binV1)
	if err != nil {
		t.Fatalf("stage v1: %v", err)
	}
	if ActiveBinaryPath(root) != staged1 {
		t.Fatalf("active binary = %q, want %q", ActiveBinaryPath(root), staged1)
	}

	addr := freeLocalAddr(t)
	dbPath := datadir.DBPath(root)
	st1, err := SpawnDetachedServer(ctx, staged1, root, addr, dbPath)
	if err != nil {
		t.Fatalf("spawn server v1: %v", err)
	}
	srvCtl := NewControlClient(datadir.ServerControlSocket(root))
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		_ = srvCtl.PostJSON(stopCtx, "/stop", nil, nil)
		c()
	}()

	if st1.Version != "2.0.1" || st1.BinaryHash != info1.BinaryHash {
		t.Fatalf("unexpected v1 status: %+v", st1)
	}
	t.Cleanup(func() { requireGone(t, st1.PID) })

	// Stage v2 and hand off the running server seamlessly via TCP FD inheritance.
	staged2, info2, err := StageBinary(ctx, root, binV2)
	if err != nil {
		t.Fatalf("stage v2: %v", err)
	}

	var resp HandoffResponse
	if err := srvCtl.PostJSON(ctx, "/handoff", HandoffRequest{TargetBinary: staged2}, &resp); err != nil {
		t.Fatalf("server handoff failed: %v", err)
	}
	if resp.OldPID != st1.PID || resp.NewPID == 0 || resp.NewPID == st1.PID {
		t.Fatalf("unexpected handoff PIDs: %+v", resp)
	}
	t.Cleanup(func() { requireGone(t, resp.NewPID) })
	if resp.TargetHash != info2.BinaryHash {
		t.Fatalf("handoff hash mismatch: got %q want %q", resp.TargetHash, info2.BinaryHash)
	}
	if resp.BackupPath == "" {
		t.Fatal("expected pre-upgrade SQLite backup path")
	}
	if _, err := os.Stat(resp.BackupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}

	// Verify the new server is answering HTTP on the inherited listener and reports v2.
	httpResp, err := http.Get("http://" + addr + "/api/v1/health")
	if err != nil || httpResp.StatusCode != http.StatusOK {
		t.Fatalf("health after handoff failed: resp=%v err=%v", httpResp, err)
	}
	_ = httpResp.Body.Close()

	var st2 ServerStatus
	if err := srvCtl.GetJSON(ctx, "/status", &st2); err != nil {
		t.Fatalf("status after handoff: %v", err)
	}
	if st2.PID != resp.NewPID || st2.Version != "2.0.2" {
		t.Fatalf("expected v2 server status, got %+v", st2)
	}
}

func TestServerHandoffRollsBackOnBrokenCandidate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary rollback test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root := t.TempDir()
	binV1 := buildTestSumiBinary(t, "2.0.1")
	staged1, _, err := StageBinary(ctx, root, binV1)
	if err != nil {
		t.Fatal(err)
	}
	addr := freeLocalAddr(t)
	st1, err := SpawnDetachedServer(ctx, staged1, root, addr, datadir.DBPath(root))
	if err != nil {
		t.Fatal(err)
	}
	srvCtl := NewControlClient(datadir.ServerControlSocket(root))
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		_ = srvCtl.PostJSON(stopCtx, "/stop", nil, nil)
		c()
	}()
	t.Cleanup(func() { requireGone(t, st1.PID) })

	// Create a fake binary that passes `version --json` probe, then crashes when run as `server`.
	badBin := filepath.Join(t.TempDir(), "bad-sumi")
	script := "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then echo '{\"version\":\"2.0.9\"}'; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(badBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	var resp HandoffResponse
	if err := srvCtl.PostJSON(ctx, "/handoff", HandoffRequest{TargetBinary: badBin}, &resp); err == nil {
		t.Fatalf("expected broken candidate handoff to fail, got %+v", resp)
	}

	// Original server must have rolled back cleanly and keep serving HTTP.
	httpResp, err := http.Get("http://" + addr + "/api/v1/health")
	if err != nil || httpResp.StatusCode != http.StatusOK {
		t.Fatalf("server did not recover after rollback: resp=%v err=%v", httpResp, err)
	}
	_ = httpResp.Body.Close()

	var after ServerStatus
	if err := srvCtl.GetJSON(ctx, "/status", &after); err != nil {
		t.Fatalf("status after rollback: %v", err)
	}
	if after.PID != st1.PID {
		t.Fatalf("expected original PID %d after rollback, got %d", st1.PID, after.PID)
	}
}

func TestComputerHotUpgradePreservesAgentHostSidecar(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary computer hot-upgrade test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	root := t.TempDir()
	binV1 := buildTestSumiBinary(t, "2.0.1")
	binV2 := buildTestSumiBinary(t, "2.0.2")
	staged1, _, err := StageBinary(ctx, root, binV1)
	if err != nil {
		t.Fatal(err)
	}
	staged2, info2, err := StageBinary(ctx, root, binV2)
	if err != nil {
		t.Fatal(err)
	}

	compDir := datadir.ComputerDir(root)
	agentHome := filepath.Join(compDir, "agents", "agent-live-1")
	spec := protocol.AssignedAgentSpec{
		MemberID: "agent-live-1", SpaceID: "space-1", DisplayName: "Lin",
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: domain.DriverConfig{BaseURL: "http://127.0.0.1:1/v1", Model: "m1", APIKey: "sk-x"},
	}

	// Launch a real detached agent-host sidecar to verify it survives `sumi computer` hot upgrade untouched.
	hostClient, hostSt1, err := agenthost.AttachOrSpawn(ctx, agenthost.AttachOptions{
		BinaryPath: staged1,
		AgentHome:  agentHome,
		Spec:       spec,
	})
	if err != nil {
		t.Fatalf("spawn agent-host: %v", err)
	}
	defer func() { _ = hostClient.GracefulClose(2) }()
	defer func() { _ = hostClient.Close() }()
	t.Cleanup(func() { requireGone(t, hostSt1.PID) })

	// Launch detached `sumi computer` v1.
	st1, err := SpawnDetachedComputer(ctx, staged1, root, "http://127.0.0.1:1", "tok-test", compDir)
	if err != nil {
		t.Fatalf("spawn computer v1: %v", err)
	}
	t.Cleanup(func() { requireGone(t, st1.PID) })
	cmpCtl := NewControlClient(datadir.ComputerControlSocket(root))
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		_ = cmpCtl.PostJSON(stopCtx, "/stop?graceful=true", nil, nil)
		c()
	}()

	// Hot-upgrade `sumi computer` from v1 to v2.
	var upResp ComputerUpgradeResponse
	if err := cmpCtl.PostJSON(ctx, "/upgrade", ComputerUpgradeRequest{
		TargetBinary: staged2,
		Mode:         "hot",
	}, &upResp); err != nil {
		t.Fatalf("computer hot upgrade failed: %v", err)
	}
	if upResp.OldPID != st1.PID || upResp.NewPID == 0 || upResp.NewPID == st1.PID {
		t.Fatalf("unexpected computer PIDs: %+v", upResp)
	}
	t.Cleanup(func() { requireGone(t, upResp.NewPID) })
	if upResp.TargetHash != info2.BinaryHash || upResp.Mode != "hot" {
		t.Fatalf("unexpected upgrade response: %+v", upResp)
	}

	// The agent-host sidecar MUST STILL BE THE EXACT SAME PID (never stopped or restarted).
	hostSt2, err := hostClient.Status(ctx)
	if err != nil {
		t.Fatalf("agent-host status after computer upgrade: %v", err)
	}
	if hostSt2.PID != hostSt1.PID {
		t.Fatalf("agent-host PID changed across hot upgrade: %d -> %d", hostSt1.PID, hostSt2.PID)
	}

	var st2 ComputerStatus
	if err := cmpCtl.GetJSON(ctx, "/status", &st2); err != nil {
		t.Fatalf("computer status after upgrade: %v", err)
	}
	if st2.PID != upResp.NewPID || st2.Version != "2.0.2" {
		t.Fatalf("expected v2 computer status, got %+v", st2)
	}
	fmt.Printf("verified hot upgrade: computer %d->%d, agent-host PID %d untouched\n", upResp.OldPID, upResp.NewPID, hostSt2.PID)
}
