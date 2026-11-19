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
	d.breaker = breakerPolicy{threshold: 2, firstProbe: 150 * time.Millisecond, maxProbe: time.Second}
	defer d.Stop()
	ctx := context.Background()
	ag := d.provisionAgent(builtinSpec("m1"))
	idle := func() bool { ag.mu.Lock(); defer ag.mu.Unlock(); return !ag.busy }
	wake := func() { d.scheduleWake(ctx, "agent-1", "c1", "dm"); waitFor(t, idle) }

	wake()
	wake()
	if n := drv.turns.Load(); n != 2 {
		t.Fatalf("turns before pause = %d", n)
	}
	ag.mu.Lock()
	paused, delay := ag.paused, ag.probeDelay
	ag.mu.Unlock()
	if !paused || delay != 150*time.Millisecond {
		t.Fatalf("expected pause after threshold: paused=%v delay=%v", paused, delay)
	}

	wake() // Ignored while paused: no tokens burned on a failing driver.
	if n := drv.turns.Load(); n != 2 {
		t.Fatalf("paused agent ran a turn: %d", n)
	}

	// The probe runs exactly one turn; its failure re-pauses with a doubled delay.
	waitFor(t, func() bool { return drv.turns.Load() == 3 })
	waitFor(t, idle)
	ag.mu.Lock()
	paused, delay = ag.paused, ag.probeDelay
	ag.mu.Unlock()
	if !paused || delay != 300*time.Millisecond {
		t.Fatalf("failed probe must re-pause with doubled delay: paused=%v delay=%v", paused, delay)
	}

	// A human Restart clears the breaker immediately.
	d.handleControl(ctx, protocol.AgentControlFrame{AgentMemberID: "agent-1", Action: "restart"})
	waitFor(t, func() bool { return drv.turns.Load() == 4 })
	waitFor(t, idle)
	ag.mu.Lock()
	streak := ag.failStreak
	ag.mu.Unlock()
	if streak != 1 {
		t.Fatalf("restart must reset the failure streak, got %d", streak)
	}
}
