use super::{
    http,
    object_storage::AttachmentObjectStore,
    postgres::{PostgresAdapter, PostgresQueries},
    query::QueryRegistry,
};
use crate::config::ServerConfig;
use crate::server::domain::access::SessionLifetime;
use anyhow::Context;
use sqlx::postgres::PgPoolOptions;
use time::OffsetDateTime;
use tower_http::{
    services::{ServeDir, ServeFile},
    trace::TraceLayer,
};

pub(in crate::server) async fn run(config: ServerConfig) -> anyhow::Result<()> {
    let pool = PgPoolOptions::new()
        .max_connections(10)
        .connect(&config.database_url)
        .await
        .context("failed to connect to PostgreSQL")?;
    let storage = PostgresAdapter::new(pool.clone());
    storage
        .initialize_schema()
        .await
        .context("failed to initialize PostgreSQL schema")?;
    tokio::fs::create_dir_all(&config.attachment_dir)
        .await
        .context("failed to create Attachment directory")?;
    let object_store =
        object_store::local::LocalFileSystem::new_with_prefix(&config.attachment_dir)
            .context("failed to open Attachment directory")?;
    let web_origin = config
        .web_origin
        .as_ref()
        .map(|origin| origin.origin().ascii_serialization());
    let state = http::RuntimeState {
        #[cfg(test)]
        pool: pool.clone(),
        storage,
        read: PostgresQueries::new(pool.clone()),
        objects: std::sync::Arc::new(AttachmentObjectStore::new(std::sync::Arc::new(
            object_store,
        ))),
        session_lifetime: SessionLifetime::from_hours(config.session_ttl_hours)
            .context("Server session TTL must be a positive number of hours")?,
        attachment_max_bytes: config.attachment_max_bytes,
        queries: QueryRegistry::default(),
        agent_graph_cache: http::AgentGraphCache::default(),
        web_origin: web_origin.clone(),
        space_events: super::space_events::SpaceEventSignal::default(),
    };
    let mut api = http::api_router(state.clone(), 100 * 1024 * 1024);
    if let Some(web_origin) = &web_origin {
        api = http::allow_web_origin(api, web_origin);
    }
    let dispatcher = tokio::spawn(dispatch_available_work_forever(state.clone()));
    let space_event_listener =
        tokio::spawn(state.space_events.clone().listen_forever(pool.clone()));
    let mut app = axum::Router::new()
        .nest("/api/v1", api)
        .route("/install.sh", axum::routing::get(computer_install_script))
        .fallback_service(
            ServeDir::new(&config.web_dist)
                .append_index_html_on_directories(true)
                .not_found_service(ServeFile::new(config.web_dist.join("index.html"))),
        );
    if let Some(update_dir) = config.computer_update_dir.clone() {
        app = app.route(
            "/api/v1/computer-updates/stable/{target}/manifest.json",
            axum::routing::get(move |target| computer_release_manifest(update_dir.clone(), target)),
        );
    }
    let app = app.layer(TraceLayer::new_for_http());
    let listener = tokio::net::TcpListener::bind(config.bind)
        .await
        .with_context(|| format!("failed to bind Server at {}", config.bind))?;
    let served = axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal())
        .await
        .context("Server stopped unexpectedly");
    dispatcher.abort();
    space_event_listener.abort();
    served
}

/// How often the Server looks for Agents with work and no live Run.
///
/// Short because it bounds how long an Agent waits before starting, and nothing else: no Run outcome
/// depends on this timer, so a slow or skipped tick delays work and cannot fail it. A tick is needed
/// at all because availability is a future fact, set by ambient debounce and by Agent-chosen defer
/// times.
const DISPATCH_INTERVAL: std::time::Duration = std::time::Duration::from_secs(1);
/// Agents dispatched per tick. Caps one pass, not total throughput: the next tick takes the rest.
const DISPATCH_BATCH: u32 = 64;

async fn dispatch_available_work_forever(state: http::RuntimeState) {
    let mut ticker = tokio::time::interval(DISPATCH_INTERVAL);
    ticker.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    loop {
        ticker.tick().await;
        match http::dispatch_available_work(&state, OffsetDateTime::now_utc(), DISPATCH_BATCH).await
        {
            Ok(outcome) if outcome.failed > 0 => tracing::warn!(
                dispatched = outcome.dispatched,
                failed = outcome.failed,
                "some Runs could not be dispatched; their Items stay pending for the next pass"
            ),
            Ok(_) => {}
            Err(error) => tracing::error!(
                %error,
                "dispatch pass failed; retrying on the next tick"
            ),
        }
    }
}
/// Serves only `<target>/manifest.json`; artifacts are downloaded from the URL inside it.
async fn computer_release_manifest(
    update_dir: std::path::PathBuf,
    axum::extract::Path(target): axum::extract::Path<String>,
) -> axum::response::Response {
    use axum::response::IntoResponse;

    let valid_target = !target.is_empty()
        && target
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_'));
    if !valid_target {
        return axum::http::StatusCode::NOT_FOUND.into_response();
    }
    match tokio::fs::read(update_dir.join(&target).join("manifest.json")).await {
        Ok(manifest) => (
            [
                (axum::http::header::CONTENT_TYPE, "application/json"),
                (axum::http::header::CACHE_CONTROL, "no-cache"),
            ],
            manifest,
        )
            .into_response(),
        Err(_) => axum::http::StatusCode::NOT_FOUND.into_response(),
    }
}

const COMPUTER_INSTALL_SCRIPT: &str = include_str!("install-computer.sh");

async fn computer_install_script() -> impl axum::response::IntoResponse {
    (
        [
            (
                axum::http::header::CONTENT_TYPE,
                "text/x-shellscript; charset=utf-8",
            ),
            (axum::http::header::CACHE_CONTROL, "no-cache"),
        ],
        COMPUTER_INSTALL_SCRIPT,
    )
}

async fn shutdown_signal() {
    let _ = tokio::signal::ctrl_c().await;
}
