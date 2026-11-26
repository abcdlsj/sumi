package agenthost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

// releaseFile, when created in the agent home, unblocks a helper sidecar turn prompted "block".
const releaseFile = "release-turn"

type helperDriver struct{ home string }

func (h helperDriver) RunTurn(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error) {
	if in.UserPrompt == "block" {
		for {
			if _, err := os.Stat(filepath.Join(h.home, releaseFile)); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return driver.TurnResult{}, ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	return driver.TurnResult{FinalText: "done:" + in.UserPrompt}, nil
}
func (helperDriver) ResetSession() error { return nil }
func (helperDriver) Close() error        { return nil }

// TestAgentHostBuildHelperProcess is a real out-of-process sidecar launched by fakeBuild scripts.
// It reports the hash of the script that launched it, as `sumi agent-host` reports its own binary.
func TestAgentHostBuildHelperProcess(t *testing.T) {
	if os.Getenv("SUMI_TEST_AGENTHOST") != "1" {
		return
	}
	var sock, meta string
	for i := 0; i < len(os.Args)-1; i++ {
		switch os.Args[i] {
		case "--socket":
			sock = os.Args[i+1]
		case "--meta":
			meta = os.Args[i+1]
		}
	}
	buildHash, _ := BinaryHash(os.Getenv("SUMI_TEST_AGENTHOST_BIN"))
	srv := NewServer(ServerConfig{
		SocketPath: sock,
		MetaPath:   meta,
		BuildHash:  buildHash,
		DriverBuilder: func(_ protocol.AssignedAgentSpec, home string) (driver.SessionDriver, error) {
			return helperDriver{home: home}, nil
		},
	})
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start helper agenthost: %v\n", err)
		os.Exit(2)
	}
	<-srv.Done()
	os.Exit(0)
}

// fakeBuild writes a distinct `sumi` executable per build ID; different IDs hash differently.
func fakeBuild(t *testing.T, buildID string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "sumi")
	body := fmt.Sprintf("#!/bin/sh\n# build %s\nSUMI_TEST_AGENTHOST=1 SUMI_TEST_AGENTHOST_BIN=%q exec %q -test.run=^TestAgentHostBuildHelperProcess$ -- \"$@\"\n", buildID, script, os.Args[0])
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

func mustHash(t *testing.T, path string) string {
	t.Helper()
	h, err := BinaryHash(path)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func attach(t *testing.T, home, bin string) (*Client, StatusResponse) {
	t.Helper()
	c, st, err := AttachOrSpawn(context.Background(), AttachOptions{
		BinaryPath: bin, AgentHome: home, Spec: testSpec("m1"),
	})
	if err != nil {
		t.Fatalf("AttachOrSpawn(%s): %v", bin, err)
	}
	t.Cleanup(func() {
		if syscall.Kill(st.PID, 0) == nil {
			terminateProcess(st.PID)
		}
	})
	return c, st
}

func TestIdleSidecarOnOldBuildIsReplacedWithNewBuild(t *testing.T) {
	home := t.TempDir()
	oldBin, newBin := fakeBuild(t, "old"), fakeBuild(t, "new")

	_, st1 := attach(t, home, oldBin)
	if st1.BuildHash != mustHash(t, oldBin) {
		t.Fatalf("sidecar build hash = %q, want hash of old build", st1.BuildHash)
	}

	_, st2 := attach(t, home, newBin)
	if st2.PID == st1.PID {
		t.Fatalf("idle sidecar on old build was reused (pid %d)", st1.PID)
	}
	if st2.BuildHash != mustHash(t, newBin) || st2.BuildHash == st1.BuildHash {
		t.Fatalf("replacement build hash = %q, want hash of new build %q", st2.BuildHash, mustHash(t, newBin))
	}
	if !waitProcessGone(st1.PID, 3*time.Second) {
		t.Fatalf("old sidecar pid %d still running after replacement", st1.PID)
	}
}

func TestBusySidecarOnOldBuildKeepsTurnThenIsReplacedWhenIdle(t *testing.T) {
	home := t.TempDir()
	oldBin, newBin := fakeBuild(t, "old"), fakeBuild(t, "new")

	c1, st1 := attach(t, home, oldBin)
	turnDone := make(chan error, 1)
	var res driver.TurnResult
	go func() {
		var err error
		res, err = c1.RunTurnWithID(context.Background(), "turn-long", "conv-1", driver.TurnInput{UserPrompt: "block", WorkDir: home})
		turnDone <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, err := c1.Status(context.Background())
		if err == nil && st.Busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sidecar never became busy")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Upgrade while busy: re-attach to the same process, never interrupt the turn.
	_, st2 := attach(t, home, newBin)
	if st2.PID != st1.PID || !st2.Busy || st2.TurnID != "turn-long" {
		t.Fatalf("busy sidecar not re-attached as-is: before pid=%d, after=%+v", st1.PID, st2)
	}

	if err := os.WriteFile(filepath.Join(home, releaseFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-turnDone:
		if err != nil || res.FinalText != "done:block" {
			t.Fatalf("in-flight turn interrupted: res=%+v err=%v", res, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight turn did not finish")
	}

	// Next attach after the turn ends swaps in the new build.
	_, st3 := attach(t, home, newBin)
	if st3.PID == st1.PID || st3.BuildHash != mustHash(t, newBin) {
		t.Fatalf("idle sidecar not replaced after turn: before pid=%d, after=%+v", st1.PID, st3)
	}
}

func TestSidecarOnSameBuildIsReused(t *testing.T) {
	home := t.TempDir()
	bin := fakeBuild(t, "same")

	_, st1 := attach(t, home, bin)
	_, st2 := attach(t, home, bin)
	if st2.PID != st1.PID {
		t.Fatalf("same-build sidecar restarted: pid %d -> %d", st1.PID, st2.PID)
	}
}
