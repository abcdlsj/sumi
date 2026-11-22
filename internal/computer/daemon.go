package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/agenthost"
	"github.com/abcdlsj/sumi/internal/datadir"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/driver/acp"
	"github.com/abcdlsj/sumi/internal/protocol"
	"github.com/abcdlsj/sumi/internal/version"
)

type Config struct {
	ServerURL     string
	ComputerToken string
	DataDir       string
	DaemonVersion string
	// SumiBinary is the executable exposed to agents as `sumi` on PATH; defaults to os.Executable().
	SumiBinary string
	// UseSidecar hosts each agent's SessionDriver in a detached `sumi agent-host` process so
	// restarting `sumi computer` does not interrupt in-flight turns.
	UseSidecar bool
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
	spec           protocol.AssignedAgentSpec
	drv            driver.SessionDriver
	failCode       string
	failDetail     string
	busy           bool
	dirty          bool
	focusConvID    string
	attachedTurnID string
	lastReason     string
	lastConvID     string

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
	cfg              Config
	breaker          breakerPolicy
	activity         activityPolicy
	proxyRetryDelays []time.Duration
	logger           *slog.Logger
	daemonSessionID  string
	httpClient       *http.Client

	mu              sync.RWMutex
	agents          map[string]*hostedAgent
	tokenToAgent    map[string]string
	wsSendCh        chan protocol.Envelope
	connected       bool
	connectedAt     time.Time
	serverCaps      []string
	socketPath      string
	socketServer    *http.Server
	socketLn        net.Listener
	binDir          string
	draining        bool
	drainRequestID  string
	drainDeadlineAt time.Time
	drainHoldTimer  *time.Timer
	drainCheckCh    chan struct{}
	drainHoldGrace  time.Duration
	detachCh        chan struct{}
	detachOnce      sync.Once
	stopCh          chan struct{}
	stopOnce        sync.Once
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

// errServerRestarting indicates the Server announced an intentional restart and expects a fast reconnect.
var errServerRestarting = errors.New("server restarting")

func NewDaemon(cfg Config, logger *slog.Logger) *Daemon {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.DaemonVersion == "" {
		cfg.DaemonVersion = version.Value()
	}
	if cfg.DataDir == "" {
		cfg.DataDir = datadir.ComputerDir(datadir.RootOrHome())
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
		drainCheckCh:    make(chan struct{}, 1),
		drainHoldGrace:  defaultDrainHoldGrace,
		detachCh:        make(chan struct{}),
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
	d.stopWith(false, 0)
}

// StopGraceful gives each agent driver a bounded window to flush persisted session state before stopping.
func (d *Daemon) StopGraceful(timeoutSeconds int) {
	d.stopWith(true, timeoutSeconds)
}

func (d *Daemon) stopWith(graceful bool, timeoutSeconds int) {
	d.stopOnce.Do(func() {
		d.clearDrainTimerLocked()
		close(d.stopCh)
		d.closeSocketLocked()
		d.mu.Lock()
		agents := make([]*hostedAgent, 0, len(d.agents))
		for _, ag := range d.agents {
			agents = append(agents, ag)
		}
		d.mu.Unlock()
		for _, ag := range agents {
			ag.mu.Lock()
			ag.resetBreakerLocked()
			drv := ag.drv
			ag.mu.Unlock()
			if drv == nil {
				continue
			}
			if graceful {
				if gc, ok := drv.(driver.GracefulCloser); ok {
					_ = gc.GracefulClose(timeoutSeconds)
					continue
				}
			}
			_ = drv.Close()
		}
	})
}

// Detach stops this Daemon process for a hot upgrade while keeping every `sumi agent-host`
// sidecar and any in-flight turns running undisturbed.
func (d *Daemon) Detach() {
	d.detachOnce.Do(func() { close(d.detachCh) })
	d.stopOnce.Do(func() {
		d.clearDrainTimerLocked()
		close(d.stopCh)
		d.closeSocketLocked()
		d.mu.Lock()
		for _, ag := range d.agents {
			ag.mu.Lock()
			ag.resetBreakerLocked()
			if hc, ok := ag.drv.(*agenthost.Client); ok {
				hc.Detach()
			}
			ag.mu.Unlock()
		}
		d.mu.Unlock()
	})
}

func (d *Daemon) closeSocketLocked() {
	d.mu.Lock()
	srv, ln, sock := d.socketServer, d.socketLn, d.socketPath
	d.socketServer, d.socketLn = nil, nil
	d.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	} else if ln != nil {
		_ = ln.Close()
	}
	// Only unlink capability.sock while this daemon still owns the listener;
	// during hot upgrade ReleaseCapabilitySocket already released it before the
	// candidate bound the same path, so Detach/Stop must not delete the successor's socket.
	if (srv != nil || ln != nil) && sock != "" {
		_ = os.Remove(sock)
	}
}

// RebindCapabilitySocket re-opens capability.sock after a failed hot-upgrade rollback.
func (d *Daemon) RebindCapabilitySocket() error {
	return d.startCapabilitySocket()
}

// ReleaseCapabilitySocket closes capability.sock without stopping the daemon, used during
// verified replacement handoff so the candidate daemon can bind the deterministic socket path.
func (d *Daemon) ReleaseCapabilitySocket() {
	d.closeSocketLocked()
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
		fastReconnect := errors.Is(err, errServerRestarting)
		if err != nil && !fastReconnect {
			d.logger.Warn("Computer connection ended", "error", err.Error())
		}
		if fastReconnect {
			backoff = 100*time.Millisecond + time.Duration(mrand.IntN(250))*time.Millisecond
		} else if time.Since(connectedAt) > 60*time.Second {
			backoff = 500 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case <-time.After(backoff):
			if fastReconnect {
				backoff = 500 * time.Millisecond
			} else if backoff < 15*time.Second {
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
	conn.SetReadLimit(10 << 20)

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
	d.connected = true
	d.connectedAt = time.Now().UTC()
	d.serverCaps = ack.HelloAck.Capabilities
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		if d.wsSendCh == sendCh {
			d.wsSendCh = nil
			d.connected = false
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
		case protocol.TypeWorkspaceList:
			if env.WorkspaceList != nil {
				req := *env.WorkspaceList
				go d.handleWorkspaceList(req)
			}
		case protocol.TypeWorkspaceRead:
			if env.WorkspaceRead != nil {
				req := *env.WorkspaceRead
				go d.handleWorkspaceRead(req)
			}
		case protocol.TypeShutdown:
			if env.Shutdown != nil && env.Shutdown.Reason == "server_restarting" {
				return errServerRestarting
			}
			return nil
		}
	}
}

func (d *Daemon) agentWorkDir(agentID string) (string, bool) {
	d.mu.RLock()
	ag := d.agents[agentID]
	d.mu.RUnlock()
	if ag == nil {
		return "", false
	}
	return ag.workDir, true
}

func workspaceErrFrame(err error) (string, string) {
	var de *domain.Error
	if errors.As(err, &de) {
		return string(de.Code), de.Message
	}
	return string(domain.CodeInternal), "workspace request failed"
}

func (d *Daemon) handleWorkspaceList(req protocol.WorkspaceListFrame) {
	res := &protocol.WorkspaceListResultFrame{RequestID: req.RequestID}
	workDir, ok := d.agentWorkDir(req.AgentMemberID)
	if !ok {
		res.ErrorCode = string(domain.CodeNotFound)
		res.Error = "agent workspace is not hosted on this computer"
	} else {
		listing, err := listWorkspaceDir(workDir, req.Path, req.IncludeHidden)
		if err != nil {
			res.ErrorCode, res.Error = workspaceErrFrame(err)
		} else {
			res.Listing = listing
		}
	}
	d.sendWS(protocol.Envelope{
		Type:                protocol.TypeWorkspaceListResult,
		WorkspaceListResult: res,
	})
}

func (d *Daemon) handleWorkspaceRead(req protocol.WorkspaceReadFrame) {
	res := &protocol.WorkspaceReadResultFrame{RequestID: req.RequestID}
	workDir, ok := d.agentWorkDir(req.AgentMemberID)
	if !ok {
		res.ErrorCode = string(domain.CodeNotFound)
		res.Error = "agent workspace is not hosted on this computer"
	} else {
		file, err := readWorkspaceFile(workDir, req.Path)
		if err != nil {
			res.ErrorCode, res.Error = workspaceErrFrame(err)
		} else {
			res.File = file
		}
	}
	d.sendWS(protocol.Envelope{
		Type:                protocol.TypeWorkspaceReadResult,
		WorkspaceReadResult: res,
	})
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
