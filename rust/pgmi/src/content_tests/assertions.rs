use std::path::Path;
use std::sync::LazyLock;

use regex_lite::Regex;

use super::tree;

struct Rule {
    name: &'static str,
    pattern: Regex,
    fix: &'static str,
    test_only: bool,
    raw: bool,
}

static RULES: LazyLock<Vec<Rule>> = LazyLock::new(|| {
    let rule = |name, pattern: &str, fix, test_only, raw| Rule {
        name,
        pattern: Regex::new(pattern).unwrap(),
        fix,
        test_only,
        raw,
    };
    vec![
        rule(
            "!= or <> in an assertion",
            r"(?:!=|<>)",
            "use IS DISTINCT FROM — NULL != 'x' is NULL, not true",
            false,
            false,
        ),
        rule(
            "bare boolean operand",
            r"^(?:NOT\s+)?[a-z_][a-z0-9_]*(?:\.[a-z_][a-z0-9_]*)?$",
            "use <expr> IS DISTINCT FROM true/false — a NULL flag takes neither branch",
            false,
            false,
        ),
        rule(
            "unguarded NOT LIKE",
            r"(?:^|[^)\s])\s+NOT\s+LIKE",
            "wrap the left operand in coalesce(<expr>, '') — NULL NOT LIKE 'x' is NULL",
            false,
            false,
        ),
        rule(
            "unguarded array_length",
            r"(?:^|[^_a-z(])array_length\(",
            "use coalesce(array_length(x, 1), 0) — an empty array yields NULL, not 0",
            false,
            false,
        ),
        rule(
            "NOT over a value that can be NULL",
            r"(?:^|\(|\bOR |\bAND )NOT (?:\(|[a-z_])",
            "write (<expr>) IS NOT TRUE — NOT NULL is NULL, so the RAISE is skipped",
            true,
            false,
        ),
        rule(
            "= in an assertion",
            r"[^<>!=:]=[^>=]",
            "use IS NOT DISTINCT FROM — NULL = x is NULL, so the RAISE is skipped",
            true,
            false,
        ),
        rule(
            "!~ in an assertion",
            r"!~",
            "wrap the left operand in coalesce(<expr>, '') — NULL !~ 'x' is NULL",
            true,
            false,
        ),
        rule(
            "function result IS NOT DISTINCT FROM a value",
            r"(?:^|[^a-z0-9_])(?:[a-z_][a-z0-9_]*\.)?(?:[a-bd-z_][a-z0-9_]*|c(?:[a-np-z0-9_][a-z0-9_]*|o(?:[a-bd-z0-9_][a-z0-9_]*)?)?)\([^()]*\)\s+IS NOT DISTINCT FROM\s+(?:-?[0-9]|'|true\b|false\b)",
            "write coalesce(<fn>(...), <neutral>) = <value> — a NULL result is not the value, so the RAISE is skipped",
            true,
            true,
        ),
        rule(
            "NOT BETWEEN in an assertion",
            r"(?:^|[^)\s])\s+NOT BETWEEN\b",
            "write (<expr> BETWEEN a AND b) IS NOT TRUE — NULL NOT BETWEEN a AND b is NULL",
            true,
            false,
        ),
        rule(
            "bare <, <=, > or >= in an assertion",
            r"(?:^|[^)\s])\s+(?:<|<=|>|>=)\s",
            "write (<a> < <b>) IS NOT FALSE (or IS TRUE) — NULL < x is NULL, so the RAISE is skipped",
            true,
            false,
        ),
    ]
});

const NULL_SAFE_ALLOWED: [(&str, &str); 0] = [];
const NULL_UNSAFE_ALLOWED: [(&str, &str); 0] = [];

static IF_HEADER: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"^\s*(?:IF|ELSIF)\b").unwrap());
static THEN_TAIL: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"\bTHEN\s*$").unwrap());
static RAISE_START: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^\s*RAISE\s+EXCEPTION\b").unwrap());
static SQL_LITERAL: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"'(?:[^']|'')*'").unwrap());
static EXISTS_ONLY: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^(?:NOT\s+)?EXISTS\s*\(.*\)$").unwrap());
static EXISTS_NOT_FOUND: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"\bNOT (?:EXISTS|FOUND)\b").unwrap());
static NULL_TESTS: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"\bIS (?:NOT )?(?:TRUE|FALSE|NULL|DISTINCT FROM)\b|\bIS NOT DISTINCT FROM\b")
        .unwrap()
});

fn in_test_dir(path: &str) -> bool {
    path.contains("/__test__/") || path.contains("/__tests__/")
}

fn scan_assertions(path: &str, body: &str) -> Vec<String> {
    let mut out = Vec::new();
    let body = body.replace("\r\n", "\n");
    let lines: Vec<&str> = body.split('\n').collect();
    let mut i = 0;
    while i < lines.len() {
        if !IF_HEADER.is_match(lines[i]) {
            i += 1;
            continue;
        }
        let mut end = i;
        while end < lines.len() && !THEN_TAIL.is_match(lines[end].trim_end_matches([' ', '\t'])) {
            end += 1;
        }
        if end >= lines.len() {
            i += 1;
            continue;
        }
        let mut next = end + 1;
        while next < lines.len() && lines[next].trim().is_empty() {
            next += 1;
        }
        if next >= lines.len() || !RAISE_START.is_match(lines[next]) {
            i = end + 1;
            continue;
        }
        let cond = condition_text(&lines[i..=end]);
        if total_by_construction(&cond) || allowed(path, &cond) {
            i = end + 1;
            continue;
        }
        let shape = without_subqueries(&cond);
        let in_test = in_test_dir(path);
        for rule in RULES.iter() {
            if rule.test_only && !in_test {
                continue;
            }
            let subject = if rule.raw { &cond } else { &shape };
            if rule.pattern.is_match(subject) {
                out.push(format!(
                    "{path}:{} [{}] {}\n      -> {}",
                    i + 1,
                    rule.name,
                    truncate(&cond, 110),
                    rule.fix
                ));
            }
        }
        i = end + 1;
    }
    out
}

fn total_by_construction(cond: &str) -> bool {
    EXISTS_ONLY.is_match(cond)
        || cond.contains("SQLERRM")
        || cond.contains("SQLSTATE")
        || cond.contains("try_cast(")
        || cond.contains("?>")
}

fn allowed(path: &str, cond: &str) -> bool {
    NULL_SAFE_ALLOWED.iter().any(|(key, _)| {
        let (file, prefix) = key.split_once(": ").unwrap_or((key, ""));
        path.ends_with(file) && cond.starts_with(prefix)
    })
}

fn without_subqueries(cond: &str) -> String {
    let mut out = fold_calls(cond, "(SELECT ", "()");
    out = fold_calls(&out, "coalesce(", "(TOTAL)");
    out = EXISTS_NOT_FOUND.replace_all(&out, "TOTAL").into_owned();
    while let Some(m) = NULL_TESTS.find(&out) {
        let left = operand_start(&out[..m.start()]);
        out = format!("{}TOTAL {}", &out[..left], &out[m.end()..]);
    }
    out
}

fn fold_calls(cond: &str, prefix: &str, with: &str) -> String {
    let mut out = cond.to_string();
    let open = prefix.find('(').unwrap_or(0);
    while let Some(start) = out.find(prefix) {
        let bytes = out.as_bytes();
        let mut depth = 0i32;
        let mut end = None;
        for (i, b) in bytes.iter().enumerate().skip(start + open) {
            match b {
                b'(' => depth += 1,
                b')' => depth -= 1,
                _ => {}
            }
            if depth == 0 {
                end = Some(i);
                break;
            }
        }
        let Some(end) = end else { break };
        out = format!("{}{with}{}", &out[..start], &out[end + 1..]);
    }
    out
}

fn operand_start(s: &str) -> usize {
    let s = s.trim_end_matches(' ');
    let b = s.as_bytes();
    if s.ends_with(')') {
        let mut depth = 0i32;
        for i in (0..b.len()).rev() {
            match b[i] {
                b')' => depth += 1,
                b'(' => depth -= 1,
                _ => {}
            }
            if depth == 0 {
                let mut i = i;
                while i > 0
                    && (b[i - 1] == b'.' || b[i - 1] == b'_' || b[i - 1].is_ascii_alphanumeric())
                {
                    i -= 1;
                }
                return i;
            }
        }
        return 0;
    }
    let mut i = b.len();
    while i > 0 && b[i - 1] != b' ' && b[i - 1] != b'(' {
        i -= 1;
    }
    i
}

fn condition_text(lines: &[&str]) -> String {
    let mut joined = lines.join(" ");
    if let Some(idx) = joined.find("--") {
        joined.truncate(idx);
    }
    let joined = SQL_LITERAL.replace_all(&joined, "''");
    let joined = IF_HEADER.replace_all(&joined, "");
    let trimmed = joined.trim_end_matches([' ', '\t']);
    let joined = THEN_TAIL.replace_all(trimmed, "");
    joined.split_whitespace().collect::<Vec<_>>().join(" ")
}

fn truncate(s: &str, n: usize) -> String {
    if s.len() <= n {
        return s.to_string();
    }
    let mut cut = n;
    while !s.is_char_boundary(cut) {
        cut -= 1;
    }
    format!("{}…", &s[..cut])
}

fn example_sql() -> Vec<(String, String)> {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let mut out = Vec::new();
    let mut stack = vec![root.join("examples")];
    while let Some(dir) = stack.pop() {
        let Ok(entries) = std::fs::read_dir(&dir) else {
            continue;
        };
        for e in entries.flatten() {
            let p = e.path();
            if p.is_dir() {
                stack.push(p);
            } else if p.extension().is_some_and(|x| x == "sql") {
                let rel = p
                    .strip_prefix(&root)
                    .unwrap()
                    .to_string_lossy()
                    .replace('\\', "/");
                out.push((rel, std::fs::read_to_string(&p).unwrap()));
            }
        }
    }
    out
}

#[test]
fn template_tests_avoid_null_unsafe_comparisons() {
    let unsafe_op = Regex::new(r"<>|!=").unwrap();
    let mut offenders = Vec::new();
    for (path, body) in tree("templates") {
        if !path.ends_with(".sql") || !in_test_dir(&path) {
            continue;
        }
        if NULL_UNSAFE_ALLOWED.iter().any(|(p, _)| *p == path) {
            continue;
        }
        for (i, line) in body.split('\n').enumerate() {
            let code = line.split_once("--").map_or(line, |(c, _)| c);
            if unsafe_op.is_match(code) {
                offenders.push(format!("{path}:{}  {}", i + 1, line.trim()));
            }
        }
    }
    assert!(
        offenders.is_empty(),
        "{} NULL-unsafe comparison(s) in template tests:\n  {}\nUse IS DISTINCT FROM so a NULL counts as a difference, or allow the file in NULL_UNSAFE_ALLOWED with the reason its operands cannot be NULL.",
        offenders.len(),
        offenders.join("\n  ")
    );
}

#[test]
fn template_assertions_are_total() {
    let offenders: Vec<String> = tree("templates")
        .into_iter()
        .filter(|(p, _)| p.ends_with(".sql"))
        .chain(example_sql())
        .flat_map(|(p, b)| scan_assertions(&p, &b))
        .collect();
    assert!(
        offenders.is_empty(),
        "{} assertion(s) evaluate to NULL when their input is missing, so they cannot fail:\n  {}",
        offenders.len(),
        offenders.join("\n  ")
    );
}

#[test]
fn scan_assertions_flags_each_null_unsafe_shape() {
    let wrap = |head: &str| {
        format!("DO $$ BEGIN\n    {head}\n        RAISE EXCEPTION 'x';\n    END IF;\nEND $$;\n")
    };
    for (rule, head) in [
        (
            "NOT over a value that can be NULL",
            "IF NOT v_body ? 'x' THEN",
        ),
        ("= in an assertion", "IF v_etag_after = v_etag THEN"),
        ("!~ in an assertion", "IF v_html !~ 'x' THEN"),
        (
            "function result IS NOT DISTINCT FROM a value",
            "IF strpos(v_message, v_code) IS NOT DISTINCT FROM 0 THEN",
        ),
        (
            "NOT BETWEEN in an assertion",
            "IF (v_body->>'at')::timestamptz NOT BETWEEN now() AND now() + interval '1 hour' THEN",
        ),
        (
            "bare <, <=, > or >= in an assertion",
            "IF (v_new->>'expiresAt')::timestamptz <= now() THEN",
        ),
    ] {
        let got = scan_assertions("templates/advanced/lib/__test__/probe.sql", &wrap(head));
        assert!(
            got.len() == 1 && got[0].contains(rule),
            "{head}: want one {rule:?} finding, got {got:?}"
        );
        let outside = scan_assertions("templates/advanced/deploy.sql", &wrap(head));
        assert!(
            outside.is_empty(),
            "{head} outside a test file: {outside:?}"
        );
    }
    for head in [
        "IF (v_body ? 'x') IS NOT TRUE THEN",
        "IF (v_etag_after = v_etag) IS NOT FALSE THEN",
        "IF NOT EXISTS (SELECT FROM t WHERE a = b) THEN",
        "IF NOT FOUND THEN",
        "IF (SELECT count(*) FROM t WHERE a = b) IS DISTINCT FROM 1 THEN",
        "IF coalesce(strpos(v_message, v_code), 0) IS NOT DISTINCT FROM 0 THEN",
        "IF ((v_body->>'at')::timestamptz BETWEEN now() AND now() + interval '1 hour') IS NOT TRUE THEN",
        "IF ((v_new->>'expiresAt')::timestamptz > now()) IS NOT TRUE THEN",
        "IF coalesce(v_count, 0) < 10 THEN",
        "IF coalesce(v_msg, '') NOT LIKE '%x%' THEN",
    ] {
        let got = scan_assertions("templates/advanced/lib/__test__/probe.sql", &wrap(head));
        assert!(got.is_empty(), "{head}: want no finding, got {got:?}");
    }
}
