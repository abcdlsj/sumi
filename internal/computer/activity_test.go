package computer

import (
	"sync"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
)

type activityLog struct {
	mu   sync.Mutex
	sent []*domain.AgentActivity
}

func (l *activityLog) add(a *domain.AgentActivity) {
	l.mu.Lock()
	l.sent = append(l.sent, a)
	l.mu.Unlock()
}
func (l *activityLog) snapshot() []*domain.AgentActivity {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*domain.AgentActivity(nil), l.sent...)
}

func TestTurnActivityReportsToolsAndStallOnce(t *testing.T) {
	log := &activityLog{}
	ta := newTurnActivity(activityPolicy{minInterval: 0, stallAfter: 80 * time.Millisecond, checkEvery: 10 * time.Millisecond}, log.add)
	defer ta.Close()

	ta.Observe(driver.Activity{ToolCallID: "t1", Tool: "execute"})
	ta.Observe(driver.Activity{}) // Generic progress keeps the same tool: no new report.
	if got := log.snapshot(); len(got) != 1 || got[0].Tool != "execute" || got[0].StalledSeconds != 0 {
		t.Fatalf("tool start report: %+v", got)
	}

	waitFor(t, func() bool { return len(log.snapshot()) == 2 })
	time.Sleep(100 * time.Millisecond)
	got := log.snapshot()
	if len(got) != 2 || got[1].Tool != "execute" || got[1].StalledSeconds < 1 {
		t.Fatalf("a quiet open call must be reported stalled exactly once: %+v", got)
	}
	ta.mu.Lock()
	stalled := ta.stalled
	ta.mu.Unlock()
	if !stalled {
		t.Fatalf("tracker must be in stalled state")
	}

	// Any runtime event clears the stall; finishing the tool clears the activity.
	ta.Observe(driver.Activity{ToolCallID: "t1", Tool: "execute", Done: true})
	got = log.snapshot()
	if last := got[len(got)-1]; last != nil {
		t.Fatalf("finished tool must clear activity, got %+v", last)
	}

	ta.Close()
	before := len(log.snapshot())
	ta.Observe(driver.Activity{ToolCallID: "t2", Tool: "read"})
	if len(log.snapshot()) != before {
		t.Fatalf("observations after the turn ended must be dropped")
	}
}

func TestTurnActivityCoalescesToolChanges(t *testing.T) {
	log := &activityLog{}
	ta := newTurnActivity(activityPolicy{minInterval: 100 * time.Millisecond, stallAfter: time.Hour, checkEvery: time.Hour}, log.add)
	defer ta.Close()
	ta.Observe(driver.Activity{ToolCallID: "a", Tool: "read"})
	ta.Observe(driver.Activity{ToolCallID: "a", Tool: "read", Done: true})
	ta.Observe(driver.Activity{ToolCallID: "b", Tool: "edit"})
	ta.Observe(driver.Activity{ToolCallID: "b", Tool: "edit", Done: true})
	ta.Observe(driver.Activity{ToolCallID: "c", Tool: "execute"})
	waitFor(t, func() bool { return len(log.snapshot()) == 2 })
	time.Sleep(150 * time.Millisecond)
	got := log.snapshot()
	if len(got) != 2 || got[0].Tool != "read" || got[1].Tool != "execute" {
		t.Fatalf("bursts must coalesce to the latest tool: %+v", got)
	}
}

func TestSanitizeActivityDropsFreeText(t *testing.T) {
	a := domain.SanitizeActivity(&domain.AgentActivity{Tool: "execute: rm -rf /tmp/secret body"})
	if a == nil || a.Tool != "executerm-rftmpsecretbody"[:len(a.Tool)] || len(a.Tool) > 32 {
		t.Fatalf("sanitized tool=%+v", a)
	}
	if domain.SanitizeActivity(&domain.AgentActivity{Tool: "  "}) != nil {
		t.Fatalf("empty activity must be nil")
	}
}
