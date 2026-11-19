package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultServerURL = "http://127.0.0.1:8087"

// Config is the desktop shell's only persistent state. It is written by `sumi pair`.
type Config struct {
	ServerURL     string `json:"server_url"`
	ComputerToken string `json:"computer_token"`
	DataDir       string `json:"data_dir"`
}

func configPath() string {
	if custom := os.Getenv("SUMI_DESKTOP_CONFIG"); custom != "" {
		return custom
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "desktop.json"
	}
	return filepath.Join(home, ".sumi", "desktop.json")
}

func loadConfig() (Config, error) {
	path := configPath()
	cfg := Config{ServerURL: defaultServerURL}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", path, err)
		}
	case os.IsNotExist(err):
		// First run: defaults are enough to boot an in-process Server.
	default:
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	cfg.ServerURL = strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	if cfg.ServerURL == "" {
		cfg.ServerURL = defaultServerURL
	}
	if cfg.DataDir == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return Config{}, fmt.Errorf("resolve home directory: %w", homeErr)
		}
		cfg.DataDir = filepath.Join(home, ".sumi")
	}
	return cfg, nil
}
