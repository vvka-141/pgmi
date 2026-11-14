#![cfg(all(test, target_os = "linux"))]

mod common;

use std::process::{Command, Stdio};

use common::{Project, ident, pg};

#[test]
fn a_json_record_that_cannot_be_written_is_not_exit_0() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_json_unwritable");
    pg.admin()
        .exec(&format!("CREATE DATABASE {}", ident(db.name())));
    let p = Project::empty();
    p.write("deploy.sql", "SELECT 1;\n");
    let full = std::fs::OpenOptions::new()
        .write(true)
        .open("/dev/full")
        .expect("open /dev/full");
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(p.path())
        .args(["--connection", pg.conn(), "-d", db.name(), "--json"])
        .env_clear()
        .env("PGMI_NO_BANNER", "1")
        .stdin(Stdio::null())
        .stdout(full)
        .stderr(Stdio::piped())
        .output()
        .expect("run pgmi deploy");
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert_eq!(out.status.code(), Some(1), "{stderr}");
    assert!(
        stderr.contains("could not write the --json record"),
        "{stderr}"
    );
}
