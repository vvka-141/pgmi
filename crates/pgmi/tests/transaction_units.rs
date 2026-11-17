#![cfg(test)]

mod common;

use common::{Client, Outcome, Pg, Project, pg};

fn deploy_project(pg: &Pg, deploy_sql: &str, files: &[(&str, &str)], db: &common::Db) -> Outcome {
    let p = Project::empty();
    p.write("deploy.sql", deploy_sql);
    for (rel, content) in files {
        p.write(rel, content);
    }
    pg.deploy(&p, db).overwrite().run()
}

fn exists(c: &Client, rel: &str) -> bool {
    c.one(&format!("SELECT to_regclass('{rel}') IS NOT NULL"), &[])
}

#[test]
fn interleaved_concurrent_indexes() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_b_cic_interleaved");
    deploy_project(
        &pg,
        "
BEGIN;
CREATE TABLE cic_probe (id int, doubled int);
INSERT INTO cic_probe SELECT g, NULL FROM generate_series(1, 100) g;
COMMIT;
CREATE INDEX CONCURRENTLY idx_cic_first ON cic_probe (id);
BEGIN;
UPDATE cic_probe SET doubled = id * 2;
COMMIT;
CREATE INDEX CONCURRENTLY idx_cic_second ON cic_probe (doubled);
",
        &[],
        &db,
    )
    .ok();
    let c = db.client();
    for index in ["idx_cic_first", "idx_cic_second"] {
        let rows = c.query(
            "SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = $1",
            &[&index],
        );
        assert_eq!(rows.len(), 1, "index {index} not found");
        assert!(
            rows[0].get::<_, bool>(0),
            "index {index} exists but is INVALID"
        );
    }
    let backfilled: i64 = c.one("SELECT count(*) FROM cic_probe WHERE doubled = id * 2", &[]);
    assert_eq!(
        backfilled, 100,
        "backfill applied to {backfilled} rows, want 100"
    );
}

#[test]
fn tail_transaction_is_atomic() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_b_tail_atomic");
    let out = deploy_project(
        &pg,
        "
BEGIN;
CREATE TABLE head_t (id int);
COMMIT;
CREATE TABLE tail_before (id int);
BEGIN;
INSERT INTO tail_before VALUES (1);
SELECT 1/0;
COMMIT;
",
        &[],
        &db,
    );
    assert_ne!(
        out.code, 0,
        "expected the division-by-zero inside the tail transaction to fail the deploy"
    );
    let c = db.client();
    assert!(
        exists(&c, "public.head_t"),
        "head transaction was committed before the tail failure; head_t must exist"
    );
    assert!(
        exists(&c, "public.tail_before"),
        "autocommitted tail unit before the failing transaction must stay applied; tail_before must exist"
    );
    let rows: i64 = c.one("SELECT count(*) FROM tail_before", &[]);
    assert_eq!(
        rows, 0,
        "the failed tail transaction must roll back its INSERT, found {rows} rows"
    );
}

#[test]
fn tail_reads_session_views() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_b_tail_views");
    deploy_project(
        &pg,
        "
SELECT 1;
COMMIT;
CREATE TABLE tail_probe AS SELECT count(*)::int AS n FROM pg_temp.pgmi_source_view;
",
        &[("migrations/001.sql", "SELECT 1;\n")],
        &db,
    )
    .ok();
    let n: i32 = db.client().one("SELECT n FROM tail_probe", &[]);
    assert_eq!(
        n, 1,
        "pgmi_source_view visible from tail reported {n} files, want 1"
    );
}

const COMMITS_THEN_LOCK_BUSY: &str = "
BEGIN;
CREATE TABLE once_t (id int);
INSERT INTO once_t VALUES (1);
COMMIT;
DO $$ BEGIN RAISE EXCEPTION 'lock busy' USING ERRCODE = '55P03'; END $$;
";

#[test]
fn a_deploy_that_committed_a_unit_is_not_retried_by_default() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_b_no_retry_after_commit");
    let out = deploy_project(&pg, COMMITS_THEN_LOCK_BUSY, &[], &db).fails(13);
    out.says("unit 2 of 2 failed; 1 earlier unit(s) already committed")
        .says("not retried: 1 unit(s) already committed")
        .never_says("retrying in");
    let rows: i64 = db.client().one("SELECT count(*) FROM once_t", &[]);
    assert_eq!(rows, 1);
}

#[test]
fn a_retry_keeps_what_an_earlier_attempt_committed_in_the_report() {
    let Some(pg) = pg() else { return };
    let db = pg.database("pgmi_rs_b_retry_keeps_commits");
    let out = deploy_project(
        &pg,
        COMMITS_THEN_LOCK_BUSY,
        &[("pgmi.yaml", "retry_after_commit: true\n")],
        &db,
    )
    .fails(13);
    out.says("attempt 1 committed 1 unit(s); the last attempt committed nothing")
        .says("attempt 1 of 2 failed with: ERROR: lock busy (SQLSTATE 55P03)")
        .never_says("nothing committed;")
        .never_says("left empty");
    let rows: i64 = db.client().one("SELECT count(*) FROM once_t", &[]);
    assert_eq!(rows, 1);
}
