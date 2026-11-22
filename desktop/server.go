package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/sumi/internal/datadir"
	"github.com/abcdlsj/sumi/internal/lifecycle"
)

var errServerUnreachable = errors.New("server unreachable")

// runtime supervises detached `sumi server` and `sumi computer` background processes over their
// local control sockets so quitting or updating the desktop shell never interrupts running agents.
type runtime struct {
	logger       *slog.Logger
	healthCheck  func(context.Context, string) bool
	stageSelfFn  func(context.Context, string) (string, lifecycle.BuildInfo, error)
	stopComputer func(context.Context, string) error

	mu           sync.Mutex
	cfg          Config
	stagedPath   string
	stagedInfo   lifecycle.BuildInfo
	daemonStatus string
	closed       bool
}

func newRuntime(cfg Config, logger *slog.Logger) *runtime {
	return &runtime{
		cfg:          cfg.clone(),
		logger:       logger,
		healthCheck:  healthy,
		stageSelfFn:  defaultStageSelf,
		stopComputer: stopDetachedComputer,
		daemonStatus: "not paired",
	}
}

func defaultStageSelf(ctx context.Context, dataRoot string) (string, lifecycle.BuildInfo, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", lifecycle.BuildInfo{}, err
	}
	return lifecycle.StageBinary(ctx, dataRoot, exe)
}

func stopDetachedComputer(ctx context.Context, dataRoot string) error {
	c := lifecycle.NewControlClient(datadir.ComputerControlSocket(dataRoot))
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.PostJSON(stopCtx, "/stop?graceful=true", nil, nil)
}

func (r *runtime) ensureStaged(ctx context.Context) (string, lifecycle.BuildInfo, error) {
	r.mu.Lock()
	if r.stagedPath != "" {
		p, info := r.stagedPath, r.stagedInfo
		r.mu.Unlock()
		return p, info, nil
	}
	dataDir := r.cfg.DataDir
	r.mu.Unlock()

	p, info, err := r.stageSelfFn(ctx, dataDir)
	if err != nil {
		return "", lifecycle.BuildInfo{}, err
	}
	r.mu.Lock()
	r.stagedPath = p
	r.stagedInfo = info
	r.mu.Unlock()
	return p, info, nil
}

// ensureServer starts or seamlessly hands off the detached local Server for the configured URL.
// An externally managed Server already serving the URL is left untouched unless controlled via server.ctl.sock.
func (r *runtime) ensureServer(ctx context.Context) error {
	return r.ensureServerFor(ctx, r.serverURL())
}

func (r *runtime) ensureServerFor(ctx context.Context, serverURL string) error {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return fmt.Errorf("parse server_url: %w", err)
	}
	isLocal := parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()) && parsed.Port() != ""
	if !isLocal {
		if r.healthCheck(ctx, serverURL) {
			return nil
		}
		return fmt.Errorf("%w: %s", errServerUnreachable, serverURL)
	}

	r.mu.Lock()
	dataDir := r.cfg.DataDir
	r.mu.Unlock()

	stagedPath, info, stageErr := r.ensureStaged(ctx)
	if stageErr != nil {
		// If staging fails in a test/dev environment, accept an already healthy server.
		if r.healthCheck(ctx, serverURL) {
			return nil
		}
		return fmt.Errorf("stage binary: %w", stageErr)
	}

	srvCtl := lifecycle.NewControlClient(datadir.ServerControlSocket(dataDir))
	probeCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	var st lifecycle.ServerStatus
	ctlErr := srvCtl.GetJSON(probeCtx, "/status", &st)
	cancel()
	if ctlErr == nil {
		if st.BinaryHash != info.BinaryHash {
			r.logger.Info("Upgrading local server via seamless handoff", "old_pid", st.PID)
			hoCtx, hoCancel := context.WithTimeout(ctx, 20*time.Second)
			var resp lifecycle.HandoffResponse
			err := srvCtl.PostJSON(hoCtx, "/handoff", lifecycle.HandoffRequest{TargetBinary: stagedPath}, &resp)
			hoCancel()
			if err != nil {
				r.logger.Warn("Server handoff failed; continuing with existing server", "error", err.Error())
			} else {
				r.logger.Info("Local server handed off", "old_pid", resp.OldPID, "new_pid", resp.NewPID)
			}
		}
		return nil
	}

	// An external Server (e.g. `make dev`) may already serve this address without our control socket.
	if r.healthCheck(ctx, serverURL) {
		return nil
	}

	st, err = lifecycle.SpawnDetachedServer(ctx, stagedPath, dataDir, parsed.Host, datadir.DBPath(dataDir))
	if err != nil {
		return err
	}
	r.logger.Info("Detached Sumi server started", "addr", st.Addr, "pid", st.PID)
	return nil
}

// switchServer validates and activates a new server URL without tearing down
// the active connection if the target is unreachable.
func (r *runtime) switchServer(ctx context.Context, rawURL string) (Config, error) {
	targetURL, err := normalizeServerURL(rawURL)
	if err != nil {
		return Config{}, err
	}
	if targetURL == defaultServerURL {
		if err := r.ensureServerFor(ctx, targetURL); err != nil {
			return Config{}, fmt.Errorf("%w: %v", errServerUnreachable, err)
		}
	} else if !r.healthCheck(ctx, targetURL) {
		return Config{}, fmt.Errorf("%w: %s", errServerUnreachable, targetURL)
	}

	r.mu.Lock()
	dataDir := r.cfg.DataDir
	prevURL := r.cfg.ServerURL
	r.mu.Unlock()

	if prevURL != targetURL {
		_ = r.stopComputer(ctx, dataDir)
	}

	r.mu.Lock()
	r.cfg = r.cfg.withServer(targetURL)
	if r.cfg.ComputerToken == "" {
		r.daemonStatus = "waiting for confirmation"
	} else {
		r.daemonStatus = "connecting"
	}
	next := r.cfg.clone()
	r.mu.Unlock()
	return next, nil
}

// startDaemon ensures a detached `sumi computer` daemon is running for the active Server.
// When a newer binary is staged, it performs a hot upgrade so running agent-host sidecars stay alive.
func (r *runtime) startDaemon(parent context.Context) {
	r.mu.Lock()
	if r.closed || r.cfg.ComputerToken == "" {
		r.mu.Unlock()
		return
	}
	serverURL := r.cfg.ServerURL
	token := r.cfg.ComputerToken
	dataDir := r.cfg.DataDir
	r.mu.Unlock()

	stagedPath, info, err := r.ensureStaged(parent)
	if err != nil {
		r.setStatus("error")
		r.logger.Error("Cannot stage binary for computer daemon", "error", err.Error())
		return
	}

	cmpCtl := lifecycle.NewControlClient(datadir.ComputerControlSocket(dataDir))
	probeCtx, cancel := context.WithTimeout(parent, 1*time.Second)
	var st lifecycle.ComputerStatus
	ctlErr := cmpCtl.GetJSON(probeCtx, "/status", &st)
	cancel()

	if ctlErr == nil {
		if st.ServerURL == serverURL {
			if st.BinaryHash != info.BinaryHash {
				r.logger.Info("Hot-upgrading local computer daemon", "old_pid", st.PID)
				upCtx, upCancel := context.WithTimeout(parent, 20*time.Second)
				var resp lifecycle.ComputerUpgradeResponse
				err := cmpCtl.PostJSON(upCtx, "/upgrade", lifecycle.ComputerUpgradeRequest{
					TargetBinary: stagedPath,
					Mode:         "hot",
				}, &resp)
				upCancel()
				if err != nil {
					r.logger.Warn("Computer hot upgrade failed; keeping incumbent daemon", "error", err.Error())
				} else {
					r.logger.Info("Computer daemon hot-upgraded", "old_pid", resp.OldPID, "new_pid", resp.NewPID)
				}
			}
			r.setStatus("online")
			return
		}
		// Running daemon targets a different server; stop it before starting for the new server.
		_ = r.stopComputer(parent, dataDir)
	}

	launched, err := lifecycle.SpawnDetachedComputer(parent, stagedPath, dataDir, serverURL, token, datadir.ComputerDir(dataDir))
	if err != nil {
		r.setStatus("error")
		r.logger.Error("Detached computer daemon failed to start", "error", err.Error())
		return
	}
	r.logger.Info("Detached computer daemon started", "pid", launched.PID, "server", serverURL)
	r.setStatus("online")
}

// restartDaemon hot-restarts the detached Computer daemon without interrupting running agents,
// or starts it if not currently running.
func (r *runtime) restartDaemon(parent context.Context) {
	r.mu.Lock()
	dataDir := r.cfg.DataDir
	hasToken := r.cfg.ComputerToken != ""
	r.mu.Unlock()
	if !hasToken {
		return
	}
	stagedPath, _, err := r.ensureStaged(parent)
	if err != nil {
		r.setStatus("error")
		return
	}
	cmpCtl := lifecycle.NewControlClient(datadir.ComputerControlSocket(dataDir))
	upCtx, cancel := context.WithTimeout(parent, 20*time.Second)
	var resp lifecycle.ComputerUpgradeResponse
	err = cmpCtl.PostJSON(upCtx, "/upgrade", lifecycle.ComputerUpgradeRequest{
		TargetBinary: stagedPath,
		Mode:         "hot",
	}, &resp)
	cancel()
	if err == nil {
		r.logger.Info("Computer daemon hot-restarted", "old_pid", resp.OldPID, "new_pid", resp.NewPID)
		r.setStatus("online")
		return
	}
	r.startDaemon(parent)
}

// detach marks the desktop shell closed while leaving detached server, computer, and agents running.
func (r *runtime) detach() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

// stopAll explicitly stops the detached computer daemon (gracefully) and local server.
func (r *runtime) stopAll() {
	r.mu.Lock()
	r.closed = true
	dataDir := r.cfg.DataDir
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.stopComputer(ctx, dataDir)
	srvCtl := lifecycle.NewControlClient(datadir.ServerControlSocket(dataDir))
	_ = srvCtl.PostJSON(ctx, "/stop", nil, nil)
}

func (r *runtime) serverURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg.ServerURL
}

func (r *runtime) configSnapshot() Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg.clone()
}

func (r *runtime) hasComputerToken() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg.ComputerToken != ""
}

func (r *runtime) setComputerToken(serverURL, token string) Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = r.cfg.withToken(serverURL, token)
	return r.cfg.clone()
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
