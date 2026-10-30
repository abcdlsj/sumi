use time::OffsetDateTime;

use crate::ids::{InboxItemId, MemberId, SpaceId};

use crate::server::domain::{
    attention::{AttentionStrength, InboxItem, InboxItemStatus},
    execution::{Run, RunStatus},
    task::TaskStatus,
};

use super::ports::{
    ApplicationError, CollaborationTransaction, Effect, EffectSink, ExecutionTransaction,
    IdentityTransaction, InboxItemView, InboxScope, MemberKind, ServerTransaction, TransactionPort,
};

pub(in crate::server) struct ReadMemberInbox;

impl ReadMemberInbox {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        actor_id: MemberId,
        target_id: MemberId,
        space_id: SpaceId,
        scope: InboxScope,
    ) -> Result<Vec<InboxItemView>, ApplicationError> {
        port.transact(async |transaction| {
            if actor_id != target_id {
                let target = transaction
                    .space_member(target_id, space_id)
                    .await?
                    .ok_or(ApplicationError::NotFound)?;
                if target.kind != MemberKind::Agent {
                    return Err(ApplicationError::PermissionDenied);
                }
                let access = transaction.member_access_level(actor_id, space_id).await?;
                if !access.can_manage_space() {
                    return Err(ApplicationError::PermissionDenied);
                }
            }
            transaction.inbox_for_member(target_id, scope).await
        })
        .await
    }
}

pub(in crate::server) struct RequeueDeadItemInput {
    pub(in crate::server) item_id: InboxItemId,
    pub(in crate::server) actor_id: MemberId,
    pub(in crate::server) now: time::OffsetDateTime,
}

/// Returns a retired Item to the queue. The Item belongs to an Agent, so this authorizes the actor as
/// a governor of that Agent's Space rather than as the Item's owner.
pub(in crate::server) struct RequeueDeadItem;

impl RequeueDeadItem {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        input: RequeueDeadItemInput,
    ) -> Result<InboxItemView, ApplicationError> {
        port.transact(async |transaction| {
            let mut item = transaction.inbox_item(input.item_id).await?;
            let item_view = item.view();
            let access = transaction
                .member_access_level(input.actor_id, item_view.space_id)
                .await?;
            if !access.can_manage_space() {
                return Err(ApplicationError::PermissionDenied);
            }
            item.requeue_from_dead(input.now)?;
            let member_id = item_view.member_id;
            let source_message_id = item_view.message_id;
            transaction.save_inbox_item(item).await?;
            transaction
                .record_inbox_item_audit(
                    input.actor_id,
                    "inbox_item.requeued",
                    input.item_id,
                    input.now,
                )
                .await?;
            // The Item is claimable again, so both the Agent's queue and the source Message's run
            // state projection are stale.
            transaction.emit(Effect::InboxChanged(member_id));
            if let Some(message_id) = source_message_id {
                transaction.emit(Effect::MessageUpdated(message_id));
            }
            transaction.inbox_item_view(input.item_id).await
        })
        .await
    }
}

pub(in crate::server) struct MarkInboxItemReadInput {
    pub(in crate::server) item_id: InboxItemId,
    pub(in crate::server) actor_id: MemberId,
    pub(in crate::server) now: time::OffsetDateTime,
}

pub(in crate::server) struct MarkAllInboxReadInput {
    pub(in crate::server) actor_id: MemberId,
    pub(in crate::server) target_id: MemberId,
    pub(in crate::server) space_id: SpaceId,
    pub(in crate::server) now: time::OffsetDateTime,
}

/// Marks a Human-owned Item handled on its owner's explicit read. Agent Items never enter this
/// path: their terminal state belongs to the Run that leased them, so this command rejects them.
/// Reading an already handled Item is idempotent and returns the current projection.
pub(in crate::server) struct MarkInboxItemRead;

impl MarkInboxItemRead {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        input: MarkInboxItemReadInput,
    ) -> Result<InboxItemView, ApplicationError> {
        port.transact(async |transaction| {
            let mut item = transaction.inbox_item(input.item_id).await?;
            let item_view = item.view();
            if input.actor_id != item_view.member_id {
                return Err(ApplicationError::PermissionDenied);
            }
            let owner = transaction
                .space_member(item_view.member_id, item_view.space_id)
                .await?
                .ok_or(ApplicationError::NotFound)?;
            if owner.kind != MemberKind::Human {
                return Err(ApplicationError::PermissionDenied);
            }
            item.mark_read(input.now)?;
            transaction.save_inbox_item(item).await?;
            transaction.emit(Effect::InboxChanged(item_view.member_id));
            transaction.inbox_item_view(input.item_id).await
        })
        .await
    }
}

/// Marks every pending Item owned by the Human target handled in one transaction. Only pending
/// Items enter this path: assigned, deferred, and dead Items keep their status, matching the single
/// Item read command. Returns the number of Items marked.
pub(in crate::server) struct MarkAllInboxRead;

impl MarkAllInboxRead {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        input: MarkAllInboxReadInput,
    ) -> Result<u32, ApplicationError> {
        port.transact(async |transaction| {
            if input.actor_id != input.target_id {
                return Err(ApplicationError::PermissionDenied);
            }
            let owner = transaction
                .space_member(input.target_id, input.space_id)
                .await?
                .ok_or(ApplicationError::NotFound)?;
            if owner.kind != MemberKind::Human {
                return Err(ApplicationError::PermissionDenied);
            }
            let items = transaction
                .pending_inbox_items_for_member(input.target_id, input.space_id)
                .await?;
            let mut marked = 0u32;
            for mut item in items {
                item.mark_read(input.now)?;
                transaction.save_inbox_item(item).await?;
                marked += 1;
            }
            if marked > 0 {
                transaction.emit(Effect::InboxChanged(input.target_id));
            }
            Ok(marked)
        })
        .await
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(in crate::server) enum HardItemRoute {
    Pending,
    Attached { sequence: u64 },
    Notice,
}

pub(in crate::server) struct RouteHardItemInput {
    pub(in crate::server) item_id: InboxItemId,
    pub(in crate::server) now: OffsetDateTime,
}

pub(in crate::server) struct RouteHardItem;

impl RouteHardItem {
    pub(in crate::server) async fn execute<P: TransactionPort>(
        port: &mut P,
        input: RouteHardItemInput,
    ) -> Result<HardItemRoute, ApplicationError> {
        port.transact(async |transaction| {
            let item = transaction.inbox_item(input.item_id).await?;
            let item_view = item.view();
            if item_view.strength != AttentionStrength::Hard
                || item_view.status != InboxItemStatus::Pending
            {
                return Ok(HardItemRoute::Pending);
            }
            let (item_id, thread_id) = (item_view.id, item_view.thread_id);
            let Some(run_id) = transaction
                .active_run_for_agent(item_view.member_id)
                .await?
            else {
                return Ok(HardItemRoute::Pending);
            };
            let mut run = transaction.run(run_id).await?;
            let run_view = run.view();
            let (run_id, agent_id) = (run_view.id, run_view.agent_id);
            if run_view.status != RunStatus::Working {
                return Ok(HardItemRoute::Pending);
            }
            if let Some(sequence) =
                attach_to_working_run(transaction, &mut run, item, input.now).await?
            {
                return Ok(HardItemRoute::Attached { sequence });
            }
            let location_visible = transaction.can_read_thread(agent_id, thread_id).await?;
            transaction.emit(Effect::RunNotice {
                run_id,
                item_id,
                location_visible,
            });
            Ok(HardItemRoute::Notice)
        })
        .await
    }
}

/// Attaches a pending hard Item to the Agent's working Run on the same Focus Thread, so one
/// conversation is answered by one Run. When only one side carries a Task, the other side is
/// bound to it first. Returns `None` when Focus or Task scope cannot be merged.
pub(in crate::server) async fn attach_to_working_run<T: ServerTransaction>(
    transaction: &mut T,
    run: &mut Run,
    mut item: InboxItem,
    now: OffsetDateTime,
) -> Result<Option<u64>, ApplicationError> {
    let run_view = run.view();
    let item_view = item.view();
    if item_view.strength != AttentionStrength::Hard
        || item_view.status != InboxItemStatus::Pending
        || run_view.status != RunStatus::Working
        || run_view.agent_id != item_view.member_id
        || run_view.focus_thread_id != item_view.thread_id
    {
        return Ok(None);
    }
    let (run_id, agent_id) = (run_view.id, run_view.agent_id);
    match (run_view.task_id, item_view.task_id) {
        (Some(run_task), Some(item_task)) if run_task != item_task => return Ok(None),
        (Some(run_task), None) => item.bind_task(run_task)?,
        (None, Some(item_task)) => {
            let mut task = transaction.task(item_task).await?;
            if run.bind_task(&task).is_err() {
                return Ok(None);
            }
            for run_item in run.items() {
                let mut bound = transaction.inbox_item(run_item.inbox_item_id).await?;
                bound.bind_task(item_task)?;
                transaction.save_inbox_item(bound).await?;
            }
            transaction.emit(Effect::RunTaskBound {
                run_id,
                task_id: item_task,
            });
            let task_view = task.view();
            if task_view.status == TaskStatus::Todo
                && task_view.assignee_agent_member_id == Some(agent_id)
            {
                task.start(agent_id, now)?;
                transaction.save_task(task).await?;
                transaction.emit(Effect::TaskUpdated(item_task));
            }
        }
        _ => {}
    }
    let item_id = item.view().id;
    let sequence = run.attach(&item)?;
    item.attach_to_active_run(run_id)?;
    transaction.save_run(run.clone()).await?;
    transaction.save_inbox_item(item).await?;
    transaction.emit(Effect::ItemAttached {
        run_id,
        item_id,
        sequence,
    });
    Ok(Some(sequence))
}
