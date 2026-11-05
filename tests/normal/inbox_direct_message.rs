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
    TestDatabase, create_space_with_slug, register_with, reserve_local_port, spawn_server,
    wait_for_health, write_server_config,
};

#[tokio::test]
async fn inbox_projects_routed_attention_and_refuses_foreign_readers() -> Result<()> {
    let database = TestDatabase::create("sumi_inbox_dm").await?;
    let result = run_inbox_flow(&database).await;
    database.drop().await?;
    result
}

async fn run_inbox_flow(database: &TestDatabase) -> Result<()> {
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
    write_server_config(&config, bind, &database.url, &attachments, &web_dist)?;
    let mut server = spawn_server(&config)?;
    wait_for_health(&server_url).await?;

    let client = Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .build()?;
    let owner = register_with(&client, &server_url, "Ada_Lovelace", "ada@example.test").await?;
    let space = create_space_with_slug(&client, &server_url, &owner, "sumi-lab").await?;
    let space_id = space["id"].as_str().context("Space ID")?.to_owned();
    let owner_member_id = space["owner_member_id"]
        .as_str()
        .context("owner Member ID")?
        .to_owned();

    let recipient_cookie = invite_and_accept(
        &client,
        &server_url,
        &owner,
        &space_id,
        "grace@example.test",
    )
    .await?;
    let members: Value = client
        .get(server_url.join(&format!("/api/v1/spaces/{space_id}/members"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let recipient_member_id = members
        .as_array()
        .context("Member list")?
        .iter()
        .find(|member| member["id"] != Value::String(owner_member_id.clone()))
        .and_then(|member| member["id"].as_str())
        .context("recipient Member ID")?
        .to_owned();

    let empty: Value = client
        .get(server_url.join(&format!("/api/v1/spaces/{space_id}/dms"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    ensure!(empty.as_array().context("DM list")?.is_empty());

    let open = || {
        client
            .post(
                server_url
                    .join(&format!("/api/v1/spaces/{space_id}/dms"))
                    .expect("valid DM URL"),
            )
            .header("idempotency-key", Uuid::now_v7().to_string())
            .header(header::COOKIE, &owner)
            .json(&serde_json::json!({"member_id": recipient_member_id}))
    };
    let opened = open().send().await?;
    ensure!(
        opened.status() == StatusCode::CREATED,
        "{}",
        server.log_text()
    );
    let opened: Value = opened.json().await?;
    ensure!(opened["other_member"]["id"].as_str() == Some(recipient_member_id.as_str()));

    let reopened = open().send().await?;
    ensure!(reopened.status() == StatusCode::OK, "{}", server.log_text());
    let reopened: Value = reopened.json().await?;
    ensure!(reopened["channel_id"] == opened["channel_id"]);

    for cookie in [&owner, &recipient_cookie] {
        let conversations: Value = client
            .get(server_url.join(&format!("/api/v1/spaces/{space_id}/dms"))?)
            .header(header::COOKIE, cookie)
            .send()
            .await?
            .error_for_status()?
            .json()
            .await?;
        let conversations = conversations.as_array().context("DM list")?;
        ensure!(conversations.len() == 1, "{conversations:?}");
        ensure!(conversations[0]["channel_id"] == opened["channel_id"]);
    }

    let self_dm = client
        .post(server_url.join(&format!("/api/v1/spaces/{space_id}/dms"))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, &owner)
        .json(&serde_json::json!({"member_id": owner_member_id}))
        .send()
        .await?;
    ensure!(
        self_dm.status() == StatusCode::CONFLICT,
        "{}",
        server.log_text()
    );

    let own_inbox: Value = client
        .get(server_url.join(&format!("/api/v1/members/{owner_member_id}/inbox"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    ensure!(own_inbox.as_array().context("Inbox")?.is_empty());

    let foreign = client
        .get(server_url.join(&format!("/api/v1/members/{recipient_member_id}/inbox"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?;
    ensure!(
        foreign.status() == StatusCode::FORBIDDEN,
        "{}",
        server.log_text()
    );

    let outsider = register_with(&client, &server_url, "Alan_Turing", "alan@example.test").await?;
    let other_space = create_space_with_slug(&client, &server_url, &outsider, "other-lab").await?;
    let other_member_id = other_space["owner_member_id"]
        .as_str()
        .context("other Space owner")?;
    let cross_space = client
        .get(server_url.join(&format!("/api/v1/members/{other_member_id}/inbox"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?;
    ensure!(
        cross_space.status() == StatusCode::NOT_FOUND,
        "{}",
        server.log_text()
    );
    let reverse = client
        .get(server_url.join(&format!("/api/v1/members/{owner_member_id}/inbox"))?)
        .header(header::COOKIE, &outsider)
        .send()
        .await?;
    ensure!(
        reverse.status() == StatusCode::NOT_FOUND,
        "{}",
        server.log_text()
    );

    let anonymous = client
        .get(server_url.join(&format!("/api/v1/members/{owner_member_id}/inbox"))?)
        .send()
        .await?;
    ensure!(anonymous.status() == StatusCode::UNAUTHORIZED);

    server.interrupt().await?;
    Ok(())
}

#[tokio::test]
async fn human_inbox_routes_replies_mentions_and_dms_and_marks_read() -> Result<()> {
    let database = TestDatabase::create("sumi_human_inbox").await?;
    let result = run_human_inbox_flow(&database).await;
    database.drop().await?;
    result
}

async fn run_human_inbox_flow(database: &TestDatabase) -> Result<()> {
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
    write_server_config(&config, bind, &database.url, &attachments, &web_dist)?;
    let mut server = spawn_server(&config)?;
    wait_for_health(&server_url).await?;

    let client = Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .build()?;
    let owner = register_with(&client, &server_url, "Ada_Lovelace", "ada@example.test").await?;
    let space = create_space_with_slug(&client, &server_url, &owner, "sumi-lab").await?;
    let space_id = space["id"].as_str().context("Space ID")?.to_owned();
    let owner_member_id = space["owner_member_id"]
        .as_str()
        .context("owner Member ID")?
        .to_owned();
    let general_channel_id = space["general_channel_id"]
        .as_str()
        .context("general Channel ID")?
        .to_owned();

    let grace = invite_and_accept(
        &client,
        &server_url,
        &owner,
        &space_id,
        "grace@example.test",
    )
    .await?;
    let members: Value = client
        .get(server_url.join(&format!("/api/v1/spaces/{space_id}/members"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let grace_member_id = members
        .as_array()
        .context("Member list")?
        .iter()
        .find(|member| member["id"] != Value::String(owner_member_id.clone()))
        .and_then(|member| member["id"].as_str())
        .context("Grace Member ID")?
        .to_owned();

    // A plain Channel Message creates no Human Item: ordinary Channel activity is not attention.
    let root: Value = client
        .post(server_url.join(&format!("/api/v1/channels/{general_channel_id}/messages"))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, &owner)
        .json(&serde_json::json!({
            "body_markdown": "planning thread",
            "mentions": [],
            "mention_all": false,
            "attachment_ids": []
        }))
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let root_id = root["id"].as_str().context("root Message ID")?.to_owned();
    let thread_id = root["thread_id"].as_str().context("thread ID")?.to_owned();
    let quiet: Value = client
        .get(server_url.join(&format!("/api/v1/members/{owner_member_id}/inbox"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    ensure!(
        quiet.as_array().context("Inbox")?.is_empty(),
        "plain Channel Messages must not route to Humans"
    );

    // A reply to Ada's Message routes a hard reply Item to Ada, not to sender Grace.
    client
        .post(server_url.join(&format!("/api/v1/threads/{thread_id}/messages"))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, &grace)
        .json(&serde_json::json!({
            "body_markdown": "I can take this",
            "mentions": [],
            "mention_all": false,
            "attachment_ids": [],
            "reply_to_message_id": root_id
        }))
        .send()
        .await?
        .error_for_status()?;
    let inbox: Value = client
        .get(server_url.join(&format!("/api/v1/members/{owner_member_id}/inbox"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let inbox = inbox.as_array().context("Inbox")?;
    ensure!(inbox.len() == 1, "{inbox:?}");
    ensure!(inbox[0]["kind"] == "reply", "{inbox:?}");
    ensure!(inbox[0]["sender_member_id"] == grace_member_id, "{inbox:?}");
    ensure!(
        inbox[0]["message_preview"] == "I can take this",
        "Inbox preview projection mismatch"
    );
    let reply_item_id = inbox[0]["id"].as_str().context("Item ID")?.to_owned();
    let sender_inbox: Value = client
        .get(server_url.join(&format!("/api/v1/members/{grace_member_id}/inbox"))?)
        .header(header::COOKIE, &grace)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    ensure!(
        sender_inbox.as_array().context("sender Inbox")?.is_empty(),
        "the sender must not receive an Item for its own Message"
    );

    // Marking the Item read removes it from the queue; repeating the read stays idempotent.
    let read: Value = client
        .post(server_url.join(&format!("/api/v1/inbox-items/{reply_item_id}/read"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    ensure!(read["status"] == "handled", "{read:?}");
    client
        .post(server_url.join(&format!("/api/v1/inbox-items/{reply_item_id}/read"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?;
    let drained: Value = client
        .get(server_url.join(&format!("/api/v1/members/{owner_member_id}/inbox"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    ensure!(drained.as_array().context("Inbox")?.is_empty());

    // A mention routes to the named Human, and a DM routes to the other participant.
    client
        .post(server_url.join(&format!("/api/v1/channels/{general_channel_id}/messages"))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, &owner)
        .json(&serde_json::json!({
            "body_markdown": "@grace please confirm",
            "mentions": [grace_member_id],
            "mention_all": false,
            "attachment_ids": []
        }))
        .send()
        .await?
        .error_for_status()?;
    let mentioned: Value = client
        .get(server_url.join(&format!("/api/v1/members/{grace_member_id}/inbox"))?)
        .header(header::COOKIE, &grace)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let mentioned = mentioned.as_array().context("Inbox")?;
    ensure!(mentioned.len() == 1, "{mentioned:?}");
    ensure!(mentioned[0]["kind"] == "mention", "{mentioned:?}");

    let dm: Value = client
        .post(server_url.join(&format!("/api/v1/spaces/{space_id}/dms"))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, &grace)
        .json(&serde_json::json!({"member_id": owner_member_id}))
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let dm_channel_id = dm["channel_id"]
        .as_str()
        .context("DM Channel ID")?
        .to_owned();
    client
        .post(server_url.join(&format!("/api/v1/channels/{dm_channel_id}/messages"))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, &grace)
        .json(&serde_json::json!({
            "body_markdown": "ping",
            "mentions": [],
            "mention_all": false,
            "attachment_ids": []
        }))
        .send()
        .await?
        .error_for_status()?;
    let dmed: Value = client
        .get(server_url.join(&format!("/api/v1/members/{owner_member_id}/inbox"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let dmed = dmed.as_array().context("Inbox")?;
    ensure!(dmed.len() == 1, "{dmed:?}");
    ensure!(dmed[0]["kind"] == "direct", "{dmed:?}");

    server.interrupt().await?;
    Ok(())
}

async fn invite_and_accept(
    client: &Client,
    server: &Url,
    owner: &str,
    space_id: &str,
    email: &str,
) -> Result<String> {
    let created: Value = client
        .post(server.join(&format!("/api/v1/spaces/{space_id}/invites"))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, owner)
        .json(&serde_json::json!({"email": email}))
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let token = created["token"].as_str().context("plaintext token")?;
    let cookie = register_with(client, server, "Grace_Hopper", email).await?;
    client
        .post(server.join(&format!("/api/v1/invites/{token}/accept"))?)
        .header(header::COOKIE, &cookie)
        .send()
        .await?
        .error_for_status()?;
    Ok(cookie)
}

/// A governor reads another Member's Inbox to act on it. Channel membership still decides whether
/// the source body is visible: the Item stays, the body does not.
#[tokio::test]
async fn governor_reads_agent_inbox_without_private_bodies() -> Result<()> {
    let database = TestDatabase::create("sumi_inbox_visibility").await?;
    let result = run_agent_inbox_visibility_flow(&database).await;
    database.drop().await?;
    result
}

async fn run_agent_inbox_visibility_flow(database: &TestDatabase) -> Result<()> {
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
    write_server_config(&config, bind, &database.url, &attachments, &web_dist)?;
    let mut server = spawn_server(&config)?;
    wait_for_health(&server_url).await?;

    let client = Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .build()?;
    let owner = register_with(&client, &server_url, "Ada_Lovelace", "ada@example.test").await?;
    let space = create_space_with_slug(&client, &server_url, &owner, "sumi-lab").await?;
    let space_id = space["id"].as_str().context("Space ID")?.to_owned();
    let owner_member_id = space["owner_member_id"]
        .as_str()
        .context("owner Member ID")?
        .to_owned();

    // Grace owns the private conversation with the Agent; the Space Owner is not a participant.
    let grace = invite_and_accept(
        &client,
        &server_url,
        &owner,
        &space_id,
        "grace@example.test",
    )
    .await?;
    let members: Value = client
        .get(server_url.join(&format!("/api/v1/spaces/{space_id}/members"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let grace_member_id = members
        .as_array()
        .context("Member list")?
        .iter()
        .find(|member| member["id"] != Value::String(owner_member_id.clone()))
        .and_then(|member| member["id"].as_str())
        .context("Grace Member ID")?
        .to_owned();
    client
        .patch(server_url.join(&format!(
            "/api/v1/spaces/{space_id}/members/{grace_member_id}"
        ))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, &owner)
        .json(&serde_json::json!({"access_level": "admin"}))
        .send()
        .await?
        .error_for_status()?;

    let pool = sqlx::PgPool::connect(&database.url).await?;
    let agent_member_id = insert_idle_agent(&pool, &space_id).await?;

    let private: Value = client
        .post(server_url.join(&format!("/api/v1/spaces/{space_id}/channels"))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, &grace)
        .json(&serde_json::json!({
            "slug": "private-plan",
            "kind": "private",
            "agent_member_ids": [agent_member_id]
        }))
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let private_channel_id = private["id"].as_str().context("private Channel ID")?;

    let private_body = "private channel plan";
    let private_message: Value = client
        .post(server_url.join(&format!("/api/v1/channels/{private_channel_id}/messages"))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, &grace)
        .json(&serde_json::json!({
            "body_markdown": private_body,
            "mentions": [agent_member_id],
            "mention_all": false,
            "attachment_ids": []
        }))
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let private_thread_id = private_message["thread_id"]
        .as_str()
        .context("private Thread ID")?
        .to_owned();

    let dm_body = "direct message plan";
    let dm: Value = client
        .post(server_url.join(&format!("/api/v1/spaces/{space_id}/dms"))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, &grace)
        .json(&serde_json::json!({"member_id": agent_member_id}))
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let dm_channel_id = dm["channel_id"].as_str().context("DM Channel ID")?;
    client
        .post(server_url.join(&format!("/api/v1/channels/{dm_channel_id}/messages"))?)
        .header("idempotency-key", Uuid::now_v7().to_string())
        .header(header::COOKIE, &grace)
        .json(&serde_json::json!({
            "body_markdown": dm_body,
            "mentions": [],
            "mention_all": false,
            "attachment_ids": []
        }))
        .send()
        .await?
        .error_for_status()?;

    // The governor sees the Agent's Items, their source summary and their Message IDs, but neither
    // private body.
    let agent_inbox: Value = client
        .get(server_url.join(&format!("/api/v1/members/{agent_member_id}/inbox"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let agent_inbox = agent_inbox.as_array().context("Agent Inbox")?;
    ensure!(agent_inbox.len() == 2, "{agent_inbox:?}");
    ensure!(
        agent_inbox
            .iter()
            .all(|item| item["message_id"].is_string()),
        "the governor still sees which Message each Item came from: {agent_inbox:?}"
    );
    for item in agent_inbox {
        ensure!(
            item["message_preview"].is_null(),
            "a non-member governor received a private body: {item:?}"
        );
    }

    // The source itself stays closed to a non-member, so the Inbox is not the weaker path.
    let private_read = client
        .get(server_url.join(&format!("/api/v1/channels/{private_channel_id}/messages"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?;
    ensure!(
        private_read.status() == StatusCode::FORBIDDEN
            || private_read.status() == StatusCode::NOT_FOUND,
        "the private Channel admitted a non-member: {}",
        private_read.status()
    );
    let private_thread = client
        .get(server_url.join(&format!("/api/v1/threads/{private_thread_id}"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?;
    ensure!(
        private_thread.status() == StatusCode::FORBIDDEN
            || private_thread.status() == StatusCode::NOT_FOUND,
        "the private Thread admitted a non-member: {}",
        private_thread.status()
    );
    let dm_read = client
        .get(server_url.join(&format!("/api/v1/channels/{dm_channel_id}/messages"))?)
        .header(header::COOKIE, &owner)
        .send()
        .await?;
    ensure!(
        dm_read.status() == StatusCode::FORBIDDEN || dm_read.status() == StatusCode::NOT_FOUND,
        "the DM admitted a non-participant: {}",
        dm_read.status()
    );

    // Visibility follows membership, not governance: Grace governs the Space and belongs to both
    // conversations, so the same endpoint returns the bodies to her.
    let grace_inbox: Value = client
        .get(server_url.join(&format!("/api/v1/members/{agent_member_id}/inbox"))?)
        .header(header::COOKIE, &grace)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let grace_inbox = grace_inbox
        .as_array()
        .context("Agent Inbox for a member governor")?;
    ensure!(grace_inbox.len() == 2, "{grace_inbox:?}");
    let mut previews = grace_inbox
        .iter()
        .filter_map(|item| item["message_preview"].as_str())
        .collect::<Vec<_>>();
    previews.sort_unstable();
    let mut expected = vec![dm_body, private_body];
    expected.sort_unstable();
    ensure!(
        previews == expected,
        "a member governor lost previews it may read: {previews:?}"
    );

    drop(pool);
    server.interrupt().await?;
    Ok(())
}

/// Inserts an active Agent without a daemon. Attention routing only needs the Member and its
/// Computer row, and this test never dispatches a Run.
async fn insert_idle_agent(pool: &sqlx::PgPool, space_id: &str) -> Result<String> {
    let space_id = Uuid::parse_str(space_id).context("Space ID")?;
    let computer_id = Uuid::now_v7();
    sqlx::query(
        "INSERT INTO computers \
         (id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) \
         VALUES ($1,$2,'test-computer','localhost','linux',NULL,'offline',1,now())",
    )
    .bind(computer_id)
    .bind(space_id)
    .execute(pool)
    .await?;
    let agent_id = Uuid::now_v7();
    sqlx::query(
        "INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) \
         VALUES ($1,$2,'agent','Lin','member',now())",
    )
    .bind(agent_id)
    .bind(space_id)
    .execute(pool)
    .await?;
    sqlx::query(
        "INSERT INTO agents \
         (member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) \
         VALUES ($1,$2,$3,'Assist',1,'active','builtin',now())",
    )
    .bind(agent_id)
    .bind(space_id)
    .bind(computer_id)
    .execute(pool)
    .await?;
    Ok(agent_id.to_string())
}
