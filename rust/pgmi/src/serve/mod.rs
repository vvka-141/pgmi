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
use tokio::sync::mpsc::{UnboundedSender, unbounded_channel};
use tokio::task::JoinSet;
use tokio_util::sync::CancellationToken;

use crate::VERSION;
use crate::env::Env;
use crate::error::Kind;
pub(crate) use tools::{Outcome, Toolbox};
use wire::{Decoded, Field, INVALID_PARAMS, INVALID_REQUEST, METHOD_NOT_FOUND, Request};

const STATELESS: &str = "2026-07-28";
const META_VERSION: &str = "io.modelcontextprotocol/protocolVersion";
const META_CAPABILITIES: &str = "io.modelcontextprotocol/clientCapabilities";
const META_SERVER: &str = "io.modelcontextprotocol/serverInfo";
const UNSUPPORTED_VERSION: i64 = -32022;
const ECHOED: [&str; 3] = ["2025-11-25", "2025-06-18", "2024-11-05"];

pub(crate) fn run(verbose: bool, env: Env) -> u8 {
    crate::conn::tls::install();
    let runtime = match tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
    {
        Ok(r) => r,
        Err(e) => {
            let _ = std::io::Write::write_all(
                &mut std::io::stderr(),
                format!("pgmi: error: start the runtime: {e}\n").as_bytes(),
            );
            return Kind::General.exit_code();
        }
    };
    let stop = CancellationToken::new();
    runtime.block_on(async {
        let signals = tokio::spawn({
            let stop = stop.clone();
            async move {
                stop_signal().await;
                stop.cancel();
            }
        });
        serve(
            tokio::io::stdin(),
            tokio::io::stdout(),
            Arc::new(tools::Tools { env, verbose }),
            stop,
        )
        .await;
        signals.abort();
    });
    runtime.shutdown_timeout(Duration::from_millis(100));
    0
}

struct Server<T> {
    out: UnboundedSender<String>,
    calls: Mutex<HashMap<String, CancellationToken>>,
    root: CancellationToken,
    tools: Arc<T>,
}

/// Serves MCP over newline-delimited JSON-RPC until `input` ends or `stop`
/// is cancelled. Cancelling `stop` cancels every tool call in flight; a
/// cancelled call is never answered. Returns once every call has ended and
/// every answer is written.
pub(crate) async fn serve<T: Toolbox>(
    mut input: impl AsyncRead + Unpin,
    mut output: impl AsyncWrite + Unpin,
    tools: Arc<T>,
    stop: CancellationToken,
) {
    let (tx, mut rx) = unbounded_channel::<String>();
    let server = Arc::new(Server {
        out: tx,
        calls: Mutex::new(HashMap::new()),
        root: stop,
        tools,
    });
    let read = async move {
        let mut tasks = JoinSet::new();
        let mut lines = wire::Lines::default();
        let mut buf = vec![0u8; 64 * 1024];
        loop {
            let n = tokio::select! {
                biased;
                () = server.root.cancelled() => break,
                n = input.read(&mut buf) => n.unwrap_or(0),
            };
            if n == 0 {
                if let Some(line) = lines.finish() {
                    server.handle(&line, &mut tasks);
                }
                break;
            }
            for line in lines.feed(&buf[..n]) {
                server.handle(&line, &mut tasks);
            }
            while tasks.try_join_next().is_some() {}
        }
        while tasks.join_next().await.is_some() {}
    };
    let write = async move {
        while let Some(mut line) = rx.recv().await {
            line.push('\n');
            if output.write_all(line.as_bytes()).await.is_err() || output.flush().await.is_err() {
                return;
            }
        }
    };
    tokio::join!(read, write);
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

struct Era {
    stateless: bool,
}

impl<T: Toolbox> Server<T> {
    fn send(&self, line: String) {
        let _ = self.out.send(line);
    }

    fn handle(self: &Arc<Self>, line: &[u8], tasks: &mut JoinSet<()>) {
        match wire::decode(line) {
            Decoded::Skip => {}
            Decoded::Reply(r) => self.send(r),
            Decoded::Notification(n) => {
                if n.method == "notifications/cancelled" {
                    self.cancel(n.params.as_deref());
                }
            }
            Decoded::Request(r) if r.method.starts_with("notifications/") => {
                if r.method == "notifications/cancelled" {
                    self.cancel(r.params.as_deref());
                }
            }
            Decoded::Request(r) => self.request(r, tasks),
        }
    }

    fn cancel(&self, params: Option<&RawValue>) {
        let Some(id) = params
            .and_then(|p| serde_json::from_str::<wire::Members>(p.get()).ok())
            .and_then(|m| {
                m.0.into_iter()
                    .rev()
                    .find(|(k, _)| k == "requestId")
                    .map(|(_, v)| v.get().to_string())
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

    fn request(self: &Arc<Self>, r: Request, tasks: &mut JoinSet<()>) {
        let Some(id) = r.id.clone() else { return };
        let params: Option<Value> = r
            .params
            .as_ref()
            .and_then(|p| serde_json::from_str(p.get()).ok());
        let meta = params.as_ref().and_then(|p| p.get("_meta"));
        let version = meta.and_then(|m| m.get(META_VERSION));
        let era = Era {
            stateless: version.is_some() || r.method == "server/discover",
        };
        if let Some(v) = version {
            let requested = v.as_str().unwrap_or_default();
            if requested != STATELESS {
                return self.send(wire::error_with(
                    Some(&id),
                    UNSUPPORTED_VERSION,
                    "Unsupported protocol version",
                    Some(&json!({"requested": requested, "supported": [STATELESS]})),
                ));
            }
            if r.method != "server/discover"
                && meta.and_then(|m| m.get(META_CAPABILITIES)).is_none()
            {
                return self.send(wire::error(
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
        match (r.method.as_str(), era.stateless) {
            ("initialize", false) => self.send(initialize(&id, r.params.as_deref())),
            ("ping", false) => self.send(wire::result(&id, "{}")),
            ("tools/list", _) => self.send(tools_list(&id, self.tools.list(), &era)),
            ("server/discover", _) => self.send(wire::result(
                &id,
                &stateless(json!({
                    "cacheScope": "public",
                    "capabilities": {"tools": {}},
                    "supportedVersions": [STATELESS],
                    "ttlMs": 3_600_000,
                })),
            )),
            ("tools/call", _) => self.call(id, r.params.as_deref(), era, tasks),
            _ => self.send(not_found()),
        }
    }

    fn call(
        self: &Arc<Self>,
        id: String,
        params: Option<&RawValue>,
        era: Era,
        tasks: &mut JoinSet<()>,
    ) {
        let invalid = |m: &str| {
            wire::error(
                Some(&id),
                INVALID_PARAMS,
                &format!("invalid tools/call params: {m}"),
            )
        };
        let Some(params) = params else {
            return self.send(invalid("unexpected end of JSON input"));
        };
        let fields = match wire::go_struct(
            params,
            "mcp.callToolParams",
            "callToolParams",
            &[("name", Field::String), ("arguments", Field::Raw)],
        ) {
            Ok(f) => f,
            Err(e) => return self.send(invalid(&e)),
        };
        let name = fields.string("name").unwrap_or_default();
        let Some(tool) = self.tools.tool(&name) else {
            return self.send(wire::error(
                Some(&id),
                INVALID_PARAMS,
                &format!("unknown tool: {name}"),
            ));
        };
        let arguments = fields.raw("arguments").map(RawValue::to_owned);
        let token = self.root.child_token();
        let Ok(mut calls) = self.calls.lock() else {
            return;
        };
        if calls.contains_key(&id) {
            return self.send(wire::error(
                Some(&id),
                INVALID_REQUEST,
                &format!("request id {id} is already in flight"),
            ));
        }
        calls.insert(id.clone(), token.clone());
        drop(calls);
        let server = self.clone();
        tasks.spawn(async move {
            let tools = server.tools.clone();
            let task_token = token.clone();
            let handle = tokio::runtime::Handle::current();
            let outcome = match tokio::task::spawn_blocking(move || {
                handle.block_on(tools.call(tool, arguments, task_token))
            })
            .await
            {
                Ok(o) => o,
                Err(e) if e.is_panic() => Outcome::panicked(),
                Err(_) => {
                    Outcome::failed(&crate::error::Error::internal("the tool call was aborted"))
                }
            };
            if let Ok(mut calls) = server.calls.lock() {
                calls.remove(&id);
            }
            if token.is_cancelled() {
                return;
            }
            server.send(wire::result(&id, &outcome.render(era.stateless)));
        });
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

fn tools_list(id: &str, list: &str, era: &Era) -> String {
    if !era.stateless {
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
        let (tx, mut rx) = unbounded_channel::<String>();
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
            .insert("7".into(), busy.clone());
        let mut tasks = JoinSet::new();
        server.handle(
            br#"{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"templates_list","arguments":{}}}"#,
            &mut tasks,
        );
        let answer = rx.recv().await.unwrap();
        assert!(answer.contains("-32600"), "{answer}");
        assert!(answer.contains("already in flight"), "{answer}");
        assert!(tasks.is_empty());
        assert!(!busy.is_cancelled());
        assert!(server.calls.lock().unwrap().contains_key("7"));
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
