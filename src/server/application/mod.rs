pub(in crate::server) mod agent_graph;
pub(in crate::server) mod attachment;
pub(in crate::server) mod attention;
pub(in crate::server) mod computer;
pub(in crate::server) mod conversation;
pub(in crate::server) mod execution;
pub(in crate::server) mod identity;
pub(in crate::server) mod invitation;
pub(in crate::server) mod ports;
pub(in crate::server) mod task;

#[cfg(test)]
mod tests;

use crate::ids::{IdempotencyKey, MemberId};
use ports::{ApplicationError, EffectSink};

/// Claims one Browser write for its idempotency key.
///
/// Returns `true` when the key already applied `action` to `resource`, so the caller answers with
/// the current state instead of writing again. A key reused for another resource is a conflict.
/// A new key is recorded in the caller's transaction, so it commits or rolls back with the write.
pub(in crate::server) async fn replayed<T: EffectSink>(
    transaction: &mut T,
    actor: MemberId,
    action: &str,
    key: IdempotencyKey,
    resource: uuid::Uuid,
) -> Result<bool, ApplicationError> {
    match transaction
        .resource_for_idempotency(actor, action, key)
        .await?
    {
        Some(recorded) if recorded == resource => Ok(true),
        Some(_) => Err(ApplicationError::Conflict),
        None => {
            transaction
                .record_resource_idempotency(actor, action, key, resource)
                .await?;
            Ok(false)
        }
    }
}
