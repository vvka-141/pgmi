#![cfg(all(test, target_os = "linux"))]

mod common;

use std::process::{Command, Stdio};

use common::Project;

fn root() -> bool {
    std::fs::read_to_string("/proc/self/status")
        .ok()
        .and_then(|s| {
            s.lines().find_map(|l| {
                l.strip_prefix("Uid:")
                    .map(|u| u.split_whitespace().nth(1) == Some("0"))
            })
        })
        .unwrap_or(true)
}

#[test]
fn a_runtime_that_cannot_start_still_writes_the_json_record() {
    if root() {
        return;
    }
    let p = Project::empty();
    p.write("deploy.sql", "SELECT 1;\n");
    let out = Command::new("prlimit")
        .arg("--nproc=1")
        .arg("--")
        .arg(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(p.path())
        .args(["--connection", "postgresql://u@127.0.0.1:1/x", "--json"])
        .env_clear()
        .env("PGMI_NO_BANNER", "1")
        .stdin(Stdio::null())
        .output()
        .expect("run pgmi deploy under prlimit");
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert_eq!(out.status.code(), Some(1), "{stderr}");
    assert!(
        stderr.contains("the system refused a new thread"),
        "{stderr}"
    );
    let record: serde_json::Value = serde_json::from_slice(&out.stdout).expect("one JSON record");
    assert_eq!(record["status"], "failed");
    assert_eq!(record["exitCode"], 1);
}
