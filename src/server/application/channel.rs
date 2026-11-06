use std::collections::BTreeSet;

use time::OffsetDateTime;

use crate::ids::{ChannelId, IdempotencyKey, MemberId, MessageId, RunId, SpaceId, ThreadId};
use crate::server::domain::{
    conversation::{Channel, ChannelKind, Message, MessageContent},
    identity::PermissionAction,
};

use super::ports::{
    ApplicationError, CollaborationTransaction, Effect, EffectSink, ExecutionTransaction,
    IdentityTransaction, TransactionPort,
};

pub(in crate::server) struct ArchiveChannel;

impl ArchiveChannel {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        actor_member_id: MemberId,
        channel_id: ChannelId,
        idempotency_key: IdempotencyKey,
        now: OffsetDateTime,
    ) -> Result<Channel, ApplicationError> {
        port.transact(async |transaction| {
            if super::replayed(
                transaction,
                actor_member_id,
                "channel.archive",
                idempotency_key,
                channel_id.into_uuid(),
            )
            .await?
            {
                return transaction.channel(channel_id).await;
            }
            let mut channel = transaction.channel(channel_id).await?;
            let access = transaction
                .member_access_level(actor_member_id, channel.space_id)
                .await?;
            if !access.can_manage_space() {
                return Err(ApplicationError::PermissionDenied);
            }
            channel.archive(now)?;
            transaction.save_channel(channel.clone()).await?;
            transaction.emit(Effect::ChannelUpdated(channel.id));
            Ok(channel)
        })
        .await
    }
}

pub(in crate::server) struct JoinChannel;

impl JoinChannel {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        actor_member_id: MemberId,
        channel_id: ChannelId,
        idempotency_key: IdempotencyKey,
        now: OffsetDateTime,
    ) -> Result<Channel, ApplicationError> {
        port.transact(async |transaction| {
            if super::replayed(
                transaction,
                actor_member_id,
                "channel.join",
                idempotency_key,
                channel_id.into_uuid(),
            )
            .await?
            {
                return transaction.channel(channel_id).await;
            }
            let mut channel = transaction.channel(channel_id).await?;
            transaction
                .member_access_level(actor_member_id, channel.space_id)
                .await?;
            channel.admit(actor_member_id)?;
            transaction
                .join_channel(actor_member_id, channel_id, now)
                .await?;
            transaction.emit(Effect::ChannelUpdated(channel.id));
            Ok(channel)
        })
        .await
    }
}

pub(in crate::server) struct SetThreadSubscription;

impl SetThreadSubscription {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        actor_member_id: MemberId,
        thread_id: ThreadId,
        following: bool,
        idempotency_key: IdempotencyKey,
        now: OffsetDateTime,
    ) -> Result<bool, ApplicationError> {
        let action = if following {
            "thread.follow"
        } else {
            "thread.unfollow"
        };
        port.transact(async |transaction| {
            if super::replayed(
                transaction,
                actor_member_id,
                action,
                idempotency_key,
                thread_id.into_uuid(),
            )
            .await?
            {
                return Ok(following);
            }
            if !transaction
                .can_read_thread(actor_member_id, thread_id)
                .await?
            {
                return Err(ApplicationError::NotFound);
            }
            transaction
                .set_thread_subscription(thread_id, actor_member_id, following, now)
                .await?;
            Ok(following)
        })
        .await
    }
}

pub(in crate::server) struct CreateChannelActionInput {
    pub(in crate::server) channel_id: ChannelId,
    pub(in crate::server) audience: BTreeSet<MemberId>,
    pub(in crate::server) kind: ChannelKind,
    pub(in crate::server) slug: Option<String>,
    pub(in crate::server) topic: Option<String>,
    pub(in crate::server) action_message_id: MessageId,
    pub(in crate::server) actor_member_id: MemberId,
    pub(in crate::server) idempotency_key: IdempotencyKey,
    pub(in crate::server) current_run_id: RunId,
    pub(in crate::server) now: OffsetDateTime,
}

pub(in crate::server) struct CreateChannelInput {
    pub(in crate::server) channel_id: ChannelId,
    pub(in crate::server) space_id: SpaceId,
    pub(in crate::server) audience: BTreeSet<MemberId>,
    pub(in crate::server) kind: ChannelKind,
    pub(in crate::server) slug: Option<String>,
    pub(in crate::server) topic: Option<String>,
    pub(in crate::server) actor_member_id: MemberId,
    pub(in crate::server) idempotency_key: IdempotencyKey,
    pub(in crate::server) now: OffsetDateTime,
}

pub(in crate::server) struct CreateChannel;

impl CreateChannel {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        input: CreateChannelInput,
    ) -> Result<Channel, ApplicationError> {
        port.transact(async |transaction| {
            if let Some(channel_id) = transaction
                .resource_for_idempotency(
                    input.actor_member_id,
                    "channel.create",
                    input.idempotency_key,
                )
                .await?
            {
                return transaction.channel(ChannelId::from_uuid(channel_id)).await;
            }
            let access = transaction
                .member_access_level(input.actor_member_id, input.space_id)
                .await?;
            if !access.can_manage_space() || !input.audience.contains(&input.actor_member_id) {
                return Err(ApplicationError::PermissionDenied);
            }
            let channel = Channel::create(
                input.channel_id,
                input.space_id,
                input.audience,
                input.kind,
                input.slug,
                input.topic,
                input.now,
            )?;
            transaction.insert_channel(channel.clone()).await?;
            transaction
                .record_resource_idempotency(
                    input.actor_member_id,
                    "channel.create",
                    input.idempotency_key,
                    channel.id.into_uuid(),
                )
                .await?;
            transaction.emit(Effect::ChannelCreated(channel.id));
            Ok(channel)
        })
        .await
    }
}

pub(in crate::server) struct CreateChannelAction;

impl CreateChannelAction {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        input: CreateChannelActionInput,
    ) -> Result<Channel, ApplicationError> {
        port.transact(async |transaction| {
            if let Some(channel_id) = transaction
                .resource_for_idempotency(
                    input.actor_member_id,
                    "channel.create",
                    input.idempotency_key,
                )
                .await?
            {
                return transaction.channel(ChannelId::from_uuid(channel_id)).await;
            }
            let run = transaction.run(input.current_run_id).await?;
            run.require_working_for(input.actor_member_id)
                .map_err(ApplicationError::Domain)?;
            let run_view = run.view();
            if !transaction
                .has_permission(input.actor_member_id, PermissionAction::ChannelCreate)
                .await?
                || !transaction
                    .can_read_thread(input.actor_member_id, run_view.focus_thread_id)
                    .await?
            {
                return Err(ApplicationError::PermissionDenied);
            }
            let audience = if input.audience.is_empty() {
                transaction
                    .channel_action_audience(
                        run_view.focus_thread_id,
                        run_view.space_id,
                        input.kind == ChannelKind::Private,
                    )
                    .await?
            } else {
                input.audience
            };
            let channel = Channel::create(
                input.channel_id,
                run_view.space_id,
                audience,
                input.kind,
                input.slug,
                input.topic,
                input.now,
            )?;
            let action = Message::action_reply(
                input.action_message_id,
                run_view.focus_thread_id,
                input.actor_member_id,
                MessageContent::ChannelCreated(channel.id),
                input.now,
            )?;
            transaction.insert_channel(channel.clone()).await?;
            transaction.insert_message(action).await?;
            transaction
                .record_resource_idempotency(
                    input.actor_member_id,
                    "channel.create",
                    input.idempotency_key,
                    channel.id.into_uuid(),
                )
                .await?;
            transaction.emit(Effect::ChannelCreated(channel.id));
            Ok(channel)
        })
        .await
    }
}

pub(in crate::server) struct AddChannelAgents;

impl AddChannelAgents {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        actor: MemberId,
        channel_id: ChannelId,
        agent_ids: Vec<MemberId>,
        idempotency_key: IdempotencyKey,
        now: OffsetDateTime,
    ) -> Result<Vec<MemberId>, ApplicationError> {
        port.transact(async |transaction| {
            transaction
                .add_channel_agents(actor, channel_id, agent_ids, idempotency_key, now)
                .await
        })
        .await
    }
}

pub(in crate::server) struct RemoveChannelAgent;

impl RemoveChannelAgent {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        actor: MemberId,
        channel_id: ChannelId,
        agent_id: MemberId,
        idempotency_key: IdempotencyKey,
        now: OffsetDateTime,
    ) -> Result<(), ApplicationError> {
        port.transact(async |transaction| {
            if super::replayed(
                transaction,
                actor,
                "channel.remove",
                idempotency_key,
                agent_id.into_uuid(),
            )
            .await?
            {
                return Ok(());
            }
            transaction
                .remove_channel_agent(actor, channel_id, agent_id, now)
                .await
        })
        .await
    }
}

pub(in crate::server) struct InviteChannelMember;

impl InviteChannelMember {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        actor: MemberId,
        channel_id: ChannelId,
        member_id: MemberId,
        idempotency_key: IdempotencyKey,
        now: OffsetDateTime,
    ) -> Result<bool, ApplicationError> {
        apply_channel_membership(
            port,
            "channel.invite",
            PermissionAction::ChannelInvite,
            ChannelMembership::Invite,
            ChannelMembershipWrite {
                actor,
                channel_id,
                member_id,
                idempotency_key,
                now,
            },
        )
        .await
    }
}

pub(in crate::server) struct RemoveChannelMember;

impl RemoveChannelMember {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        actor: MemberId,
        channel_id: ChannelId,
        member_id: MemberId,
        idempotency_key: IdempotencyKey,
        now: OffsetDateTime,
    ) -> Result<bool, ApplicationError> {
        apply_channel_membership(
            port,
            "channel.remove",
            PermissionAction::ChannelRemove,
            ChannelMembership::Remove,
            ChannelMembershipWrite {
                actor,
                channel_id,
                member_id,
                idempotency_key,
                now,
            },
        )
        .await
    }
}

/// Invite and remove differ only in the permission, the idempotency action and the write itself.
struct ChannelMembershipWrite {
    actor: MemberId,
    channel_id: ChannelId,
    member_id: MemberId,
    idempotency_key: IdempotencyKey,
    now: OffsetDateTime,
}

async fn apply_channel_membership<P: TransactionPort>(
    port: &mut P,
    action: &str,
    permission: PermissionAction,
    membership: ChannelMembership,
    write: ChannelMembershipWrite,
) -> Result<bool, ApplicationError> {
    let ChannelMembershipWrite {
        actor,
        channel_id,
        member_id,
        idempotency_key,
        now,
    } = write;
    port.transact(async |transaction| {
        if let Some(resource_id) = transaction
            .resource_for_idempotency(actor, action, idempotency_key)
            .await?
        {
            if resource_id != channel_id.into_uuid() {
                return Err(ApplicationError::Conflict);
            }
            return Ok(false);
        }
        let channel = transaction.channel(channel_id).await?;
        transaction
            .member_access_level(actor, channel.space_id)
            .await?;
        if !transaction.has_permission(actor, permission).await? {
            return Err(ApplicationError::PermissionDenied);
        }
        match membership {
            ChannelMembership::Invite => {
                transaction
                    .invite_channel_member(actor, channel_id, member_id, idempotency_key, now)
                    .await
            }
            ChannelMembership::Remove => {
                transaction
                    .remove_channel_member(actor, channel_id, member_id, idempotency_key, now)
                    .await
            }
        }
    })
    .await
}

#[derive(Clone, Copy)]
enum ChannelMembership {
    Invite,
    Remove,
}

pub(in crate::server) struct LeaveChannel;

impl LeaveChannel {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        agent_id: MemberId,
        channel_id: ChannelId,
        idempotency_key: IdempotencyKey,
        now: OffsetDateTime,
    ) -> Result<(), ApplicationError> {
        port.transact(async |transaction| {
            transaction
                .leave_channel(agent_id, channel_id, idempotency_key, now)
                .await
        })
        .await
    }
}
