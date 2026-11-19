package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/abcdlsj/sumi/internal/pair"
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
	contentType := ""
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(raw)
		contentType = "application/json"
	}
	return c.do(method, path, r, contentType)
}

func (c *Client) do(method, path string, r io.Reader, contentType string) (int, []byte, error) {
	req, err := http.NewRequest(method, "http://unix"+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.driverToken)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, out, err
}

// Upload streams a local file as a multipart attachment.
func (c *Client) Upload(filePath string) (int, []byte, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		part, err := mw.CreateFormFile("file", filepath.Base(filePath))
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, f); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = mw.Close()
	}()
	return c.do(http.MethodPost, "/attachments", pr, mw.FormDataContentType())
}

// Download saves an attachment into the agent workspace and returns its path.
func (c *Client) Download(attachmentID string) (string, error) {
	status, raw, err := c.do(http.MethodGet, "/attachments/"+attachmentID, nil, "")
	if err != nil {
		return "", err
	}
	if e, _ := responseError(status, raw); e != nil {
		return "", e
	}
	workDir := os.Getenv("SUMI_ATTACHMENT_DIR")
	if workDir == "" {
		workDir = "attachments"
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(workDir, attachmentID)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// uploadAll uploads each path and returns attachment IDs in order.
func (c *Client) uploadAll(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(paths))
	for _, p := range paths {
		status, raw, err := c.Upload(p)
		if err != nil {
			return nil, err
		}
		if e, _ := responseError(status, raw); e != nil {
			e.Message = "upload " + filepath.Base(p) + ": " + e.Message
			return nil, e
		}
		var parsed struct {
			Attachment struct {
				ID string `json:"id"`
			} `json:"attachment"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Attachment.ID == "" {
			return nil, fmt.Errorf("upload %s returned no attachment id", filepath.Base(p))
		}
		ids = append(ids, parsed.Attachment.ID)
	}
	return ids, nil
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

	fail := func(e *EnvelopeError, data any) error {
		exitCode = 1
		writeEnvelope(stdout, false, data, e)
		return e
	}
	call := func(method, path string, body any) error {
		c, err := NewFromEnv()
		if err != nil {
			return fail(localError(codeUnauthenticated, err), nil)
		}
		status, raw, err := c.Do(method, path, body)
		if err != nil {
			return fail(localError(codeUnavailable, err), nil)
		}
		if e, data := responseError(status, raw); e != nil {
			return fail(e, data)
		}
		var parsed any
		_ = json.Unmarshal(raw, &parsed)
		writeEnvelope(stdout, true, parsed, nil)
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

	var sendBody string
	var sendReplyTo string
	var sendAttach []string
	sendCmd := &cobra.Command{
		Use:   "send <conversation_id>",
		Args:  cobra.ExactArgs(1),
		Short: "Send a message (protected by Freshness Guard)",
		RunE: func(_ *cobra.Command, a []string) error {
			c, err := NewFromEnv()
			if err != nil {
				return fail(localError(codeUnauthenticated, err), nil)
			}
			ids, err := c.uploadAll(sendAttach)
			if err != nil {
				return fail(asEnvelopeError(err), nil)
			}
			return call(http.MethodPost, "/conversations/"+a[0]+"/messages", map[string]any{
				"body_markdown":       sendBody,
				"reply_to_message_id": sendReplyTo,
				"attachment_ids":      ids,
			})
		},
	}
	sendCmd.Flags().StringVar(&sendBody, "body", "", "message markdown")
	sendCmd.Flags().StringVar(&sendReplyTo, "reply-to", "", "root message id for thread reply")
	sendCmd.Flags().StringArrayVar(&sendAttach, "attach", nil, "local file to upload and attach (repeatable)")
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

	root.AddCommand(&cobra.Command{
		Use:   "attachment <attachment_id>",
		Args:  cobra.ExactArgs(1),
		Short: "Download an attachment to the current workspace",
		RunE: func(_ *cobra.Command, a []string) error {
			c, err := NewFromEnv()
			if err != nil {
				return fail(localError(codeUnauthenticated, err), nil)
			}
			path, err := c.Download(a[0])
			if err != nil {
				return fail(asEnvelopeError(err), nil)
			}
			writeEnvelope(stdout, true, map[string]string{"path": path}, nil)
			return nil
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
	var agName, agRole, agDriver, agSrcConv, agProvider, agBaseURL, agModel string
	agentCreateCmd := &cobra.Command{
		Use: "create",
		RunE: func(_ *cobra.Command, _ []string) error {
			// api_key is never accepted on argv to keep secrets out of process listings.
			cfg := map[string]any{}
			switch agDriver {
			case "acp":
				cfg["provider"] = agProvider
			default:
				cfg["base_url"] = agBaseURL
				cfg["model"] = agModel
			}
			return call(http.MethodPost, "/agents", map[string]any{
				"display_name":           agName,
				"role_text":              agRole,
				"driver_kind":            agDriver,
				"driver_config":          cfg,
				"source_conversation_id": agSrcConv,
			})
		},
	}
	agentCreateCmd.Flags().StringVar(&agName, "name", "", "display_name")
	agentCreateCmd.Flags().StringVar(&agRole, "role", "", "role text")
	agentCreateCmd.Flags().StringVar(&agDriver, "driver", "builtin", "builtin|acp")
	agentCreateCmd.Flags().StringVar(&agProvider, "provider", "", "acp provider: pi|codex|claude")
	agentCreateCmd.Flags().StringVar(&agBaseURL, "base-url", "", "builtin OpenAI-compatible base URL")
	agentCreateCmd.Flags().StringVar(&agModel, "model", "", "builtin model")
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

	// Local bootstrap: sign in, ensure Space, pair this machine, write desktop config.
	var pairOpts pair.Options
	pairCmd := &cobra.Command{
		Use:   "pair",
		Short: "Pair this computer and write the desktop config",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if pairOpts.ConfigPath == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return fail(localError(codeInternal, err), nil)
				}
				pairOpts.ConfigPath = filepath.Join(home, ".sumi", "desktop.json")
				pairOpts.DataDir = filepath.Join(home, ".sumi", "v2")
			}
			norm := pairOpts.ServerURL
			if norm == "" {
				norm = "http://127.0.0.1:8087"
			}
			pairOpts.ServerURL = norm
			res, err := pair.Run(pairOpts)
			if err != nil {
				return fail(localError(codeUnavailable, err), nil)
			}
			writeEnvelope(stdout, true, res, nil)
			fmt.Fprintln(cmd.ErrOrStderr(), "computer paired; start the desktop app with `make app`")
			return nil
		},
	}
	pairCmd.Flags().StringVar(&pairOpts.ServerURL, "server", "http://127.0.0.1:8087", "Sumi server URL")
	pairCmd.Flags().StringVar(&pairOpts.Email, "email", "", "account email")
	pairCmd.Flags().StringVar(&pairOpts.Password, "password", "", "account password")
	pairCmd.Flags().StringVar(&pairOpts.DisplayName, "display-name", "", "display name for a new account")
	pairCmd.Flags().StringVar(&pairOpts.SpaceSlug, "space", "studio", "space slug")
	pairCmd.Flags().StringVar(&pairOpts.SpaceName, "space-name", "", "space name when creating it")
	pairCmd.Flags().StringVar(&pairOpts.ConfigPath, "config", "", "desktop config path (default ~/.sumi/desktop.json)")
	pairCmd.Flags().StringVar(&pairOpts.DataDir, "data-dir", "", "desktop data directory (default ~/.sumi)")
	_ = pairCmd.MarkFlagRequired("email")
	_ = pairCmd.MarkFlagRequired("password")
	root.AddCommand(pairCmd)

	root.SetArgs(args)
	if err := root.Execute(); err != nil && exitCode == 0 {
		exitCode = 2
		writeEnvelope(stdout, false, nil, localError(codeInvalidArgument, err))
	}
	return exitCode
}

func writeEnvelope(w io.Writer, ok bool, data any, e *EnvelopeError) {
	env := map[string]any{"ok": ok}
	if data != nil {
		env["data"] = data
	}
	if e != nil {
		env["error"] = e
	}
	_ = json.NewEncoder(w).Encode(env)
}
