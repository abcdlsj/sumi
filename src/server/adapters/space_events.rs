use std::time::Duration;

use sqlx::{PgPool, postgres::PgListener};
use tokio::sync::broadcast;
use uuid::Uuid;

/// PostgreSQL channel the `outbox_events` insert trigger notifies with the Space ID.
const CHANNEL: &str = "sumi_space_events";

/// How long an idle Browser event stream waits before re-reading without a wake-up.
///
/// The wake-up is a latency optimization only: `outbox_events` stays the source of truth, so a
/// notification lost while the listener reconnects delays an event by at most this long.
pub(super) const FALLBACK_POLL: Duration = Duration::from_secs(30);

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(super) enum Wake {
    Space(Uuid),
    /// The listener (re)connected and may have missed notifications.
    All,
}

/// Fans committed Space event notifications out to Browser event streams in this process.
#[derive(Clone)]
pub(super) struct SpaceEventSignal {
    sender: broadcast::Sender<Wake>,
}

impl Default for SpaceEventSignal {
    fn default() -> Self {
        Self {
            sender: broadcast::channel(1024).0,
        }
    }
}

impl SpaceEventSignal {
    pub(super) fn subscribe(&self) -> broadcast::Receiver<Wake> {
        self.sender.subscribe()
    }

    pub(super) async fn listen_forever(self, pool: PgPool) {
        loop {
            if let Err(error) = self.listen(&pool).await {
                tracing::warn!(%error, "Space event listener disconnected; reconnecting");
            }
            tokio::time::sleep(Duration::from_secs(1)).await;
        }
    }

    async fn listen(&self, pool: &PgPool) -> Result<(), sqlx::Error> {
        let mut listener = PgListener::connect_with(pool).await?;
        listener.listen(CHANNEL).await?;
        let _ = self.sender.send(Wake::All);
        loop {
            match listener.try_recv().await? {
                Some(notification) => {
                    if let Ok(space_id) = notification.payload().parse() {
                        let _ = self.sender.send(Wake::Space(space_id));
                    }
                }
                // The connection dropped; the next receive reconnects and re-listens.
                None => {
                    let _ = self.sender.send(Wake::All);
                }
            }
        }
    }
}

/// Waits until `space_id` may have new events, or until the fallback poll is due.
pub(super) async fn wait_for_space(receiver: &mut broadcast::Receiver<Wake>, space_id: Uuid) {
    let woken = async {
        loop {
            match receiver.recv().await {
                Ok(Wake::Space(id)) if id != space_id => continue,
                Ok(_) | Err(broadcast::error::RecvError::Lagged(_)) => return,
                Err(broadcast::error::RecvError::Closed) => std::future::pending().await,
            }
        }
    };
    let _ = tokio::time::timeout(FALLBACK_POLL, woken).await;
    // A burst of commits needs one re-read, not one per notification.
    while !matches!(
        receiver.try_recv(),
        Err(broadcast::error::TryRecvError::Empty | broadcast::error::TryRecvError::Closed)
    ) {}
}
