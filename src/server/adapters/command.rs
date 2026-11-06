use tokio::sync::mpsc;
use uuid::Uuid;

use super::connection_slots::{ConnectionHandle, ConnectionSlots};

/// In-process wake-up registry for queued Computer commands.
///
/// The Server persists commands in PostgreSQL, then pushes them over the Computer WebSocket. This
/// registry lets the transaction that committed a command wake the online connection immediately
/// instead of waiting for the next heartbeat. It is a delivery optimization only: the database
/// remains the source of truth, and reconnect/heartbeat replay still guarantees delivery.
#[derive(Clone, Default)]
pub(super) struct CommandRegistry {
    slots: ConnectionSlots<()>,
}

pub(super) struct CommandConnection(ConnectionHandle);

impl CommandRegistry {
    pub(super) fn connect(
        &self,
        computer_id: Uuid,
    ) -> (CommandConnection, mpsc::UnboundedReceiver<()>) {
        let (handle, receiver) = self.slots.connect(computer_id);
        (CommandConnection(handle), receiver)
    }

    pub(super) fn disconnect(&self, connection: CommandConnection) {
        self.slots.disconnect(connection.0);
    }

    pub(super) fn is_connected(&self, computer_id: Uuid) -> bool {
        self.slots.is_connected(computer_id)
    }

    pub(super) fn notify(&self, computer_id: Uuid) {
        if let Some(sender) = self.slots.sender(computer_id) {
            let _ = sender.send(());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn notify_wakes_only_the_connected_computer() {
        let registry = CommandRegistry::default();
        let computer = Uuid::now_v7();
        let (_connection, mut receiver) = registry.connect(computer);

        registry.notify(computer);
        assert!(
            tokio::time::timeout(std::time::Duration::from_millis(100), receiver.recv())
                .await
                .is_ok_and(|wakeup| wakeup == Some(()))
        );

        registry.notify(Uuid::now_v7());
        assert!(
            tokio::time::timeout(std::time::Duration::from_millis(50), receiver.recv())
                .await
                .is_err()
        );
    }

    #[tokio::test]
    async fn stale_disconnect_keeps_the_newest_connection() {
        let registry = CommandRegistry::default();
        let computer = Uuid::now_v7();
        let (old_connection, mut old_receiver) = registry.connect(computer);
        let (_new_connection, mut new_receiver) = registry.connect(computer);

        registry.disconnect(old_connection);
        registry.notify(computer);

        assert!(
            tokio::time::timeout(std::time::Duration::from_millis(100), old_receiver.recv())
                .await
                .is_ok_and(|wakeup| wakeup.is_none())
        );
        assert!(
            tokio::time::timeout(std::time::Duration::from_millis(100), new_receiver.recv())
                .await
                .is_ok_and(|wakeup| wakeup == Some(()))
        );
    }
}
