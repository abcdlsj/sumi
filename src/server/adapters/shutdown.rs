use std::sync::Arc;

use tokio::sync::watch;

/// Tells long-lived connections the Server is stopping.
///
/// Graceful shutdown waits for every open connection, and Computer sockets and Browser event
/// streams never end on their own, so each one must observe this signal and close itself.
#[derive(Clone)]
pub(super) struct ShutdownSignal {
    sender: Arc<watch::Sender<bool>>,
}

impl Default for ShutdownSignal {
    fn default() -> Self {
        Self {
            sender: Arc::new(watch::channel(false).0),
        }
    }
}

impl ShutdownSignal {
    pub(super) fn trigger(&self) {
        self.sender.send_replace(true);
    }

    pub(super) async fn stopped(&self) {
        let mut receiver = self.sender.subscribe();
        // The sender lives in `self`, so the wait cannot end with a closed channel.
        let _ = receiver.wait_for(|stopped| *stopped).await;
    }
}
