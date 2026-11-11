use std::io::Write;
use std::path::{Path, PathBuf};

use crate::cli::InitArgs;
use crate::commands::config::stopped;
use crate::env::Env;
use crate::error::{Error, Result};
use crate::templates::Template;
use crate::tui::connection::Done;
use crate::vfs::{Kind, OsFs, Vfs};

const KEPT: &[&str] = &["pgmi.yaml", ".env", ".git"];

pub(crate) fn template(name: &str) -> Result<Template> {
    name.parse()
        .map_err(|_| Error::usage(format!("unknown template: {name}")))
}

fn blocking(target: &impl Vfs) -> Vec<String> {
    target
        .read_dir("")
        .unwrap_or_default()
        .into_iter()
        .map(|e| e.name.as_str().to_string())
        .filter(|n| !KEPT.contains(&n.as_str()))
        .collect()
}

pub(crate) fn check_empty(target: &impl Vfs) -> Result<()> {
    let blocking = blocking(target);
    if blocking.is_empty() {
        return Ok(());
    }
    Err(Error::usage(format!(
        "target directory '{}' is not empty ({})\n\npgmi init requires an empty directory to avoid overwriting existing files.\n\nOptions:\n• Choose a different location\n• Remove existing files manually\n• Use a new directory name",
        target.display(""),
        blocking.join(", ")
    )))
}

pub(crate) fn run(a: &InitArgs, env: &Env, runtime: &tokio::runtime::Handle) -> Result<()> {
    let target = a.path.clone().unwrap_or_else(|| PathBuf::from("."));
    check_empty(&OsFs::new(&target))?;
    let mut connection = None;
    let template = match a.template.as_deref() {
        Some(t) => template(t)?,
        None if crate::tui::interactive(env) => {
            match crate::tui::init::run(&target.display().to_string())? {
                crate::tui::init::Done::Finished { template, connect } => {
                    if connect {
                        connection = Some(match crate::tui::connection::run(None, env, runtime)? {
                            Done::Finished(s) => s,
                            Done::Cancelled => return Err(stopped("init", false)),
                            Done::Interrupted => return Err(stopped("init", true)),
                        });
                    }
                    template
                }
                crate::tui::init::Done::Cancelled => return Err(crate::tui::init::cancelled()),
            }
        }
        None => Template::Minimal,
    };
    scaffold(&target, template, None)?;
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
        match crate::commands::config::save(&target, s, false) {
            Err(e) => {
                let _ = writeln!(
                    std::io::stderr(),
                    "WARNING: could not save connection: {}",
                    e.text()
                );
            }
            Ok(()) => {
                let _ = writeln!(
                    std::io::stderr(),
                    "Wrote {}",
                    target.join("pgmi.yaml").display()
                );
                crate::commands::config::offer_pgpass(s, env);
                db = Some(s.database.as_str()).filter(|d| !d.is_empty());
            }
        }
    }
    let _ = std::io::stderr()
        .write_all(next_steps(template, &target.display().to_string(), db).as_bytes());
    Ok(())
}

pub(crate) fn scaffold(target: &Path, template: Template, name: Option<&str>) -> Result<()> {
    let name = match name {
        Some(n) => n.to_string(),
        None => std::path::absolute(target)
            .ok()
            .and_then(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
            .unwrap_or_else(|| "project".into()),
    };
    let files = template
        .render(&name, crate::VERSION)
        .map_err(|e| Error::internal(format!("embedded template {template}: {e}")))?;
    for (rel, bytes) in writable(&OsFs::new(target), files) {
        write(&target.join(&rel), &bytes)?;
    }
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
                .is_ok_and(|s| !(s.link && s.kind == Kind::Other)))
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
        if entry.stat.link || entry.stat.kind != Kind::Dir {
            out.push_str(&format!("{indent}{branch}{name}\n"));
            continue;
        }
        out.push_str(&format!("{indent}{branch}{name}/\n"));
        let child = if rel.is_empty() {
            name.to_string()
        } else {
            format!("{rel}/{name}")
        };
        if name == ".git" {
            continue;
        }
        if name == ".agents" && indent.is_empty() {
            let skills = fs
                .read_dir(&format!("{child}/skills"))
                .map(|e| e.iter().filter(|e| e.stat.kind == Kind::Dir).count())
                .unwrap_or(0);
            out.push_str(&format!("{indent}{more}└── skills/  ({skills} skills)\n"));
            continue;
        }
        draw(fs, &child, &format!("{indent}{more}"), out);
    }
}

fn write(path: &Path, bytes: &[u8]) -> Result<()> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)
            .map_err(|e| Error::general(format!("create {}", dir.display())).with_source(e))?;
    }
    std::fs::write(path, bytes)
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
            assert!(blocking(&fs).is_empty());
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
        assert_eq!(blocking(&fs), ["README.md", "lib"]);
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
