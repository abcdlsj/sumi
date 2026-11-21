package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
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
	if cfg.Servers["http://127.0.0.1:9000"].ComputerToken != "tok" {
		t.Fatal("legacy computer_token must populate servers map")
	}

	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("broken config must fail loudly")
	}
}

func TestNormalizeServerURL(t *testing.T) {
	valid := map[string]string{
		"":                           defaultServerURL,
		"local":                      defaultServerURL,
		"LOCAL":                      defaultServerURL,
		"https://sumi.example.com/":  "https://sumi.example.com",
		"http://10.0.0.5:8087":       "http://10.0.0.5:8087",
		"  HTTPS://Sumi.Example.Com": "https://Sumi.Example.Com",
	}
	for in, want := range valid {
		got, err := normalizeServerURL(in)
		if err != nil {
			t.Fatalf("normalizeServerURL(%q) unexpected error: %v", in, err)
		}
		if got != want {
			t.Fatalf("normalizeServerURL(%q) = %q, want %q", in, got, want)
		}
	}
	invalid := []string{
		"ftp://example.com",
		"sumi.example.com",
		"https://user:pass@example.com",
		"https://example.com/subpath",
		"https://example.com?a=1",
		"https://example.com#frag",
		"http://",
	}
	for _, in := range invalid {
		if got, err := normalizeServerURL(in); err == nil {
			t.Fatalf("normalizeServerURL(%q) = %q, expected error", in, got)
		}
	}
}

func TestSwitchServerPreservesTokensAndGuardsUnreachable(t *testing.T) {
	reachable := map[string]bool{
		defaultServerURL:       true,
		"https://sumi.team":    true,
		"https://offline.team": false,
	}
	rt := newRuntime(Config{
		ServerURL:     defaultServerURL,
		ComputerToken: "tok-local",
		DataDir:       t.TempDir(),
		Servers: map[string]ServerProfile{
			defaultServerURL:    {ComputerToken: "tok-local"},
			"https://sumi.team": {ComputerToken: "tok-remote"},
		},
	}, slog.New(slog.DiscardHandler))
	rt.healthCheck = func(_ context.Context, u string) bool { return reachable[u] }
	defer rt.shutdown()

	// Unreachable target must fail without altering current ServerURL or token.
	if _, err := rt.switchServer(context.Background(), "https://offline.team"); !errors.Is(err, errServerUnreachable) {
		t.Fatalf("expected errServerUnreachable, got %v", err)
	}
	if rt.serverURL() != defaultServerURL || rt.configSnapshot().ComputerToken != "tok-local" {
		t.Fatal("failed switch must preserve active server and token")
	}

	// Switch to paired remote restores its saved token.
	cfg, err := rt.switchServer(context.Background(), "https://sumi.team")
	if err != nil {
		t.Fatalf("switch to remote: %v", err)
	}
	if cfg.ServerURL != "https://sumi.team" || cfg.ComputerToken != "tok-remote" {
		t.Fatalf("unexpected remote config: %+v", cfg)
	}

	// Switch back to local restores local token.
	cfg, err = rt.switchServer(context.Background(), "local")
	if err != nil {
		t.Fatalf("switch back to local: %v", err)
	}
	if cfg.ServerURL != defaultServerURL || cfg.ComputerToken != "tok-local" {
		t.Fatalf("unexpected local config: %+v", cfg)
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
