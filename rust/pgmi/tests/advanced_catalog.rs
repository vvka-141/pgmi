#![cfg(test)]

mod b_support;
mod common;

use b_support::{deploy, ok, project, refused, write};
use common::{Db, pg};

const TABLE_PROBE: &str = r#"/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000070" idempotent="false">
  <sortKeys><key>900/000</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE core.probe_status (code text PRIMARY KEY, label text NOT NULL);
CREATE TABLE core.probe_task (id int PRIMARY KEY, status text REFERENCES core.probe_status (code) ON DELETE SET NULL);
CREATE INDEX ix_probe_task_status ON core.probe_task (status);
SELECT pg_temp.exempt_from_rls('core.probe_status', 'test probe');
SELECT pg_temp.exempt_from_rls('core.probe_task', 'test probe');
"#;

const SYNC_PROBE: &str = r#"/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000071" idempotent="true">
  <sortKeys><key>900/001</key></sortKeys>
</pgmi-meta>
*/
SELECT pg_temp.sync_catalog('core.probe_status');
"#;

const CATALOG_FILE: &str = "data/catalogs/core.probe_status.json";
const DEV: &[(&str, &str)] = &[("env", "dev")];

fn statuses(db: &Db) -> String {
    db.client()
        .one::<Option<String>>(
            "SELECT string_agg(code || '=' || label, ',' ORDER BY code) FROM core.probe_status",
            &[],
        )
        .unwrap_or_default()
}

#[test]
fn catalog_sync() {
    let Some(pg) = pg() else { return };
    let p = project(
        "advanced",
        &[
            ("core/catalog_probe.sql", TABLE_PROBE),
            ("core/catalog_probe_sync.sql", SYNC_PROBE),
            (
                CATALOG_FILE,
                r#"[{"code": "open", "label": "Open"}, {"code": "done", "label": "Done"}]"#,
            ),
        ],
    );
    let db = pg.database("pgmi_rs_b_advanced_catalog");
    ok(deploy(&pg, &p, &db, true, DEV), "first deploy");
    assert_eq!(
        statuses(&db),
        "done=Done,open=Open",
        "after the first deploy"
    );

    write(
        &p,
        &[(
            CATALOG_FILE,
            r#"[{"code": "open", "label": "Opened"}, {"code": "blocked", "label": "Blocked"}]"#,
        )],
    );
    ok(
        deploy(&pg, &p, &db, false, DEV),
        "deploy with an edited catalog",
    );
    assert_eq!(
        statuses(&db),
        "blocked=Blocked,open=Opened",
        "after editing the catalog"
    );

    let c = db.client();
    c.exec("INSERT INTO core.probe_task VALUES (1, 'blocked')");
    write(
        &p,
        &[(CATALOG_FILE, r#"[{"code": "open", "label": "Opened"}]"#)],
    );
    let f = refused(
        deploy(&pg, &p, &db, false, DEV),
        "dropping a catalog row that is still referenced",
    );
    for want in [
        format!("./{CATALOG_FILE}"),
        "(blocked), still referenced by core.probe_task".to_string(),
    ] {
        assert!(f.says(&want), "want {want:?} in:\n{}", f.text);
    }
    let status: Option<String> = c.one("SELECT status FROM core.probe_task WHERE id = 1", &[]);
    assert_eq!(
        status.as_deref(),
        Some("blocked"),
        "the refused deploy changed the referencing row"
    );

    write(
        &p,
        &[
            (
                CATALOG_FILE,
                r#"[{"code": "open", "label": "Opened"}, {"code": "blocked", "label": "Blocked"}]"#,
            ),
            ("data/catalogs/core.probe_kind.json", "[]"),
        ],
    );
    let f = refused(
        deploy(&pg, &p, &db, false, DEV),
        "a catalog file no file syncs",
    );
    assert!(
        f.says("./data/catalogs/core.probe_kind.json"),
        "want the error to name ./data/catalogs/core.probe_kind.json, got:\n{}",
        f.text
    );
}

#[test]
fn deploy_history_views() {
    let Some(pg) = pg() else { return };
    let p = project(
        "advanced",
        &[
            ("core/catalog_probe.sql", TABLE_PROBE),
            ("core/catalog_probe_sync.sql", SYNC_PROBE),
            (CATALOG_FILE, "[]"),
        ],
    );
    let db = pg.database("pgmi_rs_b_advanced_history");
    ok(deploy(&pg, &p, &db, true, DEV), "first deploy");
    write(
        &p,
        &[(
            "core/catalog_probe_sync.sql",
            &format!("{}SELECT 1;\n", SYNC_PROBE),
        )],
    );
    ok(deploy(&pg, &p, &db, false, DEV), "second deploy");

    let c = db.client();
    let login: String = c.one("SELECT session_user::text", &[]);
    let got: Vec<(String, String, i64, i64, i64, i64)> = c
        .query(
            "SELECT deployed_by::text, environment::text, scripts_run::bigint, run_once_scripts_run::bigint, new_scripts::bigint, changed_scripts::bigint
             FROM internal.vw_deployment ORDER BY deployed_at, xact_id",
            &[],
        )
        .iter()
        .map(|r| (r.get(0), r.get(1), r.get(2), r.get(3), r.get(4), r.get(5)))
        .collect();
    assert_eq!(got.len(), 2, "want 2 deployments, got {got:?}");
    for (i, d) in got.iter().enumerate() {
        assert!(
            d.0 == login && d.1 == "dev",
            "deployment {}: by {} in {}, want {login} in dev",
            i + 1,
            d.0,
            d.1
        );
    }
    let (first, second) = (&got[0], &got[1]);
    assert!(
        first.2 != 0 && first.3 != 0 && first.4 == first.2 && first.5 == 0,
        "first deployment: {first:?}; want every script new, some run-once, none changed"
    );
    assert!(
        second.2 != 0 && second.3 == 0 && second.4 == 0 && second.5 == 1,
        "second deployment: {second:?}; want no run-once or new script, one changed"
    );

    for (path, idempotent, runs, versions) in [
        ("./core/catalog_probe.sql", false, 1i64, 1i64),
        ("./core/catalog_probe_sync.sql", true, 2, 2),
        ("./lib/internal/views.sql", true, 2, 1),
    ] {
        let rows = c.query(
            "SELECT s.idempotent, s.run_count::bigint, s.content_versions::bigint,
                    s.last_xact_id = (SELECT max(xact_id) FROM internal.deployment)
             FROM internal.vw_deployed_script s WHERE s.path = $1",
            &[&path],
        );
        assert_eq!(rows.len(), 1, "{path} in internal.vw_deployed_script");
        let r = &rows[0];
        let got: (bool, i64, i64, bool) = (r.get(0), r.get(1), r.get(2), r.get(3));
        assert_eq!(
            got,
            (idempotent, runs, versions, idempotent),
            "{path}: idempotent, runs, versions, ran in the last deploy"
        );
    }
}
