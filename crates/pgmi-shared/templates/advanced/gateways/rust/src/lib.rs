// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own gateway that fits this project: its framework, its
// configuration, its names. Every place that needs a project decision is
// marked ADAPT. The wire rules it keeps are in ../../PROTOCOL.md.
//
// A gateway in front of the advanced template, in axum:
//
//     ANY  /api/<path>     REST: api.rest_invoke                         rest.rs
//     POST /mcp            MCP, both eras: api.mcp_handle_request         mcp.rs
//     GET  /auth/login, /auth/callback, POST /auth/logout   OIDC sign-in  login.rs
//     POST /auth/dev-login development sign-in (DEV_LOGIN=1)              login.rs
//     GET  /.well-known/oauth-protected-resource[/mcp]                    resource_server.rs
//     GET  /health
//
// The two extension seams (PROTOCOL.md, "Extension seams"):
//
//     channel.rs       every database call behind one trait, decorated at
//                      startup: retries and tracing are decorators, so are yours
//     interceptors.rs  request interceptors after the policy call and before
//                      the transaction, response interceptors after the commit
//
// Everything a request needs is in one Arc<Gateway>, built once in
// Gateway::build and handed to every handler as axum State. Nothing in it
// changes after startup except behind a lock (rate limits, pending sign-ins,
// key caches), and the compiler checks that it is Send + Sync.

pub mod channel;
pub mod db;
pub mod error;
pub mod interceptors;
pub mod login;
pub mod mcp;
pub mod oidc;
pub mod resource_server;
pub mod rest;

use std::collections::HashSet;
use std::env;
use std::net::{IpAddr, SocketAddr};
use std::str::FromStr;
use std::sync::Arc;
use std::time::Duration;

use axum::body::Body;
use axum::extract::{ConnectInfo, DefaultBodyLimit, Request, State};
use axum::http::header::{CONTENT_TYPE, ORIGIN};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::{any, get, post};
use axum::{Extension, Router};
use deadpool_postgres::Pool;
use tower::Layer;

use crate::channel::{Channel, ConnectionRetry, Postgres, SerializationRetry, Tracing};
use crate::error::{GatewayError, Problem};
use crate::interceptors::{Failure, Pipeline};
use crate::login::Login;
use crate::resource_server::{RateLimiter, ResourceServer};

pub type BoxError = Box<dyn std::error::Error + Send + Sync>;

pub const MAX_BODY: usize = 1024 * 1024;
/// ADAPT: how long a client may take to send its headers, then its body.
pub const HEADER_READ_TIMEOUT: Duration = Duration::from_secs(10);
pub const MAX_HEADER_BYTES: usize = 64 << 10;
pub const BODY_READ_TIMEOUT: Duration = Duration::from_secs(30);

#[derive(Clone, Debug)]
pub struct OidcConfig {
    pub issuer: String,
    pub client_id: String,
    pub client_secret: Option<String>,
    pub redirect_uri: String,
    pub scopes: String,
}

/// The environment variables of ../../PROTOCOL.md ("Configuration"), the same
/// names in every reference gateway.
#[derive(Clone, Debug)]
pub struct Config {
    pub database_url: String,
    pub host: String,
    pub port: u16,
    pub allowed_origins: HashSet<String>,
    pub csrf_secret: Vec<u8>,
    pub dev_login: bool,
    pub pool_max_size: usize,
    pub retry_max_attempts: u32,
    pub retry_base_delay: Duration,
    /// Interceptors read what is left of it from their context, and every
    /// transaction takes it as its statement_timeout.
    pub request_deadline: Duration,
    pub interceptors: Vec<String>,
    pub embed_failure: Failure,
    pub oidc: Option<OidcConfig>,
    pub mcp_resource: Option<String>,
    pub mcp_authorization_servers: Vec<String>,
    pub mcp_scopes: Vec<String>,
    pub rate_limit_per_ip: u32,
    pub rate_limit_per_client: u32,
}

fn var(name: &str) -> Option<String> {
    env::var(name).ok().filter(|v| !v.is_empty())
}

fn number<T: FromStr>(name: &str, fallback: T) -> Result<T, String> {
    var(name).map_or(Ok(fallback), |value| value.parse().map_err(|_| format!("{name}={value} is not a number")))
}

fn list(value: &str, separator: char) -> Vec<String> {
    value.split(separator).map(str::trim).filter(|s| !s.is_empty()).map(str::to_owned).collect()
}

impl Config {
    /// The defaults, for tests and for from_env.
    pub fn new(database_url: impl Into<String>) -> Self {
        Config {
            database_url: database_url.into(),
            host: "127.0.0.1".into(),
            port: 8080,
            allowed_origins: HashSet::new(),
            csrf_secret: rand::random::<[u8; 32]>().to_vec(),
            dev_login: false,
            pool_max_size: 10,
            retry_max_attempts: 3,
            retry_base_delay: Duration::from_millis(50),
            request_deadline: Duration::from_secs(30),
            interceptors: Vec::new(),
            embed_failure: Failure::Fail,
            oidc: None,
            mcp_resource: None,
            mcp_authorization_servers: Vec::new(),
            mcp_scopes: vec!["openid".into()],
            rate_limit_per_ip: 600,
            rate_limit_per_client: 300,
        }
    }

    /// Refuses a setting it cannot apply, at startup, rather than dropping it.
    pub fn from_env() -> Result<Self, String> {
        let mut config = Config::new(var("DATABASE_URL").ok_or("DATABASE_URL is required")?);
        config.host = var("HOST").unwrap_or(config.host);
        config.port = number("PORT", config.port)?;
        config.allowed_origins = match var("ALLOWED_ORIGINS") {
            Some(origins) => list(&origins, ',').into_iter().collect(),
            None => [format!("http://localhost:{}", config.port), format!("http://127.0.0.1:{}", config.port)].into(),
        };
        if let Some(secret) = var("LOGIN_CSRF_SECRET") {
            config.csrf_secret = secret.into_bytes();
        }
        config.dev_login = var("DEV_LOGIN").as_deref() == Some("1");
        config.pool_max_size = number("POOL_MAX_SIZE", config.pool_max_size)?;
        config.retry_max_attempts = number("RETRY_MAX_ATTEMPTS", config.retry_max_attempts)?;
        config.retry_base_delay = Duration::try_from_secs_f64(number("RETRY_BASE_DELAY", 0.05)?)
            .map_err(|_| "RETRY_BASE_DELAY must be a number of seconds")?;
        config.request_deadline = Duration::try_from_secs_f64(number("REQUEST_DEADLINE_SECONDS", 30.0)?)
            .ok()
            .filter(|d| !d.is_zero())
            .ok_or("REQUEST_DEADLINE_SECONDS must be a positive number of seconds")?;
        config.interceptors = var("INTERCEPTORS").map_or_else(Vec::new, |names| list(&names, ','));
        config.embed_failure = match var("EMBED_FAILURE_POLICY").as_deref() {
            None | Some("fail") => Failure::Fail,
            Some("degrade") => Failure::Degrade,
            Some(other) => return Err(format!("EMBED_FAILURE_POLICY={other}: fail or degrade")),
        };
        if let Some(issuer) = var("OIDC_ISSUER") {
            let required = |name| var(name).ok_or(format!("OIDC_ISSUER is set but {name} is not"));
            config.oidc = Some(OidcConfig {
                issuer,
                client_id: required("OIDC_CLIENT_ID")?,
                client_secret: var("OIDC_CLIENT_SECRET"),
                redirect_uri: required("OIDC_REDIRECT_URI")?,
                scopes: var("OIDC_SCOPES").unwrap_or_else(|| "openid email profile".into()),
            });
        }
        config.mcp_resource = var("MCP_RESOURCE");
        config.mcp_authorization_servers = var("MCP_AUTHORIZATION_SERVERS").map_or_else(Vec::new, |s| list(&s, ','));
        if let Some(scopes) = var("MCP_SCOPES") {
            config.mcp_scopes = list(&scopes, ' ');
        }
        config.rate_limit_per_ip = number("RATE_LIMIT_PER_IP", config.rate_limit_per_ip)?;
        config.rate_limit_per_client = number("RATE_LIMIT_PER_CLIENT", config.rate_limit_per_client)?;
        Ok(config)
    }
}

pub struct Gateway {
    /// For sign-in and logout; request dispatch goes through `channel`.
    pub pool: Pool,
    pub channel: Arc<dyn Channel>,
    pub pipeline: Pipeline,
    pub request_deadline: Duration,
    pub allowed_origins: HashSet<String>,
    /// None: neither OIDC nor DEV_LOGIN is configured, and /api is anonymous.
    pub login: Option<Login>,
    /// None: MCP_RESOURCE is unset, and /mcp takes connection handles only.
    pub resource: Option<ResourceServer>,
    /// /mcp, /auth/login and /auth/callback: one limit per client address.
    pub per_ip: RateLimiter<IpAddr>,
    pub per_client: RateLimiter<resource_server::Identity>,
}

impl Gateway {
    pub fn build(config: Config) -> Result<Arc<Gateway>, BoxError> {
        let (pool, tls) = db::create_pool(&config.database_url, config.pool_max_size)?;
        // ADAPT: the decorators your gateway needs, in one place. Both
        // retries wrap the tracing, so each attempt is a log line of its own.
        let channel = ConnectionRetry::new(SerializationRetry::new(
            Tracing(Postgres::new(pool.clone(), tls)),
            config.retry_max_attempts,
            config.retry_base_delay,
        ));
        // One HTTP client for every outgoing call: it pools its connections
        // and is cheap to clone.
        let http = reqwest::Client::builder().connect_timeout(Duration::from_secs(5)).build()?;
        let login_client_id = config.oidc.as_ref().map(|o| o.client_id.clone());
        let resource = config
            .mcp_resource
            .as_deref()
            .map(|resource| ResourceServer::new(resource, &config.mcp_authorization_servers, config.mcp_scopes.clone(),
                                                login_client_id.as_deref(), http.clone()))
            .transpose()?;
        Ok(Arc::new(Gateway {
            pool,
            channel: Arc::new(channel),
            pipeline: interceptors::build_pipeline(&config.interceptors, config.embed_failure, &http)?,
            request_deadline: config.request_deadline,
            allowed_origins: config.allowed_origins,
            login: Login::new(config.oidc, config.csrf_secret, config.dev_login, http)?,
            resource,
            per_ip: RateLimiter::new(config.rate_limit_per_ip),
            per_client: RateLimiter::new(config.rate_limit_per_client),
        }))
    }
}

pub fn router(gateway: Arc<Gateway>) -> Router {
    let per_ip = middleware::from_fn_with_state(gateway.clone(), resource_server::limit_per_ip);
    let browser_facing = Router::new()
        .route("/api", any(rest::handle))
        .route("/api/{*path}", any(rest::handle))
        .route("/mcp", post(mcp::handle).layer(per_ip.clone()).fallback(mcp::method_not_allowed))
        .route("/auth/logout", post(login::logout))
        .route("/auth/dev-login", post(login::dev_sign_in))
        .route_layer(middleware::from_fn_with_state(gateway.clone(), check_origin));
    // Sign-in starts and ends with top-level navigations, which state and
    // PKCE protect; the identity provider's redirect is not an allowed Origin.
    // Both share /mcp's per-IP limit, so one address cannot push other
    // people's pending sign-ins out.
    let mut router = browser_facing
        .route("/auth/login", get(login::begin).layer(per_ip.clone()))
        .route("/auth/callback", get(login::callback).layer(per_ip))
        .route("/health", get(|| async { ([(CONTENT_TYPE, "application/json")], r#"{"status":"healthy"}"#) }));
    for path in gateway.resource.iter().flat_map(|r| r.metadata_paths.clone()) {
        router = router.route(&path, get(resource_server::metadata));
    }
    router
        .fallback(|| async { GatewayError::from(Problem::not_found()) })
        .layer(DefaultBodyLimit::max(MAX_BODY))
        .layer(middleware::from_fn(read_body))
        .with_state(gateway)
}

// Standard web plumbing, not pgmi-specific: replace it with your framework's own.
/// DNS-rebinding protection: a browser sends Origin, and any value not on
/// the allowlist is refused, on every browser-facing route. Clients that
/// send no Origin are not browsers and pass.
async fn check_origin(State(gateway): State<Arc<Gateway>>, request: Request, next: Next) -> Response {
    match request.headers().get(ORIGIN).map(|o| o.to_str().unwrap_or_default()) {
        Some(origin) if !gateway.allowed_origins.contains(origin) => {
            GatewayError::from(Problem::forbidden("pgmi.origin_refused")).into_response()
        }
        _ => next.run(request).await,
    }
}

/// The body is read here once, within the size limit and the read timeout,
/// and handed on: a client that stops sending gets 408 instead of holding a
/// task, and one that sends too much gets 413 before the database is called.
async fn read_body(request: Request, next: Next) -> Response {
    let (parts, body) = request.into_parts();
    match tokio::time::timeout(BODY_READ_TIMEOUT, axum::body::to_bytes(body, MAX_BODY)).await {
        Err(_) => GatewayError::from(
            Problem::new(axum::http::StatusCode::REQUEST_TIMEOUT, "Request Timeout", "pgmi.request_timeout")
                .detail(format!("the request body did not arrive within {} s", BODY_READ_TIMEOUT.as_secs())),
        )
        .into_response(),
        Ok(Err(_)) => GatewayError::from(
            Problem::new(axum::http::StatusCode::PAYLOAD_TOO_LARGE, "Content Too Large", "pgmi.body_too_large")
                .detail(format!("the request body is limited to {MAX_BODY} bytes")),
        )
        .into_response(),
        Ok(Ok(bytes)) => next.run(Request::from_parts(parts, Body::from(bytes))).await,
    }
}

/// Serves `app` on `listener`, closing a connection whose request headers do
/// not arrive within HEADER_READ_TIMEOUT (axum::serve has no such setting),
/// and telling every request its peer address (ConnectInfo) for the per-IP
/// rate limit. It runs until the process stops: a failed accept (EMFILE
/// under a connection flood, a connection aborted before it was taken) is
/// logged and waited out by axum's Listener, never the end of the gateway.
pub async fn serve(mut listener: tokio::net::TcpListener, app: Router) {
    use axum::serve::Listener;
    use hyper_util::rt::{TokioExecutor, TokioIo, TokioTimer};
    use hyper_util::server::conn::auto::Builder;
    use hyper_util::service::TowerToHyperService;
    loop {
        let (stream, peer) = Listener::accept(&mut listener).await;
        let service = TowerToHyperService::new(Extension(ConnectInfo::<SocketAddr>(peer)).layer(app.clone()));
        tokio::spawn(async move {
            let mut builder = Builder::new(TokioExecutor::new());
            builder.http1().timer(TokioTimer::new()).header_read_timeout(HEADER_READ_TIMEOUT).max_buf_size(MAX_HEADER_BYTES);
            builder.http2().max_header_list_size(MAX_HEADER_BYTES as u32);
            let _ = builder.serve_connection(TokioIo::new(stream), service).await;
        });
    }
}
