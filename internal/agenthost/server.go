package agenthost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/driver/acp"
	"github.com/abcdlsj/sumi/internal/driver/builtin"
	"github.com/abcdlsj/sumi/internal/protocol"
	"github.com/abcdlsj/sumi/internal/version"
)

// DriverBuilder constructs the underlying SessionDriver for a configured agent spec.
type DriverBuilder func(spec protocol.AssignedAgentSpec, agentHome string) (driver.SessionDriver, error)

// ServerConfig configures an agenthost HTTP-over-Unix server.
type ServerConfig struct {
	SocketPath    string
	MetaPath      string
	HostVersion   string
	DriverBuilder DriverBuilder
}

type activeTurn struct {
	id          string
	focusConvID string
	cancel      context.CancelFunc
	done        chan struct{}

	mu           sync.Mutex
	subscribers  map[chan Event]struct{}
	lastActivity *driver.Activity
	finalEvent   *Event
}

func (t *activeTurn) subscribe() (chan Event, *Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finalEvent != nil {
		ev := *t.finalEvent
		return nil, &ev
	}
	ch := make(chan Event, 64)
	t.subscribers[ch] = struct{}{}
	return ch, nil
}

func (t *activeTurn) unsubscribe(ch chan Event) {
	t.mu.Lock()
	delete(t.subscribers, ch)
	t.mu.Unlock()
}

func (t *activeTurn) broadcast(ev Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ev.Type == "activity" && ev.Activity != nil {
		a := *ev.Activity
		t.lastActivity = &a
	}
	if ev.Type == "done" {
		copyEv := ev
		t.finalEvent = &copyEv
	}
	for ch := range t.subscribers {
		select {
		case ch <- ev:
		default:
			// Drop stale activity ticks for a slow subscriber; done events always retry below.
			if ev.Type == "done" {
				select {
				case <-ch:
				default:
				}
				select {
				case ch <- ev:
				default:
				}
			}
		}
	}
}

// Server hosts one agent's SessionDriver behind a local Unix socket.
type Server struct {
	cfg       ServerConfig
	startedAt time.Time
	httpSrv   *http.Server
	ln        net.Listener

	mu            sync.Mutex
	drv           driver.SessionDriver
	spec          protocol.AssignedAgentSpec
	specHash      string
	homeDir       string
	active        *activeTurn
	lastCompleted *CompletedTurn
	shutdownOnce  sync.Once
	shutdownCh    chan struct{}
}

func NewServer(cfg ServerConfig) *Server {
	if cfg.HostVersion == "" {
		cfg.HostVersion = version.Value()
	}
	if cfg.DriverBuilder == nil {
		cfg.DriverBuilder = DefaultDriverBuilder
	}
	s := &Server{
		cfg:        cfg,
		startedAt:  time.Now().UTC(),
		shutdownCh: make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/configure", s.handleConfigure)
	mux.HandleFunc("/turns", s.handleStartTurn)
	mux.HandleFunc("/turns/", s.handleTurnSubroute)
	mux.HandleFunc("/reset-session", s.handleResetSession)
	mux.HandleFunc("/shutdown", s.handleShutdown)
	s.httpSrv = &http.Server{Handler: mux}
	return s
}

// Start binds the Unix socket (0600) and starts serving asynchronously.
func (s *Server) Start() error {
	if s.cfg.SocketPath == "" {
		return errors.New("agenthost socket path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.SocketPath), 0o700); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	_ = os.Remove(s.cfg.SocketPath)
	ln, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("listen agenthost socket: %w", err)
	}
	_ = os.Chmod(s.cfg.SocketPath, 0o600)
	s.ln = ln
	s.writeMetaLocked("")
	go func() {
		_ = s.httpSrv.Serve(ln)
	}()
	return nil
}

// Done closes when /shutdown completes or Close is called.
func (s *Server) Done() <-chan struct{} {
	return s.shutdownCh
}

// Close stops the sidecar and underlying driver.
func (s *Server) Close(graceful bool) {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		drv := s.drv
		active := s.active
		s.drv = nil
		s.mu.Unlock()

		if active != nil && !graceful {
			active.cancel()
		}
		if drv != nil {
			if graceful {
				if gc, ok := drv.(driver.GracefulCloser); ok {
					_ = gc.GracefulClose(2)
				} else {
					_ = drv.Close()
				}
			} else {
				_ = drv.Close()
			}
		}
		if s.httpSrv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.httpSrv.Shutdown(ctx)
			cancel()
		}
		if s.cfg.SocketPath != "" {
			_ = os.Remove(s.cfg.SocketPath)
		}
		if s.cfg.MetaPath != "" {
			_ = os.Remove(s.cfg.MetaPath)
		}
		close(s.shutdownCh)
	})
}

func (s *Server) writeMetaLocked(specHash string) {
	if s.cfg.MetaPath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.cfg.MetaPath), 0o700)
	raw, _ := json.Marshal(Meta{
		ProtocolVersion: ProtocolVersion,
		PID:             os.Getpid(),
		HostVersion:     s.cfg.HostVersion,
		SocketPath:      s.cfg.SocketPath,
		SpecHash:        specHash,
		StartedAt:       s.startedAt,
	})
	tmp := s.cfg.MetaPath + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, s.cfg.MetaPath)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	resp := StatusResponse{
		ProtocolVersion: ProtocolVersion,
		PID:             os.Getpid(),
		HostVersion:     s.cfg.HostVersion,
		SpecHash:        s.specHash,
		Configured:      s.drv != nil,
		StartedAt:       s.startedAt,
	}
	if s.active != nil {
		resp.Busy = true
		resp.TurnID = s.active.id
		resp.FocusConvID = s.active.focusConvID
		s.active.mu.Lock()
		if s.active.lastActivity != nil {
			a := *s.active.lastActivity
			resp.LastActivity = &a
		}
		s.active.mu.Unlock()
	}
	if s.lastCompleted != nil {
		cp := *s.lastCompleted
		resp.LastCompleted = &cp
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConfigure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req ConfigureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrJSON(w, http.StatusBadRequest, domain.AgentErrDriverConfigInvalid, "invalid configure payload")
		return
	}
	hash := SpecHash(req.Spec)
	s.mu.Lock()
	if s.drv != nil && s.specHash == hash {
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "spec_hash": hash})
		return
	}
	oldDrv := s.drv
	oldActive := s.active
	s.mu.Unlock()

	drv, err := s.cfg.DriverBuilder(req.Spec, req.AgentHome)
	if err != nil {
		code, detail := driverErrorParts(err, req.Spec)
		if code == domain.AgentErrTurnFailed {
			code = domain.AgentErrDriverStartFailed
		}
		writeErrJSON(w, http.StatusUnprocessableEntity, code, detail)
		return
	}
	if oldDrv != nil {
		_ = drv.ResetSession()
	}

	s.mu.Lock()
	s.drv = drv
	s.spec = req.Spec
	s.specHash = hash
	s.homeDir = req.AgentHome
	s.writeMetaLocked(hash)
	s.mu.Unlock()

	if oldActive != nil {
		oldActive.cancel()
	}
	if oldDrv != nil {
		go func() { _ = oldDrv.Close() }()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "spec_hash": hash})
}

func (s *Server) handleStartTurn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req StartTurnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TurnID == "" {
		writeErrJSON(w, http.StatusBadRequest, domain.AgentErrTurnFailed, "invalid turn request")
		return
	}

	s.mu.Lock()
	if s.drv == nil {
		s.mu.Unlock()
		writeErrJSON(w, http.StatusPreconditionFailed, domain.AgentErrDriverStartFailed, "agenthost is not configured")
		return
	}
	if s.active != nil {
		if s.active.id == req.TurnID {
			turn := s.active
			s.mu.Unlock()
			s.streamTurn(w, r, turn)
			return
		}
		s.mu.Unlock()
		writeErrJSON(w, http.StatusConflict, domain.AgentErrTurnFailed, "another turn is already active")
		return
	}

	// Turn context is intentionally independent of r.Context() so a disconnecting
	// computer daemon never cancels the running turn.
	turnCtx, cancel := context.WithCancel(context.Background())
	turn := &activeTurn{
		id:          req.TurnID,
		focusConvID: req.FocusConvID,
		cancel:      cancel,
		done:        make(chan struct{}),
		subscribers: make(map[chan Event]struct{}),
	}
	drv := s.drv
	spec := s.spec
	s.active = turn
	s.mu.Unlock()

	go s.runTurn(turnCtx, drv, spec, turn, req)
	s.streamTurn(w, r, turn)
}

func (s *Server) runTurn(ctx context.Context, drv driver.SessionDriver, spec protocol.AssignedAgentSpec, turn *activeTurn, req StartTurnRequest) {
	defer func() {
		turn.cancel()
		close(turn.done)
	}()

	res, err := drv.RunTurn(ctx, driver.TurnInput{
		SystemPrompt:   req.SystemPrompt,
		StartupContext: req.StartupContext,
		UserPrompt:     req.UserPrompt,
		WorkDir:        req.WorkDir,
		MemoryDir:      req.MemoryDir,
		Env:            req.Env,
		OnActivity: func(a driver.Activity) {
			ac := a
			turn.broadcast(Event{Type: "activity", Activity: &ac})
		},
	})

	ev := Event{Type: "done", Result: &res}
	completed := &CompletedTurn{
		TurnID:      turn.id,
		FocusConvID: turn.focusConvID,
		Result:      res,
		FinishedAt:  time.Now().UTC(),
	}
	if err != nil {
		code, detail := driverErrorParts(err, spec)
		ev.ErrorCode, ev.ErrorDetail = code, detail
		completed.ErrorCode, completed.ErrorDetail = code, detail
	}

	s.mu.Lock()
	s.lastCompleted = completed
	if s.active == turn {
		s.active = nil
	}
	s.mu.Unlock()

	turn.broadcast(ev)
}

func (s *Server) handleTurnSubroute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/turns/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	turnID, action := parts[0], parts[1]
	switch action {
	case "wait":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.mu.Lock()
		active := s.active
		last := s.lastCompleted
		s.mu.Unlock()
		if active != nil && active.id == turnID {
			s.streamTurn(w, r, active)
			return
		}
		if last != nil && last.TurnID == turnID {
			res := last.Result
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(Event{
				Type:        "done",
				Result:      &res,
				ErrorCode:   last.ErrorCode,
				ErrorDetail: last.ErrorDetail,
			})
			return
		}
		writeErrJSON(w, http.StatusNotFound, domain.AgentErrTurnFailed, "turn not found")
	case "cancel":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		if active != nil && active.id == turnID {
			active.cancel()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) streamTurn(w http.ResponseWriter, r *http.Request, turn *activeTurn) {
	ch, alreadyDone := turn.subscribe()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	if alreadyDone != nil {
		_ = enc.Encode(alreadyDone)
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	defer turn.unsubscribe(ch)
	if flusher != nil {
		flusher.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if err := enc.Encode(ev); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			if ev.Type == "done" {
				return
			}
		}
	}
}

func (s *Server) handleResetSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	drv := s.drv
	s.mu.Unlock()
	if drv != nil {
		if err := drv.ResetSession(); err != nil {
			writeErrJSON(w, http.StatusInternalServerError, domain.AgentErrTurnFailed, domain.BoundErrorDetail(err.Error()))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	graceful := r.URL.Query().Get("graceful") == "true"
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	go s.Close(graceful)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErrJSON(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]any{
		"error_code":   code,
		"error_detail": detail,
	})
}

func driverErrorParts(err error, spec protocol.AssignedAgentSpec) (string, string) {
	code := domain.AgentErrTurnFailed
	var f *driver.Failure
	if errors.As(err, &f) && f.Code != "" {
		code = f.Code
	}
	detail := err.Error()
	if key := spec.DriverConfig.APIKey; key != "" {
		detail = strings.ReplaceAll(detail, key, "[redacted]")
	}
	return code, domain.BoundErrorDetail(detail)
}

// DefaultDriverBuilder constructs the production SessionDriver (builtin or ACP).
func DefaultDriverBuilder(spec protocol.AssignedAgentSpec, homeDir string) (driver.SessionDriver, error) {
	cfg, err := domain.NormalizeDriverConfig(spec.DriverKind, spec.DriverConfig)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			err = errors.New(de.Message)
		}
		return nil, driver.Fail(domain.AgentErrDriverConfigInvalid, err)
	}
	switch spec.DriverKind {
	case domain.DriverBuiltin:
		return builtin.New(builtin.Config{
			BaseURL:     cfg.BaseURL,
			APIKey:      cfg.APIKey,
			Model:       cfg.Model,
			SessionFile: filepath.Join(homeDir, "session.jsonl"),
		}, nil)
	case domain.DriverACP:
		acfg := acp.Config{
			Provider:    cfg.Provider,
			Command:     cfg.Command,
			SessionFile: filepath.Join(homeDir, "acp-session.json"),
			Fingerprint: strings.Join([]string{
				"provider=" + cfg.Provider,
				"pi_provider=" + cfg.PiProvider,
				"pi_model=" + cfg.PiModel,
			}, ";"),
		}
		argv, err := acfg.ResolvedCommand()
		if err != nil {
			return nil, driver.Fail(domain.AgentErrDriverConfigInvalid, err)
		}
		bins := []string{argv[0]}
		if len(cfg.Command) == 0 {
			bins = acp.ProviderBinaries(cfg.Provider)
		}
		for _, bin := range bins {
			if _, err := exec.LookPath(bin); err != nil {
				return nil, driver.Fail(domain.AgentErrDriverStartFailed, fmt.Errorf("acp command %q not found on PATH", bin))
			}
		}
		return acp.New(acfg), nil
	default:
		return nil, driver.Fail(domain.AgentErrDriverConfigInvalid, fmt.Errorf("unsupported driver kind %q", spec.DriverKind))
	}
}
