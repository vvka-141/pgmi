// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own gateway that fits this project. The rules it keeps are in
// ../../PROTOCOL.md ("MCP", "MCP status codes").
//
// POST /mcp, Streamable HTTP with JSON answers only (no SSE stream, no
// session), both protocol eras. The gateway knows no protocol version: it
// forwards the body and the MCP-Protocol-Version, Mcp-Method and Mcp-Name
// headers, as sent, to api.mcp_request_policy, and the database decides. A
// refusal is the whole answer (400) and nothing is dispatched, so nothing is
// logged. The one piece of protocol knowledge kept here is the status of a
// dispatched answer (`envelope_status`). Mcp-Session-Id and Last-Event-ID are
// ignored: there is no session.

use std::sync::Arc;

use axum::Json;
use axum::body::Bytes;
use axum::extract::State;
use axum::http::header::{ACCEPT, CONTENT_TYPE};
use axum::http::{HeaderMap, HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};
use serde_json::{Value, json};

use crate::channel::{McpHeaders, Protocol, RequestContext};
use crate::error::{GatewayError, Problem, rpc_error};
use crate::interceptors::{self, Body};
use crate::resource_server::McpCaller;
use crate::rest::requested_isolation;
use crate::{Gateway, REQUEST_DEADLINE};

/// ADAPT: forward them as your server hands them over.
const MCP_HEADERS: [&str; 3] = ["mcp-protocol-version", "mcp-method", "mcp-name"];

/// The status of a dispatched answer. In the 2026-07-28 era (`modern`, from
/// the policy) -32601 is 404 and -32022 is 400; every other answer is 200, a
/// tool's -32602 included: the client reads JSON-RPC errors from the body.
/// Every legacy answer is 200.
pub fn envelope_status(envelope: &Value, modern: bool) -> StatusCode {
    match envelope.pointer("/error/code").and_then(Value::as_i64) {
        Some(-32601) if modern => StatusCode::NOT_FOUND,
        Some(-32022) if modern => StatusCode::BAD_REQUEST,
        _ => StatusCode::OK,
    }
}

/// The database knows no live connection by the handle sent: answered 401,
/// as a token the gateway refused.
pub fn handle_unknown(envelope: &Value) -> bool {
    envelope.pointer("/error/code").and_then(Value::as_i64) == Some(-31001)
        && envelope.pointer("/error/data/code").and_then(Value::as_str) == Some("pgmi.connection_unknown")
}

/// A media range with q=0 is a refusal (RFC 9110 §12.5.1), not an acceptance.
pub fn accepts_json(accept: &str) -> bool {
    accept.split(',').any(|entry| {
        let mut parts = entry.split(';');
        let range = parts.next().unwrap_or_default().trim().to_ascii_lowercase();
        let q = parts
            .filter_map(|p| p.trim().strip_prefix("q="))
            .next()
            .map_or(1.0, |q| q.trim().parse::<f64>().unwrap_or(1.0));
        matches!(range.as_str(), "application/json" | "application/*" | "*/*") && q > 0.0
    })
}

/// What api.mcp_request_policy routes on: an interceptor that changes it
/// sends the request to another handler, so the policy is resolved again.
fn route(request: &Value) -> [Option<Value>; 3] {
    [request.get("method"), request.pointer("/params/name"), request.pointer("/params/uri")].map(|v| v.cloned())
}

/// Any other method: there is no SSE stream to open (GET) and no session to
/// end (DELETE). A problem, like every refusal of the gateway's own; axum's
/// default 405 has no body.
pub async fn method_not_allowed() -> Response {
    let mut response = GatewayError::from(Problem::new(StatusCode::METHOD_NOT_ALLOWED, "Method Not Allowed",
                                                       "pgmi.method_not_allowed")).into_response();
    response.headers_mut().insert(axum::http::header::ALLOW, HeaderValue::from_static("POST"));
    response
}

fn request_id(request: &Value) -> Value {
    match request.get("id") {
        Some(id @ (Value::String(_) | Value::Number(_))) => id.clone(),
        _ => Value::Null,
    }
}

pub async fn handle(State(gateway): State<Arc<Gateway>>, McpCaller(caller): McpCaller, headers: HeaderMap, body: Bytes)
    -> Response {
    let version = headers.get("mcp-protocol-version").cloned();
    let mut response = answer(&gateway, caller, &headers, body).await;
    // Echoed on every answer of this endpoint.
    if let Some(version) = version {
        response.headers_mut().insert("mcp-protocol-version", version);
    }
    response
}

async fn answer(gateway: &Gateway, caller: Option<Value>, headers: &HeaderMap, body: Bytes) -> Response {
    let header = |name| headers.get(name).and_then(|v: &HeaderValue| v.to_str().ok());
    let content_type = header(CONTENT_TYPE.as_str()).unwrap_or_default();
    if !content_type.split(';').next().unwrap_or_default().trim().eq_ignore_ascii_case("application/json") {
        return GatewayError::from(Problem::new(StatusCode::UNSUPPORTED_MEDIA_TYPE, "Unsupported Media Type",
                                               "pgmi.unsupported_media_type")
            .detail("MCP requires Content-Type: application/json")).into_response();
    }
    if header(ACCEPT.as_str()).is_some_and(|accept| !accepts_json(accept)) {
        return GatewayError::from(Problem::new(StatusCode::NOT_ACCEPTABLE, "Not Acceptable", "pgmi.not_acceptable")
            .detail("this endpoint answers application/json")).into_response();
    }
    let Ok(request) = serde_json::from_slice::<Value>(&body) else {
        return rpc_error(StatusCode::BAD_REQUEST, Value::Null, -32700, "Parse error", None);
    };
    let id = request_id(&request);
    let requested = match requested_isolation(header("x-pgmi-transaction-isolation")) {
        Ok(requested) => requested,
        Err(problem) => {
            return rpc_error(StatusCode::BAD_REQUEST, id, -32600, "Unsupported X-PGMI-Transaction-Isolation",
                             Some(json!({"code": problem.code})));
        }
    };
    let mcp_headers: McpHeaders = MCP_HEADERS.map(|name| header(name).map(str::to_owned));
    dispatch(gateway, request, requested, &mcp_headers, caller.as_ref()).await.unwrap_or_else(|e| e.into_rpc(id))
}

async fn dispatch(gateway: &Gateway, mut request: Value, requested: Option<&str>, mcp_headers: &McpHeaders,
                  caller: Option<&Value>) -> Result<Response, GatewayError> {
    let method = request.get("method").and_then(Value::as_str).unwrap_or_default().to_owned();
    let mut ctx = RequestContext::new(Protocol::Mcp, method, "/mcp", tokio::time::Instant::now() + REQUEST_DEADLINE);
    let channel = &gateway.channel;

    // One call before the transaction: the protocol checks (the database owns
    // them), the transaction policy and the handler's metadata.
    let mut policy = channel.mcp_policy(&ctx, &request, requested, mcp_headers).await?;
    if policy.refusal.is_none() {
        ctx = ctx.with_policy(&policy);
        let selected = gateway.pipeline.requests_for(&ctx);
        if !selected.is_empty() {
            // The request is already parsed: interceptors share it. On /mcp
            // only the body reaches the database; the caller's identity is
            // not in it, so no interceptor can change who is calling.
            let before = route(&request);
            let mut message = interceptors::Request {
                method: "POST".into(),
                url: "/mcp".into(),
                headers: Default::default(),
                body: Body::json_value(request),
            };
            gateway.pipeline.run_requests(&selected, &mut message, &ctx).await.map_err(|_| {
                GatewayError::InterceptorFailed { request_id: ctx.request_id.clone(), applied: false }
            })?;
            request = message.body.into_json().unwrap_or(Value::Null);
            if route(&request) != before {
                policy = channel.mcp_policy(&ctx, &request, requested, mcp_headers).await?;
                ctx = ctx.with_policy(&policy);
            }
        }
    }
    if let Some(refusal) = policy.refusal.take() {
        return Ok((StatusCode::BAD_REQUEST, Json(refusal)).into_response());
    }

    // None: a notification. Accepted notifications are 202 with no body.
    let Some(envelope) = channel.mcp_request(&ctx, &policy, &request, caller).await? else {
        return Ok(StatusCode::ACCEPTED.into_response());
    };
    if handle_unknown(&envelope) {
        let challenge = gateway.resource.as_ref()
            .map_or_else(|| r#"Bearer error="invalid_token""#.to_owned(), |r| r.challenge(Some("invalid_token")));
        return Err(GatewayError::Unauthorized { challenge });
    }
    let status = envelope_status(&envelope, policy.modern);
    let selected = gateway.pipeline.responses_for(&ctx);
    if selected.is_empty() {
        return Ok((status, Json(envelope)).into_response());
    }
    let mut answer = interceptors::Response { status: status.as_u16(), headers: Default::default(), body: Body::json_value(envelope) };
    gateway.pipeline.run_responses(&selected, &mut answer, &ctx).await.map_err(|_| {
        GatewayError::InterceptorFailed { request_id: ctx.request_id.clone(), applied: true }
    })?;
    let mut response = (StatusCode::from_u16(answer.status).map_err(GatewayError::internal)?,
                        Json(answer.body.into_json().unwrap_or(Value::Null))).into_response();
    for (name, value) in answer.headers {
        if let (Ok(name), Ok(value)) = (axum::http::HeaderName::from_bytes(name.as_bytes()), HeaderValue::from_str(&value)) {
            response.headers_mut().insert(name, value);
        }
    }
    Ok(response)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_status_of_a_dispatched_answer() {
        let error = |code: i64| json!({"jsonrpc": "2.0", "id": 1, "error": {"code": code, "message": "m"}});
        assert_eq!(envelope_status(&error(-32601), true), StatusCode::NOT_FOUND);
        assert_eq!(envelope_status(&error(-32022), true), StatusCode::BAD_REQUEST);
        assert_eq!(envelope_status(&error(-32602), true), StatusCode::OK);
        assert_eq!(envelope_status(&error(-32601), false), StatusCode::OK);
        assert_eq!(envelope_status(&json!({"result": {}}), true), StatusCode::OK);
    }

    #[test]
    fn an_unknown_handle_is_unauthorized() {
        let refusal = |code: &str| json!({"error": {"code": -31001, "message": "m", "data": {"code": code}}});
        assert!(handle_unknown(&refusal("pgmi.connection_unknown")));
        assert!(!handle_unknown(&refusal("membership.connection.paused")));
        assert!(!handle_unknown(&json!({"result": {}})));
    }

    #[test]
    fn accept_with_q_zero_is_a_refusal() {
        assert!(accepts_json("application/json"));
        assert!(accepts_json("text/event-stream, */*;q=0.1"));
        assert!(!accepts_json("application/json;q=0"));
        assert!(!accepts_json("text/html"));
    }
}
