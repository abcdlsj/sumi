use std::collections::BTreeSet;

use async_trait::async_trait;
use serde_json::json;
use sha2::{Digest, Sha256};
use sqlx::{PgPool, Postgres, Row, pool::PoolConnection};
use time::OffsetDateTime;
use uuid::Uuid;

use crate::{
    ids::{
        AgentId, AttachmentId, ChannelId, CommandId, ComputerId, EventId, IdempotencyKey,
        InboxItemId, MemberId, MessageId, NoticeId, RunId, SpaceId, TaskId, ThreadId,
    },
    protocol::computer::{
        ActionKind, ActionTarget, ActivityEventKind, ActivityEventSnapshot, AgentRetire,
        AttentionNotice, AttentionStrength as WireAttentionStrength,
        ChannelActivityMessageSnapshot, ChannelActivitySnapshot, Command, CommandAck,
        CommandDiagnostic, CommandEnvelope, CommandSequence, DeliverySequence, FocusSnapshot,
        InboxItemSnapshot, InboxSourceKind, MessageContent as WireMessageContent, MessageSnapshot,
        NoticeLocation, RunAttachItem, RunNotice, RunStart, RunStop, RunTaskBound,
        SessionChangeReason, SessionCommand, SessionScope, StopReason, TaskSnapshot,
        TaskStatus as WireTaskStatus,
    },
    server::{
        application::ports::{
            ApplicationError, AttachmentTransaction, AuthenticatedHuman, CollaborationTransaction,
            ComputerRecord, CreatedSpace, DirectMessageView, DispatchCandidate, Effect, EffectSink,
            ExecutionTransaction, HumanMemberRecord, IdentityTransaction, InboxItemView,
            InboxScope, MemberKind, MessageDraft, ObservationScope, ObservedMessage,
            PairedComputer, PublishedMessage, RunCapabilityProof, SpaceHumanMember,
            SpaceMemberView, TaskTransaction, TransactionPort, UnobservedMessages,
        },
        domain::{
            access::{HumanRegistration, SpaceAccess},
            attachment::{Attachment, AttachmentSnapshot, AttachmentStatus},
            attention::{
                AmbientAggregateSnapshot, AttentionStrength, InboxItem, InboxItemDisposition,
                InboxItemKind, InboxItemSnapshot as DomainInboxItemSnapshot, InboxItemStatus,
            },
            conversation::{
                Channel, ChannelKind, Message, MessageContent, MessagePlacement, Thread,
            },
            execution::{
                DeliveryOutcome, Run, RunErrorCode, RunItemSnapshot, RunOutcome,
                RunSnapshot as DomainRunSnapshot, RunStatus, RunTrigger,
            },
            identity::{
                AccessLevel, AcpProvider, Agent, AgentLifecycle, Computer, ComputerLifecycle,
                DriverKind, Member, PermissionAction,
            },
            invitation::{Invitation, InvitationDraft, InvitationStatus},
            pairing::{ComputerOs, Pairing, PairingRequest, PairingStatus},
            task::{
                CloseReason, RelatedThreadSnapshot, Task, TaskSnapshot as DomainTaskSnapshot,
                TaskStatus,
            },
        },
    },
};

const BASELINE: &str = include_str!("../../../../schema/postgres.sql");

/// Where a Run's Focus sits, for Run activity entries.
pub(super) struct RunActivityFocus {
    pub(super) agent_id: MemberId,
    pub(super) space_id: SpaceId,
    pub(super) thread_id: ThreadId,
    pub(super) channel_id: Uuid,
    pub(super) channel_seq: i64,
    pub(super) slug: Option<String>,
}

/// Schema version written by `schema/postgres.sql`, which always describes the current schema.
/// A database at any other version is refused instead of upgraded.
const SCHEMA_VERSION: i32 = 14;

/// How many `outbox_events` rows one stream read scans before the stream decides to page on.
const EVENT_PAGE: usize = 100;

#[derive(Clone)]
pub(super) struct PostgresAdapter {
    pool: PgPool,
    commands: super::command::CommandRegistry,
}

pub(super) struct PostgresTransaction {
    connection: PoolConnection<Postgres>,
    effects: Vec<Effect>,
    /// Space events of this transaction, written by [`PostgresTransaction::commit`] right before
    /// `COMMIT` so the Space row lock that orders them is held only for the commit itself.
    events: Vec<PendingSpaceEvent>,
    notified_computers: std::collections::BTreeSet<Uuid>,
}

struct PendingSpaceEvent {
    event_id: EventId,
    space_id: SpaceId,
    kind: String,
    payload: serde_json::Value,
    created_at: OffsetDateTime,
    idempotent: bool,
}

pub(super) struct ChannelActivityQueryRequest {
    pub(super) computer_id: ComputerId,
    pub(super) run_id: RunId,
    pub(super) agent_id: MemberId,
    pub(super) channel_id: ChannelId,
    pub(super) after_sequence: u64,
    pub(super) through_sequence: u64,
    pub(super) limit: u32,
}

#[derive(Clone, Copy)]
struct AmbientActivityEvent {
    channel_id: ChannelId,
    channel_seq: u64,
    kind: ActivityEventKind,
    message_id: Option<MessageId>,
    member_id: Option<MemberId>,
    now: OffsetDateTime,
}

struct AmbientActivityInput {
    space_id: SpaceId,
    member_id: MemberId,
    channel_id: ChannelId,
    thread_id: ThreadId,
    kind: InboxItemKind,
    event: AmbientActivityEvent,
}

struct ChannelMemberActivityInput {
    channel_id: ChannelId,
    actor: MemberId,
    subject_member_id: MemberId,
    thread_id: ThreadId,
    event: AmbientActivityEvent,
}

impl PostgresAdapter {
    pub(super) fn new(pool: PgPool) -> Self {
        Self {
            pool,
            commands: super::command::CommandRegistry::default(),
        }
    }

    pub(super) fn commands(&self) -> super::command::CommandRegistry {
        self.commands.clone()
    }

    pub(super) async fn channel_activity_snapshot(
        &self,
        request: ChannelActivityQueryRequest,
    ) -> Result<ChannelActivitySnapshot, ApplicationError> {
        let connection = self.pool.acquire().await.map_err(map_sqlx)?;
        let mut transaction = PostgresTransaction {
            connection,
            effects: Vec::new(),
            events: Vec::new(),
            notified_computers: BTreeSet::new(),
        };
        transaction.channel_activity_snapshot(request).await
    }

    pub(super) async fn initialize_schema(&self) -> Result<(), sqlx::Error> {
        let mut transaction = self.pool.begin().await?;
        sqlx::query("SELECT pg_advisory_xact_lock(hashtextextended('sumi-schema-baseline', 0))")
            .execute(&mut *transaction)
            .await?;
        let initialized: bool =
            sqlx::query_scalar("SELECT to_regclass('public.schema_meta') IS NOT NULL")
                .fetch_one(&mut *transaction)
                .await?;
        if !initialized {
            sqlx::raw_sql(BASELINE).execute(&mut *transaction).await?;
        }
        let version: i32 = sqlx::query_scalar("SELECT max(version) FROM schema_meta")
            .fetch_one(&mut *transaction)
            .await?;
        if version < SCHEMA_VERSION {
            return Err(sqlx::Error::Protocol(format!(
                "Server database is at schema version {version}, older than the supported \
                 {SCHEMA_VERSION}; recreate the database from schema/postgres.sql"
            )));
        }
        if version > SCHEMA_VERSION {
            return Err(sqlx::Error::Protocol(format!(
                "Server database is at schema version {version}, newer than this Server supports \
                 ({SCHEMA_VERSION}); upgrade the Server"
            )));
        }
        transaction.commit().await
    }

    /// Records that a Computer answered a heartbeat; an unknown Computer is ignored.
    pub(super) async fn touch_computer(
        &self,
        computer_id: ComputerId,
        observed_at: OffsetDateTime,
    ) -> Result<(), ApplicationError> {
        sqlx::query("UPDATE computers SET last_seen_at=$2 WHERE id=$1")
            .bind(computer_id.into_uuid())
            .bind(observed_at)
            .execute(&self.pool)
            .await
            .map_err(map_sqlx)?;
        Ok(())
    }

    /// The Agent and Channel a Run's Focus belongs to, for Run activity that needs a location.
    pub(super) async fn run_activity_focus(
        &self,
        run_id: RunId,
    ) -> Result<Option<RunActivityFocus>, ApplicationError> {
        let row = sqlx::query(
            "SELECT agent_runs.agent_id,agent_runs.space_id,agent_runs.focus_thread_id,\
                    messages.channel_id,messages.channel_seq,channels.slug \
             FROM agent_runs JOIN messages ON messages.id=agent_runs.focus_thread_id \
                    JOIN channels ON channels.id=messages.channel_id \
             WHERE agent_runs.id=$1",
        )
        .bind(run_id.into_uuid())
        .fetch_optional(&self.pool)
        .await
        .map_err(map_sqlx)?;
        Ok(row.map(|row| RunActivityFocus {
            agent_id: MemberId::from_uuid(row.get("agent_id")),
            space_id: SpaceId::from_uuid(row.get("space_id")),
            thread_id: ThreadId::from_uuid(row.get("focus_thread_id")),
            channel_id: row.get("channel_id"),
            channel_seq: row.get("channel_seq"),
            slug: row.get("slug"),
        }))
    }

    pub(super) async fn pending_commands(
        &self,
        computer_id: ComputerId,
        watermark: CommandSequence,
    ) -> Result<Vec<CommandEnvelope>, ApplicationError> {
        let watermark = i64::try_from(watermark.0).map_err(|_| ApplicationError::Conflict)?;
        let rows = sqlx::query(
            "SELECT id,computer_seq,payload_json FROM computer_commands \
             WHERE computer_id=$1 AND computer_seq>$2 AND acked_at IS NULL ORDER BY computer_seq",
        )
        .bind(computer_id.into_uuid())
        .bind(watermark)
        .fetch_all(&self.pool)
        .await
        .map_err(map_sqlx)?;
        rows.into_iter()
            .map(|row| {
                let sequence = u64::try_from(row.get::<i64, _>("computer_seq"))
                    .map_err(|_| ApplicationError::Internal)?;
                let command = serde_json::from_value(row.get("payload_json"))
                    .map_err(|_| ApplicationError::Internal)?;
                Ok(CommandEnvelope {
                    command_id: CommandId::from_uuid(row.get("id")),
                    sequence: CommandSequence(sequence),
                    command,
                })
            })
            .collect()
    }

    /// Settles pending Run commands whose target Run is already terminal. A command can be queued
    /// in the same instant the Run result commits (the attention router and the result writer do
    /// not share one transaction), so replay must not deliver it to the Computer.
    pub(super) async fn settle_stale_run_commands(
        &self,
        computer_id: ComputerId,
        watermark: CommandSequence,
    ) -> Result<(), ApplicationError> {
        let watermark = i64::try_from(watermark.0).map_err(|_| ApplicationError::Conflict)?;
        sqlx::query(
            "UPDATE computer_commands SET acked_at=COALESCE(acked_at,now()) \
             WHERE computer_id=$1 AND computer_seq>$2 AND acked_at IS NULL \
               AND kind IN ('run.start','run.attach_item','run.notice','run.task_bound','run.stop') \
               AND EXISTS ( \
                 SELECT 1 FROM agent_runs r \
                 WHERE r.id=(computer_commands.payload_json #>> '{payload,run_id}')::uuid \
                   AND r.status IN ('completed','yielded','failed','canceled'))",
        )
        .bind(computer_id.into_uuid())
        .bind(watermark)
        .execute(&self.pool)
        .await
        .map_err(map_sqlx)?;
        Ok(())
    }

    pub(super) async fn acknowledge_command(
        &self,
        computer_id: ComputerId,
        ack: &CommandAck,
    ) -> Result<(), ApplicationError> {
        let sequence = i64::try_from(ack.sequence.0).map_err(|_| ApplicationError::Conflict)?;
        let changed = sqlx::query(
            "UPDATE computer_commands SET acked_at=COALESCE(acked_at,now()) \
             WHERE id=$1 AND computer_id=$2 AND computer_seq=$3",
        )
        .bind(ack.command_id.into_uuid())
        .bind(computer_id.into_uuid())
        .bind(sequence)
        .execute(&self.pool)
        .await
        .map_err(map_sqlx)?;
        if changed.rows_affected() == 1 {
            Ok(())
        } else {
            Err(ApplicationError::ContextChanged)
        }
    }

    pub(super) async fn command_is_acknowledged(
        &self,
        computer_id: ComputerId,
        command_id: CommandId,
        sequence: u64,
    ) -> Result<bool, ApplicationError> {
        let sequence = i64::try_from(sequence).map_err(|_| ApplicationError::Conflict)?;
        sqlx::query_scalar(
            "SELECT acked_at IS NOT NULL FROM computer_commands \
             WHERE id=$1 AND computer_id=$2 AND computer_seq=$3",
        )
        .bind(command_id.into_uuid())
        .bind(computer_id.into_uuid())
        .bind(sequence)
        .fetch_optional(&self.pool)
        .await
        .map_err(map_sqlx)?
        .ok_or(ApplicationError::NotFound)
    }

    pub(super) async fn command_diagnostic(
        &self,
        computer_id: ComputerId,
        command_id: CommandId,
        sequence: u64,
    ) -> Result<Option<CommandDiagnostic>, ApplicationError> {
        let sequence = i64::try_from(sequence).map_err(|_| ApplicationError::Conflict)?;
        let row = sqlx::query(
            "SELECT payload_json FROM computer_commands \
             WHERE id=$1 AND computer_id=$2 AND computer_seq=$3",
        )
        .bind(command_id.into_uuid())
        .bind(computer_id.into_uuid())
        .bind(sequence)
        .fetch_optional(&self.pool)
        .await
        .map_err(map_sqlx)?;
        let Some(row) = row else {
            return Ok(None);
        };
        let command = serde_json::from_value::<Command>(row.get("payload_json"))
            .map_err(|_| ApplicationError::Internal)?;
        Ok(Some(command.diagnostic()))
    }

    pub(super) async fn run_attach_command_target(
        &self,
        computer_id: ComputerId,
        command_id: CommandId,
        sequence: u64,
    ) -> Result<Option<(RunId, u64)>, ApplicationError> {
        let sequence = i64::try_from(sequence).map_err(|_| ApplicationError::Conflict)?;
        let row = sqlx::query(
            "SELECT kind,payload_json #>> '{payload,run_id}' AS run_id,\
                    payload_json #>> '{payload,delivery_sequence}' AS delivery_sequence \
             FROM computer_commands WHERE id=$1 AND computer_id=$2 AND computer_seq=$3",
        )
        .bind(command_id.into_uuid())
        .bind(computer_id.into_uuid())
        .bind(sequence)
        .fetch_optional(&self.pool)
        .await
        .map_err(map_sqlx)?;
        let Some(row) = row else {
            return Err(ApplicationError::NotFound);
        };
        if row.get::<&str, _>("kind") != "run.attach_item" {
            return Ok(None);
        }
        let run_id = Uuid::parse_str(row.get::<&str, _>("run_id"))
            .map_err(|_| ApplicationError::Internal)?;
        let delivery_sequence = row
            .get::<&str, _>("delivery_sequence")
            .parse::<u64>()
            .map_err(|_| ApplicationError::Internal)?;
        Ok(Some((RunId::from_uuid(run_id), delivery_sequence)))
    }

    pub(super) async fn run_start_command_target(
        &self,
        computer_id: ComputerId,
        command_id: CommandId,
        sequence: u64,
    ) -> Result<Option<RunId>, ApplicationError> {
        let sequence = i64::try_from(sequence).map_err(|_| ApplicationError::Conflict)?;
        let row = sqlx::query(
            "SELECT kind,payload_json #>> '{payload,run_id}' AS run_id \
             FROM computer_commands WHERE id=$1 AND computer_id=$2 AND computer_seq=$3",
        )
        .bind(command_id.into_uuid())
        .bind(computer_id.into_uuid())
        .bind(sequence)
        .fetch_optional(&self.pool)
        .await
        .map_err(map_sqlx)?;
        let Some(row) = row else {
            return Err(ApplicationError::NotFound);
        };
        if row.get::<&str, _>("kind") != "run.start" {
            return Ok(None);
        }
        let run_id = Uuid::parse_str(row.get::<&str, _>("run_id"))
            .map_err(|_| ApplicationError::Internal)?;
        Ok(Some(RunId::from_uuid(run_id)))
    }

    /// Reads one page of the Space event stream as one viewer sees it, starting after `cursor`.
    ///
    /// `viewer_member_id` filters events whose payload names a Channel the viewer cannot read, and
    /// Inbox events belonging to another Member, so a private Channel does not leak through the
    /// stream. Filtering happens after the page is read: the returned `scanned` cursor is derived
    /// from the scanned rows, never from the visible ones, so an unreadable backlog cannot stall the
    /// stream or be re-read on every wake-up.
    pub(super) async fn browser_events(
        &self,
        space_id: SpaceId,
        viewer_member_id: MemberId,
        cursor: Option<super::realtime::EventCursor>,
    ) -> Result<super::realtime::BrowserEventPage<serde_json::Value>, ApplicationError> {
        let rows = if let Some(cursor) = cursor {
            sqlx::query(
                "SELECT id,kind,payload_json,created_at,sequence FROM outbox_events \
                 WHERE space_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3",
            )
            .bind(space_id.into_uuid())
            .bind(sequence_bind(cursor)?)
            .bind(EVENT_PAGE as i64)
            .fetch_all(&self.pool)
            .await
            .map_err(map_sqlx)?
        } else {
            sqlx::query(
                "SELECT id,kind,payload_json,created_at,sequence FROM outbox_events \
                 WHERE space_id=$1 ORDER BY sequence LIMIT $2",
            )
            .bind(space_id.into_uuid())
            .bind(EVENT_PAGE as i64)
            .fetch_all(&self.pool)
            .await
            .map_err(map_sqlx)?
        };
        let full = rows.len() == EVENT_PAGE;
        let scanned = rows
            .last()
            .map(|last| {
                u64::try_from(last.get::<i64, _>("sequence"))
                    .map(super::realtime::EventCursor::new)
                    .map_err(|_| ApplicationError::Internal)
            })
            .transpose()?;
        let readable_channels = sqlx::query_scalar::<_, Uuid>(
            "SELECT channel_id FROM channel_members WHERE member_id=$1",
        )
        .bind(viewer_member_id.into_uuid())
        .fetch_all(&self.pool)
        .await
        .map_err(map_sqlx)?
        .into_iter()
        .collect::<std::collections::HashSet<_>>();
        // Governors read Agent Inboxes, so they must also receive their `inbox.changed`.
        let governs_space = sqlx::query_scalar::<_, bool>(
            "SELECT access_level IN ('owner','admin') FROM members WHERE id=$1",
        )
        .bind(viewer_member_id.into_uuid())
        .fetch_optional(&self.pool)
        .await
        .map_err(map_sqlx)?
        .unwrap_or(false);
        let events = rows
            .into_iter()
            .map(|row| {
                Ok(super::realtime::BrowserEvent {
                    event_id: EventId::from_uuid(row.get("id")),
                    event_type: row.get("kind"),
                    space_id,
                    occurred_at: row.get("created_at"),
                    data: row.get::<serde_json::Value, _>("payload_json"),
                    cursor: super::realtime::EventCursor::new(
                        u64::try_from(row.get::<i64, _>("sequence"))
                            .map_err(|_| ApplicationError::Internal)?,
                    ),
                })
            })
            .collect::<Result<Vec<_>, ApplicationError>>()?
            .into_iter()
            .filter(|event| {
                event_is_visible(
                    &event.event_type,
                    &event.data,
                    viewer_member_id,
                    governs_space,
                    &readable_channels,
                )
            })
            .collect();
        Ok(super::realtime::BrowserEventPage {
            events,
            scanned,
            full,
        })
    }

    /// Position the stream should start from when it has no `Last-Event-ID`: the Space tail.
    pub(super) async fn space_event_tail(
        &self,
        space_id: SpaceId,
    ) -> Result<super::realtime::EventCursor, ApplicationError> {
        let tail: i64 = sqlx::query_scalar("SELECT event_sequence FROM spaces WHERE id=$1")
            .bind(space_id.into_uuid())
            .fetch_one(&self.pool)
            .await
            .map_err(map_sqlx)?;
        Ok(super::realtime::EventCursor::new(
            u64::try_from(tail).map_err(|_| ApplicationError::Internal)?,
        ))
    }

    /// Maps a stream `Last-Event-ID` to its position. `None` means the event is unknown here — it
    /// belongs to another Space or left the outbox — and the stream turns that into
    /// `context_changed` so the Browser rebuilds its projection.
    pub(super) async fn event_cursor(
        &self,
        space_id: SpaceId,
        event_id: EventId,
    ) -> Result<Option<super::realtime::EventCursor>, ApplicationError> {
        let sequence: Option<i64> =
            sqlx::query_scalar("SELECT sequence FROM outbox_events WHERE id=$1 AND space_id=$2")
                .bind(event_id.into_uuid())
                .bind(space_id.into_uuid())
                .fetch_optional(&self.pool)
                .await
                .map_err(map_sqlx)?;
        sequence
            .map(|sequence| {
                u64::try_from(sequence)
                    .map(super::realtime::EventCursor::new)
                    .map_err(|_| ApplicationError::Internal)
            })
            .transpose()
    }

    /// Best-effort insert of an ephemeral `agent.activity` event for the Browser feed. A failed
    /// insert must never fail the Agent action that produced the activity.
    ///
    /// The activity is written after the action commits, so a resent action reaches this point
    /// again. `origin` names that request; the event ID is derived from it, the kind and the IDs
    /// in the payload, so the retry finds the first row and adds none. Without an origin there
    /// is nothing to match a retry against and the event gets a fresh ID.
    pub(in crate::server::adapters) async fn record_agent_activity(
        &self,
        origin: Option<Uuid>,
        space_id: SpaceId,
        agent_member_id: MemberId,
        kind: &str,
        payload: serde_json::Value,
    ) {
        let event_id = origin.map_or_else(
            || EventId::from_uuid(Uuid::now_v7()),
            |origin| agent_activity_event_id(origin, kind, &payload),
        );
        if let Err(error) = self
            .record_agent_activity_with_id(event_id, space_id, agent_member_id, kind, payload)
            .await
        {
            tracing::warn!(
                agent_member_id = %agent_member_id.into_uuid(),
                kind,
                error = %error,
                "agent.activity event could not be recorded"
            );
        }
    }

    pub(in crate::server::adapters) async fn record_agent_activity_with_id(
        &self,
        event_id: EventId,
        space_id: SpaceId,
        agent_member_id: MemberId,
        kind: &str,
        payload: serde_json::Value,
    ) -> Result<(), ApplicationError> {
        let mut record = payload;
        if let Some(object) = record.as_object_mut() {
            object.insert(
                "member_id".to_owned(),
                serde_json::json!(agent_member_id.into_uuid()),
            );
            object.insert("kind".to_owned(), serde_json::json!(kind));
        } else {
            record = serde_json::json!({
                "member_id": agent_member_id.into_uuid(),
                "kind": kind,
            });
        }
        // Arguments carry bounded labels such as a Channel slug or Task title, so they require a
        // Channel scope. Keep the action fact if a caller forgot the scope, but drop its details.
        let has_channel_scope = ["channel_id", "scope_channel_id"].iter().any(|field| {
            record
                .get(*field)
                .and_then(serde_json::Value::as_str)
                .and_then(|value| value.parse::<Uuid>().ok())
                .is_some()
        });
        if !has_channel_scope && let Some(object) = record.as_object_mut() {
            object.remove("arguments");
        }
        // The activity event still needs a Space commit position, so it runs in its own
        // transaction: allocation and insert either both land or neither does.
        let mut transaction = PostgresTransaction::begin(&self.pool).await?;
        transaction.insert_space_event_once(
            event_id,
            space_id,
            "agent.activity",
            record,
            OffsetDateTime::now_utc(),
        );
        transaction.commit().await.map(|_| ())
    }
}

impl PostgresAdapter {
    /// Records that a Computer's socket opened or closed, and tells its Space when the status
    /// actually changed. The status write and its `computer.changed` event commit together, so a
    /// Browser that refetches on the event reads the new status.
    pub(in crate::server::adapters) async fn set_computer_connection(
        &self,
        computer_id: Uuid,
        connection: ComputerConnection<'_>,
    ) -> Result<(), ApplicationError> {
        let now = OffsetDateTime::now_utc();
        let (status, daemon_version) = match connection {
            ComputerConnection::Online { daemon_version } => ("online", Some(daemon_version)),
            ComputerConnection::Offline => ("offline", None),
        };
        let mut transaction = PostgresTransaction::begin(&self.pool).await?;
        // The row lock orders concurrent writers, so each one compares against the status the
        // previous writer committed and exactly one of them reports a change.
        let changed = sqlx::query(
            "WITH previous AS (SELECT id,connection_status FROM computers WHERE id=$1 FOR UPDATE) \
             UPDATE computers c SET connection_status=$2,\
             daemon_version=COALESCE($3,c.daemon_version),\
             last_seen_at=CASE WHEN $2='online' THEN $4 ELSE c.last_seen_at END \
             FROM previous WHERE c.id=previous.id \
             RETURNING c.space_id,previous.connection_status<>$2 AS changed",
        )
        .bind(computer_id)
        .bind(status)
        .bind(daemon_version)
        .bind(now)
        .fetch_optional(&mut *transaction.connection)
        .await
        .map_err(map_sqlx)?;
        if let Some(row) = changed
            && row.get::<bool, _>("changed")
        {
            transaction
                .insert_space_event(
                    EventId::from_uuid(Uuid::now_v7()),
                    SpaceId::from_uuid(row.get("space_id")),
                    "computer.changed",
                    json!({"resource_id": computer_id, "status": status}),
                    now,
                )
                .await?;
        }
        transaction.commit().await.map(|_| ())
    }

    /// Marks every online Computer offline, with one `computer.changed` event each. Runs when this
    /// process stops, because it holds every Computer connection.
    pub(in crate::server::adapters) async fn disconnect_all_computers(
        &self,
    ) -> Result<(), ApplicationError> {
        let now = OffsetDateTime::now_utc();
        let mut transaction = PostgresTransaction::begin(&self.pool).await?;
        let rows = sqlx::query(
            "UPDATE computers SET connection_status='offline' \
             WHERE connection_status='online' RETURNING id,space_id",
        )
        .fetch_all(&mut *transaction.connection)
        .await
        .map_err(map_sqlx)?;
        for row in rows {
            transaction
                .insert_space_event(
                    EventId::from_uuid(Uuid::now_v7()),
                    SpaceId::from_uuid(row.get("space_id")),
                    "computer.changed",
                    json!({"resource_id": row.get::<Uuid, _>("id"), "status": "offline"}),
                    now,
                )
                .await?;
        }
        transaction.commit().await.map(|_| ())
    }
}

/// Socket state of a Computer as the Server sees it.
pub(in crate::server::adapters) enum ComputerConnection<'a> {
    Online { daemon_version: &'a str },
    Offline,
}

/// Derives the event ID of one Agent activity from the request that produced it. Only IDs in the
/// payload take part: labels such as a Channel slug may change between an attempt and its retry.
fn agent_activity_event_id(origin: Uuid, kind: &str, payload: &serde_json::Value) -> EventId {
    let mut hasher = Sha256::new();
    hasher.update(origin.as_bytes());
    hasher.update(kind.as_bytes());
    if let Some(object) = payload.as_object() {
        let mut ids: Vec<(&String, &str)> = object
            .iter()
            .filter(|(field, _)| field.ends_with("_id"))
            .filter_map(|(field, value)| value.as_str().map(|value| (field, value)))
            .collect();
        ids.sort();
        for (field, value) in ids {
            hasher.update([0]);
            hasher.update(field.as_bytes());
            hasher.update([0]);
            hasher.update(value.as_bytes());
        }
    }
    let digest = hasher.finalize();
    let mut bytes = [0_u8; 16];
    bytes.copy_from_slice(&digest[..16]);
    EventId::from_uuid(uuid::Builder::from_custom_bytes(bytes).into_uuid())
}

/// Decides whether one event may reach a viewer. An event that names no Channel and no Member
/// carries only a resource ID the viewer already reaches through an authorized read, so it passes.
fn event_is_visible(
    kind: &str,
    payload: &serde_json::Value,
    viewer_member_id: MemberId,
    governs_space: bool,
    readable_channels: &std::collections::HashSet<Uuid>,
) -> bool {
    let payload_uuid = |field: &str| {
        payload
            .get(field)
            .and_then(|value| value.as_str())
            .and_then(|value| value.parse::<Uuid>().ok())
    };
    let visibility_channel =
        payload_uuid("scope_channel_id").or_else(|| payload_uuid("channel_id"));
    if let Some(channel_id) = visibility_channel
        && !readable_channels.contains(&channel_id)
    {
        return false;
    }
    if kind != "agent.activity"
        && !(kind == "member.changed" && payload_uuid("channel_id").is_some())
        && let Some(member_id) = payload_uuid("member_id")
        && member_id != viewer_member_id.into_uuid()
        && !governs_space
    {
        return false;
    }
    true
}

impl TransactionPort for PostgresAdapter {
    type Transaction = PostgresTransaction;

    async fn transact<T>(
        &mut self,
        operation: impl for<'a> AsyncFnOnce(&'a mut Self::Transaction) -> Result<T, ApplicationError>,
    ) -> Result<T, ApplicationError> {
        let mut transaction = PostgresTransaction::begin(&self.pool)
            .await
            .map_err(|_| ApplicationError::Unavailable)?;
        match operation(&mut transaction).await {
            Ok(value) => {
                for computer_id in transaction.commit().await? {
                    self.commands.notify(computer_id);
                }
                Ok(value)
            }
            Err(error) => {
                transaction.rollback().await;
                Err(error)
            }
        }
    }
}

mod agent_graph;
mod attachment;
mod attention;
mod conversation;
mod execution;
mod identity;
mod query;
mod rows;
mod task;

pub(in crate::server::adapters) use agent_graph::*;
pub(in crate::server::adapters) use query::*;
use rows::*;

impl PostgresTransaction {
    async fn begin(pool: &PgPool) -> Result<Self, ApplicationError> {
        let mut transaction = Self {
            connection: pool.acquire().await.map_err(map_sqlx)?,
            effects: Vec::new(),
            events: Vec::new(),
            notified_computers: BTreeSet::new(),
        };
        sqlx::query("BEGIN")
            .execute(&mut *transaction.connection)
            .await
            .map_err(map_sqlx)?;
        Ok(transaction)
    }

    /// Queues one Browser event; [`Self::commit`] gives it the next commit position of its Space.
    async fn insert_space_event(
        &mut self,
        event_id: EventId,
        space_id: SpaceId,
        kind: &str,
        payload: serde_json::Value,
        created_at: OffsetDateTime,
    ) -> Result<(), ApplicationError> {
        self.queue_space_event(event_id, space_id, kind, payload, created_at, false);
        Ok(())
    }

    /// Same as [`Self::insert_space_event`], but a repeated `event_id` is a no-op instead of a
    /// conflict. Retried best-effort activity events keep their id and must not fail the retry; the
    /// consumed position stays unused, which readers only skip.
    fn insert_space_event_once(
        &mut self,
        event_id: EventId,
        space_id: SpaceId,
        kind: &str,
        payload: serde_json::Value,
        created_at: OffsetDateTime,
    ) {
        self.queue_space_event(event_id, space_id, kind, payload, created_at, true);
    }

    fn queue_space_event(
        &mut self,
        event_id: EventId,
        space_id: SpaceId,
        kind: &str,
        payload: serde_json::Value,
        created_at: OffsetDateTime,
        idempotent: bool,
    ) {
        self.events.push(PendingSpaceEvent {
            event_id,
            space_id,
            kind: kind.to_owned(),
            payload,
            created_at,
            idempotent,
        });
    }

    /// Writes effects and queued Space events, then commits, returning the Computers to wake.
    ///
    /// Each Space's `event_sequence` is bumped by a row lock this transaction keeps until it
    /// commits, so the allocation order is the commit order: a transaction that allocated a higher
    /// sequence cannot commit before one holding a lower sequence. That is why the stream can follow
    /// `sequence` alone, while `created_at` is only the time the application entered the write.
    /// The lock is taken here, after all other work, so concurrent writes to one Space serialize
    /// only on their commit, and Spaces are locked in ID order so two multi-Space commits cannot
    /// deadlock.
    async fn commit(mut self) -> Result<BTreeSet<Uuid>, ApplicationError> {
        let written = async {
            self.flush_effects().await?;
            self.write_space_events().await?;
            sqlx::query("COMMIT")
                .execute(&mut *self.connection)
                .await
                .map_err(map_sqlx)
        }
        .await;
        if let Err(error) = written {
            self.rollback().await;
            return Err(error);
        }
        Ok(std::mem::take(&mut self.notified_computers))
    }

    async fn write_space_events(&mut self) -> Result<(), ApplicationError> {
        let mut by_space = std::collections::BTreeMap::<Uuid, Vec<PendingSpaceEvent>>::new();
        for event in std::mem::take(&mut self.events) {
            by_space
                .entry(event.space_id.into_uuid())
                .or_default()
                .push(event);
        }
        for (space_id, events) in by_space {
            let count = i64::try_from(events.len()).map_err(|_| ApplicationError::Internal)?;
            let last: i64 = sqlx::query_scalar(
                "UPDATE spaces SET event_sequence=event_sequence+$2 WHERE id=$1 \
                 RETURNING event_sequence",
            )
            .bind(space_id)
            .bind(count)
            .fetch_one(&mut *self.connection)
            .await
            .map_err(map_sqlx)?;
            for (sequence, event) in (last - count + 1..).zip(events) {
                let conflict = if event.idempotent {
                    " ON CONFLICT (id) DO NOTHING"
                } else {
                    ""
                };
                sqlx::query(&format!(
                    "INSERT INTO outbox_events (id,space_id,kind,payload_json,created_at,sequence) \
                     VALUES ($1,$2,$3,$4,$5,$6){conflict}"
                ))
                .bind(event.event_id.into_uuid())
                .bind(space_id)
                .bind(&event.kind)
                .bind(event.payload)
                .bind(event.created_at)
                .bind(sequence)
                .execute(&mut *self.connection)
                .await
                .map_err(map_sqlx)?;
            }
        }
        Ok(())
    }

    async fn space_of_member(
        &mut self,
        member_id: MemberId,
    ) -> Result<Option<SpaceId>, ApplicationError> {
        Ok(
            sqlx::query_scalar::<_, Uuid>("SELECT space_id FROM members WHERE id=$1")
                .bind(member_id.into_uuid())
                .fetch_optional(&mut *self.connection)
                .await
                .map_err(map_sqlx)?
                .map(SpaceId::from_uuid),
        )
    }

    async fn lock_idempotency(
        &mut self,
        actor: MemberId,
        action: &str,
        key: IdempotencyKey,
    ) -> Result<(), ApplicationError> {
        let lock_key = format!("{}:{action}:{}", actor.into_uuid(), key.into_uuid());
        sqlx::query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))")
            .bind(lock_key)
            .execute(&mut *self.connection)
            .await
            .map_err(map_sqlx)?;
        Ok(())
    }

    async fn resource_for_idempotency(
        &mut self,
        actor: MemberId,
        action: &str,
        key: IdempotencyKey,
    ) -> Result<Option<Uuid>, ApplicationError> {
        sqlx::query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))")
            .bind(format!("{}:{action}:{}", actor, key))
            .execute(&mut *self.connection)
            .await
            .map_err(map_sqlx)?;
        sqlx::query_scalar(
            "SELECT resource_id FROM idempotency_records \
             WHERE actor_member_id=$1 AND action=$2 AND idempotency_key=$3",
        )
        .bind(actor.into_uuid())
        .bind(action)
        .bind(key.into_uuid())
        .fetch_optional(&mut *self.connection)
        .await
        .map_err(map_sqlx)
    }

    async fn record_resource_idempotency(
        &mut self,
        actor: MemberId,
        action: &str,
        key: IdempotencyKey,
        resource_id: Uuid,
    ) -> Result<(), ApplicationError> {
        let hash = Sha256::digest(resource_id.as_bytes());
        sqlx::query(
            "INSERT INTO idempotency_records (actor_member_id,action,idempotency_key,response_code, \
             resource_id,result_hash,created_at) VALUES ($1,$2,$3,'ok',$4,$5,now())",
        )
        .bind(actor.into_uuid())
        .bind(action)
        .bind(key.into_uuid())
        .bind(resource_id)
        .bind(hash.as_slice())
        .execute(&mut *self.connection)
        .await
        .map_err(map_sqlx)?;
        Ok(())
    }

    async fn rollback(&mut self) {
        let _ = sqlx::query("ROLLBACK").execute(&mut *self.connection).await;
    }

    async fn flush_effects(&mut self) -> Result<(), ApplicationError> {
        for effect in std::mem::take(&mut self.effects) {
            let (space_id, kind, payload) = self.effect_record(effect).await?;
            self.insert_space_event(
                EventId::from_uuid(Uuid::now_v7()),
                space_id,
                kind,
                payload,
                OffsetDateTime::now_utc(),
            )
            .await?;
        }
        Ok(())
    }

    async fn effect_record(
        &mut self,
        effect: Effect,
    ) -> Result<(SpaceId, &'static str, serde_json::Value), ApplicationError> {
        let (kind, subject_id) = match effect {
            Effect::MessageCreated(id) => ("message.created", id.into_uuid()),
            Effect::MessageUpdated(id) => ("message.updated", id.into_uuid()),
            Effect::MessageDeleted(id) => ("message.deleted", id.into_uuid()),
            Effect::TaskCreated(id) => ("task.created", id.into_uuid()),
            Effect::RunTaskBound { run_id, task_id } => {
                let computer_id = self.computer_for_run(run_id).await?;
                let task = self.task_snapshot(task_id).await?;
                self.queue_command(
                    computer_id,
                    Command::RunTaskBound(RunTaskBound { run_id, task }),
                )
                .await?;
                return Ok((
                    self.space_for_run(run_id).await?,
                    "run.task_bound",
                    json!({"run_id": run_id, "task_id": task_id}),
                ));
            }
            Effect::ThreadLinked { task_id, thread_id } => {
                return Ok((
                    self.space_for_task(task_id).await?,
                    "task.linked",
                    json!({"task_id": task_id, "thread_id": thread_id}),
                ));
            }
            Effect::ThreadUnlinked { task_id, thread_id } => {
                return Ok((
                    self.space_for_task(task_id).await?,
                    "task.unlinked",
                    json!({"task_id": task_id, "thread_id": thread_id}),
                ));
            }
            Effect::ItemAttached {
                run_id,
                item_id,
                sequence,
            } => {
                let computer_id = self.computer_for_run(run_id).await?;
                let item = self.inbox_snapshot(item_id).await?;
                self.queue_command(
                    computer_id,
                    Command::RunAttachItem(RunAttachItem {
                        run_id,
                        delivery_sequence: DeliverySequence(sequence),
                        item,
                    }),
                )
                .await?;
                return Ok((
                    self.space_for_run(run_id).await?,
                    "run.item_attached",
                    json!({"run_id": run_id, "item_id": item_id, "delivery_sequence": sequence}),
                ));
            }
            Effect::RunNotice {
                run_id,
                item_id,
                location_visible,
            } => {
                let computer_id = self.computer_for_run(run_id).await?;
                let notice = self.attention_notice(item_id, location_visible).await?;
                self.queue_command(
                    computer_id,
                    Command::RunNotice(RunNotice { run_id, notice }),
                )
                .await?;
                return Ok((
                    self.space_for_run(run_id).await?,
                    "run.notice",
                    json!({"run_id": run_id, "notice_id": item_id}),
                ));
            }
            Effect::RunDispatched(run_id) => {
                let computer_id = self.computer_for_run(run_id).await?;
                let command = self.run_start(run_id).await?;
                self.record_run_start_observations(&command).await?;
                self.queue_command(computer_id, Command::RunStart(command))
                    .await?;
                ("run.changed", run_id.into_uuid())
            }
            Effect::RunCancelRequested(run_id) => {
                let computer_id = self.computer_for_run(run_id).await?;
                self.queue_command(
                    computer_id,
                    Command::RunStop(RunStop {
                        run_id,
                        reason: StopReason::HumanRequest,
                    }),
                )
                .await?;
                ("run.changed", run_id.into_uuid())
            }
            Effect::RunStarted(id) => ("run.changed", id.into_uuid()),
            Effect::RunCompleted(id) => ("run.changed", id.into_uuid()),
            Effect::TaskCompleted {
                task_id,
                result_message_id,
            } => {
                return Ok((
                    self.space_for_task(task_id).await?,
                    "task.finished",
                    json!({"task_id": task_id, "result_message_id": result_message_id}),
                ));
            }
            Effect::TaskFinished(id) => ("task.finished", id.into_uuid()),
            Effect::SessionClose(id) => {
                if let Some((agent_id, computer_id)) = self.task_assignment(id).await? {
                    self.queue_command(
                        computer_id,
                        Command::SessionClose(SessionCommand {
                            agent_id: AgentId::from_uuid(agent_id.into_uuid()),
                            scope: SessionScope::Task(id),
                            reason: SessionChangeReason::TaskFinished,
                        }),
                    )
                    .await?;
                }
                ("session.close", id.into_uuid())
            }
            Effect::SessionReset(id) => {
                if let Some((agent_id, computer_id)) = self.task_assignment(id).await? {
                    self.queue_command(
                        computer_id,
                        Command::SessionReset(SessionCommand {
                            agent_id: AgentId::from_uuid(agent_id.into_uuid()),
                            scope: SessionScope::Task(id),
                            reason: SessionChangeReason::ExplicitReset,
                        }),
                    )
                    .await?;
                }
                ("session.reset", id.into_uuid())
            }
            Effect::AgentRetired {
                agent_id,
                computer_id,
            } => {
                self.queue_command(
                    computer_id,
                    Command::AgentRetire(AgentRetire {
                        agent_id: AgentId::from_uuid(agent_id.into_uuid()),
                    }),
                )
                .await?;
                ("agent.changed", agent_id.into_uuid())
            }
            Effect::ComputerDeleted(id) => ("computer.changed", id.into_uuid()),
            Effect::TaskUpdated(id) => ("task.updated", id.into_uuid()),
            Effect::ChannelCreated(id) => ("channel.created", id.into_uuid()),
            Effect::ChannelUpdated(id) => ("channel.updated", id.into_uuid()),
            Effect::AgentUpdated(id) => ("agent.updated", id.into_uuid()),
            Effect::AgentCreated {
                agent_id,
                computer_id,
            } => {
                let configuration = self.agent_configuration(agent_id).await?;
                self.queue_command(computer_id, Command::AgentProvision(configuration))
                    .await?;
                ("agent.created", agent_id.into_uuid())
            }
            Effect::PermissionChanged(id) => ("member.changed", id.into_uuid()),
            Effect::InboxChanged(member_id) => {
                let space_id =
                    sqlx::query_scalar::<_, Uuid>("SELECT space_id FROM members WHERE id=$1")
                        .bind(member_id.into_uuid())
                        .fetch_one(&mut *self.connection)
                        .await
                        .map_err(map_sqlx)?;
                return Ok((
                    SpaceId::from_uuid(space_id),
                    "inbox.changed",
                    json!({"member_id": member_id}),
                ));
            }
            Effect::ThreadUpdated(thread_id) => {
                let row = sqlx::query(
                    "SELECT space_id,channel_id FROM messages \
                     WHERE id=$1 AND placement='root'",
                )
                .bind(thread_id.into_uuid())
                .fetch_one(&mut *self.connection)
                .await
                .map_err(map_sqlx)?;
                return Ok((
                    SpaceId::from_uuid(row.get("space_id")),
                    "thread.updated",
                    json!({
                        "resource_id": thread_id,
                        "channel_id": row.get::<Uuid, _>("channel_id"),
                    }),
                ));
            }
        };
        if kind.starts_with("message.") {
            // The Thread lets a Browser refresh only the Root Message and Thread the change is in.
            let row = sqlx::query("SELECT space_id,channel_id,thread_id FROM messages WHERE id=$1")
                .bind(subject_id)
                .fetch_one(&mut *self.connection)
                .await
                .map_err(map_sqlx)?;
            return Ok((
                SpaceId::from_uuid(row.get("space_id")),
                kind,
                json!({
                    "resource_id": subject_id,
                    "channel_id": row.get::<Uuid, _>("channel_id"),
                    "thread_id": row.get::<Uuid, _>("thread_id"),
                }),
            ));
        }
        let space_id = self.space_for_subject(kind, subject_id).await?;
        Ok((space_id, kind, json!({"resource_id": subject_id})))
    }

    async fn space_for_run(&mut self, id: RunId) -> Result<SpaceId, ApplicationError> {
        self.space_by_query(
            "SELECT space_id FROM agent_runs WHERE id=$1",
            id.into_uuid(),
        )
        .await
    }

    async fn space_for_task(&mut self, id: TaskId) -> Result<SpaceId, ApplicationError> {
        self.space_by_query("SELECT space_id FROM tasks WHERE id=$1", id.into_uuid())
            .await
    }

    async fn space_for_subject(
        &mut self,
        kind: &str,
        id: Uuid,
    ) -> Result<SpaceId, ApplicationError> {
        let query =
            if kind.starts_with("task.") || matches!(kind, "session.close" | "session.reset") {
                "SELECT space_id FROM tasks WHERE id=$1"
            } else if kind.starts_with("message.") {
                "SELECT space_id FROM messages WHERE id=$1"
            } else if kind.starts_with("run.") {
                "SELECT space_id FROM agent_runs WHERE id=$1"
            } else if kind.starts_with("agent.") {
                "SELECT space_id FROM agents WHERE member_id=$1"
            } else if kind.starts_with("member.") {
                "SELECT space_id FROM members WHERE id=$1"
            } else if kind.starts_with("computer.") {
                "SELECT space_id FROM computers WHERE id=$1"
            } else {
                "SELECT space_id FROM channels WHERE id=$1"
            };
        self.space_by_query(query, id).await
    }

    async fn space_by_query(&mut self, query: &str, id: Uuid) -> Result<SpaceId, ApplicationError> {
        sqlx::query_scalar::<_, Uuid>(query)
            .bind(id)
            .fetch_one(&mut *self.connection)
            .await
            .map(SpaceId::from_uuid)
            .map_err(map_sqlx)
    }
}

async fn optional_uuid(
    connection: &mut PoolConnection<Postgres>,
    query: &str,
    id: Uuid,
) -> Result<Option<Uuid>, ApplicationError> {
    sqlx::query_scalar(query)
        .bind(id)
        .fetch_optional(&mut **connection)
        .await
        .map_err(map_sqlx)
}

/// `sequence` is a database `BIGINT`; a cursor that no row can carry fails closed.
fn sequence_bind(cursor: super::realtime::EventCursor) -> Result<i64, ApplicationError> {
    i64::try_from(cursor.sequence()).map_err(|_| ApplicationError::Internal)
}

fn map_sqlx(error: sqlx::Error) -> ApplicationError {
    match &error {
        sqlx::Error::RowNotFound => ApplicationError::NotFound,
        sqlx::Error::Database(database)
            if matches!(
                database.code().as_deref(),
                Some("23503" | "23505" | "23514")
            ) =>
        {
            ApplicationError::Conflict
        }
        sqlx::Error::PoolTimedOut | sqlx::Error::PoolClosed | sqlx::Error::Io(_) => {
            ApplicationError::Unavailable
        }
        _ => ApplicationError::Internal,
    }
}

#[cfg(test)]
mod tests;
#[async_trait]
impl EffectSink for PostgresTransaction {
    async fn queue_agent_suspend(
        &mut self,
        agent_id: MemberId,
        computer_id: Option<ComputerId>,
        cancel_current_run: bool,
    ) -> Result<(), ApplicationError> {
        self.queue_agent_suspend(agent_id, computer_id, cancel_current_run)
            .await
    }
    async fn queue_agent_resume(
        &mut self,
        agent_id: MemberId,
        computer_id: Option<ComputerId>,
    ) -> Result<(), ApplicationError> {
        self.queue_agent_resume(agent_id, computer_id).await
    }
    async fn queue_agent_restart(
        &mut self,
        agent_id: MemberId,
        computer_id: Option<ComputerId>,
    ) -> Result<(), ApplicationError> {
        self.queue_agent_restart(agent_id, computer_id).await
    }
    async fn queue_agent_configuration(&mut self, agent: &Agent) -> Result<(), ApplicationError> {
        self.queue_agent_configuration(agent).await
    }
    async fn lock_idempotency(
        &mut self,
        actor: MemberId,
        action: &str,
        key: IdempotencyKey,
    ) -> Result<(), ApplicationError> {
        self.lock_idempotency(actor, action, key).await
    }
    async fn resource_for_idempotency(
        &mut self,
        actor: MemberId,
        action: &str,
        key: IdempotencyKey,
    ) -> Result<Option<uuid::Uuid>, ApplicationError> {
        self.resource_for_idempotency(actor, action, key).await
    }
    async fn insert_inbox_item(&mut self, item: InboxItem) -> Result<(), ApplicationError> {
        self.insert_inbox_item(item).await
    }
    async fn save_inbox_item(&mut self, item: InboxItem) -> Result<(), ApplicationError> {
        self.save_inbox_item(item).await
    }
    async fn insert_dead_item_notice(
        &mut self,
        agent_id: MemberId,
        thread_id: ThreadId,
        error_code: &'static str,
        now: time::OffsetDateTime,
    ) -> Result<InboxItemId, ApplicationError> {
        self.insert_dead_item_notice(agent_id, thread_id, error_code, now)
            .await
    }
    async fn record_completed_run_event(
        &mut self,
        event_id: EventId,
        run_id: RunId,
    ) -> Result<(), ApplicationError> {
        self.record_completed_run_event(event_id, run_id).await
    }
    async fn record_task_idempotency(
        &mut self,
        actor: MemberId,
        action: &str,
        key: IdempotencyKey,
        task_id: TaskId,
    ) -> Result<(), ApplicationError> {
        self.record_task_idempotency(actor, action, key, task_id)
            .await
    }
    async fn record_resource_idempotency(
        &mut self,
        actor: MemberId,
        action: &str,
        key: IdempotencyKey,
        resource_id: uuid::Uuid,
    ) -> Result<(), ApplicationError> {
        self.record_resource_idempotency(actor, action, key, resource_id)
            .await
    }
    async fn record_task_audit(
        &mut self,
        actor: MemberId,
        action: &str,
        task_id: TaskId,
        now: time::OffsetDateTime,
    ) -> Result<(), ApplicationError> {
        self.record_task_audit(actor, action, task_id, now).await
    }
    async fn record_inbox_item_audit(
        &mut self,
        actor: MemberId,
        action: &str,
        item_id: InboxItemId,
        now: time::OffsetDateTime,
    ) -> Result<(), ApplicationError> {
        self.record_inbox_item_audit(actor, action, item_id, now)
            .await
    }
    fn emit(&mut self, effect: Effect) {
        self.effects.push(effect);
    }
}
