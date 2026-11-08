//! One throwaway PostgreSQL database per adapter test.

use std::str::FromStr;

use sqlx::{Connection, PgConnection, PgPool, postgres::PgConnectOptions};
use url::Url;
use uuid::Uuid;

use super::postgres::PostgresAdapter;

/// Creates a scratch database from the current baseline and drops it when the test ends, including
/// when the test panics before calling `close`.
pub(super) struct TestDatabase {
    name: String,
    admin_url: String,
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
        Self {
            name,
            admin_url,
            pool,
        }
    }

    /// Closes the pool early so a test can observe the database without this process connected.
    pub(super) async fn close(self) {
        self.pool.close().await;
    }
}

impl Drop for TestDatabase {
    /// A panicking test never reaches `close`, and a leaked database would make the next run collide
    /// with a name it cannot explain. `Drop` cannot await, so the cleanup opens its own short-lived
    /// runtime and admin connection; `WITH (FORCE)` disconnects the pool this value still holds.
    fn drop(&mut self) {
        let name = self.name.clone();
        let admin_url = self.admin_url.clone();
        std::thread::spawn(move || {
            let Ok(runtime) = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
            else {
                return;
            };
            runtime.block_on(async move {
                let Ok(options) = PgConnectOptions::from_str(&admin_url) else {
                    return;
                };
                let Ok(mut admin) = PgConnection::connect_with(&options).await else {
                    return;
                };
                let _ = sqlx::query(&format!("DROP DATABASE \"{name}\" WITH (FORCE)"))
                    .execute(&mut admin)
                    .await;
            });
        })
        .join()
        .expect("database cleanup thread");
    }
}

const DEFAULT_ADMIN_URL: &str = "postgres://localhost/postgres";

#[cfg(test)]
mod tests {
    use super::*;

    /// The same path a panicking test takes: dropping the value removes the scratch database even
    /// while its pool has not been closed.
    #[tokio::test]
    async fn dropping_the_scratch_database_removes_it() {
        let database = TestDatabase::create("sumi_drop_cleanup").await;
        let name = database.name.clone();
        let admin_url = database.admin_url.clone();

        drop(database);

        let mut admin =
            PgConnection::connect_with(&PgConnectOptions::from_str(&admin_url).unwrap())
                .await
                .unwrap();
        let remaining: bool =
            sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)")
                .bind(&name)
                .fetch_one(&mut admin)
                .await
                .unwrap();
        assert!(!remaining, "the scratch database must be gone after drop");
    }
}
