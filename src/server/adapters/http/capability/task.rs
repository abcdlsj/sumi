use super::*;

pub(in crate::server::adapters::http) fn task_post_target(
    target: wire_capability::PostTarget,
) -> TaskPostTarget {
    match target {
        wire_capability::PostTarget::Focus => TaskPostTarget::Focus,
        wire_capability::PostTarget::Source => TaskPostTarget::Source,
    }
}

pub(in crate::server::adapters::http) async fn finish_agent_task(
    state: &RuntimeState,
    computer_id: Uuid,
    context: &wire_capability::RunContext,
    idempotency_key: Option<IdempotencyKey>,
    outcome: TaskOutcome,
) -> Result<Value, wire_capability::Error> {
    let task_id = context.task_id.ok_or_else(|| {
        capability_error(
            wire_capability::ErrorCode::Conflict,
            "Run is not bound to a Task",
            false,
        )
    })?;
    let idempotency_key = idempotency_key.ok_or_else(|| {
        capability_error(
            wire_capability::ErrorCode::InvalidArgument,
            "idempotency key is required",
            false,
        )
    })?;
    let (activity_kind, activity_arguments, activity_post_target) = match &outcome {
        TaskOutcome::SubmitReview { message } => {
            let post_to = message.as_ref().map(|message| message.post_to);
            (
                "task.submit_review",
                post_to
                    .map(|target| vec![("post_to", activity_task_post_target(target).to_owned())])
                    .unwrap_or_default(),
                post_to,
            )
        }
        TaskOutcome::Done {
            result: TaskResult::Publish(result),
        } => (
            "task.done",
            vec![(
                "post_to",
                activity_task_post_target(result.post_to).to_owned(),
            )],
            Some(result.post_to),
        ),
        TaskOutcome::Done {
            result: TaskResult::Existing(_),
        } => (
            "task.done",
            vec![("result", "existing_message".to_owned())],
            None,
        ),
        TaskOutcome::Close { reason, .. } => {
            let reason = match reason {
                CloseReason::Invalid => "invalid",
                CloseReason::Duplicate => "duplicate",
                CloseReason::NotNeeded => "not_needed",
                CloseReason::Obsolete => "obsolete",
                CloseReason::Other => "other",
            };
            ("task.close", vec![("reason", reason.to_owned())], None)
        }
    };
    let activity_thread_id = match activity_post_target {
        Some(TaskPostTarget::Focus) | None => context.focus_thread_id,
        Some(TaskPostTarget::Source) => activity_task_source_thread(&state.read, task_id)
            .await
            .unwrap_or(context.focus_thread_id),
        Some(TaskPostTarget::Thread(thread_id)) => thread_id,
    };
    let activity_thread = activity_thread_reference(&state.read, activity_thread_id).await;
    let mut storage = state.storage.clone();
    RecordTaskOutcome::execute(
        &mut storage,
        RecordTaskOutcomeInput {
            scope: TaskOutcomeScope::AgentRun(OutcomeRunContext {
                run_id: context.run_id,
                computer_id: ComputerId::from_uuid(computer_id),
            }),
            actor_member_id: MemberId::from_uuid(context.agent_id.into_uuid()),
            idempotency_key,
            outcome,
            now: OffsetDateTime::now_utc(),
        },
    )
    .await
    .map_err(app_to_capability)?;
    record_agent_activity(
        state,
        Some(idempotency_key.into_uuid()),
        context.space_id.into_uuid(),
        context.agent_id.into_uuid(),
        activity_kind,
        agent_activity_details(
            json!({
                "run_id": context.run_id,
                "task_id": task_id,
                "thread_id": activity_thread_id,
                "scope_channel_id": activity_thread.as_ref().map(|reference| reference.channel_id),
            }),
            activity_arguments,
        ),
    )
    .await;
    capability_value(
        &task_projection(&state.read, task_id.into_uuid())
            .await
            .map_err(api_to_capability)?,
    )
}

pub(in crate::server::adapters::http) async fn record_agent_item_disposition(
    state: &RuntimeState,
    computer_id: Uuid,
    context: &wire_capability::RunContext,
    item_id: InboxItemId,
    disposition: InboxItemDisposition,
    defer_until: Option<OffsetDateTime>,
) -> Result<(), wire_capability::Error> {
    let mut storage = state.storage.clone();
    RecordRunItemDisposition::execute(
        &mut storage,
        RecordRunItemDispositionInput {
            run_id: context.run_id,
            computer_id: ComputerId::from_uuid(computer_id),
            item_id,
            disposition,
            defer_until,
            now: OffsetDateTime::now_utc(),
        },
    )
    .await
    .map_err(app_to_capability)?;
    Ok(())
}
