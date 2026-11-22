package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/version"
)

const (
	// initializeTimeout covers a first `npx -y` package download.
	initializeTimeout = 120 * time.Second
	sessionTimeout    = 120 * time.Second
	stderrTailBytes   = 2048
)

type Config struct {
	Provider string   `json:"provider"` // "codex" | "claude" | "pi"
	Command  []string `json:"command,omitempty"`
	// SessionFile persists the active sessionId so a restarted daemon resumes it via session/load.
	SessionFile string `json:"-"`
	// Fingerprint names the credentials and model behind the command, so a session is never
	// resumed across a provider or model change that the argv alone cannot show.
	Fingerprint string `json:"-"`
}

// ResolvedCommand returns the argv to launch; Command overrides the provider default.
func (c Config) ResolvedCommand() ([]string, error) {
	if len(c.Command) > 0 {
		return c.Command, nil
	}
	switch c.Provider {
	case domain.ACPProviderCodex:
		return []string{"npx", "-y", "@zed-industries/codex-acp@0.16.0"}, nil
	case domain.ACPProviderClaude:
		return []string{"npx", "-y", "@zed-industries/claude-code-acp@0.16.2"}, nil
	case domain.ACPProviderPi:
		return []string{"npx", "-y", "pi-acp@0.0.34"}, nil
	default:
		return nil, fmt.Errorf("unsupported acp provider %q", c.Provider)
	}
}

// ProviderBinaries lists the executables a provider's default command needs on PATH.
func ProviderBinaries(provider string) []string {
	switch provider {
	case domain.ACPProviderPi, domain.ACPProviderCodex, domain.ACPProviderClaude:
		return []string{provider, "npx"}
	default:
		return nil
	}
}

// Driver manages a single persistent ACP stdio JSON-RPC child process and one active sessionId.
type Driver struct {
	mu        sync.Mutex
	cfg       Config
	proc      *process
	sessionID string
	// canLoad reports the agent's initialize-time loadSession capability for the current process.
	canLoad bool
	// resumed marks a sessionId restored via session/load in the current process.
	resumed bool

	closeOnce sync.Once
	closed    chan struct{}
}

func New(cfg Config) *Driver {
	return &Driver{cfg: cfg, closed: make(chan struct{})}
}

func (d *Driver) ResetSession() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sessionID = ""
	d.resumed = false
	return d.forgetSession()
}

// savedSession is the on-disk record. Command and Fingerprint pin the credentials and model so
// a session is only resumed by the same provider that created it.
type savedSession struct {
	SessionID   string   `json:"session_id"`
	Command     []string `json:"command"`
	Fingerprint string   `json:"fingerprint,omitempty"`
}

func (d *Driver) loadSavedSession(argv []string) string {
	if d.cfg.SessionFile == "" {
		return ""
	}
	raw, err := os.ReadFile(d.cfg.SessionFile)
	if err != nil {
		return ""
	}
	var s savedSession
	if json.Unmarshal(raw, &s) != nil || !slices.Equal(s.Command, argv) || s.Fingerprint != d.cfg.Fingerprint {
		return ""
	}
	return s.SessionID
}

func (d *Driver) saveSession(argv []string) error {
	if d.cfg.SessionFile == "" {
		return nil
	}
	raw, _ := json.Marshal(savedSession{SessionID: d.sessionID, Command: argv, Fingerprint: d.cfg.Fingerprint})
	tmp := d.cfg.SessionFile + ".tmp"
	if err := os.MkdirAll(filepath.Dir(d.cfg.SessionFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.cfg.SessionFile)
}

func (d *Driver) forgetSession() error {
	if d.cfg.SessionFile == "" {
		return nil
	}
	if err := os.Remove(d.cfg.SessionFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Close kills the child without waiting for an in-flight turn, so a replaced driver releases promptly.
func (d *Driver) Close() error {
	d.closeOnce.Do(func() { close(d.closed) })
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopLocked()
	return nil
}

// GracefulClose closes stdin and sends SIGTERM to the process group so the provider can flush
// its persisted session before falling back to SIGKILL after timeoutSeconds.
func (d *Driver) GracefulClose(timeoutSeconds int) error {
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	d.mu.Lock()
	p := d.proc
	d.sessionID = ""
	d.canLoad = false
	d.resumed = false
	d.proc = nil
	d.mu.Unlock()
	if p != nil {
		p.gracefulStop(timeout)
	}
	d.closeOnce.Do(func() { close(d.closed) })
	return nil
}

func (d *Driver) stopLocked() {
	d.sessionID = ""
	d.canLoad = false
	d.resumed = false
	if d.proc != nil {
		d.proc.kill()
		d.proc = nil
	}
}

func (d *Driver) isClosed() bool {
	select {
	case <-d.closed:
		return true
	default:
		return false
	}
}

func startFailure(format string, err error, p *process) error {
	msg := fmt.Sprintf(format, err)
	if p != nil {
		if tail := p.stderr.lastLines(2); tail != "" {
			msg += "; stderr: " + tail
		}
	}
	return driver.Fail(domain.AgentErrDriverStartFailed, errors.New(msg))
}

func (d *Driver) ensureStartedLocked(ctx context.Context, workDir string, env []string) error {
	if d.proc != nil && !d.proc.exited() {
		return nil
	}
	d.stopLocked()
	if d.isClosed() {
		return driver.Fail(domain.AgentErrDriverStartFailed, errors.New("acp driver closed"))
	}
	argv, err := d.cfg.ResolvedCommand()
	if err != nil {
		return driver.Fail(domain.AgentErrDriverConfigInvalid, err)
	}
	p, err := startProcess(argv, workDir, env)
	if err != nil {
		return startFailure("start acp process: %v", err, nil)
	}
	d.proc = p
	// Close must be able to kill the child while a turn holds d.mu.
	go func() {
		select {
		case <-d.closed:
			p.kill()
		case <-p.done:
		}
	}()

	initCtx, cancel := context.WithTimeout(ctx, initializeTimeout)
	defer cancel()
	res, err := p.call(initCtx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "sumi", "version": version.Value()},
	})
	if err != nil {
		d.stopLocked()
		return startFailure("acp initialize: %v", err, p)
	}
	var init struct {
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	_ = json.Unmarshal(res, &init)
	d.canLoad = init.AgentCapabilities.LoadSession
	return nil
}

func (d *Driver) RunTurn(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ensureStartedLocked(ctx, in.WorkDir, in.Env); err != nil {
		return driver.TurnResult{}, err
	}
	p := d.proc
	fresh := false
	if d.sessionID == "" {
		if err := d.openSessionLocked(ctx, p, in.WorkDir); err != nil {
			return driver.TurnResult{}, err
		}
		fresh = !d.resumed
	}

	// ACP has no system role: standing instructions open a fresh session and persist in it.
	promptText := in.UserPrompt
	if fresh {
		for _, part := range []string{in.StartupContext, in.SystemPrompt} {
			if part != "" {
				promptText = part + "\n\n" + promptText
			}
		}
	}
	if in.OnActivity != nil {
		fn := func(params json.RawMessage) { in.Emit(activityOf(params)) }
		p.onUpdate.Store(&fn)
		defer p.onUpdate.Store(nil)
	}
	res, err := p.call(ctx, "session/prompt", map[string]any{
		"sessionId": d.sessionID,
		"prompt": []map[string]any{
			{"type": "text", "text": promptText},
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			p.notify("session/cancel", map[string]any{"sessionId": d.sessionID})
			return driver.TurnResult{}, driver.Fail(domain.AgentErrTurnFailed, fmt.Errorf("acp session/prompt: %w", ctx.Err()))
		}
		if p.exited() {
			if d.resumed {
				// A resumed session that kills the agent must not be resumed again.
				_ = d.forgetSession()
			}
			d.stopLocked()
			return driver.TurnResult{}, startFailure("acp process exited during prompt: %v", err, p)
		}
		return driver.TurnResult{}, driver.Fail(domain.AgentErrTurnFailed, fmt.Errorf("acp session/prompt: %w", err))
	}
	var stop struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(res, &stop)
	if stop.StopReason != "" && stop.StopReason != "end_turn" {
		return driver.TurnResult{}, driver.Fail(domain.AgentErrTurnFailed, fmt.Errorf("acp prompt stopped with %s", stop.StopReason))
	}
	return driver.TurnResult{}, nil
}

// openSessionLocked resumes the persisted session when the agent supports session/load and
// otherwise starts a new one, persisting its id.
func (d *Driver) openSessionLocked(ctx context.Context, p *process, workDir string) error {
	argv, _ := d.cfg.ResolvedCommand()
	if saved := d.loadSavedSession(argv); saved != "" && d.canLoad {
		loadCtx, cancel := context.WithTimeout(ctx, sessionTimeout)
		_, err := p.call(loadCtx, "session/load", map[string]any{
			"sessionId":  saved,
			"cwd":        workDir,
			"mcpServers": []any{},
		})
		cancel()
		if err == nil {
			d.sessionID, d.resumed = saved, true
			return nil
		}
		if p.exited() {
			_ = d.forgetSession()
			d.stopLocked()
			return startFailure("acp session/load: %v", err, p)
		}
	}
	sessCtx, cancel := context.WithTimeout(ctx, sessionTimeout)
	res, err := p.call(sessCtx, "session/new", map[string]any{
		"cwd":        workDir,
		"mcpServers": []any{},
	})
	cancel()
	if err != nil {
		d.stopLocked()
		return startFailure("acp session/new: %v", err, p)
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &out); err != nil || out.SessionID == "" {
		d.stopLocked()
		return startFailure("acp session/new: %v", errors.New("missing sessionId"), p)
	}
	d.sessionID, d.resumed = out.SessionID, false
	if err := d.saveSession(argv); err != nil {
		return driver.Fail(domain.AgentErrDriverStartFailed, fmt.Errorf("persist acp session: %w", err))
	}
	return nil
}

// activityOf maps an ACP session/update to an Activity, keeping only the tool call id, its kind
// and whether it finished; titles and content may quote prompts and are dropped.
func activityOf(params json.RawMessage) driver.Activity {
	var u struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			ToolCallID    string `json:"toolCallId"`
			Kind          string `json:"kind"`
			Status        string `json:"status"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &u) != nil {
		return driver.Activity{}
	}
	switch u.Update.SessionUpdate {
	case "tool_call", "tool_call_update":
		kind := u.Update.Kind
		if kind == "" {
			kind = "tool"
		}
		done := u.Update.Status == "completed" || u.Update.Status == "failed"
		return driver.Activity{ToolCallID: u.Update.ToolCallID, Tool: kind, Done: done}
	}
	return driver.Activity{}
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

// process owns one ACP child. A single reader goroutine dispatches responses to pending calls,
// answers agent->client requests, and drops notifications so stdout never stalls.
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tailBuffer

	writeMu sync.Mutex
	nextID  atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan rpcResponse

	// onUpdate receives session/update params for the in-flight turn; nil between turns.
	onUpdate atomic.Pointer[func(json.RawMessage)]

	done    chan struct{}
	readErr error
	killed  sync.Once
}

func startProcess(argv []string, workDir string, env []string) (*process, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = workDir
	cmd.Env = env
	// Own process group so kill also reaps launcher children (npx -> node).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tail := &tailBuffer{max: stderrTailBytes}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &process{
		cmd:     cmd,
		stdin:   stdin,
		stderr:  tail,
		pending: make(map[int64]chan rpcResponse),
		done:    make(chan struct{}),
	}
	go p.readLoop(stdout)
	return p, nil
}

func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *process) kill() {
	p.killed.Do(func() {
		_ = p.stdin.Close()
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
		go func() { _ = p.cmd.Wait() }()
	})
}

func (p *process) gracefulStop(timeout time.Duration) {
	p.killed.Do(func() {
		_ = p.stdin.Close()
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		}
		waited := make(chan struct{})
		go func() {
			_ = p.cmd.Wait()
			close(waited)
		}()
		select {
		case <-waited:
		case <-time.After(timeout):
			if p.cmd.Process != nil {
				_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			}
			<-waited
		}
	})
}

func (p *process) write(msg map[string]any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err = p.stdin.Write(append(raw, '\n'))
	return err
}

func (p *process) notify(method string, params any) {
	_ = p.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (p *process) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := p.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	p.pendingMu.Lock()
	p.pending[id] = ch
	p.pendingMu.Unlock()
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
	}()

	if err := p.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		if p.readErr != nil {
			return nil, fmt.Errorf("acp process ended: %w", p.readErr)
		}
		return nil, errors.New("acp process ended")
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("acp rpc %s: %w", method, r.err)
		}
		return r.result, nil
	}
}

type inbound struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *process) readLoop(stdout io.Reader) {
	defer close(p.done)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var msg inbound
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		hasID := len(msg.ID) > 0 && string(msg.ID) != "null"
		switch {
		case msg.Method != "" && hasID:
			p.answerRequest(msg)
		case msg.Method == "session/update":
			// Content is not rendered; only tool lifecycle feeds activity reporting.
			if fn := p.onUpdate.Load(); fn != nil {
				(*fn)(msg.Params)
			}
		case msg.Method != "":
		case hasID:
			var id int64
			if err := json.Unmarshal(msg.ID, &id); err != nil {
				continue
			}
			p.pendingMu.Lock()
			ch := p.pending[id]
			p.pendingMu.Unlock()
			if ch == nil {
				continue
			}
			if msg.Error != nil {
				ch <- rpcResponse{err: fmt.Errorf("error %d: %s", msg.Error.Code, msg.Error.Message)}
			} else {
				ch <- rpcResponse{result: msg.Result}
			}
		}
	}
	p.readErr = scanner.Err()
}

// answerRequest auto-approves permission prompts inside the full-access agent workspace and
// rejects other client methods, which Sumi does not advertise in clientCapabilities.
func (p *process) answerRequest(msg inbound) {
	if msg.Method != "session/request_permission" {
		_ = p.write(map[string]any{
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"error":   map[string]any{"code": -32601, "message": "method not supported by sumi"},
		})
		return
	}
	var params struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(msg.Params, &params)
	optionID := "allow_once"
	for _, kind := range []string{"allow_always", "allow_once"} {
		found := false
		for _, o := range params.Options {
			if o.Kind == kind {
				optionID, found = o.OptionID, true
				break
			}
		}
		if found {
			break
		}
	}
	_ = p.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      msg.ID,
		"result":  map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}},
	})
}

// tailBuffer keeps the last max bytes written, for bounded stderr diagnostics.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append([]byte(nil), t.buf[over:]...)
	}
	return len(b), nil
}

// lastLines returns up to n trailing non-empty lines joined by " | ".
func (t *tailBuffer) lastLines(n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(string(t.buf), "\n")
	out := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			out = append([]string{l}, out...)
		}
	}
	return strings.Join(out, " | ")
}
