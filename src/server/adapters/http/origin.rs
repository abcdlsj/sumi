use super::*;
use axum::{
    extract::Request,
    http::Method,
    middleware::{self, Next},
};
use tower_http::cors::CorsLayer;

/// Lets a Web UI served from `web_origin` call the API with its Browser Session cookie, and keeps
/// Browser Session writes on an allowed origin even when no separate Web UI is configured.
///
/// CORS only decides whether that page may read a response. A write from any other page that
/// still carries the cookie (a sibling host on the same site) is rejected by its `Origin` before
/// it reaches a handler, so Browser Session writes stay limited to the Web UI.
pub(in crate::server::adapters) fn allow_web_origin(
    router: Router,
    web_origin: Option<&str>,
) -> Router {
    let allowed = web_origin.map(|origin| HeaderValue::from_str(origin).expect("validated origin"));
    let guard_allowed = allowed.clone();
    let router = router.layer(middleware::from_fn(move |request: Request, next: Next| {
        let allowed = guard_allowed.clone();
        async move {
            if writes_from_foreign_origin(&request, allowed.as_ref()) {
                return ApiError {
                    status: StatusCode::FORBIDDEN,
                    code: "permission_denied",
                    message: "request origin is not allowed",
                }
                .into_response();
            }
            next.run(request).await
        }
    }));
    match allowed {
        Some(allowed) => router.layer(
            CorsLayer::new()
                .allow_origin(allowed)
                .allow_credentials(true)
                .allow_methods([
                    Method::GET,
                    Method::POST,
                    Method::PUT,
                    Method::PATCH,
                    Method::DELETE,
                ])
                .allow_headers([
                    header::CONTENT_TYPE,
                    header::HeaderName::from_static("idempotency-key"),
                    header::HeaderName::from_static("last-event-id"),
                ]),
        ),
        None => router,
    }
}

fn writes_from_foreign_origin(request: &Request, allowed: Option<&HeaderValue>) -> bool {
    if matches!(
        *request.method(),
        Method::GET | Method::HEAD | Method::OPTIONS
    ) {
        return false;
    }
    // Computers and the Agent CLI send no Origin and authenticate with bearer tokens.
    let Some(origin) = request.headers().get(header::ORIGIN) else {
        return false;
    };
    if allowed == Some(origin) {
        return false;
    }
    // An opaque or malformed Origin cannot be the Server's own origin.
    let Some(origin) = origin
        .to_str()
        .ok()
        .and_then(|origin| url::Url::parse(origin).ok())
    else {
        return true;
    };
    !is_server_origin(request, &origin)
}

/// The Web UI this Server serves itself is same-origin, so its writes pass without configuration.
/// The comparison covers scheme, host and port: a page on another scheme of the same host is a
/// different origin and carries no valid Browser Session write.
fn is_server_origin(request: &Request, origin: &url::Url) -> bool {
    let Some(host) = request
        .headers()
        .get(header::HOST)
        .and_then(|host| host.to_str().ok())
    else {
        return false;
    };
    let Some(scheme) = request_scheme(request) else {
        return false;
    };
    let Ok(server) = url::Url::parse(&format!("{scheme}://{host}")) else {
        return false;
    };
    origin.scheme() == server.scheme()
        && origin.host_str() == server.host_str()
        && origin.port_or_known_default() == server.port_or_known_default()
}

/// The Server terminates plain HTTP; TLS is added by a reverse proxy that must forward the scheme.
fn request_scheme(request: &Request) -> Option<&'static str> {
    let forwarded = request
        .headers()
        .get("forwarded")
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.split(',').next())
        .and_then(|first| {
            first
                .split(';')
                .find_map(|part| part.trim().strip_prefix("proto="))
        });
    let proto = forwarded.map_or_else(
        || {
            request
                .headers()
                .get("x-forwarded-proto")
                .and_then(|value| value.to_str().ok())
                .and_then(|value| value.split(',').next())
        },
        Some,
    );
    match proto.map(str::trim).map(|proto| proto.trim_matches('"')) {
        Some("https") => Some("https"),
        Some("http") | None => Some("http"),
        Some(_) => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::Body;
    use tower::ServiceExt;

    const WEB_ORIGIN: &str = "https://app.example.com";

    fn app(web_origin: Option<&str>) -> Router {
        allow_web_origin(
            Router::new().route("/write", post(|| async { StatusCode::NO_CONTENT })),
            web_origin,
        )
    }

    async fn write(
        web_origin: Option<&str>,
        host: &str,
        origin: Option<&str>,
        forwarded_proto: Option<&str>,
    ) -> Response {
        let mut request = Request::post("/write").header(header::HOST, host);
        if let Some(origin) = origin {
            request = request.header(header::ORIGIN, origin);
        }
        if let Some(proto) = forwarded_proto {
            request = request.header("x-forwarded-proto", proto);
        }
        app(web_origin)
            .oneshot(request.body(Body::empty()).unwrap())
            .await
            .unwrap()
    }

    #[tokio::test]
    async fn writes_are_limited_to_the_web_origin_and_bearer_clients() {
        let web = write(
            Some(WEB_ORIGIN),
            "api.example.com:8443",
            Some(WEB_ORIGIN),
            Some("https"),
        )
        .await;
        assert_eq!(web.status(), StatusCode::NO_CONTENT);
        assert_eq!(
            web.headers()[header::ACCESS_CONTROL_ALLOW_ORIGIN],
            WEB_ORIGIN
        );
        assert_eq!(
            web.headers()[header::ACCESS_CONTROL_ALLOW_CREDENTIALS],
            "true"
        );
        assert_eq!(
            write(Some(WEB_ORIGIN), "api.example.com:8443", None, None)
                .await
                .status(),
            StatusCode::NO_CONTENT
        );
        assert_eq!(
            write(
                Some(WEB_ORIGIN),
                "api.example.com:8443",
                Some("https://api.example.com:8443"),
                Some("https")
            )
            .await
            .status(),
            StatusCode::NO_CONTENT
        );
        assert_eq!(
            write(
                Some(WEB_ORIGIN),
                "api.example.com:8443",
                Some("https://evil.example.com"),
                None
            )
            .await
            .status(),
            StatusCode::FORBIDDEN
        );
    }

    #[tokio::test]
    async fn the_server_origin_is_compared_with_scheme_host_and_port() {
        // Same host and port on another scheme is another origin.
        assert_eq!(
            write(
                Some(WEB_ORIGIN),
                "api.example.com:8443",
                Some("http://api.example.com:8443"),
                Some("https")
            )
            .await
            .status(),
            StatusCode::FORBIDDEN
        );
        // A default port is the same origin as an omitted one.
        assert_eq!(
            write(
                Some(WEB_ORIGIN),
                "app.example.com",
                Some("http://app.example.com"),
                None
            )
            .await
            .status(),
            StatusCode::NO_CONTENT
        );
        assert_eq!(
            write(
                Some(WEB_ORIGIN),
                "app.example.com:80",
                Some("http://app.example.com"),
                None
            )
            .await
            .status(),
            StatusCode::NO_CONTENT
        );
        // The configured Web UI origin is a full origin, not a host.
        assert_eq!(
            write(
                Some(WEB_ORIGIN),
                "api.example.com",
                Some("http://app.example.com"),
                None
            )
            .await
            .status(),
            StatusCode::FORBIDDEN
        );
        assert_eq!(
            write(
                Some(WEB_ORIGIN),
                "api.example.com",
                Some("https://app.example.com:8443"),
                None
            )
            .await
            .status(),
            StatusCode::FORBIDDEN
        );
        // An unknown forwarded scheme cannot be confirmed as the Server's own.
        assert_eq!(
            write(
                Some(WEB_ORIGIN),
                "api.example.com",
                Some("wss://api.example.com"),
                Some("wss")
            )
            .await
            .status(),
            StatusCode::FORBIDDEN
        );
    }

    #[tokio::test]
    async fn an_unconfigured_server_still_rejects_cross_origin_writes() {
        assert_eq!(
            write(None, "127.0.0.1:3000", Some("http://127.0.0.1:3000"), None)
                .await
                .status(),
            StatusCode::NO_CONTENT
        );
        assert_eq!(
            write(None, "127.0.0.1:3000", Some("https://127.0.0.1:3000"), None)
                .await
                .status(),
            StatusCode::FORBIDDEN
        );
        assert_eq!(
            write(None, "127.0.0.1:3000", Some("http://evil.example"), None)
                .await
                .status(),
            StatusCode::FORBIDDEN
        );
        assert_eq!(
            write(None, "127.0.0.1:3000", Some("null"), None)
                .await
                .status(),
            StatusCode::FORBIDDEN
        );
        assert_eq!(
            write(None, "127.0.0.1:3000", None, None).await.status(),
            StatusCode::NO_CONTENT
        );
    }

    #[tokio::test]
    async fn preflight_allows_the_headers_the_web_ui_sends() {
        let response = app(Some(WEB_ORIGIN))
            .oneshot(
                Request::options("/write")
                    .header(header::ORIGIN, WEB_ORIGIN)
                    .header(header::ACCESS_CONTROL_REQUEST_METHOD, "POST")
                    .header(
                        header::ACCESS_CONTROL_REQUEST_HEADERS,
                        "content-type,idempotency-key",
                    )
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert!(response.status().is_success());
        let allowed = response.headers()[header::ACCESS_CONTROL_ALLOW_HEADERS]
            .to_str()
            .unwrap()
            .to_ascii_lowercase();
        assert!(allowed.contains("idempotency-key"));
    }
}
