package domain

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"
)

// ACP providers with built-in default commands.
const (
	ACPProviderPi     = "pi"
	ACPProviderCodex  = "codex"
	ACPProviderClaude = "claude"
)

var ACPProviders = []string{ACPProviderPi, ACPProviderCodex, ACPProviderClaude}

func IsACPProvider(p string) bool {
	for _, known := range ACPProviders {
		if p == known {
			return true
		}
	}
	return false
}

// Agent runtime error codes reported by Computers in agent_state frames.
const (
	AgentErrDriverConfigInvalid = "driver_config_invalid"
	AgentErrDriverStartFailed   = "driver_start_failed"
	AgentErrModelAuthFailed     = "model_auth_failed"
	AgentErrModelUnreachable    = "model_unreachable"
	AgentErrTurnFailed          = "turn_failed"
	AgentErrTurnStalled         = "turn_stalled"
	AgentErrTurnBudgetExceeded  = "turn_budget_exceeded"
	AgentErrInboxUnreachable    = "inbox_unreachable"
)

// MaxAgentErrorDetail bounds agents.last_error_detail in characters.
const MaxAgentErrorDetail = 240

// BoundErrorDetail flattens detail to one line and truncates it to MaxAgentErrorDetail runes.
func BoundErrorDetail(detail string) string {
	detail = strings.Join(strings.Fields(detail), " ")
	if utf8.RuneCountInString(detail) <= MaxAgentErrorDetail {
		return detail
	}
	r := []rune(detail)
	return string(r[:MaxAgentErrorDetail-1]) + "…"
}

// DriverConfig is the full stored driver configuration. It may contain APIKey and must
// only travel to the hosting Computer; Browser-facing responses use DriverConfigView.
type DriverConfig struct {
	// builtin
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	// acp
	Provider string   `json:"provider,omitempty"`
	Command  []string `json:"command,omitempty"`
	// acp/pi: per-agent overrides written into the isolated pi runtime settings.
	PiProvider string   `json:"pi_provider,omitempty"`
	PiModel    string   `json:"pi_model,omitempty"`
	PiPackages []string `json:"pi_packages,omitempty"`
}

// NormalizeDriverConfig validates cfg for kind and returns a copy holding only the fields of that kind.
func NormalizeDriverConfig(kind DriverKind, cfg DriverConfig) (DriverConfig, error) {
	switch kind {
	case DriverBuiltin:
		out := DriverConfig{
			BaseURL: strings.TrimSpace(cfg.BaseURL),
			Model:   strings.TrimSpace(cfg.Model),
			APIKey:  strings.TrimSpace(cfg.APIKey),
		}
		u, err := url.Parse(out.BaseURL)
		if out.BaseURL == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return DriverConfig{}, NewError(CodeInvalidArgument, "driver_config.base_url must be an http(s) URL")
		}
		if out.Model == "" {
			return DriverConfig{}, NewError(CodeInvalidArgument, "driver_config.model is required")
		}
		return out, nil
	case DriverACP:
		out := DriverConfig{Provider: strings.TrimSpace(cfg.Provider)}
		for _, arg := range cfg.Command {
			if strings.TrimSpace(arg) == "" {
				return DriverConfig{}, NewError(CodeInvalidArgument, "driver_config.command must not contain empty arguments")
			}
			out.Command = append(out.Command, arg)
		}
		if out.Provider != "" && !IsACPProvider(out.Provider) {
			return DriverConfig{}, NewError(CodeInvalidArgument, "driver_config.provider must be one of pi, codex, claude")
		}
		if out.Provider == "" && len(out.Command) == 0 {
			return DriverConfig{}, NewError(CodeInvalidArgument, "driver_config.provider or driver_config.command is required")
		}
		out.PiProvider = strings.TrimSpace(cfg.PiProvider)
		out.PiModel = strings.TrimSpace(cfg.PiModel)
		for _, pkg := range cfg.PiPackages {
			trimmed := strings.TrimSpace(pkg)
			if !strings.HasPrefix(trimmed, "npm:") || len(trimmed) <= len("npm:") {
				return DriverConfig{}, NewError(CodeInvalidArgument, "driver_config.pi_packages entries must look like npm:<package>")
			}
			out.PiPackages = append(out.PiPackages, trimmed)
		}
		if out.PiProvider != "" || out.PiModel != "" || len(out.PiPackages) > 0 {
			if out.Provider != ACPProviderPi {
				return DriverConfig{}, NewError(CodeInvalidArgument, "driver_config.pi_provider, pi_model and pi_packages require provider pi")
			}
			if !validPiProviderName(out.PiProvider) {
				return DriverConfig{}, NewError(CodeInvalidArgument, "driver_config.pi_provider must be letters, digits, dot, dash or underscore")
			}
			if strings.ContainsAny(out.PiModel, " \t\n") {
				return DriverConfig{}, NewError(CodeInvalidArgument, "driver_config.pi_model must not contain whitespace")
			}
		}
		return out, nil
	default:
		return DriverConfig{}, NewError(CodeInvalidArgument, "driver_kind must be acp or builtin")
	}
}

// validPiProviderName accepts an empty name (no override) or a pi provider identifier.
func validPiProviderName(name string) bool {
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// ParseDriverConfig decodes a stored driver_config_json value.
func ParseDriverConfig(raw string) DriverConfig {
	var cfg DriverConfig
	_ = json.Unmarshal([]byte(raw), &cfg)
	return cfg
}

// Redact returns the Browser-safe projection of cfg for kind.
func (c DriverConfig) Redact(kind DriverKind) DriverConfigView {
	v := DriverConfigView{Kind: kind}
	switch kind {
	case DriverBuiltin:
		set := c.APIKey != ""
		v.BaseURL, v.Model, v.APIKeySet = c.BaseURL, c.Model, &set
	case DriverACP:
		v.Provider, v.Command = c.Provider, c.Command
		v.PiProvider, v.PiModel, v.PiPackages = c.PiProvider, c.PiModel, c.PiPackages
	}
	return v
}

// DriverConfigView is the redacted driver configuration; api_key is replaced with api_key_set.
type DriverConfigView struct {
	Kind      DriverKind `json:"-"`
	BaseURL   string     `json:"base_url,omitempty"`
	Model     string     `json:"model,omitempty"`
	APIKeySet *bool      `json:"api_key_set,omitempty"`
	Provider  string     `json:"provider,omitempty"`
	Command   []string   `json:"command,omitempty"`

	PiProvider string   `json:"pi_provider,omitempty"`
	PiModel    string   `json:"pi_model,omitempty"`
	PiPackages []string `json:"pi_packages,omitempty"`
}

// MarshalJSON emits the exact per-kind shape: builtin {base_url, model, api_key_set}, acp {provider, command?}.
func (v DriverConfigView) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case DriverBuiltin:
		return json.Marshal(struct {
			BaseURL   string `json:"base_url"`
			Model     string `json:"model"`
			APIKeySet bool   `json:"api_key_set"`
		}{v.BaseURL, v.Model, v.APIKeySet != nil && *v.APIKeySet})
	case DriverACP:
		return json.Marshal(struct {
			Provider   string   `json:"provider"`
			Command    []string `json:"command,omitempty"`
			PiProvider string   `json:"pi_provider,omitempty"`
			PiModel    string   `json:"pi_model,omitempty"`
			PiPackages []string `json:"pi_packages,omitempty"`
		}{v.Provider, v.Command, v.PiProvider, v.PiModel, v.PiPackages})
	default:
		type plain DriverConfigView
		return json.Marshal(plain(v))
	}
}
