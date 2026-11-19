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

// Failure tags a driver error with a stable agent error code (domain.AgentErr*).
// Its message must describe the driver/HTTP failure only, never prompts or message bodies.
type Failure struct {
	Code string
	Err  error
}

func (f *Failure) Error() string { return f.Err.Error() }
func (f *Failure) Unwrap() error { return f.Err }

func Fail(code string, err error) error {
	if err == nil {
		return nil
	}
	return &Failure{Code: code, Err: err}
}
