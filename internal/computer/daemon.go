package computer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	// Optional factory override for deterministic scenario tests.
	DriverFactory func(spec protocol.AssignedAgentSpec, agentHome string) (driver.SessionDriver, error)
}

type hostedAgent struct {
	spec        protocol.AssignedAgentSpec
	homeDir     string
	workDir     string
	memoryDir   string
	driverToken string
	drv         driver.SessionDriver

	mu         sync.Mutex
	busy       bool
	dirty      bool
	lastReason string
	lastConvID string
}

type Daemon struct {
	cfg             Config
	logger          *slog.Logger
	daemonSessionID string
	httpClient      *http.Client

	mu           sync.RWMutex
	agents       map[string]*hostedAgent
	tokenToAgent map[string]string
	wsSendCh     chan protocol.Envelope
	socketPath   string
	socketServer *http.Server
	socketLn     net.Listener
	stopCh       chan struct{}
	stopOnce     sync.Once
}

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
	if err := d.startCapabilitySocket(); err != nil {
		return err
	}
	go d.connectLoop(ctx)
	return nil
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
			if ag.drv != nil {
				_ = ag.drv.Close()
			}
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
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
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
			OS:              "macos",
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

	sendCh := make(chan protocol.Envelope, 64)
	d.mu.Lock()
	d.wsSendCh = sendCh
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
		if err := d.provisionAgent(sp); err != nil {
			d.logger.Error("Failed to provision agent", "agent_id", sp.MemberID, "error", err.Error())
			continue
		}
		d.reportAgentState(sp.MemberID, domain.RuntimeIdle, "", "")
		d.scheduleWake(ctx, sp.MemberID, "", "reconnect")
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

func (d *Daemon) reportAgentState(agentID string, state domain.RuntimeState, focusConvID, errCode string) {
	d.sendWS(protocol.Envelope{
		Type: protocol.TypeAgentState,
		AgentState: &protocol.AgentStateFrame{
			DaemonSessionID:     d.daemonSessionID,
			AgentMemberID:       agentID,
			RuntimeState:        state,
			FocusConversationID: focusConvID,
			ErrorCode:           errCode,
		},
	})
}

func (d *Daemon) provisionAgent(spec protocol.AssignedAgentSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.agents[spec.MemberID]; exists {
		return nil
	}
	homeDir := filepath.Join(d.cfg.DataDir, "agents", spec.MemberID)
	workDir := filepath.Join(homeDir, "workspace")
	memDir := filepath.Join(homeDir, "memory")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		return err
	}
	memFile := filepath.Join(memDir, "MEMORY.md")
	if _, err := os.Stat(memFile); os.IsNotExist(err) {
		_ = os.WriteFile(memFile, []byte("# Memory\n"), 0o600)
	}

	var drv driver.SessionDriver
	var err error
	if d.cfg.DriverFactory != nil {
		drv, err = d.cfg.DriverFactory(spec, homeDir)
	} else {
		drv, err = buildDriver(spec, homeDir)
	}
	if err != nil {
		return err
	}

	tokBuf := make([]byte, 24)
	_, _ = rand.Read(tokBuf)
	tok := hex.EncodeToString(tokBuf)

	d.agents[spec.MemberID] = &hostedAgent{
		spec:        spec,
		homeDir:     homeDir,
		workDir:     workDir,
		memoryDir:   memDir,
		driverToken: tok,
		drv:         drv,
	}
	d.tokenToAgent[tok] = spec.MemberID
	return nil
}

func buildDriver(spec protocol.AssignedAgentSpec, homeDir string) (driver.SessionDriver, error) {
	switch spec.DriverKind {
	case domain.DriverBuiltin:
		var cfg builtin.Config
		_ = json.Unmarshal([]byte(spec.DriverConfigJSON), &cfg)
		cfg.SessionFile = filepath.Join(homeDir, "session.jsonl")
		return builtin.New(cfg, nil)
	case domain.DriverACP:
		var cfg acp.Config
		_ = json.Unmarshal([]byte(spec.DriverConfigJSON), &cfg)
		return acp.New(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported driver kind %q", spec.DriverKind)
	}
}

func (d *Daemon) handleControl(ctx context.Context, ctrl protocol.AgentControlFrame) {
	switch ctrl.Action {
	case "provision":
		if ctrl.Spec != nil {
			if err := d.provisionAgent(*ctrl.Spec); err != nil {
				d.reportAgentState(ctrl.AgentMemberID, domain.RuntimeError, "", "provision_failed")
				return
			}
			d.reportAgentState(ctrl.AgentMemberID, domain.RuntimeIdle, "", "")
			d.scheduleWake(ctx, ctrl.AgentMemberID, "", "provisioned")
		}
	case "reset_session":
		d.mu.RLock()
		ag := d.agents[ctrl.AgentMemberID]
		d.mu.RUnlock()
		if ag != nil && ag.drv != nil {
			_ = ag.drv.ResetSession()
			d.reportAgentState(ctrl.AgentMemberID, domain.RuntimeIdle, "", "")
		}
	case "restart":
		d.mu.RLock()
		ag := d.agents[ctrl.AgentMemberID]
		d.mu.RUnlock()
		if ag != nil {
			d.reportAgentState(ctrl.AgentMemberID, domain.RuntimeIdle, "", "")
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
	ag.lastReason = reason
	if convID != "" {
		ag.lastConvID = convID
	}
	if ag.busy {
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
	for {
		items, err := d.fetchAgentInbox(ctx, ag.spec.MemberID)
		if err != nil {
			d.reportAgentState(ag.spec.MemberID, domain.RuntimeError, "", "inbox_unreachable")
			break
		}
		if len(items) == 0 {
			d.reportAgentState(ag.spec.MemberID, domain.RuntimeIdle, "", "")
			break
		}

		focus := items[0]
		d.reportAgentState(ag.spec.MemberID, domain.RuntimeWorking, focus.ConversationID, "")

		sysPrompt := fmt.Sprintf(
			"You are %s, a persistent AI collaborator in Sumi.\nRole: %s\nUse the `sumi` CLI via bash for all Sumi actions (`sumi inbox`, `sumi read <conv_id> --unread`, `sumi send <conv_id> --body ...`, `sumi task ...`).\nBe concise and direct. Never send filler confirmations.",
			ag.spec.DisplayName, ag.spec.RoleText,
		)
		var b strings.Builder
		b.WriteString("You have unread attention items:\n")
		for _, it := range items {
			fmt.Fprintf(&b, "- %s (conversation_id=%s, reason=%s, unread=%d, latest_by=%s)\n",
				it.Title, it.ConversationID, it.Reason, it.UnreadCount, it.LatestAuthorName)
		}
		b.WriteString("Read the relevant conversation(s) with `sumi read <conversation_id> --unread` before acting.")

		env := append(os.Environ(),
			"SUMI_SOCKET="+d.SocketPath(),
			"SUMI_DRIVER_TOKEN="+ag.driverToken,
			"HOME="+ag.homeDir,
		)
		turnCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		_, runErr := ag.drv.RunTurn(turnCtx, driver.TurnInput{
			SystemPrompt: sysPrompt,
			UserPrompt:   b.String(),
			WorkDir:      ag.workDir,
			MemoryDir:    ag.memoryDir,
			Env:          env,
		})
		cancel()

		if runErr != nil {
			d.logger.Warn("Agent turn failed", "agent_id", ag.spec.MemberID, "error", runErr.Error())
			d.reportAgentState(ag.spec.MemberID, domain.RuntimeError, focus.ConversationID, "driver_error")
			break
		}
		d.reportAgentState(ag.spec.MemberID, domain.RuntimeIdle, "", "")

		// Check if new wakes arrived while busy.
		ag.mu.Lock()
		if !ag.dirty {
			ag.busy = false
			ag.mu.Unlock()
			return
		}
		ag.dirty = false
		ag.mu.Unlock()
	}

	ag.mu.Lock()
	ag.busy = false
	ag.mu.Unlock()
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
