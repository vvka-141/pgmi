#![cfg(test)]

mod common;

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use common::{Project, pg};
use regex_lite::Regex;

#[derive(Debug, Default)]
struct Catalog {
    schemas: BTreeSet<String>,
    objects: BTreeSet<String>,
    columns: BTreeMap<String, BTreeSet<String>>,
}

struct Block {
    source: String,
    sql: String,
}

fn sql_blocks(source: &str, markdown: &str) -> Vec<Block> {
    let fence = Regex::new(r"(?ms)^```sql[^\n]*\n(.*?)^```\s*$").unwrap();
    fence
        .captures_iter(&markdown.replace("\r\n", "\n"))
        .map(|m| Block {
            source: source.to_string(),
            sql: m[1].to_string(),
        })
        .collect()
}

/// `sql` lower-cased, without comments and string literals, so names in a
/// message or an error code are not taken for references.
fn code(sql: &str) -> String {
    let mut out = String::with_capacity(sql.len());
    let mut chars = sql.chars().peekable();
    while let Some(c) = chars.next() {
        match c {
            '-' if chars.peek() == Some(&'-') => {
                for c in chars.by_ref() {
                    if c == '\n' {
                        out.push('\n');
                        break;
                    }
                }
            }
            '/' if chars.peek() == Some(&'*') => {
                chars.next();
                let mut last = ' ';
                for c in chars.by_ref() {
                    if last == '*' && c == '/' {
                        break;
                    }
                    last = c;
                }
                out.push(' ');
            }
            '\'' => {
                while let Some(c) = chars.next() {
                    if c == '\'' {
                        if chars.peek() == Some(&'\'') {
                            chars.next();
                        } else {
                            break;
                        }
                    }
                }
                out.push_str("''");
            }
            c => out.extend(c.to_lowercase()),
        }
    }
    out
}

fn defined(blocks: &[Block]) -> BTreeSet<String> {
    let create = Regex::new(
        r"\bcreate\s+(?:or\s+replace\s+)?(?:unique\s+)?(?:materialized\s+)?(?:table|view|function|procedure|type|index|sequence|domain|trigger|aggregate)\s+(?:concurrently\s+)?(?:if\s+not\s+exists\s+)?([a-z_][a-z0-9_]*)\.([a-z_][a-z0-9_]*)",
    )
    .unwrap();
    blocks
        .iter()
        .flat_map(|b| {
            create
                .captures_iter(&code(&b.sql))
                .map(|m| format!("{}.{}", &m[1], &m[2]))
                .collect::<Vec<_>>()
        })
        .collect()
}

/// Every name in a template schema that the template does not have and no
/// skill creates, and every column the skills name on a template table
/// (in `REFERENCES t (...)`, `INSERT INTO t (...)` and `ON t (...)`) that
/// the table does not have.
fn problems(blocks: &[Block], catalog: &Catalog) -> Vec<String> {
    let made = defined(blocks);
    let qualified = Regex::new(r"([a-z_][a-z0-9_]*)\.([a-z_][a-z0-9_]*)").unwrap();
    let column_list = Regex::new(
        r"\b(?:references|insert\s+into|on)\s+([a-z_][a-z0-9_]*)\.([a-z_][a-z0-9_]*)\s*\(([^)]*)\)",
    )
    .unwrap();
    let ident = Regex::new(r"^[a-z_][a-z0-9_]*$").unwrap();
    let mut out = BTreeSet::new();
    for b in blocks {
        let sql = code(&b.sql);
        for m in qualified.captures_iter(&sql) {
            let all = m.get(0).unwrap();
            let before = sql[..all.start()].chars().next_back();
            let after = sql[all.end()..].chars().next();
            if before.is_some_and(|c| c == '.' || c == '<' || c == '_' || c.is_alphanumeric())
                || after.is_some_and(|c| c == '.' || c == '<' || c == '{')
            {
                continue;
            }
            let name = format!("{}.{}", &m[1], &m[2]);
            if catalog.schemas.contains(&m[1])
                && !catalog.objects.contains(&name)
                && !made.contains(&name)
            {
                out.insert(format!(
                    "{}: {name} is not in the advanced template",
                    b.source
                ));
            }
        }
        for m in column_list.captures_iter(&sql) {
            let table = format!("{}.{}", &m[1], &m[2]);
            let Some(columns) = catalog.columns.get(&table) else {
                continue;
            };
            for c in m[3].split(',').map(str::trim) {
                if ident.is_match(c) && !columns.contains(c) {
                    out.insert(format!("{}: {table} has no column {c}", b.source));
                }
            }
        }
    }
    out.into_iter().collect()
}

fn walk_md(dir: &Path, out: &mut Vec<PathBuf>) {
    let mut entries: Vec<_> = std::fs::read_dir(dir)
        .unwrap_or_else(|e| panic!("{}: {e}", dir.display()))
        .map(|e| e.unwrap().path())
        .collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            walk_md(&p, out);
        } else if p.extension().is_some_and(|e| e == "md") {
            out.push(p);
        }
    }
}

fn catalog_for_tests() -> Catalog {
    let mut c = Catalog::default();
    c.schemas.extend(["membership".into(), "core".into()]);
    c.objects.extend([
        "membership.organization_membership".into(),
        "core.apply_org_rls".into(),
    ]);
    c.columns.insert(
        "membership.organization_membership".into(),
        ["object_id", "organization_object_id", "principal_object_id"]
            .map(String::from)
            .into(),
    );
    c
}

#[test]
fn a_wrong_tenant_column_or_an_unknown_name_is_reported() {
    let md = "```sql\nCREATE TABLE core.task_note (\n    organization_id uuid NOT NULL,\n    membership_id uuid NOT NULL,\n    FOREIGN KEY (organization_id, membership_id)\n        REFERENCES membership.organization_membership (organization_id, object_id)\n);\nSELECT core.apply_org_rls('core.task_note'); -- core.not_checked\nSELECT core.no_such_helper(), 'membership.connection.pending', core.fn_<entity>_problem(x);\n```\n";
    assert_eq!(
        problems(&sql_blocks("probe.md", md), &catalog_for_tests()),
        [
            "probe.md: core.no_such_helper is not in the advanced template",
            "probe.md: membership.organization_membership has no column organization_id",
        ]
    );
}

#[test]
fn the_right_tenant_column_and_names_a_skill_creates_pass() {
    let md = "```sql runnable target=core/x.sql\nCREATE TABLE core.task_note (organization_object_id uuid,\n    membership_id uuid,\n    FOREIGN KEY (organization_object_id, membership_id)\n        REFERENCES membership.organization_membership (organization_object_id, object_id));\n```\n\n```sql\nINSERT INTO core.task_note (organization_object_id) SELECT 1;\n```\n";
    assert!(problems(&sql_blocks("ok.md", md), &catalog_for_tests()).is_empty());
}

#[test]
fn skill_sql_examples_name_what_the_advanced_template_has() {
    let Some(pg) = pg() else { return };
    let project = Project::init("advanced");
    let db = pg.database("pgmi_rs_skill_schema");
    pg.deploy(&project, &db)
        .params(&[
            ("env", "test"),
            ("database_admin_password", "TestPassword123!"),
        ])
        .overwrite()
        .run()
        .ok();
    let c = db.client();
    let mut catalog = Catalog::default();
    for row in c.query(
        "SELECT nspname::text FROM pg_namespace WHERE nspname !~ '^pg_' AND nspname NOT IN ('information_schema', 'public')",
        &[],
    ) {
        catalog.schemas.insert(row.get(0));
    }
    for row in c.query(
        "SELECT n.nspname || '.' || o.name FROM pg_namespace n
         JOIN (SELECT relnamespace AS ns, relname::text AS name FROM pg_class
               UNION ALL SELECT pronamespace, proname::text FROM pg_proc
               UNION ALL SELECT typnamespace, typname::text FROM pg_type) o ON o.ns = n.oid",
        &[],
    ) {
        catalog.objects.insert(row.get(0));
    }
    for row in c.query(
        "SELECT n.nspname || '.' || r.relname, a.attname::text
         FROM pg_attribute a JOIN pg_class r ON r.oid = a.attrelid
         JOIN pg_namespace n ON n.oid = r.relnamespace
         WHERE a.attnum > 0 AND NOT a.attisdropped AND r.relkind IN ('r', 'p', 'v', 'm', 'f')",
        &[],
    ) {
        catalog
            .columns
            .entry(row.get(0))
            .or_default()
            .insert(row.get(1));
    }
    assert!(
        catalog
            .columns
            .contains_key("membership.organization_membership"),
        "the catalog query found no template tables"
    );
    let mut files = Vec::new();
    walk_md(&project.file(".agents/skills"), &mut files);
    let root = project.path();
    let blocks: Vec<Block> = files
        .iter()
        .flat_map(|f| {
            let source = f.strip_prefix(root).unwrap().display().to_string();
            sql_blocks(&source, &std::fs::read_to_string(f).unwrap())
        })
        .collect();
    assert!(blocks.len() > 100, "only {} SQL blocks found", blocks.len());
    let found = problems(&blocks, &catalog);
    assert!(found.is_empty(), "{}", found.join("\n"));
}
