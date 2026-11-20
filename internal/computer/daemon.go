package computer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/driver/acp"
	"github.com/abcdlsj/sumi/internal/driver/builtin"
	"github.com/abcdlsj/sumi/internal/protocol"
)

type Config struct {
	ServerURL     string
	ComputerToken string
	DataDir       string
	DaemonVersion string
	// SumiBinary is the executable exposed to agents as `sumi` on PATH; defaults to os.Executable().
	SumiBinary string
	// Optional factory override for deterministic scenario tests.
	DriverFactory func(spec protocol.AssignedAgentSpec, agentHome string) (driver.SessionDriver, error)
}

type hostedAgent struct {
	homeDir     string
	workDir     string
	memoryDir   string
	driverToken string

	mu sync.Mutex
	// spec and drv are replaced on re-provision; drv is nil when the last provision failed,
	// in which case failCode/failDetail describe why.
	spec       protocol.AssignedAgentSpec
	drv        driver.SessionDriver
	failCode   string
	failDetail string
	busy       bool
	dirty      bool
	lastReason string
	lastConvID string

	// Failure breaker: after failurePauseThreshold consecutive failed turns the Agent stops
	// taking wakes until a probe timer, Restart, Reset session or a new spec. Unread triggers stay
	// on the Server, so nothing is lost while paused.
	failStreak int
	paused     bool
	probeDelay time.Duration
	probeTimer *time.Timer
	// retryTimer re-runs the loop after a failed turn; wakes during the backoff only set dirty.
	retryTimer *time.Timer
}

// resetBreakerLocked clears the failure breaker; caller holds ag.mu.
func (ag *hostedAgent) resetBreakerLocked() {
	ag.failStreak, ag.paused, ag.probeDelay = 0, false, 0
	if ag.probeTimer != nil {
		ag.probeTimer.Stop()
		ag.probeTimer = nil
	}
	if ag.retryTimer != nil {
		ag.retryTimer.Stop()
		ag.retryTimer = nil
	}
}

type Daemon struct {
	cfg             Config
	breaker         breakerPolicy
	activity        activityPolicy
	logger          *slog.Logger
	daemonSessionID string
	httpClient      *http.Client

	mu           sync.RWMutex
	agents       map[string]*hostedAgent
	tokenToAgent map[string]string
	wsSendCh     chan protocol.Envelope
	serverCaps   []string
	socketPath   string
	socketServer *http.Server
	socketLn     net.Listener
	binDir       string
	stopCh       chan struct{}
	stopOnce     sync.Once
}

type breakerPolicy struct {
	threshold  int
	firstProbe time.Duration
	maxProbe   time.Duration
	// firstRetry/maxRetry back off re-running a failed turn below the threshold.
	firstRetry time.Duration
	maxRetry   time.Duration
}

var defaultBreakerPolicy = breakerPolicy{threshold: 3, firstProbe: 10 * time.Minute, maxProbe: 2 * time.Hour, firstRetry: 10 * time.Second, maxRetry: 5 * time.Minute}

// errTurnStalled is the cancel cause when a silent turn is cut so queued work can run.
var errTurnStalled = errors.New("turn produced no runtime events while new work was waiting")

func NewDaemon(cfg Config, logger *slog.Logger) *Daemon {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.DaemonVersion == "" {
		cfg.DaemonVersion = "2.0.0"
	}
	if cfg.DataDir == "" {
		home, _ := os.UserHomeDir()
		cfg.DataDir = filepath.Join(home, ".sumi")
	}
	return &Daemon{
		cfg:             cfg,
		breaker:         defaultBreakerPolicy,
		activity:        defaultActivityPolicy,
		logger:          logger,
		daemonSessionID: uuid.NewString(),
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		agents:          make(map[string]*hostedAgent),
		tokenToAgent:    make(map[string]string),
		stopCh:          make(chan struct{}),
	}
}

func (d *Daemon) SocketPath() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.socketPath
}

func (d *Daemon) Start(ctx context.Context) error {
	if err := os.MkdirAll(d.cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create daemon data dir: %w", err)
	}
	if err := d.installCLI(); err != nil {
		return err
	}
	if err := d.startCapabilitySocket(); err != nil {
		return err
	}
	go d.connectLoop(ctx)
	return nil
}

// installCLI points <DataDir>/bin/sumi at the current executable so agents can run `sumi`.
func (d *Daemon) installCLI() error {
	target := d.cfg.SumiBinary
	if target == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve daemon executable: %w", err)
		}
		target = exe
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	binDir := filepath.Join(d.cfg.DataDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return fmt.Errorf("create cli bin dir: %w", err)
	}
	link := filepath.Join(binDir, "sumi")
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("link sumi cli: %w", err)
	}
	if err := os.Rename(tmp, link); err != nil {
		return fmt.Errorf("install sumi cli: %w", err)
	}
	d.mu.Lock()
	d.binDir = binDir
	d.mu.Unlock()
	return nil
}

// hostOS maps runtime.GOOS onto the protocol's os values.
func hostOS() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}

// detectACPProviders reports providers whose default command binaries resolve on PATH.
func detectACPProviders() []string {
	out := make([]string, 0, len(domain.ACPProviders))
	for _, p := range domain.ACPProviders {
		ok := true
		for _, bin := range acp.ProviderBinaries(p) {
			if _, err := exec.LookPath(bin); err != nil {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

func (d *Daemon) Stop() {
	d.stopOnce.Do(func() {
		close(d.stopCh)
		if d.socketServer != nil {
			_ = d.socketServer.Close()
		}
		if d.socketPath != "" {
			_ = os.Remove(d.socketPath)
		}
		d.mu.Lock()
		for _, ag := range d.agents {
			ag.mu.Lock()
			ag.resetBreakerLocked()
			if ag.drv != nil {
				_ = ag.drv.Close()
			}
			ag.mu.Unlock()
		}
		d.mu.Unlock()
	})
}

func (d *Daemon) startCapabilitySocket() error {
	sockPath := filepath.Join(d.cfg.DataDir, "capability.sock")
	// macOS sockaddr_un.sun_path is limited to 104 bytes; fall back to /tmp when DataDir is deep.
	if len(sockPath) > 96 {
		sockPath = filepath.Join(os.TempDir(), "sumi-"+d.daemonSessionID[:8]+".sock")
	}
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen capability socket: %w", err)
	}
	_ = os.Chmod(sockPath, 0o600)

	srv := &http.Server{Handler: http.HandlerFunc(d.handleCapabilityProxy)}
	d.mu.Lock()
	d.socketPath = sockPath
	d.socketLn = ln
	d.socketServer = srv
	d.mu.Unlock()

	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

// handleCapabilityProxy verifies per-agent SUMI_DRIVER_TOKEN and forwards requests to Server
// using the Computer bearer token so agents never hold Server credentials.
func (d *Daemon) handleCapabilityProxy(w http.ResponseWriter, r *http.Request) {
	driverToken := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	d.mu.RLock()
	agentID := d.tokenToAgent[driverToken]
	d.mu.RUnlock()
	if agentID == "" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated","message":"invalid driver token"}}`))
		return
	}

	targetURL := strings.TrimRight(d.cfg.ServerURL, "/") + "/api/v1/computer/agents/" + agentID + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.ComputerToken)
	for _, h := range []string{"Content-Type", "Idempotency-Key"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := d.httpClient.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"server unreachable"}}`))
		return
	}
	defer resp.Body.Close()
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (d *Daemon) connectLoop(ctx context.Context) {
	backoff := 500 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		default:
		}
		connectedAt := time.Now()
		err := d.runConnection(ctx)
		if err != nil {
			d.logger.Warn("Computer connection ended", "error", err.Error())
		}
		if time.Since(connectedAt) > 60*time.Second {
			backoff = 500 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case <-time.After(backoff):
			if backoff < 15*time.Second {
				backoff *= 2
			}
		}
	}
}

func (d *Daemon) runConnection(ctx context.Context) error {
	wsURL := strings.Replace(strings.TrimRight(d.cfg.ServerURL, "/"), "http", "ws", 1) + "/ws/computer"
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + d.cfg.ComputerToken}},
	})
	cancel()
	if err != nil {
		return err
	}
	defer conn.CloseNow()

	hostname, _ := os.Hostname()
	hello := protocol.Envelope{
		Type: protocol.TypeHello,
		Hello: &protocol.HelloFrame{
			ProtocolVersion: protocol.Version,
			DaemonSessionID: d.daemonSessionID,
			DaemonVersion:   d.cfg.DaemonVersion,
			Hostname:        hostname,
			OS:              hostOS(),
			ACPProviders:    detectACPProviders(),
			Capabilities:    protocol.Supported,
		},
	}
	helloBytes, _ := json.Marshal(hello)
	if err := conn.Write(ctx, websocket.MessageText, helloBytes); err != nil {
		return err
	}

	_, ackBytes, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	var ack protocol.Envelope
	if err := json.Unmarshal(ackBytes, &ack); err != nil || ack.Type != protocol.TypeHelloAck || ack.HelloAck == nil {
		return fmt.Errorf("unexpected hello_ack")
	}

	if !protocol.Compatible(ack.HelloAck.ProtocolVersion) {
		return fmt.Errorf("server protocol version %d is not supported", ack.HelloAck.ProtocolVersion)
	}

	sendCh := make(chan protocol.Envelope, 64)
	d.mu.Lock()
	d.wsSendCh = sendCh
	d.serverCaps = ack.HelloAck.Capabilities
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		if d.wsSendCh == sendCh {
			d.wsSendCh = nil
		}
		d.mu.Unlock()
		close(sendCh)
	}()

	// Provision all assigned agents and trigger initial inbox check on reconnect.
	for _, sp := range ack.HelloAck.Agents {
		d.provisionAndWake(ctx, sp, "reconnect")
	}

	// Writer + heartbeat loop.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		hb := time.NewTicker(15 * time.Second)
		defer hb.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-d.stopCh:
				return
			case <-hb.C:
				raw, _ := json.Marshal(protocol.Envelope{
					Type:      protocol.TypeHeartbeat,
					Heartbeat: &protocol.HeartbeatFrame{DaemonSessionID: d.daemonSessionID},
				})
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Write(wctx, websocket.MessageText, raw)
				wcancel()
				if err != nil {
					return
				}
			case env, ok := <-sendCh:
				if !ok {
					return
				}
				raw, _ := json.Marshal(env)
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Write(wctx, websocket.MessageText, raw)
				wcancel()
				if err != nil {
					return
				}
			}
		}
	}()
	defer func() { <-writerDone }()

	for {
		_, msgBytes, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var env protocol.Envelope
		if err := json.Unmarshal(msgBytes, &env); err != nil {
			continue
		}
		switch env.Type {
		case protocol.TypeWake:
			if env.Wake != nil {
				d.scheduleWake(ctx, env.Wake.AgentMemberID, env.Wake.ConversationID, env.Wake.Reason)
			}
		case protocol.TypeAgentControl:
			if env.AgentControl != nil {
				d.handleControl(ctx, *env.AgentControl)
			}
		case protocol.TypeShutdown:
			return nil
		}
	}
}

func (d *Daemon) serverHas(c string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return protocol.Has(d.serverCaps, c)
}

func (d *Daemon) sendWS(env protocol.Envelope) {
	d.mu.RLock()
	ch := d.wsSendCh
	d.mu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- env:
	default:
	}
}

func (d *Daemon) reportAgentState(agentID string, state domain.RuntimeState, focusConvID, errCode, errDetail string) {
	d.reportAgentStateWith(agentID, state, focusConvID, errCode, errDetail, nil)
}

func (d *Daemon) reportAgentStateWith(agentID string, state domain.RuntimeState, focusConvID, errCode, errDetail string, activity *domain.AgentActivity) {
	if activity != nil && !d.serverHas(protocol.CapAgentActivity) {
		return
	}
	d.sendWS(protocol.Envelope{
		Type: protocol.TypeAgentState,
		AgentState: &protocol.AgentStateFrame{
			DaemonSessionID:     d.daemonSessionID,
			AgentMemberID:       agentID,
			RuntimeState:        state,
			FocusConversationID: focusConvID,
			ErrorCode:           errCode,
			ErrorDetail:         errDetail,
			Activity:            activity,
		},
	})
}

// provisionAndWake (re)provisions an agent and reports its resulting state.
func (d *Daemon) provisionAndWake(ctx context.Context, spec protocol.AssignedAgentSpec, reason string) {
	ag := d.provisionAgent(spec)
	ag.mu.Lock()
	failCode, failDetail, ok := ag.failCode, ag.failDetail, ag.drv != nil
	ag.mu.Unlock()
	if !ok {
		d.logger.Warn("Agent provision failed", "agent_id", spec.MemberID, "error_code", failCode)
		d.reportAgentState(spec.MemberID, domain.RuntimeError, "", failCode, failDetail)
		return
	}
	d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
	d.scheduleWake(ctx, spec.MemberID, "", reason)
}

// provisionAgent registers the agent or, when its spec changed, closes the old driver and
// rebuilds from the new spec. Workspace and MEMORY.md persist; the Runtime Session is reset.
func (d *Daemon) provisionAgent(spec protocol.AssignedAgentSpec) *hostedAgent {
	d.mu.Lock()
	ag := d.agents[spec.MemberID]
	if ag == nil {
		homeDir := filepath.Join(d.cfg.DataDir, "agents", spec.MemberID)
		tokBuf := make([]byte, 24)
		_, _ = rand.Read(tokBuf)
		ag = &hostedAgent{
			homeDir:     homeDir,
			workDir:     filepath.Join(homeDir, "workspace"),
			memoryDir:   filepath.Join(homeDir, "memory"),
			driverToken: hex.EncodeToString(tokBuf),
		}
		d.agents[spec.MemberID] = ag
		d.tokenToAgent[ag.driverToken] = spec.MemberID
	}
	d.mu.Unlock()

	ag.mu.Lock()
	defer ag.mu.Unlock()
	if ag.drv != nil && reflect.DeepEqual(ag.spec, spec) {
		return ag
	}
	reprovision := ag.drv != nil
	old := ag.drv
	drv, code, detail := d.buildAgentDriver(ag, spec)
	if drv != nil && reprovision {
		if err := drv.ResetSession(); err != nil {
			d.logger.Warn("Reset session on re-provision failed", "agent_id", spec.MemberID, "error", err.Error())
		}
	}
	ag.spec, ag.drv, ag.failCode, ag.failDetail = spec, drv, code, detail
	ag.resetBreakerLocked()
	if old != nil {
		// Closing may wait for an in-flight turn; never block provisioning on it.
		go func() { _ = old.Close() }()
	}
	return ag
}

func (d *Daemon) buildAgentDriver(ag *hostedAgent, spec protocol.AssignedAgentSpec) (driver.SessionDriver, string, string) {
	for _, dir := range []string{ag.workDir, ag.memoryDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, domain.AgentErrDriverStartFailed, domain.BoundErrorDetail(err.Error())
		}
	}
	if spec.DriverKind == domain.DriverACP && spec.DriverConfig.Provider != "" {
		if err := prepareACPRuntimeDir(ag.homeDir, spec.DriverConfig.Provider); err != nil {
			return nil, domain.AgentErrDriverStartFailed, domain.BoundErrorDetail(err.Error())
		}
	}
	memFile := filepath.Join(ag.memoryDir, "MEMORY.md")
	if _, err := os.Stat(memFile); os.IsNotExist(err) {
		_ = os.WriteFile(memFile, []byte(memoryTemplate), 0o600)
	}
	var drv driver.SessionDriver
	var err error
	if d.cfg.DriverFactory != nil {
		drv, err = d.cfg.DriverFactory(spec, ag.homeDir)
	} else {
		drv, err = buildDriver(spec, ag.homeDir)
	}
	if err != nil {
		code, detail := failureOf(err, spec)
		if code == domain.AgentErrTurnFailed {
			code = domain.AgentErrDriverStartFailed
		}
		return nil, code, detail
	}
	return drv, "", ""
}

func buildDriver(spec protocol.AssignedAgentSpec, homeDir string) (driver.SessionDriver, error) {
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
		acfg := acp.Config{Provider: cfg.Provider, Command: cfg.Command, SessionFile: filepath.Join(homeDir, "acp-session.json")}
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

// failureOf maps a driver error to an agent error code and a bounded, secret-free detail.
func failureOf(err error, spec protocol.AssignedAgentSpec) (string, string) {
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

// agentEnv builds the child environment: `sumi` first on PATH, capability socket credentials,
// HOME pointed at Agent Home for builtin, and per-agent isolated provider config dirs for ACP.
// Inherited SUMI_* variables and anything carrying the Computer token are dropped so an
// Agent can only reach the Server through its own driver token.
func (d *Daemon) agentEnv(ag *hostedAgent, kind domain.DriverKind, provider string) []string {
	d.mu.RLock()
	binDir := d.binDir
	d.mu.RUnlock()
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SUMI_") {
			continue
		}
		if tok := d.cfg.ComputerToken; tok != "" && strings.Contains(kv, tok) {
			continue
		}
		env = append(env, kv)
	}
	path := os.Getenv("PATH")
	if binDir != "" {
		path = binDir + string(os.PathListSeparator) + path
	}
	env = setEnv(env, "PATH", path)
	env = setEnv(env, "SUMI_SOCKET", d.SocketPath())
	env = setEnv(env, "SUMI_DRIVER_TOKEN", ag.driverToken)
	env = setEnv(env, "SUMI_ATTACHMENT_DIR", filepath.Join(ag.workDir, "attachments"))
	env = setEnv(env, "SUMI_DRAFT_DIR", filepath.Join(ag.homeDir, "drafts"))
	switch kind {
	case domain.DriverBuiltin:
		env = setEnv(env, "HOME", ag.homeDir)
	case domain.DriverACP:
		if runtimeDir := acpRuntimeDir(ag.homeDir, provider); runtimeDir != "" {
			switch provider {
			case domain.ACPProviderPi:
				env = setEnv(env, "PI_CODING_AGENT_DIR", runtimeDir)
			case domain.ACPProviderCodex:
				env = setEnv(env, "CODEX_HOME", runtimeDir)
			case domain.ACPProviderClaude:
				env = unsetEnv(env, "CLAUDECODE")
				env = setEnv(env, "CLAUDE_CONFIG_DIR", runtimeDir)
				env = setEnv(env, "CLAUDE_CODE_DISABLE_AUTO_MEMORY", "1")
			}
		}
	}
	return env
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+value)
}

func unsetEnv(env []string, key string) []string {
	prefix := key + "="
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

func (d *Daemon) handleControl(ctx context.Context, ctrl protocol.AgentControlFrame) {
	switch ctrl.Action {
	case "provision":
		if ctrl.Spec != nil {
			d.provisionAndWake(ctx, *ctrl.Spec, "provisioned")
		}
	case "reset_session":
		d.mu.RLock()
		ag := d.agents[ctrl.AgentMemberID]
		d.mu.RUnlock()
		if ag == nil {
			return
		}
		ag.mu.Lock()
		drv := ag.drv
		ag.resetBreakerLocked()
		ag.mu.Unlock()
		if drv != nil {
			_ = drv.ResetSession()
			d.reportAgentState(ctrl.AgentMemberID, domain.RuntimeIdle, "", "", "")
			d.scheduleWake(ctx, ctrl.AgentMemberID, "", "reset_session")
		}
	case "restart":
		d.mu.RLock()
		ag := d.agents[ctrl.AgentMemberID]
		d.mu.RUnlock()
		if ag != nil {
			ag.mu.Lock()
			ag.resetBreakerLocked()
			ag.mu.Unlock()
			d.reportAgentState(ctrl.AgentMemberID, domain.RuntimeIdle, "", "", "")
			d.scheduleWake(ctx, ctrl.AgentMemberID, "", "restarted")
		}
	}
}

// scheduleWake enforces at most one active turn per Agent; concurrent wakes coalesce into dirty=true.
func (d *Daemon) scheduleWake(ctx context.Context, agentID, convID, reason string) {
	d.mu.RLock()
	ag := d.agents[agentID]
	d.mu.RUnlock()
	if ag == nil {
		return
	}
	ag.mu.Lock()
	if ag.drv == nil {
		code, detail := ag.failCode, ag.failDetail
		ag.mu.Unlock()
		d.reportAgentState(agentID, domain.RuntimeError, "", code, detail)
		return
	}
	if ag.paused {
		ag.mu.Unlock()
		return
	}
	ag.lastReason = reason
	if convID != "" {
		ag.lastConvID = convID
	}
	if ag.busy || (ag.retryTimer != nil && reason != "retry") {
		ag.dirty = true
		ag.mu.Unlock()
		return
	}
	ag.busy = true
	ag.dirty = false
	ag.mu.Unlock()

	go d.runAgentLoop(ctx, ag)
}

func (d *Daemon) runAgentLoop(ctx context.Context, ag *hostedAgent) {
	defer func() {
		ag.mu.Lock()
		ag.busy = false
		ag.mu.Unlock()
	}()
	for {
		ag.mu.Lock()
		spec, drv := ag.spec, ag.drv
		failCode, failDetail := ag.failCode, ag.failDetail
		ag.mu.Unlock()
		if drv == nil {
			d.reportAgentState(spec.MemberID, domain.RuntimeError, "", failCode, failDetail)
			return
		}

		items, err := d.fetchAgentInbox(ctx, spec.MemberID)
		if err != nil {
			d.reportAgentState(spec.MemberID, domain.RuntimeError, "", domain.AgentErrInboxUnreachable, domain.BoundErrorDetail(err.Error()))
			return
		}
		if len(items) == 0 {
			d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
			return
		}

		focus := items[0]
		turnStart := time.Now()
		d.logger.Info("Agent turn started", "agent_id", spec.MemberID, "conversation_id", focus.ConversationID, "inbox_items", len(items), "reason", focus.Reason)
		d.reportAgentState(spec.MemberID, domain.RuntimeWorking, focus.ConversationID, "", "")

		turnCtx, cancel := context.WithCancelCause(ctx)
		tracker := newWatchedTurnActivity(d.activity, func(a *domain.AgentActivity) {
			d.reportAgentStateWith(spec.MemberID, domain.RuntimeWorking, focus.ConversationID, "", "", a)
		}, func() bool {
			ag.mu.Lock()
			defer ag.mu.Unlock()
			return ag.dirty
		}, func() { cancel(errTurnStalled) })
		if spec.DriverKind == domain.DriverACP && spec.DriverConfig.Provider != "" {
			if err := prepareACPRuntimeDir(ag.homeDir, spec.DriverConfig.Provider); err != nil {
				d.logger.Warn("Refresh ACP runtime dir failed", "agent_id", spec.MemberID, "error", err.Error())
			}
		}
		_, runErr := drv.RunTurn(turnCtx, driver.TurnInput{
			SystemPrompt:   buildSystemPrompt(spec, ag.workDir, ag.memoryDir),
			StartupContext: buildStartupContext(spec, ag.memoryDir),
			UserPrompt:     buildWakePrompt(items),
			WorkDir:        ag.workDir,
			MemoryDir:      ag.memoryDir,
			Env:            d.agentEnv(ag, spec.DriverKind, spec.DriverConfig.Provider),
			OnActivity:     tracker.Observe,
		})
		if errors.Is(context.Cause(turnCtx), errTurnStalled) {
			runErr = driver.Fail(domain.AgentErrTurnStalled, errTurnStalled)
		}
		cancel(nil)
		tracker.Close()

		ag.mu.Lock()
		replaced := ag.drv != drv
		ag.mu.Unlock()
		if replaced {
			// Re-provisioned mid-turn: the old driver's outcome is moot; continue with the new one.
			continue
		}
		if runErr != nil {
			code, detail := failureOf(runErr, spec)
			d.logger.Warn("Agent turn failed", "agent_id", spec.MemberID, "error_code", code, "error", detail)
			if delay, paused := d.recordFailure(ctx, ag); paused {
				d.logger.Warn("Agent paused after consecutive failures", "agent_id", spec.MemberID, "error_code", code, "probe_in", delay.String())
				detail = domain.BoundErrorDetail(fmt.Sprintf("paused after %d failed turns, retry in %s: %s", d.breaker.threshold, delay.Round(time.Second), detail))
			} else {
				delay := d.armRetry(ctx, ag)
				detail = domain.BoundErrorDetail(fmt.Sprintf("retrying in %s: %s", delay.Round(time.Second), detail))
			}
			d.reportAgentState(spec.MemberID, domain.RuntimeError, focus.ConversationID, code, detail)
			return
		}
		ag.mu.Lock()
		ag.resetBreakerLocked()
		ag.mu.Unlock()
		d.logger.Info("Agent turn completed", "agent_id", spec.MemberID, "conversation_id", focus.ConversationID, "duration_ms", time.Since(turnStart).Milliseconds())
		d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")

		// Check if new wakes arrived while busy.
		ag.mu.Lock()
		if !ag.dirty {
			ag.mu.Unlock()
			return
		}
		ag.dirty = false
		ag.mu.Unlock()
	}
}

// recordFailure counts a failed turn and, at the threshold, pauses the Agent and arms one probe
// wake. Each failure while at the threshold doubles the probe delay up to maxProbe.
func (d *Daemon) recordFailure(ctx context.Context, ag *hostedAgent) (time.Duration, bool) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	ag.failStreak++
	if ag.failStreak < d.breaker.threshold {
		return 0, false
	}
	switch {
	case ag.probeDelay == 0:
		ag.probeDelay = d.breaker.firstProbe
	case ag.probeDelay < d.breaker.maxProbe:
		ag.probeDelay = min(ag.probeDelay*2, d.breaker.maxProbe)
	}
	ag.paused = true
	if ag.probeTimer != nil {
		ag.probeTimer.Stop()
	}
	agentID, delay := ag.spec.MemberID, ag.probeDelay
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		ag.mu.Lock()
		if ag.probeTimer != timer {
			ag.mu.Unlock()
			return
		}
		ag.paused, ag.probeTimer = false, nil
		ag.mu.Unlock()
		d.scheduleWake(ctx, agentID, "", "probe")
	})
	ag.probeTimer = timer
	return delay, true
}

// armRetry schedules one re-run of the loop after a failed turn below the breaker threshold, so
// triggers queued during the failed turn are redelivered without waiting for another wake.
func (d *Daemon) armRetry(ctx context.Context, ag *hostedAgent) time.Duration {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	delay := d.breaker.firstRetry
	for i := 1; i < ag.failStreak && delay < d.breaker.maxRetry; i++ {
		delay *= 2
	}
	delay = min(delay, d.breaker.maxRetry)
	// ±20% jitter keeps many Agents failing on the same provider from retrying in lockstep.
	delay += time.Duration((mrand.Float64()*0.4 - 0.2) * float64(delay))
	if ag.retryTimer != nil {
		ag.retryTimer.Stop()
	}
	agentID := ag.spec.MemberID
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		ag.mu.Lock()
		if ag.retryTimer != timer {
			ag.mu.Unlock()
			return
		}
		ag.retryTimer = nil
		ag.mu.Unlock()
		d.scheduleWake(ctx, agentID, "", "retry")
	})
	ag.retryTimer = timer
	d.logger.Info("Agent retry scheduled", "agent_id", agentID, "fail_streak", ag.failStreak, "delay_ms", delay.Milliseconds())
	return delay
}

func (d *Daemon) fetchAgentInbox(ctx context.Context, agentID string) ([]domain.InboxItem, error) {
	url := strings.TrimRight(d.cfg.ServerURL, "/") + "/api/v1/computer/agents/" + agentID + "/inbox?filter=needs_me"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.ComputerToken)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("inbox status %d", resp.StatusCode)
	}
	var out struct {
		Items []domain.InboxItem `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Items, nil
}
