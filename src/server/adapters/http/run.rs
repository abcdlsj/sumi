use super::*;
use crate::protocol::computer::{ComputerErrorCode, DeliveryOutcome, DeliveryReceipt, RunStarted};
use crate::server::{
    adapters::activity::{
        activity_thread_reference, agent_activity_details, record_agent_activity,
    },
    domain::{
        attention::InboxItemDisposition,
        execution::{DeliveryOutcome as ServerDeliveryOutcome, RunErrorCode, RunOutcome},
    },
};

pub(super) async fn run_started(
    State(state): State<RuntimeState>,
    headers: HeaderMap,
    Path((computer_id, run_id)): Path<(Uuid, Uuid)>,
    Json(started): Json<RunStarted>,
) -> Result<StatusCode, ApiError> {
    authenticate_computer(&state, &headers, computer_id).await?;
    if started.run_id.into_uuid() != run_id {
        return Err(ApiError::invalid("Run ID does not match the request path"));
    }
    let mut storage = state.storage.clone();
    StartRun::execute(
        &mut storage,
        StartRunInput {
            run_id: started.run_id,
            computer_id: ComputerId::from_uuid(computer_id),
            now: started.observed_at,
        },
    )
    .await
    .map_err(application_error)?;
    Ok(StatusCode::OK)
}

pub(super) async fn delivery_receipt(
    State(state): State<RuntimeState>,
    headers: HeaderMap,
    Path((computer_id, run_id)): Path<(Uuid, Uuid)>,
    Json(receipt): Json<DeliveryReceipt>,
) -> Result<StatusCode, ApiError> {
    authenticate_computer(&state, &headers, computer_id).await?;
    if receipt.run_id.into_uuid() != run_id {
        return Err(ApiError::invalid("Run ID does not match the request path"));
    }
    let mut storage = state.storage.clone();
    AcknowledgeDelivery::execute(
        &mut storage,
        AcknowledgeDeliveryInput {
            event_id: receipt.event_id,
            run_id: receipt.run_id,
            computer_id: ComputerId::from_uuid(computer_id),
            delivery_sequence: receipt.delivery_sequence.0,
            outcome: match receipt.outcome {
                DeliveryOutcome::Accepted => ServerDeliveryOutcome::Accepted,
                DeliveryOutcome::TooLate => ServerDeliveryOutcome::TooLate,
                DeliveryOutcome::Unsupported => ServerDeliveryOutcome::Unsupported,
            },
            now: OffsetDateTime::now_utc(),
        },
    )
    .await
    .map_err(application_error)?;
    Ok(StatusCode::OK)
}

pub(super) async fn apply_run_result(
    state: &RuntimeState,
    computer_id: Uuid,
    result: RunResult,
) -> Result<(), ApiError> {
    let error_code = result.error_code.map(super::run_error_code);
    let outcome = match result.status {
        RunTerminalStatus::Completed => RunOutcome::Completed,
        RunTerminalStatus::Yielded => RunOutcome::Yielded,
        RunTerminalStatus::Failed => RunOutcome::Failed,
        RunTerminalStatus::Canceled => RunOutcome::Canceled,
    };
    let item_dispositions = result
        .item_outcomes
        .into_iter()
        .map(|item| ItemDispositionInput {
            item_id: item.item_id,
            disposition: match item.disposition {
                ItemDisposition::Handled => InboxItemDisposition::Handled,
                ItemDisposition::Deferred => InboxItemDisposition::Deferred,
                ItemDisposition::Released => InboxItemDisposition::Released,
            },
        })
        .collect();
    let mut storage = state.storage.clone();
    let run = CompleteRun::execute(
        &mut storage,
        CompleteRunInput {
            event_id: result.event_id,
            run_id: result.run_id,
            computer_id: ComputerId::from_uuid(computer_id),
            outcome,
            error_code,
            item_dispositions,
            continuation_note: result.continuation_note,
            max_retry_count: AttentionPolicy::MAX_RETRY_COUNT,
            now: OffsetDateTime::now_utc(),
        },
    )
    .await
    .map_err(application_error)?;
    if result.status == RunTerminalStatus::Yielded {
        let run_view = run.view();
        let focus = activity_thread_reference(&state.read, run_view.focus_thread_id).await;
        let arguments = focus
            .as_ref()
            .map(|reference| vec![("focus", reference.label.clone())])
            .unwrap_or_default();
        record_agent_activity(
            state,
            Some(result.event_id.into_uuid()),
            run_view.space_id.into_uuid(),
            run_view.agent_id.into_uuid(),
            "run.yield",
            agent_activity_details(
                json!({
                    "run_id": run_view.id,
                    "thread_id": run_view.focus_thread_id,
                    "channel_id": focus.as_ref().map(|reference| reference.channel_id),
                    "scope_channel_id": focus.as_ref().map(|reference| reference.channel_id),
                }),
                arguments,
            ),
        )
        .await;
    }
    Ok(())
}

pub(super) async fn run_result(
    State(state): State<RuntimeState>,
    headers: HeaderMap,
    Path((computer_id, run_id)): Path<(Uuid, Uuid)>,
    Json(result): Json<RunResult>,
) -> Result<StatusCode, ApiError> {
    authenticate_computer(&state, &headers, computer_id).await?;
    if result.run_id.into_uuid() != run_id {
        return Err(ApiError::invalid("Run ID does not match the request path"));
    }
    apply_run_result(&state, computer_id, result).await?;
    Ok(StatusCode::OK)
}

#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub(in crate::server::adapters) struct DispatchOutcome {
    pub(crate) dispatched: u32,
    pub(crate) failed: u32,
}

/// Dispatches one Run per Agent that has available work and no live Run.
///
/// Each candidate gets its own transaction, so one Agent whose dispatch fails does not stop the rest.
/// A failed dispatch leaves the Item pending; it carries no deadline, so the next pass retries it.
pub(in crate::server::adapters) async fn dispatch_available_work(
    state: &RuntimeState,
    now: OffsetDateTime,
    limit: u32,
) -> Result<DispatchOutcome, ApplicationError> {
    let mut storage = state.storage.clone();
    let candidates = FindDispatchableWork::candidates(&mut storage, now, limit).await?;
    let mut outcome = DispatchOutcome::default();
    for candidate in candidates {
        let mut storage = state.storage.clone();
        let result = DispatchRun::execute(
            &mut storage,
            DispatchRunInput {
                run_id: RunId::from_uuid(Uuid::now_v7()),
                agent_id: candidate.agent_id,
                task_id: candidate.task_id,
                focus_thread_id: candidate.thread_id,
                trigger: candidate.trigger,
                item_ids: vec![candidate.item_id],
            },
        )
        .await;
        match result {
            Ok(_) => outcome.dispatched += 1,
            Err(error) => {
                outcome.failed += 1;
                let error_code = run_dispatch_error_code(&error);
                let mut storage = state.storage.clone();
                let changed = FindDispatchableWork::record_failure(
                    &mut storage,
                    candidate.item_id,
                    candidate.message_id,
                    candidate.channel_id,
                    error_code,
                )
                .await?;
                if changed {
                    tracing::warn!(
                        computer_id = %candidate.computer_id.into_uuid(),
                        item_id = %candidate.item_id.into_uuid(),
                        agent_id = %candidate.agent_id.into_uuid(),
                        error_code,
                        "Run dispatch failed; the Inbox Item remains pending"
                    );
                }
            }
        }
    }
    Ok(outcome)
}

/// Records a Human's request to stop a Run. Returns immediately: the Run stays live until the Computer
/// reports that the Driver actually stopped, because only the Computer can know that.
pub(super) async fn cancel_run(
    State(state): State<RuntimeState>,
    jar: CookieJar,
    headers: HeaderMap,
    Path((agent_id, run_id)): Path<(Uuid, Uuid)>,
) -> Result<StatusCode, ApiError> {
    let viewer_id = agent_space_member(&state, &jar, agent_id).await?;
    let mut storage = state.storage.clone();
    RequestRunCancel::execute(
        &mut storage,
        RequestRunCancelInput {
            run_id: RunId::from_uuid(run_id),
            requested_by: MemberId::from_uuid(viewer_id),
            idempotency_key: IdempotencyKey::from_uuid(idempotency_header(&headers)?),
            now: OffsetDateTime::now_utc(),
        },
    )
    .await
    .map_err(application_error)?;
    Ok(StatusCode::ACCEPTED)
}

pub(super) fn run_dispatch_error_code(error: &ApplicationError) -> &'static str {
    match error {
        ApplicationError::NotFound => "run_dispatch_not_found",
        ApplicationError::Unauthenticated => "run_dispatch_unauthenticated",
        ApplicationError::PayloadTooLarge => "run_dispatch_payload_too_large",
        ApplicationError::PermissionDenied => "run_dispatch_permission_denied",
        ApplicationError::Conflict | ApplicationError::Domain(_) => "run_dispatch_conflict",
        ApplicationError::ContextChanged | ApplicationError::StaleMessageContext => {
            "run_dispatch_context_changed"
        }
        ApplicationError::Unavailable => "run_dispatch_unavailable",
        ApplicationError::Internal => "run_dispatch_internal",
    }
}

#[derive(Clone, Copy)]
pub(in crate::server::adapters) struct ComputerPrincipal {
    pub(in crate::server::adapters) computer_id: ComputerId,
}

pub(in crate::server::adapters) async fn submit_run_result<P: TransactionPort + Clone>(
    port: &P,
    principal: ComputerPrincipal,
    result: RunResult,
) -> Result<StatusCode, ApiError> {
    let outcome = match result.status {
        RunTerminalStatus::Completed => RunOutcome::Completed,
        RunTerminalStatus::Yielded => RunOutcome::Yielded,
        RunTerminalStatus::Failed => RunOutcome::Failed,
        RunTerminalStatus::Canceled => RunOutcome::Canceled,
    };
    let error_code = result.error_code.map(run_error_code);
    let item_dispositions = result
        .item_outcomes
        .into_iter()
        .map(|item| ItemDispositionInput {
            item_id: item.item_id,
            disposition: match item.disposition {
                ItemDisposition::Handled => InboxItemDisposition::Handled,
                ItemDisposition::Deferred => InboxItemDisposition::Deferred,
                ItemDisposition::Released => InboxItemDisposition::Released,
            },
        })
        .collect();
    let mut port = port.clone();
    CompleteRun::execute(
        &mut port,
        CompleteRunInput {
            event_id: result.event_id,
            run_id: result.run_id,
            computer_id: principal.computer_id,
            outcome,
            error_code,
            item_dispositions,
            continuation_note: result.continuation_note,
            max_retry_count: AttentionPolicy::MAX_RETRY_COUNT,
            now: OffsetDateTime::now_utc(),
        },
    )
    .await?;
    Ok(StatusCode::OK)
}

pub(in crate::server::adapters) fn run_error_code(code: ComputerErrorCode) -> RunErrorCode {
    match code {
        ComputerErrorCode::DriverError => RunErrorCode::DriverError,
        ComputerErrorCode::DriverLost => RunErrorCode::DriverLost,
        ComputerErrorCode::ComputerRestarted => RunErrorCode::ComputerRestarted,
        ComputerErrorCode::SessionUnavailable => RunErrorCode::SessionUnavailable,
        ComputerErrorCode::AgentUnavailable => RunErrorCode::AgentUnavailable,
        ComputerErrorCode::InvalidCommand => RunErrorCode::InvalidCommand,
        ComputerErrorCode::UnhandledItems => RunErrorCode::UnhandledItems,
        ComputerErrorCode::Internal => RunErrorCode::Internal,
    }
}
