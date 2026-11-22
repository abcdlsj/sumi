package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/datadir"
	"github.com/abcdlsj/sumi/internal/server"
	"github.com/abcdlsj/sumi/internal/store"
)

const InheritListenFDEnv = "SUMI_INHERIT_LISTEN_FD"

// ServerRunnerConfig configures a managed `sumi server` instance with local control and FD handoff.
type ServerRunnerConfig struct {
	DataRoot  string
	Addr      string
	DBPath    string
	WebOrigin string
	WebDir    string
	StaticFS  http.Handler
	Logger    *slog.Logger
}

// ServerRunner owns a live Sumi server, its SQLite store, TCP listener, and control socket.
type ServerRunner struct {
	cfg       ServerRunnerConfig
	info      BuildInfo
	startedAt time.Time
	logger    *slog.Logger

	mu         sync.Mutex
	st         *store.Store
	srv        *server.Server
	httpSrv    *http.Server
	ln         net.Listener
	ctl        *ControlServer
	handingOff bool
	exitOnce   sync.Once
	exitCh     chan struct{}
}

// ListenOrInherit adopts an inherited TCP listener FD during seamless handoff, or binds addr fresh.
func ListenOrInherit(addr string) (net.Listener, error) {
	rawFD := strings.TrimSpace(os.Getenv(InheritListenFDEnv))
	if rawFD != "" {
		_ = os.Unsetenv(InheritListenFDEnv)
		fd, err := strconv.Atoi(rawFD)
		if err != nil || fd < 3 {
			return nil, fmt.Errorf("invalid %s value %q", InheritListenFDEnv, rawFD)
		}
		f := os.NewFile(uintptr(fd), "sumi-inherited-listener")
		if f == nil {
			return nil, fmt.Errorf("open inherited listener fd %d", fd)
		}
		ln, err := net.FileListener(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("adopt inherited listener: %w", err)
		}
		return ln, nil
	}
	return net.Listen("tcp", addr)
}

func NewServerRunner(cfg ServerRunnerConfig) (*ServerRunner, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	info, err := CurrentBuildInfo()
	if err != nil {
		return nil, err
	}
	r := &ServerRunner{
		cfg:       cfg,
		info:      info,
		startedAt: time.Now().UTC(),
		logger:    cfg.Logger,
		exitCh:    make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", r.handleStatus)
	mux.HandleFunc("/handoff", r.handleHandoff)
	mux.HandleFunc("/stop", r.handleStop)
	r.ctl = NewControlServer(datadir.ServerControlSocket(cfg.DataRoot), mux)
	return r, nil
}

// Start opens SQLite, binds or inherits the TCP listener, and exposes server.ctl.sock.
func (r *ServerRunner) Start() error {
	ln, err := ListenOrInherit(r.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", r.cfg.Addr, err)
	}
	if err := r.startServing(ln); err != nil {
		_ = ln.Close()
		return err
	}
	if err := r.ctl.Listen(); err != nil {
		r.stopServing("server startup failed")
		return err
	}
	_ = WritePID(datadir.ServerPIDPath(r.cfg.DataRoot), os.Getpid())
	return nil
}

func (r *ServerRunner) startServing(ln net.Listener) error {
	st, err := store.Open(r.cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open sqlite store: %w", err)
	}
	svc := app.NewService(st, app.NewEventHub(), nil)
	srv := server.New(svc, server.Config{
		WebOrigin:  r.cfg.WebOrigin,
		StaticFS:   r.cfg.StaticFS,
		AppVersion: r.info.Version,
	}, r.logger)
	httpSrv := &http.Server{
		Addr:              r.cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	r.mu.Lock()
	r.st = st
	r.srv = srv
	r.httpSrv = httpSrv
	r.ln = ln
	r.mu.Unlock()

	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			r.logger.Error("HTTP server stopped unexpectedly", "error", err.Error())
		}
	}()
	return nil
}

func (r *ServerRunner) stopServing(reason string) {
	r.mu.Lock()
	srv, httpSrv, st := r.srv, r.httpSrv, r.st
	r.srv, r.httpSrv, r.st, r.ln = nil, nil, nil, nil
	r.mu.Unlock()

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if srv != nil {
		srv.ShutdownWithReason(shutCtx, reason)
	}
	if httpSrv != nil {
		_ = httpSrv.Shutdown(shutCtx)
	}
	if st != nil {
		_ = st.Close()
	}
}

// Done closes when the server has been asked to stop or has completed a verified handoff.
func (r *ServerRunner) Done() <-chan struct{} { return r.exitCh }

func (r *ServerRunner) signalExit() {
	r.exitOnce.Do(func() { close(r.exitCh) })
}

// Shutdown cleanly stops the server, control socket, and PID file.
func (r *ServerRunner) Shutdown() {
	r.ctl.Close()
	r.stopServing("server shutting down")
	RemovePIDIfSelf(datadir.ServerPIDPath(r.cfg.DataRoot))
	r.signalExit()
}

func (r *ServerRunner) handleStatus(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.mu.Lock()
	addr := r.cfg.Addr
	if r.ln != nil {
		addr = r.ln.Addr().String()
	}
	r.mu.Unlock()
	writeControlJSON(w, http.StatusOK, ServerStatus{
		PID:        os.Getpid(),
		Version:    r.info.Version,
		BinaryHash: r.info.BinaryHash,
		Addr:       addr,
		DBPath:     r.cfg.DBPath,
		DataRoot:   r.cfg.DataRoot,
		StartedAt:  r.startedAt,
	})
}

func (r *ServerRunner) handleStop(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeControlJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go r.Shutdown()
}

func (r *ServerRunner) handleHandoff(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body HandoffRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil || strings.TrimSpace(body.TargetBinary) == "" {
		writeControlError(w, http.StatusBadRequest, "target_binary is required")
		return
	}
	resp, err := r.Handoff(req.Context(), body.TargetBinary)
	if err != nil {
		writeControlError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Connection", "close")
	writeControlJSON(w, http.StatusOK, resp)
	// Give the HTTP response a moment to flush on the already-accepted connection before exiting.
	go func() {
		time.Sleep(50 * time.Millisecond)
		r.signalExit()
	}()
}

// Handoff performs a verified zero-refuse replacement of the running server:
// 1) Probe candidate binary, 2) SQLite online backup, 3) Dup TCP listener FD,
// 4) Notify computers + drain HTTP + close SQLite, 5) Spawn candidate with inherited FD,
// 6) Attest candidate PID & hash on server.ctl.sock, 7) Roll back on any failure.
func (r *ServerRunner) Handoff(ctx context.Context, targetBinary string) (HandoffResponse, error) {
	r.mu.Lock()
	if r.handingOff {
		r.mu.Unlock()
		return HandoffResponse{}, errors.New("server handoff already in progress")
	}
	r.handingOff = true
	st := r.st
	ln := r.ln
	r.mu.Unlock()

	committed := false
	defer func() {
		if !committed {
			r.mu.Lock()
			r.handingOff = false
			r.mu.Unlock()
		}
	}()

	targetInfo, err := ProbeBinary(ctx, targetBinary)
	if err != nil {
		return HandoffResponse{}, fmt.Errorf("candidate preflight failed: %w", err)
	}
	tcpLn, ok := ln.(*net.TCPListener)
	if !ok {
		return HandoffResponse{}, errors.New("active listener is not a TCP listener")
	}
	lnFile, err := tcpLn.File()
	if err != nil {
		return HandoffResponse{}, fmt.Errorf("duplicate listener fd: %w", err)
	}

	// Online backup before touching the database connection.
	backupPath := filepath.Join(datadir.BackupsDir(r.cfg.DataRoot), fmt.Sprintf("sumi-%s.db", time.Now().UTC().Format("20060102-150405")))
	if st != nil && r.cfg.DBPath != ":memory:" {
		if err := st.BackupTo(ctx, backupPath); err != nil {
			_ = lnFile.Close()
			return HandoffResponse{}, fmt.Errorf("pre-upgrade sqlite backup failed: %w", err)
		}
	} else {
		backupPath = ""
	}

	// Notify connected computers to reconnect quickly, drain in-flight HTTP, and release SQLite.
	r.stopServing("server_restarting")
	r.ctl.ReleaseListener()

	rollback := func(cause error) (HandoffResponse, error) {
		restoredLn, lnErr := net.FileListener(lnFile)
		_ = lnFile.Close()
		if lnErr == nil {
			_ = r.startServing(restoredLn)
		}
		_ = r.ctl.Listen()
		_ = WritePID(datadir.ServerPIDPath(r.cfg.DataRoot), os.Getpid())
		return HandoffResponse{}, cause
	}

	args := []string{
		"server",
		"--addr", r.cfg.Addr,
		"--db", r.cfg.DBPath,
		"--data-root", r.cfg.DataRoot,
	}
	if r.cfg.WebOrigin != "" {
		args = append(args, "--web-origin", r.cfg.WebOrigin)
	}
	if r.cfg.WebDir != "" {
		args = append(args, "--web-dir", r.cfg.WebDir)
	}
	cmd := exec.Command(targetInfo.Executable, args...)
	cmd.ExtraFiles = []*os.File{lnFile}
	cmd.Env = append(os.Environ(), InheritListenFDEnv+"=3")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logPath := filepath.Join(r.cfg.DataRoot, "server.log")
	if logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	}
	if err := cmd.Start(); err != nil {
		return rollback(fmt.Errorf("spawn candidate server: %w", err))
	}
	newPID := cmd.Process.Pid
	// Reap the child if it exits prematurely so rollback triggers immediately instead of waiting on a zombie.
	candExited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(candExited)
	}()

	// Wait for the candidate server to bind server.ctl.sock and attest its PID + binary hash.
	client := NewControlClient(datadir.ServerControlSocket(r.cfg.DataRoot))
	deadline := time.Now().Add(10 * time.Second)
	var attested bool
	for time.Now().Before(deadline) {
		select {
		case <-candExited:
			deadline = time.Time{}
		default:
		}
		if deadline.IsZero() {
			break
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		var cand ServerStatus
		err := client.GetJSON(probeCtx, "/status", &cand)
		cancel()
		if err == nil && cand.PID == newPID && cand.BinaryHash == targetInfo.BinaryHash {
			attested = true
			break
		}
		time.Sleep(60 * time.Millisecond)
	}
	if !attested {
		killCandidate(newPID)
		return rollback(errors.New("candidate server failed readiness attestation; rolled back"))
	}

	_ = lnFile.Close()
	committed = true
	return HandoffResponse{
		OldPID:     os.Getpid(),
		NewPID:     newPID,
		TargetHash: targetInfo.BinaryHash,
		BackupPath: backupPath,
	}, nil
}

// SpawnDetachedServer starts a detached `sumi server` process and waits for it to pass /status.
func SpawnDetachedServer(ctx context.Context, binaryPath, dataRoot, addr, dbPath string) (ServerStatus, error) {
	args := []string{
		"server",
		"--addr", addr,
		"--db", dbPath,
		"--data-root", dataRoot,
	}
	cmd := exec.Command(binaryPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = os.MkdirAll(dataRoot, 0o700)
	if logFile, err := os.OpenFile(filepath.Join(dataRoot, "server.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	}
	if err := cmd.Start(); err != nil {
		return ServerStatus{}, fmt.Errorf("spawn detached server: %w", err)
	}
	pid := cmd.Process.Pid
	// Reap in the background: releasing without waiting would leave a zombie
	// behind for as long as this process lives.
	go func() { _ = cmd.Wait() }()

	client := NewControlClient(datadir.ServerControlSocket(dataRoot))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		var st ServerStatus
		err := client.GetJSON(probeCtx, "/status", &st)
		cancel()
		if err == nil && st.PID == pid {
			return st, nil
		}
		if !ProcessAlive(pid) {
			killCandidate(pid)
			return ServerStatus{}, errors.New("detached server exited before becoming ready")
		}
		time.Sleep(60 * time.Millisecond)
	}
	// A spawned-but-never-ready child must not outlive this call.
	killCandidate(pid)
	return ServerStatus{}, errors.New("detached server did not become ready in time")
}
