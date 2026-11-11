#![cfg(test)]

mod common;

use std::collections::BTreeSet;
use std::path::Path;

use common::{Pg, Project, pg};
use regex_lite::Regex;

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

#[derive(Debug, Default, Clone)]
struct Block {
    source: String,
    target: String,
    tail: bool,
    requires: Vec<String>,
    sql: String,
}

fn clean(path: &str) -> String {
    let mut parts: Vec<&str> = Vec::new();
    let absolute = path.starts_with('/');
    for p in path.split('/') {
        match p {
            "" | "." => {}
            ".." if parts.last().is_some_and(|l| *l != "..") => {
                parts.pop();
            }
            ".." if absolute => {}
            p => parts.push(p),
        }
    }
    let joined = parts.join("/");
    match (absolute, joined.is_empty()) {
        (true, _) => format!("/{joined}"),
        (false, true) => ".".to_string(),
        (false, false) => joined,
    }
}

fn parse_runnable_blocks(source: &str, markdown: &str) -> Result<Vec<Block>, String> {
    let fence = Regex::new(r"(?ms)^```sql runnable([^\n]*)\n(.*?)^```\s*$").unwrap();
    let text = markdown.replace("\r\n", "\n");
    let mut blocks = Vec::new();
    for m in fence.captures_iter(&text) {
        let mut b = Block {
            source: source.to_string(),
            sql: m[2].to_string(),
            ..Block::default()
        };
        for field in m[1].split_whitespace() {
            match field.split_once('=') {
                Some(("target", v)) => b.target = clean(v),
                Some(("kind", "tail")) => b.tail = true,
                Some(("requires", v)) => b.requires.extend(v.split(',').map(String::from)),
                _ => return Err(format!("{source}: unknown runnable attribute {field:?}")),
            }
        }
        if b.tail == !b.target.is_empty() {
            return Err(format!(
                "{source}: a runnable block needs exactly one of target=<path> or kind=tail"
            ));
        }
        if !b.target.is_empty() && (b.target.starts_with("..") || b.target.starts_with('/')) {
            return Err(format!(
                "{source}: target {:?} must stay inside the project",
                b.target
            ));
        }
        blocks.push(b);
    }
    Ok(blocks)
}

#[test]
fn parse_runnable_blocks_reads_targets_tails_and_requirements() {
    let md = "intro\n```sql\nSELECT 'illustration';\n```\n```sql runnable target=__test__/test_a.sql\nSELECT 1;\n```\n```sql runnable kind=tail requires=btree_gist,hstore\nVACUUM;\n```\n";
    let blocks = parse_runnable_blocks("skill.md", md).unwrap();
    assert_eq!(
        blocks.len(),
        2,
        "plain sql is illustration, so only two blocks run"
    );
    assert_eq!(blocks[0].target, "__test__/test_a.sql");
    assert_eq!(blocks[0].sql, "SELECT 1;\n");
    assert!(blocks[1].tail);
    assert_eq!(blocks[1].requires, ["btree_gist", "hstore"]);

    const ONE: &str = "skill.md: a runnable block needs exactly one of target=<path> or kind=tail";
    for (name, bad, want) in [
        ("no target", "```sql runnable\nSELECT 1;\n```\n", ONE),
        (
            "target and tail",
            "```sql runnable target=a.sql kind=tail\nSELECT 1;\n```\n",
            ONE,
        ),
        (
            "escapes project",
            "```sql runnable target=../a.sql\nSELECT 1;\n```\n",
            "skill.md: target \"../a.sql\" must stay inside the project",
        ),
        (
            "unknown key",
            "```sql runnable target=a.sql flavour=x\nSELECT 1;\n```\n",
            "skill.md: unknown runnable attribute \"flavour=x\"",
        ),
    ] {
        assert_eq!(
            parse_runnable_blocks("skill.md", bad).err().as_deref(),
            Some(want),
            "{name}"
        );
    }
}

fn walk_md(dir: &Path, out: &mut Vec<std::path::PathBuf>) {
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

fn project_skill_blocks(project: &Project) -> Vec<Block> {
    let mut files = Vec::new();
    walk_md(&project.file(".agents/skills"), &mut files);
    let mut blocks = Vec::new();
    for f in files {
        let rel = f
            .strip_prefix(project.path())
            .unwrap()
            .to_string_lossy()
            .replace('\\', "/");
        let content = std::fs::read_to_string(&f).unwrap();
        blocks.extend(parse_runnable_blocks(&rel, &content).unwrap_or_else(|e| panic!("{e}")));
    }
    blocks
}

fn available_extensions(pg: &Pg) -> BTreeSet<String> {
    pg.admin()
        .query("SELECT name FROM pg_available_extensions", &[])
        .iter()
        .map(|r| r.get(0))
        .collect()
}

fn quote_literal(s: &str) -> String {
    format!("'{}'", s.replace('\'', "''"))
}

fn deploy_skill_blocks(
    pg: &Pg,
    template: &str,
    project: &Project,
    db: &str,
) -> Result<usize, String> {
    let blocks = project_skill_blocks(project);
    let available = available_extensions(pg);
    let mut markers = Vec::new();
    for (i, b) in blocks.iter().enumerate() {
        let missing: Vec<&str> = b
            .requires
            .iter()
            .filter(|e| !available.contains(*e))
            .map(String::as_str)
            .collect();
        if !missing.is_empty() {
            eprintln!(
                "SKIPPED {} block {}: the server lacks extension(s) {}",
                b.source,
                i + 1,
                missing.join(", ")
            );
            continue;
        }
        let marker = format!("pgmi-skill-block {} {}", i + 1, b.source);
        let body = format!(
            "{}\nDO $marker$ BEGIN RAISE NOTICE {}; END $marker$;\n",
            b.sql,
            quote_literal(&marker)
        );
        markers.push(marker);
        if b.tail {
            let deploy = project.read("deploy.sql");
            project.write("deploy.sql", &format!("{deploy}\n{body}"));
            continue;
        }
        if project.file(&b.target).exists() {
            return Err(format!(
                "{} block {}: target {} already exists in the {template} template",
                b.source,
                i + 1,
                b.target
            ));
        }
        project.write(&b.target, &body);
    }

    let database = pg.database(db);
    let out = pg
        .deploy(project, &database)
        .param("env", "dev")
        .overwrite()
        .run();
    if out.code != 0 {
        return Err(out.stderr);
    }
    let seen: BTreeSet<&str> = out.lines().into_iter().collect();
    let mut ran = 0;
    for marker in &markers {
        if !seen.contains(marker.as_str()) {
            return Err(format!(
                "block {marker:?} was written but nothing executed it: put its target where deploy.sql runs files, or in a __test__/ directory"
            ));
        }
        ran += 1;
    }
    Ok(ran)
}

fn enable_guard_options(project: &Project, names: &[&str]) {
    let mut out = project.read("deploy.sql");
    for name in names {
        let row = Regex::new(&format!(r"(\('{name}',\s*)false\)")).unwrap();
        assert!(
            row.is_match(&out),
            "deploy.sql has no switch for guard option {name}"
        );
        out = row.replace_all(&out, "${1}true)").into_owned();
    }
    project.write("deploy.sql", &out);
}

fn skill_blocks_deploy(template: &str) {
    let Some(pg) = pg() else { return };
    let project = Project::init(template);
    let ran = deploy_skill_blocks(
        &pg,
        template,
        &project,
        &format!("pgmi_rs_c_skill_blocks_{template}"),
    )
    .unwrap_or_else(|e| panic!("skill blocks of the {template} template do not deploy: {e}"));
    assert!(
        ran > 0,
        "no runnable skill block ran for the {template} template: the gate checks nothing"
    );
}

#[test]
fn skill_blocks_of_the_basic_template_deploy() {
    skill_blocks_deploy("basic");
}

#[test]
fn skill_blocks_of_the_advanced_template_deploy() {
    skill_blocks_deploy("advanced");
}

#[test]
fn skill_blocks_of_the_advanced_template_pass_every_guard() {
    let Some(pg) = pg() else { return };
    let project = Project::init("advanced");
    enable_guard_options(&project, &GUARD_OPTIONS);
    deploy_skill_blocks(&pg, "advanced", &project, "pgmi_rs_c_skill_blocks_guarded")
        .unwrap_or_else(|e| {
            panic!("skill blocks of the advanced template do not pass its guards: {e}")
        });
}

#[test]
fn skill_blocks_gate_fails_on_a_broken_block() {
    let Some(pg) = pg() else { return };
    let project = Project::init("basic");
    project.write(
        ".agents/skills/broken-probe/SKILL.md",
        "---\nname: broken-probe\ndescription: probe\n---\n```sql runnable target=__test__/test_broken_probe.sql\nSELECT * FROM no_such_table;\n```\n",
    );
    match deploy_skill_blocks(&pg, "basic", &project, "pgmi_rs_c_skill_blocks_broken") {
        Err(e) if e.contains("no_such_table") => {}
        other => panic!("a broken block must fail the gate with its own error, got {other:?}"),
    }
}

#[test]
fn skill_blocks_gate_fails_on_an_unexecuted_block() {
    let Some(pg) = pg() else { return };
    let project = Project::init("basic");
    project.write(
        ".agents/skills/orphan-probe/SKILL.md",
        "---\nname: orphan-probe\ndescription: probe\n---\n```sql runnable target=elsewhere/never_run.txt\nSELECT 1;\n```\n",
    );
    match deploy_skill_blocks(&pg, "basic", &project, "pgmi_rs_c_skill_blocks_orphan") {
        Err(e) if e.contains("nothing executed it") => {}
        other => panic!("an unexecuted block must fail the gate, got {other:?}"),
    }
}

fn naming_violations(sql: &str) -> Vec<String> {
    let problem_code = Regex::new(r"(?i)\b(?:code\s*=>|problem\s*:=)\s*'([^']*)'").unwrap();
    let created_function = Regex::new(
        r"(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+([a-z_][a-z0-9_]*)\.([a-z_][a-z0-9_]*)",
    )
    .unwrap();
    let writer_verb = Regex::new(r"^(create|add|update|delete|complete|set)_").unwrap();
    let mut found = Vec::new();
    for m in problem_code.captures_iter(sql) {
        if !m[1].contains('.') {
            found.push(format!(
                "problem code '{}' has no dot: write <schema>.<entity>.<reason>",
                &m[1]
            ));
        }
    }
    for m in created_function.captures_iter(sql) {
        if writer_verb.is_match(&m[2].to_lowercase()) {
            found.push(format!(
                "writer function {}.{} must be named fn_{}",
                &m[1], &m[2], &m[2]
            ));
        }
    }
    found
}

#[test]
fn naming_violations_flag_undotted_codes_and_unprefixed_writers() {
    for (sql, want) in [
        (
            "RETURN api.problem_response(404, 'Not Found', 'x', code => 'not_found');",
            "has no dot",
        ),
        ("v_problem text; BEGIN problem := 'missing';", "has no dot"),
        (
            "CREATE OR REPLACE FUNCTION public.create_x() RETURNS void",
            "fn_create_x",
        ),
        (
            "create function core.complete_task(p uuid) returns void",
            "fn_complete_task",
        ),
        ("code => 'core.task.not_found'", ""),
        ("CREATE FUNCTION core.fn_create_task() RETURNS void", ""),
        ("CREATE FUNCTION core.task_problem(p uuid) RETURNS text", ""),
    ] {
        let got = naming_violations(sql).join("\n");
        if want.is_empty() {
            assert!(got.is_empty(), "{sql:?}: unexpected violation {got}");
        } else {
            assert!(
                got.contains(want),
                "{sql:?}: want a violation containing {want:?}, got {got:?}"
            );
        }
    }
}

#[test]
fn skill_blocks_follow_the_naming_rules() {
    let mut checked = 0;
    let mut violations = Vec::new();
    for template in ["basic", "advanced"] {
        let project = Project::init(template);
        for b in project_skill_blocks(&project) {
            checked += 1;
            for v in naming_violations(&b.sql) {
                violations.push(format!("{} ({template}): {v}", b.source));
            }
        }
    }
    assert!(
        checked > 0,
        "no runnable skill block found: the naming gate checks nothing"
    );
    assert!(violations.is_empty(), "{}", violations.join("\n"));
}
