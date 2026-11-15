#![cfg(all(test, unix))]

mod common;

use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use common::{Project, ident, pg};

const BIG_RECORD: &str = "
DO $$ BEGIN
  FOR i IN 1..400 LOOP RAISE NOTICE 'line % %', i, repeat('x', 2000); END LOOP;
END $$;
CREATE TABLE done_marker ();
";

#[test]
fn a_signal_ends_pgmi_while_the_json_record_waits_on_a_stdout_nobody_reads() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_blocked_stdout");
    pg.admin()
        .exec(&format!("CREATE DATABASE {}", ident(db.name())));
    let p = Project::empty();
    p.write("deploy.sql", BIG_RECORD);
    let mut child = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(p.path())
        .args(["--connection", pg.conn(), "-d", db.name(), "--json"])
        .env_clear()
        .env("PGMI_NO_BANNER", "1")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .expect("run pgmi deploy");
    let started = Instant::now();
    let done = "SELECT count(*) FROM pg_catalog.pg_class WHERE relname = 'done_marker'";
    while db.client().one::<i64>(done, &[]) == 0 {
        assert!(
            started.elapsed() < Duration::from_secs(30),
            "the deploy never committed"
        );
        std::thread::sleep(Duration::from_millis(100));
    }
    std::thread::sleep(Duration::from_millis(500));
    assert!(
        child.try_wait().expect("wait for pgmi").is_none(),
        "pgmi exited although nobody read its record"
    );
    let killed = Command::new("kill")
        .args(["-TERM", &child.id().to_string()])
        .status()
        .expect("run kill");
    assert!(killed.success());
    let signalled = Instant::now();
    let code = loop {
        if let Some(status) = child.try_wait().expect("wait for pgmi") {
            break status.code();
        }
        if signalled.elapsed() > Duration::from_secs(3) {
            let _ = child.kill();
            let _ = child.wait();
            break None;
        }
        std::thread::sleep(Duration::from_millis(50));
    };
    assert_eq!(code, Some(130), "SIGTERM did not end pgmi within 3 s");
}
