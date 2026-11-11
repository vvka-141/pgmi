#![cfg(test)]

mod common;

use std::collections::{BTreeMap, BTreeSet};
use std::process::Command;

use common::{Db, Outcome, Pg, Project, ident, pg};
use serde_json::Value;

fn project(files: &[(&str, &str)]) -> Project {
    let p = Project::empty();
    for (rel, content) in files {
        p.write(rel, content);
    }
    p
}

fn deploy(pg: &Pg, db: &str, files: &[(&str, &str)]) -> (Outcome, Db) {
    deploy_with(pg, db, files, &[])
}

fn deploy_with(
    pg: &Pg,
    db: &str,
    files: &[(&str, &str)],
    params: &[(&str, &str)],
) -> (Outcome, Db) {
    let p = project(files);
    let db = pg.database(db);
    let out = pg.deploy(&p, &db).params(params).overwrite().run();
    (out, db)
}

fn reported(out: &Outcome, tag: &str) -> Value {
    let prefix = format!("{tag} ");
    let line = out
        .lines()
        .into_iter()
        .find_map(|l| l.strip_prefix(prefix.as_str()))
        .unwrap_or_else(|| panic!("deploy reported no {tag}:\n{}", out.stderr));
    serde_json::from_str(line).unwrap_or_else(|e| panic!("{tag}: {e}\n{line}"))
}

fn report(tag: &str, query: &str) -> String {
    format!("DO $report$ BEGIN RAISE NOTICE '{tag} %', ({query})::text; END $report$;\n")
}

fn contract() -> Value {
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .args(["ai", "contract"])
        .env_clear()
        .output()
        .expect("run pgmi ai contract");
    assert!(out.status.success(), "pgmi ai contract failed");
    serde_json::from_slice(&out.stdout).expect("contract JSON")
}

fn metadata_plan(p: &Project) -> Vec<Value> {
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .args([
            "metadata",
            "plan",
            &p.path().display().to_string(),
            "--json",
        ])
        .env_clear()
        .output()
        .expect("run pgmi metadata plan");
    assert!(
        out.status.success(),
        "pgmi metadata plan: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    let v: Value = serde_json::from_slice(&out.stdout).expect("plan JSON");
    v["plan"].as_array().expect("plan array").clone()
}

fn strings(v: &Value) -> Vec<String> {
    v.as_array()
        .unwrap_or_else(|| panic!("not an array: {v}"))
        .iter()
        .map(|s| s.as_str().expect("string").to_string())
        .collect()
}

fn param_names(args: &[String]) -> Vec<String> {
    args.iter()
        .filter_map(|a| {
            let mut fields = a.split_whitespace().peekable();
            if fields.peek().is_some_and(|f| {
                matches!(
                    f.to_ascii_uppercase().as_str(),
                    "IN" | "OUT" | "INOUT" | "VARIADIC"
                )
            }) {
                fields.next();
            }
            fields.next().map(String::from)
        })
        .collect()
}

const SESSION_VIEWS: &str = "SELECT jsonb_object_agg(relname, cols) FROM (
    SELECT c.relname, jsonb_agg(a.attname ORDER BY a.attnum) AS cols
    FROM pg_class c
    JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
    WHERE c.relnamespace = pg_my_temp_schema() AND c.relkind = 'v'
    GROUP BY c.relname) s";

const SESSION_FUNCTIONS: &str = "SELECT jsonb_agg(jsonb_build_array(p.proname, pg_get_function_arguments(p.oid), pg_get_function_result(p.oid)) ORDER BY p.proname)
    FROM pg_proc p WHERE p.pronamespace = pg_my_temp_schema()";

const SESSION_TYPES: &str = "SELECT jsonb_object_agg(typname, fields) FROM (
    SELECT t.typname, jsonb_agg(a.attname || ' ' || format_type(a.atttypid, a.atttypmod) ORDER BY a.attnum) AS fields
    FROM pg_type t
    JOIN pg_class c ON c.oid = t.typrelid
    JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
    WHERE t.typnamespace = pg_my_temp_schema()
    GROUP BY t.typname) s";

#[test]
fn contract_view_columns_match_the_live_session() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_contract_columns",
        &[("deploy.sql", &report("VIEWS", SESSION_VIEWS))],
    );
    let out = out.ok();
    let live = reported(&out, "VIEWS");
    let live = live.as_object().expect("views object");
    let mut problems = Vec::new();
    let mut declared = BTreeSet::new();
    for v in contract()["views"].as_array().expect("views") {
        let name = v["name"].as_str().expect("name");
        declared.insert(name.to_string());
        match live.get(name) {
            None => problems.push(format!(
                "view {name:?} is in the contract but the session does not create it"
            )),
            Some(got) if strings(got) != strings(&v["columns"]) => problems.push(format!(
                "view {name:?}: contract says {}, session has {got}",
                v["columns"]
            )),
            Some(_) => {}
        }
    }
    for name in live.keys() {
        if !declared.contains(name) {
            problems.push(format!(
                "session publishes view {name:?}, which the contract does not declare"
            ));
        }
    }
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}

fn live_functions(pg: &Pg, db: &str) -> BTreeMap<String, (String, String)> {
    let (out, _db) = deploy(
        pg,
        db,
        &[("deploy.sql", &report("FUNCTIONS", SESSION_FUNCTIONS))],
    );
    let out = out.ok();
    reported(&out, "FUNCTIONS")
        .as_array()
        .expect("functions")
        .iter()
        .map(|f| {
            (
                f[0].as_str().expect("name").to_string(),
                (
                    f[1].as_str().expect("args").to_string(),
                    f[2].as_str().expect("result").to_string(),
                ),
            )
        })
        .collect()
}

#[test]
fn contract_function_signatures_match_the_live_session() {
    let Some(pg) = pg() else { return };
    let live = live_functions(&pg, "pgmi_rs_c_contract_functions");
    let mut problems = Vec::new();
    for f in contract()["functions"].as_array().expect("functions") {
        let name = f["name"].as_str().expect("name");
        let Some((args, result)) = live.get(name) else {
            problems.push(format!("{name} is in the contract but not in the session"));
            continue;
        };
        let want = param_names(&strings(&f["args"]));
        let got = param_names(&args.split(", ").map(String::from).collect::<Vec<_>>());
        if want != got {
            problems.push(format!(
                "{name}: parameter names differ:\n  contract {want:?}\n  session  {got:?}\n  session signature: {args}"
            ));
        }
        let want = strings(&f["returns"]).join(", ");
        let got = match result.strip_prefix("TABLE(") {
            Some(cols) => param_names(
                &cols
                    .trim_end_matches(')')
                    .split(", ")
                    .map(String::from)
                    .collect::<Vec<_>>(),
            )
            .join(", "),
            None => result.clone(),
        };
        if want != got {
            problems.push(format!(
                "{name}: return shape differs:\n  contract {want:?}\n  session  {got:?} ({result})"
            ));
        }
    }
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}

#[test]
fn every_session_function_is_declared_in_the_contract() {
    let Some(pg) = pg() else { return };
    let live = live_functions(&pg, "pgmi_rs_c_session_functions");
    let declared: BTreeSet<String> = contract()["functions"]
        .as_array()
        .expect("functions")
        .iter()
        .map(|f| f["name"].as_str().expect("name").to_string())
        .collect();
    let undeclared: Vec<&String> = live.keys().filter(|n| !declared.contains(*n)).collect();
    assert!(
        undeclared.is_empty(),
        "the session creates {undeclared:?}, which the contract does not mention: declare them"
    );
}

#[test]
fn contract_test_event_type_matches_the_live_session() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_test_event_type",
        &[("deploy.sql", &report("TYPES", SESSION_TYPES))],
    );
    let out = out.ok();
    let live = reported(&out, "TYPES");
    let types = contract()["types"].as_array().expect("types").clone();
    assert!(!types.is_empty(), "the contract declares no types");
    for t in types {
        let name = t["name"].as_str().expect("name");
        let got = live.get(name).map_or_else(
            || panic!("{name} is in the contract but the session creates no such type"),
            strings,
        );
        let want: Vec<String> = t["fields"]
            .as_array()
            .expect("fields")
            .iter()
            .map(|f| {
                format!(
                    "{} {}",
                    f["name"].as_str().expect("field name"),
                    f["type"].as_str().expect("field type")
                )
            })
            .collect();
        assert_eq!(
            want, got,
            "{name}: fields differ (order matters, callbacks destructure positionally)"
        );
    }
}

#[test]
fn a_real_test_plan_yields_exactly_the_declared_step_types() {
    let Some(pg) = pg() else { return };
    let mut declared = strings(&contract()["step_types"]);
    assert!(!declared.is_empty(), "the contract declares no step types");
    declared.sort();
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_step_types",
        &[
            (
                "deploy.sql",
                &report(
                    "STEPS",
                    "SELECT jsonb_agg(DISTINCT step_type ORDER BY step_type) FROM pg_temp.pgmi_test_plan()",
                ),
            ),
            ("__test__/_setup.sql", "SELECT 1;"),
            ("__test__/test_a.sql", "SELECT 1;"),
            ("__test__/nested/_setup.sql", "SELECT 1;"),
            ("__test__/nested/test_b.sql", "SELECT 1;"),
        ],
    );
    let out = out.ok();
    assert_eq!(
        strings(&reported(&out, "STEPS")),
        declared,
        "plan step types differ from the contract"
    );
}

#[test]
fn pgmi_test_plan_emits_no_undeclared_step_type() {
    let declared = strings(&contract()["step_types"]);
    assert!(!declared.is_empty(), "the contract declares no step types");
    let schema = std::str::from_utf8(pgmi_shared::file("session/schema.sql").expect("schema.sql"))
        .expect("utf-8");
    let start = schema
        .find("CREATE OR REPLACE FUNCTION pg_temp.pgmi_test_plan")
        .expect("pgmi_test_plan not found in schema.sql");
    let body = &schema[start..];
    let body = body.split_once("\n$$;").map_or(body, |(f, _)| f);
    let literal = regex_lite::Regex::new(r"'([a-z]+)'(?:\s+AS\s+step_type)?,").unwrap();
    let emitted: BTreeSet<&str> = literal
        .captures_iter(body)
        .map(|c| c.get(1).unwrap().as_str())
        .filter(|s| declared.iter().any(|d| d == s))
        .collect();
    for d in &declared {
        assert!(
            emitted.contains(d.as_str()),
            "contract declares step type {d:?}, but pgmi_test_plan never emits it"
        );
    }
}

fn generic_id_matches_the_metadata_plan(encoding: &str) {
    let Some(pg) = pg() else { return };
    let ids = report(
        "IDS",
        "SELECT jsonb_build_object('server', current_setting('server_encoding'), 'client', current_setting('client_encoding'), 'ids', (SELECT jsonb_object_agg(path, generic_id) FROM (SELECT DISTINCT path, generic_id FROM pg_temp.pgmi_plan_view) s))",
    );
    let files = [
        ("deploy.sql", ids.as_str()),
        ("migrations/001_plain.sql", "SELECT 1;\n"),
        ("migrations/café.sql", "SELECT 1;\n"),
        ("migrations/миграции.sql", "SELECT 1;\n"),
    ];
    let p = project(&files);
    let expected: BTreeMap<String, String> = metadata_plan(&p)
        .iter()
        .map(|r| {
            (
                r["path"].as_str().expect("path").to_string(),
                r["id"].as_str().expect("id").to_string(),
            )
        })
        .collect();
    assert_eq!(expected.len(), 3, "metadata plan lists {expected:?}");
    {
        let db = pg.database(&format!("pgmi_rs_c_generic_id_{}", encoding.to_lowercase()));
        pg.admin().exec(&format!(
            "CREATE DATABASE {} ENCODING '{encoding}' TEMPLATE template0 LC_COLLATE 'C' LC_CTYPE 'C'",
            ident(db.name())
        ));
        let out = pg.deploy(&p, &db).run().ok();
        let r = reported(&out, "IDS");
        assert_eq!(
            r["server"], encoding,
            "server_encoding: the fixture did not take"
        );
        let ids = r["ids"].as_object().expect("ids");
        assert_eq!(
            ids.len(),
            3,
            "plan view returned {ids:?}: the comparison ran on nothing"
        );
        for (path, want) in &expected {
            assert_eq!(
                ids.get(path).and_then(Value::as_str),
                Some(want.as_str()),
                "{path} on {encoding} (client_encoding={}): pgmi_plan_view.generic_id differs from `pgmi metadata plan`",
                r["client"]
            );
        }
    }
}

#[test]
fn plan_view_generic_id_matches_the_metadata_plan_on_utf8() {
    generic_id_matches_the_metadata_plan("UTF8");
}

#[test]
fn plan_view_generic_id_matches_the_metadata_plan_on_latin1() {
    generic_id_matches_the_metadata_plan("LATIN1");
}

#[test]
fn sql_extension_parity_between_the_loader_and_the_session() {
    let Some(pg) = pg() else { return };
    let candidates = [
        "x.sql",
        "x.ddl",
        "x.dml",
        "x.dql",
        "x.dcl",
        "x.psql",
        "x.pgsql",
        "x.plpgsql",
        "x.SQL",
        "x.Sql",
        "x.PSQL",
        "x.PlPgSql",
        "x.md",
        "x.json",
        "x.csv",
        "x.yaml",
        "x.txt",
        "x.xml",
        "x.sql.bak",
        "x.sql~",
        "x.sql.orig",
        "x.sqlx",
        "x.bak",
        "x",
        "migrations/001.sql",
        "migrations/001.sql.bak",
    ];
    let names = candidates
        .iter()
        .map(|c| format!("'{c}'"))
        .collect::<Vec<_>>()
        .join(", ");
    let deploy_sql = report(
        "SQLFILES",
        &format!(
            "SELECT jsonb_object_agg(n, pg_temp.pgmi_is_sql_file(n)) FROM unnest(ARRAY[{names}]) n"
        ),
    );
    let mut files: Vec<(&str, &str)> = vec![("deploy.sql", &deploy_sql)];
    files.extend(candidates.iter().map(|c| (*c, "SELECT 1;\n")));
    let p = project(&files);
    let rust: BTreeMap<String, bool> = metadata_plan(&p)
        .iter()
        .map(|r| {
            (
                r["path"]
                    .as_str()
                    .expect("path")
                    .trim_start_matches("./")
                    .to_string(),
                r["isSqlFile"].as_bool().expect("isSqlFile"),
            )
        })
        .collect();
    let db = pg.database("pgmi_rs_c_sql_extension_parity");
    let out = pg.deploy(&p, &db).overwrite().run().ok();
    let session = reported(&out, "SQLFILES");
    let mut problems = Vec::new();
    for c in candidates {
        let from_sql = session[c].as_bool().expect("bool");
        let from_rust = rust
            .get(c)
            .copied()
            .unwrap_or_else(|| panic!("{c} is missing from the metadata plan"));
        if from_rust != from_sql {
            problems.push(format!(
                "{c:?}: the loader says is_sql={from_rust} but pg_temp.pgmi_is_sql_file={from_sql}"
            ));
        }
    }
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}

type VarCase<'a> = (&'a str, &'a [(&'a str, &'a str)], &'a [(&'a str, Value)]);

#[test]
fn session_variables_carry_parameters_and_defaults() {
    let Some(pg) = pg() else { return };
    let probe = report(
        "VARS",
        "SELECT jsonb_build_object(
            'env', current_setting('pgmi.env', true),
            'db_name', current_setting('pgmi.db_name', true),
            'missing', current_setting('pgmi.nonexistent', true),
            'defaulted', COALESCE(current_setting('pgmi.env', true), 'development'),
            'view_count', (SELECT count(*) FROM pg_temp.pgmi_parameter_view WHERE key IN ('env', 'api_port')),
            'view_env', (SELECT value FROM pg_temp.pgmi_parameter_view WHERE key = 'env'))",
    );
    let files = [
        ("migrations/001_test.sql", "SELECT 1;"),
        ("deploy.sql", probe.as_str()),
    ];
    let cases: [VarCase; 4] = [
        (
            "CLI parameters become session variables",
            &[("env", "production"), ("db_name", "myapp")],
            &[
                ("env", Value::from("production")),
                ("db_name", Value::from("myapp")),
                ("defaulted", Value::from("production")),
            ],
        ),
        (
            "CLI parameters are in pgmi_parameter_view",
            &[("env", "staging"), ("api_port", "8080")],
            &[
                ("view_count", Value::from(2)),
                ("view_env", Value::from("staging")),
            ],
        ),
        (
            "missing parameters are NULL and COALESCE supplies the default",
            &[],
            &[
                ("missing", Value::Null),
                ("env", Value::Null),
                ("defaulted", Value::from("development")),
            ],
        ),
        (
            "a missing key stays NULL beside given ones",
            &[("env", "production")],
            &[
                ("missing", Value::Null),
                ("defaulted", Value::from("production")),
            ],
        ),
    ];
    for (i, (name, params, want)) in cases.iter().enumerate() {
        let (out, _db) = deploy_with(&pg, &format!("pgmi_rs_c_session_vars_{i}"), &files, params);
        let out = out.ok();
        let got = reported(&out, "VARS");
        for (key, value) in *want {
            assert_eq!(&got[*key], value, "{name}: {key}");
        }
    }
}

const EVENT_LOGGER: &str = "
CREATE TABLE test_event_log (
    id SERIAL PRIMARY KEY,
    event TEXT NOT NULL,
    path TEXT,
    directory TEXT,
    depth INT,
    ordinal INT
);

CREATE OR REPLACE FUNCTION pg_temp.event_logger(e pg_temp.pgmi_test_event)
RETURNS void LANGUAGE plpgsql AS $cb$
BEGIN
    INSERT INTO test_event_log (event, path, directory, depth, ordinal)
    VALUES (e.event, e.path, e.directory, e.depth, e.ordinal);
END $cb$;

BEGIN;
CALL pgmi_test(NULL, 'pg_temp.event_logger');
COMMIT;
";

fn persisted_events(db: &Db) -> Vec<(String, i32)> {
    db.client()
        .query("SELECT event, depth FROM test_event_log ORDER BY id", &[])
        .iter()
        .map(|r| (r.get(0), r.get(1)))
        .collect()
}

fn assert_only_root_events_persist(db: &Db) {
    let events = persisted_events(db);
    let names: Vec<&str> = events.iter().map(|(e, _)| e.as_str()).collect();
    assert_eq!(
        names,
        ["suite_start", "teardown_end", "suite_end"],
        "only events outside savepoints persist"
    );
    assert_eq!(
        events[1].1, 0,
        "the persisted teardown_end must be the root's (depth 0)"
    );
}

#[test]
fn macro_sequence_without_a_fixture_persists_the_root_events() {
    let Some(pg) = pg() else { return };
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_seq_no_fixture",
        &[
            ("deploy.sql", EVENT_LOGGER),
            (
                "__test__/test_simple.sql",
                "DO $$ BEGIN RAISE NOTICE 'Simple test passed'; END $$;",
            ),
        ],
    );
    out.ok();
    assert_only_root_events_persist(&db);
}

#[test]
fn macro_sequence_with_a_fixture_persists_the_root_events() {
    let Some(pg) = pg() else { return };
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_seq_with_fixture",
        &[
            ("deploy.sql", EVENT_LOGGER),
            (
                "__test__/_setup.sql",
                "CREATE TEMP TABLE fixture_data (id int);",
            ),
            (
                "__test__/test_fixture.sql",
                "DO $$ BEGIN RAISE NOTICE 'Test with fixture'; END $$;",
            ),
        ],
    );
    out.ok();
    assert_only_root_events_persist(&db);
}

const NESTED_TESTS: [(&str, &str); 6] = [
    (
        "__test__/_setup.sql",
        "CREATE TEMP TABLE root_marker (id int);",
    ),
    (
        "__test__/test_root.sql",
        "DO $$ BEGIN RAISE NOTICE 'Root test'; END $$;",
    ),
    (
        "__test__/child/_setup.sql",
        "CREATE TEMP TABLE child_marker (id int);",
    ),
    (
        "__test__/child/test_child.sql",
        "DO $$ BEGIN RAISE NOTICE 'Child test'; END $$;",
    ),
    (
        "__test__/child/grandchild/_setup.sql",
        "CREATE TEMP TABLE grandchild_marker (id int);",
    ),
    (
        "__test__/child/grandchild/test_deep.sql",
        "DO $$ BEGIN RAISE NOTICE 'Grandchild test'; END $$;",
    ),
];

#[test]
fn macro_sequence_in_nested_directories_persists_only_the_root_teardown() {
    let Some(pg) = pg() else { return };
    let mut files = vec![("deploy.sql", EVENT_LOGGER)];
    files.extend(NESTED_TESTS);
    let (out, db) = deploy(&pg, "pgmi_rs_c_seq_nested", &files);
    out.ok();
    assert_only_root_events_persist(&db);
}

#[test]
fn plan_order_over_nested_directories_persists_only_the_root_teardown() {
    let Some(pg) = pg() else { return };
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_plan_order",
        &[
            ("deploy.sql", EVENT_LOGGER),
            ("__test__/_setup.sql", "SELECT 1;"),
            ("__test__/test_root.sql", "SELECT 'root';"),
            ("__test__/child/_setup.sql", "SELECT 2;"),
            ("__test__/child/test_child.sql", "SELECT 'child';"),
            ("__test__/child/grandchild/_setup.sql", "SELECT 3;"),
            (
                "__test__/child/grandchild/test_deep.sql",
                "SELECT 'grandchild';",
            ),
        ],
    );
    out.ok();
    assert_only_root_events_persist(&db);
}

#[test]
fn a_failing_test_fails_the_deploy_with_its_message() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_seq_test_fail",
        &[
            ("deploy.sql", "BEGIN; CALL pgmi_test(); COMMIT;"),
            (
                "__test__/test_fail.sql",
                "DO $$ BEGIN RAISE EXCEPTION 'Intentional failure'; END $$;",
            ),
        ],
    );
    out.fails(13).says("Intentional failure");
}

#[test]
fn a_failing_fixture_fails_the_deploy_with_its_message() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_seq_fixture_fail",
        &[
            ("deploy.sql", "BEGIN; CALL pgmi_test(); COMMIT;"),
            (
                "__test__/_setup.sql",
                "DO $$ BEGIN RAISE EXCEPTION 'Fixture explosion'; END $$;",
            ),
            (
                "__test__/test_should_not_run.sql",
                "DO $$ BEGIN RAISE NOTICE 'This should never run'; END $$;",
            ),
        ],
    );
    out.fails(13).says("Fixture explosion");
}

const COLLATION_PROBE: &str = "
CREATE TABLE collation_probe AS
WITH f(name) AS (VALUES ('{a}'), ('{b}'))
SELECT
    (SELECT datcollate FROM pg_database WHERE datname = current_database()) AS db_collate,
    (SELECT string_agg(name, ',' ORDER BY name) FROM f) AS default_order,
    (SELECT string_agg(name, ',' ORDER BY name COLLATE \"C\") FROM f) AS c_order;
";

fn require_linguistic_collation(db: &Db) {
    let row = &db.client().query(
        "SELECT db_collate::text, default_order, c_order FROM collation_probe",
        &[],
    )[0];
    let (collate, default_order, c_order): (String, String, String) =
        (row.get(0), row.get(1), row.get(2));
    assert_ne!(
        default_order, c_order,
        "this server's collation ({collate}) orders the fixture as C does, so the test cannot tell a fixed view from a broken one; run against a linguistic-collation database (the postgres image is en_US.utf8)"
    );
}

fn column(db: &Db, sql: &str) -> Vec<String> {
    db.client()
        .query(sql, &[])
        .iter()
        .map(|r| r.get(0))
        .collect()
}

#[test]
fn plan_view_orders_sort_keys_and_path_fallbacks_in_byte_order() {
    let Some(pg) = pg() else { return };
    let deploy_sql = format!(
        "CREATE TABLE plan_order_capture (
    execution_order BIGINT PRIMARY KEY,
    sort_key TEXT NOT NULL,
    path TEXT NOT NULL
);
INSERT INTO plan_order_capture (execution_order, sort_key, path)
SELECT execution_order, sort_key, path FROM pg_temp.pgmi_plan_view;
{}",
        COLLATION_PROBE
            .replace("{a}", "fallback_a.sql")
            .replace("{b}", "fallback-b.sql")
    );
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_plan_collation",
        &[
            ("deploy.sql", &deploy_sql),
            (
                "migrations/002_data.sql",
                "SELECT 'no metadata: sorts by path';\n",
            ),
            (
                "setup/functions.sql",
                "SELECT 'no metadata: sorts by path';\n",
            ),
            (
                "fallback_a.sql",
                "SELECT 'no metadata: punctuation pair for collation probe';\n",
            ),
            (
                "fallback-b.sql",
                "SELECT 'no metadata: punctuation pair for collation probe';\n",
            ),
            (
                "first.sql",
                "/*\n<pgmi-meta id=\"c0000001-0001-4000-8000-000000000001\" idempotent=\"true\">\n  <sortKeys><key>001/000</key></sortKeys>\n</pgmi-meta>\n*/\nSELECT 'sortKey 001/000';\n",
            ),
            (
                "last.sql",
                "/*\n<pgmi-meta id=\"c0000001-0001-4000-8000-000000000002\" idempotent=\"true\">\n  <sortKeys><key>999/000</key></sortKeys>\n</pgmi-meta>\n*/\nSELECT 'sortKey 999/000';\n",
            ),
        ],
    );
    out.ok();
    require_linguistic_collation(&db);
    assert_eq!(
        column(
            &db,
            "SELECT path FROM plan_order_capture ORDER BY execution_order"
        ),
        [
            "./fallback-b.sql",
            "./fallback_a.sql",
            "./migrations/002_data.sql",
            "./setup/functions.sql",
            "./first.sql",
            "./last.sql",
        ],
        "execution_order is not C byte order"
    );
}

#[test]
fn test_plan_orders_sibling_tests_in_byte_order() {
    let Some(pg) = pg() else { return };
    let deploy_sql = format!(
        "CREATE TABLE test_plan_capture (ordinal INT PRIMARY KEY, script_path TEXT NOT NULL);
INSERT INTO test_plan_capture (ordinal, script_path)
SELECT ordinal, script_path FROM pg_temp.pgmi_test_plan(NULL) WHERE step_type = 'test';
{}",
        COLLATION_PROBE
            .replace("{a}", "test_a.sql")
            .replace("{b}", "test-b.sql")
    );
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_test_plan_collation",
        &[
            ("deploy.sql", &deploy_sql),
            ("__test__/test_a.sql", "SELECT 1;\n"),
            ("__test__/test-b.sql", "SELECT 1;\n"),
            ("__test__/testc.sql", "SELECT 1;\n"),
        ],
    );
    out.ok();
    require_linguistic_collation(&db);
    assert_eq!(
        column(
            &db,
            "SELECT script_path FROM test_plan_capture ORDER BY ordinal"
        ),
        [
            "./__test__/test-b.sql",
            "./__test__/test_a.sql",
            "./__test__/testc.sql",
        ],
        "intra-directory test order is not C byte order"
    );
}

#[test]
fn a_gapped_test_directory_stays_under_its_ancestor() {
    let Some(pg) = pg() else { return };
    let deploy_sql = "
CREATE TABLE test_plan_capture (
    ordinal INT PRIMARY KEY,
    step_type TEXT NOT NULL,
    script_path TEXT,
    directory TEXT NOT NULL,
    depth INT NOT NULL
);

INSERT INTO test_plan_capture (ordinal, step_type, script_path, directory, depth)
SELECT ordinal, step_type, script_path, directory, depth
FROM pg_temp.pgmi_test_plan(NULL);

COMMIT;

BEGIN;
CALL pgmi_test();
COMMIT;
";
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_test_plan_gap",
        &[
            ("deploy.sql", deploy_sql),
            (
                "__test__/_setup.sql",
                "CREATE TEMP TABLE gap_state (v TEXT);\n",
            ),
            (
                "__test__/test_a.sql",
                "INSERT INTO gap_state VALUES ('a');\n",
            ),
            (
                "__test__/sub/deep/test_b.sql",
                "INSERT INTO gap_state VALUES ('b');\n",
            ),
        ],
    );
    let c = db.client();
    let plan: Vec<(i32, String, String)> = c
        .query(
            "SELECT ordinal, step_type, coalesce(script_path, directory) FROM test_plan_capture ORDER BY ordinal",
            &[],
        )
        .iter()
        .map(|r| (r.get(0), r.get(1), r.get(2)))
        .collect();
    let root_teardown = plan
        .iter()
        .find(|(_, s, l)| s == "teardown" && l == "./__test__/")
        .map(|(o, _, _)| *o);
    let last_deep = plan
        .iter()
        .filter(|(_, _, l)| l.starts_with("./__test__/sub/deep/"))
        .map(|(o, _, _)| *o)
        .max();
    let (Some(root_teardown), Some(last_deep)) = (root_teardown, last_deep) else {
        panic!("fixture did not produce both a root teardown and a nested step; plan was {plan:?}");
    };
    assert!(
        root_teardown > last_deep,
        "./__test__/ teardown runs at ordinal {root_teardown}, before ./__test__/sub/deep/ finishes at {last_deep}\nplan: {plan:?}"
    );
    let deep_depth: Option<i32> = c.one(
        "SELECT min(depth) FROM test_plan_capture WHERE directory = './__test__/sub/deep/'",
        &[],
    );
    assert_ne!(
        deep_depth,
        Some(0),
        "./__test__/sub/deep/ is reported at depth 0: it was treated as its own tree root"
    );
    out.ok();
}

#[test]
fn session_preparation_emits_no_notices() {
    let Some(pg) = pg() else { return };
    let p = project(&[("deploy.sql", "SELECT 1;\n")]);
    let db = pg.database("pgmi_rs_c_silent_prep");
    let out = pg.deploy(&p, &db).overwrite().arg("--json").run().ok();
    let notices = &out.record()["notices"];
    assert_eq!(
        notices,
        &Value::Array(vec![]),
        "session preparation must not emit notices; a deploy.sql of `SELECT 1;` produced {notices}"
    );
}

#[test]
fn source_view_orders_paths_in_byte_order() {
    let Some(pg) = pg() else { return };
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_source_collation",
        &[
            (
                "deploy.sql",
                "CREATE TABLE source_order (n serial, path text);
INSERT INTO source_order (path) SELECT path FROM pg_temp.pgmi_source_view WHERE is_sql_file ORDER BY path;",
            ),
            ("A.sql", "SELECT 1;"),
            ("a_c.sql", "SELECT 1;"),
            ("ab.sql", "SELECT 1;"),
            ("b.sql", "SELECT 1;"),
        ],
    );
    out.ok();
    let row = &db.client().query(
        "SELECT (SELECT datcollate::text FROM pg_database WHERE datname = current_database()),
            (SELECT string_agg(p, ' ' ORDER BY p) FROM unnest(ARRAY['./A.sql', './a_c.sql', './ab.sql', './b.sql']) p),
            string_agg(path, ' ' ORDER BY n) FROM source_order",
        &[],
    )[0];
    let (collate, default_order, got): (String, String, String) =
        (row.get(0), row.get(1), row.get(2));
    let want = "./A.sql ./a_c.sql ./ab.sql ./b.sql";
    assert_ne!(
        default_order, want,
        "this server's collation ({collate}) orders the fixture as C does, so the test cannot tell"
    );
    assert_eq!(
        got, want,
        "ORDER BY path over pgmi_source_view is not byte order on {collate}"
    );
}

const MIGRATE: &str = "
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN
        SELECT content
        FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
";

const TESTS_ROLLED_BACK: &str = "
BEGIN;
SAVEPOINT _tests;
CALL pgmi_test();
ROLLBACK TO SAVEPOINT _tests;
COMMIT;
";

#[test]
fn passing_tests_let_the_deploy_through() {
    let Some(pg) = pg() else { return };
    let deploy_sql = format!("{MIGRATE}{TESTS_ROLLED_BACK}");
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_direct_pass",
        &[
            ("deploy.sql", &deploy_sql),
            (
                "migrations/001_users.sql",
                "CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);",
            ),
            (
                "__test__/test_users_exist.sql",
                "DO $$\nBEGIN\n    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'users') THEN\n        RAISE EXCEPTION 'users table not found';\n    END IF;\n    RAISE NOTICE 'users table exists';\nEND $$;\n",
            ),
        ],
    );
    out.ok();
    assert!(
        db.client().one::<bool>(
            "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'users')",
            &[]
        ),
        "the users table must exist after the deploy"
    );
}

#[test]
fn a_failing_test_stops_the_deploy() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_direct_fail",
        &[
            ("deploy.sql", "BEGIN;\nCALL pgmi_test();\nCOMMIT;\n"),
            (
                "__test__/test_fail.sql",
                "DO $$\nBEGIN\n    RAISE EXCEPTION 'Intentional test failure';\nEND $$;\n",
            ),
        ],
    );
    out.fails(13).says("Intentional test failure");
}

#[test]
fn a_pattern_runs_only_the_matching_tests() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_direct_filter",
        &[
            (
                "deploy.sql",
                "BEGIN;\nCALL pgmi_test('/passing/');\nCOMMIT;\n",
            ),
            (
                "__test__/passing/test_pass.sql",
                "DO $$ BEGIN RAISE NOTICE 'Passing test'; END $$;",
            ),
            (
                "__test__/failing/test_fail.sql",
                "DO $$ BEGIN RAISE EXCEPTION 'This test should be filtered out'; END $$;",
            ),
        ],
    );
    out.ok();
}

#[test]
fn test_writes_are_rolled_back() {
    let Some(pg) = pg() else { return };
    let deploy_sql = format!("{MIGRATE}{TESTS_ROLLED_BACK}");
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_direct_savepoint",
        &[
            ("deploy.sql", &deploy_sql),
            (
                "migrations/001_users.sql",
                "CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);",
            ),
            (
                "__test__/test_insert.sql",
                "INSERT INTO users (name) VALUES ('test_user_1'), ('test_user_2');\n\nDO $$\nDECLARE\n    v_count INT;\nBEGIN\n    SELECT COUNT(*) INTO v_count FROM users;\n    IF v_count IS DISTINCT FROM 2 THEN\n        RAISE EXCEPTION 'Expected 2 users, got %', v_count;\n    END IF;\nEND $$;\n",
            ),
        ],
    );
    out.ok();
    assert_eq!(
        db.client().one::<i64>("SELECT count(*) FROM users", &[]),
        0,
        "test data must be rolled back"
    );
}

#[test]
fn a_fixture_runs_before_the_tests_of_its_directory() {
    let Some(pg) = pg() else { return };
    let deploy_sql = format!("{MIGRATE}{TESTS_ROLLED_BACK}");
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_direct_fixture",
        &[
            ("deploy.sql", &deploy_sql),
            (
                "migrations/001_products.sql",
                "CREATE TABLE products (id SERIAL PRIMARY KEY, name TEXT, price NUMERIC);",
            ),
            (
                "__test__/_setup.sql",
                "INSERT INTO products (name, price) VALUES ('Widget', 9.99), ('Gadget', 19.99), ('Gizmo', 29.99);",
            ),
            (
                "__test__/test_products.sql",
                "DO $$\nDECLARE\n    v_count INT;\nBEGIN\n    SELECT COUNT(*) INTO v_count FROM products;\n    IF v_count IS DISTINCT FROM 3 THEN\n        RAISE EXCEPTION 'Expected 3 products from fixture, got %', v_count;\n    END IF;\nEND $$;\n",
            ),
        ],
    );
    out.ok();
}

#[test]
fn a_custom_callback_receives_the_suite_events() {
    let Some(pg) = pg() else { return };
    let deploy_sql = format!(
        "{MIGRATE}
CREATE OR REPLACE FUNCTION pg_temp.event_logger(e pg_temp.pgmi_test_event)
RETURNS void LANGUAGE plpgsql AS $cb$
BEGIN
    INSERT INTO test_event_log (event, path, directory, depth, ordinal)
    VALUES (e.event, e.path, e.directory, e.depth, e.ordinal);
END $cb$;

BEGIN;
CALL pgmi_test(NULL, 'pg_temp.event_logger');
COMMIT;
"
    );
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_direct_callback",
        &[
            ("deploy.sql", &deploy_sql),
            (
                "migrations/001_schema.sql",
                "CREATE TABLE items (id SERIAL PRIMARY KEY, name TEXT);\nCREATE TABLE test_event_log (\n    id SERIAL PRIMARY KEY,\n    event TEXT NOT NULL,\n    path TEXT,\n    directory TEXT,\n    depth INT,\n    ordinal INT\n);\n",
            ),
            (
                "__test__/_setup.sql",
                "INSERT INTO items (name) VALUES ('fixture_item');",
            ),
            (
                "__test__/test_fixture_data.sql",
                "DO $$\nDECLARE\n    v_count INT;\nBEGIN\n    SELECT COUNT(*) INTO v_count FROM items WHERE name = 'fixture_item';\n    IF v_count IS DISTINCT FROM 1 THEN\n        RAISE EXCEPTION 'Expected fixture item, got % items', v_count;\n    END IF;\nEND $$;\n",
            ),
        ],
    );
    out.ok();
    let c = db.client();
    for event in ["suite_start", "teardown_end", "suite_end"] {
        assert!(
            c.one::<i64>(
                "SELECT count(*) FROM test_event_log WHERE event = $1",
                &[&event]
            ) > 0,
            "expected at least one {event} event"
        );
    }
    let ordinal = |event: &str| -> i32 {
        c.one(
            "SELECT ordinal FROM test_event_log WHERE event = $1 LIMIT 1",
            &[&event],
        )
    };
    assert_eq!(ordinal("suite_start"), 0, "suite_start ordinal");
    let row = &c.query(
        "SELECT directory, depth FROM test_event_log WHERE event = 'teardown_end' LIMIT 1",
        &[],
    )[0];
    let (directory, depth): (String, i32) = (row.get(0), row.get(1));
    assert!(
        directory.contains("__test__"),
        "teardown directory {directory:?}"
    );
    assert_eq!(depth, 0, "root teardown depth");
    assert!(
        ordinal("suite_end") >= ordinal("teardown_end"),
        "suite_end ordinal must not precede teardown_end's"
    );
}

#[test]
fn test_plan_returns_tests_in_order() {
    let Some(pg) = pg() else { return };
    let deploy_sql = "
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN
        SELECT content FROM pg_temp.pgmi_source_view WHERE path LIKE '%schema.sql'
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

BEGIN;

DO $$
DECLARE
    v_test RECORD;
    v_test_name TEXT;
BEGIN
    FOR v_test IN
        SELECT ordinal, script_path, step_type
        FROM pg_temp.pgmi_test_plan()
        WHERE step_type = 'test'
        ORDER BY ordinal
    LOOP
        v_test_name := regexp_replace(v_test.script_path, '^.*/([^/]+)\\.sql$', '\\1');
        INSERT INTO test_execution_log (execution_log) VALUES (v_test_name);
    END LOOP;
END $$;

COMMIT;

INSERT INTO test_execution_log (execution_log)
SELECT string_agg(execution_log, ',' ORDER BY id)
FROM (
    SELECT id, execution_log FROM test_execution_log
    WHERE execution_log IN ('test_001', 'test_002', 'test_003')
    ORDER BY id
) subq;
";
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_unittest_plan",
        &[
            ("deploy.sql", deploy_sql),
            (
                "schema.sql",
                "CREATE UNLOGGED TABLE test_execution_log (id SERIAL PRIMARY KEY, execution_log TEXT);",
            ),
            (
                "__test__/test_001.sql",
                "INSERT INTO test_execution_log (execution_log) VALUES ('test_001');",
            ),
            (
                "__test__/test_002.sql",
                "INSERT INTO test_execution_log (execution_log) VALUES ('test_002');",
            ),
            (
                "__test__/test_003.sql",
                "INSERT INTO test_execution_log (execution_log) VALUES ('test_003');",
            ),
        ],
    );
    out.ok();
    let log: Vec<String> = column(
        &db,
        "SELECT execution_log FROM test_execution_log WHERE execution_log LIKE '%,%' ORDER BY id DESC LIMIT 1",
    );
    assert_eq!(
        log,
        ["test_001,test_002,test_003"],
        "aggregated execution log"
    );
}

#[test]
fn test_plan_function_and_test_source_table_exist_during_deploy() {
    let Some(pg) = pg() else { return };
    let (out, db) = deploy(
        &pg,
        "pgmi_rs_c_unittest_tables",
        &[
            (
                "deploy.sql",
                "CREATE TABLE unittest_table_check (plan_function_exists BOOLEAN, source_table_exists BOOLEAN);
INSERT INTO unittest_table_check (plan_function_exists, source_table_exists)
SELECT
    EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'pgmi_test_plan' AND pronamespace = pg_my_temp_schema()),
    EXISTS (SELECT 1 FROM pg_class WHERE relname = '_pgmi_test_source' AND relnamespace = pg_my_temp_schema());",
            ),
            ("__test__/test_simple.sql", "SELECT 1;"),
        ],
    );
    out.ok();
    let row = &db.client().query(
        "SELECT plan_function_exists, source_table_exists FROM unittest_table_check",
        &[],
    )[0];
    assert_eq!(
        (row.get::<_, bool>(0), row.get::<_, bool>(1)),
        (true, true),
        "pg_temp.pgmi_test_plan() and pg_temp._pgmi_test_source must exist while deploy.sql runs"
    );
}

#[test]
fn session_foundation_suite_passes_on_a_multi_level_project() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy_with(
        &pg,
        "pgmi_rs_c_session_foundation",
        &[
            ("deploy.sql", include_str!("session_foundation.sql")),
            (
                "migrations/001_schema.sql",
                "CREATE TABLE users (id SERIAL PRIMARY KEY, username TEXT NOT NULL);",
            ),
            (
                "migrations/002_data.sql",
                "INSERT INTO users (username) VALUES ('admin'), ('user');",
            ),
            (
                "setup/functions.sql",
                "CREATE FUNCTION get_user(id INT) RETURNS TEXT AS $$ SELECT username FROM users WHERE id = $1; $$ LANGUAGE sql;",
            ),
            (
                "__test__/_setup.sql",
                "CREATE TEMP TABLE test_state (key TEXT, value TEXT);",
            ),
            (
                "__test__/test_basic.sql",
                "INSERT INTO test_state VALUES ('test', 'basic');",
            ),
            (
                "__test__/auth/_setup.sql",
                "CREATE TEMP TABLE auth_state (user_id INT);",
            ),
            (
                "__test__/auth/test_login.sql",
                "INSERT INTO auth_state VALUES (1);",
            ),
            (
                "__test__/auth/oauth/test_google.sql",
                "INSERT INTO auth_state VALUES (2);",
            ),
            (
                "__test__/billing/test_stripe.sql",
                "INSERT INTO test_state VALUES ('billing', 'stripe');",
            ),
        ],
        &[("env", "test"), ("version", "1.0.0")],
    );
    out.ok();
}

#[test]
fn an_empty_project_has_no_sources_and_no_tests() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_session_empty",
        &[(
            "deploy.sql",
            &report(
                "COUNTS",
                "SELECT jsonb_build_array((SELECT count(*) FROM pg_temp.pgmi_source_view), (SELECT count(*) FROM pg_temp.pgmi_test_plan()))",
            ),
        )],
    );
    let out = out.ok();
    assert_eq!(reported(&out, "COUNTS"), serde_json::json!([0, 0]));
}

#[test]
fn a_migrations_only_project_has_its_migrations_and_no_tests() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_session_migrations",
        &[
            (
                "deploy.sql",
                &report(
                    "COUNTS",
                    "SELECT jsonb_build_array((SELECT count(*) FROM pg_temp.pgmi_source_view WHERE directory ~ 'migrations'), (SELECT count(*) FROM pg_temp.pgmi_test_plan()))",
                ),
            ),
            (
                "migrations/001_init.sql",
                "CREATE TABLE products (id SERIAL PRIMARY KEY);",
            ),
            (
                "migrations/002_indexes.sql",
                "CREATE INDEX idx_products_id ON products(id);",
            ),
        ],
    );
    let out = out.ok();
    assert_eq!(reported(&out, "COUNTS"), serde_json::json!([2, 0]));
}

const GUARDED_LOOP: &str = "
BEGIN;
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT p.path, p.content FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND p.path LIKE './migrations/%'
        ORDER BY p.execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;
";

const UNGUARDED_LOOP: &str = "
BEGIN;
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;
";

fn plan_filter(pg: &Pg, db: &str, deploy_sql: &str) -> (bool, bool) {
    let (out, db) = deploy(
        pg,
        db,
        &[
            ("deploy.sql", deploy_sql),
            (
                "migrations/001_init.sql",
                "CREATE TABLE IF NOT EXISTS good_table(id int);",
            ),
            (
                "migrations/001_init.sql~",
                "CREATE TABLE IF NOT EXISTS stale_backup_leaked(id int);",
            ),
            ("README.md", "# Project\n\nNot SQL.\n"),
        ],
    );
    out.ok();
    let c = db.client();
    (
        c.one("SELECT to_regclass('public.good_table') IS NOT NULL", &[]),
        c.one(
            "SELECT to_regclass('public.stale_backup_leaked') IS NOT NULL",
            &[],
        ),
    )
}

#[test]
fn the_is_sql_file_guard_keeps_an_editor_backup_from_running() {
    let Some(pg) = pg() else { return };
    let (good, backup) = plan_filter(&pg, "pgmi_rs_c_plan_filter_guarded", GUARDED_LOOP);
    assert!(
        good,
        "the real migration did not run: the loop is broken, not merely unfiltered"
    );
    assert!(
        !backup,
        "is_sql_file guard did not stop 001_init.sql~ from executing as a migration"
    );
    let (good, backup) = plan_filter(&pg, "pgmi_rs_c_plan_filter_unguarded", UNGUARDED_LOOP);
    assert!(good, "the real migration did not run");
    assert!(
        backup,
        "unguarded loop did not execute 001_init.sql~: this test no longer demonstrates the hazard the guard prevents"
    );
}

#[test]
fn an_empty_test_plan_fails_the_deploy() {
    let Some(pg) = pg() else { return };
    for (i, (with_tests, call, want)) in [
        (false, "CALL pgmi_test();", Some("no tests were discovered")),
        (
            true,
            "CALL pgmi_test('no_such_test_anywhere');",
            Some("no tests matched the requested pattern"),
        ),
        (true, "CALL pgmi_test();", None),
    ]
    .into_iter()
    .enumerate()
    {
        let deploy_sql = format!("BEGIN;\n{call}\nCOMMIT;\n");
        let mut files = vec![("deploy.sql", deploy_sql.as_str())];
        if with_tests {
            files.push(("__test__/test_noop.sql", "SELECT 1;\n"));
        }
        let (out, _db) = deploy(&pg, &format!("pgmi_rs_c_test_gate_{i}"), &files);
        match want {
            None => {
                out.ok();
            }
            Some(want) => {
                out.fails(13).says(want);
            }
        }
    }
}

#[test]
fn a_double_s_tests_directory_runs_and_stays_out_of_the_source_view() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_double_s_tests",
        &[
            (
                "deploy.sql",
                "
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_temp.pgmi_source_view WHERE path LIKE '%/__tests__/%') THEN
        RAISE EXCEPTION 'a __tests__ file reached pgmi_source_view';
    END IF;
END $$;
BEGIN;
CALL pgmi_test();
COMMIT;",
            ),
            (
                "__tests__/test_marker.sql",
                "DO $$ BEGIN RAISE EXCEPTION 'ran-from-double-s'; END $$;",
            ),
        ],
    );
    out.fails(13).says("ran-from-double-s");
}

#[test]
fn a_nested_deploy_sql_is_an_ordinary_project_file() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_nested_deploy_sql",
        &[
            (
                "deploy.sql",
                "
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_temp.pgmi_source_view WHERE path = './examples/deploy.sql') THEN
        RAISE EXCEPTION 'nested examples/deploy.sql is missing from pgmi_source_view';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_temp.pgmi_source_view WHERE path = './deploy.sql') THEN
        RAISE EXCEPTION 'the root deploy.sql leaked into pgmi_source_view';
    END IF;
END $$;",
            ),
            ("examples/deploy.sql", "SELECT 1;"),
        ],
    );
    out.ok();
}

#[test]
fn metadata_without_sort_keys_stays_in_the_plan() {
    let Some(pg) = pg() else { return };
    let (out, _db) = deploy(
        &pg,
        "pgmi_rs_c_meta_no_sortkeys",
        &[
            (
                "deploy.sql",
                "
DO $$ BEGIN
    IF (SELECT count(*) FROM pg_temp.pgmi_plan_view WHERE path = './migrations/001.sql') IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'a file with metadata but no sortKeys must appear once in pgmi_plan_view';
    END IF;
    IF (SELECT id FROM pg_temp.pgmi_plan_view WHERE path = './migrations/001.sql')
       IS DISTINCT FROM '6f1c2d4e-0261-4000-8000-000000000001'::uuid THEN
        RAISE EXCEPTION 'the plan row must carry the declared id';
    END IF;
END $$;",
            ),
            (
                "migrations/001.sql",
                "/*\n<pgmi-meta id=\"6f1c2d4e-0261-4000-8000-000000000001\" idempotent=\"true\">\n  <description>no sortKeys</description>\n</pgmi-meta>\n*/\nSELECT 1;",
            ),
        ],
    );
    out.ok();
}

#[test]
fn a_bad_or_unmatched_test_pattern_fails_fast() {
    let Some(pg) = pg() else { return };
    for (db, pattern, want) in [
        (
            "pgmi_rs_c_pattern_invalid",
            "[unclosed",
            "Invalid regex pattern",
        ),
        ("pgmi_rs_c_pattern_nomatch", "nomatch", "no tests matched"),
    ] {
        let deploy_sql = format!("BEGIN;\nCALL pgmi_test('{pattern}');\nCOMMIT;");
        let (out, _db) = deploy(
            &pg,
            db,
            &[
                ("deploy.sql", &deploy_sql),
                ("__test__/test_a.sql", "SELECT 1;"),
            ],
        );
        out.fails(13).says(want);
    }
}
