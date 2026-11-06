use super::*;
use crate::server::application::ports::CollaborationTransaction;

/// Resolve `@display_name` tokens in an Agent message body against the target
/// Channel Members. The Server never parses message bodies for consumers, but
/// the Agent CLI sends plain Markdown, so the Server resolves mentions from the
/// body at the single write entry point. A token starts at the beginning of the
/// body or after any character that cannot be part of a display name (letter,
/// digit, underscore, or another `@`).
pub(in crate::server::adapters::http) async fn agent_mention_ids(
    queries: &PostgresQueries,
    channel_id: Uuid,
    body: &str,
) -> Result<Vec<Uuid>, ApiError> {
    let members = queries
        .agent_mention_members(channel_id)
        .await
        .map_err(application_error)?;
    let names: Vec<(String, Uuid)> = members
        .iter()
        .map(|(id, name)| (name.to_lowercase(), *id))
        .collect();
    let chars: Vec<(usize, char)> = body.char_indices().collect();
    let mut ids: Vec<Uuid> = Vec::new();
    let mut index = 0;
    while index < chars.len() {
        let (offset, character) = chars[index];
        if character == '@'
            && (index == 0
                || !(chars[index - 1].1.is_alphanumeric()
                    || chars[index - 1].1 == '_'
                    || chars[index - 1].1 == '@'))
        {
            let mut end = index + 1;
            while end < chars.len() && (chars[end].1.is_alphabetic() || chars[end].1 == '_') {
                end += 1;
            }
            let name_end = if end < chars.len() {
                chars[end].0
            } else {
                body.len()
            };
            let candidate = &body[offset + 1..name_end].to_lowercase();
            if let Some((_, id)) = names.iter().find(|(name, _)| name == candidate)
                && !ids.contains(id)
            {
                ids.push(*id);
            }
            index = end;
        } else {
            index += 1;
        }
    }
    Ok(ids)
}

pub(in crate::server::adapters::http) async fn agent_action(
    State(state): State<RuntimeState>,
    headers: HeaderMap,
    Path(computer_id): Path<Uuid>,
    Json(request): Json<AgentActionRequest>,
) -> Json<wire_capability::Response<Value>> {
    match execute_agent_action(&state, &headers, computer_id, request).await {
        Ok(value) => Json(wire_capability::Response::success(value)),
        Err(error) => Json(wire_capability::Response::failure(error)),
    }
}

pub(in crate::server::adapters::http) async fn execute_agent_action(
    state: &RuntimeState,
    headers: &HeaderMap,
    computer_id: Uuid,
    request: AgentActionRequest,
) -> Result<Value, wire_capability::Error> {
    let raw = bearer_token(headers).map_err(|_| {
        capability_error(
            wire_capability::ErrorCode::Unauthenticated,
            "Computer authentication failed",
            false,
        )
    })?;
    let authenticated = state
        .read
        .computer_authenticated(computer_id, &token_hash(raw))
        .await
        .map_err(|_| {
            capability_error(
                wire_capability::ErrorCode::Internal,
                "Computer authentication failed",
                false,
            )
        })?;
    if !authenticated {
        return Err(capability_error(
            wire_capability::ErrorCode::Unauthenticated,
            "Computer authentication failed",
            false,
        ));
    }
    let context = &request.context;
    let request_origin = request.idempotency_key.map(IdempotencyKey::into_uuid);
    if let Some(key) = request.idempotency_key
        && matches!(
            &request.action,
            wire_capability::Action::TaskSubmitReview { .. }
                | wire_capability::Action::TaskDone { .. }
                | wire_capability::Action::TaskClose { .. }
        )
    {
        let replayed = state
            .read
            .task_action_replay(
                context.agent_id.into_uuid(),
                request.action.name(),
                key.into_uuid(),
                context.task_id.map(TaskId::into_uuid),
                context.space_id.into_uuid(),
                computer_id,
            )
            .await
            .map_err(|_| {
                capability_error(
                    wire_capability::ErrorCode::Internal,
                    "Task action replay could not be checked",
                    false,
                )
            })?;
        if let Some(task_id) = replayed {
            return capability_value(
                &task_projection(&state.read, task_id)
                    .await
                    .map_err(api_to_capability)?,
            );
        }
    }
    if let (Some(key), wire_capability::Action::ChannelLeave { channel_id }) =
        (request.idempotency_key, &request.action)
    {
        let replayed = state
            .read
            .channel_leave_replay(ChannelLeaveReplayQuery {
                agent_id: context.agent_id.into_uuid(),
                key: key.into_uuid(),
                space_id: context.space_id.into_uuid(),
                computer_id,
                run_id: context.run_id.into_uuid(),
                task_id: context.task_id.map(TaskId::into_uuid),
                focus_thread_id: context.focus_thread_id.into_uuid(),
            })
            .await
            .map_err(|_| {
                capability_error(
                    wire_capability::ErrorCode::Internal,
                    "Channel leave replay could not be checked",
                    false,
                )
            })?;
        if let Some(replayed_channel_id) = replayed {
            if replayed_channel_id != channel_id.into_uuid() {
                return Err(capability_error(
                    wire_capability::ErrorCode::Conflict,
                    "Idempotency key belongs to another Channel",
                    false,
                ));
            }
            return Ok(json!({
                "channel_id": replayed_channel_id,
                "member_id": context.agent_id,
            }));
        }
    }
    let mut storage = state.storage.clone();
    let valid = AuthorizeRunCapability::execute(
        &mut storage,
        RunCapabilityProof {
            computer_id: ComputerId::from_uuid(computer_id),
            run_id: context.run_id,
            agent_id: MemberId::from_uuid(context.agent_id.into_uuid()),
            space_id: context.space_id,
            task_id: context.task_id,
            focus_thread_id: context.focus_thread_id,
        },
    )
    .await
    .map_err(app_to_capability)?;
    if !valid {
        return Err(capability_error(
            wire_capability::ErrorCode::ContextChanged,
            "Run context is no longer active",
            false,
        ));
    }
    match request.action {
        wire_capability::Action::Discover { operation } => {
            discover_operation(state, context, &operation).await
        }
        wire_capability::Action::ContextCurrent => {
            let task = match context.task_id {
                Some(id) => Some(
                    task_projection(&state.read, id.into_uuid())
                        .await
                        .map_err(api_to_capability)?,
                ),
                None => None,
            };
            let items = state
                .read
                .run_items(context.run_id.into_uuid())
                .await
                .map_err(|_| {
                    capability_error(
                        wire_capability::ErrorCode::Internal,
                        "Run Items could not be read",
                        false,
                    )
                })?;
            let scope = match context.task_id {
                Some(task_id) => SessionScope::Task(task_id),
                None => SessionScope::Thread(context.focus_thread_id),
            };
            let continuity = agent_continuity(state, context.agent_id.into_uuid(), scope).await;
            Ok(
                json!({"agent":{"id":context.agent_id,"space_id":context.space_id},"task":task,"focus_thread_id":context.focus_thread_id,"run":{"id":context.run_id},"dispatched_items":items.iter().map(|row|json!({"id":row.id,"kind":row.kind,"strength":row.strength,"status":row.status,"available_at":timestamp(row.available_at),"delivery_outcome":row.delivery_outcome,"delivery_event_id":row.delivery_event_id,"delivery_receipt_at":row.delivery_receipt_at.map(timestamp),"disposition":row.disposition,"disposition_at":row.disposition_at.map(timestamp)})).collect::<Vec<_>>(),"session_continuity":continuity}),
            )
        }
        wire_capability::Action::SpaceMembers => {
            agent_read_space_members(state, context.space_id.into_uuid()).await
        }
        wire_capability::Action::MessageRead(page) => {
            let (value, sequences) =
                agent_read_thread(state, context.focus_thread_id.into_uuid(), page).await?;
            record_read(
                state,
                context,
                ObservationScope::Thread(context.focus_thread_id),
                &sequences,
            )
            .await;
            Ok(value)
        }
        wire_capability::Action::ThreadRead { thread_id, page } => {
            let (value, sequences) = agent_read_thread(state, thread_id.into_uuid(), page).await?;
            record_read(
                state,
                context,
                ObservationScope::Thread(thread_id),
                &sequences,
            )
            .await;
            Ok(value)
        }
        wire_capability::Action::ChannelMembers { channel_id } => {
            agent_read_channel_members(
                state,
                context.agent_id.into_uuid(),
                context.space_id.into_uuid(),
                channel_id.into_uuid(),
            )
            .await
        }
        wire_capability::Action::ChannelRead {
            channel_id,
            around_message_id,
            limit,
        } => {
            if limit == 0 || limit > 100 {
                return Err(capability_error(
                    wire_capability::ErrorCode::InvalidArgument,
                    "limit must be between 1 and 100",
                    false,
                ));
            }
            let (value, sequences) = agent_read_channel(
                state,
                context.agent_id.into_uuid(),
                channel_id.into_uuid(),
                around_message_id.map(MessageId::into_uuid),
                limit,
            )
            .await?;
            record_read(
                state,
                context,
                ObservationScope::Channel(channel_id),
                &sequences,
            )
            .await;
            Ok(value)
        }
        wire_capability::Action::MessageSend(send) => {
            let target = activity_message_target(state, context, &send.target).await;
            let attachment_count = send.attachment_ids.len();
            let handles_item = send.handle_item_id.is_some();
            let (channel_id, thread_id) = match send.target {
                wire_capability::MessageTarget::Focus => {
                    let mut storage = state.storage.clone();
                    storage
                        .transact(async |transaction| {
                            transaction
                                .channel_for_thread(context.focus_thread_id)
                                .await
                        })
                        .await
                        .map_err(app_to_capability)?
                        .map(|channel| {
                            (
                                channel.into_uuid(),
                                Some(context.focus_thread_id.into_uuid()),
                            )
                        })
                        .ok_or_else(|| {
                            capability_error(
                                wire_capability::ErrorCode::NotFound,
                                "Focus Thread was not found",
                                false,
                            )
                        })?
                }
                wire_capability::MessageTarget::Thread(thread_id) => {
                    let mut storage = state.storage.clone();
                    storage
                        .transact(async |transaction| {
                            transaction.channel_for_thread(thread_id).await
                        })
                        .await
                        .map_err(app_to_capability)?
                        .map(|channel| (channel.into_uuid(), Some(thread_id.into_uuid())))
                        .ok_or_else(|| {
                            capability_error(
                                wire_capability::ErrorCode::NotFound,
                                "Thread was not found",
                                false,
                            )
                        })?
                }
                wire_capability::MessageTarget::Channel(channel_id) => {
                    (channel_id.into_uuid(), None)
                }
                wire_capability::MessageTarget::Member(member_id) => {
                    let opened = OpenDirectMessage::execute(
                        &mut storage,
                        OpenDirectMessageInput {
                            channel_id: ChannelId::from_uuid(Uuid::now_v7()),
                            space_id: context.space_id,
                            actor_member_id: MemberId::from_uuid(context.agent_id.into_uuid()),
                            other_member_id: member_id,
                            idempotency_key: request.idempotency_key.ok_or_else(|| {
                                capability_error(
                                    wire_capability::ErrorCode::InvalidArgument,
                                    "Idempotency key is required",
                                    false,
                                )
                            })?,
                            now: time::OffsetDateTime::now_utc(),
                        },
                    )
                    .await
                    .map_err(app_to_capability)?;
                    (opened.view.channel_id.into_uuid(), None)
                }
            };
            let mentions = agent_mention_ids(&state.read, channel_id, &send.body)
                .await
                .map_err(api_to_capability)?;
            let outcome = publish_message(
                state,
                channel_id,
                context.agent_id.into_uuid(),
                MessageWriteContext {
                    idempotency_key: request.idempotency_key.ok_or_else(|| {
                        capability_error(
                            wire_capability::ErrorCode::InvalidArgument,
                            "Idempotency key is required",
                            false,
                        )
                    })?,
                    thread_id,
                    handled_item: send
                        .handle_item_id
                        .map(|item_id| (context.run_id.into_uuid(), item_id.into_uuid())),
                },
                CreateMessageBody {
                    body_markdown: send.body,
                    mentions,
                    mention_all: false,
                    attachment_ids: send
                        .attachment_ids
                        .into_iter()
                        .map(AttachmentId::into_uuid)
                        .collect(),
                    reply_to_message_id: None,
                },
                Some(context.run_id),
            )
            .await
            .map_err(app_to_capability)?;
            let message_id = match outcome {
                PublishOutcome::Published(published) => published.message_id.into_uuid(),
                PublishOutcome::Held(messages) => {
                    return held_message_send(state, channel_id, thread_id, &messages).await;
                }
            };
            record_agent_activity(
                state,
                request_origin,
                context.space_id.into_uuid(),
                context.agent_id.into_uuid(),
                "message.send",
                agent_activity_details(
                    json!({
                        "run_id": context.run_id,
                        "channel_id": channel_id,
                        "thread_id": thread_id.unwrap_or(message_id),
                        "message_id": message_id,
                        "scope_channel_id": channel_id,
                    }),
                    vec![
                        ("target", target),
                        ("attachment_count", attachment_count.to_string()),
                        ("handle_item", handles_item.to_string()),
                    ],
                ),
            )
            .await;
            Ok(
                json!({"status":"posted","message_id":message_id,"thread_id":thread_id.unwrap_or(message_id),"channel_id":channel_id}),
            )
        }
        wire_capability::Action::TaskCreate {
            title,
            assignee,
            source_thread,
            link_threads,
        } => {
            let key = request.idempotency_key.ok_or_else(|| {
                capability_error(
                    wire_capability::ErrorCode::InvalidArgument,
                    "idempotency key is required",
                    false,
                )
            })?;
            let source_thread_id = source_thread.unwrap_or(context.focus_thread_id);
            let default_title = state
                .read
                .focus_body(source_thread_id.into_uuid())
                .await
                .map_err(|_| {
                    capability_error(
                        wire_capability::ErrorCode::NotFound,
                        "Focus Root Message was not found",
                        false,
                    )
                })?;
            let title = title.unwrap_or_else(|| default_title.chars().take(120).collect());
            let assignee_label = match assignee.as_ref() {
                Some(member_id) => activity_member_label(&state.read, *member_id)
                    .await
                    .unwrap_or_else(|| "Assigned Agent".to_owned()),
                None => "Unassigned".to_owned(),
            };
            let focus = activity_thread_reference(&state.read, source_thread_id).await;
            let mut storage = state.storage.clone();
            let task = CreateTaskFromRootMessage::execute(
                &mut storage,
                CreateTaskInput {
                    task_id: TaskId::from_uuid(Uuid::now_v7()),
                    actor_member_id: MemberId::from_uuid(context.agent_id.into_uuid()),
                    source: TaskSource::AgentRun(context.run_id),
                    title: title.clone(),
                    assignee_agent_member_id: assignee,
                    source_thread_id: source_thread,
                    link_thread_ids: link_threads.clone(),
                    idempotency_key: key,
                    now: OffsetDateTime::now_utc(),
                },
            )
            .await
            .map_err(app_to_capability)?;
            let task_id = task.view().id;
            record_agent_activity(
                state,
                request_origin,
                context.space_id.into_uuid(),
                context.agent_id.into_uuid(),
                "task.create",
                agent_activity_details(
                    json!({
                        "run_id": context.run_id,
                        "task_id": task_id,
                        "thread_id": source_thread_id,
                        "scope_channel_id": focus.as_ref().map(|reference| reference.channel_id),
                    }),
                    vec![("title", title), ("assignee", assignee_label)],
                ),
            )
            .await;
            for thread_id in link_threads {
                let target = activity_thread_reference(&state.read, thread_id).await;
                record_agent_activity(
                    state,
                    request_origin,
                    context.space_id.into_uuid(),
                    context.agent_id.into_uuid(),
                    "task.link_thread",
                    agent_activity_details(
                        json!({
                            "run_id": context.run_id,
                            "task_id": task_id,
                            "thread_id": thread_id,
                            "scope_channel_id": target.as_ref().map(|reference| reference.channel_id),
                        }),
                        vec![(
                            "thread",
                            target
                                .as_ref()
                                .map(|reference| reference.label.clone())
                                .unwrap_or_else(|| "Thread".to_owned()),
                        )],
                    ),
                )
                .await;
            }
            capability_value(
                &task_projection(&state.read, task_id.into_uuid())
                    .await
                    .map_err(api_to_capability)?,
            )
        }
        wire_capability::Action::TaskLinkThread { thread_id } => {
            let target = activity_thread_reference(&state.read, thread_id).await;
            let key = request.idempotency_key.ok_or_else(|| {
                capability_error(
                    wire_capability::ErrorCode::InvalidArgument,
                    "Idempotency key is required",
                    false,
                )
            })?;
            let task_id = context.task_id.ok_or_else(|| {
                capability_error(
                    wire_capability::ErrorCode::Conflict,
                    "Run is not bound to a Task",
                    false,
                )
            })?;
            let mut storage = state.storage.clone();
            LinkThreadToTask::execute(
                &mut storage,
                LinkThreadInput {
                    task_id,
                    target_thread_id: thread_id,
                    actor_member_id: MemberId::from_uuid(context.agent_id.into_uuid()),
                    idempotency_key: key,
                    now: OffsetDateTime::now_utc(),
                },
            )
            .await
            .map_err(app_to_capability)?;
            record_agent_activity(
                state,
                request_origin,
                context.space_id.into_uuid(),
                context.agent_id.into_uuid(),
                "task.link_thread",
                agent_activity_details(
                    json!({
                        "run_id": context.run_id,
                        "task_id": task_id,
                        "thread_id": thread_id,
                        "scope_channel_id": target.as_ref().map(|reference| reference.channel_id),
                    }),
                    vec![(
                        "thread",
                        target
                            .as_ref()
                            .map(|reference| reference.label.clone())
                            .unwrap_or_else(|| "Thread".to_owned()),
                    )],
                ),
            )
            .await;
            capability_value(
                &task_projection(&state.read, task_id.into_uuid())
                    .await
                    .map_err(api_to_capability)?,
            )
        }
        wire_capability::Action::TaskUnlinkThread { thread_id } => {
            let target = activity_thread_reference(&state.read, thread_id).await;
            let key = request.idempotency_key.ok_or_else(|| {
                capability_error(
                    wire_capability::ErrorCode::InvalidArgument,
                    "Idempotency key is required",
                    false,
                )
            })?;
            let task_id = context.task_id.ok_or_else(|| {
                capability_error(
                    wire_capability::ErrorCode::Conflict,
                    "Run is not bound to a Task",
                    false,
                )
            })?;
            let mut storage = state.storage.clone();
            UnlinkThreadFromTask::execute(
                &mut storage,
                LinkThreadInput {
                    task_id,
                    target_thread_id: thread_id,
                    actor_member_id: MemberId::from_uuid(context.agent_id.into_uuid()),
                    idempotency_key: key,
                    now: OffsetDateTime::now_utc(),
                },
            )
            .await
            .map_err(app_to_capability)?;
            record_agent_activity(
                state,
                request_origin,
                context.space_id.into_uuid(),
                context.agent_id.into_uuid(),
                "task.unlink_thread",
                agent_activity_details(
                    json!({
                        "run_id": context.run_id,
                        "task_id": task_id,
                        "thread_id": thread_id,
                        "scope_channel_id": target.as_ref().map(|reference| reference.channel_id),
                    }),
                    vec![(
                        "thread",
                        target
                            .as_ref()
                            .map(|reference| reference.label.clone())
                            .unwrap_or_else(|| "Thread".to_owned()),
                    )],
                ),
            )
            .await;
            capability_value(
                &task_projection(&state.read, task_id.into_uuid())
                    .await
                    .map_err(api_to_capability)?,
            )
        }
        wire_capability::Action::TaskSubmitReview { body, post_to } => {
            finish_agent_task(
                state,
                computer_id,
                context,
                request.idempotency_key,
                TaskOutcome::SubmitReview {
                    message: Some(OutcomeMessage {
                        message_id: MessageId::from_uuid(Uuid::now_v7()),
                        body_markdown: body,
                        post_to: task_post_target(post_to),
                    }),
                },
            )
            .await
        }
        wire_capability::Action::TaskDone {
            result,
            post_to,
            result_message,
        } => {
            let result = match (result, result_message) {
                (Some(body), None) => TaskResult::Publish(OutcomeMessage {
                    message_id: MessageId::from_uuid(Uuid::now_v7()),
                    body_markdown: body,
                    post_to: task_post_target(post_to),
                }),
                (None, Some(message_id)) => TaskResult::Existing(message_id),
                _ => {
                    return Err(capability_error(
                        wire_capability::ErrorCode::InvalidArgument,
                        "task done needs exactly one of a result body and a result message",
                        false,
                    ));
                }
            };
            finish_agent_task(
                state,
                computer_id,
                context,
                request.idempotency_key,
                TaskOutcome::Done { result },
            )
            .await
        }
        wire_capability::Action::TaskClose { reason, note } => {
            let reason = match reason {
                wire_capability::CloseReason::Invalid => CloseReason::Invalid,
                wire_capability::CloseReason::Duplicate => CloseReason::Duplicate,
                wire_capability::CloseReason::NotNeeded => CloseReason::NotNeeded,
                wire_capability::CloseReason::Obsolete => CloseReason::Obsolete,
                wire_capability::CloseReason::Other => CloseReason::Other,
            };
            finish_agent_task(
                state,
                computer_id,
                context,
                request.idempotency_key,
                TaskOutcome::Close { reason, note },
            )
            .await
        }
        wire_capability::Action::ChannelCreate {
            slug,
            topic,
            private,
        } => {
            let slug = slug.trim().to_owned();
            let activity_slug = slug.clone();
            let topic = topic
                .map(|value| value.trim().to_owned())
                .filter(|value| !value.is_empty());
            let activity_topic = topic.clone();
            let focus = activity_thread_reference(&state.read, context.focus_thread_id).await;
            let channel_id = ChannelId::from_uuid(Uuid::now_v7());
            let mut storage = state.storage.clone();
            let channel = CreateChannelAction::execute(
                &mut storage,
                CreateChannelActionInput {
                    channel_id,
                    audience: Default::default(),
                    kind: if private {
                        ChannelKind::Private
                    } else {
                        ChannelKind::Public
                    },
                    slug: Some(slug),
                    topic,
                    action_message_id: MessageId::from_uuid(Uuid::now_v7()),
                    actor_member_id: MemberId::from_uuid(context.agent_id.into_uuid()),
                    idempotency_key: request.idempotency_key.ok_or_else(|| {
                        capability_error(
                            wire_capability::ErrorCode::InvalidArgument,
                            "Idempotency key is required",
                            false,
                        )
                    })?,
                    current_run_id: context.run_id,
                    now: OffsetDateTime::now_utc(),
                },
            )
            .await
            .map_err(app_to_capability)?;
            let mut arguments = vec![("slug", activity_slug)];
            if let Some(topic) = activity_topic {
                arguments.push(("topic", topic));
            }
            arguments.push(("private", private.to_string()));
            record_agent_activity(
                state,
                request_origin,
                context.space_id.into_uuid(),
                context.agent_id.into_uuid(),
                "channel.create",
                agent_activity_details(
                    json!({
                        "run_id": context.run_id,
                        "channel_id": channel.id,
                        "thread_id": context.focus_thread_id,
                        "scope_channel_id": focus.as_ref().map(|reference| reference.channel_id),
                    }),
                    arguments,
                ),
            )
            .await;
            Ok(json!({
                "channel_id": channel.id,
                "slug": channel.slug,
                "topic": channel.topic,
                "kind": if private { "private" } else { "public" }
            }))
        }
        wire_capability::Action::ChannelLeave { channel_id } => {
            let channel_label = activity_channel_address(&state.read, channel_id)
                .await
                .unwrap_or_else(|| "Channel".to_owned());
            let key = request.idempotency_key.ok_or_else(|| {
                capability_error(
                    wire_capability::ErrorCode::InvalidArgument,
                    "Idempotency key is required",
                    false,
                )
            })?;
            let mut storage = state.storage.clone();
            LeaveChannel::execute(
                &mut storage,
                MemberId::from_uuid(context.agent_id.into_uuid()),
                channel_id,
                key,
                OffsetDateTime::now_utc(),
            )
            .await
            .map_err(app_to_capability)?;
            record_agent_activity(
                state,
                request_origin,
                context.space_id.into_uuid(),
                context.agent_id.into_uuid(),
                "channel.leave",
                agent_activity_details(
                    json!({
                        "run_id": context.run_id,
                        "channel_id": channel_id,
                        "scope_channel_id": channel_id,
                    }),
                    vec![("channel", channel_label)],
                ),
            )
            .await;
            Ok(json!({"channel_id": channel_id, "member_id": context.agent_id}))
        }
        wire_capability::Action::ChannelInvite {
            channel_id,
            member_id,
        } => {
            let key = request.idempotency_key.ok_or_else(|| {
                capability_error(
                    wire_capability::ErrorCode::InvalidArgument,
                    "Idempotency key is required",
                    false,
                )
            })?;
            let mut storage = state.storage.clone();
            let invited = InviteChannelMember::execute(
                &mut storage,
                MemberId::from_uuid(context.agent_id.into_uuid()),
                channel_id,
                member_id,
                key,
                OffsetDateTime::now_utc(),
            )
            .await
            .map_err(app_to_capability)?;
            let channel_label = activity_channel_address(&state.read, channel_id)
                .await
                .unwrap_or_else(|| "Channel".to_owned());
            let member_label = activity_member_label(&state.read, member_id)
                .await
                .unwrap_or_else(|| "Member".to_owned());
            record_agent_activity(
                state,
                request_origin,
                context.space_id.into_uuid(),
                context.agent_id.into_uuid(),
                "channel.invite",
                agent_activity_details(
                    json!({
                        "run_id": context.run_id,
                        "channel_id": channel_id,
                        "target_member_id": member_id,
                        "scope_channel_id": channel_id,
                    }),
                    vec![("channel", channel_label), ("member", member_label)],
                ),
            )
            .await;
            Ok(json!({
                "channel_id": channel_id,
                "member_id": member_id,
                "invited": invited,
            }))
        }
        wire_capability::Action::ChannelRemove {
            channel_id,
            member_id,
        } => {
            let key = request.idempotency_key.ok_or_else(|| {
                capability_error(
                    wire_capability::ErrorCode::InvalidArgument,
                    "Idempotency key is required",
                    false,
                )
            })?;
            let mut storage = state.storage.clone();
            let removed = RemoveChannelMember::execute(
                &mut storage,
                MemberId::from_uuid(context.agent_id.into_uuid()),
                channel_id,
                member_id,
                key,
                OffsetDateTime::now_utc(),
            )
            .await
            .map_err(app_to_capability)?;
            let channel_label = activity_channel_address(&state.read, channel_id)
                .await
                .unwrap_or_else(|| "Channel".to_owned());
            let member_label = activity_member_label(&state.read, member_id)
                .await
                .unwrap_or_else(|| "Member".to_owned());
            record_agent_activity(
                state,
                request_origin,
                context.space_id.into_uuid(),
                context.agent_id.into_uuid(),
                "channel.remove",
                agent_activity_details(
                    json!({
                        "run_id": context.run_id,
                        "channel_id": channel_id,
                        "target_member_id": member_id,
                        "scope_channel_id": channel_id,
                    }),
                    vec![("channel", channel_label), ("member", member_label)],
                ),
            )
            .await;
            Ok(json!({
                "channel_id": channel_id,
                "member_id": member_id,
                "removed": removed,
            }))
        }
        wire_capability::Action::AgentCreate {
            name,
            role,
            driver,
            computer_id: target_computer_id,
        } => {
            let activity_name = name.clone();
            let activity_role = bounded_activity_text(&role).0;
            let (driver_kind, activity_driver) = match driver {
                wire_capability::DriverKind::Acp {
                    provider: wire_capability::AcpProvider::Codex,
                } => (DriverKind::Acp(AcpProvider::Codex), "codex"),
                wire_capability::DriverKind::Acp {
                    provider: wire_capability::AcpProvider::Claude,
                } => (DriverKind::Acp(AcpProvider::Claude), "claude"),
                wire_capability::DriverKind::Acp {
                    provider: wire_capability::AcpProvider::Pi,
                } => (DriverKind::Acp(AcpProvider::Pi), "pi"),
                wire_capability::DriverKind::Builtin => (DriverKind::Builtin, "builtin"),
            };
            let activity_computer = activity_computer_label(&state.read, target_computer_id)
                .await
                .unwrap_or_else(|| "Computer".to_owned());
            let focus = activity_thread_reference(&state.read, context.focus_thread_id).await;
            let agent_id = MemberId::from_uuid(Uuid::now_v7());
            let mut storage = state.storage.clone();
            let agent = match CreateAgentAction::execute(
                &mut storage,
                CreateAgentActionInput {
                    agent_member_id: agent_id,
                    display_name: name.clone(),
                    role_text: role,
                    computer_id: target_computer_id,
                    driver_kind,
                    action_message_id: MessageId::from_uuid(Uuid::now_v7()),
                    actor_member_id: MemberId::from_uuid(context.agent_id.into_uuid()),
                    idempotency_key: request.idempotency_key.ok_or_else(|| {
                        capability_error(
                            wire_capability::ErrorCode::InvalidArgument,
                            "Idempotency key is required",
                            false,
                        )
                    })?,
                    current_run_id: context.run_id,
                    now: OffsetDateTime::now_utc(),
                },
            )
            .await
            {
                Ok(agent) => agent,
                Err(ApplicationError::Conflict) => {
                    let computers = state
                        .read
                        .computer_options(context.space_id.into_uuid())
                        .await
                        .map_err(|_| {
                            capability_error(
                                wire_capability::ErrorCode::Internal,
                                "Computer options could not be read",
                                false,
                            )
                        })?;
                    let mut details = std::collections::BTreeMap::new();
                    details.insert(
                        "reason".to_owned(),
                        serde_json::json!("computer_not_available"),
                    );
                    details.insert(
                        "next_action".to_owned(),
                        serde_json::json!(
                            "run `sumi agent discover agent.create --json` and reuse one of its `computer_id` values"
                        ),
                    );
                    details.insert(
                        "available_computers".to_owned(),
                        serde_json::json!(
                            computers
                                .iter()
                                .map(|row| json!({
                                    "value": row.id,
                                    "label": row.name,
                                    "hostname": row.hostname,
                                    "os": row.os,
                                    "status": row.connection_status,
                                    "available": row.connection_status == "online"
                                }))
                                .collect::<Vec<_>>()
                        ),
                    );
                    return Err(wire_capability::Error {
                        code: wire_capability::ErrorCode::Conflict,
                        message: "Computer is not online or does not belong to this Space".into(),
                        retryable: false,
                        details,
                    });
                }
                Err(error) => return Err(app_to_capability(error)),
            };
            record_agent_activity(
                state,
                request_origin,
                context.space_id.into_uuid(),
                context.agent_id.into_uuid(),
                "agent.create",
                agent_activity_details(
                    json!({
                        "run_id": context.run_id,
                        "target_member_id": agent.member_id,
                        "thread_id": context.focus_thread_id,
                        "scope_channel_id": focus.as_ref().map(|reference| reference.channel_id),
                    }),
                    vec![
                        ("name", activity_name),
                        ("role", activity_role),
                        ("driver", activity_driver.to_owned()),
                        ("computer", activity_computer),
                    ],
                ),
            )
            .await;
            Ok(json!({"agent_id":agent.member_id,"lifecycle":"provisioning"}))
        }
        wire_capability::Action::InboxCurrent => {
            let rows = state
                .read
                .run_items(context.run_id.into_uuid())
                .await
                .map_err(|_| {
                    capability_error(
                        wire_capability::ErrorCode::Internal,
                        "Run Items could not be read",
                        false,
                    )
                })?;
            Ok(
                json!({"run_id":context.run_id,"items":rows.iter().map(|row|json!({"id":row.id,"kind":row.kind,"strength":row.strength,"status":row.status,"available_at":timestamp(row.available_at),"delivery_outcome":row.delivery_outcome,"delivery_event_id":row.delivery_event_id,"delivery_receipt_at":row.delivery_receipt_at.map(timestamp),"disposition":row.disposition,"disposition_at":row.disposition_at.map(timestamp)})).collect::<Vec<_>>(),"notices":[]}),
            )
        }
        wire_capability::Action::InboxAck { item_id, reason } => {
            let source = activity_inbox_thread_reference(&state.read, item_id).await;
            let mut activity_arguments = source
                .as_ref()
                .map(|reference| vec![("source", reference.label.clone())])
                .unwrap_or_default();
            activity_arguments.push(("disposition", "handled".to_owned()));
            if let Some(reason) = reason {
                activity_arguments.push(("reason", reason));
            }
            record_agent_item_disposition(
                state,
                computer_id,
                context,
                item_id,
                InboxItemDisposition::Handled,
                None,
            )
            .await?;
            record_agent_activity(
                state,
                request_origin,
                context.space_id.into_uuid(),
                context.agent_id.into_uuid(),
                "inbox.ack",
                agent_activity_details(
                    json!({
                        "run_id": context.run_id,
                        "item_id": item_id,
                        "thread_id": source.as_ref().map(|reference| reference.thread_id),
                        "scope_channel_id": source.as_ref().map(|reference| reference.channel_id),
                    }),
                    activity_arguments,
                ),
            )
            .await;
            Ok(json!({"item_id":item_id,"disposition":"handled"}))
        }
        wire_capability::Action::InboxDefer { item_id, until } => {
            let source = activity_inbox_thread_reference(&state.read, item_id).await;
            let mut activity_arguments = source
                .as_ref()
                .map(|reference| vec![("source", reference.label.clone())])
                .unwrap_or_default();
            activity_arguments.extend([
                ("disposition", "deferred".to_owned()),
                ("until", timestamp(until)),
            ]);
            record_agent_item_disposition(
                state,
                computer_id,
                context,
                item_id,
                InboxItemDisposition::Deferred,
                Some(until),
            )
            .await?;
            record_agent_activity(
                state,
                request_origin,
                context.space_id.into_uuid(),
                context.agent_id.into_uuid(),
                "inbox.defer",
                agent_activity_details(
                    json!({
                        "run_id": context.run_id,
                        "item_id": item_id,
                        "thread_id": source.as_ref().map(|reference| reference.thread_id),
                        "scope_channel_id": source.as_ref().map(|reference| reference.channel_id),
                    }),
                    activity_arguments,
                ),
            )
            .await;
            Ok(json!({"item_id":item_id,"disposition":"deferred","available_at":timestamp(until)}))
        }
        _ => Err(capability_error(
            wire_capability::ErrorCode::Unavailable,
            "Agent action is not connected in this runtime",
            false,
        )),
    }
}
