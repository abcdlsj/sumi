package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// adoptLoginShellPath replaces PATH with the user's login-shell PATH. Apps launched from
// Finder inherit a minimal PATH without Homebrew, so ACP agents (pi, codex, claude) would
// otherwise be invisible to the daemon.
func adoptLoginShellPath() {
	shell := os.Getenv("SHELL")
	if shell == "" || !filepath.IsAbs(shell) {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, "-l", "-c", `printf '%s' "$PATH"`).Output()
	if err != nil {
		return
	}
	if p := loginPath(string(out), os.Getenv("PATH")); p != "" {
		_ = os.Setenv("PATH", p)
	}
}

// loginPath keeps the login-shell entries first and appends any current entries it lacks.
func loginPath(login, current string) string {
	login = strings.TrimSpace(login)
	if login == "" || strings.ContainsAny(login, "\n\r") {
		return ""
	}
	seen := map[string]bool{}
	var parts []string
	for _, p := range append(strings.Split(login, ":"), strings.Split(current, ":")...) {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		parts = append(parts, p)
	}
	return strings.Join(parts, ":")
}
