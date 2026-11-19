package driver

import "context"

// TurnInput carries the per-turn wake prompt and execution environment for a single-session agent.
type TurnInput struct {
	SystemPrompt string
	UserPrompt   string
	WorkDir      string
	MemoryDir    string
	Env          []string
}

type TurnResult struct {
	FinalText    string
	InputTokens  int
	OutputTokens int
	ToolCalls    int
}

// SessionDriver maintains one persistent runtime session per Agent to preserve prompt cache
// and cross-conversation continuity.
type SessionDriver interface {
	RunTurn(ctx context.Context, in TurnInput) (TurnResult, error)
	ResetSession() error
	Close() error
}
