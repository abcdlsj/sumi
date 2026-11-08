use super::*;
use crate::server::domain::DomainError;
use serde::Serialize;
use utoipa::ToSchema;

#[derive(Debug)]
pub(in crate::server::adapters) struct ApiError {
    pub(in crate::server::adapters) status: StatusCode,
    pub(in crate::server::adapters) code: &'static str,
    pub(in crate::server::adapters) message: &'static str,
    /// Whether repeating the same request can succeed without changing it.
    pub(in crate::server::adapters) retryable: bool,
}

impl ApiError {
    pub(in crate::server::adapters) fn unauthenticated() -> Self {
        Self {
            status: StatusCode::UNAUTHORIZED,
            code: "unauthenticated",
            message: "Browser Session is missing or expired",
            retryable: false,
        }
    }

    pub(in crate::server::adapters) fn invalid(message: &'static str) -> Self {
        Self {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_argument",
            message,
            retryable: false,
        }
    }

    pub(in crate::server::adapters) fn not_found() -> Self {
        Self {
            status: StatusCode::NOT_FOUND,
            code: "not_found",
            message: "resource was not found",
            retryable: false,
        }
    }

    pub(in crate::server::adapters) fn internal() -> Self {
        Self {
            status: StatusCode::INTERNAL_SERVER_ERROR,
            code: "internal",
            message: "Server could not complete the request",
            retryable: false,
        }
    }

    pub(in crate::server::adapters) fn computer_unreachable() -> Self {
        Self {
            status: StatusCode::SERVICE_UNAVAILABLE,
            code: "computer_unreachable",
            message: "Computer did not answer the query",
            retryable: true,
        }
    }

    pub(in crate::server::adapters) fn context_changed() -> Self {
        let class = ErrorClass::context_changed();
        Self {
            status: class.status,
            code: class.code,
            message: class.message,
            retryable: class.retryable,
        }
    }
}

/// Single source of truth for mapping an `ApplicationError` to the HTTP wire
/// contract shared by REST responses and Agent capability responses.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(in crate::server::adapters) struct ErrorClass {
    pub(in crate::server::adapters) status: StatusCode,
    pub(in crate::server::adapters) code: &'static str,
    pub(in crate::server::adapters) message: &'static str,
    pub(in crate::server::adapters) retryable: bool,
}

impl ErrorClass {
    /// The write was prepared against a context that has moved on. The caller re-reads, then retries
    /// once, so the rejection is not blindly retryable.
    pub(in crate::server::adapters) fn context_changed() -> Self {
        Self {
            status: StatusCode::CONFLICT,
            code: "context_changed",
            message: "Run context changed before the write; re-read the current context, then retry once",
            retryable: false,
        }
    }
}

pub(in crate::server::adapters) fn classify(error: &ApplicationError) -> ErrorClass {
    match error {
        ApplicationError::NotFound => ErrorClass {
            status: StatusCode::NOT_FOUND,
            code: "not_found",
            message: "resource was not found",
            retryable: false,
        },
        ApplicationError::Unauthenticated => ErrorClass {
            status: StatusCode::UNAUTHORIZED,
            code: "unauthenticated",
            message: "credential is missing or expired",
            retryable: false,
        },
        ApplicationError::PayloadTooLarge => ErrorClass {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_argument",
            message: "request payload exceeds the configured limit",
            retryable: false,
        },
        ApplicationError::PermissionDenied => ErrorClass {
            status: StatusCode::FORBIDDEN,
            code: "permission_denied",
            message: "actor is not allowed to perform this action",
            retryable: false,
        },
        ApplicationError::Conflict => ErrorClass {
            status: StatusCode::CONFLICT,
            code: "conflict",
            message: "request conflicts with current state",
            retryable: false,
        },
        ApplicationError::ContextChanged => ErrorClass::context_changed(),
        ApplicationError::StaleMessageContext => ErrorClass {
            status: StatusCode::CONFLICT,
            code: "context_changed",
            message: "The Focus Thread has Messages you have not read; read the Thread, then retry once",
            retryable: false,
        },
        ApplicationError::Unavailable => ErrorClass {
            status: StatusCode::SERVICE_UNAVAILABLE,
            code: "unavailable",
            message: "external dependency is unavailable",
            retryable: true,
        },
        ApplicationError::Internal => ErrorClass {
            status: StatusCode::INTERNAL_SERVER_ERROR,
            code: "internal",
            message: "server adapter failed",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::RunNotWorkingForAgent) => {
            ErrorClass::context_changed()
        }
        ApplicationError::Domain(DomainError::GovernorRequired) => ErrorClass {
            status: StatusCode::FORBIDDEN,
            code: "permission_denied",
            message: "Space Owner or Admin access is required",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::InvalidCredential) => ErrorClass {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_argument",
            message: "display name, email, and a password of at least 12 characters are required",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::InvalidPairing) => ErrorClass {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_argument",
            message: "Computer pairing request is invalid",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::InvalidChannelSlug) => ErrorClass {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_argument",
            message: "Use 1-32 lowercase ASCII letters or numbers separated by single hyphens for the Channel slug. Use topic for the human-readable description; topic supports Unicode.",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::EmptyTaskResult) => ErrorClass {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_argument",
            message: "Task Result must not be empty",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::InvalidAgentName) => ErrorClass {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_argument",
            message: "Display Name must be 1-40 letters or underscores",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::InvalidRole) => ErrorClass {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_argument",
            message: "Role is required and must be at most 12000 characters",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::PairingLapsed) => ErrorClass {
            status: StatusCode::CONFLICT,
            code: "pairing_lapsed",
            message: "Computer pairing expired or was already confirmed",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::InvalidAttachment) => ErrorClass {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_argument",
            message: "Attachment name and media type are required",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::AttachmentContentMismatch) => ErrorClass {
            status: StatusCode::BAD_REQUEST,
            code: "invalid_argument",
            message: "Attachment size or SHA-256 does not match uploaded content",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::AttachmentNotOpen) => ErrorClass {
            status: StatusCode::CONFLICT,
            code: "conflict",
            message: "Attachment upload is not open",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::AttachmentNotOwned) => ErrorClass {
            status: StatusCode::FORBIDDEN,
            code: "permission_denied",
            message: "actor is not allowed to perform this action",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::AttachmentNotReady) => ErrorClass {
            status: StatusCode::NOT_FOUND,
            code: "not_found",
            message: "resource was not found",
            retryable: false,
        },
        ApplicationError::Domain(DomainError::ComputerHasAgents) => ErrorClass {
            status: StatusCode::CONFLICT,
            code: "computer_has_agents",
            message: "Computer still has assigned Agents",
            retryable: false,
        },
        ApplicationError::Domain(_) => ErrorClass {
            status: StatusCode::CONFLICT,
            code: "conflict",
            message: "request conflicts with current state",
            retryable: false,
        },
    }
}

pub(super) fn bearer_token(headers: &HeaderMap) -> Result<&str, ApiError> {
    headers
        .get("Authorization")
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
        .filter(|value| !value.is_empty())
        .ok_or_else(ApiError::unauthenticated)
}

pub(in crate::server::adapters) fn application_error(error: ApplicationError) -> ApiError {
    ApiError::from(error)
}

pub(in crate::server::adapters) fn token_hash(token: &str) -> String {
    hex::encode(Sha256::digest(token.as_bytes()))
}

/// Every Browser API failure, from any adapter layer, is this one body.
#[derive(Serialize, ToSchema)]
pub(in crate::server::adapters) struct ErrorEnvelope {
    pub(in crate::server::adapters) error: ErrorBody,
}

#[derive(Serialize, ToSchema)]
pub(in crate::server::adapters) struct ErrorBody {
    pub(in crate::server::adapters) code: &'static str,
    pub(in crate::server::adapters) message: &'static str,
    pub(in crate::server::adapters) retryable: bool,
    /// Present only for `rate_limited`, so a client can wait exactly as long as the Server says.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(in crate::server::adapters) retry_after_seconds: Option<u64>,
}

impl From<ApplicationError> for ApiError {
    fn from(error: ApplicationError) -> Self {
        let class = classify(&error);
        Self {
            status: class.status,
            code: class.code,
            message: class.message,
            retryable: class.retryable,
        }
    }
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        (
            self.status,
            Json(ErrorEnvelope {
                error: ErrorBody {
                    code: self.code,
                    message: self.message,
                    retryable: self.retryable,
                    retry_after_seconds: None,
                },
            }),
        )
            .into_response()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn classify_context_changed_is_actionable_and_not_blindly_retryable() {
        let stale = classify(&ApplicationError::StaleMessageContext);
        assert_eq!(stale.code, "context_changed");
        assert!(!stale.retryable);
        assert!(stale.message.contains("read the Thread"));
        assert!(stale.message.contains("retry once"));

        let changed = classify(&ApplicationError::ContextChanged);
        assert_eq!(changed.code, "context_changed");
        assert!(!changed.retryable);
        assert!(changed.message.contains("re-read the current context"));
    }

    #[tokio::test]
    async fn the_error_body_carries_the_classified_retryable_flag() {
        let unavailable = ApiError::from(ApplicationError::Unavailable).into_response();
        assert_eq!(unavailable.status(), StatusCode::SERVICE_UNAVAILABLE);
        let body: serde_json::Value = serde_json::from_slice(
            &axum::body::to_bytes(unavailable.into_body(), usize::MAX)
                .await
                .unwrap(),
        )
        .unwrap();
        assert_eq!(body["error"]["code"], "unavailable");
        assert_eq!(body["error"]["retryable"], true);

        let missing = ApiError::from(ApplicationError::NotFound).into_response();
        let body: serde_json::Value = serde_json::from_slice(
            &axum::body::to_bytes(missing.into_body(), usize::MAX)
                .await
                .unwrap(),
        )
        .unwrap();
        assert_eq!(body["error"]["retryable"], false);
    }

    #[test]
    fn classify_marks_transient_dependencies_retryable() {
        let unavailable = classify(&ApplicationError::Unavailable);
        assert_eq!(unavailable.code, "unavailable");
        assert!(unavailable.retryable);
    }
}
