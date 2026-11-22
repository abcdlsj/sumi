package pi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
)

const (
	// DefaultSoftCompactionTokens caps active context growth on large-window models (e.g. 1M)
	// before token costs and attention degradation accumulate.
	DefaultSoftCompactionTokens = 150_000
	softCompactionWindowRatio   = 0.75

	rpcCallTimeout     = 30 * time.Second
	statsCallTimeout   = 5 * time.Second
	compactCallTimeout = 90 * time.Second
	abortSettleTimeout = 2 * time.Second
	stderrTailBytes    = 2048

	compactionInstructions = "Preserve active Sumi conversation targets (#channel, task:#N, dm:@name, thread:<id>), open commitments or handoffs owed to teammates, workspace file changes made so far, and key facts needed to stay aligned with MEMORY.md."
)

type Config struct {
	// Binary is the pi executable name or path; defaults to "pi".
	Binary string
	// PrefixArgs are prepended before pi flags (used by tests to run a helper binary).
	PrefixArgs []string
	// SessionDir is passed to `pi --session-dir` to keep session transcripts inside the agent runtime dir.
	SessionDir string
	// SessionFile persists the active pi session file path and config fingerprint across restarts.
	SessionFile string
	// SystemPromptFile is written atomically from TurnInput.SystemPrompt and passed via `--system-prompt`,
	// replacing pi's built-in coding assistant prompt so Sumi instructions survive compaction.
	SystemPromptFile string
	// Fingerprint pins provider, model, and packages so a changed spec never resumes a stale session.
	Fingerprint string
	// SoftCompactionTokens triggers proactive `compact` after a settled turn when context usage
	// exceeds min(SoftCompactionTokens, 75% of contextWindow). Zero uses DefaultSoftCompactionTokens;
	// negative values disable proactive compaction.
	SoftCompactionTokens int
}

// Driver manages a persistent `pi --mode rpc` child process with a single active session.
type Driver struct {
	mu            sync.Mutex
	cfg           Config
	proc          *process
	sessionFile   string
	sysPromptHash string
	resumed       bool
	sentFirstTurn bool

	prevInputTokens  int
	prevOutputTokens int
	prevToolCalls    int

	closeOnce sync.Once
	closed    chan struct{}
}

func New(cfg Config) *Driver {
	return &Driver{cfg: cfg, closed: make(chan struct{})}
}

func (d *Driver) softCompactionLimit() int {
	if d.cfg.SoftCompactionTokens < 0 {
		return 0
	}
	if d.cfg.SoftCompactionTokens == 0 {
		return DefaultSoftCompactionTokens
	}
	return d.cfg.SoftCompactionTokens
}

func (d *Driver) ResetSession() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopLocked()
	d.sessionFile = ""
	d.resumed = false
	d.sentFirstTurn = false
	d.prevInputTokens, d.prevOutputTokens, d.prevToolCalls = 0, 0, 0
	return d.forgetSession()
}

// Close kills the child process group immediately so a replaced driver releases promptly.
func (d *Driver) Close() error {
	d.closeOnce.Do(func() { close(d.closed) })
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopLocked()
	return nil
}

// GracefulClose gives the child process group a bounded window to flush session state before SIGKILL.
func (d *Driver) GracefulClose(timeoutSeconds int) error {
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	d.mu.Lock()
	p := d.proc
	d.proc = nil
	d.resumed = false
	d.mu.Unlock()
	if p != nil {
		p.gracefulStop(timeout)
	}
	d.closeOnce.Do(func() { close(d.closed) })
	return nil
}

func (d *Driver) stopLocked() {
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

type savedSession struct {
	SessionFile string `json:"session_file"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func (d *Driver) loadSavedSession() string {
	if d.cfg.SessionFile == "" {
		return ""
	}
	raw, err := os.ReadFile(d.cfg.SessionFile)
	if err != nil {
		return ""
	}
	var s savedSession
	if json.Unmarshal(raw, &s) != nil || s.SessionFile == "" || s.Fingerprint != d.cfg.Fingerprint {
		return ""
	}
	if fi, err := os.Stat(s.SessionFile); err != nil || fi.IsDir() {
		return ""
	}
	return s.SessionFile
}

func (d *Driver) saveSession(sessionFile string) error {
	if d.cfg.SessionFile == "" || sessionFile == "" {
		return nil
	}
	raw, _ := json.Marshal(savedSession{
		SessionFile: sessionFile,
		Fingerprint: d.cfg.Fingerprint,
	})
	if err := os.MkdirAll(filepath.Dir(d.cfg.SessionFile), 0o700); err != nil {
		return err
	}
	tmp := d.cfg.SessionFile + ".tmp"
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

func (d *Driver) resolvedSessionDir(workDir string) string {
	if d.cfg.SessionDir != "" {
		return d.cfg.SessionDir
	}
	if d.cfg.SessionFile != "" {
		return filepath.Join(filepath.Dir(d.cfg.SessionFile), "runtime", "pi", "sessions")
	}
	return filepath.Join(workDir, ".pi-sessions")
}

func (d *Driver) resolvedSystemPromptFile(workDir string) string {
	if d.cfg.SystemPromptFile != "" {
		return d.cfg.SystemPromptFile
	}
	if d.cfg.SessionDir != "" {
		return filepath.Join(filepath.Dir(d.cfg.SessionDir), "SYSTEM.md")
	}
	if d.cfg.SessionFile != "" {
		return filepath.Join(filepath.Dir(d.cfg.SessionFile), "runtime", "pi", "SYSTEM.md")
	}
	return filepath.Join(workDir, ".pi-system-prompt.md")
}

func writeSystemPromptFile(path, prompt string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(prompt))
	hash := hex.EncodeToString(sum[:])
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(prompt), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return hash, nil
}

func (d *Driver) buildArgs(sysPromptPath, sessionDir, resumeFile string) []string {
	args := make([]string, 0, len(d.cfg.PrefixArgs)+14)
	args = append(args, d.cfg.PrefixArgs...)
	args = append(args,
		"--mode", "rpc",
		"--session-dir", sessionDir,
		"--no-skills",
		"--no-prompt-templates",
		"--no-themes",
		"--no-context-files",
		"--no-mcp",
	)
	if sysPromptPath != "" {
		args = append(args, "--system-prompt", sysPromptPath)
	}
	if resumeFile != "" {
		args = append(args, "--session", resumeFile)
	}
	return args
}

func (d *Driver) ensureStartedLocked(ctx context.Context, in driver.TurnInput) error {
	sysPromptPath := ""
	sysHash := ""
	if strings.TrimSpace(in.SystemPrompt) != "" {
		sysPromptPath = d.resolvedSystemPromptFile(in.WorkDir)
		h, err := writeSystemPromptFile(sysPromptPath, in.SystemPrompt)
		if err != nil {
			return driver.Fail(domain.AgentErrDriverStartFailed, fmt.Errorf("write pi system prompt: %w", err))
		}
		sysHash = h
	}

	if d.proc != nil && !d.proc.exited() {
		if sysHash == d.sysPromptHash {
			return nil
		}
		// Standing instructions changed; restart pi cleanly so `--system-prompt` reloads while keeping the session.
		d.proc.gracefulStop(2 * time.Second)
		d.proc = nil
	}
	d.stopLocked()
	if d.isClosed() {
		return driver.Fail(domain.AgentErrDriverStartFailed, errors.New("pi driver closed"))
	}

	sessionDir := d.resolvedSessionDir(in.WorkDir)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return driver.Fail(domain.AgentErrDriverStartFailed, fmt.Errorf("create pi session dir: %w", err))
	}

	resumeFile := ""
	if d.sessionFile != "" {
		if fi, err := os.Stat(d.sessionFile); err == nil && !fi.IsDir() {
			resumeFile = d.sessionFile
		}
	}
	if resumeFile == "" {
		resumeFile = d.loadSavedSession()
	}

	err := d.spawnAndProbeLocked(ctx, in.WorkDir, in.Env, sysPromptPath, sessionDir, resumeFile)
	if err != nil && resumeFile != "" {
		// Saved session may be corrupt or incompatible; forget it and open a fresh session once.
		_ = d.forgetSession()
		d.sessionFile = ""
		d.sentFirstTurn = false
		d.prevInputTokens, d.prevOutputTokens, d.prevToolCalls = 0, 0, 0
		err = d.spawnAndProbeLocked(ctx, in.WorkDir, in.Env, sysPromptPath, sessionDir, "")
	}
	if err != nil {
		return err
	}
	d.sysPromptHash = sysHash
	return nil
}

func (d *Driver) spawnAndProbeLocked(ctx context.Context, workDir string, env []string, sysPromptPath, sessionDir, resumeFile string) error {
	bin := d.cfg.Binary
	if bin == "" {
		bin = "pi"
	}
	args := d.buildArgs(sysPromptPath, sessionDir, resumeFile)
	p, err := startProcess(bin, args, workDir, env)
	if err != nil {
		return startFailure("start pi rpc: %v", err, nil)
	}
	d.proc = p
	go func() {
		select {
		case <-d.closed:
			p.kill()
		case <-p.done:
		}
	}()

	probeCtx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()
	data, err := p.call(probeCtx, "get_state", nil)
	if err != nil {
		d.stopLocked()
		return startFailure("pi get_state: %v", err, p)
	}
	var st struct {
		SessionFile string `json:"sessionFile"`
	}
	_ = json.Unmarshal(data, &st)
	if st.SessionFile != "" {
		d.sessionFile = st.SessionFile
	} else if resumeFile != "" {
		d.sessionFile = resumeFile
	}
	d.resumed = resumeFile != ""
	if d.resumed && d.sessionFile != "" {
		_ = d.saveSession(d.sessionFile)
	}
	return nil
}

type settledEvent struct {
	aborted bool
}

func (d *Driver) RunTurn(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ensureStartedLocked(ctx, in); err != nil {
		return driver.TurnResult{}, err
	}
	p := d.proc

	fresh := !d.sentFirstTurn && !d.resumed
	promptText := in.UserPrompt
	if fresh && in.StartupContext != "" {
		promptText = in.StartupContext + "\n\n" + promptText
	}

	settledCh := make(chan settledEvent, 1)
	var errMu sync.Mutex
	var lastRunErr string

	onEvent := func(ev rpcEvent) {
		switch ev.Type {
		case "tool_execution_start":
			tool := ev.ToolName
			if tool == "" {
				tool = "tool"
			}
			in.Emit(driver.Activity{ToolCallID: ev.ToolCallID, Tool: tool, Done: false})
		case "tool_execution_end":
			tool := ev.ToolName
			if tool == "" {
				tool = "tool"
			}
			in.Emit(driver.Activity{ToolCallID: ev.ToolCallID, Tool: tool, Done: true})
		case "message_update", "turn_start", "turn_end", "compaction_start", "compaction_end", "auto_retry_start":
			in.Emit(driver.Activity{})
		case "auto_retry_end":
			in.Emit(driver.Activity{})
			if !ev.Success && ev.FinalError != "" {
				errMu.Lock()
				lastRunErr = ev.FinalError
				errMu.Unlock()
			}
		case "agent_end":
			runErr := extractAssistantError(ev.Messages)
			errMu.Lock()
			if runErr != "" {
				lastRunErr = runErr
			} else if !ev.WillRetry {
				lastRunErr = ""
			}
			errMu.Unlock()
		case "agent_settled":
			select {
			case settledCh <- settledEvent{aborted: ev.Aborted}:
			default:
			}
		}
	}
	p.onEvent.Store(&onEvent)
	defer p.onEvent.Store(nil)

	promptCtx, promptCancel := context.WithTimeout(ctx, rpcCallTimeout)
	promptData, err := p.call(promptCtx, "prompt", map[string]any{"message": promptText})
	promptCancel()
	if err != nil {
		if ctx.Err() != nil {
			d.abortLocked(p, settledCh)
			return driver.TurnResult{}, driver.Fail(domain.AgentErrTurnFailed, fmt.Errorf("pi prompt: %w", ctx.Err()))
		}
		if p.exited() {
			if d.resumed {
				_ = d.forgetSession()
			}
			d.stopLocked()
			return driver.TurnResult{}, startFailure("pi process exited on prompt: %v", err, p)
		}
		return driver.TurnResult{}, classifyError(err.Error())
	}

	var disp struct {
		Disposition string `json:"disposition"`
	}
	_ = json.Unmarshal(promptData, &disp)
	if disp.Disposition == "handled" {
		d.sentFirstTurn = true
		d.persistSessionIfPresent()
		return driver.TurnResult{}, nil
	}

	select {
	case settled := <-settledCh:
		d.sentFirstTurn = true
		d.persistSessionIfPresent()
		if settled.aborted && ctx.Err() != nil {
			return driver.TurnResult{}, driver.Fail(domain.AgentErrTurnFailed, fmt.Errorf("pi turn cancelled: %w", ctx.Err()))
		}
		errMu.Lock()
		finalErr := lastRunErr
		errMu.Unlock()
		if finalErr != "" {
			return driver.TurnResult{}, classifyError(finalErr)
		}
		return d.postTurnStatsAndCompact(ctx, p, in), nil
	case <-ctx.Done():
		d.abortLocked(p, settledCh)
		return driver.TurnResult{}, driver.Fail(domain.AgentErrTurnFailed, fmt.Errorf("pi turn cancelled: %w", ctx.Err()))
	case <-p.done:
		if d.resumed {
			_ = d.forgetSession()
		}
		d.stopLocked()
		return driver.TurnResult{}, startFailure("pi process exited during turn: %v", p.readErr, p)
	}
}

func (d *Driver) abortLocked(p *process, settledCh <-chan settledEvent) {
	abortCtx, cancel := context.WithTimeout(context.Background(), abortSettleTimeout)
	_, _ = p.call(abortCtx, "abort", nil)
	cancel()
	select {
	case <-settledCh:
	case <-p.done:
	case <-time.After(abortSettleTimeout):
		// Unsettled abort leaves the child streaming; kill it so the next turn starts clean.
		d.stopLocked()
	}
}

func (d *Driver) persistSessionIfPresent() {
	if d.sessionFile == "" {
		return
	}
	if fi, err := os.Stat(d.sessionFile); err == nil && !fi.IsDir() {
		_ = d.saveSession(d.sessionFile)
	}
}

type sessionStats struct {
	SessionFile string `json:"sessionFile"`
	ToolCalls   int    `json:"toolCalls"`
	Tokens      struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		CacheRead int `json:"cacheRead"`
	} `json:"tokens"`
	ContextUsage *struct {
		Tokens        int `json:"tokens"`
		ContextWindow int `json:"contextWindow"`
	} `json:"contextUsage"`
}

func shouldProactiveCompact(contextTokens, contextWindow, softLimit int) bool {
	if softLimit <= 0 || contextTokens <= 0 {
		return false
	}
	threshold := softLimit
	if contextWindow > 0 {
		if ratioCap := int(float64(contextWindow) * softCompactionWindowRatio); ratioCap > 0 && ratioCap < threshold {
			threshold = ratioCap
		}
	}
	return contextTokens > threshold
}

func (d *Driver) postTurnStatsAndCompact(ctx context.Context, p *process, in driver.TurnInput) driver.TurnResult {
	var res driver.TurnResult
	statsCtx, cancel := context.WithTimeout(ctx, statsCallTimeout)
	raw, err := p.call(statsCtx, "get_session_stats", nil)
	cancel()
	if err != nil {
		return res
	}
	var st sessionStats
	if json.Unmarshal(raw, &st) != nil {
		return res
	}
	if st.SessionFile != "" {
		d.sessionFile = st.SessionFile
		d.persistSessionIfPresent()
	}

	totalIn := st.Tokens.Input + st.Tokens.CacheRead
	if totalIn >= d.prevInputTokens {
		res.InputTokens = totalIn - d.prevInputTokens
	}
	if st.Tokens.Output >= d.prevOutputTokens {
		res.OutputTokens = st.Tokens.Output - d.prevOutputTokens
	}
	if st.ToolCalls >= d.prevToolCalls {
		res.ToolCalls = st.ToolCalls - d.prevToolCalls
	}
	d.prevInputTokens, d.prevOutputTokens, d.prevToolCalls = totalIn, st.Tokens.Output, st.ToolCalls

	if st.ContextUsage != nil && shouldProactiveCompact(st.ContextUsage.Tokens, st.ContextUsage.ContextWindow, d.softCompactionLimit()) {
		in.Emit(driver.Activity{})
		compCtx, compCancel := context.WithTimeout(ctx, compactCallTimeout)
		_, _ = p.call(compCtx, "compact", map[string]any{"customInstructions": compactionInstructions})
		compCancel()
	}
	return res
}

func extractAssistantError(rawMessages json.RawMessage) string {
	if len(rawMessages) == 0 {
		return ""
	}
	var msgs []struct {
		Role         string `json:"role"`
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
	}
	if json.Unmarshal(rawMessages, &msgs) != nil {
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		if msgs[i].StopReason == "error" {
			if msgs[i].ErrorMessage != "" {
				return msgs[i].ErrorMessage
			}
			return "pi assistant turn ended with error"
		}
		return ""
	}
	return ""
}

func classifyError(msg string) error {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "401"),
		strings.Contains(lower, "403"),
		strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "invalid api key"),
		strings.Contains(lower, "authentication"),
		strings.Contains(lower, "not logged in"),
		strings.Contains(lower, "no api key"):
		return driver.Fail(domain.AgentErrModelAuthFailed, errors.New(msg))
	case strings.Contains(lower, "econnrefused"),
		strings.Contains(lower, "enotfound"),
		strings.Contains(lower, "etimedout"),
		strings.Contains(lower, "fetch failed"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "unreachable"),
		strings.Contains(lower, "dial tcp"):
		return driver.Fail(domain.AgentErrModelUnreachable, errors.New(msg))
	default:
		return driver.Fail(domain.AgentErrTurnFailed, errors.New(msg))
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

type rpcResponse struct {
	data json.RawMessage
	err  error
}

type rpcEvent struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Command    string          `json:"command"`
	Success    bool            `json:"success"`
	Data       json.RawMessage `json:"data"`
	Error      string          `json:"error"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Method     string          `json:"method"`
	Aborted    bool            `json:"aborted"`
	WillRetry  bool            `json:"willRetry"`
	FinalError string          `json:"finalError"`
	Messages   json.RawMessage `json:"messages"`
}

type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tailBuffer

	writeMu sync.Mutex
	nextID  atomic.Int64

	pendingMu sync.Mutex
	pending   map[string]chan rpcResponse

	onEvent atomic.Pointer[func(rpcEvent)]

	done    chan struct{}
	readErr error
	killed  sync.Once
}

func startProcess(bin string, args []string, workDir string, env []string) (*process, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = workDir
	cmd.Env = env
	// Own process group so kill/gracefulStop reaps the entire child tree.
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
		pending: make(map[string]chan rpcResponse),
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

func (p *process) call(ctx context.Context, cmdType string, fields map[string]any) (json.RawMessage, error) {
	id := strconv.FormatInt(p.nextID.Add(1), 10)
	ch := make(chan rpcResponse, 1)
	p.pendingMu.Lock()
	p.pending[id] = ch
	p.pendingMu.Unlock()
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
	}()

	payload := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		payload[k] = v
	}
	payload["id"] = id
	payload["type"] = cmdType
	if err := p.write(payload); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		if p.readErr != nil {
			return nil, fmt.Errorf("pi process ended: %w", p.readErr)
		}
		return nil, errors.New("pi process ended")
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("pi rpc %s: %w", cmdType, r.err)
		}
		return r.data, nil
	}
}

func (p *process) readLoop(stdout io.Reader) {
	defer close(p.done)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var ev rpcEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "response":
			if ev.ID == "" {
				continue
			}
			p.pendingMu.Lock()
			ch := p.pending[ev.ID]
			p.pendingMu.Unlock()
			if ch == nil {
				continue
			}
			if !ev.Success {
				msg := ev.Error
				if msg == "" {
					msg = "command failed"
				}
				ch <- rpcResponse{err: errors.New(msg)}
			} else {
				ch <- rpcResponse{data: ev.Data}
			}
		case "extension_ui_request":
			// Headless agents cannot answer interactive TUI prompts; cancel them immediately to avoid deadlock.
			switch ev.Method {
			case "select", "confirm", "input", "editor":
				if ev.ID != "" {
					_ = p.write(map[string]any{
						"type":      "extension_ui_response",
						"id":        ev.ID,
						"cancelled": true,
					})
				}
			}
		default:
			if fn := p.onEvent.Load(); fn != nil {
				(*fn)(ev)
			}
		}
	}
	p.readErr = scanner.Err()
}

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
