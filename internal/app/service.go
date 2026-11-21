package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
	"github.com/abcdlsj/sumi/internal/store"
)

type Service struct {
	store     *store.Store
	hub       *EventHub
	notifier  ComputerNotifier
	workspace WorkspaceClient

	// activity holds ephemeral in-turn Agent activity keyed by member id; it is only exposed
	// while the Agent's persisted runtime_state is working.
	activityMu sync.Mutex
	activity   map[string]domain.AgentActivity
}

func NewService(s *store.Store, hub *EventHub, notifier ComputerNotifier) *Service {
	if hub == nil {
		hub = NewEventHub()
	}
	if notifier == nil {
		notifier = noopNotifier{}
	}
	return &Service{
		store:    s,
		hub:      hub,
		notifier: notifier,
		activity: make(map[string]domain.AgentActivity),
	}
}

func (s *Service) Store() *store.Store { return s.store }
func (s *Service) Hub() *EventHub      { return s.hub }

func (s *Service) SetNotifier(n ComputerNotifier) {
	if n == nil {
		s.notifier = noopNotifier{}
		return
	}
	s.notifier = n
}

func (s *Service) SetWorkspaceClient(w WorkspaceClient) {
	s.workspace = w
}

func randomToken(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type wakeTarget struct {
	computerID     string
	agentMemberID  string
	conversationID string
	reason         string
}

func (s *Service) dispatchPostCommit(events []domain.SpaceEvent, wakes []wakeTarget) {
	for _, ev := range events {
		s.hub.Publish(ev)
	}
	seen := make(map[string]struct{}, len(wakes))
	for _, w := range wakes {
		if w.computerID == "" {
			continue
		}
		key := w.agentMemberID + ":" + w.conversationID
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		s.notifier.SendWake(w.computerID, protocol.WakeFrame{
			AgentMemberID:  w.agentMemberID,
			ConversationID: w.conversationID,
			Reason:         w.reason,
		})
	}
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func loadMember(ctx context.Context, q queryer, memberID string) (domain.Member, error) {
	var m domain.Member
	var kind, access, createdAt string
	var retiredAt sql.NullString
	err := q.QueryRowContext(ctx,
		`SELECT id, space_id, kind, display_name, access_level, created_at, retired_at FROM members WHERE id = ?`,
		memberID,
	).Scan(&m.ID, &m.SpaceID, &kind, &m.DisplayName, &access, &createdAt, &retiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Member{}, domain.NewError(domain.CodeNotFound, "member not found")
	}
	if err != nil {
		return domain.Member{}, domain.WrapError(domain.CodeInternal, "query member", err)
	}
	m.Kind = domain.MemberKind(kind)
	m.AccessLevel = domain.AccessLevel(access)
	m.CreatedAt = store.ParseTime(createdAt)
	if retiredAt.Valid {
		t := store.ParseTime(retiredAt.String)
		m.RetiredAt = &t
	}
	return m, nil
}

func hasPermission(ctx context.Context, q queryer, m domain.Member, perm domain.Permission) (bool, error) {
	if m.RetiredAt != nil {
		return false, nil
	}
	if m.AccessLevel == domain.AccessOwner || m.AccessLevel == domain.AccessAdmin {
		return true, nil
	}
	var count int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM member_permissions WHERE member_id = ? AND action_code = ?`,
		m.ID, string(perm),
	).Scan(&count); err != nil {
		return false, domain.WrapError(domain.CodeInternal, "check permission", err)
	}
	return count > 0, nil
}

type convMeta struct {
	ID                   string
	SpaceID              string
	Kind                 domain.ConversationKind
	Slug                 string
	ParentConversationID string
	RootMessageID        string
	TaskID               string
	NextSeq              int64
	ArchivedAt           *time.Time
}

func loadConvMeta(ctx context.Context, q queryer, convID string) (convMeta, error) {
	var c convMeta
	var kind string
	var slug, parentID, rootMsgID, taskID, archivedAt sql.NullString
	err := q.QueryRowContext(ctx,
		`SELECT id, space_id, kind, slug, parent_conversation_id, root_message_id, task_id, next_seq, archived_at
		 FROM conversations WHERE id = ?`,
		convID,
	).Scan(&c.ID, &c.SpaceID, &kind, &slug, &parentID, &rootMsgID, &taskID, &c.NextSeq, &archivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return convMeta{}, domain.NewError(domain.CodeNotFound, "conversation not found")
	}
	if err != nil {
		return convMeta{}, domain.WrapError(domain.CodeInternal, "query conversation", err)
	}
	c.Kind = domain.ConversationKind(kind)
	c.Slug = slug.String
	c.ParentConversationID = parentID.String
	c.RootMessageID = rootMsgID.String
	c.TaskID = taskID.String
	if archivedAt.Valid && archivedAt.String != "" {
		t := store.ParseTime(archivedAt.String)
		c.ArchivedAt = &t
	}
	return c, nil
}

// checkNotArchived rejects writes to an archived channel or any thread/task under it.
func checkNotArchived(ctx context.Context, q queryer, c convMeta) error {
	if c.ArchivedAt != nil {
		return domain.NewError(domain.CodeConflict, "channel is archived")
	}
	boundary, err := resolveVisibilityBoundary(ctx, q, c)
	if err != nil {
		return err
	}
	if boundary.ArchivedAt != nil {
		return domain.NewError(domain.CodeConflict, "channel is archived")
	}
	return nil
}

// resolveVisibilityBoundary returns the conversation ID that owns membership (parent for thread/task).
func resolveVisibilityBoundary(ctx context.Context, q queryer, c convMeta) (convMeta, error) {
	if c.Kind == domain.ConversationThread || c.Kind == domain.ConversationTask {
		return loadConvMeta(ctx, q, c.ParentConversationID)
	}
	return c, nil
}

// canAccessConversation enforces visibility in one place for HTTP, CLI, and SSE.
func canAccessConversation(ctx context.Context, q queryer, member domain.Member, convID string) (convMeta, error) {
	c, err := loadConvMeta(ctx, q, convID)
	if err != nil {
		return convMeta{}, err
	}
	if c.SpaceID != member.SpaceID {
		return convMeta{}, domain.NewError(domain.CodePermissionDenied, "conversation belongs to another space")
	}
	boundary, err := resolveVisibilityBoundary(ctx, q, c)
	if err != nil {
		return convMeta{}, err
	}
	if boundary.Kind == domain.ConversationPublic && member.Kind == domain.MemberKindHuman {
		return c, nil
	}
	var count int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM conversation_members WHERE conversation_id = ? AND member_id = ?`,
		boundary.ID, member.ID,
	).Scan(&count); err != nil {
		return convMeta{}, domain.WrapError(domain.CodeInternal, "check conversation membership", err)
	}
	if count == 0 {
		return convMeta{}, domain.NewError(domain.CodePermissionDenied, "not a member of this conversation")
	}
	return c, nil
}

func normalizeEmail(email string) string {
	return strings.TrimSpace(strings.ToLower(email))
}
