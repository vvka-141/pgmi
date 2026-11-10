// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own tests for the gateway you write.
//
// The router in-process (tower's oneshot) and on a real socket. The
// end-to-end harness in the pgmi repository covers the wire rules from
// outside; these cover what it cannot reach. Needs an env=dev deploy and
// DATABASE_URL as a login in the api role; without it the database test
// returns early and says so.

use std::collections::HashMap;
use std::io::{Read, Write};
use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use axum::Router;
use axum::body::Body;
use axum::http::{HeaderMap, Request, StatusCode};
use http_body_util::BodyExt;
use pgmi_gateway::db::APPLICATION_NAME;
use pgmi_gateway::{Config, Gateway, router};
use serde_json::{Value, json};
use tower::ServiceExt;

struct Reply {
    status: StatusCode,
    headers: HeaderMap,
    content_type: String,
    set_cookies: Vec<String>,
    body: Value,
}

struct Client {
    app: Router,
    session: String,
    csrf: String,
}

impl Client {
    async fn call(&self, method: &str, path: &str, body: Option<Value>, headers: &[(&str, &str)], signed_in: bool) -> Reply {
        let mut request = Request::builder().method(method).uri(path).header("accept", "application/json");
        if body.is_some() {
            request = request.header("content-type", "application/json");
        }
        if signed_in {
            request = request.header("cookie", format!("__Host-session={}", self.session));
        }
        for (name, value) in headers {
            request = request.header(*name, *value);
        }
        let request = request.body(body.map_or_else(Body::empty, |b| Body::from(b.to_string()))).unwrap();
        let response = self.app.clone().oneshot(request).await.unwrap();
        let status = response.status();
        let content_type = response.headers().get("content-type").map(|v| v.to_str().unwrap().to_owned()).unwrap_or_default();
        let set_cookies = response.headers().get_all("set-cookie").iter().map(|v| v.to_str().unwrap().to_owned()).collect();
        let headers = response.headers().clone();
        let bytes = response.into_body().collect().await.unwrap().to_bytes();
        let body = if bytes.is_empty() { Value::Null } else { serde_json::from_slice(&bytes).unwrap() };
        Reply { status, headers, content_type, set_cookies, body }
    }

    async fn post(&self, path: &str, body: Value, headers: &[(&str, &str)]) -> Reply {
        let mut all = vec![("x-csrf-token", self.csrf.as_str())];
        all.extend_from_slice(headers);
        self.call("POST", path, Some(body), &all, true).await
    }

    /// An MCP 2026-07-28 call, as the agent of an approved connection.
    async fn mcp(&self, method: &str, params: Value, connection: &str) -> Reply {
        let mut headers = vec![("mcp-protocol-version", "2026-07-28"), ("mcp-method", method), ("x-connection", connection)];
        let name = params.get("name").and_then(Value::as_str).map(str::to_owned);
        if let Some(name) = &name {
            headers.push(("mcp-name", name));
        }
        let mut params = params.clone();
        params["_meta"] = json!({"io.modelcontextprotocol/protocolVersion": "2026-07-28",
                                 "io.modelcontextprotocol/clientCapabilities": {}});
        self.call("POST", "/mcp", Some(json!({"jsonrpc": "2.0", "id": 1, "method": method, "params": params})), &headers, false)
            .await
    }
}

fn assert_problem(reply: &Reply, status: u16, code: &str) {
    assert_eq!(reply.status.as_u16(), status, "{}", reply.body);
    assert!(reply.content_type.starts_with("application/problem+json"));
    assert_eq!(reply.body["code"], code);
}

fn gateway(database_url: &str) -> Arc<Gateway> {
    let mut config = Config::new(database_url);
    config.dev_login = true;
    Gateway::build(config).unwrap()
}

async fn serve(app: Router) -> SocketAddr {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    tokio::spawn(pgmi_gateway::serve(listener, app));
    address
}

/// The raw response to a raw request, so what hyper puts on the wire is seen.
async fn exchange(address: SocketAddr, request: String) -> (String, Vec<u8>) {
    let raw = tokio::task::spawn_blocking(move || {
        let mut stream = std::net::TcpStream::connect(address).unwrap();
        stream.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
        stream.write_all(request.as_bytes()).unwrap();
        let mut raw = Vec::new();
        let _ = stream.read_to_end(&mut raw);
        raw
    })
    .await
    .unwrap();
    let split = raw.windows(4).position(|w| w == b"\r\n\r\n").expect("a complete head");
    (String::from_utf8_lossy(&raw[..split]).to_lowercase(), raw[split + 4..].to_vec())
}

fn content_length(head: &str) -> Option<&str> {
    head.lines().find_map(|line| line.strip_prefix("content-length: "))
}

#[tokio::test]
async fn refusals_before_the_database() {
    let c = Client { app: router(gateway("postgresql://nobody@127.0.0.1:1/none")), session: String::new(), csrf: String::new() };

    let big = vec![b'a'; 1024 * 1024 + 1];
    let request = Request::post("/api/echo").header("content-type", "application/json").body(Body::from(big)).unwrap();
    let response = c.app.clone().oneshot(request).await.unwrap();
    assert_eq!(response.status(), StatusCode::PAYLOAD_TOO_LARGE);
    let body: Value = serde_json::from_slice(&response.into_body().collect().await.unwrap().to_bytes()).unwrap();
    assert_eq!(body["code"], "pgmi.body_too_large");

    assert_problem(&c.call("GET", "/api/hello", None, &[("x-organization-session", "a"), ("X-Organization-Session", "b")], false).await,
                   400, "pgmi.header_collision");
    assert_problem(&c.call("GET", "/api/hello", None, &[("origin", "https://evil.example")], false).await, 403, "pgmi.origin_refused");
    assert_problem(&c.call("GET", "/nowhere", None, &[], false).await, 404, "pgmi.not_found");

    // /mcp is POST only
    for method in ["GET", "DELETE"] {
        let reply = c.call(method, "/mcp", None, &[], false).await;
        assert_problem(&reply, 405, "pgmi.method_not_allowed");
        assert_eq!(reply.headers["allow"], "POST");
    }

    let address = serve(c.app.clone()).await;
    let (head, _) = exchange(address, "POST /api/echo HTTP/1.1\r\nHost: x\r\nContent-Length: -1\r\nConnection: close\r\n\r\n".into()).await;
    assert!(head.starts_with("http/1.1 400"), "{head}");

    let closed_after = tokio::task::spawn_blocking(move || {
        let mut stream = std::net::TcpStream::connect(address).unwrap();
        stream.set_read_timeout(Some(Duration::from_secs(30))).unwrap();
        stream.write_all(b"GET /api/he").unwrap();
        let started = std::time::Instant::now();
        let mut rest = Vec::new();
        let _ = stream.read_to_end(&mut rest);
        started.elapsed()
    })
    .await
    .unwrap();
    assert!(closed_after < pgmi_gateway::HEADER_READ_TIMEOUT + Duration::from_secs(5),
            "a client that stops mid-request must be cut off; the connection stayed open {closed_after:?}");
}

#[tokio::test]
async fn the_per_ip_limit_counts_on_the_socket_peer() {
    let mut config = Config::new("postgresql://nobody@127.0.0.1:1/none");
    config.rate_limit_per_ip = 2;
    let address = serve(router(Gateway::build(config).unwrap())).await;
    let post = "POST /mcp HTTP/1.1\r\nHost: x\r\nContent-Type: text/plain\r\nContent-Length: 0\r\nConnection: close\r\n\r\n";
    for _ in 0..2 {
        assert!(exchange(address, post.into()).await.0.starts_with("http/1.1 415"));
    }
    let (head, _) = exchange(address, post.into()).await;
    assert!(head.starts_with("http/1.1 429") && head.contains("retry-after: "), "{head}");
    for path in ["/auth/login", "/auth/callback?code=x&state=y"] {
        let get = format!("GET {path} HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
        let (head, body) = exchange(address, get).await;
        assert!(head.starts_with("http/1.1 429") && String::from_utf8_lossy(&body).contains("pgmi.rate_limited"), "{path} shares the limit: {head}");
    }
}

#[tokio::test]
async fn against_the_deployed_template() {
    let Ok(database_url) = std::env::var("DATABASE_URL") else {
        eprintln!("skipped: DATABASE_URL is not set");
        return;
    };
    let shared = gateway(&database_url);
    let pool = shared.pool.clone();
    let mut c = Client { app: router(shared), session: String::new(), csrf: String::new() };

    // Development sign-in. In production the gateway runs OIDC and calls
    // api.open_login_session instead (src/login.rs).
    let sign_in = c
        .call("POST", "/auth/dev-login",
              Some(json!({"subject": "client-rust", "email": "client-rust@example.com", "displayName": "Rust client"})),
              &[], false)
        .await;
    assert_eq!(sign_in.status, StatusCode::NO_CONTENT, "{}", sign_in.body);
    let cookies: HashMap<String, String> = sign_in
        .set_cookies
        .iter()
        .filter_map(|c| c.split(';').next()?.split_once('=').map(|(k, v)| (k.to_owned(), v.to_owned())))
        .collect();
    c.session = cookies["__Host-session"].clone();
    c.csrf = cookies["__Host-csrf"].clone();

    // openapi.json lists what the caller may call
    let anonymous = c.call("GET", "/api/openapi.json", None, &[], false).await;
    assert_eq!(anonymous.status, StatusCode::OK);
    assert!(anonymous.body["paths"].get("/hello").is_some());
    assert!(anonymous.body["paths"].get("/me").is_none());
    let signed_in = c.call("GET", "/api/openapi.json", None, &[], true).await;
    assert!(signed_in.body["paths"].get("/me").is_some());

    // a public route
    let hello = c.call("GET", "/api/hello?name=Rust", None, &[], false).await;
    assert_eq!(hello.status, StatusCode::OK);
    assert_eq!(hello.body["message"], "Hello, Rust!");

    // HEAD carries the GET length and no content
    let address = serve(c.app.clone()).await;
    let (get_head, get_body) =
        exchange(address, "GET /api/openapi.json HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n".into()).await;
    let (head_head, head_body) =
        exchange(address, "HEAD /api/openapi.json HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n".into()).await;
    assert!(head_head.starts_with("http/1.1 200"), "{head_head}");
    assert_eq!(content_length(&get_head), Some(get_body.len().to_string().as_str()), "{get_head}");
    assert_eq!(content_length(&head_head), Some(get_body.len().to_string().as_str()), "{head_head}");
    assert!(head_body.is_empty());

    // an authenticated route, and a client-sent session header that is dropped
    assert_eq!(c.call("GET", "/api/me", None, &[], false).await.status, StatusCode::UNAUTHORIZED);
    assert_eq!(c.call("GET", "/api/me", None, &[], true).await.status, StatusCode::OK);
    let session = c.session.clone();
    assert_eq!(c.call("GET", "/api/me", None, &[("x-login-session", &session)], false).await.status,
               StatusCode::UNAUTHORIZED);

    // coded problems
    assert_problem(&c.call("POST", "/api/connections", Some(json!({"clientName": "x"})), &[], true).await, 403, "pgmi.csrf_refused");
    assert_problem(&c.post("/api/connections", json!({"clientName": 5}), &[]).await, 400, "pgmi.invalid_body");
    assert_problem(&c.post("/api/connections",
                           json!({"clientName": "x", "organizationId": "00000000-0000-4000-8000-000000000000", "roleId": "agent.reader"}), &[]).await,
                   422, "membership.connection.not_member");
    let slug = format!("rs-{:08x}", rand::random::<u32>());
    let created = c.post("/api/organizations", json!({"name": "Rust client", "slug": slug}), &[]).await;
    assert_eq!(created.status, StatusCode::CREATED, "{}", created.body);
    assert_problem(&c.post("/api/connections",
                           json!({"clientName": "x", "validDays": 999, "organizationId": created.body["id"], "roleId": "agent.reader"}),
                           &[]).await,
                   400, "pgmi.invalid_body");

    // MCP as the agent of a connection the person approved on the web
    let approved = c.post("/api/connections",
                          json!({"clientName": "Rust test", "validDays": 1, "organizationId": created.body["id"], "roleId": "agent.reader"}),
                          &[]).await;
    assert_eq!(approved.status, StatusCode::CREATED, "{}", approved.body);
    let connection = approved.body["connection"].as_str().unwrap().to_owned();
    let listed = c.mcp("tools/list", json!({}), &connection).await;
    assert_eq!(listed.status, StatusCode::OK, "{}", listed.body);
    assert_eq!(listed.headers["mcp-protocol-version"], "2026-07-28");
    assert!(listed.body["result"]["tools"].as_array().unwrap().iter().any(|t| t["name"] == "echo_note"), "{}", listed.body);
    let called = c.mcp("tools/call", json!({"name": "echo_note", "arguments": {"note": "hi"}}), &connection).await;
    assert_eq!(called.status, StatusCode::OK, "{}", called.body);
    assert_eq!(called.body["result"]["structuredContent"]["note"], "hi", "{}", called.body);
    let refused = c.mcp("tools/call", json!({"name": "echo_note", "arguments": {}}), "cn_unknown").await;
    assert_eq!(refused.status, StatusCode::UNAUTHORIZED, "{}", refused.body);
    assert_eq!(refused.headers["www-authenticate"], r#"Bearer error="invalid_token""#);

    // application_name and a bounded pool
    let count: i64 = pool
        .get()
        .await
        .unwrap()
        .query_one("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1",
                   &[&APPLICATION_NAME])
        .await
        .unwrap()
        .get(0);
    assert!((1..=10).contains(&count), "connections named {APPLICATION_NAME}: {count}");

    // logout: a write like any other, then the handle is dead
    assert_problem(&c.call("POST", "/auth/logout", None, &[], true).await, 403, "pgmi.csrf_refused");
    let csrf = c.csrf.clone();
    let logout = c.call("POST", "/auth/logout", None, &[("x-csrf-token", &csrf)], true).await;
    assert_eq!(logout.status, StatusCode::NO_CONTENT, "{}", logout.body);
    assert_eq!(logout.headers["cache-control"], "no-store");
    assert!(logout.set_cookies.iter().any(|c| c == "__Host-session=; Path=/; Max-Age=0; Secure; SameSite=Strict; HttpOnly"),
            "{:?}", logout.set_cookies);
    assert!(logout.set_cookies.iter().any(|c| c == "__Host-csrf=; Path=/; Max-Age=0; Secure; SameSite=Strict"),
            "{:?}", logout.set_cookies);
    assert_eq!(c.call("GET", "/api/me", None, &[], true).await.status, StatusCode::UNAUTHORIZED);

    // no free connection within the acquire timeout is 503, not 500
    let full = gateway(&database_url);
    let mut held = Vec::new();
    for _ in 0..full.pool.status().max_size {
        held.push(full.pool.get().await.unwrap());
    }
    c.app = router(full);
    let busy = c.call("GET", "/api/hello", None, &[], false).await;
    assert_problem(&busy, 503, "pgmi.unavailable");
    assert_eq!(busy.headers["retry-after"], "1");
    drop(held);
}
