#![cfg(test)]

mod common;

use std::process::Command;
use std::str::FromStr;

use common::Project;
use tokio_postgres::{Config, NoTls};

const DB: &str = "pgmi_rs_a_pg18_floor";

fn pg17() -> Option<String> {
    let conn = std::env::vars_os()
        .find(|(k, _)| k == "PGMI_TEST_CONN_PG17")
        .and_then(|(_, v)| v.into_string().ok())
        .filter(|v| !v.is_empty());
    if conn.is_none() {
        eprintln!("skipped: PGMI_TEST_CONN_PG17 (a PostgreSQL 17 server) is not set");
    }
    conn
}

fn on(conn: &str, db: &str, sql: &str, batch: bool) -> Vec<String> {
    let mut config = Config::from_str(conn).expect("PGMI_TEST_CONN_PG17");
    config.dbname(db);
    let host = match config.get_hosts().first() {
        Some(tokio_postgres::config::Host::Tcp(h)) => h.clone(),
        _ => panic!("PGMI_TEST_CONN_PG17 must name a TCP host"),
    };
    let port = config.get_ports().first().copied().unwrap_or(5432);
    let rt = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .expect("runtime");
    rt.block_on(async {
        let stream = tokio::net::TcpStream::connect((host, port))
            .await
            .expect("reach the PostgreSQL 17 server");
        let (client, connection) = config.connect_raw(stream, NoTls).await.expect("connect");
        tokio::spawn(connection);
        if batch {
            client.batch_execute(sql).await.expect("batch");
            return Vec::new();
        }
        let rows = client.query(sql, &[&DB]).await.expect("query");
        rows.iter().map(|r| r.get(0)).collect()
    })
}

#[test]
fn refuses_server_below_postgres_18() {
    let Some(conn) = pg17() else { return };
    on(
        &conn,
        "postgres",
        &format!("DROP DATABASE IF EXISTS {DB} WITH (FORCE)"),
        true,
    );
    let project = Project::init("advanced");
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(project.path())
        .args(["--connection", &conn, "-d", DB, "--overwrite", "--force"])
        .args(["--param", "env=dev"])
        .env_clear()
        .output()
        .expect("run pgmi deploy");
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert_ne!(
        out.status.code(),
        Some(0),
        "deploy on PostgreSQL 17 succeeded; the advanced template must refuse it"
    );
    assert!(
        stderr.contains("requires PostgreSQL 18 or newer") && stderr.contains("17."),
        "want a refusal naming the found version and the 18 requirement, got:\n{stderr}"
    );
    let leaked = on(
        &conn,
        DB,
        "SELECT name FROM (
            SELECT nspname::text AS name FROM pg_namespace WHERE nspname IN ('internal', 'core', 'api', 'common', 'membership', 'extensions')
            UNION ALL
            SELECT rolname::text FROM pg_roles WHERE rolname LIKE $1 || '\\_%'
        ) s ORDER BY name",
        false,
    );
    on(
        &conn,
        "postgres",
        &format!("DROP DATABASE IF EXISTS {DB} WITH (FORCE)"),
        true,
    );
    assert!(
        leaked.is_empty(),
        "refused deploy left objects behind: {leaked:?}"
    );
}
