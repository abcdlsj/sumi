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
        // Whatever ends the wait (an unreachable Computer, a send failure, the timeout, or the
        // caller dropping the future) must take the registration with it.
        let _registration = PendingQuery {
            state: self.state.clone(),
            query_id,
        };
        let Some(frames) = self.slots.sender(computer_id) else {
            return unreachable();
        };
        if frames
            .send(ServerFrame::Query {
                query: QueryEnvelope { query_id, query },
            })
            .is_err()
        {
            return unreachable();
        }
        match tokio::time::timeout(QUERY_TIMEOUT, receiver).await {
            Ok(Ok(result)) => result,
            Ok(Err(_)) | Err(_) => unreachable(),
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

/// Removes a query's registration when its caller stops waiting, so a cancelled `ask` leaves no
/// entry behind for a later reply to fill.
struct PendingQuery {
    state: Arc<Mutex<RegistryState>>,
    query_id: QueryId,
}

impl Drop for PendingQuery {
    fn drop(&mut self) {
        self.state
            .lock()
            .expect("query registry lock")
            .pending
            .remove(&self.query_id);
    }
}

fn unreachable() -> QueryResult {
    QueryResult::Unavailable {
        code: QueryErrorCode::Unreachable,
    }
}

#[cfg(test)]
mod tests {
    use std::{
        future::Future,
        task::{Context, Poll, Waker},
    };

    use super::*;
    use crate::protocol::computer::{MemoryListResult, MemoryQuery};

    fn pending(ask: &mut std::pin::Pin<&mut impl Future<Output = QueryResult>>) -> bool {
        let mut context = Context::from_waker(Waker::noop());
        matches!(ask.as_mut().poll(&mut context), Poll::Pending)
    }

    fn memory_list() -> Query {
        Query::MemoryList(MemoryQuery {
            agent_id: crate::ids::AgentId::from_uuid(Uuid::now_v7()),
        })
    }

    #[tokio::test]
    async fn a_reply_that_arrives_before_the_caller_checks_again_is_not_lost() {
        let registry = QueryRegistry::default();
        let computer = Uuid::now_v7();
        let (handle, mut frames) = registry.connect(computer);

        let mut ask = Box::pin(registry.ask(computer, memory_list()));
        // The first poll registers the query and writes the frame, then waits for the reply. The
        // Computer answers before the caller polls again, the way a socket read can deliver a reply
        // between two polls of the waiting task.
        assert!(pending(&mut ask.as_mut()));
        let Some(ServerFrame::Query { query }) = frames.try_recv().ok() else {
            panic!("the query must reach the Computer");
        };
        // The registration is in place before the frame is observable, so a reply can never arrive
        // without somewhere to go.
        assert!(
            registry
                .state
                .lock()
                .unwrap()
                .pending
                .contains_key(&query.query_id)
        );
        registry.resolve(QueryResultEnvelope {
            query_id: query.query_id,
            result: QueryResult::MemoryList(MemoryListResult { files: Vec::new() }),
        });

        assert!(matches!(ask.await, QueryResult::MemoryList(_)));
        assert!(
            registry.state.lock().unwrap().pending.is_empty(),
            "a resolved query leaves no registration behind"
        );
        registry.disconnect(handle);
    }

    #[tokio::test]
    async fn dropping_a_waiting_ask_removes_its_registration() {
        let registry = QueryRegistry::default();
        let computer = Uuid::now_v7();
        let (handle, _frames) = registry.connect(computer);

        let mut ask = Box::pin(registry.ask(computer, memory_list()));
        assert!(pending(&mut ask.as_mut()));
        assert_eq!(registry.state.lock().unwrap().pending.len(), 1);

        drop(ask);

        assert!(
            registry.state.lock().unwrap().pending.is_empty(),
            "a cancelled query must not stay registered"
        );
        registry.disconnect(handle);
    }
}
