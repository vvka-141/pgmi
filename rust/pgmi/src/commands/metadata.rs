use std::collections::{BTreeMap, BTreeSet};
use std::fmt::Write as _;
use std::path::{Path, PathBuf};

use serde::Serialize;

use super::{json, stderr, stdout};
use crate::cli::{MetadataArgs, MetadataCommand, MetadataReport, ScaffoldArgs};
use crate::config::edit::write_atomic;
use crate::env::Env;
use crate::error::{Error, Result};
use crate::output::Reporter;
use crate::project::metadata::{self, Meta, MetaError};
use crate::project::walk::{self, File, is_sql};

pub(crate) fn run(
    a: &MetadataArgs,
    verbose: bool,
    env: &Env,
    help: impl FnOnce() -> Result<()>,
) -> Result<()> {
    let reporter = Reporter::new(verbose, env);
    match &a.command {
        None => help(),
        Some(MetadataCommand::Plan(r)) => {
            reporter.verbose(&format!("Project path: {}", r.path.display()));
            reporter.verbose(&format!("JSON output: {}", r.json));
            plan(r, env)
        }
        Some(MetadataCommand::Validate(r)) => {
            reporter.verbose(&format!("Project path: {}", r.path.display()));
            reporter.verbose(&format!("JSON output: {}", r.json));
            validate(r, env)
        }
        Some(MetadataCommand::Scaffold(s)) => {
            reporter.verbose(&format!("Project path: {}", s.path.display()));
            reporter.verbose(&format!("Preview only: {}", !s.write));
            reporter.verbose(&format!("Default idempotent: {}", s.idempotent));
            scaffold(s, env)
        }
    }
}

fn failed(json_out: bool, e: Error, extra: serde_json::Value) -> Error {
    if json_out {
        let mut value = serde_json::json!({"error": e.text(), "exitCode": e.exit_code()});
        if let (Some(obj), serde_json::Value::Object(more)) = (value.as_object_mut(), extra) {
            obj.extend(more);
        }
        let _ = json(&value);
    }
    e
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct Row {
    execution_order: usize,
    path: String,
    sort_key: String,
    id: String,
    idempotent: bool,
    description: String,
    is_sql_file: bool,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct Plan {
    total_files: usize,
    plan: Vec<Row>,
}

fn rows(sources: &[File], metas: &[Meta]) -> Vec<Row> {
    let by_path: BTreeMap<&str, &Meta> = metas.iter().map(|m| (m.path.as_str(), m)).collect();
    let mut rows = Vec::new();
    for f in sources {
        let meta = by_path.get(f.path.as_str());
        let keys = match meta {
            Some(m) if !m.sort_keys.is_empty() => m.sort_keys.clone(),
            _ => vec![f.path.clone()],
        };
        for key in keys {
            rows.push(Row {
                execution_order: 0,
                path: f.path.clone(),
                sort_key: key,
                id: meta.map_or_else(|| metadata::fallback_id(&f.path), |m| m.id.clone()),
                idempotent: meta.is_none_or(|m| m.idempotent),
                description: meta.and_then(|m| m.description.clone()).unwrap_or_default(),
                is_sql_file: is_sql(&f.path),
            });
        }
    }
    rows.sort_by(|a, b| {
        (a.sort_key.as_bytes(), a.path.as_bytes()).cmp(&(b.sort_key.as_bytes(), b.path.as_bytes()))
    });
    for (i, r) in rows.iter_mut().enumerate() {
        r.execution_order = i + 1;
    }
    rows
}

pub(crate) fn plan_report(path: &Path, env: &Env) -> Result<Plan> {
    let max_size = walk::max_size(env)?;
    let scan_failed = walk::scan_failed(None);
    let project = crate::vfs::OsFs::project(path)
        .and_then(|fs| walk::scan(&fs, max_size))
        .map_err(&scan_failed)?;
    let scan = metadata::scan(&project.sources);
    scan.readable().map_err(scan_failed)?;
    let metas = scan.unique()?;
    Ok(Plan {
        total_files: project.sources.len(),
        plan: rows(&project.sources, &metas),
    })
}

fn plan(r: &MetadataReport, env: &Env) -> Result<()> {
    let plan = plan_report(&r.path, env).map_err(|e| failed(r.json, e, serde_json::json!({})))?;
    if r.json {
        return json(&plan);
    }
    let header = ["ORDER", "SORT KEY", "PATH"];
    let cells: Vec<[String; 3]> = plan
        .plan
        .iter()
        .map(|r| {
            [
                r.execution_order.to_string(),
                r.sort_key.clone(),
                r.path.clone(),
            ]
        })
        .collect();
    let width = |i: usize| {
        cells
            .iter()
            .map(|c| c[i].chars().count())
            .chain([header[i].len()])
            .max()
            .unwrap_or(0)
            + 2
    };
    let (w0, w1, w2) = (width(0), width(1), width(2));
    let mut text = format!(
        "{:<w0$}{:<w1$}{:<w2$}IDEMPOTENT\n",
        header[0], header[1], header[2]
    );
    for (c, row) in cells.iter().zip(&plan.plan) {
        let note = if row.is_sql_file { "" } else { "  (not SQL)" };
        let _ = writeln!(
            text,
            "{:<w0$}{:<w1$}{:<w2$}{}{note}",
            c[0], c[1], c[2], row.idempotent
        );
    }
    stdout(&text)
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct Validation {
    pub(crate) total_files: usize,
    pub(crate) files_with_metadata: usize,
    pub(crate) files_without_metadata: usize,
    pub(crate) validation_passed: bool,
    pub(crate) duplicate_ids: Vec<String>,
    pub(crate) warnings: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) exit_code: Option<u8>,
}

/// What `metadata validate` found: a report, or the `<pgmi-meta>` blocks
/// it could not read.
pub(crate) enum Validated {
    Report(Validation),
    Unreadable(Vec<MetaError>),
}

/// The refusal for blocks `metadata validate` could not read, one per line.
pub(crate) fn unreadable(errors: &[MetaError]) -> Vec<String> {
    errors.iter().map(ToString::to_string).collect()
}

pub(crate) fn validate_report(path: &Path, env: &Env) -> Result<Validated> {
    let max_size = walk::max_size(env)?;
    let project = walk::scan(&crate::vfs::OsFs::project(path)?, max_size)
        .map_err(|e| Error::new(e.kind(), "validation failed").with_source(e))?;
    let total = project.sources.len() + project.test_dir_files.len();
    let scan = metadata::scan(project.sources.iter());
    if !scan.errors.is_empty() {
        return Ok(Validated::Unreadable(scan.errors));
    }
    let by_path: BTreeSet<&str> = scan.metas.iter().map(|m| m.path.as_str()).collect();
    let warnings: Vec<String> = project
        .sources
        .iter()
        .filter(|f| is_sql(&f.path) && !by_path.contains(f.path.as_str()))
        .filter(|f| metadata::outside_comment(&f.content, None))
        .map(|f| format!("{}: <pgmi-meta> outside a /* */ block comment is not read; a -- line comment or a string is ignored, so this file has no metadata", f.path))
        .collect();
    let duplicates: Vec<String> = scan
        .duplicates
        .iter()
        .map(|(id, p)| metadata::duplicate_line(id, p))
        .collect();
    let with = scan.metas.len();
    Ok(Validated::Report(Validation {
        total_files: total,
        files_with_metadata: with,
        files_without_metadata: total - with,
        validation_passed: duplicates.is_empty(),
        duplicate_ids: duplicates,
        warnings,
        error: None,
        exit_code: None,
    }))
}

fn validate(r: &MetadataReport, env: &Env) -> Result<()> {
    let report = validate_report(&r.path, env)
        .map_err(|e| failed(r.json, e, serde_json::json!({"validationPassed": false})))?;
    let mut v = match report {
        Validated::Report(v) => v,
        Validated::Unreadable(errors) => {
            if let Some(invalid) = errors
                .iter()
                .find(|e| matches!(e, MetaError::Invalid { .. }))
            {
                return Err(failed(
                    r.json,
                    Error::config(format!("validation failed: {invalid}")),
                    serde_json::json!({"validationPassed": false}),
                ));
            }
            let errors = unreadable(&errors);
            let e = Error::config(format!("validation failed: {}", errors.join("\n")));
            return Err(failed(
                r.json,
                e,
                serde_json::json!({"errors": errors, "validationPassed": false}),
            ));
        }
    };
    let failure = (!v.validation_passed).then(|| {
        Error::config(format!(
            "metadata validation failed: {} duplicate id(s)",
            v.duplicate_ids.len()
        ))
    });
    if r.json {
        if let Some(e) = &failure {
            v.error = Some(e.text());
            v.exit_code = Some(e.exit_code());
        }
        json(&v)?;
    } else {
        let mut text = format!(
            "Total files: {}\nFiles with metadata: {}\nFiles without metadata: {}\n",
            v.total_files, v.files_with_metadata, v.files_without_metadata
        );
        for d in &v.duplicate_ids {
            let _ = writeln!(text, "Duplicate id: {d}");
        }
        stdout(&text)?;
        for w in &v.warnings {
            stderr(&format!("warning: {w}\n"));
        }
    }
    failure.map_or(Ok(()), Err)
}

fn project_root(named: &Path) -> PathBuf {
    let start = std::path::absolute(named).unwrap_or_else(|_| named.to_path_buf());
    for dir in start.ancestors() {
        if dir.join("deploy.sql").is_file() {
            return dir.to_path_buf();
        }
        if dir.join(".git").exists() {
            break;
        }
    }
    start
}

fn prefix_of(key: &str) -> &str {
    key.rfind('/').map_or("", |i| &key[..=i])
}

fn directory(path: &str) -> &str {
    path.rfind('/').map_or("", |i| &path[..i])
}

fn xml_escape(s: &str) -> String {
    s.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
}

/// A `<pgmi-meta>` block; without a key the file keeps its path order.
pub(crate) fn block(id: &str, idempotent: bool, description: &str, key: Option<&str>) -> String {
    let keys = key.map_or_else(String::new, |k| {
        format!(
            "  <sortKeys>\n    <key>{}</key>\n  </sortKeys>\n",
            xml_escape(k)
        )
    });
    format!(
        "/*\n<pgmi-meta\n    id=\"{id}\"\n    idempotent=\"{idempotent}\">\n  <description>\n    {}\n  </description>\n{keys}</pgmi-meta>\n*/\n\n",
        xml_escape(description)
    )
}

/// Each file's sort key: `--sort-key` plus the file name, else the prefix of
/// the first key every sibling with metadata uses. A directory whose metadata
/// has no sort keys (the basic template) runs in path order, so its new files
/// get none either.
fn sort_keys(metas: &[Meta], todo: &[&File], given: Option<&str>) -> Result<Vec<Option<String>>> {
    let mut keys = Vec::new();
    let (mut none, mut mixed) = (Vec::new(), Vec::new());
    for f in todo {
        let name = f.path.rsplit('/').next().unwrap_or(&f.path);
        let dir = directory(&f.path);
        let siblings: Vec<&Meta> = metas.iter().filter(|m| directory(&m.path) == dir).collect();
        let prefixes: BTreeSet<&str> = siblings
            .iter()
            .filter_map(|m| m.sort_keys.first())
            .map(|k| prefix_of(k))
            .collect();
        keys.push(match (given, prefixes.len()) {
            (Some(p), _) => Some(format!("{}/{name}", p.trim_end_matches('/'))),
            (None, 1) => prefixes.first().map(|p| format!("{p}{name}")),
            (None, 0) if !siblings.is_empty() => None,
            (None, 0) => {
                none.push(f.path.as_str());
                continue;
            }
            (None, _) => {
                mixed.push(f.path.as_str());
                continue;
            }
        });
    }
    let Some(example) = none.iter().chain(&mixed).next() else {
        return Ok(keys);
    };
    let mut parts = Vec::new();
    if !none.is_empty() {
        parts.push(format!(
            "no file in the same directory has metadata: {}",
            none.join(", ")
        ));
    }
    if !mixed.is_empty() {
        parts.push(format!(
            "the files in the same directory use different sort-key prefixes: {}",
            mixed.join(", ")
        ));
    }
    let dir = format!("{}/", directory(example));
    Err(Error::usage(format!(
        "no sort-key prefix to derive; {}\npass --sort-key with the layer, e.g. --sort-key 005/core/ (advanced), or --sort-key ./{} to keep path order",
        parts.join("; "),
        dir.trim_start_matches("./")
    )))
}

fn scaffold(a: &ScaffoldArgs, env: &Env) -> Result<()> {
    crate::vfs::OsFs::project(&a.path)?;
    let named = std::path::absolute(&a.path).unwrap_or_else(|_| a.path.clone());
    let root = project_root(&a.path);
    if root != named {
        stderr(&format!("Project root: {}\n", root.display()));
    }
    stderr("Scanning SQL files...\n");
    let max_size = walk::max_size(env)?;
    let project = walk::scan(&crate::vfs::OsFs::project(&root)?, max_size)
        .map_err(walk::scan_failed(None))?;
    let scan = metadata::scan(&project.sources);
    scan.readable().map_err(walk::scan_failed(None))?;
    let metas = scan.metas;
    let under = named
        .strip_prefix(&root)
        .map(|p| p.to_string_lossy().replace('\\', "/"))
        .unwrap_or_default();
    let scope = if under.is_empty() {
        "./".to_string()
    } else {
        format!("./{under}/")
    };
    let have: BTreeSet<&str> = metas.iter().map(|m| m.path.as_str()).collect();
    let todo: Vec<&File> = project
        .sources
        .iter()
        .filter(|f| {
            is_sql(&f.path) && f.path.starts_with(&scope) && !have.contains(f.path.as_str())
        })
        .collect();
    if todo.is_empty() {
        stderr("All SQL files already have metadata.\n");
        return Ok(());
    }
    let keys = sort_keys(&metas, &todo, a.sort_key.as_deref())?;
    stderr(&format!(
        "Found {} file(s) without metadata:\n\n",
        todo.len()
    ));
    for (f, key) in todo.iter().zip(&keys) {
        let id = uuid::Uuid::new_v4().hyphenated().to_string();
        stdout(&format!(
            "  {}\n    ID: {id}\n    Sort key: {}\n    Idempotent: {}\n",
            f.path,
            key.as_deref().unwrap_or("(none, path order)"),
            a.idempotent
        ))?;
        if a.write {
            let file = root.join(f.path.trim_start_matches("./"));
            let name = f.path.rsplit('/').next().unwrap_or(&f.path);
            let text = format!(
                "{}{}",
                block(&id, a.idempotent, name, key.as_deref()),
                f.content
            );
            write_atomic(&file, &text)?;
            stderr(&format!("    Written to {}\n", file.display()));
        }
        stdout("\n")?;
    }
    if a.write {
        stderr(&format!("Generated metadata for {} file(s).\n", todo.len()));
    } else {
        stderr("Preview mode: no files were modified. Use --write to apply changes.\n");
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validate_reports_each_unreadable_block_as_its_own_error() {
        let dir = crate::testdir::TempDir::new("validate-unreadable");
        std::fs::write(dir.join("deploy.sql"), "SELECT 1;").unwrap();
        std::fs::write(
            dir.join("a.sql"),
            "/* <pgmi-meta id=\"0d3b6f0e-5a8e-4f64-9a3e-6a1c2b7d9e01\" idempotent=\"maybe\"></pgmi-meta> */",
        )
        .unwrap();
        let Ok(Validated::Unreadable(errors)) = validate_report(dir.path(), &Env::default()) else {
            panic!("the block was read");
        };
        assert!(
            matches!(&errors[..], [MetaError::Idempotent { path, value }] if path == "./a.sql" && value == "maybe"),
            "{errors:?}"
        );
    }

    #[test]
    fn plan_order_and_fallback() {
        let f = |p: &str, c: &str| File {
            path: p.into(),
            content: c.into(),
        };
        let files = [
            f(
                "./z.sql",
                "/* <pgmi-meta id=\"0d3b6f0e-5a8e-4f64-9a3e-6a1c2b7d9e01\" idempotent=\"true\"><sortKeys><key>001/a</key><key>003/c</key></sortKeys></pgmi-meta> */",
            ),
            f("./notes.txt", "x"),
            f("./plain.sql", "SELECT 1;"),
        ];
        let metas = metadata::collect(&files).unwrap();
        let r = rows(&files, &metas);
        let got: Vec<(&str, &str)> = r
            .iter()
            .map(|r| (r.sort_key.as_str(), r.path.as_str()))
            .collect();
        assert_eq!(
            got,
            [
                ("./notes.txt", "./notes.txt"),
                ("./plain.sql", "./plain.sql"),
                ("001/a", "./z.sql"),
                ("003/c", "./z.sql")
            ]
        );
        assert_eq!(r[1].id, "1d622beb-19db-dd1d-f3e3-8c353019e16a");
        assert!(!r[0].is_sql_file);
    }

    #[test]
    fn written_block_parses() {
        let text = format!(
            "{}SELECT 1;",
            block(
                "0d3b6f0e-5a8e-4f64-9a3e-6a1c2b7d9e01",
                false,
                "a&b.sql",
                Some("010/a&b.sql")
            )
        );
        let m = metadata::parse("./a.sql", &text).unwrap().unwrap();
        assert_eq!(m.sort_keys, ["010/a&b.sql"]);
        assert!(!m.idempotent);
        assert_eq!(m.description.as_deref().map(str::trim), Some("a&b.sql"));
        let text = format!(
            "{}SELECT 1;",
            block("0d3b6f0e-5a8e-4f64-9a3e-6a1c2b7d9e01", true, "a.sql", None)
        );
        let m = metadata::parse("./a.sql", &text).unwrap().unwrap();
        assert!(m.sort_keys.is_empty());
    }

    fn meta(path: &str, keys: &[&str]) -> Meta {
        let keys = keys.iter().fold(String::new(), |mut out, k| {
            let _ = write!(out, "<key>{k}</key>");
            out
        });
        metadata::parse(
            path,
            &format!("/* <pgmi-meta id=\"0d3b6f0e-5a8e-4f64-9a3e-6a1c2b7d9e01\" idempotent=\"true\"><sortKeys>{keys}</sortKeys></pgmi-meta> */"),
        )
        .unwrap()
        .unwrap()
    }

    #[test]
    fn scaffold_keys_follow_go() {
        let file = |p: &str| File {
            path: p.into(),
            content: String::new(),
        };
        let (a, b) = (file("./core/b.sql"), file("./physical/x.sql"));
        let metas = [
            meta("./core/a.sql", &["005/core/a.sql", "900/x/a.sql"]),
            meta("./physical/001.sql", &[]),
        ];
        assert_eq!(
            sort_keys(&metas, &[&a, &b], None).unwrap(),
            [Some("005/core/b.sql".to_string()), None],
            "the first key decides the prefix; a sibling without keys means path order"
        );
        assert_eq!(
            sort_keys(&metas, &[&a], Some("010/api/")).unwrap(),
            [Some("010/api/b.sql".to_string())]
        );
        let lone = file("./api/c.sql");
        let e = sort_keys(&metas, &[&lone], None).unwrap_err();
        assert_eq!(e.exit_code(), 2);
        assert!(
            e.text()
                .ends_with("or --sort-key ./api/ to keep path order"),
            "{}",
            e.text()
        );
        let mixed = [
            meta("./api/a.sql", &["010/api/a.sql"]),
            meta("./api/b.sql", &["020/x/b.sql"]),
        ];
        let e = sort_keys(&mixed, &[&lone], None).unwrap_err();
        assert!(
            e.text()
                .contains("use different sort-key prefixes: ./api/c.sql"),
            "{}",
            e.text()
        );
    }

    #[test]
    fn root_search_stops_at_git() {
        let base = crate::testdir::TempDir::new("root");
        let lib = base.join("repo/lib");
        std::fs::create_dir_all(&lib).unwrap();
        std::fs::create_dir_all(base.join("repo/.git")).unwrap();
        std::fs::write(base.join("deploy.sql"), "SELECT 1;").unwrap();
        assert_eq!(project_root(&lib), lib);
        std::fs::write(base.join("repo/deploy.sql"), "SELECT 1;").unwrap();
        assert_eq!(project_root(&lib), base.join("repo"));
    }

    #[cfg(unix)]
    #[test]
    fn write_never_follows_a_planted_temp_link() {
        let base = crate::testdir::TempDir::new("plant");
        let victim = base.join("victim");
        std::fs::write(&victim, "keep").unwrap();
        for name in [".a.sql.pgmi-tmp", "a.sql.pgmi-tmp"] {
            std::os::unix::fs::symlink(&victim, base.join(name)).unwrap();
        }
        write_atomic(&base.join("a.sql"), "new").unwrap();
        assert_eq!(std::fs::read_to_string(&victim).unwrap(), "keep");
        assert_eq!(std::fs::read_to_string(base.join("a.sql")).unwrap(), "new");
    }

    #[test]
    fn prefixes() {
        assert_eq!(prefix_of("005/core/a.sql"), "005/core/");
        assert_eq!(prefix_of("zzz"), "");
    }
}
