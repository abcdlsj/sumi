package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
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

// Run executes an Agent CLI command via cobra and writes a single JSON envelope to stdout.
func Run(args []string, stdout io.Writer) int {
	exitCode := 0
	root := &cobra.Command{
		Use:           "sumi",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.SetErr(io.Discard)

	call := func(method, path string, body any) error {
		c, err := NewFromEnv()
		if err != nil {
			exitCode = 1
			writeEnvelope(stdout, false, nil, err.Error())
			return err
		}
		status, raw, err := c.Do(method, path, body)
		if err != nil {
			exitCode = 1
			writeEnvelope(stdout, false, nil, err.Error())
			return err
		}
		var parsed any
		_ = json.Unmarshal(raw, &parsed)
		if status >= 400 {
			exitCode = 1
			writeEnvelope(stdout, false, parsed, fmt.Sprintf("http %d", status))
			return fmt.Errorf("http %d", status)
		}
		writeEnvelope(stdout, true, parsed, "")
		return nil
	}

	var inboxFilter string
	inboxCmd := &cobra.Command{
		Use:   "inbox",
		Short: "List unread attention items",
		RunE: func(_ *cobra.Command, _ []string) error {
			return call(http.MethodGet, "/inbox?filter="+inboxFilter, nil)
		},
	}
	inboxCmd.Flags().StringVar(&inboxFilter, "filter", "needs_me", "needs_me|unread|all")
	root.AddCommand(inboxCmd)

	root.AddCommand(&cobra.Command{
		Use:   "conversations",
		Short: "List accessible channels and DMs",
		RunE:  func(_ *cobra.Command, _ []string) error { return call(http.MethodGet, "/conversations", nil) },
	})
	root.AddCommand(&cobra.Command{
		Use:   "members",
		Short: "List space members",
		RunE:  func(_ *cobra.Command, _ []string) error { return call(http.MethodGet, "/members", nil) },
	})

	var readUnread bool
	var readLimit int
	readCmd := &cobra.Command{
		Use:   "read <conversation_id>",
		Args:  cobra.ExactArgs(1),
		Short: "Read messages from a conversation and advance read_seq",
		RunE: func(_ *cobra.Command, a []string) error {
			q := fmt.Sprintf("/conversations/%s/messages?limit=%d", a[0], readLimit)
			if readUnread {
				q += "&unread=true"
			}
			return call(http.MethodGet, q, nil)
		},
	}
	readCmd.Flags().BoolVar(&readUnread, "unread", false, "only unread messages")
	readCmd.Flags().IntVar(&readLimit, "limit", 50, "max messages")
	root.AddCommand(readCmd)

	var sendBody, sendReplyTo string
	sendCmd := &cobra.Command{
		Use:   "send <conversation_id>",
		Args:  cobra.ExactArgs(1),
		Short: "Send a message (protected by Freshness Guard)",
		RunE: func(_ *cobra.Command, a []string) error {
			return call(http.MethodPost, "/conversations/"+a[0]+"/messages", map[string]any{
				"body_markdown":       sendBody,
				"reply_to_message_id": sendReplyTo,
			})
		},
	}
	sendCmd.Flags().StringVar(&sendBody, "body", "", "message markdown")
	sendCmd.Flags().StringVar(&sendReplyTo, "reply-to", "", "root message id for thread reply")
	_ = sendCmd.MarkFlagRequired("body")
	root.AddCommand(sendCmd)

	root.AddCommand(&cobra.Command{
		Use:  "dm <peer_member_id>",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			return call(http.MethodPost, "/dm", map[string]any{"peer_member_id": a[0]})
		},
	})
	root.AddCommand(&cobra.Command{
		Use:  "follow <conversation_id>",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			return call(http.MethodPut, "/conversations/"+a[0]+"/follow", map[string]any{"following": true})
		},
	})
	root.AddCommand(&cobra.Command{
		Use:  "unfollow <conversation_id>",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			return call(http.MethodPut, "/conversations/"+a[0]+"/follow", map[string]any{"following": false})
		},
	})
	root.AddCommand(&cobra.Command{
		Use:  "mute <conversation_id>",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			return call(http.MethodPut, "/conversations/"+a[0]+"/notify", map[string]any{"level": "mute"})
		},
	})

	// Task subcommands
	taskCmd := &cobra.Command{Use: "task", Short: "Manage tasks"}
	var taskStatusFilter, taskAssigneeFilter string
	taskListCmd := &cobra.Command{
		Use: "list",
		RunE: func(_ *cobra.Command, _ []string) error {
			return call(http.MethodGet, fmt.Sprintf("/tasks?status=%s&assignee=%s", taskStatusFilter, taskAssigneeFilter), nil)
		},
	}
	taskListCmd.Flags().StringVar(&taskStatusFilter, "status", "active", "status filter")
	taskListCmd.Flags().StringVar(&taskAssigneeFilter, "assignee", "", "assignee filter")
	taskCmd.AddCommand(taskListCmd)

	var tcChannel, tcSource, tcTitle, tcDesc, tcAssignee string
	taskCreateCmd := &cobra.Command{
		Use: "create",
		RunE: func(_ *cobra.Command, _ []string) error {
			return call(http.MethodPost, "/tasks", map[string]any{
				"channel_id":         tcChannel,
				"source_message_id":  tcSource,
				"title":              tcTitle,
				"description":        tcDesc,
				"assignee_member_id": tcAssignee,
			})
		},
	}
	taskCreateCmd.Flags().StringVar(&tcChannel, "channel", "", "channel_id")
	taskCreateCmd.Flags().StringVar(&tcSource, "source", "", "source_message_id")
	taskCreateCmd.Flags().StringVar(&tcTitle, "title", "", "title")
	taskCreateCmd.Flags().StringVar(&tcDesc, "description", "", "description")
	taskCreateCmd.Flags().StringVar(&tcAssignee, "assignee", "", "assignee_member_id")
	taskCmd.AddCommand(taskCreateCmd)

	taskCmd.AddCommand(&cobra.Command{
		Use:  "claim <task_id>",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error { return call(http.MethodPost, "/tasks/"+a[0]+"/claim", nil) },
	})
	taskCmd.AddCommand(&cobra.Command{
		Use:  "unclaim <task_id>",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error { return call(http.MethodPost, "/tasks/"+a[0]+"/unclaim", nil) },
	})

	var assignTo, assignReason string
	var assignRev int64
	taskAssignCmd := &cobra.Command{
		Use:  "assign <task_id>",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			return call(http.MethodPost, "/tasks/"+a[0]+"/assign", map[string]any{
				"assignee_member_id": assignTo,
				"reason":             assignReason,
				"expected_revision":  assignRev,
			})
		},
	}
	taskAssignCmd.Flags().StringVar(&assignTo, "to", "", "assignee_member_id")
	taskAssignCmd.Flags().StringVar(&assignReason, "reason", "", "handoff reason")
	taskAssignCmd.Flags().Int64Var(&assignRev, "expected-revision", 0, "expected revision")
	taskCmd.AddCommand(taskAssignCmd)

	taskCmd.AddCommand(&cobra.Command{
		Use:  "status <task_id> <status>",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, a []string) error {
			return call(http.MethodPut, "/tasks/"+a[0]+"/status", map[string]any{"status": a[1]})
		},
	})

	var amendTitle, amendDesc string
	var amendRev int64
	taskAmendCmd := &cobra.Command{
		Use:  "amend <task_id>",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, a []string) error {
			payload := map[string]any{"expected_revision": amendRev}
			if amendTitle != "" {
				payload["title"] = amendTitle
			}
			if amendDesc != "" {
				payload["description"] = amendDesc
			}
			return call(http.MethodPatch, "/tasks/"+a[0], payload)
		},
	}
	taskAmendCmd.Flags().StringVar(&amendTitle, "title", "", "new title")
	taskAmendCmd.Flags().StringVar(&amendDesc, "description", "", "new description")
	taskAmendCmd.Flags().Int64Var(&amendRev, "expected-revision", 0, "expected revision")
	taskCmd.AddCommand(taskAmendCmd)
	root.AddCommand(taskCmd)

	// Agent subcommands
	agentCmd := &cobra.Command{Use: "agent", Short: "Manage agents"}
	var agName, agRole, agDriver, agSrcConv string
	agentCreateCmd := &cobra.Command{
		Use: "create",
		RunE: func(_ *cobra.Command, _ []string) error {
			return call(http.MethodPost, "/agents", map[string]any{
				"display_name":           agName,
				"role_text":              agRole,
				"driver_kind":            agDriver,
				"source_conversation_id": agSrcConv,
			})
		},
	}
	agentCreateCmd.Flags().StringVar(&agName, "name", "", "display_name")
	agentCreateCmd.Flags().StringVar(&agRole, "role", "", "role text")
	agentCreateCmd.Flags().StringVar(&agDriver, "driver", "builtin", "builtin|acp")
	agentCreateCmd.Flags().StringVar(&agSrcConv, "source-conversation", "", "optional conversation id")
	agentCmd.AddCommand(agentCreateCmd)
	root.AddCommand(agentCmd)

	// Channel subcommands
	chCmd := &cobra.Command{Use: "channel", Short: "Manage channels"}
	var chSlug, chKind, chTopic, chSrcConv string
	chCreateCmd := &cobra.Command{
		Use: "create",
		RunE: func(_ *cobra.Command, _ []string) error {
			return call(http.MethodPost, "/channels", map[string]any{
				"slug":                   chSlug,
				"kind":                   chKind,
				"topic":                  chTopic,
				"source_conversation_id": chSrcConv,
			})
		},
	}
	chCreateCmd.Flags().StringVar(&chSlug, "slug", "", "channel slug")
	chCreateCmd.Flags().StringVar(&chKind, "kind", "public", "public|private")
	chCreateCmd.Flags().StringVar(&chTopic, "topic", "", "topic")
	chCreateCmd.Flags().StringVar(&chSrcConv, "source-conversation", "", "optional conversation id")
	chCmd.AddCommand(chCreateCmd)
	root.AddCommand(chCmd)

	root.SetArgs(args)
	if err := root.Execute(); err != nil && exitCode == 0 {
		exitCode = 2
		writeEnvelope(stdout, false, nil, err.Error())
	}
	return exitCode
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
