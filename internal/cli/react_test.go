package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIReactAddRemoveAndValidation(t *testing.T) {
	// Keep unix socket path under macOS's 104-byte sun_path limit.
	dir, err := os.MkdirTemp("", "sumi-cli")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	var gotMethod, gotPath string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reactions":[{"emoji":"rocket","count":1,"member_ids":["a1"],"reacted":true}]}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	t.Setenv("SUMI_SOCKET", sock)
	t.Setenv("SUMI_DRIVER_TOKEN", "tok-test")

	// Add reaction -> PUT
	var out bytes.Buffer
	if code := Run([]string{"react", "msg-42", "rocket"}, &out); code != 0 {
		t.Fatalf("react add exit=%d out=%s", code, out.String())
	}
	if gotMethod != http.MethodPut || gotPath != "/messages/msg-42/reactions/rocket" {
		t.Fatalf("got %s %s", gotMethod, gotPath)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || env["ok"] != true {
		t.Fatalf("unexpected envelope: %s", out.String())
	}

	// Remove reaction -> DELETE
	out.Reset()
	if code := Run([]string{"react", "msg-42", "rocket", "--remove"}, &out); code != 0 {
		t.Fatalf("react remove exit=%d out=%s", code, out.String())
	}
	if gotMethod != http.MethodDelete || gotPath != "/messages/msg-42/reactions/rocket" {
		t.Fatalf("got %s %s", gotMethod, gotPath)
	}

	// Invalid emoji fails locally before hitting socket.
	out.Reset()
	gotMethod = ""
	if code := Run([]string{"react", "msg-42", "unknown"}, &out); code == 0 {
		t.Fatalf("expected non-zero exit for invalid emoji, out=%s", out.String())
	}
	if gotMethod != "" {
		t.Fatalf("invalid emoji should not send HTTP request, got %s", gotMethod)
	}
}
