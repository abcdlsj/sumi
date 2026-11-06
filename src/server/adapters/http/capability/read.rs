use super::*;

pub(in crate::server::adapters::http) async fn discover_operation(
    state: &RuntimeState,
    context: &wire_capability::RunContext,
    operation: &str,
) -> Result<Value, wire_capability::Error> {
    if operation != "agent.create" {
        return Err(capability_error(
            wire_capability::ErrorCode::NotFound,
            "Discovery operation is not available",
            false,
        ));
    }

    let computers = state
        .read
        .computer_options(context.space_id.into_uuid())
        .await
        .map_err(|_| {
            capability_error(
                wire_capability::ErrorCode::Internal,
                "Discovery options could not be read",
                false,
            )
        })?;
    let permission_granted = state
        .read
        .permission_granted(
            context.agent_id.into_uuid(),
            context.space_id.into_uuid(),
            "agent.create",
        )
        .await
        .map_err(|_| {
            capability_error(
                wire_capability::ErrorCode::Internal,
                "Discovery permission state could not be read",
                false,
            )
        })?;

    Ok(json!({
        "operation": "agent.create",
        "description": "Create an Agent in the current Space",
        "input": {
            "fields": [
                {
                    "name": "name",
                    "value_type": "display_name",
                    "required": true
                },
                {
                    "name": "role_file",
                    "value_type": "agent_role_file",
                    "required": true
                },
                {
                    "name": "computer_id",
                    "value_type": "computer_id",
                    "required": true,
                    "available": computers.iter().map(|row| json!({
                        "value": row.id,
                        "label": row.name,
                        "hostname": row.hostname,
                        "os": row.os,
                        "status": row.connection_status,
                        "available": row.connection_status == "online"
                    })).collect::<Vec<_>>()
                },
                {
                    "name": "driver",
                    "value_type": "driver_kind",
                    "required": true,
                    "available": [
                        { "value": "codex", "label": "Codex (ACP)" },
                        { "value": "claude", "label": "Claude (ACP)" },
                        { "value": "pi", "label": "Pi (ACP)" },
                        { "value": "builtin", "label": "Builtin" }
                    ]
                }
            ]
        },
        "permission": {
            "action": "agent.create",
            "granted": permission_granted
        }
    }))
}

/// Returns the page and the sequences it contains, so the caller can record what the Run observed.
pub(in crate::server::adapters::http) async fn agent_read_thread(
    state: &RuntimeState,
    thread_id: Uuid,
    page: wire_capability::Page,
) -> Result<(Value, Vec<u64>), wire_capability::Error> {
    let limit = i64::from(page.limit);
    let rows = state
        .read
        .agent_thread_messages(
            thread_id,
            page.before.map(|value| value as i64),
            page.after.map(|value| value as i64),
            limit,
        )
        .await
        .map_err(|_| {
            capability_error(
                wire_capability::ErrorCode::Internal,
                "Messages could not be read",
                false,
            )
        })?;
    let sequences = sequences(rows.iter().map(|row| row.channel_seq));
    Ok((
        json!({"thread_id":thread_id,"messages":rows.iter().map(|row|json!({"id":row.id,"seq":row.channel_seq,"author_member_id":row.author_member_id,"content":{"type":"text","body_markdown":row.body_markdown.clone().unwrap_or_default()},"created_at":timestamp(row.created_at)})).collect::<Vec<_>>()}),
        sequences,
    ))
}

pub(in crate::server::adapters::http) fn sequences(values: impl Iterator<Item = i64>) -> Vec<u64> {
    values
        .filter_map(|value| u64::try_from(value).ok())
        .collect()
}

pub(in crate::server::adapters::http) async fn agent_read_space_members(
    state: &RuntimeState,
    space_id: Uuid,
) -> Result<Value, wire_capability::Error> {
    let rows = state.read.members_in_space(space_id).await.map_err(|_| {
        capability_error(
            wire_capability::ErrorCode::Internal,
            "Space Members could not be read",
            false,
        )
    })?;
    Ok(json!({
        "members": rows
            .iter()
            .map(|row| json!({
                "member_id": row.id,
                "display_name": row.display_name,
            }))
            .collect::<Vec<_>>(),
    }))
}

pub(in crate::server::adapters::http) async fn agent_read_channel_members(
    state: &RuntimeState,
    agent_id: Uuid,
    space_id: Uuid,
    channel_id: Uuid,
) -> Result<Value, wire_capability::Error> {
    let member = state
        .read
        .channel_member_visible(channel_id, agent_id)
        .await
        .map_err(app_to_capability)?;
    if !member {
        return Err(capability_error(
            wire_capability::ErrorCode::PermissionDenied,
            "Channel is not visible to the Agent",
            false,
        ));
    }
    let channel = state
        .read
        .channel(channel_id, agent_id)
        .await
        .map_err(|_| {
            capability_error(
                wire_capability::ErrorCode::Internal,
                "Channel could not be read",
                false,
            )
        })?
        .ok_or_else(|| {
            capability_error(
                wire_capability::ErrorCode::NotFound,
                "Channel was not found",
                false,
            )
        })?;
    if channel.space_id != space_id {
        return Err(capability_error(
            wire_capability::ErrorCode::PermissionDenied,
            "Channel is not visible to the Agent",
            false,
        ));
    }
    let rows = state.read.channel_members(channel_id).await.map_err(|_| {
        capability_error(
            wire_capability::ErrorCode::Internal,
            "Channel Members could not be read",
            false,
        )
    })?;
    Ok(json!({
        "members": rows
            .iter()
            .map(|row| json!({
                "member_id": row.id,
                "display_name": row.display_name,
            }))
            .collect::<Vec<_>>(),
    }))
}

pub(in crate::server::adapters::http) async fn agent_read_channel(
    state: &RuntimeState,
    agent_id: Uuid,
    channel_id: Uuid,
    around_message_id: Option<Uuid>,
    limit: u16,
) -> Result<(Value, Vec<u64>), wire_capability::Error> {
    let member = state
        .read
        .channel_member_visible(channel_id, agent_id)
        .await
        .map_err(app_to_capability)?;
    if !member {
        return Err(capability_error(
            wire_capability::ErrorCode::PermissionDenied,
            "Channel is not visible to the Agent",
            false,
        ));
    }
    let around_sequence = match around_message_id {
        Some(message_id) => Some(
            state
                .read
                .message_sequence_in_channel(message_id, channel_id)
                .await
                .map_err(app_to_capability)?
                .ok_or_else(|| {
                    capability_error(
                        wire_capability::ErrorCode::NotFound,
                        "Around Message was not found in the Channel",
                        false,
                    )
                })?,
        ),
        None => None,
    };
    let rows = state
        .read
        .channel_messages(channel_id, around_sequence, i64::from(limit))
        .await
        .map_err(|_| {
            capability_error(
                wire_capability::ErrorCode::Internal,
                "Channel Messages could not be read",
                false,
            )
        })?;
    let mut rows = rows;
    rows.sort_by_key(|row| row.channel_seq);
    let mut messages = Vec::with_capacity(rows.len());
    for row in &rows {
        messages.push(
            message_row(&state.read, row)
                .await
                .map_err(api_to_capability)?,
        );
    }
    let snapshot = state
        .read
        .channel_snapshot(channel_id)
        .await
        .map_err(app_to_capability)?;
    Ok((
        json!({
            "channel_id": channel_id,
            "messages": messages,
            "snapshot_channel_seq": snapshot
        }),
        sequences(rows.iter().map(|row| row.channel_seq)),
    ))
}

/// Records what a read returned. A failure only leaves the position behind, which can hold a later
/// write; it never lets a write skip unobserved Messages.
pub(in crate::server::adapters::http) async fn record_read(
    state: &RuntimeState,
    context: &wire_capability::RunContext,
    scope: ObservationScope,
    sequences: &[u64],
) {
    let mut storage = state.storage.clone();
    if let Err(error) = RecordObservedRead::execute(
        &mut storage,
        RecordObservedReadInput {
            run_id: context.run_id,
            observer: MemberId::from_uuid(context.agent_id.into_uuid()),
            scope,
            sequences: sequences.to_vec(),
        },
    )
    .await
    {
        tracing::warn!(%context.run_id, %error, "observed read position record failed");
    }
}

/// The send did not commit. The response carries the unobserved Messages so the Agent can revise
/// and resend without another read.
pub(in crate::server::adapters::http) async fn held_message_send(
    state: &RuntimeState,
    channel_id: Uuid,
    thread_id: Option<Uuid>,
    held: &[ObservedMessage],
) -> Result<Value, wire_capability::Error> {
    let mut messages = Vec::with_capacity(held.len());
    for message in held {
        let row = state
            .read
            .message(message.id.into_uuid())
            .await
            .map_err(|_| {
                capability_error(
                    wire_capability::ErrorCode::Internal,
                    "Unobserved Messages could not be read",
                    false,
                )
            })?
            .ok_or_else(|| {
                capability_error(
                    wire_capability::ErrorCode::Internal,
                    "Unobserved Messages could not be read",
                    false,
                )
            })?;
        messages.push(
            message_row(&state.read, &row)
                .await
                .map_err(api_to_capability)?,
        );
    }
    Ok(json!({
        "status": "held",
        "reason": "Not posted: the target has Messages you had not seen. Read them, then send again only if your Message is still needed; revise it to fit them.",
        "channel_id": channel_id,
        "thread_id": thread_id,
        "unseen_messages": messages,
    }))
}

pub(in crate::server::adapters::http) fn capability_error(
    code: wire_capability::ErrorCode,
    message: &str,
    retryable: bool,
) -> wire_capability::Error {
    wire_capability::Error {
        code,
        message: message.into(),
        retryable,
        details: Default::default(),
    }
}
