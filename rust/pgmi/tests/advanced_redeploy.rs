#![cfg(test)]

mod b_support;
mod common;

use b_support::{create_database, deploy, ok, project, refused, unlink, write};
use common::{Project, pg};

const DEV: &[(&str, &str)] = &[("env", "dev")];
const TEST: &[(&str, &str)] = &[
    ("database_admin_password", "TestPassword123!"),
    ("env", "test"),
];

#[test]
fn redeploys_onto_signed_in_people() {
    let Some(pg) = pg() else { return };
    let p = Project::init("advanced");
    let db = pg.database("pgmi_rs_b_advanced_populated");
    ok(deploy(&pg, &p, &db, true, DEV), "first deploy");
    let status: i32 = db.client().one(
        r#"SELECT (api.rest_invoke('POST', '/organizations',
            extensions.hstore(ARRAY['x-login-session', api.open_dev_login_session('dev', 'alice', 'alice@example.com', 'Alice'),
                                    'content-type', 'application/json']),
            convert_to('{"name": "Acme Corp", "slug": "alices-projects"}', 'UTF8'))).status_code"#,
        &[],
    );
    assert_eq!(
        status, 201,
        "self-serve POST /organizations answered {status}, want 201"
    );
    ok(
        deploy(&pg, &p, &db, false, DEV),
        "redeploy onto a database with a signed-in person",
    );
}

const V013_DEPLOY_LOG: &str = "
CREATE SCHEMA internal;
CREATE TABLE internal.deployment_script_execution_log(
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    deployment_script_object_id uuid NOT NULL,
    deployment_script_content_checksum TEXT NOT NULL,
    xact_id xid8 NOT NULL,
    file_path TEXT NOT NULL,
    idempotent BOOLEAN NOT NULL,
    sort_key TEXT,
    executed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    executed_by TEXT NOT NULL DEFAULT CURRENT_USER
);";

#[test]
fn refuses_pre_v014_database() {
    let Some(pg) = pg() else { return };
    let p = Project::init("advanced");
    let db = pg.database("pgmi_rs_b_advanced_pre_v014");
    create_database(&pg, &db);
    let c = db.client();
    c.exec(V013_DEPLOY_LOG);
    let f = refused(
        deploy(&pg, &p, &db, false, DEV),
        "deploy onto a v0.13 database",
    );
    assert!(
        f.sqlstate() == "55000"
            && f.field("error").contains("built by pgmi v0.13 or earlier")
            && f.hint().contains("docs/advanced/UPGRADING.md"),
        "want the pre-v0.14 refusal (55000) with a hint to UPGRADING.md, got:\n{}",
        f.text
    );
    let row = &c.query(
        r"SELECT (SELECT count(*) FROM pg_namespace WHERE nspname IN ('core', 'api', 'membership', 'common', 'extensions')),
                 (SELECT count(*) FROM pg_class WHERE relnamespace = 'internal'::regnamespace),
                 (SELECT count(*) FROM pg_roles WHERE rolname LIKE $1 || '\_%')",
        &[&db.name()],
    )[0];
    let got: (i64, i64, i64) = (row.get(0), row.get(1), row.get(2));
    assert_eq!(
        got,
        (0, 3, 0),
        "the refused deploy changed the database: schemas, relations in internal, roles"
    );
}

fn redeploy_while_holding(name: &str, mode: &str, tables: &[&str]) {
    let Some(pg) = pg() else { return };
    let p = Project::init("advanced");
    let db = pg.database(name);
    pg.deploy(&p, &db).params(TEST).overwrite().run().ok();

    let holder = db.client();
    holder.exec("BEGIN");
    for table in tables {
        holder.exec(&format!("LOCK TABLE {table} IN {mode} MODE"));
    }
    let start = std::time::Instant::now();
    let out = pg
        .deploy(&p, &db)
        .params(TEST)
        .arg("--timeout")
        .arg("1m")
        .run();
    assert_eq!(
        out.code,
        0,
        "redeploy while another session holds {mode} failed after {:?}; the deploy still takes a conflicting lock:\n{}",
        start.elapsed(),
        out.stderr
    );
    holder.exec("ROLLBACK");
}

#[test]
fn redeploy_does_not_lock_read_tables() {
    redeploy_while_holding(
        "pgmi_rs_b_redeploy_locks",
        "ACCESS SHARE",
        &[
            "membership.organization",
            "membership.principal",
            "membership.permission",
            "api.handler",
            "api.rest_route",
            "api.mcp_route",
            "api.vw_current_user",
            "membership.vw_user_principal",
        ],
    );
}

#[test]
fn redeploy_does_not_block_writers() {
    redeploy_while_holding(
        "pgmi_rs_b_redeploy_writers",
        "ROW EXCLUSIVE",
        &[
            "api.exchange",
            "api.rest_exchange",
            "api.rpc_exchange",
            "api.mcp_exchange",
            "membership.principal",
            "membership.user_principal",
            "membership.external_identity",
            "membership.organization",
            "membership.organization_membership",
            "membership.role_assignment",
            "membership.audit_event",
        ],
    );
}

const DRIFT_PROBE_ID: &str = "ffffffff-0446-4000-8000-000000000010";
const DRIFT_PROBE_BODY: &str = "CREATE TABLE core.drift_probe (id int);\nSELECT pg_temp.exempt_from_rls('core.drift_probe', 'test probe');\n";

fn drift_probe(sort_key: &str, body: &str) -> String {
    format!(
        "/*\n<pgmi-meta id=\"{DRIFT_PROBE_ID}\" idempotent=\"false\">\n  <sortKeys><key>{sort_key}</key></sortKeys>\n</pgmi-meta>\n*/\n{body}"
    )
}

type DriftCase = (
    &'static str,
    Vec<(&'static str, String)>,
    Option<&'static str>,
);

#[test]
fn refuses_run_once_drift() {
    let Some(pg) = pg() else { return };
    let original = drift_probe("900/000", DRIFT_PROBE_BODY);
    let cases: Vec<DriftCase> = vec![
        (
            "DDL edited",
            vec![(
                "core/drift_probe.sql",
                drift_probe(
                    "900/000",
                    "CREATE TABLE core.drift_probe (id int, note text);\nSELECT pg_temp.exempt_from_rls('core.drift_probe', 'test probe');\n",
                ),
            )],
            Some("./core/drift_probe.sql: content edited"),
        ),
        (
            "moved",
            vec![(
                "core/moved/drift_probe.sql",
                drift_probe("900/000", DRIFT_PROBE_BODY),
            )],
            Some("./core/moved/drift_probe.sql: moved from ./core/drift_probe.sql"),
        ),
        (
            "re-sorted",
            vec![(
                "core/drift_probe.sql",
                drift_probe("900/500", DRIFT_PROBE_BODY),
            )],
            Some("./core/drift_probe.sql: sort key 900/500, ran at 900/000"),
        ),
        (
            "removed",
            vec![],
            Some("./core/drift_probe.sql: removed from the project"),
        ),
        (
            "comment, whitespace and keyword case edit",
            vec![(
                "core/drift_probe.sql",
                drift_probe(
                    "900/000",
                    "-- the probe table\ncreate   table core.drift_probe\n    (id int); /* unchanged */\nselect pg_temp.exempt_from_rls('core.drift_probe', 'test probe');\n",
                ),
            )],
            None,
        ),
    ];
    let marker = "/*\n<pgmi-meta id=\"ffffffff-0446-4000-8000-000000000011\" idempotent=\"false\">\n  <sortKeys><key>005/000</key></sortKeys>\n</pgmi-meta>\n*/\nCREATE TABLE core.drift_marker (id int);\nSELECT pg_temp.exempt_from_rls('core.drift_marker', 'test probe');\n";
    for (i, (name, second, want)) in cases.into_iter().enumerate() {
        let p = project("advanced", &[("core/drift_probe.sql", &original)]);
        let db = pg.database(&format!("pgmi_rs_b_advanced_drift_{i}"));
        ok(
            deploy(&pg, &p, &db, true, DEV),
            &format!("{name}: first deploy"),
        );
        unlink(&p, &["core/drift_probe.sql"]);
        write(&p, &[("core/drift_marker.sql", marker)]);
        for (rel, content) in &second {
            write(&p, &[(rel, content)]);
        }
        let result = deploy(&pg, &p, &db, false, DEV);
        let c = db.client();
        let marker_applied: bool =
            c.one("SELECT to_regclass('core.drift_marker') IS NOT NULL", &[]);
        let Some(want) = want else {
            ok(result, &format!("{name}: a comment and whitespace edit"));
            let runs: i64 = c.one(
                "SELECT count(*) FROM internal.deployment_script_execution_log WHERE deployment_script_object_id = $1::text::uuid",
                &[&DRIFT_PROBE_ID],
            );
            assert_eq!(runs, 1, "{name}: the probe ran {runs} times, want 1");
            continue;
        };
        let f = refused(
            result,
            &format!("{name}: deploy after a run-once file drifted"),
        );
        assert!(f.says(want), "{name}: want {want:?} in:\n{}", f.text);
        let hint = f.hint();
        assert!(
            hint.contains(&format!("-d {} ", db.name())) && !hint.contains("--force"),
            "{name}: the drift hint must name -d {} and not --force: {hint:?}",
            db.name()
        );
        assert!(
            !marker_applied,
            "{name}: the refused deploy applied a file: core.drift_marker exists"
        );
    }
}

#[test]
fn tail_refuses_invalid_index() {
    let Some(pg) = pg() else { return };
    let p = Project::init("advanced");
    let db = pg.database("pgmi_rs_b_advanced_invalid_index");
    ok(deploy(&pg, &p, &db, true, DEV), "first deploy");
    let c = db.client();
    c.exec("CREATE TABLE core.tail_probe (n int); INSERT INTO core.tail_probe VALUES (1), (1)");
    assert!(
        c.batch("CREATE UNIQUE INDEX CONCURRENTLY tail_probe_n ON core.tail_probe (n)")
            .is_err(),
        "a unique index over duplicate rows was built"
    );
    let f = refused(
        deploy(&pg, &p, &db, false, DEV),
        "deploy with an invalid index in the database",
    );
    assert!(
        f.says("core.tail_probe_n"),
        "want the error to name core.tail_probe_n, got:\n{}",
        f.text
    );
    c.exec("DROP INDEX CONCURRENTLY core.tail_probe_n");
    ok(
        deploy(&pg, &p, &db, false, DEV),
        "deploy after dropping the invalid index",
    );
}

#[test]
fn redeploy_repairs_view_options() {
    let Some(pg) = pg() else { return };
    let p = Project::init("advanced");
    let db = pg.database("pgmi_rs_b_advanced_view_options");
    ok(deploy(&pg, &p, &db, true, DEV), "first deploy");
    let views = ["api.vw_current_user", "membership.vw_user_principal"];
    let c = db.client();
    for v in views {
        c.exec(&format!("ALTER VIEW {v} RESET (security_invoker)"));
    }
    ok(deploy(&pg, &p, &db, false, DEV), "redeploy");
    for v in views {
        let invoker: bool = c.one(
            "SELECT coalesce('security_invoker=true' = ANY (reloptions), false) FROM pg_class WHERE oid = $1::text::regclass",
            &[&v],
        );
        assert!(invoker, "{v} lacks security_invoker after the redeploy");
    }
}

#[test]
fn rejects_sql_file_without_plan_position() {
    let Some(pg) = pg() else { return };
    let p = project(
        "advanced",
        &[(
            "core/my_tables.sql",
            "CREATE TABLE core.untagged_probe (id core.entity_id);\n",
        )],
    );
    let db = pg.database("pgmi_rs_b_advanced_untagged");
    let f = refused(
        deploy(&pg, &p, &db, true, DEV),
        "a SQL file that has no place in the plan",
    );
    assert!(
        f.says("without a place in the plan") && f.says("./core/my_tables.sql"),
        "want an error naming ./core/my_tables.sql, got:\n{}",
        f.text
    );
}

const ADVANCED_TRIMMABLE: &[&[&str]] = &[
    &["gateways/"],
    &["api/dev/"],
    &[
        "lib/core/change-tracking.sql",
        "lib/core/change-tracking-actor.sql",
        "lib/core/change-tracking-functions.sql",
        "lib/__test__/test_change_tracking.sql",
    ],
    &[
        "lib/core/entity-registry.sql",
        "lib/core/entity-registry-functions.sql",
        "lib/__test__/test_entity_registry.sql",
    ],
    &[
        "lib/core/organization-counter.sql",
        "lib/core/organization-counter-functions.sql",
        "lib/__test__/test_organization_counter.sql",
    ],
];

#[test]
fn trimming_list_leaves_a_template_that_deploys_twice() {
    let Some(pg) = pg() else { return };
    for (i, item) in ADVANCED_TRIMMABLE.iter().enumerate() {
        let p = Project::init("advanced");
        for rel in *item {
            assert!(
                p.file(rel).exists(),
                "listed path {rel} is not in the scaffold"
            );
            p.remove(rel.trim_end_matches('/'));
        }
        let db = pg.database(&format!("pgmi_rs_b_advanced_trim_{i}"));
        for overwrite in [true, false] {
            ok(
                deploy(&pg, &p, &db, overwrite, DEV),
                &format!("deploy (overwrite={overwrite}) without {item:?}"),
            );
        }
    }
}
