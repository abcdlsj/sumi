package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/agenthost"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

type stubSidecarDriver struct{}

func (stubSidecarDriver) RunTurn(context.Context, driver.TurnInput) (driver.TurnResult, error) {
	return driver.TurnResult{FinalText: "ok"}, nil
}
func (stubSidecarDriver) ResetSession() error { return nil }
func (stubSidecarDriver) Close() error        { return nil }

// TestAgentHostHelperProcess serves a lightweight agent-host sidecar when invoked via fakeSumiBinary.
func TestAgentHostHelperProcess(t *testing.T) {
	if os.Getenv("SUMI_TEST_AGENTHOST") != "1" {
		return
	}
	var sock, meta string
	args := os.Args
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--socket":
			sock = args[i+1]
		case "--meta":
			meta = args[i+1]
		}
	}
	// Report the wrapper script's hash as the build identity, as a real sidecar reports the
	// hash of the binary it was spawned from, so unchanged builds are re-attached.
	buildHash, _ := agenthost.BinaryHash(os.Getenv("SUMI_TEST_AGENTHOST_BIN"))
	srv := agenthost.NewServer(agenthost.ServerConfig{
		SocketPath: sock,
		MetaPath:   meta,
		BuildHash:  buildHash,
		DriverBuilder: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			if gate := os.Getenv("SUMI_TEST_AGENTHOST_GATE"); gate != "" {
				return gatedSidecarDriver{gate: gate}, nil
			}
			return stubSidecarDriver{}, nil
		},
	})
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start helper agenthost: %v\n", err)
		os.Exit(2)
	}
	<-srv.Done()
	os.Exit(0)
}

func fakeSumiBinary(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "sumi")
	body := fmt.Sprintf("#!/bin/sh\nSUMI_TEST_AGENTHOST=1 SUMI_TEST_AGENTHOST_BIN=%q exec %q -test.run=^TestAgentHostHelperProcess$ -- \"$@\"\n", script, os.Args[0])
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

func killSidecarHard(t *testing.T, pid int, sockPath string) {
	t.Helper()
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		waitFor(t, func() bool { return syscall.Kill(pid, 0) != nil })
	}
	_ = os.Remove(sockPath)
}

func TestSidecarReprovisionKeepsReplacementAliveAndRecoversDeadSidecar(t *testing.T) {
	d := NewDaemon(Config{
		DataDir:    t.TempDir(),
		UseSidecar: true,
		SumiBinary: fakeSumiBinary(t),
	}, nil)
	t.Cleanup(d.Stop)

	// 1. Initial provision spawns sidecar #1.
	ag := d.provisionAgent(builtinSpec("m1"))
	hc1, ok := ag.drv.(*agenthost.Client)
	if !ok || hc1 == nil {
		t.Fatalf("expected *agenthost.Client, got %T (code=%q detail=%q)", ag.drv, ag.failCode, ag.failDetail)
	}
	st1, err := hc1.Status(context.Background())
	if err != nil || st1.PID <= 0 {
		t.Fatalf("sidecar 1 status: st=%+v err=%v", st1, err)
	}

	// 2. Re-provision with a new spec must replace sidecar #1 and NEVER shut down sidecar #2.
	d.provisionAgent(builtinSpec("m2"))
	hc2, ok := ag.drv.(*agenthost.Client)
	if !ok || hc2 == nil {
		t.Fatalf("expected *agenthost.Client after reprovision, got %T (code=%q detail=%q)", ag.drv, ag.failCode, ag.failDetail)
	}
	// Give any async cleanup goroutine time to fire so a stale old.Close() would reproduce.
	time.Sleep(200 * time.Millisecond)

	st2, err := hc2.Status(context.Background())
	if err != nil {
		t.Fatalf("replacement sidecar unreachable after reprovision: %v", err)
	}
	if st2.PID == st1.PID {
		t.Fatalf("expected new sidecar PID after spec change, still %d", st2.PID)
	}
	if _, err := hc2.RunTurnWithID(context.Background(), "turn-after-reprovision", "conv-1", driver.TurnInput{
		UserPrompt: "ping",
		WorkDir:    ag.workDir,
	}); err != nil {
		t.Fatalf("turn after reprovision failed: %v", err)
	}

	// 3. If the sidecar dies externally, Restart respawns it cleanly.
	sockPath := agenthost.SocketPath(ag.homeDir)
	killSidecarHard(t, st2.PID, sockPath)
	d.handleControl(context.Background(), protocol.AgentControlFrame{
		Action:        "restart",
		AgentMemberID: ag.spec.MemberID,
	})
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy
	})
	ag.mu.Lock()
	hc3, _ := ag.drv.(*agenthost.Client)
	ag.mu.Unlock()
	if hc3 == nil {
		t.Fatalf("expected sidecar client after restart")
	}
	st3, err := hc3.Status(context.Background())
	if err != nil || st3.PID == st2.PID {
		t.Fatalf("restart did not respawn healthy sidecar: st=%+v err=%v", st3, err)
	}

	// 4. If the sidecar dies externally, a reconnect provision with the same spec also respawns it.
	killSidecarHard(t, st3.PID, sockPath)
	d.provisionAgent(builtinSpec("m2"))
	ag.mu.Lock()
	hc4, _ := ag.drv.(*agenthost.Client)
	ag.mu.Unlock()
	st4, err := hc4.Status(context.Background())
	if err != nil || st4.PID == st3.PID {
		t.Fatalf("reconnect provision did not respawn dead sidecar: st=%+v err=%v", st4, err)
	}

	// 5. If the sidecar dies mid-flight before a turn dials host.sock, runAgentLoop recovers automatically.
	var served int
	inboxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		items := []domain.InboxItem{}
		if served == 1 {
			items = []domain.InboxItem{{ConversationID: "conv-1", Reason: "dm"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	}))
	defer inboxSrv.Close()
	d.cfg.ServerURL = inboxSrv.URL

	killSidecarHard(t, st4.PID, sockPath)
	d.scheduleWake(context.Background(), ag.spec.MemberID, "conv-1", "dm")
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy && served >= 1
	})
	ag.mu.Lock()
	hc5, _ := ag.drv.(*agenthost.Client)
	failStreak := ag.failStreak
	ag.mu.Unlock()
	if failStreak != 0 {
		t.Fatalf("expected turn to self-heal dead sidecar without failure, failStreak=%d", failStreak)
	}
	st5, err := hc5.Status(context.Background())
	if err != nil || st5.PID == st4.PID {
		t.Fatalf("runAgentLoop did not respawn dead sidecar: st=%+v err=%v", st5, err)
	}
	// Sanity-check that PID string formatting/parsing wasn't empty.
	if strings.TrimSpace(strconv.Itoa(st5.PID)) == "" {
		t.Fatal("invalid pid")
	}
}
