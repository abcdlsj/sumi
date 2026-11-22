package domain

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

type MemberKind string

const (
	MemberKindHuman MemberKind = "human"
	MemberKindAgent MemberKind = "agent"
)

type AccessLevel string

const (
	AccessOwner  AccessLevel = "owner"
	AccessAdmin  AccessLevel = "admin"
	AccessMember AccessLevel = "member"
)

type Permission string

const (
	PermAgentCreate          Permission = "agent.create"
	PermChannelCreate        Permission = "channel.create"
	PermChannelManageMembers Permission = "channel.manage_members"
)

type DriverKind string

const (
	DriverACP     DriverKind = "acp"
	DriverBuiltin DriverKind = "builtin"
)

type RuntimeState string

const (
	RuntimeIdle    RuntimeState = "idle"
	RuntimeWorking RuntimeState = "working"
	RuntimeOffline RuntimeState = "offline"
	RuntimeError   RuntimeState = "error"
)

type ConversationKind string

const (
	ConversationPublic  ConversationKind = "public"
	ConversationPrivate ConversationKind = "private"
	ConversationDM      ConversationKind = "dm"
	ConversationThread  ConversationKind = "thread"
	ConversationTask    ConversationKind = "task"
)

type MessageKind string

const (
	MessageKindText   MessageKind = "text"
	MessageKindAction MessageKind = "action"
	MessageKindSystem MessageKind = "system"
)

type ActionKind string

const (
	ActionChannelCreated    ActionKind = "channel_created"
	ActionChannelArchived   ActionKind = "channel_archived"
	ActionChannelUnarchived ActionKind = "channel_unarchived"
	ActionAgentCreated      ActionKind = "agent_created"
	ActionTaskEvent         ActionKind = "task_event"
	ActionActionCard        ActionKind = "action_card"
)

type ActionCardType string

const (
	ActionCardAgentCreate ActionCardType = "agent.create"
)

type ActionCardStatus string

const (
	ActionCardPending   ActionCardStatus = "pending"
	ActionCardExecuted  ActionCardStatus = "executed"
	ActionCardDismissed ActionCardStatus = "dismissed"
)

// ActionCardPayload is the generic envelope stored in payload_json for action_kind="action_card" messages.
// Type-specific inputs and outcomes live in Params and Result so future card types need no schema changes.
type ActionCardPayload struct {
	Version            int              `json:"version"`
	CardType           ActionCardType   `json:"card_type"`
	Status             ActionCardStatus `json:"status"`
	Title              string           `json:"title"`
	Description        string           `json:"description,omitempty"`
	Params             json.RawMessage  `json:"params,omitempty"`
	Result             json.RawMessage  `json:"result,omitempty"`
	ExecutedByMemberID string           `json:"executed_by_member_id,omitempty"`
	ExecutedByName     string           `json:"executed_by_name,omitempty"`
	ExecutedAt         *time.Time       `json:"executed_at,omitempty"`
}

type AgentCreateCardParams struct {
	DisplayName  string           `json:"display_name,omitempty"`
	RoleText     string           `json:"role_text,omitempty"`
	ComputerID   string           `json:"computer_id,omitempty"`
	DriverKind   DriverKind       `json:"driver_kind,omitempty"`
	DriverConfig DriverConfigView `json:"driver_config,omitempty"`
}

type AgentCreateCardResult struct {
	AgentID     string `json:"agent_id"`
	DisplayName string `json:"display_name"`
}

type TaskStatus string

const (
	TaskTodo       TaskStatus = "todo"
	TaskInProgress TaskStatus = "in_progress"
	TaskInReview   TaskStatus = "in_review"
	TaskDone       TaskStatus = "done"
	TaskClosed     TaskStatus = "closed"
)

type NotifyLevel string

const (
	NotifyAll      NotifyLevel = "all"
	NotifyMentions NotifyLevel = "mentions"
	NotifyMute     NotifyLevel = "mute"
)

type User struct {
	ID              string    `json:"id"`
	EmailNormalized string    `json:"email"`
	DisplayName     string    `json:"display_name"`
	CreatedAt       time.Time `json:"created_at"`
}

type Space struct {
	ID            string    `json:"id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	Accent        string    `json:"accent"`
	OwnerMemberID string    `json:"owner_member_id"`
	EventSeq      int64     `json:"event_seq"`
	CreatedAt     time.Time `json:"created_at"`
}

type Member struct {
	ID          string       `json:"id"`
	SpaceID     string       `json:"space_id"`
	Kind        MemberKind   `json:"kind"`
	DisplayName string       `json:"display_name"`
	AccessLevel AccessLevel  `json:"access_level"`
	Permissions []Permission `json:"permissions,omitempty"`
	Agent       *AgentInfo   `json:"agent,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	RetiredAt   *time.Time   `json:"retired_at,omitempty"`
}

type AgentInfo struct {
	ComputerID            string           `json:"computer_id,omitempty"`
	RoleText              string           `json:"role_text"`
	DriverKind            DriverKind       `json:"driver_kind"`
	DriverConfig          DriverConfigView `json:"driver_config"`
	RuntimeState          RuntimeState     `json:"runtime_state"`
	FocusConversationID   string           `json:"focus_conversation_id,omitempty"`
	LastErrorCode         string           `json:"last_error_code,omitempty"`
	LastErrorDetail       string           `json:"last_error_detail,omitempty"`
	RuntimeStateUpdatedAt *time.Time       `json:"runtime_state_updated_at,omitempty"`
	// Activity is ephemeral in-turn progress, only present while working; never persisted.
	Activity *AgentActivity `json:"activity,omitempty"`
}

// AgentActivity names the tool kind an Agent is running and, when it has produced no runtime
// event for a while, how long it has been silent. It never carries tool arguments or output.
type AgentActivity struct {
	Tool           string `json:"tool,omitempty"`
	StalledSeconds int64  `json:"stalled_seconds,omitempty"`
}

// SanitizeActivity bounds an activity reported by a Computer to a short tool identifier.
func SanitizeActivity(a *AgentActivity) *AgentActivity {
	if a == nil {
		return nil
	}
	tool := make([]rune, 0, 32)
	for _, r := range a.Tool {
		if len(tool) == 32 {
			break
		}
		if r == '_' || r == '-' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			tool = append(tool, r)
		}
	}
	out := &AgentActivity{Tool: string(tool), StalledSeconds: max(a.StalledSeconds, 0)}
	if out.Tool == "" && out.StalledSeconds == 0 {
		return nil
	}
	return out
}

type Computer struct {
	ID string `json:"id"`
	// OwnerUserID is never sent to browsers; OwnerMemberID is the owner's member id in the viewing Space.
	OwnerUserID      string     `json:"-"`
	OwnerMemberID    string     `json:"owner_member_id,omitempty"`
	Name             string     `json:"name"`
	Hostname         string     `json:"hostname"`
	OS               string     `json:"os"`
	ConnectionStatus string     `json:"connection_status"`
	DaemonVersion    string     `json:"daemon_version,omitempty"`
	ACPProviders     []string   `json:"acp_providers"`
	LastSeenAt       *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

type Conversation struct {
	ID                   string           `json:"id"`
	SpaceID              string           `json:"space_id"`
	Kind                 ConversationKind `json:"kind"`
	Slug                 string           `json:"slug,omitempty"`
	Topic                string           `json:"topic,omitempty"`
	ParentConversationID string           `json:"parent_conversation_id,omitempty"`
	RootMessageID        string           `json:"root_message_id,omitempty"`
	TaskID               string           `json:"task_id,omitempty"`
	NextSeq              int64            `json:"next_seq"`
	CreatedAt            time.Time        `json:"created_at"`
	ArchivedAt           *time.Time       `json:"archived_at,omitempty"`
	// Viewer-specific attention projections (optional, populated for list/read responses).
	ReadSeq         int64        `json:"read_seq"`
	UnreadCount     int64        `json:"unread_count"`
	MentionCount    int64        `json:"mention_count"`
	NotifyLevel     NotifyLevel  `json:"notify_level,omitempty"`
	Following       bool         `json:"following"`
	PeerMemberID    string       `json:"peer_member_id,omitempty"`
	PeerDisplayName string       `json:"peer_display_name,omitempty"`
	PeerKind        MemberKind   `json:"peer_kind,omitempty"`
	PeerRuntime     RuntimeState `json:"peer_runtime_state,omitempty"`
	// Target and ParentTarget are CLI addresses, set on Agent-facing listings only.
	Target       string `json:"target,omitempty"`
	ParentTarget string `json:"parent_target,omitempty"`
}

type Attachment struct {
	ID               string    `json:"id"`
	SpaceID          string    `json:"space_id"`
	UploaderMemberID string    `json:"uploader_member_id"`
	Name             string    `json:"name"`
	MediaType        string    `json:"media_type"`
	SizeBytes        int64     `json:"size_bytes"`
	MessageID        string    `json:"message_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type ReactionGroup struct {
	Emoji     string   `json:"emoji"`
	Count     int      `json:"count"`
	MemberIDs []string `json:"member_ids"`
	Reacted   bool     `json:"reacted"`
}

type Message struct {
	ID               string               `json:"id"`
	SpaceID          string               `json:"space_id"`
	ConversationID   string               `json:"conversation_id"`
	Seq              int64                `json:"seq"`
	AuthorMemberID   string               `json:"author_member_id"`
	AuthorName       string               `json:"author_display_name,omitempty"`
	AuthorKind       MemberKind           `json:"author_kind,omitempty"`
	Kind             MessageKind          `json:"kind"`
	BodyMarkdown     string               `json:"body_markdown"`
	ActionKind       ActionKind           `json:"action_kind,omitempty"`
	PayloadJSON      string               `json:"payload_json,omitempty"`
	MentionAll       bool                 `json:"mention_all,omitempty"`
	MentionMemberIDs []string             `json:"mention_member_ids,omitempty"`
	ReplyCount       int64                `json:"reply_count,omitempty"`
	ThreadConvID     string               `json:"thread_conversation_id,omitempty"`
	LastReplyAt      *time.Time           `json:"last_reply_at,omitempty"`
	LatestReplies    []ThreadReplyPreview `json:"latest_replies,omitempty"`
	AttachedTasks    []Task               `json:"attached_tasks,omitempty"`
	Attachments      []Attachment         `json:"attachments,omitempty"`
	Reactions        []ReactionGroup      `json:"reactions,omitempty"`
	CreatedAt        time.Time            `json:"created_at"`
	EditedAt         *time.Time           `json:"edited_at,omitempty"`
	DeletedAt        *time.Time           `json:"deleted_at,omitempty"`
}

// ThreadReplyPreview is the one-line excerpt of a thread reply that rides along
// with its root message, so a channel row can show what the thread says.
// ReplyCount stays authoritative; this is the newest few, display only.
type ThreadReplyPreview struct {
	MessageID  string     `json:"message_id"`
	Seq        int64      `json:"seq"`
	AuthorID   string     `json:"author_member_id"`
	AuthorName string     `json:"author_display_name,omitempty"`
	AuthorKind MemberKind `json:"author_kind,omitempty"`
	Preview    string     `json:"preview"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Task struct {
	ID               string     `json:"id"`
	SpaceID          string     `json:"space_id"`
	Number           int64      `json:"number"`
	ChannelID        string     `json:"channel_id"`
	ChannelSlug      string     `json:"channel_slug,omitempty"`
	SourceMessageID  string     `json:"source_message_id,omitempty"`
	ConversationID   string     `json:"conversation_id,omitempty"`
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	Status           TaskStatus `json:"status"`
	CreatorMemberID  string     `json:"creator_member_id"`
	AssigneeMemberID string     `json:"assignee_member_id,omitempty"`
	AssigneeName     string     `json:"assignee_display_name,omitempty"`
	AssigneeKind     MemberKind `json:"assignee_kind,omitempty"`
	Revision         int64      `json:"revision"`
	MessageCount     int64      `json:"message_count"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type ConversationState struct {
	ConversationID string      `json:"conversation_id"`
	MemberID       string      `json:"member_id"`
	ReadSeq        int64       `json:"read_seq"`
	NotifyLevel    NotifyLevel `json:"notify_level"`
	Following      bool        `json:"following"`
	DoneSeq        int64       `json:"done_seq"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type InboxItem struct {
	ConversationID   string           `json:"conversation_id"`
	Target           string           `json:"target"`
	Kind             ConversationKind `json:"kind"`
	Title            string           `json:"title"`
	ParentSlug       string           `json:"parent_slug,omitempty"`
	TaskNumber       int64            `json:"task_number,omitempty"`
	TaskID           string           `json:"task_id,omitempty"`
	UnreadCount      int64            `json:"unread_count"`
	MentionCount     int64            `json:"mention_count"`
	NeedsAttention   bool             `json:"needs_attention"`
	Reason           string           `json:"reason"`
	LatestSeq        int64            `json:"latest_seq"`
	LatestAuthorName string           `json:"latest_author_name,omitempty"`
	LatestPreview    string           `json:"latest_preview,omitempty"`
	LatestAt         time.Time        `json:"latest_at"`
	Following        bool             `json:"following"`
	NotifyLevel      NotifyLevel      `json:"notify_level"`
}

type SpaceEvent struct {
	Sequence    int64     `json:"sequence"`
	SpaceID     string    `json:"space_id"`
	Kind        string    `json:"kind"`
	PayloadJSON string    `json:"payload_json"`
	CreatedAt   time.Time `json:"created_at"`
}

// ChannelGroup is a member-owned collapsible sidebar group for organizing channels.
type ChannelGroup struct {
	ID              string    `json:"id"`
	SpaceID         string    `json:"space_id"`
	Name            string    `json:"name"`
	Position        int       `json:"position"`
	Collapsed       bool      `json:"collapsed"`
	ConversationIDs []string  `json:"conversation_ids"`
	CreatedAt       time.Time `json:"created_at"`
}

var (
	displayNameRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	slugRe        = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

func ValidateDisplayName(name string) error {
	if !displayNameRe.MatchString(name) || strings.EqualFold(name, "all") {
		return NewError(CodeInvalidArgument, "display_name must be 1-32 alphanumeric/underscore characters and not 'all'")
	}
	return nil
}

// ReactionEmojis is the fixed allowlist of curated reaction emoji codes.
var ReactionEmojis = []string{"thumbsup", "eyes", "check", "rocket", "fire", "sparkles", "thinking", "party"}

func ValidateReactionEmoji(emoji string) error {
	for _, e := range ReactionEmojis {
		if e == emoji {
			return nil
		}
	}
	return NewError(CodeInvalidArgument, "emoji must be one of "+strings.Join(ReactionEmojis, ", "))
}

// SpaceAccents is the allowlist of Space accent names; the Web UI maps each to its palette.
var SpaceAccents = []string{"flame", "gold", "rose", "iris", "sky"}

// DefaultSpaceAccent is the accent a new Space starts with.
const DefaultSpaceAccent = "flame"

func ValidateSpaceAccent(accent string) error {
	for _, a := range SpaceAccents {
		if a == accent {
			return nil
		}
	}
	return NewError(CodeInvalidArgument, "accent must be one of flame, gold, rose, iris, sky")
}

func ValidateSlug(slug string) error {
	if len(slug) < 1 || len(slug) > 32 || !slugRe.MatchString(slug) {
		return NewError(CodeInvalidArgument, "slug must be 1-32 lowercase alphanumeric characters with single hyphens")
	}
	return nil
}

func ValidateTaskStatus(s TaskStatus) error {
	switch s {
	case TaskTodo, TaskInProgress, TaskInReview, TaskDone, TaskClosed:
		return nil
	default:
		return NewError(CodeInvalidArgument, "invalid task status")
	}
}

func ValidateNotifyLevel(n NotifyLevel) error {
	switch n {
	case NotifyAll, NotifyMentions, NotifyMute:
		return nil
	default:
		return NewError(CodeInvalidArgument, "invalid notification level")
	}
}

func ValidatePermission(p Permission) error {
	switch p {
	case PermAgentCreate, PermChannelCreate, PermChannelManageMembers:
		return nil
	default:
		return NewError(CodeInvalidArgument, "invalid permission")
	}
}

const (
	// MaxAttachmentBytes bounds a single uploaded file.
	MaxAttachmentBytes int64 = 25 << 20
	// MaxAttachmentsPerMessage bounds attachments carried by one message.
	MaxAttachmentsPerMessage = 10
)

func ValidateAttachmentName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len(trimmed) > 200 {
		return NewError(CodeInvalidArgument, "attachment name must be 1-200 characters")
	}
	if strings.ContainsAny(trimmed, "/\\\x00") || trimmed == "." || trimmed == ".." {
		return NewError(CodeInvalidArgument, "attachment name must not contain path separators")
	}
	return nil
}
