use std::collections::BTreeSet;

use clap::CommandFactory;
use regex_lite::Regex;
use serde_json::Value;

use super::{embedded, repo_file, repo_tree, scaffold};
use crate::cli::Cli;
use crate::commands::ai::CONTRACT;
use crate::error::Kind;
use crate::templates::Template;
use crate::vfs::Vfs;

const KINDS: [Kind; 11] = [
    Kind::General,
    Kind::Usage,
    Kind::Internal,
    Kind::Config,
    Kind::Connection,
    Kind::Denied,
    Kind::Sql,
    Kind::NoDeploySql,
    Kind::Locked,
    Kind::Timeout,
    Kind::Interrupted,
];

fn exit_codes() -> BTreeSet<u64> {
    std::iter::once(0)
        .chain(KINDS.iter().map(|k| u64::from(k.exit_code())))
        .collect()
}

fn table_codes(doc: &str, heading: &str) -> BTreeSet<u64> {
    let Some((_, section)) = doc.split_once(heading) else {
        return BTreeSet::new();
    };
    let section = section.split_once("\n## ").map_or(section, |(s, _)| s);
    Regex::new(r"(?m)^\|\s*[`*]*(\d+)[`*]*\s*\|")
        .unwrap()
        .captures_iter(section)
        .map(|c| c[1].parse().unwrap())
        .collect()
}

#[test]
fn the_contract_publishes_exactly_the_exit_codes_pgmi_returns() {
    let contract: Value = serde_json::from_str(CONTRACT).unwrap();
    let published: BTreeSet<u64> = contract["exit_codes"]
        .as_array()
        .expect("contract has no exit_codes")
        .iter()
        .map(|e| e["code"].as_u64().unwrap())
        .collect();
    assert_eq!(
        published,
        exit_codes(),
        "pgmi ai contract against error::Kind"
    );
}

#[test]
fn the_cli_reference_lists_exactly_the_exit_codes_pgmi_returns() {
    assert_eq!(
        table_codes(&repo_file("docs/CLI.md"), "## Exit Codes"),
        exit_codes(),
        "docs/CLI.md Exit Codes table against error::Kind"
    );
}

#[test]
fn the_debug_skill_diagnoses_only_exit_codes_pgmi_returns() {
    let diagnosed = table_codes(
        &embedded("skills/pgmi-debug-deploy/SKILL.md"),
        "## Exit codes",
    );
    assert!(
        !diagnosed.is_empty(),
        "no exit-code rows in pgmi-debug-deploy"
    );
    let invented: Vec<u64> = diagnosed.difference(&exit_codes()).copied().collect();
    assert!(
        invented.is_empty(),
        "pgmi-debug-deploy diagnoses exit codes pgmi never returns: {invented:?}"
    );
}

#[test]
fn the_cli_reference_documents_every_top_level_command() {
    let reference = repo_file("docs/CLI.md");
    let missing: Vec<String> = Cli::command()
        .get_subcommands()
        .map(clap::Command::get_name)
        .filter(|n| *n != "help")
        .map(|n| match n {
            "completion" => "## Shell Completion".to_string(),
            n => format!("## pgmi {n}"),
        })
        .filter(|h| !reference.contains(h.as_str()))
        .collect();
    assert!(missing.is_empty(), "docs/CLI.md is missing {missing:?}");
}

fn unknown_commands(markdown: &str) -> Vec<String> {
    let fenced = Regex::new("(?s)```[^\n]*\n(.*?)```").unwrap();
    let inline = Regex::new("`([^`\n]+)`").unwrap();
    let invocation = Regex::new(r"(?:^|[^\w./-])pgmi((?:[ \t]+[a-z][a-z-]*)+)").unwrap();
    let rest = fenced.replace_all(markdown, "");
    let code: Vec<String> = fenced
        .captures_iter(markdown)
        .chain(inline.captures_iter(&rest))
        .map(|c| c[1].to_string())
        .collect();
    let root = Cli::command();
    let mut unknown = Vec::new();
    for block in &code {
        for m in invocation.captures_iter(block) {
            let mut cmd = &root;
            for word in m[1].split_whitespace() {
                let next = cmd
                    .get_subcommands()
                    .find(|c| c.get_name() == word || c.get_all_aliases().any(|a| a == word));
                let Some(next) = next else {
                    if cmd.has_subcommands() {
                        unknown.push(format!("pgmi{}", &m[1]));
                    }
                    break;
                };
                cmd = next;
            }
        }
    }
    unknown
}

#[test]
fn unknown_commands_finds_removed_commands_in_code_only() {
    for (markdown, hits) in [
        ("`pgmi ai skill pgmi-handler-patterns` prints it", 1),
        ("```bash\npgmi ai setup\n```", 1),
        ("`pgmi init --template advanced`", 0),
        ("```bash\npgmi deploy . --overwrite --force\n```", 0),
        ("`pgmi ai contract`", 0),
        ("pgmi loads your files and hands control to deploy.sql", 0),
    ] {
        let got = unknown_commands(markdown);
        assert_eq!(got.len(), hits, "{markdown:?}: {got:?}");
    }
}

#[test]
fn articles_name_only_existing_commands() {
    let articles: Vec<(String, String)> = repo_tree("website/content/articles")
        .into_iter()
        .filter(|(p, _)| p.ends_with(".md"))
        .collect();
    assert!(!articles.is_empty(), "no articles found");
    let unknown: Vec<String> = articles
        .iter()
        .flat_map(|(path, text)| {
            unknown_commands(text)
                .into_iter()
                .map(move |c| format!("{path}: {c}"))
        })
        .collect();
    assert!(
        unknown.is_empty(),
        "articles name commands pgmi does not have:\n  {}",
        unknown.join("\n  ")
    );
}

#[test]
fn quickstart_tree_matches_init() {
    let doc = repo_file("docs/QUICKSTART.md");
    let block = doc
        .split_once("## Step 3: Create a project")
        .and_then(|(_, s)| s.split_once("```\nmyapp/\n"))
        .and_then(|(_, s)| s.split_once("```"))
        .map(|(b, _)| b)
        .expect("no myapp/ tree in QUICKSTART Step 3");
    let fs = scaffold(Template::Basic);
    let mut stack: Vec<String> = Vec::new();
    let mut named = BTreeSet::new();
    let mut problems = Vec::new();
    for line in block.lines() {
        let Some(i) = line.find("── ") else {
            continue;
        };
        let depth = line[..i].chars().count() / 4;
        let name = line[i + "── ".len()..]
            .split_whitespace()
            .next()
            .unwrap_or("")
            .trim_end_matches('/')
            .to_string();
        stack.truncate(depth);
        stack.push(name.clone());
        if depth == 0 {
            named.insert(name);
        }
        let path = stack.join("/");
        if fs.stat(&path).is_err() {
            problems.push(format!(
                "QUICKSTART names {path}, which pgmi init does not write"
            ));
        }
    }
    for e in fs.read_dir("").unwrap() {
        if !named.contains(e.name.as_str()) {
            problems.push(format!(
                "pgmi init writes {}, which the QUICKSTART tree leaves out",
                e.name.as_str()
            ));
        }
    }
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}
