package app

import (
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestInboxTargetCoversEveryConversationKind(t *testing.T) {
	cases := []struct {
		kind   domain.ConversationKind
		convID string
		slug   string
		peer   string
		number int64
		want   string
	}{
		{domain.ConversationPublic, "c1", "general", "", 0, "#general"},
		{domain.ConversationPrivate, "c2", "ops", "", 0, "#ops"},
		{domain.ConversationDM, "c3", "", "Lin", 0, "dm:@Lin"},
		{domain.ConversationThread, "c4", "general", "", 0, "thread:c4"},
		{domain.ConversationTask, "c5", "general", "", 14, "task:#14"},
	}
	for _, tc := range cases {
		if got := inboxTarget(tc.kind, tc.convID, tc.slug, tc.peer, tc.number); got != tc.want {
			t.Fatalf("%s target = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
