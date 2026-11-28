package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
)

// TestFakeClaude is re-executed as a fake `claude -p` stream-json subprocess by unit tests.
func TestFakeClaude(t *testing.T) {
	mode := os.Getenv("SUMI_FAKE_CLAUDE")
	if mode == "" {
		t.Skip("helper process")
	}
	logPath := os.Getenv("SUMI_FAKE_CLAUDE_LOG")
	logLine := func(s string) {
		f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		_, _ = fmt.Fprintln(f, s)
		_ = f.Close()
	}
	args := os.Args[1:]
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	logLine("ARGS:" + strings.Join(args, "\x1f"))
	logLine("WINDOW:" + os.Getenv(ContextWindowEnv))
	session, resumed := "sess-new", false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--resume" {
			session, resumed = args[i+1], true
		}
	}
	out := json.NewEncoder(os.Stdout)
	emit := func(v map[string]any) { _ = out.Encode(v) }
	result := func(isErr bool, subtype, text string) {
		emit(map[string]any{"type": "result", "subtype": subtype, "is_error": isErr, "result": text, "session_id": session,
			"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 100, "cache_creation_input_tokens": 5, "output_tokens": 7}})
	}
	if resumed && mode == "stale-resume" {
		in := bufio.NewScanner(os.Stdin)
		in.Scan()
		result(true, "error_during_execution", "No conversation found with session ID: "+session)
		os.Exit(1)
	}
	envInt := func(k string, def int) int {
		if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
			return v
		}
		return def
	}
	tokens := envInt("SUMI_FAKE_CLAUDE_TOKENS", 1000)
	window := envInt("SUMI_FAKE_CLAUDE_WINDOW", 200_000)
	tools := envInt("SUMI_FAKE_CLAUDE_TOOLS", 1)

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<24)
	inSteerTurn := false
	for in.Scan() {
		var msg struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
			Message struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(in.Bytes(), &msg) != nil {
			continue
		}
		switch msg.Type {
		case "control_request":
			logLine("CONTROL:" + msg.Request.Subtype)
			var resp any = map[string]any{}
			if msg.Request.Subtype == "get_context_usage" {
				resp = map[string]any{"totalTokens": tokens, "maxTokens": window}
			}
			emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": msg.RequestID, "response": resp}})
			if msg.Request.Subtype == "interrupt" {
				result(true, "error_during_execution", "interrupted")
			}
		case "user":
			text := msg.Message.Content[0].Text
			if inSteerTurn {
				logLine("STEER:" + strings.ReplaceAll(text, "\n", "\\n"))
				inSteerTurn = false
				result(false, "success", "steered")
				continue
			}
			logLine("PROMPT:" + strings.ReplaceAll(text, "\n", "\\n"))
			if strings.HasPrefix(text, "/compact ") {
				emit(map[string]any{"type": "system", "subtype": "status", "status": "compacting"})
				emit(map[string]any{"type": "system", "subtype": "compact_boundary"})
				tokens = envInt("SUMI_FAKE_CLAUDE_POST_TOKENS", 500)
				result(false, "success", "")
				continue
			}
			if mode == "auth" {
				result(true, "success", "Invalid API key · Please run /login")
				continue
			}
			emit(map[string]any{"type": "system", "subtype": "init", "session_id": session})
			for i := 0; i < tools; i++ {
				id := fmt.Sprintf("tu-%d", i)
				emit(map[string]any{"type": "assistant", "parent_tool_use_id": nil, "message": map[string]any{
					"id": fmt.Sprintf("msg-%d", i), "content": []map[string]any{{"type": "tool_use", "id": id, "name": "Bash"}}}})
				emit(map[string]any{"type": "user", "message": map[string]any{
					"content": []map[string]any{{"type": "tool_result", "tool_use_id": id}}}})
			}
			if mode == "steer_wait" {
				inSteerTurn = true
				continue
			}
			if mode == "hang" {
				continue // wait for interrupt
			}
			emit(map[string]any{"type": "assistant", "message": map[string]any{"id": "msg-final", "content": []map[string]any{{"type": "text", "text": "done"}}}})
			result(false, "success", "done")
		}
	}
}

func newFakeDriver(t *testing.T, mode string, cfg Config, env ...string) (*Driver, driver.TurnInput, string) {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "fake.log")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Binary = exe
	cfg.PrefixArgs = []string{"-test.run=^TestFakeClaude$", "--"}
	if cfg.SessionFile == "" {
		cfg.SessionFile = filepath.Join(dir, "claude-session.json")
	}
	in := driver.TurnInput{
		SystemPrompt:   "You are a Sumi agent.",
		StartupContext: "MEMORY",
		UserPrompt:     "wake",
		WorkDir:        work,
		Env: append(append(os.Environ(),
			"SUMI_FAKE_CLAUDE="+mode,
			"SUMI_FAKE_CLAUDE_LOG="+logPath,
			ContextWindowEnv+"=600000",
		), env...),
	}
	d := New(cfg)
	t.Cleanup(func() { _ = d.Close() })
	return d, in, logPath
}

func logLines(t *testing.T, path, prefix string) []string {
	t.Helper()
	raw, _ := os.ReadFile(path)
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, strings.TrimPrefix(l, prefix))
		}
	}
	return out
}

func TestTurnUsesSystemPromptSlotIsolationFlagsAndContextWindow(t *testing.T) {
	d, in, logPath := newFakeDriver(t, "ok", Config{ContextWindow: 300_000})
	var tools []driver.Activity
	in.OnActivity = func(a driver.Activity) {
		if a.ToolCallID != "" {
			tools = append(tools, a)
		}
	}
	res, err := d.RunTurn(context.Background(), in)
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if res.InputTokens != 115 || res.OutputTokens != 7 || res.ToolCalls != 1 {
		t.Fatalf("result=%+v", res)
	}
	if len(tools) != 2 || tools[0].Tool != "Bash" || tools[0].Done || !tools[1].Done {
		t.Fatalf("activity=%+v", tools)
	}
	args := strings.Split(logLines(t, logPath, "ARGS:")[0], "\x1f")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-p", "--input-format stream-json", "--output-format stream-json", "--strict-mcp-config",
		"--permission-mode bypassPermissions", "--system-prompt-snapshot off", "--disallowed-tools " + strings.Join(DisallowedTools, ",")} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
	for i, a := range args {
		if a == "--setting-sources" && (i+1 >= len(args) || args[i+1] != "") {
			t.Fatalf("setting sources must be empty: %v", args)
		}
		if a == "--system-prompt-file" {
			raw, _ := os.ReadFile(args[i+1])
			if string(raw) != in.SystemPrompt {
				t.Fatalf("system prompt file=%q", raw)
			}
		}
	}
	if strings.Contains(joined, "--resume") {
		t.Fatalf("fresh session must not resume: %v", args)
	}
	// The per-agent window replaces an inherited value.
	if w := logLines(t, logPath, "WINDOW:"); w[0] != "300000" {
		t.Fatalf("window env=%v", w)
	}
	if p := logLines(t, logPath, "PROMPT:"); p[0] != `MEMORY\n\nwake` {
		t.Fatalf("first prompt must carry startup context, got %q", p[0])
	}
	if _, err := d.RunTurn(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if p := logLines(t, logPath, "PROMPT:"); p[len(p)-1] != "wake" {
		t.Fatalf("second prompt=%q", p[len(p)-1])
	}
	if n := len(logLines(t, logPath, "ARGS:")); n != 1 {
		t.Fatalf("process must persist across turns, started %d", n)
	}
}

func TestResumeAcrossRestartAndStaleResumeFallsBackFresh(t *testing.T) {
	d, in, logPath := newFakeDriver(t, "stale-resume", Config{Fingerprint: "fp1"})
	if _, err := d.RunTurn(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	d2 := New(d.cfg)
	t.Cleanup(func() { _ = d2.Close() })
	if _, err := d2.RunTurn(context.Background(), in); err != nil {
		t.Fatalf("stale resume must retry fresh: %v", err)
	}
	args := logLines(t, logPath, "ARGS:")
	if len(args) != 3 || !strings.Contains(args[1], "--resume\x1fsess-new") || strings.Contains(args[2], "--resume") {
		t.Fatalf("launches=%q", args)
	}
	if p := logLines(t, logPath, "PROMPT:"); p[len(p)-1] != `MEMORY\n\nwake` {
		t.Fatalf("fresh retry must carry startup context, got %q", p[len(p)-1])
	}

	// A fingerprint change never resumes.
	_ = d2.Close()
	cfg := d.cfg
	cfg.Fingerprint = "fp2"
	d3 := New(cfg)
	t.Cleanup(func() { _ = d3.Close() })
	if _, err := d3.RunTurn(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if args := logLines(t, logPath, "ARGS:"); strings.Contains(args[len(args)-1], "--resume") {
		t.Fatalf("changed fingerprint resumed: %q", args[len(args)-1])
	}
}

func TestProactiveCompactionAndHardCeilingRecycle(t *testing.T) {
	d, in, logPath := newFakeDriver(t, "ok", Config{}, "SUMI_FAKE_CLAUDE_TOKENS=180000", "SUMI_FAKE_CLAUDE_WINDOW=1000000")
	if _, err := d.RunTurn(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	prompts := logLines(t, logPath, "PROMPT:")
	if len(prompts) != 2 || !strings.HasPrefix(prompts[1], "/compact ") {
		t.Fatalf("prompts=%q", prompts)
	}
	if d.lastContextTokens != 500 || d.lastContextWindow != 1_000_000 || !d.compactArmed {
		t.Fatalf("post compaction state tokens=%d window=%d armed=%v", d.lastContextTokens, d.lastContextWindow, d.compactArmed)
	}

	// Post-compaction context above min(400K, 85% window) recycles at turn end.
	d2, in2, _ := newFakeDriver(t, "ok", Config{}, "SUMI_FAKE_CLAUDE_TOKENS=180000", "SUMI_FAKE_CLAUDE_POST_TOKENS=190000", "SUMI_FAKE_CLAUDE_WINDOW=200000")
	if _, err := d2.RunTurn(context.Background(), in2); err != nil {
		t.Fatal(err)
	}
	if d2.proc != nil || d2.sessionID != "" {
		t.Fatalf("hard ceiling must recycle the session")
	}
	if _, err := os.Stat(d2.cfg.SessionFile); !os.IsNotExist(err) {
		t.Fatalf("recycled session file must be removed: %v", err)
	}
}

func TestToolCallBudgetInterruptsTurn(t *testing.T) {
	d, in, logPath := newFakeDriver(t, "hang", Config{MaxTurnToolCalls: 3}, "SUMI_FAKE_CLAUDE_TOOLS=5")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := d.RunTurn(ctx, in)
	var f *driver.Failure
	if !errors.As(err, &f) || f.Code != domain.AgentErrTurnBudgetExceeded {
		t.Fatalf("want turn_budget_exceeded, got %v", err)
	}
	controls := strings.Join(logLines(t, logPath, "CONTROL:"), ",")
	if !strings.HasPrefix(controls, "interrupt") || !strings.Contains(controls, "get_context_usage") {
		t.Fatalf("controls=%s", controls)
	}
}

func TestAuthErrorClassified(t *testing.T) {
	d, in, _ := newFakeDriver(t, "auth", Config{})
	_, err := d.RunTurn(context.Background(), in)
	var f *driver.Failure
	if !errors.As(err, &f) || f.Code != domain.AgentErrModelAuthFailed {
		t.Fatalf("want model_auth_failed, got %v", err)
	}
}

func TestThresholdMath(t *testing.T) {
	if got := compactionThreshold(1_000_000, DefaultSoftCompactionTokens); got != 150_000 {
		t.Fatalf("soft 1M=%d", got)
	}
	if got := compactionThreshold(100_000, DefaultSoftCompactionTokens); got != 75_000 {
		t.Fatalf("soft 100k=%d", got)
	}
	if !shouldHardRecycle(171_000, 200_000, DefaultHardRecycleTokens) || shouldHardRecycle(399_000, 1_000_000, DefaultHardRecycleTokens) {
		t.Fatal("hard ceiling math")
	}
	if compactBackoffTurns(1) != 1 || compactBackoffTurns(2) != 2 || compactBackoffTurns(9) != 4 {
		t.Fatal("backoff")
	}
}

func TestStartNamesMissingWorkDir(t *testing.T) {
	d, in, _ := newFakeDriver(t, "ok", Config{})
	in.WorkDir = filepath.Join(t.TempDir(), "gone")
	_, err := d.RunTurn(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "work dir "+in.WorkDir+" does not exist") {
		t.Fatalf("err=%v", err)
	}
}

func TestSteerInjectsMidTurnAndErrorsWhenIdle(t *testing.T) {
	d, in, logPath := newFakeDriver(t, "steer_wait", Config{})
	if err := d.Steer(context.Background(), "too early"); !errors.Is(err, driver.ErrTurnNotActive) {
		t.Fatalf("expected ErrTurnNotActive when idle, got %v", err)
	}

	ready := make(chan struct{}, 1)
	in.OnActivity = func(a driver.Activity) {
		if a.Tool == "Bash" && !a.Done {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := d.RunTurn(context.Background(), in)
		errCh <- err
	}()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake claude turn to emit model activity")
	}

	if err := d.Steer(context.Background(), "[Sumi inbox notice]\n- #ops (unread=1)"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if steers := logLines(t, logPath, "STEER:"); len(steers) != 1 || !strings.Contains(steers[0], "#ops") {
		t.Fatalf("expected steer logged, got %q", steers)
	}
}
