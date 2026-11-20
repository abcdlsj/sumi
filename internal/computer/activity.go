package computer

import (
	"sync"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
)

type activityPolicy struct {
	// minInterval coalesces tool-change reports; stall reports bypass it.
	minInterval time.Duration
	// stallAfter is how long an open tool call may go without any runtime event before it is
	// reported as stalled. Stall detection only observes; it never cancels the turn.
	stallAfter time.Duration
	// cancelIdle / cancelIdleOpenTool bound how long a turn may stay silent (no runtime event)
	// once new work is waiting for the Agent; there is no wall-clock cap on an active turn.
	cancelIdle         time.Duration
	cancelIdleOpenTool time.Duration
	checkEvery         time.Duration
}

var defaultActivityPolicy = activityPolicy{
	minInterval:        5 * time.Second,
	stallAfter:         10 * time.Minute,
	cancelIdle:         15 * time.Minute,
	cancelIdleOpenTool: 60 * time.Minute,
	checkEvery:         30 * time.Second,
}

type openCall struct {
	tool    string
	started time.Time
}

// turnActivity tracks one turn's open tool calls and reports the newest running tool, or a
// stall warning once per quiet span, through report.
type turnActivity struct {
	policy activityPolicy
	report func(*domain.AgentActivity)
	// pending reports whether new work is queued; cancel ends the turn. Both may be nil.
	hasPending func() bool
	cancel     func()

	mu        sync.Mutex
	open      map[string]openCall
	lastEvent time.Time
	stalled   bool
	sent      *domain.AgentActivity
	sentAt    time.Time
	pending   *time.Timer
	stop      chan struct{}
	stopOnce  sync.Once
}

func newTurnActivity(policy activityPolicy, report func(*domain.AgentActivity)) *turnActivity {
	return newWatchedTurnActivity(policy, report, nil, nil)
}

// newWatchedTurnActivity also cancels the turn via cancel when it has been silent past the
// policy's idle bound while hasPending reports queued work.
func newWatchedTurnActivity(policy activityPolicy, report func(*domain.AgentActivity), hasPending func() bool, cancel func()) *turnActivity {
	t := &turnActivity{policy: policy, report: report, hasPending: hasPending, cancel: cancel, open: make(map[string]openCall), lastEvent: time.Now(), stop: make(chan struct{})}
	go t.watch()
	return t
}

// idleBoundLocked is the silence after which a turn with queued work is cancelled.
func (t *turnActivity) idleBoundLocked() time.Duration {
	if len(t.open) > 0 {
		return t.policy.cancelIdleOpenTool
	}
	return t.policy.cancelIdle
}

func (t *turnActivity) Observe(a driver.Activity) {
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.stop:
		return // The turn ended; its final state report must not be overtaken.
	default:
	}
	now := time.Now()
	t.lastEvent = now
	t.stalled = false
	if a.ToolCallID != "" {
		if a.Done {
			delete(t.open, a.ToolCallID)
		} else if _, ok := t.open[a.ToolCallID]; !ok {
			t.open[a.ToolCallID] = openCall{tool: a.Tool, started: now}
		}
	}
	t.scheduleLocked(t.currentLocked(), false)
}

// currentLocked names the most recently started open tool call.
func (t *turnActivity) currentLocked() *domain.AgentActivity {
	var newest openCall
	for _, c := range t.open {
		if c.started.After(newest.started) {
			newest = c
		}
	}
	if newest.tool == "" {
		return nil
	}
	return &domain.AgentActivity{Tool: newest.tool}
}

func (t *turnActivity) scheduleLocked(next *domain.AgentActivity, urgent bool) {
	if sameActivity(next, t.sent) {
		if t.pending != nil {
			t.pending.Stop()
			t.pending = nil
		}
		return
	}
	wait := t.policy.minInterval - time.Since(t.sentAt)
	if urgent || wait <= 0 {
		t.sendLocked(next)
		return
	}
	if t.pending == nil {
		t.pending = time.AfterFunc(wait, func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.pending = nil
			select {
			case <-t.stop:
				return
			default:
			}
			if cur := t.latestLocked(); !sameActivity(cur, t.sent) {
				t.sendLocked(cur)
			}
		})
	}
}

func (t *turnActivity) latestLocked() *domain.AgentActivity {
	if t.stalled {
		return t.stallLocked()
	}
	return t.currentLocked()
}

func (t *turnActivity) stallLocked() *domain.AgentActivity {
	var oldest openCall
	for _, c := range t.open {
		if oldest.tool == "" || c.started.Before(oldest.started) {
			oldest = c
		}
	}
	return &domain.AgentActivity{Tool: oldest.tool, StalledSeconds: max(1, int64(time.Since(t.lastEvent)/time.Second))}
}

func (t *turnActivity) sendLocked(a *domain.AgentActivity) {
	t.sent, t.sentAt = a, time.Now()
	t.report(a)
}

func (t *turnActivity) watch() {
	tick := time.NewTicker(t.policy.checkEvery)
	defer tick.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-tick.C:
			t.mu.Lock()
			if !t.stalled && len(t.open) > 0 && time.Since(t.lastEvent) >= t.policy.stallAfter {
				t.stalled = true
				t.scheduleLocked(t.stallLocked(), true)
			}
			idle, bound := time.Since(t.lastEvent), t.idleBoundLocked()
			t.mu.Unlock()
			// hasPending takes the Agent lock, so it is called without holding t.mu.
			if t.cancel != nil && bound > 0 && idle >= bound && t.hasPending != nil && t.hasPending() {
				t.cancel()
				return
			}
		}
	}
}

func (t *turnActivity) Close() {
	t.stopOnce.Do(func() {
		close(t.stop)
		t.mu.Lock()
		if t.pending != nil {
			t.pending.Stop()
		}
		t.mu.Unlock()
	})
}

func sameActivity(a, b *domain.AgentActivity) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Tool == b.Tool && (a.StalledSeconds > 0) == (b.StalledSeconds > 0)
}
