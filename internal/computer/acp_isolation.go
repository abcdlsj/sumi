package computer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/abcdlsj/sumi/internal/domain"
)

// acpRuntimeDir returns the isolated config directory under Agent Home for a known ACP provider.
func acpRuntimeDir(agentHome, provider string) string {
	switch provider {
	case domain.ACPProviderPi, domain.ACPProviderCodex, domain.ACPProviderClaude:
		return filepath.Join(agentHome, "runtime", provider)
	default:
		return ""
	}
}

// prepareACPRuntimeDir creates the provider's isolated runtime directory under Agent Home and
// syncs only auth/model config from the host so host skills, hooks, plugins and sessions do not leak.
func prepareACPRuntimeDir(agentHome string, cfg domain.DriverConfig) error {
	runtimeDir := acpRuntimeDir(agentHome, cfg.Provider)
	if runtimeDir == "" {
		return nil
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return fmt.Errorf("create acp runtime dir: %w", err)
	}
	userHome, _ := os.UserHomeDir()
	switch cfg.Provider {
	case domain.ACPProviderPi:
		hostDir := resolveHostDir("PI_CODING_AGENT_DIR", filepath.Join(userHome, ".pi", "agent"))
		return syncPiRuntimeDir(hostDir, runtimeDir, cfg)
	case domain.ACPProviderCodex:
		hostDir := resolveHostDir("CODEX_HOME", filepath.Join(userHome, ".codex"))
		return syncCodexRuntimeDir(hostDir, runtimeDir, cfg.ContextWindow)
	case domain.ACPProviderClaude:
		// Claude uses API env vars; keep CLAUDE_CONFIG_DIR empty of host hooks/skills/plugins.
		// context_window travels as CLAUDE_CODE_AUTO_COMPACT_WINDOW set by the claude driver.
		return nil
	default:
		return nil
	}
}

func resolveHostDir(envKey, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return filepath.Clean(v)
	}
	return filepath.Clean(fallback)
}

func sameDir(a, b string) bool {
	ca, errA := filepath.Abs(a)
	cb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ca == cb
}

// syncPiRuntimeDir symlinks host auth/models and writes a minimal settings.json with only model,
// transport and package preferences, omitting skills, hooks and prompts.
func syncPiRuntimeDir(hostDir, runtimeDir string, cfg domain.DriverConfig) error {
	if hostDir == "" || sameDir(hostDir, runtimeDir) {
		return nil
	}
	if err := syncFileSymlink(filepath.Join(hostDir, "auth.json"), filepath.Join(runtimeDir, "auth.json")); err != nil {
		return err
	}
	if err := syncPiModels(hostDir, runtimeDir, cfg); err != nil {
		return err
	}
	packages, err := piPackages(filepath.Join(hostDir, "settings.json"), cfg)
	if err != nil {
		return err
	}
	// Plugins, requested or followed from the host, must be loadable from the isolated dir.
	npmDst := filepath.Join(runtimeDir, "npm")
	if len(packages) > 0 {
		if err := syncDirSymlink(filepath.Join(hostDir, "npm"), npmDst); err != nil {
			return err
		}
	} else if fi, err := os.Lstat(npmDst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := removeIfExists(npmDst); err != nil {
			return err
		}
	}
	return syncPiSettings(filepath.Join(hostDir, "settings.json"), filepath.Join(runtimeDir, "settings.json"), cfg, packages)
}

// piPackages returns the plugins to load. An agent that sets neither pi_provider nor
// pi_packages follows the host default model, whose provider often comes from a host plugin,
// so it inherits the host npm: packages; otherwise only the agent's own list loads.
func piPackages(hostSettings string, cfg domain.DriverConfig) ([]string, error) {
	if cfg.PiProvider != "" || len(cfg.PiPackages) > 0 {
		return cfg.PiPackages, nil
	}
	raw, err := os.ReadFile(hostSettings)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var src struct {
		Packages []any `json:"packages"`
	}
	_ = json.Unmarshal(raw, &src)
	var out []string
	for _, p := range src.Packages {
		// Same npm:-only rule as pi_packages; other sources are not forwarded.
		if s, ok := p.(string); ok && strings.HasPrefix(s, "npm:") && len(s) > len("npm:") {
			out = append(out, s)
		}
	}
	return out, nil
}

var piAllowedSettingsKeys = []string{
	"defaultProvider",
	"defaultModel",
	"defaultThinkingLevel",
	"transport",
	"compaction",
}

func syncPiSettings(srcPath, dstPath string, cfg domain.DriverConfig, packages []string) error {
	filtered := map[string]any{}
	raw, err := os.ReadFile(srcPath)
	switch {
	case err == nil:
		var src map[string]any
		if json.Unmarshal(raw, &src) == nil {
			for _, k := range piAllowedSettingsKeys {
				if v, ok := src[k]; ok {
					filtered[k] = v
				}
			}
		}
	case os.IsNotExist(err):
	default:
		return err
	}
	if cfg.PiProvider != "" {
		filtered["defaultProvider"] = cfg.PiProvider
	}
	if cfg.PiModel != "" {
		filtered["defaultModel"] = cfg.PiModel
	}
	if len(packages) > 0 {
		filtered["packages"] = packages
	}
	if len(filtered) == 0 {
		return removeIfExists(dstPath)
	}
	out, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(dstPath, append(out, '\n'), 0o600)
}

// syncPiModels symlinks the host models.json, or writes a copy whose active model carries
// cfg.ContextWindow: inline on a models.json-defined model, else as a modelOverrides entry.
func syncPiModels(hostDir, runtimeDir string, cfg domain.DriverConfig) error {
	src, dst := filepath.Join(hostDir, "models.json"), filepath.Join(runtimeDir, "models.json")
	if cfg.ContextWindow <= 0 {
		// Drop a copy left by an earlier context_window so the host file is used again.
		if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			if err := removeIfExists(dst); err != nil {
				return err
			}
		}
		return syncFileSymlink(src, dst)
	}
	provider, model := cfg.PiProvider, cfg.PiModel
	if provider == "" || model == "" {
		var settings struct {
			DefaultProvider string `json:"defaultProvider"`
			DefaultModel    string `json:"defaultModel"`
		}
		if raw, err := os.ReadFile(filepath.Join(hostDir, "settings.json")); err == nil {
			_ = json.Unmarshal(raw, &settings)
		}
		if provider == "" {
			provider = settings.DefaultProvider
		}
		if model == "" {
			model = settings.DefaultModel
		}
	}
	if provider == "" || model == "" {
		return errors.New("driver_config.context_window for pi needs pi_provider and pi_model or host defaultProvider/defaultModel")
	}
	doc := map[string]any{}
	switch raw, err := os.ReadFile(src); {
	case err == nil:
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("parse host pi models.json: %w", err)
		}
	case !os.IsNotExist(err):
		return err
	}
	providers, _ := doc["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
		doc["providers"] = providers
	}
	prov, _ := providers[provider].(map[string]any)
	if prov == nil {
		prov = map[string]any{}
		providers[provider] = prov
	}
	inline := false
	if models, ok := prov["models"].([]any); ok {
		for _, m := range models {
			if entry, ok := m.(map[string]any); ok && entry["id"] == model {
				entry["contextWindow"] = cfg.ContextWindow
				inline = true
			}
		}
	}
	if !inline {
		overrides, _ := prov["modelOverrides"].(map[string]any)
		if overrides == nil {
			overrides = map[string]any{}
			prov["modelOverrides"] = overrides
		}
		entry, _ := overrides[model].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
			overrides[model] = entry
		}
		entry["contextWindow"] = cfg.ContextWindow
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	// Replace a previous symlink instead of writing through it into the host file.
	if err := removeIfExists(dst); err != nil {
		return err
	}
	return writeFileAtomic(dst, append(out, '\n'), 0o600)
}

// codexWindowKeys are written from driver_config.context_window when set, replacing host values.
var codexWindowKeys = []string{"model_context_window", "model_auto_compact_token_limit"}

// codexAutoCompactRatio matches the pi driver's soft compaction ratio.
const codexAutoCompactRatio = 0.75

// syncCodexRuntimeDir symlinks host auth/model cache and writes a filtered config.toml keeping
// only model/provider/profile settings while excluding MCP servers, hooks, plugins and projects.
func syncCodexRuntimeDir(hostDir, runtimeDir string, contextWindow int) error {
	if hostDir == "" || sameDir(hostDir, runtimeDir) {
		return nil
	}
	for _, name := range []string{"auth.json", "models_cache.json"} {
		if err := syncFileSymlink(filepath.Join(hostDir, name), filepath.Join(runtimeDir, name)); err != nil {
			return err
		}
	}
	return syncCodexConfig(filepath.Join(hostDir, "config.toml"), filepath.Join(runtimeDir, "config.toml"), contextWindow)
}

var codexAllowedTopKeys = map[string]bool{
	"model":                          true,
	"model_provider":                 true,
	"model_reasoning_effort":         true,
	"model_reasoning_summary":        true,
	"model_verbosity":                true,
	"cli_auth_credentials_store":     true,
	"chatgpt_base_url":               true,
	"profile":                        true,
	"model_catalog_json":             true,
	"model_context_window":           true,
	"model_auto_compact_token_limit": true,
}

func syncCodexConfig(srcPath, dstPath string, contextWindow int) error {
	raw, err := os.ReadFile(srcPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	filtered := filterCodexConfigTOML(string(raw))
	if contextWindow > 0 {
		filtered = withCodexContextWindow(filtered, contextWindow)
	}
	if strings.TrimSpace(filtered) == "" {
		return removeIfExists(dstPath)
	}
	return writeFileAtomic(dstPath, []byte(filtered), 0o600)
}

// filterCodexConfigTOML keeps only allowlisted top-level keys and [model_providers.*] /
// [profiles.*] tables so host MCP servers, hooks, plugins and project rules are never inherited.
func filterCodexConfigTOML(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var out []string
	section := ""
	keepSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if header, ok := parseTOMLTableHeader(trimmed); ok {
			section = header
			keepSection = isAllowedCodexSection(section)
			if keepSection {
				if len(out) > 0 && out[len(out)-1] != "" {
					out = append(out, "")
				}
				out = append(out, line)
			}
			continue
		}
		if section == "" {
			if key, ok := parseTOMLKey(trimmed); ok && codexAllowedTopKeys[key] {
				out = append(out, line)
			}
			continue
		}
		if keepSection {
			out = append(out, line)
		}
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// withCodexContextWindow drops host window keys and prepends the per-agent values, so they stay
// top-level ahead of any table.
func withCodexContextWindow(content string, contextWindow int) string {
	var kept []string
	for _, line := range strings.Split(content, "\n") {
		if key, ok := parseTOMLKey(strings.TrimSpace(line)); ok && slices.Contains(codexWindowKeys, key) {
			continue
		}
		kept = append(kept, line)
	}
	head := fmt.Sprintf("model_context_window = %d\nmodel_auto_compact_token_limit = %d\n",
		contextWindow, int(float64(contextWindow)*codexAutoCompactRatio))
	rest := strings.TrimLeft(strings.Join(kept, "\n"), "\n")
	if rest == "" {
		return head
	}
	return head + rest
}

func parseTOMLTableHeader(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	// Strip trailing comment outside quotes.
	s := stripTOMLComment(trimmed)
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[[") && strings.HasSuffix(s, "]]") {
		return strings.TrimSpace(s[2 : len(s)-2]), true
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return strings.TrimSpace(s[1 : len(s)-1]), true
	}
	return "", false
}

func stripTOMLComment(line string) string {
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if inDouble && c == '\\' {
			escaped = true
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if c == '#' && !inSingle && !inDouble {
			return line[:i]
		}
	}
	return line
}

func isAllowedCodexSection(section string) bool {
	root := section
	if idx := strings.IndexByte(section, '.'); idx >= 0 {
		root = section[:idx]
	}
	root = strings.Trim(strings.TrimSpace(root), `"'`)
	return root == "model_providers" || root == "profiles"
}

func parseTOMLKey(trimmed string) (string, bool) {
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	eq := strings.IndexByte(trimmed, '=')
	if eq <= 0 {
		return "", false
	}
	key := strings.TrimSpace(trimmed[:eq])
	key = strings.Trim(key, `"'`)
	return key, key != ""
}

func syncFileSymlink(srcPath, dstPath string) error {
	if _, err := os.Stat(srcPath); err != nil {
		if os.IsNotExist(err) {
			// Remove stale symlink if host file disappeared.
			if fi, lerr := os.Lstat(dstPath); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
				return removeIfExists(dstPath)
			}
			return nil
		}
		return err
	}
	if target, err := os.Readlink(dstPath); err == nil && target == srcPath {
		return nil
	}
	tmp := dstPath + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(srcPath, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, dstPath)
}

// syncDirSymlink points dst at the host directory so requested provider packages resolve.
func syncDirSymlink(srcPath, dstPath string) error {
	info, err := os.Stat(srcPath)
	if err != nil || !info.IsDir() {
		return nil
	}
	if target, err := os.Readlink(dstPath); err == nil && target == srcPath {
		return nil
	}
	if err := removeIfExists(dstPath); err != nil {
		return err
	}
	tmp := dstPath + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(srcPath, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, dstPath)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
