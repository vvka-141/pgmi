#![cfg(test)]

mod common;

use std::collections::BTreeMap;

use common::{Client, Outcome, Project, pg};

const FINGERPRINT: &str = r"
WITH app AS (
    SELECT oid, nspname FROM pg_namespace
    WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
)
SELECT coalesce(string_agg(line, chr(10) ORDER BY line), '') FROM (
    SELECT format('rel %s.%s %s', a.nspname, c.relname, c.relkind) AS line
    FROM pg_class c JOIN app a ON a.oid = c.relnamespace
    WHERE c.relkind IN ('r', 'v', 'm', 'p', 'i', 'S')
    UNION ALL
    SELECT format('col %s.%s.%s %s', a.nspname, c.relname, att.attname, att.atttypid::regtype)
    FROM pg_attribute att
    JOIN pg_class c ON c.oid = att.attrelid
    JOIN app a ON a.oid = c.relnamespace
    WHERE att.attnum > 0 AND NOT att.attisdropped
    UNION ALL
    SELECT format('con %s.%s %s', a.nspname, c.relname, con.conname)
    FROM pg_constraint con
    JOIN pg_class c ON c.oid = con.conrelid
    JOIN app a ON a.oid = c.relnamespace
    UNION ALL
    SELECT format('pol %s.%s %s', a.nspname, c.relname, p.polname)
    FROM pg_policy p
    JOIN pg_class c ON c.oid = p.polrelid
    JOIN app a ON a.oid = c.relnamespace
    UNION ALL
    SELECT format('trg %s.%s %s', a.nspname, c.relname, tg.tgname)
    FROM pg_trigger tg
    JOIN pg_class c ON c.oid = tg.tgrelid
    JOIN app a ON a.oid = c.relnamespace
    WHERE NOT tg.tgisinternal
    UNION ALL
    SELECT format('fn %s.%s(%s)', a.nspname, pr.proname,
                  pg_get_function_identity_arguments(pr.oid))
    FROM pg_proc pr JOIN app a ON a.oid = pr.pronamespace
) parts";

const ROW_COUNTS: &str = r"
SELECT n.nspname || '.' || c.relname AS name,
       (xpath('/row/c/text()',
              query_to_xml(format('SELECT count(*) AS c FROM %I.%I', n.nspname, c.relname),
                           false, true, '')))[1]::text::bigint AS cnt
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r'
  AND n.nspname NOT LIKE 'pg\_%'
  AND n.nspname <> 'information_schema'";

const APPEND_ONLY_ON_REDEPLOY: [(&str, &str); 5] = [
    (
        "internal.deployment_script_execution_log",
        "audit log: one row per script per deploy, by design",
    ),
    (
        "internal.deployment",
        "deploy history: one row per deploy, by design",
    ),
    (
        "api.rest_exchange",
        "request/response log written by autoLog handlers",
    ),
    (
        "api.exchange",
        "base of the exchange log: counts the rows of api.rest_exchange",
    ),
    (
        "api.access_denial",
        "refusal log, one row per minute: the deploy's anonymous POST /echo demonstration is refused",
    ),
];

const ADVANCED_POSTCONDITIONS: &str = r"
DO $$
DECLARE
    v_problems text[] := '{}';
    v_handlers bigint;
BEGIN
    v_problems := v_problems || ARRAY(
        SELECT format('schema %s absent', s)
        FROM unnest(ARRAY['api', 'core', 'membership', 'internal']) AS s
        WHERE NOT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = s)
    );

    v_problems := v_problems || ARRAY(
        SELECT format('role %s uncreated', current_database() || s)
        FROM unnest(ARRAY['_owner', '_admin', '_api', '_kernel']) AS s
        WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_database() || s)
    );

    IF to_regclass('api.handler') IS NULL THEN
        v_problems := v_problems || 'api.handler absent - the route registry was never created'::text;
    ELSE
        SELECT count(*) INTO v_handlers FROM api.handler WHERE deleted_at IS NULL;
        IF v_handlers = 0 THEN
            v_problems := v_problems || 'route registry empty - the deploy registered no handlers'::text;
        END IF;
    END IF;

    IF cardinality(v_problems) > 0 THEN
        RAISE EXCEPTION 'advanced deploy post-conditions failed: %',
            array_to_string(v_problems, '; ');
    END IF;
END $$;
";

fn min_executed_tests(template: &str) -> usize {
    match template {
        "minimal" => 0,
        "basic" => 1,
        "advanced" => 25,
        t => panic!("no test floor for template {t}"),
    }
}

fn framework_line_budget(template: &str) -> usize {
    match template {
        "minimal" => 2,
        "basic" => 11,
        "advanced" => 45,
        t => panic!("no framework NOTICE budget for template {t}"),
    }
}

fn notices(out: &Outcome) -> Vec<String> {
    let lines = out.lines();
    let start = lines
        .iter()
        .position(|l| *l == "Executing deploy.sql")
        .map_or(0, |i| i + 1);
    let end = lines
        .iter()
        .rposition(|l| l.starts_with("ok "))
        .unwrap_or(lines.len());
    lines[start..end.max(start)]
        .iter()
        .map(|l| (*l).to_string())
        .collect()
}

fn tally(lines: &[String]) -> (usize, Vec<String>) {
    let mut framework = 0;
    let mut chatty = Vec::new();
    let mut step = String::new();
    let mut count = 0;
    for l in lines {
        if l.starts_with("[pgmi] Executing: ") {
            step.clear();
        } else if let Some(s) = l
            .strip_prefix("[pgmi] Fixture: ")
            .or_else(|| l.strip_prefix("[pgmi] Test: "))
        {
            step = s.to_string();
            count = 0;
        } else if l.starts_with("[pgmi] Test suite passed") {
            step.clear();
            framework += 1;
        } else if !step.is_empty() {
            count += 1;
            if count == 2 {
                chatty.push(step.clone());
            }
        } else {
            framework += 1;
        }
    }
    (framework, chatty)
}

#[test]
fn tally_counts_framework_lines_and_names_chatty_steps() {
    let run = |extra: &[&str]| -> Vec<String> {
        let mut v: Vec<String> = [
            "[pgmi] Parameter: env = test",
            "[pgmi] Executing: ./lib/a.sql",
            "[pgmi] Executing: ./lib/b.sql",
            "[pgmi] Catalog data/x.json: 1 row(s)",
            "[pgmi] Test suite started",
            "[pgmi] Fixture: ./__test__/_setup.sql",
            "[pgmi] Test: ./__test__/test_a.sql",
            "  + a holds",
            "[pgmi] Test: ./__test__/test_b.sql",
        ]
        .iter()
        .map(|s| (*s).to_string())
        .collect();
        v.extend(extra.iter().map(|s| (*s).to_string()));
        v.extend(["[pgmi] Test suite passed".to_string(), "DONE".to_string()]);
        v
    };
    assert_eq!(tally(&run(&[])), (5, vec![]));
    let (_, chatty) = tally(&run(&["  + b holds", "  + b also holds"]));
    assert_eq!(chatty, ["./__test__/test_b.sql"]);
}

fn fingerprint(c: &Client) -> String {
    let f: String = c.one(FINGERPRINT, &[]);
    assert!(
        !f.is_empty(),
        "schema fingerprint is empty, so comparing it would prove nothing"
    );
    f
}

fn row_counts(c: &Client) -> BTreeMap<String, i64> {
    let counts: BTreeMap<String, i64> = c
        .query(ROW_COUNTS, &[])
        .iter()
        .map(|r| (r.get(0), r.get(1)))
        .collect();
    assert!(
        !counts.is_empty(),
        "no tables found, so comparing row counts would prove nothing"
    );
    counts
}

fn data_drift(before: &BTreeMap<String, i64>, after: &BTreeMap<String, i64>) -> Vec<String> {
    let mut drift = Vec::new();
    for (name, was) in before {
        let Some(now) = after.get(name) else {
            drift.push(format!("{name} disappeared"));
            continue;
        };
        if let Some((_, reason)) = APPEND_ONLY_ON_REDEPLOY.iter().find(|(t, _)| t == name) {
            if now < was {
                drift.push(format!("{name} shrank, but it is append-only ({reason})"));
            }
        } else if now != was {
            drift.push(format!("{name} changed across a redeploy"));
        }
    }
    for name in after.keys() {
        if !before.contains_key(name) {
            drift.push(format!("{name} appeared only after the redeploy"));
        }
    }
    drift
}

#[test]
fn data_drift_spares_only_growing_append_only_tables() {
    let m = |pairs: &[(&str, i64)]| -> BTreeMap<String, i64> {
        pairs.iter().map(|(k, v)| ((*k).to_string(), *v)).collect()
    };
    let before = m(&[("internal.deployment", 1), ("core.x", 2), ("core.gone", 1)]);
    let after = m(&[("internal.deployment", 2), ("core.x", 3), ("core.new", 0)]);
    assert_eq!(
        data_drift(&before, &after),
        [
            "core.gone disappeared",
            "core.x changed across a redeploy",
            "core.new appeared only after the redeploy"
        ]
    );
    let shrank = data_drift(
        &m(&[("internal.deployment", 2)]),
        &m(&[("internal.deployment", 1)]),
    );
    assert_eq!(shrank.len(), 1);
}

#[expect(
    clippy::too_many_lines,
    reason = "one deploy, then each check Go's template test makes"
)]
fn deploy_template(template: &str) {
    let Some(pg) = pg() else { return };
    let project = Project::init(template);
    let db = pg.database(&format!("pgmi_rs_template_{template}"));
    let params: &[(&str, &str)] = if template == "advanced" {
        &[
            ("env", "test"),
            ("database_admin_password", "TestPassword123!"),
        ]
    } else {
        &[("env", "dev")]
    };

    let first = pg
        .deploy(&project, &db)
        .params(params)
        .overwrite()
        .run()
        .ok();
    let first_notices = notices(&first);
    let ran = first_notices
        .iter()
        .filter(|l| l.starts_with("[pgmi] Test: "))
        .count();
    assert!(
        ran >= min_executed_tests(template),
        "{template} deploy ran {ran} test(s), want >= {}",
        min_executed_tests(template)
    );
    if template == "basic" {
        for path in ["./physical/001_project.sql", "./semantic/task.sql"] {
            assert!(
                first_notices.contains(&format!("[pgmi] Executing: {path}")),
                "a basic deploy must name every file it runs at NOTICE; {path} missing"
            );
        }
    }
    let (framework, chatty) = tally(&first_notices);
    assert!(
        framework <= framework_line_budget(template),
        "{template} deploy printed {framework} framework NOTICE lines, want <= {}:\n{}",
        framework_line_budget(template),
        first_notices.join("\n")
    );
    assert!(
        chatty.is_empty(),
        "printed more than one NOTICE line; demote the rest to DEBUG: {chatty:?}"
    );

    let before = (template != "minimal").then(|| {
        let c = db.client();
        (fingerprint(&c), row_counts(&c))
    });

    let second = pg.deploy(&project, &db).params(params).run().ok();
    let second_notices = notices(&second);
    for n in &second_notices {
        assert!(
            !n.contains("already exists, skipping"),
            "a converged redeploy should be quiet, got: {n}"
        );
    }

    if let Some((fp, counts)) = before {
        let c = db.client();
        let after = fingerprint(&c);
        assert!(
            after == fp,
            "redeploying changed the schema, so the second deploy is not idempotent\nbefore:\n{fp}\nafter:\n{after}"
        );
        let drift = data_drift(&counts, &row_counts(&c));
        assert!(
            drift.is_empty(),
            "redeploying changed data that should have been idempotent:\n  {}\nA seed INSERT without ON CONFLICT looks exactly like this.",
            drift.join("\n  ")
        );
    }

    if template == "minimal" {
        for (i, n) in [first_notices, second_notices].iter().enumerate() {
            assert_eq!(
                n,
                &["Executing: ./hello_world.sql", "Hello, world!"],
                "minimal deploy {}",
                i + 1
            );
        }
    }

    let c = db.client();
    assert_eq!(c.one::<i32>("SELECT 1", &[]), 1);
    match template {
        "basic" => {
            let row = &c.query(
                "SELECT (SELECT state FROM project_view WHERE name = 'Demo'), (SELECT count(*) FROM deployment.run_once)",
                &[],
            )[0];
            let state: Option<String> = row.get(0);
            let run_once: i64 = row.get(1);
            assert_eq!(
                (state.as_deref(), run_once),
                (Some("empty"), 2),
                "basic deploy: Demo project state and run-once file count"
            );
        }
        "advanced" => c.exec(ADVANCED_POSTCONDITIONS),
        _ => {}
    }
}

#[test]
fn minimal_template_deploys_and_redeploys() {
    deploy_template("minimal");
}

#[test]
fn basic_template_deploys_and_redeploys() {
    deploy_template("basic");
}

#[test]
fn advanced_template_deploys_and_redeploys() {
    deploy_template("advanced");
}
