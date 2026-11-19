package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abcdlsj/sumi/internal/driver"
)

type Config struct {
	Provider string   `json:"provider"` // "codex" | "claude" | "pi"
	Command  []string `json:"command,omitempty"`
}

func (c Config) resolvedCommand() ([]string, error) {
	if len(c.Command) > 0 {
		return c.Command, nil
	}
	switch c.Provider {
	case "codex":
		return []string{"npx", "-y", "@zed-industries/codex-acp@0.16.0"}, nil
	case "claude":
		return []string{"npx", "-y", "@zed-industries/claude-code-acp@0.16.2"}, nil
	case "pi":
		return []string{"npx", "-y", "pi-acp@0.0.34"}, nil
	default:
		return nil, fmt.Errorf("unsupported acp provider %q", c.Provider)
	}
}

// Driver manages a single persistent ACP stdio JSON-RPC child process and one active sessionId.
type Driver struct {
	mu        sync.Mutex
	cfg       Config
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Scanner
	sessionID string
	reqID     atomic.Int64
}

func New(cfg Config) *Driver {
	return &Driver{cfg: cfg}
}

func (d *Driver) ResetSession() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sessionID = ""
	return nil
}

func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopLocked()
}

func (d *Driver) stopLocked() error {
	d.sessionID = ""
	if d.stdin != nil {
		_ = d.stdin.Close()
		d.stdin = nil
	}
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
		_ = d.cmd.Wait()
	}
	d.cmd = nil
	d.stdout = nil
	return nil
}

func (d *Driver) ensureStartedLocked(ctx context.Context, workDir string, env []string) error {
	if d.cmd != nil && d.cmd.Process != nil {
		return nil
	}
	argv, err := d.cfg.resolvedCommand()
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = workDir
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start acp process: %w", err)
	}
	d.cmd = cmd
	d.stdin = stdin
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	d.stdout = scanner

	initCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if _, err := d.rpcLocked(initCtx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "sumi", "version": "2.0.0"},
	}); err != nil {
		_ = d.stopLocked()
		return fmt.Errorf("acp initialize: %w", err)
	}
	return nil
}

func (d *Driver) RunTurn(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ensureStartedLocked(ctx, in.WorkDir, in.Env); err != nil {
		return driver.TurnResult{}, err
	}
	if d.sessionID == "" {
		sessCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		res, err := d.rpcLocked(sessCtx, "session/new", map[string]any{
			"cwd":        in.WorkDir,
			"mcpServers": []any{},
		})
		cancel()
		if err != nil {
			_ = d.stopLocked()
			return driver.TurnResult{}, fmt.Errorf("acp session/new: %w", err)
		}
		var out struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(res, &out); err != nil || out.SessionID == "" {
			return driver.TurnResult{}, errors.New("acp session/new missing sessionId")
		}
		d.sessionID = out.SessionID
	}

	promptText := in.UserPrompt
	if in.SystemPrompt != "" {
		promptText = in.SystemPrompt + "\n\n" + in.UserPrompt
	}
	res, err := d.rpcLocked(ctx, "session/prompt", map[string]any{
		"sessionId": d.sessionID,
		"prompt": []map[string]any{
			{"type": "text", "text": promptText},
		},
	})
	if err != nil {
		return driver.TurnResult{}, err
	}
	var stop struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(res, &stop)
	if stop.StopReason != "" && stop.StopReason != "end_turn" {
		return driver.TurnResult{}, fmt.Errorf("acp prompt stopped with %s", stop.StopReason)
	}
	return driver.TurnResult{}, nil
}

func (d *Driver) rpcLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := d.reqID.Add(1)
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if _, err := d.stdin.Write(raw); err != nil {
		return nil, err
	}

	type lineRes struct {
		line []byte
		ok   bool
		err  error
	}
	for {
		ch := make(chan lineRes, 1)
		go func() {
			ok := d.stdout.Scan()
			var b []byte
			if ok {
				b = append([]byte(nil), d.stdout.Bytes()...)
			}
			ch <- lineRes{line: b, ok: ok, err: d.stdout.Err()}
		}()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-ch:
			if !r.ok {
				if r.err != nil {
					return nil, r.err
				}
				return nil, io.EOF
			}
			var msg struct {
				ID     *int64          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(r.line, &msg); err != nil {
				continue
			}
			// Auto-approve permission requests inside full-access agent sandbox.
			if msg.Method == "session/request_permission" && msg.ID != nil {
				reply, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      *msg.ID,
					"result":  map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow_once"}},
				})
				_, _ = d.stdin.Write(append(reply, '\n'))
				continue
			}
			if msg.ID != nil && *msg.ID == id {
				if msg.Error != nil {
					return nil, fmt.Errorf("acp rpc %s error %d: %s", method, msg.Error.Code, msg.Error.Message)
				}
				return msg.Result, nil
			}
		}
	}
}
