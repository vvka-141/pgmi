// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own that fits this project. The rules it keeps are in
// ../../PROTOCOL.md ("Extension seams").
//
// Request and response interceptors: an ordered pipeline around each dispatch.
// A request interceptor runs after the policy call and before the transaction
// opens; it may change the method, URL, headers and body. A response
// interceptor runs after the commit; it may change the status, headers and
// body. Uses: embedding vectors for text fields, enrichment from another
// service, signing storage URLs the handler returned, redaction.
//
// The rules:
//
// - The database still decides. An interceptor never decides access or
//   validity; it selects itself from what the policy call returned (the
//   handler, the x-* annotations on its inputSchema), never from a path list.
// - Zero cost when unused. No interceptor applies: the body passes as bytes,
//   never parsed. Some need JSON: it is parsed once and serialized once.
// - Failure policy per interceptor. Fail (the default) answers 502
//   pgmi.interceptor_failed; Degrade logs and goes on with the request as it
//   was before that interceptor. Overrunning the timeout is a failure.
// - No database connection is held while an interceptor runs: the policy
//   call has returned its connection, the dispatch has not taken one yet.
// - Thread safety. The pipeline is built at startup and only read afterwards
//   (Arc<dyn ... + Send + Sync>); interceptors take &self and keep no
//   per-request state, which travels in the message and the context. An
//   outside service is called through one long-lived client created at
//   startup and handed to the constructor: reqwest::Client is pooled and
//   cheap to clone.
//
// This file also holds the two examples INTERCEPTORS can enable:
// EmbedFields with a FakeEmbedder, and RequestIdHeader.

use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use axum::body::Bytes;
use serde_json::Value;
use sha2::{Digest, Sha256};
use tokio::time::Instant;

use crate::BoxError;
use crate::channel::{Protocol, RequestContext};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Failure {
    /// 502 pgmi.interceptor_failed; a request interceptor's failure means
    /// nothing was dispatched.
    Fail,
    /// Logged, and the request goes on as it was before this interceptor.
    Degrade,
}

/// A body readable as the bytes the client sent or as JSON parsed once.
#[derive(Clone, Debug, Default)]
pub struct Body {
    bytes: Bytes,
    parsed: Parsed,
}

#[derive(Clone, Debug, Default)]
enum Parsed {
    #[default]
    No,
    Json(Value),
    NotJson,
}

impl Body {
    pub fn bytes(bytes: Bytes) -> Self {
        Body { bytes, parsed: Parsed::No }
    }

    /// A body the gateway already parsed (an MCP request, an MCP envelope).
    pub fn json_value(value: Value) -> Self {
        Body { bytes: Bytes::new(), parsed: Parsed::Json(value) }
    }

    /// The parsed JSON, parsed on the first call and shared by every
    /// interceptor after it. None for a body that is not JSON: it passes on
    /// as the bytes it was, and the database answers it.
    pub fn json(&mut self) -> Option<&mut Value> {
        if let Parsed::No = self.parsed {
            self.parsed = serde_json::from_slice(&self.bytes).map_or(Parsed::NotJson, Parsed::Json);
        }
        match &mut self.parsed {
            Parsed::Json(value) => Some(value),
            _ => None,
        }
    }

    /// Replaces the body with raw bytes; the JSON view starts over.
    pub fn set_bytes(&mut self, bytes: Bytes) {
        *self = Body::bytes(bytes);
    }

    /// The bytes to send on: serialized once if anyone parsed it, untouched otherwise.
    pub fn into_bytes(self) -> Bytes {
        match self.parsed {
            Parsed::Json(value) => serde_json::to_vec(&value).expect("a Value serializes").into(),
            _ => self.bytes,
        }
    }

    pub fn into_json(self) -> Option<Value> {
        match self.parsed {
            Parsed::Json(value) => Some(value),
            _ => serde_json::from_slice(&self.bytes).ok(),
        }
    }
}

/// On /mcp only the body reaches the database; method and URL are /mcp's.
#[derive(Clone, Debug)]
pub struct Request {
    pub method: String,
    pub url: String,
    /// Lower-case names, each once.
    pub headers: BTreeMap<String, String>,
    pub body: Body,
}

#[derive(Clone, Debug)]
pub struct Response {
    pub status: u16,
    pub headers: BTreeMap<String, String>,
    pub body: Body,
}

/// What every interceptor declares about itself.
pub trait Interceptor: Send + Sync {
    /// Shows in the log.
    fn name(&self) -> &'static str;

    /// Whether it reads the body as JSON. One that only touches headers
    /// leaves the body unparsed.
    fn needs_json(&self) -> bool {
        false
    }

    fn failure(&self) -> Failure {
        Failure::Fail
    }

    fn timeout(&self) -> Duration {
        Duration::from_secs(2)
    }

    /// Selects it from the context, which holds what the database published
    /// for the route.
    fn applies(&self, _ctx: &RequestContext) -> bool {
        true
    }
}

#[async_trait]
pub trait RequestInterceptor: Interceptor {
    async fn on_request(&self, request: &mut Request, ctx: &RequestContext) -> Result<(), BoxError>;
}

#[async_trait]
pub trait ResponseInterceptor: Interceptor {
    async fn on_response(&self, response: &mut Response, ctx: &RequestContext) -> Result<(), BoxError>;
}

/// An interceptor failed under the Fail policy.
#[derive(Debug)]
pub struct InterceptorFailed;

#[derive(Default)]
pub struct Pipeline {
    requests: Vec<Arc<dyn RequestInterceptor>>,
    responses: Vec<Arc<dyn ResponseInterceptor>>,
}

/// The interceptors that apply to one request; empty means zero cost.
pub type Selected<'a, I> = Vec<&'a I>;

impl Pipeline {
    pub fn new(requests: Vec<Arc<dyn RequestInterceptor>>, responses: Vec<Arc<dyn ResponseInterceptor>>) -> Self {
        Pipeline { requests, responses }
    }

    pub fn requests_for(&self, ctx: &RequestContext) -> Selected<'_, dyn RequestInterceptor> {
        self.requests.iter().map(Arc::as_ref).filter(|i| i.applies(ctx)).collect()
    }

    pub fn responses_for(&self, ctx: &RequestContext) -> Selected<'_, dyn ResponseInterceptor> {
        self.responses.iter().map(Arc::as_ref).filter(|i| i.applies(ctx)).collect()
    }

    pub async fn run_requests(&self, selected: &[&dyn RequestInterceptor], request: &mut Request,
                              ctx: &RequestContext) -> Result<(), InterceptorFailed> {
        for interceptor in selected {
            // Degrade must be able to go on "as before": it works on a copy,
            // kept only when it succeeds. Fail needs no copy: the request ends.
            let mut copy = (interceptor.failure() == Failure::Degrade).then(|| request.clone());
            let work = copy.as_mut().unwrap_or(&mut *request);
            if interceptor.needs_json() {
                work.body.json();
            }
            let (bounded, deadline) = bounded(ctx, interceptor.timeout());
            let outcome = tokio::time::timeout_at(deadline, interceptor.on_request(work, &bounded)).await;
            if settle(*interceptor, ctx, outcome)?
                && let Some(done) = copy
            {
                *request = done;
            }
        }
        Ok(())
    }

    pub async fn run_responses(&self, selected: &[&dyn ResponseInterceptor], response: &mut Response,
                               ctx: &RequestContext) -> Result<(), InterceptorFailed> {
        for interceptor in selected {
            let mut copy = (interceptor.failure() == Failure::Degrade).then(|| response.clone());
            let work = copy.as_mut().unwrap_or(&mut *response);
            if interceptor.needs_json() {
                work.body.json();
            }
            let (bounded, deadline) = bounded(ctx, interceptor.timeout());
            let outcome = tokio::time::timeout_at(deadline, interceptor.on_response(work, &bounded)).await;
            if settle(*interceptor, ctx, outcome)?
                && let Some(done) = copy
            {
                *response = done;
            }
        }
        Ok(())
    }
}

/// The interceptor's own timeout, never past the request's deadline. The
/// interceptor sees the narrower deadline in its context and passes what is
/// left of it to any outgoing call.
fn bounded(ctx: &RequestContext, timeout: Duration) -> (RequestContext, Instant) {
    let deadline = ctx.deadline.min(Instant::now() + timeout);
    (RequestContext { deadline, ..ctx.clone() }, deadline)
}

/// Ok(true): keep its changes. Ok(false): degraded, drop them. Err: fail.
/// A timeout drops the interceptor's future, which cancels whatever it was
/// waiting on: nothing it does late can touch the request.
fn settle(interceptor: &(impl Interceptor + ?Sized), ctx: &RequestContext,
          outcome: Result<Result<(), BoxError>, tokio::time::error::Elapsed>) -> Result<bool, InterceptorFailed> {
    let reason = match outcome {
        Ok(Ok(())) => return Ok(true),
        Ok(Err(error)) => error.to_string(),
        Err(_) => "timed out".to_owned(),
    };
    if interceptor.failure() == Failure::Degrade {
        tracing::warn!(request_id = %ctx.request_id, interceptor = interceptor.name(), %reason, "interceptor degraded");
        return Ok(false);
    }
    tracing::error!(request_id = %ctx.request_id, interceptor = interceptor.name(), %reason, "interceptor failed");
    Err(InterceptorFailed)
}

/// The object the handler's inputSchema describes: the REST body, or an MCP
/// call's params.arguments.
pub fn arguments<'a>(request: &'a mut Request, ctx: &RequestContext) -> Option<&'a mut serde_json::Map<String, Value>> {
    let value = request.body.json()?;
    let value = match ctx.protocol {
        Protocol::Rest => value,
        Protocol::Mcp => value.get_mut("params")?.get_mut("arguments")?,
    };
    value.as_object_mut()
}

// -- Example: a computed field for every property the schema marks x-embed --

/// The outside call behind a trait: a real one posts to an embedding API
/// through one shared reqwest::Client (see README.md); tests use the fake.
#[async_trait]
pub trait Embedder: Send + Sync {
    async fn embed(&self, texts: Vec<String>) -> Result<Vec<Vec<f64>>, BoxError>;
}

/// Deterministic and offline, the same in every reference gateway: the first
/// four bytes of the SHA-256 of the UTF-8 text, each divided by 255 and
/// rounded to four places. Like a real model with a token limit, it refuses
/// a text over MAX_CHARS characters.
pub struct FakeEmbedder;

impl FakeEmbedder {
    pub const DIMENSIONS: usize = 4;
    pub const MAX_CHARS: usize = 2000;

    pub fn vector(text: &str) -> Vec<f64> {
        Sha256::digest(text.as_bytes())[..Self::DIMENSIONS]
            .iter()
            .map(|&b| (f64::from(b) / 255.0 * 10_000.0).round() / 10_000.0)
            .collect()
    }
}

#[async_trait]
impl Embedder for FakeEmbedder {
    async fn embed(&self, texts: Vec<String>) -> Result<Vec<Vec<f64>>, BoxError> {
        texts
            .iter()
            .map(|text| match text.chars().count() {
                n if n > Self::MAX_CHARS => Err(format!("text over {} characters", Self::MAX_CHARS).into()),
                _ => Ok(Self::vector(text)),
            })
            .collect()
    }
}

/// For each inputSchema property marked "x-embed": true, adds a
/// <property>_embedding sibling holding its vector. The schema declares the
/// sibling too, so the database validates it like any other field.
///
/// One batched call for all fields. A sibling already present is left alone:
/// a client that embeds for itself is not embedded twice.
///
/// ADAPT: an embedding outage can degrade (the handler searches without the
/// vector) or fail; choose per project.
pub struct EmbedFields {
    embedder: Arc<dyn Embedder>,
    failure: Failure,
}

impl EmbedFields {
    pub fn new(embedder: Arc<dyn Embedder>, failure: Failure) -> Self {
        EmbedFields { embedder, failure }
    }
}

fn embedded(annotation: &Value) -> bool {
    annotation.get("x-embed") == Some(&Value::Bool(true))
}

impl Interceptor for EmbedFields {
    fn name(&self) -> &'static str {
        "embed-fields"
    }

    fn needs_json(&self) -> bool {
        true
    }

    fn failure(&self) -> Failure {
        self.failure
    }

    fn applies(&self, ctx: &RequestContext) -> bool {
        ctx.annotations.values().any(embedded)
    }
}

#[async_trait]
impl RequestInterceptor for EmbedFields {
    async fn on_request(&self, request: &mut Request, ctx: &RequestContext) -> Result<(), BoxError> {
        let Some(args) = arguments(request, ctx) else { return Ok(()) };
        let fields: Vec<String> = ctx
            .annotations
            .iter()
            .filter(|(name, annotation)| {
                embedded(annotation)
                    && args.get(name.as_str()).and_then(Value::as_str).is_some_and(|text| !text.trim().is_empty())
                    && !args.contains_key(&format!("{name}_embedding"))
            })
            .map(|(name, _)| name.clone())
            .collect();
        if fields.is_empty() {
            return Ok(());
        }
        let texts = fields.iter().map(|name| args[name.as_str()].as_str().unwrap_or_default().to_owned()).collect();
        // The request is borrowed across this await: no other task can see
        // it, and a timeout drops this future before it writes anything.
        let vectors = self.embedder.embed(texts).await?;
        if vectors.len() != fields.len() {
            return Err("the embedder answered a different number of vectors".into());
        }
        for (name, vector) in fields.into_iter().zip(vectors) {
            args.insert(format!("{name}_embedding"), vector.into());
        }
        Ok(())
    }
}

// -- Example: a response header ----------------------------------------------

/// Answers the gateway's request id in X-Request-Id, the id its log lines
/// carry. Headers only: the body is never parsed.
pub struct RequestIdHeader;

impl Interceptor for RequestIdHeader {
    fn name(&self) -> &'static str {
        "request-id"
    }
}

#[async_trait]
impl ResponseInterceptor for RequestIdHeader {
    async fn on_response(&self, response: &mut Response, ctx: &RequestContext) -> Result<(), BoxError> {
        response.headers.insert("x-request-id".into(), ctx.request_id.clone());
        Ok(())
    }
}

/// The example interceptors INTERCEPTORS names, in its order. Unset, none run
/// and every body passes as bytes.
///
/// ADAPT: build your own here, each with its long-lived dependencies: the
/// shared `http` client (clone it, never create one per request), a signing
/// key from configuration.
pub fn build_pipeline(names: &[String], embed_failure: Failure, _http: &reqwest::Client) -> Result<Pipeline, String> {
    let mut pipeline = Pipeline::default();
    for name in names {
        match name.as_str() {
            "embed" => pipeline.requests.push(Arc::new(EmbedFields::new(Arc::new(FakeEmbedder), embed_failure))),
            "request-id" => pipeline.responses.push(Arc::new(RequestIdHeader)),
            other => return Err(format!("unknown interceptor {other:?}; known: embed, request-id")),
        }
    }
    Ok(pipeline)
}

#[cfg(test)]
mod tests {
    use std::sync::atomic::{AtomicUsize, Ordering};

    use serde_json::json;

    use super::*;

    fn ctx(protocol: Protocol, annotations: Value) -> RequestContext {
        let mut ctx = RequestContext::new(protocol, "POST", "/x", Instant::now() + Duration::from_secs(30));
        ctx.annotations = Arc::new(annotations.as_object().cloned().unwrap_or_default());
        ctx
    }

    fn request(body: &[u8]) -> Request {
        Request { method: "POST".into(), url: "/x".into(), headers: BTreeMap::new(), body: Body::bytes(Bytes::copy_from_slice(body)) }
    }

    /// Sets a field, then behaves as told.
    struct Probe {
        failure: Failure,
        outcome: &'static str,
        calls: AtomicUsize,
    }

    impl Probe {
        fn new(failure: Failure, outcome: &'static str) -> Arc<Self> {
            Arc::new(Probe { failure, outcome, calls: AtomicUsize::new(0) })
        }
    }

    impl Interceptor for Probe {
        fn name(&self) -> &'static str {
            "probe"
        }
        fn needs_json(&self) -> bool {
            true
        }
        fn failure(&self) -> Failure {
            self.failure
        }
        fn timeout(&self) -> Duration {
            Duration::from_millis(50)
        }
    }

    #[async_trait]
    impl RequestInterceptor for Probe {
        async fn on_request(&self, request: &mut Request, _: &RequestContext) -> Result<(), BoxError> {
            self.calls.fetch_add(1, Ordering::SeqCst);
            if let Some(body) = request.body.json() {
                body["probe"] = json!(true);
            }
            match self.outcome {
                "error" => Err("refused".into()),
                "hang" => {
                    tokio::time::sleep(Duration::from_secs(60)).await;
                    Ok(())
                }
                _ => Ok(()),
            }
        }
    }

    #[test]
    fn the_fake_embedder_matches_the_protocol() {
        // sha256("hello") = 2cf24dba...: 0x2c, 0xf2, 0x4d, 0xba
        assert_eq!(FakeEmbedder::vector("hello"), vec![0.1725, 0.949, 0.302, 0.7294]);
    }

    #[tokio::test]
    async fn no_interceptor_applies_and_the_bytes_pass_untouched() {
        let pipeline = build_pipeline(&["embed".into()], Failure::Fail, &reqwest::Client::new()).unwrap();
        let ctx = ctx(Protocol::Rest, json!({"text": {"x-other": 1}}));
        assert!(pipeline.requests_for(&ctx).is_empty());
        let odd = br#"{ "text" :"a",  "n": 1.50 }"#;
        assert_eq!(request(odd).body.into_bytes().as_ref(), odd);
    }

    #[tokio::test]
    async fn embed_adds_a_vector_and_keeps_one_the_client_sent() {
        let pipeline = build_pipeline(&["embed".into()], Failure::Fail, &reqwest::Client::new()).unwrap();
        let ctx = ctx(Protocol::Rest, json!({"text": {"x-embed": true}, "other": {"x-embed": true}}));
        let selected = pipeline.requests_for(&ctx);
        let mut sent = request(br#"{"text": "hello", "other": "a", "other_embedding": [1]}"#);
        pipeline.run_requests(&selected, &mut sent, &ctx).await.unwrap();
        let body = sent.body.into_json().unwrap();
        assert_eq!(body["text_embedding"], json!(FakeEmbedder::vector("hello")));
        assert_eq!(body["other_embedding"], json!([1]));
    }

    #[tokio::test]
    async fn embed_reaches_mcp_arguments() {
        let pipeline = build_pipeline(&["embed".into()], Failure::Fail, &reqwest::Client::new()).unwrap();
        let ctx = ctx(Protocol::Mcp, json!({"text": {"x-embed": true}}));
        let mut sent = Request { method: "POST".into(), url: "/mcp".into(), headers: BTreeMap::new(),
                                 body: Body::json_value(json!({"params": {"arguments": {"text": "hello"}}})) };
        pipeline.run_requests(&pipeline.requests_for(&ctx), &mut sent, &ctx).await.unwrap();
        assert_eq!(sent.body.into_json().unwrap()["params"]["arguments"]["text_embedding"],
                   json!(FakeEmbedder::vector("hello")));
    }

    #[tokio::test]
    async fn fail_stops_and_degrade_goes_on_as_it_was() {
        let ctx = ctx(Protocol::Rest, json!({}));
        for (outcome, failure) in [("error", Failure::Fail), ("hang", Failure::Fail)] {
            let probe = Probe::new(failure, outcome);
            let pipeline = Pipeline::new(vec![probe.clone()], vec![]);
            assert!(pipeline.run_requests(&pipeline.requests_for(&ctx), &mut request(b"{}"), &ctx).await.is_err(), "{outcome}");
        }
        for outcome in ["error", "hang"] {
            let pipeline = Pipeline::new(vec![Probe::new(Failure::Degrade, outcome)], vec![]);
            let mut sent = request(br#"{"a": 1}"#);
            pipeline.run_requests(&pipeline.requests_for(&ctx), &mut sent, &ctx).await.unwrap();
            assert_eq!(sent.body.into_bytes().as_ref(), br#"{"a": 1}"#, "{outcome}: degraded changes must be dropped");
        }
    }

    #[tokio::test]
    async fn json_is_parsed_once_for_all_and_a_body_that_is_not_json_passes_as_it_was() {
        let first = Probe::new(Failure::Fail, "ok");
        let second = Probe::new(Failure::Fail, "ok");
        let pipeline = Pipeline::new(vec![first.clone(), second.clone()], vec![]);
        let ctx = ctx(Protocol::Rest, json!({}));
        let mut sent = request(br#"{"a": 1}"#);
        pipeline.run_requests(&pipeline.requests_for(&ctx), &mut sent, &ctx).await.unwrap();
        assert_eq!(sent.body.into_json().unwrap(), json!({"a": 1, "probe": true}));
        assert_eq!(first.calls.load(Ordering::SeqCst) + second.calls.load(Ordering::SeqCst), 2);

        let mut not_json = request(b"not json");
        pipeline.run_requests(&pipeline.requests_for(&ctx), &mut not_json, &ctx).await.unwrap();
        assert_eq!(not_json.body.into_bytes().as_ref(), b"not json");
    }

    #[tokio::test]
    async fn the_request_deadline_bounds_every_interceptor() {
        let probe = Probe::new(Failure::Fail, "hang");
        let pipeline = Pipeline::new(vec![probe], vec![]);
        let mut ctx = ctx(Protocol::Rest, json!({}));
        ctx.deadline = Instant::now();
        let started = Instant::now();
        assert!(pipeline.run_requests(&pipeline.requests_for(&ctx), &mut request(b"{}"), &ctx).await.is_err());
        assert!(started.elapsed() < Duration::from_millis(40));
    }

    #[tokio::test]
    async fn the_fake_refuses_a_text_over_its_limit() {
        assert!(FakeEmbedder.embed(vec!["x".repeat(FakeEmbedder::MAX_CHARS + 1)]).await.is_err());
        assert!(FakeEmbedder.embed(vec!["é".repeat(FakeEmbedder::MAX_CHARS)]).await.is_ok());
    }
}
