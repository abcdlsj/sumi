package computer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

type failingDriver struct{ turns atomic.Int32 }

func (f *failingDriver) RunTurn(context.Context, driver.TurnInput) (driver.TurnResult, error) {
	f.turns.Add(1)
	return driver.TurnResult{}, driver.Fail(domain.AgentErrModelAuthFailed, errors.New("401 unauthorized"))
}
func (f *failingDriver) ResetSession() error { return nil }
func (f *failingDriver) Close() error        { return nil }

func TestFailureBreakerPausesProbesAndResetsOnRestart(t *testing.T) {
	inbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []domain.InboxItem{{ConversationID: "c1", Title: "@Owner", Reason: "dm", UnreadCount: 1}}})
	}))
	defer inbox.Close()

	drv := &failingDriver{}
	d := NewDaemon(Config{
		ServerURL: inbox.URL,
		DataDir:   t.TempDir(),
		DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			return drv, nil
		},
	}, nil)
	d.breaker = breakerPolicy{threshold: 2, firstProbe: 150 * time.Millisecond, maxProbe: time.Second, firstRetry: 20 * time.Millisecond, maxRetry: 40 * time.Millisecond}
	defer d.Stop()
	ctx := context.Background()
	ag := d.provisionAgent(builtinSpec("m1"))
	state := func() (bool, time.Duration) { ag.mu.Lock(); defer ag.mu.Unlock(); return ag.paused, ag.probeDelay }
	pausedWith := func(delay time.Duration) func() bool {
		return func() bool { p, dl := state(); return p && dl == delay }
	}

	// One wake: the first failure retries on its own, the second hits the threshold and pauses.
	d.scheduleWake(ctx, "agent-1", "c1", "dm")
	waitFor(t, pausedWith(150*time.Millisecond))
	if n := drv.turns.Load(); n != 2 {
		t.Fatalf("turns before pause = %d", n)
	}

	d.scheduleWake(ctx, "agent-1", "c1", "dm") // Ignored while paused: no tokens burned on a failing driver.
	if n := drv.turns.Load(); n != 2 {
		t.Fatalf("paused agent ran a turn: %d", n)
	}

	// The probe runs exactly one turn; its failure re-pauses with a doubled delay.
	waitFor(t, pausedWith(300*time.Millisecond))
	if n := drv.turns.Load(); n != 3 {
		t.Fatalf("probe turns = %d", n)
	}

	// A human Restart clears the breaker: two fresh failures pause again at the first probe delay.
	d.handleControl(ctx, protocol.AgentControlFrame{AgentMemberID: "agent-1", Action: "restart"})
	waitFor(t, pausedWith(150*time.Millisecond))
	if n := drv.turns.Load(); n != 5 {
		t.Fatalf("turns after restart = %d", n)
	}
}

// flakyDriver fails its first turn and succeeds afterwards.
type flakyDriver struct{ turns atomic.Int32 }

func (f *flakyDriver) RunTurn(context.Context, driver.TurnInput) (driver.TurnResult, error) {
	if f.turns.Add(1) == 1 {
		return driver.TurnResult{}, errors.New("transient")
	}
	return driver.TurnResult{}, nil
}
func (f *flakyDriver) ResetSession() error { return nil }
func (f *flakyDriver) Close() error        { return nil }

// pendingInbox serves one unread item until the agent has run `clearAfter` turns.
func pendingInbox(t *testing.T, turns func() int32, clearAfter int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		items := []domain.InboxItem{}
		if turns() < clearAfter {
			items = append(items, domain.InboxItem{ConversationID: "c1", Title: "#werewolf", Reason: "mention", UnreadCount: 1})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFailedTurnRedeliversWithoutNewWake(t *testing.T) {
	drv := &flakyDriver{}
	srv := pendingInbox(t, drv.turns.Load, 2)
	d := NewDaemon(Config{ServerURL: srv.URL, DataDir: t.TempDir(),
		DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) { return drv, nil }}, nil)
	d.breaker = breakerPolicy{threshold: 3, firstProbe: time.Hour, maxProbe: time.Hour, firstRetry: 30 * time.Millisecond, maxRetry: time.Second}
	defer d.Stop()
	ag := d.provisionAgent(builtinSpec("m1"))

	d.scheduleWake(context.Background(), "agent-1", "c1", "mention")
	waitFor(t, func() bool { return drv.turns.Load() == 2 })
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy && ag.failStreak == 0 && ag.retryTimer == nil
	})
}

func TestRetryBackoffHoldsWakesAndGrows(t *testing.T) {
	d := NewDaemon(Config{DataDir: t.TempDir()}, nil)
	d.breaker = breakerPolicy{threshold: 9, firstProbe: time.Hour, maxProbe: time.Hour, firstRetry: time.Second, maxRetry: 4 * time.Second}
	defer d.Stop()
	ag := d.provisionAgent(builtinSpec("m1"))
	for streak, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 6: 4 * time.Second} {
		ag.mu.Lock()
		ag.failStreak = streak
		ag.mu.Unlock()
		got := d.armRetry(context.Background(), ag)
		if lo, hi := want*8/10, want*12/10; got < lo || got > hi {
			t.Fatalf("streak %d: retry in %s, want %s ±20%%", streak, got, want)
		}
	}
	// A wake during the backoff only marks dirty; the retry runs it.
	d.scheduleWake(context.Background(), "agent-1", "c1", "dm")
	ag.mu.Lock()
	busy, dirty := ag.busy, ag.dirty
	ag.resetBreakerLocked()
	stopped := ag.retryTimer == nil
	ag.mu.Unlock()
	if busy || !dirty || !stopped {
		t.Fatalf("wake during backoff: busy=%v dirty=%v timerCleared=%v", busy, dirty, stopped)
	}
}

// silentDriver blocks without emitting runtime events until its context ends.
type silentDriver struct{ turns atomic.Int32 }

func (s *silentDriver) RunTurn(ctx context.Context, _ driver.TurnInput) (driver.TurnResult, error) {
	if s.turns.Add(1) > 1 {
		return driver.TurnResult{}, nil
	}
	<-ctx.Done()
	return driver.TurnResult{}, ctx.Err()
}
func (s *silentDriver) ResetSession() error { return nil }
func (s *silentDriver) Close() error        { return nil }

func TestSilentTurnIsCancelledOnlyWhenWorkIsWaiting(t *testing.T) {
	drv := &silentDriver{}
	srv := pendingInbox(t, drv.turns.Load, 2)
	d := NewDaemon(Config{ServerURL: srv.URL, DataDir: t.TempDir(),
		DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) { return drv, nil }}, nil)
	d.activity = activityPolicy{minInterval: time.Millisecond, stallAfter: time.Hour, cancelIdle: 60 * time.Millisecond, cancelIdleOpenTool: time.Hour, checkEvery: 10 * time.Millisecond}
	d.breaker = breakerPolicy{threshold: 3, firstProbe: time.Hour, maxProbe: time.Hour, firstRetry: 10 * time.Millisecond, maxRetry: time.Second}
	ch := make(chan protocol.Envelope, 64)
	d.wsSendCh = ch
	defer d.Stop()
	ag := d.provisionAgent(builtinSpec("m1"))
	ctx := context.Background()

	d.scheduleWake(ctx, "agent-1", "c1", "mention")
	// Silent past the bound with nothing queued: the turn keeps running.
	time.Sleep(200 * time.Millisecond)
	if n := drv.turns.Load(); n != 1 {
		t.Fatalf("turns = %d", n)
	}
	ag.mu.Lock()
	busy := ag.busy
	ag.mu.Unlock()
	if !busy {
		t.Fatal("silent turn without queued work must not be cancelled")
	}

	// New work arrives: the silent turn is cut as turn_stalled and the retry handles the wake.
	d.scheduleWake(ctx, "agent-1", "c1", "mention")
	waitFor(t, func() bool { return drv.turns.Load() == 2 })
	sawStalled := false
	for len(ch) > 0 {
		if env := <-ch; env.AgentState != nil && env.AgentState.ErrorCode == domain.AgentErrTurnStalled {
			sawStalled = true
		}
	}
	if !sawStalled {
		t.Fatal("expected a turn_stalled error report")
	}
}

type stoppableDriver struct {
	started   chan struct{}
	turns     atomic.Int32
	resetCnt  atomic.Int32
	cancelled atomic.Int32
}

func (b *stoppableDriver) RunTurn(ctx context.Context, _ driver.TurnInput) (driver.TurnResult, error) {
	if b.turns.Add(1) == 1 {
		close(b.started)
		<-ctx.Done()
		b.cancelled.Add(1)
		return driver.TurnResult{}, ctx.Err()
	}
	return driver.TurnResult{}, nil
}
func (b *stoppableDriver) ResetSession() error { b.resetCnt.Add(1); return nil }
func (b *stoppableDriver) Close() error        { return nil }

func TestStopTurnCancelsInFlightTurnWithoutTrippingBreakerOrResettingSession(t *testing.T) {
	drv := &stoppableDriver{started: make(chan struct{})}
	srv := pendingInbox(t, drv.turns.Load, 2)
	d := NewDaemon(Config{
		ServerURL: srv.URL,
		DataDir:   t.TempDir(),
		DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			return drv, nil
		},
	}, nil)
	ch := make(chan protocol.Envelope, 64)
	d.wsSendCh = ch
	defer d.Stop()
	ag := d.provisionAgent(builtinSpec("m1"))
	ctx := context.Background()

	d.scheduleWake(ctx, "agent-1", "c1", "mention")
	<-drv.started

	// Queue a wake while busy; stop_turn must cancel the turn and not immediately re-wake it.
	d.scheduleWake(ctx, "agent-1", "c1", "mention")
	d.handleControl(ctx, protocol.AgentControlFrame{AgentMemberID: "agent-1", Action: "stop_turn"})

	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy && ag.cancelTurn == nil
	})
	if drv.cancelled.Load() != 1 {
		t.Fatalf("cancelled = %d, want 1", drv.cancelled.Load())
	}
	if drv.resetCnt.Load() != 0 {
		t.Fatalf("stop_turn must not reset session, resetCnt = %d", drv.resetCnt.Load())
	}
	ag.mu.Lock()
	failStreak, paused, dirty, retry := ag.failStreak, ag.paused, ag.dirty, ag.retryTimer
	ag.mu.Unlock()
	if failStreak != 0 || paused || dirty || retry != nil {
		t.Fatalf("unexpected state after stop_turn: failStreak=%d paused=%v dirty=%v retry=%v", failStreak, paused, dirty, retry != nil)
	}
	if drv.turns.Load() != 1 {
		t.Fatalf("stop_turn must not immediately start a second turn, turns=%d", drv.turns.Load())
	}

	var lastState domain.RuntimeState
	for len(ch) > 0 {
		env := <-ch
		if env.AgentState != nil {
			if env.AgentState.RuntimeState == domain.RuntimeError {
				t.Fatalf("stop_turn must not report error state: %+v", env.AgentState)
			}
			lastState = env.AgentState.RuntimeState
		}
	}
	if lastState != domain.RuntimeIdle {
		t.Fatalf("last reported state = %q, want idle", lastState)
	}

	// A subsequent wake runs normally on the preserved session.
	d.scheduleWake(ctx, "agent-1", "c1", "mention")
	waitFor(t, func() bool { return drv.turns.Load() == 2 })
}
