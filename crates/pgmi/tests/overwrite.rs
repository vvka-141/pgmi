#![cfg(test)]

mod common;

use common::{Project, ident, pg};

static TEMPLATE1: std::sync::Mutex<()> = std::sync::Mutex::new(());

#[test]
fn a_held_overwrite_lock_refuses_a_second_overwrite_with_exit_15() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_overwrite_lock");
    let project = Project::empty();
    project.write(
        "deploy.sql",
        "CREATE TABLE IF NOT EXISTS marker (n int);\nINSERT INTO marker VALUES (1);\n",
    );
    pg.deploy(&project, &db).run().ok();
    let other = pg.admin();
    other.exec(&format!(
        "SELECT pg_advisory_lock(hashtextextended('pgmi.overwrite.' || '{}', 0))",
        db.name()
    ));
    pg.deploy(&project, &db)
        .overwrite()
        .run()
        .fails(15)
        .says(&format!(
            "another pgmi --overwrite of \"{}\" is running",
            db.name()
        ));
    let marker: i64 = db.client().one("SELECT count(*) FROM marker", &[]);
    assert_eq!(
        marker,
        1,
        "the refused overwrite dropped {}",
        ident(db.name())
    );
}

#[cfg(unix)]
#[test]
fn a_signal_after_the_drop_still_recreates_and_restores_the_database() {
    use std::process::{Command, Stdio};
    use std::time::{Duration, Instant};

    let Some(pg) = pg() else { return };
    let _template1 = TEMPLATE1
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner);
    let db = pg.database("pgmi_rs_overwrite_window");
    let owner = "pgmi_rs_overwrite_window_owner";
    let name = ident(db.name());
    let admin = pg.admin();
    admin.exec(&format!("DROP ROLE IF EXISTS {owner}; CREATE ROLE {owner}"));
    admin.exec(&format!(
        "CREATE DATABASE {name} OWNER {owner} CONNECTION LIMIT 17"
    ));
    admin.exec(&format!(
        "ALTER DATABASE {name} SET statement_timeout = '9s';
         COMMENT ON DATABASE {name} IS 'kept across the window';"
    ));
    let project = Project::empty();
    project.write("deploy.sql", "SELECT 1;\n");

    let blocker = pg.admin();
    blocker.exec("BEGIN; COMMENT ON DATABASE template1 IS 'holds CREATE DATABASE'");
    let child = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(project.path())
        .args(["--connection", pg.conn(), "-d", db.name()])
        .args(["--overwrite", "--force", "--json"])
        .env_clear()
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("run pgmi deploy");
    let started = Instant::now();
    while admin.one::<i64>(
        "SELECT count(*) FROM pg_stat_activity
         WHERE wait_event_type = 'Lock' AND query LIKE 'CREATE DATABASE%'",
        &[],
    ) == 0
    {
        assert!(
            started.elapsed() < Duration::from_secs(30),
            "pgmi never reached CREATE DATABASE"
        );
        std::thread::sleep(Duration::from_millis(50));
    }
    let signalled = Command::new("kill")
        .args(["-INT", &child.id().to_string()])
        .status()
        .expect("run kill");
    assert!(signalled.success());
    std::thread::sleep(Duration::from_secs(1));
    blocker.exec("ROLLBACK");
    let out = child.wait_with_output().expect("pgmi deploy output");
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert_eq!(out.status.code(), Some(130), "{stderr}");
    let record: serde_json::Value = serde_json::from_slice(&out.stdout).expect("--json record");
    assert_eq!(record["recreated"], true, "{record}");

    let row = admin.query(
        "SELECT pg_get_userbyid(d.datdba)::text, d.datconnlimit,
                shobj_description(d.oid, 'pg_database'),
                (SELECT s.setconfig FROM pg_db_role_setting s
                 WHERE s.setdatabase = d.oid AND s.setrole = 0)
         FROM pg_database d WHERE d.datname = $1",
        &[&db.name()],
    );
    assert_eq!(
        row.len(),
        1,
        "the signal after the drop lost the database\n{stderr}"
    );
    assert_eq!(row[0].get::<_, String>(0), owner);
    assert_eq!(row[0].get::<_, i32>(1), 17);
    assert_eq!(
        row[0].get::<_, Option<String>>(2).as_deref(),
        Some("kept across the window")
    );
    assert_eq!(
        row[0].get::<_, Option<Vec<String>>>(3),
        Some(vec!["statement_timeout=9s".to_string()])
    );
    drop(db);
    admin.exec(&format!("DROP ROLE IF EXISTS {owner}"));
}

#[test]
fn a_failed_create_after_the_drop_says_the_database_is_gone() {
    let Some(pg) = pg() else { return };
    let _template1 = TEMPLATE1
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner);
    let db = pg.database("pgmi_rs_overwrite_dropped");
    let project = Project::empty();
    project.write("deploy.sql", "SELECT 1;\n");
    pg.deploy(&project, &db).run().ok();
    let holder = pg.client_for("template1");
    let out = pg
        .deploy(&project, &db)
        .overwrite()
        .arg("--json")
        .run()
        .fails(13);
    let sentence = format!(
        "database \"{}\" was dropped and not recreated; run the deploy again",
        db.name()
    );
    out.says(&sentence);
    assert!(
        out.record()["error"]
            .as_str()
            .is_some_and(|m| m.contains(&sentence)),
        "{}",
        out.record()
    );
    drop(holder);
}
