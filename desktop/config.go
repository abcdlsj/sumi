package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const defaultServerURL = "http://127.0.0.1:8087"

// ServerProfile holds per-server state so switching between Local and Remote
// preserves each server's paired computer token.
type ServerProfile struct {
	ComputerToken string `json:"computer_token,omitempty"`
}

// Config is the desktop shell's persistent state, written by pairing, server switching, or `sumi pair`.
type Config struct {
	ServerURL     string                   `json:"server_url"`
	ComputerToken string                   `json:"computer_token"`
	DataDir       string                   `json:"data_dir"`
	Servers       map[string]ServerProfile `json:"servers,omitempty"`
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

// normalizeServerURL canonicalizes a user-provided server address to scheme://host[:port].
// Empty string and "local" resolve to defaultServerURL.
func normalizeServerURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.EqualFold(trimmed, "local") {
		return defaultServerURL, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("server URL must start with http:// or https://")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", errors.New("server URL must include a host")
	}
	if parsed.User != nil {
		return "", errors.New("server URL must not include credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("server URL must be an origin without path, query, or fragment")
	}
	return scheme + "://" + parsed.Host, nil
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
	if cfg.Servers == nil {
		cfg.Servers = map[string]ServerProfile{}
	}
	if cfg.ComputerToken != "" && cfg.Servers[cfg.ServerURL].ComputerToken == "" {
		cfg.Servers[cfg.ServerURL] = ServerProfile{ComputerToken: cfg.ComputerToken}
	} else if cfg.ComputerToken == "" && cfg.Servers[cfg.ServerURL].ComputerToken != "" {
		cfg.ComputerToken = cfg.Servers[cfg.ServerURL].ComputerToken
	}
	return cfg, nil
}

func (c Config) clone() Config {
	out := c
	out.Servers = make(map[string]ServerProfile, len(c.Servers))
	for k, v := range c.Servers {
		out.Servers[k] = v
	}
	return out
}

func (c Config) withServer(serverURL string) Config {
	out := c.clone()
	if out.ServerURL != "" && out.ComputerToken != "" {
		out.Servers[out.ServerURL] = ServerProfile{ComputerToken: out.ComputerToken}
	}
	out.ServerURL = serverURL
	out.ComputerToken = out.Servers[serverURL].ComputerToken
	return out
}

func (c Config) withToken(serverURL, token string) Config {
	out := c.clone()
	out.ServerURL = serverURL
	out.ComputerToken = token
	if token != "" {
		out.Servers[serverURL] = ServerProfile{ComputerToken: token}
	} else {
		delete(out.Servers, serverURL)
	}
	return out
}
