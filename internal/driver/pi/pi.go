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

	// DefaultHardRecycleTokens forces a clean session recycle at turn end when post-compaction
	// context still exceeds the ceiling, bounding continuously active sessions that never go idle.
	DefaultHardRecycleTokens     = 400_000
	hardRecycleWindowRatio       = 0.85
	DefaultMaxCompactionFailures = 3

	// DefaultColdRecycleIdle and DefaultColdRecycleMinTokens recycle large idle sessions once
	// the provider's prompt cache has expired, starting fresh with MEMORY.md instead of paying
	// a full cold rewrite of a bloated history.
	DefaultColdRecycleIdle      = 60 * time.Minute
	DefaultColdRecycleMinTokens = 64_000

	// DefaultCompactTimeout bounds a post-turn proactive `compact` RPC; large contexts need
	// several minutes for summarization, so 90s causes premature timeouts.
	DefaultCompactTimeout  = 5 * time.Minute
	maxCompactBackoffTurns = 4

	// DefaultMaxTurnSteps and DefaultMaxTurnToolCalls bound runaway multi-step / tool-call
	// loops within a single wake turn.
	DefaultMaxTurnSteps     = 60
	DefaultMaxTurnToolCalls = 120

	rpcCallTimeout     = 30 * time.Second
	statsCallTimeout   = 5 * time.Second
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
	// CompactTimeout bounds how long a post-turn proactive `compact` RPC may run.
	// Zero uses DefaultCompactTimeout.
	CompactTimeout time.Duration
	// HardRecycleTokens forces a warm session recycle at a clean turn end when post-compaction
	// context still exceeds min(HardRecycleTokens, 85% of contextWindow). Zero uses
	// DefaultHardRecycleTokens; negative values disable hard ceiling recycling.
	HardRecycleTokens int
	// MaxCompactionFailures forces a session recycle after this many consecutive failed
	// post-turn compactions while over the soft budget. Zero uses DefaultMaxCompactionFailures;
	// negative values disable failure-streak recycling.
	MaxCompactionFailures int
	// ColdRecycleIdle is the idle gap past which a large session is recycled instead of resumed.
	// Zero uses DefaultColdRecycleIdle; negative values disable cold wake recycling.
	ColdRecycleIdle time.Duration
	// ColdRecycleMinTokens is the minimum prior context size eligible for cold wake recycling.
	// Zero uses DefaultColdRecycleMinTokens; negative values disable cold wake recycling.
	ColdRecycleMinTokens int
	// MaxTurnSteps caps LLM steps (`turn_start` events) within one RunTurn. Zero uses
	// DefaultMaxTurnSteps; negative values disable the step budget.
	MaxTurnSteps int
	// MaxTurnToolCalls caps tool invocations (`tool_execution_start` events) within one RunTurn.
	// Zero uses DefaultMaxTurnToolCalls; negative values disable the tool call budget.
	MaxTurnToolCalls int
	// SessionRotateBytes triggers post-compaction JSONL seed rotation once the session file
	// reaches this size (preserving the same session ID). Zero uses DefaultSessionRotateBytes;
	// negative values disable rotation.
	SessionRotateBytes int64
	// Now overrides the clock for deterministic tests; defaults to time.Now.
	Now func() time.Time
}

// Driver manages a persistent `pi --mode rpc` child process with a single active session.
type Driver struct {
	mu                sync.Mutex
	cfg               Config
	proc              *process
	sessionFile       string
	sysPromptHash     string
	resumed           bool
	sentFirstTurn     bool
	compactArmed      bool
	compactFailStreak int
	compactSkipTurns  int
	lastActiveAt      time.Time
	lastContextTokens int
	lastContextWindow int

	prevInputTokens  int
	prevOutputTokens int
	prevToolCalls    int

	closeOnce sync.Once
	closed    chan struct{}
}

func New(cfg Config) *Driver {
	return &Driver{
		cfg:          cfg,
		compactArmed: true,
		closed:       make(chan struct{}),
	}
}

func (d *Driver) now() time.Time {
	if d.cfg.Now != nil {
		return d.cfg.Now()
	}
	return time.Now()
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

func (d *Driver) compactTimeout() time.Duration {
	if d.cfg.CompactTimeout > 0 {
		return d.cfg.CompactTimeout
	}
	return DefaultCompactTimeout
}

func (d *Driver) hardRecycleLimit() int {
	if d.cfg.HardRecycleTokens < 0 {
		return 0
	}
	if d.cfg.HardRecycleTokens == 0 {
		return DefaultHardRecycleTokens
	}
	return d.cfg.HardRecycleTokens
}

func (d *Driver) maxCompactionFailures() int {
	if d.cfg.MaxCompactionFailures < 0 {
		return 0
	}
	if d.cfg.MaxCompactionFailures == 0 {
		return DefaultMaxCompactionFailures
	}
	return d.cfg.MaxCompactionFailures
}

func (d *Driver) maxTurnSteps() int {
	if d.cfg.MaxTurnSteps < 0 {
		return 0
	}
	if d.cfg.MaxTurnSteps == 0 {
		return DefaultMaxTurnSteps
	}
	return d.cfg.MaxTurnSteps
}

func (d *Driver) maxTurnToolCalls() int {
	if d.cfg.MaxTurnToolCalls < 0 {
		return 0
	}
	if d.cfg.MaxTurnToolCalls == 0 {
		return DefaultMaxTurnToolCalls
	}
	return d.cfg.MaxTurnToolCalls
}

func (d *Driver) sessionRotateBytes() int64 {
	if d.cfg.SessionRotateBytes < 0 {
		return 0
	}
	if d.cfg.SessionRotateBytes == 0 {
		return DefaultSessionRotateBytes
	}
	return d.cfg.SessionRotateBytes
}

func (d *Driver) coldRecycleLimits() (time.Duration, int) {
	if d.cfg.ColdRecycleIdle < 0 || d.cfg.ColdRecycleMinTokens < 0 {
		return 0, 0
	}
	idle := d.cfg.ColdRecycleIdle
	if idle == 0 {
		idle = DefaultColdRecycleIdle
	}
	minTokens := d.cfg.ColdRecycleMinTokens
	if minTokens == 0 {
		minTokens = DefaultColdRecycleMinTokens
	}
	return idle, minTokens
}

func (d *Driver) shouldColdRecycle(lastActive time.Time, contextTokens int) bool {
	idle, minTokens := d.coldRecycleLimits()
	if idle <= 0 || minTokens <= 0 || lastActive.IsZero() || contextTokens <= minTokens {
		return false
	}
	return d.now().Sub(lastActive) >= idle
}

func (d *Driver) resetSessionStateLocked() {
	d.sessionFile = ""
	d.resumed = false
	d.sentFirstTurn = false
	d.compactArmed = true
	d.compactFailStreak = 0
	d.compactSkipTurns = 0
	d.lastActiveAt = time.Time{}
	d.lastContextTokens = 0
	d.lastContextWindow = 0
	d.prevInputTokens, d.prevOutputTokens, d.prevToolCalls = 0, 0, 0
}

func (d *Driver) recycleSessionLocked() {
	if d.proc != nil && !d.proc.exited() {
		d.proc.gracefulStop(2 * time.Second)
		d.proc = nil
	}
	d.stopLocked()
	_ = d.forgetSession()
	d.resetSessionStateLocked()
}

func (d *Driver) ResetSession() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopLocked()
	d.resetSessionStateLocked()
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
	SessionFile       string `json:"session_file"`
	Fingerprint       string `json:"fingerprint,omitempty"`
	LastActiveAt      string `json:"last_active_at,omitempty"`
	LastContextTokens int    `json:"last_context_tokens,omitempty"`
	LastContextWindow int    `json:"last_context_window,omitempty"`
}

func (d *Driver) loadSavedSession() (sessionFile string, lastActive time.Time, lastTokens, lastWindow int) {
	if d.cfg.SessionFile == "" {
		return "", time.Time{}, 0, 0
	}
	raw, err := os.ReadFile(d.cfg.SessionFile)
	if err != nil {
		return "", time.Time{}, 0, 0
	}
	var s savedSession
	if json.Unmarshal(raw, &s) != nil || s.SessionFile == "" || s.Fingerprint != d.cfg.Fingerprint {
		return "", time.Time{}, 0, 0
	}
	if fi, err := os.Stat(s.SessionFile); err != nil || fi.IsDir() {
		return "", time.Time{}, 0, 0
	}
	if s.LastActiveAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, s.LastActiveAt); err == nil {
			lastActive = t
		}
	}
	if d.shouldColdRecycle(lastActive, s.LastContextTokens) || shouldHardRecycle(s.LastContextTokens, s.LastContextWindow, d.hardRecycleLimit()) {
		_ = d.forgetSession()
		return "", time.Time{}, 0, 0
	}
	return s.SessionFile, lastActive, s.LastContextTokens, s.LastContextWindow
}

func (d *Driver) saveSession(sessionFile string, lastActive time.Time, lastTokens, lastWindow int) error {
	if d.cfg.SessionFile == "" || sessionFile == "" {
		return nil
	}
	rec := savedSession{
		SessionFile:       sessionFile,
		Fingerprint:       d.cfg.Fingerprint,
		LastContextTokens: lastTokens,
		LastContextWindow: lastWindow,
	}
	if !lastActive.IsZero() {
		rec.LastActiveAt = lastActive.UTC().Format(time.RFC3339Nano)
	}
	raw, _ := json.Marshal(rec)
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

func (d *Driver) resolvedCompactionFitFile(workDir string) string {
	if d.cfg.SessionDir != "" {
		return filepath.Join(filepath.Dir(d.cfg.SessionDir), "compaction-fit.mjs")
	}
	if d.cfg.SessionFile != "" {
		return filepath.Join(filepath.Dir(d.cfg.SessionFile), "runtime", "pi", "compaction-fit.mjs")
	}
	return filepath.Join(workDir, ".pi-compaction-fit.mjs")
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

func (d *Driver) buildArgs(sysPromptPath, compactionFitPath, sessionDir, resumeFile string) []string {
	args := make([]string, 0, len(d.cfg.PrefixArgs)+16)
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
	if compactionFitPath != "" {
		args = append(args, "--extension", compactionFitPath)
	}
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

	if d.shouldColdRecycle(d.lastActiveAt, d.lastContextTokens) {
		d.recycleSessionLocked()
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

	compactionFitPath := d.resolvedCompactionFitFile(in.WorkDir)
	if err := writeCompactionFitExtension(compactionFitPath); err != nil {
		return driver.Fail(domain.AgentErrDriverStartFailed, fmt.Errorf("write pi compaction-fit extension: %w", err))
	}

	resumeFile := ""
	if d.sessionFile != "" {
		if fi, err := os.Stat(d.sessionFile); err == nil && !fi.IsDir() {
			resumeFile = d.sessionFile
		}
	}
	if resumeFile == "" {
		savedFile, lastActive, lastTokens, lastWindow := d.loadSavedSession()
		if savedFile != "" {
			resumeFile = savedFile
			d.lastActiveAt = lastActive
			d.lastContextTokens = lastTokens
			d.lastContextWindow = lastWindow
		}
	}

	err := d.spawnAndProbeLocked(ctx, in.WorkDir, in.Env, sysPromptPath, compactionFitPath, sessionDir, resumeFile)
	if err != nil && resumeFile != "" {
		// Saved session may be corrupt or incompatible; forget it and open a fresh session once.
		_ = d.forgetSession()
		d.resetSessionStateLocked()
		err = d.spawnAndProbeLocked(ctx, in.WorkDir, in.Env, sysPromptPath, compactionFitPath, sessionDir, "")
	}
	if err != nil {
		return err
	}
	d.sysPromptHash = sysHash
	return nil
}

func (d *Driver) spawnAndProbeLocked(ctx context.Context, workDir string, env []string, sysPromptPath, compactionFitPath, sessionDir, resumeFile string) error {
	bin := d.cfg.Binary
	if bin == "" {
		bin = "pi"
	}
	args := d.buildArgs(sysPromptPath, compactionFitPath, sessionDir, resumeFile)
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
		_ = d.saveSession(d.sessionFile, d.lastActiveAt, d.lastContextTokens, d.lastContextWindow)
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
	budgetExceededCh := make(chan string, 1)
	maxSteps := d.maxTurnSteps()
	maxTools := d.maxTurnToolCalls()
	var stepCount atomic.Int64
	var toolCallCount atomic.Int64
	var errMu sync.Mutex
	var lastRunErr string

	signalBudgetExceeded := func(reason string) {
		select {
		case budgetExceededCh <- reason:
		default:
		}
	}

	onEvent := func(ev rpcEvent) {
		switch ev.Type {
		case "tool_execution_start":
			tool := ev.ToolName
			if tool == "" {
				tool = "tool"
			}
			in.Emit(driver.Activity{ToolCallID: ev.ToolCallID, Tool: tool, Done: false})
			if maxTools > 0 && toolCallCount.Add(1) > int64(maxTools) {
				signalBudgetExceeded(fmt.Sprintf("pi turn exceeded tool call budget (%d)", maxTools))
			}
		case "tool_execution_end":
			tool := ev.ToolName
			if tool == "" {
				tool = "tool"
			}
			in.Emit(driver.Activity{ToolCallID: ev.ToolCallID, Tool: tool, Done: true})
		case "turn_start":
			in.Emit(driver.Activity{})
			if maxSteps > 0 && stepCount.Add(1) > int64(maxSteps) {
				signalBudgetExceeded(fmt.Sprintf("pi turn exceeded step budget (%d)", maxSteps))
			}
		case "message_update", "turn_end", "compaction_start", "compaction_end", "auto_retry_start":
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
		d.lastActiveAt = d.now()
		d.persistSessionIfPresent()
		return driver.TurnResult{}, nil
	}

	finishBudgetExceeded := func(reason string, alreadySettled bool) (driver.TurnResult, error) {
		if !alreadySettled {
			d.abortLocked(p, settledCh)
		}
		d.sentFirstTurn = true
		d.lastActiveAt = d.now()
		var res driver.TurnResult
		if d.proc != nil && !d.proc.exited() {
			res = d.postTurnStatsAndCompact(ctx, p, in)
		} else {
			d.persistSessionIfPresent()
		}
		return res, driver.Fail(domain.AgentErrTurnBudgetExceeded, errors.New(reason))
	}

	select {
	case reason := <-budgetExceededCh:
		return finishBudgetExceeded(reason, false)
	case settled := <-settledCh:
		// If budget was tripped right as the turn settled, still enforce the budget outcome.
		select {
		case reason := <-budgetExceededCh:
			return finishBudgetExceeded(reason, true)
		default:
		}
		d.sentFirstTurn = true
		d.lastActiveAt = d.now()
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
		_ = d.saveSession(d.sessionFile, d.lastActiveAt, d.lastContextTokens, d.lastContextWindow)
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

func compactionThreshold(contextWindow, softLimit int) int {
	if softLimit <= 0 {
		return 0
	}
	threshold := softLimit
	if contextWindow > 0 {
		if ratioCap := int(float64(contextWindow) * softCompactionWindowRatio); ratioCap > 0 && ratioCap < threshold {
			threshold = ratioCap
		}
	}
	return threshold
}

func shouldProactiveCompact(contextTokens, contextWindow, softLimit int) bool {
	threshold := compactionThreshold(contextWindow, softLimit)
	return threshold > 0 && contextTokens > threshold
}

func hardRecycleThreshold(contextWindow, hardLimit int) int {
	if hardLimit <= 0 {
		return 0
	}
	threshold := hardLimit
	if contextWindow > 0 {
		if ratioCap := int(float64(contextWindow) * hardRecycleWindowRatio); ratioCap > 0 && ratioCap < threshold {
			threshold = ratioCap
		}
	}
	return threshold
}

func shouldHardRecycle(contextTokens, contextWindow, hardLimit int) bool {
	threshold := hardRecycleThreshold(contextWindow, hardLimit)
	return threshold > 0 && contextTokens > threshold
}

type compactionOutcome int

const (
	compactionCompleted compactionOutcome = iota
	// compactionRefused means the runtime did not run compaction (busy, already compacting, nothing to compact).
	compactionRefused
	// compactionFailed means compaction ran and errored or timed out.
	compactionFailed
)

func classifyCompactionError(err error) compactionOutcome {
	if err == nil {
		return compactionCompleted
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return compactionFailed
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "already compacting"),
		strings.Contains(lower, "compaction already"),
		strings.Contains(lower, "nothing to compact"),
		strings.Contains(lower, "no messages to compact"),
		strings.Contains(lower, "not enough messages"),
		strings.Contains(lower, "busy"),
		strings.Contains(lower, "streaming"),
		strings.Contains(lower, "cannot compact"):
		return compactionRefused
	default:
		return compactionFailed
	}
}

func compactBackoffTurns(failStreak int) int {
	if failStreak <= 0 {
		return 0
	}
	shift := failStreak - 1
	if shift > 2 {
		shift = 2
	}
	return min(1<<shift, maxCompactBackoffTurns)
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

	compactedOK := false
	if st.ContextUsage != nil && st.ContextUsage.Tokens > 0 {
		d.lastContextTokens = st.ContextUsage.Tokens
		if st.ContextUsage.ContextWindow > 0 {
			d.lastContextWindow = st.ContextUsage.ContextWindow
		}
		threshold := compactionThreshold(st.ContextUsage.ContextWindow, d.softCompactionLimit())
		if threshold > 0 {
			if st.ContextUsage.Tokens <= threshold {
				d.compactArmed = true
				d.compactFailStreak = 0
				d.compactSkipTurns = 0
			} else if d.compactArmed {
				if d.compactSkipTurns > 0 {
					d.compactSkipTurns--
				} else {
					in.Emit(driver.Activity{})
					compCtx, compCancel := context.WithTimeout(ctx, d.compactTimeout())
					_, compErr := p.call(compCtx, "compact", map[string]any{"customInstructions": compactionInstructions})
					compCancel()
					switch classifyCompactionError(compErr) {
					case compactionRefused:
						// Runtime did nothing; keep armed for the next settled turn without backoff.
						d.compactSkipTurns = 0
					case compactionFailed:
						// Back off bounded turns instead of permanently disarming on a transient failure or timeout.
						d.compactFailStreak++
						d.compactSkipTurns = compactBackoffTurns(d.compactFailStreak)
					case compactionCompleted:
						compactedOK = true
						d.compactFailStreak = 0
						d.compactSkipTurns = 0
						postCtx, postCancel := context.WithTimeout(ctx, statsCallTimeout)
						postRaw, postErr := p.call(postCtx, "get_session_stats", nil)
						postCancel()
						if postErr == nil {
							var postSt sessionStats
							if json.Unmarshal(postRaw, &postSt) == nil && postSt.ContextUsage != nil && postSt.ContextUsage.Tokens > 0 {
								d.lastContextTokens = postSt.ContextUsage.Tokens
								if postSt.ContextUsage.ContextWindow > 0 {
									d.lastContextWindow = postSt.ContextUsage.ContextWindow
								}
								// Only disarm when a successful compaction still left context above threshold.
								d.compactArmed = postSt.ContextUsage.Tokens <= threshold
							} else {
								d.compactSkipTurns = 1
							}
						} else {
							d.compactSkipTurns = 1
						}
					}
				}
			}
		}

		// Warm hard ceiling / repeated compaction failure backstop: recycle immediately at this
		// clean turn end so continuously woken sessions never grow unbounded.
		hitHardCeiling := shouldHardRecycle(d.lastContextTokens, d.lastContextWindow, d.hardRecycleLimit())
		maxFails := d.maxCompactionFailures()
		hitFailCap := maxFails > 0 && d.compactFailStreak >= maxFails && threshold > 0 && d.lastContextTokens > threshold
		if hitHardCeiling || hitFailCap {
			d.recycleSessionLocked()
			return res
		}
	}

	// Post-compaction JSONL rotation: when the compacted session file exceeds the size threshold,
	// flush Pi cleanly and rewrite a lean same-ID seed so compacted-away entries stop bloating disk/RAM.
	if compactedOK && d.sessionFile != "" {
		if limit := d.sessionRotateBytes(); limit > 0 {
			if fi, err := os.Stat(d.sessionFile); err == nil && !fi.IsDir() && fi.Size() >= limit {
				if d.proc != nil && !d.proc.exited() {
					d.proc.gracefulStop(2 * time.Second)
					d.proc = nil
					d.resumed = false
				}
				if activeFile, rotated, _ := rotateSessionFile(d.sessionFile, d.now()); rotated && activeFile != "" {
					d.sessionFile = activeFile
					// Cumulative usage entries before the kept segment are dropped on rotation.
					d.prevInputTokens, d.prevOutputTokens, d.prevToolCalls = 0, 0, 0
				}
			}
		}
	}
	d.persistSessionIfPresent()
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
	if err := driver.EnsureWorkDir(workDir); err != nil {
		return nil, err
	}
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
