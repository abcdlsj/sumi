package computer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
func prepareACPRuntimeDir(agentHome, provider string) error {
	runtimeDir := acpRuntimeDir(agentHome, provider)
	if runtimeDir == "" {
		return nil
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return fmt.Errorf("create acp runtime dir: %w", err)
	}
	userHome, _ := os.UserHomeDir()
	switch provider {
	case domain.ACPProviderPi:
		hostDir := resolveHostDir("PI_CODING_AGENT_DIR", filepath.Join(userHome, ".pi", "agent"))
		return syncPiRuntimeDir(hostDir, runtimeDir)
	case domain.ACPProviderCodex:
		hostDir := resolveHostDir("CODEX_HOME", filepath.Join(userHome, ".codex"))
		return syncCodexRuntimeDir(hostDir, runtimeDir)
	case domain.ACPProviderClaude:
		// Claude uses API env vars; keep CLAUDE_CONFIG_DIR empty of host hooks/skills/plugins.
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

// syncPiRuntimeDir symlinks host auth/models and writes a minimal settings.json with only model
// and transport preferences, omitting packages, extensions, skills and prompts.
func syncPiRuntimeDir(hostDir, runtimeDir string) error {
	if hostDir == "" || sameDir(hostDir, runtimeDir) {
		return nil
	}
	for _, name := range []string{"auth.json", "models.json"} {
		if err := syncFileSymlink(filepath.Join(hostDir, name), filepath.Join(runtimeDir, name)); err != nil {
			return err
		}
	}
	return syncPiSettings(filepath.Join(hostDir, "settings.json"), filepath.Join(runtimeDir, "settings.json"))
}

var piAllowedSettingsKeys = []string{
	"defaultProvider",
	"defaultModel",
	"defaultThinkingLevel",
	"transport",
	"compaction",
}

func syncPiSettings(srcPath, dstPath string) error {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return removeIfExists(dstPath)
		}
		return err
	}
	var src map[string]any
	if err := json.Unmarshal(raw, &src); err != nil {
		// Ignore malformed host settings rather than blocking agent startup.
		return removeIfExists(dstPath)
	}
	filtered := make(map[string]any, len(piAllowedSettingsKeys))
	for _, k := range piAllowedSettingsKeys {
		if v, ok := src[k]; ok {
			filtered[k] = v
		}
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

// syncCodexRuntimeDir symlinks host auth/model cache and writes a filtered config.toml keeping
// only model/provider/profile settings while excluding MCP servers, hooks, plugins and projects.
func syncCodexRuntimeDir(hostDir, runtimeDir string) error {
	if hostDir == "" || sameDir(hostDir, runtimeDir) {
		return nil
	}
	for _, name := range []string{"auth.json", "models_cache.json"} {
		if err := syncFileSymlink(filepath.Join(hostDir, name), filepath.Join(runtimeDir, name)); err != nil {
			return err
		}
	}
	return syncCodexConfig(filepath.Join(hostDir, "config.toml"), filepath.Join(runtimeDir, "config.toml"))
}

var codexAllowedTopKeys = map[string]bool{
	"model":                      true,
	"model_provider":             true,
	"model_reasoning_effort":     true,
	"model_reasoning_summary":    true,
	"model_verbosity":            true,
	"cli_auth_credentials_store": true,
	"chatgpt_base_url":           true,
	"profile":                    true,
	"model_catalog_json":         true,
}

func syncCodexConfig(srcPath, dstPath string) error {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return removeIfExists(dstPath)
		}
		return err
	}
	filtered := filterCodexConfigTOML(string(raw))
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
