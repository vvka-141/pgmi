#![cfg(test)]

mod common;

use common::{Client, Db, Pg, Project, describe, pg, sqlstate};

fn deploy_advanced(pg: &Pg, name: &str) -> (Project, Db) {
    let p = Project::init("advanced");
    let db = pg.database(name);
    pg.deploy(&p, &db)
        .params(&[
            ("database_admin_password", "TestPassword123!"),
            ("env", "test"),
        ])
        .overwrite()
        .run()
        .ok();
    (p, db)
}

fn contend(db: &Db, setup: &str, invoke: &str, table: &str, winner_ok: impl Fn(&Client)) {
    let pool = db.client();
    pool.exec(setup);
    let winner = db.client();
    let loser = db.client();

    winner.exec("BEGIN ISOLATION LEVEL REPEATABLE READ");
    winner_ok(&winner);

    loser.exec("BEGIN ISOLATION LEVEL REPEATABLE READ");
    loser.exec("SELECT 1");

    let (loser, result) = std::thread::scope(|s| {
        let h = s.spawn(move || {
            let r = loser.try_query(invoke, &[]).map(|_| ());
            (loser, r)
        });
        winner.exec("COMMIT");
        h.join().expect("loser thread")
    });

    let err = result.expect_err(
        "the losing transaction must fail: the serialization failure was swallowed and a response returned, so the client has no way to know it should retry",
    );
    assert_eq!(
        sqlstate(&err).as_deref(),
        Some("40001"),
        "expected SQLSTATE 40001 (serialization_failure) to reach the client, got {}",
        describe(&err)
    );

    loser.exec("ROLLBACK");
    loser.exec("BEGIN ISOLATION LEVEL REPEATABLE READ");
    let retry = loser.try_query(invoke, &[]).unwrap_or_else(|e| {
        panic!(
            "retry on a fresh transaction should succeed, got: {}",
            describe(&e)
        )
    });
    assert_eq!(retry.len(), 1);
    loser.exec("COMMIT");

    let fin: i32 = pool.one(&format!("SELECT v FROM {table} WHERE id = 1"), &[]);
    assert_eq!(
        fin, 2,
        "both increments must be durable after the retry; counter is {fin}, want 2 (a value of 1 means a write was silently dropped)"
    );
    if invoke.contains("rest_invoke") {
        let status: i32 = retry[0].get(0);
        assert_eq!(status, 200, "retry should have returned 200, got {status}");
    }
}

#[test]
fn serialization_failure_reaches_the_rest_client() {
    let Some(pg) = pg() else { return };
    let (_p, db) = deploy_advanced(&pg, "pgmi_rs_b_retry_contract");
    let invoke = "SELECT (api.rest_invoke('POST', '/contend')).status_code";
    contend(
        &db,
        r"
CREATE TABLE public.contended_counter (id int PRIMARY KEY, v int NOT NULL);
INSERT INTO public.contended_counter VALUES (1, 0);
GRANT ALL ON public.contended_counter TO PUBLIC;
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'ffffffff-0179-4000-8000-000000000001',
        'path', '/contend',
        'httpMethod', '^POST$',
        'name', 'contend_test',
        'access', 'public',
        'autoLog', false
    ),
    $body$
BEGIN
    UPDATE public.contended_counter SET v = v + 1 WHERE id = 1;
    RETURN api.json_response(200, jsonb_build_object('ok', true));
END;
    $body$
);
",
        invoke,
        "public.contended_counter",
        |w| {
            let status: i32 = w.one(invoke, &[]);
            assert_eq!(
                status, 200,
                "winner should have succeeded, got status {status}"
            );
        },
    );
}

#[test]
fn serialization_failure_reaches_the_mcp_client() {
    let Some(pg) = pg() else { return };
    let (_p, db) = deploy_advanced(&pg, "pgmi_rs_b_mcp_retry_contract");
    let invoke = r#"SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"c1","method":"tools/call","params":{"name":"mcp_contend_test","arguments":{}}}'::jsonb
)).envelope::text"#;
    contend(
        &db,
        r"
CREATE TABLE public.mcp_contended_counter (id int PRIMARY KEY, v int NOT NULL);
INSERT INTO public.mcp_contended_counter VALUES (1, 0);
GRANT ALL ON public.mcp_contended_counter TO PUBLIC;
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'ffffffff-0179-4000-8000-000000000002',
        'type', 'tool', 'effect', 'write', 'reversible', true,
        'name', 'mcp_contend_test',
        'description', 'Increments a contended row',
        'inputSchema', jsonb_build_object('type', 'object'),
        'access', 'public'
    ),
    $body$
BEGIN
    UPDATE public.mcp_contended_counter SET v = v + 1 WHERE id = 1;
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('ok')),
        (request).request_id
    );
END;
    $body$
);
",
        invoke,
        "public.mcp_contended_counter",
        |w| {
            let envelope: String = w.one(invoke, &[]);
            assert!(
                !envelope.contains("\"error\""),
                "winner should have succeeded, got: {envelope}"
            );
        },
    );
}
