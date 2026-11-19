package pair

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/server"
	"github.com/abcdlsj/sumi/internal/store"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "pair.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := app.NewService(st, app.NewEventHub(), nil)
	srv := server.New(svc, server.Config{}, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestRunRegistersPairsAndWritesConfig(t *testing.T) {
	ts := newTestServer(t)
	configPath := filepath.Join(t.TempDir(), "desktop.json")

	res, err := Run(Options{
		ServerURL:   ts.URL,
		Email:       "owner@sumi.dev",
		Password:    "password123",
		SpaceSlug:   "studio",
		SpaceName:   "Sumi Studio",
		ConfigPath:  configPath,
		DataDir:     t.TempDir(),
		DisplayName: "Lisi",
	})
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if !res.CreatedUser || !res.CreatedSpace || !res.PairedMachine {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.ComputerID == "" {
		t.Fatal("computer id must be returned")
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config must be 0600, got %o", perm)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "computer_token") || strings.Contains(string(raw), ts.URL+"token") {
		t.Fatalf("unexpected config contents: %s", raw)
	}

	// Running again reuses the account and space but pairs a fresh Computer.
	again, err := Run(Options{
		ServerURL:  ts.URL,
		Email:      "owner@sumi.dev",
		Password:   "password123",
		SpaceSlug:  "studio",
		ConfigPath: configPath,
		DataDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("second pair: %v", err)
	}
	if again.CreatedUser || again.CreatedSpace {
		t.Fatalf("second run must reuse account and space: %+v", again)
	}
	if again.ComputerID == res.ComputerID {
		t.Fatal("each run must pair a distinct computer")
	}
}

func TestRunRequiresCredentials(t *testing.T) {
	if _, err := Run(Options{ServerURL: "http://127.0.0.1:1"}); err == nil {
		t.Fatal("missing credentials must fail before any request")
	}
}

func TestRunSurfacesServerErrors(t *testing.T) {
	ts := newTestServer(t)
	_, err := Run(Options{
		ServerURL:  ts.URL,
		Email:      "not-an-email",
		Password:   "password123",
		ConfigPath: filepath.Join(t.TempDir(), "desktop.json"),
		DataDir:    t.TempDir(),
	})
	if err == nil {
		t.Fatal("invalid email must fail")
	}
	if _, statErr := os.Stat(filepath.Join(t.TempDir(), "desktop.json")); statErr == nil {
		t.Fatal("no config should be written on failure")
	}
	_ = http.StatusOK
}
