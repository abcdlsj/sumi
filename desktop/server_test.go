package main

import (
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
