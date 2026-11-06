use std::{
    collections::HashMap,
    net::{IpAddr, SocketAddr},
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

use axum::{
    extract::{ConnectInfo, FromRequestParts},
    http::{HeaderMap, StatusCode, header, request::Parts},
    response::{IntoResponse, Response},
};

/// One fixed-window limit: at most `attempts` per `window` for each key.
#[derive(Clone, Copy, Debug)]
pub(in crate::server::adapters) struct Limit {
    pub(in crate::server::adapters) scope: &'static str,
    pub(in crate::server::adapters) attempts: u32,
    pub(in crate::server::adapters) window: Duration,
}

/// Sign-in attempts for one email. Bounds password guessing against a single account.
pub(in crate::server::adapters) const LOGIN_PER_EMAIL: Limit = Limit {
    scope: "login.email",
    attempts: 10,
    window: Duration::from_secs(15 * 60),
};
/// Sign-in attempts from one client, across emails.
pub(in crate::server::adapters) const LOGIN_PER_CLIENT: Limit = Limit {
    scope: "login.client",
    attempts: 30,
    window: Duration::from_secs(15 * 60),
};
pub(in crate::server::adapters) const REGISTER_PER_CLIENT: Limit = Limit {
    scope: "register.client",
    attempts: 10,
    window: Duration::from_secs(60 * 60),
};
pub(in crate::server::adapters) const PAIRING_BEGIN_PER_CLIENT: Limit = Limit {
    scope: "pairing.begin.client",
    attempts: 20,
    window: Duration::from_secs(60 * 60),
};
/// Code checks for one pairing. The code has six digits, so ten guesses over the pairing's
/// ten-minute lifetime leave a one in a hundred thousand chance.
pub(in crate::server::adapters) const PAIRING_CODE_PER_PAIRING: Limit = Limit {
    scope: "pairing.code",
    attempts: 10,
    window: Duration::from_secs(10 * 60),
};

/// Per-process fixed-window counters for unauthenticated or credential-checking endpoints.
///
/// Counters live in memory: a restart clears them, and each Server process limits on its own.
#[derive(Clone, Default)]
pub(in crate::server::adapters) struct RateLimits {
    windows: Arc<Mutex<HashMap<(&'static str, String), Window>>>,
}

#[derive(Clone, Copy)]
struct Window {
    started: Instant,
    attempts: u32,
}

/// Entries kept before expired windows are swept on the next check.
const SWEEP_THRESHOLD: usize = 10_000;

impl RateLimits {
    /// Counts one attempt for `key`, rejecting it once the window is full.
    pub(in crate::server::adapters) fn check(
        &self,
        limit: Limit,
        key: &str,
    ) -> Result<(), RateLimited> {
        self.check_at(limit, key, Instant::now())
    }

    fn check_at(&self, limit: Limit, key: &str, now: Instant) -> Result<(), RateLimited> {
        let mut windows = self
            .windows
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        if windows.len() >= SWEEP_THRESHOLD {
            windows.retain(|(scope, _), window| {
                now.duration_since(window.started) < scope_window(scope)
            });
        }
        let window = windows
            .entry((limit.scope, key.to_owned()))
            .or_insert(Window {
                started: now,
                attempts: 0,
            });
        let elapsed = now.duration_since(window.started);
        if elapsed >= limit.window {
            *window = Window {
                started: now,
                attempts: 0,
            };
        }
        if window.attempts >= limit.attempts {
            let remaining = limit.window - now.duration_since(window.started);
            return Err(RateLimited {
                retry_after_secs: remaining.as_secs().max(1),
            });
        }
        window.attempts += 1;
        Ok(())
    }
}

fn scope_window(scope: &str) -> Duration {
    [
        LOGIN_PER_EMAIL,
        LOGIN_PER_CLIENT,
        REGISTER_PER_CLIENT,
        PAIRING_BEGIN_PER_CLIENT,
        PAIRING_CODE_PER_PAIRING,
    ]
    .into_iter()
    .find(|limit| limit.scope == scope)
    .map_or(Duration::ZERO, |limit| limit.window)
}

#[derive(Debug)]
pub(in crate::server::adapters) struct RateLimited {
    pub(in crate::server::adapters) retry_after_secs: u64,
}

impl IntoResponse for RateLimited {
    fn into_response(self) -> Response {
        (
            StatusCode::TOO_MANY_REQUESTS,
            [(header::RETRY_AFTER, self.retry_after_secs.to_string())],
            axum::Json(super::ErrorEnvelope {
                error: super::ErrorBody {
                    code: "rate_limited",
                    message: "too many attempts; retry later",
                    retryable: true,
                    retry_after_seconds: Some(self.retry_after_secs),
                },
            }),
        )
            .into_response()
    }
}

/// Error of a rate-limited endpoint: either the limit or the endpoint's own failure.
pub(in crate::server::adapters) enum LimitedError {
    Limited(RateLimited),
    Api(super::ApiError),
}

impl From<RateLimited> for LimitedError {
    fn from(value: RateLimited) -> Self {
        Self::Limited(value)
    }
}

impl From<super::ApiError> for LimitedError {
    fn from(value: super::ApiError) -> Self {
        Self::Api(value)
    }
}

impl IntoResponse for LimitedError {
    fn into_response(self) -> Response {
        match self {
            Self::Limited(limited) => limited.into_response(),
            Self::Api(error) => error.into_response(),
        }
    }
}

/// The address a rate limit counts a request against.
///
/// A peer on a loopback or private address is taken to be a reverse proxy, so the first
/// `X-Forwarded-For` hop names the client; a public peer is the client itself, so a forged header
/// from the internet cannot pick its own key.
pub(in crate::server::adapters) struct ClientAddress(pub(in crate::server::adapters) String);

impl<S: Send + Sync> FromRequestParts<S> for ClientAddress {
    type Rejection = std::convert::Infallible;

    async fn from_request_parts(parts: &mut Parts, _: &S) -> Result<Self, Self::Rejection> {
        let peer = parts
            .extensions
            .get::<ConnectInfo<SocketAddr>>()
            .map(|ConnectInfo(address)| address.ip());
        Ok(Self(client_address(peer, &parts.headers)))
    }
}

fn client_address(peer: Option<IpAddr>, headers: &HeaderMap) -> String {
    let proxied = peer.is_none_or(|peer| match peer {
        IpAddr::V4(address) => address.is_loopback() || address.is_private(),
        IpAddr::V6(address) => address.is_loopback() || address.is_unique_local(),
    });
    let forwarded = proxied
        .then(|| {
            headers
                .get("x-forwarded-for")
                .and_then(|value| value.to_str().ok())
                .and_then(|value| value.split(',').next())
                .and_then(|first| first.trim().parse::<IpAddr>().ok())
        })
        .flatten();
    forwarded
        .or(peer)
        .map_or_else(|| "unknown".to_owned(), |address| address.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    const LIMIT: Limit = Limit {
        scope: "login.email",
        attempts: 2,
        window: Duration::from_secs(60),
    };

    #[test]
    fn a_full_window_rejects_until_it_rolls_over() {
        let limits = RateLimits::default();
        let start = Instant::now();
        assert!(limits.check_at(LIMIT, "ada", start).is_ok());
        assert!(limits.check_at(LIMIT, "ada", start).is_ok());
        let rejected = limits
            .check_at(LIMIT, "ada", start + Duration::from_secs(20))
            .unwrap_err();
        assert_eq!(rejected.retry_after_secs, 40);
        // Another key has its own window.
        assert!(limits.check_at(LIMIT, "grace", start).is_ok());
        assert!(
            limits
                .check_at(LIMIT, "ada", start + Duration::from_secs(60))
                .is_ok()
        );
    }

    #[tokio::test]
    async fn the_rejection_reuses_the_error_envelope_with_the_wait() {
        let response = RateLimited {
            retry_after_secs: 42,
        }
        .into_response();
        assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);
        assert_eq!(
            response
                .headers()
                .get(header::RETRY_AFTER)
                .and_then(|value| value.to_str().ok()),
            Some("42")
        );
        let body: serde_json::Value = serde_json::from_slice(
            &axum::body::to_bytes(response.into_body(), usize::MAX)
                .await
                .unwrap(),
        )
        .unwrap();
        assert_eq!(body["error"]["code"], "rate_limited");
        assert_eq!(body["error"]["retryable"], true);
        assert_eq!(body["error"]["retry_after_seconds"], 42);
    }

    #[test]
    fn only_a_private_peer_may_name_the_client() {
        let mut headers = HeaderMap::new();
        headers.insert("x-forwarded-for", "203.0.113.7, 10.0.0.2".parse().unwrap());
        let proxy: IpAddr = "10.0.0.2".parse().unwrap();
        let public: IpAddr = "198.51.100.4".parse().unwrap();
        assert_eq!(client_address(Some(proxy), &headers), "203.0.113.7");
        assert_eq!(client_address(Some(public), &headers), "198.51.100.4");
        assert_eq!(client_address(Some(proxy), &HeaderMap::new()), "10.0.0.2");
    }
}
