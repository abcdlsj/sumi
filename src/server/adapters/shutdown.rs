use std::sync::Arc;

use tokio::sync::watch;

/// Tells long-lived connections the Server is stopping.
///
/// Graceful shutdown waits for every open HTTP request, and Computer sockets and Browser event
/// streams never end on their own, so each one must observe this signal and close itself. Upgraded
/// sockets are invisible to that wait, so they also report themselves here and shutdown waits for
/// them to finish closing before the process exits.
#[derive(Clone)]
pub(super) struct ShutdownSignal {
    sender: Arc<watch::Sender<bool>>,
    sockets: Arc<watch::Sender<usize>>,
}

impl Default for ShutdownSignal {
    fn default() -> Self {
        Self {
            sender: Arc::new(watch::channel(false).0),
            sockets: Arc::new(watch::channel(0).0),
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

    /// Counts an upgraded socket as open until the returned guard is dropped.
    ///
    /// A socket holds this guard until it has written its last frame and closed, so shutdown can
    /// tell a socket that is still draining from one that never existed.
    pub(super) fn socket(&self) -> OpenSocket {
        self.sockets.send_modify(|open| *open += 1);
        OpenSocket {
            sockets: self.sockets.clone(),
        }
    }

    /// Waits for every counted socket to finish closing, or until `limit` passes.
    ///
    /// Bounded because one socket that ignores the signal must not hold the process open.
    pub(super) async fn sockets_closed(&self, limit: std::time::Duration) {
        let mut receiver = self.sockets.subscribe();
        let _ = tokio::time::timeout(limit, receiver.wait_for(|open| *open == 0)).await;
    }
}

pub(super) struct OpenSocket {
    sockets: Arc<watch::Sender<usize>>,
}

impl Drop for OpenSocket {
    fn drop(&mut self) {
        self.sockets
            .send_modify(|open| *open = open.saturating_sub(1));
    }
}
