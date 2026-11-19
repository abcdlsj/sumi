package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "desktop.json")
	t.Setenv("SUMI_DESKTOP_CONFIG", path)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.ServerURL != defaultServerURL {
		t.Fatalf("unexpected default server url %q", cfg.ServerURL)
	}
	if cfg.ComputerToken != "" {
		t.Fatalf("token must default to empty, got %q", cfg.ComputerToken)
	}
	if cfg.DataDir == "" {
		t.Fatal("data dir must default to a real path")
	}

	if err := os.WriteFile(path, []byte(`{"server_url":"http://127.0.0.1:9000/","computer_token":"tok","data_dir":"/tmp/x"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err = loadConfig()
	if err != nil {
		t.Fatalf("load explicit config: %v", err)
	}
	if cfg.ServerURL != "http://127.0.0.1:9000" {
		t.Fatalf("server url must be trimmed: %q", cfg.ServerURL)
	}
	if cfg.DataDir != "/tmp/x" {
		t.Fatalf("explicit data dir must win: %q", cfg.DataDir)
	}

	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("broken config must fail loudly")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "LOCALHOST", "::1", "[::1]"} {
		if !isLoopbackHost(host) {
			t.Fatalf("%q should be loopback", host)
		}
	}
	for _, host := range []string{"0.0.0.0", "api.sumi.dev", "192.168.1.4", ""} {
		if isLoopbackHost(host) {
			t.Fatalf("%q must not be treated as loopback", host)
		}
	}
}

func TestSPAHandlerServesIndexAndAssets(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	handler := spaHandler(assets)

	cases := []struct {
		path     string
		wantCode int
		wantBody string
	}{
		{"/", http.StatusOK, "<html>app</html>"},
		{"/assets/app.js", http.StatusOK, "console.log(1)"},
		{"/some/client/route", http.StatusOK, "<html>app</html>"},
		{"/api/v1/health", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.wantCode {
			t.Fatalf("%s: status %d, want %d", tc.path, rec.Code, tc.wantCode)
		}
		if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
			t.Fatalf("%s: body %q, want %q", tc.path, rec.Body.String(), tc.wantBody)
		}
	}
}

func TestEmbeddedAssetsResolve(t *testing.T) {
	assets := webAssets()
	if _, err := assets.Open("index.html"); err != nil {
		t.Fatalf("web assets must always contain index.html: %v", err)
	}
}

func TestRuntimeStatusStartsUnpaired(t *testing.T) {
	rt := newRuntime(Config{ServerURL: defaultServerURL, DataDir: t.TempDir()}, slog.New(slog.DiscardHandler))
	if rt.status() != "not paired" {
		t.Fatalf("unexpected initial status %q", rt.status())
	}
	rt.shutdown()
}
