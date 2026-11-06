//! Browser event stream acceptance: a first connection starts at the Space tail, a reconnect
//! continues right after `Last-Event-ID`, and a page of unreadable events does not stop the stream.
//! The rules are defined in `docs/DESIGN.md` 「数据与事务」.

#[path = "../support/mod.rs"]
mod support;

use std::{net::SocketAddr, time::Duration};

use anyhow::{Context, Result, ensure};
use reqwest::{Client, StatusCode, header};
use serde_json::Value;
use sqlx::PgPool;
use tempfile::{TempDir, tempdir};
use url::Url;
use uuid::Uuid;

use support::{
    SpaceResponse, SumiProcess, TestDatabase, register_human, reserve_local_port, spawn_server,
    wait_for_health, write_server_config,
};

#[tokio::test]
async fn the_event_stream_starts_at_the_tail_and_resumes_after_last_event_id() -> Result<()> {
    let database = TestDatabase::create("sumi_space_events").await?;
    let result = async {
        let fixture = Fixture::start(&database).await?;
        // History before any stream exists.
        fixture.publish_message("before the stream").await?;

        let mut stream = fixture.open_stream(None).await?;
        // The stream was opened after the history above, so this commit is the first thing it may
        // send. A replay would deliver the Space creation events instead.
        let live = fixture.publish_message("after the stream").await?;
        let first = stream.next_event(Duration::from_secs(15)).await?;
        ensure!(
            first.event_type == "message.created"
                && first.data["data"]["resource_id"].as_str() == Some(&live.to_string()),
            "a first connection must deliver the live event, not replay history: {first:?}"
        );

        // A reconnect continues after the event the Browser already handled.
        drop(stream);
        let resumed = fixture.publish_message("after reconnect").await?;
        let mut stream = fixture.open_stream(Some(&first.id)).await?;
        let next = stream.next_event(Duration::from_secs(15)).await?;
        ensure!(
            next.event_type == "message.created"
                && next.data["data"]["resource_id"].as_str() == Some(&resumed.to_string()),
            "a reconnect must resume after Last-Event-ID: {next:?}"
        );
        Ok(())
    }
    .await;
    database.drop().await?;
    result
}

/// A page the viewer cannot read still advances the stream, so the visible event behind it arrives
/// without waiting for a wake-up that the already-committed page cannot produce.
#[tokio::test]
async fn an_unreadable_backlog_does_not_stop_the_event_stream() -> Result<()> {
    let database = TestDatabase::create("sumi_space_events").await?;
    let result = async {
        let fixture = Fixture::start(&database).await?;
        let mut stream = fixture.open_stream(None).await?;
        // One commit that fills a whole page with events of a Channel the viewer cannot read, then
        // one event every Space Member receives.
        fixture.commit_private_backlog(100).await?;
        let visible = stream.next_event(Duration::from_secs(15)).await?;
        ensure!(
            visible.event_type == "task.updated",
            "the event after the unreadable page must arrive: {visible:?}"
        );
        Ok(())
    }
    .await;
    database.drop().await?;
    result
}

struct Fixture {
    _root: TempDir,
    _server: SumiProcess,
    client: Client,
    server_url: Url,
    cookie: String,
    space: SpaceResponse,
    pool: PgPool,
}

impl Fixture {
    async fn start(database: &TestDatabase) -> Result<Self> {
        let root = tempdir()?;
        let web_dist = root.path().join("web");
        let attachments = root.path().join("attachments");
        std::fs::create_dir(&web_dist)?;
        std::fs::create_dir(&attachments)?;
        std::fs::write(
            web_dist.join("index.html"),
            "<!doctype html><title>Sumi</title>",
        )?;
        let bind = SocketAddr::from(([127, 0, 0, 1], reserve_local_port()?));
        let server_url = Url::parse(&format!("http://{bind}"))?;
        let config = root.path().join("server.toml");
        write_server_config(&config, bind, &database.url, &attachments, &web_dist)?;
        let server = spawn_server(&config)?;
        wait_for_health(&server_url).await?;
        let client = Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .build()?;
        let cookie = register_human(&client, &server_url).await?;
        let space = support::create_space(&client, &server_url, &cookie).await?;
        let pool = PgPool::connect(&database.url).await?;
        Ok(Self {
            _root: root,
            _server: server,
            client,
            server_url,
            cookie,
            space,
            pool,
        })
    }

    fn events_url(&self) -> Url {
        self.server_url
            .join(&format!("/api/v1/spaces/{}/events", self.space.id))
            .expect("event stream URL")
    }

    async fn publish_message(&self, body: &str) -> Result<Uuid> {
        let response = self
            .client
            .post(self.server_url.join(&format!(
                "/api/v1/channels/{}/messages",
                self.space.general_channel_id
            ))?)
            .header("idempotency-key", Uuid::now_v7().to_string())
            .header(header::COOKIE, &self.cookie)
            .json(&serde_json::json!({ "body_markdown": body, "mentions": [] }))
            .send()
            .await?;
        ensure!(
            response.status() == StatusCode::CREATED,
            "publish Message: {}",
            response.status()
        );
        let message: Value = response.json().await?;
        Ok(message["id"].as_str().context("Message ID")?.parse()?)
    }

    async fn open_stream(&self, last_event_id: Option<&str>) -> Result<EventStream> {
        EventStream::open(
            &self.client,
            &self.events_url(),
            &self.cookie,
            last_event_id,
        )
        .await
    }

    /// Commits `unreadable` events scoped to a Channel the Owner never joined, then one event
    /// addressed to the whole Space, in the same commit order the Server allocates.
    async fn commit_private_backlog(&self, unreadable: usize) -> Result<()> {
        let channel_id = Uuid::now_v7();
        let mut transaction = self.pool.begin().await?;
        sqlx::query(
            "INSERT INTO channels(id,space_id,kind,slug,next_seq,created_at) \
             VALUES($1,$2,'public','backlog',1,now())",
        )
        .bind(channel_id)
        .bind(self.space.id)
        .execute(&mut *transaction)
        .await?;
        let total = i64::try_from(unreadable + 1)?;
        sqlx::query(
            "WITH allocated AS ( \
                 UPDATE spaces SET event_sequence=event_sequence+$2 WHERE id=$1 \
                 RETURNING event_sequence \
             ) \
             INSERT INTO outbox_events(id,space_id,kind,payload_json,created_at,sequence) \
             SELECT md5(random()::text || clock_timestamp()::text || g)::uuid,$1, \
                 CASE WHEN g=$2 THEN 'task.updated' ELSE 'message.created' END, \
                 CASE WHEN g=$2 THEN jsonb_build_object('resource_id',gen_random_uuid()::text) \
                      ELSE jsonb_build_object('resource_id',gen_random_uuid()::text, \
                         'channel_id',$3::text) END, \
                 now(),allocated.event_sequence-$2+g \
             FROM allocated,generate_series(1,$2) AS g",
        )
        .bind(self.space.id)
        .bind(total)
        .bind(channel_id)
        .execute(&mut *transaction)
        .await?;
        transaction.commit().await?;
        Ok(())
    }
}

#[derive(Debug)]
struct Frame {
    id: String,
    event_type: String,
    data: Value,
}

struct EventStream {
    response: reqwest::Response,
    buffer: String,
}

impl EventStream {
    async fn open(
        client: &Client,
        url: &Url,
        cookie: &str,
        last_event_id: Option<&str>,
    ) -> Result<Self> {
        let mut request = client.get(url.clone()).header(header::COOKIE, cookie);
        if let Some(last_event_id) = last_event_id {
            request = request.header("last-event-id", last_event_id);
        }
        let response = request.send().await?;
        ensure!(
            response.status() == StatusCode::OK,
            "open the event stream: {}",
            response.status()
        );
        Ok(Self {
            response,
            buffer: String::new(),
        })
    }

    /// Waits for the next frame that carries an event; keep-alive comments are skipped.
    async fn next_event(&mut self, within: Duration) -> Result<Frame> {
        let deadline = tokio::time::Instant::now() + within;
        loop {
            if let Some(frame) = self.take_frame()? {
                return Ok(frame);
            }
            let chunk = tokio::time::timeout_at(deadline, self.response.chunk())
                .await
                .context("the event stream stayed silent")??;
            let chunk = chunk.context("the event stream ended")?;
            self.buffer.push_str(&String::from_utf8_lossy(&chunk));
        }
    }

    fn take_frame(&mut self) -> Result<Option<Frame>> {
        while let Some(position) = self.buffer.find("\n\n") {
            let block: String = self.buffer.drain(..position + 2).collect();
            if let Some(frame) = parse_frame(&block)? {
                return Ok(Some(frame));
            }
        }
        Ok(None)
    }
}

fn parse_frame(block: &str) -> Result<Option<Frame>> {
    let (mut id, mut event_type, mut data) = (None, None, None);
    for line in block.lines() {
        if let Some(value) = line.strip_prefix("id:") {
            id = Some(value.trim().to_owned());
        } else if let Some(value) = line.strip_prefix("event:") {
            event_type = Some(value.trim().to_owned());
        } else if let Some(value) = line.strip_prefix("data:") {
            data = Some(serde_json::from_str::<Value>(value.trim())?);
        }
    }
    Ok(match (id, event_type, data) {
        (Some(id), Some(event_type), Some(data)) => Some(Frame {
            id,
            event_type,
            data,
        }),
        _ => None,
    })
}
