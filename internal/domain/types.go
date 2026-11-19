package domain

import (
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
	ActionChannelCreated ActionKind = "channel_created"
	ActionAgentCreated   ActionKind = "agent_created"
	ActionTaskEvent      ActionKind = "task_event"
)

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
	ComputerID            string       `json:"computer_id,omitempty"`
	RoleText              string       `json:"role_text"`
	DriverKind            DriverKind   `json:"driver_kind"`
	DriverConfigJSON      string       `json:"driver_config_json"`
	RuntimeState          RuntimeState `json:"runtime_state"`
	FocusConversationID   string       `json:"focus_conversation_id,omitempty"`
	LastErrorCode         string       `json:"last_error_code,omitempty"`
	RuntimeStateUpdatedAt *time.Time   `json:"runtime_state_updated_at,omitempty"`
}

type Computer struct {
	ID               string     `json:"id"`
	SpaceID          string     `json:"space_id"`
	Name             string     `json:"name"`
	Hostname         string     `json:"hostname"`
	OS               string     `json:"os"`
	ConnectionStatus string     `json:"connection_status"`
	DaemonVersion    string     `json:"daemon_version,omitempty"`
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

type Message struct {
	ID               string       `json:"id"`
	SpaceID          string       `json:"space_id"`
	ConversationID   string       `json:"conversation_id"`
	Seq              int64        `json:"seq"`
	AuthorMemberID   string       `json:"author_member_id"`
	AuthorName       string       `json:"author_display_name,omitempty"`
	AuthorKind       MemberKind   `json:"author_kind,omitempty"`
	Kind             MessageKind  `json:"kind"`
	BodyMarkdown     string       `json:"body_markdown"`
	ActionKind       ActionKind   `json:"action_kind,omitempty"`
	PayloadJSON      string       `json:"payload_json,omitempty"`
	MentionAll       bool         `json:"mention_all,omitempty"`
	MentionMemberIDs []string     `json:"mention_member_ids,omitempty"`
	ReplyCount       int64        `json:"reply_count,omitempty"`
	ThreadConvID     string       `json:"thread_conversation_id,omitempty"`
	LastReplyAt      *time.Time   `json:"last_reply_at,omitempty"`
	AttachedTasks    []Task       `json:"attached_tasks,omitempty"`
	Attachments      []Attachment `json:"attachments,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
	EditedAt         *time.Time   `json:"edited_at,omitempty"`
	DeletedAt        *time.Time   `json:"deleted_at,omitempty"`
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
