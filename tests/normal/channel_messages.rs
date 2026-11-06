//! Channel timeline reads: Root Messages come in pages, each Root carries its newest replies, and
//! one Message can be read alone. The rules are defined in `docs/DESIGN.md` 「对话与 Task」.

#[path = "../support/mod.rs"]
mod support;

use std::net::SocketAddr;

use anyhow::{Context, Result, ensure};
use reqwest::{Client, StatusCode, header};
use serde_json::Value;
use tempfile::tempdir;
use url::Url;
use uuid::Uuid;

use support::{
    TestDatabase, register_human, reserve_local_port, spawn_server, wait_for_health,
    write_server_config,
};

#[tokio::test]
async fn the_timeline_pages_roots_and_previews_their_newest_replies() -> Result<()> {
    let database = TestDatabase::create("sumi_channel_messages").await?;
    let result = run(&database).await;
    database.drop().await?;
    result
}

async fn run(database: &TestDatabase) -> Result<()> {
    let root = tempdir()?;
    let web_dist = root.path().join("web");
    let attachments = root.path().join("attachments");
    std::fs::create_dir(&web_dist)?;
    std::fs::create_dir(&attachments)?;
    std::fs::write(web_dist.join("index.html"), "<!doctype html>")?;
    let bind = SocketAddr::from(([127, 0, 0, 1], reserve_local_port()?));
    let server_url = Url::parse(&format!("http://{bind}"))?;
    let config = root.path().join("server.toml");
    write_server_config(&config, bind, &database.url, &attachments, &web_dist)?;
    let _server = spawn_server(&config)?;
    wait_for_health(&server_url).await?;
    let client = Client::new();
    let cookie = register_human(&client, &server_url).await?;
    let space = support::create_space(&client, &server_url, &cookie).await?;
    let channel_id = space.general_channel_id;

    let post = |path: String, body: Value| {
        let request = client
            .post(server_url.join(&path).unwrap())
            .header("idempotency-key", Uuid::now_v7().to_string())
            .header(header::COOKIE, &cookie)
            .json(&body);
        async move {
            let response = request.send().await?.error_for_status()?;
            anyhow::Ok(response.json::<Value>().await?)
        }
    };
    let message = |body: &str| serde_json::json!({ "body_markdown": body, "mentions": [] });
    let mut roots = Vec::new();
    for body in ["first", "second", "third"] {
        let posted = post(
            format!("/api/v1/channels/{channel_id}/messages"),
            message(body),
        )
        .await?;
        roots.push(posted["id"].as_str().context("root ID")?.to_owned());
    }
    for body in ["r1", "r2", "r3", "r4"] {
        let mut reply = message(body);
        reply["reply_to_message_id"] = Value::from(roots[1].clone());
        post(format!("/api/v1/threads/{}/messages", roots[1]), reply).await?;
    }

    let page = |query: String| {
        let request = client
            .get(
                server_url
                    .join(&format!("/api/v1/channels/{channel_id}/messages{query}"))
                    .unwrap(),
            )
            .header(header::COOKIE, &cookie);
        async move {
            let response = request.send().await?.error_for_status()?;
            anyhow::Ok(response.json::<Value>().await?)
        }
    };
    let bodies = |page: &Value| -> Vec<String> {
        page["messages"]
            .as_array()
            .into_iter()
            .flatten()
            .filter(|message| message["content"]["type"] == "text")
            .map(|message| {
                message["content"]["body_markdown"]
                    .as_str()
                    .unwrap_or("")
                    .to_owned()
            })
            .collect()
    };

    // The newest page comes first, oldest first inside the page, and says older roots remain.
    let newest = page("?limit=2".to_owned()).await?;
    ensure!(bodies(&newest) == ["second", "third"], "{newest}");
    ensure!(newest["has_more_before"] == true, "{newest}");
    let second = &newest["messages"][0];
    ensure!(second["reply_count"] == 4, "{second}");
    let previews: Vec<&str> = second["latest_replies"]
        .as_array()
        .context("latest replies")?
        .iter()
        .map(|reply| reply["content"]["body_markdown"].as_str().unwrap_or(""))
        .collect();
    ensure!(previews == ["r2", "r3", "r4"], "{previews:?}");

    let before = second["seq"].as_u64().context("seq")?;
    let older = page(format!("?limit=2&before_seq={before}")).await?;
    let older_bodies = bodies(&older);
    ensure!(
        older_bodies.last().map(String::as_str) == Some("first"),
        "{older}"
    );
    ensure!(
        older["messages"]
            .as_array()
            .context("older page")?
            .iter()
            .all(|message| message["seq"].as_u64() < Some(before)),
        "{older}"
    );

    // One Message can be read alone; a stranger cannot tell it exists.
    let single = client
        .get(server_url.join(&format!("/api/v1/messages/{}", roots[1]))?)
        .header(header::COOKIE, &cookie)
        .send()
        .await?
        .error_for_status()?
        .json::<Value>()
        .await?;
    ensure!(single["reply_count"] == 4, "{single}");
    let stranger = register_human(&client, &server_url).await?;
    let hidden = client
        .get(server_url.join(&format!("/api/v1/messages/{}", roots[1]))?)
        .header(header::COOKIE, &stranger)
        .send()
        .await?;
    ensure!(
        hidden.status() == StatusCode::NOT_FOUND,
        "{}",
        hidden.status()
    );
    Ok(())
}
