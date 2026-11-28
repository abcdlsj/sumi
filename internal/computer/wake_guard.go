package computer

import (
	"sync"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
)

const (
	wakeLoopBackoffAt      = 2
	wakeLoopParkAt         = 3
	defaultWakeLoopBackoff = 60 * time.Second
)

type wakeLoopAction int

const (
	wakeLoopProceed wakeLoopAction = iota
	wakeLoopNudge                  // 1st zero-progress: immediate unread nudge
	wakeLoopBackoff                // 2nd zero-progress: cool off before one final retry
	wakeLoopPark                   // 3rd+ zero-progress: park until a newer latest_seq or operator reset
	wakeLoopSkip                   // seq <= 0 (no watermark): skip for this drain pass
)

type wakeStreak struct {
	seq   int64
	count int
}

type wakeBackoff struct {
	seq   int64
	until time.Time
}

// wakeLoopGuard tracks per-(conversation, latest_seq) zero-progress wakes across turns so a
// conversation the Agent repeatedly fails to read cannot starve other work or spin forever.
type wakeLoopGuard struct {
	mu         sync.Mutex
	backoff    time.Duration
	streaks    map[string]wakeStreak
	backoffMap map[string]wakeBackoff
	parked     map[string]int64
}

func newWakeLoopGuard(backoff time.Duration) *wakeLoopGuard {
	if backoff <= 0 {
		backoff = defaultWakeLoopBackoff
	}
	return &wakeLoopGuard{
		backoff:    backoff,
		streaks:    make(map[string]wakeStreak),
		backoffMap: make(map[string]wakeBackoff),
		parked:     make(map[string]int64),
	}
}

// NoteProgress clears zero-progress state for convID once the Agent advances read_seq past seq.
func (g *wakeLoopGuard) NoteProgress(convID string, seq int64) {
	if g == nil || convID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.streaks, convID)
	delete(g.backoffMap, convID)
	if parkedSeq, ok := g.parked[convID]; ok && (seq == 0 || seq >= parkedSeq) {
		delete(g.parked, convID)
	}
}

// RecordZeroProgress records that a turn focused on (convID, seq) ended without advancing read_seq.
func (g *wakeLoopGuard) RecordZeroProgress(convID string, seq int64, now time.Time) (wakeLoopAction, time.Duration) {
	if g == nil || convID == "" || seq <= 0 {
		return wakeLoopSkip, 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.streaks[convID]
	if st.seq != seq {
		st = wakeStreak{seq: seq}
	}
	st.count++
	if st.count >= wakeLoopParkAt {
		g.parked[convID] = seq
		delete(g.backoffMap, convID)
		delete(g.streaks, convID)
		return wakeLoopPark, 0
	}
	g.streaks[convID] = st
	if st.count >= wakeLoopBackoffAt {
		g.backoffMap[convID] = wakeBackoff{seq: seq, until: now.Add(g.backoff)}
		return wakeLoopBackoff, g.backoff
	}
	return wakeLoopNudge, 0
}

// IsNudgeDue reports whether (convID, seq) is due for its single immediate unread-nudge retry.
func (g *wakeLoopGuard) IsNudgeDue(convID string, seq int64) bool {
	if g == nil || convID == "" || seq <= 0 {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.streaks[convID]
	return st.seq == seq && st.count == 1
}

// Filter returns inbox items eligible to run now, skipping locally skipped, backed-off, and parked
// items. If any item is in backoff, earliestBackoff reports the shortest remaining wait.
func (g *wakeLoopGuard) Filter(items []domain.InboxItem, localSkip map[string]int64, now time.Time) ([]domain.InboxItem, time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ready := make([]domain.InboxItem, 0, len(items))
	var earliest time.Duration
	for _, it := range items {
		if skipSeq, ok := localSkip[it.ConversationID]; ok && it.LatestSeq <= skipSeq {
			continue
		}
		if parkedSeq, ok := g.parked[it.ConversationID]; ok {
			if it.LatestSeq <= parkedSeq {
				continue
			}
			delete(g.parked, it.ConversationID)
		}
		if bo, ok := g.backoffMap[it.ConversationID]; ok {
			if it.LatestSeq <= bo.seq && now.Before(bo.until) {
				rem := bo.until.Sub(now)
				if earliest == 0 || rem < earliest {
					earliest = rem
				}
				continue
			}
			delete(g.backoffMap, it.ConversationID)
		}
		ready = append(ready, it)
	}
	return ready, earliest
}

// Forget clears all streaks, backoffs, and parked conversations (used on operator restart/reset).
func (g *wakeLoopGuard) Forget() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	clear(g.streaks)
	clear(g.backoffMap)
	clear(g.parked)
}
