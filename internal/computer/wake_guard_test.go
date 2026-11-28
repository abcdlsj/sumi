package computer

import (
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestWakeLoopGuardEscalatesNudgeBackoffParkAndUnparksOnNewSeqOrForget(t *testing.T) {
	t0 := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	g := newWakeLoopGuard(60 * time.Second)
	item5 := domain.InboxItem{ConversationID: "c1", Target: "#eng", LatestSeq: 5}
	other := domain.InboxItem{ConversationID: "c2", Target: "#ops", LatestSeq: 2}

	// 1st zero-progress -> immediate unread nudge; item remains eligible.
	act, wait := g.RecordZeroProgress("c1", 5, t0)
	if act != wakeLoopNudge || wait != 0 || !g.IsNudgeDue("c1", 5) {
		t.Fatalf("1st zero-progress: act=%v wait=%v nudgeDue=%v", act, wait, g.IsNudgeDue("c1", 5))
	}
	ready, earliest := g.Filter([]domain.InboxItem{item5, other}, nil, t0)
	if len(ready) != 2 || earliest != 0 {
		t.Fatalf("after 1st zero-progress: ready=%d earliest=%v", len(ready), earliest)
	}

	// 2nd zero-progress -> 60s backoff; #ops stays ready while #eng waits.
	act, wait = g.RecordZeroProgress("c1", 5, t0.Add(time.Second))
	if act != wakeLoopBackoff || wait != 60*time.Second || g.IsNudgeDue("c1", 5) {
		t.Fatalf("2nd zero-progress: act=%v wait=%v nudgeDue=%v", act, wait, g.IsNudgeDue("c1", 5))
	}
	ready, earliest = g.Filter([]domain.InboxItem{item5, other}, nil, t0.Add(11*time.Second))
	if len(ready) != 1 || ready[0].ConversationID != "c2" || earliest != 50*time.Second {
		t.Fatalf("during backoff: ready=%+v earliest=%v", ready, earliest)
	}

	// Once backoff expires, c1@5 becomes ready again for one final retry.
	ready, earliest = g.Filter([]domain.InboxItem{item5}, nil, t0.Add(62*time.Second))
	if len(ready) != 1 || ready[0].ConversationID != "c1" || earliest != 0 {
		t.Fatalf("after backoff expiry: ready=%+v earliest=%v", ready, earliest)
	}

	// 3rd zero-progress -> parked indefinitely for seq <= 5.
	act, wait = g.RecordZeroProgress("c1", 5, t0.Add(63*time.Second))
	if act != wakeLoopPark || wait != 0 {
		t.Fatalf("3rd zero-progress: act=%v wait=%v", act, wait)
	}
	ready, earliest = g.Filter([]domain.InboxItem{item5}, nil, t0.Add(2*time.Hour))
	if len(ready) != 0 || earliest != 0 {
		t.Fatalf("parked item must stay excluded: ready=%+v earliest=%v", ready, earliest)
	}

	// A newer message (seq 6 > 5) automatically unparks c1.
	item6 := domain.InboxItem{ConversationID: "c1", Target: "#eng", LatestSeq: 6}
	ready, earliest = g.Filter([]domain.InboxItem{item6}, nil, t0.Add(2*time.Hour))
	if len(ready) != 1 || ready[0].LatestSeq != 6 || earliest != 0 {
		t.Fatalf("newer seq must unpark: ready=%+v earliest=%v", ready, earliest)
	}

	// Re-park seq 6, then verify Forget() clears parked state (operator restart/reset).
	g.RecordZeroProgress("c1", 6, t0)
	g.RecordZeroProgress("c1", 6, t0)
	if act, _ := g.RecordZeroProgress("c1", 6, t0); act != wakeLoopPark {
		t.Fatalf("expected park on 3rd strike for seq 6, got %v", act)
	}
	g.Forget()
	ready, _ = g.Filter([]domain.InboxItem{item6}, nil, t0)
	if len(ready) != 1 {
		t.Fatalf("Forget must unpark c1@6: ready=%+v", ready)
	}
}
