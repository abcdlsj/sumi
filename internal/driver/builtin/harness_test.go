package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/sumi/internal/driver"
)

type scriptProvider struct {
	step      int
	seenLens  []int
	responses []CompletionResponse
}

func (p *scriptProvider) Complete(_ context.Context, _ string, messages []Message, _ []map[string]any) (CompletionResponse, error) {
	p.seenLens = append(p.seenLens, len(messages))
	idx := p.step
	if idx >= len(p.responses) {
		idx = len(p.responses) - 1
	}
	p.step++
	return p.responses[idx], nil
}

func TestHarnessToolLoopSessionPersistenceAndCompaction(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "workspace")
	memDir := filepath.Join(root, "memory")
	_ = os.MkdirAll(workDir, 0o755)
	_ = os.MkdirAll(memDir, 0o755)
	sessFile := filepath.Join(root, "session.jsonl")

	prov := &scriptProvider{
		responses: []CompletionResponse{
			{
				Message: Message{
					Role: "assistant",
					ToolCalls: []ToolCall{{
						ID:   "call-1",
						Type: "function",
						Function: FunctionCall{
							Name:      "write_file",
							Arguments: `{"path":"workspace/note.txt","content":"hello sumi v2"}`,
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 20,
			},
			{
				Message:      Message{Role: "assistant", Content: "Saved note.txt"},
				InputTokens:  140,
				OutputTokens: 15,
			},
			{
				Message:      Message{Role: "assistant", Content: "Turn 2 complete"},
				InputTokens:  180,
				OutputTokens: 10,
			},
		},
	}

	h, err := New(Config{
		Model:                  "test-model",
		ContextWindowTokens:    200,
		CompactionTriggerRatio: 0.75,
		KeepRecentMessages:     2,
		SessionFile:            sessFile,
	}, prov)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	res, err := h.RunTurn(context.Background(), driver.TurnInput{
		SystemPrompt: "You are a Sumi agent.",
		UserPrompt:   "Create note.txt",
		WorkDir:      workDir,
		MemoryDir:    memDir,
	})
	if err != nil {
		t.Fatalf("run turn 1: %v", err)
	}
	if res.FinalText != "Saved note.txt" || res.ToolCalls != 1 {
		t.Fatalf("unexpected turn 1 result: %+v", res)
	}
	data, err := os.ReadFile(filepath.Join(workDir, "note.txt"))
	if err != nil || string(data) != "hello sumi v2" {
		t.Fatalf("workspace file not written: %q err=%v", string(data), err)
	}

	// Verify path escape is blocked.
	if _, err := resolveSafeAgentPath(workDir, memDir, "../secret.txt"); err == nil {
		t.Fatalf("expected path escape error")
	}

	// Reload from persisted JSONL session and trigger compaction with a large prompt.
	h2, err := New(Config{
		Model:                  "test-model",
		ContextWindowTokens:    120,
		CompactionTriggerRatio: 0.5,
		KeepRecentMessages:     2,
		SessionFile:            sessFile,
	}, prov)
	if err != nil {
		t.Fatalf("reload harness: %v", err)
	}
	res2, err := h2.RunTurn(context.Background(), driver.TurnInput{
		SystemPrompt: "You are a Sumi agent.",
		UserPrompt:   strings.Repeat("x", 600),
		WorkDir:      workDir,
		MemoryDir:    memDir,
	})
	if err != nil || res2.FinalText != "Turn 2 complete" {
		t.Fatalf("turn 2 failed: %+v err=%v", res2, err)
	}
	if err := h2.ResetSession(); err != nil {
		t.Fatalf("reset session: %v", err)
	}
	if _, err := os.Stat(sessFile); !os.IsNotExist(err) {
		t.Fatalf("expected session file deleted on reset")
	}
}
