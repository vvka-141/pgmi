# Rust reference gateway

Demo code from pgmi, a baseline to learn from, not a library. Read it, then
write your own gateway that fits this project. The wire rules are in
[../PROTOCOL.md](../PROTOCOL.md); every place that needs a project decision is
marked `ADAPT`.

axum 0.8 on tokio, tokio-postgres with a deadpool pool, `jsonwebtoken` for
token checks and one `reqwest::Client` for every outgoing call.

| File | What |
|---|---|
| `src/main.rs` | logging, configuration, listen |
| `src/lib.rs` | `Config` (the environment), `Gateway` (the shared state, built once), the router and its layers, `serve` |
| `src/error.rs` | `GatewayError`: every answer the gateway gives on its own, mapped in one place |
| `src/rest.rs` | REST under `/api` |
| `src/mcp.rs` | MCP on `/mcp`, both protocol eras |
| `src/login.rs` | OIDC sign-in, dev sign-in, logout, session and CSRF cookies |
| `src/resource_server.rs` | `/mcp` as an OAuth resource server: metadata, token checks, the `McpCaller` extractor, rate limits |
| `src/oidc.rs` | discovery, the cached key set, the token checks both of the above share |
| `src/channel.rs` | every database call behind the `Channel` trait, and its decorators |
| `src/interceptors.rs` | the request and response interceptor pipeline, and two examples |
| `src/db.rs` | the pool and libpq `sslmode` |
| `tests/` | the router in-process and on a socket; database tests with `DATABASE_URL` |

## Run it

```bash
DATABASE_URL="postgresql://mydb_gateway:$GATEWAY_PASSWORD@localhost:5432/mydb" cargo run
cargo test      # the database test runs when DATABASE_URL is set
```

Build and run the binary on the same system (or in the same container image):
a binary built against a newer glibc does not start on an older one.

Settings are the environment variables of `../PROTOCOL.md` ("Configuration").

## How a request flows

`Gateway::build` creates everything once: the pool, the decorated channel,
the interceptor pipeline, the sign-in, the resource server, the rate limiters,
one `reqwest::Client`. It goes into an `Arc<Gateway>`, which every handler
receives as `State`. The compiler checks that all of it is `Send + Sync`;
what changes at run time (rate-limit buckets, pending sign-ins, key caches)
sits behind a lock.

Rules that hold for many routes are layers in `router()`: the body read
(size limit, read timeout), the `Origin` check, the per-IP rate limit on
`/mcp`. Per-route input comes from extractors: `McpCaller` verifies the
bearer token or takes the connection handle before the MCP handler runs.

A handler returns `Result<Response, GatewayError>`. `?` turns a database
error, a refused token or a failed interceptor into the right answer;
`IntoResponse for GatewayError` writes the problem, and `into_rpc` the same
failure as a JSON-RPC error on `/mcp`. Add a variant there, never a
hand-written error response in a handler.

## Calling a new handler

REST and MCP need no gateway change: a new REST route is reachable at
`/api/<its path>` and a new MCP tool through `tools/call`, because the gateway
forwards to `api.rest_invoke` and `api.mcp_handle_request` and the database
routes. A Rust client calls it like any other route:

```rust
let response = http
    .post("http://localhost:8080/api/projects")
    .header("x-csrf-token", &csrf)
    .header("cookie", format!("__Host-session={session}"))
    .json(&serde_json::json!({"name": "Apollo"}))
    .send()
    .await?;
if !response.status().is_success() {
    let problem: serde_json::Value = response.json().await?;  // application/problem+json
    // branch on problem["code"], never on title or detail
}
```

A new database entry point (RPC through `api.rpc_route_policy` and
`api.rpc_invoke`, say) is one more method on `Channel` in `channel.rs`,
implemented on `Postgres` with the same policy-then-dispatch pattern, and on
each decorator (most pass it through).

## The two seams

Both are in `../PROTOCOL.md` ("Extension seams").

### The database channel

`Gateway::build` composes it once:

```rust
let channel = ConnectionRetry::new(SerializationRetry::new(
    Tracing(Postgres::new(pool.clone())), config.retry_max_attempts, config.retry_base_delay));
```

A decorator is a struct holding the inner channel and implementing `Channel`
itself. `ConnectionRetry` shows the rule every retry keeps: a call is sent
again only when it cannot apply anything twice (a policy lookup, a
`readOnly` route). A write never is. The driver's error is classified once,
in `From<tokio_postgres::Error> for DbError`; decorators branch on the
variant (`ConnectionLost`, `Server { sqlstate }`, `PoolTimeout`).

### Interceptors

`INTERCEPTORS=embed,request-id` runs the two examples; unset, none run and
every body passes as bytes. Add your own in `build_pipeline()` in
`interceptors.rs`. An interceptor implements `Interceptor` (name, failure
policy, timeout, `applies`) and `RequestInterceptor` or `ResponseInterceptor`.
Its timeout cancels it: the future is dropped, so nothing it does late can
touch the request.

Embeddings from a real model. `reqwest::Client` pools its connections and is
cheap to clone; clone the `http` client `build_pipeline` receives (rename
it from `_http`):

```rust
struct ModelEmbedder { http: reqwest::Client, url: String, key: String }

#[async_trait]
impl Embedder for ModelEmbedder {
    async fn embed(&self, texts: Vec<String>) -> Result<Vec<Vec<f64>>, BoxError> {
        #[derive(serde::Deserialize)] struct Item { embedding: Vec<f64> }
        #[derive(serde::Deserialize)] struct Answer { data: Vec<Item> }
        let answer: Answer = self.http.post(&self.url).bearer_auth(&self.key)
            .json(&serde_json::json!({"model": "text-embedding-3-small", "input": texts}))
            .send().await?.error_for_status()?.json().await?;
        Ok(answer.data.into_iter().map(|item| item.embedding).collect())
    }
}

// in build_pipeline:
let embedder = ModelEmbedder { http: http.clone(), url: env::var("EMBED_URL")?, key: env::var("EMBED_KEY")? };
pipeline.requests.push(Arc::new(EmbedFields::new(Arc::new(embedder), Failure::Degrade)));
```

Then mark the property in the handler's `inputSchema`:
`"query": {"type": "string", "x-embed": true}, "query_embedding": {"type": "array"}`.

Signed storage URLs, after the commit:

```rust
struct SignUrls { signer: Arc<dyn Signer> }   // the key comes from configuration, never logged

impl Interceptor for SignUrls {
    fn name(&self) -> &'static str { "sign-urls" }
    fn needs_json(&self) -> bool { true }
    // ADAPT: handler names, as the database publishes them
    fn applies(&self, ctx: &RequestContext) -> bool { ctx.handler.as_deref() == Some("list_files") }
}

#[async_trait]
impl ResponseInterceptor for SignUrls {
    async fn on_response(&self, response: &mut Response, _: &RequestContext) -> Result<(), BoxError> {
        if let Some(files) = response.body.json().and_then(|b| b.get_mut("files")).and_then(Value::as_array_mut) {
            for file in files {
                let signed = self.signer.sign(file["url"].as_str().unwrap_or_default(), 300)?;
                file["url"] = signed.into();
            }
        }
        Ok(())
    }
}
```

The rules: an interceptor keeps no per-request state (it is in the request,
the response and the context), selects itself from the context, and is
`Failure::Fail` unless the request is still right without it
(`Failure::Degrade`).

## OAuth and sign-in

### MCP clients with OAuth

`MCP_RESOURCE=https://mcp.example.com/mcp MCP_AUTHORIZATION_SERVERS=https://login.example.com`
turns it on (`../PROTOCOL.md`, "MCP clients with OAuth"). The `McpCaller`
extractor verifies the token and puts only `Identity::context()` into the
MCP context. A connection handle (`Authorization: Bearer cn_...`, or
`X-Connection` without another `Authorization`) goes to the database as it
is, OAuth on or off; one the database does not know is 401
(`mcp::handle_unknown`). The places to adapt are marked `ADAPT` in
`resource_server.rs`.

### Sign-in

With `OIDC_ISSUER`, `OIDC_CLIENT_ID` and `OIDC_REDIRECT_URI` set, `login.rs`
runs the authorization code flow with PKCE and checks the ID token with
`jsonwebtoken`, over the discovery document and key set `oidc.rs` caches.
`jsonwebtoken` does not check `iat` or a single audience, so `oidc.rs` does.
The pending sign-ins live in this process's memory: several processes behind
a load balancer need a shared store or sticky sessions (`ADAPT`).
`DEV_LOGIN=1` serves `POST /auth/dev-login` for an `env=dev` deploy; never in
production.

## What to adapt

Every place is marked `ADAPT` in the code.

| What | Where |
|---|---|
| the name in `pg_stat_activity`, the pool size | `db.rs` |
| the channel decorators; the header and body read timeouts | `lib.rs` |
| your interceptors | `build_pipeline()` in `interceptors.rs` |
| RPC, if you serve it | `Channel` in `channel.rs` |
| logging: JSON lines, OpenTelemetry, levels | `main.rs` |
| cookie names and lifetimes, the development issuer, second-factor mapping; pending sign-ins across processes | `login.rs` |
| client ids that are never agents; the client address behind a reverse proxy | `resource_server.rs` |
