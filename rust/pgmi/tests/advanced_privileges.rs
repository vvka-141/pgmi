#![cfg(test)]

mod b_support;
mod common;

use b_support::{create_database, deploy, ok, project, refused, write};
use common::{Project, ident, pg};

const DEV: &[(&str, &str)] = &[("env", "dev")];

const CLOSED_ROUTINES_PROBE: &str = r#"/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000030" idempotent="true">
  <sortKeys><key>900/000</key></sortKeys>
</pgmi-meta>
*/
CREATE SCHEMA IF NOT EXISTS probe;
CREATE OR REPLACE FUNCTION core.closed_probe() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;
CREATE OR REPLACE FUNCTION probe.opened_probe() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;
GRANT EXECUTE ON FUNCTION probe.opened_probe() TO PUBLIC;
"#;

const CLOSED_ROUTINES_TEST_PROBE: &str = r"CREATE FUNCTION core.test_file_probe() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;
DO $$
BEGIN
    IF has_function_privilege('public', 'core.test_file_probe()', 'EXECUTE') THEN
        RAISE EXCEPTION 'TEST FAILED: a routine created by a test file is executable by PUBLIC';
    END IF;
END $$;
";

fn approve_schema(p: &Project, schema: &str) {
    let public = "           ('public',     NULL,                   NULL)";
    assert!(
        p.read("deploy/settings.sql").contains(public),
        "deploy/settings.sql has no public row in pg_temp.approved_schema_grant()"
    );
    p.replace(
        "deploy/settings.sql",
        public,
        &format!("           ('{schema}', NULL, NULL),\n{public}"),
    );
}

#[test]
fn closed_by_default() {
    let Some(pg) = pg() else { return };
    let p = project(
        "advanced",
        &[
            ("core/closed_probe.sql", CLOSED_ROUTINES_PROBE),
            (
                "core/__test__/test_closed_probe.sql",
                CLOSED_ROUTINES_TEST_PROBE,
            ),
        ],
    );
    approve_schema(&p, "probe");
    let db = pg.database("pgmi_rs_b_advanced_closed");
    create_database(&pg, &db);
    let c = db.client();
    c.exec(
        "CREATE EXTENSION hstore SCHEMA public; CREATE TABLE public.preinstalled (h public.hstore)",
    );

    ok(deploy(&pg, &p, &db, false, DEV), "deploy");

    let row = &c.query(
        "SELECT e.extnamespace::regnamespace::text, format_type(a.atttypid, a.atttypmod)
         FROM pg_extension e, pg_attribute a
         WHERE e.extname = 'hstore' AND a.attrelid = 'public.preinstalled'::regclass AND a.attname = 'h'",
        &[],
    )[0];
    let (schema, column_type): (String, String) = (row.get(0), row.get(1));
    assert_eq!(
        (schema.as_str(), column_type.as_str()),
        ("extensions", "extensions.hstore"),
        "hstore's schema and the column type"
    );

    for f in ["core.closed_probe()", "probe.opened_probe()"] {
        let open: bool = c.one(
            "SELECT has_function_privilege('public', $1, 'EXECUTE')",
            &[&f],
        );
        assert!(!open, "{f} is executable by PUBLIC");
    }

    let public_routines: String = c.one(
        "SELECT coalesce(string_agg(p.oid::regprocedure::text, ', '), '')
         FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
         WHERE n.nspname IN ('api', 'core', 'common', 'internal', 'membership', 'probe')
           AND has_function_privilege('public', p.oid, 'EXECUTE')",
        &[],
    );
    assert!(
        public_routines.is_empty(),
        "routines executable by PUBLIC: {public_routines}"
    );

    let row = &c.query(
        "SELECT has_database_privilege('public', current_database(), 'CONNECT'),
                has_database_privilege('public', current_database(), 'TEMPORARY'),
                has_database_privilege(current_database() || '_admin', current_database(), 'CONNECT'),
                has_database_privilege(current_database() || '_admin', current_database(), 'TEMPORARY')",
        &[],
    )[0];
    let (public_connect, public_temp, admin_connect, admin_temp): (bool, bool, bool, bool) =
        (row.get(0), row.get(1), row.get(2), row.get(3));
    assert!(
        !public_connect && !public_temp,
        "PUBLIC keeps CONNECT={public_connect} TEMPORARY={public_temp} on the database"
    );
    assert!(
        admin_connect && admin_temp,
        "the admin role lacks CONNECT={admin_connect} TEMPORARY={admin_temp}"
    );
}

#[test]
fn refuses_extension_that_cannot_move() {
    let Some(pg) = pg() else { return };
    let p = Project::init("advanced");
    let db = pg.database("pgmi_rs_b_advanced_stuck_extension");
    create_database(&pg, &db);
    let c = db.client();
    c.exec(
        "CREATE EXTENSION hstore SCHEMA public;
         UPDATE pg_extension SET extrelocatable = false WHERE extname = 'hstore'",
    );
    let f = refused(
        deploy(&pg, &p, &db, false, DEV),
        "deploy with hstore stuck in public",
    );
    assert!(
        f.says("Extension hstore is installed in schema public, cannot be moved"),
        "want the refusal to name hstore and public, got:\n{}",
        f.text
    );
    let schemas: i64 = c.one(
        "SELECT count(*) FROM pg_namespace WHERE nspname IN ('internal', 'core', 'api', 'membership')",
        &[],
    );
    assert_eq!(schemas, 0, "the refused deploy created {schemas} schemas");
}

const ACCESS_PROBE_CODE: &str = r#"/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000031" idempotent="true">
  <sortKeys><key>900/000</key></sortKeys>
</pgmi-meta>
*/
CREATE OR REPLACE FUNCTION common.access_probe_fn() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;
CREATE OR REPLACE FUNCTION core.access_probe_trg() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;
DO $$ BEGIN EXECUTE format('GRANT EXECUTE ON FUNCTION common.access_probe_fn() TO %I', current_database() || '_kernel'); END $$;
"#;

const ACCESS_PROBE_STRUCTURE: &str = r#"/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000032" idempotent="false">
  <sortKeys><key>900/001</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE core.access_probe (id int, note text);
SELECT pg_temp.exempt_from_rls('core.access_probe', 'test probe');
ALTER TABLE core.access_probe ENABLE ROW LEVEL SECURITY;
CREATE POLICY access_probe_read ON core.access_probe FOR SELECT USING (true);
CREATE TRIGGER access_probe_trg BEFORE INSERT ON core.access_probe FOR EACH ROW EXECUTE FUNCTION core.access_probe_trg();
DO $$
BEGIN
    EXECUTE format('GRANT SELECT ON core.access_probe TO %I', current_database() || '_admin');
    EXECUTE format('GRANT UPDATE (note) ON core.access_probe TO %I', current_database() || '_admin');
END $$;
"#;

fn access_revoke_probe(declared: &str) -> String {
    format!(
        r#"/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000033" idempotent="true">
  <sortKeys><key>900/002</key></sortKeys>
</pgmi-meta>
*/
DROP POLICY IF EXISTS access_probe_read ON core.access_probe;
DROP TRIGGER IF EXISTS access_probe_trg ON core.access_probe;
DO $$
DECLARE
    v_admin text := current_database() || '_admin';
BEGIN
    EXECUTE format('REVOKE SELECT ON core.access_probe FROM %I', v_admin);
    EXECUTE format('REVOKE UPDATE (note) ON core.access_probe FROM %I', v_admin);
    EXECUTE format('REVOKE EXECUTE ON FUNCTION common.access_probe_fn() FROM %I', current_database() || '_kernel');
    {declared}
END $$;
"#
    )
}

#[test]
fn access_baseline() {
    let Some(pg) = pg() else { return };
    let p = project(
        "advanced",
        &[
            ("core/access_probe_code.sql", ACCESS_PROBE_CODE),
            ("core/access_probe.sql", ACCESS_PROBE_STRUCTURE),
        ],
    );
    let db = pg.database("pgmi_rs_b_advanced_access");
    ok(deploy(&pg, &p, &db, true, DEV), "first deploy");

    write(&p, &[("core/revoke_probe.sql", &access_revoke_probe(""))]);
    let f = refused(
        deploy(&pg, &p, &db, false, DEV),
        "a file took away existing grants, a policy and a trigger",
    );
    let admin = format!("{}_admin", db.name());
    for want in [
        "took away grants, policies or triggers".to_string(),
        format!("core.access_probe: grant SELECT to {admin}"),
        format!("core.access_probe.note: grant UPDATE to {admin}"),
        format!(
            "common.access_probe_fn(): grant EXECUTE to {}_kernel",
            db.name()
        ),
        "core.access_probe: policy access_probe_read".to_string(),
        "core.access_probe: trigger access_probe_trg".to_string(),
    ] {
        assert!(f.says(&want), "want {want:?} in:\n{}", f.text);
    }

    write(
        &p,
        &[(
            "core/revoke_probe.sql",
            &access_revoke_probe(
                "INSERT INTO pg_temp.access_removal VALUES
        ('core.access_probe', 'grant SELECT to ' || v_admin),
        ('core.access_probe.note', 'grant UPDATE to ' || v_admin),
        ('common.access_probe_fn()', 'grant EXECUTE to ' || current_database() || '_kernel'),
        ('core.access_probe', 'policy access_probe_read'),
        ('core.access_probe', 'trigger access_probe_trg');",
            ),
        )],
    );
    ok(
        deploy(&pg, &p, &db, false, DEV),
        "deploy with the removals declared",
    );
}

const PROVIDER_ROLES: [&str; 3] = ["pgmi_lbx_reader", "pgmi_lbx_writer", "pgmi_lbx_control"];

/// What Lakebase puts in every database: the schema `__db_system` and event
/// triggers that grant its roles on every new schema and table.
fn provider_fixture(reader: &str, writer: &str, control: &str) -> String {
    r"
CREATE SCHEMA __db_system AUTHORIZATION {control};

CREATE FUNCTION public.grant_usage_on_new_schema() RETURNS event_trigger LANGUAGE plpgsql AS $$
DECLARE c record;
BEGIN
    FOR c IN SELECT object_identity FROM pg_event_trigger_ddl_commands() WHERE command_tag = 'CREATE SCHEMA' LOOP
        EXECUTE format('GRANT USAGE, CREATE ON SCHEMA %s TO {writer}', c.object_identity);
        EXECUTE format('GRANT USAGE ON SCHEMA %s TO {reader}', c.object_identity);
    END LOOP;
END $$;

CREATE FUNCTION public.grant_select_on_new_objects() RETURNS event_trigger LANGUAGE plpgsql AS $$
DECLARE c record;
BEGIN
    FOR c IN SELECT object_identity FROM pg_event_trigger_ddl_commands()
             WHERE command_tag IN ('CREATE TABLE', 'CREATE VIEW') AND object_type IN ('table', 'view') LOOP
        EXECUTE format('GRANT SELECT ON %s TO {reader}', c.object_identity);
        EXECUTE format('GRANT ALL ON %s TO {control}', c.object_identity);
    END LOOP;
END $$;

CREATE EVENT TRIGGER on_create_schema ON ddl_command_end WHEN TAG IN ('CREATE SCHEMA')
    EXECUTE FUNCTION public.grant_usage_on_new_schema();
CREATE EVENT TRIGGER on_create_table_or_view ON ddl_command_end WHEN TAG IN ('CREATE TABLE', 'CREATE VIEW')
    EXECUTE FUNCTION public.grant_select_on_new_objects();
"
        .replace("{reader}", reader)
        .replace("{writer}", writer)
        .replace("{control}", control)
}

fn drop_provider_roles(pg: &common::Pg, db: &str) {
    let admin = pg.admin();
    let mut roles: Vec<String> = ["_admin", "_api", "_kernel", "_owner", "_jobs"]
        .iter()
        .map(|s| format!("{db}{s}"))
        .collect();
    roles.extend(PROVIDER_ROLES.iter().map(|r| (*r).to_string()));
    for role in roles {
        let _ = admin.batch(&format!("DROP ROLE IF EXISTS {}", ident(&role)));
    }
}

#[test]
#[expect(
    clippy::too_many_lines,
    reason = "one provider setup, then the deploy and its checks"
)]
fn deploys_beside_provider_grants() {
    let Some(pg) = pg() else { return };
    let name = "pgmi_rs_b_advanced_provider";
    let db = pg.database(name);
    drop_provider_roles(&pg, name);
    let admin = pg.admin();
    for role in PROVIDER_ROLES {
        admin.exec(&format!("CREATE ROLE {role} NOLOGIN"));
    }
    create_database(&pg, &db);
    let target = db.client();
    target.exec(&provider_fixture(
        "pgmi_lbx_reader",
        "pgmi_lbx_writer",
        "pgmi_lbx_control",
    ));

    let p = Project::init("advanced");
    let mut params = vec![
        ("env", "test"),
        ("database_admin_password", "TestPassword123!"),
    ];
    let f = refused(
        deploy(&pg, &p, &db, false, &params),
        "without the provider settings",
    );
    assert!(
        f.says("TEST FAILED"),
        "without the provider settings a guard must refuse the provider's grants, got:\n{}",
        f.text
    );

    params.push(("provider_role_pattern", r"pgmi\_lbx\_%"));
    params.push(("provider_schemas", r#"["__db_system"]"#));
    for run in ["first", "second"] {
        ok(
            deploy(&pg, &p, &db, false, &params),
            &format!("{run} deploy with the provider settings"),
        );
    }

    for schema in [
        "internal",
        "core",
        "api",
        "common",
        "membership",
        "extensions",
    ] {
        let row = &target.query(
            "SELECT has_schema_privilege('pgmi_lbx_writer', $1, 'CREATE'), has_schema_privilege('pgmi_lbx_writer', $1, 'USAGE')",
            &[&schema],
        )[0];
        let (create, usage): (bool, bool) = (row.get(0), row.get(1));
        assert!(
            !create && usage,
            "the provider's writer must keep USAGE and lose CREATE on {schema} (a schema on a search_path), got CREATE={create} USAGE={usage}"
        );
    }

    let row = &target.query(
        "SELECT has_table_privilege('pgmi_lbx_control', 'membership.organization', 'TRUNCATE')
             OR has_table_privilege('pgmi_lbx_control', 'membership.organization', 'TRIGGER')
             OR has_table_privilege('pgmi_lbx_control', 'membership.organization', 'REFERENCES')
             OR has_table_privilege('pgmi_lbx_control', 'membership.organization', 'MAINTAIN'),
                has_table_privilege('pgmi_lbx_control', 'membership.organization', 'SELECT, INSERT, UPDATE, DELETE')",
        &[],
    )[0];
    let (whole, rows): (bool, bool) = (row.get(0), row.get(1));
    assert!(
        !whole && rows,
        "the provider's admin group must lose TRUNCATE, TRIGGER, REFERENCES and MAINTAIN and keep its row privileges, got whole-table={whole} rows={rows}"
    );

    let logs = [
        "api.access_denial",
        "api.exchange",
        "api.rest_exchange",
        "api.rpc_exchange",
        "api.mcp_exchange",
    ];
    for table in logs {
        let granted: bool = target.one(
            "SELECT has_table_privilege('pgmi_lbx_reader', $1, 'SELECT')",
            &[&table],
        );
        assert!(
            granted,
            "the provider's trigger must have granted its reader {table} (else this test proves nothing)"
        );
    }

    target.exec("BEGIN");
    target.exec(
        "INSERT INTO api.access_denial (bucket, handler_object_id, reason, organization_object_id, actor_kind)
         SELECT date_trunc('minute', now()), object_id, 'permission_missing', gen_random_uuid(), 'human' FROM api.handler LIMIT 1",
    );
    target.exec(
        "INSERT INTO api.rest_exchange (handler_object_id, context, request)
         SELECT object_id, jsonb_build_object('organizationId', gen_random_uuid()),
                ROW('POST', '/x', ''::extensions.hstore, convert_to('secret body', 'UTF8'), '{}'::jsonb)::api.rest_request
         FROM api.handler LIMIT 1",
    );
    target.exec("SET LOCAL ROLE pgmi_lbx_reader");
    for table in logs {
        let seen: i64 = target.one(&format!("SELECT count(*) FROM {table}"), &[]);
        assert_eq!(
            seen, 0,
            "the provider's reader read {seen} row(s) of {table}; row-level security must hide every organization's rows from it"
        );
    }
    target.exec("ROLLBACK");
    drop(target);
    drop(db);
    drop_provider_roles(&pg, name);
}

const LAKEBASE_ROLES: [&str; 3] = [
    "databricks_reader_874",
    "databricks_writer_874",
    "databricks_superuser",
];

#[test]
fn lakebase_is_detected_and_deploys_without_provider_settings() {
    let Some(pg) = pg() else { return };
    let name = "pgmi_rs_b_advanced_lakebase";
    let db = pg.database(name);
    let admin = pg.admin();
    let drop_roles = || {
        for role in ["_admin", "_api", "_kernel", "_owner", "_jobs"]
            .iter()
            .map(|s| format!("{name}{s}"))
            .chain(LAKEBASE_ROLES.iter().map(|r| (*r).to_string()))
        {
            let _ = admin.batch(&format!("DROP ROLE IF EXISTS {}", ident(&role)));
        }
    };
    drop_roles();
    for role in LAKEBASE_ROLES {
        admin.exec(&format!("CREATE ROLE {role} NOLOGIN"));
    }
    create_database(&pg, &db);
    let target = db.client();
    target.exec(&provider_fixture(
        LAKEBASE_ROLES[0],
        LAKEBASE_ROLES[1],
        LAKEBASE_ROLES[2],
    ));
    let p = Project::init("advanced");
    let params = [
        ("env", "test"),
        ("database_admin_password", "TestPassword123!"),
    ];
    for run in ["first", "second"] {
        ok(
            deploy(&pg, &p, &db, false, &params),
            &format!("{run} deploy on a Lakebase-shaped database, no provider settings"),
        );
    }
    let create: bool = target.one(
        "SELECT has_schema_privilege('databricks_writer_874', 'core', 'CREATE')",
        &[],
    );
    assert!(
        !create,
        "the detected pattern must revoke the provider's CREATE on core"
    );
    drop(target);
    drop(db);

    let dev_name = format!("{name}_dev");
    let dev = pg.database(&dev_name);
    create_database(&pg, &dev);
    dev.client().exec(&provider_fixture(
        LAKEBASE_ROLES[0],
        LAKEBASE_ROLES[1],
        LAKEBASE_ROLES[2],
    ));
    let out = ok(
        deploy(&pg, &p, &dev, false, &[("env", "dev")]),
        "env=dev on a Lakebase-shaped database",
    );
    assert!(
        out.stderr.contains("set to a generated value"),
        "the dev password must be generated on Lakebase, which refuses postgres:\n{}",
        out.stderr
    );
    assert!(
        common::Client::try_connect(&dev, &format!("{dev_name}_admin"), "postgres").is_err(),
        "the admin login must not accept the password postgres on Lakebase"
    );
    drop(dev);
    for role in ["_admin", "_api", "_kernel", "_owner", "_jobs"] {
        let _ = admin.batch(&format!(
            "DROP ROLE IF EXISTS {}",
            ident(&format!("{dev_name}{role}"))
        ));
    }
    drop_roles();
}
