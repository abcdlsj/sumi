package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

// quoteTargetArg formats a CLI target for copy-pasteable shell hints without altering clean tokens
// like `#eng`, `dm:@Alice`, `thread:t1`, or `task:#4`.
func quoteTargetArg(target string) string {
	if target == "" {
		return "<target>"
	}
	for i := 0; i < len(target); i++ {
		c := target[i]
		safe := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '#' || c == '@' || c == ':' || c == '-' || c == '_' || c == '.' || c == '/'
		if !safe {
			return "'" + strings.ReplaceAll(target, "'", `'\''`) + "'"
		}
	}
	return target
}

func heldNextCommands(target string) map[string]string {
	t := quoteTargetArg(target)
	return map[string]string{
		"send_draft":    fmt.Sprintf("sumi send %s --draft", t),
		"discard_draft": fmt.Sprintf("sumi send %s --discard-draft", t),
		"read_more":     fmt.Sprintf("sumi read %s --limit 20", t),
	}
}

func heldDraftHint(target string) string {
	cmds := heldNextCommands(target)
	return fmt.Sprintf("Your message was saved as a draft (kept 10 minutes). Read data.unread_messages (older ones: `%s`). Then drop it with `%s` (a normal outcome when it is no longer needed), send it unchanged with `%s`, or send a rewritten --body.",
		cmds["read_more"], cmds["discard_draft"], cmds["send_draft"])
}

func heldHint(target string) string {
	t := quoteTargetArg(target)
	return fmt.Sprintf("Read data.unread_messages (older ones: `sumi read %s --limit 20`). Then decide: skip if your reply is no longer needed, or rewrite it and run the command again.", t)
}

func hintFor(code string) string {
	switch code {
	case codeHeld:
		return heldHint("")
	case "invalid_argument":
		return "Fix the argument named in message; run `sumi <command> --help` for usage."
	case "not_found":
		return "Check the target: `sumi inbox` and `sumi conversations` list `#channel` / `dm:@name`, `sumi task list --status all` lists `task:#N`."
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
