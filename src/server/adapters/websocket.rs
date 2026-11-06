use crate::protocol::{
    computer::{
        ChannelActivityQueryErrorCode, ChannelActivityQueryResult, ChannelActivityResultEnvelope,
        CommandAck, CommandEnvelope, CommandOutcome, CommandResult, CommandSequence, ComputerFrame,
        ComputerHello, DeliveryOutcome, HandshakeErrorCode, Receipt, ReceiptKind,
        RunTerminalStatus, ServerFrame, ServerHandshake, ShutdownCode,
    },
    version::SUPPORTED,
};
use crate::{
    ids::{ComputerId, DaemonSessionId, EventId, MemberId},
    server::application::ports::ApplicationError,
};

use super::postgres::{ComputerConnection, PostgresAdapter};

pub(super) async fn replay_commands(
    storage: &PostgresAdapter,
    computer_id: ComputerId,
    watermark: CommandSequence,
) -> Result<Vec<CommandEnvelope>, ApplicationError> {
    storage
        .settle_stale_run_commands(computer_id, watermark)
        .await?;
    storage.pending_commands(computer_id, watermark).await
}

pub(super) async fn acknowledge_command(
    storage: &PostgresAdapter,
    computer_id: ComputerId,
    ack: &CommandAck,
) -> Result<(), ApplicationError> {
    storage.acknowledge_command(computer_id, ack).await
}

fn channel_activity_result(
    query_id: crate::ids::QueryId,
    result: Result<crate::protocol::computer::ChannelActivitySnapshot, ApplicationError>,
) -> ChannelActivityResultEnvelope {
    let result = match result {
        Ok(snapshot) => ChannelActivityQueryResult::Snapshot(snapshot),
        Err(error) => ChannelActivityQueryResult::Unavailable {
            code: channel_activity_error_code(&error),
        },
    };
    ChannelActivityResultEnvelope { query_id, result }
}

fn channel_activity_error_code(error: &ApplicationError) -> ChannelActivityQueryErrorCode {
    match error {
        ApplicationError::PermissionDenied => ChannelActivityQueryErrorCode::PermissionDenied,
        ApplicationError::Unavailable => ChannelActivityQueryErrorCode::Unavailable,
        ApplicationError::Conflict | ApplicationError::NotFound => {
            ChannelActivityQueryErrorCode::InvalidRequest
        }
        ApplicationError::Domain(_)
        | ApplicationError::Unauthenticated
        | ApplicationError::PayloadTooLarge
        | ApplicationError::ContextChanged
        | ApplicationError::StaleMessageContext
        | ApplicationError::Internal => ChannelActivityQueryErrorCode::Internal,
    }
}

pub(super) fn negotiate(
    hello: &ComputerHello,
    computer_deleted: bool,
    authenticated: bool,
) -> ServerHandshake {
    let rejection = if !authenticated {
        Some(HandshakeErrorCode::Unauthenticated)
    } else if computer_deleted {
        Some(HandshakeErrorCode::ComputerDeleted)
    } else if SUPPORTED.negotiate(hello.supported_versions).is_none() {
        Some(HandshakeErrorCode::NoCommonVersion)
    } else {
        None
    };
    if let Some(code) = rejection {
        return ServerHandshake::Rejected {
            code,
            supported_versions: SUPPORTED,
        };
    }
    ServerHandshake::Welcome {
        selected_version: SUPPORTED
            .negotiate(hello.supported_versions)
            .expect("common version checked above"),
        supported_versions: SUPPORTED,
        heartbeat_interval_seconds: 15,
    }
}

use super::command::CommandRegistry;
use super::http::{ApiError, ComputerPrincipal};
use super::query::QueryRegistry;
use super::shutdown::ShutdownSignal;
use crate::server::application::execution::{
    AcknowledgeDelivery, AcknowledgeDeliveryInput, ApplyCommandResult, CompleteRun,
    CompleteRunInput, StartRun, StartRunInput, SyncComputerRuns, SyncComputerRunsInput,
};
use crate::server::domain::attention::AttentionPolicy;
use crate::server::domain::execution::{DeliveryOutcome as ServerDeliveryOutcome, RunOutcome};
use axum::extract::ws::{Message as WebSocketMessage, WebSocket};
use futures_util::StreamExt;
use time::OffsetDateTime;
use uuid::Uuid;
pub(super) async fn computer_socket(
    mut socket: WebSocket,
    storage: PostgresAdapter,
    queries: QueryRegistry,
    commands: CommandRegistry,
    shutdown: ShutdownSignal,
    computer_id: Uuid,
    deleted: bool,
) {
    // Counted until the last frame is written and the close finishes; shutdown waits for it.
    let _open_socket = shutdown.socket();
    let Ok(Some(Ok(WebSocketMessage::Text(encoded)))) =
        tokio::time::timeout(COMPUTER_IDLE_TIMEOUT, socket.next()).await
    else {
        return;
    };
    let Ok(hello) = serde_json::from_str::<ComputerHello>(&encoded) else {
        return;
    };
    let handshake = super::websocket::negotiate(&hello, deleted, true);
    if send_json(&mut socket, &handshake).await.is_err() {
        return;
    }
    if !matches!(handshake, ServerHandshake::Welcome { .. }) {
        return;
    }
    tracing::info!(
        %computer_id,
        daemon_version = %hello.daemon_version,
        live_run_count = hello.live_run_ids.len(),
        "Computer connected"
    );
    // Reconcile before replaying commands. The Computer just told us which Runs it still holds; every
    // other non-terminal Run of ours died with its previous daemon process. This is the only path that
    // fails a Run the Server never heard about, and it runs because the Computer reported, not because
    // time passed.
    {
        let mut application = storage.clone();
        match SyncComputerRuns::execute(
            &mut application,
            SyncComputerRunsInput {
                computer_id: ComputerId::from_uuid(computer_id),
                live_run_ids: hello.live_run_ids.clone(),
                max_retry_count: AttentionPolicy::MAX_RETRY_COUNT,
                now: OffsetDateTime::now_utc(),
            },
        )
        .await
        {
            Ok(synced) if synced.runs_failed > 0 => tracing::warn!(
                %computer_id,
                runs_failed = synced.runs_failed,
                items_released = synced.items_released,
                items_dead = synced.items_dead,
                "reconnecting Computer no longer holds these Runs; their Items returned to the queue"
            ),
            Ok(_) => {}
            Err(error) => tracing::error!(
                %computer_id,
                %error,
                "Computer Run sync failed"
            ),
        }
    }
    let mut sent_watermark = replay_pending_commands(
        &storage,
        &mut socket,
        ComputerId::from_uuid(computer_id),
        hello.command_watermark,
    )
    .await;
    let (connection, mut outbound) = queries.connect(computer_id);
    let (command_connection, mut command_wakeups) = commands.connect(computer_id);
    // Marked online only after registering, so a superseded session's offline write below can
    // see this connection and restore the status.
    mark_connection(
        &storage,
        computer_id,
        ComputerConnection::Online {
            daemon_version: &hello.daemon_version,
        },
    )
    .await;
    // The Computer sends a heartbeat and a ping every 15 seconds; silence beyond three periods
    // means a half-open socket.
    let mut read_deadline = tokio::time::Instant::now() + COMPUTER_IDLE_TIMEOUT;
    loop {
        let frame = tokio::select! {
            () = shutdown.stopped() => {
                let _ = send_json(
                    &mut socket,
                    &ServerFrame::Shutdown { code: ShutdownCode::ServerShutdown },
                )
                .await;
                let _ = socket.send(WebSocketMessage::Close(None)).await;
                drain_before_close(&mut socket).await;
                break;
            }
            outgoing = outbound.recv() => {
                let Some(outgoing) = outgoing else { break };
                if send_json(&mut socket, &outgoing).await.is_err() {
                    break;
                }
                continue;
            }
            wakeup = command_wakeups.recv() => {
                let Some(()) = wakeup else { break };
                sent_watermark = replay_pending_commands(
                    &storage,
                    &mut socket,
                    ComputerId::from_uuid(computer_id),
                    sent_watermark,
                )
                .await;
                continue;
            }
            frame = tokio::time::timeout_at(read_deadline, socket.next()) => match frame {
                Ok(Some(frame)) => {
                    read_deadline = tokio::time::Instant::now() + COMPUTER_IDLE_TIMEOUT;
                    frame
                }
                Ok(None) => break,
                Err(_) => {
                    tracing::warn!(%computer_id, "Computer sent nothing within the idle timeout");
                    break;
                }
            },
        };
        let Ok(WebSocketMessage::Text(encoded)) = frame else {
            continue;
        };
        let Ok(frame) = serde_json::from_str::<ComputerFrame>(&encoded) else {
            continue;
        };
        let Some(frame) = from_current_session(frame, hello.daemon_session_id) else {
            tracing::warn!(
                %computer_id,
                "Computer frame arrived from an earlier daemon session; dropped"
            );
            continue;
        };
        match frame {
            ComputerFrame::ChannelActivityQuery { query } => {
                let application = storage.clone();
                let result = application
                    .channel_activity_snapshot(super::postgres::ChannelActivityQueryRequest {
                        computer_id: ComputerId::from_uuid(computer_id),
                        run_id: query.run_id,
                        agent_id: MemberId::from_uuid(query.agent_id.into_uuid()),
                        channel_id: query.channel_id,
                        after_sequence: query.after_sequence,
                        through_sequence: query.through_sequence,
                        limit: query.limit,
                    })
                    .await;
                let result = channel_activity_result(query.query_id, result);
                let _ =
                    send_json(&mut socket, &ServerFrame::ChannelActivityResult { result }).await;
            }
            ComputerFrame::QueryResult { result } => queries.resolve(result),
            ComputerFrame::Heartbeat { heartbeat } => {
                if let Err(error) = storage
                    .touch_computer(ComputerId::from_uuid(computer_id), heartbeat.observed_at)
                    .await
                {
                    tracing::warn!(%computer_id, %error, "Computer heartbeat could not be recorded");
                }
                let replay_watermark = replay_pending_commands(
                    &storage,
                    &mut socket,
                    ComputerId::from_uuid(computer_id),
                    CommandSequence(0),
                )
                .await;
                if replay_watermark.0 > sent_watermark.0 {
                    sent_watermark = replay_watermark;
                }
            }
            ComputerFrame::CommandAck { .. } => {}
            ComputerFrame::RunResult { result } => {
                let event_id = result.event_id;
                let run_id = result.run_id;
                let yielded = result.status == RunTerminalStatus::Yielded;
                let response = super::http::submit_run_result(
                    &storage,
                    ComputerPrincipal {
                        computer_id: ComputerId::from_uuid(computer_id),
                    },
                    result,
                )
                .await;
                if response.is_err() {
                    // Without a receipt the Computer resends this result on every reconnect.
                    tracing::warn!(%computer_id, %run_id, %event_id, "Run result rejected");
                }
                if response.is_ok() {
                    if yielded {
                        match storage.run_activity_focus(run_id).await {
                            Ok(Some(focus)) => {
                                let focus_label = super::activity::activity_thread_label(
                                    focus.slug.as_deref(),
                                    focus.channel_seq,
                                );
                                storage
                                    .record_agent_activity(
                                        Some(event_id.into_uuid()),
                                        focus.space_id,
                                        focus.agent_id,
                                        "run.yield",
                                        super::activity::agent_activity_details(
                                            serde_json::json!({
                                                "run_id": run_id,
                                                "thread_id": focus.thread_id,
                                                "channel_id": focus.channel_id,
                                                "scope_channel_id": focus.channel_id,
                                            }),
                                            vec![("focus", focus_label)],
                                        ),
                                    )
                                    .await;
                            }
                            Ok(None) => {}
                            Err(error) => tracing::warn!(
                                %run_id,
                                %error,
                                "yielded Run activity could not locate its Focus"
                            ),
                        }
                    }
                    let _ = send_json(
                        &mut socket,
                        &ServerFrame::Receipt {
                            receipt: Receipt {
                                event_id,
                                kind: ReceiptKind::RunResult,
                            },
                        },
                    )
                    .await;
                }
            }
            ComputerFrame::RunStarted { started } => {
                let mut application = storage.clone();
                let applied = StartRun::execute(
                    &mut application,
                    StartRunInput {
                        run_id: started.run_id,
                        computer_id: ComputerId::from_uuid(computer_id),
                        now: started.observed_at,
                    },
                )
                .await;
                if applied.is_ok() {
                    let _ = send_json(
                        &mut socket,
                        &ServerFrame::Receipt {
                            receipt: Receipt {
                                event_id: started.event_id,
                                kind: ReceiptKind::RunStarted,
                            },
                        },
                    )
                    .await;
                }
            }
            ComputerFrame::DeliveryReceipt { receipt } => {
                let mut application = storage.clone();
                let applied = AcknowledgeDelivery::execute(
                    &mut application,
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
                .await;
                if applied.is_ok() {
                    let _ = send_json(
                        &mut socket,
                        &ServerFrame::Receipt {
                            receipt: Receipt {
                                event_id: receipt.event_id,
                                kind: ReceiptKind::Delivery,
                            },
                        },
                    )
                    .await;
                }
            }
            ComputerFrame::CommandResult { result } => {
                if let CommandOutcome::Rejected { ref code } = result.outcome {
                    let diagnostic = storage
                        .command_diagnostic(
                            ComputerId::from_uuid(computer_id),
                            result.command_id,
                            result.sequence.0,
                        )
                        .await
                        .ok()
                        .flatten();
                    tracing::warn!(
                        %computer_id,
                        command_id = %result.command_id.into_uuid(),
                        sequence = result.sequence.0,
                        command_kind = diagnostic.as_ref().map_or("<unknown>", |item| item.kind),
                        agent_id = ?diagnostic.as_ref().and_then(|item| item.agent_id),
                        run_id = ?diagnostic.as_ref().and_then(|item| item.run_id),
                        task_id = ?diagnostic.as_ref().and_then(|item| item.task_id),
                        delivery_sequence =
                            diagnostic.as_ref().and_then(|item| item.delivery_sequence),
                        error_code = ?code,
                        "Computer command rejected"
                    );
                }
                if apply_command_result(&storage, computer_id, &result)
                    .await
                    .is_ok()
                {
                    if matches!(result.outcome, CommandOutcome::Rejected { .. })
                        && let Err(error) =
                            apply_rejected_command(&storage, computer_id, &result).await
                    {
                        tracing::error!(
                            %computer_id,
                            command_id = %result.command_id.into_uuid(),
                            sequence = result.sequence.0,
                            ?error,
                            "Rejected Computer command could not be reconciled"
                        );
                        continue;
                    }
                    let ack = CommandAck {
                        command_id: result.command_id,
                        sequence: result.sequence,
                    };
                    let _ = super::websocket::acknowledge_command(
                        &storage,
                        ComputerId::from_uuid(computer_id),
                        &ack,
                    )
                    .await;
                }
            }
        }
    }
    commands.disconnect(command_connection);
    queries.disconnect(connection);
    tracing::info!(%computer_id, "Computer disconnected");
    mark_connection(&storage, computer_id, ComputerConnection::Offline).await;
    // A newer session may have registered and written online before this offline write landed.
    // Checking after the write is ordered: a session registering later writes online after it.
    if commands.is_connected(computer_id) {
        mark_connection(
            &storage,
            computer_id,
            ComputerConnection::Online {
                daemon_version: &hello.daemon_version,
            },
        )
        .await;
    }
}

/// Frames whose fields describe execution state carry the daemon session that produced them. A
/// reconnecting Computer replays durable results under a new session, so a frame from an earlier
/// session can still arrive on this socket; applying it would attribute work to a connection the
/// Server already reconciled.
fn from_current_session(frame: ComputerFrame, session: DaemonSessionId) -> Option<ComputerFrame> {
    let frame_session = match &frame {
        ComputerFrame::Heartbeat { heartbeat } => Some(heartbeat.daemon_session_id),
        ComputerFrame::RunResult { result } => Some(result.daemon_session_id),
        ComputerFrame::CommandResult { result } => Some(result.daemon_session_id),
        _ => None,
    };
    match frame_session {
        Some(frame_session) if frame_session != session => None,
        _ => Some(frame),
    }
}

async fn mark_connection(
    storage: &PostgresAdapter,
    computer_id: Uuid,
    connection: ComputerConnection<'_>,
) {
    if let Err(error) = storage
        .set_computer_connection(computer_id, connection)
        .await
    {
        tracing::warn!(%computer_id, %error, "Computer connection status could not be recorded");
    }
}

const COMPUTER_IDLE_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(45);
const SEND_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(30);
const SHUTDOWN_DRAIN_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(5);

/// Sends every pending command after `watermark` and returns the last sequence actually sent.
async fn replay_pending_commands(
    storage: &PostgresAdapter,
    socket: &mut WebSocket,
    computer_id: ComputerId,
    watermark: CommandSequence,
) -> CommandSequence {
    let mut sent = watermark;
    if let Ok(commands) = super::websocket::replay_commands(storage, computer_id, watermark).await {
        for envelope in commands {
            let sequence = envelope.sequence;
            if send_json(
                socket,
                &ServerFrame::Command {
                    envelope: Box::new(envelope),
                },
            )
            .await
            .is_err()
            {
                break;
            }
            sent = sequence;
        }
    }
    sent
}

async fn apply_command_result(
    storage: &PostgresAdapter,
    computer_id: Uuid,
    result: &CommandResult,
) -> Result<(), ApiError> {
    let mut storage = storage.clone();
    ApplyCommandResult::execute(
        &mut storage,
        ComputerId::from_uuid(computer_id),
        result.command_id,
        result.sequence.0,
        matches!(result.outcome, CommandOutcome::Applied),
    )
    .await
    .map_err(|_| ApiError::internal())
}

async fn apply_rejected_command(
    storage: &PostgresAdapter,
    computer_id: Uuid,
    result: &CommandResult,
) -> Result<(), ApiError> {
    // A command is acknowledged only after its rejected delivery compensation succeeds. Once the
    // acknowledgement is durable, replaying the same result must not emit another activity event.
    if storage
        .command_is_acknowledged(
            ComputerId::from_uuid(computer_id),
            result.command_id,
            result.sequence.0,
        )
        .await
        .map_err(|_| ApiError::internal())?
    {
        return Ok(());
    }
    if let Some((run_id, delivery_sequence)) = storage
        .run_attach_command_target(
            ComputerId::from_uuid(computer_id),
            result.command_id,
            result.sequence.0,
        )
        .await
        .map_err(|_| ApiError::internal())?
    {
        let mut application = storage.clone();
        AcknowledgeDelivery::execute(
            &mut application,
            crate::server::application::execution::AcknowledgeDeliveryInput {
                event_id: EventId::from_uuid(result.command_id.into_uuid()),
                run_id,
                computer_id: ComputerId::from_uuid(computer_id),
                delivery_sequence,
                outcome: ServerDeliveryOutcome::Unsupported,
                now: OffsetDateTime::now_utc(),
            },
        )
        .await
        .map_err(|_| ApiError::internal())?;

        if let Some(focus) = storage
            .run_activity_focus(run_id)
            .await
            .map_err(|_| ApiError::internal())?
        {
            storage
                .record_agent_activity_with_id(
                    EventId::from_uuid(result.command_id.into_uuid()),
                    focus.space_id,
                    focus.agent_id,
                    "run.delivery_rejected",
                    super::activity::agent_activity_details(
                        serde_json::json!({
                            "run_id": run_id,
                            "thread_id": focus.thread_id,
                            "channel_id": focus.channel_id,
                            "scope_channel_id": focus.channel_id,
                        }),
                        vec![("delivery_sequence", delivery_sequence.to_string())],
                    ),
                )
                .await
                .map_err(|_| ApiError::internal())?;
        }
        return Ok(());
    }

    let Some(run_id) = storage
        .run_start_command_target(
            ComputerId::from_uuid(computer_id),
            result.command_id,
            result.sequence.0,
        )
        .await
        .map_err(|_| ApiError::internal())?
    else {
        return Ok(());
    };
    let CommandOutcome::Rejected { code } = &result.outcome else {
        return Ok(());
    };
    let mut application = storage.clone();
    CompleteRun::execute(
        &mut application,
        CompleteRunInput {
            event_id: EventId::from_uuid(result.command_id.into_uuid()),
            run_id,
            computer_id: ComputerId::from_uuid(computer_id),
            outcome: RunOutcome::Failed,
            error_code: Some(super::http::run_error_code(*code)),
            item_dispositions: Vec::new(),
            continuation_note: None,
            max_retry_count: AttentionPolicy::MAX_RETRY_COUNT,
            now: OffsetDateTime::now_utc(),
        },
    )
    .await
    .map_err(|_| ApiError::internal())?;
    Ok(())
}

/// Reads until the peer closes so the Shutdown frame reaches it.
///
/// Dropping a socket while its receive buffer still holds unread frames makes the kernel send a
/// reset instead of a FIN, and a reset discards data the peer has not read yet, including the
/// Shutdown frame that was just written. Reading to the peer's Close keeps the close orderly.
async fn drain_before_close(socket: &mut WebSocket) {
    let _ = tokio::time::timeout(SHUTDOWN_DRAIN_TIMEOUT, async {
        while let Some(Ok(frame)) = socket.next().await {
            if matches!(frame, WebSocketMessage::Close(_)) {
                break;
            }
        }
    })
    .await;
}

async fn send_json(
    socket: &mut WebSocket,
    value: &impl serde::Serialize,
) -> Result<(), axum::Error> {
    let encoded = serde_json::to_string(value).map_err(axum::Error::new)?;
    tokio::time::timeout(
        SEND_TIMEOUT,
        socket.send(WebSocketMessage::Text(encoded.into())),
    )
    .await
    .map_err(axum::Error::new)?
}

#[cfg(test)]
mod tests {
    use uuid::Uuid;

    use super::*;
    use crate::protocol::{
        computer::{CommandOutcome, CommandSequence, Heartbeat, RunResult, RunTerminalStatus},
        version::{ProtocolVersion, ProtocolVersionRange},
    };

    fn hello(range: ProtocolVersionRange) -> ComputerHello {
        ComputerHello {
            supported_versions: range,
            daemon_version: "1.0.0".into(),
            daemon_session_id: DaemonSessionId::from_uuid(Uuid::now_v7()),
            command_watermark: CommandSequence(0),
            live_run_ids: Vec::new(),
        }
    }

    #[test]
    fn frames_from_an_earlier_daemon_session_are_dropped() {
        let session = DaemonSessionId::from_uuid(Uuid::now_v7());
        let earlier = DaemonSessionId::from_uuid(Uuid::now_v7());
        let heartbeat = |daemon_session_id| ComputerFrame::Heartbeat {
            heartbeat: Heartbeat {
                daemon_session_id,
                observed_at: OffsetDateTime::now_utc(),
            },
        };

        assert!(from_current_session(heartbeat(earlier), session).is_none());
        assert!(from_current_session(heartbeat(session), session).is_some());
        let run_result = |daemon_session_id| ComputerFrame::RunResult {
            result: RunResult {
                daemon_session_id,
                event_id: EventId::from_uuid(Uuid::now_v7()),
                run_id: crate::ids::RunId::from_uuid(Uuid::now_v7()),
                status: RunTerminalStatus::Completed,
                item_outcomes: Vec::new(),
                continuation_note: None,
                error_code: None,
            },
        };
        assert!(from_current_session(run_result(earlier), session).is_none());
        assert!(from_current_session(run_result(session), session).is_some());
        let command_result = |daemon_session_id| ComputerFrame::CommandResult {
            result: CommandResult {
                daemon_session_id,
                command_id: crate::ids::CommandId::from_uuid(Uuid::now_v7()),
                sequence: CommandSequence(3),
                outcome: CommandOutcome::Applied,
            },
        };
        assert!(from_current_session(command_result(earlier), session).is_none());
        assert!(from_current_session(command_result(session), session).is_some());
    }

    #[test]
    fn handshake_rejects_deleted_computer_and_disjoint_protocol() {
        assert!(matches!(
            negotiate(&hello(SUPPORTED), true, true),
            ServerHandshake::Rejected {
                code: HandshakeErrorCode::ComputerDeleted,
                ..
            }
        ));
        let future = ProtocolVersionRange::new(ProtocolVersion::new(8), ProtocolVersion::new(8));
        assert!(matches!(
            negotiate(&hello(future), false, true),
            ServerHandshake::Rejected {
                code: HandshakeErrorCode::NoCommonVersion,
                ..
            }
        ));
    }

    #[test]
    fn channel_activity_query_failure_is_explicit_and_content_free() {
        let query_id = crate::ids::QueryId::from_uuid(Uuid::now_v7());
        let frame = channel_activity_result(query_id, Err(ApplicationError::Unavailable));
        assert_eq!(frame.query_id, query_id);
        assert!(matches!(
            frame.result,
            ChannelActivityQueryResult::Unavailable {
                code: ChannelActivityQueryErrorCode::Unavailable,
            }
        ));
        let encoded = serde_json::to_string(&frame).unwrap();
        assert!(!encoded.contains("Message body"));
    }
}
