package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/sumi/internal/driver"
)

type Config struct {
	BaseURL                string  `json:"base_url"`
	APIKey                 string  `json:"api_key"`
	Model                  string  `json:"model"`
	ContextWindowTokens    int     `json:"context_window_tokens"`
	CompactionTriggerRatio float64 `json:"compaction_trigger_ratio"`
	KeepRecentMessages     int     `json:"keep_recent_messages"`
	MaxStepsPerTurn        int     `json:"max_steps_per_turn"`
	SessionFile            string  `json:"-"`
}

func (c *Config) normalize() {
	if c.BaseURL == "" {
		c.BaseURL = os.Getenv("OPENAI_BASE_URL")
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.openai.com/v1"
	}
	if c.APIKey == "" {
		c.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	if c.Model == "" {
		c.Model = "gpt-4o-mini"
	}
	if c.ContextWindowTokens <= 0 {
		c.ContextWindowTokens = 128000
	}
	if c.CompactionTriggerRatio <= 0 || c.CompactionTriggerRatio >= 1 {
		c.CompactionTriggerRatio = 0.78
	}
	if c.KeepRecentMessages <= 0 {
		c.KeepRecentMessages = 16
	}
	if c.MaxStepsPerTurn <= 0 {
		c.MaxStepsPerTurn = 24
	}
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type CompletionResponse struct {
	Message      Message
	InputTokens  int
	OutputTokens int
}

// Provider abstracts the LLM chat-completions endpoint for production HTTP and deterministic testing.
type Provider interface {
	Complete(ctx context.Context, model string, messages []Message, tools []map[string]any) (CompletionResponse, error)
}

type Harness struct {
	mu       sync.Mutex
	cfg      Config
	provider Provider
	history  []Message
}

func New(cfg Config, provider Provider) (*Harness, error) {
	cfg.normalize()
	if provider == nil {
		provider = &openAIProvider{
			baseURL: strings.TrimRight(cfg.BaseURL, "/"),
			apiKey:  cfg.APIKey,
			client:  &http.Client{Timeout: 120 * time.Second},
		}
	}
	h := &Harness{
		cfg:      cfg,
		provider: provider,
	}
	if cfg.SessionFile != "" {
		_ = h.loadSession()
	}
	return h, nil
}

func (h *Harness) ResetSession() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.history = nil
	if h.cfg.SessionFile != "" {
		if err := os.Remove(h.cfg.SessionFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (h *Harness) Close() error { return nil }

func (h *Harness) RunTurn(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.history = append(h.history, Message{Role: "user", Content: in.UserPrompt})
	var totalIn, totalOut, totalTools int
	tools := builtinToolsSpec()

	for step := 0; step < h.cfg.MaxStepsPerTurn; step++ {
		if h.needsCompaction(in.SystemPrompt) {
			h.compactHistory()
		}

		reqMsgs := make([]Message, 0, len(h.history)+1)
		if in.SystemPrompt != "" {
			reqMsgs = append(reqMsgs, Message{Role: "system", Content: in.SystemPrompt})
		}
		reqMsgs = append(reqMsgs, h.history...)

		resp, err := h.completeWithRetry(ctx, reqMsgs, tools)
		if err != nil {
			return driver.TurnResult{InputTokens: totalIn, OutputTokens: totalOut, ToolCalls: totalTools}, err
		}
		totalIn += resp.InputTokens
		totalOut += resp.OutputTokens
		h.history = append(h.history, resp.Message)
		_ = h.saveSession()

		if len(resp.Message.ToolCalls) == 0 {
			return driver.TurnResult{
				FinalText:    resp.Message.Content,
				InputTokens:  totalIn,
				OutputTokens: totalOut,
				ToolCalls:    totalTools,
			}, nil
		}

		for _, tc := range resp.Message.ToolCalls {
			totalTools++
			out := executeTool(ctx, tc, in)
			h.history = append(h.history, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    out,
			})
		}
		_ = h.saveSession()
	}

	return driver.TurnResult{
		InputTokens:  totalIn,
		OutputTokens: totalOut,
		ToolCalls:    totalTools,
	}, fmt.Errorf("builtin harness reached max_steps_per_turn (%d)", h.cfg.MaxStepsPerTurn)
}

func (h *Harness) completeWithRetry(ctx context.Context, msgs []Message, tools []map[string]any) (CompletionResponse, error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := h.provider.Complete(ctx, h.cfg.Model, msgs, tools)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryableErr(err) || attempt == 2 {
			return CompletionResponse{}, err
		}
		select {
		case <-ctx.Done():
			return CompletionResponse{}, ctx.Err()
		case <-time.After(backoff):
			backoff *= 3
		}
	}
	return CompletionResponse{}, lastErr
}

func isRetryableErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "429") || strings.Contains(s, "500") || strings.Contains(s, "502") || strings.Contains(s, "503") || strings.Contains(s, "timeout") || strings.Contains(s, "connection reset")
}

func (h *Harness) needsCompaction(systemPrompt string) bool {
	chars := len(systemPrompt)
	for _, m := range h.history {
		chars += len(m.Content)
		for _, tc := range m.ToolCalls {
			chars += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
	}
	estimatedTokens := chars / 4
	threshold := int(float64(h.cfg.ContextWindowTokens) * h.cfg.CompactionTriggerRatio)
	return estimatedTokens >= threshold && len(h.history) > h.cfg.KeepRecentMessages+2
}

// compactHistory preserves the most recent messages and summarizes earlier turns into a single bounded digest.
func (h *Harness) compactHistory() {
	cut := len(h.history) - h.cfg.KeepRecentMessages
	if cut <= 1 {
		return
	}
	// Align cut point to a user message boundary to avoid orphaned tool_call/tool pairs.
	for cut > 1 && h.history[cut].Role != "user" {
		cut--
	}
	if cut <= 1 {
		return
	}
	var b strings.Builder
	b.WriteString("[Compacted earlier session history]\n")
	for _, m := range h.history[:cut] {
		snippet := strings.TrimSpace(m.Content)
		if len(snippet) > 160 {
			snippet = snippet[:160] + "..."
		}
		if snippet != "" {
			b.WriteString("- ")
			b.WriteString(m.Role)
			b.WriteString(": ")
			b.WriteString(snippet)
			b.WriteByte('\n')
		}
	}
	summary := Message{Role: "user", Content: b.String()}
	next := make([]Message, 0, 1+len(h.history[cut:]))
	next = append(next, summary)
	next = append(next, h.history[cut:]...)
	h.history = next
}

func (h *Harness) loadSession() error {
	data, err := os.ReadFile(h.cfg.SessionFile)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	msgs := make([]Message, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m Message
		if err := json.Unmarshal([]byte(line), &m); err == nil {
			msgs = append(msgs, m)
		}
	}
	h.history = msgs
	return nil
}

func (h *Harness) saveSession() error {
	if h.cfg.SessionFile == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(h.cfg.SessionFile), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, m := range h.history {
		b, err := json.Marshal(m)
		if err != nil {
			continue
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return os.WriteFile(h.cfg.SessionFile, buf.Bytes(), 0o600)
}

func builtinToolsSpec() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "bash",
				"description": "Execute a shell command inside the agent workspace. Use `sumi` CLI for all Sumi collaboration actions.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
					},
					"required": []string{"command"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read a file inside workspace/ or memory/ (relative to agent home).",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write_file",
				"description": "Write a file inside workspace/ or memory/ (relative to agent home).",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
					},
					"required": []string{"path", "content"},
				},
			},
		},
	}
}

func resolveSafeAgentPath(workDir, memoryDir, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", errors.New("path must be relative and stay within workspace/ or memory/")
	}
	if clean == "MEMORY.md" || strings.HasPrefix(clean, "memory/") {
		sub := strings.TrimPrefix(clean, "memory/")
		target := filepath.Join(memoryDir, sub)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(memoryDir)) {
			return "", errors.New("path escapes memory directory")
		}
		return target, nil
	}
	sub := strings.TrimPrefix(clean, "workspace/")
	target := filepath.Join(workDir, sub)
	if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(workDir)) {
		return "", errors.New("path escapes workspace directory")
	}
	return target, nil
}

func executeTool(ctx context.Context, tc ToolCall, in driver.TurnInput) string {
	switch tc.Function.Name {
	case "bash":
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("invalid arguments: %v", err)
		}
		cmdCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cmdCtx, "bash", "-lc", args.Command)
		cmd.Dir = in.WorkDir
		cmd.Env = in.Env
		out, err := cmd.CombinedOutput()
		text := string(out)
		if len(text) > 16000 {
			text = text[:16000] + "\n...[truncated]"
		}
		if err != nil {
			return fmt.Sprintf("exit error: %v\n%s", err, text)
		}
		return text
	case "read_file":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("invalid arguments: %v", err)
		}
		p, err := resolveSafeAgentPath(in.WorkDir, in.MemoryDir, args.Path)
		if err != nil {
			return err.Error()
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return fmt.Sprintf("read error: %v", err)
		}
		if len(b) > 32000 {
			return string(b[:32000]) + "\n...[truncated]"
		}
		return string(b)
	case "write_file":
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("invalid arguments: %v", err)
		}
		p, err := resolveSafeAgentPath(in.WorkDir, in.MemoryDir, args.Path)
		if err != nil {
			return err.Error()
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Sprintf("mkdir error: %v", err)
		}
		if err := os.WriteFile(p, []byte(args.Content), 0o644); err != nil {
			return fmt.Sprintf("write error: %v", err)
		}
		return "ok"
	default:
		return fmt.Sprintf("unknown tool: %s", tc.Function.Name)
	}
}

type openAIProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func (p *openAIProvider) Complete(ctx context.Context, model string, messages []Message, tools []map[string]any) (CompletionResponse, error) {
	if p.apiKey == "" {
		return CompletionResponse{}, errors.New("missing OPENAI_API_KEY for builtin driver")
	}
	payload := map[string]any{
		"model":    model,
		"messages": messages,
		"tools":    tools,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return CompletionResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return CompletionResponse{}, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return CompletionResponse{}, err
	}
	if resp.StatusCode >= 400 {
		return CompletionResponse{}, fmt.Errorf("provider status %d", resp.StatusCode)
	}

	var parsed struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return CompletionResponse{}, err
	}
	if len(parsed.Choices) == 0 {
		return CompletionResponse{}, errors.New("provider returned empty choices")
	}
	return CompletionResponse{
		Message:      parsed.Choices[0].Message,
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}, nil
}
