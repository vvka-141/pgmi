#![cfg(feature = "testhooks")]

use std::process::Command;

#[test]
fn a_panic_exits_3_and_still_prints_the_json_record() {
    let dir = std::env::temp_dir().join(format!("pgmi-panic-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    std::fs::write(dir.join("deploy.sql"), "SELECT 1;\n").unwrap();
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .args(["deploy", dir.to_str().unwrap(), "--json"])
        .args(["--connection", "postgresql://nobody@127.0.0.1:1/target"])
        .env_clear()
        .env("PGMI_TEST_PANIC", "1")
        .output()
        .unwrap();
    std::fs::remove_dir_all(&dir).unwrap();
    assert_eq!(out.status.code(), Some(3));
    let record: serde_json::Value = serde_json::from_slice(&out.stdout).unwrap();
    assert_eq!(record["exitCode"], 3);
    assert_eq!(record["status"], "failed");
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(stderr.contains("PGMI_TEST_PANIC"), "{stderr}");
}

#[test]
fn a_panicking_serve_tool_answers_is_error_and_the_session_goes_on() {
    use std::io::{BufRead, Write};
    let dir = std::env::temp_dir().join(format!("pgmi-panic-serve-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    std::fs::write(dir.join("deploy.sql"), "SELECT 1;\n").unwrap();
    let mut child = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("serve")
        .env_clear()
        .env("PGMI_TEST_PANIC", "1")
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::null())
        .spawn()
        .unwrap();
    let call = serde_json::json!({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "deploy", "arguments": {"path": dir, "connection": "postgresql://nobody@127.0.0.1:1/target"}}});
    let mut stdin = child.stdin.take().unwrap();
    writeln!(stdin, "{call}").unwrap();
    writeln!(
        stdin,
        "{{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"ping\"}}"
    )
    .unwrap();
    let mut answers: Vec<serde_json::Value> = Vec::new();
    let mut lines = std::io::BufReader::new(child.stdout.take().unwrap()).lines();
    while answers.len() < 2 {
        answers.push(serde_json::from_str(&lines.next().unwrap().unwrap()).unwrap());
    }
    drop(stdin);
    let status = child.wait().unwrap();
    std::fs::remove_dir_all(&dir).unwrap();
    assert_eq!(status.code(), Some(0));
    let call = answers.iter().find(|a| a["id"] == 1).unwrap();
    assert_eq!(call["result"]["isError"], true);
    assert_eq!(call["result"]["structuredContent"]["exitCode"], 3);
    assert!(call["result"]["structuredContent"]["stack"].is_string());
    assert!(answers.iter().any(|a| a["id"] == 2));
}

#[test]
fn without_the_trigger_nothing_panics() {
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .args(["deploy", "/nonexistent-pgmi-project"])
        .env_clear()
        .output()
        .unwrap();
    assert_eq!(out.status.code(), Some(10));
}
