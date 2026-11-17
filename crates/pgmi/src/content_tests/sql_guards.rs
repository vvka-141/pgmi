use regex_lite::Regex;

use super::{line_of, repo_file, repo_root, repo_tree, tree, unguarded_plan_view_loops};

const PLAN_VIEW_GUARD_EXEMPT: [&str; 1] = ["examples/execution-order-policy/project/deploy.sql"];

fn unguarded(files: impl IntoIterator<Item = (String, String)>) -> Vec<String> {
    files
        .into_iter()
        .flat_map(|(path, text)| {
            unguarded_plan_view_loops(&text)
                .into_iter()
                .map(move |at| format!("{path}:{}", line_of(&text, at)))
                .collect::<Vec<_>>()
        })
        .collect()
}

#[test]
fn docs_plan_view_loops_filter_sql_files() {
    let local_guidance = ["AGENTS.md", "CLAUDE.md"]
        .into_iter()
        .filter(|f| repo_root().join(f).is_file())
        .map(|f| (f.to_string(), repo_file(f)));
    let offenders = unguarded(
        repo_tree("docs")
            .into_iter()
            .filter(|(p, _)| p.ends_with(".md"))
            .chain(local_guidance),
    );
    assert!(
        offenders.is_empty(),
        "unguarded pgmi_plan_view execute loop(s); join pg_temp.pgmi_source_view and filter on is_sql_file:\n  {}",
        offenders.join("\n  ")
    );
}

#[test]
fn example_plan_view_loops_filter_sql_files() {
    let offenders = unguarded(
        repo_tree("examples")
            .into_iter()
            .filter(|(p, _)| p.ends_with(".sql") && !PLAN_VIEW_GUARD_EXEMPT.contains(&p.as_str())),
    );
    assert!(
        offenders.is_empty(),
        "{} example pgmi_plan_view execute loop(s) omit the is_sql_file guard, so a stale editor backup beside a migration runs as one:\n  {}",
        offenders.len(),
        offenders.join("\n  ")
    );
}

#[test]
fn plan_view_guard_exemptions_still_exist() {
    for path in PLAN_VIEW_GUARD_EXEMPT {
        assert!(
            repo_root().join(path).is_file(),
            "{path} is exempt but gone"
        );
    }
}

#[test]
fn docs_do_not_teach_sqlstate_flattening_reraise() {
    let reraise = Regex::new(
        r"(?is)WHEN\s+OTHERS\s+THEN\s+(?:--[^\n]*\n\s*)*RAISE\s+EXCEPTION\s+'[^']*'[^;]*;",
    )
    .unwrap();
    let files = ["docs", "examples", "README.md"]
        .into_iter()
        .flat_map(repo_tree)
        .chain(["ai", "templates"].into_iter().flat_map(|p| {
            tree(p)
                .into_iter()
                .map(|(path, text)| (format!("crates/pgmi-shared/{path}"), text))
        }))
        .filter(|(p, _)| p.ends_with(".md") || p.ends_with(".sql"));
    let mut offenders = Vec::new();
    for (path, text) in files {
        for m in reraise.find_iter(&text) {
            if !m.as_str().to_uppercase().contains("ERRCODE") {
                offenders.push(format!("{path}:{}", line_of(&text, m.start())));
            }
        }
    }
    assert!(
        offenders.is_empty(),
        "RAISE EXCEPTION in WHEN OTHERS without USING ERRCODE turns every failure into P0001; let the error propagate or use a bare RAISE:\n  {}",
        offenders.join("\n  ")
    );
}
