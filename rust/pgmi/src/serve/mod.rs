mod tools;
pub mod wire;

#[cfg(test)]
mod transport_tests;

use std::collections::HashMap;
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::Duration;

use serde_json::value::RawValue;
use serde_json::{Value, json};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::runtime::Runtime;
use tokio::sync::Semaphore;
use tokio::sync::mpsc::{Receiver, Sender, channel};
use tokio::sync::watch;
use tokio::task::JoinSet;
use tokio_util::sync::CancellationToken;

use crate::VERSION;
use crate::env::Env;
use crate::error::Error;
pub(crate) use tools::{Outcome, Slot, Toolbox};
use wire::{Decoded, Field, INVALID_PARAMS, INVALID_REQUEST, METHOD_NOT_FOUND, Request};

const STATELESS: &str = "2026-07-28";
const META_VERSION: &str = "io.modelcontextprotocol/protocolVersion";
const META_CAPABILITIES: &str = "io.modelcontextprotocol/clientCapabilities";
const META_SERVER: &str = "io.modelcontextprotocol/serverInfo";
const UNSUPPORTED_VERSION: i64 = -32022;
const ECHOED: [&str; 3] = ["2025-11-25", "2025-06-18", "2024-11-05"];
const DRAIN: Duration = Duration::from_secs(2);
const SLICE: usize = 4 * 1024;
const MAX_CALLS: usize = 32;
const QUEUED_ANSWERS: usize = 1024;
const BUSY: i64 = -32000;

pub(crate) fn run(runtime: Runtime, verbose: bool, env: Env) -> Result<(), Error> {
    crate::conn::tls::install();
    let stop = CancellationToken::new();
    let ended = runtime.block_on(async {
        let signals = tokio::spawn(on_signals(stop.clone()));
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
    /// The client took no answer for `DRAIN` while answers waited.
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
}

/// Why an answer was not queued.
enum Unqueued {
    /// The session ended first.
    Ended,
    /// The client took nothing for `DRAIN`; the session is being ended.
    SlowReader,
}

/// What a request gets.
enum Reply {
    Now(String),
    /// A tool call: its task answers.
    Later,
    /// A notification or a skipped line.
    Never,
}

enum Delivery {
    Delivered,
    Failed(std::io::Error),
    TimedOut,
}

struct Server<T> {
    out: Sender<String>,
    taken: watch::Receiver<u64>,
    calls: Mutex<HashMap<CallId, CancellationToken>>,
    slots: Arc<Semaphore>,
    root: CancellationToken,
    dropped: CancellationToken,
    tools: Arc<T>,
}

/// The end of the client's input, as the writer sees it: until then it
/// waits on the client as long as the client takes; after it, the client
/// must take every slice within `DRAIN`.
#[derive(Clone, Default)]
struct InputEnd(CancellationToken);

impl InputEnd {
    fn reached(&self) {
        self.0.cancel();
    }

    /// Runs one write step; `None` once input has ended and the step did not
    /// finish within `DRAIN`.
    async fn progress<F: Future>(&self, step: F) -> Option<F::Output> {
        tokio::pin!(step);
        tokio::select! {
            biased;
            done = &mut step => return Some(done),
            () = self.0.cancelled() => {}
        }
        tokio::time::timeout(DRAIN, step).await.ok()
    }
}

/// Serves MCP over newline-delimited JSON-RPC until `input` ends, `stop` is
/// cancelled or writing to `output` fails. Ending cancels every tool call in
/// flight; a cancelled call is never answered. Answers are written in
/// flushed slices of `SLICE` bytes, so each slice the client takes is
/// progress. An answer that finds `QUEUED_ANSWERS` waiting waits for room
/// while the client takes slices; a client that takes none for `DRAIN`
/// then, or at any slice once input has ended, is dropped. A tool call
/// keeps its place among `MAX_CALLS` until its answer is queued. Returns
/// once every call has ended and the answers are written, with the reason
/// the session ended.
pub(crate) async fn serve<T: Toolbox>(
    input: impl AsyncRead + Unpin,
    output: impl AsyncWrite + Unpin,
    tools: Arc<T>,
    stop: CancellationToken,
) -> Ended {
    let session = stop.child_token();
    let dropped = CancellationToken::new();
    let input_end = InputEnd::default();
    let (tx, rx) = channel::<String>(QUEUED_ANSWERS);
    let (took, taken) = watch::channel(0);
    let reader = Reader {
        server: Arc::new(Server {
            out: tx,
            taken,
            calls: Mutex::new(HashMap::new()),
            slots: Arc::new(Semaphore::new(MAX_CALLS)),
            root: session.clone(),
            dropped: dropped.clone(),
            tools,
        }),
        tasks: JoinSet::new(),
        lines: wire::Lines::default(),
    };
    let write = async {
        let delivery = tokio::select! {
            delivery = writer(rx, output, &input_end, &took) => delivery,
            () = dropped.cancelled() => Delivery::TimedOut,
        };
        if !matches!(delivery, Delivery::Delivered) {
            session.cancel();
        }
        delivery
    };
    match tokio::join!(reader.run(input, &input_end), write) {
        (_, Delivery::Failed(e)) => Ended::WriteFailed(e),
        (_, Delivery::TimedOut) => Ended::SlowReader,
        _ if dropped.is_cancelled() => Ended::SlowReader,
        (ReadEnd::Failed(e), _) => Ended::ReadFailed(e),
        (ReadEnd::Cancelled, _) => Ended::Stopped,
        (ReadEnd::Eof, Delivery::Delivered) => Ended::EndOfInput,
    }
}

/// Reads requests, answers what needs no tool and starts a task per call.
struct Reader<T> {
    server: Arc<Server<T>>,
    tasks: JoinSet<()>,
    lines: wire::Lines,
}

impl<T: Toolbox> Reader<T> {
    /// Reads until input ends or the session is cancelled, marks the input
    /// end, then waits for every call. Dropping the server afterwards closes
    /// the answer queue, which ends the writer.
    async fn run(mut self, mut input: impl AsyncRead + Unpin, input_end: &InputEnd) -> ReadEnd {
        let end = match self.read(&mut input).await {
            Ok(()) => ReadEnd::Eof,
            Err(end) => end,
        };
        input_end.reached();
        while self.tasks.join_next().await.is_some() {}
        end
    }

    async fn read(&mut self, input: &mut (impl AsyncRead + Unpin)) -> Result<(), ReadEnd> {
        let mut buf = vec![0u8; 64 * 1024];
        loop {
            let n = tokio::select! {
                biased;
                () = self.server.root.cancelled() => return Err(ReadEnd::Cancelled),
                n = input.read(&mut buf) => n.map_err(ReadEnd::Failed)?,
            };
            if n == 0 {
                if let Some(line) = self.lines.finish() {
                    self.answer(&line).await?;
                }
                return Ok(());
            }
            while self.tasks.try_join_next().is_some() {}
            let read = self.lines.feed(buf.get(..n).unwrap_or_default());
            for _ in 0..self.lines.take_oversized() {
                let refusal = wire::error(
                    None,
                    INVALID_REQUEST,
                    &format!(
                        "invalid request: a message may hold at most {} MiB",
                        wire::MAX_LINE >> 20
                    ),
                );
                self.server.deliver(refusal).await?;
            }
            for line in read {
                self.answer(&line).await?;
            }
        }
    }

    async fn answer(&mut self, line: &[u8]) -> Result<(), ReadEnd> {
        match self.server.handle(line, &mut self.tasks) {
            Reply::Now(answer) => self.server.deliver(answer).await,
            Reply::Later | Reply::Never => Ok(()),
        }
    }
}

/// Writes each answer as one line, in flushed slices of `SLICE` bytes,
/// until the queue closes, counting each slice the client took in `took`.
/// Fails with the write error, or times out once input has ended and a
/// slice waited `DRAIN`.
async fn writer(
    mut answers: Receiver<String>,
    mut output: impl AsyncWrite + Unpin,
    input_end: &InputEnd,
    took: &watch::Sender<u64>,
) -> Delivery {
    while let Some(mut line) = answers.recv().await {
        line.push('\n');
        for slice in line.as_bytes().chunks(SLICE) {
            let step = async {
                output.write_all(slice).await?;
                output.flush().await
            };
            match input_end.progress(step).await {
                None => return Delivery::TimedOut,
                Some(Err(e)) => return Delivery::Failed(e),
                Some(Ok(())) => took.send_modify(|n| *n += 1),
            }
        }
    }
    Delivery::Delivered
}

/// Cancels `stop` at the first SIGINT or SIGTERM, so serve drains its
/// answers, and exits 130 at the second, as `deploy` does.
async fn on_signals(stop: CancellationToken) {
    #[cfg(unix)]
    {
        use tokio::signal::unix::{SignalKind, signal};
        let (Ok(mut int), Ok(mut term)) = (
            signal(SignalKind::interrupt()),
            signal(SignalKind::terminate()),
        ) else {
            return;
        };
        tokio::select! {
            _ = int.recv() => {}
            _ = term.recv() => {}
        }
        stop.cancel();
        tokio::select! {
            _ = int.recv() => {}
            _ = term.recv() => {}
        }
    }
    #[cfg(not(unix))]
    {
        if tokio::signal::ctrl_c().await.is_err() {
            return;
        }
        stop.cancel();
        if tokio::signal::ctrl_c().await.is_err() {
            return;
        }
    }
    std::process::exit(i32::from(crate::error::Kind::Interrupted.exit_code()));
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
            b'"' => Some(CallId::Text(
                serde_json::from_str(raw).unwrap_or_else(|_| raw.to_string()),
            )),
            b'-' | b'0'..=b'9' => Some(CallId::Number(raw.to_string())),
            _ => None,
        }
    }
}

impl<T: Toolbox> Server<T> {
    /// Queues `answer`, waiting for room as long as the client keeps taking
    /// slices. A client that takes none for `DRAIN` is dropped: the session
    /// ends as `SlowReader`.
    async fn queue(&self, answer: String) -> Result<(), Unqueued> {
        let mut taken = self.taken.clone();
        taken.borrow_and_update();
        let room = self.out.reserve();
        tokio::pin!(room);
        loop {
            tokio::select! {
                biased;
                () = self.root.cancelled() => return Err(Unqueued::Ended),
                room = &mut room => {
                    let Ok(permit) = room else {
                        return Err(Unqueued::Ended);
                    };
                    permit.send(answer);
                    return Ok(());
                }
                took = taken.changed() => {
                    if took.is_err() {
                        return Err(Unqueued::Ended);
                    }
                }
                () = tokio::time::sleep(DRAIN) => {
                    self.dropped.cancel();
                    self.root.cancel();
                    return Err(Unqueued::SlowReader);
                }
            }
        }
    }

    /// Queues the reader's answer; the reader stops once the session ends.
    async fn deliver(&self, answer: String) -> Result<(), ReadEnd> {
        self.queue(answer).await.map_err(|_| ReadEnd::Cancelled)
    }

    fn handle(self: &Arc<Self>, line: &[u8], tasks: &mut JoinSet<()>) -> Reply {
        match wire::decode(line) {
            Decoded::Skip => Reply::Never,
            Decoded::Reply(r) => Reply::Now(r),
            Decoded::Notification(n) => {
                if n.method == "notifications/cancelled" {
                    self.cancel(n.params.as_deref());
                }
                Reply::Never
            }
            Decoded::Request(r) if r.method.starts_with("notifications/") => {
                if r.method == "notifications/cancelled" {
                    self.cancel(r.params.as_deref());
                }
                Reply::Never
            }
            Decoded::Request(r) => self.request(&r, tasks),
        }
    }

    fn calls(&self) -> MutexGuard<'_, HashMap<CallId, CancellationToken>> {
        self.calls.lock().unwrap_or_else(PoisonError::into_inner)
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
        if let Some(token) = self.calls().get(&id) {
            token.cancel();
        }
    }

    fn request(self: &Arc<Self>, r: &Request, tasks: &mut JoinSet<()>) -> Reply {
        let Some(id) = r.id.clone() else {
            return Reply::Never;
        };
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
                return Reply::Now(wire::error_with(
                    Some(&id),
                    UNSUPPORTED_VERSION,
                    "Unsupported protocol version",
                    Some(&json!({"requested": requested, "supported": [STATELESS]})),
                ));
            }
            if r.method != "server/discover"
                && meta.and_then(|m| m.get(META_CAPABILITIES)).is_none()
            {
                return Reply::Now(wire::error(
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
            ("initialize", Era::Session) => Reply::Now(initialize(&id, r.params.as_deref())),
            ("ping", Era::Session) => Reply::Now(wire::result(&id, "{}")),
            ("tools/list", _) => Reply::Now(tools_list(&id, self.tools.list(), era)),
            ("server/discover", _) => Reply::Now(wire::result(
                &id,
                &stateless(json!({
                    "cacheScope": "public",
                    "capabilities": {"tools": {}},
                    "supportedVersions": [STATELESS],
                    "ttlMs": 3_600_000,
                })),
            )),
            ("tools/call", _) => self.call(id, r.params.as_deref(), era, tasks),
            _ => Reply::Now(not_found()),
        }
    }

    fn call(
        self: &Arc<Self>,
        id: String,
        params: Option<&RawValue>,
        era: Era,
        tasks: &mut JoinSet<()>,
    ) -> Reply {
        let invalid = |m: &str| {
            wire::error(
                Some(&id),
                INVALID_PARAMS,
                &format!("invalid tools/call params: {m}"),
            )
        };
        let Some(params) = params else {
            return Reply::Now(invalid("unexpected end of JSON input"));
        };
        let fields = match wire::go_struct(
            params,
            "mcp.callToolParams",
            "callToolParams",
            &[("name", Field::String), ("arguments", Field::Raw)],
        ) {
            Ok(f) => f,
            Err(e) => return Reply::Now(invalid(&e.to_string())),
        };
        let name = fields.string("name").unwrap_or_default();
        let Some(tool) = self.tools.tool(&name) else {
            return Reply::Now(wire::error(
                Some(&id),
                INVALID_PARAMS,
                &format!("unknown tool: {name}"),
            ));
        };
        let arguments = fields.raw("arguments").map(RawValue::to_owned);
        let Some(key) = CallId::of(&id) else {
            return Reply::Never;
        };
        let token = self.root.child_token();
        let mut calls = self.calls();
        if calls.contains_key(&key) {
            return Reply::Now(wire::error(
                Some(&id),
                INVALID_REQUEST,
                &format!("request id {id} is already in flight"),
            ));
        }
        let Ok(permit) = self.slots.clone().try_acquire_owned() else {
            return Reply::Now(wire::error(
                Some(&id),
                BUSY,
                &format!("{MAX_CALLS} tool calls are already running; wait for one to finish"),
            ));
        };
        calls.insert(key.clone(), token.clone());
        drop(calls);
        let server = self.clone();
        let slot = Slot::from(permit);
        tasks.spawn(async move {
            let tools = server.tools.clone();
            let work = tools.call(tool, arguments, token.clone(), slot.clone());
            let outcome = match crate::error::catching_async(work).await {
                Ok(o) => o,
                Err(p) => Outcome::panicked(&p),
            };
            server.calls().remove(&key);
            if token.is_cancelled() {
                return;
            }
            let _ = server.queue(wire::result(&id, &outcome.render(era))).await;
            drop(slot);
        });
        Reply::Later
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

    fn server() -> (Arc<Server<tools::Tools>>, Receiver<String>) {
        let (tx, rx) = channel::<String>(1);
        let server = Arc::new(Server {
            out: tx,
            taken: watch::channel(0).1,
            calls: Mutex::new(HashMap::new()),
            slots: Arc::new(Semaphore::new(MAX_CALLS)),
            root: CancellationToken::new(),
            dropped: CancellationToken::new(),
            tools: Arc::new(tools::Tools {
                env: Env::default(),
                verbose: false,
            }),
        });
        (server, rx)
    }

    #[tokio::test]
    async fn a_request_id_already_in_flight_is_refused() {
        let (server, _rx) = server();
        let busy = CancellationToken::new();
        server
            .calls
            .lock()
            .unwrap()
            .insert(CallId::Number("7".into()), busy.clone());
        let mut tasks = JoinSet::new();
        let Reply::Now(answer) = server.handle(
            br#"{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"templates_list","arguments":{}}}"#,
            &mut tasks,
        ) else {
            panic!("the call was not answered at once");
        };
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

    #[tokio::test]
    async fn a_call_whose_id_holds_a_lone_surrogate_still_runs() {
        let (server, _rx) = server();
        let mut tasks = JoinSet::new();
        let answer = server.handle(
            br#"{"jsonrpc":"2.0","id":"\ud800","method":"tools/call","params":{"name":"templates_list","arguments":{}}}"#,
            &mut tasks,
        );
        assert!(matches!(answer, Reply::Later));
        assert_eq!(tasks.len(), 1);
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
