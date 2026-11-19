package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
)

// TestFakeACPAgent is not a real test: it is re-executed as the ACP child process.
func TestFakeACPAgent(t *testing.T) {
	mode := os.Getenv("SUMI_FAKE_ACP")
	if mode == "" {
		t.Skip("helper process")
	}
	if mode == "crash" {
		fmt.Fprintln(os.Stderr, "booting")
		fmt.Fprintln(os.Stderr, "fatal: no provider credentials")
		os.Exit(3)
	}
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	logMethod := func(m string) {
		if path := os.Getenv("SUMI_FAKE_ACP_LOG"); path != "" && m != "" {
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			fmt.Fprintln(f, m)
			_ = f.Close()
		}
	}
	var promptID json.RawMessage
	for in.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			} `json:"params"`
			Result struct {
				Outcome struct {
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			} `json:"result"`
		}
		_ = json.Unmarshal(in.Bytes(), &msg)
		logMethod(msg.Method)
		switch msg.Method {
		case "initialize":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"protocolVersion":   1,
				"agentCapabilities": map[string]any{"loadSession": mode == "resume"},
			}})
		case "session/load":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
		case "session/new":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"sessionId": "sess-1"}})
		case "session/prompt":
			promptID = msg.ID
			if len(msg.Params.Prompt) > 0 && strings.HasPrefix(msg.Params.Prompt[0].Text, "SYS") {
				logMethod("prompt-with-system")
			}
			for i := 0; i < 500; i++ {
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "sess-1", "n": i}})
			}
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": "perm-1", "method": "session/request_permission", "params": map[string]any{
				"options": []map[string]any{{"optionId": "reject", "kind": "reject_once"}, {"optionId": "ok-once", "kind": "allow_once"}},
			}})
		case "":
			// Response to our permission request.
			if msg.Result.Outcome.OptionID == "ok-once" {
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": promptID, "result": map[string]any{"stopReason": "end_turn"}})
			} else {
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": promptID, "error": map[string]any{"code": 1, "message": "permission not granted"}})
			}
		}
	}
	os.Exit(0)
}

func fakeDriver(mode string) (*Driver, driver.TurnInput) {
	return fakeDriverWith(Config{}, mode)
}

func fakeDriverWith(cfg Config, mode string) (*Driver, driver.TurnInput) {
	cfg.Command = []string{os.Args[0], "-test.run=^TestFakeACPAgent$"}
	d := New(cfg)
	in := driver.TurnInput{
		UserPrompt: "hello",
		WorkDir:    os.TempDir(),
		Env:        append(os.Environ(), "SUMI_FAKE_ACP="+mode),
	}
	return d, in
}

func TestRunTurnHandlesNotificationsAndPermissions(t *testing.T) {
	d, in := fakeDriver("ok")
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		if _, err := d.RunTurn(ctx, in); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
}

func TestStartFailureReportsStderrTail(t *testing.T) {
	d, in := fakeDriver("crash")
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := d.RunTurn(ctx, in)
	var f *driver.Failure
	if !errors.As(err, &f) || f.Code != domain.AgentErrDriverStartFailed {
		t.Fatalf("want driver_start_failed, got %v", err)
	}
	if !strings.Contains(err.Error(), "fatal: no provider credentials") {
		t.Fatalf("detail must include stderr tail: %v", err)
	}
}

func TestMissingCommandIsStartFailure(t *testing.T) {
	d := New(Config{Command: []string{"/nonexistent/acp-binary"}})
	_, err := d.RunTurn(context.Background(), driver.TurnInput{WorkDir: os.TempDir()})
	var f *driver.Failure
	if !errors.As(err, &f) || f.Code != domain.AgentErrDriverStartFailed {
		t.Fatalf("want driver_start_failed, got %v", err)
	}
}

func TestCloseInterruptsInFlightTurn(t *testing.T) {
	d := New(Config{Command: []string{"sh", "-c", "read line; sleep 60"}})
	done := make(chan error, 1)
	go func() {
		_, err := d.RunTurn(context.Background(), driver.TurnInput{WorkDir: os.TempDir(), Env: os.Environ()})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	_ = d.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected error after close")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("close did not interrupt the turn")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("close too slow")
	}
}

func TestSessionResumesAcrossDriversAndResetForgetsIt(t *testing.T) {
	dir := t.TempDir()
	logPath := dir + "/methods.log"
	cfg := Config{SessionFile: dir + "/acp-session.json"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func() {
		d, in := fakeDriverWith(cfg, "resume")
		in.Env = append(in.Env, "SUMI_FAKE_ACP_LOG="+logPath)
		in.SystemPrompt, in.StartupContext = "SYS rules", "memory"
		defer d.Close()
		for i := 0; i < 2; i++ {
			if _, err := d.RunTurn(ctx, in); err != nil {
				t.Fatalf("turn: %v", err)
			}
		}
	}
	methods := func() []string {
		raw, _ := os.ReadFile(logPath)
		_ = os.Remove(logPath)
		var out []string
		for _, m := range strings.Fields(string(raw)) {
			if m == "session/new" || m == "session/load" || m == "prompt-with-system" {
				out = append(out, m)
			}
		}
		return out
	}

	// Standing instructions open a fresh session once; later turns and resumed sessions omit them.
	run()
	if got := strings.Join(methods(), ","); got != "session/new,prompt-with-system" {
		t.Fatalf("first driver: %v", got)
	}
	run() // A new driver (daemon restart) resumes the persisted session.
	if got := strings.Join(methods(), ","); got != "session/load" {
		t.Fatalf("restarted driver must resume without re-sending instructions: %v", got)
	}

	d, _ := fakeDriverWith(cfg, "resume")
	if err := d.ResetSession(); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	run()
	if got := strings.Join(methods(), ","); got != "session/new,prompt-with-system" {
		t.Fatalf("reset must start a new session: %v", got)
	}

	// A different command never resumes another agent's session.
	other := New(Config{SessionFile: cfg.SessionFile, Command: []string{"other-agent"}})
	if id := other.loadSavedSession([]string{"other-agent"}); id != "" {
		t.Fatalf("session leaked across commands: %q", id)
	}
}
