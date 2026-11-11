#![cfg(test)]

mod adv;
mod common;

use adv::{PW, deployed, scaffold};
use common::{Client, pg};

const OK_BODY: &str =
    "BEGIN\n RETURN api.json_response(200, jsonb_build_object('ok', true));\nEND;";
const WRITE_BODY: &str = "BEGIN\n INSERT INTO public.policy_probe VALUES (1);\n RETURN api.json_response(200, jsonb_build_object('ok', true));\nEND;";

fn register(c: &Client, id: &str, path: &str, name: &str, meta: &str, body: &str) {
    let full = format!(
        r#"{{"id":"{id}","path":"{path}","httpMethod":"^GET$","name":"{name}","access":"public"{meta}}}"#
    );
    c.query(
        "SELECT api.create_or_replace_rest_handler($1::text::jsonb, $2)::text",
        &[&full, &body],
    );
}

fn invoke(c: &Client, begin: &str, url: &str) -> i32 {
    c.exec(begin);
    let status = c.try_query(
        "SELECT (api.rest_invoke('GET', $1, ''::extensions.hstore, NULL::bytea)).status_code",
        &[&url],
    );
    let _ = c.batch("ROLLBACK");
    match status {
        Ok(rows) => rows[0].get(0),
        Err(e) => panic!("rest_invoke {url} after {begin}: {}", common::describe(&e)),
    }
}

#[test]
fn transaction_isolation() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_a_tx_isolation");
    deployed(
        &pg,
        &scaffold(&[]),
        &db,
        true,
        &[("database_admin_password", PW), ("env", "test")],
    );
    let c = db.client();
    register(
        &c,
        "eeeeeeee-7401-4000-8000-000000000001",
        "/iso-ser",
        "iso_ser",
        r#","minTransactionIsolation":"serializable""#,
        OK_BODY,
    );
    register(
        &c,
        "eeeeeeee-7402-4000-8000-000000000001",
        "/iso-rr",
        "iso_rr",
        r#","minTransactionIsolation":"repeatable read""#,
        OK_BODY,
    );
    for (name, iso, url, want) in [
        (
            "serializable floor rejects read committed",
            "READ COMMITTED",
            "/iso-ser",
            428,
        ),
        (
            "serializable floor rejects repeatable read",
            "REPEATABLE READ",
            "/iso-ser",
            428,
        ),
        (
            "serializable floor accepts serializable",
            "SERIALIZABLE",
            "/iso-ser",
            200,
        ),
        (
            "repeatable read floor rejects read committed",
            "READ COMMITTED",
            "/iso-rr",
            428,
        ),
        (
            "repeatable read floor accepts repeatable read",
            "REPEATABLE READ",
            "/iso-rr",
            200,
        ),
        (
            "repeatable read floor accepts serializable",
            "SERIALIZABLE",
            "/iso-rr",
            200,
        ),
    ] {
        assert_eq!(
            invoke(&c, &format!("BEGIN ISOLATION LEVEL {iso}"), url),
            want,
            "{name}"
        );
    }
}

#[derive(Debug)]
struct Policy {
    isolation: Option<String>,
    read_only: bool,
    deferrable: bool,
}

impl Policy {
    fn begin(&self) -> String {
        let mut s = "BEGIN".to_string();
        if let Some(iso) = &self.isolation {
            s.push_str(&format!(" ISOLATION LEVEL {iso}"));
        }
        if self.read_only {
            s.push_str(" READ ONLY");
        }
        if self.deferrable {
            s.push_str(" DEFERRABLE");
        }
        s
    }
}

fn resolve(c: &Client, url: &str, requested: Option<&str>) -> Policy {
    let row = &c.query(
        "SELECT transaction_isolation, transaction_read_only, transaction_deferrable FROM api.rest_route_policy($1, $2, $3)",
        &[&"GET", &url, &requested],
    )[0];
    Policy {
        isolation: row.get(0),
        read_only: row.get(1),
        deferrable: row.get(2),
    }
}

#[test]
fn transaction_policy() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_a_tx_policy");
    deployed(
        &pg,
        &scaffold(&[]),
        &db,
        true,
        &[("database_admin_password", PW), ("env", "test")],
    );
    let c = db.client();
    register(
        &c,
        "eeeeeeee-7501-4000-8000-000000000001",
        "/ro-report",
        "ro_report",
        r#","readOnly":true,"autoLog":false,"minTransactionIsolation":"repeatable read""#,
        OK_BODY,
    );
    register(
        &c,
        "eeeeeeee-7502-4000-8000-000000000001",
        "/ro-ser",
        "ro_ser",
        r#","readOnly":true,"autoLog":false,"minTransactionIsolation":"serializable""#,
        OK_BODY,
    );
    c.exec("CREATE TABLE IF NOT EXISTS public.policy_probe(x int)");
    register(
        &c,
        "eeeeeeee-7503-4000-8000-000000000001",
        "/ro-writer",
        "ro_writer",
        r#","readOnly":true,"autoLog":false"#,
        WRITE_BODY,
    );
    register(
        &c,
        "eeeeeeee-7504-4000-8000-000000000001",
        "/rw-writer",
        "rw_writer",
        "",
        WRITE_BODY,
    );

    let p = resolve(&c, "/ro-report", None);
    assert!(
        p.isolation.as_deref() == Some("repeatable read") && p.read_only && !p.deferrable,
        "no request resolves the floor and read-only: expected (repeatable read, ro, not deferrable), got {p:?}"
    );
    let p = resolve(&c, "/ro-report", Some("read committed"));
    assert_eq!(
        p.isolation.as_deref(),
        Some("repeatable read"),
        "weaker request cannot downgrade: expected the floor to win, got {p:?}"
    );
    let p = resolve(&c, "/ro-report", Some("serializable"));
    assert!(
        p.isolation.as_deref() == Some("serializable") && p.deferrable,
        "stronger request escalates and turns deferrable: got {p:?}"
    );

    let p = resolve(&c, "/ro-report", None);
    assert_eq!(
        invoke(&c, &p.begin(), "/ro-report"),
        200,
        "resolved characteristics dispatch the route"
    );
    let p = resolve(&c, "/ro-ser", None);
    assert!(
        p.isolation.as_deref() == Some("serializable") && p.read_only && p.deferrable,
        "expected SERIALIZABLE READ ONLY DEFERRABLE, got {p:?}"
    );
    assert_eq!(
        invoke(&c, &p.begin(), "/ro-ser"),
        200,
        "serializable read-only deferrable dispatches"
    );

    assert_eq!(
        invoke(&c, "BEGIN ISOLATION LEVEL REPEATABLE READ", "/ro-report"),
        428,
        "read-write transaction is rejected fail-closed"
    );
    assert_eq!(
        invoke(&c, "BEGIN READ ONLY", "/rw-writer"),
        428,
        "read-write route in a read-only transaction asks the caller to reopen"
    );

    let p = resolve(&c, "/ro-writer", None);
    assert!(p.read_only, "expected read-only policy, got {p:?}");
    assert_eq!(
        invoke(&c, &p.begin(), "/ro-writer"),
        500,
        "write in a read-only handler is a hard error, sanitized as 500"
    );
    let rows: i64 = c.one("SELECT count(*) FROM public.policy_probe", &[]);
    assert_eq!(rows, 0, "READ ONLY transaction must prevent the write");
}
