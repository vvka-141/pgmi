mod assertions;
mod cli_docs;
mod contract;
mod docs;
mod gateways;
mod leak;
mod links;
mod release;
mod session_api;
mod skills;
mod snippets;
mod sql_guards;
mod templates;

use std::path::{Path, PathBuf};

use regex_lite::Regex;

use crate::templates::Template;
use crate::vfs::{self, EmbeddedFs, MemFs, Vfs};

fn tree(prefix: &str) -> Vec<(String, String)> {
    let files = vfs::files(&EmbeddedFs::new(prefix)).unwrap();
    assert!(!files.is_empty(), "nothing embedded under {prefix}");
    files
        .into_iter()
        .map(|(p, b)| {
            (
                format!("{prefix}/{p}"),
                String::from_utf8_lossy(&b).into_owned(),
            )
        })
        .collect()
}

fn embedded(path: &str) -> String {
    String::from_utf8(
        pgmi_shared::file(path)
            .unwrap_or_else(|| panic!("{path} is not embedded"))
            .to_vec(),
    )
    .unwrap()
}

fn scaffold(t: Template) -> MemFs {
    MemFs::from_files(t.render("proj", crate::VERSION).unwrap())
}

fn read(fs: &impl Vfs, rel: &str) -> String {
    fs.read_to_string(rel)
        .unwrap_or_else(|e| panic!("{rel}: {e}"))
}

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn repo_file(rel: &str) -> String {
    let path = repo_root().join(rel);
    std::fs::read_to_string(&path)
        .unwrap_or_else(|e| panic!("{}: {e}", path.display()))
        .replace("\r\n", "\n")
}

fn repo_tree(rel: &str) -> Vec<(String, String)> {
    let root = repo_root();
    let mut out = Vec::new();
    let mut stack = vec![root.join(rel)];
    while let Some(p) = stack.pop() {
        if p.is_dir() {
            let entries = std::fs::read_dir(&p).unwrap_or_else(|e| panic!("{}: {e}", p.display()));
            stack.extend(entries.map(|e| e.unwrap().path()));
        } else if p.is_file() {
            let body = std::fs::read(&p).unwrap_or_else(|e| panic!("{}: {e}", p.display()));
            let rel = p
                .strip_prefix(&root)
                .unwrap()
                .to_string_lossy()
                .replace('\\', "/");
            out.push((rel, String::from_utf8_lossy(&body).replace("\r\n", "\n")));
        }
    }
    out.sort();
    out
}

fn line_of(text: &str, at: usize) -> usize {
    text[..at].matches('\n').count() + 1
}

fn unguarded_plan_view_loops(text: &str) -> Vec<usize> {
    let plan_view_query =
        Regex::new(r"(?s)SELECT[^;]{0,400}?pgmi_plan_view[^;]{0,400}?(?:LOOP|\)\s*$)").unwrap();
    let exact_path = Regex::new(r"path\s*=\s*'").unwrap();
    let select_star = Regex::new(r"(?i)SELECT\s+(?:\w+\.)?\*").unwrap();
    plan_view_query
        .find_iter(text)
        .filter(|m| {
            let q = m.as_str();
            (q.contains("content") || select_star.is_match(q))
                && !exact_path.is_match(q)
                && !q.contains("is_sql_file")
        })
        .map(|m| m.start())
        .collect()
}
