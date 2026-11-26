// Package claude drives Claude Code natively through `claude -p` with stream-json stdin/stdout,
// mirroring the pi driver's session, compaction, recycling and per-turn budget guarantees.
package claude

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
	DefaultSoftCompactionTokens = 150_000
	softCompactionWindowRatio   = 0.75

	DefaultHardRecycleTokens     = 400_000
	hardRecycleWindowRatio       = 0.85
	DefaultMaxCompactionFailures = 3

	DefaultColdRecycleIdle      = 60 * time.Minute
	DefaultColdRecycleMinTokens = 64_000

	DefaultCompactTimeout  = 5 * time.Minute
	maxCompactBackoffTurns = 4

	// A single wake turn routinely covers a whole feature (read, edit, test, fix), which
	// needs well over 60 model round-trips; 60 cut real work off mid-turn.
	DefaultMaxTurnSteps     = 120
	DefaultMaxTurnToolCalls = 240

	controlCallTimeout     = 10 * time.Second
	interruptSettleTimeout = 5 * time.Second
	stderrTailBytes        = 2048

	// ContextWindowEnv sets Claude Code's auto-compact window; it overrides --autocompact and settings.
	ContextWindowEnv = "CLAUDE_CODE_AUTO_COMPACT_WINDOW"

	compactionInstructions = "Preserve active Sumi conversation targets (#channel, task:#N, dm:@name, thread:<id>), open commitments or handoffs owed to teammates, workspace file changes made so far, and key facts needed to stay aligned with MEMORY.md."
)

// DisallowedTools are Claude Code tools that schedule work outside a Sumi turn or block on an
// interactive human, which a headless wake-driven agent must not use.
var DisallowedTools = []string{
	"CronCreate", "CronDelete", "CronList", "ScheduleWakeup",
	"EnterPlanMode", "ExitPlanMode", "EnterWorktree", "ExitWorktree",
	"Workflow", "Artifact", "ArtifactComments", "ArtifactData", "ArtifactCheck", "SendFeedback",
}

type Config struct {
	// Binary is the claude executable; defaults to "claude".
	Binary string
	// PrefixArgs are prepended before claude flags (launcher wrappers, test helpers).
	PrefixArgs []string
	// SessionFile persists the session id, fingerprint and last context usage across restarts.
	SessionFile string
	// SystemPromptFile receives TurnInput.SystemPrompt and is passed via --system-prompt-file,
	// replacing Claude Code's default prompt.
	SystemPromptFile string
	// Fingerprint pins provider and model so a changed spec never resumes a stale session.
	Fingerprint string
	// ContextWindow, when positive, is exported as CLAUDE_CODE_AUTO_COMPACT_WINDOW.
	ContextWindow int

	// The knobs below match pi.Config: zero uses the default, negative disables.
	SoftCompactionTokens  int
	CompactTimeout        time.Duration
	HardRecycleTokens     int
	MaxCompactionFailures int
	ColdRecycleIdle       time.Duration
	ColdRecycleMinTokens  int
	MaxTurnSteps          int
	MaxTurnToolCalls      int
	Now                   func() time.Time
}

// Driver manages one persistent `claude -p` stream-json child and its single session.
type Driver struct {
	mu                sync.Mutex
	cfg               Config
	proc              *process
	sessionID         string
	sysPromptHash     string
	resumed           bool
	sentFirstTurn     bool
	compactArmed      bool
	compactFailStreak int
	compactSkipTurns  int
	lastActiveAt      time.Time
	lastContextTokens int
	lastContextWindow int

	closeOnce sync.Once
	closed    chan struct{}
}

func New(cfg Config) *Driver {
	return &Driver{cfg: cfg, compactArmed: true, closed: make(chan struct{})}
}

func knob(v, def int) int {
	if v < 0 {
		return 0
	}
	if v == 0 {
		return def
	}
	return v
}

func (d *Driver) now() time.Time {
	if d.cfg.Now != nil {
		return d.cfg.Now()
	}
	return time.Now()
}

func (d *Driver) softLimit() int {
	return knob(d.cfg.SoftCompactionTokens, DefaultSoftCompactionTokens)
}
func (d *Driver) hardLimit() int { return knob(d.cfg.HardRecycleTokens, DefaultHardRecycleTokens) }
func (d *Driver) maxFailures() int {
	return knob(d.cfg.MaxCompactionFailures, DefaultMaxCompactionFailures)
}
func (d *Driver) maxSteps() int     { return knob(d.cfg.MaxTurnSteps, DefaultMaxTurnSteps) }
func (d *Driver) maxToolCalls() int { return knob(d.cfg.MaxTurnToolCalls, DefaultMaxTurnToolCalls) }

func (d *Driver) compactTimeout() time.Duration {
	if d.cfg.CompactTimeout > 0 {
		return d.cfg.CompactTimeout
	}
	return DefaultCompactTimeout
}

func (d *Driver) shouldColdRecycle(lastActive time.Time, tokens int) bool {
	if d.cfg.ColdRecycleIdle < 0 || d.cfg.ColdRecycleMinTokens < 0 || lastActive.IsZero() {
		return false
	}
	idle := d.cfg.ColdRecycleIdle
	if idle == 0 {
		idle = DefaultColdRecycleIdle
	}
	if tokens <= knob(d.cfg.ColdRecycleMinTokens, DefaultColdRecycleMinTokens) {
		return false
	}
	return d.now().Sub(lastActive) >= idle
}

// windowThreshold returns min(limit, ratio*window), or 0 when limit is disabled.
func windowThreshold(window, limit int, ratio float64) int {
	if limit <= 0 {
		return 0
	}
	if window > 0 {
		if c := int(float64(window) * ratio); c > 0 && c < limit {
			return c
		}
	}
	return limit
}

func compactionThreshold(window, soft int) int {
	return windowThreshold(window, soft, softCompactionWindowRatio)
}

func shouldHardRecycle(tokens, window, hard int) bool {
	t := windowThreshold(window, hard, hardRecycleWindowRatio)
	return t > 0 && tokens > t
}

func compactBackoffTurns(failStreak int) int {
	if failStreak <= 0 {
		return 0
	}
	return min(1<<min(failStreak-1, 2), maxCompactBackoffTurns)
}

func (d *Driver) resetSessionStateLocked() {
	d.sessionID = ""
	d.resumed = false
	d.sentFirstTurn = false
	d.compactArmed = true
	d.compactFailStreak = 0
	d.compactSkipTurns = 0
	d.lastActiveAt = time.Time{}
	d.lastContextTokens = 0
	d.lastContextWindow = 0
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

// Close kills the child process group without waiting for an in-flight turn.
func (d *Driver) Close() error {
	d.closeOnce.Do(func() { close(d.closed) })
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopLocked()
	return nil
}

// GracefulClose lets claude flush its transcript before SIGKILL.
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
	SessionID         string `json:"session_id"`
	Fingerprint       string `json:"fingerprint,omitempty"`
	LastActiveAt      string `json:"last_active_at,omitempty"`
	LastContextTokens int    `json:"last_context_tokens,omitempty"`
	LastContextWindow int    `json:"last_context_window,omitempty"`
}

func (d *Driver) loadSavedSession() (id string, lastActive time.Time, tokens, window int) {
	if d.cfg.SessionFile == "" {
		return "", time.Time{}, 0, 0
	}
	raw, err := os.ReadFile(d.cfg.SessionFile)
	if err != nil {
		return "", time.Time{}, 0, 0
	}
	var s savedSession
	if json.Unmarshal(raw, &s) != nil || s.SessionID == "" || s.Fingerprint != d.cfg.Fingerprint {
		return "", time.Time{}, 0, 0
	}
	if s.LastActiveAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, s.LastActiveAt); err == nil {
			lastActive = t
		}
	}
	if d.shouldColdRecycle(lastActive, s.LastContextTokens) || shouldHardRecycle(s.LastContextTokens, s.LastContextWindow, d.hardLimit()) {
		_ = d.forgetSession()
		return "", time.Time{}, 0, 0
	}
	return s.SessionID, lastActive, s.LastContextTokens, s.LastContextWindow
}

func (d *Driver) persistSession() {
	if d.cfg.SessionFile == "" || d.sessionID == "" {
		return
	}
	rec := savedSession{
		SessionID:         d.sessionID,
		Fingerprint:       d.cfg.Fingerprint,
		LastContextTokens: d.lastContextTokens,
		LastContextWindow: d.lastContextWindow,
	}
	if !d.lastActiveAt.IsZero() {
		rec.LastActiveAt = d.lastActiveAt.UTC().Format(time.RFC3339Nano)
	}
	raw, _ := json.Marshal(rec)
	if err := os.MkdirAll(filepath.Dir(d.cfg.SessionFile), 0o700); err != nil {
		return
	}
	tmp := d.cfg.SessionFile + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, d.cfg.SessionFile)
	}
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

func (d *Driver) resolvedSystemPromptFile(workDir string) string {
	if d.cfg.SystemPromptFile != "" {
		return d.cfg.SystemPromptFile
	}
	if d.cfg.SessionFile != "" {
		return filepath.Join(filepath.Dir(d.cfg.SessionFile), "runtime", "claude", "SYSTEM.md")
	}
	return filepath.Join(workDir, ".claude-system-prompt.md")
}

func writeSystemPromptFile(path, prompt string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(prompt))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(prompt), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum[:]), nil
}

func (d *Driver) buildArgs(sysPromptPath, resumeID string) []string {
	args := append([]string{}, d.cfg.PrefixArgs...)
	args = append(args,
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		// Empty sources skip user/project/local settings so hooks and plugins never load.
		"--setting-sources", "",
		"--strict-mcp-config",
		"--permission-mode", "bypassPermissions",
		"--dangerously-skip-permissions",
		"--disallowed-tools", strings.Join(DisallowedTools, ","),
	)
	if sysPromptPath != "" {
		// Snapshot off so a changed prompt applies when a restarted process resumes the session.
		args = append(args, "--system-prompt-file", sysPromptPath, "--system-prompt-snapshot", "off")
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	return args
}

func (d *Driver) ensureStartedLocked(in driver.TurnInput) error {
	sysPromptPath, sysHash := "", ""
	if strings.TrimSpace(in.SystemPrompt) != "" {
		sysPromptPath = d.resolvedSystemPromptFile(in.WorkDir)
		h, err := writeSystemPromptFile(sysPromptPath, in.SystemPrompt)
		if err != nil {
			return driver.Fail(domain.AgentErrDriverStartFailed, fmt.Errorf("write claude system prompt: %w", err))
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
		d.proc.gracefulStop(2 * time.Second)
		d.proc = nil
	}
	d.stopLocked()
	if d.isClosed() {
		return driver.Fail(domain.AgentErrDriverStartFailed, errors.New("claude driver closed"))
	}
	resumeID := d.sessionID
	if resumeID == "" {
		id, lastActive, tokens, window := d.loadSavedSession()
		if id != "" {
			resumeID = id
			d.sessionID = id
			d.lastActiveAt, d.lastContextTokens, d.lastContextWindow = lastActive, tokens, window
		}
	}
	bin := d.cfg.Binary
	if bin == "" {
		bin = "claude"
	}
	env := in.Env
	if d.cfg.ContextWindow > 0 {
		env = setEnv(env, ContextWindowEnv, strconv.Itoa(d.cfg.ContextWindow))
	}
	p, err := startProcess(bin, d.buildArgs(sysPromptPath, resumeID), in.WorkDir, env)
	if err != nil {
		return startFailure("start claude: %v", err, nil)
	}
	d.proc = p
	go func() {
		select {
		case <-d.closed:
			p.kill()
		case <-p.done:
		}
	}()
	d.resumed = resumeID != ""
	d.sysPromptHash = sysHash
	return nil
}

func (d *Driver) RunTurn(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err, staleResume := d.runTurnLocked(ctx, in)
	if staleResume {
		// The saved session is gone or unloadable; start fresh once with MEMORY.md.
		d.stopLocked()
		_ = d.forgetSession()
		d.resetSessionStateLocked()
		res, err, _ = d.runTurnLocked(ctx, in)
	}
	return res, err
}

// turnObserver tracks one prompt's stream: activity, budgets and the terminal result.
type turnObserver struct {
	in          driver.TurnInput
	maxSteps    int
	maxTools    int
	steps       map[string]bool
	toolCalls   atomic.Int64
	sawModel    atomic.Bool
	sawCompact  atomic.Bool
	compactFail atomic.Bool
	resultCh    chan streamMsg
	budgetCh    chan string
}

func newObserver(in driver.TurnInput, maxSteps, maxTools int) *turnObserver {
	return &turnObserver{
		in: in, maxSteps: maxSteps, maxTools: maxTools,
		steps:    map[string]bool{},
		resultCh: make(chan streamMsg, 1),
		budgetCh: make(chan string, 1),
	}
}

func (o *turnObserver) budget(reason string) {
	select {
	case o.budgetCh <- reason:
	default:
	}
}

// handle runs on the reader goroutine; it must not block.
func (o *turnObserver) handle(m streamMsg) {
	switch m.Type {
	case "assistant":
		o.sawModel.Store(true)
		o.in.Emit(driver.Activity{})
		var msg struct {
			ID      string `json:"id"`
			Content []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content"`
		}
		_ = json.Unmarshal(m.Message, &msg)
		topLevel := m.ParentToolUseID == nil || *m.ParentToolUseID == ""
		if topLevel && msg.ID != "" && !o.steps[msg.ID] {
			o.steps[msg.ID] = true
			if o.maxSteps > 0 && len(o.steps) > o.maxSteps {
				o.budget(fmt.Sprintf("claude turn exceeded step budget (%d)", o.maxSteps))
			}
		}
		for _, c := range msg.Content {
			if c.Type != "tool_use" {
				continue
			}
			name := c.Name
			if name == "" {
				name = "tool"
			}
			o.in.Emit(driver.Activity{ToolCallID: c.ID, Tool: name})
			if topLevel {
				if n := o.toolCalls.Add(1); o.maxTools > 0 && n > int64(o.maxTools) {
					o.budget(fmt.Sprintf("claude turn exceeded tool call budget (%d)", o.maxTools))
				}
			}
		}
	case "user":
		var msg struct {
			Content json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(m.Message, &msg)
		var blocks []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
		}
		if json.Unmarshal(msg.Content, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "tool_result" && b.ToolUseID != "" {
					o.in.Emit(driver.Activity{ToolCallID: b.ToolUseID, Tool: "tool", Done: true})
				}
			}
		}
	case "system":
		switch m.Subtype {
		case "compact_boundary":
			o.sawCompact.Store(true)
		case "status":
			if m.CompactResult != "" && m.CompactResult != "success" {
				o.compactFail.Store(true)
			}
		}
		o.in.Emit(driver.Activity{})
	case "result":
		select {
		case o.resultCh <- m:
		default:
		}
	}
}

func userMessage(text string) map[string]any {
	return map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": text}}},
	}
}

// runTurnLocked returns staleResume=true when a resumed session failed before the model ran.
func (d *Driver) runTurnLocked(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error, bool) {
	if err := d.ensureStartedLocked(in); err != nil {
		return driver.TurnResult{}, err, false
	}
	p := d.proc
	fresh := !d.sentFirstTurn && !d.resumed
	prompt := in.UserPrompt
	if fresh && in.StartupContext != "" {
		prompt = in.StartupContext + "\n\n" + prompt
	}

	obs := newObserver(in, d.maxSteps(), d.maxToolCalls())
	fn := obs.handle
	p.onMsg.Store(&fn)
	defer p.onMsg.Store(nil)
	if err := p.write(userMessage(prompt)); err != nil {
		d.stopLocked()
		return driver.TurnResult{}, startFailure("write claude prompt: %v", err, p), false
	}

	finish := func(r streamMsg, budgetReason string) (driver.TurnResult, error, bool) {
		if r.SessionID != "" {
			d.sessionID = r.SessionID
		}
		if budgetReason == "" && r.IsError && d.resumed && !obs.sawModel.Load() &&
			strings.Contains(strings.ToLower(r.errorText()), "no conversation found") {
			return driver.TurnResult{}, nil, true
		}
		d.sentFirstTurn = true
		d.lastActiveAt = d.now()
		res := driver.TurnResult{ToolCalls: int(obs.toolCalls.Load())}
		if r.Usage != nil {
			res.InputTokens = r.Usage.InputTokens + r.Usage.CacheReadInputTokens + r.Usage.CacheCreationInputTokens
			res.OutputTokens = r.Usage.OutputTokens
		}
		if budgetReason == "" && r.IsError {
			d.persistSession()
			return res, classifyError(r.errorText()), false
		}
		if d.proc != nil && !d.proc.exited() {
			d.postTurnStatsAndCompact(ctx, p, in)
		} else {
			d.persistSession()
		}
		if budgetReason != "" {
			return res, driver.Fail(domain.AgentErrTurnBudgetExceeded, errors.New(budgetReason)), false
		}
		return res, nil, false
	}

	select {
	case reason := <-obs.budgetCh:
		r, ok := d.interruptLocked(p, obs.resultCh)
		if !ok {
			d.sentFirstTurn = true
			d.lastActiveAt = d.now()
			d.persistSession()
			return driver.TurnResult{ToolCalls: int(obs.toolCalls.Load())}, driver.Fail(domain.AgentErrTurnBudgetExceeded, errors.New(reason)), false
		}
		return finish(r, reason)
	case r := <-obs.resultCh:
		select {
		case reason := <-obs.budgetCh:
			return finish(r, reason)
		default:
		}
		return finish(r, "")
	case <-ctx.Done():
		d.interruptLocked(p, obs.resultCh)
		return driver.TurnResult{}, driver.Fail(domain.AgentErrTurnFailed, fmt.Errorf("claude turn cancelled: %w", ctx.Err())), false
	case <-p.done:
		if d.resumed && !obs.sawModel.Load() {
			return driver.TurnResult{}, nil, true
		}
		if d.resumed {
			_ = d.forgetSession()
		}
		d.stopLocked()
		return driver.TurnResult{}, startFailure("claude exited during turn: %v", p.readErr, p), false
	}
}

// interruptLocked asks claude to stop the in-flight query and waits for its result; an unsettled
// interrupt kills the child so the next turn starts clean.
func (d *Driver) interruptLocked(p *process, resultCh <-chan streamMsg) (streamMsg, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), interruptSettleTimeout)
	defer cancel()
	_, _ = p.control(ctx, "interrupt", nil)
	select {
	case r := <-resultCh:
		return r, true
	case <-p.done:
	case <-ctx.Done():
		d.stopLocked()
	}
	return streamMsg{}, false
}

type contextUsage struct {
	TotalTokens int `json:"totalTokens"`
	MaxTokens   int `json:"maxTokens"`
}

func (d *Driver) contextUsageLocked(ctx context.Context, p *process) (contextUsage, bool) {
	cctx, cancel := context.WithTimeout(ctx, controlCallTimeout)
	defer cancel()
	raw, err := p.control(cctx, "get_context_usage", nil)
	if err != nil {
		return contextUsage{}, false
	}
	var u contextUsage
	if json.Unmarshal(raw, &u) != nil || u.TotalTokens <= 0 {
		return contextUsage{}, false
	}
	return u, true
}

type compactionOutcome int

const (
	compactionCompleted compactionOutcome = iota
	compactionRefused
	compactionFailed
)

// compactLocked runs `/compact` as a stdin turn and classifies the outcome.
func (d *Driver) compactLocked(ctx context.Context, p *process, in driver.TurnInput) compactionOutcome {
	obs := newObserver(in, 0, 0)
	fn := obs.handle
	p.onMsg.Store(&fn)
	defer p.onMsg.Store(nil)
	if p.write(userMessage("/compact "+compactionInstructions)) != nil {
		return compactionFailed
	}
	cctx, cancel := context.WithTimeout(ctx, d.compactTimeout())
	defer cancel()
	select {
	case r := <-obs.resultCh:
		switch {
		case obs.sawCompact.Load() && !obs.compactFail.Load():
			return compactionCompleted
		case obs.compactFail.Load():
			return compactionFailed
		case r.IsError && !isRefusal(r.errorText()):
			return compactionFailed
		default:
			return compactionRefused
		}
	case <-cctx.Done():
		d.interruptLocked(p, obs.resultCh)
		return compactionFailed
	case <-p.done:
		return compactionFailed
	}
}

func isRefusal(msg string) bool {
	lower := strings.ToLower(msg)
	for _, s := range []string{"not enough messages", "nothing to compact", "no messages to compact", "already compacting"} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func (d *Driver) postTurnStatsAndCompact(ctx context.Context, p *process, in driver.TurnInput) {
	defer d.persistSession()
	u, ok := d.contextUsageLocked(ctx, p)
	if !ok {
		return
	}
	d.lastContextTokens, d.lastContextWindow = u.TotalTokens, u.MaxTokens
	threshold := compactionThreshold(u.MaxTokens, d.softLimit())
	if threshold > 0 {
		switch {
		case u.TotalTokens <= threshold:
			d.compactArmed, d.compactFailStreak, d.compactSkipTurns = true, 0, 0
		case !d.compactArmed:
		case d.compactSkipTurns > 0:
			d.compactSkipTurns--
		default:
			in.Emit(driver.Activity{})
			switch d.compactLocked(ctx, p, in) {
			case compactionRefused:
				d.compactSkipTurns = 0
			case compactionFailed:
				d.compactFailStreak++
				d.compactSkipTurns = compactBackoffTurns(d.compactFailStreak)
			case compactionCompleted:
				d.compactFailStreak, d.compactSkipTurns = 0, 0
				if post, ok := d.contextUsageLocked(ctx, p); ok {
					d.lastContextTokens = post.TotalTokens
					if post.MaxTokens > 0 {
						d.lastContextWindow = post.MaxTokens
					}
					// Disarm only when a successful compaction still left context above threshold.
					d.compactArmed = post.TotalTokens <= threshold
				} else {
					d.compactSkipTurns = 1
				}
			}
		}
	}
	hitHard := shouldHardRecycle(d.lastContextTokens, d.lastContextWindow, d.hardLimit())
	maxFails := d.maxFailures()
	hitFails := maxFails > 0 && d.compactFailStreak >= maxFails && threshold > 0 && d.lastContextTokens > threshold
	if hitHard || hitFails {
		d.recycleSessionLocked()
	}
}

func classifyError(msg string) error {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "401"), strings.Contains(lower, "403"),
		strings.Contains(lower, "unauthorized"), strings.Contains(lower, "invalid api key"),
		strings.Contains(lower, "authentication"), strings.Contains(lower, "not logged in"),
		strings.Contains(lower, "/login"):
		return driver.Fail(domain.AgentErrModelAuthFailed, errors.New(msg))
	case strings.Contains(lower, "econnrefused"), strings.Contains(lower, "enotfound"),
		strings.Contains(lower, "etimedout"), strings.Contains(lower, "fetch failed"),
		strings.Contains(lower, "connection refused"), strings.Contains(lower, "unreachable"),
		strings.Contains(lower, "connection error"):
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

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+value)
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type streamMsg struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Message         json.RawMessage `json:"message"`
	IsError         bool            `json:"is_error"`
	Result          string          `json:"result"`
	Errors          []string        `json:"errors"`
	Usage           *usage          `json:"usage"`
	CompactResult   string          `json:"compact_result"`
	RequestID       string          `json:"request_id"`
	Response        *struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Response  json.RawMessage `json:"response"`
		Error     string          `json:"error"`
	} `json:"response"`
}

// errorText describes a failed result. Only called when is_error is set, so Result holds the
// runtime's error text rather than model output.
func (m streamMsg) errorText() string {
	if len(m.Errors) > 0 {
		return strings.Join(m.Errors, "; ")
	}
	if m.Result != "" {
		return m.Result
	}
	if m.Subtype != "" {
		return "claude result " + m.Subtype
	}
	return "claude turn failed"
}

type controlResponse struct {
	data json.RawMessage
	err  error
}

type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tailBuffer

	writeMu sync.Mutex
	nextID  atomic.Int64

	pendingMu sync.Mutex
	pending   map[string]chan controlResponse

	// onMsg receives stream messages for the in-flight prompt; nil between prompts.
	onMsg atomic.Pointer[func(streamMsg)]

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
		cmd: cmd, stdin: stdin, stderr: tail,
		pending: map[string]chan controlResponse{},
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

func (p *process) write(msg any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err = p.stdin.Write(append(raw, '\n'))
	return err
}

// control sends a stream-json control_request and waits for the matching control_response.
func (p *process) control(ctx context.Context, subtype string, fields map[string]any) (json.RawMessage, error) {
	id := "sumi-" + strconv.FormatInt(p.nextID.Add(1), 10)
	ch := make(chan controlResponse, 1)
	p.pendingMu.Lock()
	p.pending[id] = ch
	p.pendingMu.Unlock()
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
	}()
	req := map[string]any{"subtype": subtype}
	for k, v := range fields {
		req[k] = v
	}
	if err := p.write(map[string]any{"type": "control_request", "request_id": id, "request": req}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return nil, errors.New("claude process ended")
	case r := <-ch:
		return r.data, r.err
	}
}

func (p *process) readLoop(stdout io.Reader) {
	defer close(p.done)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		var m streamMsg
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "control_response":
			if m.Response == nil {
				continue
			}
			p.pendingMu.Lock()
			ch := p.pending[m.Response.RequestID]
			p.pendingMu.Unlock()
			if ch == nil {
				continue
			}
			if m.Response.Subtype == "error" {
				msg := m.Response.Error
				if msg == "" {
					msg = "control request failed"
				}
				ch <- controlResponse{err: errors.New(msg)}
			} else {
				ch <- controlResponse{data: m.Response.Response}
			}
		case "control_request":
			// Sumi registers no hooks or permission tools; reject so claude never waits on us.
			_ = p.write(map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype": "error", "request_id": m.RequestID, "error": "not supported by sumi",
				},
			})
		default:
			if fn := p.onMsg.Load(); fn != nil {
				(*fn)(m)
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
