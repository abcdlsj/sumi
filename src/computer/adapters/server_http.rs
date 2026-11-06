//! The daemon's HTTP adapter for the Server: Agent capability actions and Attachment proxying.

use std::path::Path;

use serde::Serialize;
use sha2::{Digest, Sha256};
use uuid::Uuid;

use crate::{
    computer::application::ApplicationError,
    ids::{AttachmentId, IdempotencyKey},
    protocol::capability,
};

use super::AgentHomeAdapter;

#[derive(Serialize)]
pub(in crate::computer) struct AgentActionRequest {
    pub(in crate::computer) context: capability::RunContext,
    pub(in crate::computer) action: capability::Action,
    pub(in crate::computer) idempotency_key: Option<IdempotencyKey>,
}

#[allow(clippy::too_many_arguments)]
pub(in crate::computer) async fn proxy_attachment_upload(
    client: &reqwest::Client,
    server_url: &url::Url,
    computer_id: Uuid,
    token: &str,
    computer_home: &Path,
    context: &capability::RunContext,
    path: &str,
    idempotency_key: Option<IdempotencyKey>,
) -> capability::Response<serde_json::Value> {
    let Some(idempotency_key) = idempotency_key else {
        return capability_error_response(
            capability::ErrorCode::InvalidArgument,
            "idempotency key is required",
            false,
        );
    };
    let homes = AgentHomeAdapter::new(computer_home.to_path_buf(), None, None, None);
    let (name, content) = match homes
        .read_attachment_source(context.agent_id, Path::new(path))
        .await
    {
        Ok(source) => source,
        Err(error) => return local_attachment_error(error),
    };
    let base = match server_url.join(&format!(
        "api/v1/computers/{computer_id}/agents/{}/runs/{}/attachments/",
        context.agent_id, context.run_id
    )) {
        Ok(base) => base,
        Err(_) => return capability_failure(),
    };
    let created = match client
        .post(
            base.join("uploads")
                .expect("relative Attachment URL is valid"),
        )
        .bearer_auth(token)
        .header("idempotency-key", idempotency_key.to_string())
        .json(&serde_json::json!({
            "original_name": name,
            "media_type": "application/octet-stream"
        }))
        .send()
        .await
    {
        Ok(response) => match attachment_json(response).await {
            Ok(value) => value,
            Err(error) => return error,
        },
        Err(_) => return capability_failure(),
    };
    let Some(attachment_id) = created
        .get("id")
        .and_then(serde_json::Value::as_str)
        .and_then(|value| Uuid::parse_str(value).ok())
    else {
        return capability_failure();
    };
    let content_url = base
        .join(&format!("{attachment_id}/content"))
        .expect("relative Attachment URL is valid");
    let uploaded = client
        .put(content_url)
        .bearer_auth(token)
        .body(content.clone())
        .send()
        .await;
    match uploaded {
        Ok(response) if response.status().is_success() => {}
        Ok(response) => return attachment_http_error(response).await,
        Err(_) => return capability_failure(),
    }
    let digest = hex::encode(Sha256::digest(&content));
    match client
        .post(
            base.join(&format!("{attachment_id}/complete"))
                .expect("relative Attachment URL is valid"),
        )
        .bearer_auth(token)
        .header("idempotency-key", idempotency_key.to_string())
        .json(&serde_json::json!({"size":content.len(),"sha256":digest}))
        .send()
        .await
    {
        Ok(response) => match attachment_json(response).await {
            Ok(value) => capability::Response::success(value),
            Err(error) => error,
        },
        Err(_) => capability_failure(),
    }
}

#[allow(clippy::too_many_arguments)]
pub(in crate::computer) async fn proxy_attachment_download(
    client: &reqwest::Client,
    server_url: &url::Url,
    computer_id: Uuid,
    token: &str,
    computer_home: &Path,
    context: &capability::RunContext,
    attachment_id: AttachmentId,
    output: &str,
) -> capability::Response<serde_json::Value> {
    let endpoint = match server_url.join(&format!(
        "api/v1/computers/{computer_id}/agents/{}/runs/{}/attachments/{attachment_id}/download",
        context.agent_id, context.run_id
    )) {
        Ok(endpoint) => endpoint,
        Err(_) => return capability_failure(),
    };
    let response = match client.get(endpoint).bearer_auth(token).send().await {
        Ok(response) => response,
        Err(_) => return capability_failure(),
    };
    if !response.status().is_success() {
        return attachment_http_error(response).await;
    }
    let content = match response.bytes().await {
        Ok(content) => content,
        Err(_) => return capability_failure(),
    };
    let homes = AgentHomeAdapter::new(computer_home.to_path_buf(), None, None, None);
    if let Err(error) = homes
        .write_attachment_output(context.agent_id, Path::new(output), &content)
        .await
    {
        return local_attachment_error(error);
    }
    capability::Response::success(serde_json::json!({
        "attachment_id": attachment_id,
        "output": output,
        "size": content.len(),
        "sha256": hex::encode(Sha256::digest(&content))
    }))
}

pub(in crate::computer) async fn attachment_json(
    response: reqwest::Response,
) -> Result<serde_json::Value, capability::Response<serde_json::Value>> {
    if !response.status().is_success() {
        return Err(attachment_http_error(response).await);
    }
    response.json().await.map_err(|_| capability_failure())
}

pub(in crate::computer) async fn attachment_http_error(
    response: reqwest::Response,
) -> capability::Response<serde_json::Value> {
    let retryable = response.status().is_server_error();
    let payload = response.json::<serde_json::Value>().await.ok();
    let code = payload
        .as_ref()
        .and_then(|value| value.pointer("/error/code"))
        .and_then(serde_json::Value::as_str)
        .map(capability::ErrorCode::parse)
        .unwrap_or(capability::ErrorCode::Internal);
    capability_error_response(code, "Attachment operation failed", retryable)
}

pub(in crate::computer) fn local_attachment_error(
    error: ApplicationError,
) -> capability::Response<serde_json::Value> {
    let code = match error {
        ApplicationError::NotFound => capability::ErrorCode::NotFound,
        ApplicationError::Conflict | ApplicationError::Core(_) => capability::ErrorCode::Conflict,
        _ => capability::ErrorCode::Internal,
    };
    capability_error_response(code, "Attachment file operation failed", false)
}

pub(in crate::computer) fn capability_error_response(
    code: capability::ErrorCode,
    message: &str,
    retryable: bool,
) -> capability::Response<serde_json::Value> {
    capability::Response::failure(capability::Error {
        code,
        message: message.into(),
        retryable,
        details: Default::default(),
    })
}

pub(in crate::computer) fn capability_failure() -> capability::Response<serde_json::Value> {
    capability::Response::failure(capability::Error {
        code: capability::ErrorCode::Unavailable,
        message: "Server Agent capability transport is unavailable".into(),
        retryable: true,
        details: Default::default(),
    })
}
