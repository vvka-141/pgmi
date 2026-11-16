#![cfg(test)]

mod common;

use common::{Project, pg};

/// The limit on what pgmi's own queries may return must not reach the
/// project's SQL: rows deploy.sql's statements return are dropped whatever
/// their number, before and after its first COMMIT, and the SQL
/// `CALL pgmi_test()` expands to holds every test file.
#[test]
fn large_answers_to_the_projects_own_sql_are_not_refused() {
    let Some(pg) = pg() else { return };
    let project = Project::empty();
    let padding = "x".repeat(7 << 20);
    for name in ["a", "b", "c"] {
        project.write(
            &format!("__test__/test_{name}.sql"),
            &format!("DO $$ BEGIN PERFORM length('{padding}'); END $$;\n"),
        );
    }
    project.write(
        "deploy.sql",
        "BEGIN;\n\
         SELECT repeat('x', 1000) FROM generate_series(1, 40000);\n\
         CALL pgmi_test();\n\
         CREATE TABLE head_ran (id int);\n\
         COMMIT;\n\
         SELECT repeat('x', 1000) FROM generate_series(1, 40000);\n\
         CREATE TABLE tail_ran (id int);\n",
    );
    for verbose in [false, true] {
        let db = pg.database("pgmi_rs_large_answers");
        let mut deploy = pg.deploy(&project, &db);
        if verbose {
            deploy = deploy.arg("--verbose");
        }
        let out = deploy.run();
        assert_eq!(out.code, 0, "verbose={verbose}:\n{}", tail(&out.stderr));
        let ran: i64 = db.client().one(
            "SELECT count(*) FROM pg_tables WHERE tablename IN ('head_ran', 'tail_ran')",
            &[],
        );
        assert_eq!(ran, 2, "verbose={verbose}");
    }
}

/// The SQL `CALL pgmi_test()` expands to comes back as one value, which
/// grows with the number of tests and the length of their paths: past what
/// a short answer holds, it is still read.
#[test]
fn a_test_plan_larger_than_a_short_answer_is_not_refused() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_large_test_plan");
    let project = Project::empty();
    let deep = vec!["d".repeat(250); 15].join("/");
    for i in 0..1100 {
        project.write(
            &format!("__test__/{deep}/test_{i:04}.sql"),
            "DO $$ BEGIN PERFORM 1; END $$;\n",
        );
    }
    project.write(
        "deploy.sql",
        "BEGIN;\nCALL pgmi_test();\nCREATE TABLE tests_ran (id int);\nCOMMIT;\n",
    );
    let out = pg.deploy(&project, &db).run();
    assert_eq!(out.code, 0, "{}", tail(&out.stderr));
    let ran: i64 = db.client().one(
        "SELECT count(*) FROM pg_tables WHERE tablename = 'tests_ran'",
        &[],
    );
    assert_eq!(ran, 1);
}

fn tail(text: &str) -> &str {
    let from = text.len().saturating_sub(2000);
    text.get(from..).unwrap_or(text)
}
