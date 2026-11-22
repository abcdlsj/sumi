package computer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

type blockingDriver struct {
	started chan struct{}
	release chan struct{}
	runs    atomic.Int32
}

func (b *blockingDriver) RunTurn(context.Context, driver.TurnInput) (driver.TurnResult, error) {
	b.runs.Add(1)
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-b.release
	return driver.TurnResult{}, nil
}
func (b *blockingDriver) ResetSession() error { return nil }
func (b *blockingDriver) Close() error        { return nil }

func TestDrainHoldsNewWakesWaitsForBusyAndSkipsPaused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"conversation_id":"c1","reason":"mention"}]}`))
	}))
	defer srv.Close()

	bd := &blockingDriver{started: make(chan struct{}, 1), release: make(chan struct{})}
	d := NewDaemon(Config{
		ServerURL: srv.URL,
		DataDir:   t.TempDir(),
		DriverFactory: func(spec protocol.AssignedAgentSpec, _ string) (driver.SessionDriver, error) {
			if spec.MemberID == "agent-1" {
				return bd, nil
			}
			return &countingDriver{}, nil
		},
	}, nil)
	d.drainHoldGrace = 200 * time.Millisecond

	s1 := builtinSpec("m1")
	s2 := builtinSpec("m1")
	s2.MemberID = "agent-2"
	d.provisionAgent(s1)
	ag2 := d.provisionAgent(s2)
	// A paused agent (circuit breaker open) must not block drain even if marked busy.
	ag2.mu.Lock()
	ag2.paused = true
	ag2.busy = true
	ag2.mu.Unlock()

	d.scheduleWake(context.Background(), "agent-1", "c1", "mention")
	<-bd.started

	drainDone := make(chan DrainOutcome, 1)
	go func() {
		drainDone <- d.BeginDrain(context.Background(), "req-1", 5*time.Second)
	}()

	waitFor(t, func() bool { return d.isDraining() })

	// Wakes arriving during drain are held and must not start a second turn.
	d.scheduleWake(context.Background(), "agent-1", "c1", "mention")

	// Release the in-flight turn; drain must settle as "quiet" immediately without running a second turn.
	close(bd.release)
	select {
	case out := <-drainDone:
		if out.Finished != "quiet" || len(out.WaitedFor) != 1 || out.WaitedFor[0] != "agent-1" || len(out.NotQuiet) != 0 {
			t.Fatalf("unexpected drain outcome: %+v", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not finish after active turn ended")
	}

	if got := bd.runs.Load(); got != 1 {
		t.Fatalf("drain must not start held turns while holding, runs=%d", got)
	}
	if !d.isDraining() {
		t.Fatal("drain hold must remain active until shutdown or EndDrain")
	}

	// EndDrain releases the hold and dispatches the dirty wake.
	d.EndDrain(context.Background())
	waitFor(t, func() bool { return bd.runs.Load() >= 2 })
}

func TestDrainHoldCapAutoReleases(t *testing.T) {
	d := NewDaemon(Config{
		DataDir: t.TempDir(),
		DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			return &countingDriver{}, nil
		},
	}, nil)
	d.drainHoldGrace = 60 * time.Millisecond
	d.provisionAgent(builtinSpec("m1"))

	out := d.BeginDrain(context.Background(), "req-cap", 20*time.Millisecond)
	if out.Finished != "quiet" {
		t.Fatalf("expected quiet, got %+v", out)
	}
	if !d.isDraining() {
		t.Fatal("expected draining to stay active initially")
	}
	waitFor(t, func() bool { return !d.isDraining() })
}
