#![cfg(test)]

mod common;

use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use common::{Project, ident, pg};

const FLOOD: &str = "
BEGIN;
CREATE TABLE IF NOT EXISTS flood_lock (x int);
LOCK flood_lock IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
  FOR i IN 1..200000 LOOP RAISE NOTICE 'line % %', i, repeat('x', 100); END LOOP;
  PERFORM pg_sleep(60);
END $$;
COMMIT;
";

#[test]
fn a_stderr_nobody_reads_does_not_defeat_the_timeout() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_blocked_stderr");
    pg.admin()
        .exec(&format!("CREATE DATABASE {}", ident(db.name())));
    let p = Project::empty();
    p.write("deploy.sql", FLOOD);
    let started = Instant::now();
    let mut child = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(p.path())
        .args([
            "--connection",
            pg.conn(),
            "-d",
            db.name(),
            "--timeout",
            "4s",
        ])
        .env_clear()
        .env("PGMI_NO_BANNER", "1")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .expect("run pgmi deploy");
    let code = loop {
        if let Some(status) = child.try_wait().expect("wait for pgmi") {
            break status.code();
        }
        if started.elapsed() > Duration::from_secs(30) {
            let _ = child.kill();
            break None;
        }
        std::thread::sleep(Duration::from_millis(100));
    };
    let pipe = child.stderr.take();
    assert_eq!(
        code,
        Some(16),
        "pgmi did not stop at --timeout while stderr was blocked"
    );
    drop(pipe);
    let held: i64 = db.client().one(
        "SELECT count(*) FROM pg_locks l JOIN pg_class c ON c.oid = l.relation WHERE c.relname = 'flood_lock'",
        &[],
    );
    assert_eq!(held, 0, "the deploy's session still holds its lock");
}

fn notices_then_an_error(count: u32) -> String {
    format!(
        "DO $$ BEGIN
  FOR i IN 1..{count} LOOP RAISE NOTICE 'line % %', i, repeat('x', 100); END LOOP;
  RAISE EXCEPTION 'THE_REAL_ERROR';
END $$;"
    )
}

fn deploy_read_by(
    database: &str,
    deploy_sql: &str,
    read: impl FnOnce(std::process::ChildStderr) -> Vec<u8>,
) -> Option<(Option<i32>, String)> {
    let pg = pg()?;
    let db = pg.database(database);
    pg.admin()
        .exec(&format!("CREATE DATABASE {}", ident(db.name())));
    let p = Project::empty();
    p.write("deploy.sql", deploy_sql);
    let mut child = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(p.path())
        .args(["--connection", pg.conn(), "-d", db.name()])
        .env_clear()
        .env("PGMI_NO_BANNER", "1")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .expect("run pgmi deploy");
    let stderr = read(child.stderr.take().expect("stderr"));
    let code = child.wait().expect("wait for pgmi").code();
    Some((code, String::from_utf8_lossy(&stderr).into_owned()))
}

fn ends_with_the_error_and_the_summary(stderr: &str) {
    let tail: Vec<&str> = stderr.lines().rev().take(12).collect();
    assert!(
        tail.iter().any(|l| l.starts_with("FAILED ")),
        "no summary at the end: {tail:?}"
    );
    assert!(
        tail.iter()
            .any(|l| l.starts_with("pgmi: error: ") && l.contains("THE_REAL_ERROR")),
        "no error line at the end: {tail:?}"
    );
}

#[test]
fn a_paused_stderr_still_gets_the_error_and_the_summary() {
    use std::io::Read;
    let Some((code, stderr)) = deploy_read_by(
        "pgmi_rs_paused_stderr",
        &notices_then_an_error(2000),
        |mut pipe| {
            std::thread::sleep(Duration::from_secs(3));
            let mut all = Vec::new();
            pipe.read_to_end(&mut all).expect("read stderr");
            all
        },
    ) else {
        return;
    };
    assert_eq!(code, Some(13));
    assert_eq!(
        stderr.lines().filter(|l| l.starts_with("line ")).count(),
        2000
    );
    ends_with_the_error_and_the_summary(&stderr);
}

#[test]
fn a_slow_stderr_reader_still_gets_the_error_and_the_summary() {
    use std::io::Read;
    let Some((code, stderr)) = deploy_read_by(
        "pgmi_rs_slow_stderr",
        &notices_then_an_error(200_000),
        |mut pipe| {
            let (mut all, mut buf) = (Vec::new(), vec![0u8; 100 << 10]);
            loop {
                match pipe.read(&mut buf).expect("read stderr") {
                    0 => return all,
                    n => all.extend_from_slice(&buf[..n]),
                }
                std::thread::sleep(Duration::from_millis(100));
            }
        },
    ) else {
        return;
    };
    assert_eq!(code, Some(13));
    assert!(
        stderr.contains("server notice(s) not shown: stderr was read more slowly"),
        "no note about the dropped notices"
    );
    ends_with_the_error_and_the_summary(&stderr);
}
