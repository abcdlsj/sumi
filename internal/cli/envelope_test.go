package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// serveSocket runs h on a unix socket and points the CLI environment at it.
func serveSocket(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sumi-cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("SUMI_SOCKET", sock)
	t.Setenv("SUMI_DRIVER_TOKEN", "tok")
	// Held sends write drafts; keep them out of the package directory.
	if os.Getenv("SUMI_DRAFT_DIR") == "" {
		t.Setenv("SUMI_DRAFT_DIR", t.TempDir())
	}
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *EnvelopeError  `json:"error"`
}

func runCLI(t *testing.T, args ...string) (envelope, int) {
	t.Helper()
	var out bytes.Buffer
	code := Run(args, &out)
	var env envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", out.String(), err)
	}
	return env, code
}

func TestEnvelopeCarriesServerErrorCode(t *testing.T) {
	serveSocket(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"permission_denied","message":"agent.create permission required"}}`))
	})
	env, code := runCLI(t, "agent", "create", "--name", "x", "--role", "y")
	if env.OK || code != 1 || env.Error == nil {
		t.Fatalf("ok=%v code=%d err=%v", env.OK, code, env.Error)
	}
	if env.Error.Code != "permission_denied" || env.Error.Message != "agent.create permission required" || env.Error.Hint == "" {
		t.Fatalf("error=%+v", env.Error)
	}
}

func TestEnvelopeReportsHeldWithUnreadData(t *testing.T) {
	serveSocket(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"held":true,"unread_messages":[{"id":"m1","seq":3}],"omitted_unread_count":2}`))
	})
	env, _ := runCLI(t, "send", "conv-1", "--body", "hi")
	if env.OK || env.Error == nil || env.Error.Code != "held" {
		t.Fatalf("env=%+v", env)
	}
	var data struct {
		Unread  []map[string]any `json:"unread_messages"`
		Omitted int              `json:"omitted_unread_count"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil || len(data.Unread) != 1 || data.Omitted != 2 {
		t.Fatalf("held data=%s err=%v", env.Data, err)
	}
}

func TestEnvelopeLocalErrors(t *testing.T) {
	t.Setenv("SUMI_SOCKET", "")
	t.Setenv("SUMI_DRIVER_TOKEN", "")
	env, code := runCLI(t, "inbox")
	if code != 1 || env.Error == nil || env.Error.Code != "unauthenticated" {
		t.Fatalf("missing env: code=%d err=%+v", code, env.Error)
	}
	env, code = runCLI(t, "read")
	if code != 2 || env.Error == nil || env.Error.Code != "invalid_argument" {
		t.Fatalf("usage error: code=%d err=%+v", code, env.Error)
	}
}

func TestHeldSendSavesDraftAndDraftResendsWithSameKey(t *testing.T) {
	t.Setenv("SUMI_DRAFT_DIR", t.TempDir())
	type req struct {
		Key  string
		Body string
	}
	var seen []req
	serveSocket(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BodyMarkdown string `json:"body_markdown"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, req{Key: r.Header.Get("Idempotency-Key"), Body: body.BodyMarkdown})
		if len(seen) == 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"held":true,"unread_messages":[]}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"message":{"id":"m9"}}`))
	})

	env, _ := runCLI(t, "send", "conv-1", "--body", "the answer")
	if env.Error == nil || env.Error.Code != "held" || !bytes.Contains(env.Data, []byte(`"draft_saved":true`)) {
		t.Fatalf("held send must save a draft: %+v data=%s", env.Error, env.Data)
	}
	if env, _ := runCLI(t, "send", "conv-1", "--draft", "--body", "x"); env.Error == nil || env.Error.Code != "invalid_argument" {
		t.Fatalf("--draft with --body must be rejected: %+v", env.Error)
	}
	env, code := runCLI(t, "send", "conv-1", "--draft")
	if !env.OK || code != 0 {
		t.Fatalf("draft resend failed: %+v", env.Error)
	}
	if len(seen) != 2 || seen[1].Body != "the answer" || seen[1].Key == "" || seen[1].Key != seen[0].Key {
		t.Fatalf("draft must resend the same body and key: %+v", seen)
	}
	if env, _ := runCLI(t, "send", "conv-1", "--draft"); env.Error == nil || env.Error.Code != "invalid_argument" {
		t.Fatalf("draft must be deleted after a successful send: %+v", env.Error)
	}
	if _, err := draftPath("../escape"); err == nil {
		t.Fatalf("conversation ids must not escape the draft dir")
	}
}

func TestDraftSurvivesUnrelatedSendAndIsReplacedWithBackup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SUMI_DRAFT_DIR", dir)
	held := map[string]bool{}
	serveSocket(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BodyMarkdown string `json:"body_markdown"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if held[body.BodyMarkdown] {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"held":true,"unread_messages":[]}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"message":{"id":"m1"}}`))
	})
	held["first"], held["second"] = true, true

	if env, _ := runCLI(t, "send", "conv-1", "--body", "first"); env.Error == nil || env.Error.Code != "held" {
		t.Fatalf("first send should be held: %+v", env.Error)
	}
	// An unrelated successful send keeps the draft and flags it as pending.
	env, _ := runCLI(t, "send", "conv-1", "--body", "VOTE: bram")
	if !env.OK || !bytes.Contains(env.Data, []byte(`"draft_pending":true`)) {
		t.Fatalf("unrelated send must keep the draft: ok=%v data=%s", env.OK, env.Data)
	}
	if d, err := loadDraft("conv-1"); err != nil || d.BodyMarkdown != "first" {
		t.Fatalf("draft lost after unrelated send: %+v %v", d, err)
	}

	// A different held send replaces the draft but archives the old body.
	env, _ = runCLI(t, "send", "conv-1", "--body", "second")
	if !bytes.Contains(env.Data, []byte(`"replaced_draft"`)) {
		t.Fatalf("replacing a draft must report the backup: %s", env.Data)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "replaced", "conv-1-*.json"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}

	if env, _ := runCLI(t, "send", "conv-1", "--discard-draft", "--body", "x"); env.Error == nil {
		t.Fatal("--discard-draft with --body must be rejected")
	}
	if env, _ := runCLI(t, "send", "conv-1", "--discard-draft"); !env.OK {
		t.Fatalf("discard failed: %+v", env.Error)
	}
	if _, err := loadDraft("conv-1"); err == nil {
		t.Fatal("draft must be gone after --discard-draft")
	}
}

func TestExpiredDraftCannotBeResent(t *testing.T) {
	t.Setenv("SUMI_DRAFT_DIR", t.TempDir())
	serveSocket(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an expired draft must not reach the server")
	})
	if _, err := saveDraft(draft{ConversationID: "conv-1", BodyMarkdown: "old", IdempotencyKey: "k", SavedAt: time.Now().Add(-draftTTL - time.Minute)}); err != nil {
		t.Fatal(err)
	}
	env, _ := runCLI(t, "send", "conv-1", "--draft")
	if env.Error == nil || !bytes.Contains(env.Data, []byte(`"draft_expired":true`)) || !bytes.Contains(env.Data, []byte(`"old"`)) {
		t.Fatalf("expired draft: %+v data=%s", env.Error, env.Data)
	}
	if _, err := loadDraft("conv-1"); err == nil {
		t.Fatal("expired draft must be removed")
	}
}
