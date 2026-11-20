package computer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestPreparePiRuntimeDirIsolatesHostExtensionsAndSkills(t *testing.T) {
	hostPi := filepath.Join(t.TempDir(), "host-pi")
	if err := os.MkdirAll(filepath.Join(hostPi, "skills", "personal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hostPi, "extensions"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(hostPi, "auth.json"), `{"provider":"demo"}`)
	mustWrite(t, filepath.Join(hostPi, "models.json"), `{"providers":{}}`)
	mustWrite(t, filepath.Join(hostPi, "AGENTS.md"), "host instructions")
	mustWrite(t, filepath.Join(hostPi, "settings.json"), `{
		"defaultProvider": "openai",
		"defaultModel": "gpt-5",
		"defaultThinkingLevel": "medium",
		"packages": ["npm:some-plugin"],
		"extensions": ["/tmp/ext.ts"],
		"skills": ["/tmp/skills"],
		"prompts": ["/tmp/prompts"]
	}`)
	t.Setenv("PI_CODING_AGENT_DIR", hostPi)

	agentHome := filepath.Join(t.TempDir(), "agent-1")
	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: domain.ACPProviderPi}); err != nil {
		t.Fatalf("prepare pi runtime dir: %v", err)
	}

	rtDir := acpRuntimeDir(agentHome, domain.ACPProviderPi)
	for _, name := range []string{"auth.json", "models.json"} {
		target, err := os.Readlink(filepath.Join(rtDir, name))
		if err != nil || target != filepath.Join(hostPi, name) {
			t.Fatalf("%s symlink=%q err=%v", name, target, err)
		}
	}
	for _, leaked := range []string{"skills", "extensions", "AGENTS.md"} {
		if _, err := os.Lstat(filepath.Join(rtDir, leaked)); !os.IsNotExist(err) {
			t.Fatalf("host %s must not exist in isolated pi dir", leaked)
		}
	}

	raw, err := os.ReadFile(filepath.Join(rtDir, "settings.json"))
	if err != nil {
		t.Fatalf("read filtered settings.json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	if got["defaultProvider"] != "openai" || got["defaultModel"] != "gpt-5" || got["defaultThinkingLevel"] != "medium" {
		t.Fatalf("unexpected allowed settings: %v", got)
	}
	for _, blocked := range []string{"packages", "extensions", "skills", "prompts"} {
		if _, ok := got[blocked]; ok {
			t.Fatalf("blocked key %q leaked into isolated settings.json", blocked)
		}
	}

	// Removing host auth.json cleans up the stale symlink on refresh.
	if err := os.Remove(filepath.Join(hostPi, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: domain.ACPProviderPi}); err != nil {
		t.Fatalf("refresh pi runtime dir: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(rtDir, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("stale auth.json symlink was not removed")
	}
}

func TestPreparePiRuntimeDirAppliesPerAgentProviderOverride(t *testing.T) {
	hostPi := filepath.Join(t.TempDir(), "host-pi")
	if err := os.MkdirAll(hostPi, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(hostPi, "auth.json"), `{}`)
	mustWrite(t, filepath.Join(hostPi, "models.json"), `{"providers":{}}`)
	mustWrite(t, filepath.Join(hostPi, "settings.json"), `{
		"defaultProvider": "deepseek",
		"defaultModel": "deepseek-chat",
		"packages": ["npm:host-only-plugin"],
		"extensions": ["/tmp/ext.ts"]
	}`)
	if err := os.MkdirAll(filepath.Join(hostPi, "npm", "node_modules", "pi-commandcode-provider"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(hostPi, "npm", "node_modules", "pi-commandcode-provider", "index.js"), "module.exports = 1")
	t.Setenv("PI_CODING_AGENT_DIR", hostPi)

	agentHome := filepath.Join(t.TempDir(), "agent-override")
	cfg := domain.DriverConfig{
		Provider:   domain.ACPProviderPi,
		PiProvider: "ak-opendesign-pi",
		PiModel:    "deepseek-v4.1-flash",
	}
	if err := prepareACPRuntimeDir(agentHome, cfg); err != nil {
		t.Fatalf("prepare pi runtime dir: %v", err)
	}
	rtDir := acpRuntimeDir(agentHome, domain.ACPProviderPi)
	got := readPiSettings(t, rtDir)
	if got["defaultProvider"] != "ak-opendesign-pi" || got["defaultModel"] != "deepseek-v4.1-flash" {
		t.Fatalf("override not applied: %v", got)
	}
	if _, ok := got["packages"]; ok {
		t.Fatalf("host packages must stay out unless requested: %v", got)
	}
	if _, err := os.Lstat(filepath.Join(rtDir, "npm")); !os.IsNotExist(err) {
		t.Fatal("host npm dir must not be linked without requested packages")
	}

	// Requesting a provider plugin links host npm and forwards exactly that package list.
	cfg.PiPackages = []string{"npm:pi-commandcode-provider"}
	if err := prepareACPRuntimeDir(agentHome, cfg); err != nil {
		t.Fatalf("prepare pi runtime dir with packages: %v", err)
	}
	target, err := os.Readlink(filepath.Join(rtDir, "npm"))
	if err != nil || target != filepath.Join(hostPi, "npm") {
		t.Fatalf("npm symlink=%q err=%v", target, err)
	}
	got = readPiSettings(t, rtDir)
	pkgs, _ := got["packages"].([]any)
	if len(pkgs) != 1 || pkgs[0] != "npm:pi-commandcode-provider" {
		t.Fatalf("packages = %v, want only the requested plugin", got["packages"])
	}

	// Dropping the override restores the host defaults and removes the npm link.
	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: domain.ACPProviderPi}); err != nil {
		t.Fatalf("prepare pi runtime dir without override: %v", err)
	}
	got = readPiSettings(t, rtDir)
	if got["defaultProvider"] != "deepseek" || got["defaultModel"] != "deepseek-chat" {
		t.Fatalf("host defaults must return: %v", got)
	}
}

func readPiSettings(t *testing.T, rtDir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(rtDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	return out
}

func TestPrepareCodexRuntimeDirFiltersConfigAndIsolatesHostState(t *testing.T) {
	hostCodex := filepath.Join(t.TempDir(), "host-codex")
	if err := os.MkdirAll(filepath.Join(hostCodex, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hostCodex, "plugins"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(hostCodex, "auth.json"), `{}`)
	mustWrite(t, filepath.Join(hostCodex, "models_cache.json"), `{}`)
	mustWrite(t, filepath.Join(hostCodex, "hooks.json"), `{}`)
	mustWrite(t, filepath.Join(hostCodex, "AGENTS.md"), "host rules")
	mustWrite(t, filepath.Join(hostCodex, "config.toml"), `
model = "gpt-5.4"
model_provider = "custom"
model_reasoning_effort = "high"
notify = ["osascript", "-e", "beep"]
developer_instructions = "host prompt"

[features]
codex_hooks = true

[model_providers.custom]
name = "Custom"
base_url = "https://example.invalid/v1"
env_key = "CUSTOM_KEY"

[model_providers.custom.http_headers]
X-Test = "1"

[mcp_servers.personal]
command = "npx"
args = ["-y", "personal-mcp"]

[projects."/Users/me/work"]
trust_level = "trusted"

[profiles.fast]
model = "gpt-5.4-mini"
`)
	t.Setenv("CODEX_HOME", hostCodex)

	agentHome := filepath.Join(t.TempDir(), "agent-2")
	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: domain.ACPProviderCodex}); err != nil {
		t.Fatalf("prepare codex runtime dir: %v", err)
	}

	rtDir := acpRuntimeDir(agentHome, domain.ACPProviderCodex)
	for _, name := range []string{"auth.json", "models_cache.json"} {
		target, err := os.Readlink(filepath.Join(rtDir, name))
		if err != nil || target != filepath.Join(hostCodex, name) {
			t.Fatalf("%s symlink=%q err=%v", name, target, err)
		}
	}
	for _, leaked := range []string{"skills", "plugins", "hooks.json", "AGENTS.md"} {
		if _, err := os.Lstat(filepath.Join(rtDir, leaked)); !os.IsNotExist(err) {
			t.Fatalf("host %s must not exist in isolated codex dir", leaked)
		}
	}

	raw, err := os.ReadFile(filepath.Join(rtDir, "config.toml"))
	if err != nil {
		t.Fatalf("read filtered config.toml: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`model = "gpt-5.4"`,
		`model_provider = "custom"`,
		`model_reasoning_effort = "high"`,
		`[model_providers.custom]`,
		`base_url = "https://example.invalid/v1"`,
		`[model_providers.custom.http_headers]`,
		`[profiles.fast]`,
		`model = "gpt-5.4-mini"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("filtered config.toml missing %q:\n%s", want, got)
		}
	}
	for _, blocked := range []string{
		"notify =",
		"developer_instructions",
		"[features]",
		"codex_hooks",
		"[mcp_servers.personal]",
		"personal-mcp",
		`[projects."/Users/me/work"]`,
		"trust_level",
	} {
		if strings.Contains(got, blocked) {
			t.Fatalf("filtered config.toml leaked %q:\n%s", blocked, got)
		}
	}
}

func TestPrepareClaudeRuntimeDirIsClean(t *testing.T) {
	agentHome := filepath.Join(t.TempDir(), "agent-3")
	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: domain.ACPProviderClaude}); err != nil {
		t.Fatalf("prepare claude runtime dir: %v", err)
	}
	rtDir := acpRuntimeDir(agentHome, domain.ACPProviderClaude)
	entries, err := os.ReadDir(rtDir)
	if err != nil {
		t.Fatalf("read claude runtime dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty claude runtime dir, got %d entries", len(entries))
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
