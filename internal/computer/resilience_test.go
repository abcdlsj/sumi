package computer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

func TestDriverTokenPersistsAcrossDaemonRestarts(t *testing.T) {
	dataDir := t.TempDir()
	factory := func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
		return &countingDriver{}, nil
	}

	d1 := NewDaemon(Config{DataDir: dataDir, DriverFactory: factory}, nil)
	ag1 := d1.provisionAgent(builtinSpec("m1"))
	tok1 := ag1.driverToken
	if tok1 == "" {
		t.Fatal("expected non-empty driver token")
	}
	info, err := os.Stat(filepath.Join(ag1.homeDir, "driver.token"))
	if err != nil {
		t.Fatalf("driver.token missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("driver.token permissions = %o, want 0600", perm)
	}

	d2 := NewDaemon(Config{DataDir: dataDir, DriverFactory: factory}, nil)
	ag2 := d2.provisionAgent(builtinSpec("m1"))
	if ag2.driverToken != tok1 {
		t.Fatalf("driver token changed across restart: got %q want %q", ag2.driverToken, tok1)
	}
}

func TestReconnectPreservesBusyAgentState(t *testing.T) {
	d := NewDaemon(Config{
		DataDir: t.TempDir(),
		DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			return &countingDriver{}, nil
		},
	}, nil)
	ch := make(chan protocol.Envelope, 4)
	d.wsSendCh = ch

	spec := builtinSpec("m1")
	ag := d.provisionAgent(spec)
	ag.mu.Lock()
	ag.busy = true
	ag.focusConvID = "conv-42"
	ag.dirty = false
	ag.mu.Unlock()

	d.provisionAndWake(context.Background(), spec, "reconnect")

	select {
	case env := <-ch:
		if env.AgentState == nil || env.AgentState.RuntimeState != domain.RuntimeWorking || env.AgentState.FocusConversationID != "conv-42" {
			t.Fatalf("expected working state for conv-42, got %+v", env.AgentState)
		}
	default:
		t.Fatal("expected agent_state frame on reconnect")
	}

	ag.mu.Lock()
	dirty := ag.dirty
	ag.mu.Unlock()
	if !dirty {
		t.Fatal("reconnect during busy turn must mark agent dirty for post-turn inbox check")
	}
}

func TestCapabilityProxyRetriesAcrossServerRestart(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	d := NewDaemon(Config{
		ServerURL:     srv.URL,
		ComputerToken: "comp-tok",
		DataDir:       t.TempDir(),
	}, nil)
	d.proxyRetryDelays = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond}
	d.tokenToAgent["drv-tok"] = "agent-1"

	// Safe GET retries through transient 503s.
	req := httptest.NewRequest(http.MethodGet, "/inbox", nil)
	req.Header.Set("Authorization", "Bearer drv-tok")
	rec := httptest.NewRecorder()
	d.handleCapabilityProxy(rec, req)
	if rec.Code != http.StatusOK || hits.Load() != 3 {
		t.Fatalf("GET retry failed: status=%d hits=%d", rec.Code, hits.Load())
	}

	// POST with Idempotency-Key also retries; POST without Idempotency-Key does not.
	hits.Store(0)
	reqPostKey := httptest.NewRequest(http.MethodPost, "/conversations/c1/messages", strings.NewReader(`{"body":"hi"}`))
	reqPostKey.Header.Set("Authorization", "Bearer drv-tok")
	reqPostKey.Header.Set("Idempotency-Key", "idem-1")
	rec = httptest.NewRecorder()
	d.handleCapabilityProxy(rec, reqPostKey)
	if rec.Code != http.StatusOK || hits.Load() != 3 {
		t.Fatalf("keyed POST retry failed: status=%d hits=%d", rec.Code, hits.Load())
	}

	hits.Store(0)
	reqPostPlain := httptest.NewRequest(http.MethodPost, "/dm", strings.NewReader(`{"peer_member_id":"m1"}`))
	reqPostPlain.Header.Set("Authorization", "Bearer drv-tok")
	rec = httptest.NewRecorder()
	d.handleCapabilityProxy(rec, reqPostPlain)
	if rec.Code != http.StatusServiceUnavailable || hits.Load() != 1 {
		t.Fatalf("unkeyed POST must not retry: status=%d hits=%d", rec.Code, hits.Load())
	}
}

func TestCapabilitySocketPathDeterministicWhenDeep(t *testing.T) {
	deep := filepath.Join(t.TempDir(), strings.Repeat("a", 50), strings.Repeat("b", 50))
	d1 := NewDaemon(Config{DataDir: deep}, nil)
	if err := d1.startCapabilitySocket(); err != nil {
		t.Fatalf("start 1: %v", err)
	}
	p1 := d1.SocketPath()
	d1.Stop()

	d2 := NewDaemon(Config{DataDir: deep}, nil)
	if err := d2.startCapabilitySocket(); err != nil {
		t.Fatalf("start 2: %v", err)
	}
	p2 := d2.SocketPath()
	d2.Stop()

	if p1 != p2 {
		t.Fatalf("socket path changed across restarts: %q vs %q", p1, p2)
	}
}
