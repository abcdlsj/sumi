use super::*;
use axum::{
    extract::Request,
    http::Method,
    middleware::{self, Next},
};
use tower_http::cors::CorsLayer;

/// Lets a Web UI served from `web_origin` call the API with its Browser Session cookie.
///
/// CORS only decides whether that page may read a response. A write from any other page that
/// still carries the cookie (a sibling host on the same site) is rejected by its `Origin` before
/// it reaches a handler, so Browser Session writes stay limited to the Web UI.
pub(in crate::server::adapters) fn allow_web_origin(router: Router, web_origin: &str) -> Router {
    let allowed = HeaderValue::from_str(web_origin).expect("validated web origin");
    let cors = CorsLayer::new()
        .allow_origin(allowed.clone())
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
        ]);
    router
        .layer(middleware::from_fn(move |request: Request, next: Next| {
            let allowed = allowed.clone();
            async move {
                if writes_from_foreign_origin(&request, &allowed) {
                    return ApiError {
                        status: StatusCode::FORBIDDEN,
                        code: "permission_denied",
                        message: "request origin is not allowed",
                    }
                    .into_response();
                }
                next.run(request).await
            }
        }))
        .layer(cors)
}

fn writes_from_foreign_origin(request: &Request, allowed: &HeaderValue) -> bool {
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
    if origin == allowed {
        return false;
    }
    // A Web UI served by this Server itself stays same-origin.
    let same_host = origin
        .to_str()
        .ok()
        .and_then(|origin| url::Url::parse(origin).ok())
        .zip(request.headers().get(header::HOST))
        .is_some_and(|(origin, host)| {
            let authority = match (origin.host_str(), origin.port()) {
                (Some(name), Some(port)) => format!("{name}:{port}"),
                (Some(name), None) => name.to_owned(),
                (None, _) => return false,
            };
            host.as_bytes().eq_ignore_ascii_case(authority.as_bytes())
        });
    !same_host
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::Body;
    use tower::ServiceExt;

    fn app() -> Router {
        allow_web_origin(
            Router::new().route("/write", post(|| async { StatusCode::NO_CONTENT })),
            "https://app.example.com",
        )
    }

    async fn write(origin: Option<&str>) -> Response {
        let mut request = Request::post("/write").header(header::HOST, "api.example.com:8443");
        if let Some(origin) = origin {
            request = request.header(header::ORIGIN, origin);
        }
        app()
            .oneshot(request.body(Body::empty()).unwrap())
            .await
            .unwrap()
    }

    #[tokio::test]
    async fn writes_are_limited_to_the_web_origin_and_bearer_clients() {
        let web = write(Some("https://app.example.com")).await;
        assert_eq!(web.status(), StatusCode::NO_CONTENT);
        assert_eq!(
            web.headers()[header::ACCESS_CONTROL_ALLOW_ORIGIN],
            "https://app.example.com"
        );
        assert_eq!(
            web.headers()[header::ACCESS_CONTROL_ALLOW_CREDENTIALS],
            "true"
        );
        assert_eq!(write(None).await.status(), StatusCode::NO_CONTENT);
        assert_eq!(
            write(Some("https://api.example.com:8443")).await.status(),
            StatusCode::NO_CONTENT
        );
        assert_eq!(
            write(Some("https://evil.example.com")).await.status(),
            StatusCode::FORBIDDEN
        );
    }

    #[tokio::test]
    async fn preflight_allows_the_headers_the_web_ui_sends() {
        let response = app()
            .oneshot(
                Request::options("/write")
                    .header(header::ORIGIN, "https://app.example.com")
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
