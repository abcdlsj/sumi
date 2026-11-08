use super::http::{
    AddChannelAgentsBody, CloseTaskBody, CompleteTaskBody, CompleteUploadBody, ConfirmPairingBody,
    CreateAgentBody, CreateChannelBody, CreateMessageBody, CreateSpaceBody, CreateTaskBody,
    CreateUploadBody, ErrorBody, ErrorEnvelope, InviteHumanBody, LifecycleActionBody,
    LinkThreadBody, LoginBody, OpenDirectMessageBody, ReadMemoryBody, RegisterBody,
    UpdateAgentBody, UpdateMemberBody, UpdateMessageBody,
};
use anyhow::Context;
use serde::{Deserialize, Serialize};
use utoipa::{OpenApi, ToSchema};
use uuid::Uuid;

#[derive(OpenApi)]
#[openapi(
    info(title = "Sumi Browser API", version = "1"),
    components(schemas(
        RegisterBody,
        UserResponse,
        RegisterResponse,
        LoginBody,
        LoginResponse,
        ErrorEnvelope,
        ErrorBody,
        CreateSpaceBody,
        SpaceResponse,
        ConfirmPairingBody,
        ComputerResponse,
        PairingDetailsResponse,
        CreateAgentBody,
        AgentResponse,
        AgentActivityResponse,
        AttentionConfig,
        MemoryFileResponse,
        ReadMemoryBody,
        MemoryContentResponse,
        UpdateAgentBody,
        LifecycleActionBody,
        SuspendMode,
        LifecycleActionKind,
        MemberResponse,
        UpdateMemberBody,
        InviteHumanBody,
        InvitationResponse,
        CreatedInvitationResponse,
        ChannelResponse,
        ChannelMembersResponse,
        ChannelListResponse,
        DirectMessageResponse,
        OpenDirectMessageBody,
        CreateChannelBody,
        AddChannelAgentsBody,
        MessageAuthor,
        MessageContentResponse,
        MessageTaskSummary,
        MessageTaskRefResponse,
        MessageResponse,
        ReplyPreviewResponse,
        MessagePageResponse,
        CreateMessageBody,
        UpdateMessageBody,
        AttachmentResponse,
        CreateUploadBody,
        CompleteUploadBody,
        InboxItemResponse,
        MarkAllInboxReadResponse,
        TaskStatus,
        RunStatus,
        SessionContinuityState,
        ThreadReferenceResponse,
        RunResponse,
        SessionContinuityResponse,
        RuntimeRunState,
        RuntimeDiagnosticsResponse,
        TaskResponse,
        CreateTaskBody,
        LinkThreadBody,
        CompleteTaskBody,
        CloseTaskBody,
        AgentRuntimeResponse,
        AgentGraphResponse,
        AgentGraphNodeResponse,
        AgentGraphEdgeResponse,
        AgentGraphMessageResponse,
        LlmUsageResponse,
        LlmUsageBucketResponse,
        LlmUsageBreakdownResponse,
        LlmUsageAgentSeriesResponse,
        LlmUsageAgentModelResponse,
        ThreadReadResponse,
        ThreadSubscriptionResponse
    ))
)]
struct BrowserApi;

pub(in crate::server) fn write_browser_openapi() -> anyhow::Result<()> {
    let schema = serde_json::to_string_pretty(&BrowserApi::openapi())
        .context("failed to serialize Browser API OpenAPI schema")?;
    println!("{schema}");
    Ok(())
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct UserResponse {
    pub(super) id: Uuid,
    pub(super) display_name: String,
    pub(super) email: String,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct RegisterResponse {
    pub(super) user: UserResponse,
    pub(super) next: String,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct LoginResponse {
    pub(super) user: UserResponse,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct SpaceResponse {
    pub(super) id: Uuid,
    pub(super) name: String,
    pub(super) slug: String,
    pub(super) accent: String,
    pub(super) owner_member_id: Uuid,
    pub(super) current_member_id: Uuid,
    pub(super) general_channel_id: Uuid,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct ComputerResponse {
    pub(super) id: Uuid,
    pub(super) space_id: Uuid,
    pub(super) name: String,
    pub(super) hostname: String,
    pub(super) os: ComputerOs,
    pub(super) daemon_version: String,
    pub(super) status: ComputerStatus,
    pub(super) last_seen_at: Option<String>,
    pub(super) created_at: String,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum ComputerOs {
    Macos,
    Linux,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum ComputerStatus {
    Online,
    Offline,
    Revoked,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct PairingDetailsResponse {
    pairing_id: Uuid,
    hostname: String,
    os: String,
    daemon_version: String,
    token_fingerprint: String,
    status: PairingStatus,
    expires_at: String,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum PairingStatus {
    Pending,
    Confirmed,
    Expired,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct AttentionConfig {
    pub(super) dm_immediate: bool,
    pub(super) mention_immediate: bool,
    pub(super) ambient_enabled: bool,
    pub(super) ambient_debounce_seconds: u32,
    pub(super) ambient_max_wait_seconds: u32,
    pub(super) max_retry_count: u32,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum DriverKind {
    Acp,
    Builtin,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum AcpProvider {
    Codex,
    Claude,
    Pi,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum AgentAccessLevel {
    Member,
    Admin,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct AgentActivityResponse {
    pub(super) kind: String,
    pub(super) label: String,
    pub(super) status: AgentActivityStatus,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct AgentResponse {
    pub(super) member_id: Uuid,
    pub(super) space_id: Uuid,
    pub(super) computer_id: Option<Uuid>,
    pub(super) name: String,
    pub(super) access_level: AgentAccessLevel,
    pub(super) role_text: String,
    pub(super) role_revision: u64,
    pub(super) desired_lifecycle: AgentLifecycle,
    pub(super) provision_status: ProvisionStatus,
    pub(super) activity_status: AgentActivityStatus,
    /// Whether the Computer hosting this Agent is currently connected. Reported separately from
    /// `activity_status` so a Run in progress is not relabelled as unreachable.
    pub(super) computer_reachable: bool,
    pub(super) driver_kind: DriverKind,
    /// Set when `driver_kind` is `acp`.
    pub(super) driver_provider: Option<AcpProvider>,
    pub(super) attention_config: AttentionConfig,
    pub(super) activity: Option<AgentActivityResponse>,
    pub(super) last_error_code: Option<String>,
    pub(super) memory_files: Vec<MemoryFileResponse>,
    pub(super) created_at: String,
    pub(super) updated_at: String,
    pub(super) retired_at: Option<String>,
}
#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum AgentLifecycle {
    Active,
    Suspended,
    Retired,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum ProvisionStatus {
    Provisioning,
    Ready,
    Error,
}
#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
/// What the Agent is doing. Independent of whether its Computer is reachable: an Agent can be
/// `working` while `computer_reachable` is false, which is the honest description of a Run in progress
/// on a Computer we cannot currently reach.
pub(super) enum AgentActivityStatus {
    Idle,
    Dispatched,
    Working,
    Suspended,
    Error,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct MemoryFileResponse {
    pub(super) path: String,
    pub(super) size: u64,
    pub(super) sha256: String,
    pub(super) updated_at: String,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct MemoryContentResponse {
    pub(super) path: String,
    pub(super) size: u64,
    pub(super) sha256: String,
    pub(super) updated_at: String,
    pub(super) content: String,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum LifecycleActionKind {
    Suspend,
    Resume,
    Restart,
    Retry,
    Retire,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum SuspendMode {
    StopAfterCurrent,
    CancelNow,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct MemberResponse {
    pub(super) id: Uuid,
    pub(super) kind: MemberKind,
    pub(super) display_name: String,
    pub(super) access_level: AccessLevel,
    pub(super) permissions: Vec<String>,
}
#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum MemberKind {
    Human,
    Agent,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum AccessLevel {
    Owner,
    Admin,
    Member,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct InvitationResponse {
    pub(super) id: Uuid,
    pub(super) space_id: Uuid,
    pub(super) space_name: String,
    pub(super) space_slug: String,
    pub(super) email: String,
    pub(super) expires_at: String,
    pub(super) accepted_at: Option<String>,
    pub(super) accepted_by_member_id: Option<Uuid>,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct CreatedInvitationResponse {
    pub(super) id: Uuid,
    pub(super) space_id: Uuid,
    pub(super) space_name: String,
    pub(super) space_slug: String,
    pub(super) email: String,
    pub(super) expires_at: String,
    pub(super) accepted_at: Option<String>,
    pub(super) accepted_by_member_id: Option<Uuid>,
    pub(super) token: Option<String>,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct ChannelResponse {
    pub(super) id: Uuid,
    pub(super) space_id: Uuid,
    pub(super) slug: String,
    pub(super) topic: Option<String>,
    pub(super) kind: ChannelKind,
    pub(super) created_by_member_id: Uuid,
    pub(super) joined: bool,
    pub(super) archived_at: Option<String>,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum ChannelKind {
    Public,
    Private,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct ChannelMembersResponse {
    pub(super) members: Vec<MemberResponse>,
    pub(super) can_manage: bool,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct ChannelListResponse {
    pub(super) channels: Vec<ChannelResponse>,
    pub(super) can_create: bool,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct DirectMessageResponse {
    pub(super) channel_id: Uuid,
    pub(super) space_id: Uuid,
    pub(super) other_member: MemberResponse,
    pub(super) created_at: String,
}

#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct MessageAuthor {
    pub(super) id: Uuid,
    pub(super) kind: MemberKind,
    pub(super) display_name: String,
}
#[derive(Clone, Serialize, Deserialize, ToSchema)]
#[serde(tag = "type", rename_all = "snake_case")]
pub(super) enum MessageContentResponse {
    Text { body_markdown: String },
    ChannelCreated { channel: ActionChannelResponse },
    AgentCreated { agent: ActionAgentResponse },
    SystemNotice { body_markdown: String },
}
#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct ActionChannelResponse {
    pub(super) id: Uuid,
    pub(super) slug: String,
    pub(super) available: bool,
}
#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct ActionAgentResponse {
    pub(super) member_id: Uuid,
    pub(super) name: String,
    pub(super) lifecycle: AgentLifecycle,
    pub(super) available: bool,
}
#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct MessageTaskSummary {
    pub(super) id: Uuid,
    pub(super) seq: u64,
    pub(super) title: String,
    pub(super) status: TaskStatus,
    pub(super) assignee_agent_member_id: Option<Uuid>,
    pub(super) assignee_name: Option<String>,
    pub(super) working_elsewhere: bool,
}
#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct MessageTaskRefResponse {
    pub(super) seq: u64,
    pub(super) task_id: Uuid,
    pub(super) title: String,
    pub(super) status: TaskStatus,
}
#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct AttentionFailureResponse {
    pub(super) agent_member_id: Uuid,
    pub(super) error_code: String,
    pub(super) retrying: bool,
}
#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct MessageResponse {
    pub(super) id: Uuid,
    pub(super) channel_id: Uuid,
    pub(super) thread_id: Uuid,
    pub(super) placement: MessagePlacement,
    pub(super) seq: u64,
    pub(super) author: MessageAuthor,
    pub(super) content: MessageContentResponse,
    pub(super) mentions: Vec<Uuid>,
    pub(super) mention_all: bool,
    pub(super) attachments: Vec<AttachmentResponse>,
    pub(super) reply_count: u64,
    /// The newest replies of a Root Message, oldest first, for the Channel timeline preview. Empty
    /// for a reply.
    pub(super) latest_replies: Vec<ReplyPreviewResponse>,
    pub(super) task: Option<MessageTaskSummary>,
    pub(super) task_refs: Vec<MessageTaskRefResponse>,
    pub(super) attention_failures: Vec<AttentionFailureResponse>,
    pub(super) created_at: String,
    pub(super) edited_at: Option<String>,
    pub(super) deleted_at: Option<String>,
}
#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct ReplyPreviewResponse {
    pub(super) id: Uuid,
    pub(super) author: MessageAuthor,
    pub(super) content: MessageContentResponse,
    pub(super) deleted_at: Option<String>,
}
#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum MessagePlacement {
    Root,
    Reply,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct MessagePageResponse {
    pub(super) channel_id: Uuid,
    pub(super) snapshot_channel_seq: u64,
    pub(super) messages: Vec<MessageResponse>,
    pub(super) has_more_before: bool,
    pub(super) has_more_after: bool,
}

#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct AttachmentResponse {
    pub(super) id: Uuid,
    pub(super) space_id: Uuid,
    pub(super) uploader_member_id: Uuid,
    pub(super) original_name: String,
    pub(super) media_type: String,
    pub(super) size: Option<u64>,
    pub(super) sha256: Option<String>,
    pub(super) status: AttachmentStatus,
    pub(super) upload_path: Option<String>,
    pub(super) download_path: Option<String>,
    pub(super) created_at: String,
}
#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum AttachmentStatus {
    Uploading,
    Ready,
    Deleted,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct InboxItemResponse {
    pub(super) id: Uuid,
    pub(super) space_id: Uuid,
    pub(super) member_id: Uuid,
    pub(super) kind: InboxKind,
    pub(super) priority: InboxPriority,
    pub(super) channel_id: Option<Uuid>,
    pub(super) channel_slug: Option<String>,
    pub(super) thread_id: Option<Uuid>,
    pub(super) message_id: Option<Uuid>,
    pub(super) sender_member_id: Option<Uuid>,
    pub(super) sender_display_name: Option<String>,
    /// A bounded preview of the source Message body, present only when the reader is a member of
    /// the source Channel. Full content is read through the Message API.
    pub(super) message_preview: Option<String>,
    pub(super) activity_events: Vec<InboxActivityEventResponse>,
    pub(super) summary: String,
    pub(super) status: InboxStatus,
    pub(super) available_at: String,
    pub(super) created_at: String,
    /// Failed delivery attempts so far. Reaching `max_retry_count` retires the Item as `dead`.
    pub(super) retry_count: u32,
    /// Times a governor returned this Item from `dead` to the queue.
    pub(super) requeue_count: u32,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct MarkAllInboxReadResponse {
    /// Number of pending Items marked handled.
    pub(super) count: u32,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct InboxActivityEventResponse {
    pub(super) sequence: u64,
    pub(super) kind: InboxActivityEventKind,
    pub(super) message_id: Option<Uuid>,
    pub(super) member_id: Option<Uuid>,
}

#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum InboxActivityEventKind {
    Message,
    MemberJoined,
    MemberLeft,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum InboxKind {
    Direct,
    Mention,
    Reply,
    TaskActivity,
    ThreadActivity,
    ChannelActivity,
    System,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum InboxPriority {
    Hard,
    Ambient,
}
#[derive(Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum InboxStatus {
    Pending,
    Assigned,
    Deferred,
    Handled,
    Dead,
}

#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum TaskStatus {
    Todo,
    InProgress,
    InReview,
    Done,
    Closed,
}
#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum RunStatus {
    Dispatched,
    Working,
    Completed,
    Yielded,
    Failed,
    Canceled,
}
#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum SessionContinuityState {
    Warm,
    Cold,
    ResetRequired,
    Unavailable,
}
#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct ThreadReferenceResponse {
    pub(super) id: Uuid,
    pub(super) channel_id: Uuid,
    pub(super) channel_slug: Option<String>,
    pub(super) root_message_id: Uuid,
    pub(super) root_message_seq: u64,
    pub(super) relation: ThreadRelation,
}
#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum ThreadRelation {
    Source,
    Related,
}
#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct RunResponse {
    pub(super) id: Uuid,
    pub(super) task_id: Option<Uuid>,
    pub(super) agent_member_id: Uuid,
    pub(super) agent_name: String,
    pub(super) focus: ThreadReferenceResponse,
    pub(super) status: RunStatus,
    pub(super) outcome: Option<RunOutcome>,
    pub(super) continuation_note: Option<String>,
    pub(super) error_code: Option<String>,
    pub(super) started_at: Option<String>,
    pub(super) finished_at: Option<String>,
}
#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum RunOutcome {
    Completed,
    Yielded,
    Failed,
    Canceled,
}
#[derive(Clone, Serialize, Deserialize, ToSchema)]
pub(super) struct SessionContinuityResponse {
    pub(super) state: SessionContinuityState,
    pub(super) generation: Option<u64>,
    pub(super) reason_code: Option<String>,
}
#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum RuntimeRunState {
    Queued,
    Starting,
    Running,
    Finalizing,
    Stopping,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct RuntimeDiagnosticsResponse {
    pub(super) local_run_id: Option<Uuid>,
    pub(super) local_run_state: Option<RuntimeRunState>,
    pub(super) queued_runs: u32,
    pub(super) active_runs: u32,
    pub(super) pending_commands: u32,
    pub(super) pending_result_events: u32,
    pub(super) warm_sessions: u32,
    pub(super) cold_sessions: u32,
    pub(super) reset_required_sessions: u32,
    pub(super) observed_at: String,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct TaskResponse {
    pub(super) id: Uuid,
    pub(super) seq: u64,
    pub(super) space_id: Uuid,
    pub(super) title: String,
    pub(super) status: TaskStatus,
    pub(super) creator_member_id: Uuid,
    pub(super) creator_name: String,
    pub(super) assignee_agent_member_id: Option<Uuid>,
    pub(super) assignee_name: Option<String>,
    pub(super) source_thread: ThreadReferenceResponse,
    pub(super) related_threads: Vec<ThreadReferenceResponse>,
    /// The Linked Thread a Browser Mark done publishes the Result to; absent when the viewer can read
    /// none of them.
    pub(super) result_thread: Option<ThreadReferenceResponse>,
    pub(super) result_message: Option<MessageResponse>,
    pub(super) close_reason_code: Option<CloseReason>,
    pub(super) close_reason_note: Option<String>,
    pub(super) current_run: Option<RunResponse>,
    pub(super) recent_runs: Vec<RunResponse>,
    pub(super) session_continuity: SessionContinuityResponse,
    pub(super) runtime_issue_code: Option<String>,
    pub(super) created_at: String,
    pub(super) updated_at: String,
    pub(super) finished_at: Option<String>,
}
#[derive(Clone, Copy, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub(super) enum CloseReason {
    Invalid,
    Duplicate,
    NotNeeded,
    Obsolete,
    Other,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct AgentRuntimeResponse {
    pub(super) current_run: Option<RunResponse>,
    pub(super) current_task: Option<TaskResponse>,
    pub(super) focus: Option<ThreadReferenceResponse>,
    pub(super) another_item_waiting: bool,
    pub(super) session_continuity: SessionContinuityResponse,
    pub(super) diagnostics: Option<RuntimeDiagnosticsResponse>,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct AgentGraphResponse {
    pub(super) nodes: Vec<AgentGraphNodeResponse>,
    pub(super) edges: Vec<AgentGraphEdgeResponse>,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct AgentGraphNodeResponse {
    pub(super) member_id: Uuid,
    pub(super) display_name: String,
    pub(super) role_text: String,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct AgentGraphEdgeResponse {
    pub(super) member_a_id: Uuid,
    pub(super) member_b_id: Uuid,
    pub(super) dm_message_count: i64,
    pub(super) mention_a_to_b: i64,
    pub(super) mention_b_to_a: i64,
    pub(super) reply_a_to_b: i64,
    pub(super) reply_b_to_a: i64,
    pub(super) total_interactions: i64,
    pub(super) last_message_at: Option<String>,
    pub(super) recent_messages: Vec<AgentGraphMessageResponse>,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct AgentGraphMessageResponse {
    pub(super) id: Uuid,
    pub(super) channel_id: Uuid,
    pub(super) kind: String,
    pub(super) author_member_id: Uuid,
    pub(super) target_member_id: Uuid,
    pub(super) created_at: String,
    pub(super) body_markdown: String,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct LlmUsageResponse {
    pub(super) requests: u64,
    pub(super) input_tokens: u64,
    pub(super) output_tokens: u64,
    pub(super) cached_input_tokens: u64,
    pub(super) cache_write_tokens: u64,
    pub(super) cache_hit_rate: f64,
    pub(super) first_at: Option<String>,
    pub(super) last_at: Option<String>,
    pub(super) series: Vec<LlmUsageBucketResponse>,
    pub(super) by_model: Vec<LlmUsageBreakdownResponse>,
    pub(super) by_agent: Vec<LlmUsageBreakdownResponse>,
    pub(super) by_agent_series: Vec<LlmUsageAgentSeriesResponse>,
    pub(super) by_agent_model: Vec<LlmUsageAgentModelResponse>,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct LlmUsageBucketResponse {
    pub(super) bucket: String,
    pub(super) requests: u64,
    pub(super) input_tokens: u64,
    pub(super) output_tokens: u64,
    pub(super) cached_input_tokens: u64,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct LlmUsageBreakdownResponse {
    pub(super) key: String,
    pub(super) requests: u64,
    pub(super) input_tokens: u64,
    pub(super) output_tokens: u64,
    pub(super) cached_input_tokens: u64,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct LlmUsageAgentSeriesResponse {
    pub(super) agent_id: Uuid,
    pub(super) requests: u64,
    pub(super) input_tokens: u64,
    pub(super) output_tokens: u64,
    pub(super) cached_input_tokens: u64,
    pub(super) series: Vec<LlmUsageBucketResponse>,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct LlmUsageAgentModelResponse {
    pub(super) agent_id: Uuid,
    pub(super) model: String,
    pub(super) requests: u64,
    pub(super) input_tokens: u64,
    pub(super) output_tokens: u64,
    pub(super) cached_input_tokens: u64,
}

#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct ThreadReadResponse {
    pub(super) thread_id: Uuid,
    pub(super) channel_id: Uuid,
    pub(super) snapshot_channel_seq: u64,
    pub(super) root: MessageResponse,
    pub(super) replies: Vec<MessageResponse>,
    pub(super) is_following: bool,
    pub(super) task: Option<MessageTaskSummary>,
    pub(super) task_relation: Option<ThreadRelation>,
}
#[derive(Serialize, Deserialize, ToSchema)]
pub(super) struct ThreadSubscriptionResponse {
    pub(super) thread_id: Uuid,
    pub(super) channel_id: Uuid,
    pub(super) is_following: bool,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn browser_schema_uses_current_task_run_and_message_models() {
        let document = serde_json::to_value(BrowserApi::openapi()).expect("OpenAPI document");
        assert_eq!(
            document["components"]["schemas"]["TaskStatus"]["enum"],
            serde_json::json!(["todo", "in_progress", "in_review", "done", "closed"])
        );
        assert_eq!(
            document["components"]["schemas"]["SessionContinuityState"]["enum"],
            serde_json::json!(["warm", "cold", "reset_required", "unavailable"])
        );
        let message = &document["components"]["schemas"]["MessageContentResponse"];
        assert!(
            message["oneOf"].is_array(),
            "Message content must be tagged"
        );
        assert!(
            document["components"]["schemas"]["ThreadReadResponse"]["properties"]["task_relation"]
                .is_object(),
            "Thread projection must distinguish Source and Related"
        );
        let serialized = serde_json::to_string(&document).expect("schema JSON");
        for forbidden in [
            "provider_session_locator",
            "provider_transcript",
            "hidden_reasoning",
            "assigned_agent_member_id",
        ] {
            assert!(!serialized.contains(forbidden), "schema leaks {forbidden}");
        }
    }
}
