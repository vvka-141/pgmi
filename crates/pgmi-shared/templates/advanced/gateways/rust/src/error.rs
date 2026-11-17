// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own gateway that fits this project. The rules it keeps are in
// ../../PROTOCOL.md ("Status codes").
//
// Every answer the gateway gives on its own, in one place. A handler returns
// Result<Response, GatewayError>; `?` turns any failure into the right
// problem. REST answers RFC 9457 problems (IntoResponse); /mcp answers the
// same failures as JSON-RPC errors (GatewayError::into_rpc).

use std::borrow::Cow;

use axum::Json;
use axum::http::header::{CACHE_CONTROL, CONTENT_LENGTH, CONTENT_TYPE, RETRY_AFTER, WWW_AUTHENTICATE};
use axum::http::{HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};
use serde_json::{Value, json};

use crate::channel::DbError;

/// An RFC 9457 problem with a code. Branch on the code, never on the title.
#[derive(Debug)]
pub struct Problem {
    pub status: StatusCode,
    pub title: &'static str,
    pub code: Cow<'static, str>,
    pub detail: Option<String>,
}

impl Problem {
    pub fn new(status: StatusCode, title: &'static str, code: impl Into<Cow<'static, str>>) -> Self {
        Problem { status, title, code: code.into(), detail: None }
    }

    pub fn detail(mut self, detail: impl Into<String>) -> Self {
        self.detail = Some(detail.into());
        self
    }

    pub fn bad_request(code: &'static str) -> Self {
        Problem::new(StatusCode::BAD_REQUEST, "Bad Request", code)
    }

    pub fn forbidden(code: &'static str) -> Self {
        Problem::new(StatusCode::FORBIDDEN, "Forbidden", code)
    }

    pub fn not_found() -> Self {
        Problem::new(StatusCode::NOT_FOUND, "Not Found", "pgmi.not_found")
    }
}

#[derive(Debug)]
pub enum GatewayError {
    /// Answered as is: 400, 403, 404, 413 and the sign-in refusals.
    Problem(Problem),
    /// No pooled connection within the acquire timeout.
    Unavailable,
    /// The transaction lost a serialization race on every attempt; nothing
    /// was applied and the same request may succeed when repeated.
    RetriesExhausted { sqlstate: String, attempts: u32 },
    /// `applied`: it failed after the commit. The client is told, so it does
    /// not repeat a write that already happened.
    InterceptorFailed { request_id: String, applied: bool },
    /// The request ran past its deadline and the database cancelled it.
    /// Whatever it did was rolled back.
    DeadlineExceeded { request_id: String },
    RateLimited { retry_after: u64 },
    /// 401 with the challenge that says where to get a token (RFC 9728).
    Unauthorized { challenge: String },
    IdentityProviderUnavailable,
    /// Logged here; the client never sees its text.
    Internal(String),
}

impl From<Problem> for GatewayError {
    fn from(problem: Problem) -> Self {
        GatewayError::Problem(problem)
    }
}

impl From<DbError> for GatewayError {
    fn from(error: DbError) -> Self {
        match error {
            DbError::NoConnection => GatewayError::Unavailable,
            DbError::DeadlineExceeded { request_id } => GatewayError::DeadlineExceeded { request_id },
            DbError::RetriesExhausted { sqlstate, attempts } => {
                GatewayError::RetriesExhausted { sqlstate: sqlstate.code().to_owned(), attempts }
            }
            other => GatewayError::Internal(other.to_string()),
        }
    }
}

impl From<deadpool_postgres::PoolError> for GatewayError {
    fn from(error: deadpool_postgres::PoolError) -> Self {
        DbError::from(error).into()
    }
}

impl GatewayError {
    pub fn internal(error: impl std::fmt::Display) -> Self {
        GatewayError::Internal(error.to_string())
    }

    fn log(&self) {
        match self {
            GatewayError::Internal(error) => tracing::error!(%error, "internal error"),
            GatewayError::RetriesExhausted { sqlstate, attempts } => {
                tracing::warn!(%sqlstate, attempts, "serialization failure, retries exhausted")
            }
            GatewayError::Unavailable => tracing::warn!("no pooled connection within the acquire timeout"),
            GatewayError::DeadlineExceeded { request_id } => tracing::warn!(%request_id, "deadline exceeded"),
            _ => {}
        }
    }

    /// The same failure on /mcp: a JSON-RPC error carrying the request's id.
    pub fn into_rpc(self, id: Value) -> Response {
        self.log();
        let (status, code, message, data, retry_after) = match self {
            GatewayError::RetriesExhausted { sqlstate, attempts } => (
                StatusCode::OK, -31000,
                "The call conflicted with a concurrent change and was rolled back; repeat it unchanged",
                json!({"code": "pgmi.transaction_retryable", "sqlstate": sqlstate, "attempts": attempts}), Some("1"),
            ),
            GatewayError::Unavailable => (
                StatusCode::SERVICE_UNAVAILABLE, -31000, "The database is unavailable; repeat the call later",
                json!({"code": "pgmi.unavailable"}), Some("1"),
            ),
            GatewayError::IdentityProviderUnavailable => (
                StatusCode::SERVICE_UNAVAILABLE, -31000, "The identity provider is unavailable; repeat the call later",
                json!({"code": "pgmi.identity_provider_unavailable"}), Some("5"),
            ),
            GatewayError::InterceptorFailed { request_id, .. } => (
                StatusCode::BAD_GATEWAY, -32603, "The gateway could not complete the call",
                json!({"code": "pgmi.interceptor_failed", "requestId": request_id}), None,
            ),
            GatewayError::DeadlineExceeded { request_id } => (
                StatusCode::GATEWAY_TIMEOUT, -32603, "The call ran past its deadline and was cancelled",
                json!({"code": "pgmi.deadline_exceeded", "requestId": request_id}), None,
            ),
            GatewayError::Problem(problem) => {
                let data = json!({"code": problem.code});
                return rpc_error(problem.status, id, -32600, problem.detail.as_deref().unwrap_or(problem.title), Some(data));
            }
            // 401 and 429 are HTTP answers before any JSON-RPC is read.
            other @ (GatewayError::Unauthorized { .. } | GatewayError::RateLimited { .. }) => return other.into_response(),
            GatewayError::Internal(_) => {
                return rpc_error(StatusCode::INTERNAL_SERVER_ERROR, id, -32603, "Internal error", None);
            }
        };
        let mut response = rpc_error(status, id, code, message, Some(data));
        if let Some(seconds) = retry_after {
            response.headers_mut().insert(RETRY_AFTER, HeaderValue::from_static(seconds));
        }
        response
    }
}

pub fn rpc_error(status: StatusCode, id: Value, code: i64, message: &str, data: Option<Value>) -> Response {
    let mut error = json!({"code": code, "message": message});
    if let Some(data) = data {
        error["data"] = data;
    }
    (status, Json(json!({"jsonrpc": "2.0", "id": id, "error": error}))).into_response()
}

impl IntoResponse for GatewayError {
    fn into_response(self) -> Response {
        self.log();
        let (problem, retry_after, request_id) = match self {
            GatewayError::Problem(problem) => (problem, None, None),
            GatewayError::Unavailable => (
                Problem::new(StatusCode::SERVICE_UNAVAILABLE, "Service Unavailable", "pgmi.unavailable")
                    .detail("no database connection is free; repeat the request"),
                Some("1".to_owned()), None,
            ),
            // Only /mcp checks tokens, and it answers JSON-RPC even before
            // the body is read.
            GatewayError::IdentityProviderUnavailable => {
                return GatewayError::IdentityProviderUnavailable.into_rpc(Value::Null);
            }
            GatewayError::RetriesExhausted { .. } => (
                Problem::new(StatusCode::CONFLICT, "Conflict", "pgmi.transaction_retryable")
                    .detail("the request conflicted with a concurrent change; repeat it"),
                Some("1".to_owned()), None,
            ),
            GatewayError::InterceptorFailed { request_id, applied } => (
                Problem::new(StatusCode::BAD_GATEWAY, "Bad Gateway", "pgmi.interceptor_failed").detail(if applied {
                    "the request was applied; its answer could not be completed"
                } else {
                    "the request was not applied"
                }),
                None, Some(request_id),
            ),
            GatewayError::DeadlineExceeded { request_id } => (
                Problem::new(StatusCode::GATEWAY_TIMEOUT, "Gateway Timeout", "pgmi.deadline_exceeded")
                    .detail("the request ran past its deadline and was cancelled; nothing was applied"),
                None, Some(request_id),
            ),
            GatewayError::RateLimited { retry_after } => {
                (Problem::new(StatusCode::TOO_MANY_REQUESTS, "Too Many Requests", "pgmi.rate_limited"),
                 Some(retry_after.to_string()), None)
            }
            GatewayError::Unauthorized { challenge } => {
                let mut response = StatusCode::UNAUTHORIZED.into_response();
                let headers = response.headers_mut();
                headers.insert(WWW_AUTHENTICATE, HeaderValue::from_str(&challenge).expect("built from configuration"));
                headers.insert(CONTENT_LENGTH, HeaderValue::from_static("0"));
                return response;
            }
            GatewayError::Internal(_) => (
                Problem::new(StatusCode::INTERNAL_SERVER_ERROR, "Internal Server Error", "pgmi.internal_error"),
                None, None,
            ),
        };

        let mut body = json!({"type": "about:blank", "title": problem.title, "status": problem.status.as_u16(), "code": problem.code});
        if let Some(detail) = problem.detail {
            body["detail"] = detail.into();
        }
        let mut response = (problem.status, body.to_string()).into_response();
        let headers = response.headers_mut();
        headers.insert(CONTENT_TYPE, HeaderValue::from_static("application/problem+json"));
        headers.insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
        if let Some(seconds) = retry_after.and_then(|s| HeaderValue::from_str(&s).ok()) {
            headers.insert(RETRY_AFTER, seconds);
        }
        if let Some(id) = request_id.and_then(|id| HeaderValue::from_str(&id).ok()) {
            headers.insert("x-request-id", id);
        }
        response
    }
}

#[cfg(test)]
mod tests {
    use axum::body::to_bytes;

    use super::*;

    async fn json_body(response: Response) -> Value {
        serde_json::from_slice(&to_bytes(response.into_body(), usize::MAX).await.unwrap()).unwrap()
    }

    fn past_deadline() -> GatewayError {
        DbError::DeadlineExceeded { request_id: "r1".into() }.into()
    }

    #[tokio::test]
    async fn a_deadline_exceeded_is_a_504_problem() {
        let response = past_deadline().into_response();
        assert_eq!(response.status(), StatusCode::GATEWAY_TIMEOUT);
        assert_eq!(response.headers()[CONTENT_TYPE], "application/problem+json");
        assert_eq!(response.headers()["x-request-id"], "r1");
        assert_eq!(json_body(response).await["code"], "pgmi.deadline_exceeded");
    }

    #[tokio::test]
    async fn a_deadline_exceeded_on_mcp_is_a_504_internal_error() {
        let response = past_deadline().into_rpc(json!(7));
        assert_eq!(response.status(), StatusCode::GATEWAY_TIMEOUT);
        let body = json_body(response).await;
        assert_eq!(body["id"], 7);
        assert_eq!(body["error"]["code"], -32603);
        assert_eq!(body["error"]["data"], json!({"code": "pgmi.deadline_exceeded", "requestId": "r1"}));
    }
}
