package domain

import "fmt"

// Code is a stable machine-readable error code shared across API, CLI, and protocol layers.
type Code string

const (
	CodeInvalidArgument  Code = "invalid_argument"
	CodeUnauthenticated  Code = "unauthenticated"
	CodePermissionDenied Code = "permission_denied"
	CodeNotFound         Code = "not_found"
	CodeConflict         Code = "conflict"
	CodeHeld             Code = "held"
	CodeExpired          Code = "expired"
	CodeRateLimited      Code = "rate_limited"
	CodeUnavailable      Code = "unavailable"
	CodeInternal         Code = "internal"
)

// Error carries a stable code, a human-safe message, and optional non-sensitive structured metadata.
// Never place message bodies, attachments, memory contents, prompts, or secrets inside Message or Details.
type Error struct {
	Code      Code           `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	Cause     error          `json:"-"`
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func WrapError(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}

func (e *Error) WithDetails(details map[string]any) *Error {
	e.Details = details
	return e
}

func (e *Error) WithRetryable(retryable bool) *Error {
	e.Retryable = retryable
	return e
}
