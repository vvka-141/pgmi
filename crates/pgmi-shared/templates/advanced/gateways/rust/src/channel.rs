// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own that fits this project. The rules it keeps are in
// ../../PROTOCOL.md ("Extension seams", "One request, one transaction").
//
// The database channel: every call the gateway makes to PostgreSQL for a
// request, behind one trait.
//
//     Channel              one method per call: the policy and the dispatch
//                          of each protocol the gateway serves
//     Postgres             the real thing, over the deadpool pool
//     Tracing              a log line per call: op, route, outcome, time
//     SerializationRetry   40001/40P01: the whole transaction again
//     ConnectionRetry      a dropped connection: again, idempotent calls only
//
// A decorator is a struct that holds another Channel and is a Channel itself,
// so cross-cutting behaviour (caching, metrics, a test double) is composed
// once, at startup, and no call site changes:
//
//     ConnectionRetry::new(SerializationRetry::new(Tracing(Postgres::new(pool, tls)), 3, base))
//
// Both retries sit outside the tracing, so every attempt is a log line of its
// own. The composed channel lives in an Arc<dyn Channel> shared by every
// request task: `Send + Sync` on the trait is the compiler's proof that it is
// safe to share, and per-request data travels in the arguments.

use std::collections::BTreeMap;
use std::fmt;
use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use axum::body::Bytes;
use deadpool_postgres::{Object, Pool, PoolError};
use postgres_native_tls::MakeTlsConnector;
use serde_json::{Map, Value};
use tokio::time::Instant;
use tokio_postgres::error::SqlState;
use tokio_postgres::types::ToSql;
use tokio_postgres::{IsolationLevel, Row};

use crate::BoxError;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Protocol {
    Rest,
    Mcp,
}

/// What every channel call and every interceptor may read about the request.
/// Interceptors get `&RequestContext`: read-only, by the borrow checker.
///
/// There is no principal here: the gateway forwards handles and the database
/// resolves who is calling inside the transaction, so nothing before the
/// dispatch knows. `handler` and `annotations` come from the policy call.
#[derive(Clone, Debug)]
pub struct RequestContext {
    pub request_id: String,
    pub protocol: Protocol,
    /// REST: the HTTP method; MCP: the JSON-RPC method.
    pub method: String,
    /// REST: the URL under /api, query included; MCP: /mcp.
    pub url: String,
    /// The request is abandoned after this; interceptors are bounded by it.
    pub deadline: Instant,
    pub handler: Option<String>,
    /// `{"property": {"x-keyword": value}}` from the handler's inputSchema.
    pub annotations: Arc<Map<String, Value>>,
}

impl RequestContext {
    pub fn new(protocol: Protocol, method: impl Into<String>, url: impl Into<String>, deadline: Instant) -> Self {
        RequestContext {
            request_id: format!("{:032x}", rand::random::<u128>()),
            protocol,
            method: method.into(),
            url: url.into(),
            deadline,
            handler: None,
            annotations: Arc::default(),
        }
    }

    /// The context with what the policy says about the handler.
    pub fn with_policy(mut self, policy: &Policy) -> Self {
        self.handler = policy.handler.clone();
        self.annotations = Arc::new(match &policy.annotations {
            Some(Value::Object(map)) => map.clone(),
            _ => Map::new(),
        });
        self
    }
}

/// One row of a policy function (api.rest_route_policy, api.mcp_request_policy).
/// isolation None: the connection default.
#[derive(Clone, Debug, Default)]
pub struct Policy {
    pub isolation: Option<IsolationLevel>,
    pub read_only: bool,
    pub deferrable: bool,
    pub handler: Option<String>,
    pub annotations: Option<Value>,
    /// MCP only: the 2026-07-28 era.
    pub modern: bool,
    /// MCP only: the answer instead of a dispatch.
    pub refusal: Option<Value>,
}

#[derive(Clone, Debug)]
pub struct RestResult {
    pub status: u16,
    /// Lower-case names.
    pub headers: BTreeMap<String, String>,
    pub content: Bytes,
}

/// The MCP-Protocol-Version, Mcp-Method and Mcp-Name headers, as sent.
pub type McpHeaders = [Option<String>; 3];

/// What a call can end in, classified once, where the driver's error is
/// still at hand. Decorators branch on the variant, never on a message.
#[derive(Debug)]
pub enum DbError {
    /// No pooled connection: none free within the acquire timeout, or the
    /// database could not be reached. Nothing was sent, so not a lost
    /// connection: answered 503 at once rather than waited out twice.
    NoConnection,
    /// The connection broke. Nothing says whether the server committed.
    ConnectionLost(BoxError),
    /// The server answered with a SQLSTATE.
    Server { sqlstate: SqlState, source: BoxError },
    RetriesExhausted { sqlstate: SqlState, attempts: u32 },
    /// The request ran past its deadline: statement_timeout cancelled the
    /// statement (57014), or nothing was left to start one. Never retried.
    DeadlineExceeded { request_id: String },
    Other(BoxError),
}

impl DbError {
    /// What a log line may say: never the server's message, which can quote data.
    pub fn summary(&self) -> String {
        match self {
            DbError::NoConnection => "no connection".into(),
            DbError::ConnectionLost(_) => "connection lost".into(),
            DbError::DeadlineExceeded { .. } => "deadline exceeded".into(),
            DbError::Server { sqlstate, .. } | DbError::RetriesExhausted { sqlstate, .. } => sqlstate.code().into(),
            DbError::Other(_) => "error".into(),
        }
    }
}

impl fmt::Display for DbError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            DbError::NoConnection => write!(f, "no pooled connection: none free in time, or the database unreachable"),
            DbError::ConnectionLost(e) => write!(f, "connection lost: {e}"),
            DbError::DeadlineExceeded { request_id } => write!(f, "request {request_id} ran past its deadline"),
            DbError::Server { sqlstate, source } => write!(f, "{}: {source}", sqlstate.code()),
            DbError::RetriesExhausted { sqlstate, attempts } => {
                write!(f, "{} on each of {attempts} attempts", sqlstate.code())
            }
            DbError::Other(e) => write!(f, "{e}"),
        }
    }
}

impl From<tokio_postgres::Error> for DbError {
    /// Class 08 (connection exception) and 57P01..57P03 (the server shut
    /// down) are a lost connection; so is a closed socket with no SQLSTATE.
    /// 40001 is not: the server answered.
    fn from(error: tokio_postgres::Error) -> Self {
        match error.code().cloned() {
            Some(code) if code.code().starts_with("08") || matches!(code.code(), "57P01" | "57P02" | "57P03") => {
                DbError::ConnectionLost(error.into())
            }
            Some(sqlstate) => DbError::Server { sqlstate, source: error.into() },
            None if error.is_closed() || std::error::Error::source(&error).is_some_and(|s| s.is::<std::io::Error>()) => {
                DbError::ConnectionLost(error.into())
            }
            None => DbError::Other(error.into()),
        }
    }
}

impl From<PoolError> for DbError {
    fn from(error: PoolError) -> Self {
        match error {
            PoolError::Timeout(_) => DbError::NoConnection,
            PoolError::Backend(_) => DbError::NoConnection,
            other => DbError::Other(other.into()),
        }
    }
}

/// One entry per database call. ADAPT: a gateway that serves RPC adds
/// rpc_policy and rpc_invoke here, and every decorator gets them too.
#[async_trait]
pub trait Channel: Send + Sync {
    async fn rest_policy(&self, ctx: &RequestContext, requested: Option<&str>) -> Result<Policy, DbError>;

    async fn rest_invoke(&self, ctx: &RequestContext, policy: &Policy, headers: &BTreeMap<String, String>,
                         body: Option<&[u8]>) -> Result<RestResult, DbError>;

    async fn mcp_policy(&self, ctx: &RequestContext, request: &Value, requested: Option<&str>,
                        mcp_headers: &McpHeaders) -> Result<Policy, DbError>;

    /// None: a notification, which has no answer.
    async fn mcp_request(&self, ctx: &RequestContext, policy: &Policy, request: &Value,
                         context: Option<&Value>) -> Result<Option<Value>, DbError>;
}

// -- the real channel ------------------------------------------------------

/// Each policy call runs on a pooled connection in its own short transaction;
/// each dispatch in one transaction started with the resolved characteristics
/// (`START TRANSACTION ISOLATION LEVEL ... READ ONLY DEFERRABLE`). Both set
/// what is left of the request's deadline as statement_timeout, local to the
/// transaction, so a pooled connection carries nothing from one request to
/// the next. The SQL is fixed text; the request only ever travels as
/// parameters.
///
/// The policy costs one round trip per request. It changes only at deploy
/// time: a caching decorator would key it on api.catalog_version().
///
/// A client that hangs up cancels its statement: see Cancellable.
pub struct Postgres {
    pool: Pool,
    tls: MakeTlsConnector,
}

impl Postgres {
    pub fn new(pool: Pool, tls: MakeTlsConnector) -> Self {
        Postgres { pool, tls }
    }

    async fn policy_row(&self, ctx: &RequestContext, sql: &str, params: &[&(dyn ToSql + Sync)])
        -> Result<Option<Row>, DbError> {
        self.run(ctx, None, sql, params).await
    }

    async fn dispatch(&self, ctx: &RequestContext, policy: &Policy, sql: &str, params: &[&(dyn ToSql + Sync)])
        -> Result<Option<Row>, DbError> {
        self.run(ctx, Some(policy), sql, params).await
    }

    /// One attempt is one transaction. The commit is inside it: it can fail
    /// with 40001 too, and SerializationRetry must see that. An error drops
    /// the transaction, which rolls it back.
    async fn run(&self, ctx: &RequestContext, characteristics: Option<&Policy>, sql: &str,
                 params: &[&(dyn ToSql + Sync)]) -> Result<Option<Row>, DbError> {
        let mut client = Cancellable::new(self.pool.get().await?, self.tls.clone());
        let result = Self::attempt(client.object(), ctx, characteristics, sql, params).await;
        client.finished();
        result
    }

    async fn attempt(client: &mut Object, ctx: &RequestContext, characteristics: Option<&Policy>, sql: &str,
                     params: &[&(dyn ToSql + Sync)]) -> Result<Option<Row>, DbError> {
        let mut builder = client.build_transaction();
        if let Some(policy) = characteristics {
            builder = builder.read_only(policy.read_only).deferrable(policy.deferrable);
            if let Some(level) = policy.isolation {
                builder = builder.isolation_level(level);
            }
        }
        let transaction = builder.start().await?;
        let timeout = transaction.prepare_cached("SELECT set_config('statement_timeout', $1, true)").await?;
        let milliseconds = statement_timeout(ctx.deadline.saturating_duration_since(Instant::now()))
            .ok_or_else(|| DbError::DeadlineExceeded { request_id: ctx.request_id.clone() })?;
        transaction.execute(&timeout, &[&milliseconds]).await?;
        let outcome = async {
            let statement = transaction.prepare_cached(sql).await?;
            let row = transaction.query_opt(&statement, params).await?;
            transaction.commit().await?;
            Ok::<_, tokio_postgres::Error>(row)
        };
        outcome.await.map_err(|error| {
            // A 57014 before the deadline is someone else's cancel
            // (pg_cancel_backend), not ours: an ordinary server error.
            if error.code() == Some(&SqlState::QUERY_CANCELED) && Instant::now() >= ctx.deadline {
                DbError::DeadlineExceeded { request_id: ctx.request_id.clone() }
            } else {
                error.into()
            }
        })
    }
}

/// A pooled connection for the length of one attempt. When the client hangs
/// up, hyper drops the handler's future, and with it this guard before
/// `finished` ran. tokio-postgres alone would only stop waiting: the server
/// would run the statement to its end on a connection nobody reads. So Drop
/// sends a cancel request for the backend (what pg_cancel_backend does, over
/// a connection of its own) and takes the connection out of the pool instead
/// of returning it: it is still busy with the statement, and a cancel that
/// lands late would hit the next request's statement. Closing it once the
/// cancel is sent rolls back whatever the transaction did.
struct Cancellable {
    client: Option<Object>,
    tls: MakeTlsConnector,
}

impl Cancellable {
    fn new(client: Object, tls: MakeTlsConnector) -> Self {
        Cancellable { client: Some(client), tls }
    }

    fn object(&mut self) -> &mut Object {
        self.client.as_mut().expect("held until finished")
    }

    /// The attempt ended, in success or an error: the connection goes back
    /// to the pool, which drops it if it broke.
    fn finished(mut self) {
        self.client.take();
    }
}

impl Drop for Cancellable {
    fn drop(&mut self) {
        let Some(client) = self.client.take() else { return };
        let client = Object::take(client);
        let (token, tls) = (client.cancel_token(), self.tls.clone());
        // Drop cannot await: the cancel is its own task, and the connection
        // closes after it, so the server reads the cancel first.
        tokio::spawn(async move {
            if let Err(error) = token.cancel_query(tls).await {
                tracing::warn!(%error, "the cancel request for a dropped request failed");
            }
            drop(client);
        });
    }
}

/// What is left of the request, as statement_timeout takes it: milliseconds
/// rounded up, so the server's timer fires no earlier than the gateway's
/// deadline, as text. None when nothing is left: 0 would disable the timeout.
fn statement_timeout(left: Duration) -> Option<String> {
    Some(left.as_nanos().div_ceil(1_000_000)).filter(|&ms| ms > 0).map(|ms| ms.to_string())
}

fn isolation(level: Option<String>) -> Option<IsolationLevel> {
    match level?.as_str() {
        "serializable" => Some(IsolationLevel::Serializable),
        "repeatable read" => Some(IsolationLevel::RepeatableRead),
        _ => Some(IsolationLevel::ReadCommitted),
    }
}

#[async_trait]
impl Channel for Postgres {
    async fn rest_policy(&self, ctx: &RequestContext, requested: Option<&str>) -> Result<Policy, DbError> {
        let row = self
            .policy_row(
                ctx,
                "SELECT transaction_isolation, transaction_read_only, transaction_deferrable, handler_name, \
                 input_annotations FROM api.rest_route_policy($1, $2, $3)",
                &[&ctx.method, &ctx.url, &requested],
            )
            .await?;
        // No row: no such route. Open with the defaults; the invoke answers 404.
        let Some(row) = row else { return Ok(Policy::default()) };
        Ok(Policy {
            isolation: isolation(row.get("transaction_isolation")),
            read_only: row.get("transaction_read_only"),
            deferrable: row.get("transaction_deferrable"),
            handler: row.get("handler_name"),
            annotations: row.get("input_annotations"),
            ..Policy::default()
        })
    }

    async fn rest_invoke(&self, ctx: &RequestContext, policy: &Policy, headers: &BTreeMap<String, String>,
                         body: Option<&[u8]>) -> Result<RestResult, DbError> {
        let names: Vec<&str> = headers.keys().map(String::as_str).collect();
        let values: Vec<&str> = headers.values().map(String::as_str).collect();
        let row = self
            .dispatch(
                ctx,
                policy,
                "SELECT r.status_code, extensions.hstore_to_json(r.headers) AS headers, r.content \
                 FROM api.rest_invoke($1, $2, extensions.hstore($3::text[], $4::text[]), $5::bytea) r",
                &[&ctx.method, &ctx.url, &names, &values, &body],
            )
            .await?
            .ok_or_else(|| DbError::Other("api.rest_invoke returned no row".into()))?;
        let headers = match row.get::<_, Option<Value>>("headers") {
            Some(Value::Object(map)) => map
                .into_iter()
                .filter_map(|(name, value)| Some((name.to_lowercase(), value.as_str()?.to_owned())))
                .collect(),
            _ => BTreeMap::new(),
        };
        Ok(RestResult {
            status: u16::try_from(row.get::<_, i32>("status_code")).map_err(|e| DbError::Other(e.into()))?,
            headers,
            content: row.get::<_, Option<Vec<u8>>>("content").unwrap_or_default().into(),
        })
    }

    async fn mcp_policy(&self, ctx: &RequestContext, request: &Value, requested: Option<&str>,
                        mcp_headers: &McpHeaders) -> Result<Policy, DbError> {
        let [version, method, name] = mcp_headers;
        let request = request.to_string();
        let row = self
            .policy_row(
                ctx,
                "SELECT transaction_isolation, transaction_read_only, transaction_deferrable, handler_name, \
                 input_annotations, modern, refusal FROM api.mcp_request_policy($1, $2, $3, $4, $5)",
                &[&request, &requested, version, method, name],
            )
            .await?
            .ok_or_else(|| DbError::Other("api.mcp_request_policy returned no row".into()))?;
        Ok(Policy {
            isolation: isolation(row.get("transaction_isolation")),
            read_only: row.get("transaction_read_only"),
            deferrable: row.get("transaction_deferrable"),
            handler: row.get("handler_name"),
            annotations: row.get("input_annotations"),
            modern: row.get("modern"),
            refusal: row.get("refusal"),
        })
    }

    async fn mcp_request(&self, ctx: &RequestContext, policy: &Policy, request: &Value,
                         context: Option<&Value>) -> Result<Option<Value>, DbError> {
        let row = self
            .dispatch(ctx, policy, "SELECT (api.mcp_handle_request($1, $2)).envelope", &[request, &context])
            .await?;
        Ok(row.and_then(|row| row.get(0)))
    }
}

// -- decorators ------------------------------------------------------------

/// One line per call with the request id, so a slow or failing request can be
/// followed from the gateway log into the database's exchange log. Never
/// bodies or headers: they carry personal data and session handles.
pub struct Tracing<C>(pub C);

async fn traced<T>(ctx: &RequestContext, op: &'static str, call: impl Future<Output = Result<T, DbError>>,
                   outcome: impl FnOnce(&T) -> String) -> Result<T, DbError> {
    let started = Instant::now();
    let result = call.await;
    let ms = started.elapsed().as_secs_f64() * 1000.0;
    // A query can carry a token: the line names the path only.
    let path = ctx.url.split('?').next().unwrap_or_default();
    match &result {
        Ok(value) => tracing::info!(request_id = %ctx.request_id, op, method = %ctx.method, url = %path,
                                    outcome = %outcome(value), ms, "db"),
        Err(error) => tracing::warn!(request_id = %ctx.request_id, op, method = %ctx.method, url = %path,
                                     error = %error.summary(), ms, "db"),
    }
    result
}

#[async_trait]
impl<C: Channel> Channel for Tracing<C> {
    async fn rest_policy(&self, ctx: &RequestContext, requested: Option<&str>) -> Result<Policy, DbError> {
        traced(ctx, "rest.policy", self.0.rest_policy(ctx, requested), |p| format!("handler={:?}", p.handler)).await
    }

    async fn rest_invoke(&self, ctx: &RequestContext, policy: &Policy, headers: &BTreeMap<String, String>,
                         body: Option<&[u8]>) -> Result<RestResult, DbError> {
        traced(ctx, "rest.invoke", self.0.rest_invoke(ctx, policy, headers, body), |r| format!("status={}", r.status))
            .await
    }

    async fn mcp_policy(&self, ctx: &RequestContext, request: &Value, requested: Option<&str>,
                        mcp_headers: &McpHeaders) -> Result<Policy, DbError> {
        traced(ctx, "mcp.policy", self.0.mcp_policy(ctx, request, requested, mcp_headers), |p| match p.refusal {
            Some(_) => "refused".into(),
            None => format!("handler={:?}", p.handler),
        })
        .await
    }

    async fn mcp_request(&self, ctx: &RequestContext, policy: &Policy, request: &Value,
                         context: Option<&Value>) -> Result<Option<Value>, DbError> {
        traced(ctx, "mcp.request", self.0.mcp_request(ctx, policy, request, context), |e| match e {
            None => "notification".into(),
            Some(envelope) if envelope.get("error").is_some() => "error".into(),
            Some(_) => "result".into(),
        })
        .await
    }
}

/// Exponential backoff with full jitter: unjittered losers of a race retry in
/// lockstep and collide again.
fn backoff(attempt: u32, base: Duration) -> Duration {
    let ceiling = base.saturating_mul(1 << attempt.min(16)).min(Duration::from_secs(1));
    ceiling.mul_f64(rand::random::<f64>())
}

/// 40001 (serialization failure) and 40P01 (deadlock): the database rolled
/// the whole transaction back, so running it again is safe for any call,
/// writes included. Each attempt is a new transaction and therefore a fresh
/// snapshot; a savepoint or a loop in PL/pgSQL keeps the old snapshot and
/// never converges. After the last attempt, DbError::RetriesExhausted.
///
/// A handler on a route that can hit 40001 must be safe to run twice: a side
/// effect outside the transaction (an HTTP call, a queue publish) happens again.
pub struct SerializationRetry<C> {
    inner: C,
    attempts: u32,
    base_delay: Duration,
}

impl<C> SerializationRetry<C> {
    pub fn new(inner: C, attempts: u32, base_delay: Duration) -> Self {
        SerializationRetry { inner, attempts: attempts.max(1), base_delay }
    }

    async fn retrying<T, F, Fut>(&self, mut call: F) -> Result<T, DbError>
    where
        F: FnMut() -> Fut,
        Fut: Future<Output = Result<T, DbError>>,
    {
        let mut attempt = 0;
        loop {
            match call().await {
                Err(DbError::Server { sqlstate, .. })
                    if sqlstate == SqlState::T_R_SERIALIZATION_FAILURE || sqlstate == SqlState::T_R_DEADLOCK_DETECTED =>
                {
                    attempt += 1;
                    if attempt >= self.attempts {
                        return Err(DbError::RetriesExhausted { sqlstate, attempts: self.attempts });
                    }
                    tokio::time::sleep(backoff(attempt - 1, self.base_delay)).await;
                }
                other => return other,
            }
        }
    }
}

#[async_trait]
impl<C: Channel> Channel for SerializationRetry<C> {
    async fn rest_policy(&self, ctx: &RequestContext, requested: Option<&str>) -> Result<Policy, DbError> {
        self.inner.rest_policy(ctx, requested).await
    }

    async fn rest_invoke(&self, ctx: &RequestContext, policy: &Policy, headers: &BTreeMap<String, String>,
                         body: Option<&[u8]>) -> Result<RestResult, DbError> {
        self.retrying(|| self.inner.rest_invoke(ctx, policy, headers, body)).await
    }

    async fn mcp_policy(&self, ctx: &RequestContext, request: &Value, requested: Option<&str>,
                        mcp_headers: &McpHeaders) -> Result<Policy, DbError> {
        self.inner.mcp_policy(ctx, request, requested, mcp_headers).await
    }

    async fn mcp_request(&self, ctx: &RequestContext, policy: &Policy, request: &Value,
                         context: Option<&Value>) -> Result<Option<Value>, DbError> {
        self.retrying(|| self.inner.mcp_request(ctx, policy, request, context)).await
    }
}

/// Sends a call again after the connection dropped ONLY if sending it twice
/// cannot apply anything twice: the policy lookups (STABLE functions) and a
/// dispatch the database runs READ ONLY (the route declared readOnly, so
/// PostgreSQL refuses any write in it). A write is never re-sent: the
/// connection can drop after the server committed and before the answer
/// arrived, and a second send would apply it twice. MCP is never read-only
/// (every exchange is logged), so its dispatch is never retried here.
///
/// The HTTP method says nothing: a GET handler can write. The database's
/// readOnly declaration does.
pub struct ConnectionRetry<C> {
    inner: C,
    attempts: u32,
    base_delay: Duration,
}

impl<C> ConnectionRetry<C> {
    pub fn new(inner: C) -> Self {
        ConnectionRetry { inner, attempts: 2, base_delay: Duration::from_millis(50) }
    }

    async fn retrying<T, F, Fut>(&self, ctx: &RequestContext, op: &'static str, mut call: F) -> Result<T, DbError>
    where
        F: FnMut() -> Fut,
        Fut: Future<Output = Result<T, DbError>>,
    {
        let mut attempt = 0;
        loop {
            match call().await {
                Err(DbError::ConnectionLost(error)) if attempt + 1 < self.attempts => {
                    attempt += 1;
                    tracing::warn!(request_id = %ctx.request_id, op, %error, attempt = attempt + 1, "connection lost, sending again");
                    tokio::time::sleep(backoff(attempt - 1, self.base_delay)).await;
                }
                other => return other,
            }
        }
    }
}

#[async_trait]
impl<C: Channel> Channel for ConnectionRetry<C> {
    async fn rest_policy(&self, ctx: &RequestContext, requested: Option<&str>) -> Result<Policy, DbError> {
        self.retrying(ctx, "rest.policy", || self.inner.rest_policy(ctx, requested)).await
    }

    async fn rest_invoke(&self, ctx: &RequestContext, policy: &Policy, headers: &BTreeMap<String, String>,
                         body: Option<&[u8]>) -> Result<RestResult, DbError> {
        if !policy.read_only {
            return self.inner.rest_invoke(ctx, policy, headers, body).await;
        }
        self.retrying(ctx, "rest.invoke", || self.inner.rest_invoke(ctx, policy, headers, body)).await
    }

    async fn mcp_policy(&self, ctx: &RequestContext, request: &Value, requested: Option<&str>,
                        mcp_headers: &McpHeaders) -> Result<Policy, DbError> {
        self.retrying(ctx, "mcp.policy", || self.inner.mcp_policy(ctx, request, requested, mcp_headers)).await
    }

    async fn mcp_request(&self, ctx: &RequestContext, policy: &Policy, request: &Value,
                         context: Option<&Value>) -> Result<Option<Value>, DbError> {
        self.inner.mcp_request(ctx, policy, request, context).await
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use super::*;

    /// A test double: answers each call from a script and counts the calls.
    #[derive(Default)]
    struct Scripted {
        answers: Mutex<Vec<Result<(), DbError>>>,
        calls: Mutex<Vec<&'static str>>,
    }

    impl Scripted {
        fn new(answers: Vec<Result<(), DbError>>) -> Self {
            Scripted { answers: Mutex::new(answers), calls: Mutex::default() }
        }

        fn next(&self, op: &'static str) -> Result<(), DbError> {
            self.calls.lock().unwrap().push(op);
            let mut answers = self.answers.lock().unwrap();
            if answers.is_empty() { Ok(()) } else { answers.remove(0) }
        }

        fn calls(&self) -> Vec<&'static str> {
            self.calls.lock().unwrap().clone()
        }
    }

    #[async_trait]
    impl Channel for Arc<Scripted> {
        async fn rest_policy(&self, _: &RequestContext, _: Option<&str>) -> Result<Policy, DbError> {
            self.next("rest.policy").map(|_| Policy::default())
        }

        async fn rest_invoke(&self, _: &RequestContext, _: &Policy, _: &BTreeMap<String, String>, _: Option<&[u8]>)
            -> Result<RestResult, DbError> {
            self.next("rest.invoke").map(|_| RestResult { status: 200, headers: BTreeMap::new(), content: Bytes::new() })
        }

        async fn mcp_policy(&self, _: &RequestContext, _: &Value, _: Option<&str>, _: &McpHeaders) -> Result<Policy, DbError> {
            self.next("mcp.policy").map(|_| Policy::default())
        }

        async fn mcp_request(&self, _: &RequestContext, _: &Policy, _: &Value, _: Option<&Value>)
            -> Result<Option<Value>, DbError> {
            self.next("mcp.request").map(|_| None)
        }
    }

    fn lost() -> Result<(), DbError> {
        Err(DbError::ConnectionLost("broken pipe".into()))
    }

    fn conflict() -> Result<(), DbError> {
        Err(DbError::Server { sqlstate: SqlState::T_R_SERIALIZATION_FAILURE, source: "conflict".into() })
    }

    fn ctx() -> RequestContext {
        RequestContext::new(Protocol::Rest, "GET", "/x", Instant::now() + Duration::from_secs(30))
    }

    fn read_only() -> Policy {
        Policy { read_only: true, ..Policy::default() }
    }

    #[tokio::test]
    async fn a_lost_connection_retries_the_policy_lookups_and_a_read_only_dispatch() {
        let fake = Arc::new(Scripted::new(vec![lost(), Ok(()), lost(), Ok(()), lost()]));
        let channel = ConnectionRetry::new(fake.clone());
        channel.rest_policy(&ctx(), None).await.unwrap();
        channel.mcp_policy(&ctx(), &Value::Null, None, &Default::default()).await.unwrap();
        channel.rest_invoke(&ctx(), &read_only(), &BTreeMap::new(), None).await.unwrap();
        assert_eq!(fake.calls(), ["rest.policy", "rest.policy", "mcp.policy", "mcp.policy", "rest.invoke", "rest.invoke"]);
    }

    #[tokio::test]
    async fn a_write_is_never_sent_twice() {
        let fake = Arc::new(Scripted::new(vec![lost(), lost()]));
        let channel = ConnectionRetry::new(fake.clone());
        assert!(matches!(channel.rest_invoke(&ctx(), &Policy::default(), &BTreeMap::new(), None).await,
                         Err(DbError::ConnectionLost(_))));
        assert!(matches!(channel.mcp_request(&ctx(), &read_only(), &Value::Null, None).await,
                         Err(DbError::ConnectionLost(_))));
        assert_eq!(fake.calls(), ["rest.invoke", "mcp.request"]);
    }

    #[tokio::test]
    async fn an_answer_from_the_server_and_a_pool_timeout_are_not_a_lost_connection() {
        let fake = Arc::new(Scripted::new(vec![conflict(), Err(DbError::NoConnection)]));
        let channel = ConnectionRetry::new(fake.clone());
        assert!(channel.rest_policy(&ctx(), None).await.is_err());
        assert!(matches!(channel.rest_policy(&ctx(), None).await, Err(DbError::NoConnection)));
        assert_eq!(fake.calls(), ["rest.policy", "rest.policy"]);
    }

    #[tokio::test]
    async fn serialization_failures_rerun_the_whole_transaction_then_give_up() {
        let fake = Arc::new(Scripted::new(vec![conflict(), Ok(()), conflict(), conflict(), conflict()]));
        let channel = SerializationRetry::new(fake.clone(), 3, Duration::from_millis(1));
        channel.rest_invoke(&ctx(), &Policy::default(), &BTreeMap::new(), None).await.unwrap();
        let exhausted = channel.mcp_request(&ctx(), &Policy::default(), &Value::Null, None).await;
        assert!(matches!(exhausted, Err(DbError::RetriesExhausted { attempts: 3, .. })), "{exhausted:?}");
        assert_eq!(fake.calls(), ["rest.invoke", "rest.invoke", "mcp.request", "mcp.request", "mcp.request"]);
    }

    #[test]
    fn the_statement_timeout_is_what_is_left_and_never_zero() {
        assert_eq!(statement_timeout(Duration::from_millis(1999)).as_deref(), Some("1999"));
        assert_eq!(statement_timeout(Duration::from_secs_f64(2.5)).as_deref(), Some("2500"));
        assert_eq!(statement_timeout(Duration::from_micros(1999_500)).as_deref(), Some("2000"));
        assert_eq!(statement_timeout(Duration::from_micros(999)).as_deref(), Some("1"));
        assert_eq!(statement_timeout(Duration::ZERO), None);
    }

    #[tokio::test]
    async fn a_deadline_exceeded_is_not_retried() {
        let past = || Err(DbError::DeadlineExceeded { request_id: "r".into() });
        let fake = Arc::new(Scripted::new(vec![past(), past()]));
        let channel = ConnectionRetry::new(SerializationRetry::new(fake.clone(), 3, Duration::from_millis(1)));
        assert!(matches!(channel.rest_invoke(&ctx(), &read_only(), &BTreeMap::new(), None).await,
                         Err(DbError::DeadlineExceeded { .. })));
        assert!(matches!(channel.rest_policy(&ctx(), None).await, Err(DbError::DeadlineExceeded { .. })));
        assert_eq!(fake.calls(), ["rest.invoke", "rest.policy"]);
    }

    /// Needs DATABASE_URL; any login will do.
    #[tokio::test]
    async fn a_dropped_call_cancels_its_statement_and_leaves_the_pool() {
        let Ok(database_url) = std::env::var("DATABASE_URL") else {
            eprintln!("skipped: DATABASE_URL is not set");
            return;
        };
        let (pool, tls) = crate::db::create_pool(&database_url, 4).unwrap();
        // Its own pool: a connection the cancel missed would queue the count
        // behind the sleep.
        let (observer, _) = crate::db::create_pool(&database_url, 1).unwrap();
        let sleeping = || async {
            let client = observer.get().await.unwrap();
            let row = client
                .query_one("SELECT count(*) FROM pg_stat_activity WHERE usename = current_user \
                            AND application_name = $1 AND wait_event = 'PgSleep'", &[&crate::db::APPLICATION_NAME])
                .await
                .unwrap();
            row.get::<_, i64>(0)
        };
        let channel = Postgres::new(pool.clone(), tls);
        let ctx = ctx();
        // Dropped once the sleep runs: a cancel that arrives before it is lost.
        tokio::select! {
            _ = channel.run(&ctx, None, "SELECT pg_sleep(30)::text", &[]) => panic!("the sleep ended early"),
            () = async { while sleeping().await == 0 { tokio::time::sleep(Duration::from_millis(20)).await } } => {}
        }
        let deadline = Instant::now() + Duration::from_secs(3);
        while sleeping().await > 0 && Instant::now() < deadline {
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        assert_eq!(sleeping().await, 0, "the statement still runs after its call was dropped");
        assert_eq!(pool.status().size, 0, "the cancelled connection went back to the pool");
    }

    #[tokio::test]
    async fn the_decorators_compose() {
        let fake = Arc::new(Scripted::new(vec![lost(), conflict(), Ok(())]));
        let channel: Arc<dyn Channel> = Arc::new(ConnectionRetry::new(SerializationRetry::new(
            Tracing(fake.clone()), 3, Duration::from_millis(1))));
        channel.rest_invoke(&ctx(), &read_only(), &BTreeMap::new(), None).await.unwrap();
        assert_eq!(fake.calls(), ["rest.invoke", "rest.invoke", "rest.invoke"]);
    }
}
