#![cfg(test)]

mod adv;
mod common;

use adv::{PW, Roles, deploy, deployed, refused, refused_with, scaffold, sqlstate};
use common::pg;

const DEV_PROBE: &str = r#"/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000020" idempotent="false">
  <sortKeys><key>900/000</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE core.dev_probe (id int);
SELECT pg_temp.exempt_from_rls('core.dev_probe', 'test probe');
"#;

struct Case {
    name: &'static str,
    files: &'static [(&'static str, &'static str)],
    params: Vec<(&'static str, String)>,
    want: String,
    not_want: Option<&'static str>,
}

fn case(name: &'static str, params: &[(&'static str, &str)], want: &str) -> Case {
    Case {
        name,
        files: &[],
        params: params.iter().map(|(k, v)| (*k, (*v).to_string())).collect(),
        want: want.to_string(),
        not_want: None,
    }
}

#[test]
#[expect(
    clippy::too_many_lines,
    reason = "one refusal per parameter, each a short block"
)]
fn refuses_bad_parameters() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let project = scaffold(&[]);
    const DB: &str = "pgmi_rs_a_params";
    let long_role = "r".repeat(64);
    let cases = vec![
        Case {
            not_want: Some("database_admin_password"),
            ..case(
                "no env",
                &[("database_admin_password", PW)],
                "Missing required parameter: env",
            )
        },
        Case {
            not_want: Some("database_admin_password"),
            ..case(
                "no env and no password",
                &[],
                "Missing required parameter: env",
            )
        },
        case(
            "blank env",
            &[("env", "  ")],
            "Missing required parameter: env",
        ),
        case(
            "unknown env",
            &[("env", "staging"), ("database_admin_password", PW)],
            "env must be dev, test or prod",
        ),
        Case {
            not_want: Some("--param <key>"),
            ..case(
                "prod without password",
                &[("env", "prod")],
                "Missing required parameters: database_admin_password. Set PGMI_PARAM_DATABASE_ADMIN_PASSWORD",
            )
        },
        case(
            "prod with blank password",
            &[("env", "prod"), ("database_admin_password", " ")],
            "Missing required parameters: database_admin_password",
        ),
        case(
            "relabel naming another label",
            &[
                ("env", "prod"),
                ("relabel_environment", "test"),
                ("database_admin_password", PW),
            ],
            "relabel_environment=test does not name the label env=prod",
        ),
        case(
            "relabel on a new database",
            &[
                ("env", "prod"),
                ("relabel_environment", "prod"),
                ("database_admin_password", PW),
            ],
            "carries no environment label yet",
        ),
        case(
            "upgrade of a new database",
            &[
                ("env", "prod"),
                ("upgrade", "true"),
                ("database_admin_password", PW),
            ],
            "a first deploy is not an upgrade",
        ),
        case(
            "upgrade not a boolean",
            &[
                ("env", "prod"),
                ("upgrade", "yes"),
                ("database_admin_password", PW),
            ],
            "upgrade must be true or false",
        ),
        case(
            "retention not a whole number",
            &[
                ("env", "test"),
                ("database_admin_password", PW),
                ("exchange_retention_days", "7.5"),
            ],
            "exchange_retention_days must be a whole number of days",
        ),
        case(
            "retention zero",
            &[
                ("env", "test"),
                ("database_admin_password", PW),
                ("exchange_retention_days", "0"),
            ],
            "exchange_retention_days must be a whole number of days",
        ),
        case(
            "challenge with a line break",
            &[
                ("env", "test"),
                ("database_admin_password", PW),
                ("www_authenticate", "Bearer\r\nSet-Cookie: x=1"),
            ],
            "www_authenticate must be one header value",
        ),
        case(
            "undeclared parameter",
            &[
                ("env", "test"),
                ("database_admin_password", PW),
                ("no_such_param", "x"),
            ],
            "Unknown parameters: no_such_param",
        ),
        case(
            "mfa flag not a boolean",
            &[
                ("env", "test"),
                ("database_admin_password", PW),
                ("platform_mfa_required", "yes"),
            ],
            "platform_mfa_required must be true or false",
        ),
        case(
            "api base path with a trailing slash",
            &[
                ("env", "test"),
                ("database_admin_password", PW),
                ("api_base_path", "/api/"),
            ],
            "api_base_path must be / or a path like /api",
        ),
        case(
            "provider pattern matching a template role",
            &[
                ("env", "dev"),
                ("provider_role_pattern", "pgmi\\_rs\\_a\\_%"),
            ],
            "provider_role_pattern matches the template's own role(s): database_admin_role (pgmi_rs_a_params_admin)",
        ),
        case(
            "provider schemas not an array",
            &[("env", "dev"), ("provider_schemas", "__db_system")],
            "provider_schemas must be a JSON array of schema names",
        ),
        case(
            "provider schemas holding a number",
            &[
                ("env", "dev"),
                ("provider_schemas", r#"["__db_system", 1]"#),
            ],
            "provider_schemas must be a JSON array of schema names",
        ),
        Case {
            files: &[(
                "core/setting_probe.sql",
                "/*\n<pgmi-meta id=\"f6470000-0000-4000-8000-000000000001\" idempotent=\"true\">\n  <sortKeys><key>005/100/probe/1</key></sortKeys>\n</pgmi-meta>\n*/\nSELECT pg_temp.deployment_setting('no_such_setting');\n",
            )],
            ..case(
                "a file reads an undeclared setting",
                &[("env", "dev")],
                r#"Required deployment parameter "no_such_setting" not set"#,
            )
        },
        case(
            "role name past the identifier limit",
            &[("env", "dev"), ("database_owner_role", &long_role)],
            "Role name(s) longer than 63 bytes: database_owner_role (64 bytes)",
        ),
        case(
            "upgrade and relabel together",
            &[
                ("env", "prod"),
                ("upgrade", "true"),
                ("relabel_environment", "prod"),
                ("database_admin_password", PW),
            ],
            "upgrade=true and relabel_environment=prod together",
        ),
        Case {
            files: &[("core/dev/__test__/test_probe.sql", "SELECT 1;\n")],
            ..case(
                "test under a dev folder",
                &[("env", "dev")],
                "Test file(s) under a dev/ folder: ./core/dev/__test__/test_probe.sql",
            )
        },
    ];
    let stateful = [
        "relabel on a new database",
        "upgrade of a new database",
        "test under a dev folder",
    ];
    for c in &cases {
        let own;
        let path = if c.files.is_empty() {
            &project
        } else {
            own = scaffold(c.files);
            &own
        };
        let db = pg.database(DB);
        let params: Vec<(&str, &str)> = c.params.iter().map(|(k, v)| (*k, v.as_str())).collect();
        let out = deploy(&pg, path, &db, true, &params);
        let text = refused(&out);
        assert!(
            text.contains(&c.want),
            "{}: want {:?}, got:\n{text}",
            c.name,
            c.want
        );
        if let Some(nw) = c.not_want {
            assert!(
                !text.contains(nw),
                "{}: the error must not mention {nw:?}, got:\n{text}",
                c.name
            );
        }
        if !stateful.contains(&c.name) {
            assert_eq!(
                sqlstate(&out),
                "22023",
                "{}: want SQLSTATE 22023 invalid_parameter_value",
                c.name
            );
        }
        let schemas: i64 = db.client().one(
            "SELECT count(*) FROM pg_namespace WHERE nspname IN ('internal', 'core', 'api', 'membership')",
            &[],
        );
        assert_eq!(schemas, 0, "{}: the refused deploy created schemas", c.name);
    }
}

#[test]
#[expect(
    clippy::too_many_lines,
    reason = "one deploy per environment, checked in sequence"
)]
fn environment_label() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let project = scaffold(&[("core/dev/dev_probe.sql", DEV_PROBE)]);
    const DB: &str = "pgmi_rs_a_env_label";
    const PROD_PW: &str = "ProdPassword123!";
    let db = pg.database(DB);

    let state = || -> (Option<String>, bool, String) {
        let c = db.client();
        let row = &c.query(
            "SELECT (SELECT label FROM internal.environment), to_regclass('core.dev_probe') IS NOT NULL",
            &[],
        )[0];
        let password: String = c.one(
            "SELECT rolpassword FROM pg_authid WHERE rolname = $1",
            &[&format!("{DB}_admin")],
        );
        (row.get(0), row.get(1), password)
    };
    let expect = |label: &str, dev_probe: bool| -> String {
        let (l, p, password) = state();
        assert_eq!(l.as_deref(), Some(label), "label");
        assert_eq!(p, dev_probe, "core.dev_probe present");
        password
    };
    let rejected = |params: &[(&str, &str)], want: &str| {
        refused_with(&deploy(&pg, &project, &db, false, params), want);
    };

    deployed(
        &pg,
        &project,
        &db,
        true,
        &[("env", "prod"), ("database_admin_password", PROD_PW)],
    );
    let prod_password = expect("prod", false);

    rejected(
        &[("env", "dev")],
        "env=dev does not match database pgmi_rs_a_env_label's environment label (prod)",
    );
    rejected(
        &[("env", "dev"), ("relabel_environment", "dev")],
        "Missing required parameters: database_admin_password",
    );
    rejected(
        &[
            ("env", "prod"),
            ("relabel_environment", "prod"),
            ("database_admin_password", PROD_PW),
        ],
        "is already labelled prod",
    );
    rejected(
        &[
            ("env", "test"),
            ("upgrade", "true"),
            ("database_admin_password", PROD_PW),
        ],
        "env=test does not match",
    );
    assert_eq!(
        expect("prod", false),
        prod_password,
        "a refused deploy changed the admin role's password"
    );

    deployed(
        &pg,
        &project,
        &db,
        false,
        &[
            ("env", "prod"),
            ("upgrade", "true"),
            ("database_admin_password", PROD_PW),
        ],
    );
    deployed(
        &pg,
        &project,
        &db,
        false,
        &[
            ("env", "test"),
            ("relabel_environment", "test"),
            ("database_admin_password", PROD_PW),
        ],
    );
    expect("test", false);
    deployed(
        &pg,
        &project,
        &db,
        false,
        &[
            ("env", "dev"),
            ("relabel_environment", "dev"),
            ("database_admin_password", PROD_PW),
        ],
    );
    expect("dev", true);

    let dev_entry = || -> (bool, i64, i64) {
        let row = &db.client().query(
            "SELECT to_regprocedure('api.open_dev_login_session(text, text, text, text)') IS NOT NULL,
                    (SELECT count(*) FROM membership.login_session s
                     JOIN membership.external_identity e ON e.object_id = s.external_identity_object_id
                     JOIN membership.identity_issuer i ON i.id = e.issuer_id
                     WHERE i.kind = 'development' AND s.revoked_at IS NULL),
                    (SELECT count(*) FROM membership.identity_issuer i
                     WHERE i.kind = 'development'
                       AND NOT EXISTS (SELECT FROM membership.external_identity e WHERE e.issuer_id = i.id))",
            &[],
        )[0];
        (row.get(0), row.get(1), row.get(2))
    };
    {
        let c = db.client();
        let token: Option<String> = c.one(
            "SELECT api.open_dev_login_session('dev', 'eve-001', 'eve@example.test', 'Eve')",
            &[],
        );
        assert!(
            token.is_some(),
            "a dev database must open a dev login session"
        );
        c.exec("INSERT INTO membership.identity_issuer (id, kind) VALUES ('stray', 'development')");
    }
    assert_eq!(
        dev_entry(),
        (true, 1, 1),
        "on dev: function, open dev sessions, unreferenced dev issuers"
    );

    deployed(
        &pg,
        &project,
        &db,
        false,
        &[
            ("env", "test"),
            ("relabel_environment", "test"),
            ("database_admin_password", PROD_PW),
        ],
    );
    expect("test", true);
    assert_eq!(
        dev_entry(),
        (false, 0, 0),
        "after the relabel from dev: function, open dev sessions, unreferenced dev issuers"
    );
}

#[test]
fn refuses_a_role_name_too_long() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let name = format!("pgmi_rs_a_long_{}", "n".repeat(45));
    let db = pg.database(&name);
    let out = deploy(&pg, &scaffold(&[]), &db, true, &[("env", "dev")]);
    let text = refused(&out);
    assert!(
        sqlstate(&out) == "22023" && text.contains("database_kernel_role (67 bytes)"),
        "want 22023 naming database_kernel_role, got {}: {text}",
        sqlstate(&out)
    );
    let roles: i64 = pg.admin().one(
        "SELECT count(*) FROM pg_roles WHERE rolname LIKE 'pgmi\\_rs\\_a\\_long\\_%'",
        &[],
    );
    assert_eq!(roles, 0, "role(s) created before the refusal");
}

#[test]
fn refuses_a_foreign_admin_role() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    const DB: &str = "pgmi_rs_a_foreign_admin";
    const ROLE: &str = "pgmi_rs_a_foreign_admin_login";
    let _roles = Roles::new(&pg, &[ROLE]);
    let db = pg.database(DB);
    let admin = pg.admin();
    admin.exec(&format!(
        "CREATE ROLE {ROLE} LOGIN PASSWORD 'prod-secret-123'"
    ));
    let before: String = admin.one(
        "SELECT rolpassword FROM pg_authid WHERE rolname = $1",
        &[&ROLE],
    );
    let out = deploy(
        &pg,
        &scaffold(&[]),
        &db,
        true,
        &[("env", "dev"), ("database_admin_role", ROLE)],
    );
    let text = refused(&out);
    assert!(
        sqlstate(&out) == "22023" && text.contains("database_admin_role"),
        "want 22023 naming database_admin_role, got {}: {text}",
        sqlstate(&out)
    );
    let row = &admin.query(
        "SELECT a.rolpassword, (SELECT count(*) FROM pg_auth_members m WHERE m.member = a.oid)
         FROM pg_authid a WHERE a.rolname = $1",
        &[&ROLE],
    )[0];
    let after: String = row.get(0);
    let memberships: i64 = row.get(1);
    assert!(
        after == before && memberships == 0,
        "foreign role touched: password changed {}, memberships {memberships}",
        after != before
    );
}

#[test]
fn refuses_a_foreign_template_role() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    const DB: &str = "pgmi_rs_a_foreign_roles";
    let mallory = format!("{DB}_mallory");
    let project = scaffold(&[]);
    let names: Vec<String> = ["_api", "_owner", "_kernel", "_jobs"]
        .iter()
        .map(|s| format!("{DB}{s}"))
        .chain([mallory.clone()])
        .collect();
    let refs: Vec<&str> = names.iter().map(String::as_str).collect();
    for (name, setup, role) in [
        (
            "api role created by a CREATEROLE peer",
            format!("SET ROLE {mallory}; CREATE ROLE {DB}_api NOLOGIN; RESET ROLE"),
            format!("{DB}_api"),
        ),
        (
            "owner role with a foreign member",
            format!("CREATE ROLE {DB}_owner NOLOGIN; GRANT {DB}_owner TO {mallory}"),
            format!("{DB}_owner"),
        ),
        (
            "kernel role with a foreign member",
            format!("CREATE ROLE {DB}_kernel NOLOGIN; GRANT {DB}_kernel TO {mallory}"),
            format!("{DB}_kernel"),
        ),
        (
            "jobs role created by a CREATEROLE peer",
            format!("SET ROLE {mallory}; CREATE ROLE {DB}_jobs LOGIN; RESET ROLE"),
            format!("{DB}_jobs"),
        ),
    ] {
        let _roles = Roles::new(&pg, &refs);
        let db = pg.database(DB);
        let admin = pg.admin();
        admin.exec(&format!("CREATE ROLE {mallory} LOGIN CREATEROLE; {setup}"));
        let out = deploy(&pg, &project, &db, true, &[("env", "dev")]);
        let text = refused(&out);
        assert!(
            sqlstate(&out) == "22023" && text.contains(&role),
            "{name}: want 22023 naming {role}, got {}: {text}",
            sqlstate(&out)
        );
        let memberships: i64 = admin.one(
            "SELECT count(*) FROM pg_auth_members WHERE roleid = to_regrole($1)",
            &[&role],
        );
        assert_eq!(memberships, 1, "{name}: {role} memberships changed");
        drop(db);
    }
}

#[test]
fn ignores_an_unregistered_jobs_login() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    const DB: &str = "pgmi_rs_a_unregistered_jobs";
    let mallory = format!("{DB}_mallory");
    let jobs = format!("{DB}_jobs");
    let project = scaffold(&[]);
    let params = [("env", "dev")];
    let privileges = |db: &common::Db| -> (i64, bool, bool) {
        let row = &db.client().query(
            "SELECT (SELECT count(*) FROM pg_proc
                     WHERE pronamespace = 'api'::regnamespace AND proname LIKE 'job\\_%'
                       AND has_function_privilege($1, oid, 'EXECUTE')),
                    has_schema_privilege($1, 'api', 'USAGE'),
                    has_database_privilege($1, current_database(), 'CONNECT')",
            &[&jobs],
        )[0];
        (row.get(0), row.get(1), row.get(2))
    };
    let want_none = |db: &common::Db| {
        let (routines, usage, connect) = privileges(db);
        assert!(
            routines == 0 && !usage && !connect,
            "unregistered {jobs} holds EXECUTE on {routines} job(s), USAGE on api {usage}, CONNECT {connect}"
        );
    };

    for (name, setup) in [
        (
            "created by a CREATEROLE login that then dropped itself",
            format!(
                "CREATE ROLE {mallory} LOGIN CREATEROLE; SET ROLE {mallory}; CREATE ROLE {jobs} LOGIN; RESET ROLE; DROP ROLE {mallory}"
            ),
        ),
        (
            "created by a superuser",
            format!("CREATE ROLE {jobs} LOGIN"),
        ),
    ] {
        let _roles = Roles::new(&pg, &[&jobs, &mallory]);
        let db = pg.database(DB);
        pg.admin().exec(&setup);
        let out = deploy(&pg, &project, &db, true, &params);
        assert_eq!(out.code, 0, "{name}: deploy:\n{}", out.stderr);
        want_none(&db);
    }

    {
        let _roles = Roles::new(&pg, &[&jobs, &mallory]);
        let db = pg.database(DB);
        deployed(&pg, &project, &db, true, &params);
        let c = db.client();
        c.exec(&format!("CREATE ROLE {jobs} LOGIN"));
        c.query(
            "SELECT membership.fn_register_service_principal('service.jobs', 'Background jobs', $1)::text",
            &[&jobs],
        );
        deployed(&pg, &project, &db, false, &params);
        let (routines, usage, connect) = privileges(&db);
        assert!(
            routines > 0 && usage && connect,
            "registered {jobs} holds EXECUTE on {routines} job(s), USAGE on api {usage}, CONNECT {connect}"
        );
        c.query(
            "DELETE FROM membership.service_principal WHERE database_role = $1",
            &[&jobs],
        );
        deployed(&pg, &project, &db, false, &params);
        want_none(&db);
    }

    {
        let _roles = Roles::new(&pg, &[&jobs, &mallory]);
        let db = pg.database(DB);
        deployed(&pg, &project, &db, true, &params);
        pg.admin().exec(&format!(
            "CREATE ROLE {mallory} LOGIN; GRANT {DB}_admin TO {mallory}"
        ));
        let out = deploy(&pg, &project, &db, false, &params);
        let text = refused(&out);
        assert!(
            sqlstate(&out) == "22023" && text.contains(&format!("{DB}_admin")),
            "want 22023 naming {DB}_admin, got {}: {text}",
            sqlstate(&out)
        );
    }
}

#[test]
fn redeploys_with_a_gateway_login_in_the_api_role() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    const DB: &str = "pgmi_rs_a_gateway_member";
    const GATEWAY: &str = "pgmi_rs_a_gateway_member_login";
    let _roles = Roles::new(&pg, &[GATEWAY]);
    let db = pg.database(DB);
    let project = scaffold(&[]);
    let params = [("env", "dev")];
    deployed(&pg, &project, &db, true, &params);
    pg.admin().exec(&format!(
        "CREATE ROLE {GATEWAY} LOGIN; GRANT {DB}_api TO {GATEWAY}"
    ));
    deployed(&pg, &project, &db, false, &params);
    deployed(&pg, &project, &db, true, &params);
}

#[test]
fn deploys_with_quoted_or_custom_role_names() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let project = scaffold(&[]);
    for (name, params) in [
        ("pgmi-rs-a-hyphen-app", vec![("env", "dev")]),
        (
            "pgmi_rs_a_custom_owner",
            vec![
                ("env", "dev"),
                ("database_owner_role", "pgmi_rs_a_custom_owner_ownr"),
            ],
        ),
    ] {
        let db = pg.database(name);
        let out = deploy(&pg, &project, &db, true, &params);
        assert_eq!(out.code, 0, "{name}: deploy:\n{}", out.stderr);
    }
}
