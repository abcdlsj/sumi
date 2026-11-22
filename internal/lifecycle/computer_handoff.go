package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/computer"
	"github.com/abcdlsj/sumi/internal/datadir"
)

// ComputerRunnerConfig configures a managed `sumi computer` daemon with local control and live upgrades.
type ComputerRunnerConfig struct {
	DataRoot      string
	ServerURL     string
	ComputerToken string
	DataDir       string
	UseSidecar    bool
	Logger        *slog.Logger
}

// ComputerRunner owns a live computer.Daemon, its control socket, and verified upgrade handoff.
type ComputerRunner struct {
	cfg       ComputerRunnerConfig
	info      BuildInfo
	startedAt time.Time
	logger    *slog.Logger

	mu        sync.Mutex
	daemon    *computer.Daemon
	daemonCtx context.Context
	daemonC   context.CancelFunc
	ctl       *ControlServer
	upgrading bool
	exitOnce  sync.Once
	exitCh    chan struct{}
}

func NewComputerRunner(cfg ComputerRunnerConfig) (*ComputerRunner, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	info, err := CurrentBuildInfo()
	if err != nil {
		return nil, err
	}
	r := &ComputerRunner{
		cfg:       cfg,
		info:      info,
		startedAt: time.Now().UTC(),
		logger:    cfg.Logger,
		exitCh:    make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", r.handleStatus)
	mux.HandleFunc("/upgrade", r.handleUpgrade)
	mux.HandleFunc("/stop", r.handleStop)
	r.ctl = NewControlServer(datadir.ComputerControlSocket(cfg.DataRoot), mux)
	return r, nil
}

// Start boots the Computer daemon and binds computer.ctl.sock.
func (r *ComputerRunner) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	d := computer.NewDaemon(computer.Config{
		ServerURL:     r.cfg.ServerURL,
		ComputerToken: r.cfg.ComputerToken,
		DataDir:       r.cfg.DataDir,
		DaemonVersion: r.info.Version,
		SumiBinary:    r.info.Executable,
		UseSidecar:    r.cfg.UseSidecar,
	}, r.logger)
	if err := d.Start(ctx); err != nil {
		cancel()
		return err
	}
	r.mu.Lock()
	r.daemon = d
	r.daemonCtx = ctx
	r.daemonC = cancel
	r.mu.Unlock()

	if err := r.ctl.Listen(); err != nil {
		cancel()
		d.Stop()
		return err
	}
	_ = WritePID(datadir.ComputerPIDPath(r.cfg.DataRoot), os.Getpid())
	return nil
}

func (r *ComputerRunner) Daemon() *computer.Daemon {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.daemon
}

func (r *ComputerRunner) Done() <-chan struct{} { return r.exitCh }

func (r *ComputerRunner) signalExit() {
	r.exitOnce.Do(func() { close(r.exitCh) })
}

// Shutdown stops the Computer daemon and all its hosted agents cleanly.
func (r *ComputerRunner) Shutdown(graceful bool) {
	r.ctl.Close()
	r.mu.Lock()
	d, cancel := r.daemon, r.daemonC
	r.daemon, r.daemonC = nil, nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if d != nil {
		if graceful {
			d.StopGraceful(2)
		} else {
			d.Stop()
		}
	}
	RemovePIDIfSelf(datadir.ComputerPIDPath(r.cfg.DataRoot))
	r.signalExit()
}

func (r *ComputerRunner) handleStatus(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	d := r.Daemon()
	var ds computer.DaemonStatus
	if d != nil {
		ds = d.Status()
	}
	writeControlJSON(w, http.StatusOK, ComputerStatus{
		PID:        os.Getpid(),
		Version:    r.info.Version,
		BinaryHash: r.info.BinaryHash,
		ServerURL:  r.cfg.ServerURL,
		DataDir:    r.cfg.DataDir,
		DataRoot:   r.cfg.DataRoot,
		Daemon:     ds,
		StartedAt:  r.startedAt,
	})
}

func (r *ComputerRunner) handleStop(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	graceful := req.URL.Query().Get("graceful") != "false"
	writeControlJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go r.Shutdown(graceful)
}

func (r *ComputerRunner) handleUpgrade(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body ComputerUpgradeRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil || strings.TrimSpace(body.TargetBinary) == "" {
		writeControlError(w, http.StatusBadRequest, "target_binary is required")
		return
	}
	resp, err := r.Upgrade(req.Context(), body)
	if err != nil {
		writeControlError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Connection", "close")
	writeControlJSON(w, http.StatusOK, resp)
	go func() {
		time.Sleep(50 * time.Millisecond)
		r.signalExit()
	}()
}

// Upgrade performs a verified replacement of `sumi computer`:
//   - mode "hot" (default when sidecars are enabled): leaves every `sumi agent-host` and in-flight
//     turn running, hands off sockets to the candidate binary, attests PID+hash, then detaches.
//   - mode "drain": holds new wakes until active turns finish (or deadline), gracefully closes
//     drivers so `acp-session.json` flushes, then performs verified handoff.
//
// Any startup or attestation failure automatically kills the candidate and rolls back to this daemon.
func (r *ComputerRunner) Upgrade(ctx context.Context, req ComputerUpgradeRequest) (ComputerUpgradeResponse, error) {
	r.mu.Lock()
	if r.upgrading {
		r.mu.Unlock()
		return ComputerUpgradeResponse{}, errors.New("computer upgrade already in progress")
	}
	r.upgrading = true
	d := r.daemon
	r.mu.Unlock()

	committed := false
	defer func() {
		if !committed {
			r.mu.Lock()
			r.upgrading = false
			r.mu.Unlock()
		}
	}()

	targetInfo, err := ProbeBinary(ctx, req.TargetBinary)
	if err != nil {
		return ComputerUpgradeResponse{}, fmt.Errorf("candidate preflight failed: %w", err)
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		if r.cfg.UseSidecar {
			mode = "hot"
		} else {
			mode = "drain"
		}
	}
	if mode != "hot" && mode != "drain" {
		return ComputerUpgradeResponse{}, fmt.Errorf("unsupported upgrade mode %q", mode)
	}

	var drainOutcome *computer.DrainOutcome
	if mode == "drain" && d != nil {
		deadline := 10 * time.Minute
		if req.DeadlineSeconds > 0 {
			deadline = time.Duration(req.DeadlineSeconds) * time.Second
		}
		out := d.BeginDrain(ctx, uuid.NewString(), deadline)
		drainOutcome = &out
	}

	wasConnected := d != nil && d.Status().Connected

	// Release deterministic sockets so the candidate can bind them while keeping this HTTP
	// request connection alive to return the final outcome.
	r.ctl.ReleaseListener()
	if d != nil {
		d.ReleaseCapabilitySocket()
	}

	rollback := func(cause error) (ComputerUpgradeResponse, error) {
		if d != nil {
			_ = d.RebindCapabilitySocket()
			if mode == "drain" {
				d.EndDrain(context.Background())
			}
		}
		_ = r.ctl.Listen()
		_ = WritePID(datadir.ComputerPIDPath(r.cfg.DataRoot), os.Getpid())
		return ComputerUpgradeResponse{}, cause
	}

	args := []string{
		"computer",
		"--server", r.cfg.ServerURL,
		"--data-dir", r.cfg.DataDir,
		"--data-root", r.cfg.DataRoot,
	}
	if !r.cfg.UseSidecar {
		args = append(args, "--sidecar=false")
	}
	cmd := exec.Command(targetInfo.Executable, args...)
	// Pass the Computer bearer token via env (never argv) so process listings never expose it.
	cmd.Env = setOrReplaceEnv(os.Environ(), "SUMI_COMPUTER_TOKEN", r.cfg.ComputerToken)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = os.MkdirAll(r.cfg.DataDir, 0o700)
	if logFile, err := os.OpenFile(filepath.Join(r.cfg.DataDir, "computer.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	}
	if err := cmd.Start(); err != nil {
		return rollback(fmt.Errorf("spawn candidate computer: %w", err))
	}
	newPID := cmd.Process.Pid
	candExited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(candExited)
	}()

	client := NewControlClient(datadir.ComputerControlSocket(r.cfg.DataRoot))
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
		var cand ComputerStatus
		err := client.GetJSON(probeCtx, "/status", &cand)
		cancel()
		if err == nil && cand.PID == newPID && cand.BinaryHash == targetInfo.BinaryHash {
			if !wasConnected || cand.Daemon.Connected || time.Until(deadline) < 4*time.Second {
				attested = true
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
	}
	if !attested {
		killCandidate(newPID)
		return rollback(errors.New("candidate computer failed readiness attestation; rolled back"))
	}

	// Candidate is live and serving; detach (hot) or gracefully close (drain) this old instance.
	committed = true
	if d != nil {
		if mode == "hot" {
			d.Detach()
		} else {
			d.StopGraceful(2)
		}
	}
	return ComputerUpgradeResponse{
		OldPID:     os.Getpid(),
		NewPID:     newPID,
		TargetHash: targetInfo.BinaryHash,
		Mode:       mode,
		Drain:      drainOutcome,
	}, nil
}

// SpawnDetachedComputer starts a detached `sumi computer` process and waits for it to pass /status.
func SpawnDetachedComputer(ctx context.Context, binaryPath, dataRoot, serverURL, token, dataDir string) (ComputerStatus, error) {
	args := []string{
		"computer",
		"--server", serverURL,
		"--data-dir", dataDir,
		"--data-root", dataRoot,
	}
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = setOrReplaceEnv(os.Environ(), "SUMI_COMPUTER_TOKEN", token)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = os.MkdirAll(dataDir, 0o700)
	if logFile, err := os.OpenFile(filepath.Join(dataDir, "computer.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	}
	if err := cmd.Start(); err != nil {
		return ComputerStatus{}, fmt.Errorf("spawn detached computer: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()

	client := NewControlClient(datadir.ComputerControlSocket(dataRoot))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		var st ComputerStatus
		err := client.GetJSON(probeCtx, "/status", &st)
		cancel()
		if err == nil && st.PID == pid {
			return st, nil
		}
		if !ProcessAlive(pid) {
			return ComputerStatus{}, errors.New("detached computer exited before becoming ready")
		}
		time.Sleep(60 * time.Millisecond)
	}
	return ComputerStatus{}, errors.New("detached computer did not become ready in time")
}

func setOrReplaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+value)
}
