package app

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

type SearchMessagesInput struct {
	Query          string
	ConversationID string
	Limit          int
}

type SearchHit struct {
	MessageID            string                  `json:"message_id"`
	ConversationID       string                  `json:"conversation_id"`
	ConversationKind     domain.ConversationKind `json:"conversation_kind"`
	ConversationTitle    string                  `json:"conversation_title"`
	ParentConversationID string                  `json:"parent_conversation_id,omitempty"`
	ParentSlug           string                  `json:"parent_slug,omitempty"`
	RootMessageID        string                  `json:"root_message_id,omitempty"`
	TaskID               string                  `json:"task_id,omitempty"`
	TaskNumber           int64                   `json:"task_number,omitempty"`
	Seq                  int64                   `json:"seq"`
	AuthorMemberID       string                  `json:"author_member_id"`
	AuthorName           string                  `json:"author_display_name"`
	AuthorKind           domain.MemberKind       `json:"author_kind"`
	Snippet              string                  `json:"snippet"`
	CreatedAt            time.Time               `json:"created_at"`
}

// SearchMessages searches text messages visible to the viewer within the space.
// Visibility mirrors canAccessConversation so private channels and other members' DMs are never exposed.
func (s *Service) SearchMessages(ctx context.Context, viewer domain.Member, in SearchMessagesInput) ([]SearchHit, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return []SearchHit{}, nil
	}
	limit := in.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	var convFilter string
	if in.ConversationID != "" {
		c, err := canAccessConversation(ctx, s.store.ReadDB(), viewer, in.ConversationID)
		if err != nil {
			return nil, err
		}
		convFilter = c.ID
	}

	args := []any{viewer.ID, viewer.ID, viewer.SpaceID, string(viewer.Kind)}
	convClause := ""
	if convFilter != "" {
		convClause = " AND msg.conversation_id = ?"
		args = append(args, convFilter)
	}
	args = append(args, strings.ToLower(query), limit)

	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT msg.id, msg.conversation_id, c.kind, COALESCE(c.slug, ''), COALESCE(c.parent_conversation_id, ''),
		        COALESCE(pc.slug, ''), COALESCE(c.root_message_id, ''), COALESCE(c.task_id, ''), COALESCE(t.number, 0),
		        COALESCE(t.title, ''), COALESCE(peer.display_name, ''),
		        msg.seq, msg.author_member_id, am.display_name, am.kind, msg.body_markdown, msg.created_at
		 FROM messages msg
		 JOIN conversations c ON c.id = msg.conversation_id
		 LEFT JOIN conversations pc ON pc.id = c.parent_conversation_id
		 LEFT JOIN conversation_members cm ON cm.conversation_id = COALESCE(c.parent_conversation_id, c.id) AND cm.member_id = ?
		 LEFT JOIN tasks t ON t.id = c.task_id
		 LEFT JOIN conversation_members pcm ON c.kind = 'dm' AND pcm.conversation_id = c.id AND pcm.member_id <> ?
		 LEFT JOIN members peer ON peer.id = pcm.member_id
		 JOIN members am ON am.id = msg.author_member_id
		 WHERE msg.space_id = ?
		   AND msg.kind = 'text'
		   AND msg.deleted_at IS NULL
		   AND (
		       (COALESCE(pc.kind, c.kind) = 'public' AND ? = 'human')
		       OR cm.member_id IS NOT NULL
		   )`+convClause+`
		   AND instr(lower(msg.body_markdown), ?) > 0
		 ORDER BY msg.created_at DESC, msg.seq DESC
		 LIMIT ?`,
		args...,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "search messages", err)
	}
	defer rows.Close()

	hits := make([]SearchHit, 0)
	for rows.Next() {
		var h SearchHit
		var kindStr, slug, taskTitle, peerName, authorKind, body, createdAt string
		if err := rows.Scan(
			&h.MessageID, &h.ConversationID, &kindStr, &slug, &h.ParentConversationID,
			&h.ParentSlug, &h.RootMessageID, &h.TaskID, &h.TaskNumber,
			&taskTitle, &peerName,
			&h.Seq, &h.AuthorMemberID, &h.AuthorName, &authorKind, &body, &createdAt,
		); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "scan search hit", err)
		}
		h.ConversationKind = domain.ConversationKind(kindStr)
		h.AuthorKind = domain.MemberKind(authorKind)
		h.CreatedAt = store.ParseTime(createdAt)
		switch h.ConversationKind {
		case domain.ConversationPublic, domain.ConversationPrivate:
			h.ConversationTitle = "#" + slug
		case domain.ConversationDM:
			h.ConversationTitle = peerName
		case domain.ConversationThread:
			if h.ParentSlug != "" {
				h.ConversationTitle = "Thread in #" + h.ParentSlug
			} else {
				h.ConversationTitle = "Thread"
			}
		case domain.ConversationTask:
			h.ConversationTitle = fmt.Sprintf("#%d %s", h.TaskNumber, taskTitle)
		}
		h.Snippet = searchSnippet(body, query, 140)
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "iterate search hits", err)
	}
	return hits, nil
}

// searchSnippet extracts a bounded single-line excerpt centered around the first match.
func searchSnippet(body, query string, maxRunes int) string {
	flat := strings.Join(strings.Fields(body), " ")
	if flat == "" {
		return ""
	}
	runes := []rune(flat)
	if len(runes) <= maxRunes {
		return flat
	}
	lowerFlat := strings.ToLower(flat)
	lowerQuery := strings.ToLower(query)
	byteIdx := strings.Index(lowerFlat, lowerQuery)
	if byteIdx < 0 {
		return string(runes[:maxRunes]) + "…"
	}
	matchRuneIdx := utf8.RuneCountInString(lowerFlat[:byteIdx])
	lead := maxRunes / 3
	start := matchRuneIdx - lead
	if start < 0 {
		start = 0
	}
	end := start + maxRunes
	if end > len(runes) {
		end = len(runes)
		start = end - maxRunes
		if start < 0 {
			start = 0
		}
	}
	prefix := ""
	suffix := ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(runes) {
		suffix = "…"
	}
	return prefix + string(runes[start:end]) + suffix
}
