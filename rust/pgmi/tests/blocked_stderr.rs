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
