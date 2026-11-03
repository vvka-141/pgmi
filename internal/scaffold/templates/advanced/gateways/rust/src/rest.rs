// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own gateway that fits this project. The rules it keeps are in
// ../../PROTOCOL.md ("One request, one transaction", "Identity").
//
// REST under /api: /api/<path> runs api.rest_invoke for /<path> in one
// transaction per request, opened with the policy api.rest_route_policy
// resolved first. The login session comes from the gateway's cookie only.

use std::collections::BTreeMap;
use std::sync::Arc;

use axum::body::{Body, Bytes};
use axum::extract::State;
use axum::http::header::CONTENT_LENGTH;
use axum::http::{HeaderMap, HeaderName, HeaderValue, Method, StatusCode, Uri};
use axum::response::Response;

use crate::channel::{Protocol, RequestContext};
use crate::error::{GatewayError, Problem};
use crate::interceptors::{self, Body as MessageBody};
use crate::login::SESSION_COOKIE;
use crate::{Gateway, REQUEST_DEADLINE};

// Session and connection handles the gateway did not issue, the cookie that
// carries the one it did, the CSRF token, and hop-by-hop headers.
const NOT_FORWARDED: &[&str] = &[
    "x-login-session", "x-connection", "cookie", "x-csrf-token",
    "connection", "keep-alive", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade",
];
const NOT_COPIED: &[&str] = &["content-length", "transfer-encoding", "connection"];
/// A repeat of one of these is refused, not guessed at.
pub const SINGLE_VALUED: &[&str] = &[
    "authorization", "content-length", "content-type", "host", "origin",
    "x-csrf-token", "x-organization-session", "x-pgmi-transaction-isolation",
];

/// The headers the database sees: lower-cased names (HeaderMap already folds
/// case), each once, a repeat joined with ", ". The login session comes from
/// the gateway's cookie only, whatever the client or an interceptor sent.
pub fn forwarded_headers<'a>(headers: impl IntoIterator<Item = (&'a str, Vec<&'a str>)>, login_session: Option<&str>)
    -> Result<BTreeMap<String, String>, Problem> {
    let mut out = BTreeMap::new();
    for (name, values) in headers {
        if values.len() > 1 && SINGLE_VALUED.contains(&name) {
            return Err(Problem::bad_request("pgmi.header_collision").detail(format!("header {name} is sent more than once")));
        }
        if !NOT_FORWARDED.contains(&name) {
            out.insert(name.to_owned(), values.join(", "));
        }
    }
    if let Some(session) = login_session {
        out.insert("x-login-session".to_owned(), session.to_owned());
    }
    Ok(out)
}

fn header_values(headers: &HeaderMap) -> impl Iterator<Item = (&str, Vec<&str>)> {
    headers
        .keys()
        .map(|name| (name.as_str(), headers.get_all(name).iter().map(|v| v.to_str().unwrap_or_default()).collect()))
}

/// X-PGMI-Transaction-Isolation: case and separators free, mirroring
/// internal.normalize_transaction_isolation. None when unknown. PostgreSQL
/// folds read uncommitted onto read committed; so does this.
pub fn normalize_isolation(value: &str) -> Option<&'static str> {
    let canonical = value
        .trim()
        .to_lowercase()
        .split(|c: char| c.is_whitespace() || c == '_' || c == '-')
        .filter(|part| !part.is_empty())
        .collect::<Vec<_>>()
        .join(" ");
    match canonical.as_str() {
        "read committed" | "read uncommitted" => Some("read committed"),
        "repeatable read" => Some("repeatable read"),
        "serializable" => Some("serializable"),
        _ => None,
    }
}

/// The isolation a client asked for: escalation, never obligation. The
/// database opens max(route floor, this).
pub fn requested_isolation(value: Option<&str>) -> Result<Option<&'static str>, Problem> {
    value
        .map(|v| normalize_isolation(v).ok_or_else(|| Problem::bad_request("pgmi.transaction_isolation_unsupported")))
        .transpose()
}

pub async fn handle(State(gateway): State<Arc<Gateway>>, method: Method, uri: Uri, headers: HeaderMap, body: Bytes)
    -> Result<Response, GatewayError> {
    let login_session = gateway.login.as_ref().and_then(|_| crate::login::cookie(&headers, SESSION_COOKIE));
    if let Some(login) = &gateway.login {
        login.check_csrf(&method, &headers, login_session)?;
    }
    let mut forwarded = forwarded_headers(header_values(&headers), login_session)?;
    let requested = requested_isolation(forwarded.get("x-pgmi-transaction-isolation").map(String::as_str))?;

    // The path is the database's: the raw request target, never cleaned or
    // percent-decoded (a Path extractor decodes), so `/api//x` and
    // `/api/%2e%2e/x` reach api.rest_route_policy as sent.
    let path = uri.path().strip_prefix("/api").unwrap_or_default();
    let mut url = if path.is_empty() { "/".to_owned() } else { path.to_owned() };
    if let Some(query) = uri.query() {
        url = format!("{url}?{query}");
    }
    let mut body = body;
    let mut ctx = RequestContext::new(Protocol::Rest, method.as_str(), url, tokio::time::Instant::now() + REQUEST_DEADLINE);
    let channel = &gateway.channel;

    let mut policy = channel.rest_policy(&ctx, requested).await?;
    ctx = ctx.with_policy(&policy);
    let selected = gateway.pipeline.requests_for(&ctx);
    if !selected.is_empty() {
        // The policy call returned its connection: none is held while an
        // interceptor waits on another service.
        let mut request = interceptors::Request {
            method: ctx.method.clone(),
            url: ctx.url.clone(),
            headers: forwarded,
            body: MessageBody::bytes(body),
        };
        gateway.pipeline.run_requests(&selected, &mut request, &ctx).await.map_err(|_| {
            GatewayError::InterceptorFailed { request_id: ctx.request_id.clone(), applied: false }
        })?;
        // Identity is the gateway's: whatever an interceptor did to the
        // headers, the session is the cookie's again.
        forwarded = forwarded_headers(request.headers.iter().map(|(k, v)| (k.as_str(), vec![v.as_str()])), login_session)?;
        body = request.body.into_bytes();
        // Another route is another handler: resolve its policy again.
        if (request.method.as_str(), request.url.as_str()) != (ctx.method.as_str(), ctx.url.as_str()) {
            ctx.method = request.method;
            ctx.url = request.url;
            policy = channel.rest_policy(&ctx, requested).await?;
            ctx = ctx.with_policy(&policy);
        }
    }

    let content = (!body.is_empty()).then_some(body.as_ref());
    let mut result = channel.rest_invoke(&ctx, &policy, &forwarded, content).await?;
    let selected = gateway.pipeline.responses_for(&ctx);
    if !selected.is_empty() {
        let mut response = interceptors::Response { status: result.status, headers: result.headers, body: MessageBody::bytes(result.content) };
        gateway.pipeline.run_responses(&selected, &mut response, &ctx).await.map_err(|_| {
            GatewayError::InterceptorFailed { request_id: ctx.request_id.clone(), applied: true }
        })?;
        result.status = response.status;
        result.headers = response.headers;
        result.content = response.body.into_bytes();
    }
    render(&method, result)
}

/// Every header the database returned except hop-by-hop ones. Content-Length
/// is the content's, except for HEAD: there it is the database's, the length
/// GET would have sent (RFC 9110 §8.6), and no content follows.
fn render(method: &Method, result: crate::channel::RestResult) -> Result<Response, GatewayError> {
    let head = method == Method::HEAD;
    let mut response = Response::new(if head { Body::empty() } else { Body::from(result.content) });
    *response.status_mut() = StatusCode::from_u16(result.status).map_err(GatewayError::internal)?;
    for (name, value) in &result.headers {
        if head && name == "content-length" {
            if let Ok(value) = HeaderValue::from_str(value) {
                response.headers_mut().insert(CONTENT_LENGTH, value);
            }
            continue;
        }
        if NOT_COPIED.contains(&name.as_str()) {
            continue;
        }
        if let (Ok(name), Ok(value)) = (HeaderName::from_bytes(name.as_bytes()), HeaderValue::from_str(value)) {
            response.headers_mut().append(name, value);
        }
    }
    Ok(response)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn forwarded(headers: &HeaderMap, session: Option<&str>) -> Result<BTreeMap<String, String>, Problem> {
        forwarded_headers(header_values(headers), session)
    }

    #[test]
    fn the_login_session_comes_from_the_cookie_only() {
        let mut headers = HeaderMap::new();
        headers.insert("x-login-session", HeaderValue::from_static("ls_forged"));
        headers.insert("x-connection", HeaderValue::from_static("cn_x"));
        headers.insert("accept", HeaderValue::from_static("application/json"));
        let out = forwarded(&headers, Some("ls_real")).unwrap();
        assert_eq!(out["x-login-session"], "ls_real");
        assert_eq!(out["accept"], "application/json");
        assert!(!out.contains_key("x-connection"));
    }

    #[test]
    fn a_repeated_single_valued_header_is_refused() {
        for name in SINGLE_VALUED {
            let mut headers = HeaderMap::new();
            headers.append(*name, HeaderValue::from_static("a"));
            headers.append(HeaderName::from_bytes(name.to_uppercase().as_bytes()).unwrap(), HeaderValue::from_static("b"));
            assert_eq!(forwarded(&headers, None).unwrap_err().code, "pgmi.header_collision", "{name}");
        }
    }

    #[test]
    fn a_repeated_list_header_is_joined() {
        let mut headers = HeaderMap::new();
        headers.append("accept", HeaderValue::from_static("application/json"));
        headers.append("Accept", HeaderValue::from_static("*/*"));
        assert_eq!(forwarded(&headers, None).unwrap()["accept"], "application/json, */*");
    }

    #[test]
    fn isolation_names_are_case_and_separator_free() {
        assert_eq!(normalize_isolation("Repeatable_Read"), Some("repeatable read"));
        assert_eq!(normalize_isolation("read-uncommitted"), Some("read committed"));
        assert_eq!(normalize_isolation("snapshot"), None);
    }
}
