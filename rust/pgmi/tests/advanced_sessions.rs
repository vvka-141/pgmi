#![cfg(test)]

mod adv;
mod common;

use std::time::{Duration, Instant};

use adv::{Roles, deploy_via, deployed, scaffold, sqlstate};
use common::{Client, Db, describe, pg};

const EXPIRED_REST: &str =
    "SELECT count(*) FROM api.rest_exchange WHERE enqueued_at < now() - api.exchange_retention()";

fn count(c: &Client, sql: &str) -> i64 {
    c.one(sql, &[])
}

fn job_count(c: &Client, job: &str) -> i64 {
    c.one(
        "SELECT count(*) FROM internal.job_run_log WHERE job_name = $1",
        &[&job],
    )
}

#[test]
fn background_jobs() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let project = scaffold(&[]);
    let db = pg.database("pgmi_rs_a_jobs");
    deployed(&pg, &project, &db, true, &[("env", "dev")]);
    let a = db.client();
    let b = db.client();

    a.exec(
        "INSERT INTO api.rest_exchange (object_id, enqueued_at, handler_object_id, request, response, completed_at)
         SELECT gen_random_uuid(), now() - api.exchange_retention() - make_interval(hours => n),
                (SELECT object_id FROM api.handler WHERE handler_function_name = 'hello_world'),
                ROW('GET', '/t', ''::extensions.hstore, NULL::bytea, '{}'::jsonb)::api.rest_request,
                ROW(200, ''::extensions.hstore, NULL::bytea)::api.http_response, now()
         FROM generate_series(1, 7) n",
    );

    let run: String = a.one("SELECT api.job_run_started('purge_exchanges')::text", &[]);
    b.exec("CALL api.job_purge_exchanges(2)");
    assert_eq!(
        count(&b, EXPIRED_REST),
        7,
        "session B purged while A held the job"
    );
    assert_eq!(
        job_count(&b, "purge_exchanges"),
        1,
        "session B logged a run while A held the job"
    );
    a.query(
        "SELECT api.job_run_finished($1::text::uuid, 0, 0)::text",
        &[&run],
    );

    b.exec("CALL api.job_purge_exchanges(2, 2)");
    assert_eq!(
        count(&b, EXPIRED_REST),
        3,
        "two batches of 2 must leave 3 of 7 expired exchanges"
    );
    let row = &b.query(
        "SELECT outcome, rows_affected::bigint, batches::bigint, finished_at >= started_at
         FROM internal.job_run_log WHERE job_name = 'purge_exchanges' ORDER BY started_at DESC LIMIT 1",
        &[],
    )[0];
    let outcome: Option<String> = row.get(0);
    let rows: Option<i64> = row.get(1);
    let batches: Option<i64> = row.get(2);
    let finished: Option<bool> = row.get(3);
    assert_eq!(
        (outcome.as_deref(), rows, batches, finished),
        (Some("succeeded"), Some(4), Some(2), Some(true)),
        "run log: outcome, rows, batches, finished"
    );

    b.exec("CALL api.job_purge_exchanges(2)");
    assert_eq!(
        count(&b, EXPIRED_REST),
        0,
        "an unlimited run must purge every expired exchange"
    );
    let batches: Option<i64> = b.one(
        "SELECT batches::bigint FROM internal.job_run_log WHERE job_name = 'purge_exchanges' ORDER BY started_at DESC LIMIT 1",
        &[],
    );
    assert_eq!(
        batches,
        Some(3),
        "3 rows in batches of 2 take 2 batches and an empty one"
    );

    a.exec(
        "CREATE FUNCTION api.job_probe_fail_batch(int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'probe failure'; END $$;
         CREATE FUNCTION api.job_probe_ok_batch(int) RETURNS int LANGUAGE sql AS $$ SELECT 0 $$;",
    );
    let failing = db.client();
    let err = failing
        .batch("CALL api.job_run('probe', 'api.job_probe_fail_batch(int)')")
        .expect_err("the failing batch must fail the run");
    assert!(
        describe(&err).contains("probe failure"),
        "the failing batch must fail the run, got {}",
        describe(&err)
    );
    b.exec("CALL api.job_run('probe', 'api.job_probe_ok_batch(int)')");
    assert_eq!(
        job_count(&b, "probe"),
        1,
        "a run started while the failed session holds the job"
    );
    let pid: i32 = failing.one("SELECT pg_backend_pid()", &[]);
    drop(failing);
    let deadline = Instant::now() + Duration::from_secs(10);
    while b.one::<i64>(
        "SELECT count(*) FROM pg_stat_activity WHERE pid = $1",
        &[&pid],
    ) > 0
    {
        assert!(Instant::now() < deadline, "the failed session did not end");
        std::thread::sleep(Duration::from_millis(50));
    }
    b.exec("CALL api.job_run('probe', 'api.job_probe_ok_batch(int)')");
    let matching: i64 = b.one(
        "SELECT count(*) FROM internal.job_run_log
         WHERE job_name = 'probe' AND outcome IS NOT DISTINCT FROM CASE WHEN backend_pid = $1 THEN 'abandoned' ELSE 'succeeded' END",
        &[&pid],
    );
    assert_eq!(
        matching, 2,
        "want the failed run abandoned and the new one succeeded"
    );

    let pooled = db.client();
    assert!(
        pooled
            .batch("CALL api.job_run('probe', 'api.job_probe_fail_batch(int)')")
            .is_err(),
        "the failing batch must fail the run"
    );
    pooled.exec("CALL api.job_run('probe', 'api.job_probe_ok_batch(int)')");
    let held = count(
        &pooled,
        "SELECT count(*) FROM pg_locks l WHERE l.locktype = 'advisory' AND l.pid = pg_backend_pid() AND l.objsubid = 2
            AND l.classid::int4 = (api.job_lock_key('probe'))[1] AND l.objid::int4 = (api.job_lock_key('probe'))[2]",
    );
    assert_eq!(
        held, 0,
        "after its successful run the session still holds the job's lock"
    );
    let before = job_count(&b, "probe");
    b.exec("CALL api.job_run('probe', 'api.job_probe_ok_batch(int)')");
    assert_eq!(
        job_count(&b, "probe"),
        before + 1,
        "another session must run the job"
    );

    let listener = db.client();
    let run: String = a.one("SELECT api.job_run_started('probe')::text", &[]);
    listener.exec("CALL api.job_run('probe', 'api.job_probe_ok_batch(int)')");
    let notices = listener.notices();
    a.query(
        "SELECT api.job_run_finished($1::text::uuid, 0, 0)::text",
        &[&run],
    );
    assert!(
        notices.iter().any(|m| m.contains("job probe skipped")),
        "want a NOTICE naming the skipped job, got {notices:?}"
    );

    let inside = db.client();
    inside.exec("BEGIN");
    let err = inside
        .batch("CALL api.job_run('inside', 'api.job_probe_ok_batch(int)')")
        .expect_err("a CALL inside a transaction block must be refused");
    assert_eq!(
        common::sqlstate(&err).as_deref(),
        Some("2D000"),
        "want 2D000 invalid_transaction_termination, got {}",
        describe(&err)
    );
    inside.exec("ROLLBACK");
    let locks = count(
        &inside,
        "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid()
            AND objsubid = 2 AND classid::int4 = hashtext('api.job_run')",
    );
    assert_eq!(locks, 0, "the refused call left job lock(s) held");
    b.exec("CALL api.job_run('inside', 'api.job_probe_ok_batch(int)')");
    assert_eq!(
        job_count(&b, "inside"),
        1,
        "another session must have run the job once"
    );
}

#[test]
fn jobs_login() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    const DB: &str = "pgmi_rs_a_jobs_login";
    let jobs = format!("{DB}_jobs");
    let _roles = Roles::new(&pg, &[&jobs]);
    let db = pg.database(DB);
    let project = scaffold(&[]);
    let params = [("env", "dev")];
    deployed(&pg, &project, &db, true, &params);
    let owner = db.client();
    let existing: i64 = owner.one("SELECT count(*) FROM pg_roles WHERE rolname = $1", &[&jobs]);
    assert_eq!(existing, 0, "a default deploy created {jobs}");
    owner.exec(&format!(
        "CREATE ROLE {jobs} LOGIN PASSWORD 'jobs-secret' CONNECTION LIMIT 2"
    ));
    let service: String = owner.one(
        "SELECT (membership.fn_register_service_principal('service.jobs', 'Background jobs', $1)).id::text",
        &[&jobs],
    );
    assert_eq!(service, "service.jobs", "register the service principal");
    deployed(&pg, &project, &db, false, &params);

    let conn = db.client_as(&jobs, "jobs-secret");
    conn.exec("CALL api.job_purge_exchanges()");
    let attributed = count(
        &owner,
        "SELECT count(*) FROM internal.job_run_log l
         JOIN membership.service_principal s ON s.database_role = l.database_role
         WHERE l.job_name = 'purge_exchanges' AND l.outcome = 'succeeded' AND s.id = 'service.jobs'",
    );
    assert_eq!(
        attributed, 1,
        "want one succeeded run attributed to service.jobs"
    );
    let err = conn
        .batch("SELECT api.catalog_version()")
        .expect_err("the jobs login must not reach the gateway");
    assert_eq!(
        common::sqlstate(&err).as_deref(),
        Some("42501"),
        "the jobs login must not reach the gateway, got {}",
        describe(&err)
    );
    drop(conn);
    drop(owner);
}

const SIGN_IN: &str = "SELECT api.open_dev_login_session('dev', $1, $2, 'Race')";

#[test]
fn login_sessions_under_concurrency() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let project = scaffold(&[]);
    let db = pg.database("pgmi_rs_a_login_race");
    deployed(&pg, &project, &db, true, &[("env", "dev")]);
    let a = db.client();
    let b = db.client();

    let principals_before = count(&a, "SELECT count(*) FROM membership.principal");
    a.exec("BEGIN");
    let token_a: Option<String> = a.one(SIGN_IN, &[&"race-1", &"race@example.test"]);
    let (token_b, err) = std::thread::scope(|s| {
        let waiter = s.spawn(
            || match b.try_query(SIGN_IN, &[&"race-1", &"race@example.test"]) {
                Ok(rows) => (rows[0].get::<_, Option<String>>(0), None),
                Err(e) => (None, Some(describe(&e))),
            },
        );
        std::thread::sleep(Duration::from_millis(300));
        a.exec("COMMIT");
        waiter.join().expect("session B")
    });
    assert!(
        err.is_none(),
        "session B's sign-in must wait for A and then succeed, got {err:?}"
    );
    assert!(
        token_a.is_some() && token_b.is_some(),
        "both sign-ins must return a handle: A={token_a:?} B={token_b:?}"
    );
    assert_eq!(
        count(
            &a,
            "SELECT count(*) FROM membership.user_principal WHERE email = 'race@example.test'"
        ),
        1,
        "one login must make one person"
    );
    assert_eq!(
        count(&a, "SELECT count(*) FROM membership.principal") - principals_before,
        1,
        "one new principal expected (an orphan was left behind)"
    );

    let token: String = a.one(SIGN_IN, &[&"slide-1", &"slide@example.test"]);
    a.query(
        "UPDATE membership.login_session SET last_seen_at = now() - interval '10 minutes'
         WHERE token_hash = membership.fn_session_token_hash($1)",
        &[&token],
    );
    a.exec("BEGIN");
    a.query(
        "SELECT internal.resolve_session_context($1, NULL)::text",
        &[&token],
    );
    b.exec("SET lock_timeout = '2s'");
    let second = b.try_query(
        "SELECT internal.resolve_session_context($1, NULL)::text",
        &[&token],
    );
    let _ = a.batch("COMMIT");
    if let Err(e) = second {
        panic!(
            "a second request on the same session must not queue behind the first: {}",
            describe(&e)
        );
    }
}

#[test]
fn raced_organization_slug_is_422() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let project = scaffold(&[]);
    let db = pg.database("pgmi_rs_a_slug_race");
    deployed(&pg, &project, &db, true, &[("env", "dev")]);
    let a = db.client();
    let b = db.client();
    const CREATE: &str = "SELECT r.status_code, convert_from(r.content, 'UTF8')::jsonb->>'code'
        FROM api.rest_invoke('POST', '/organizations',
            extensions.hstore(ARRAY['x-login-session', 'content-type'], ARRAY[$1, 'application/json']),
            convert_to('{\"name\": \"Race One\", \"slug\": \"race-one\"}', 'UTF8')) r";
    let token_a: String = a.one(
        "SELECT api.open_dev_login_session('dev', 'slug-a', 'slug-a@example.test', 'A')",
        &[],
    );
    let token_b: String = b.one(
        "SELECT api.open_dev_login_session('dev', 'slug-b', 'slug-b@example.test', 'B')",
        &[],
    );
    a.exec("BEGIN");
    let row = &a.query(CREATE, &[&token_a])[0];
    let status: i32 = row.get(0);
    let code: Option<String> = row.get(1);
    assert_eq!(status, 201, "session A creates race-one: {code:?}");
    let raced = std::thread::scope(|s| {
        let waiter = s.spawn(|| {
            b.try_query(CREATE, &[&token_b])
                .map(|rows| {
                    (
                        rows[0].get::<_, i32>(0),
                        rows[0].get::<_, Option<String>>(1),
                    )
                })
                .map_err(|e| describe(&e))
        });
        std::thread::sleep(Duration::from_millis(300));
        a.exec("COMMIT");
        waiter.join().expect("session B")
    });
    assert_eq!(
        raced,
        Ok((422, Some("membership.organization.slug_taken".to_string()))),
        "the raced create must answer 422 slug_taken"
    );
}

fn postgres_url(db: &Db, user: &str, password: &str) -> String {
    let url = db.url_as(user, password);
    let base = url
        .strip_suffix(db.name())
        .expect("url ends with the database name");
    format!("{base}postgres")
}

#[test]
fn deploys_without_superuser() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    const DEPLOYER: &str = "pgmi_rs_a_nonsuper_deployer";
    const PASSWORD: &str = "deployer-pw";
    const DB: &str = "pgmi_rs_a_nonsuper";
    let names: Vec<String> = ["_admin", "_api", "_kernel", "_owner", "_jobs"]
        .iter()
        .map(|s| format!("{DB}{s}"))
        .chain([DEPLOYER.to_string()])
        .collect();
    let refs: Vec<&str> = names.iter().map(String::as_str).collect();
    let _roles = Roles::new(&pg, &refs);
    let db = pg.database(DB);
    let admin = pg.admin();
    admin.exec(&format!(
        "CREATE ROLE {DEPLOYER} LOGIN CREATEROLE CREATEDB NOSUPERUSER NOBYPASSRLS PASSWORD '{PASSWORD}'"
    ));
    let row = &admin.query(
        "SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = $1",
        &[&DEPLOYER],
    )[0];
    let (sup, bypass): (bool, bool) = (row.get(0), row.get(1));
    assert!(
        !sup && !bypass,
        "deployer has SUPERUSER={sup} BYPASSRLS={bypass}; the test must run without both"
    );
    let conn = postgres_url(&db, DEPLOYER, PASSWORD);
    let project = scaffold(&[]);
    let params = [("env", "dev")];
    let out = deploy_via(&pg, Some(&conn), &project, &db, false, &params);
    assert_eq!(
        out.code, 0,
        "first deploy as a non-superuser:\n{}",
        out.stderr
    );
    {
        let as_deployer = db.client_as(DEPLOYER, PASSWORD);
        as_deployer.exec(&format!("CREATE ROLE {DB}_jobs LOGIN"));
        as_deployer.query(
            "SELECT membership.fn_register_service_principal('service.jobs', 'Background jobs', $1)::text",
            &[&format!("{DB}_jobs")],
        );
    }
    let out = deploy_via(&pg, Some(&conn), &project, &db, false, &params);
    assert_eq!(
        out.code, 0,
        "second deploy as a non-superuser, with a jobs role it created:\n{}",
        out.stderr
    );
    let can_connect: bool = admin.one(
        "SELECT has_database_privilege($1, $2, 'CONNECT')",
        &[&format!("{DB}_jobs"), &DB],
    );
    assert!(
        can_connect,
        "the jobs role the deploy login created got no CONNECT"
    );
}

#[test]
fn admin_password_never_in_role_error() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    const DEPLOYER: &str = "pgmi_rs_a_pwleak_deployer";
    const PASSWORD: &str = "deployer-pw";
    const DB: &str = "pgmi_rs_a_pwleak";
    const SECRET: &str = "TopSecret_Sentinel_4471";
    let names: Vec<String> = ["_admin", "_api", "_kernel", "_owner"]
        .iter()
        .map(|s| format!("{DB}{s}"))
        .chain([DEPLOYER.to_string()])
        .collect();
    let refs: Vec<&str> = names.iter().map(String::as_str).collect();
    let _roles = Roles::new(&pg, &refs);
    let db = pg.database(DB);
    let admin = pg.admin();
    for stmt in [
        format!(
            "CREATE ROLE {DEPLOYER} LOGIN CREATEROLE CREATEDB NOSUPERUSER NOBYPASSRLS PASSWORD '{PASSWORD}'"
        ),
        format!("CREATE ROLE {DB}_owner NOLOGIN"),
        format!("CREATE ROLE {DB}_admin LOGIN IN ROLE {DB}_owner"),
    ] {
        admin.exec(&stmt);
    }
    let out = deploy_via(
        &pg,
        Some(&postgres_url(&db, DEPLOYER, PASSWORD)),
        &scaffold(&[]),
        &db,
        false,
        &[("env", "dev"), ("database_admin_password", SECRET)],
    );
    assert_ne!(
        out.code, 0,
        "deploy succeeded; the deployer must be refused the pre-existing admin role"
    );
    assert_eq!(
        sqlstate(&out),
        "42501",
        "want 42501 (the refused ALTER ROLE):\n{}",
        out.stderr
    );
    let record = out.record();
    for channel in ["error", "detail", "hint", "where"] {
        let text = record[channel].as_str().unwrap_or("");
        assert!(
            !text.contains(SECRET),
            "the admin password appears in the error's {channel}:\n{text}"
        );
    }
    assert!(
        !out.stderr.contains(SECRET) && !out.stdout.contains(SECRET),
        "the admin password appears in the deploy output"
    );
    let message = record["error"].as_str().unwrap_or("");
    assert!(
        message.contains(&format!("{DB}_admin")),
        "the error does not name the role, so the user cannot tell what failed: {message}"
    );
}
