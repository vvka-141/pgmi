use std::sync::LazyLock;

use regex_lite::Regex;

use super::{line_of, tree};

static SQL_FENCE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new("(?s)```sql[^\n]*\n(.*?)```").unwrap());
static PGMI_TEST_CALL: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?i)CALL\s+(?:pg_temp\.)?pgmi_test\s*\(").unwrap());
static TOP_LEVEL_BEGIN: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?im)^\s*BEGIN\s*;").unwrap());
static NULL_BLIND_JSON_COMPARE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?s)IF\s[^;]*?->>\s*'[^']*'\)?\s*(?:<>|!=)[^;]*?THEN\s+RAISE\s+EXCEPTION").unwrap()
});
static SWALLOWED_ERROR: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?is)WHEN\s+OTHERS\s+THEN\s+RAISE\s+WARNING[^;]*;\s*(?:--[^\n]*\n\s*)*END")
        .unwrap()
});
static ASSERT_STATEMENT: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?im)^\s*ASSERT\s").unwrap());
static CONNECTION_HANDLE_BEARER: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?i)authorization:\s*bearer\s+cn_").unwrap());

static RETIRED_APIS: LazyLock<Vec<(Regex, &str)>> = LazyLock::new(|| {
    [
        (r"requiresAuth", "requiresAuth: a handler declares access or permissions"),
        (
            r#"['"](?:uri|version)['"]\s*[:,]\s*['"](?:\^|\.\*)"#,
            "uri/version route keys: a route is a path template",
        ),
        (
            r#"['"]query['"]\s*[:,]\s*(?:jsonb_build_array|\[)"#,
            "query array: query parameters are declared in querySchema",
        ),
        (r"api_key", "API keys: machine access uses approved connections"),
        (
            r"(?i)x-(?:user-id|user-email|tenant-id)\b",
            "identity headers: identity comes from login and organization sessions",
        ),
        (
            r"(?i)authorization:\s*bearer",
            "Authorization: Bearer as identity: identity comes from login and organization sessions",
        ),
        (
            r"provision_current_user|current_user_id|current_member_org_ids|current_user_is_admin",
            "identity readers: use api.current_principal_id(), current_organization_id(), has_permission()",
        ),
        (
            r"(?i)<\w+>_customer|\w+_customer\s+NOLOGIN|customer_role|customer role|→ customer",
            "customer role: the roles are owner, admin, api and kernel",
        ),
        (r"inbound_queue", "inbound_queue: the exchange log replaced it"),
        (
            r"api\.purge_exchanges\b",
            "api.purge_exchanges: use api.job_purge_exchanges or api.purge_exchanges_batch",
        ),
        (
            r"rest_route_policy_for",
            "rest_route_policy_for: removed with route versioning",
        ),
        (
            r"ensure_policy|ensure_rls|has_column|vw_deployment_script",
            "deploy helper removed: structure lives in run-once files",
        ),
        (
            r"pgmi_run_test_source",
            "pgmi_run_test_source: the test macro runs files itself",
        ),
        (
            r"pgmi ai (?:setup|check)",
            "pgmi ai setup/check: pgmi init writes the agent guidance",
        ),
        (
            r"pgmi ai (?:skills?|client)\b|\bai_skills?\b",
            "pgmi ai skill/skills/client: skills live in .agents/skills/, gateways in gateways/",
        ),
        (
            r"tools/mcp-gateway",
            "tools/mcp-gateway.py: the gateways live in gateways/",
        ),
    ]
    .into_iter()
    .map(|(re, why)| (Regex::new(re).unwrap(), why))
    .collect()
});

fn shipped_skills() -> Vec<(String, String)> {
    let files: Vec<(String, String)> = tree("skills")
        .into_iter()
        .chain(
            tree("templates")
                .into_iter()
                .filter(|(p, _)| p.contains("/.agents/skills/")),
        )
        .filter(|(p, _)| p.ends_with(".md"))
        .collect();
    assert!(
        !files.is_empty(),
        "no skill files scanned: the guard checks nothing"
    );
    files
}

fn retired_api_hits(path: &str, markdown: &str) -> Vec<String> {
    let mut out = Vec::new();
    for (i, line) in markdown.replace("\r\n", "\n").split('\n').enumerate() {
        let line = CONNECTION_HANDLE_BEARER.replace_all(line, "");
        for (re, why) in RETIRED_APIS.iter() {
            if let Some(m) = re.find(&line) {
                out.push(format!("{path}:{}  {:?} ({why})", i + 1, m.as_str()));
            }
        }
    }
    out
}

fn silent_failures(path: &str, markdown: &str) -> Vec<String> {
    let text = markdown.replace("\r\n", "\n");
    let mut out = Vec::new();
    for c in SQL_FENCE.captures_iter(&text) {
        let Some(body) = c.get(1) else { continue };
        let block = body.as_str();
        let mut at = |off: usize, why: &str| {
            out.push(format!(
                "{path}:{}  {why}",
                line_of(&text, body.start() + off)
            ));
        };
        if let Some(call) = PGMI_TEST_CALL.find(block)
            && !TOP_LEVEL_BEGIN.is_match(&block[..call.start()])
        {
            at(
                call.start(),
                "CALL pgmi_test() without a preceding top-level BEGIN; (fails with 25P01)",
            );
        }
        for m in NULL_BLIND_JSON_COMPARE.find_iter(block) {
            at(
                m.start(),
                "->> compared with <>/!=: a missing key is NULL and never fails; use IS DISTINCT FROM",
            );
        }
        for m in SWALLOWED_ERROR.find_iter(block) {
            at(
                m.start(),
                "WHEN OTHERS THEN RAISE WARNING without re-raise: a failed file commits and exits 0",
            );
        }
        for m in ASSERT_STATEMENT.find_iter(block) {
            at(
                m.start(),
                "ASSERT: plpgsql.check_asserts = off silences it; fail with RAISE EXCEPTION",
            );
        }
    }
    out
}

#[test]
fn skill_sql_snippets_do_not_teach_silent_failures() {
    let offenders: Vec<String> = shipped_skills()
        .iter()
        .flat_map(|(p, t)| silent_failures(p, t))
        .collect();
    assert!(
        offenders.is_empty(),
        "{} skill snippet(s) teach a pattern that fails or passes silently:\n  {}",
        offenders.len(),
        offenders.join("\n  ")
    );
}

#[test]
fn skills_name_no_retired_api() {
    let offenders: Vec<String> = shipped_skills()
        .iter()
        .flat_map(|(p, t)| retired_api_hits(p, t))
        .collect();
    assert!(
        offenders.is_empty(),
        "{} skill line(s) name a removed or renamed API:\n  {}",
        offenders.len(),
        offenders.join("\n  ")
    );
}

#[test]
fn silent_failures_flags_each_shape() {
    for (name, md) in [
        (
            "pgmi_test without BEGIN",
            "```sql\nCALL pgmi_test();\n```\n",
        ),
        (
            "null-blind json compare",
            "```sql runnable target=__test__/t.sql\nDO $$ BEGIN IF (r->>'k') <> 'v' THEN RAISE EXCEPTION 'x'; END IF; END $$;\n```\n",
        ),
        (
            "swallowed error",
            "```sql\nBEGIN\n  PERFORM 1;\nEXCEPTION WHEN OTHERS THEN RAISE WARNING 'x';\nEND\n```\n",
        ),
        (
            "assert statement",
            "```sql\nDO $$ BEGIN\n    ASSERT (SELECT count(*) FROM t) = 5, 'five rows';\nEND $$;\n```\n",
        ),
    ] {
        let got = silent_failures("skill.md", md);
        assert_eq!(got.len(), 1, "{name}: {got:?}");
    }
    assert!(
        silent_failures(
            "skill.md",
            "```sql\nBEGIN;\nCALL pgmi_test();\nCOMMIT;\n```\n"
        )
        .is_empty()
    );
}

#[test]
fn retired_api_hits_flags_each_term() {
    for (name, line) in [
        ("requiresAuth", "'requiresAuth', true"),
        ("uri route key", "'uri', '^/projects/([0-9]+)$'"),
        ("version route key", r#""version": ".*""#),
        (
            "query array",
            "'query', jsonb_build_array(jsonb_build_object('name', 'q'))",
        ),
        ("api key", "membership.api_key"),
        ("identity header", "send X-User-Id with the request"),
        ("bearer identity", "Authorization: Bearer <token>"),
        (
            "identity reader",
            "WHERE org_id = ANY(api.current_member_org_ids())",
        ),
        (
            "customer role",
            "GRANT USAGE ON SCHEMA api TO <database>_customer;",
        ),
        ("inbound queue", "api.inbound_queue_with_protocol"),
        (
            "purge procedure",
            "CALL api.purge_exchanges(interval '30 days');",
        ),
        ("route policy", "api.rest_route_policy_for('GET', '/x')"),
        (
            "removed helper",
            "SELECT pg_temp.has_column('core', 'task', 'title');",
        ),
        (
            "test runner",
            "pg_temp.pgmi_run_test_source('./__test__/test_a.sql')",
        ),
        ("ai setup", "run pgmi ai setup in the project"),
        ("ai skill", "load it with pgmi ai skill pgmi-sql"),
        ("ai skills", "list them with pgmi ai skills"),
        ("ai client", "pgmi ai client typescript prints a skeleton"),
        ("mcp ai_skill", "call the ai_skill tool"),
        ("python gateway path", "python tools/mcp-gateway.py"),
    ] {
        let got = retired_api_hits("skill.md", &format!("intro\n{line}\n"));
        assert_eq!(got.len(), 1, "{name}: {got:?}");
    }
    for line in [
        "api.job_purge_exchanges(5000) and api.purge_exchanges_batch(cutoff, 100)",
        "'uri', (request).uri",
        "{\"name\", \"version\"}",
        "internal.unloggable_headers() drops x-api-key",
        "WWW-Authenticate: Bearer realm=\"app\"",
        "send the handle as `Authorization: Bearer cn_...` or X-Connection",
        "'querySchema', jsonb_build_object('type', 'object')",
    ] {
        let got = retired_api_hits("skill.md", line);
        assert!(got.is_empty(), "a current API was flagged: {got:?}");
    }
}
