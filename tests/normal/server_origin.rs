#[path = "../support/mod.rs"]
mod support;

use std::net::SocketAddr;

use anyhow::{Context, Result, ensure};
use reqwest::{Client, StatusCode, header};
use tempfile::tempdir;
use url::Url;
use uuid::Uuid;

use support::{
    TestDatabase, reserve_local_port, spawn_server, wait_for_health, write_server_config,
    write_server_config_with_web_origin,
};

#[tokio::test]
async fn a_configured_web_origin_narrows_writes_to_full_origins() -> Result<()> {
    let database = TestDatabase::create("sumi_origin_configured").await?;
    let result = run_configured_origin_flow(&database).await;
    database.drop().await?;
    result
}

async fn run_configured_origin_flow(database: &TestDatabase) -> Result<()> {
    let web_origin = Url::parse("https://app.example.test")?;
    let (root, server_url, mut server) = spawn_test_server(database, Some(&web_origin)).await?;
    let client = Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .build()?;
    let own_origin = server_url.origin().ascii_serialization();
    let web_origin = web_origin.origin().ascii_serialization();

    let web = register(&client, &server_url, "ada@example.test", Some(&web_origin)).await?;
    ensure!(
        web.status() == StatusCode::CREATED,
        "the configured Web UI origin was rejected: {} {}",
        web.status(),
        server.log_text()
    );

    let session = web
        .headers()
        .get(header::SET_COOKIE)
        .context("registration did not set a Session cookie")?
        .to_str()?
        .split(';')
        .next()
        .context("Session cookie is empty")?
        .to_owned();

    let foreign = create_space(&client, &server_url, &session, "https://evil.example").await?;
    ensure!(
        foreign.status() == StatusCode::FORBIDDEN,
        "a foreign Origin reached the handler: {}",
        foreign.status()
    );

    let wrong_scheme = create_space(
        &client,
        &server_url,
        &session,
        &format!("https://{}", server_url.authority()),
    )
    .await?;
    ensure!(
        wrong_scheme.status() == StatusCode::FORBIDDEN,
        "the same host on another scheme is another origin: {}",
        wrong_scheme.status()
    );

    let own = create_space(&client, &server_url, &session, &own_origin).await?;
    ensure!(
        own.status() == StatusCode::CREATED,
        "the Server's own origin was rejected: {} {}",
        own.status(),
        server.log_text()
    );

    drop(root);
    server.interrupt().await?;
    Ok(())
}

async fn spawn_test_server(
    database: &TestDatabase,
    web_origin: Option<&Url>,
) -> Result<(tempfile::TempDir, Url, support::SumiProcess)> {
    let root = tempdir()?;
    let web_dist = root.path().join("web");
    let attachments = root.path().join("attachments");
    std::fs::create_dir(&web_dist)?;
    std::fs::write(
        web_dist.join("index.html"),
        "<!doctype html><title>Sumi</title>",
    )?;

    let bind = SocketAddr::from(([127, 0, 0, 1], reserve_local_port()?));
    let server_url = Url::parse(&format!("http://{bind}"))?;
    let config = root.path().join("server.toml");
    match web_origin {
        Some(web_origin) => write_server_config_with_web_origin(
            &config,
            bind,
            &database.url,
            &attachments,
            &web_dist,
            web_origin,
        )?,
        None => write_server_config(&config, bind, &database.url, &attachments, &web_dist)?,
    }
    let server = spawn_server(&config)?;
    wait_for_health(&server_url).await?;
    Ok((root, server_url, server))
}

async fn register(
    client: &Client,
    server: &Url,
    email: &str,
    origin: Option<&str>,
) -> Result<reqwest::Response> {
    let mut request = client
        .post(server.join("/api/v1/auth/register")?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .json(&serde_json::json!({
            "display_name": "Human_Ada",
            "email": email,
            "password": "correct horse battery staple"
        }));
    if let Some(origin) = origin {
        request = request.header(header::ORIGIN, origin);
    }
    Ok(request.send().await?)
}

async fn create_space(
    client: &Client,
    server: &Url,
    session: &str,
    origin: &str,
) -> Result<reqwest::Response> {
    Ok(client
        .post(server.join("/api/v1/spaces")?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, session)
        .header(header::ORIGIN, origin)
        .json(&serde_json::json!({
            "name": "origin-lab",
            "slug": "origin-lab",
            "accent": "#F0602F"
        }))
        .send()
        .await?)
}
