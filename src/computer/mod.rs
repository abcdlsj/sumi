mod adapters;
mod application;
mod core;
mod drivers;
mod pairing;
mod server_socket;
pub(crate) mod update;

use std::{
    collections::{BTreeMap, HashSet, VecDeque},
    path::{Path, PathBuf},
};

use adapters::{
    AgentHomeAdapter, LlmUsageWriter, LocalIpcAdapter, ServerConnectionAdapter, SqliteAdapter,
};
use anyhow::{Context, ensure};
use application::{
    pipeline::RunPipelineService,
    ports::{AgentHomePort, ComputerTransaction, DriverPort, LlmUsageStore, TransactionPort},
    recovery::RecoveryService,
    scheduler::SchedulerService,
};
use backon::{BackoffBuilder, ExponentialBuilder};
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use futures_util::{Sink, SinkExt, Stream, StreamExt};
use rand::{RngCore, rngs::OsRng};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use sumi_agent_core::SandboxAdapter;
use tokio_tungstenite::tungstenite::{Message as WebSocketMessage, client::IntoClientRequest};
use uuid::Uuid;

use crate::{
    cli::ComputerArgs,
    config::{daemon_socket_path, load, runtime_dir_for},
    ids::{DaemonSessionId, EventId, RunId},
    private_paths::ensure_private_directory,
    protocol::{
        capability,
        computer::{
            CommandSequence, ComputerFrame, ComputerHello, Heartbeat, ServerFrame, ServerHandshake,
        },
        version::SUPPORTED,
    },
};

pub(crate) async fn run(args: ComputerArgs) -> anyhow::Result<()> {
    let restart_args = args.clone();
    let mut config = load(args.config.as_ref())?.computer;
    if let Some(server) = args.server.clone() {
        config.server_url = server;
    }
    update::install_release_binary_if_needed(&args, config.auto_update).await?;
    let (computer_home, secrets) = match pairing::find_paired_computer(&config.state_dir).await? {
        Some(paired) => paired,
        None => pairing::pair(&config.state_dir, &config.server_url).await?,
    };
    ensure_private_directory(&computer_home).await?;
    let database_path = computer_home.join("daemon.db");
    let mut storage = SqliteAdapter::open(&database_path)
        .await
        .map_err(|error| anyhow::anyhow!(error))?;
    let runtime_dir = runtime_dir_for(&computer_home);
    tokio::fs::create_dir_all(&runtime_dir)
        .await
        .context("failed to create Computer runtime directory")?;
    tracing::info!(
        computer_id = %secrets.computer_id,
        space_id = %secrets.space_id,
        server = %config.server_url,
        "Computer local baseline is ready"
    );
    SandboxAdapter::validate().map_err(|error| anyhow::anyhow!("{error:?}"))?;
    let mut driver_secret = [0_u8; 32];
    OsRng.fill_bytes(&mut driver_secret);
    let mut capability_store = SqliteAdapter::open(&database_path)
        .await
        .map_err(|error| anyhow::anyhow!(error))?;
    let usage_database_path = database_path.clone();
    let (usage_tx, usage_rx) = tokio::sync::mpsc::unbounded_channel();
    tokio::spawn(run_usage_writer(usage_database_path, usage_rx));
    let ipc = LocalIpcAdapter::bind(&daemon_socket_path(&computer_home))
        .await
        .map_err(|error| anyhow::anyhow!(error))?;
    let capability_endpoint = config.server_url.join(&format!(
        "api/v1/computers/{}/agent-actions",
        secrets.computer_id
    ))?;
    let capability_server_url = config.server_url.clone();
    let capability_computer_id = secrets.computer_id;
    let capability_computer_home = computer_home.clone();
    let capability_token = secrets.token.clone();
    // A read timeout bounds a stalled transfer without capping how long a slow upload may take.
    let capability_client = reqwest::Client::builder()
        .connect_timeout(std::time::Duration::from_secs(10))
        .read_timeout(std::time::Duration::from_secs(60))
        .build()?;
    let mut capability_homes = AgentHomeAdapter::new(
        computer_home.clone(),
        config.codex_config_source.clone(),
        config.codex_auth_source.clone(),
        config.pi_agent_dir_source.clone(),
    );
    let (yield_interrupt_tx, mut yield_interrupt_rx) = tokio::sync::mpsc::unbounded_channel();
    tokio::spawn(async move {
        loop {
            let endpoint = capability_endpoint.clone();
            let token = capability_token.clone();
            let client = capability_client.clone();
            let server_url = capability_server_url.clone();
            let computer_home = capability_computer_home.clone();
            let result = ipc
                .serve_capability(
                    &mut capability_store,
                    &mut capability_homes,
                    driver_secret,
                    |run_id| {
                        let _ = yield_interrupt_tx.send(run_id);
                    },
                    move |context, action, idempotency_key| async move {
                        match action {
                            capability::Action::AttachmentUpload { path } => {
                                return adapters::server_http::proxy_attachment_upload(
                                    &client,
                                    &server_url,
                                    capability_computer_id,
                                    &token,
                                    &computer_home,
                                    &context,
                                    &path,
                                    idempotency_key,
                                )
                                .await;
                            }
                            capability::Action::AttachmentDownload {
                                attachment_id,
                                output,
                            } => {
                                return adapters::server_http::proxy_attachment_download(
                                    &client,
                                    &server_url,
                                    capability_computer_id,
                                    &token,
                                    &computer_home,
                                    &context,
                                    attachment_id,
                                    &output,
                                )
                                .await;
                            }
                            action => {
                                let response = client
                                    .post(endpoint)
                                    .bearer_auth(token)
                                    .json(&adapters::server_http::AgentActionRequest {
                                        context,
                                        action,
                                        idempotency_key,
                                    })
                                    .send()
                                    .await;
                                match response {
                                    Ok(response) => response.json().await.unwrap_or_else(|_| {
                                        adapters::server_http::capability_failure()
                                    }),
                                    Err(_) => adapters::server_http::capability_failure(),
                                }
                            }
                        }
                    },
                )
                .await;
            if let Err(error) = result {
                tracing::warn!(%error, "local Agent capability request failed");
            }
        }
    });
    let mut homes = AgentHomeAdapter::new(
        computer_home.clone(),
        config.codex_config_source.clone(),
        config.codex_auth_source.clone(),
        config.pi_agent_dir_source.clone(),
    );
    let mut driver = drivers::runtime(&computer_home, &config, driver_secret, Some(usage_tx))
        .map_err(|error| anyhow::anyhow!(error))?;
    RecoveryService::recover(
        &mut storage,
        &mut driver,
        &mut homes,
        config.max_concurrent_runs,
    )
    .await
    .map_err(|error| anyhow::anyhow!(error))?;
    update::signal_ready(&computer_home).await?;
    let mut updates =
        update::start_checker(&config, config.server_url.clone(), computer_home.clone());
    let reconnect_backoff = || {
        ExponentialBuilder::default()
            .with_jitter()
            .with_min_delay(std::time::Duration::from_secs(1))
            .with_max_delay(std::time::Duration::from_secs(30))
            .without_max_times()
            .build()
    };
    let mut reconnect = reconnect_backoff();
    loop {
        let connection_started = tokio::time::Instant::now();
        // The connection future is created once per attempt and only completes when the connection
        // ends: a staged release is handed to it, never able to cancel it.
        let exit = server_socket::connect(
            &config.server_url,
            &secrets,
            &mut storage,
            &mut driver,
            &mut homes,
            config.max_concurrent_runs,
            server_socket::ConnectionChannels {
                yield_interrupts: &mut yield_interrupt_rx,
                updates: updates.as_mut(),
            },
        )
        .await;
        match exit {
            Ok(server_socket::ConnectionExit::Closed) => return Ok(()),
            Ok(server_socket::ConnectionExit::Activate(staged)) => {
                update::launch_updater(
                    &staged,
                    &computer_home,
                    &restart_args,
                    config.update_ready_timeout_seconds,
                )
                .await?;
                return Ok(());
            }
            Err(error) => {
                // A connection that lasted is not part of a failure streak.
                if connection_started.elapsed() >= server_socket::STABLE_CONNECTION {
                    reconnect = reconnect_backoff();
                }
                let delay = reconnect
                    .next()
                    .expect("an unlimited reconnect backoff always yields a delay");
                tracing::warn!(%error, ?delay, "Computer connection lost; retrying");
                tokio::time::sleep(delay).await;
            }
        }
    }
}

/// What one connection borrows from the daemon loop besides its local state.
async fn shutdown_signal() {
    #[cfg(unix)]
    {
        use tokio::signal::unix::{SignalKind, signal};
        match signal(SignalKind::terminate()) {
            Ok(mut terminate) => {
                tokio::select! {
                    _ = tokio::signal::ctrl_c() => {}
                    _ = terminate.recv() => {}
                }
            }
            Err(_) => {
                let _ = tokio::signal::ctrl_c().await;
            }
        }
    }
    #[cfg(not(unix))]
    {
        let _ = tokio::signal::ctrl_c().await;
    }
}

async fn run_usage_writer(
    database_path: PathBuf,
    mut usage_rx: tokio::sync::mpsc::UnboundedReceiver<application::usage::LlmUsageRecord>,
) {
    let Ok(usage_writer) = LlmUsageWriter::open(&database_path).await else {
        tracing::error!("LLM usage writer could not open the local daemon database");
        return;
    };
    while let Some(record) = usage_rx.recv().await {
        if let Err(error) = usage_writer.record(&record).await {
            tracing::warn!(%error, "LLM usage record could not be stored locally");
            // A malformed or transient row must not tear down the long-lived writer. Keep
            // consuming subsequent completions so one failure cannot suppress all telemetry.
            continue;
        }
    }
}
