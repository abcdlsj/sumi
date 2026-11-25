package agenthost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/driver/acp"
	pidriver "github.com/abcdlsj/sumi/internal/driver/pi"
	"github.com/abcdlsj/sumi/internal/protocol"
)

type gatedDriver struct {
	started  chan struct{}
	release  chan struct{}
	canceled atomic.Bool
	closed   atomic.Bool
	resets   atomic.Int32
}

func (g *gatedDriver) RunTurn(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error) {
	in.Emit(driver.Activity{ToolCallID: "t1", Tool: "read"})
	select {
	case g.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		g.canceled.Store(true)
		return driver.TurnResult{}, ctx.Err()
	case <-g.release:
	}
	in.Emit(driver.Activity{ToolCallID: "t1", Tool: "read", Done: true})
	return driver.TurnResult{ToolCalls: 1}, nil
}

func (g *gatedDriver) ResetSession() error { g.resets.Add(1); return nil }
func (g *gatedDriver) Close() error        { g.closed.Store(true); return nil }

func testSpec(model string) protocol.AssignedAgentSpec {
	return protocol.AssignedAgentSpec{
		MemberID: "agent-1", SpaceID: "space-1", DisplayName: "Lin",
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: domain.DriverConfig{BaseURL: "http://127.0.0.1:1/v1", Model: model, APIKey: "sk-test"},
	}
}

func TestTurnSurvivesClientDetachAndReattach(t *testing.T) {
	home := t.TempDir()
	sock := SocketPath(home)
	meta := MetaPath(home)
	gd := &gatedDriver{started: make(chan struct{}, 1), release: make(chan struct{})}

	srv := NewServer(ServerConfig{
		SocketPath:  sock,
		MetaPath:    meta,
		HostVersion: "2.1.0",
		DriverBuilder: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			return gd, nil
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srv.Close(false)

	c1 := NewClient(sock)
	if err := c1.Configure(context.Background(), testSpec("m1"), home); err != nil {
		t.Fatalf("configure: %v", err)
	}

	// Client 1 starts a turn, observes initial activity, then detaches (simulating Computer upgrade).
	turnCtx, cancel := context.WithCancelCause(context.Background())
	var act1Mu sync.Mutex
	var act1 []driver.Activity
	c1Done := make(chan error, 1)
	go func() {
		_, err := c1.RunTurnWithID(turnCtx, "turn-1", "conv-9", driver.TurnInput{
			UserPrompt: "work",
			WorkDir:    home,
			OnActivity: func(a driver.Activity) {
				act1Mu.Lock()
				act1 = append(act1, a)
				act1Mu.Unlock()
			},
		})
		c1Done <- err
	}()

	<-gd.started
	cancel(ErrDetaching)
	c1.Detach()

	select {
	case err := <-c1Done:
		if !errors.Is(err, ErrDetaching) {
			t.Fatalf("expected ErrDetaching, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client 1 did not return after detach")
	}

	// Underlying driver must NOT be cancelled on detach.
	time.Sleep(50 * time.Millisecond)
	if gd.canceled.Load() {
		t.Fatal("detach must never cancel in-flight turn inside sidecar")
	}

	// Client 2 (new upgraded Computer daemon) connects, sees busy turn, and re-attaches.
	c2 := NewClient(sock)
	st, err := c2.Status(context.Background())
	if err != nil {
		t.Fatalf("client 2 status: %v", err)
	}
	if !st.Busy || st.TurnID != "turn-1" || st.FocusConvID != "conv-9" {
		t.Fatalf("unexpected sidecar status: %+v", st)
	}

	var act2 []driver.Activity
	waitDone := make(chan struct {
		res driver.TurnResult
		err error
	}, 1)
	go func() {
		res, err := c2.WaitTurn(context.Background(), st.TurnID, func(a driver.Activity) {
			act2 = append(act2, a)
		})
		waitDone <- struct {
			res driver.TurnResult
			err error
		}{res, err}
	}()

	time.Sleep(30 * time.Millisecond)
	close(gd.release)

	select {
	case out := <-waitDone:
		if out.err != nil || out.res.ToolCalls != 1 {
			t.Fatalf("client 2 wait turn failed: res=%+v err=%v", out.res, out.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client 2 did not receive turn completion")
	}
	if len(act2) == 0 || !act2[len(act2)-1].Done {
		t.Fatalf("expected completion activity on re-attached client, got %+v", act2)
	}
}

func TestCompletedTurnResultAvailableAfterDetachWindow(t *testing.T) {
	home := t.TempDir()
	sock := SocketPath(home)
	gd := &gatedDriver{started: make(chan struct{}, 1), release: make(chan struct{})}

	srv := NewServer(ServerConfig{
		SocketPath: sock,
		MetaPath:   filepath.Join(home, "host.json"),
		DriverBuilder: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			return gd, nil
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close(false)

	c1 := NewClient(sock)
	if err := c1.Configure(context.Background(), testSpec("m1"), home); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c1.RunTurnWithID(ctx, "turn-quick", "conv-1", driver.TurnInput{UserPrompt: "go", WorkDir: home})
		done <- err
	}()
	<-gd.started
	cancel(ErrDetaching)
	<-done

	// Turn completes while NO computer daemon is connected.
	close(gd.release)
	deadline := time.Now().Add(2 * time.Second)
	c2 := NewClient(sock)
	for time.Now().Before(deadline) {
		st, err := c2.Status(context.Background())
		if err == nil && !st.Busy && st.LastCompleted != nil && st.LastCompleted.TurnID == "turn-quick" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	res, err := c2.WaitTurn(context.Background(), "turn-quick", nil)
	if err != nil || res.ToolCalls != 1 {
		t.Fatalf("expected cached completed turn result, got res=%+v err=%v", res, err)
	}
}

func TestExplicitCancelCancelsUnderlyingDriver(t *testing.T) {
	home := t.TempDir()
	sock := SocketPath(home)
	gd := &gatedDriver{started: make(chan struct{}, 1), release: make(chan struct{})}

	srv := NewServer(ServerConfig{
		SocketPath: sock,
		DriverBuilder: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			return gd, nil
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Close(false)

	c := NewClient(sock)
	if err := c.Configure(context.Background(), testSpec("m1"), home); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.RunTurnWithID(ctx, "turn-cancel", "conv-1", driver.TurnInput{UserPrompt: "go", WorkDir: home})
		done <- err
	}()
	<-gd.started
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after explicit cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel timed out")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !gd.canceled.Load() {
		time.Sleep(15 * time.Millisecond)
	}
	if !gd.canceled.Load() {
		t.Fatal("explicit cancel must cancel underlying driver context")
	}
}

func TestDefaultDriverBuilderRoutesPiToNativeRPC(t *testing.T) {
	binDir := t.TempDir()
	fakePi := filepath.Join(binDir, "pi")
	fakeWrapper := filepath.Join(binDir, "ak")
	fakeACP := filepath.Join(binDir, "custom-acp")
	for _, p := range []string{fakePi, fakeWrapper, fakeACP} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)

	home := t.TempDir()
	piSpec := protocol.AssignedAgentSpec{
		MemberID:     "agent-pi",
		SpaceID:      "space-1",
		DisplayName:  "Piper",
		DriverKind:   domain.DriverACP,
		DriverConfig: domain.DriverConfig{Provider: domain.ACPProviderPi},
	}
	drv, err := DefaultDriverBuilder(piSpec, home)
	if err != nil {
		t.Fatalf("build pi driver: %v", err)
	}
	defer drv.Close()
	if _, ok := drv.(*pidriver.Driver); !ok {
		t.Fatalf("expected *pi.Driver for provider=pi, got %T", drv)
	}

	piWrappedSpec := protocol.AssignedAgentSpec{
		MemberID:     "agent-pi-ak",
		SpaceID:      "space-1",
		DisplayName:  "Cato",
		DriverKind:   domain.DriverACP,
		DriverConfig: domain.DriverConfig{Provider: domain.ACPProviderPi, Command: []string{"ak", "opendesign-pi"}},
	}
	drvWrapped, err := DefaultDriverBuilder(piWrappedSpec, home)
	if err != nil {
		t.Fatalf("build wrapped pi driver: %v", err)
	}
	defer drvWrapped.Close()
	if _, ok := drvWrapped.(*pidriver.Driver); !ok {
		t.Fatalf("expected *pi.Driver for provider=pi with wrapper command, got %T", drvWrapped)
	}

	customSpec := protocol.AssignedAgentSpec{
		MemberID:     "agent-custom",
		SpaceID:      "space-1",
		DisplayName:  "Custom",
		DriverKind:   domain.DriverACP,
		DriverConfig: domain.DriverConfig{Command: []string{fakeACP}},
	}
	drv2, err := DefaultDriverBuilder(customSpec, home)
	if err != nil {
		t.Fatalf("build custom acp driver: %v", err)
	}
	defer drv2.Close()
	if _, ok := drv2.(*acp.Driver); !ok {
		t.Fatalf("expected *acp.Driver for custom command, got %T", drv2)
	}
}

func TestShutdownIgnoresMismatchedPID(t *testing.T) {
	home := t.TempDir()
	sock := SocketPath(home)
	srv := NewServer(ServerConfig{
		SocketPath: sock,
		MetaPath:   MetaPath(home),
		DriverBuilder: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			return &gatedDriver{started: make(chan struct{}, 1), release: make(chan struct{})}, nil
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close(false)

	c := NewClient(sock)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://unix/shutdown?pid=%d", os.Getpid()+99999), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		t.Fatalf("shutdown request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("mismatched pid status = %d, want 409", resp.StatusCode)
	}

	// Server must stay alive and responsive after a mismatched shutdown.
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatalf("server died after mismatched pid shutdown: %v", err)
	}

	// Matching PID shuts down cleanly.
	c.pid = os.Getpid()
	if err := c.Close(); err != nil {
		t.Fatalf("matching close: %v", err)
	}
	select {
	case <-srv.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop after matching pid shutdown")
	}
}

func TestCloseDoesNotUnlinkReplacementSocketOrMeta(t *testing.T) {
	home := t.TempDir()
	sock := SocketPath(home)
	meta := MetaPath(home)
	builder := func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
		return &gatedDriver{started: make(chan struct{}, 1), release: make(chan struct{})}, nil
	}

	srv1 := NewServer(ServerConfig{SocketPath: sock, MetaPath: meta, DriverBuilder: builder})
	if err := srv1.Start(); err != nil {
		t.Fatalf("start srv1: %v", err)
	}

	// Simulate a replacement sidecar binding the same paths while srv1 is still tearing down.
	srv2 := NewServer(ServerConfig{SocketPath: sock, MetaPath: meta, DriverBuilder: builder})
	if err := srv2.Start(); err != nil {
		t.Fatalf("start srv2: %v", err)
	}
	defer srv2.Close(false)
	rawMeta, _ := json.Marshal(Meta{PID: os.Getpid() + 1, SocketPath: sock})
	if err := os.WriteFile(meta, rawMeta, 0o600); err != nil {
		t.Fatal(err)
	}

	srv1.Close(false)

	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("srv1.Close removed replacement socket: %v", err)
	}
	if _, err := os.Stat(meta); err != nil {
		t.Fatalf("srv1.Close removed replacement meta: %v", err)
	}
	if _, err := NewClient(sock).Status(context.Background()); err != nil {
		t.Fatalf("replacement sidecar unreachable after srv1.Close: %v", err)
	}
}
