package computer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
)

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

func TestSystemPromptGuidesLongTaskProgressSync(t *testing.T) {
	p := buildSystemPrompt(protocol.AssignedAgentSpec{
		DisplayName: "Lin",
		MemberID:    "m-lin",
		RoleText:    "Backend engineer",
		DriverKind:  domain.DriverACP,
	}, "/tmp/workspace", "/tmp/memory")
	for _, want := range []string{
		"only output channel and needs no further permission",
		"Before diving into a long or multi-step task",
		"meaningful milestones",
		"skip routine tool-by-tool narration",
		"Before stopping, check whether you still owe",
		"post progress updates in the task's own conversation",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("system prompt missing %q", want)
		}
	}
}
