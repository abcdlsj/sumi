package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
)

// TestFakePiRPC is re-executed as a fake `pi --mode rpc` subprocess by unit tests.
func TestFakePiRPC(t *testing.T) {
	mode := os.Getenv("SUMI_FAKE_PI")
	if mode == "" {
		t.Skip("helper process")
	}
	if mode == "crash" {
		fmt.Fprintln(os.Stderr, "booting pi")
		fmt.Fprintln(os.Stderr, "fatal: missing provider auth")
		os.Exit(3)
	}

	logPath := os.Getenv("SUMI_FAKE_PI_LOG")
	logLine := func(s string) {
		if logPath == "" {
			return
		}
		f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		_, _ = fmt.Fprintln(f, s)
		_ = f.Close()
	}

	var sysPromptPath, sessionDir, resumePath string
	var flags []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--system-prompt":
			if i+1 < len(args) {
				sysPromptPath = args[i+1]
				i++
			}
		case "--session-dir":
			if i+1 < len(args) {
				sessionDir = args[i+1]
				i++
			}
		case "--session":
			if i+1 < len(args) {
				resumePath = args[i+1]
				i++
			}
		case "--mode":
			if i+1 < len(args) {
				flags = append(flags, "--mode="+args[i+1])
				i++
			}
		default:
			if strings.HasPrefix(args[i], "--no-") {
				flags = append(flags, args[i])
			}
		}
	}
	logLine("FLAGS:" + strings.Join(flags, ","))
	if sysPromptPath != "" {
		raw, _ := os.ReadFile(sysPromptPath)
		logLine("SYSPROMPT:" + strings.TrimSpace(string(raw)))
	}
	sessionFile := resumePath
	if sessionFile == "" {
		sessionFile = filepath.Join(sessionDir, "sess-1.jsonl")
		logLine("OPEN:new")
	} else {
		logLine("OPEN:resume")
	}

	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	turns := 0
	compacted := false
	compactAttempts := 0
	postStatsFails := 0
	for in.Scan() {
		var cmd struct {
			ID                 string `json:"id"`
			Type               string `json:"type"`
			Message            string `json:"message"`
			CustomInstructions string `json:"customInstructions"`
			Cancelled          bool   `json:"cancelled"`
		}
		_ = json.Unmarshal(in.Bytes(), &cmd)
		switch cmd.Type {
		case "get_state":
			_ = out.Encode(map[string]any{
				"id":      cmd.ID,
				"type":    "response",
				"command": "get_state",
				"success": true,
				"data": map[string]any{
					"sessionFile": sessionFile,
					"sessionId":   "sess-1",
				},
			})
		case "prompt":
			turns++
			compacted = false
			_ = os.MkdirAll(filepath.Dir(sessionFile), 0o700)
			_ = os.WriteFile(sessionFile, []byte("{\"type\":\"session\"}\n"), 0o600)
			logLine("PROMPT:" + strings.ReplaceAll(cmd.Message, "\n", "\\n"))
			_ = out.Encode(map[string]any{
				"id":      cmd.ID,
				"type":    "response",
				"command": "prompt",
				"success": true,
				"data":    map[string]any{"disposition": "started"},
			})
			_ = out.Encode(map[string]any{"type": "agent_start"})
			_ = out.Encode(map[string]any{"type": "tool_execution_start", "toolCallId": "tc-1", "toolName": "bash"})
			if mode == "ui_request" {
				_ = out.Encode(map[string]any{"type": "extension_ui_request", "id": "ui-1", "method": "confirm", "title": "confirm?"})
				// Wait until client cancels the UI request before finishing the turn.
				continue
			}
			if mode == "hang" {
				continue
			}
			_ = out.Encode(map[string]any{"type": "tool_execution_end", "toolCallId": "tc-1", "toolName": "bash", "isError": false})
			if mode == "auth_error" {
				_ = out.Encode(map[string]any{
					"type": "agent_end",
					"messages": []map[string]any{
						{"role": "assistant", "stopReason": "error", "errorMessage": "401 unauthorized: invalid api key"},
					},
					"willRetry": false,
				})
			} else {
				_ = out.Encode(map[string]any{"type": "agent_end", "messages": []any{}, "willRetry": false})
			}
			_ = out.Encode(map[string]any{"type": "agent_settled", "aborted": false})
		case "extension_ui_response":
			if cmd.ID == "ui-1" && cmd.Cancelled {
				logLine("UI_CANCELLED:ui-1")
				_ = out.Encode(map[string]any{"type": "tool_execution_end", "toolCallId": "tc-1", "toolName": "bash", "isError": false})
				_ = out.Encode(map[string]any{"type": "agent_end", "messages": []any{}, "willRetry": false})
				_ = out.Encode(map[string]any{"type": "agent_settled", "aborted": false})
			}
		case "abort":
			logLine("ABORT")
			_ = out.Encode(map[string]any{"id": cmd.ID, "type": "response", "command": "abort", "success": true})
			_ = out.Encode(map[string]any{"type": "agent_settled", "aborted": true})
		case "get_session_stats":
			if mode == "compact_post_stats_fail_then_ok" && compacted && postStatsFails == 0 {
				postStatsFails++
				_ = out.Encode(map[string]any{
					"id":      cmd.ID,
					"type":    "response",
					"command": "get_session_stats",
					"success": false,
					"error":   "temporary stats error",
				})
				continue
			}
			ctxTokens := 4000
			switch mode {
			case "over_budget", "over_budget_sticky":
				ctxTokens = 200_000
			case "over_budget_shrinks", "compact_refused_then_ok", "compact_fail_then_ok", "compact_timeout_then_ok", "compact_post_stats_fail_then_ok":
				if compacted {
					ctxTokens = 30_000
				} else {
					ctxTokens = 200_000
				}
			case "large_idle":
				ctxTokens = 80_000
			}
			_ = out.Encode(map[string]any{
				"id":      cmd.ID,
				"type":    "response",
				"command": "get_session_stats",
				"success": true,
				"data": map[string]any{
					"sessionFile": sessionFile,
					"toolCalls":   turns,
					"tokens": map[string]any{
						"input":     1000 * turns,
						"cacheRead": 500 * turns,
						"output":    200 * turns,
					},
					"contextUsage": map[string]any{
						"tokens":        ctxTokens,
						"contextWindow": 1_048_576,
					},
				},
			})
		case "compact":
			compactAttempts++
			logLine(fmt.Sprintf("COMPACT:%d:%s", compactAttempts, cmd.CustomInstructions))
			if mode == "compact_refused_then_ok" && compactAttempts == 1 {
				_ = out.Encode(map[string]any{
					"id":      cmd.ID,
					"type":    "response",
					"command": "compact",
					"success": false,
					"error":   "already compacting",
				})
				continue
			}
			if mode == "compact_fail_then_ok" && compactAttempts == 1 {
				_ = out.Encode(map[string]any{
					"id":      cmd.ID,
					"type":    "response",
					"command": "compact",
					"success": false,
					"error":   "provider 503 upstream failure",
				})
				continue
			}
			if mode == "compact_timeout_then_ok" && compactAttempts == 1 {
				time.Sleep(250 * time.Millisecond)
				_ = out.Encode(map[string]any{
					"id":      cmd.ID,
					"type":    "response",
					"command": "compact",
					"success": true,
					"data":    map[string]any{},
				})
				continue
			}
			compacted = true
			_ = out.Encode(map[string]any{"id": cmd.ID, "type": "response", "command": "compact", "success": true, "data": map[string]any{}})
		}
	}
	os.Exit(0)
}

func newFakeDriver(t *testing.T, mode string, cfg Config) (*Driver, driver.TurnInput, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "pi.log")
	cfg.Binary = os.Args[0]
	cfg.PrefixArgs = []string{"-test.run=^TestFakePiRPC$", "--"}
	if cfg.SessionDir == "" {
		cfg.SessionDir = filepath.Join(dir, "runtime", "pi", "sessions")
	}
	if cfg.SessionFile == "" {
		cfg.SessionFile = filepath.Join(dir, "pi-session.json")
	}
	if cfg.SystemPromptFile == "" {
		cfg.SystemPromptFile = filepath.Join(dir, "runtime", "pi", "SYSTEM.md")
	}
	in := driver.TurnInput{
		SystemPrompt:   "SUMI STANDING RULES",
		StartupContext: "MEMORY SNAPSHOT",
		UserPrompt:     "wake: #dev",
		WorkDir:        dir,
		Env:            append(os.Environ(), "SUMI_FAKE_PI="+mode, "SUMI_FAKE_PI_LOG="+logPath),
	}
	return New(cfg), in, logPath
}

func readLogLines(path string) []string {
	raw, _ := os.ReadFile(path)
	if len(raw) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestSystemPromptReplacesDefaultAndIsolationFlagsPassed(t *testing.T) {
	d, in, logPath := newFakeDriver(t, "ok", Config{Fingerprint: "fp1"})
	defer d.Close()

	var acts []driver.Activity
	in.OnActivity = func(a driver.Activity) { acts = append(acts, a) }

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res1, err := d.RunTurn(ctx, in)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if res1.InputTokens != 1500 || res1.OutputTokens != 200 || res1.ToolCalls != 1 {
		t.Fatalf("turn 1 stats: %+v", res1)
	}
	res2, err := d.RunTurn(ctx, in)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if res2.InputTokens != 1500 || res2.OutputTokens != 200 || res2.ToolCalls != 1 {
		t.Fatalf("turn 2 delta stats: %+v", res2)
	}

	lines := readLogLines(logPath)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"--mode=rpc",
		"--no-skills",
		"--no-prompt-templates",
		"--no-themes",
		"--no-context-files",
		"--no-mcp",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing flag %q in log:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "SYSPROMPT:SUMI STANDING RULES") {
		t.Fatalf("system prompt file not passed:\n%s", joined)
	}
	var prompts []string
	for _, l := range lines {
		if strings.HasPrefix(l, "PROMPT:") {
			prompts = append(prompts, strings.TrimPrefix(l, "PROMPT:"))
		}
	}
	if len(prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %v", prompts)
	}
	if strings.Contains(prompts[0], "SUMI STANDING RULES") {
		t.Fatalf("standing prompt must not be duplicated in user prompt: %q", prompts[0])
	}
	if !strings.Contains(prompts[0], "MEMORY SNAPSHOT") {
		t.Fatalf("first fresh turn must include StartupContext: %q", prompts[0])
	}
	if strings.Contains(prompts[1], "MEMORY SNAPSHOT") {
		t.Fatalf("second turn must not resend StartupContext: %q", prompts[1])
	}
	if len(acts) < 2 || acts[0] != (driver.Activity{ToolCallID: "tc-1", Tool: "bash", Done: false}) || acts[1] != (driver.Activity{ToolCallID: "tc-1", Tool: "bash", Done: true}) {
		t.Fatalf("unexpected activities: %+v", acts)
	}
}

func TestSessionResumeFingerprintAndReset(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "pi.log")
	baseCfg := Config{
		Binary:           os.Args[0],
		PrefixArgs:       []string{"-test.run=^TestFakePiRPC$", "--"},
		SessionDir:       filepath.Join(dir, "runtime", "pi", "sessions"),
		SessionFile:      filepath.Join(dir, "pi-session.json"),
		SystemPromptFile: filepath.Join(dir, "runtime", "pi", "SYSTEM.md"),
		Fingerprint:      "provider=pi;pi_provider=a;pi_model=m1",
	}
	in := driver.TurnInput{
		SystemPrompt:   "SUMI RULES",
		StartupContext: "MEM",
		UserPrompt:     "wake",
		WorkDir:        dir,
		Env:            append(os.Environ(), "SUMI_FAKE_PI=ok", "SUMI_FAKE_PI_LOG="+logPath),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Fresh session writes pi-session.json.
	d1 := New(baseCfg)
	if _, err := d1.RunTurn(ctx, in); err != nil {
		t.Fatalf("d1 turn: %v", err)
	}
	_ = d1.GracefulClose(2)

	// 2. Restarted driver with same fingerprint resumes saved session without re-sending StartupContext.
	_ = os.Remove(logPath)
	d2 := New(baseCfg)
	if _, err := d2.RunTurn(ctx, in); err != nil {
		t.Fatalf("d2 turn: %v", err)
	}
	_ = d2.Close()
	joined := strings.Join(readLogLines(logPath), "\n")
	if !strings.Contains(joined, "OPEN:resume") || strings.Contains(joined, "MEM") {
		t.Fatalf("expected resume without startup context:\n%s", joined)
	}

	// 3. Different fingerprint starts fresh session.
	_ = os.Remove(logPath)
	changedCfg := baseCfg
	changedCfg.Fingerprint = "provider=pi;pi_provider=b;pi_model=m2"
	d3 := New(changedCfg)
	if _, err := d3.RunTurn(ctx, in); err != nil {
		t.Fatalf("d3 turn: %v", err)
	}
	if err := d3.ResetSession(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	_ = d3.Close()
	joined = strings.Join(readLogLines(logPath), "\n")
	if !strings.Contains(joined, "OPEN:new") || !strings.Contains(joined, "MEM") {
		t.Fatalf("fingerprint change must start fresh:\n%s", joined)
	}

	// 4. After ResetSession, next turn starts fresh.
	_ = os.Remove(logPath)
	d4 := New(changedCfg)
	defer d4.Close()
	if _, err := d4.RunTurn(ctx, in); err != nil {
		t.Fatalf("d4 turn: %v", err)
	}
	joined = strings.Join(readLogLines(logPath), "\n")
	if !strings.Contains(joined, "OPEN:new") {
		t.Fatalf("expected fresh session after reset:\n%s", joined)
	}
}

func TestProactiveSoftCompactionOnLargeWindow(t *testing.T) {
	d, in, logPath := newFakeDriver(t, "over_budget", Config{
		Fingerprint:          "fp",
		SoftCompactionTokens: 150_000,
	})
	defer d.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := d.RunTurn(ctx, in); err != nil {
		t.Fatalf("turn: %v", err)
	}
	joined := strings.Join(readLogLines(logPath), "\n")
	if !strings.Contains(joined, "COMPACT:") || !strings.Contains(joined, "MEMORY.md") {
		t.Fatalf("expected proactive compaction when 200k > 150k soft budget:\n%s", joined)
	}

	// Below threshold must NOT compact.
	d2, in2, logPath2 := newFakeDriver(t, "ok", Config{
		Fingerprint:          "fp",
		SoftCompactionTokens: 150_000,
	})
	defer d2.Close()
	if _, err := d2.RunTurn(ctx, in2); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if strings.Contains(strings.Join(readLogLines(logPath2), "\n"), "COMPACT:") {
		t.Fatalf("must not compact when below threshold")
	}
}

func TestExtensionUIRequestAutoCancelled(t *testing.T) {
	d, in, logPath := newFakeDriver(t, "ui_request", Config{Fingerprint: "fp"})
	defer d.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := d.RunTurn(ctx, in); err != nil {
		t.Fatalf("turn with extension_ui_request: %v", err)
	}
	if !strings.Contains(strings.Join(readLogLines(logPath), "\n"), "UI_CANCELLED:ui-1") {
		t.Fatalf("expected extension_ui_request to be auto-cancelled")
	}
}

func TestAuthErrorAndCancelAbortAndCrash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Assistant auth error classification.
	dAuth, inAuth, _ := newFakeDriver(t, "auth_error", Config{Fingerprint: "fp"})
	defer dAuth.Close()
	_, err := dAuth.RunTurn(ctx, inAuth)
	var f *driver.Failure
	if !errors.As(err, &f) || f.Code != domain.AgentErrModelAuthFailed {
		t.Fatalf("want model_auth_failed, got %v", err)
	}

	// Start failure includes stderr tail.
	dCrash, inCrash, _ := newFakeDriver(t, "crash", Config{Fingerprint: "fp"})
	defer dCrash.Close()
	_, err = dCrash.RunTurn(ctx, inCrash)
	if !errors.As(err, &f) || f.Code != domain.AgentErrDriverStartFailed || !strings.Contains(err.Error(), "missing provider auth") {
		t.Fatalf("want driver_start_failed with stderr tail, got %v", err)
	}

	// Context cancellation sends abort and returns cleanly.
	dHang, inHang, logPath := newFakeDriver(t, "hang", Config{Fingerprint: "fp"})
	defer dHang.Close()
	turnCtx, turnCancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer turnCancel()
	_, err = dHang.RunTurn(turnCtx, inHang)
	if err == nil {
		t.Fatalf("expected cancellation error")
	}
	if !strings.Contains(strings.Join(readLogLines(logPath), "\n"), "ABORT") {
		t.Fatalf("expected abort RPC on turn cancel")
	}
}

func TestShouldProactiveCompactThresholdMath(t *testing.T) {
	if !shouldProactiveCompact(150_001, 1_048_576, 150_000) {
		t.Fatal("150,001 on 1M window must compact under 150k soft limit")
	}
	if shouldProactiveCompact(149_999, 1_048_576, 150_000) {
		t.Fatal("149,999 must not compact under 150k soft limit")
	}
	if !shouldProactiveCompact(97_000, 128_000, 150_000) {
		t.Fatal("97k on 128k window exceeds 75% ratio cap (96k) and must compact")
	}
	if shouldProactiveCompact(200_000, 1_048_576, 0) {
		t.Fatal("0 soft limit disables proactive compaction")
	}
}

func TestSystemPromptUpdateRestartsProcessWhileKeepingSession(t *testing.T) {
	d, in, logPath := newFakeDriver(t, "ok", Config{Fingerprint: "fp"})
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := d.RunTurn(ctx, in); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	_ = os.Remove(logPath)

	in.SystemPrompt = "UPDATED SUMI RULES"
	if _, err := d.RunTurn(ctx, in); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	joined := strings.Join(readLogLines(logPath), "\n")
	if !strings.Contains(joined, "SYSPROMPT:UPDATED SUMI RULES") {
		t.Fatalf("expected reloaded system prompt:\n%s", joined)
	}
	if !strings.Contains(joined, "OPEN:resume") {
		t.Fatalf("expected existing session to resume across system prompt reload:\n%s", joined)
	}
}

func TestCompactionArmingPreventsRetryLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	countCompacts := func(lines []string) int {
		n := 0
		for _, l := range lines {
			if strings.HasPrefix(l, "COMPACT:") {
				n++
			}
		}
		return n
	}

	// If compaction leaves context above threshold, arming disarms and does not retry on the next turn.
	dSticky, inSticky, logSticky := newFakeDriver(t, "over_budget_sticky", Config{
		Fingerprint:          "fp",
		SoftCompactionTokens: 150_000,
	})
	defer dSticky.Close()
	if _, err := dSticky.RunTurn(ctx, inSticky); err != nil {
		t.Fatalf("sticky turn 1: %v", err)
	}
	if _, err := dSticky.RunTurn(ctx, inSticky); err != nil {
		t.Fatalf("sticky turn 2: %v", err)
	}
	if got := countCompacts(readLogLines(logSticky)); got != 1 {
		t.Fatalf("expected exactly 1 compaction when context stays above threshold, got %d", got)
	}

	// When compaction drops context below threshold, arming re-arms so a later breach compacts again.
	dShrink, inShrink, logShrink := newFakeDriver(t, "over_budget_shrinks", Config{
		Fingerprint:          "fp",
		SoftCompactionTokens: 150_000,
	})
	defer dShrink.Close()
	if _, err := dShrink.RunTurn(ctx, inShrink); err != nil {
		t.Fatalf("shrink turn 1: %v", err)
	}
	if _, err := dShrink.RunTurn(ctx, inShrink); err != nil {
		t.Fatalf("shrink turn 2: %v", err)
	}
	if got := countCompacts(readLogLines(logShrink)); got != 2 {
		t.Fatalf("expected 2 compactions after shrinking re-armed, got %d", got)
	}
}

func TestClassifyCompactionErrorAndBackoff(t *testing.T) {
	if got := classifyCompactionError(nil); got != compactionCompleted {
		t.Fatalf("nil err = %v, want completed", got)
	}
	for _, msg := range []string{"already compacting", "Nothing to compact", "session busy", "agent is streaming"} {
		if got := classifyCompactionError(errors.New(msg)); got != compactionRefused {
			t.Fatalf("err %q = %v, want refused", msg, got)
		}
	}
	for _, err := range []error{context.DeadlineExceeded, context.Canceled, errors.New("provider 503"), errors.New("pi process ended")} {
		if got := classifyCompactionError(err); got != compactionFailed {
			t.Fatalf("err %v = %v, want failed", err, got)
		}
	}
	wantBackoff := []int{0, 1, 2, 4, 4, 4}
	for streak, want := range wantBackoff {
		if got := compactBackoffTurns(streak); got != want {
			t.Fatalf("compactBackoffTurns(%d) = %d, want %d", streak, got, want)
		}
	}
}

func TestCompactionRefusedFailedTimeoutAndStatsRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	countCompacts := func(lines []string) int {
		n := 0
		for _, l := range lines {
			if strings.HasPrefix(l, "COMPACT:") {
				n++
			}
		}
		return n
	}

	// 1. Refused keeps armed: turn 1 refused -> turn 2 retries immediately and succeeds.
	dRef, inRef, logRef := newFakeDriver(t, "compact_refused_then_ok", Config{
		Fingerprint:          "fp",
		SoftCompactionTokens: 150_000,
	})
	defer dRef.Close()
	if _, err := dRef.RunTurn(ctx, inRef); err != nil {
		t.Fatalf("refused turn 1: %v", err)
	}
	if got := countCompacts(readLogLines(logRef)); got != 1 {
		t.Fatalf("refused turn 1 compacts = %d, want 1", got)
	}
	if _, err := dRef.RunTurn(ctx, inRef); err != nil {
		t.Fatalf("refused turn 2: %v", err)
	}
	if got := countCompacts(readLogLines(logRef)); got != 2 {
		t.Fatalf("refused must keep armed for immediate retry on turn 2, got %d compacts", got)
	}

	// 2. Failed applies 1-turn backoff then re-arms: turn 1 fails, turn 2 skips, turn 3 retries and succeeds.
	dFail, inFail, logFail := newFakeDriver(t, "compact_fail_then_ok", Config{
		Fingerprint:          "fp",
		SoftCompactionTokens: 150_000,
	})
	defer dFail.Close()
	if _, err := dFail.RunTurn(ctx, inFail); err != nil {
		t.Fatalf("fail turn 1: %v", err)
	}
	if _, err := dFail.RunTurn(ctx, inFail); err != nil {
		t.Fatalf("fail turn 2: %v", err)
	}
	if got := countCompacts(readLogLines(logFail)); got != 1 {
		t.Fatalf("turn 2 after failure must back off, got %d compacts", got)
	}
	if _, err := dFail.RunTurn(ctx, inFail); err != nil {
		t.Fatalf("fail turn 3: %v", err)
	}
	if got := countCompacts(readLogLines(logFail)); got != 2 {
		t.Fatalf("turn 3 must re-arm and retry after backoff, got %d compacts", got)
	}

	// 3. Timeout behaves as failed (backs off 1 turn, then re-arms and succeeds).
	dTO, inTO, logTO := newFakeDriver(t, "compact_timeout_then_ok", Config{
		Fingerprint:          "fp",
		SoftCompactionTokens: 150_000,
		CompactTimeout:       60 * time.Millisecond,
	})
	defer dTO.Close()
	if _, err := dTO.RunTurn(ctx, inTO); err != nil {
		t.Fatalf("timeout turn 1: %v", err)
	}
	if _, err := dTO.RunTurn(ctx, inTO); err != nil {
		t.Fatalf("timeout turn 2: %v", err)
	}
	if got := countCompacts(readLogLines(logTO)); got != 1 {
		t.Fatalf("turn 2 after timeout must back off, got %d compacts", got)
	}
	if _, err := dTO.RunTurn(ctx, inTO); err != nil {
		t.Fatalf("timeout turn 3: %v", err)
	}
	if got := countCompacts(readLogLines(logTO)); got != 2 {
		t.Fatalf("turn 3 after timeout must re-arm and retry, got %d compacts", got)
	}

	// 4. Post-compaction stats read failure does not permanently lock out arming.
	dStats, inStats, logStats := newFakeDriver(t, "compact_post_stats_fail_then_ok", Config{
		Fingerprint:          "fp",
		SoftCompactionTokens: 150_000,
	})
	defer dStats.Close()
	if _, err := dStats.RunTurn(ctx, inStats); err != nil {
		t.Fatalf("stats-fail turn 1: %v", err)
	}
	if _, err := dStats.RunTurn(ctx, inStats); err != nil {
		t.Fatalf("stats-fail turn 2: %v", err)
	}
	if got := countCompacts(readLogLines(logStats)); got != 1 {
		t.Fatalf("turn 2 after post-stats error must skip once, got %d compacts", got)
	}
	if _, err := dStats.RunTurn(ctx, inStats); err != nil {
		t.Fatalf("stats-fail turn 3: %v", err)
	}
	if got := countCompacts(readLogLines(logStats)); got != 2 {
		t.Fatalf("turn 3 after post-stats error must re-arm and retry, got %d compacts", got)
	}
}

func TestColdWakeRecycleOnLiveProcessAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cur := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return cur }

	dir := t.TempDir()
	logPath := filepath.Join(dir, "pi.log")
	baseCfg := Config{
		Binary:               os.Args[0],
		PrefixArgs:           []string{"-test.run=^TestFakePiRPC$", "--"},
		SessionDir:           filepath.Join(dir, "runtime", "pi", "sessions"),
		SessionFile:          filepath.Join(dir, "pi-session.json"),
		SystemPromptFile:     filepath.Join(dir, "runtime", "pi", "SYSTEM.md"),
		Fingerprint:          "fp",
		ColdRecycleIdle:      60 * time.Minute,
		ColdRecycleMinTokens: 64_000,
		Now:                  clock,
	}
	in := driver.TurnInput{
		SystemPrompt:   "SUMI RULES",
		StartupContext: "MEMORY SNAPSHOT",
		UserPrompt:     "wake",
		WorkDir:        dir,
		Env:            append(os.Environ(), "SUMI_FAKE_PI=large_idle", "SUMI_FAKE_PI_LOG="+logPath),
	}

	d := New(baseCfg)
	if _, err := d.RunTurn(ctx, in); err != nil {
		t.Fatalf("turn 1: %v", err)
	}

	// Idle 30m (< 60m): stay on the warm session without re-sending StartupContext.
	_ = os.Remove(logPath)
	cur = cur.Add(30 * time.Minute)
	if _, err := d.RunTurn(ctx, in); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	joined := strings.Join(readLogLines(logPath), "\n")
	if strings.Contains(joined, "OPEN:new") || strings.Contains(joined, "MEMORY SNAPSHOT") {
		t.Fatalf("short idle must keep warm session:\n%s", joined)
	}

	// Idle 61m (>= 60m) with 80k tokens (> 64k): recycle live process into fresh session with MEMORY.
	_ = os.Remove(logPath)
	cur = cur.Add(61 * time.Minute)
	if _, err := d.RunTurn(ctx, in); err != nil {
		t.Fatalf("turn 3: %v", err)
	}
	joined = strings.Join(readLogLines(logPath), "\n")
	if !strings.Contains(joined, "OPEN:new") || !strings.Contains(joined, "MEMORY SNAPSHOT") {
		t.Fatalf("cold idle on live process must recycle into fresh session:\n%s", joined)
	}
	_ = d.GracefulClose(2)

	// Restarted driver after another 61m idle with 80k tokens on disk also recycles instead of resuming.
	_ = os.Remove(logPath)
	cur = cur.Add(61 * time.Minute)
	d2 := New(baseCfg)
	defer d2.Close()
	if _, err := d2.RunTurn(ctx, in); err != nil {
		t.Fatalf("restarted turn: %v", err)
	}
	joined = strings.Join(readLogLines(logPath), "\n")
	if !strings.Contains(joined, "OPEN:new") || !strings.Contains(joined, "MEMORY SNAPSHOT") {
		t.Fatalf("cold idle across restart must recycle into fresh session:\n%s", joined)
	}

	// Small session (4k <= 64k) idle for 2h must NOT recycle.
	dSmall, inSmall, logSmall := newFakeDriver(t, "ok", Config{
		Fingerprint:          "fp",
		ColdRecycleIdle:      60 * time.Minute,
		ColdRecycleMinTokens: 64_000,
		Now:                  clock,
	})
	defer dSmall.Close()
	if _, err := dSmall.RunTurn(ctx, inSmall); err != nil {
		t.Fatalf("small turn 1: %v", err)
	}
	_ = os.Remove(logSmall)
	cur = cur.Add(2 * time.Hour)
	if _, err := dSmall.RunTurn(ctx, inSmall); err != nil {
		t.Fatalf("small turn 2: %v", err)
	}
	joined = strings.Join(readLogLines(logSmall), "\n")
	if strings.Contains(joined, "OPEN:new") || strings.Contains(joined, "MEMORY SNAPSHOT") {
		t.Fatalf("small session must keep continuity even after long idle:\n%s", joined)
	}
}
