use super::*;

pub(in crate::server::adapters::http) fn api_to_capability(
    error: ApiError,
) -> wire_capability::Error {
    let code = if error.code == "context_changed" {
        wire_capability::ErrorCode::ContextChanged
    } else {
        match error.status {
            StatusCode::BAD_REQUEST | StatusCode::UNPROCESSABLE_ENTITY => {
                wire_capability::ErrorCode::InvalidArgument
            }
            StatusCode::NOT_FOUND => wire_capability::ErrorCode::NotFound,
            StatusCode::FORBIDDEN => wire_capability::ErrorCode::PermissionDenied,
            StatusCode::CONFLICT => wire_capability::ErrorCode::Conflict,
            _ => wire_capability::ErrorCode::Internal,
        }
    };
    capability_error(code, error.message, false)
}

pub(in crate::server::adapters::http) fn app_to_capability(
    error: ApplicationError,
) -> wire_capability::Error {
    let class = classify(&error);
    wire_capability::Error {
        code: wire_capability::ErrorCode::parse(class.code),
        message: class.message.to_owned(),
        retryable: class.retryable,
        details: Default::default(),
    }
}

/// Authorizes a Run-scoped call. The Computer's own token plus a `working` Run hosted by that
/// Computer is the whole proof: there is no per-Run credential and no deadline to check.
pub(in crate::server::adapters::http) async fn require_active_agent_run(
    state: &RuntimeState,
    headers: &HeaderMap,
    computer_id: Uuid,
    agent_id: Uuid,
    run_id: Uuid,
) -> Result<Uuid, ApiError> {
    let computer_token = bearer_token(headers)?;
    state
        .read
        .active_run_proof(computer_id, &token_hash(computer_token), agent_id, run_id)
        .await
        .map_err(application_error)?
        .ok_or_else(ApiError::unauthenticated)
}

pub(in crate::server::adapters::http) fn capability_value(
    value: &impl serde::Serialize,
) -> Result<Value, wire_capability::Error> {
    serde_json::to_value(value).map_err(|_| {
        capability_error(
            wire_capability::ErrorCode::Internal,
            "projection could not be encoded",
            false,
        )
    })
}
