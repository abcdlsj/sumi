package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

type CreateChannelInput struct {
	Slug                 string
	Kind                 domain.ConversationKind // public or private
	Topic                string
	MemberIDs            []string
	SourceConversationID string
}

func (s *Service) CreateChannel(ctx context.Context, actor domain.Member, in CreateChannelInput) (domain.Conversation, error) {
	ok, err := hasPermission(ctx, s.store.ReadDB(), actor, domain.PermChannelCreate)
	if err != nil {
		return domain.Conversation{}, err
	}
	if !ok {
		return domain.Conversation{}, domain.NewError(domain.CodePermissionDenied, "missing channel.create permission")
	}
	if err := domain.ValidateSlug(in.Slug); err != nil {
		return domain.Conversation{}, err
	}
	if in.Kind != domain.ConversationPublic && in.Kind != domain.ConversationPrivate {
		return domain.Conversation{}, domain.NewError(domain.CodeInvalidArgument, "channel kind must be public or private")
	}

	now := time.Now().UTC()
	conv := domain.Conversation{
		ID:          uuid.NewString(),
		SpaceID:     actor.SpaceID,
		Kind:        in.Kind,
		Slug:        in.Slug,
		Topic:       strings.TrimSpace(in.Topic),
		NextSeq:     1,
		CreatedAt:   now,
		NotifyLevel: domain.NotifyMentions,
	}
	var events []domain.SpaceEvent

	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO conversations (id, space_id, kind, slug, topic, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			conv.ID, conv.SpaceID, string(conv.Kind), conv.Slug, conv.Topic, store.FormatTime(now),
		); err != nil {
			if store.IsUniqueViolation(err) {
				return domain.NewError(domain.CodeConflict, "channel slug already exists")
			}
			return domain.WrapError(domain.CodeInternal, "insert channel", err)
		}

		membersToAdd := make(map[string]struct{})
		membersToAdd[actor.ID] = struct{}{}
		for _, mid := range in.MemberIDs {
			if mid != "" {
				membersToAdd[mid] = struct{}{}
			}
		}
		for mid := range membersToAdd {
			m, err := loadMember(ctx, tx, mid)
			if err != nil || m.SpaceID != actor.SpaceID || m.RetiredAt != nil {
				return domain.NewError(domain.CodeInvalidArgument, "invalid channel member")
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
				conv.ID, actor.SpaceID, mid, store.FormatTime(now),
			); err != nil {
				return domain.WrapError(domain.CodeInternal, "add channel member", err)
			}
		}

		if in.SourceConversationID != "" {
			if _, err := canAccessConversation(ctx, tx, actor, in.SourceConversationID); err != nil {
				return err
			}
			_, ev, err := insertActionMessageTx(ctx, tx, actor, in.SourceConversationID, domain.ActionChannelCreated, map[string]any{
				"channel_id": conv.ID,
				"slug":       conv.Slug,
				"kind":       conv.Kind,
			})
			if err != nil {
				return err
			}
			events = append(events, ev)
		}

		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.created", map[string]any{
			"conversation_id": conv.ID,
			"kind":            conv.Kind,
			"slug":            conv.Slug,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return domain.Conversation{}, err
	}
	s.dispatchPostCommit(events, nil)
	return conv, nil
}

func (s *Service) InviteToChannel(ctx context.Context, actor domain.Member, channelID, targetMemberID string) error {
	ok, err := hasPermission(ctx, s.store.ReadDB(), actor, domain.PermChannelManageMembers)
	if err != nil {
		return err
	}
	if !ok {
		return domain.NewError(domain.CodePermissionDenied, "missing channel.manage_members permission")
	}
	var events []domain.SpaceEvent
	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := canAccessConversation(ctx, tx, actor, channelID)
		if err != nil {
			return err
		}
		if c.Kind != domain.ConversationPublic && c.Kind != domain.ConversationPrivate {
			return domain.NewError(domain.CodeInvalidArgument, "can only invite to public or private channels")
		}
		if err := checkNotArchived(ctx, tx, c); err != nil {
			return err
		}
		target, err := loadMember(ctx, tx, targetMemberID)
		if err != nil || target.SpaceID != actor.SpaceID || target.RetiredAt != nil {
			return domain.NewError(domain.CodeNotFound, "target member not found")
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
			c.ID, actor.SpaceID, target.ID, store.FormatTime(time.Now().UTC()),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "invite member", err)
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.members_changed", map[string]any{
			"conversation_id": c.ID,
			"member_id":       target.ID,
			"action":          "joined",
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return err
	}
	s.dispatchPostCommit(events, nil)
	return nil
}

func (s *Service) RemoveFromChannel(ctx context.Context, actor domain.Member, channelID, targetMemberID string) error {
	if actor.ID != targetMemberID {
		ok, err := hasPermission(ctx, s.store.ReadDB(), actor, domain.PermChannelManageMembers)
		if err != nil {
			return err
		}
		if !ok {
			return domain.NewError(domain.CodePermissionDenied, "missing channel.manage_members permission")
		}
	}
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := canAccessConversation(ctx, tx, actor, channelID)
		if err != nil {
			return err
		}
		if c.Kind != domain.ConversationPublic && c.Kind != domain.ConversationPrivate {
			return domain.NewError(domain.CodeInvalidArgument, "can only remove members from channels")
		}
		if err := checkNotArchived(ctx, tx, c); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM conversation_members WHERE conversation_id = ? AND member_id = ?`,
			c.ID, targetMemberID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "remove member", err)
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.members_changed", map[string]any{
			"conversation_id": c.ID,
			"member_id":       targetMemberID,
			"action":          "removed",
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		return err
	}
	s.dispatchPostCommit(events, nil)
	return nil
}

// ListConversationMembers lists explicit members of the conversation. Threads and tasks inherit
// their parent's membership roster.
func (s *Service) ListConversationMembers(ctx context.Context, viewer domain.Member, convID string) ([]domain.Member, error) {
	c, err := canAccessConversation(ctx, s.store.ReadDB(), viewer, convID)
	if err != nil {
		return nil, err
	}
	boundary, err := resolveVisibilityBoundary(ctx, s.store.ReadDB(), c)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT m.id, m.space_id, m.kind, m.display_name, m.access_level
		 FROM members m
		 JOIN conversation_members cm ON cm.conversation_id = ? AND cm.member_id = m.id
		 WHERE m.space_id = ? AND m.retired_at IS NULL
		 ORDER BY m.kind ASC, lower(m.display_name) ASC`,
		boundary.ID, boundary.SpaceID,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list conversation members", err)
	}
	defer rows.Close()

	members := make([]domain.Member, 0)
	for rows.Next() {
		var m domain.Member
		var kind, access string
		if err := rows.Scan(&m.ID, &m.SpaceID, &kind, &m.DisplayName, &access); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "scan conversation member", err)
		}
		m.Kind = domain.MemberKind(kind)
		m.AccessLevel = domain.AccessLevel(access)
		members = append(members, m)
	}
	return members, rows.Err()
}

func (s *Service) GetOrCreateDM(ctx context.Context, actor domain.Member, peerMemberID string) (domain.Conversation, error) {
	if peerMemberID == "" || peerMemberID == actor.ID {
		return domain.Conversation{}, domain.NewError(domain.CodeInvalidArgument, "peer_member_id must be another member")
	}
	var conv domain.Conversation
	var events []domain.SpaceEvent

	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		peer, err := loadMember(ctx, tx, peerMemberID)
		if err != nil {
			return err
		}
		if peer.SpaceID != actor.SpaceID || peer.RetiredAt != nil {
			return domain.NewError(domain.CodeNotFound, "peer member not found")
		}

		// Find existing DM shared by exactly actor and peer.
		var existingID, createdAt string
		var nextSeq int64
		err = tx.QueryRowContext(ctx,
			`SELECT c.id, c.next_seq, c.created_at
			 FROM conversations c
			 JOIN conversation_members m1 ON m1.conversation_id = c.id AND m1.member_id = ?
			 JOIN conversation_members m2 ON m2.conversation_id = c.id AND m2.member_id = ?
			 WHERE c.space_id = ? AND c.kind = 'dm'
			 LIMIT 1`,
			actor.ID, peer.ID, actor.SpaceID,
		).Scan(&existingID, &nextSeq, &createdAt)
		if err == nil {
			conv = domain.Conversation{
				ID:              existingID,
				SpaceID:         actor.SpaceID,
				Kind:            domain.ConversationDM,
				NextSeq:         nextSeq,
				CreatedAt:       store.ParseTime(createdAt),
				PeerMemberID:    peer.ID,
				PeerDisplayName: peer.DisplayName,
				PeerKind:        peer.Kind,
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.WrapError(domain.CodeInternal, "query dm", err)
		}

		now := time.Now().UTC()
		dmID := uuid.NewString()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO conversations (id, space_id, kind, created_at) VALUES (?, ?, 'dm', ?)`,
			dmID, actor.SpaceID, store.FormatTime(now),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "insert dm", err)
		}
		for _, mid := range []string{actor.ID, peer.ID} {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO conversation_members (conversation_id, space_id, member_id, joined_at) VALUES (?, ?, ?, ?)`,
				dmID, actor.SpaceID, mid, store.FormatTime(now),
			); err != nil {
				return domain.WrapError(domain.CodeInternal, "insert dm member", err)
			}
		}
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.created", map[string]any{
			"conversation_id": dmID,
			"kind":            "dm",
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		conv = domain.Conversation{
			ID:              dmID,
			SpaceID:         actor.SpaceID,
			Kind:            domain.ConversationDM,
			NextSeq:         1,
			CreatedAt:       now,
			PeerMemberID:    peer.ID,
			PeerDisplayName: peer.DisplayName,
			PeerKind:        peer.Kind,
		}
		return nil
	})
	if err != nil {
		return domain.Conversation{}, err
	}
	s.dispatchPostCommit(events, nil)
	return conv, nil
}

func (s *Service) ListConversations(ctx context.Context, viewer domain.Member) ([]domain.Conversation, error) {
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT c.id, c.space_id, c.kind, COALESCE(c.slug, ''), c.topic, c.next_seq, c.created_at,
		        COALESCE(cs.read_seq, 0), COALESCE(cs.notify_level, 'mentions'), COALESCE(cs.following, 0),
		        COALESCE((SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id AND m.seq > COALESCE(cs.read_seq, 0) AND m.author_member_id <> ? AND m.deleted_at IS NULL), 0),
		        COALESCE((SELECT COUNT(*) FROM message_mentions mm WHERE mm.conversation_id = c.id AND mm.member_id = ? AND mm.seq > COALESCE(cs.read_seq, 0)), 0),
		        COALESCE(peer.id, ''), COALESCE(peer.display_name, ''), COALESCE(peer.kind, ''), COALESCE(pa.runtime_state, '')
		 FROM conversations c
		 LEFT JOIN conversation_members cm ON cm.conversation_id = c.id AND cm.member_id = ?
		 LEFT JOIN conversation_states cs ON cs.conversation_id = c.id AND cs.member_id = ?
		 LEFT JOIN conversation_members pcm ON c.kind = 'dm' AND pcm.conversation_id = c.id AND pcm.member_id <> ?
		 LEFT JOIN members peer ON peer.id = pcm.member_id
		 LEFT JOIN agents pa ON pa.member_id = peer.id
		 WHERE c.space_id = ?
		   AND c.archived_at IS NULL
		   AND (
		       (c.kind = 'public' AND ? = 'human')
		       OR (c.kind IN ('public', 'private', 'dm') AND cm.member_id IS NOT NULL)
		   )
		 ORDER BY CASE c.kind WHEN 'public' THEN 0 WHEN 'private' THEN 1 ELSE 2 END, lower(COALESCE(c.slug, peer.display_name, '')) ASC`,
		viewer.ID, viewer.ID, viewer.ID, viewer.ID, viewer.ID, viewer.SpaceID, string(viewer.Kind),
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list conversations", err)
	}
	defer rows.Close()

	out := make([]domain.Conversation, 0)
	for rows.Next() {
		var c domain.Conversation
		var kind, createdAt, notify, peerKind, peerRT string
		var following int
		if err := rows.Scan(
			&c.ID, &c.SpaceID, &kind, &c.Slug, &c.Topic, &c.NextSeq, &createdAt,
			&c.ReadSeq, &notify, &following, &c.UnreadCount, &c.MentionCount,
			&c.PeerMemberID, &c.PeerDisplayName, &peerKind, &peerRT,
		); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "scan conversation", err)
		}
		c.Kind = domain.ConversationKind(kind)
		c.CreatedAt = store.ParseTime(createdAt)
		c.NotifyLevel = domain.NotifyLevel(notify)
		c.Following = following == 1
		c.PeerKind = domain.MemberKind(peerKind)
		c.PeerRuntime = domain.RuntimeState(peerRT)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListAgentConversations is the Agent view of `sumi conversations`: visible channels and DMs plus
// the threads and task conversations the agent follows, each carrying its CLI target.
func (s *Service) ListAgentConversations(ctx context.Context, viewer domain.Member) ([]domain.Conversation, error) {
	convs, err := s.ListConversations(ctx, viewer)
	if err != nil {
		return nil, err
	}
	targets := make(map[string]string, len(convs))
	for i := range convs {
		c := &convs[i]
		c.Target = inboxTarget(c.Kind, c.ID, c.Slug, c.PeerDisplayName, 0)
		targets[c.ID] = c.Target
	}
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT c.id, c.space_id, c.kind, COALESCE(c.topic, ''), COALESCE(c.parent_conversation_id, ''), COALESCE(c.root_message_id, ''),
		        COALESCE(c.task_id, ''), COALESCE(t.number, 0), c.next_seq, c.created_at, cs.read_seq, cs.notify_level,
		        COALESCE((SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id AND m.seq > cs.read_seq AND m.author_member_id <> ? AND m.deleted_at IS NULL), 0),
		        COALESCE((SELECT COUNT(*) FROM message_mentions mm WHERE mm.conversation_id = c.id AND mm.member_id = ? AND mm.seq > cs.read_seq), 0)
		 FROM conversation_states cs
		 JOIN conversations c ON c.id = cs.conversation_id
		 LEFT JOIN tasks t ON t.id = c.task_id
		 WHERE cs.member_id = ? AND cs.following = 1 AND c.space_id = ? AND c.kind IN ('thread', 'task') AND c.archived_at IS NULL
		 ORDER BY c.created_at ASC`,
		viewer.ID, viewer.ID, viewer.ID, viewer.SpaceID,
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list followed conversations", err)
	}
	var followed []domain.Conversation
	for rows.Next() {
		var c domain.Conversation
		var kind, createdAt, notify string
		var taskNumber int64
		if err := rows.Scan(&c.ID, &c.SpaceID, &kind, &c.Topic, &c.ParentConversationID, &c.RootMessageID, &c.TaskID, &taskNumber,
			&c.NextSeq, &createdAt, &c.ReadSeq, &notify, &c.UnreadCount, &c.MentionCount); err != nil {
			_ = rows.Close()
			return nil, domain.WrapError(domain.CodeInternal, "scan followed conversation", err)
		}
		c.Kind = domain.ConversationKind(kind)
		c.CreatedAt = store.ParseTime(createdAt)
		c.NotifyLevel = domain.NotifyLevel(notify)
		c.Following = true
		c.Target = inboxTarget(c.Kind, c.ID, "", "", taskNumber)
		followed = append(followed, c)
	}
	if err := rows.Close(); err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list followed conversations", err)
	}
	for _, c := range followed {
		// Following survives losing access (e.g. removed from a private channel); re-check visibility.
		parent, ok := targets[c.ParentConversationID]
		if !ok {
			continue
		}
		c.ParentTarget = parent
		convs = append(convs, c)
	}
	return convs, nil
}

func (s *Service) ArchiveChannel(ctx context.Context, actor domain.Member, channelID string) (domain.Conversation, error) {
	if actor.Kind != domain.MemberKindHuman || (actor.AccessLevel != domain.AccessOwner && actor.AccessLevel != domain.AccessAdmin) {
		return domain.Conversation{}, domain.NewError(domain.CodePermissionDenied, "only owners and admins can archive channels")
	}
	var conv domain.Conversation
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := loadConvMeta(ctx, tx, channelID)
		if err != nil {
			return err
		}
		if c.SpaceID != actor.SpaceID {
			return domain.NewError(domain.CodePermissionDenied, "conversation belongs to another space")
		}
		if c.Kind != domain.ConversationPublic && c.Kind != domain.ConversationPrivate {
			return domain.NewError(domain.CodeInvalidArgument, "only public or private channels can be archived")
		}
		if c.ArchivedAt != nil {
			return domain.NewError(domain.CodeConflict, "channel is already archived")
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx,
			`UPDATE conversations SET archived_at = ? WHERE id = ?`,
			store.FormatTime(now), c.ID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "archive channel", err)
		}
		_, evMsg, err := insertActionMessageTx(ctx, tx, actor, c.ID, domain.ActionChannelArchived, map[string]any{
			"channel_id": c.ID,
			"slug":       c.Slug,
		})
		if err != nil {
			return err
		}
		events = append(events, evMsg)
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.updated", map[string]any{
			"conversation_id": c.ID,
			"archived":        true,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)

		conv = domain.Conversation{
			ID:         c.ID,
			SpaceID:    c.SpaceID,
			Kind:       c.Kind,
			Slug:       c.Slug,
			NextSeq:    c.NextSeq + 1,
			ArchivedAt: &now,
		}
		return nil
	})
	if err != nil {
		return domain.Conversation{}, err
	}
	s.dispatchPostCommit(events, nil)
	return conv, nil
}

// UnarchiveChannel restores an archived channel so members can post and receive notifications again.
func (s *Service) UnarchiveChannel(ctx context.Context, actor domain.Member, channelID string) (domain.Conversation, error) {
	if actor.Kind != domain.MemberKindHuman || (actor.AccessLevel != domain.AccessOwner && actor.AccessLevel != domain.AccessAdmin) {
		return domain.Conversation{}, domain.NewError(domain.CodePermissionDenied, "only owners and admins can unarchive channels")
	}
	var conv domain.Conversation
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		c, err := loadConvMeta(ctx, tx, channelID)
		if err != nil {
			return err
		}
		if c.SpaceID != actor.SpaceID {
			return domain.NewError(domain.CodePermissionDenied, "conversation belongs to another space")
		}
		if c.Kind != domain.ConversationPublic && c.Kind != domain.ConversationPrivate {
			return domain.NewError(domain.CodeInvalidArgument, "only public or private channels can be unarchived")
		}
		if c.ArchivedAt == nil {
			return domain.NewError(domain.CodeConflict, "channel is not archived")
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE conversations SET archived_at = NULL WHERE id = ?`,
			c.ID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "unarchive channel", err)
		}
		_, evMsg, err := insertActionMessageTx(ctx, tx, actor, c.ID, domain.ActionChannelUnarchived, map[string]any{
			"channel_id": c.ID,
			"slug":       c.Slug,
		})
		if err != nil {
			return err
		}
		events = append(events, evMsg)
		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.updated", map[string]any{
			"conversation_id": c.ID,
			"archived":        false,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)

		conv = domain.Conversation{
			ID:      c.ID,
			SpaceID: c.SpaceID,
			Kind:    c.Kind,
			Slug:    c.Slug,
			NextSeq: c.NextSeq + 1,
		}
		return nil
	})
	if err != nil {
		return domain.Conversation{}, err
	}
	s.dispatchPostCommit(events, nil)
	return conv, nil
}

// ListArchivedChannels returns archived public/private channels visible to viewer.
func (s *Service) ListArchivedChannels(ctx context.Context, viewer domain.Member) ([]domain.Conversation, error) {
	isAdmin := viewer.Kind == domain.MemberKindHuman && (viewer.AccessLevel == domain.AccessOwner || viewer.AccessLevel == domain.AccessAdmin)
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT c.id, c.space_id, c.kind, COALESCE(c.slug, ''), c.topic, c.next_seq, c.created_at, c.archived_at
		 FROM conversations c
		 LEFT JOIN conversation_members cm ON cm.conversation_id = c.id AND cm.member_id = ?
		 WHERE c.space_id = ?
		   AND c.kind IN ('public', 'private')
		   AND c.archived_at IS NOT NULL
		   AND (? = 1 OR (c.kind = 'public' AND ? = 'human') OR cm.member_id IS NOT NULL)
		 ORDER BY c.archived_at DESC, lower(COALESCE(c.slug, '')) ASC`,
		viewer.ID, viewer.SpaceID, boolToInt(isAdmin), string(viewer.Kind),
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list archived channels", err)
	}
	defer rows.Close()
	out := make([]domain.Conversation, 0)
	for rows.Next() {
		var c domain.Conversation
		var kind, createdAt, archivedAt string
		if err := rows.Scan(&c.ID, &c.SpaceID, &kind, &c.Slug, &c.Topic, &c.NextSeq, &createdAt, &archivedAt); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "scan archived channel", err)
		}
		c.Kind = domain.ConversationKind(kind)
		c.CreatedAt = store.ParseTime(createdAt)
		t := store.ParseTime(archivedAt)
		c.ArchivedAt = &t
		out = append(out, c)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
