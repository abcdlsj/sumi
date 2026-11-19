package driver

import "context"

// TurnInput carries the per-turn wake prompt and execution environment for a single-session agent.
type TurnInput struct {
	SystemPrompt string
	// StartupContext (the MEMORY.md excerpt) is prepended to UserPrompt only on the first turn
	// of a fresh Runtime Session; resumed sessions already hold it.
	StartupContext string
	UserPrompt     string
	WorkDir        string
	MemoryDir      string
	Env            []string
	// OnActivity, if set, receives runtime progress during the turn. It must not block.
	OnActivity func(Activity)
}

// Activity is one runtime progress signal. Tool names a tool kind or builtin tool name, never
// its arguments or output. An empty ToolCallID means generic progress (model output, updates).
type Activity struct {
	ToolCallID string
	Tool       string
	Done       bool
}

// Emit forwards a to OnActivity when set.
func (in TurnInput) Emit(a Activity) {
	if in.OnActivity != nil {
		in.OnActivity(a)
	}
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
