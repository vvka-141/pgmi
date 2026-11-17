use regex_lite::Regex;

use super::{embedded, line_of, repo_file, repo_tree, tree};
use crate::vfs::{EmbeddedFs, FileKind, Vfs};

fn docs_markdown() -> Vec<(String, String)> {
    repo_tree("docs")
        .into_iter()
        .filter(|(p, _)| p.ends_with(".md"))
        .collect()
}

#[test]
fn highlights_count_matches_documentation() {
    let body = repo_file("docs/HIGHLIGHTS.md");
    let numbers: Vec<String> = Regex::new(r"(?m)^## (\d+)\.")
        .unwrap()
        .captures_iter(&body)
        .map(|c| c[1].to_string())
        .collect();
    assert!(
        !numbers.is_empty(),
        "docs/HIGHLIGHTS.md has no numbered capability headings"
    );
    for (i, n) in numbers.iter().enumerate() {
        assert_eq!(
            *n,
            (i + 1).to_string(),
            "highlight {} is numbered {n}",
            i + 1
        );
    }
    let words = [
        "zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
        "eleven", "twelve",
    ];
    let word = words
        .get(numbers.len())
        .unwrap_or_else(|| panic!("add a count word for {} highlights", numbers.len()));
    let stated = Regex::new(&format!(r"(?i)\b{word}\b")).unwrap();
    let pages = [
        ("README.md", repo_file("README.md")),
        ("docs/README.md", repo_file("docs/README.md")),
        ("docs/HIGHLIGHTS.md", body.clone()),
        ("docs/STYLE.md", repo_file("docs/STYLE.md")),
        (
            "crates/pgmi-shared/ai/overview.md",
            embedded("ai/overview.md"),
        ),
    ];
    let silent: Vec<&str> = pages
        .iter()
        .filter(|(_, text)| !stated.is_match(text))
        .map(|(p, _)| *p)
        .collect();
    assert!(
        silent.is_empty(),
        "{silent:?} do not state the current count of {} highlights ({word:?})",
        numbers.len()
    );
}

#[test]
fn rollback_claims_stay_within_postgresql_guarantees() {
    let overbroad = [
        "database is unchanged",
        "database unchanged",
        "database is untouched",
        "byte-identical",
        "zero changes to your database",
    ];
    let pages = [
        ("README.md", repo_file("README.md")),
        ("docs/QUICKSTART.md", repo_file("docs/QUICKSTART.md")),
        ("docs/HIGHLIGHTS.md", repo_file("docs/HIGHLIGHTS.md")),
        ("docs/COMING-FROM.md", repo_file("docs/COMING-FROM.md")),
        ("docs/TESTING.md", repo_file("docs/TESTING.md")),
        ("docs/session-api.md", repo_file("docs/session-api.md")),
        (
            "examples/test-gated-deploy/README.md",
            repo_file("examples/test-gated-deploy/README.md"),
        ),
        (
            "crates/pgmi-shared/ai/overview.md",
            embedded("ai/overview.md"),
        ),
        (
            "crates/pgmi-shared/skills/pgmi-sql/SKILL.md",
            embedded("skills/pgmi-sql/SKILL.md"),
        ),
        (
            "crates/pgmi-shared/templates/advanced/README.md",
            embedded("templates/advanced/README.md"),
        ),
        (
            "crates/pgmi-shared/templates/advanced/__test__/README.md",
            embedded("templates/advanced/__test__/README.md"),
        ),
    ];
    let mut claims = Vec::new();
    for (path, text) in &pages {
        let text = text.to_lowercase();
        for phrase in overbroad {
            if text.contains(phrase) {
                claims.push(format!("{path}: {phrase:?}"));
            }
        }
    }
    assert!(
        claims.is_empty(),
        "overbroad rollback claims (sequences and effects outside the transaction survive a rollback):\n  {}",
        claims.join("\n  ")
    );
}

#[test]
fn docs_have_no_stale_template_claims() {
    let stale = [
        "19k lines",
        "~19k",
        "several hundred lines",
        "migration scaffold",
        "inbound queue",
    ];
    let pages = docs_markdown()
        .into_iter()
        .filter(|(p, _)| !p.starts_with("docs/design/"))
        .chain([("README.md".to_string(), repo_file("README.md"))]);
    let mut found = Vec::new();
    for (path, text) in pages {
        for s in stale {
            if text.contains(s) {
                found.push(format!("{path} still says {s:?}"));
            }
        }
    }
    assert!(found.is_empty(), "{}", found.join("\n"));
}

#[test]
fn documented_deploy_summaries_match_the_real_format() {
    let summary_line = Regex::new(r"^[✓✗] ").unwrap();
    let summary_format = Regex::new(r"^[✓✗] [^\s:]+: \S").unwrap();
    let pages = docs_markdown()
        .into_iter()
        .chain([("README.md".to_string(), repo_file("README.md"))])
        .chain(tree("ai").into_iter().filter(|(p, _)| p.ends_with(".md")));
    let mut offenders = Vec::new();
    for (path, text) in pages {
        for (i, line) in text.lines().enumerate() {
            if summary_line.is_match(line) && !summary_format.is_match(line) {
                offenders.push(format!("{path}:{}  {line}", i + 1));
            }
        }
    }
    assert!(
        offenders.is_empty(),
        "{} documented deploy summary line(s) do not match \"<icon> <database>: <detail>\":\n  {}",
        offenders.len(),
        offenders.join("\n  ")
    );
}

#[test]
fn shipped_content_cites_no_issue_keys() {
    let ticket = Regex::new(r"PGMI-[0-9]+").unwrap();
    let shipped = ["templates", "skills", "ai"]
        .into_iter()
        .flat_map(|p| {
            tree(p)
                .into_iter()
                .map(|(path, text)| (format!("crates/pgmi-shared/{path}"), text))
        })
        .chain(repo_tree("examples"));
    let mut offenders = Vec::new();
    for (path, text) in shipped {
        for m in ticket.find_iter(&text) {
            offenders.push(format!(
                "{path}:{}  {}",
                line_of(&text, m.start()),
                m.as_str()
            ));
        }
    }
    assert!(
        offenders.is_empty(),
        "{} issue-key reference(s) in content that ships to users; say what the ticket says instead:\n  {}",
        offenders.len(),
        offenders.join("\n  ")
    );
}

#[test]
fn docs_example_folders_exist_or_are_declared() {
    let filter = Regex::new(r"(?:directory\s*=|LIKE)\s*'\./([A-Za-z0-9_.-]+)/").unwrap();
    let mut template_folders = Vec::new();
    for t in ["basic", "advanced"] {
        let fs = EmbeddedFs::new(&format!("templates/{t}"));
        for e in fs.read_dir("").unwrap() {
            if e.stat.kind == FileKind::Dir {
                template_folders.push(e.name.as_str().to_string());
            }
        }
    }
    let mut undeclared = Vec::new();
    for (path, text) in docs_markdown() {
        let page = path.trim_start_matches("docs/");
        if page.contains('/') || ["COMING-FROM.md", "METADATA.md"].contains(&page) {
            continue;
        }
        let note = text
            .find("**Example layout.**")
            .map_or("", |i| text[i..].split('\n').next().unwrap_or(""));
        for c in filter.captures_iter(&text) {
            let folder = &c[1];
            if !template_folders.iter().any(|f| f == folder)
                && !note.contains(&format!("`{folder}/`"))
            {
                undeclared.push(format!("{path} filters on ./{folder}/"));
            }
        }
    }
    assert!(
        undeclared.is_empty(),
        "no template has these folders; add each to the page's **Example layout.** note or use a template folder:\n  {}",
        undeclared.join("\n  ")
    );
}
