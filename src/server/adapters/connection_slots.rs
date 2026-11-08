//! One live channel per Computer, shared by the command and query registries.
//!
//! Both registries persist their real state elsewhere (PostgreSQL for commands, the request itself
//! for queries) and only keep a wake-up channel per connected Computer. A reconnect replaces the
//! channel, so a stale disconnect must never remove the newer one.

use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
};

use tokio::sync::mpsc;
use uuid::Uuid;

#[derive(Clone)]
pub(super) struct ConnectionSlots<T> {
    state: Arc<Mutex<SlotsState<T>>>,
}

impl<T> Default for ConnectionSlots<T> {
    fn default() -> Self {
        Self {
            state: Arc::new(Mutex::new(SlotsState::default())),
        }
    }
}

struct SlotsState<T> {
    next_connection: u64,
    computers: HashMap<Uuid, Slot<T>>,
}

impl<T> Default for SlotsState<T> {
    fn default() -> Self {
        Self {
            next_connection: 0,
            computers: HashMap::new(),
        }
    }
}

struct Slot<T> {
    id: u64,
    sender: mpsc::UnboundedSender<T>,
}

/// Identifies the channel a connection owns, so a later connection can supersede it.
pub(super) struct ConnectionHandle {
    computer_id: Uuid,
    id: u64,
}

impl<T: Send + 'static> ConnectionSlots<T> {
    pub(super) fn connect(
        &self,
        computer_id: Uuid,
    ) -> (ConnectionHandle, mpsc::UnboundedReceiver<T>) {
        let (sender, receiver) = mpsc::unbounded_channel();
        let mut state = self.lock();
        state.next_connection += 1;
        let id = state.next_connection;
        state.computers.insert(computer_id, Slot { id, sender });
        (ConnectionHandle { computer_id, id }, receiver)
    }

    pub(super) fn disconnect(&self, handle: ConnectionHandle) {
        let mut state = self.lock();
        if state
            .computers
            .get(&handle.computer_id)
            .is_some_and(|slot| slot.id == handle.id)
        {
            state.computers.remove(&handle.computer_id);
        }
    }

    pub(super) fn is_connected(&self, computer_id: Uuid) -> bool {
        self.lock().computers.contains_key(&computer_id)
    }

    pub(super) fn sender(&self, computer_id: Uuid) -> Option<mpsc::UnboundedSender<T>> {
        self.lock()
            .computers
            .get(&computer_id)
            .map(|slot| slot.sender.clone())
    }

    fn lock(&self) -> std::sync::MutexGuard<'_, SlotsState<T>> {
        self.state
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn a_stale_handle_never_removes_the_newest_connection() {
        let slots = ConnectionSlots::<()>::default();
        let computer = Uuid::now_v7();
        let (old_handle, mut old_receiver) = slots.connect(computer);
        let (new_handle, mut new_receiver) = slots.connect(computer);

        slots.disconnect(old_handle);
        assert!(slots.is_connected(computer));
        slots
            .sender(computer)
            .expect("the newest connection is still registered")
            .send(())
            .expect("the newest connection receives");
        assert_eq!(new_receiver.recv().await, Some(()));
        assert!(old_receiver.try_recv().is_err());

        slots.disconnect(new_handle);
        assert!(!slots.is_connected(computer));
        assert!(slots.sender(computer).is_none());
    }
}
