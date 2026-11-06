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
        {
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
            self.state
                .lock()
                .expect("query registry lock")
                .pending
                .insert(query_id, sender);
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
