package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeDriverConfig(t *testing.T) {
	cases := []struct {
		name    string
		kind    DriverKind
		in      DriverConfig
		wantErr string
		want    DriverConfig
	}{
		{"builtin ok keeps only builtin fields", DriverBuiltin,
			DriverConfig{BaseURL: " https://api.example.com/v1 ", Model: "gpt", APIKey: "sk", Provider: "pi"}, "",
			DriverConfig{BaseURL: "https://api.example.com/v1", Model: "gpt", APIKey: "sk"}},
		{"builtin keyless local gateway", DriverBuiltin,
			DriverConfig{BaseURL: "http://127.0.0.1:11434/v1", Model: "llama"}, "",
			DriverConfig{BaseURL: "http://127.0.0.1:11434/v1", Model: "llama"}},
		{"builtin missing base_url", DriverBuiltin, DriverConfig{Model: "m"}, "driver_config.base_url", DriverConfig{}},
		{"builtin non-http base_url", DriverBuiltin, DriverConfig{BaseURL: "ftp://x", Model: "m"}, "driver_config.base_url", DriverConfig{}},
		{"builtin hostless base_url", DriverBuiltin, DriverConfig{BaseURL: "http://", Model: "m"}, "driver_config.base_url", DriverConfig{}},
		{"builtin missing model", DriverBuiltin, DriverConfig{BaseURL: "http://x"}, "driver_config.model", DriverConfig{}},
		{"acp provider", DriverACP, DriverConfig{Provider: "pi", APIKey: "leak", BaseURL: "http://x"}, "",
			DriverConfig{Provider: "pi"}},
		{"acp command only", DriverACP, DriverConfig{Command: []string{"my-acp", "--stdio"}}, "",
			DriverConfig{Command: []string{"my-acp", "--stdio"}}},
		{"acp unknown provider", DriverACP, DriverConfig{Provider: "gemini"}, "driver_config.provider", DriverConfig{}},
		{"acp empty", DriverACP, DriverConfig{}, "driver_config.provider", DriverConfig{}},
		{"acp blank argv", DriverACP, DriverConfig{Command: []string{"x", " "}}, "driver_config.command", DriverConfig{}},
		{"unknown kind", DriverKind("mcp"), DriverConfig{}, "driver_kind", DriverConfig{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeDriverConfig(tc.kind, tc.in)
			if tc.wantErr != "" {
				var de *Error
				if !errors.As(err, &de) || de.Code != CodeInvalidArgument || !strings.Contains(de.Message, tc.wantErr) {
					t.Fatalf("want invalid_argument naming %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(tc.want)
			if string(a) != string(b) {
				t.Fatalf("got %s want %s", a, b)
			}
		})
	}
}

func TestDriverConfigRedactShapes(t *testing.T) {
	raw, _ := json.Marshal(DriverConfig{BaseURL: "http://x", Model: "m", APIKey: "sk-secret"}.Redact(DriverBuiltin))
	if string(raw) != `{"base_url":"http://x","model":"m","api_key_set":true}` {
		t.Fatalf("builtin view = %s", raw)
	}
	raw, _ = json.Marshal(DriverConfig{BaseURL: "http://x", Model: "m"}.Redact(DriverBuiltin))
	if string(raw) != `{"base_url":"http://x","model":"m","api_key_set":false}` {
		t.Fatalf("keyless builtin view = %s", raw)
	}
	raw, _ = json.Marshal(DriverConfig{Provider: "pi", APIKey: "sk-secret"}.Redact(DriverACP))
	if string(raw) != `{"provider":"pi"}` {
		t.Fatalf("acp view = %s", raw)
	}
	raw, _ = json.Marshal(DriverConfig{Provider: "pi", Command: []string{"a"}}.Redact(DriverACP))
	if string(raw) != `{"provider":"pi","command":["a"]}` {
		t.Fatalf("acp command view = %s", raw)
	}
}

func TestBoundErrorDetail(t *testing.T) {
	got := BoundErrorDetail("line one\n\tline   two")
	if got != "line one line two" {
		t.Fatalf("flatten = %q", got)
	}
	long := BoundErrorDetail(strings.Repeat("é", 500))
	if n := utf8.RuneCountInString(long); n != MaxAgentErrorDetail || !utf8.ValidString(long) {
		t.Fatalf("bounded to %d runes valid=%v", n, utf8.ValidString(long))
	}
}
