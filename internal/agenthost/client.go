package agenthost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

// Client talks to one per-agent `sumi agent-host` sidecar over Unix socket and implements
// driver.SessionDriver + driver.GracefulCloser.
type Client struct {
	socketPath string
	pid        int
	httpClient *http.Client
	transport  *http.Transport
}

// PID returns the sidecar process ID observed when attaching or spawning, or 0 if unknown.
func (c *Client) PID() int { return c.pid }

// NewClient creates a client bound to an existing sidecar Unix socket.
func NewClient(socketPath string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{
		socketPath: socketPath,
		transport:  tr,
		httpClient: &http.Client{Transport: tr},
	}
}

// AttachOptions configures AttachOrSpawn.
type AttachOptions struct {
	BinaryPath     string
	AgentHome      string
	Spec           protocol.AssignedAgentSpec
	ScrubEnvToken  string
	ExtraArgs      []string
	StartupTimeout time.Duration
}

// AttachOrSpawn re-attaches to an already-running compatible sidecar when possible, otherwise
// spawns a new detached `sumi agent-host` process and configures it with Spec.
func AttachOrSpawn(ctx context.Context, opts AttachOptions) (*Client, StatusResponse, error) {
	sockPath := SocketPath(opts.AgentHome)
	metaPath := MetaPath(opts.AgentHome)
	wantHash := SpecHash(opts.Spec)
	client := NewClient(sockPath)

	if st, err := client.Status(ctx); err == nil {
		if st.ProtocolVersion == ProtocolVersion && st.Configured && st.SpecHash == wantHash {
			client.pid = st.PID
			return client, st, nil
		}
		// Spec or protocol changed: replace the existing sidecar cleanly and wait for it to
		// exit before binding the same socket path with a replacement process.
		client.pid = st.PID
		_ = client.GracefulClose(2)
		if st.PID > 0 && st.PID != os.Getpid() {
			if !waitProcessGone(st.PID, 3*time.Second) {
				terminateProcess(st.PID)
			}
		}
		waitSocketGone(sockPath, 2*time.Second)
		client.pid = 0
	}

	bin := opts.BinaryPath
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, StatusResponse{}, driver.Fail(domain.AgentErrDriverStartFailed, fmt.Errorf("resolve executable for agent-host: %w", err))
		}
		bin = exe
	}
	if err := os.MkdirAll(opts.AgentHome, 0o700); err != nil {
		return nil, StatusResponse{}, driver.Fail(domain.AgentErrDriverStartFailed, fmt.Errorf("create agent home: %w", err))
	}

	args := append([]string{}, opts.ExtraArgs...)
	args = append(args, "agent-host", "--socket", sockPath, "--meta", metaPath)
	cmd := exec.Command(bin, args...)
	cmd.Dir = opts.AgentHome
	cmd.Env = cleanHostEnv(opts.ScrubEnvToken)
	// Setsid detaches the sidecar into its own session so restarting `sumi computer` never signals it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	logPath := filepath.Join(opts.AgentHome, "agent-host.log")
	if logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	}
	if err := cmd.Start(); err != nil {
		return nil, StatusResponse{}, driver.Fail(domain.AgentErrDriverStartFailed, fmt.Errorf("spawn agent-host: %w", err))
	}
	pid := cmd.Process.Pid
	// Reap in the background so a retired sidecar never lingers as a zombie.
	go func() { _ = cmd.Wait() }()
	// Failures below must never leave an orphaned sidecar behind.
	killSidecar := func() { terminateProcess(pid) }

	timeout := opts.StartupTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var st StatusResponse
	var lastErr error
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		st, lastErr = client.Status(probeCtx)
		cancel()
		if lastErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			killSidecar()
			return nil, StatusResponse{}, driver.Fail(domain.AgentErrDriverStartFailed, ctx.Err())
		case <-time.After(40 * time.Millisecond):
		}
	}
	if lastErr != nil {
		killSidecar()
		return nil, StatusResponse{}, driver.Fail(domain.AgentErrDriverStartFailed, fmt.Errorf("agent-host did not become ready: %w", lastErr))
	}

	if err := client.Configure(ctx, opts.Spec, opts.AgentHome); err != nil {
		_ = client.Close()
		killSidecar()
		return nil, StatusResponse{}, err
	}
	st, _ = client.Status(ctx)
	if st.PID > 0 {
		client.pid = st.PID
	} else {
		client.pid = pid
	}
	return client, st, nil
}

func cleanHostEnv(computerToken string) []string {
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SUMI_") {
			continue
		}
		if computerToken != "" && strings.Contains(kv, computerToken) {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func waitSocketGone(sockPath string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); os.IsNotExist(err) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func waitProcessGone(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		time.Sleep(30 * time.Millisecond)
	}
	return syscall.Kill(pid, 0) != nil
}

// terminateProcess reaps a detached sidecar that never reached a usable state.
func terminateProcess(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/status", nil)
	if err != nil {
		return StatusResponse{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return StatusResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StatusResponse{}, fmt.Errorf("agenthost status %d", resp.StatusCode)
	}
	var st StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return StatusResponse{}, err
	}
	return st, nil
}

func (c *Client) Configure(ctx context.Context, spec protocol.AssignedAgentSpec, agentHome string) error {
	raw, err := json.Marshal(ConfigureRequest{Spec: spec, AgentHome: agentHome})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/configure", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return driver.Fail(domain.AgentErrDriverStartFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeSidecarError(resp.Body, domain.AgentErrDriverStartFailed)
	}
	return nil
}

// RunTurn starts a turn in the sidecar and streams activity events until completion.
func (c *Client) RunTurn(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error) {
	return c.RunTurnWithID(ctx, uuid.NewString(), "", in)
}

// RunTurnWithID starts a turn with explicit turnID and focus conversation ID.
func (c *Client) RunTurnWithID(ctx context.Context, turnID, focusConvID string, in driver.TurnInput) (driver.TurnResult, error) {
	raw, err := json.Marshal(StartTurnRequest{
		TurnID:         turnID,
		FocusConvID:    focusConvID,
		SystemPrompt:   in.SystemPrompt,
		StartupContext: in.StartupContext,
		UserPrompt:     in.UserPrompt,
		WorkDir:        in.WorkDir,
		MemoryDir:      in.MemoryDir,
		Env:            in.Env,
	})
	if err != nil {
		return driver.TurnResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/turns", bytes.NewReader(raw))
	if err != nil {
		return driver.TurnResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.consumeTurnStream(ctx, turnID, req, in.OnActivity)
}

// WaitTurn re-attaches to an already running (or recently completed) turn inside the sidecar.
func (c *Client) WaitTurn(ctx context.Context, turnID string, onActivity func(driver.Activity)) (driver.TurnResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/turns/"+turnID+"/wait", nil)
	if err != nil {
		return driver.TurnResult{}, err
	}
	return c.consumeTurnStream(ctx, turnID, req, onActivity)
}

func (c *Client) consumeTurnStream(ctx context.Context, turnID string, req *http.Request, onActivity func(driver.Activity)) (driver.TurnResult, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return c.handleContextEnd(ctx, turnID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return driver.TurnResult{}, decodeSidecarError(resp.Body, domain.AgentErrTurnFailed)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 16*1024), 4*1024*1024)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "activity":
			if onActivity != nil && ev.Activity != nil {
				onActivity(*ev.Activity)
			}
		case "done":
			var res driver.TurnResult
			if ev.Result != nil {
				res = *ev.Result
			}
			if ev.ErrorCode != "" || ev.ErrorDetail != "" {
				code := ev.ErrorCode
				if code == "" {
					code = domain.AgentErrTurnFailed
				}
				return res, driver.Fail(code, errors.New(ev.ErrorDetail))
			}
			return res, nil
		}
	}
	if ctx.Err() != nil {
		return c.handleContextEnd(ctx, turnID, ctx.Err())
	}
	if err := scanner.Err(); err != nil {
		return driver.TurnResult{}, driver.Fail(domain.AgentErrTurnFailed, fmt.Errorf("agenthost stream: %w", err))
	}
	return driver.TurnResult{}, driver.Fail(domain.AgentErrTurnFailed, errors.New("agenthost stream closed before turn completed"))
}

// handleContextEnd distinguishes daemon detach (leave turn running!) from real cancellation.
func (c *Client) handleContextEnd(ctx context.Context, turnID string, err error) (driver.TurnResult, error) {
	if errors.Is(context.Cause(ctx), ErrDetaching) {
		return driver.TurnResult{}, ErrDetaching
	}
	if ctx.Err() != nil {
		c.CancelTurn(turnID)
		return driver.TurnResult{}, driver.Fail(domain.AgentErrTurnFailed, ctx.Err())
	}
	return driver.TurnResult{}, driver.Fail(domain.AgentErrTurnFailed, err)
}

// CancelTurn explicitly cancels an active turn in the sidecar.
func (c *Client) CancelTurn(turnID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/turns/"+turnID+"/cancel", nil)
	if err != nil {
		return
	}
	resp, err := c.httpClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

func (c *Client) ResetSession() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/reset-session", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeSidecarError(resp.Body, domain.AgentErrTurnFailed)
	}
	return nil
}

// Detach closes idle local connections while leaving the sidecar and any in-flight turn alive.
func (c *Client) Detach() {
	c.transport.CloseIdleConnections()
}

// Close shuts down the sidecar immediately (used on reprovision or explicit agent stop).
func (c *Client) Close() error {
	return c.shutdown(false)
}

// GracefulClose asks the sidecar to flush ACP session state before exiting.
func (c *Client) GracefulClose(_ int) error {
	return c.shutdown(true)
}

func (c *Client) shutdown(graceful bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := "http://unix/shutdown"
	var params []string
	if graceful {
		params = append(params, "graceful=true")
	}
	if c.pid > 0 {
		params = append(params, fmt.Sprintf("pid=%d", c.pid))
	}
	if len(params) > 0 {
		u += "?" + strings.Join(params, "&")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	c.transport.CloseIdleConnections()
	return nil
}

func decodeSidecarError(r io.Reader, fallbackCode string) error {
	var body struct {
		ErrorCode   string `json:"error_code"`
		ErrorDetail string `json:"error_detail"`
	}
	_ = json.NewDecoder(io.LimitReader(r, 8192)).Decode(&body)
	code := body.ErrorCode
	if code == "" {
		code = fallbackCode
	}
	detail := body.ErrorDetail
	if detail == "" {
		detail = "agenthost request failed"
	}
	return driver.Fail(code, errors.New(detail))
}
