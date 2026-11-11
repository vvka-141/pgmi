use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use serde_json::value::RawValue;
use serde_json::{Value, json};
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader, DuplexStream};
use tokio::sync::mpsc::{UnboundedReceiver, UnboundedSender, unbounded_channel};
use tokio::time::timeout;
use tokio_util::sync::CancellationToken;

use super::{Outcome, Toolbox, serve};

const MODERN_META: &str = r#""_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}"#;

#[derive(Clone, Copy)]
enum Fake {
    Echo,
    EchoLen,
    Slow,
    Panic,
}

struct Fakes {
    started: UnboundedSender<()>,
    stopped: UnboundedSender<&'static str>,
    calls: AtomicUsize,
}

struct Probes {
    started: UnboundedReceiver<()>,
    stopped: UnboundedReceiver<&'static str>,
}

fn fakes() -> (Arc<Fakes>, Probes) {
    let (started, started_rx) = unbounded_channel();
    let (stopped, stopped_rx) = unbounded_channel();
    (
        Arc::new(Fakes {
            started,
            stopped,
            calls: AtomicUsize::new(0),
        }),
        Probes {
            started: started_rx,
            stopped: stopped_rx,
        },
    )
}

impl Toolbox for Fakes {
    type Tool = Fake;

    fn list(&self) -> &str {
        r#"[{"name":"echo"}]"#
    }

    fn tool(&self, name: &str) -> Option<Fake> {
        Some(match name {
            "echo" => Fake::Echo,
            "echo_len" => Fake::EchoLen,
            "slow" => Fake::Slow,
            "panic" => Fake::Panic,
            _ => return None,
        })
    }

    async fn call(
        &self,
        tool: Fake,
        arguments: Option<Box<RawValue>>,
        token: CancellationToken,
    ) -> Outcome {
        self.calls.fetch_add(1, Ordering::SeqCst);
        match tool {
            Fake::Echo => Outcome::json(&json!({"echoed": true})),
            Fake::EchoLen => Outcome::json(&json!({"n": arguments.map_or(0, |a| a.get().len())})),
            Fake::Slow => {
                let _ = self.started.send(());
                tokio::select! {
                    () = token.cancelled() => {
                        let _ = self.stopped.send("cancelled");
                        Outcome::failed(&crate::error::Error::new(crate::error::Kind::Interrupted, "cancelled"))
                    }
                    () = tokio::time::sleep(Duration::from_secs(10)) => {
                        let _ = self.stopped.send("finished");
                        Outcome::json(&json!("done"))
                    }
                }
            }
            Fake::Panic => panic!("the tool blew up"),
        }
    }
}

async fn session(tools: Arc<Fakes>, input: &str) -> String {
    let mut out = Vec::new();
    serve(input.as_bytes(), &mut out, tools, CancellationToken::new()).await;
    String::from_utf8(out).unwrap()
}

async fn responses(input: &str) -> Vec<Value> {
    session(fakes().0, input)
        .await
        .lines()
        .map(|l| serde_json::from_str(l).unwrap())
        .collect()
}

struct Running {
    input: DuplexStream,
    output: BufReader<DuplexStream>,
    stop: CancellationToken,
    server: tokio::task::JoinHandle<()>,
}

impl Running {
    fn start(tools: Arc<Fakes>) -> Self {
        let (input, server_in) = tokio::io::duplex(1024);
        let (server_out, output) = tokio::io::duplex(1024);
        let stop = CancellationToken::new();
        let server = tokio::spawn(serve(server_in, server_out, tools, stop.clone()));
        Running {
            input,
            output: BufReader::new(output),
            stop,
            server,
        }
    }

    async fn send(&mut self, line: &str) {
        self.input.write_all(line.as_bytes()).await.unwrap();
        self.input.write_all(b"\n").await.unwrap();
    }

    async fn frame(&mut self) -> Option<String> {
        let mut line = String::new();
        let n = timeout(Duration::from_secs(3), self.output.read_line(&mut line))
            .await
            .expect("no frame within 3s")
            .unwrap();
        (n > 0).then_some(line)
    }

    async fn ended(self) -> BufReader<DuplexStream> {
        timeout(Duration::from_secs(3), self.server)
            .await
            .expect("serve did not return within 3s")
            .expect("serve panicked");
        self.output
    }
}

#[tokio::test]
async fn ping_is_answered_and_cancel_stops_a_slow_tool_call() {
    let (tools, mut probes) = fakes();
    let mut s = Running::start(tools);
    s.send(r#"{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"slow"}}"#)
        .await;
    probes.started.recv().await.unwrap();
    s.send(r#"{"jsonrpc":"2.0","id":8,"method":"ping"}"#).await;
    let frame = s.frame().await.unwrap();
    assert!(frame.contains(r#""id":8"#), "{frame}");
    s.send(r#"{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"user"}}"#)
        .await;
    let stopped = timeout(Duration::from_secs(1), probes.stopped.recv()).await;
    assert_eq!(stopped.unwrap(), Some("cancelled"));
    s.input.shutdown().await.unwrap();
    let mut output = s.ended().await;
    let mut rest = String::new();
    tokio::io::AsyncReadExt::read_to_string(&mut output, &mut rest)
        .await
        .unwrap();
    assert_eq!(rest, "", "a cancelled request must not be answered");
}

#[tokio::test]
async fn request_ids_match_by_value_not_by_spelling() {
    let (tools, mut probes) = fakes();
    let mut s = Running::start(tools);
    s.send(r#"{"jsonrpc":"2.0","id":"job","method":"tools/call","params":{"name":"slow"}}"#)
        .await;
    probes.started.recv().await.unwrap();
    s.send(r#"{"jsonrpc":"2.0","id":"\u006aob","method":"tools/call","params":{"name":"slow"}}"#)
        .await;
    let frame = s.frame().await.unwrap();
    assert!(frame.contains("-32600"), "{frame}");
    assert!(frame.contains("already in flight"), "{frame}");
    s.send(r#"{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow"}}"#)
        .await;
    probes.started.recv().await.unwrap();
    s.send(r#"{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"1"}}"#)
        .await;
    s.send(
        r#"{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"\u006aob"}}"#,
    )
    .await;
    let stopped = timeout(Duration::from_secs(1), probes.stopped.recv()).await;
    assert_eq!(stopped.unwrap(), Some("cancelled"));
    assert!(
        timeout(Duration::from_millis(300), probes.stopped.recv())
            .await
            .is_err(),
        "the string id \"1\" cancelled the number id 1"
    );
    s.stop.cancel();
    s.ended().await;
}

#[tokio::test]
async fn stopping_the_server_during_a_tool_call_ends_it_cleanly() {
    let (tools, mut probes) = fakes();
    let mut s = Running::start(tools);
    s.send(r#"{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow"}}"#)
        .await;
    probes.started.recv().await.unwrap();
    s.stop.cancel();
    assert_eq!(probes.stopped.recv().await, Some("cancelled"));
    let mut output = s.ended().await;
    let mut rest = String::new();
    tokio::io::AsyncReadExt::read_to_string(&mut output, &mut rest)
        .await
        .unwrap();
    assert_eq!(rest, "");
}

#[tokio::test]
async fn stopping_unblocks_a_read_that_waits_for_input() {
    let mut s = Running::start(fakes().0);
    s.send(r#"{"jsonrpc":"2.0","id":1,"method":"ping"}"#).await;
    let frame = s.frame().await.unwrap();
    assert!(frame.contains(r#""id":1"#), "{frame}");
    s.stop.cancel();
    let mut output = s.ended().await;
    let mut rest = String::new();
    tokio::io::AsyncReadExt::read_to_string(&mut output, &mut rest)
        .await
        .unwrap();
    assert!(!rest.contains("-32700"), "{rest}");
}

#[tokio::test]
async fn a_stopped_server_reads_nothing() {
    let stop = CancellationToken::new();
    stop.cancel();
    let mut out = Vec::new();
    serve(
        &br#"{"jsonrpc":"2.0","id":1,"method":"ping"}"#[..],
        &mut out,
        fakes().0,
        stop,
    )
    .await;
    assert!(out.is_empty());
}

#[tokio::test]
async fn a_frame_larger_than_any_buffer_is_read_whole() {
    let (mut client, server_in) = tokio::io::duplex(1024);
    let blob = "x".repeat(256 * 1024);
    let input = format!(
        "{{\"jsonrpc\":\"2.0\",\"id\":11,\"method\":\"tools/call\",\"params\":{{\"name\":\"echo_len\",\"arguments\":{{\"blob\":\"{blob}\"}}}}}}\n{{\"jsonrpc\":\"2.0\",\"id\":12,\"method\":\"ping\"}}\n"
    );
    let writer = tokio::spawn(async move {
        client.write_all(input.as_bytes()).await.unwrap();
        client.shutdown().await.unwrap();
    });
    let mut out = Vec::new();
    serve(server_in, &mut out, fakes().0, CancellationToken::new()).await;
    writer.await.unwrap();
    let text = String::from_utf8(out).unwrap();
    let lines: Vec<Value> = text
        .lines()
        .map(|l| serde_json::from_str(l).unwrap())
        .collect();
    let big = lines.iter().find(|v| v["id"] == 11).unwrap();
    assert!(
        big["result"]["structuredContent"]["n"].as_u64().unwrap() > 256 * 1024,
        "{big}"
    );
    assert!(lines.iter().any(|v| v["id"] == 12));
}

#[tokio::test]
async fn a_message_split_over_lines_is_refused_and_the_session_goes_on() {
    let r = responses(
        "{\"jsonrpc\":\"2.0\",\n\"id\":1,\"method\":\"ping\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"ping\"}\n",
    )
    .await;
    assert!(r.iter().any(|v| v["error"]["code"] == -32700), "{r:?}");
    assert!(
        r.iter().any(|v| v["id"] == 2 && v.get("result").is_some()),
        "{r:?}"
    );
}

#[tokio::test]
async fn every_response_carries_an_id() {
    let r = responses(concat!(
        r#"{"jsonrpc":"2.0","id":1,"method":"ping"}"#,
        "\n",
        r#"{"jsonrpc":"2.0","id":"two","method":"tools/list"}"#,
        "\n",
        r#"{"jsonrpc":"2.0","id":3,"method":"nosuch"}"#,
        "\n",
        "{\"jsonrpc\":\"2.0\"\n",
    ))
    .await;
    assert_eq!(r.len(), 4, "{r:?}");
    for v in &r {
        assert!(v.get("id").is_some(), "{v}");
    }
}

#[tokio::test]
async fn notifications_get_no_answer_whatever_the_method() {
    for line in [
        r#"{"jsonrpc":"2.0","method":"notifications/initialized"}"#,
        r#"{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2025-06-18"}}"#,
        r#"{"jsonrpc":"2.0","method":"tools/list"}"#,
        r#"{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo","arguments":{}}}"#,
        r#"{"jsonrpc":"2.0","id":5,"method":"notifications/initialized"}"#,
    ] {
        let (tools, _probes) = fakes();
        let out = session(tools.clone(), line).await;
        assert_eq!(out, "", "{line}");
        assert_eq!(tools.calls.load(Ordering::SeqCst), 0, "{line}");
    }
}

#[tokio::test]
async fn a_panicking_tool_answers_is_error_with_exit_code_3() {
    let r = responses(
        r#"{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"panic","arguments":{}}}"#,
    )
    .await;
    assert_eq!(r[0]["result"]["isError"], true, "{r:?}");
    assert_eq!(r[0]["result"]["structuredContent"]["exitCode"], 3);
}

#[tokio::test]
async fn both_protocol_eras_are_served_in_one_process() {
    let r = responses(&format!(
        "{}\n{}\n{}\n",
        r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}"#,
        format_args!(r#"{{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{{{MODERN_META}}}}}"#),
        r#"{"jsonrpc":"2.0","id":3,"method":"tools/list"}"#,
    ))
    .await;
    let ids: Vec<i64> = r.iter().map(|v| v["id"].as_i64().unwrap()).collect();
    assert_eq!(ids, [1, 2, 3]);
    assert_eq!(r[0]["result"]["protocolVersion"], "2025-11-25");
    assert_eq!(r[1]["result"]["resultType"], "complete");
    assert!(r[2]["result"].get("resultType").is_none(), "{}", r[2]);
    assert_eq!(r[2]["result"]["tools"], json!([{"name": "echo"}]));
}

#[tokio::test]
async fn server_discover_answers_a_complete_2026_result() {
    for params in [format!("{{{MODERN_META}}}"), "{}".to_string()] {
        let r = responses(&format!(
            r#"{{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{params}}}"#
        ))
        .await;
        let m = &r[0]["result"];
        assert_eq!(m["resultType"], "complete", "{m}");
        assert_eq!(m["cacheScope"], "public");
        assert_eq!(m["supportedVersions"], json!(["2026-07-28"]));
        assert!(m["capabilities"]["tools"].is_object());
        assert!(m["ttlMs"].is_u64());
        let info = &m["_meta"]["io.modelcontextprotocol/serverInfo"];
        assert_eq!(info["name"], "pgmi");
        assert_eq!(info["version"], crate::VERSION);
    }
}

#[tokio::test]
async fn modern_calls_need_no_initialize_and_name_the_server() {
    let r = responses(&format!(
        "{}\n{}\n",
        format_args!(r#"{{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{{{MODERN_META}}}}}"#),
        format_args!(
            r#"{{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{{"name":"echo","arguments":{{}},{MODERN_META}}}}}"#
        ),
    ))
    .await;
    assert_eq!(r.len(), 2);
    for v in &r {
        assert_eq!(v["result"]["resultType"], "complete", "{v}");
        assert_eq!(
            v["result"]["_meta"]["io.modelcontextprotocol/serverInfo"]["name"],
            "pgmi"
        );
    }
    assert_eq!(r[0]["result"]["cacheScope"], "public");
    assert!(r[0]["result"]["ttlMs"].is_u64());
    assert!(r[1]["result"].get("isError").is_none(), "{}", r[1]);
    assert_eq!(r[1]["result"]["structuredContent"]["echoed"], true);
}

#[tokio::test]
async fn a_modern_request_without_client_capabilities_is_invalid_params() {
    let r = responses(
        r#"{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}"#,
    )
    .await;
    assert_eq!(r[0]["error"]["code"], -32602, "{r:?}");
}

#[tokio::test]
async fn a_modern_request_with_an_unknown_version_is_refused() {
    for method in ["server/discover", "tools/list"] {
        let r = responses(&format!(
            r#"{{"jsonrpc":"2.0","id":1,"method":"{method}","params":{{"_meta":{{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{{}}}}}}}}"#
        ))
        .await;
        let e = &r[0]["error"];
        assert_eq!(e["code"], -32022, "{method}: {r:?}");
        assert_eq!(e["data"]["supported"], json!(["2026-07-28"]));
        assert_eq!(e["data"]["requested"], "2099-01-01");
    }
}

#[tokio::test]
async fn modern_initialize_and_ping_are_method_not_found() {
    let r = responses(&format!(
        "{}\n{}\n",
        format_args!(
            r#"{{"jsonrpc":"2.0","id":1,"method":"initialize","params":{{"protocolVersion":"2026-07-28",{MODERN_META}}}}}"#
        ),
        format_args!(r#"{{"jsonrpc":"2.0","id":2,"method":"ping","params":{{{MODERN_META}}}}}"#),
    ))
    .await;
    assert_eq!(r.len(), 2);
    for v in &r {
        assert_eq!(v["error"]["code"], -32601, "{v}");
    }
}

#[tokio::test]
async fn a_legacy_session_is_unchanged() {
    let r = responses(concat!(
        r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}"#,
        "\n",
        r#"{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"a":1}}}"#,
    ))
    .await;
    assert_eq!(r[0]["result"]["protocolVersion"], "2025-11-25");
    assert!(r[1]["result"].get("resultType").is_none(), "{}", r[1]);
    assert_eq!(r[1]["result"]["structuredContent"]["echoed"], true);
}

#[tokio::test]
async fn a_client_that_stops_reading_does_not_hold_up_the_stop() {
    let (mut input, server_in) = tokio::io::duplex(1024);
    let (server_out, _unread) = tokio::io::duplex(64);
    let stop = CancellationToken::new();
    let server = tokio::spawn(serve(server_in, server_out, fakes().0, stop.clone()));
    for id in 0..50 {
        let ping = format!("{{\"jsonrpc\":\"2.0\",\"id\":{id},\"method\":\"ping\"}}\n");
        input.write_all(ping.as_bytes()).await.unwrap();
    }
    tokio::time::sleep(Duration::from_millis(200)).await;
    stop.cancel();
    timeout(Duration::from_secs(10), server)
        .await
        .expect("serve waited for a client that never reads")
        .unwrap();
}

#[tokio::test]
async fn an_output_failure_ends_the_session() {
    let (tools, mut probes) = fakes();
    let (mut input, server_in) = tokio::io::duplex(1024);
    let (server_out, gone) = tokio::io::duplex(1024);
    drop(gone);
    let server = tokio::spawn(serve(
        server_in,
        server_out,
        tools,
        CancellationToken::new(),
    ));
    let call = r#"{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow"}}"#;
    input
        .write_all(format!("{call}\n").as_bytes())
        .await
        .unwrap();
    probes.started.recv().await.unwrap();
    let ping = r#"{"jsonrpc":"2.0","id":2,"method":"ping"}"#;
    input
        .write_all(format!("{ping}\n").as_bytes())
        .await
        .unwrap();
    let stopped = timeout(Duration::from_secs(3), probes.stopped.recv()).await;
    assert_eq!(stopped.expect("the call kept running"), Some("cancelled"));
    timeout(Duration::from_secs(3), server)
        .await
        .expect("serve kept reading after its output failed")
        .unwrap();
    drop(input);
}

#[tokio::test]
async fn a_line_over_16_mib_is_refused_at_once_and_the_session_goes_on() {
    let (mut input, server_in) = tokio::io::duplex(64 * 1024);
    let (server_out, output) = tokio::io::duplex(64 * 1024);
    let mut output = BufReader::new(output);
    let server = tokio::spawn(serve(
        server_in,
        server_out,
        fakes().0,
        CancellationToken::new(),
    ));
    let head = r#"{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo_len","arguments":{"blob":""#;
    input.write_all(head.as_bytes()).await.unwrap();
    let blob = vec![b'x'; 1 << 20];
    for _ in 0..16 {
        input.write_all(&blob).await.unwrap();
    }
    let mut line = String::new();
    timeout(Duration::from_secs(3), output.read_line(&mut line))
        .await
        .expect("no answer before the line ended")
        .unwrap();
    let refused: Value = serde_json::from_str(&line).unwrap();
    assert_eq!(refused["error"]["code"], -32600, "{refused}");
    assert_eq!(refused["id"], Value::Null, "{refused}");
    let ping = r#"{"jsonrpc":"2.0","id":2,"method":"ping"}"#;
    input
        .write_all(format!("\"}}}}}}\n{ping}\n").as_bytes())
        .await
        .unwrap();
    line.clear();
    timeout(Duration::from_secs(3), output.read_line(&mut line))
        .await
        .expect("no answer to the ping")
        .unwrap();
    let answered: Value = serde_json::from_str(&line).unwrap();
    assert_eq!(answered["id"], 2, "{answered}");
    assert!(answered.get("result").is_some(), "{answered}");
    drop(input);
    timeout(Duration::from_secs(3), server)
        .await
        .expect("serve did not end")
        .unwrap();
    let mut rest = String::new();
    tokio::io::AsyncReadExt::read_to_string(&mut output, &mut rest)
        .await
        .unwrap();
    assert_eq!(rest, "", "the rest of the long line was answered");
}

#[tokio::test]
async fn calls_beyond_32_are_refused_and_cancels_still_get_through() {
    let (tools, mut probes) = fakes();
    let mut s = Running::start(tools);
    for id in 1..=32 {
        s.send(&format!(
            r#"{{"jsonrpc":"2.0","id":{id},"method":"tools/call","params":{{"name":"slow"}}}}"#
        ))
        .await;
    }
    for _ in 0..32 {
        probes.started.recv().await.unwrap();
    }
    s.send(r#"{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"slow"}}"#)
        .await;
    let refused: Value = serde_json::from_str(&s.frame().await.unwrap()).unwrap();
    assert_eq!(refused["id"], 99, "{refused}");
    assert_eq!(refused["error"]["code"], -32000, "{refused}");
    s.send(r#"{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}"#)
        .await;
    let stopped = timeout(Duration::from_secs(1), probes.stopped.recv()).await;
    assert_eq!(stopped.unwrap(), Some("cancelled"));
    s.send(r#"{"jsonrpc":"2.0","id":100,"method":"ping"}"#)
        .await;
    let answered: Value = serde_json::from_str(&s.frame().await.unwrap()).unwrap();
    assert_eq!(answered["id"], 100, "{answered}");
    s.stop.cancel();
    s.ended().await;
}

#[tokio::test]
async fn a_client_that_never_reads_is_dropped_once_its_answers_pile_up() {
    let (mut input, server_in) = tokio::io::duplex(64 * 1024);
    let (server_out, _unread) = tokio::io::duplex(64);
    let server = tokio::spawn(serve(
        server_in,
        server_out,
        fakes().0,
        CancellationToken::new(),
    ));
    let pings = tokio::spawn(async move {
        for id in 0..1500 {
            let ping = format!("{{\"jsonrpc\":\"2.0\",\"id\":{id},\"method\":\"ping\"}}\n");
            if input.write_all(ping.as_bytes()).await.is_err() {
                break;
            }
        }
        input
    });
    timeout(Duration::from_secs(10), server)
        .await
        .expect("serve kept queueing answers nobody reads")
        .unwrap();
    drop(pings);
}
