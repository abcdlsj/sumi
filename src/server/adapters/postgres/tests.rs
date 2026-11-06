use super::*;

use crate::server::adapters::test_database::TestDatabase;

use std::str::FromStr;

use sqlx::{Connection, PgConnection, postgres::PgConnectOptions};
use time::OffsetDateTime;
use url::Url;

use crate::server::application::attention::{RequeueDeadItem, RequeueDeadItemInput};
use crate::server::application::conversation::{
    AddChannelAgents, CreateAgentAction, CreateAgentActionInput,
};
use crate::server::application::execution::{
    CompleteRun, CompleteRunInput, DispatchRun, DispatchRunInput, ItemDispositionInput,
    SyncComputerRuns, SyncComputerRunsInput,
};
use crate::server::application::identity::{AgentLifecycleAction, UpdateAgent, UpdateAgentInput};
use crate::server::application::ports::{InboxActivityEventKind, InboxScope};
use crate::server::application::task::{CreateTaskFromRootMessage, CreateTaskInput, TaskSource};

#[test]
fn the_event_stream_hides_unreadable_channels_and_other_members_inboxes() {
    let viewer = MemberId::from_uuid(Uuid::now_v7());
    let other_member = MemberId::from_uuid(Uuid::now_v7());
    let readable = Uuid::now_v7();
    let private = Uuid::now_v7();
    let channels = std::collections::HashSet::from([readable]);

    let in_readable_channel = json!({"resource_id": Uuid::now_v7(), "channel_id": readable});
    let in_private_channel = json!({"resource_id": Uuid::now_v7(), "channel_id": private});
    assert!(event_is_visible(
        "message.created",
        &in_readable_channel,
        viewer,
        false,
        &channels
    ));
    assert!(!event_is_visible(
        "message.created",
        &in_private_channel,
        viewer,
        false,
        &channels
    ));
    // A governor reads Agent Inboxes but still holds no membership in a private Channel.
    assert!(!event_is_visible(
        "message.created",
        &in_private_channel,
        viewer,
        true,
        &channels
    ));

    let own_inbox = json!({"member_id": viewer});
    let foreign_inbox = json!({"member_id": other_member});
    assert!(event_is_visible(
        "inbox.changed",
        &own_inbox,
        viewer,
        false,
        &channels
    ));
    assert!(!event_is_visible(
        "inbox.changed",
        &foreign_inbox,
        viewer,
        false,
        &channels
    ));
    assert!(event_is_visible(
        "inbox.changed",
        &foreign_inbox,
        viewer,
        true,
        &channels
    ));

    // agent.activity reaches readable scopes, but never crosses a Channel boundary.
    let agent_activity_in_readable = json!({"member_id": other_member, "channel_id": readable});
    let agent_activity_in_private = json!({"member_id": other_member, "channel_id": private});
    assert!(event_is_visible(
        "agent.activity",
        &agent_activity_in_readable,
        viewer,
        false,
        &channels
    ));
    assert!(!event_is_visible(
        "agent.activity",
        &agent_activity_in_private,
        viewer,
        false,
        &channels
    ));
    assert!(event_is_visible(
        "agent.activity",
        &json!({"member_id": other_member, "scope_channel_id": readable}),
        viewer,
        false,
        &channels
    ));
    assert!(!event_is_visible(
        "agent.activity",
        &json!({"member_id": other_member, "scope_channel_id": private}),
        viewer,
        false,
        &channels
    ));

    // An event naming no Channel and no Member reaches every Space Member.
    assert!(event_is_visible(
        "task.updated",
        &json!({"resource_id": Uuid::now_v7()}),
        viewer,
        false,
        &channels
    ));
}

#[tokio::test]
async fn empty_database_builds_final_schema_with_concurrency_constraints() {
    let database = TestDatabase::create("sumi_server_adapter").await;
    let pool = database.pool.clone();
    let adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();

    let active_index: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM pg_indexes \
             WHERE indexname='agent_runs_one_active_per_agent' \
             AND indexdef LIKE '%WHERE (status <> ALL%')",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    let no_threads_table: bool = sqlx::query_scalar(
        "SELECT NOT EXISTS(SELECT 1 FROM information_schema.tables \
             WHERE table_schema='public' AND table_name='threads')",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    let messages_self_fk: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM pg_constraint c \
             JOIN pg_class r ON r.oid=c.conrelid \
             JOIN pg_class f ON f.oid=c.confrelid \
             WHERE r.relname='messages' AND f.relname='messages' AND c.contype='f')",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    let legacy_session_table: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM information_schema.tables \
             WHERE table_schema='public' AND table_name='sessions')",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    let slug_constraint: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM pg_constraint \
             WHERE conname='channels_slug_form_check')",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    let run_observations: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM information_schema.tables \
             WHERE table_name='run_observations')",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    let event_sequence_index: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM pg_indexes \
             WHERE indexname='outbox_events_space_sequence' \
             AND indexdef LIKE '%UNIQUE%')",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    let schema_version: i32 = sqlx::query_scalar("SELECT max(version) FROM schema_meta")
        .fetch_one(&pool)
        .await
        .unwrap();

    assert!(active_index);
    assert!(no_threads_table);
    assert!(messages_self_fk);
    assert!(!legacy_session_table);
    assert!(slug_constraint);
    assert!(run_observations);
    assert!(event_sequence_index);
    assert_eq!(schema_version, 14);

    // A database at any other version is refused instead of upgraded or downgraded.
    sqlx::raw_sql(
        "DELETE FROM schema_meta; INSERT INTO schema_meta(version,applied_at) VALUES (11,now());",
    )
    .execute(&pool)
    .await
    .unwrap();
    let older = adapter.initialize_schema().await.unwrap_err();
    assert!(
        older.to_string().contains("older than the supported"),
        "{older}"
    );
    sqlx::raw_sql(
        "DELETE FROM schema_meta; INSERT INTO schema_meta(version,applied_at) VALUES (15,now());",
    )
    .execute(&pool)
    .await
    .unwrap();
    let newer = adapter.initialize_schema().await.unwrap_err();
    assert!(
        newer
            .to_string()
            .contains("newer than this Server supports"),
        "{newer}"
    );
    sqlx::raw_sql(
        "DELETE FROM schema_meta; INSERT INTO schema_meta(version,applied_at) VALUES (14,now());",
    )
    .execute(&pool)
    .await
    .unwrap();
    pool.close().await;
    database.close().await;
}

#[tokio::test]
async fn channel_activity_query_rejects_another_computer_impersonating_an_agent() {
    let database = TestDatabase::create("sumi_channel_query_auth").await;
    let pool = database.pool.clone();
    let adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let space = Uuid::now_v7();
    let owner = Uuid::now_v7();
    let agent = Uuid::now_v7();
    let assigned_computer = Uuid::now_v7();
    let other_computer = Uuid::now_v7();
    let channel = Uuid::now_v7();
    let root = Uuid::now_v7();
    let run = Uuid::now_v7();
    let inbox_item = Uuid::now_v7();
    sqlx::raw_sql(&format!(
        "BEGIN;
         INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at)
             VALUES ('{space}','query-auth','Query Auth','#F0602F','{owner}',now());
         INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES
             ('{owner}','{space}','human','Owner','owner',now()),
             ('{agent}','{space}','agent','Agent','member',now());
         INSERT INTO computers(id,space_id,name,hostname,os,token_hash,connection_status,created_at)
             VALUES ('{assigned_computer}','{space}','Assigned','localhost','linux','assigned-hash','offline',now()),
                    ('{other_computer}','{space}','Other','localhost','linux','other-hash','offline',now());
         INSERT INTO agents(member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at)
             VALUES ('{agent}','{space}','{assigned_computer}','Act',1,'active','builtin',now());
         INSERT INTO channels(id,space_id,kind,slug,next_seq,created_at)
             VALUES ('{channel}','{space}','public','general',2,now());
         INSERT INTO channel_members(channel_id,space_id,member_id,joined_at)
             VALUES ('{channel}','{space}','{agent}',now());
         INSERT INTO messages(id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at)
             VALUES ('{root}','{space}','{channel}','{root}',1,'root','text','{agent}','safe body',now());
         INSERT INTO agent_runs(id,space_id,agent_id,focus_thread_id,status,trigger_kind,created_at)
             VALUES ('{run}','{space}','{agent}','{root}','dispatched','mention',now());
         INSERT INTO inbox_items(id,space_id,member_id,message_id,thread_id,kind,strength,status,available_at,assigned_run_id,created_at)
             VALUES ('{inbox_item}','{space}','{agent}','{root}','{root}','mention','hard','assigned',now(),'{run}',now());
         INSERT INTO run_items(run_id,inbox_item_id,delivery_seq,attached_at)
             VALUES ('{run}','{inbox_item}',1,now());
         COMMIT;"
    ))
    .execute(&pool)
    .await
    .unwrap();

    let result = adapter
        .channel_activity_snapshot(super::ChannelActivityQueryRequest {
            computer_id: ComputerId::from_uuid(other_computer),
            run_id: RunId::from_uuid(run),
            agent_id: MemberId::from_uuid(agent),
            channel_id: ChannelId::from_uuid(channel),
            after_sequence: 0,
            through_sequence: 1,
            limit: 10,
        })
        .await;
    let error = result.err().expect("foreign computer must be rejected");
    assert_eq!(error, ApplicationError::PermissionDenied);
    pool.close().await;
    database.close().await;
}

#[tokio::test]
async fn run_item_diagnostic_columns_reject_partial_receipts_and_dispositions() {
    let database = TestDatabase::create("sumi_run_item_constraints").await;
    let pool = database.pool.clone();
    let adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let space = Uuid::now_v7();
    let owner = Uuid::now_v7();
    let agent = Uuid::now_v7();
    let computer = Uuid::now_v7();
    let channel = Uuid::now_v7();
    let root = Uuid::now_v7();
    let run = Uuid::now_v7();
    let inbox_item = Uuid::now_v7();
    sqlx::raw_sql(&format!(
        "BEGIN;
         INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at)
             VALUES ('{space}','run-item-constraints','Run Item Constraints','#F0602F','{owner}',now());
         INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES
             ('{owner}','{space}','human','Owner','owner',now()),
             ('{agent}','{space}','agent','Agent','member',now());
         INSERT INTO computers(id,space_id,name,hostname,os,token_hash,connection_status,created_at)
             VALUES ('{computer}','{space}','Computer','localhost','linux','computer-hash','offline',now());
         INSERT INTO agents(member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at)
             VALUES ('{agent}','{space}','{computer}','Act',1,'active','builtin',now());
         INSERT INTO channels(id,space_id,kind,slug,next_seq,created_at)
             VALUES ('{channel}','{space}','public','general',2,now());
         INSERT INTO channel_members(channel_id,space_id,member_id,joined_at)
             VALUES ('{channel}','{space}','{agent}',now());
         INSERT INTO messages(id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at)
             VALUES ('{root}','{space}','{channel}','{root}',1,'root','text','{agent}','safe body',now());
         INSERT INTO agent_runs(id,space_id,agent_id,focus_thread_id,status,trigger_kind,created_at)
             VALUES ('{run}','{space}','{agent}','{root}','dispatched','mention',now());
         INSERT INTO inbox_items(id,space_id,member_id,message_id,thread_id,kind,strength,status,available_at,assigned_run_id,created_at)
             VALUES ('{inbox_item}','{space}','{agent}','{root}','{root}','mention','hard','assigned',now(),'{run}',now());
         INSERT INTO run_items(run_id,inbox_item_id,delivery_seq,attached_at)
             VALUES ('{run}','{inbox_item}',1,now());
         COMMIT;"
    ))
    .execute(&pool)
    .await
    .unwrap();

    for assignment in [
        "delivery_outcome='accepted'".to_owned(),
        format!("delivery_event_id='{}'", Uuid::now_v7()),
        "delivery_receipt_at=now()".to_owned(),
        "disposition='handled'".to_owned(),
        "disposition_at=now()".to_owned(),
    ] {
        let update = format!(
            "UPDATE run_items SET {assignment} WHERE run_id='{run}' AND inbox_item_id='{inbox_item}'"
        );
        assert!(
            sqlx::query(&update).execute(&pool).await.is_err(),
            "invalid run item half-state must be rejected: {assignment}"
        );
    }
    pool.close().await;
    database.close().await;
}

#[tokio::test]
async fn mention_all_expands_active_channel_members_and_deduplicates_agents() {
    let database = TestDatabase::create("sumi_mention_all").await;
    let pool = database.pool.clone();
    let mut adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let space = Uuid::now_v7();
    let owner = Uuid::now_v7();
    let agent = Uuid::now_v7();
    let second_agent = Uuid::now_v7();
    let space_only = Uuid::now_v7();
    let computer = Uuid::now_v7();
    let channel = Uuid::now_v7();
    let root = Uuid::now_v7();
    sqlx::raw_sql(&format!(
        "BEGIN;
         INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at) VALUES ('{space}','mention-all','Mention All','#F0602F','{owner}',now());
         INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES
           ('{owner}','{space}','human','Owner','owner',now()),
           ('{agent}','{space}','agent','Agent','member',now()),
           ('{second_agent}','{space}','agent','Second','member',now()),
           ('{space_only}','{space}','human','SpaceOnly','member',now());
         INSERT INTO computers(id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer}','{space}','Computer','localhost','linux','mention-all-hash','offline',1,now());
         INSERT INTO agents(member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES
           ('{agent}','{space}','{computer}','Act',1,'active','builtin',now()),
           ('{second_agent}','{space}','{computer}','Act',1,'active','builtin',now());
         INSERT INTO channels(id,space_id,kind,slug,next_seq,created_at) VALUES ('{channel}','{space}','public','general',2,now());
         INSERT INTO channel_members(channel_id,space_id,member_id,joined_at) VALUES
           ('{channel}','{space}','{owner}',now()),('{channel}','{space}','{agent}',now()),('{channel}','{space}','{second_agent}',now());
         INSERT INTO messages(id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES ('{root}','{space}','{channel}','{root}',1,'root','text','{owner}','source',now());
         COMMIT;"
    ))
    .execute(&pool)
    .await
    .unwrap();
    let message = MessageId::from_uuid(Uuid::now_v7());
    adapter
        .transact(async |transaction| {
            transaction
                .publish_message(MessageDraft {
                    message_id: message,
                    channel_id: ChannelId::from_uuid(channel),
                    author_member_id: MemberId::from_uuid(owner),
                    idempotency_key: IdempotencyKey::from_uuid(Uuid::now_v7()),
                    thread_id: Some(ThreadId::from_uuid(root)),
                    reply_to_message_id: None,
                    body_markdown: "@all @agent".into(),
                    mentions: vec![MemberId::from_uuid(agent), MemberId::from_uuid(agent)],
                    mention_all: true,
                    attachment_ids: Vec::new(),
                    handled_item: None,
                    now: OffsetDateTime::now_utc(),
                })
                .await
        })
        .await
        .unwrap();
    let facts: (i64, i64, bool) = sqlx::query_as(
        "SELECT (SELECT count(*) FROM message_mentions WHERE message_id=$1),
                (SELECT count(*) FROM inbox_items WHERE message_id=$1 AND kind='mention'),
                (SELECT mention_all FROM messages WHERE id=$1)",
    )
    .bind(message.into_uuid())
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(facts, (2, 2, true));
    let targets: Vec<Uuid> =
        sqlx::query_scalar("SELECT member_id FROM message_mentions WHERE message_id=$1")
            .bind(message.into_uuid())
            .fetch_all(&pool)
            .await
            .unwrap();
    assert!(!targets.contains(&owner));
    assert!(targets.contains(&agent));
    assert!(targets.contains(&second_agent));

    let run_id = RunId::from_uuid(Uuid::now_v7());
    sqlx::query(
        "INSERT INTO agent_runs(id,space_id,agent_id,focus_thread_id,status,trigger_kind,created_at) \
         VALUES($1,$2,$3,$4,'dispatched','mention',now())",
    )
    .bind(run_id.into_uuid())
    .bind(space)
    .bind(agent)
    .bind(root)
    .execute(&pool)
    .await
    .unwrap();
    let start = adapter
        .transact(async |transaction| transaction.run_start(run_id).await)
        .await
        .unwrap();
    let run_member_ids = start
        .channel_members
        .iter()
        .map(|member| member.member_id.into_uuid())
        .collect::<std::collections::BTreeSet<_>>();
    assert_eq!(run_member_ids.len(), 3);
    assert!(run_member_ids.contains(&owner));
    assert!(run_member_ids.contains(&agent));
    assert!(run_member_ids.contains(&second_agent));
    assert!(!run_member_ids.contains(&space_only));
    pool.close().await;
    database.close().await;
}

#[tokio::test]
async fn agent_messages_do_not_create_ambient_items_for_other_agents() {
    let admin_url = std::env::var("SUMI_TEST_DATABASE_URL")
        .unwrap_or_else(|_| "postgres://localhost/postgres".to_owned());
    let database_name = format!("sumi_agent_ambient_{}", Uuid::now_v7().simple());
    let mut admin = PgConnection::connect_with(&PgConnectOptions::from_str(&admin_url).unwrap())
        .await
        .unwrap();
    sqlx::query(&format!("CREATE DATABASE \"{database_name}\""))
        .execute(&mut admin)
        .await
        .unwrap();
    let mut database_url = Url::parse(&admin_url).unwrap();
    database_url.set_path(&format!("/{database_name}"));

    let result = async {
        let pool = PgPool::connect(database_url.as_str()).await.unwrap();
        let mut adapter = PostgresAdapter::new(pool.clone());
        adapter.initialize_schema().await.unwrap();
        let space = Uuid::now_v7();
        let owner = Uuid::now_v7();
        let author_agent = Uuid::now_v7();
        let recipient_agent = Uuid::now_v7();
        let subscribed_agent = Uuid::now_v7();
        let computer = Uuid::now_v7();
        let channel = Uuid::now_v7();
        let root = Uuid::now_v7();
        sqlx::raw_sql(&format!(
            "BEGIN;
             INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at) VALUES ('{space}','agent-ambient','Agent Ambient','#FE7DA8','{owner}',now());
             INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES
               ('{owner}','{space}','human','Owner','owner',now()),
               ('{author_agent}','{space}','agent','Author','member',now()),
               ('{recipient_agent}','{space}','agent','Recipient','member',now()),
               ('{subscribed_agent}','{space}','agent','Subscriber','member',now());
             INSERT INTO computers(id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer}','{space}','Computer','localhost','linux','agent-ambient-hash','offline',1,now());
             INSERT INTO agents(member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES
               ('{author_agent}','{space}','{computer}','Act',1,'active','builtin',now()),
               ('{recipient_agent}','{space}','{computer}','Act',1,'active','builtin',now()),
               ('{subscribed_agent}','{space}','{computer}','Act',1,'active','builtin',now());
             INSERT INTO channels(id,space_id,kind,slug,next_seq,created_at) VALUES ('{channel}','{space}','public','general',2,now());
             INSERT INTO channel_members(channel_id,space_id,member_id,joined_at) VALUES
               ('{channel}','{space}','{owner}',now()),
               ('{channel}','{space}','{author_agent}',now()),
               ('{channel}','{space}','{recipient_agent}',now()),
               ('{channel}','{space}','{subscribed_agent}',now());
             INSERT INTO messages(id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES ('{root}','{space}','{channel}','{root}',1,'root','text','{owner}','source',now());
             INSERT INTO thread_subscriptions(thread_id,space_id,member_id,created_at) VALUES ('{root}','{space}','{subscribed_agent}',now());
             COMMIT;"
        ))
        .execute(&pool)
        .await
        .unwrap();

        let ordinary_message = MessageId::from_uuid(Uuid::now_v7());
        adapter
            .transact(async |transaction| {
                transaction
                    .publish_message(MessageDraft {
                        message_id: ordinary_message,
                        channel_id: ChannelId::from_uuid(channel),
                        author_member_id: MemberId::from_uuid(author_agent),
                        idempotency_key: IdempotencyKey::from_uuid(Uuid::now_v7()),
                        body_markdown: "routine progress".into(),
                        thread_id: Some(ThreadId::from_uuid(root)),
                        reply_to_message_id: None,
                        mentions: Vec::new(),
                        mention_all: false,
                        attachment_ids: Vec::new(),
                        handled_item: None,
                        now: OffsetDateTime::now_utc(),
                    })
                    .await
            })
            .await
            .unwrap();
        let ordinary_items: i64 = sqlx::query_scalar(
            "SELECT count(*) FROM inbox_items WHERE message_id=$1 OR (message_id IS NULL AND member_id IN ($2,$3))",
        )
        .bind(ordinary_message.into_uuid())
        .bind(recipient_agent)
        .bind(subscribed_agent)
        .fetch_one(&pool)
        .await
        .unwrap();
        assert_eq!(ordinary_items, 0);

        let addressed_message = MessageId::from_uuid(Uuid::now_v7());
        adapter
            .transact(async |transaction| {
                transaction
                    .publish_message(MessageDraft {
                        message_id: addressed_message,
                        channel_id: ChannelId::from_uuid(channel),
                        author_member_id: MemberId::from_uuid(author_agent),
                        idempotency_key: IdempotencyKey::from_uuid(Uuid::now_v7()),
                        body_markdown: "@Recipient please review".into(),
                        thread_id: Some(ThreadId::from_uuid(root)),
                        reply_to_message_id: None,
                        mentions: vec![MemberId::from_uuid(recipient_agent)],
                        mention_all: false,
                        attachment_ids: Vec::new(),
                        handled_item: None,
                        now: OffsetDateTime::now_utc(),
                    })
                    .await
            })
            .await
            .unwrap();
        let addressed: (Uuid, String, String) = sqlx::query_as(
            "SELECT member_id,kind,strength FROM inbox_items WHERE message_id=$1",
        )
        .bind(addressed_message.into_uuid())
        .fetch_one(&pool)
        .await
        .unwrap();
        assert_eq!(addressed, (recipient_agent, "mention".into(), "hard".into()));
        pool.close().await;
    }
    .await;
    sqlx::query(&format!("DROP DATABASE \"{database_name}\" WITH (FORCE)"))
        .execute(&mut admin)
        .await
        .unwrap();
    result
}

#[tokio::test]
async fn failing_an_orphaned_run_unblocks_the_agent_and_subscription_raises_thread_activity() {
    let admin_url = std::env::var("SUMI_TEST_DATABASE_URL")
        .unwrap_or_else(|_| "postgres://localhost/postgres".to_owned());
    let database_name = format!("sumi_orphan_sync_{}", Uuid::now_v7().simple());
    let mut admin = PgConnection::connect_with(&PgConnectOptions::from_str(&admin_url).unwrap())
        .await
        .unwrap();
    sqlx::query(&format!("CREATE DATABASE \"{database_name}\""))
        .execute(&mut admin)
        .await
        .unwrap();
    let mut database_url = Url::parse(&admin_url).unwrap();
    database_url.set_path(&format!("/{database_name}"));

    let result = async {
            let pool = PgPool::connect(database_url.as_str()).await.unwrap();
            let mut adapter = PostgresAdapter::new(pool.clone());
            adapter.initialize_schema().await.unwrap();
            let space = Uuid::now_v7();
            let owner = Uuid::now_v7();
            let agent = Uuid::now_v7();
            let subscriber = Uuid::now_v7();
            let computer_id = Uuid::now_v7();
            let channel = Uuid::now_v7();
            let root = Uuid::now_v7();
            let stale_run = Uuid::now_v7();
            let stale_item = Uuid::now_v7();
            let pending_run = Uuid::now_v7();
            let pending_start_command = Uuid::now_v7();
            let stale_notice_command = Uuid::now_v7();
            let stale_notice_id = Uuid::now_v7();
            sqlx::raw_sql(&format!(
                "BEGIN;
                 INSERT INTO spaces (id,slug,name,accent,owner_member_id,created_at) VALUES ('{space}','space','Space','#F0602F','{owner}',now());
                 INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{owner}','{space}','human','Owner','owner',now());
                 INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{agent}','{space}','agent','Lin','member',now());
                 INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{subscriber}','{space}','agent','Ada','member',now());
                 INSERT INTO computers (id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer_id}','{space}','Computer','localhost','linux','hash','offline',3,now());
                 INSERT INTO agents (member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES ('{agent}','{space}','{computer_id}','Act',1,'active','builtin',now());
                 INSERT INTO agents (member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES ('{subscriber}','{space}','{computer_id}','Watch',1,'active','builtin',now());
                 INSERT INTO channels (id,space_id,kind,slug,next_seq,created_at) VALUES ('{channel}','{space}','public','general',2,now());
                 INSERT INTO channel_members (channel_id,space_id,member_id,joined_at) VALUES ('{channel}','{space}','{owner}',now()),('{channel}','{space}','{agent}',now()),('{channel}','{space}','{subscriber}',now());
                 INSERT INTO messages (id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES ('{root}','{space}','{channel}','{root}',1,'root','text','{owner}','source',now());
                 INSERT INTO thread_subscriptions (thread_id,space_id,member_id,created_at) VALUES ('{root}','{space}','{subscriber}',now());
                 INSERT INTO agent_runs (id,space_id,agent_id,focus_thread_id,status,trigger_kind,created_at,started_at) VALUES ('{stale_run}','{space}','{agent}','{root}','working','mention',now(),now());
                 INSERT INTO inbox_items (id,space_id,member_id,message_id,thread_id,kind,strength,status,available_at,assigned_run_id,retry_count,created_at) VALUES ('{stale_item}','{space}','{agent}','{root}','{root}','mention','hard','assigned',now(),'{stale_run}',0,now());
                 INSERT INTO run_items (run_id,inbox_item_id,delivery_seq,attached_at) VALUES ('{stale_run}','{stale_item}',1,now());
                 INSERT INTO agent_runs (id,space_id,agent_id,focus_thread_id,status,trigger_kind,created_at) VALUES ('{pending_run}','{space}','{subscriber}','{root}','dispatched','mention',now());
                 INSERT INTO computer_commands (id,computer_id,computer_seq,kind,payload_json,created_at) VALUES ('{pending_start_command}','{computer_id}',1,'run.start',jsonb_build_object('kind','run.start','payload',jsonb_build_object('run_id','{pending_run}')),now());
                 INSERT INTO computer_commands (id,computer_id,computer_seq,kind,payload_json,created_at) VALUES ('{stale_notice_command}','{computer_id}',2,'run.notice',jsonb_build_object('kind','run.notice','payload',jsonb_build_object('run_id','{stale_run}','notice',jsonb_build_object('notice_id','{stale_notice_id}'))),now());
                 COMMIT;"
            ))
            .execute(&pool)
            .await
            .unwrap();

            let reclaimed = SyncComputerRuns::execute(
                &mut adapter,
                SyncComputerRunsInput {
                    computer_id: ComputerId::from_uuid(computer_id),
                    // The Computer reconnected holding nothing, so the stale Run is gone with it.
                    live_run_ids: Vec::new(),
                    max_retry_count: 5,
                    now: OffsetDateTime::now_utc(),
                },
            )
            .await
            .unwrap();
            assert_eq!(reclaimed.runs_failed, 1);
            assert_eq!(reclaimed.items_released, 1);

            // Commands targeting the failed orphaned Run are settled; the pending Run's start
            // command stays queued for the reconnecting Computer.
            let stale_notice_acked: bool = sqlx::query_scalar(
                "SELECT acked_at IS NOT NULL FROM computer_commands WHERE id=$1",
            )
            .bind(stale_notice_command)
            .fetch_one(&pool)
            .await
            .unwrap();
            assert!(stale_notice_acked);
            let pending_start_acked: bool = sqlx::query_scalar(
                "SELECT acked_at IS NOT NULL FROM computer_commands WHERE id=$1",
            )
            .bind(pending_start_command)
            .fetch_one(&pool)
            .await
            .unwrap();
            assert!(!pending_start_acked);

            let recovered: (String, String, String, i32) = sqlx::query_as(
                "SELECT r.status,r.outcome_code,i.status,i.retry_count \
                 FROM agent_runs r JOIN inbox_items i ON i.id=$2 WHERE r.id=$1",
            )
            .bind(stale_run)
            .bind(stale_item)
            .fetch_one(&pool)
            .await
            .unwrap();
            assert_eq!(
                recovered,
                ("failed".into(), "failed".into(), "pending".into(), 1)
            );
            let pending_status: String = sqlx::query_scalar(
                "SELECT status FROM agent_runs WHERE id=$1",
            )
            .bind(pending_run)
            .fetch_one(&pool)
            .await
            .unwrap();
            assert_eq!(pending_status, "dispatched");

            // The Run is terminal, so the partial unique index now admits a new Run for that Agent.
            sqlx::query(
                "INSERT INTO agent_runs (id,space_id,agent_id,focus_thread_id,status,\
                 trigger_kind,created_at) \
                 VALUES ($1,$2,$3,$4,'dispatched','mention',now())",
            )
            .bind(Uuid::now_v7())
            .bind(space)
            .bind(agent)
            .bind(root)
            .execute(&pool)
            .await
            .expect("failing the abandoned Run unblocks the Agent");

            // A reply routes thread_activity to the subscriber and channel_activity to the rest.
            adapter
                .transact(async |transaction| {
                    transaction
                        .publish_message(MessageDraft {
                            message_id: MessageId::from_uuid(Uuid::now_v7()),
                            channel_id: ChannelId::from_uuid(channel),
                            author_member_id: MemberId::from_uuid(owner),
                            idempotency_key: IdempotencyKey::from_uuid(Uuid::now_v7()),
                            body_markdown: "reply".into(),
                            thread_id: Some(ThreadId::from_uuid(root)),
                            reply_to_message_id: None,
                            mentions: Vec::new(),
                            mention_all: false,
                            attachment_ids: Vec::new(),
                            handled_item: None,
                            now: OffsetDateTime::now_utc(),
                        })
                        .await
                })
                .await
                .unwrap();
            let routed: (String, String) = sqlx::query_as(
                "SELECT (SELECT kind FROM inbox_items WHERE member_id=$1 ORDER BY created_at DESC LIMIT 1), \
                        (SELECT kind FROM inbox_items WHERE member_id=$2 ORDER BY created_at DESC LIMIT 1)",
            )
            .bind(subscriber)
            .bind(agent)
            .fetch_one(&pool)
            .await
            .unwrap();
            assert_eq!(routed, ("thread_activity".into(), "channel_activity".into()));

            // Exhausting the retry budget retires the Item, and a governor's requeue must make it
            // claimable again rather than leaving it to be retired on the next expiry.
            let retired_run = Uuid::now_v7();
            sqlx::raw_sql(&format!(
                "BEGIN;
                 UPDATE inbox_items SET status='dead',assigned_run_id=NULL,retry_count=5 WHERE id='{stale_item}';
                 INSERT INTO agent_runs (id,space_id,agent_id,focus_thread_id,status,trigger_kind,outcome_code,created_at,started_at,finished_at) VALUES ('{retired_run}','{space}','{agent}','{root}','completed','mention','completed',now(),now(),now());
                 COMMIT;"
            ))
            .execute(&pool)
            .await
            .unwrap();
            // Terminate the Run inserted above so the Agent has capacity to take work again.
            sqlx::query(
                "UPDATE agent_runs SET status='canceled',outcome_code='canceled',finished_at=now() \
                 WHERE agent_id=$1 AND status='dispatched'",
            )
            .bind(agent)
            .execute(&pool)
            .await
            .unwrap();

            let requeued = RequeueDeadItem::execute(
                &mut adapter,
                RequeueDeadItemInput {
                    idempotency_key: crate::ids::IdempotencyKey::from_uuid(Uuid::now_v7()),
                    item_id: InboxItemId::from_uuid(stale_item),
                    actor_id: MemberId::from_uuid(owner),
                    now: OffsetDateTime::now_utc(),
                },
            )
            .await
            .expect("the Space Owner returns a retired Item to the queue");
            assert_eq!(requeued.status, InboxItemStatus::Pending);
            assert_eq!((requeued.retry_count, requeued.requeue_count), (0, 1));

            // A fresh retry budget is what makes the requeue useful, so the Item must survive a claim.
            DispatchRun::execute(
                &mut adapter,
                DispatchRunInput {
                    run_id: RunId::from_uuid(Uuid::now_v7()),
                    agent_id: MemberId::from_uuid(agent),
                    task_id: None,
                    focus_thread_id: ThreadId::from_uuid(root),
                    trigger: RunTrigger::Mention,
                    item_ids: vec![InboxItemId::from_uuid(stale_item)],
                },
            )
            .await
            .expect("a requeued Item can be claimed again");

            // An Item already back in the queue is not a retirement to undo.
            assert!(
                RequeueDeadItem::execute(
                    &mut adapter,
                    RequeueDeadItemInput {
                        idempotency_key: crate::ids::IdempotencyKey::from_uuid(Uuid::now_v7()),
                        item_id: InboxItemId::from_uuid(stale_item),
                        actor_id: MemberId::from_uuid(owner),
                        now: OffsetDateTime::now_utc(),
                    },
                )
                .await
                .is_err()
            );

            let audited: i64 = sqlx::query_scalar(
                "SELECT count(*) FROM audit_events \
                 WHERE action='inbox_item.requeued' AND subject_type='inbox_item' AND subject_id=$1",
            )
            .bind(stale_item)
            .fetch_one(&pool)
            .await
            .unwrap();
            assert_eq!(audited, 1, "only the accepted requeue is audited");

            pool.close().await;
        }
        .await;

    sqlx::query(&format!("DROP DATABASE \"{database_name}\" WITH (FORCE)"))
        .execute(&mut admin)
        .await
        .unwrap();
    result
}

/// Terminal Run results settle queued commands targeting that Run, so a reconnect or a later
/// attention router cannot replay an obsolete start/attach/notice/stop to the Computer.
#[tokio::test]
async fn completing_a_run_settles_its_pending_commands() {
    let admin_url = std::env::var("SUMI_TEST_DATABASE_URL")
        .unwrap_or_else(|_| "postgres://localhost/postgres".to_owned());
    let database_name = format!("sumi_complete_settles_{}", Uuid::now_v7().simple());
    let mut admin = PgConnection::connect_with(&PgConnectOptions::from_str(&admin_url).unwrap())
        .await
        .unwrap();
    sqlx::query(&format!("CREATE DATABASE \"{database_name}\""))
        .execute(&mut admin)
        .await
        .unwrap();
    let mut database_url = Url::parse(&admin_url).unwrap();
    database_url.set_path(&format!("/{database_name}"));

    let result = async {
            let pool = PgPool::connect(database_url.as_str()).await.unwrap();
            let mut adapter = PostgresAdapter::new(pool.clone());
            adapter.initialize_schema().await.unwrap();
            let space = Uuid::now_v7();
            let owner = Uuid::now_v7();
            let agent = Uuid::now_v7();
            let computer_id = Uuid::now_v7();
            let channel = Uuid::now_v7();
            let root = Uuid::now_v7();
            let run_id = Uuid::now_v7();
            let item = Uuid::now_v7();
            let attach_command = Uuid::now_v7();
            let notice_command = Uuid::now_v7();
            let stop_command = Uuid::now_v7();
            let notice_id = Uuid::now_v7();
            sqlx::raw_sql(&format!(
                "BEGIN;
                 INSERT INTO spaces (id,slug,name,accent,owner_member_id,created_at) VALUES ('{space}','space','Space','#F0602F','{owner}',now());
                 INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{owner}','{space}','human','Owner','owner',now());
                 INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{agent}','{space}','agent','Lin','member',now());
                 INSERT INTO computers (id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer_id}','{space}','Computer','localhost','linux','hash','online',4,now());
                 INSERT INTO agents (member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES ('{agent}','{space}','{computer_id}','Act',1,'active','builtin',now());
                 INSERT INTO channels (id,space_id,kind,slug,next_seq,created_at) VALUES ('{channel}','{space}','public','general',2,now());
                 INSERT INTO channel_members (channel_id,space_id,member_id,joined_at) VALUES ('{channel}','{space}','{owner}',now()),('{channel}','{space}','{agent}',now());
                 INSERT INTO messages (id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES ('{root}','{space}','{channel}','{root}',1,'root','text','{owner}','source',now());
                 INSERT INTO agent_runs (id,space_id,agent_id,focus_thread_id,status,trigger_kind,created_at,started_at) VALUES ('{run_id}','{space}','{agent}','{root}','working','mention',now(),now());
                 INSERT INTO inbox_items (id,space_id,member_id,message_id,thread_id,kind,strength,status,available_at,assigned_run_id,retry_count,created_at) VALUES ('{item}','{space}','{agent}','{root}','{root}','mention','hard','assigned',now(),'{run_id}',0,now());
                 INSERT INTO run_items (run_id,inbox_item_id,delivery_seq,attached_at) VALUES ('{run_id}','{item}',1,now());
                 INSERT INTO computer_commands (id,computer_id,computer_seq,kind,payload_json,created_at) VALUES ('{attach_command}','{computer_id}',1,'run.attach_item',jsonb_build_object('kind','run.attach_item','payload',jsonb_build_object('run_id','{run_id}','delivery_sequence',2)),now());
                 INSERT INTO computer_commands (id,computer_id,computer_seq,kind,payload_json,created_at) VALUES ('{notice_command}','{computer_id}',2,'run.notice',jsonb_build_object('kind','run.notice','payload',jsonb_build_object('run_id','{run_id}','notice',jsonb_build_object('notice_id','{notice_id}'))),now());
                 INSERT INTO computer_commands (id,computer_id,computer_seq,kind,payload_json,created_at) VALUES ('{stop_command}','{computer_id}',3,'run.stop',jsonb_build_object('kind','run.stop','payload',jsonb_build_object('run_id','{run_id}')),now());
                 COMMIT;"
            ))
            .execute(&pool)
            .await
            .unwrap();

            CompleteRun::execute(
                &mut adapter,
                CompleteRunInput {
                    event_id: EventId::from_uuid(Uuid::now_v7()),
                    run_id: RunId::from_uuid(run_id),
                    computer_id: ComputerId::from_uuid(computer_id),
                    outcome: RunOutcome::Completed,
                    error_code: None,
                    item_dispositions: vec![ItemDispositionInput {
                        item_id: InboxItemId::from_uuid(item),
                        disposition: InboxItemDisposition::Handled,
                    }],
                    continuation_note: None,
                    max_retry_count: 5,
                    now: OffsetDateTime::now_utc(),
                },
            )
            .await
            .unwrap();

            let unacked: i64 = sqlx::query_scalar(
                "SELECT count(*) FROM computer_commands \
                 WHERE computer_id=$1 AND kind IN ('run.start','run.attach_item','run.notice','run.task_bound','run.stop') \
                   AND acked_at IS NULL",
            )
            .bind(computer_id)
            .fetch_one(&pool)
            .await
            .unwrap();
            assert_eq!(unacked, 0, "terminal Run must not leave replayable commands");

            pool.close().await;
        }
        .await;

    sqlx::query(&format!("DROP DATABASE \"{database_name}\" WITH (FORCE)"))
        .execute(&mut admin)
        .await
        .unwrap();
    result
}

/// Replay drops and settles commands queued for a Run that is already terminal. This closes the
/// race where the attention router queues a notice just as the Run result commits.
#[tokio::test]
async fn replay_skips_commands_for_terminal_runs() {
    let admin_url = std::env::var("SUMI_TEST_DATABASE_URL")
        .unwrap_or_else(|_| "postgres://localhost/postgres".to_owned());
    let database_name = format!("sumi_replay_stale_{}", Uuid::now_v7().simple());
    let mut admin = PgConnection::connect_with(&PgConnectOptions::from_str(&admin_url).unwrap())
        .await
        .unwrap();
    sqlx::query(&format!("CREATE DATABASE \"{database_name}\""))
        .execute(&mut admin)
        .await
        .unwrap();
    let mut database_url = Url::parse(&admin_url).unwrap();
    database_url.set_path(&format!("/{database_name}"));

    let result = async {
            let pool = PgPool::connect(database_url.as_str()).await.unwrap();
            let mut adapter = PostgresAdapter::new(pool.clone());
            adapter.initialize_schema().await.unwrap();
            let space = Uuid::now_v7();
            let owner = Uuid::now_v7();
            let agent = Uuid::now_v7();
            let computer_id = Uuid::now_v7();
            let channel = Uuid::now_v7();
            let root = Uuid::now_v7();
            let terminal_run = Uuid::now_v7();
            let live_run = Uuid::now_v7();
            sqlx::raw_sql(&format!(
                "BEGIN;
                 INSERT INTO spaces (id,slug,name,accent,owner_member_id,created_at) VALUES ('{space}','space','Space','#F0602F','{owner}',now());
                 INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{owner}','{space}','human','Owner','owner',now());
                 INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{agent}','{space}','agent','Lin','member',now());
                 INSERT INTO computers (id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer_id}','{space}','Computer','localhost','linux','hash','offline',3,now());
                 INSERT INTO agents (member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES ('{agent}','{space}','{computer_id}','Act',1,'active','builtin',now());
                 INSERT INTO channels (id,space_id,kind,slug,next_seq,created_at) VALUES ('{channel}','{space}','public','general',2,now());
                 INSERT INTO channel_members (channel_id,space_id,member_id,joined_at) VALUES ('{channel}','{space}','{owner}',now()),('{channel}','{space}','{agent}',now());
                 INSERT INTO messages (id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES ('{root}','{space}','{channel}','{root}',1,'root','text','{owner}','source',now());
                 INSERT INTO agent_runs (id,space_id,agent_id,focus_thread_id,status,trigger_kind,outcome_code,created_at,started_at,finished_at) VALUES ('{terminal_run}','{space}','{agent}','{root}','completed','mention','completed',now(),now(),now());
                 INSERT INTO agent_runs (id,space_id,agent_id,focus_thread_id,status,trigger_kind,created_at) VALUES ('{live_run}','{space}','{agent}','{root}','dispatched','mention',now());
                 COMMIT;"
            ))
            .execute(&pool)
            .await
            .unwrap();
            adapter
                .transact(async |transaction| {
                    transaction
                        .queue_command(
                            ComputerId::from_uuid(computer_id),
                            Command::RunNotice(RunNotice {
                                run_id: RunId::from_uuid(terminal_run),
                                notice: AttentionNotice {
                                    notice_id: NoticeId::from_uuid(Uuid::now_v7()),
                                    source_kind: InboxSourceKind::Mention,
                                    strength: WireAttentionStrength::Hard,
                                    location: NoticeLocation::Visible {
                                        task_id: None,
                                        thread_id: ThreadId::from_uuid(root),
                                    },
                                    explicit_human_redirect: false,
                                    arrived_at: OffsetDateTime::now_utc(),
                                },
                            }),
                        )
                        .await?;
                    transaction
                        .queue_command(
                            ComputerId::from_uuid(computer_id),
                            Command::RunStop(RunStop {
                                run_id: RunId::from_uuid(live_run),
                                reason: StopReason::Suspend,
                            }),
                        )
                        .await?;
                    Ok(())
                })
                .await
                .unwrap();

            let replayed = crate::server::adapters::websocket::replay_commands(
                &adapter,
                ComputerId::from_uuid(computer_id),
                CommandSequence(0),
            )
            .await
            .unwrap();
            assert_eq!(replayed.len(), 1);
            let Command::RunStop(stop) = &replayed[0].command else {
                panic!("expected run.stop command")
            };
            assert_eq!(stop.run_id, RunId::from_uuid(live_run));
            assert_eq!(stop.reason, StopReason::Suspend);
            let pending: Vec<(String, Uuid)> = sqlx::query_as(
                "SELECT kind,(payload_json #>> '{payload,run_id}')::uuid \
                 FROM computer_commands WHERE computer_id=$1 AND acked_at IS NULL",
            )
            .bind(computer_id)
            .fetch_all(&pool)
            .await
            .unwrap();
            assert_eq!(
                pending,
                vec![("run.stop".to_owned(), live_run)],
                "the terminal Run's notice is settled; only the live Run's command replays"
            );

            pool.close().await;
        }
        .await;

    sqlx::query(&format!("DROP DATABASE \"{database_name}\" WITH (FORCE)"))
        .execute(&mut admin)
        .await
        .unwrap();
    result
}

/// Concurrent ambient publishers must not lose count or force-time updates, and the schema must
/// refuse a second open aggregate for one Agent and Thread. Both are SQL-level guarantees, so this
/// runs against real PostgreSQL.
#[tokio::test]
async fn concurrent_ambient_messages_accumulate_into_one_bounded_aggregate() {
    let admin_url = std::env::var("SUMI_TEST_DATABASE_URL")
        .unwrap_or_else(|_| "postgres://localhost/postgres".to_owned());
    let database_name = format!("sumi_ambient_aggregate_{}", Uuid::now_v7().simple());
    let mut admin = PgConnection::connect_with(&PgConnectOptions::from_str(&admin_url).unwrap())
        .await
        .unwrap();
    sqlx::query(&format!("CREATE DATABASE \"{database_name}\""))
        .execute(&mut admin)
        .await
        .unwrap();
    let mut database_url = Url::parse(&admin_url).unwrap();
    database_url.set_path(&format!("/{database_name}"));

    let result = async {
            let pool = PgPool::connect(database_url.as_str()).await.unwrap();
            PostgresAdapter::new(pool.clone())
                .initialize_schema()
                .await
                .unwrap();
            let space = Uuid::now_v7();
            let owner = Uuid::now_v7();
            let agent = Uuid::now_v7();
            let computer_id = Uuid::now_v7();
            let channel = Uuid::now_v7();
            let root = Uuid::now_v7();
            let second_root = Uuid::now_v7();
            sqlx::raw_sql(&format!(
                "BEGIN;
                 INSERT INTO spaces (id,slug,name,accent,owner_member_id,created_at) VALUES ('{space}','space','Space','#F0602F','{owner}',now());
                 INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{owner}','{space}','human','Owner','owner',now());
                 INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{agent}','{space}','agent','Lin','member',now());
                 INSERT INTO computers (id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer_id}','{space}','Computer','localhost','linux','hash','offline',1,now());
                 INSERT INTO agents (member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES ('{agent}','{space}','{computer_id}','Act',1,'active','builtin',now());
                 INSERT INTO channels (id,space_id,kind,slug,next_seq,created_at) VALUES ('{channel}','{space}','public','general',3,now());
                 INSERT INTO channel_members (channel_id,space_id,member_id,joined_at) VALUES ('{channel}','{space}','{owner}',now()),('{channel}','{space}','{agent}',now());
                 INSERT INTO messages (id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES ('{root}','{space}','{channel}','{root}',1,'root','text','{owner}','source',now());
                 INSERT INTO messages (id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES ('{second_root}','{space}','{channel}','{second_root}',2,'root','text','{owner}','second source',now());
                 COMMIT;"
            ))
            .execute(&pool)
            .await
            .unwrap();

            // Twelve replies published concurrently. Each one is ordinary Channel activity for the
            // Agent, so all twelve belong in a single aggregate.
            const REPLIES: i32 = 12;
            let published = (0..REPLIES)
                .map(|index| {
                    let pool = pool.clone();
                    let thread_id = if index < REPLIES / 2 { root } else { second_root };
                    tokio::spawn(async move {
                        let mut adapter = PostgresAdapter::new(pool);
                        adapter
                            .transact(async |transaction| {
                                transaction
                                    .publish_message(MessageDraft {
                                        message_id: MessageId::from_uuid(Uuid::now_v7()),
                                        channel_id: ChannelId::from_uuid(channel),
                                        author_member_id: MemberId::from_uuid(owner),
                                        idempotency_key: IdempotencyKey::from_uuid(Uuid::now_v7()),
                                        body_markdown: "reply".into(),
                                        thread_id: Some(ThreadId::from_uuid(thread_id)),
                                        reply_to_message_id: None,
                                        mentions: Vec::new(),
                                        mention_all: false,
                                        attachment_ids: Vec::new(),
                                        handled_item: None,
                                        now: OffsetDateTime::now_utc(),
                                    })
                                    .await
                            })
                            .await
                    })
                })
                .collect::<Vec<_>>();
            for task in published {
                task.await
                    .expect("the publisher task runs")
                    .expect("every concurrent ambient publisher commits");
            }

            let (items, count, first_seq, last_seq, force_at, available_at): (
                i64,
                i32,
                i64,
                i64,
                OffsetDateTime,
                OffsetDateTime,
            ) = sqlx::query_as(
                "SELECT count(*) OVER (),aggregated_count,first_message_seq,last_message_seq,\
                        force_at,available_at \
                 FROM inbox_items WHERE member_id=$1 LIMIT 1",
            )
            .bind(agent)
            .fetch_one(&pool)
            .await
            .unwrap();
            assert_eq!(items, 1, "ambient activity collapses into one Item");
            assert_eq!(
                count, REPLIES,
                "no concurrent update was lost from the count"
            );
            // The two Root Messages hold sequences 1 and 2, so replies from both Threads occupy
            // one Channel aggregate over sequences 3 through REPLIES + 2.
            assert_eq!((first_seq, last_seq), (3, i64::from(REPLIES) + 2));
            let event_count: i64 = sqlx::query_scalar(
                "SELECT count(*) FROM inbox_activity_events e JOIN inbox_items i ON i.id=e.inbox_item_id WHERE i.member_id=$1",
            )
            .bind(agent)
            .fetch_one(&pool)
            .await
            .unwrap();
            assert_eq!(event_count, i64::from(REPLIES));
            assert!(
                available_at <= force_at,
                "a busy Thread cannot postpone the aggregate past its deadline"
            );

            // The single-aggregate rule is enforced by the schema, not only by the write path.
            let duplicate = sqlx::query(
                "INSERT INTO inbox_items(id,space_id,member_id,message_id,thread_id,task_id,kind,\
                 strength,status,available_at,first_message_seq,last_message_seq,aggregated_count,\
                 force_at,ambient_channel_id,created_at) \
                 VALUES($1,$2,$3,NULL,$4,NULL,'channel_activity','ambient','pending',now(),99,99,1,\
                 now(),$5,now())",
            )
            .bind(Uuid::now_v7())
            .bind(space)
            .bind(agent)
            .bind(root)
            .bind(channel)
            .execute(&pool)
            .await;
            assert!(
                duplicate.is_err(),
                "a second open ambient aggregate for one Agent and Channel must be rejected"
            );

            // The aggregate names no source Message, so claiming it must not depend on one. Claiming
            // also closes it to further Messages, which is why this runs last.
            let (aggregate_id, aggregate_thread): (Uuid, Uuid) = sqlx::query_as(
                "SELECT id,thread_id FROM inbox_items WHERE member_id=$1",
            )
            .bind(agent)
            .fetch_one(&pool)
            .await
            .unwrap();
            sqlx::query("UPDATE inbox_items SET available_at=now() WHERE id=$1")
                .bind(aggregate_id)
                .execute(&pool)
                .await
                .unwrap();
            let mut adapter = PostgresAdapter::new(pool.clone());
            DispatchRun::execute(
                &mut adapter,
                DispatchRunInput {
                    run_id: RunId::from_uuid(Uuid::now_v7()),
                    agent_id: MemberId::from_uuid(agent),
                    task_id: None,
                    focus_thread_id: ThreadId::from_uuid(aggregate_thread),
                    trigger: RunTrigger::Mention,
                    item_ids: vec![InboxItemId::from_uuid(aggregate_id)],
                },
            )
            .await
            .expect("an ambient aggregate is claimable without a source Message");
            let leased: (String, Option<Uuid>) =
                sqlx::query_as("SELECT status,message_id FROM inbox_items WHERE id=$1")
                    .bind(aggregate_id)
                    .fetch_one(&pool)
                    .await
                    .unwrap();
            assert_eq!(leased, ("assigned".into(), None));

            // Activity during the lease opens a second aggregate. Releasing the leased one must
            // fold that aggregate in instead of rejecting the Run result on the unique index.
            adapter
                .transact(async |transaction| {
                    transaction
                        .publish_message(MessageDraft {
                            message_id: MessageId::from_uuid(Uuid::now_v7()),
                            channel_id: ChannelId::from_uuid(channel),
                            author_member_id: MemberId::from_uuid(owner),
                            idempotency_key: IdempotencyKey::from_uuid(Uuid::now_v7()),
                            body_markdown: "reply during the lease".into(),
                            thread_id: Some(ThreadId::from_uuid(root)),
                            reply_to_message_id: None,
                            mentions: Vec::new(),
                            mention_all: false,
                            attachment_ids: Vec::new(),
                            handled_item: None,
                            now: OffsetDateTime::now_utc(),
                        })
                        .await
                })
                .await
                .unwrap();
            let run_id: Uuid =
                sqlx::query_scalar("SELECT assigned_run_id FROM inbox_items WHERE id=$1")
                    .bind(aggregate_id)
                    .fetch_one(&pool)
                    .await
                    .unwrap();
            CompleteRun::execute(
                &mut adapter,
                CompleteRunInput {
                    event_id: EventId::from_uuid(Uuid::now_v7()),
                    run_id: RunId::from_uuid(run_id),
                    computer_id: ComputerId::from_uuid(computer_id),
                    outcome: RunOutcome::Yielded,
                    error_code: None,
                    item_dispositions: vec![ItemDispositionInput {
                        item_id: InboxItemId::from_uuid(aggregate_id),
                        disposition: InboxItemDisposition::Released,
                    }],
                    continuation_note: None,
                    max_retry_count: crate::server::domain::attention::AttentionPolicy::MAX_RETRY_COUNT,
                    now: OffsetDateTime::now_utc(),
                },
            )
            .await
            .expect("a released aggregate absorbs the one opened during its lease");
            let open: Vec<(Uuid, i32, i64, i64)> = sqlx::query_as(
                "SELECT id,aggregated_count,first_message_seq,last_message_seq FROM inbox_items \
                 WHERE member_id=$1 AND status='pending'",
            )
            .bind(agent)
            .fetch_all(&pool)
            .await
            .unwrap();
            assert_eq!(
                open,
                vec![(aggregate_id, REPLIES + 1, 3, i64::from(REPLIES) + 3)]
            );
            let event_count: i64 = sqlx::query_scalar(
                "SELECT count(*) FROM inbox_activity_events WHERE inbox_item_id=$1",
            )
            .bind(aggregate_id)
            .fetch_one(&pool)
            .await
            .unwrap();
            assert_eq!(event_count, i64::from(REPLIES) + 1);

            pool.close().await;
        }
        .await;

    sqlx::query(&format!("DROP DATABASE \"{database_name}\" WITH (FORCE)"))
        .execute(&mut admin)
        .await
        .unwrap();
    result
}

#[tokio::test]
async fn channel_member_activity_preserves_order_and_stops_after_leave() {
    let admin_url = std::env::var("SUMI_TEST_DATABASE_URL")
        .unwrap_or_else(|_| "postgres://localhost/postgres".to_owned());
    let database_name = format!("sumi_member_activity_{}", Uuid::now_v7().simple());
    let mut admin = PgConnection::connect_with(&PgConnectOptions::from_str(&admin_url).unwrap())
        .await
        .unwrap();
    sqlx::query(&format!("CREATE DATABASE \"{database_name}\""))
        .execute(&mut admin)
        .await
        .unwrap();
    let mut database_url = Url::parse(&admin_url).unwrap();
    database_url.set_path(&format!("/{database_name}"));

    let result = async {
        let pool = PgPool::connect(database_url.as_str()).await.unwrap();
        PostgresAdapter::new(pool.clone())
            .initialize_schema()
            .await
            .unwrap();
        let space = Uuid::now_v7();
        let owner = Uuid::now_v7();
        let watcher = Uuid::now_v7();
        let joining_agent = Uuid::now_v7();
        let computer_id = Uuid::now_v7();
        let channel = Uuid::now_v7();
        let root = Uuid::now_v7();
        sqlx::raw_sql(&format!(
            "BEGIN;
             INSERT INTO spaces (id,slug,name,accent,owner_member_id,created_at) VALUES ('{space}','space','Space','#F0602F','{owner}',now());
             INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{owner}','{space}','human','Owner','owner',now());
             INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{watcher}','{space}','agent','Watcher','member',now());
             INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{joining_agent}','{space}','agent','Joining','member',now());
             INSERT INTO computers (id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer_id}','{space}','Computer','localhost','linux','hash','offline',1,now());
             INSERT INTO agents (member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES ('{watcher}','{space}','{computer_id}','Watch',1,'active','builtin',now()),('{joining_agent}','{space}','{computer_id}','Join',1,'active','builtin',now());
             INSERT INTO channels (id,space_id,kind,slug,next_seq,created_at) VALUES ('{channel}','{space}','public','general',2,now());
             INSERT INTO channel_members (channel_id,space_id,member_id,joined_at) VALUES ('{channel}','{space}','{owner}',now()),('{channel}','{space}','{watcher}',now());
             INSERT INTO messages (id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES ('{root}','{space}','{channel}','{root}',1,'root','text','{owner}','source',now());
             COMMIT;"
        ))
        .execute(&pool)
        .await
        .unwrap();

        let mut adapter = PostgresAdapter::new(pool.clone());
        let inserted = adapter
            .transact(async |transaction| {
                transaction
                    .add_channel_agents(
                        MemberId::from_uuid(owner),
                        ChannelId::from_uuid(channel),
                        vec![MemberId::from_uuid(joining_agent)],
                        IdempotencyKey::from_uuid(Uuid::now_v7()),
                        OffsetDateTime::now_utc(),
                    )
                    .await
            })
            .await
            .unwrap();
        assert_eq!(inserted, vec![MemberId::from_uuid(joining_agent)]);

        let first_message = MessageId::from_uuid(Uuid::now_v7());
        adapter
            .transact(async |transaction| {
                transaction
                    .publish_message(MessageDraft {
                        message_id: first_message,
                        channel_id: ChannelId::from_uuid(channel),
                        author_member_id: MemberId::from_uuid(owner),
                        idempotency_key: IdempotencyKey::from_uuid(Uuid::now_v7()),
                        body_markdown: "first message".into(),
                        thread_id: Some(ThreadId::from_uuid(root)),
                        reply_to_message_id: None,
                        mentions: Vec::new(),
                        mention_all: false,
                        attachment_ids: Vec::new(),
                        handled_item: None,
                        now: OffsetDateTime::now_utc(),
                    })
                    .await
            })
            .await
            .unwrap();

        adapter
            .transact(async |transaction| {
                transaction
                    .remove_channel_agent(
                        MemberId::from_uuid(owner),
                        ChannelId::from_uuid(channel),
                        MemberId::from_uuid(joining_agent),
                        OffsetDateTime::now_utc(),
                    )
                    .await
            })
            .await
            .unwrap();

        adapter
            .transact(async |transaction| {
                transaction
                    .publish_message(MessageDraft {
                        message_id: MessageId::from_uuid(Uuid::now_v7()),
                        channel_id: ChannelId::from_uuid(channel),
                        author_member_id: MemberId::from_uuid(owner),
                        idempotency_key: IdempotencyKey::from_uuid(Uuid::now_v7()),
                        body_markdown: "after leave".into(),
                        thread_id: Some(ThreadId::from_uuid(root)),
                        reply_to_message_id: None,
                        mentions: Vec::new(),
                        mention_all: false,
                        attachment_ids: Vec::new(),
                        handled_item: None,
                        now: OffsetDateTime::now_utc(),
                    })
                    .await
            })
            .await
            .unwrap();

        let watcher_events: Vec<(i64, String)> = sqlx::query_as(
            "SELECT e.channel_seq,e.kind FROM inbox_activity_events e \
             JOIN inbox_items i ON i.id=e.inbox_item_id \
             WHERE i.member_id=$1 ORDER BY e.channel_seq",
        )
        .bind(watcher)
        .fetch_all(&pool)
        .await
        .unwrap();
        assert_eq!(
            watcher_events,
            vec![
                (2, "member_joined".to_owned()),
                (3, "message".to_owned()),
                (4, "member_left".to_owned()),
                (5, "message".to_owned()),
            ]
        );

        let watcher_items = adapter
            .transact(async |transaction| {
                transaction
                    .inbox_for_member(
                        MemberId::from_uuid(watcher),
                        MemberId::from_uuid(watcher),
                        InboxScope::Queue,
                    )
                    .await
            })
            .await
            .unwrap();
        assert_eq!(watcher_items.len(), 1);
        assert_eq!(
            watcher_items[0]
                .activity_events
                .iter()
                .map(|event| (event.sequence, event.kind))
                .collect::<Vec<_>>(),
            vec![
                (2, InboxActivityEventKind::MemberJoined),
                (3, InboxActivityEventKind::Message),
                (4, InboxActivityEventKind::MemberLeft),
                (5, InboxActivityEventKind::Message),
            ]
        );

        let joining_counts: (i64, i64) = sqlx::query_as(
            "SELECT count(DISTINCT i.id),count(e.channel_seq) \
             FROM inbox_items i LEFT JOIN inbox_activity_events e ON e.inbox_item_id=i.id \
             WHERE i.member_id=$1",
        )
        .bind(joining_agent)
        .fetch_one(&pool)
        .await
        .unwrap();
        assert_eq!(joining_counts, (1, 1));

        let candidates = adapter
            .transact(async |transaction| {
                transaction
                    .dispatchable_work(
                        OffsetDateTime::now_utc() + time::Duration::minutes(1),
                        64,
                    )
                    .await
            })
            .await
            .unwrap();
        assert!(!candidates
            .iter()
            .any(|candidate| candidate.agent_id == MemberId::from_uuid(joining_agent)));

        pool.close().await;
    }
    .await;

    sqlx::query(&format!("DROP DATABASE \"{database_name}\" WITH (FORCE)"))
        .execute(&mut admin)
        .await
        .unwrap();
    result
}

#[tokio::test]
async fn run_observations_start_frozen_and_advance_only_over_what_a_read_returned() {
    let database = TestDatabase::create("sumi_run_observations").await;
    let pool = database.pool.clone();
    let mut adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let space = Uuid::now_v7();
    let owner = Uuid::now_v7();
    let agent = Uuid::now_v7();
    let computer = Uuid::now_v7();
    let channel = Uuid::now_v7();
    let root = Uuid::now_v7();
    let run = Uuid::now_v7();
    let item = Uuid::now_v7();
    sqlx::raw_sql(&format!(
        "BEGIN;
         INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at) VALUES ('{space}','observations','Observations','#F0602F','{owner}',now());
         INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES
           ('{owner}','{space}','human','Owner','owner',now()),
           ('{agent}','{space}','agent','Agent','member',now());
         INSERT INTO computers(id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer}','{space}','Computer','localhost','linux','observations-hash','online',1,now());
         INSERT INTO agents(member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES ('{agent}','{space}','{computer}','Act',1,'active','builtin',now());
         INSERT INTO channels(id,space_id,kind,slug,next_seq,created_at) VALUES ('{channel}','{space}','public','general',3,now());
         INSERT INTO channel_members(channel_id,space_id,member_id,joined_at) VALUES
           ('{channel}','{space}','{owner}',now()),('{channel}','{space}','{agent}',now());
         INSERT INTO messages(id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES
           ('{root}','{space}','{channel}','{root}',1,'root','text','{owner}','source',now()),
           ('{}','{space}','{channel}','{root}',2,'reply','text','{owner}','early reply',now());
         INSERT INTO inbox_items(id,space_id,member_id,message_id,thread_id,kind,strength,status,available_at,created_at) VALUES
           ('{item}','{space}','{agent}','{root}','{root}','mention','hard','pending',now(),now());
         COMMIT;",
        Uuid::now_v7()
    ))
    .execute(&pool)
    .await
    .unwrap();

    DispatchRun::execute(
        &mut adapter,
        DispatchRunInput {
            run_id: RunId::from_uuid(run),
            agent_id: MemberId::from_uuid(agent),
            task_id: None,
            focus_thread_id: ThreadId::from_uuid(root),
            trigger: RunTrigger::Mention,
            item_ids: vec![InboxItemId::from_uuid(item)],
        },
    )
    .await
    .unwrap();

    let positions = |pool: PgPool| async move {
        sqlx::query_as::<_, (String, i64)>(
            "SELECT scope_kind,observed_seq FROM run_observations WHERE run_id=$1 ORDER BY scope_kind",
        )
        .bind(run)
        .fetch_all(&pool)
        .await
        .unwrap()
    };
    assert_eq!(
        positions(pool.clone()).await,
        vec![("channel".to_owned(), 2), ("thread".to_owned(), 2)],
        "RunStart freezes both positions at the dispatched snapshot"
    );

    let append = |pool: PgPool, sequence: i64, body: &'static str| async move {
        sqlx::query(
            "INSERT INTO messages(id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) \
             VALUES($1,$2,$3,$4,$5,'reply','text',$6,$7,now())",
        )
        .bind(Uuid::now_v7())
        .bind(space)
        .bind(channel)
        .bind(root)
        .bind(sequence)
        .bind(owner)
        .bind(body)
        .execute(&pool)
        .await
        .unwrap();
    };

    append(pool.clone(), 3, "third").await;
    adapter
        .transact(async |transaction| {
            transaction
                .record_read(
                    RunId::from_uuid(run),
                    MemberId::from_uuid(agent),
                    ObservationScope::Thread(ThreadId::from_uuid(root)),
                    &[3],
                )
                .await
        })
        .await
        .unwrap();
    assert_eq!(positions(pool.clone()).await[1].1, 3);

    // A read that skips a visible Message cannot claim it, so the position stays where it was.
    append(pool.clone(), 4, "fourth").await;
    append(pool.clone(), 5, "fifth").await;
    adapter
        .transact(async |transaction| {
            transaction
                .record_read(
                    RunId::from_uuid(run),
                    MemberId::from_uuid(agent),
                    ObservationScope::Thread(ThreadId::from_uuid(root)),
                    &[5],
                )
                .await
        })
        .await
        .unwrap();
    assert_eq!(positions(pool.clone()).await[1].1, 3);

    adapter
        .transact(async |transaction| {
            transaction
                .record_read(
                    RunId::from_uuid(run),
                    MemberId::from_uuid(agent),
                    ObservationScope::Thread(ThreadId::from_uuid(root)),
                    &[4, 5],
                )
                .await
        })
        .await
        .unwrap();
    assert_eq!(positions(pool.clone()).await[1].1, 5);

    let observed = adapter
        .transact(async |transaction| {
            transaction
                .unobserved_messages(
                    RunId::from_uuid(run),
                    MemberId::from_uuid(agent),
                    ObservationScope::Thread(ThreadId::from_uuid(root)),
                    20,
                )
                .await
        })
        .await
        .unwrap()
        .unwrap();
    assert!(observed.messages.is_empty());
    assert_eq!(observed.consecutive_holds, 0);

    append(pool.clone(), 6, "sixth").await;
    let observed = adapter
        .transact(async |transaction| {
            transaction
                .unobserved_messages(
                    RunId::from_uuid(run),
                    MemberId::from_uuid(agent),
                    ObservationScope::Thread(ThreadId::from_uuid(root)),
                    20,
                )
                .await
        })
        .await
        .unwrap()
        .unwrap();
    assert_eq!(observed.messages.len(), 1);
    assert_eq!(observed.messages[0].sequence, 6);

    for expected in [1, 2] {
        adapter
            .transact(async |transaction| {
                transaction
                    .record_hold(
                        RunId::from_uuid(run),
                        ObservationScope::Thread(ThreadId::from_uuid(root)),
                        6,
                    )
                    .await
            })
            .await
            .unwrap();
        let observed = adapter
            .transact(async |transaction| {
                transaction
                    .unobserved_messages(
                        RunId::from_uuid(run),
                        MemberId::from_uuid(agent),
                        ObservationScope::Thread(ThreadId::from_uuid(root)),
                        20,
                    )
                    .await
            })
            .await
            .unwrap()
            .unwrap();
        assert_eq!(observed.consecutive_holds, expected);
    }
    adapter
        .transact(async |transaction| {
            transaction
                .clear_holds(
                    RunId::from_uuid(run),
                    ObservationScope::Thread(ThreadId::from_uuid(root)),
                )
                .await
        })
        .await
        .unwrap();
    let observed = adapter
        .transact(async |transaction| {
            transaction
                .unobserved_messages(
                    RunId::from_uuid(run),
                    MemberId::from_uuid(agent),
                    ObservationScope::Thread(ThreadId::from_uuid(root)),
                    20,
                )
                .await
        })
        .await
        .unwrap()
        .unwrap();
    assert_eq!(observed.consecutive_holds, 0);

    pool.close().await;
    database.close().await;
}

#[tokio::test]
async fn application_transaction_commits_task_source_idempotency_and_outbox_together() {
    let admin_url = std::env::var("SUMI_TEST_DATABASE_URL")
        .unwrap_or_else(|_| "postgres://localhost/postgres".to_owned());
    let database_name = format!("sumi_postgres_port_{}", Uuid::now_v7().simple());
    let mut admin = PgConnection::connect_with(&PgConnectOptions::from_str(&admin_url).unwrap())
        .await
        .unwrap();
    sqlx::query(&format!("CREATE DATABASE \"{database_name}\""))
        .execute(&mut admin)
        .await
        .unwrap();
    let mut database_url = Url::parse(&admin_url).unwrap();
    database_url.set_path(&format!("/{database_name}"));

    let pool = PgPool::connect(database_url.as_str()).await.unwrap();
    let mut adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let space = Uuid::now_v7();
    let member = Uuid::now_v7();
    let channel = Uuid::now_v7();
    let root = Uuid::now_v7();
    let actor_agent = Uuid::now_v7();
    let computer_id = Uuid::now_v7();
    let run_id = Uuid::now_v7();
    sqlx::raw_sql(&format!(
            "BEGIN;
             INSERT INTO spaces (id,slug,name,accent,owner_member_id,created_at) VALUES ('{space}','space','Space','#F0602F','{member}',now());
             INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{member}','{space}','human','Owner','owner',now());
             INSERT INTO members (id,space_id,kind,display_name,access_level,created_at) VALUES ('{actor_agent}','{space}','agent','Actor','member',now());
             INSERT INTO computers (id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer_id}','{space}','Computer','localhost','linux','hash','online',1,now());
             INSERT INTO agents (member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES ('{actor_agent}','{space}','{computer_id}','Act',1,'active','builtin',now());
             INSERT INTO member_permissions (member_id,space_id,action_code,granted_by_member_id,created_at) VALUES ('{actor_agent}','{space}','agent.create','{member}',now());
             INSERT INTO channels (id,space_id,kind,slug,next_seq,created_at) VALUES ('{channel}','{space}','private','general',2,now());
             INSERT INTO channel_members (channel_id,space_id,member_id,joined_at) VALUES ('{channel}','{space}','{member}',now());
             INSERT INTO channel_members (channel_id,space_id,member_id,joined_at) VALUES ('{channel}','{space}','{actor_agent}',now());
             INSERT INTO messages (id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES ('{root}','{space}','{channel}','{root}',1,'root','text','{member}','source',now());
             INSERT INTO agent_runs (id,space_id,agent_id,focus_thread_id,status,trigger_kind,created_at,started_at) VALUES ('{run_id}','{space}','{actor_agent}','{root}','working','mention',now(),now());
             COMMIT;"
        ))
        .execute(&pool)
        .await
        .unwrap();

    let task_id = TaskId::from_uuid(Uuid::now_v7());
    let idempotency_key = IdempotencyKey::from_uuid(Uuid::now_v7());
    let created = CreateTaskFromRootMessage::execute(
        &mut adapter,
        CreateTaskInput {
            task_id,
            actor_member_id: MemberId::from_uuid(member),
            source: TaskSource::HumanRoot(ThreadId::from_uuid(root)),
            title: "PostgreSQL transaction".into(),
            assignee_agent_member_id: None,
            source_thread_id: None,
            link_thread_ids: vec![],
            idempotency_key,
            now: OffsetDateTime::now_utc(),
        },
    )
    .await
    .unwrap();
    assert_eq!(created.view().id, task_id);
    let facts: (i64, i64, i64) = (
        sqlx::query_scalar("SELECT count(*) FROM tasks WHERE id=$1")
            .bind(task_id.into_uuid())
            .fetch_one(&pool)
            .await
            .unwrap(),
        sqlx::query_scalar("SELECT count(*) FROM idempotency_records WHERE resource_id=$1")
            .bind(task_id.into_uuid())
            .fetch_one(&pool)
            .await
            .unwrap(),
        sqlx::query_scalar("SELECT count(*) FROM outbox_events WHERE kind='task.created'")
            .fetch_one(&pool)
            .await
            .unwrap(),
    );
    assert_eq!(facts, (1, 1, 1));

    let new_root = Uuid::now_v7();
    sqlx::raw_sql(&format!(
        "INSERT INTO messages (id,space_id,channel_id,thread_id,channel_seq,placement, \
         content_kind,author_member_id,body_markdown,created_at) \
         VALUES ('{new_root}','{space}','{channel}','{new_root}',2,'root','text', \
                 '{actor_agent}','independent',now());"
    ))
    .execute(&pool)
    .await
    .unwrap();
    let second_task_id = TaskId::from_uuid(Uuid::now_v7());
    let created = CreateTaskFromRootMessage::execute(
        &mut adapter,
        CreateTaskInput {
            task_id: second_task_id,
            actor_member_id: MemberId::from_uuid(actor_agent),
            source: TaskSource::AgentRun(RunId::from_uuid(run_id)),
            title: "独立任务".into(),
            assignee_agent_member_id: None,
            source_thread_id: Some(ThreadId::from_uuid(new_root)),
            link_thread_ids: vec![],
            idempotency_key: IdempotencyKey::from_uuid(Uuid::now_v7()),
            now: OffsetDateTime::now_utc(),
        },
    )
    .await
    .unwrap();
    assert_eq!(created.view().status, TaskStatus::Todo);
    let item: (Uuid, Uuid, String, String) =
        sqlx::query_as("SELECT thread_id,task_id,kind,status FROM inbox_items WHERE member_id=$1")
            .bind(actor_agent)
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(
        item,
        (
            new_root,
            second_task_id.into_uuid(),
            "task_activity".to_owned(),
            "pending".to_owned()
        )
    );

    let conflicting_root = Uuid::now_v7();
    sqlx::raw_sql(&format!(
        "INSERT INTO messages (id,space_id,channel_id,thread_id,channel_seq,placement, \
         content_kind,author_member_id,body_markdown,created_at) \
         VALUES ('{conflicting_root}','{space}','{channel}','{conflicting_root}',3,'root', \
                 'text','{actor_agent}','conflict',now());"
    ))
    .execute(&pool)
    .await
    .unwrap();
    sqlx::query("UPDATE channels SET next_seq=4 WHERE id=$1")
        .bind(channel)
        .execute(&pool)
        .await
        .unwrap();
    let conflict = CreateTaskFromRootMessage::execute(
        &mut adapter,
        CreateTaskInput {
            task_id: TaskId::from_uuid(Uuid::now_v7()),
            actor_member_id: MemberId::from_uuid(actor_agent),
            source: TaskSource::AgentRun(RunId::from_uuid(run_id)),
            title: "冲突任务".into(),
            assignee_agent_member_id: None,
            source_thread_id: Some(ThreadId::from_uuid(conflicting_root)),
            link_thread_ids: vec![ThreadId::from_uuid(root)],
            idempotency_key: IdempotencyKey::from_uuid(Uuid::now_v7()),
            now: OffsetDateTime::now_utc(),
        },
    )
    .await
    .unwrap_err();
    assert_eq!(conflict, ApplicationError::Conflict);
    let item_count: i64 = sqlx::query_scalar("SELECT count(*) FROM inbox_items WHERE member_id=$1")
        .bind(actor_agent)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(item_count, 1);

    let invalid_done = sqlx::query("UPDATE tasks SET status='done',finished_at=now() WHERE id=$1")
        .bind(task_id.into_uuid())
        .execute(&pool)
        .await
        .unwrap_err();
    assert_eq!(
        invalid_done.as_database_error().unwrap().code().as_deref(),
        Some("23514")
    );
    let parallel_run = sqlx::query(
        "INSERT INTO agent_runs (id,space_id,agent_id,focus_thread_id,status, \
             trigger_kind,created_at) \
             VALUES ($1,$2,$3,$4,'dispatched','mention',now())",
    )
    .bind(Uuid::now_v7())
    .bind(space)
    .bind(actor_agent)
    .bind(root)
    .execute(&pool)
    .await
    .unwrap_err();
    assert_eq!(
        parallel_run.as_database_error().unwrap().code().as_deref(),
        Some("23505")
    );

    let created_agent = MemberId::from_uuid(Uuid::now_v7());
    CreateAgentAction::execute(
        &mut adapter,
        CreateAgentActionInput {
            agent_member_id: created_agent,
            display_name: "NewAgent".into(),
            role_text: "Implement".into(),
            computer_id: ComputerId::from_uuid(computer_id),
            driver_kind: DriverKind::Acp(AcpProvider::Codex),
            action_message_id: MessageId::from_uuid(Uuid::now_v7()),
            actor_member_id: MemberId::from_uuid(actor_agent),
            idempotency_key: IdempotencyKey::from_uuid(Uuid::now_v7()),
            current_run_id: RunId::from_uuid(run_id),
            now: OffsetDateTime::now_utc(),
        },
    )
    .await
    .unwrap();
    let agent_action_facts: (i64, i64, i64) = (
        sqlx::query_scalar("SELECT count(*) FROM agents WHERE member_id=$1")
            .bind(created_agent.into_uuid())
            .fetch_one(&pool)
            .await
            .unwrap(),
        sqlx::query_scalar(
            "SELECT count(*) FROM messages WHERE content_kind='agent_created' \
                 AND action_agent_member_id=$1",
        )
        .bind(created_agent.into_uuid())
        .fetch_one(&pool)
        .await
        .unwrap(),
        sqlx::query_scalar("SELECT count(*) FROM computer_commands WHERE kind='agent.provision'")
            .fetch_one(&pool)
            .await
            .unwrap(),
    );
    assert_eq!(agent_action_facts, (1, 1, 1));
    let commands = adapter
        .pending_commands(ComputerId::from_uuid(computer_id), CommandSequence(0))
        .await
        .unwrap();
    assert_eq!(commands.len(), 1);
    assert!(matches!(commands[0].command, Command::AgentProvision(_)));
    adapter
        .acknowledge_command(
            ComputerId::from_uuid(computer_id),
            &CommandAck {
                command_id: commands[0].command_id,
                sequence: commands[0].sequence,
            },
        )
        .await
        .unwrap();
    assert!(
        adapter
            .pending_commands(ComputerId::from_uuid(computer_id), CommandSequence(0))
            .await
            .unwrap()
            .is_empty()
    );

    pool.close().await;
    sqlx::query(&format!("DROP DATABASE \"{database_name}\" WITH (FORCE)"))
        .execute(&mut admin)
        .await
        .unwrap();
}

#[tokio::test]
async fn adding_channel_agents_replays_without_duplicate_membership_or_notices() {
    let database = TestDatabase::create("sumi_add_channel_agents").await;
    let pool = database.pool.clone();
    let mut adapter = PostgresAdapter::new(pool.clone());
    let space = Uuid::now_v7();
    let owner = Uuid::now_v7();
    let agent = Uuid::now_v7();
    let computer = Uuid::now_v7();
    let channel = Uuid::now_v7();
    let key = IdempotencyKey::from_uuid(Uuid::now_v7());
    sqlx::raw_sql(&format!(
        "BEGIN;
         INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at)
             VALUES ('{space}','add-agents','Add Agents','#F0602F','{owner}',now());
         INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES
             ('{owner}','{space}','human','Owner','owner',now()),
             ('{agent}','{space}','agent','Lin','member',now());
         INSERT INTO computers(id,space_id,name,hostname,os,token_hash,connection_status,created_at)
             VALUES ('{computer}','{space}','Computer','localhost','linux','add-agents-hash','offline',now());
         INSERT INTO agents(member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at)
             VALUES ('{agent}','{space}','{computer}','Write',1,'active','builtin',now());
         INSERT INTO channels(id,space_id,kind,slug,next_seq,created_at)
             VALUES ('{channel}','{space}','public','general',1,now());
         INSERT INTO channel_members(channel_id,space_id,member_id,joined_at)
             VALUES ('{channel}','{space}','{owner}',now());
         COMMIT;"
    ))
    .execute(&pool)
    .await
    .unwrap();

    let added = AddChannelAgents::execute(
        &mut adapter,
        MemberId::from_uuid(owner),
        ChannelId::from_uuid(channel),
        vec![MemberId::from_uuid(agent)],
        key,
        OffsetDateTime::now_utc(),
    )
    .await
    .unwrap();
    let replayed = AddChannelAgents::execute(
        &mut adapter,
        MemberId::from_uuid(owner),
        ChannelId::from_uuid(channel),
        vec![MemberId::from_uuid(agent)],
        key,
        OffsetDateTime::now_utc(),
    )
    .await
    .unwrap();
    assert_eq!(added, vec![MemberId::from_uuid(agent)]);
    assert!(replayed.is_empty(), "a replay adds nothing");

    let facts: (i64, i64) = (
        sqlx::query_scalar("SELECT count(*) FROM channel_members WHERE member_id=$1")
            .bind(agent)
            .fetch_one(&pool)
            .await
            .unwrap(),
        sqlx::query_scalar(
            "SELECT count(*) FROM messages WHERE content_kind='system_notice' AND channel_id=$1",
        )
        .bind(channel)
        .fetch_one(&pool)
        .await
        .unwrap(),
    );
    assert_eq!(facts, (1, 1));
    database.close().await;
}

#[tokio::test]
async fn committed_command_wakes_the_online_connection() {
    let database = TestDatabase::create("sumi_command_wakeup").await;
    let pool = database.pool.clone();
    let mut adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let space_id = Uuid::now_v7();
    let owner_id = Uuid::now_v7();
    let computer_id = Uuid::now_v7();
    sqlx::raw_sql(&format!(
        "BEGIN;
         INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at) VALUES ('{space_id}','wakeup','Wake Up','#F0602F','{owner_id}',now());
         INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES ('{owner_id}','{space_id}','human','Owner','owner',now());
         INSERT INTO computers(id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer_id}','{space_id}','Computer','localhost','linux','wakeup-hash','online',1,now());
         COMMIT;"
    ))
    .execute(&pool)
    .await
    .unwrap();
    let registry = adapter.commands();
    let (_connection, mut wakeups) = registry.connect(computer_id);

    adapter
        .transact(async |transaction| {
            transaction
                .queue_command(
                    ComputerId::from_uuid(computer_id),
                    Command::AgentRetire(AgentRetire {
                        agent_id: AgentId::from_uuid(Uuid::now_v7()),
                    }),
                )
                .await
        })
        .await
        .unwrap();

    assert_eq!(
        tokio::time::timeout(std::time::Duration::from_secs(1), wakeups.recv())
            .await
            .expect("committed command must wake the online connection")
            .expect("command registry channel must stay open"),
        ()
    );

    let pending: i64 =
        sqlx::query_scalar("SELECT count(*) FROM computer_commands WHERE acked_at IS NULL")
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(pending, 1);
    database.close().await;
}

#[tokio::test]
async fn committed_space_event_wakes_browser_streams_only_after_commit() {
    use crate::server::adapters::space_events::{SpaceEventSignal, Wake};

    let database = TestDatabase::create("sumi_space_event_wakeup").await;
    let pool = database.pool.clone();
    PostgresAdapter::new(pool.clone())
        .initialize_schema()
        .await
        .unwrap();
    let space_id = Uuid::now_v7();
    let owner_id = Uuid::now_v7();
    sqlx::raw_sql(&format!(
        "BEGIN;
         INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at) VALUES ('{space_id}','events','Events','#F0602F','{owner_id}',now());
         INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES ('{owner_id}','{space_id}','human','Owner','owner',now());
         COMMIT;"
    ))
    .execute(&pool)
    .await
    .unwrap();
    let signal = SpaceEventSignal::default();
    let mut wakeups = signal.subscribe();
    let listener = tokio::spawn(signal.clone().listen_forever(pool.clone()));
    let within = std::time::Duration::from_secs(5);
    assert_eq!(
        tokio::time::timeout(within, wakeups.recv())
            .await
            .unwrap()
            .unwrap(),
        Wake::All
    );

    let mut transaction = pool.begin().await.unwrap();
    sqlx::query(
        "INSERT INTO outbox_events(id,space_id,kind,payload_json,created_at,sequence) \
         VALUES($1,$2,'task.updated','{}',now(),1)",
    )
    .bind(Uuid::now_v7())
    .bind(space_id)
    .execute(&mut *transaction)
    .await
    .unwrap();
    assert!(
        tokio::time::timeout(std::time::Duration::from_millis(300), wakeups.recv())
            .await
            .is_err(),
        "an uncommitted event must not wake streams"
    );
    transaction.commit().await.unwrap();
    assert_eq!(
        tokio::time::timeout(within, wakeups.recv())
            .await
            .unwrap()
            .unwrap(),
        Wake::Space(space_id)
    );
    listener.abort();
    database.close().await;
}

#[tokio::test]
async fn claim_run_inserts_the_run_before_leasing_its_inbox_item() {
    let admin_url = std::env::var("SUMI_TEST_DATABASE_URL")
        .unwrap_or_else(|_| "postgres://localhost/postgres".to_owned());
    let database_name = format!("sumi_claim_run_{}", Uuid::now_v7().simple());
    let mut admin = PgConnection::connect_with(&PgConnectOptions::from_str(&admin_url).unwrap())
        .await
        .unwrap();
    sqlx::query(&format!("CREATE DATABASE \"{database_name}\""))
        .execute(&mut admin)
        .await
        .unwrap();
    let mut database_url = Url::parse(&admin_url).unwrap();
    database_url.set_path(&format!("/{database_name}"));

    let pool = PgPool::connect(database_url.as_str()).await.unwrap();
    let mut adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let space_id = Uuid::now_v7();
    let owner_id = Uuid::now_v7();
    let agent_id = Uuid::now_v7();
    let computer_id = Uuid::now_v7();
    let channel_id = Uuid::now_v7();
    let message_id = Uuid::now_v7();
    let item_id = Uuid::now_v7();
    sqlx::raw_sql(&format!(
            "BEGIN;
             INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at) VALUES ('{space_id}','claim-run','Claim Run','#F0602F','{owner_id}',now());
             INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES ('{owner_id}','{space_id}','human','Owner','owner',now());
             INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES ('{agent_id}','{space_id}','agent','Agent','member',now());
             INSERT INTO computers(id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer_id}','{space_id}','Computer','localhost','linux','claim-run-hash','online',1,now());
             INSERT INTO agents(member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES ('{agent_id}','{space_id}','{computer_id}','Reply',1,'active','builtin',now());
             INSERT INTO channels(id,space_id,kind,slug,next_seq,created_at) VALUES ('{channel_id}','{space_id}','public','general',2,now());
             INSERT INTO channel_members(channel_id,space_id,member_id,joined_at) VALUES ('{channel_id}','{space_id}','{owner_id}',now()),('{channel_id}','{space_id}','{agent_id}',now());
             INSERT INTO messages(id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES ('{message_id}','{space_id}','{channel_id}','{message_id}',1,'root','text','{owner_id}','mention',now());
             INSERT INTO inbox_items(id,space_id,member_id,message_id,thread_id,kind,strength,status,available_at,last_error_code,created_at) VALUES ('{item_id}','{space_id}','{agent_id}','{message_id}','{message_id}','mention','hard','pending',now(),'run_claim_unavailable',now());
             COMMIT;"
        ))
        .execute(&pool)
        .await
        .unwrap();

    let run_id = RunId::from_uuid(Uuid::now_v7());
    DispatchRun::execute(
        &mut adapter,
        DispatchRunInput {
            run_id,
            agent_id: MemberId::from_uuid(agent_id),
            task_id: None,
            focus_thread_id: ThreadId::from_uuid(message_id),
            trigger: RunTrigger::Mention,
            item_ids: vec![InboxItemId::from_uuid(item_id)],
        },
    )
    .await
    .unwrap();

    let facts: (String, String, Option<String>, i64, i64, i64) = sqlx::query_as(
        "SELECT r.status,i.status,i.last_error_code, \
             (SELECT count(*) FROM run_items WHERE run_id=r.id), \
             (SELECT count(*) FROM computer_commands WHERE kind='run.start'), \
             (SELECT count(*) FROM outbox_events WHERE kind='message.updated') \
             FROM agent_runs r JOIN inbox_items i ON i.assigned_run_id=r.id WHERE r.id=$1",
    )
    .bind(run_id.into_uuid())
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(
        facts,
        ("dispatched".into(), "assigned".into(), None, 1, 1, 1)
    );

    pool.close().await;
    sqlx::query(&format!("DROP DATABASE \"{database_name}\" WITH (FORCE)"))
        .execute(&mut admin)
        .await
        .unwrap();
}

#[tokio::test]
async fn resuming_an_agent_queues_resume_before_a_later_run_start() {
    let admin_url = std::env::var("SUMI_TEST_DATABASE_URL")
        .unwrap_or_else(|_| "postgres://localhost/postgres".to_owned());
    let database_name = format!("sumi_resume_order_{}", Uuid::now_v7().simple());
    let mut admin = PgConnection::connect_with(&PgConnectOptions::from_str(&admin_url).unwrap())
        .await
        .unwrap();
    sqlx::query(&format!("CREATE DATABASE \"{database_name}\""))
        .execute(&mut admin)
        .await
        .unwrap();
    let mut database_url = Url::parse(&admin_url).unwrap();
    database_url.set_path(&format!("/{database_name}"));

    let pool = PgPool::connect(database_url.as_str()).await.unwrap();
    let mut adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let space_id = Uuid::now_v7();
    let owner_id = Uuid::now_v7();
    let agent_id = Uuid::now_v7();
    let computer_id = Uuid::now_v7();
    let message_id = Uuid::now_v7();
    let item_id = Uuid::now_v7();
    sqlx::raw_sql(&format!(
        "BEGIN;
         INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at) VALUES ('{space_id}','resume-order','Resume Order','#F0602F','{owner_id}',now());
         INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES ('{owner_id}','{space_id}','human','Owner','owner',now());
         INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) VALUES ('{agent_id}','{space_id}','agent','Agent','member',now());
         INSERT INTO computers(id,space_id,name,hostname,os,token_hash,connection_status,next_command_seq,created_at) VALUES ('{computer_id}','{space_id}','Computer','localhost','linux','resume-order-hash','offline',1,now());
         INSERT INTO agents(member_id,space_id,computer_id,role_text,role_revision,lifecycle,driver_kind,created_at) VALUES ('{agent_id}','{space_id}','{computer_id}','Reply',1,'suspended','builtin',now());
         INSERT INTO channels(id,space_id,kind,slug,next_seq,created_at) VALUES ('{message_id}','{space_id}','public','general',2,now());
         INSERT INTO channel_members(channel_id,space_id,member_id,joined_at) VALUES ('{message_id}','{space_id}','{owner_id}',now()),('{message_id}','{space_id}','{agent_id}',now());
         INSERT INTO messages(id,space_id,channel_id,thread_id,channel_seq,placement,content_kind,author_member_id,body_markdown,created_at) VALUES ('{message_id}','{space_id}','{message_id}','{message_id}',1,'root','text','{owner_id}','mention',now());
         INSERT INTO inbox_items(id,space_id,member_id,message_id,thread_id,kind,strength,status,available_at,created_at) VALUES ('{item_id}','{space_id}','{agent_id}','{message_id}','{message_id}','mention','hard','pending',now(),now());
         COMMIT;"
    ))
    .execute(&pool)
    .await
    .unwrap();

    UpdateAgent::execute(
        &mut adapter,
        UpdateAgentInput {
            idempotency_key: crate::ids::IdempotencyKey::from_uuid(Uuid::now_v7()),
            actor_id: MemberId::from_uuid(owner_id),
            agent_id: MemberId::from_uuid(agent_id),
            role_text: None,
            lifecycle: Some(AgentLifecycleAction::Resume),
        },
    )
    .await
    .unwrap();

    DispatchRun::execute(
        &mut adapter,
        DispatchRunInput {
            run_id: RunId::from_uuid(Uuid::now_v7()),
            agent_id: MemberId::from_uuid(agent_id),
            task_id: None,
            focus_thread_id: ThreadId::from_uuid(message_id),
            trigger: RunTrigger::Mention,
            item_ids: vec![InboxItemId::from_uuid(item_id)],
        },
    )
    .await
    .unwrap();

    let commands: Vec<(i64, String)> = sqlx::query_as(
        "SELECT computer_seq,kind FROM computer_commands WHERE computer_id=$1 ORDER BY computer_seq",
    )
    .bind(computer_id)
    .fetch_all(&pool)
    .await
    .unwrap();
    assert_eq!(
        commands,
        vec![(1, "agent.resume".to_owned()), (2, "run.start".to_owned())]
    );

    pool.close().await;
    sqlx::query(&format!("DROP DATABASE \"{database_name}\" WITH (FORCE)"))
        .execute(&mut admin)
        .await
        .unwrap();
}

/// The migration orders existing events by the only order they have — `(created_at,id)` — and
/// gives every Space a `event_sequence` that matches its newest event so new allocations continue
/// where the backfill stopped.
#[tokio::test]
async fn space_event_sequence_follows_commit_order_not_creation_time() {
    let database = TestDatabase::create("sumi_event_commit_order").await;
    let pool = database.pool.clone();
    PostgresAdapter::new(pool.clone())
        .initialize_schema()
        .await
        .unwrap();
    let space_id = Uuid::now_v7();
    let owner_id = Uuid::now_v7();
    sqlx::raw_sql(&format!(
        "BEGIN; \
         INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at) \
             VALUES ('{space_id}','ordering','Ordering','#F0602F','{owner_id}',now()); \
         INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) \
             VALUES ('{owner_id}','{space_id}','human','Owner','owner',now()); \
         COMMIT;"
    ))
    .execute(&pool)
    .await
    .unwrap();
    let space_id = SpaceId::from_uuid(space_id);
    let entered_first = OffsetDateTime::now_utc();

    let (committed_tx, committed) = tokio::sync::oneshot::channel::<()>();
    let pool_first = pool.clone();
    // Started first, committed last: its `created_at` is older than the event that already
    // reached the reader.
    let commits_last = async move {
        let mut transaction = PostgresTransaction::begin(&pool_first).await.unwrap();
        committed.await.unwrap();
        transaction
            .insert_space_event(
                EventId::from_uuid(Uuid::now_v7()),
                space_id,
                "inbox.changed",
                json!({"resource_id": Uuid::now_v7()}),
                entered_first,
            )
            .await
            .unwrap();
        transaction.commit().await.unwrap();
    };
    let commits_first = async {
        let mut transaction = PostgresTransaction::begin(&pool).await.unwrap();
        transaction
            .insert_space_event(
                EventId::from_uuid(Uuid::now_v7()),
                space_id,
                "task.updated",
                json!({"resource_id": Uuid::now_v7()}),
                entered_first + time::Duration::seconds(30),
            )
            .await
            .unwrap();
        transaction.commit().await.unwrap();
        committed_tx.send(()).unwrap();
    };
    tokio::join!(commits_last, commits_first);

    let committed: Vec<(i64, String, OffsetDateTime)> = sqlx::query_as(
        "SELECT sequence,kind,created_at FROM outbox_events WHERE space_id=$1 \
         ORDER BY sequence",
    )
    .bind(space_id.into_uuid())
    .fetch_all(&pool)
    .await
    .unwrap();
    assert_eq!(committed.len(), 2);
    assert_eq!(committed[0].0, 1);
    assert_eq!(committed[1].0, 2);
    // The transaction that started first committed last, so its older `created_at` sits behind
    // the newer one that committed before it.
    assert!(committed[0].2 > committed[1].2);
    let tail: i64 = sqlx::query_scalar("SELECT event_sequence FROM spaces WHERE id=$1")
        .bind(space_id.into_uuid())
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(tail, 2);
    pool.close().await;
    database.close().await;
}

/// A write that queued a Space event but has not reached its commit must not hold the Space row:
/// another write to the same Space commits meanwhile, and the open one still lands after it.
#[tokio::test]
async fn an_open_write_does_not_block_other_writes_to_its_space() {
    let database = TestDatabase::create("sumi_event_lock").await;
    let pool = database.pool.clone();
    PostgresAdapter::new(pool.clone())
        .initialize_schema()
        .await
        .unwrap();
    let (space_id, _, _) = event_test_space(&pool).await;

    let mut open = PostgresTransaction::begin(&pool).await.unwrap();
    open.insert_space_event(
        EventId::from_uuid(Uuid::now_v7()),
        space_id,
        "task.updated",
        json!({"resource_id": Uuid::now_v7()}),
        OffsetDateTime::now_utc(),
    )
    .await
    .unwrap();
    tokio::time::timeout(
        std::time::Duration::from_secs(5),
        commit_events(
            &pool,
            space_id,
            1,
            "inbox.changed",
            json!({"resource_id": Uuid::now_v7()}),
        ),
    )
    .await
    .expect("an uncommitted write must not block the Space");
    open.commit().await.unwrap();

    let kinds: Vec<(i64, String)> = sqlx::query_as(
        "SELECT sequence,kind FROM outbox_events WHERE space_id=$1 ORDER BY sequence",
    )
    .bind(space_id.into_uuid())
    .fetch_all(&pool)
    .await
    .unwrap();
    assert_eq!(
        kinds,
        vec![
            (1, "inbox.changed".to_owned()),
            (2, "task.updated".to_owned())
        ]
    );
    pool.close().await;
    database.close().await;
}

/// Builds a Space with an Owner who reads the whole Space but is not a member of the given
/// Channel, so events scoped to that Channel are invisible to them.
async fn event_test_space(pool: &PgPool) -> (SpaceId, MemberId, Uuid) {
    let space_id = Uuid::now_v7();
    let owner_id = Uuid::now_v7();
    let private_channel_id = Uuid::now_v7();
    let slug = format!("events-{}", &space_id.simple().to_string()[..12]);
    sqlx::raw_sql(&format!(
        "BEGIN; \
         INSERT INTO spaces(id,slug,name,accent,owner_member_id,created_at) \
             VALUES ('{space_id}','{slug}','Events','#F0602F','{owner_id}',now()); \
         INSERT INTO members(id,space_id,kind,display_name,access_level,created_at) \
             VALUES ('{owner_id}','{space_id}','human','Owner','owner',now()); \
         INSERT INTO channels(id,space_id,kind,slug,next_seq,created_at) \
             VALUES ('{private_channel_id}','{space_id}','public','private-log',1,now()); \
         COMMIT;"
    ))
    .execute(pool)
    .await
    .unwrap();
    (
        SpaceId::from_uuid(space_id),
        MemberId::from_uuid(owner_id),
        private_channel_id,
    )
}

/// Commits `count` events in one transaction on behalf of the given Space.
async fn commit_events(
    pool: &PgPool,
    space_id: SpaceId,
    count: usize,
    kind: &str,
    payload: serde_json::Value,
) {
    let mut transaction = PostgresTransaction::begin(pool).await.unwrap();
    for _ in 0..count {
        transaction
            .insert_space_event(
                EventId::from_uuid(Uuid::now_v7()),
                space_id,
                kind,
                payload.clone(),
                OffsetDateTime::now_utc(),
            )
            .await
            .unwrap();
    }
    transaction.commit().await.unwrap();
}

/// A page the viewer cannot read still moves the stream to its last row, so the visible event
/// behind it arrives instead of the stream re-reading the same private page forever.
#[tokio::test]
async fn a_full_page_of_unreadable_events_still_advances_the_stream() {
    use super::super::realtime::EventCursor;

    let database = TestDatabase::create("sumi_event_visibility").await;
    let pool = database.pool.clone();
    let adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let (space_id, owner_id, private_channel_id) = event_test_space(&pool).await;
    let visible_payload = json!({"resource_id": Uuid::now_v7()});

    commit_events(
        &pool,
        space_id,
        100,
        "message.created",
        json!({"resource_id": Uuid::now_v7(), "channel_id": private_channel_id}),
    )
    .await;
    commit_events(&pool, space_id, 1, "task.updated", visible_payload.clone()).await;

    let private_page = adapter
        .browser_events(space_id, owner_id, Some(EventCursor::new(0)))
        .await
        .unwrap();
    assert!(
        private_page.events.is_empty(),
        "an event scoped to an unreadable Channel must not reach the viewer"
    );
    assert!(private_page.full);
    let next = private_page
        .scanned
        .expect("a page must hand the stream its last scanned row");
    assert_eq!(next, EventCursor::new(100));

    let following = adapter
        .browser_events(space_id, owner_id, Some(next))
        .await
        .unwrap();
    assert_eq!(following.events.len(), 1);
    assert_eq!(following.events[0].event_type, "task.updated");
    assert_eq!(following.events[0].data, visible_payload);
    assert!(
        !following.full,
        "a page that is not full means the stream caught up"
    );

    // A short page whose tail the viewer cannot read still moves the cursor past that tail.
    commit_events(
        &pool,
        space_id,
        2,
        "message.created",
        json!({"resource_id": Uuid::now_v7(), "channel_id": private_channel_id}),
    )
    .await;
    let private_tail = adapter
        .browser_events(space_id, owner_id, following.scanned)
        .await
        .unwrap();
    assert!(private_tail.events.is_empty() && !private_tail.full);
    assert_eq!(private_tail.scanned, Some(EventCursor::new(103)));

    pool.close().await;
    database.close().await;
}

/// A first connection starts at the Space tail: committed history is not replayed, and the first
/// new commit is delivered from that position.
#[tokio::test]
async fn a_new_stream_starts_at_the_space_tail() {
    use super::super::realtime::EventCursor;

    let database = TestDatabase::create("sumi_event_tail").await;
    let pool = database.pool.clone();
    let adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let (space_id, owner_id, _) = event_test_space(&pool).await;

    commit_events(
        &pool,
        space_id,
        3,
        "task.updated",
        json!({"resource_id": Uuid::now_v7()}),
    )
    .await;
    let tail = adapter.space_event_tail(space_id).await.unwrap();
    assert_eq!(tail, EventCursor::new(3));
    let history = adapter
        .browser_events(space_id, owner_id, Some(tail))
        .await
        .unwrap();
    assert!(
        history.events.is_empty() && history.scanned.is_none(),
        "a first connection must not replay committed history"
    );

    commit_events(
        &pool,
        space_id,
        1,
        "task.updated",
        json!({"resource_id": Uuid::now_v7()}),
    )
    .await;
    let live = adapter
        .browser_events(space_id, owner_id, Some(tail))
        .await
        .unwrap();
    assert_eq!(
        live.events.len(),
        1,
        "only the event after the tail is live"
    );
    assert_eq!(live.events[0].cursor, EventCursor::new(4));
    pool.close().await;
    database.close().await;
}

/// `Last-Event-ID` names an event, not a position: an id outside this Space has no cursor, and the
/// stream reports `context_changed` instead of guessing where to continue.
#[tokio::test]
async fn an_event_from_another_space_has_no_cursor() {
    use super::super::realtime::EventCursor;

    let database = TestDatabase::create("sumi_event_cursor").await;
    let pool = database.pool.clone();
    let adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let (space_id, _, _) = event_test_space(&pool).await;
    let (other_space_id, _, _) = event_test_space(&pool).await;
    let payload = json!({"resource_id": Uuid::now_v7()});
    commit_events(&pool, other_space_id, 1, "task.updated", payload.clone()).await;

    let foreign_event_id: Uuid =
        sqlx::query_scalar("SELECT id FROM outbox_events WHERE space_id=$1")
            .bind(other_space_id.into_uuid())
            .fetch_one(&pool)
            .await
            .unwrap();
    let foreign_event_id = EventId::from_uuid(foreign_event_id);
    assert!(
        adapter
            .event_cursor(space_id, foreign_event_id)
            .await
            .unwrap()
            .is_none(),
        "an event of another Space cannot position this stream"
    );

    commit_events(&pool, space_id, 1, "task.updated", payload).await;
    let own_event_id: Uuid = sqlx::query_scalar("SELECT id FROM outbox_events WHERE space_id=$1")
        .bind(space_id.into_uuid())
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(
        adapter
            .event_cursor(space_id, EventId::from_uuid(own_event_id))
            .await
            .unwrap(),
        Some(EventCursor::new(1))
    );
    pool.close().await;
    database.close().await;
}

/// A Computer socket opening or closing reaches the Browser as one `computer.changed` per actual
/// status change; a repeated write of the same status, such as a superseded session restoring
/// online, adds none.
#[tokio::test]
async fn computer_connection_changes_reach_the_event_stream_once_each() {
    let database = TestDatabase::create("sumi_computer_status").await;
    let pool = database.pool.clone();
    let adapter = PostgresAdapter::new(pool.clone());
    adapter.initialize_schema().await.unwrap();
    let (space_id, owner_id, _) = event_test_space(&pool).await;
    let computer_id = Uuid::now_v7();
    sqlx::query(
        "INSERT INTO computers(id,space_id,name,hostname,os,token_hash,connection_status,created_at) \
         VALUES ($1,$2,'Laptop','localhost','linux','status-hash','offline',now())",
    )
    .bind(computer_id)
    .bind(space_id.into_uuid())
    .execute(&pool)
    .await
    .unwrap();
    let tail = adapter.space_event_tail(space_id).await.unwrap();

    let online = ComputerConnection::Online {
        daemon_version: "1.2.3",
    };
    adapter
        .set_computer_connection(computer_id, online)
        .await
        .unwrap();
    adapter
        .set_computer_connection(
            computer_id,
            ComputerConnection::Online {
                daemon_version: "1.2.3",
            },
        )
        .await
        .unwrap();
    adapter
        .set_computer_connection(computer_id, ComputerConnection::Offline)
        .await
        .unwrap();
    adapter
        .set_computer_connection(
            computer_id,
            ComputerConnection::Online {
                daemon_version: "1.2.4",
            },
        )
        .await
        .unwrap();
    adapter.disconnect_all_computers().await.unwrap();
    adapter.disconnect_all_computers().await.unwrap();

    let page = adapter
        .browser_events(space_id, owner_id, Some(tail))
        .await
        .unwrap();
    let statuses: Vec<_> = page
        .events
        .iter()
        .map(|event| {
            assert_eq!(event.event_type, "computer.changed");
            assert_eq!(
                event.data["resource_id"].as_str(),
                Some(computer_id.to_string().as_str())
            );
            event.data["status"].as_str().unwrap().to_owned()
        })
        .collect();
    assert_eq!(statuses, ["online", "offline", "online", "offline"]);
    let (status, version): (String, Option<String>) =
        sqlx::query_as("SELECT connection_status,daemon_version FROM computers WHERE id=$1")
            .bind(computer_id)
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(
        (status.as_str(), version.as_deref()),
        ("offline", Some("1.2.4"))
    );
    pool.close().await;
    database.close().await;
}
