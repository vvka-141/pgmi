mod tools;
pub mod wire;

#[cfg(test)]
mod transport_tests;

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde_json::value::RawValue;
use serde_json::{Value, json};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::runtime::Runtime;
use tokio::sync::mpsc::{Sender, channel};
use tokio::task::JoinSet;
use tokio_util::sync::CancellationToken;

use crate::VERSION;
use crate::env::Env;
use crate::error::Error;
pub(crate) use tools::{Outcome, Toolbox};
use wire::{Decoded, Field, INVALID_PARAMS, INVALID_REQUEST, METHOD_NOT_FOUND, Request};

const STATELESS: &str = "2026-07-28";
const META_VERSION: &str = "io.modelcontextprotocol/protocolVersion";
const META_CAPABILITIES: &str = "io.modelcontextprotocol/clientCapabilities";
const META_SERVER: &str = "io.modelcontextprotocol/serverInfo";
const UNSUPPORTED_VERSION: i64 = -32022;
const ECHOED: [&str; 3] = ["2025-11-25", "2025-06-18", "2024-11-05"];
const DRAIN: Duration = Duration::from_secs(2);
const MAX_CALLS: usize = 32;
const QUEUED_ANSWERS: usize = 1024;
const BUSY: i64 = -32000;

pub(crate) fn run(runtime: Runtime, verbose: bool, env: Env) -> Result<(), Error> {
    crate::conn::tls::install();
    let stop = CancellationToken::new();
    let ended = runtime.block_on(async {
        let signals = tokio::spawn({
            let stop = stop.clone();
            async move {
                stop_signal().await;
                stop.cancel();
            }
        });
        let ended = serve(
            tokio::io::stdin(),
            tokio::io::stdout(),
            Arc::new(tools::Tools { env, verbose }),
            stop,
        )
        .await;
        signals.abort();
        ended
    });
    runtime.shutdown_timeout(Duration::from_millis(100));
    ended.into_result()
}

/// Why a session ended.
#[derive(Debug)]
pub(crate) enum Ended {
    /// The client closed its input.
    EndOfInput,
    /// `stop` was cancelled (a signal, in `run`).
    Stopped,
    /// Answers waited `DRAIN` for a client that did not read them.
    SlowReader,
    ReadFailed(std::io::Error),
    WriteFailed(std::io::Error),
}

impl Ended {
    /// The command's result: an ended session is a success unless reading
    /// or writing failed, as in Go.
    pub(crate) fn into_result(self) -> Result<(), Error> {
        match self {
            Ended::EndOfInput | Ended::Stopped | Ended::SlowReader => Ok(()),
            Ended::ReadFailed(e) => Err(Error::general("read request").with_source(e)),
            Ended::WriteFailed(e) => Err(Error::general("write response").with_source(e)),
        }
    }
}

enum ReadEnd {
    Eof,
    Failed(std::io::Error),
    Cancelled,
    Overflow,
}

enum Delivery {
    Delivered,
    Failed(std::io::Error),
    TimedOut,
}

struct Server<T> {
    out: Sender<String>,
    calls: Mutex<HashMap<CallId, CancellationToken>>,
    root: CancellationToken,
    tools: Arc<T>,
}

/// Serves MCP over newline-delimited JSON-RPC until `input` ends, `stop` is
/// cancelled or writing to `output` fails. Ending cancels every tool call in
/// flight; a cancelled call is never answered. While 1024 answers wait, the
/// reader waits for the client to take one, for at most `DRAIN`; a client that
/// takes none by then ends the session. Returns once every call has ended and
/// the answers are written, giving a client that stops reading `DRAIN` to take
/// them, with the reason the session ended.
pub(crate) async fn serve<T: Toolbox>(
    mut input: impl AsyncRead + Unpin,
    mut output: impl AsyncWrite + Unpin,
    tools: Arc<T>,
    stop: CancellationToken,
) -> Ended {
    let session = stop.child_token();
    let finished = CancellationToken::new();
    let (tx, mut rx) = channel::<String>(QUEUED_ANSWERS);
    let server = Arc::new(Server {
        out: tx,
        calls: Mutex::new(HashMap::new()),
        root: session.clone(),
        tools,
    });
    let read = {
        let finished = finished.clone();
        async move {
            let mut tasks = JoinSet::new();
            let mut lines = wire::Lines::default();
            let mut buf = vec![0u8; 64 * 1024];
            let end = 'read: loop {
                let n = tokio::select! {
                    biased;
                    () = server.root.cancelled() => break ReadEnd::Cancelled,
                    n = input.read(&mut buf) => match n {
                        Ok(n) => n,
                        Err(e) => break ReadEnd::Failed(e),
                    },
                };
                if n == 0 {
                    if let Some(line) = lines.finish()
                        && let Err(end) = server.deliver(server.handle(&line, &mut tasks)).await
                    {
                        break end;
                    }
                    break ReadEnd::Eof;
                }
                while tasks.try_join_next().is_some() {}
                let read = lines.feed(&buf[..n]);
                for _ in 0..lines.take_oversized() {
                    let refusal = wire::error(
                        None,
                        INVALID_REQUEST,
                        &format!(
                            "invalid request: a message may hold at most {} MiB",
                            wire::MAX_LINE >> 20
                        ),
                    );
                    if let Err(end) = server.deliver(Some(refusal)).await {
                        break 'read end;
                    }
                }
                for line in read {
                    if let Err(end) = server.deliver(server.handle(&line, &mut tasks)).await {
                        break 'read end;
                    }
                }
            };
            if matches!(end, ReadEnd::Overflow) {
                server.root.cancel();
            }
            while tasks.join_next().await.is_some() {}
            drop(server);
            finished.cancel();
            end
        }
    };
    let write = async move {
        let deliver = async {
            while let Some(mut line) = rx.recv().await {
                line.push('\n');
                if let Err(e) = output.write_all(line.as_bytes()).await {
                    return Delivery::Failed(e);
                }
                if let Err(e) = output.flush().await {
                    return Delivery::Failed(e);
                }
            }
            Delivery::Delivered
        };
        tokio::pin!(deliver);
        let delivery = tokio::select! {
            d = &mut deliver => d,
            () = finished.cancelled() => tokio::time::timeout(DRAIN, &mut deliver)
                .await
                .unwrap_or(Delivery::TimedOut),
        };
        if !matches!(delivery, Delivery::Delivered) {
            session.cancel();
        }
        delivery
    };
    match tokio::join!(read, write) {
        (_, Delivery::Failed(e)) => Ended::WriteFailed(e),
        (ReadEnd::Overflow, _) | (_, Delivery::TimedOut) => Ended::SlowReader,
        (ReadEnd::Failed(e), _) => Ended::ReadFailed(e),
        (ReadEnd::Cancelled, _) => Ended::Stopped,
        (ReadEnd::Eof, Delivery::Delivered) => Ended::EndOfInput,
    }
}

async fn stop_signal() {
    #[cfg(unix)]
    {
        use tokio::signal::unix::{SignalKind, signal};
        let (Ok(mut int), Ok(mut term)) = (
            signal(SignalKind::interrupt()),
            signal(SignalKind::terminate()),
        ) else {
            return std::future::pending().await;
        };
        tokio::select! {
            _ = int.recv() => {}
            _ = term.recv() => {}
        }
    }
    #[cfg(not(unix))]
    {
        if tokio::signal::ctrl_c().await.is_err() {
            std::future::pending::<()>().await;
        }
    }
}

/// The protocol generation a request speaks: the session-based one
/// (`initialize` first) or the stateless one that names its version in
/// `_meta`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Era {
    Session,
    Stateless,
}

#[derive(Debug, Clone, PartialEq, Eq, Hash)]
enum CallId {
    Text(String),
    Number(String),
}

impl CallId {
    fn of(raw: &str) -> Option<Self> {
        let raw = raw.trim();
        match raw.as_bytes().first()? {
            b'"' => serde_json::from_str(raw).ok().map(CallId::Text),
            b'-' | b'0'..=b'9' => Some(CallId::Number(raw.to_string())),
            _ => None,
        }
    }
}

impl<T: Toolbox> Server<T> {
    /// Queues the reader's answer, waiting up to `DRAIN` for room while the
    /// client takes earlier answers.
    async fn deliver(&self, answer: Option<String>) -> Result<(), ReadEnd> {
        let Some(answer) = answer else {
            return Ok(());
        };
        let room = tokio::select! {
            biased;
            () = self.root.cancelled() => return Err(ReadEnd::Cancelled),
            room = tokio::time::timeout(DRAIN, self.out.reserve()) => room,
        };
        match room {
            Ok(Ok(permit)) => {
                permit.send(answer);
                Ok(())
            }
            Ok(Err(_)) => Err(ReadEnd::Cancelled),
            Err(_) => Err(ReadEnd::Overflow),
        }
    }

    /// Handles one line and returns its immediate answer, if any; a tool
    /// call is answered later by its own task.
    fn handle(self: &Arc<Self>, line: &[u8], tasks: &mut JoinSet<()>) -> Option<String> {
        match wire::decode(line) {
            Decoded::Skip => None,
            Decoded::Reply(r) => Some(r),
            Decoded::Notification(n) => {
                if n.method == "notifications/cancelled" {
                    self.cancel(n.params.as_deref());
                }
                None
            }
            Decoded::Request(r) if r.method.starts_with("notifications/") => {
                if r.method == "notifications/cancelled" {
                    self.cancel(r.params.as_deref());
                }
                None
            }
            Decoded::Request(r) => self.request(&r, tasks),
        }
    }

    fn cancel(&self, params: Option<&RawValue>) {
        let Some(id) = params
            .and_then(|p| serde_json::from_str::<wire::Members>(p.get()).ok())
            .and_then(|m| {
                m.0.into_iter()
                    .rev()
                    .find(|(k, _)| k == "requestId")
                    .and_then(|(_, v)| CallId::of(v.get()))
            })
        else {
            return;
        };
        if let Ok(calls) = self.calls.lock()
            && let Some(token) = calls.get(&id)
        {
            token.cancel();
        }
    }

    fn request(self: &Arc<Self>, r: &Request, tasks: &mut JoinSet<()>) -> Option<String> {
        let id = r.id.clone()?;
        let params: Option<Value> = r
            .params
            .as_ref()
            .and_then(|p| serde_json::from_str(p.get()).ok());
        let meta = params.as_ref().and_then(|p| p.get("_meta"));
        let version = meta.and_then(|m| m.get(META_VERSION));
        let era = if version.is_some() || r.method == "server/discover" {
            Era::Stateless
        } else {
            Era::Session
        };
        if let Some(v) = version {
            let requested = v.as_str().unwrap_or_default();
            if requested != STATELESS {
                return Some(wire::error_with(
                    Some(&id),
                    UNSUPPORTED_VERSION,
                    "Unsupported protocol version",
                    Some(&json!({"requested": requested, "supported": [STATELESS]})),
                ));
            }
            if r.method != "server/discover"
                && meta.and_then(|m| m.get(META_CAPABILITIES)).is_none()
            {
                return Some(wire::error(
                    Some(&id),
                    INVALID_PARAMS,
                    &format!("missing required _meta field: {META_CAPABILITIES}"),
                ));
            }
        }
        let not_found = || {
            wire::error(
                Some(&id),
                METHOD_NOT_FOUND,
                &format!("method not found: {}", r.method),
            )
        };
        match (r.method.as_str(), era) {
            ("initialize", Era::Session) => Some(initialize(&id, r.params.as_deref())),
            ("ping", Era::Session) => Some(wire::result(&id, "{}")),
            ("tools/list", _) => Some(tools_list(&id, self.tools.list(), era)),
            ("server/discover", _) => Some(wire::result(
                &id,
                &stateless(json!({
                    "cacheScope": "public",
                    "capabilities": {"tools": {}},
                    "supportedVersions": [STATELESS],
                    "ttlMs": 3_600_000,
                })),
            )),
            ("tools/call", _) => self.call(id, r.params.as_deref(), era, tasks),
            _ => Some(not_found()),
        }
    }

    fn call(
        self: &Arc<Self>,
        id: String,
        params: Option<&RawValue>,
        era: Era,
        tasks: &mut JoinSet<()>,
    ) -> Option<String> {
        let invalid = |m: &str| {
            wire::error(
                Some(&id),
                INVALID_PARAMS,
                &format!("invalid tools/call params: {m}"),
            )
        };
        let Some(params) = params else {
            return Some(invalid("unexpected end of JSON input"));
        };
        let fields = match wire::go_struct(
            params,
            "mcp.callToolParams",
            "callToolParams",
            &[("name", Field::String), ("arguments", Field::Raw)],
        ) {
            Ok(f) => f,
            Err(e) => return Some(invalid(&e.to_string())),
        };
        let name = fields.string("name").unwrap_or_default();
        let Some(tool) = self.tools.tool(&name) else {
            return Some(wire::error(
                Some(&id),
                INVALID_PARAMS,
                &format!("unknown tool: {name}"),
            ));
        };
        let arguments = fields.raw("arguments").map(RawValue::to_owned);
        let key = CallId::of(&id)?;
        let token = self.root.child_token();
        let mut calls = self.calls.lock().ok()?;
        if calls.contains_key(&key) {
            return Some(wire::error(
                Some(&id),
                INVALID_REQUEST,
                &format!("request id {id} is already in flight"),
            ));
        }
        if tasks.len() >= MAX_CALLS {
            return Some(wire::error(
                Some(&id),
                BUSY,
                &format!("{MAX_CALLS} tool calls are already running; wait for one to finish"),
            ));
        }
        calls.insert(key.clone(), token.clone());
        drop(calls);
        let server = self.clone();
        tasks.spawn(async move {
            let tools = server.tools.clone();
            let task_token = token.clone();
            let outcome =
                match crate::error::catching_async(tools.call(tool, arguments, task_token)).await {
                    Ok(o) => o,
                    Err(p) => Outcome::panicked(&p),
                };
            if let Ok(mut calls) = server.calls.lock() {
                calls.remove(&key);
            }
            if token.is_cancelled() {
                return;
            }
            let answer = wire::result(&id, &outcome.render(era));
            tokio::select! {
                _ = server.out.send(answer) => {}
                () = server.root.cancelled() => {}
            }
        });
        None
    }
}

fn initialize(id: &str, params: Option<&RawValue>) -> String {
    let fields = match params.map(|p| {
        wire::go_struct(
            p,
            "mcp.initializeParams",
            "initializeParams",
            &[("protocolVersion", Field::String)],
        )
    }) {
        Some(Err(e)) => {
            return wire::error(
                Some(id),
                INVALID_PARAMS,
                &format!("invalid initialize params: {e}"),
            );
        }
        Some(Ok(f)) => f.string("protocolVersion").unwrap_or_default(),
        None => String::new(),
    };
    if fields.is_empty() {
        return wire::error(
            Some(id),
            INVALID_PARAMS,
            "missing required parameter: protocolVersion",
        );
    }
    let version = if ECHOED.contains(&fields.as_str()) {
        fields.as_str()
    } else {
        ECHOED[0]
    };
    let result = format!(
        "{{\"protocolVersion\":{},\"capabilities\":{{\"tools\":{{}}}},\"serverInfo\":{}}}",
        json!(version),
        server_info()
    );
    wire::result(id, &result)
}

fn server_info() -> Value {
    json!({"name": "pgmi", "version": VERSION})
}

const TOOLS: &str = include_str!("tools.json");

fn tools_list(id: &str, list: &str, era: Era) -> String {
    if era == Era::Session {
        return wire::result(id, &format!("{{\"tools\":{}}}", list.trim_end()));
    }
    let tools: Value = serde_json::from_str(list).unwrap_or_default();
    wire::result(
        id,
        &stateless(json!({"cacheScope": "public", "tools": tools, "ttlMs": 3_600_000})),
    )
}

fn stateless(mut v: Value) -> String {
    if let Value::Object(m) = &mut v {
        m.insert("_meta".into(), json!({ META_SERVER: server_info() }));
        m.insert("resultType".into(), json!("complete"));
    }
    v.to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tools_json_lists_seven_tools_in_name_order() {
        let tools: Vec<Value> = serde_json::from_str(TOOLS).unwrap();
        let names: Vec<&str> = tools.iter().filter_map(|t| t["name"].as_str()).collect();
        assert_eq!(
            names,
            [
                "ai_contract",
                "ai_overview",
                "deploy",
                "init",
                "metadata_plan",
                "metadata_validate",
                "templates_list"
            ]
        );
        for name in names {
            assert!(tools::Tool::named(name).is_some(), "{name}");
        }
    }

    #[tokio::test]
    async fn a_request_id_already_in_flight_is_refused() {
        let (tx, _rx) = channel::<String>(1);
        let server = Arc::new(Server {
            out: tx,
            calls: Mutex::new(HashMap::new()),
            root: CancellationToken::new(),
            tools: Arc::new(tools::Tools {
                env: Env::default(),
                verbose: false,
            }),
        });
        let busy = CancellationToken::new();
        server
            .calls
            .lock()
            .unwrap()
            .insert(CallId::Number("7".into()), busy.clone());
        let mut tasks = JoinSet::new();
        let answer = server
            .handle(
                br#"{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"templates_list","arguments":{}}}"#,
                &mut tasks,
            )
            .unwrap();
        assert!(answer.contains("-32600"), "{answer}");
        assert!(answer.contains("already in flight"), "{answer}");
        assert!(tasks.is_empty());
        assert!(!busy.is_cancelled());
        assert!(
            server
                .calls
                .lock()
                .unwrap()
                .contains_key(&CallId::Number("7".into()))
        );
    }

    #[test]
    fn initialize_echoes_known_versions() {
        let raw = RawValue::from_string("{\"protocolVersion\":\"2024-11-05\"}".into()).unwrap();
        assert!(initialize("1", Some(&raw)).contains("\"protocolVersion\":\"2024-11-05\""));
        let raw = RawValue::from_string("{\"protocolVersion\":\"2025-03-26\"}".into()).unwrap();
        assert!(initialize("1", Some(&raw)).contains("\"protocolVersion\":\"2025-11-25\""));
        assert!(initialize("1", None).contains("missing required parameter: protocolVersion"));
    }
}
