use serde::Serialize;
use time::OffsetDateTime;

use crate::ids::{EventId, SpaceId};

/// Position of an event in the commit order of its Space.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Ord, PartialOrd)]
pub(super) struct EventCursor(u64);

impl EventCursor {
    pub(super) fn new(sequence: u64) -> Self {
        Self(sequence)
    }

    pub(super) fn sequence(self) -> u64 {
        self.0
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub(super) struct BrowserEvent<T> {
    pub(super) event_id: EventId,
    #[serde(rename = "type")]
    pub(super) event_type: String,
    pub(super) space_id: SpaceId,
    #[serde(with = "time::serde::rfc3339")]
    pub(super) occurred_at: OffsetDateTime,
    pub(super) data: T,
    /// Where this event sits in the Space commit order. Not sent to the Browser: an SSE event is
    /// identified by `event_id`, and a reconnect resolves that id back to a cursor on the Server.
    #[serde(skip)]
    pub(super) cursor: EventCursor,
}

/// One read of the Space event stream.
pub(super) struct BrowserEventPage<T> {
    /// Rows the viewer may see, in commit order.
    pub(super) events: Vec<BrowserEvent<T>>,
    /// Cursor after the last scanned row, present only when the read filled a whole page. Invisible
    /// rows advance it too, so a page of private events cannot pin the stream in place.
    pub(super) next: Option<EventCursor>,
}

impl<T: Serialize> BrowserEvent<T> {
    pub(super) fn into_sse(self) -> Result<axum::response::sse::Event, axum::Error> {
        let id = self.event_id.to_string();
        let event_type = self.event_type.clone();
        axum::response::sse::Event::default()
            .id(id)
            .event(event_type)
            .json_data(self)
    }
}

use super::http::{ApiError, RuntimeState, application_error, current_member};
use super::space_events::Wake;
use crate::ids::MemberId;
use axum::{
    extract::{Path, State},
    http::HeaderMap,
    response::{
        Sse,
        sse::{Event as SseEvent, KeepAlive},
    },
};
use axum_extra::extract::cookie::CookieJar;
use futures_util::stream;
use std::{collections::VecDeque, convert::Infallible};
use tokio::sync::broadcast;
use uuid::Uuid;

pub(super) async fn space_events(
    State(state): State<RuntimeState>,
    jar: CookieJar,
    headers: HeaderMap,
    Path(space_id): Path<Uuid>,
) -> Result<Sse<impl futures_util::Stream<Item = Result<SseEvent, Infallible>>>, ApiError> {
    let viewer = current_member(&state, &jar, space_id).await?;
    let space_id = SpaceId::from_uuid(space_id);
    let last_event_id = headers
        .get("last-event-id")
        .map(|value| {
            value
                .to_str()
                .ok()
                .and_then(|value| value.parse().ok())
                .ok_or_else(|| ApiError::invalid("Last-Event-ID is invalid"))
        })
        .transpose()?;
    let viewer = MemberId::from_uuid(viewer);
    let shutdown = state.shutdown.clone();
    // Subscribe before the first read so a commit between the read and the wait still wakes us.
    let wakeups = state.space_events.subscribe();
    let cursor = match last_event_id {
        // A reconnect continues right after the event the Browser already handled.
        Some(event_id) => state
            .storage
            .event_cursor(space_id, event_id)
            .await
            .map_err(application_error)?
            .ok_or_else(ApiError::context_changed)?,
        // A first connection starts at the current tail: the Browser rebuilds history from a
        // projection query, so replaying it through the stream only delays live events.
        None => state
            .storage
            .space_event_tail(space_id)
            .await
            .map_err(application_error)?,
    };
    let events = stream::unfold(
        SpaceEventFeed {
            storage: state.storage,
            space_id,
            viewer,
            cursor,
            buffered: VecDeque::new(),
            paging: true,
            wakeups,
        },
        SpaceEventFeed::next_event,
    );
    let events = futures_util::StreamExt::take_until(
        events,
        Box::pin(async move {
            shutdown.stopped().await;
        }),
    );
    Ok(Sse::new(events).keep_alive(KeepAlive::default()))
}

/// Reads the Space stream page by page for one Browser connection.
struct SpaceEventFeed {
    storage: super::postgres::PostgresAdapter,
    space_id: SpaceId,
    viewer: MemberId,
    /// Position of the last row this stream has handled.
    cursor: EventCursor,
    buffered: VecDeque<BrowserEvent<serde_json::Value>>,
    /// A full page means more rows may already be committed, so the next read runs before any
    /// wait: a page of invisible events must not need a wake-up to advance.
    paging: bool,
    wakeups: broadcast::Receiver<Wake>,
}

impl SpaceEventFeed {
    async fn next_event(mut self) -> Option<(Result<SseEvent, Infallible>, Self)> {
        loop {
            if let Some(event) = self.buffered.pop_front() {
                // A full page already moved the cursor past all of its rows, so it never goes back.
                self.cursor = self.cursor.max(event.cursor);
                let event = event.into_sse().ok()?;
                return Some((Ok(event), self));
            }
            if !self.paging {
                super::space_events::wait_for_space(&mut self.wakeups, self.space_id.into_uuid())
                    .await;
            }
            let page = self
                .storage
                .browser_events(self.space_id, self.viewer, Some(self.cursor))
                .await
                .ok()?;
            self.paging = page.next.is_some();
            // An invisible tail still advances the cursor, so re-reading after a wake-up cannot
            // resend an event the Browser already has.
            self.cursor = page.next.unwrap_or_else(|| {
                page.events
                    .last()
                    .map(|event| event.cursor)
                    .unwrap_or(self.cursor)
            });
            self.buffered = VecDeque::from(page.events);
        }
    }
}
