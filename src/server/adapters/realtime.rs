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
        Some(event_id) => Some(
            state
                .storage
                .event_cursor(space_id, event_id)
                .await
                .map_err(application_error)?
                .ok_or_else(ApiError::context_changed)?,
        ),
        None => None,
    };
    let initial = state
        .storage
        .browser_events(space_id, viewer, cursor)
        .await
        .map_err(application_error)?;
    let cursor = initial
        .next
        .or_else(|| initial.events.last().map(|event| event.cursor))
        .or(cursor);
    let events = stream::unfold(
        (
            state.storage,
            space_id,
            viewer,
            cursor,
            VecDeque::from(initial.events),
            wakeups,
        ),
        move |(storage, space_id, viewer, mut cursor, mut buffered, mut wakeups)| async move {
            loop {
                if let Some(event) = buffered.pop_front() {
                    cursor = Some(cursor.map_or(event.cursor, |cursor| cursor.max(event.cursor)));
                    let event = event.into_sse().ok()?;
                    return Some((
                        Ok(event),
                        (storage, space_id, viewer, cursor, buffered, wakeups),
                    ));
                }
                super::space_events::wait_for_space(&mut wakeups, space_id.into_uuid()).await;
                let page = match storage.browser_events(space_id, viewer, cursor).await {
                    Ok(page) => page,
                    Err(_) => return None,
                };
                cursor = page
                    .next
                    .or_else(|| page.events.last().map(|event| event.cursor))
                    .or(cursor);
                buffered = VecDeque::from(page.events);
            }
        },
    );
    let events = futures_util::StreamExt::take_until(
        events,
        Box::pin(async move {
            shutdown.stopped().await;
        }),
    );
    Ok(Sse::new(events).keep_alive(KeepAlive::default()))
}
