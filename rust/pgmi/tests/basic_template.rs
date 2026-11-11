#![cfg(test)]

mod b_support;
mod common;

use b_support::{deploy, ok, project, refused, unlink, write};
use common::{Project, pg};

const PROBE_ID: &str = "ffffffff-0464-4000-8000-000000000001";
const PROBE_BODY: &str =
    "CREATE TABLE run_once_probe (id int);\nINSERT INTO run_once_probe VALUES (1);\n";

fn probe(id: &str, idempotent: &str, body: &str) -> String {
    format!("/*\n<pgmi-meta id=\"{id}\" idempotent=\"{idempotent}\">\n</pgmi-meta>\n*/\n{body}")
}

const DEV: &[(&str, &str)] = &[("env", "dev")];

#[test]
fn run_once_file_runs_once_and_a_re_run_file_every_time() {
    let Some(pg) = pg() else { return };
    let p = project(
        "basic",
        &[
            (
                "physical/900_probe.sql",
                &probe(PROBE_ID, "false", PROBE_BODY),
            ),
            (
                "semantic/zz_probe.sql",
                "INSERT INTO run_once_probe VALUES (2);\n",
            ),
        ],
    );
    let db = pg.database("pgmi_rs_b_basic_run_once");
    ok(deploy(&pg, &p, &db, true, DEV), "first deploy");
    ok(deploy(&pg, &p, &db, false, DEV), "second deploy");
    let row = &db.client().query(
        "SELECT count(*) FILTER (WHERE id = 1), count(*) FILTER (WHERE id = 2) FROM run_once_probe",
        &[],
    )[0];
    let (run_once, re_run): (i64, i64) = (row.get(0), row.get(1));
    assert_eq!(
        (run_once, re_run),
        (1, 2),
        "after two deploys: run-once file ran {run_once} time(s), re-run file {re_run}, want 1 and 2"
    );
}

type DriftCase = (&'static str, Vec<(&'static str, String)>, &'static str);

#[test]
fn basic_refuses_run_once_drift() {
    let Some(pg) = pg() else { return };
    let original = probe(PROBE_ID, "false", PROBE_BODY);
    let cases: Vec<DriftCase> = vec![
        (
            "content edited",
            vec![(
                "physical/900_probe.sql",
                probe(
                    PROBE_ID,
                    "false",
                    "CREATE TABLE run_once_probe (id int, note text);\nINSERT INTO run_once_probe VALUES (1);\n",
                ),
            )],
            "./physical/900_probe.sql: content edited",
        ),
        (
            "moved",
            vec![(
                "physical/moved/900_probe.sql",
                probe(PROBE_ID, "false", PROBE_BODY),
            )],
            "./physical/moved/900_probe.sql: moved from ./physical/900_probe.sql",
        ),
        (
            "flipped to re-run",
            vec![(
                "physical/900_probe.sql",
                probe(PROBE_ID, "true", PROBE_BODY),
            )],
            "./physical/900_probe.sql: now idempotent=\"true\"",
        ),
        (
            "removed",
            vec![],
            "./physical/900_probe.sql: removed from the project",
        ),
        (
            "comment, whitespace and keyword case only",
            vec![(
                "physical/900_probe.sql",
                probe(
                    PROBE_ID,
                    "false",
                    "-- the probe\ncreate   table run_once_probe\n    (id int); /* unchanged */\ninsert into run_once_probe values (1);\n",
                ),
            )],
            "",
        ),
    ];
    for (i, (name, second, want)) in cases.into_iter().enumerate() {
        let p = project("basic", &[("physical/900_probe.sql", &original)]);
        let db = pg.database(&format!("pgmi_rs_b_basic_drift_{i}"));
        ok(
            deploy(&pg, &p, &db, true, DEV),
            &format!("{name}: first deploy"),
        );
        unlink(&p, &["physical/900_probe.sql"]);
        let marker = probe(
            "ffffffff-0464-4000-8000-000000000002",
            "false",
            "CREATE TABLE drift_marker (id int);\n",
        );
        write(&p, &[("physical/000_marker.sql", &marker)]);
        for (rel, content) in &second {
            write(&p, &[(rel, content)]);
        }
        let result = deploy(&pg, &p, &db, false, DEV);
        if want.is_empty() {
            ok(
                result,
                &format!("{name}: a comment, whitespace and keyword case edit"),
            );
            continue;
        }
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
        let applied: bool = db
            .client()
            .one("SELECT to_regclass('public.drift_marker') IS NOT NULL", &[]);
        assert!(
            !applied,
            "{name}: the refused deploy applied a file: drift_marker exists"
        );
    }
}

#[test]
fn basic_env_decides_the_seed() {
    let Some(pg) = pg() else { return };
    let p = Project::init("basic");
    let cases = [
        ("dev", "", true),
        ("test", "", false),
        ("prod", "", false),
        ("", "Missing required parameter: env", false),
        (
            "production",
            "Parameter env must be dev, test or prod, got 'production'",
            false,
        ),
    ];
    for (i, (env, want_err, want_demo)) in cases.into_iter().enumerate() {
        let db = pg.database(&format!("pgmi_rs_b_basic_env_{i}"));
        let params: Vec<(&str, &str)> = if env.is_empty() {
            vec![]
        } else {
            vec![("env", env)]
        };
        let result = deploy(&pg, &p, &db, true, &params);
        if !want_err.is_empty() {
            let f = refused(result, &format!("env={env}"));
            assert!(
                f.says(want_err),
                "env={env}: want {want_err:?} in:\n{}",
                f.text
            );
            assert_eq!(f.sqlstate(), "22023", "env={env}: want SQLSTATE 22023");
            if env.is_empty() {
                let hint = f.hint();
                assert!(
                    hint.contains("--param env=dev") && hint.contains("PGMI_PARAM_ENV"),
                    "the missing-env hint must name the local and the CI fix, got {hint:?}"
                );
            }
            let tables: i64 = db.client().one(
                "SELECT count(*) FROM pg_tables WHERE schemaname IN ('public', 'deployment')",
                &[],
            );
            assert_eq!(
                tables, 0,
                "env={env}: the refused deploy left tables behind"
            );
            continue;
        }
        ok(result, &format!("env={env}"));
        let demo: bool = db.client().one(
            "SELECT EXISTS (SELECT FROM project WHERE name = 'Demo')",
            &[],
        );
        assert_eq!(demo, want_demo, "env={env}: Demo project exists");
    }
}

#[test]
fn basic_refuses_physical_file_without_run_once_header() {
    let Some(pg) = pg() else { return };
    let files = [
        "ALTER TABLE project ADD COLUMN archived boolean;\n".to_string(),
        probe(
            "ffffffff-0472-4000-8000-000000000001",
            "true",
            "ALTER TABLE project ADD COLUMN archived boolean;\n",
        ),
    ];
    for (i, file) in files.iter().enumerate() {
        let p = project("basic", &[("physical/003_project_archived.sql", file)]);
        let db = pg.database(&format!("pgmi_rs_b_basic_header_{i}"));
        let f = refused(
            deploy(&pg, &p, &db, true, DEV),
            "the first deploy accepted a physical/ file that would re-run",
        );
        for want in [
            "physical/ file(s) without a run-once header",
            "./physical/003_project_archived.sql",
        ] {
            assert!(f.says(want), "{i}: want {want:?} in:\n{}", f.text);
        }
        assert!(
            f.hint().contains("idempotent=\"false\""),
            "want the hint to show the header, got {:?}",
            f.hint()
        );
    }
}

#[test]
fn basic_refuses_run_once_file_with_several_sort_keys() {
    let Some(pg) = pg() else { return };
    let p = project(
        "basic",
        &[(
            "physical/003_keys.sql",
            "/*\n<pgmi-meta id=\"ffffffff-0489-4000-8000-000000000002\" idempotent=\"false\">\n  <sortKeys><key>./physical/003a</key><key>./physical/003b</key></sortKeys>\n</pgmi-meta>\n*/\nCREATE TABLE keys_probe (id int);\n",
        )],
    );
    let db = pg.database("pgmi_rs_b_basic_keys");
    let f = refused(deploy(&pg, &p, &db, true, DEV), "two-key run-once file");
    assert!(
        f.says("./physical/003_keys.sql") && f.says("Run-once file(s) with more than one sort key"),
        "want the two-key run-once file refused by name, got:\n{}",
        f.text
    );
    let created: bool = db
        .client()
        .one("SELECT to_regclass('public.keys_probe') IS NOT NULL", &[]);
    assert!(!created, "the refused deploy created keys_probe");
}

#[test]
fn basic_refuses_sql_file_outside_its_folders() {
    let Some(pg) = pg() else { return };
    for (i, path) in ["seed.sql", "tests/test_x.sql"].into_iter().enumerate() {
        let p = project("basic", &[(path, "CREATE TABLE stray ();\n")]);
        let db = pg.database(&format!("pgmi_rs_b_basic_stray_{i}"));
        let f = refused(
            deploy(&pg, &p, &db, true, DEV),
            &format!("the deploy accepted {path}, which never runs"),
        );
        for want in [
            "SQL file(s) outside physical/ and semantic/".to_string(),
            format!("./{path}"),
        ] {
            assert!(f.says(&want), "{path}: want {want:?} in:\n{}", f.text);
        }
        let hint = f.hint();
        for folder in ["physical/", "semantic/", "__test__/"] {
            assert!(
                hint.contains(folder),
                "want the hint to name {folder}, got {hint:?}"
            );
        }
    }
}

fn failing_migration(name: &str, migration: &str, sqlstate: &str, line: i64) {
    let Some(pg) = pg() else { return };
    const FAILING: &str = "./semantic/999_failing.sql";
    let p = project("basic", &[("semantic/999_failing.sql", migration)]);
    let db = pg.database(&format!("pgmi_rs_b_sqlstate_{sqlstate}"));
    let f = refused(
        deploy(&pg, &p, &db, true, DEV),
        &format!("{name}: the failing migration"),
    );
    assert_eq!(f.sqlstate(), sqlstate, "{name}: SQLSTATE");
    if sqlstate == "23505" {
        assert!(
            !f.field("detail").is_empty(),
            "the original DETAIL line was lost"
        );
    }
    assert_eq!(
        f.field("failedFile"),
        FAILING,
        "{name}: failedFile\n{}",
        f.text
    );
    if line > 0 {
        assert_eq!(
            (f.field("script"), f.record["line"].as_i64()),
            (FAILING.to_string(), Some(line)),
            "{name}: location\n{}",
            f.text
        );
    }
}

#[test]
fn failing_migration_keeps_sqlstate_and_detail() {
    failing_migration(
        "runtime error keeps SQLSTATE and DETAIL",
        "CREATE TABLE dupe_probe (email text UNIQUE);\nINSERT INTO dupe_probe VALUES ('x@example.com');\nINSERT INTO dupe_probe VALUES ('x@example.com');\n",
        "23505",
        0,
    );
}

#[test]
#[ignore = "the Rust binary does not resolve the line of a syntax error inside an executed file yet (Go reports line 4)"]
fn failing_migration_syntax_error_resolves_to_its_line() {
    failing_migration(
        "syntax error resolves to the line in the file",
        "CREATE TABLE syntax_probe (id int);\n\n-- line 3\nSELEC 1;\n",
        "42601",
        4,
    );
}

#[test]
fn session_preparation_refused_by_the_server_exits_13() {
    let Some(pg) = pg() else { return };
    let p = Project::init("minimal");
    let db = pg.database("pgmi_rs_b_readonly_session");
    b_support::create_database(&pg, &db);
    let out = std::process::Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(p.path())
        .args(["--connection", pg.conn(), "-d", db.name(), "--force"])
        .env_clear()
        .env("PGMI_NO_BANNER", "1")
        .env("PGOPTIONS", "-c default_transaction_read_only=on")
        .output()
        .expect("run pgmi deploy");
    assert_eq!(
        out.status.code(),
        Some(13),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
}
