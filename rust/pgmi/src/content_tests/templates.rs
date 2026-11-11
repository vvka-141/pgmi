use regex_lite::Regex;

use super::{embedded, read, repo_file, scaffold};
use crate::config::yaml::ProjectConfig;
use crate::deploy::script;
use crate::project::lexer::{self, TokenKind};
use crate::project::walk::{self, DEFAULT_MAX_FILE_SIZE};
use crate::templates::Template;
use crate::vfs::{self, EmbeddedFs, MemFs, Vfs};

fn loaded(fs: &MemFs) -> Vec<String> {
    let p = walk::load(fs, DEFAULT_MAX_FILE_SIZE).unwrap_or_else(|e| panic!("{}", e.text()));
    p.sources
        .into_iter()
        .chain(p.tests)
        .map(|f| f.path)
        .collect()
}

fn with_files(t: Template, names: &[&str], body: &str) -> MemFs {
    names
        .iter()
        .fold(scaffold(t), |fs, name| fs.file(name, body))
}

#[test]
fn advanced_ships_a_pgmiignore_for_clients_and_build_output() {
    let text = read(&scaffold(Template::Advanced), ".pgmiignore");
    for want in ["gateways/", "target/", "bin/", "obj/", "dist/"] {
        assert!(
            text.contains(&format!("\n{want}\n")),
            "advanced .pgmiignore must exclude {want}"
        );
    }
}

#[test]
fn templates_keep_parameter_files_out_of_session_and_git() {
    let secrets = ["secrets.env", "prod.params", "ci.params.local"];
    for t in Template::ALL {
        let fs = scaffold(t);
        let ignore_files: &[&str] = if t == Template::Minimal {
            &[".pgmiignore"]
        } else {
            &[".gitignore", ".pgmiignore"]
        };
        for name in ignore_files {
            let text = read(&fs, name);
            for want in ["*.params", "*.params.*", "*.env"] {
                assert!(
                    text.contains(&format!("\n{want}\n")),
                    "{t} {name} must exclude {want}"
                );
            }
        }
        let fs = with_files(t, &secrets, "database_admin_password=x\n");
        for path in loaded(&fs) {
            assert!(
                !secrets.iter().any(|s| path.ends_with(s)),
                "{t}: {path} is loaded into the session"
            );
        }
    }
}

#[test]
fn templates_keep_backup_copies_out_of_the_session() {
    let backups = [
        "project (copy).sql",
        "project.sql~",
        "project (1).sql",
        "project - Copy.sql",
        "project - Copy (2).sql",
        "project copy.sql",
        "project copy 2.sql",
    ];
    for t in Template::ALL {
        let shipped = loaded(&scaffold(t)).len();
        let got = loaded(&with_files(t, &backups, "SELECT 1;\n"));
        for path in &got {
            assert!(
                !backups.iter().any(|b| path.ends_with(b)),
                "{t}: the backup {path} is loaded into the session"
            );
        }
        assert_eq!(
            got.len(),
            shipped,
            "{t}: files loaded beside the backups, want the ones the template ships"
        );
    }
}

#[test]
fn minimal_template_writes_only_its_three_files() {
    let mut got: Vec<String> = Template::Minimal
        .render("proj", crate::VERSION)
        .unwrap()
        .into_iter()
        .map(|(p, _)| p)
        .collect();
    got.sort();
    assert_eq!(
        got,
        [".pgmiignore", "deploy.sql", "hello_world.sql"],
        "minimal scaffold writes exactly these (no skills, no pgmi.yaml)"
    );
}

fn do_block(sql: &str) -> &str {
    match (sql.find("DO $$"), sql.find("END $$;")) {
        (Some(s), Some(e)) if e >= s => &sql[s..e + "END $$;".len()],
        _ => "",
    }
}

#[test]
fn minimal_deploy_sql_matches_the_readme_loop() {
    let readme = repo_file("README.md");
    let example = readme
        .split_once("```sql\n-- deploy.sql\n")
        .map_or("", |(_, rest)| rest);
    let example = example.split_once("```").map_or(example, |(e, _)| e);
    let deploy = embedded("templates/minimal/deploy.sql");
    let (readme_loop, deploy_loop) = (do_block(example), do_block(&deploy));
    assert!(
        !readme_loop.is_empty() && readme_loop == deploy_loop,
        "README loop and minimal deploy.sql differ:\nREADME:\n{readme_loop}\ndeploy.sql:\n{deploy_loop}"
    );
}

#[test]
fn scaffolded_pgmi_yaml_sets_no_env() {
    for t in [Template::Basic, Template::Advanced] {
        let cfg = ProjectConfig::load(&scaffold(t))
            .unwrap_or_else(|e| panic!("{t}: {}", e.text()))
            .unwrap_or_else(|| panic!("{t}: no pgmi.yaml"));
        assert!(
            !cfg.params.contains_key("env"),
            "{t} pgmi.yaml sets env={:?}",
            cfg.params.get("env")
        );
    }
}

const GATEWAY_DEMO_HEADER: &str =
    "Demo code from pgmi. Do not copy this file into the project as is.";

fn is_gateway_source(name: &str) -> bool {
    [
        ".py", ".ts", ".go", ".cs", ".rs", ".toml", ".csproj", ".mod",
    ]
    .iter()
    .any(|e| name.ends_with(e))
        || name.rsplit('/').next() == Some("requirements.txt")
}

#[test]
fn advanced_scaffold_ships_the_gateway_examples() {
    let fs = scaffold(Template::Advanced);
    let agents = read(&fs, "AGENTS.md");
    for want in [
        "`gateways/README.md`",
        "read `gateways/` first",
        "`<path>/gateways/`",
    ] {
        assert!(agents.contains(want), "AGENTS.md does not contain {want}");
    }
    let readme = read(&fs, "gateways/README.md");
    assert!(fs.stat("gateways/PROTOCOL.md").is_ok());
    for (lang, manifest) in [("go", "go.mod"), ("rust", "Cargo.toml")] {
        assert!(
            fs.stat(&format!("gateways/{lang}/{manifest}")).is_ok(),
            "gateways/{lang} must be its own package"
        );
        assert!(
            fs.stat(&format!("gateways/{lang}/{manifest}.scaffold"))
                .is_err(),
            "{manifest}.scaffold must be written as {manifest}"
        );
    }
    let all = vfs::files(&fs).unwrap();
    for lang in ["python", "typescript", "go", "csharp", "rust"] {
        assert!(
            readme.contains(&format!("[{lang}/]({lang}/)")),
            "gateways/README.md does not list {lang}/"
        );
        let prefix = format!("gateways/{lang}/");
        let (mut sources, mut adapt, mut named) = (0, 0, false);
        for (path, body) in &all {
            if !path.starts_with(&prefix) || !is_gateway_source(path) {
                continue;
            }
            let text = String::from_utf8_lossy(body);
            sources += 1;
            assert!(
                text.contains(GATEWAY_DEMO_HEADER),
                "{path} lacks the demo-code header"
            );
            adapt += text.matches("ADAPT").count();
            named |= text.contains(&format!("\"pgmi-gateway-{lang}\""));
        }
        assert!(sources > 0, "gateways/{lang} has no source files");
        assert!(adapt > 0, "gateways/{lang} marks no ADAPT place");
        assert!(
            named,
            "gateways/{lang} never sets application_name pgmi-gateway-{lang}"
        );
    }
}

#[test]
fn gateway_problem_codes_are_namespaced() {
    let quoted = Regex::new(r#"["'][a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+["']"#).unwrap();
    let namespaced = Regex::new(r#"^["'](?:pgmi|membership|core)\."#).unwrap();
    let role_id = Regex::new(r#"^["'](?:agent|org)\."#).unwrap();
    let mut checked = 0;
    let mut bad = Vec::new();
    let fs = EmbeddedFs::new("templates/advanced/gateways");
    for (path, body) in vfs::files(&fs).unwrap() {
        if ![".go", ".py", ".ts", ".cs", ".rs"]
            .iter()
            .any(|e| path.ends_with(e))
        {
            continue;
        }
        let text = String::from_utf8_lossy(&body);
        for (n, line) in text.split('\n').enumerate() {
            if !line.to_lowercase().contains("code") {
                continue;
            }
            for code in quoted.find_iter(line) {
                if role_id.is_match(code.as_str()) {
                    continue;
                }
                checked += 1;
                if !namespaced.is_match(code.as_str()) {
                    bad.push(format!(
                        "{path}:{}: problem code {} is outside pgmi.*, membership.* and core.*",
                        n + 1,
                        code.as_str()
                    ));
                }
            }
        }
    }
    assert!(
        checked > 0,
        "found no problem code in the gateways: the scan checks nothing"
    );
    assert!(bad.is_empty(), "{}", bad.join("\n"));
}

fn code_only(sql: &str) -> String {
    let mut out = sql.as_bytes().to_vec();
    for t in lexer::tokens(sql.as_bytes()) {
        if matches!(
            t.kind,
            TokenKind::LineComment | TokenKind::BlockComment | TokenKind::Str
        ) {
            for b in &mut out[t.start..t.end] {
                if *b != b'\n' {
                    *b = b' ';
                }
            }
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}

#[test]
fn advanced_deploy_sql_wires_the_entity_standards_sweep() {
    let sql = code_only(&embedded("templates/advanced/deploy.sql"));
    let sweep = sql
        .find("pg_temp.apply_entity_standards_all()")
        .expect("deploy.sql never calls pg_temp.apply_entity_standards_all(): a user table declaring object_id core.entity_id would not get created_at/deleted_at");
    let deploy = sql
        .find("pg_temp.deploy()")
        .expect("deploy.sql never calls pg_temp.deploy()");
    assert!(
        sweep > deploy,
        "the sweep runs before pg_temp.deploy(), so it cannot see tables the deployment just created"
    );
    let tests = sql
        .find("CALL pgmi_test(")
        .expect("deploy.sql never calls pgmi_test()");
    assert!(
        sweep < tests,
        "the sweep runs after the test gate, so tests see un-swept tables"
    );
}

#[test]
fn code_only_masks_comments_and_literals() {
    let masked =
        code_only("-- CALL pgmi_test()\nSELECT 'pg_temp.deploy()'; /* x */ CALL pgmi_test();");
    assert_eq!(masked.find("CALL pgmi_test("), Some(55));
    assert!(!masked.contains("pg_temp.deploy()"));
}

#[test]
fn shipped_templates_split_at_their_documented_commit() {
    for (template, before_false_terminator, tail) in [
        ("basic", "SAVEPOINT _tests", vec!["RAISE NOTICE"]),
        (
            "advanced",
            "Required deployment parameter",
            vec![
                "Deployment lock released",
                "NOT i.indisvalid",
                "RAISE NOTICE $ascii$",
            ],
        ),
    ] {
        let units = script::split(&embedded(&format!("templates/{template}/deploy.sql")));
        assert_eq!(
            units.len(),
            1 + tail.len(),
            "{template}: splits into {} execution unit(s), want the atomic head + {} tail statements",
            units.len(),
            tail.len()
        );
        let head = units[0].text.trim();
        assert!(
            head.ends_with("COMMIT;"),
            "{template}: the atomic head ends with {:?}",
            &head[head.len().saturating_sub(60)..]
        );
        assert!(
            head.contains(before_false_terminator),
            "{template}: {before_false_terminator:?} is missing from the atomic head: the split happened at a bare END; inside a dollar-quoted body"
        );
        assert!(
            head.contains("ROLLBACK TO SAVEPOINT"),
            "{template}: ROLLBACK TO SAVEPOINT was treated as a transaction terminator"
        );
        assert!(
            head.contains("pgmi_test"),
            "{template}: the test gate is not in the atomic head"
        );
        for (i, want) in tail.iter().enumerate() {
            let unit = &units[1 + i].text;
            assert!(
                unit.contains(want),
                "{template}: tail statement {} is not the expected one ({want:?}): {:?}",
                i + 1,
                unit.trim().chars().take(60).collect::<String>()
            );
            assert!(
                !unit.contains("COMMIT;"),
                "{template}: a second COMMIT is in the tail"
            );
        }
    }
}

const ADVANCED_TRIMMABLE: [&[&str]; 5] = [
    &["gateways/"],
    &["api/dev/"],
    &[
        "lib/core/change-tracking.sql",
        "lib/core/change-tracking-actor.sql",
        "lib/core/change-tracking-functions.sql",
        "lib/__test__/test_change_tracking.sql",
    ],
    &[
        "lib/core/entity-registry.sql",
        "lib/core/entity-registry-functions.sql",
        "lib/__test__/test_entity_registry.sql",
    ],
    &[
        "lib/core/organization-counter.sql",
        "lib/core/organization-counter-functions.sql",
        "lib/__test__/test_organization_counter.sql",
    ],
];

#[test]
fn advanced_readme_trimming_list_matches_the_test() {
    let readme = embedded("templates/advanced/README.md");
    let (start, end) = (
        readme.find("**Safe to delete**"),
        readme.find("**Keep, and change instead:**"),
    );
    let (Some(start), Some(end)) = (start, end) else {
        panic!(
            "the advanced README has no \"Safe to delete\" list before \"Keep, and change instead\""
        );
    };
    assert!(start < end);
    let paths = Regex::new("`([a-z_./-]+/?)` *(?:,|—)").unwrap();
    let got: Vec<Vec<String>> = readme[start..end]
        .split('\n')
        .filter(|l| l.starts_with("- "))
        .map(|l| paths.captures_iter(l).map(|c| c[1].to_string()).collect())
        .collect();
    let want: Vec<Vec<String>> = ADVANCED_TRIMMABLE
        .iter()
        .map(|item| item.iter().map(|s| (*s).to_string()).collect())
        .collect();
    assert_eq!(
        got, want,
        "README \"Safe to delete\" differs from the trimming test's list"
    );
    let fs = EmbeddedFs::new("templates/advanced");
    for path in ADVANCED_TRIMMABLE.iter().flat_map(|i| i.iter()) {
        assert!(
            fs.stat(path.trim_end_matches('/')).is_ok(),
            "listed path {path} is not in the template"
        );
    }
}

const EXAMPLE_TEMPLATE_COPIES: [&str; 10] = [
    "deploy.sql",
    "physical/001_project.sql",
    "physical/002_task.sql",
    "physical/__test__/test_constraints.sql",
    "semantic/project.sql",
    "semantic/task.sql",
    "semantic/__test__/_setup.sql",
    "semantic/__test__/test_project.sql",
    "semantic/__test__/test_task.sql",
    "__test__/test_function_privileges.sql",
];

#[test]
fn example_matches_basic_template() {
    for rel in EXAMPLE_TEMPLATE_COPIES {
        assert!(
            repo_file(&format!("examples/test-gated-deploy/{rel}"))
                == embedded(&format!("templates/basic/{rel}")).replace("\r\n", "\n"),
            "examples/test-gated-deploy/{rel} has drifted from the basic template"
        );
    }
}

#[test]
fn overview_names_the_scaffolded_skills() {
    let body = embedded("ai/overview.md");
    let section = body.split_once("## Project Skills").map_or("", |(_, s)| s);
    let section = section.split_once("\n## ").map_or(section, |(s, _)| s);
    let mut want: Vec<String> = Vec::new();
    for prefix in ["skills", "templates/advanced/.agents/skills"] {
        let fs = EmbeddedFs::new(prefix);
        for e in fs.read_dir("").unwrap() {
            if e.stat.kind == crate::vfs::FileKind::Dir {
                want.push(e.name.as_str().to_string());
            }
        }
    }
    let skill = Regex::new("`((?:pgmi|postgres)-[a-z-]+)`").unwrap();
    let named: Vec<String> = skill
        .captures_iter(section)
        .map(|c| c[1].to_string())
        .collect();
    for s in &want {
        assert!(
            named.contains(s),
            "overview.md Project Skills does not name {s}"
        );
    }
    for s in &named {
        assert!(
            want.contains(s),
            "overview.md Project Skills names {s}, which pgmi init does not write"
        );
    }
}

#[test]
fn readme_advanced_template_size_follows_the_code() {
    let readme = repo_file("README.md");
    let claim = Regex::new(r"about ([0-9,]+) lines of SQL and ([0-9,]+) lines of tests").unwrap();
    let Some(m) = claim.captures(&readme) else {
        return;
    };
    let (mut sql, mut tests) = (0usize, 0usize);
    for (path, body) in vfs::files(&EmbeddedFs::new("templates/advanced")).unwrap() {
        if !path.ends_with(".sql") || path.starts_with("gateways/") {
            continue;
        }
        let n = body.iter().filter(|b| **b == b'\n').count();
        if path.contains("__test__/") {
            tests += n;
        } else {
            sql += n;
        }
    }
    for (claim, actual) in [(&m[1], sql), (&m[2], tests)] {
        let n: f64 = claim.replace(',', "").parse().unwrap();
        let diff = (actual as f64 - n) / n;
        assert!(
            (-0.15..=0.15).contains(&diff),
            "README says about {claim} lines, the template has {actual}"
        );
    }
}

#[test]
fn docs_name_every_advanced_extension() {
    let deploy = embedded("templates/advanced/deploy.sql");
    let row = Regex::new(r"FROM \(VALUES (.*?)\) AS r\(name\)").unwrap();
    let row = row
        .captures(&deploy)
        .expect("no extension VALUES row found in advanced/deploy.sql");
    let extensions: Vec<String> = Regex::new("'([^']+)'")
        .unwrap()
        .captures_iter(&row[1])
        .map(|c| c[1].to_string())
        .collect();
    assert!(extensions.len() >= 5, "parsed only {extensions:?}");
    for (file, marker) in [
        ("docs/PRODUCTION.md", "| advanced template |"),
        ("docs/PRODUCTION.md", "**`CREATE EXTENSION`** for"),
        ("docs/templates.md", "- Extensions:"),
        ("shared/templates/advanced/README.md", "| `extensions` |"),
    ] {
        let body = repo_file(file);
        let line = body
            .split('\n')
            .find(|l| l.contains(marker))
            .unwrap_or_else(|| panic!("{file}: no line with {marker:?}"));
        for ext in &extensions {
            assert!(
                line.contains(&format!("`{ext}`")),
                "{file} ({marker}) does not name the extension {ext}, which advanced/deploy.sql creates"
            );
        }
    }
}
