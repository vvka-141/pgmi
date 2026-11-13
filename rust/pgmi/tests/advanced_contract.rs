#![cfg(test)]

mod adv;
mod common;

use adv::{PW, deploy, deployed, probe, refused_with, scaffold};
use common::{Project, pg};

#[test]
fn refuses_file_of_the_wrong_kind() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let cases = [
        (
            "run-once file defining a function",
            false,
            "CREATE TABLE core.kind_probe (id int);\n\nCREATE OR REPLACE FUNCTION core.kind_probe_fn() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;\n",
            "CREATE OR REPLACE FUNCTION",
        ),
        (
            "re-run file creating a table",
            true,
            "CREATE OR REPLACE FUNCTION core.kind_probe_fn() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;\n\n  CREATE TABLE core.kind_probe (id int);\n",
            "CREATE TABLE",
        ),
        (
            "re-run file creating an operator",
            true,
            "CREATE OR REPLACE FUNCTION core.kind_probe_fn(text, int) RETURNS int LANGUAGE sql AS $$ SELECT $2 $$;\n\nCREATE OPERATOR core.?> (LEFTARG = text, RIGHTARG = int, FUNCTION = core.kind_probe_fn);\n",
            "CREATE OPERATOR",
        ),
        (
            "re-run file creating a constraint trigger",
            true,
            "CREATE OR REPLACE FUNCTION core.kind_probe_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;\n\nCREATE CONSTRAINT TRIGGER kind_probe_tg AFTER INSERT ON core.kind_probe\n    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION core.kind_probe_fn();\n",
            "CREATE CONSTRAINT TRIGGER",
        ),
    ];
    for (i, (name, idempotent, body, want)) in cases.iter().enumerate() {
        let file = format!(
            "/*\n<pgmi-meta id=\"ffffffff-0446-4000-8000-00000000000{i}\" idempotent=\"{idempotent}\">\n  <sortKeys><key>900/000</key></sortKeys>\n</pgmi-meta>\n*/\n-- CREATE TABLE in a comment is not a statement\n{body}"
        );
        let project = scaffold(&[("core/kind_probe.sql", &file)]);
        let db = pg.database(&format!("pgmi_rs_a_kind_{i}"));
        let text = adv::refused(&deploy(&pg, &project, &db, true, &[("env", "dev")]));
        assert!(
            text.contains("./core/kind_probe.sql") && text.contains(want),
            "{name}: want an error naming ./core/kind_probe.sql and {want}, got:\n{text}"
        );
    }
}

#[test]
fn refuses_run_once_file_with_several_sort_keys() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let project = scaffold(&[(
        "core/keys_probe.sql",
        "/*\n<pgmi-meta id=\"ffffffff-0489-4000-8000-000000000001\" idempotent=\"false\">\n  <sortKeys><key>900/000</key><key>950/000</key></sortKeys>\n</pgmi-meta>\n*/\nCREATE TABLE core.keys_probe (id int);\n",
    )]);
    let db = pg.database("pgmi_rs_a_keys");
    refused_with(
        &deploy(&pg, &project, &db, true, &[("env", "dev")]),
        "Run-once file(s) with more than one sort key: ./core/keys_probe.sql",
    );
}

const FLIP_PROBE_TABLE: &str = r#"/*
<pgmi-meta id="ffffffff-0264-4000-8000-000000000002" idempotent="false">
  <sortKeys><key>900/000</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE core.flip_probe (id serial PRIMARY KEY, note text);
SELECT pg_temp.exempt_from_rls('core.flip_probe', 'test probe');
"#;

fn flip_probe(idempotent: bool) -> String {
    let body = if idempotent {
        "SELECT count(*) FROM core.flip_probe;\n"
    } else {
        "INSERT INTO core.flip_probe (note) VALUES ('backfill');\n"
    };
    format!(
        "/*\n<pgmi-meta\n    id=\"ffffffff-0264-4000-8000-000000000001\"\n    idempotent=\"{idempotent}\">\n  <description>Flip probe: idempotent on the first deploy, a one-time backfill after.</description>\n  <sortKeys>\n    <key>900/001</key>\n  </sortKeys>\n</pgmi-meta>\n*/\n{body}"
    )
}

#[test]
fn idempotent_to_non_idempotent_flip() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let project = Project::init("advanced");
    project.write("zz_flip_probe_table.sql", FLIP_PROBE_TABLE);
    let db = pg.database("pgmi_rs_a_flip");
    let params = [("database_admin_password", PW), ("env", "test")];

    project.write("zz_flip_probe.sql", &flip_probe(true));
    deployed(&pg, &project, &db, true, &params);
    project.write("zz_flip_probe.sql", &flip_probe(false));
    deployed(&pg, &project, &db, false, &params);
    deployed(&pg, &project, &db, false, &params);

    let c = db.client();
    let rows: i64 = c.one("SELECT count(*) FROM core.flip_probe", &[]);
    assert!(
        rows != 0,
        "the flipped script never ran: its earlier idempotent log rows skipped it, so a one-time backfill would silently never happen"
    );
    assert!(
        rows <= 1,
        "the flipped script ran {rows} times; a one-time backfill must run exactly once"
    );
    let row = &c.query(
        "SELECT count(*) FILTER (WHERE idempotent), count(*) FILTER (WHERE NOT idempotent)
         FROM internal.deployment_script_execution_log
         WHERE deployment_script_object_id = 'ffffffff-0264-4000-8000-000000000001'",
        &[],
    )[0];
    let idempotent_runs: i64 = row.get(0);
    let non_idempotent_runs: i64 = row.get(1);
    assert!(
        idempotent_runs >= 1,
        "the execution log has no idempotent run for the probe ({idempotent_runs}), so the flip was never staged"
    );
    assert_eq!(
        non_idempotent_runs, 1,
        "the execution log must record exactly 1 non-idempotent run"
    );
}

const GUARD_OPTIONS: [&str; 8] = [
    "strict_fk_index",
    "rls_index_method",
    "plpgsql_check",
    "sql_lint",
    "naming",
    "handler_reads_views",
    "view_column_comments",
    "trigger_allowlist",
];

fn switch_on(src: &str, name: &str) -> Option<String> {
    let head = format!("('{name}',");
    let mut out = String::new();
    let mut rest = src;
    let mut found = false;
    while let Some(at) = rest.find(&head) {
        let after = &rest[at + head.len()..];
        let value = after.trim_start_matches(char::is_whitespace);
        let ws = &after[..after.len() - value.len()];
        out.push_str(&rest[..at + head.len()]);
        out.push_str(ws);
        if let Some(tail) = value.strip_prefix("false)") {
            out.push_str("true)");
            rest = tail;
            found = true;
        } else {
            rest = value;
        }
    }
    out.push_str(rest);
    found.then_some(out)
}

fn enable_guard_options(project: &Project, names: &[&str]) {
    let mut src = project.read("deploy/settings.sql");
    for name in names {
        src = switch_on(&src, name)
            .unwrap_or_else(|| panic!("deploy/settings.sql has no switch for guard option {name}"));
    }
    project.write("deploy/settings.sql", &src);
}

#[test]
fn switch_on_flips_only_the_named_row() {
    let src = "VALUES ('naming',   false), ('sql_lint', false), ('naming_x', false)";
    assert_eq!(
        switch_on(src, "naming").as_deref(),
        Some("VALUES ('naming',   true), ('sql_lint', false), ('naming_x', false)")
    );
    assert_eq!(switch_on(src, "absent"), None);
}

#[expect(
    clippy::too_many_lines,
    reason = "the guard fixtures as literal SQL, one per file"
)]
fn guard_files(option: &str) -> Vec<(&'static str, String)> {
    match option {
        "strict_fk_index" => vec![(
            "core/strict_fk.sql",
            probe(
                "ffffffff-0451-4000-8000-000000000101",
                "false",
                "900/000",
                "
CREATE TABLE core.guard_parent (object_id uuid PRIMARY KEY);
CREATE TABLE core.guard_child (parent_id uuid REFERENCES core.guard_parent ON DELETE CASCADE, deleted_at timestamptz);
CREATE INDEX ix_guard_child_live ON core.guard_child (parent_id) WHERE deleted_at IS NULL;
SELECT pg_temp.exempt_from_rls(t::regclass, 'test probe') FROM unnest(ARRAY['core.guard_parent', 'core.guard_child']) t;
",
            ),
        )],
        "rls_index_method" => vec![(
            "core/rls_index.sql",
            probe(
                "ffffffff-0451-4000-8000-000000000102",
                "false",
                "900/000",
                "
CREATE TABLE core.guard_tagged (organization_id uuid, tags text[]);
SELECT core.apply_org_rls('core.guard_tagged', p_has_created_by => false);
CREATE INDEX ix_guard_tagged_tags ON core.guard_tagged USING gin (tags);
",
            ),
        )],
        "plpgsql_check" => vec![(
            "core/plpgsql_check.sql",
            probe(
                "ffffffff-0451-4000-8000-000000000103",
                "true",
                "900/000",
                "
CREATE OR REPLACE FUNCTION core.guard_misspelt() RETURNS uuid LANGUAGE plpgsql AS $f$
BEGIN
    RETURN (SELECT no_such_column FROM membership.organization LIMIT 1);
END $f$;
",
            ),
        )],
        "sql_lint" => vec![(
            "core/sql_lint.sql",
            probe(
                "ffffffff-0451-4000-8000-000000000104",
                "true",
                "900/000",
                "
CREATE OR REPLACE FUNCTION core.guard_null_compare(p_id uuid) RETURNS bigint LANGUAGE sql AS $f$
    SELECT count(*) FROM membership.organization WHERE object_id = NULL
$f$;
",
            ),
        )],
        "naming" => vec![(
            "core/naming.sql",
            probe(
                "ffffffff-0451-4000-8000-000000000105",
                "false",
                "900/000",
                "
CREATE TABLE core.guard_named (id int CONSTRAINT guard_named_pk PRIMARY KEY);
SELECT pg_temp.exempt_from_rls('core.guard_named', 'test probe');
",
            ),
        )],
        "handler_reads_views" => vec![
            (
                "core/reads.sql",
                probe(
                    "ffffffff-0451-4000-8000-000000000106",
                    "false",
                    "900/000",
                    "
CREATE TABLE core.guard_fact (id int);
SELECT pg_temp.exempt_from_rls('core.guard_fact', 'test probe');
",
                ),
            ),
            (
                "api/reads.sql",
                probe(
                    "ffffffff-0451-4000-8000-000000000107",
                    "true",
                    "900/001",
                    "
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object('id', 'ffffffff-0451-4000-8000-000000000108', 'path', '/guard-facts', 'httpMethod', '^GET$',
                       'name', 'guard_facts', 'access', 'public', 'outputSchema', jsonb_build_object('type', 'object')),
    $b$BEGIN
        RETURN api.json_response(200, jsonb_build_object('n', (SELECT count(*) FROM core.guard_fact)));
    END;$b$);
",
                ),
            ),
        ],
        "view_column_comments" => vec![(
            "core/view.sql",
            probe(
                "ffffffff-0451-4000-8000-000000000109",
                "true",
                "900/000",
                "
CREATE OR REPLACE VIEW core.vw_guard_undocumented WITH (security_invoker = true) AS SELECT 1 AS answer;
",
            ),
        )],
        "trigger_allowlist" => vec![
            (
                "core/trigger.sql",
                probe(
                    "ffffffff-0451-4000-8000-000000000110",
                    "false",
                    "900/000",
                    "
CREATE TABLE core.guard_triggered (id int);
SELECT pg_temp.exempt_from_rls('core.guard_triggered', 'test probe');
",
                ),
            ),
            (
                "core/trigger_code.sql",
                probe(
                    "ffffffff-0451-4000-8000-000000000111",
                    "true",
                    "900/001",
                    "
CREATE OR REPLACE FUNCTION core.guard_touch() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN RETURN NEW; END $f$;
CREATE OR REPLACE TRIGGER trg_guard_triggered BEFORE INSERT ON core.guard_triggered
    FOR EACH ROW EXECUTE FUNCTION core.guard_touch();
",
                ),
            ),
        ],
        o => panic!("no probe for guard option {o}"),
    }
}

fn guard_want(option: &str) -> &'static str {
    match option {
        "strict_fk_index" => "core.guard_child (parent_id) -> core.guard_parent",
        "rls_index_method" => "core.ix_guard_tagged_tags (gin on core.guard_tagged)",
        "plpgsql_check" => "core.guard_misspelt(): column \"no_such_column\" does not exist",
        "sql_lint" => "core.guard_null_compare(p_id uuid): NULL comparison",
        "naming" => "primary key core.guard_named_pk on guard_named (want _pkey$)",
        "handler_reads_views" => "guard_facts reads core.guard_fact",
        "view_column_comments" => "core.vw_guard_undocumented.answer",
        "trigger_allowlist" => "core.guard_triggered trg_guard_triggered",
        o => panic!("no expectation for guard option {o}"),
    }
}

#[test]
fn guard_options_all_on_template_green() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let project = scaffold(&[]);
    enable_guard_options(&project, &GUARD_OPTIONS);
    let db = pg.database("pgmi_rs_a_guard_options");
    deployed(&pg, &project, &db, true, &[("env", "dev")]);
}

fn guard_option(option: &str) {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    if option == "plpgsql_check" {
        let available: bool = pg.admin().one(
            "SELECT EXISTS (SELECT FROM pg_available_extensions WHERE name = 'plpgsql_check')
                AND (SELECT rolsuper FROM pg_roles WHERE rolname = current_user)",
            &[],
        );
        if !available {
            eprintln!(
                "skipped: plpgsql_check is not installed on the server, or the test login is not a superuser"
            );
            return;
        }
    }
    let files = guard_files(option);
    let refs: Vec<(&str, &str)> = files.iter().map(|(p, c)| (*p, c.as_str())).collect();
    let project = scaffold(&refs);
    let db = pg.database(&format!("pgmi_rs_a_option_{option}"));
    let params = [("env", "dev")];
    let out = deploy(&pg, &project, &db, true, &params);
    assert_eq!(
        out.code, 0,
        "with {option} off, the planted object must deploy:\n{}",
        out.stderr
    );
    enable_guard_options(&project, &[option]);
    let out = deploy(&pg, &project, &db, true, &params);
    assert_ne!(
        out.code, 0,
        "with {option} on, a deploy holding the forbidden object succeeded"
    );
    let text = adv::error_text(&out);
    let want = format!("Deploy guard {option}:");
    assert!(
        text.contains(&want) && text.contains(guard_want(option)),
        "want the error to contain {want:?} and {:?}, got:\n{text}",
        guard_want(option)
    );
}

#[test]
fn guard_option_strict_fk_index() {
    guard_option("strict_fk_index");
}

#[test]
fn guard_option_rls_index_method() {
    guard_option("rls_index_method");
}

#[test]
fn guard_option_plpgsql_check() {
    guard_option("plpgsql_check");
}

#[test]
fn guard_option_sql_lint() {
    guard_option("sql_lint");
}

#[test]
fn guard_option_naming() {
    guard_option("naming");
}

#[test]
fn guard_option_handler_reads_views() {
    guard_option("handler_reads_views");
}

#[test]
fn guard_option_view_column_comments() {
    guard_option("view_column_comments");
}

#[test]
fn guard_option_trigger_allowlist() {
    guard_option("trigger_allowlist");
}

#[test]
fn pgmiignore_hiding_a_run_once_file_is_refused() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let project = scaffold(&[]);
    const HIDDEN: &str = "lib/core/foundation-structure.sql";
    assert!(
        project.file(HIDDEN).exists(),
        "{HIDDEN} must exist in the scaffold for this test"
    );
    let db = pg.database("pgmi_rs_a_pgmiignore");
    let params = [("env", "dev")];
    deployed(&pg, &project, &db, true, &params);
    let ignore = project.read(".pgmiignore");
    project.write(".pgmiignore", &format!("{ignore}{HIDDEN}\n"));
    let text = adv::refused(&deploy(&pg, &project, &db, false, &params));
    assert!(
        text.contains(&format!("./{HIDDEN}")) && text.contains("removed from the project"),
        "refusal must name ./{HIDDEN} as removed, got:\n{text}"
    );
}

#[test]
fn refuses_hand_altered_policy() {
    let _serial = adv::serial();
    let Some(pg) = pg() else { return };
    let project = scaffold(&[]);
    let db = pg.database("pgmi_rs_a_policy_drift");
    let params = [("env", "dev")];
    deployed(&pg, &project, &db, true, &params);
    db.client()
        .exec("ALTER POLICY invitation_visible ON membership.invitation USING (true)");
    refused_with(
        &deploy(&pg, &project, &db, false, &params),
        "membership.invitation invitation_visible: changed since ./membership/06-rls.sql ran",
    );
    project.write(
        "membership/90-restore-invitations.sql",
        r#"/*
<pgmi-meta id="ffffffff-0451-4000-8000-000000000001" idempotent="false">
  <sortKeys><key>005/000/090</key></sortKeys>
</pgmi-meta>
*/
ALTER POLICY invitation_visible ON membership.invitation
    USING (organization_object_id = (SELECT api.current_organization_id()) OR (SELECT api.is_platform_operator()));
"#,
    );
    deployed(&pg, &project, &db, false, &params);
}
