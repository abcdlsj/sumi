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
	for _, blocked := range []string{"extensions", "skills", "prompts"} {
		if _, ok := got[blocked]; ok {
			t.Fatalf("blocked key %q leaked into isolated settings.json", blocked)
		}
	}
	// Following the host default model brings the host npm: plugins that may provide it.
	if pkgs, _ := got["packages"].([]any); len(pkgs) != 1 || pkgs[0] != "npm:some-plugin" {
		t.Fatalf("packages = %v, want host npm: plugins", got["packages"])
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
		"packages": ["npm:host-only-plugin", "git:github.com/x/y", {"source": "npm:obj"}],
		"extensions": ["/tmp/ext.ts"]
	}`)
	if err := os.MkdirAll(filepath.Join(hostPi, "npm", "node_modules", "custom-provider"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(hostPi, "npm", "node_modules", "custom-provider", "index.js"), "module.exports = 1")
	t.Setenv("PI_CODING_AGENT_DIR", hostPi)

	agentHome := filepath.Join(t.TempDir(), "agent-override")
	cfg := domain.DriverConfig{
		Provider:   domain.ACPProviderPi,
		PiProvider: "custom-pi",
		PiModel:    "model-fast",
	}
	if err := prepareACPRuntimeDir(agentHome, cfg); err != nil {
		t.Fatalf("prepare pi runtime dir: %v", err)
	}
	rtDir := acpRuntimeDir(agentHome, domain.ACPProviderPi)
	got := readPiSettings(t, rtDir)
	if got["defaultProvider"] != "custom-pi" || got["defaultModel"] != "model-fast" {
		t.Fatalf("override not applied: %v", got)
	}
	if _, ok := got["packages"]; ok {
		t.Fatalf("host packages must stay out unless requested: %v", got)
	}
	if _, err := os.Lstat(filepath.Join(rtDir, "npm")); !os.IsNotExist(err) {
		t.Fatal("host npm dir must not be linked without requested packages")
	}

	// Requesting a provider plugin links host npm and forwards exactly that package list.
	cfg.PiPackages = []string{"npm:custom-provider"}
	if err := prepareACPRuntimeDir(agentHome, cfg); err != nil {
		t.Fatalf("prepare pi runtime dir with packages: %v", err)
	}
	target, err := os.Readlink(filepath.Join(rtDir, "npm"))
	if err != nil || target != filepath.Join(hostPi, "npm") {
		t.Fatalf("npm symlink=%q err=%v", target, err)
	}
	got = readPiSettings(t, rtDir)
	pkgs, _ := got["packages"].([]any)
	if len(pkgs) != 1 || pkgs[0] != "npm:custom-provider" {
		t.Fatalf("packages = %v, want only the requested plugin", got["packages"])
	}

	// Dropping the override follows the host: host defaults and host npm: plugins only.
	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: domain.ACPProviderPi}); err != nil {
		t.Fatalf("prepare pi runtime dir without override: %v", err)
	}
	got = readPiSettings(t, rtDir)
	if got["defaultProvider"] != "deepseek" || got["defaultModel"] != "deepseek-chat" {
		t.Fatalf("host defaults must return: %v", got)
	}
	if pkgs, _ := got["packages"].([]any); len(pkgs) != 1 || pkgs[0] != "npm:host-only-plugin" {
		t.Fatalf("packages = %v, want only host npm: specs", got["packages"])
	}
	if target, err := os.Readlink(filepath.Join(rtDir, "npm")); err != nil || target != filepath.Join(hostPi, "npm") {
		t.Fatalf("npm symlink=%q err=%v", target, err)
	}

	// A model-only override still follows the host provider and its plugins.
	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: domain.ACPProviderPi, PiModel: "deepseek-reasoner"}); err != nil {
		t.Fatal(err)
	}
	if got = readPiSettings(t, rtDir); got["defaultModel"] != "deepseek-reasoner" || got["packages"] == nil {
		t.Fatalf("model-only override settings=%v", got)
	}

	// Pinning a provider without packages drops host plugins and the npm link.
	if err := prepareACPRuntimeDir(agentHome, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.PiPackages = nil
	if err := prepareACPRuntimeDir(agentHome, cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPiSettings(t, rtDir)["packages"]; ok {
		t.Fatal("pinned provider must not inherit host packages")
	}
	if _, err := os.Lstat(filepath.Join(rtDir, "npm")); !os.IsNotExist(err) {
		t.Fatal("npm link must be removed when no packages load")
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

func TestPreparePiRuntimeDirAppliesContextWindow(t *testing.T) {
	hostPi := filepath.Join(t.TempDir(), "host-pi")
	if err := os.MkdirAll(hostPi, 0o700); err != nil {
		t.Fatal(err)
	}
	hostModels := `{"providers":{"custom":{"baseUrl":"https://x.invalid","models":[{"id":"m1","contextWindow":1000000}]}}}`
	mustWrite(t, filepath.Join(hostPi, "models.json"), hostModels)
	mustWrite(t, filepath.Join(hostPi, "settings.json"), `{"defaultProvider":"custom","defaultModel":"m1"}`)
	t.Setenv("PI_CODING_AGENT_DIR", hostPi)
	agentHome := filepath.Join(t.TempDir(), "agent-cw")
	rtModels := filepath.Join(acpRuntimeDir(agentHome, domain.ACPProviderPi), "models.json")

	readModels := func() map[string]any {
		t.Helper()
		if fi, err := os.Lstat(rtModels); err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("models.json must be a private copy: %v", err)
		}
		raw, err := os.ReadFile(rtModels)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc["providers"].(map[string]any)
	}

	// Host default model defined in models.json: window is set inline.
	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: "pi", ContextWindow: 272_000}); err != nil {
		t.Fatal(err)
	}
	m := readModels()["custom"].(map[string]any)["models"].([]any)[0].(map[string]any)
	if m["contextWindow"] != float64(272_000) || m["id"] != "m1" {
		t.Fatalf("inline model=%v", m)
	}

	// Built-in model: window goes to modelOverrides.
	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: "pi", PiProvider: "anthropic", PiModel: "claude-x", ContextWindow: 200_000}); err != nil {
		t.Fatal(err)
	}
	ov := readModels()["anthropic"].(map[string]any)["modelOverrides"].(map[string]any)["claude-x"].(map[string]any)
	if ov["contextWindow"] != float64(200_000) {
		t.Fatalf("override=%v", ov)
	}

	if raw, _ := os.ReadFile(filepath.Join(hostPi, "models.json")); string(raw) != hostModels {
		t.Fatalf("host models.json modified: %s", raw)
	}

	// Clearing the window restores the host symlink.
	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: "pi"}); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(rtModels); err != nil || target != filepath.Join(hostPi, "models.json") {
		t.Fatalf("models.json symlink=%q err=%v", target, err)
	}
}

func TestPrepareCodexRuntimeDirAppliesContextWindow(t *testing.T) {
	hostCodex := filepath.Join(t.TempDir(), "host-codex")
	if err := os.MkdirAll(hostCodex, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(hostCodex, "config.toml"), "model = \"gpt-5.4\"\nmodel_context_window = 1000000\n\n[profiles.fast]\nmodel = \"mini\"\n")
	t.Setenv("CODEX_HOME", hostCodex)
	agentHome := filepath.Join(t.TempDir(), "agent-cw")
	cfgPath := filepath.Join(acpRuntimeDir(agentHome, domain.ACPProviderCodex), "config.toml")

	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(cfgPath); !strings.Contains(string(raw), "model_context_window = 1000000") {
		t.Fatalf("host window must pass through without an override:\n%s", raw)
	}

	if err := prepareACPRuntimeDir(agentHome, domain.DriverConfig{Provider: "codex", ContextWindow: 400_000}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cfgPath)
	got := string(raw)
	want := "model_context_window = 400000\nmodel_auto_compact_token_limit = 300000\nmodel = \"gpt-5.4\"\n"
	if !strings.HasPrefix(got, want) || strings.Contains(got, "1000000") || !strings.Contains(got, "[profiles.fast]") {
		t.Fatalf("config.toml=\n%s", got)
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
