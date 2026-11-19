package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Stable error codes in the CLI envelope; server codes pass through unchanged.
const (
	codeInvalidArgument = "invalid_argument"
	codeUnauthenticated = "unauthenticated"
	codeUnavailable     = "unavailable"
	codeInternal        = "internal"
	codeHeld            = "held"
)

// EnvelopeError is the machine-readable `error` object of the CLI JSON envelope.
// Hint names the next command to try so an Agent can self-correct without guessing.
type EnvelopeError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	Hint      string         `json:"hint,omitempty"`
}

func (e *EnvelopeError) Error() string { return e.Code + ": " + e.Message }

func localError(code string, err error) *EnvelopeError {
	e := &EnvelopeError{Code: code, Message: err.Error(), Retryable: code == codeUnavailable}
	e.Hint = hintFor(code)
	return e
}

func asEnvelopeError(err error) *EnvelopeError {
	var e *EnvelopeError
	if errors.As(err, &e) {
		return e
	}
	return localError(codeUnavailable, err)
}

// responseError maps a non-success Server response to an envelope error. A held write (409 with
// `held: true`) is reported as code `held` and its body stays in data so the unread messages are visible.
func responseError(status int, raw []byte) (*EnvelopeError, any) {
	if status < 400 {
		return nil, nil
	}
	var body struct {
		Held  bool           `json:"held"`
		Error *EnvelopeError `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)
	if body.Held {
		var data any
		_ = json.Unmarshal(raw, &data)
		return &EnvelopeError{
			Code:    codeHeld,
			Message: "others posted since you last read this conversation; nothing was written",
			Hint:    hintFor(codeHeld),
		}, data
	}
	e := body.Error
	if e == nil || e.Code == "" {
		code := codeInternal
		switch {
		case status == http.StatusBadGateway || status == http.StatusServiceUnavailable:
			code = codeUnavailable
		case status == http.StatusUnauthorized:
			code = codeUnauthenticated
		}
		e = &EnvelopeError{Code: code, Message: fmt.Sprintf("server returned http %d", status), Retryable: code == codeUnavailable}
	}
	if e.Hint == "" {
		e.Hint = hintFor(e.Code)
	}
	return e, nil
}

func hintFor(code string) string {
	switch code {
	case codeHeld:
		return "Read data.unread_messages (older ones: `sumi read <conversation_id> --limit N`). Then decide: skip if your reply is no longer needed, or rewrite it and run the command again."
	case "invalid_argument":
		return "Fix the argument named in message; run `sumi <command> --help` for usage."
	case "not_found":
		return "Check the id: `sumi conversations`, `sumi members`, or `sumi task list --status all`."
	case "permission_denied":
		return "You lack this permission. Ask a human owner or admin in the conversation instead of retrying."
	case "conflict":
		return "State changed underneath you. Re-read it (`sumi task list --status all`) and retry with the current --expected-revision, or pick other work."
	case codeUnauthenticated:
		return "SUMI_SOCKET or SUMI_DRIVER_TOKEN is missing or stale; this only happens outside a Sumi turn."
	case codeUnavailable:
		return "The Sumi server is unreachable right now; retry once shortly, then stop and report."
	case "rate_limited":
		return "Wait a minute before retrying."
	}
	return ""
}
