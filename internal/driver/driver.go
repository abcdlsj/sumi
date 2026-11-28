package driver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

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

// EnsureWorkDir validates the child's working directory before fork. Drivers set SysProcAttr,
// which makes os/exec skip its own cwd pre-check, so a missing directory would surface from
// fork/exec as ENOENT on the binary path and blame the wrong file.
func EnsureWorkDir(workDir string) error {
	switch fi, err := os.Stat(workDir); {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("work dir %s does not exist", workDir)
	case err != nil:
		return fmt.Errorf("work dir %s: %w", workDir, err)
	case !fi.IsDir():
		return fmt.Errorf("work dir %s is not a directory", workDir)
	}
	return nil
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

// GracefulCloser is optionally implemented by drivers whose child processes benefit from
// a bounded graceful shutdown window to flush session state before SIGKILL.
type GracefulCloser interface {
	GracefulClose(timeoutSeconds int) error
}

// ErrSteerUnsupported is returned when a driver does not support mid-turn steering.
var ErrSteerUnsupported = errors.New("mid-turn steer is not supported")

// ErrTurnNotActive is returned when Steer is called while no turn is actively running.
var ErrTurnNotActive = errors.New("no active turn to steer")

// Steerer is optionally implemented by persistent drivers (Pi RPC, Claude stream-json) that can
// inject a mid-turn notice into an active turn without cancelling it.
type Steerer interface {
	Steer(ctx context.Context, message string) error
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
