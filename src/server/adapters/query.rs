use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
};

use tokio::sync::{mpsc, oneshot};
use uuid::Uuid;

use super::connection_slots::{ConnectionHandle, ConnectionSlots};
use crate::{
    ids::QueryId,
    protocol::computer::{
        Query, QueryEnvelope, QueryErrorCode, QueryResult, QueryResultEnvelope, ServerFrame,
    },
};

const QUERY_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(2);

#[derive(Clone, Default)]
pub(super) struct QueryRegistry {
    state: Arc<Mutex<RegistryState>>,
    slots: ConnectionSlots<ServerFrame>,
}

#[derive(Default)]
struct RegistryState {
    pending: HashMap<QueryId, oneshot::Sender<QueryResult>>,
}

impl QueryRegistry {
    pub(super) fn connect(
        &self,
        computer_id: Uuid,
    ) -> (ConnectionHandle, mpsc::UnboundedReceiver<ServerFrame>) {
        self.slots.connect(computer_id)
    }

    pub(super) fn disconnect(&self, handle: ConnectionHandle) {
        self.slots.disconnect(handle);
    }

    pub(super) async fn ask(&self, computer_id: Uuid, query: Query) -> QueryResult {
        let query_id = QueryId::from_uuid(Uuid::now_v7());
        let (sender, receiver) = oneshot::channel();
        // Register before sending: the Computer can answer on the same connection before this
        // task resumes, and a reply that arrives without a registration would be dropped.
        self.state
            .lock()
            .expect("query registry lock")
            .pending
            .insert(query_id, sender);
        let Some(frames) = self.slots.sender(computer_id) else {
            self.state
                .lock()
                .expect("query registry lock")
                .pending
                .remove(&query_id);
            return unreachable();
        };
        if frames
            .send(ServerFrame::Query {
                query: QueryEnvelope { query_id, query },
            })
            .is_err()
        {
            self.state
                .lock()
                .expect("query registry lock")
                .pending
                .remove(&query_id);
            return unreachable();
        }
        match tokio::time::timeout(QUERY_TIMEOUT, receiver).await {
            Ok(Ok(result)) => result,
            Ok(Err(_)) | Err(_) => {
                self.state
                    .lock()
                    .expect("query registry lock")
                    .pending
                    .remove(&query_id);
                unreachable()
            }
        }
    }

    pub(super) fn resolve(&self, envelope: QueryResultEnvelope) {
        let waiting = self
            .state
            .lock()
            .expect("query registry lock")
            .pending
            .remove(&envelope.query_id);
        if let Some(waiting) = waiting {
            let _ = waiting.send(envelope.result);
        }
    }
}

fn unreachable() -> QueryResult {
    QueryResult::Unavailable {
        code: QueryErrorCode::Unreachable,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::computer::{MemoryListResult, MemoryQuery};

    #[tokio::test]
    async fn a_reply_that_arrives_before_the_caller_awaits_is_not_lost() {
        let registry = QueryRegistry::default();
        let computer = Uuid::now_v7();
        let (handle, mut frames) = registry.connect(computer);
        let responder = registry.clone();
        let computer_side = tokio::spawn(async move {
            let Some(ServerFrame::Query { query }) = frames.recv().await else {
                return;
            };
            responder.resolve(QueryResultEnvelope {
                query_id: query.query_id,
                result: QueryResult::MemoryList(MemoryListResult { files: Vec::new() }),
            });
        });

        let result = registry
            .ask(
                computer,
                Query::MemoryList(MemoryQuery {
                    agent_id: crate::ids::AgentId::from_uuid(Uuid::now_v7()),
                }),
            )
            .await;

        assert!(
            matches!(result, QueryResult::MemoryList(_)),
            "an immediate reply must reach the caller, not time out: {result:?}"
        );
        computer_side.await.unwrap();
        registry.disconnect(handle);
    }
}
