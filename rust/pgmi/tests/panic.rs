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
fn without_the_trigger_nothing_panics() {
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .args(["deploy", "/nonexistent-pgmi-project"])
        .env_clear()
        .output()
        .unwrap();
    assert_eq!(out.status.code(), Some(10));
}
