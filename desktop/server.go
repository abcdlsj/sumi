package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/computer"
	"github.com/abcdlsj/sumi/internal/server"
	"github.com/abcdlsj/sumi/internal/store"
)

// runtime owns the loopback Server and the local Computer daemon for the shell's lifetime.
type runtime struct {
	cfg    Config
	logger *slog.Logger

	mu      sync.Mutex
	httpSrv *http.Server
	daemon  *computer.Daemon
	daemonC context.CancelFunc

	daemonStatus string
	closed       bool
}

func newRuntime(cfg Config, logger *slog.Logger) *runtime {
	return &runtime{cfg: cfg, logger: logger, daemonStatus: "not paired"}
}

// ensureServer starts an in-process Server when the configured one is unreachable.
// A reachable external Server is always preferred so the shell never forks state.
func (r *runtime) ensureServer(ctx context.Context) error {
	if healthy(ctx, r.cfg.ServerURL) {
		return nil
	}
	parsed, err := url.Parse(r.cfg.ServerURL)
	if err != nil {
		return fmt.Errorf("parse server_url: %w", err)
	}
	if parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) || parsed.Port() == "" {
		return errors.New("server_url must be http://127.0.0.1:<port> for the embedded server")
	}

	assets := webAssets()

	dbPath := filepath.Join(r.cfg.DataDir, "sumi.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	svc := app.NewService(st, app.NewEventHub(), nil)
	srv := server.New(svc, server.Config{StaticFS: server.SPAHandler(assets)}, r.logger)
	httpSrv := &http.Server{
		Addr:              parsed.Host,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", parsed.Host)
	if err != nil {
		_ = st.Close()
		return fmt.Errorf("listen on %s: %w", parsed.Host, err)
	}

	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			r.logger.Error("Embedded server stopped", "error", err.Error())
		}
	}()
	r.logger.Info("Embedded Sumi server started", "addr", parsed.Host, "db", dbPath)

	// Wait for the server to accept connections before the window navigates.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if healthy(ctx, r.cfg.ServerURL) {
			r.mu.Lock()
			r.httpSrv = httpSrv
			r.mu.Unlock()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = httpSrv.Close()
	_ = st.Close()
	return errors.New("embedded server did not become healthy")
}

// startDaemon launches the Computer daemon when a token is configured.
func (r *runtime) startDaemon(parent context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.daemon != nil || r.cfg.ComputerToken == "" {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	d := computer.NewDaemon(computer.Config{
		ServerURL:     r.cfg.ServerURL,
		ComputerToken: r.cfg.ComputerToken,
		DataDir:       filepath.Join(r.cfg.DataDir, "computer"),
	}, r.logger)
	if err := d.Start(ctx); err != nil {
		r.daemonStatus = "error"
		r.logger.Error("Computer daemon failed to start", "error", err.Error())
		cancel()
		return
	}
	r.daemon = d
	r.daemonC = cancel
	r.daemonStatus = "online"
}

// restartDaemon stops and starts the daemon; used by the tray menu.
func (r *runtime) restartDaemon(parent context.Context) {
	r.stopDaemon()
	r.startDaemon(parent)
}

func (r *runtime) stopDaemon() {
	r.mu.Lock()
	d, cancel := r.daemon, r.daemonC
	r.daemon, r.daemonC = nil, nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if d != nil {
		d.Stop()
	}
}

func (r *runtime) shutdown() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.stopDaemon()
	r.mu.Lock()
	srv := r.httpSrv
	r.httpSrv = nil
	r.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
}

func (r *runtime) hasComputerToken() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg.ComputerToken != ""
}

func (r *runtime) setComputerToken(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg.ComputerToken = token
}

func (r *runtime) setStatus(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.daemonStatus = status
}

func (r *runtime) status() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.daemonStatus
}

func healthy(ctx context.Context, baseURL string) bool {
	reqCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL+"/api/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	default:
		return false
	}
}
