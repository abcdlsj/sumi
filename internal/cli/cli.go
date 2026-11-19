package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	socketPath  string
	driverToken string
	httpClient  *http.Client
}

func NewFromEnv() (*Client, error) {
	sock := os.Getenv("SUMI_SOCKET")
	tok := os.Getenv("SUMI_DRIVER_TOKEN")
	if sock == "" || tok == "" {
		return nil, errors.New("SUMI_SOCKET and SUMI_DRIVER_TOKEN must be set")
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	return &Client{
		socketPath:  sock,
		driverToken: tok,
		httpClient:  &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}, nil
}

func (c *Client) Do(method, path string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://unix"+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.driverToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, out, err
}

// Run executes an Agent CLI command and writes a single JSON envelope to stdout.
func Run(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		writeEnvelope(stdout, false, nil, "usage: sumi <inbox|read|send|dm|members|follow|unfollow|mute|task|agent|channel>")
		return 2
	}
	c, err := NewFromEnv()
	if err != nil {
		writeEnvelope(stdout, false, nil, err.Error())
		return 1
	}

	switch args[0] {
	case "inbox":
		fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
		filter := fs.String("filter", "needs_me", "needs_me|unread|all")
		_ = fs.Parse(args[1:])
		return proxyOutput(stdout, c, http.MethodGet, "/inbox?filter="+*filter, nil)

	case "conversations":
		return proxyOutput(stdout, c, http.MethodGet, "/conversations", nil)

	case "members":
		return proxyOutput(stdout, c, http.MethodGet, "/members", nil)

	case "read":
		fs := flag.NewFlagSet("read", flag.ContinueOnError)
		unread := fs.Bool("unread", false, "only unread messages")
		limit := fs.Int("limit", 50, "max messages")
		_ = fs.Parse(args[1:])
		if fs.NArg() < 1 {
			writeEnvelope(stdout, false, nil, "usage: sumi read [--unread] <conversation_id>")
			return 2
		}
		convID := fs.Arg(0)
		q := fmt.Sprintf("/conversations/%s/messages?limit=%d", convID, *limit)
		if *unread {
			q += "&unread=true"
		}
		return proxyOutput(stdout, c, http.MethodGet, q, nil)

	case "send":
		fs := flag.NewFlagSet("send", flag.ContinueOnError)
		body := fs.String("body", "", "message markdown")
		replyTo := fs.String("reply-to", "", "optional root message id for thread reply")
		_ = fs.Parse(args[1:])
		if fs.NArg() < 1 || strings.TrimSpace(*body) == "" {
			writeEnvelope(stdout, false, nil, "usage: sumi send --body <text> [--reply-to <msg_id>] <conversation_id>")
			return 2
		}
		convID := fs.Arg(0)
		return proxyOutput(stdout, c, http.MethodPost, "/conversations/"+convID+"/messages", map[string]any{
			"body_markdown":       *body,
			"reply_to_message_id": *replyTo,
		})

	case "dm":
		if len(args) < 2 {
			writeEnvelope(stdout, false, nil, "usage: sumi dm <peer_member_id>")
			return 2
		}
		return proxyOutput(stdout, c, http.MethodPost, "/dm", map[string]any{"peer_member_id": args[1]})

	case "follow", "unfollow":
		if len(args) < 2 {
			writeEnvelope(stdout, false, nil, "usage: sumi follow|unfollow <conversation_id>")
			return 2
		}
		return proxyOutput(stdout, c, http.MethodPut, "/conversations/"+args[1]+"/follow", map[string]any{
			"following": args[0] == "follow",
		})

	case "mute":
		if len(args) < 2 {
			writeEnvelope(stdout, false, nil, "usage: sumi mute <conversation_id>")
			return 2
		}
		return proxyOutput(stdout, c, http.MethodPut, "/conversations/"+args[1]+"/notify", map[string]any{
			"level": "mute",
		})

	case "task":
		return runTaskCmd(c, args[1:], stdout)

	case "agent":
		return runAgentCmd(c, args[1:], stdout)

	case "channel":
		return runChannelCmd(c, args[1:], stdout)

	default:
		writeEnvelope(stdout, false, nil, "unknown command: "+args[0])
		return 2
	}
}

func runTaskCmd(c *Client, args []string, stdout io.Writer) int {
	if len(args) == 0 {
		writeEnvelope(stdout, false, nil, "usage: sumi task <list|create|claim|unclaim|assign|status|amend>")
		return 2
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("task list", flag.ContinueOnError)
		status := fs.String("status", "active", "active|all|todo|in_progress|in_review|done|closed")
		assignee := fs.String("assignee", "", "mine or member_id")
		_ = fs.Parse(args[1:])
		return proxyOutput(stdout, c, http.MethodGet, fmt.Sprintf("/tasks?status=%s&assignee=%s", *status, *assignee), nil)

	case "create":
		fs := flag.NewFlagSet("task create", flag.ContinueOnError)
		ch := fs.String("channel", "", "channel_id")
		src := fs.String("source", "", "source_message_id")
		title := fs.String("title", "", "task title")
		desc := fs.String("description", "", "task description")
		assignee := fs.String("assignee", "", "assignee_member_id")
		_ = fs.Parse(args[1:])
		return proxyOutput(stdout, c, http.MethodPost, "/tasks", map[string]any{
			"channel_id":         *ch,
			"source_message_id":  *src,
			"title":              *title,
			"description":        *desc,
			"assignee_member_id": *assignee,
		})

	case "claim":
		if len(args) < 2 {
			writeEnvelope(stdout, false, nil, "usage: sumi task claim <task_id>")
			return 2
		}
		return proxyOutput(stdout, c, http.MethodPost, "/tasks/"+args[1]+"/claim", nil)

	case "unclaim":
		if len(args) < 2 {
			writeEnvelope(stdout, false, nil, "usage: sumi task unclaim <task_id>")
			return 2
		}
		return proxyOutput(stdout, c, http.MethodPost, "/tasks/"+args[1]+"/unclaim", nil)

	case "assign":
		fs := flag.NewFlagSet("task assign", flag.ContinueOnError)
		to := fs.String("to", "", "assignee_member_id")
		reason := fs.String("reason", "", "one-line handoff reason")
		rev := fs.Int64("expected-revision", 0, "optional revision CAS")
		_ = fs.Parse(args[1:])
		if fs.NArg() < 1 {
			writeEnvelope(stdout, false, nil, "usage: sumi task assign --to <member_id> --reason <text> <task_id>")
			return 2
		}
		return proxyOutput(stdout, c, http.MethodPost, "/tasks/"+fs.Arg(0)+"/assign", map[string]any{
			"assignee_member_id": *to,
			"reason":             *reason,
			"expected_revision":  *rev,
		})

	case "status":
		if len(args) < 3 {
			writeEnvelope(stdout, false, nil, "usage: sumi task status <task_id> <todo|in_progress|in_review|done|closed>")
			return 2
		}
		return proxyOutput(stdout, c, http.MethodPut, "/tasks/"+args[1]+"/status", map[string]any{
			"status": args[2],
		})

	case "amend":
		fs := flag.NewFlagSet("task amend", flag.ContinueOnError)
		title := fs.String("title", "", "new title")
		desc := fs.String("description", "", "new description")
		rev := fs.Int64("expected-revision", 0, "optional revision CAS")
		_ = fs.Parse(args[1:])
		if fs.NArg() < 1 {
			writeEnvelope(stdout, false, nil, "usage: sumi task amend [--title ...] [--description ...] <task_id>")
			return 2
		}
		payload := map[string]any{"expected_revision": *rev}
		if *title != "" {
			payload["title"] = *title
		}
		if *desc != "" {
			payload["description"] = *desc
		}
		return proxyOutput(stdout, c, http.MethodPatch, "/tasks/"+fs.Arg(0), payload)

	default:
		writeEnvelope(stdout, false, nil, "unknown task subcommand: "+args[0])
		return 2
	}
}

func runAgentCmd(c *Client, args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "create" {
		writeEnvelope(stdout, false, nil, "usage: sumi agent create --name <name> --role <role> [--driver builtin|acp]")
		return 2
	}
	fs := flag.NewFlagSet("agent create", flag.ContinueOnError)
	name := fs.String("name", "", "display_name")
	role := fs.String("role", "", "role text")
	drv := fs.String("driver", "builtin", "builtin|acp")
	src := fs.String("source-conversation", "", "optional conversation id for action message")
	_ = fs.Parse(args[1:])
	return proxyOutput(stdout, c, http.MethodPost, "/agents", map[string]any{
		"display_name":           *name,
		"role_text":              *role,
		"driver_kind":            *drv,
		"source_conversation_id": *src,
	})
}

func runChannelCmd(c *Client, args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "create" {
		writeEnvelope(stdout, false, nil, "usage: sumi channel create --slug <slug> [--kind public|private] [--topic ...]")
		return 2
	}
	fs := flag.NewFlagSet("channel create", flag.ContinueOnError)
	slug := fs.String("slug", "", "channel slug")
	kind := fs.String("kind", "public", "public|private")
	topic := fs.String("topic", "", "channel topic")
	src := fs.String("source-conversation", "", "optional conversation id for action message")
	_ = fs.Parse(args[1:])
	return proxyOutput(stdout, c, http.MethodPost, "/channels", map[string]any{
		"slug":                   *slug,
		"kind":                   *kind,
		"topic":                  *topic,
		"source_conversation_id": *src,
	})
}

func proxyOutput(stdout io.Writer, c *Client, method, path string, body any) int {
	status, raw, err := c.Do(method, path, body)
	if err != nil {
		writeEnvelope(stdout, false, nil, err.Error())
		return 1
	}
	var parsed any
	_ = json.Unmarshal(raw, &parsed)
	if status >= 400 {
		writeEnvelope(stdout, false, parsed, fmt.Sprintf("http %d", status))
		return 1
	}
	writeEnvelope(stdout, true, parsed, "")
	return 0
}

func writeEnvelope(w io.Writer, ok bool, data any, errMsg string) {
	env := map[string]any{"ok": ok}
	if data != nil {
		env["data"] = data
	}
	if errMsg != "" {
		env["error"] = errMsg
	}
	_ = json.NewEncoder(w).Encode(env)
}
