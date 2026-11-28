package computer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

type scriptedDriver struct {
	mu      sync.Mutex
	prompts []string
	fn      func(turn int, in driver.TurnInput) (driver.TurnResult, error)
}

func (s *scriptedDriver) RunTurn(_ context.Context, in driver.TurnInput) (driver.TurnResult, error) {
	s.mu.Lock()
	turn := len(s.prompts) + 1
	s.prompts = append(s.prompts, in.UserPrompt)
	fn := s.fn
	s.mu.Unlock()
	if fn != nil {
		return fn(turn, in)
	}
	return driver.TurnResult{}, nil
}

func (s *scriptedDriver) ResetSession() error { return nil }
func (s *scriptedDriver) Close() error        { return nil }

func (s *scriptedDriver) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prompts)
}

func (s *scriptedDriver) promptAt(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prompts[i]
}

// proxyHit invokes Daemon.handleCapabilityProxy directly to simulate a `sumi` CLI call.
func proxyHit(d *Daemon, driverToken, method, reqPath string) int {
	req := httptest.NewRequest(method, reqPath, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+driverToken)
	rec := httptest.NewRecorder()
	d.handleCapabilityProxy(rec, req)
	return rec.Code
}

func TestMultiGroupDrainRunsOneFocusPerTurnFIFO(t *testing.T) {
	t0 := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	unread := map[string]domain.InboxItem{
		"c1": {ConversationID: "c1", Target: "#alpha", Title: "#alpha", Reason: "channel", UnreadCount: 1, LatestSeq: 3, LatestAt: t0.Add(1 * time.Minute)},
		"c2": {ConversationID: "c2", Target: "#beta", Title: "#beta", Reason: "mention", UnreadCount: 1, LatestSeq: 4, LatestAt: t0.Add(2 * time.Minute)},
		"c3": {ConversationID: "c3", Target: "#gamma", Title: "#gamma", Reason: "channel", UnreadCount: 1, LatestSeq: 5, LatestAt: t0.Add(3 * time.Minute)},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/inbox"):
			// Return newest-first, matching Server ListInbox ordering.
			items := make([]domain.InboxItem, 0, len(unread))
			for _, id := range []string{"c3", "c2", "c1"} {
				if it, ok := unread[id]; ok {
					items = append(items, it)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			convID := parts[len(parts)-2]
			delete(unread, convID)
			_, _ = w.Write([]byte(`{"messages":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"message":{"id":"m1"}}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	var d *Daemon
	var ag *hostedAgent
	drv := &scriptedDriver{}
	drv.fn = func(_ int, in driver.TurnInput) (driver.TurnResult, error) {
		var convID string
		switch {
		case strings.Contains(in.UserPrompt, "sumi read #alpha"):
			convID = "c1"
		case strings.Contains(in.UserPrompt, "sumi read #beta"):
			convID = "c2"
		case strings.Contains(in.UserPrompt, "sumi read #gamma"):
			convID = "c3"
		default:
			t.Errorf("unexpected focus prompt: %s", in.UserPrompt)
			return driver.TurnResult{}, nil
		}
		proxyHit(d, ag.driverToken, http.MethodGet, "/conversations/"+convID+"/messages?unread=true")
		proxyHit(d, ag.driverToken, http.MethodPost, "/conversations/"+convID+"/messages")
		return driver.TurnResult{ToolCalls: 2}, nil
	}

	d = NewDaemon(Config{
		ServerURL: srv.URL,
		DataDir:   t.TempDir(),
		DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			return drv, nil
		},
	}, nil)
	defer d.Stop()
	ag = d.provisionAgent(builtinSpec("m1"))

	// Single wake must drain all 3 conversations one-by-one in FIFO order.
	d.scheduleWake(context.Background(), "agent-1", "c3", "channel")
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy && drv.count() == 3
	})

	if !strings.Contains(drv.promptAt(0), "sumi read #alpha") || !strings.Contains(drv.promptAt(0), "do NOT read or reply to them in this turn") {
		t.Fatalf("turn 1 should focus #alpha first: %s", drv.promptAt(0))
	}
	if !strings.Contains(drv.promptAt(1), "sumi read #beta") {
		t.Fatalf("turn 2 should focus #beta second: %s", drv.promptAt(1))
	}
	if !strings.Contains(drv.promptAt(2), "sumi read #gamma") {
		t.Fatalf("turn 3 should focus #gamma third: %s", drv.promptAt(2))
	}
}

func TestFailedTurnAfterReadRecoversEvenWhenInboxEmpty(t *testing.T) {
	var mu sync.Mutex
	readDone := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/inbox"):
			items := []domain.InboxItem{}
			if !readDone {
				items = append(items, domain.InboxItem{ConversationID: "dm-1", Target: "dm:@Owner", Title: "@Owner", Reason: "dm", UnreadCount: 1, LatestSeq: 25})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
			readDone = true
			_, _ = w.Write([]byte(`{"messages":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"message":{"id":"m26"}}`))
		}
	}))
	defer srv.Close()

	var d *Daemon
	var ag *hostedAgent
	drv := &scriptedDriver{}
	drv.fn = func(turn int, _ driver.TurnInput) (driver.TurnResult, error) {
		if turn == 1 {
			// Advance read_seq on server, then fail with a network error before replying.
			proxyHit(d, ag.driverToken, http.MethodGet, "/conversations/dm-1/messages?unread=true")
			return driver.TurnResult{ToolCalls: 4}, driver.Fail(domain.AgentErrModelUnreachable, errors.New("ENOTFOUND"))
		}
		// Recovery turn sends the reply.
		proxyHit(d, ag.driverToken, http.MethodPost, "/conversations/dm-1/messages")
		return driver.TurnResult{ToolCalls: 1}, nil
	}

	d = NewDaemon(Config{
		ServerURL: srv.URL,
		DataDir:   t.TempDir(),
		DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			return drv, nil
		},
	}, nil)
	d.breaker = breakerPolicy{threshold: 3, firstProbe: time.Hour, maxProbe: time.Hour, firstRetry: 20 * time.Millisecond, maxRetry: 50 * time.Millisecond}
	defer d.Stop()
	ag = d.provisionAgent(builtinSpec("m1"))

	d.scheduleWake(context.Background(), "agent-1", "dm-1", "dm")
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy && drv.count() == 2 && ag.pendingRetry == nil
	})

	if !strings.Contains(drv.promptAt(1), "interrupted by an error (model_unreachable)") || !strings.Contains(drv.promptAt(1), "sumi read dm:@Owner --limit 10") {
		t.Fatalf("recovery turn prompt mismatch: %s", drv.promptAt(1))
	}
}

func TestSilentReplyNudgeRunsAtMostOnceAndHandlesHeldDrafts(t *testing.T) {
	t.Run("dm_without_send_nudges_once", func(t *testing.T) {
		var mu sync.Mutex
		readDone := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case strings.HasSuffix(r.URL.Path, "/inbox"):
				items := []domain.InboxItem{}
				if !readDone {
					items = append(items, domain.InboxItem{ConversationID: "dm-1", Target: "dm:@Owner", Title: "@Owner", Reason: "dm", UnreadCount: 1, LatestSeq: 10})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
				readDone = true
				_, _ = w.Write([]byte(`{"messages":[]}`))
			}
		}))
		defer srv.Close()

		var d *Daemon
		var ag *hostedAgent
		drv := &scriptedDriver{}
		drv.fn = func(turn int, _ driver.TurnInput) (driver.TurnResult, error) {
			if turn == 1 {
				// Reads DM but forgets `sumi send` (writes plain stdout instead).
				proxyHit(d, ag.driverToken, http.MethodGet, "/conversations/dm-1/messages?unread=true")
			}
			// Even if the agent still declines to send on the nudge, the loop must stop after 1 nudge.
			return driver.TurnResult{ToolCalls: 1}, nil
		}

		d = NewDaemon(Config{ServerURL: srv.URL, DataDir: t.TempDir(), DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) { return drv, nil }}, nil)
		defer d.Stop()
		ag = d.provisionAgent(builtinSpec("m1"))

		d.scheduleWake(context.Background(), "agent-1", "dm-1", "dm")
		waitFor(t, func() bool {
			ag.mu.Lock()
			defer ag.mu.Unlock()
			return !ag.busy
		})
		if drv.count() != 2 {
			t.Fatalf("turns = %d, want 2 (1 initial + 1 nudge)", drv.count())
		}
		if !strings.Contains(drv.promptAt(1), "assistant text output is NOT visible") {
			t.Fatalf("expected silent reply nudge prompt: %s", drv.promptAt(1))
		}
	})

	t.Run("unresolved_held_send_nudges_once", func(t *testing.T) {
		var mu sync.Mutex
		readDone := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case strings.HasSuffix(r.URL.Path, "/inbox"):
				items := []domain.InboxItem{}
				if !readDone {
					items = append(items, domain.InboxItem{ConversationID: "c1", Target: "#dev", Title: "#dev", Reason: "channel", UnreadCount: 1, LatestSeq: 7})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
				readDone = true
				_, _ = w.Write([]byte(`{"messages":[]}`))
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":"held"}}`))
			}
		}))
		defer srv.Close()

		var d *Daemon
		var ag *hostedAgent
		drv := &scriptedDriver{}
		drv.fn = func(turn int, _ driver.TurnInput) (driver.TurnResult, error) {
			if turn == 1 {
				proxyHit(d, ag.driverToken, http.MethodGet, "/conversations/c1/messages?unread=true")
				proxyHit(d, ag.driverToken, http.MethodPost, "/conversations/c1/messages")
			}
			return driver.TurnResult{ToolCalls: 2}, nil
		}

		d = NewDaemon(Config{ServerURL: srv.URL, DataDir: t.TempDir(), DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) { return drv, nil }}, nil)
		defer d.Stop()
		ag = d.provisionAgent(builtinSpec("m1"))

		d.scheduleWake(context.Background(), "agent-1", "c1", "channel")
		waitFor(t, func() bool {
			ag.mu.Lock()
			defer ag.mu.Unlock()
			return !ag.busy
		})
		if drv.count() != 2 {
			t.Fatalf("turns = %d, want 2", drv.count())
		}
		if !strings.Contains(drv.promptAt(1), "Freshness Guard") {
			t.Fatalf("expected held-draft nudge: %s", drv.promptAt(1))
		}
	})

	t.Run("discarded_held_draft_does_not_nudge", func(t *testing.T) {
		var mu sync.Mutex
		readDone := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case strings.HasSuffix(r.URL.Path, "/inbox"):
				items := []domain.InboxItem{}
				if !readDone {
					items = append(items, domain.InboxItem{ConversationID: "c1", Target: "#dev", Title: "#dev", Reason: "channel", UnreadCount: 1, LatestSeq: 8})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
				readDone = true
				_, _ = w.Write([]byte(`{"messages":[]}`))
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"held":true,"unread_messages":[]}`))
			}
		}))
		defer srv.Close()

		var d *Daemon
		var ag *hostedAgent
		drv := &scriptedDriver{}
		drv.fn = func(_ int, _ driver.TurnInput) (driver.TurnResult, error) {
			proxyHit(d, ag.driverToken, http.MethodGet, "/conversations/c1/messages?unread=true")
			proxyHit(d, ag.driverToken, http.MethodPost, "/conversations/c1/messages")
			// Simulate CLI saving a draft on 409 held and the agent running `--discard-draft`
			// (which leaves <homeDir>/drafts present with c1.json deleted).
			draftsDir := filepath.Join(ag.homeDir, "drafts")
			_ = os.MkdirAll(draftsDir, 0o700)
			draftPath := filepath.Join(draftsDir, "c1.json")
			_ = os.WriteFile(draftPath, []byte(`{"conversation_id":"c1"}`), 0o600)
			_ = os.Remove(draftPath)
			return driver.TurnResult{ToolCalls: 1}, nil
		}

		d = NewDaemon(Config{ServerURL: srv.URL, DataDir: t.TempDir(), DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) { return drv, nil }}, nil)
		defer d.Stop()
		ag = d.provisionAgent(builtinSpec("m1"))

		d.scheduleWake(context.Background(), "agent-1", "c1", "channel")
		waitFor(t, func() bool {
			ag.mu.Lock()
			defer ag.mu.Unlock()
			return !ag.busy
		})
		if drv.count() != 1 {
			t.Fatalf("discarded draft must not trigger held nudge, turns = %d", drv.count())
		}
	})

	t.Run("channel_quiet_read_without_work_does_not_nudge", func(t *testing.T) {
		var mu sync.Mutex
		readDone := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case strings.HasSuffix(r.URL.Path, "/inbox"):
				items := []domain.InboxItem{}
				if !readDone {
					items = append(items, domain.InboxItem{ConversationID: "c1", Target: "#general", Title: "#general", Reason: "channel", UnreadCount: 1, LatestSeq: 4})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
				readDone = true
				_, _ = w.Write([]byte(`{"messages":[]}`))
			}
		}))
		defer srv.Close()

		var d *Daemon
		var ag *hostedAgent
		drv := &scriptedDriver{}
		drv.fn = func(_ int, _ driver.TurnInput) (driver.TurnResult, error) {
			proxyHit(d, ag.driverToken, http.MethodGet, "/conversations/c1/messages?unread=true")
			return driver.TurnResult{ToolCalls: 1}, nil
		}

		d = NewDaemon(Config{ServerURL: srv.URL, DataDir: t.TempDir(), DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) { return drv, nil }}, nil)
		defer d.Stop()
		ag = d.provisionAgent(builtinSpec("m1"))

		d.scheduleWake(context.Background(), "agent-1", "c1", "channel")
		waitFor(t, func() bool {
			ag.mu.Lock()
			defer ag.mu.Unlock()
			return !ag.busy
		})
		if drv.count() != 1 {
			t.Fatalf("quiet channel read must not nudge, turns = %d", drv.count())
		}
	})
}

func TestUnreadSpinGuardSkipsUnprogressedItemAndContinuesDrain(t *testing.T) {
	t0 := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	unread := map[string]domain.InboxItem{
		"c1": {ConversationID: "c1", Target: "#stuck", Title: "#stuck", Reason: "mention", UnreadCount: 1, LatestSeq: 5, LatestAt: t0},
		"c2": {ConversationID: "c2", Target: "#other", Title: "#other", Reason: "mention", UnreadCount: 1, LatestSeq: 2, LatestAt: t0.Add(time.Minute)},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/inbox"):
			items := make([]domain.InboxItem, 0, len(unread))
			for _, id := range []string{"c2", "c1"} {
				if it, ok := unread[id]; ok {
					items = append(items, it)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			delete(unread, parts[len(parts)-2])
			_, _ = w.Write([]byte(`{"messages":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"message":{"id":"m1"}}`))
		}
	}))
	defer srv.Close()

	var d *Daemon
	var ag *hostedAgent
	drv := &scriptedDriver{}
	drv.fn = func(_ int, in driver.TurnInput) (driver.TurnResult, error) {
		if strings.Contains(in.UserPrompt, "#other") && strings.Contains(in.UserPrompt, "sumi read #other") {
			proxyHit(d, ag.driverToken, http.MethodGet, "/conversations/c2/messages?unread=true")
			proxyHit(d, ag.driverToken, http.MethodPost, "/conversations/c2/messages")
		}
		// Intentionally never reads #stuck to verify bounded nudge + skip.
		return driver.TurnResult{}, nil
	}

	d = NewDaemon(Config{ServerURL: srv.URL, DataDir: t.TempDir(), DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) { return drv, nil }}, nil)
	defer d.Stop()
	ag = d.provisionAgent(builtinSpec("m1"))

	d.scheduleWake(context.Background(), "agent-1", "c1", "mention")
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy
	})

	// Turn 1: #stuck initial -> Turn 2: #stuck unread nudge -> skips #stuck@5 -> Turn 3: #other handled -> idle.
	if drv.count() != 3 {
		t.Fatalf("turns = %d, want 3", drv.count())
	}
	if !strings.Contains(drv.promptAt(1), "without reading #stuck") {
		t.Fatalf("turn 2 should nudge unread #stuck: %s", drv.promptAt(1))
	}
	if !strings.Contains(drv.promptAt(2), "sumi read #other") {
		t.Fatalf("turn 3 should move on to #other: %s", drv.promptAt(2))
	}
}

type steerableScriptedDriver struct {
	scriptedDriver
	steeredMu sync.Mutex
	steered   []string
	steeredCh chan string
}

func (s *steerableScriptedDriver) Steer(_ context.Context, message string) error {
	s.steeredMu.Lock()
	s.steered = append(s.steered, message)
	s.steeredMu.Unlock()
	if s.steeredCh != nil {
		select {
		case s.steeredCh <- message:
		default:
		}
	}
	return nil
}

func (s *steerableScriptedDriver) steeredCount() int {
	s.steeredMu.Lock()
	defer s.steeredMu.Unlock()
	return len(s.steered)
}

func TestMidTurnSteerInjectsNoticeWithoutAdvancingReadSeq(t *testing.T) {
	t0 := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	unread := map[string]domain.InboxItem{
		"c1": {ConversationID: "c1", Target: "#eng", Title: "#eng", Reason: "mention", UnreadCount: 1, LatestSeq: 1, LatestAt: t0},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/inbox"):
			items := make([]domain.InboxItem, 0, len(unread))
			for _, it := range unread {
				items = append(items, it)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			delete(unread, parts[len(parts)-2])
			_, _ = w.Write([]byte(`{"messages":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"message":{"id":"m1"}}`))
		}
	}))
	defer srv.Close()

	turn1Started := make(chan struct{}, 1)
	releaseTurn1 := make(chan struct{})
	steeredCh := make(chan string, 4)

	var d *Daemon
	var ag *hostedAgent
	drv := &steerableScriptedDriver{steeredCh: steeredCh}
	drv.fn = func(n int, in driver.TurnInput) (driver.TurnResult, error) {
		if n == 1 {
			proxyHit(d, ag.driverToken, http.MethodGet, "/conversations/c1/messages?unread=true")
			turn1Started <- struct{}{}
			<-releaseTurn1
			proxyHit(d, ag.driverToken, http.MethodPost, "/conversations/c1/messages")
			return driver.TurnResult{}, nil
		}
		// Turn 2 drains c2 because mid-turn steer never advanced read_seq on c2.
		proxyHit(d, ag.driverToken, http.MethodGet, "/conversations/c2/messages?unread=true")
		proxyHit(d, ag.driverToken, http.MethodPost, "/conversations/c2/messages")
		return driver.TurnResult{}, nil
	}

	d = NewDaemon(Config{ServerURL: srv.URL, DataDir: t.TempDir(), DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) { return drv, nil }}, nil)
	d.steerDebounce = 25 * time.Millisecond
	defer d.Stop()
	ag = d.provisionAgent(builtinSpec("m1"))

	d.scheduleWake(context.Background(), "agent-1", "c1", "mention")
	select {
	case <-turn1Started:
	case <-time.After(3 * time.Second):
		t.Fatal("turn 1 did not start")
	}

	// New urgent message arrives in #ops while turn 1 is still running.
	mu.Lock()
	unread["c2"] = domain.InboxItem{
		ConversationID:   "c2",
		Target:           "#ops",
		Title:            "#ops",
		Reason:           "mention",
		UnreadCount:      1,
		LatestSeq:        7,
		LatestAuthorName: "Lisi",
		LatestPreview:    "Please pause deploy immediately",
		LatestAt:         t0.Add(time.Minute),
	}
	mu.Unlock()
	d.scheduleWake(context.Background(), "agent-1", "c2", "mention")

	select {
	case notice := <-steeredCh:
		if !strings.Contains(notice, "read_seq was NOT advanced") || !strings.Contains(notice, "#ops") || !strings.Contains(notice, "Please pause deploy immediately") {
			t.Fatalf("unexpected steer notice:\n%s", notice)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected mid-turn steer notice")
	}

	// Duplicate wake for the exact same unread state must be deduplicated (no second steer).
	d.scheduleWake(context.Background(), "agent-1", "c2", "mention")
	time.Sleep(80 * time.Millisecond)
	if got := drv.steeredCount(); got != 1 {
		t.Fatalf("steeredCount = %d, want 1 (duplicate fingerprint must be suppressed)", got)
	}

	// Release turn 1; c2 must still be drained in turn 2 since steer never advanced read_seq.
	close(releaseTurn1)
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy
	})
	if drv.count() != 2 {
		t.Fatalf("turns = %d, want 2 (turn 2 drains c2)", drv.count())
	}
	if !strings.Contains(drv.promptAt(1), "sumi read #ops --unread") {
		t.Fatalf("turn 2 prompt should focus #ops: %s", drv.promptAt(1))
	}
}

func TestWakeLoopGuardBackoffParkAndNewMessageUnpark(t *testing.T) {
	t0 := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	unread := map[string]domain.InboxItem{
		"c1": {ConversationID: "c1", Target: "#stuck", Title: "#stuck", Reason: "mention", UnreadCount: 1, LatestSeq: 5, LatestAt: t0},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/inbox"):
			items := make([]domain.InboxItem, 0, len(unread))
			for _, it := range unread {
				items = append(items, it)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
			delete(unread, "c1")
			_, _ = w.Write([]byte(`{"messages":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"message":{"id":"m9"}}`))
		}
	}))
	defer srv.Close()

	var d *Daemon
	var ag *hostedAgent
	drv := &scriptedDriver{}
	drv.fn = func(turn int, _ driver.TurnInput) (driver.TurnResult, error) {
		if turn <= 3 {
			// Ignore #stuck@5 three times to walk: nudge -> backoff -> park.
			return driver.TurnResult{}, nil
		}
		proxyHit(d, ag.driverToken, http.MethodGet, "/conversations/c1/messages?unread=true")
		proxyHit(d, ag.driverToken, http.MethodPost, "/conversations/c1/messages")
		return driver.TurnResult{ToolCalls: 2}, nil
	}

	d = NewDaemon(Config{ServerURL: srv.URL, DataDir: t.TempDir(), DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) { return drv, nil }}, nil)
	d.wakeLoopBackoff = 60 * time.Millisecond
	defer d.Stop()
	ag = d.provisionAgent(builtinSpec("m1"))

	d.scheduleWake(context.Background(), "agent-1", "c1", "mention")
	// Backoff timer must trigger the 3rd attempt automatically, after which c1@5 is parked.
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy && drv.count() == 3 && ag.wakeBackoffTimer == nil
	})

	// Another wake while c1 is still at seq=5 must not run any new turn.
	d.scheduleWake(context.Background(), "agent-1", "c1", "mention")
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy
	})
	if drv.count() != 3 {
		t.Fatalf("parked conversation ran extra turns: got %d, want 3", drv.count())
	}

	// A newer message (seq=6) automatically unparks c1 and completes normally.
	mu.Lock()
	unread["c1"] = domain.InboxItem{ConversationID: "c1", Target: "#stuck", Title: "#stuck", Reason: "mention", UnreadCount: 2, LatestSeq: 6, LatestAt: t0.Add(time.Minute)}
	mu.Unlock()
	d.scheduleWake(context.Background(), "agent-1", "c1", "mention")
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy && drv.count() == 4
	})
}

type stallAwareDriver struct {
	mu     sync.Mutex
	turns  int
	hang   bool
	onWork func()
}

func (s *stallAwareDriver) RunTurn(ctx context.Context, _ driver.TurnInput) (driver.TurnResult, error) {
	s.mu.Lock()
	s.turns++
	hang := s.hang
	fn := s.onWork
	s.mu.Unlock()
	if hang {
		<-ctx.Done()
		return driver.TurnResult{}, ctx.Err()
	}
	if fn != nil {
		fn()
	}
	return driver.TurnResult{ToolCalls: 1}, nil
}
func (s *stallAwareDriver) ResetSession() error { return nil }
func (s *stallAwareDriver) Close() error        { return nil }
func (s *stallAwareDriver) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turns
}

func TestStalledWithQueuedInboxCancelsAndBoundsReplacements(t *testing.T) {
	t0 := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	unread := map[string]domain.InboxItem{
		"c1": {ConversationID: "c1", Target: "#one", Title: "#one", Reason: "mention", UnreadCount: 1, LatestSeq: 1, LatestAt: t0},
		"c2": {ConversationID: "c2", Target: "#two", Title: "#two", Reason: "mention", UnreadCount: 1, LatestSeq: 1, LatestAt: t0.Add(time.Second)},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/inbox"):
			items := make([]domain.InboxItem, 0, len(unread))
			for _, id := range []string{"c1", "c2"} {
				if it, ok := unread[id]; ok {
					items = append(items, it)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			delete(unread, parts[len(parts)-2])
			_, _ = w.Write([]byte(`{"messages":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"message":{"id":"m1"}}`))
		}
	}))
	defer srv.Close()

	var d *Daemon
	var ag *hostedAgent
	drv := &stallAwareDriver{hang: true}

	d = NewDaemon(Config{ServerURL: srv.URL, DataDir: t.TempDir(), DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) { return drv, nil }}, nil)
	d.activity = activityPolicy{
		minInterval:        time.Hour,
		stallAfter:         time.Hour,
		cancelIdle:         30 * time.Millisecond,
		cancelIdleOpenTool: 30 * time.Millisecond,
		checkEvery:         10 * time.Millisecond,
	}
	d.breaker = breakerPolicy{
		threshold:  10,
		firstProbe: time.Hour,
		maxProbe:   time.Hour,
		firstRetry: 15 * time.Millisecond,
		maxRetry:   30 * time.Millisecond,
	}
	defer d.Stop()
	ag = d.provisionAgent(builtinSpec("m1"))

	// Single wake with 2 unread conversations in inbox (ag.dirty remains false during turn).
	// Must auto-replace at most maxStalledReplacements (2) times, then pause on the 3rd stall.
	d.scheduleWake(context.Background(), "agent-1", "c1", "mention")
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy && ag.paused && ag.stalledStreak == maxStalledReplacements+1
	})
	time.Sleep(80 * time.Millisecond)
	if got := drv.count(); got != maxStalledReplacements+1 {
		t.Fatalf("turns = %d, want %d (initial + %d replacements)", got, maxStalledReplacements+1, maxStalledReplacements)
	}

	// Operator restart clears paused + stalledStreak and drains the remaining inbox cleanly.
	drv.mu.Lock()
	drv.hang = false
	drv.onWork = func() {
		mu.Lock()
		var next string
		for _, id := range []string{"c1", "c2"} {
			if _, ok := unread[id]; ok {
				next = id
				break
			}
		}
		mu.Unlock()
		if next != "" {
			proxyHit(d, ag.driverToken, http.MethodGet, "/conversations/"+next+"/messages?unread=true")
			proxyHit(d, ag.driverToken, http.MethodPost, "/conversations/"+next+"/messages")
		}
	}
	drv.mu.Unlock()

	d.handleControl(context.Background(), protocol.AgentControlFrame{Action: "restart", AgentMemberID: "agent-1"})
	waitFor(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy && !ag.paused && ag.stalledStreak == 0 && drv.count() == maxStalledReplacements+3
	})
}
