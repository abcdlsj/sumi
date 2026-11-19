package computer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
)

func TestSystemPromptCarriesIdentityAndPaths(t *testing.T) {
	spec := protocol.AssignedAgentSpec{MemberID: "m-1", DisplayName: "Lin", RoleText: "Backend owner", DriverKind: domain.DriverACP}
	p := buildSystemPrompt(spec, "/home/a/workspace", "/home/a/memory")
	for _, want := range []string{"@Lin", "m-1", "Backend owner", "/home/a/workspace", "/home/a/memory/MEMORY.md", "error.hint", "held", "task:#N", "dm:@name"} {
		if !strings.Contains(p, want) {
			t.Fatalf("system prompt missing %q", want)
		}
	}
	spec.DriverKind = domain.DriverBuiltin
	if p := buildSystemPrompt(spec, "/w", "/m"); !strings.Contains(p, "memory/MEMORY.md") || strings.Contains(p, "/m/MEMORY.md") {
		t.Fatalf("builtin prompt must use agent-home relative memory paths")
	}
}

func TestStartupContextBoundsMemory(t *testing.T) {
	dir := t.TempDir()
	spec := protocol.AssignedAgentSpec{DriverKind: domain.DriverBuiltin}
	if got := buildStartupContext(spec, dir); got != "" {
		t.Fatalf("missing MEMORY.md must inject nothing: %q", got)
	}
	mem := filepath.Join(dir, "MEMORY.md")
	_ = os.WriteFile(mem, []byte(memoryTemplate), 0o600)
	if got := buildStartupContext(spec, dir); strings.Contains(got, "<memory>") {
		t.Fatalf("untouched template must not be injected: %q", got)
	}
	_ = os.WriteFile(mem, []byte("# Memory\n"+strings.Repeat("é", startupMemoryLimit)), 0o600)
	got := buildStartupContext(spec, dir)
	if !strings.Contains(got, "<memory>") || !strings.Contains(got, "was cut at 16 KiB") {
		t.Fatalf("large memory must be injected and marked truncated")
	}
	if len(got) > startupMemoryLimit+1024 || !strings.Contains(got, "é\n</memory>") {
		t.Fatalf("truncation must be bounded and rune-safe")
	}
}

func TestWakePromptPreviewsLatestMessage(t *testing.T) {
	p := buildWakePrompt([]domain.InboxItem{
		{Title: "@Owner", Target: "dm:@Owner", Kind: domain.ConversationDM, ConversationID: "c1", Reason: "dm", UnreadCount: 2, LatestAuthorName: "Owner", LatestPreview: "部署\n失败了"},
		{Title: "#general", Target: "#general", Kind: domain.ConversationPublic, ConversationID: "c2", Reason: "mention", UnreadCount: 1},
	})
	if !strings.Contains(p, `latest from Owner: "部署 失败了"`) || !strings.Contains(p, "target=#general") {
		t.Fatalf("wake prompt: %s", p)
	}
	if !strings.Contains(p, "do not count as reading") {
		t.Fatalf("wake prompt must say previews are not reads")
	}
}
