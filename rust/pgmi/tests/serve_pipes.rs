#![cfg(test)]
#![cfg(target_os = "linux")]

use std::fmt::Write as _;
use std::io::{Read, Write};
use std::path::PathBuf;
use std::process::{Child, ChildStdin, ChildStdout, Command, ExitStatus, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

const INITIALIZE: &str =
    r#"{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}"#;

fn call(id: usize, tool: &str, arguments: &serde_json::Value) -> String {
    serde_json::json!({"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": {"name": tool, "arguments": arguments}}).to_string()
}

fn ping(id: usize) -> String {
    format!(r#"{{"jsonrpc":"2.0","id":{id},"method":"ping"}}"#)
}

struct Scratch(PathBuf);

impl Scratch {
    fn new(name: &str) -> Self {
        let dir = std::env::temp_dir().join(format!("pgmi-pipes-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        Scratch(dir)
    }

    fn big_project(name: &str, files: usize) -> Self {
        let s = Self::new(name);
        std::fs::write(s.0.join("deploy.sql"), "SELECT 1;\n").unwrap();
        let migrations = s.0.join("migrations");
        std::fs::create_dir_all(&migrations).unwrap();
        for i in 1..=files {
            std::fs::write(
                migrations.join(format!("m_{i:05}_some_longer_descriptive_name.sql")),
                "SELECT 1;\n",
            )
            .unwrap();
        }
        s
    }
}

impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

struct Serve {
    child: Child,
    home: Scratch,
}

impl Serve {
    fn start(name: &str) -> (Self, ChildStdin, ChildStdout) {
        let home = Scratch::new(&format!("{name}-home"));
        let mut child = Command::new(env!("CARGO_BIN_EXE_pgmi"))
            .arg("serve")
            .env_clear()
            .env("HOME", &home.0)
            .env("PATH", "/usr/bin:/bin")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        let stdin = child.stdin.take().unwrap();
        let stdout = child.stdout.take().unwrap();
        (Serve { child, home }, stdin, stdout)
    }

    fn pid(&self) -> i32 {
        i32::try_from(self.child.id()).unwrap()
    }

    fn rss_kib(&self) -> Option<u64> {
        let status = std::fs::read_to_string(format!("/proc/{}/status", self.pid())).ok()?;
        status
            .lines()
            .find_map(|l| l.strip_prefix("VmRSS:"))
            .and_then(|v| v.split_whitespace().next())
            .and_then(|v| v.parse().ok())
    }

    fn interrupt(&self) {
        let sent = Command::new("kill")
            .args(["-INT", &self.pid().to_string()])
            .status()
            .unwrap();
        assert!(sent.success());
    }

    fn exited_within(&mut self, limit: Duration) -> Option<ExitStatus> {
        let deadline = Instant::now() + limit;
        while Instant::now() < deadline {
            if let Some(status) = self.child.try_wait().unwrap() {
                return Some(status);
            }
            std::thread::sleep(Duration::from_millis(20));
        }
        None
    }
}

impl Drop for Serve {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
        let _ = &self.home;
    }
}

struct Reader {
    taken: Arc<AtomicUsize>,
    thread: JoinHandle<Vec<u8>>,
}

impl Reader {
    /// Reads everything at about `bytes_per_second`, `chunk` bytes at a time.
    fn paced(mut out: ChildStdout, bytes_per_second: usize, chunk: usize) -> Self {
        let taken = Arc::new(AtomicUsize::new(0));
        let counter = taken.clone();
        let thread = std::thread::spawn(move || {
            let mut data = Vec::new();
            let mut buf = vec![0u8; chunk];
            loop {
                let n = out.read(&mut buf).unwrap_or(0);
                if n == 0 {
                    return data;
                }
                data.extend_from_slice(&buf[..n]);
                counter.store(data.len(), Ordering::SeqCst);
                std::thread::sleep(Duration::from_secs_f64(n as f64 / bytes_per_second as f64));
            }
        });
        Reader { taken, thread }
    }

    fn fast(out: ChildStdout) -> Self {
        Self::paced(out, 1 << 40, 64 * 1024)
    }

    fn taken(&self) -> usize {
        self.taken.load(Ordering::SeqCst)
    }

    fn answers(self) -> Vec<serde_json::Value> {
        let data = self.thread.join().unwrap();
        assert!(
            data.is_empty() || data.ends_with(b"\n"),
            "the last answer was cut after {} bytes",
            data.len()
        );
        data.split(|b| *b == b'\n')
            .filter(|l| !l.is_empty())
            .map(|l| {
                serde_json::from_slice(l)
                    .unwrap_or_else(|e| panic!("an answer of {} bytes is not JSON: {e}", l.len()))
            })
            .collect()
    }
}

fn send(stdin: &mut ChildStdin, lines: &[String]) {
    let mut text = String::new();
    for line in lines {
        let _ = writeln!(text, "{line}");
    }
    stdin.write_all(text.as_bytes()).unwrap();
}

fn ids(answers: &[serde_json::Value]) -> Vec<u64> {
    let mut ids: Vec<u64> = answers.iter().map(|a| a["id"].as_u64().unwrap()).collect();
    ids.sort_unstable();
    ids
}

#[test]
fn a_reader_taking_8_kb_a_second_gets_every_answer_after_the_input_ends() {
    let (mut serve, mut stdin, stdout) = Serve::start("trickle");
    let mut lines = vec![INITIALIZE.to_string()];
    lines.extend((1..=12).map(|id| call(id, "ai_overview", &serde_json::json!({}))));
    send(&mut stdin, &lines);
    drop(stdin);
    let answers = Reader::paced(stdout, 8 * 1024, 2048).answers();
    assert_eq!(ids(&answers), (0..=12).collect::<Vec<u64>>());
    assert!(answers[1..].iter().all(|a| {
        a["result"]["content"][0]["text"]
            .as_str()
            .is_some_and(|t| t.len() > 10_000)
    }));
    let status = serve.exited_within(Duration::from_secs(5)).unwrap();
    assert_eq!(status.code(), Some(0));
}

#[test]
fn a_huge_answer_reaches_a_reader_taking_800_kb_a_second_whole() {
    let project = Scratch::big_project("huge", 6000);
    let (mut serve, mut stdin, stdout) = Serve::start("huge");
    send(
        &mut stdin,
        &[
            INITIALIZE.to_string(),
            call(1, "metadata_plan", &serde_json::json!({"path": project.0})),
        ],
    );
    drop(stdin);
    let reader = Reader::paced(stdout, 800_000, 64 * 1024);
    let answers = reader.answers();
    assert_eq!(ids(&answers), [0, 1]);
    let plan = &answers[1]["result"]["structuredContent"];
    assert_eq!(plan["plan"].as_array().map(Vec::len), Some(6000));
    let status = serve.exited_within(Duration::from_secs(5)).unwrap();
    assert_eq!(status.code(), Some(0));
}

#[test]
fn a_reader_taking_800_kb_a_second_is_kept_while_a_huge_answer_holds_up_a_full_queue() {
    let project = Scratch::big_project("queue", 6000);
    let (mut serve, mut stdin, stdout) = Serve::start("queue");
    send(
        &mut stdin,
        &[
            INITIALIZE.to_string(),
            call(1, "metadata_plan", &serde_json::json!({"path": project.0})),
        ],
    );
    let reader = Reader::paced(stdout, 800_000, 64 * 1024);
    let waited = Instant::now();
    while reader.taken() < 256 * 1024 {
        assert!(
            waited.elapsed() < Duration::from_secs(60),
            "no plan arrived"
        );
        std::thread::sleep(Duration::from_millis(10));
    }
    let pings: Vec<String> = (2..1300).map(ping).collect();
    send(&mut stdin, &pings);
    drop(stdin);
    let answers = reader.answers();
    assert_eq!(ids(&answers), (0..1300).collect::<Vec<u64>>());
    let status = serve.exited_within(Duration::from_secs(5)).unwrap();
    assert_eq!(status.code(), Some(0));
}

#[test]
fn a_client_that_keeps_calling_and_never_reads_is_dropped_with_bounded_memory() {
    let (mut serve, mut stdin, _unread) = Serve::start("noread");
    send(&mut stdin, &[INITIALIZE.to_string()]);
    let started = Instant::now();
    let mut peak = 0;
    let mut id = 1;
    let ended = loop {
        if let Some(status) = serve.child.try_wait().unwrap() {
            break status;
        }
        peak = peak.max(serve.rss_kib().unwrap_or(0));
        assert!(
            peak < 400 * 1024,
            "RSS reached {peak} KiB after {id} calls without the client being dropped"
        );
        assert!(
            started.elapsed() < Duration::from_secs(20),
            "still serving a client that never reads after {id} calls (RSS {peak} KiB)"
        );
        let batch: Vec<String> = (id..id + 4)
            .map(|i| call(i, "ai_overview", &serde_json::json!({})))
            .collect();
        id += 4;
        let mut text = batch.join("\n");
        text.push('\n');
        if stdin.write_all(text.as_bytes()).is_err() {
            break serve.child.wait().unwrap();
        }
        std::thread::sleep(Duration::from_millis(2));
    };
    assert_eq!(ended.code(), Some(0));
    assert!(peak < 150 * 1024, "RSS peaked at {peak} KiB");
}

#[test]
fn a_burst_of_3000_requests_with_a_reader_gets_every_answer() {
    let (mut serve, mut stdin, stdout) = Serve::start("burst");
    let reader = Reader::fast(stdout);
    let lines: Vec<String> = (0..3000)
        .map(|id| {
            if id % 10 == 5 {
                call(id, "templates_list", &serde_json::json!({}))
            } else {
                ping(id)
            }
        })
        .collect();
    send(&mut stdin, &lines);
    drop(stdin);
    let answers = reader.answers();
    assert_eq!(ids(&answers), (0..3000).collect::<Vec<u64>>());
    let status = serve.exited_within(Duration::from_secs(5)).unwrap();
    assert_eq!(status.code(), Some(0));
}

#[test]
fn a_client_that_never_reads_ends_about_two_seconds_after_its_input() {
    let (mut serve, mut stdin, _unread) = Serve::start("eof");
    let mut lines = vec![INITIALIZE.to_string()];
    lines.extend((1..1000).map(ping));
    lines.extend((1000..1030).map(|id| call(id, "ai_overview", &serde_json::json!({}))));
    send(&mut stdin, &lines);
    drop(stdin);
    let closed = Instant::now();
    let status = serve
        .exited_within(Duration::from_secs(10))
        .expect("serve waited for a client that never reads");
    let waited = closed.elapsed();
    assert_eq!(status.code(), Some(0));
    assert!(
        waited >= Duration::from_millis(1500) && waited < Duration::from_secs(5),
        "{waited:?}"
    );
}

#[test]
fn a_second_signal_ends_serve_at_once_while_it_drains_to_a_slow_reader() {
    let (mut serve, mut stdin, stdout) = Serve::start("signals");
    let mut lines = vec![INITIALIZE.to_string()];
    lines.extend((1..=20).map(|id| call(id, "ai_overview", &serde_json::json!({}))));
    send(&mut stdin, &lines);
    let reader = Reader::paced(stdout, 20 * 1024, 1024);
    while reader.taken() < 70 * 1024 {
        std::thread::sleep(Duration::from_millis(10));
    }
    serve.interrupt();
    assert!(
        serve.exited_within(Duration::from_millis(700)).is_none(),
        "the first signal did not let serve drain"
    );
    serve.interrupt();
    let status = serve
        .exited_within(Duration::from_secs(1))
        .expect("the second signal did not end serve");
    assert_eq!(status.code(), Some(130));
    drop(stdin);
    drop(reader);
}
