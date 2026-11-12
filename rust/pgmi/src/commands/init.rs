use std::collections::{BTreeMap, BTreeSet};
use std::fmt::Write as _;
use std::io::Write;
use std::path::{Path, PathBuf};

use crate::cli::InitArgs;
use crate::commands::config::interrupted;
use crate::env::Env;
use crate::error::{Error, Result};
use crate::output::Reporter;
use crate::templates::{Source, Template};
use crate::tui;
use crate::tui::init::Done;
use crate::vfs::{FileKind, OsFs, Vfs};

const KEPT: &[&str] = &["pgmi.yaml", ".env", ".git"];

pub(crate) fn template(name: &str) -> Result<Template> {
    name.parse()
        .map_err(|_| Error::usage(format!("unknown template: {name}")))
}

/// What in the target stops a scaffold. A missing target blocks nothing;
/// one that cannot be listed is refused, as Go refuses it.
fn blocking(target: &impl Vfs) -> Result<Vec<String>> {
    let entries = match target.read_dir("") {
        Ok(entries) => entries,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(unlistable(target, e)),
    };
    Ok(entries
        .into_iter()
        .map(|e| e.name.as_str().to_string())
        .filter(|n| !KEPT.contains(&n.as_str()))
        .collect())
}

fn unlistable(target: &impl Vfs, listing: std::io::Error) -> Error {
    let path = target.display("");
    let checking = "failed to create project: failed to check target directory";
    match target.stat("") {
        Err(e) => Error::general(format!(
            "{checking}: failed to check directory: stat {path}"
        ))
        .with_source(e),
        Ok(s) if s.kind != FileKind::Dir => {
            Error::general(format!("{checking}: path exists but is not a directory"))
        }
        Ok(_) => Error::general(format!("{checking}: failed to read directory: open {path}"))
            .with_source(listing),
    }
}

pub(crate) fn check_empty(target: &impl Vfs) -> Result<()> {
    let blocking = blocking(target)?;
    if blocking.is_empty() {
        return Ok(());
    }
    Err(Error::usage(format!(
        "target directory '{}' is not empty ({})\n\npgmi init requires an empty directory to avoid overwriting existing files.\n\nOptions:\n• Choose a different location\n• Remove existing files manually\n• Use a new directory name",
        target.display(""),
        blocking.join(", ")
    )))
}

pub(crate) fn run(
    a: &InitArgs,
    verbose: bool,
    env: &Env,
    runtime: &tokio::runtime::Handle,
) -> Result<()> {
    let target = a.path.clone().unwrap_or_else(|| PathBuf::from("."));
    check_empty(&OsFs::new(&target))?;
    let mut connection = None;
    let template = match a.template.as_deref() {
        Some(t) => template(t)?,
        None if crate::tui::interactive(env) => {
            match tui::init::run(&target.display().to_string(), env, runtime)? {
                Done::Finished {
                    template,
                    connection: settings,
                } => {
                    connection = settings;
                    template
                }
                Done::Cancelled => return Err(tui::init::cancelled()),
                Done::Interrupted => return Err(interrupted("init")),
            }
        }
        None => Template::Minimal,
    };
    let reporter = Reporter::new(verbose, env);
    scaffold(&target, template, None, &|n| reporter.verbose(n))?;
    let _ = writeln!(
        std::io::stderr(),
        "Wrote {} ({template} template).",
        target.display()
    );
    let absolute = std::path::absolute(&target).unwrap_or_else(|_| target.clone());
    let _ = std::io::stdout()
        .lock()
        .write_all(tree(&OsFs::new(&absolute)).as_bytes());
    let mut db = None;
    if let Some(s) = &connection {
        if let Err(e) = crate::commands::config::save(&target, s, false) {
            let _ = writeln!(
                std::io::stderr(),
                "WARNING: could not save connection: {}",
                e.text()
            );
        } else {
            let _ = writeln!(
                std::io::stderr(),
                "Wrote {}",
                target.join("pgmi.yaml").display()
            );
            crate::commands::config::offer_pgpass(s, env);
            db = s.database.as_deref();
        }
    }
    let _ = std::io::stderr()
        .write_all(next_steps(template, &target.display().to_string(), db).as_bytes());
    Ok(())
}

pub(crate) fn scaffold(
    target: &Path,
    template: Template,
    name: Option<&str>,
    note: &dyn Fn(&str),
) -> Result<()> {
    let name = match name {
        Some(n) => n.to_string(),
        None => std::path::absolute(target)
            .ok()
            .and_then(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
            .unwrap_or_else(|| "project".into()),
    };
    let embedded =
        |e: std::io::Error| Error::internal(format!("embedded template {template}: {e}"));
    let rendered = template.render(&name, crate::VERSION).map_err(embedded)?;
    let mut files: BTreeMap<String, Vec<u8>> = writable(&OsFs::new(target), rendered).collect();
    note(&format!(
        "Creating project '{name}' at {} with template '{template}'",
        target.display()
    ));
    let mut dirs = BTreeSet::new();
    for source in template.sources().map_err(embedded)? {
        if let Source::Template(n) = &source {
            for (i, _) in n.match_indices('/') {
                if dirs.insert(n[..i].to_string()) {
                    note(&format!("Creating directory: {}", &n[..i]));
                }
            }
        }
        match files.remove(source.path()) {
            Some(bytes) => {
                note(&format!("Creating file: {}", source.name()));
                write(&target.join(source.path()), &bytes)?;
            }
            None => note(&format!("Skipping existing: {}", source.name())),
        }
    }
    note("Project created successfully");
    Ok(())
}

fn writable(
    target: &impl Vfs,
    files: Vec<(String, Vec<u8>)>,
) -> impl Iterator<Item = (String, Vec<u8>)> {
    files.into_iter().filter(move |(rel, _)| {
        !(KEPT.contains(&rel.as_str())
            && target
                .stat(rel)
                .is_ok_and(|s| !(s.link && s.kind == FileKind::Other)))
    })
}

const AGENTS_NOTE: &str = "AGENTS.md and .agents/skills/ tell an AI assistant how this project works;\ncommit them, and edit them as your conventions change. Run \"pgmi ai\" for the\ndocs that match this pgmi version.\n";

fn next_steps(template: Template, target: &str, db: Option<&str>) -> String {
    match template {
        Template::Minimal => {
            let db = db.unwrap_or("mydb");
            format!(
                "\nNext:\n  pgmi deploy {target} -d {db}\n\ndeploy.sql runs every .sql file in path order. Add files, then change its\nquery to decide what runs. Run \"pgmi ai\" for the docs that match this version.\n"
            )
        }
        Template::Basic => {
            let db = db.unwrap_or("mydb");
            format!("\nNext:\n  pgmi deploy {target} -d {db} --param env=dev\n\n{AGENTS_NOTE}")
        }
        Template::Advanced => {
            let db = db.unwrap_or("myapp_dev");
            format!(
                "Managed-cloud note: the advanced template needs PostgreSQL 18 or newer and the\nuuid-ossp, pgcrypto, pg_trgm, hstore and btree_gist extensions. No superuser\nrequired: deploy as a role with CREATEROLE and CREATEDB. Details:\nhttps://github.com/vvka-141/pgmi/blob/main/docs/PRODUCTION.md#managed-cloud-postgresql\n\nNext:\n  pgmi deploy {target} -d {db} --param env=dev\n\nFor a shared database use env=test or env=prod, with the passwords in\nPGMI_PARAM_* environment variables or a --params-file.\n\n{AGENTS_NOTE}"
            )
        }
    }
}

fn tree(fs: &impl Vfs) -> String {
    let mut out = format!("{}/\n", fs.display(""));
    draw(fs, "", "", &mut out);
    out.push('\n');
    out
}

fn draw(fs: &impl Vfs, rel: &str, indent: &str, out: &mut String) {
    let entries = fs.read_dir(rel).unwrap_or_default();
    let last = entries.len().saturating_sub(1);
    for (i, entry) in entries.iter().enumerate() {
        let name = entry.name.as_str();
        let (branch, more) = if i == last {
            ("└── ", "    ")
        } else {
            ("├── ", "│   ")
        };
        if entry.stat.link || entry.stat.kind != FileKind::Dir {
            let _ = writeln!(out, "{indent}{branch}{name}");
            continue;
        }
        let _ = writeln!(out, "{indent}{branch}{name}/");
        let child = if rel.is_empty() {
            name.to_string()
        } else {
            format!("{rel}/{name}")
        };
        if name == ".git" {
            continue;
        }
        if name == ".agents" && indent.is_empty() {
            let skills = fs.read_dir(&format!("{child}/skills")).map_or(0, |e| {
                e.iter().filter(|e| e.stat.kind == FileKind::Dir).count()
            });
            let _ = writeln!(out, "{indent}{more}└── skills/  ({skills} skills)");
            continue;
        }
        draw(fs, &child, &format!("{indent}{more}"), out);
    }
}

/// Creates `path` with `bytes`, never replacing a file that exists. A
/// dangling link is written through, creating its target, as Go does.
fn write(path: &Path, bytes: &[u8]) -> Result<()> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)
            .map_err(|e| Error::general(format!("create {}", dir.display())).with_source(e))?;
    }
    let target = match std::fs::read_link(path) {
        Ok(link) => path
            .parent()
            .map_or_else(|| link.clone(), |dir| dir.join(&link)),
        Err(_) => path.to_path_buf(),
    };
    std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(&target)
        .and_then(|mut f| f.write_all(bytes))
        .map_err(|e| Error::general(format!("write {}", path.display())).with_source(e))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vfs::MemFs;

    #[test]
    fn unknown_template_is_a_usage_error() {
        let e = template("nope").unwrap_err();
        assert_eq!(e.exit_code(), 2);
        assert_eq!(e.text(), "unknown template: nope");
        assert_eq!(template("advanced").unwrap(), Template::Advanced);
    }

    #[test]
    fn git_pgmi_yaml_and_env_do_not_block() {
        for fs in [
            MemFs::default(),
            MemFs::default().dir(".git"),
            MemFs::default().file(".git", "gitdir: ../.git/worktrees/x\n"),
            MemFs::default()
                .dir(".git")
                .file("pgmi.yaml", "")
                .file(".env", ""),
        ] {
            assert!(blocking(&fs).unwrap().is_empty());
            assert!(check_empty(&fs).is_ok());
        }
        let missing = OsFs::new("/definitely/not/here");
        assert!(check_empty(&missing).is_ok());
    }

    #[test]
    fn any_other_entry_blocks() {
        let fs = MemFs::default()
            .dir(".git")
            .file("README.md", "x")
            .dir("lib");
        assert_eq!(blocking(&fs).unwrap(), ["README.md", "lib"]);
        let e = check_empty(&fs).unwrap_err();
        assert_eq!(e.exit_code(), 2);
        assert!(
            e.text()
                .starts_with("target directory '/mem' is not empty (README.md, lib)\n"),
            "{}",
            e.text()
        );
    }

    #[test]
    fn a_target_that_cannot_be_listed_is_refused() {
        let e = check_empty(&MemFs::default().unreadable_dir("")).unwrap_err();
        assert_eq!(e.exit_code(), 1);
        assert_eq!(
            e.text(),
            "failed to create project: failed to check target directory: failed to read directory: open /mem: permission denied"
        );
    }

    #[test]
    fn a_file_as_the_target_is_refused() {
        let dir = crate::testdir::TempDir::new("init-file-target");
        let file = dir.join("deploy.sql");
        std::fs::write(&file, "SELECT 'mine';").unwrap();
        let e = check_empty(&OsFs::new(&file)).unwrap_err();
        assert_eq!(e.exit_code(), 1);
        assert_eq!(
            e.text(),
            "failed to create project: failed to check target directory: path exists but is not a directory"
        );
        let e = check_empty(&OsFs::new(file.join("inside"))).unwrap_err();
        assert!(
            e.text().starts_with(&format!(
                "failed to create project: failed to check target directory: failed to check directory: stat {}",
                file.join("inside").display()
            )),
            "{}",
            e.text()
        );
    }

    #[test]
    fn scaffolding_never_replaces_an_existing_file() {
        let dir = crate::testdir::TempDir::new("init-exclusive");
        std::fs::write(dir.join("deploy.sql"), "SELECT 'mine';").unwrap();
        let e = scaffold(dir.path(), Template::Minimal, Some("p"), &|_| {}).unwrap_err();
        assert_eq!(e.exit_code(), 1);
        assert!(e.text().starts_with("write "), "{}", e.text());
        assert_eq!(
            std::fs::read_to_string(dir.join("deploy.sql")).unwrap(),
            "SELECT 'mine';"
        );
    }

    #[cfg(unix)]
    #[test]
    fn a_dangling_kept_link_is_written_through() {
        let dir = crate::testdir::TempDir::new("init-dangling");
        std::os::unix::fs::symlink("real.yaml", dir.join("pgmi.yaml")).unwrap();
        scaffold(dir.path(), Template::Basic, Some("p"), &|_| {}).unwrap();
        assert!(dir.join("real.yaml").is_file());
    }

    #[test]
    fn existing_pgmi_yaml_env_and_git_are_kept() {
        let files = Template::Basic.render("p", "0").unwrap();
        assert!(files.iter().any(|(p, _)| p == "pgmi.yaml"));
        let kept = |fs: &MemFs| -> Vec<String> {
            writable(fs, files.clone())
                .map(|(p, _)| p)
                .filter(|p| KEPT.contains(&p.as_str()))
                .collect()
        };
        assert_eq!(kept(&MemFs::default()), ["pgmi.yaml"]);
        let fs = MemFs::default().file("pgmi.yaml", "connection:\n  host: myserver\n");
        assert!(kept(&fs).is_empty());
        let written: Vec<_> = writable(&fs, files.clone()).map(|(p, _)| p).collect();
        assert!(written.iter().any(|p| p == "deploy.sql"));
        assert_eq!(written.len(), files.len() - 1);
        assert_eq!(
            kept(&MemFs::default().dangling_link("pgmi.yaml")),
            ["pgmi.yaml"]
        );
    }

    #[test]
    fn the_tree_lists_the_project_under_its_root() {
        let fs = MemFs::default()
            .file("deploy.sql", "")
            .file("README.md", "")
            .file("migrations/001_test.sql", "")
            .file("__test__/test_foo.sql", "");
        assert_eq!(
            tree(&fs),
            "/mem/\n├── README.md\n├── __test__/\n│   └── test_foo.sql\n├── deploy.sql\n└── migrations/\n    └── 001_test.sql\n\n"
        );
        assert_eq!(tree(&MemFs::default()), "/mem/\n\n");
    }

    #[test]
    fn the_tree_summarises_skills_and_skips_git() {
        let fs = MemFs::default()
            .file("deploy.sql", "")
            .file(".git/HEAD", "")
            .file(".agents/skills/a/SKILL.md", "")
            .file(".agents/skills/a/references/ref.md", "")
            .file(".agents/skills/b/SKILL.md", "")
            .file("lib/x.sql", "");
        let mut out = String::new();
        draw(&fs, "", "", &mut out);
        assert_eq!(
            out,
            "├── .agents/\n│   └── skills/  (2 skills)\n├── .git/\n├── deploy.sql\n└── lib/\n    └── x.sql\n"
        );
    }

    #[test]
    fn next_steps_name_the_default_database_per_template() {
        assert!(next_steps(Template::Minimal, "p", None).contains("pgmi deploy p -d mydb\n"));
        assert!(
            next_steps(Template::Basic, "p", None)
                .contains("pgmi deploy p -d mydb --param env=dev\n")
        );
        let advanced = next_steps(Template::Advanced, "p", Some("shop"));
        assert!(advanced.starts_with("Managed-cloud note:"));
        assert!(advanced.contains("pgmi deploy p -d shop --param env=dev\n"));
    }
}
