use time::OffsetDateTime;

use crate::ids::{ChannelId, IdempotencyKey, MemberId, MessageId, RunId, SpaceId};
use crate::server::domain::conversation::{Channel, ChannelKind, Message};

use super::ports::{
    ApplicationError, AttachmentTransaction, CollaborationTransaction, DirectMessageView, Effect,
    EffectSink, ExecutionTransaction, IdentityTransaction, MessageDraft, ObservationScope,
    PublishOutcome, PublishedMessage, TransactionPort,
};

pub(in crate::server) struct ListDirectMessages;

impl ListDirectMessages {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        member_id: MemberId,
        space_id: SpaceId,
    ) -> Result<Vec<DirectMessageView>, ApplicationError> {
        port.transact(async |transaction| {
            transaction
                .direct_messages_for_member(member_id, space_id)
                .await
        })
        .await
    }
}

pub(in crate::server) struct OpenDirectMessage;

pub(in crate::server) struct OpenDirectMessageInput {
    pub(in crate::server) channel_id: ChannelId,
    pub(in crate::server) space_id: SpaceId,
    pub(in crate::server) actor_member_id: MemberId,
    pub(in crate::server) other_member_id: MemberId,
    pub(in crate::server) idempotency_key: IdempotencyKey,
    pub(in crate::server) now: OffsetDateTime,
}

pub(in crate::server) struct OpenedDirectMessage {
    pub(in crate::server) view: DirectMessageView,
    pub(in crate::server) created: bool,
}

impl OpenDirectMessage {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        input: OpenDirectMessageInput,
    ) -> Result<OpenedDirectMessage, ApplicationError> {
        if input.actor_member_id == input.other_member_id {
            return Err(ApplicationError::Conflict);
        }
        port.transact(async |transaction| {
            // The pair, not a channel ID, names the DM: a replay finds the DM the first call opened.
            super::replayed(
                transaction,
                input.actor_member_id,
                "dm.open",
                input.idempotency_key,
                input.other_member_id.into_uuid(),
            )
            .await?;
            let other = transaction
                .space_member(input.other_member_id, input.space_id)
                .await?
                .ok_or(ApplicationError::NotFound)?;
            if let Some(existing) = transaction
                .direct_message_between(
                    input.space_id,
                    input.actor_member_id,
                    input.other_member_id,
                )
                .await?
            {
                return Ok(OpenedDirectMessage {
                    view: existing,
                    created: false,
                });
            }
            let channel = Channel::create(
                input.channel_id,
                input.space_id,
                [input.actor_member_id, input.other_member_id]
                    .into_iter()
                    .collect(),
                ChannelKind::Direct,
                None,
                None,
                input.now,
            )?;
            transaction.insert_channel(channel.clone()).await?;
            transaction.emit(Effect::ChannelCreated(channel.id));
            Ok(OpenedDirectMessage {
                view: DirectMessageView {
                    channel_id: channel.id,
                    space_id: channel.space_id,
                    other_member: other,
                    created_at: channel.created_at,
                },
                created: true,
            })
        })
        .await
    }
}

/// Consecutive holds one Run accepts for one target before the next write commits unchecked.
pub(in crate::server) const MAX_CONSECUTIVE_HOLDS: u32 = 5;
/// Unobserved Messages returned by one hold; the observed position advances only past these.
pub(in crate::server) const HELD_MESSAGE_LIMIT: u32 = 20;

pub(in crate::server) struct PublishMessage;

impl PublishMessage {
    /// `observer` is the Run writing on the Agent's behalf. A Run write is held while the target
    /// has other Members' Messages the Run has not observed.
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        draft: MessageDraft,
        observer: Option<RunId>,
    ) -> Result<PublishOutcome, ApplicationError> {
        if draft.body_markdown.trim().is_empty() {
            return Err(ApplicationError::Conflict);
        }
        port.transact(async |transaction| {
            if let Some(message_id) = transaction
                .resource_for_idempotency(
                    draft.author_member_id,
                    "message.create",
                    draft.idempotency_key,
                )
                .await?
            {
                return Ok(PublishOutcome::Published(PublishedMessage {
                    message_id: MessageId::from_uuid(message_id),
                    hard_item_ids: Vec::new(),
                    notified_member_ids: Vec::new(),
                }));
            }
            let scope = match draft.thread_id {
                Some(thread_id) => ObservationScope::Thread(thread_id),
                None => ObservationScope::ChannelRoots(draft.channel_id),
            };
            if let Some(run_id) = observer {
                // The check and the write share one transaction under the Channel lock, so no
                // Message can land between them.
                let unobserved = transaction
                    .unobserved_messages(run_id, draft.author_member_id, scope, HELD_MESSAGE_LIMIT)
                    .await?;
                if let Some(unobserved) = unobserved
                    && let Some(last) = unobserved.messages.last()
                    && unobserved.consecutive_holds < MAX_CONSECUTIVE_HOLDS
                {
                    transaction
                        .record_hold(run_id, scope, last.sequence)
                        .await?;
                    return Ok(PublishOutcome::Held(unobserved.messages));
                }
            }
            if !draft.attachment_ids.is_empty() {
                let channel = transaction.channel(draft.channel_id).await?;
                for attachment_id in &draft.attachment_ids {
                    let attachment = transaction
                        .attachment(*attachment_id)
                        .await?
                        .ok_or(ApplicationError::NotFound)?;
                    if attachment.view().space_id != channel.space_id {
                        return Err(ApplicationError::NotFound);
                    }
                    attachment
                        .require_ready()
                        .map_err(|_| ApplicationError::Conflict)?;
                }
            }
            let actor = draft.author_member_id;
            let key = draft.idempotency_key;
            // A Root Message creates its Thread, so only a reply changes an existing one.
            let replied_thread_id = draft.thread_id;
            let published = transaction.publish_message(draft).await?;
            transaction
                .record_resource_idempotency(
                    actor,
                    "message.create",
                    key,
                    published.message_id.into_uuid(),
                )
                .await?;
            transaction.emit(Effect::MessageCreated(published.message_id));
            if let Some(thread_id) = replied_thread_id {
                transaction.emit(Effect::ThreadUpdated(thread_id));
            }
            for member_id in &published.notified_member_ids {
                transaction.emit(Effect::InboxChanged(*member_id));
            }
            if let Some(run_id) = observer {
                transaction.clear_holds(run_id, scope).await?;
            }
            Ok(PublishOutcome::Published(published))
        })
        .await
    }
}

pub(in crate::server) struct EditMessageInput {
    pub(in crate::server) message_id: MessageId,
    pub(in crate::server) actor_member_id: MemberId,
    pub(in crate::server) body_markdown: String,
    pub(in crate::server) mentions: Vec<MemberId>,
    pub(in crate::server) mention_all: bool,
    pub(in crate::server) idempotency_key: IdempotencyKey,
    pub(in crate::server) now: OffsetDateTime,
}

pub(in crate::server) struct EditMessage;

impl EditMessage {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        input: EditMessageInput,
    ) -> Result<Message, ApplicationError> {
        port.transact(async |transaction| {
            if let Some(message_id) = transaction
                .resource_for_idempotency(
                    input.actor_member_id,
                    "message.edit",
                    input.idempotency_key,
                )
                .await?
            {
                return transaction.message(MessageId::from_uuid(message_id)).await;
            }
            let mut message = transaction.message(input.message_id).await?;
            let thread = transaction.thread(message.thread_id).await?;
            let access = transaction
                .member_access_level(input.actor_member_id, thread.space_id)
                .await?;
            if input.actor_member_id != message.author_member_id && !access.can_manage_space() {
                return Err(ApplicationError::PermissionDenied);
            }
            message.edit_text(input.body_markdown, input.now)?;
            transaction.save_message(message.clone()).await?;
            transaction
                .save_message_mentions(message.id, input.mentions, input.mention_all, input.now)
                .await?;
            transaction
                .record_resource_idempotency(
                    input.actor_member_id,
                    "message.edit",
                    input.idempotency_key,
                    message.id.into_uuid(),
                )
                .await?;
            transaction.emit(Effect::MessageUpdated(message.id));
            Ok(message)
        })
        .await
    }
}

pub(in crate::server) struct DeleteMessage;

impl DeleteMessage {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        message_id: MessageId,
        actor_member_id: MemberId,
        idempotency_key: IdempotencyKey,
        now: OffsetDateTime,
    ) -> Result<Message, ApplicationError> {
        port.transact(async |transaction| {
            if let Some(message_id) = transaction
                .resource_for_idempotency(actor_member_id, "message.delete", idempotency_key)
                .await?
            {
                return transaction.message(MessageId::from_uuid(message_id)).await;
            }
            let mut message = transaction.message(message_id).await?;
            let thread = transaction.thread(message.thread_id).await?;
            let access = transaction
                .member_access_level(actor_member_id, thread.space_id)
                .await?;
            if actor_member_id != message.author_member_id && !access.can_manage_space() {
                return Err(ApplicationError::PermissionDenied);
            }
            message.soft_delete(now)?;
            transaction.save_message(message.clone()).await?;
            transaction
                .record_resource_idempotency(
                    actor_member_id,
                    "message.delete",
                    idempotency_key,
                    message.id.into_uuid(),
                )
                .await?;
            transaction.emit(Effect::MessageDeleted(message.id));
            Ok(message)
        })
        .await
    }
}
