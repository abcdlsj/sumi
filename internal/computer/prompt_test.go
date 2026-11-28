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
	if !strings.Contains(p, "do NOT read or reply to them in this turn") {
		t.Fatalf("wake prompt must tell agent not to pre-read queued conversations: %s", p)
	}
}

func TestReliabilityPromptBuilders(t *testing.T) {
	focus := domain.InboxItem{Title: "@Owner", Target: "dm:@Owner", ConversationID: "c1", Reason: "dm", UnreadCount: 1}
	retry := buildRetryWakePrompt(focus, domain.AgentErrModelUnreachable, []domain.InboxItem{
		{Title: "#ops", Target: "#ops", ConversationID: "c2", Reason: "mention", UnreadCount: 1},
	})
	if !strings.Contains(retry, "model_unreachable") || !strings.Contains(retry, "sumi read dm:@Owner --limit 10") || !strings.Contains(retry, "target=#ops") {
		t.Fatalf("unexpected retry prompt: %s", retry)
	}
	unread := buildUnreadNudgePrompt(focus)
	if !strings.Contains(unread, "sumi read dm:@Owner --unread") {
		t.Fatalf("unexpected unread nudge: %s", unread)
	}
	silent := buildSilentReplyNudgePrompt(focus, false)
	if !strings.Contains(silent, "assistant text output is NOT visible") {
		t.Fatalf("unexpected silent nudge: %s", silent)
	}
	held := buildSilentReplyNudgePrompt(focus, true)
	if !strings.Contains(held, "Freshness Guard") || !strings.Contains(held, "--draft") {
		t.Fatalf("unexpected held nudge: %s", held)
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
		"## Critical rules",
		"only output channel and needs no further permission",
		"Never discard, redirect, or filter `sumi read` output",
		"[Sumi inbox notice]",
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

func TestBuildSteerNoticeMarksFocusAndBoundsPreview(t *testing.T) {
	longPreview := strings.Repeat("你", 100)
	notice := buildSteerNotice("c1", []domain.InboxItem{
		{ConversationID: "c1", Target: "#eng", Title: "#eng", Reason: "mention", UnreadCount: 1, LatestAuthorName: "Alice", LatestPreview: "stop\ndeploy"},
		{ConversationID: "c2", Target: "dm:@Bob", Title: "@Bob", Reason: "dm", UnreadCount: 2, LatestAuthorName: "Bob", LatestPreview: longPreview},
	})
	for _, want := range []string{
		"read_seq was NOT advanced",
		"#eng [CURRENT CONVERSATION] (target=#eng",
		`Alice: "stop deploy"`,
		"@Bob (target=dm:@Bob",
		"…\"",
	} {
		if !strings.Contains(notice, want) {
			t.Fatalf("steer notice missing %q:\n%s", want, notice)
		}
	}
	if strings.Contains(notice, longPreview) {
		t.Fatalf("steer notice did not truncate long preview")
	}
}

func TestSystemPromptUsesColleagueIdentityAndNameAddressing(t *testing.T) {
	p := buildSystemPrompt(protocol.AssignedAgentSpec{
		DisplayName: "Lin",
		MemberID:    "m-lin",
		DriverKind:  domain.DriverACP,
	}, "/tmp/workspace", "/tmp/memory")
	for _, want := range []string{
		"Think of yourself as a colleague who is always available",
		"These sections are your initialization defaults",
		"Refer to everyone by their display name",
		"write their name without the `@`",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("system prompt missing %q", want)
		}
	}
	for _, avoid := range []string{
		"Humans and other Agents are your peers",
		"ask the human",
		"Ask a human owner",
		"When a human asks",
	} {
		if strings.Contains(p, avoid) {
			t.Fatalf("system prompt should not contain %q", avoid)
		}
	}
}
