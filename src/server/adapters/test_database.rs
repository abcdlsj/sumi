//! One throwaway PostgreSQL database per adapter test.

use std::str::FromStr;

use sqlx::{Connection, PgConnection, PgPool, postgres::PgConnectOptions};
use url::Url;
use uuid::Uuid;

use super::postgres::PostgresAdapter;

/// Creates a scratch database from the current baseline and drops it when the test ends.
pub(super) struct TestDatabase {
    name: String,
    admin: PgConnection,
    pub(super) pool: PgPool,
}

impl TestDatabase {
    pub(super) async fn create(prefix: &str) -> Self {
        let database = Self::create_empty(prefix).await;
        PostgresAdapter::new(database.pool.clone())
            .initialize_schema()
            .await
            .unwrap();
        database
    }

    /// Skips the schema, for tests that build or refuse a specific version themselves.
    pub(super) async fn create_empty(prefix: &str) -> Self {
        let admin_url =
            std::env::var("SUMI_TEST_DATABASE_URL").unwrap_or_else(|_| DEFAULT_ADMIN_URL.into());
        let name = format!("{prefix}_{}", Uuid::now_v7().simple());
        let mut admin =
            PgConnection::connect_with(&PgConnectOptions::from_str(&admin_url).unwrap())
                .await
                .unwrap();
        sqlx::query(&format!("CREATE DATABASE \"{name}\""))
            .execute(&mut admin)
            .await
            .unwrap();
        let mut url = Url::parse(&admin_url).unwrap();
        url.set_path(&format!("/{name}"));
        let pool = PgPool::connect(url.as_str()).await.unwrap();
        Self { name, admin, pool }
    }

    pub(super) async fn close(mut self) {
        self.pool.close().await;
        sqlx::query(&format!("DROP DATABASE \"{}\" WITH (FORCE)", self.name))
            .execute(&mut self.admin)
            .await
            .unwrap();
    }
}

const DEFAULT_ADMIN_URL: &str = "postgres://localhost/postgres";
