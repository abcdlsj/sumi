use serde_json::{Value, json};
use uuid::Uuid;

use crate::{
    ids::{ChannelId, ComputerId, InboxItemId, MemberId, SpaceId, TaskId, ThreadId},
    protocol::capability,
    server::application::task::TaskPostTarget,
};

use super::{http::RuntimeState, postgres::PostgresQueries};

/// Activity arguments are labels (a slug, a title, a target), bounded so none can carry a body.
const AGENT_ACTIVITY_ARGUMENT_CHARS: usize = 280;

pub(in crate::server::adapters) fn agent_activity_details(
    mut payload: Value,
    arguments: Vec<(&str, String)>,
) -> Value {
    let Some(object) = payload.as_object_mut() else {
        return payload;
    };
    object.retain(|_, value| !value.is_null());
    object.insert(
        "arguments".to_owned(),
        Value::Array(
            arguments
                .into_iter()
                .map(|(name, value)| {
                    let (value, _) = bounded_activity_text(&value);
                    json!({"name": name, "value": value})
                })
                .collect(),
        ),
    );
    payload
}

pub(in crate::server::adapters) fn bounded_activity_text(value: &str) -> (String, bool) {
    let mut characters = value.chars();
    let mut preview: String = characters
        .by_ref()
        .take(AGENT_ACTIVITY_ARGUMENT_CHARS)
        .collect();
    if characters.next().is_none() {
        return (preview, false);
    }
    preview.pop();
    preview.push('…');
    (preview, true)
}

pub(in crate::server::adapters) struct ActivityThreadReference {
    pub(in crate::server::adapters) thread_id: ThreadId,
    pub(in crate::server::adapters) channel_id: Uuid,
    pub(in crate::server::adapters) label: String,
}

pub(in crate::server::adapters) async fn activity_thread_reference(
    queries: &PostgresQueries,
    thread_id: ThreadId,
) -> Option<ActivityThreadReference> {
    let row = queries
        .activity_thread(thread_id.into_uuid())
        .await
        .ok()??;
    Some(ActivityThreadReference {
        thread_id,
        channel_id: row.channel_id,
        label: activity_thread_label(row.slug.as_deref(), row.channel_seq),
    })
}

pub(in crate::server::adapters) fn activity_thread_label(
    slug: Option<&str>,
    sequence: i64,
) -> String {
    slug.map_or_else(
        || format!("DM · message {sequence}"),
        |slug| format!("#{slug}:{sequence}"),
    )
}

pub(in crate::server::adapters) async fn activity_channel_address(
    queries: &PostgresQueries,
    channel_id: ChannelId,
) -> Option<String> {
    let slug = queries
        .channel_slug(channel_id.into_uuid())
        .await
        .ok()
        .flatten()?;
    Some(format!("#{slug}"))
}

pub(in crate::server::adapters) async fn activity_member_label(
    queries: &PostgresQueries,
    member_id: MemberId,
) -> Option<String> {
    queries
        .member_name(member_id.into_uuid())
        .await
        .ok()
        .flatten()
}

pub(in crate::server::adapters) async fn activity_computer_label(
    queries: &PostgresQueries,
    computer_id: ComputerId,
) -> Option<String> {
    queries
        .computer_name(computer_id.into_uuid())
        .await
        .ok()
        .flatten()
}

pub(in crate::server::adapters) async fn activity_inbox_thread_reference(
    queries: &PostgresQueries,
    item_id: InboxItemId,
) -> Option<ActivityThreadReference> {
    let thread_id = queries.inbox_thread(item_id.into_uuid()).await.ok()??;
    activity_thread_reference(queries, ThreadId::from_uuid(thread_id)).await
}

pub(in crate::server::adapters) async fn activity_task_source_thread(
    queries: &PostgresQueries,
    task_id: TaskId,
) -> Option<ThreadId> {
    queries
        .task_source_thread(task_id.into_uuid())
        .await
        .ok()
        .flatten()
        .map(ThreadId::from_uuid)
}

pub(in crate::server::adapters) fn activity_task_post_target(
    target: TaskPostTarget,
) -> &'static str {
    match target {
        TaskPostTarget::Focus => "focus",
        TaskPostTarget::Source => "source",
        TaskPostTarget::Thread(_) => "thread",
    }
}

pub(in crate::server::adapters) async fn activity_message_target(
    state: &RuntimeState,
    context: &capability::RunContext,
    target: &capability::MessageTarget,
) -> String {
    match target {
        capability::MessageTarget::Focus => {
            activity_thread_reference(&state.read, context.focus_thread_id)
                .await
                .map(|reference| reference.label)
                .unwrap_or_else(|| "Current Focus".to_owned())
        }
        capability::MessageTarget::Thread(thread_id) => {
            activity_thread_reference(&state.read, *thread_id)
                .await
                .map(|reference| reference.label)
                .unwrap_or_else(|| "Thread".to_owned())
        }
        capability::MessageTarget::Channel(channel_id) => {
            activity_channel_address(&state.read, *channel_id)
                .await
                .unwrap_or_else(|| "Channel".to_owned())
        }
        capability::MessageTarget::Member(member_id) => {
            activity_member_label(&state.read, *member_id)
                .await
                .map(|name| format!("DM with {name}"))
                .unwrap_or_else(|| "Direct message".to_owned())
        }
    }
}

/// Records an ephemeral `agent.activity` event for the Browser feed. The feed is best-effort, so a
/// failed insert never fails the Agent action that produced the activity.
///
/// `origin` names the request that produced the activity (its idempotency key or result event
/// ID). A resent request derives the same event ID, so the retry adds no second row.
pub(super) async fn record_agent_activity(
    state: &RuntimeState,
    origin: Option<Uuid>,
    space_id: Uuid,
    agent_member_id: Uuid,
    kind: &str,
    payload: serde_json::Value,
) {
    state
        .storage
        .record_agent_activity(
            origin,
            SpaceId::from_uuid(space_id),
            MemberId::from_uuid(agent_member_id),
            kind,
            payload,
        )
        .await;
}
