#![cfg(all(test, unix))]

mod common;

use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use common::{Project, pg};

fn deploy(pg: &common::Pg, project: &Project, db: &str) -> Command {
    let mut c = Command::new(env!("CARGO_BIN_EXE_pgmi"));
    c.arg("deploy")
        .arg(project.path())
        .args(["--connection", pg.conn(), "-d", db, "--force"])
        .env_clear()
        .env("PGMI_NON_INTERACTIVE", "1")
        .stdin(Stdio::null())
        .stdout(Stdio::null());
    c
}

#[test]
fn a_deploy_whose_stderr_reader_went_away_still_finishes() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_closed_stderr");
    let project = Project::empty();
    project.write(
        "deploy.sql",
        "SELECT pg_sleep(1);\nCREATE TABLE done (n int);\n",
    );
    let mut child = deploy(&pg, &project, db.name())
        .stderr(Stdio::piped())
        .spawn()
        .expect("run pgmi deploy");
    drop(child.stderr.take());
    let status = child.wait().expect("wait for pgmi");
    assert_eq!(status.code(), Some(0), "{status:?}");
    let done: i64 = db
        .client()
        .one("SELECT count(*) FROM pg_class WHERE relname = 'done'", &[]);
    assert_eq!(done, 1);
}

#[test]
fn sighup_cancels_the_running_statement_and_exits_130() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_sighup");
    let project = Project::empty();
    project.write("deploy.sql", "SELECT pg_sleep(60) AS hup_sleep;\n");
    let child = deploy(&pg, &project, db.name())
        .stderr(Stdio::piped())
        .spawn()
        .expect("run pgmi deploy");
    let admin = pg.admin();
    let sleeping = || {
        admin.one::<i64>(
            "SELECT count(*) FROM pg_stat_activity WHERE query LIKE '%hup_sleep%' AND pid <> pg_backend_pid() AND state = 'active'",
            &[],
        )
    };
    let started = Instant::now();
    while sleeping() == 0 {
        assert!(
            started.elapsed() < Duration::from_secs(30),
            "the statement never started"
        );
        std::thread::sleep(Duration::from_millis(50));
    }
    let hup = Command::new("kill")
        .args(["-HUP", &child.id().to_string()])
        .status()
        .expect("run kill");
    assert!(hup.success());
    let out = child.wait_with_output().expect("pgmi output");
    assert_eq!(
        out.status.code(),
        Some(130),
        "{:?} {}",
        out.status,
        String::from_utf8_lossy(&out.stderr)
    );
    let started = Instant::now();
    while sleeping() > 0 {
        assert!(
            started.elapsed() < Duration::from_secs(5),
            "the server still runs the statement"
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}
