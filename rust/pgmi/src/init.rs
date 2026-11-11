use std::io::Write;
use std::path::{Path, PathBuf};

use crate::cli::InitArgs;
use crate::commands::config::stopped;
use crate::env::Env;
use crate::error::{Error, Result};
use crate::tui::connection::Done;
use crate::vfs::{self, EmbeddedFs, Kind, OsFs, Vfs};

const KEPT: &[&str] = &["pgmi.yaml", ".env", ".git"];
const PLACEHOLDER: &str = "{{PROJECT_NAME}}";

pub(crate) fn check_empty(target: &Path) -> Result<()> {
    if let Ok(entries) = std::fs::read_dir(target) {
        let blocking: Vec<String> = entries
            .filter_map(|e| e.ok())
            .map(|e| e.file_name().to_string_lossy().into_owned())
            .filter(|n| !KEPT.contains(&n.as_str()))
            .collect();
        if !blocking.is_empty() {
            return Err(Error::usage(format!(
                "target directory '{}' is not empty ({})\n\npgmi init requires an empty directory to avoid overwriting existing files.\n\nOptions:\n• Choose a different location\n• Remove existing files manually\n• Use a new directory name",
                target.display(),
                blocking.join(", ")
            )));
        }
    }
    Ok(())
}

pub(crate) fn run(a: &InitArgs, env: &Env) -> Result<()> {
    let target = a.path.clone().unwrap_or_else(|| PathBuf::from("."));
    check_empty(&target)?;
    let mut connection = None;
    let template = match a.template.as_deref() {
        Some(t) => t,
        None if crate::tui::interactive(env) => {
            match crate::tui::init::run(&target.display().to_string())? {
                crate::tui::init::Done::Finished { template, connect } => {
                    if connect {
                        connection = Some(match crate::tui::connection::run(None, env)? {
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
        None => "minimal",
    };
    scaffold(&target, template, None)?;
    let _ = writeln!(
        std::io::stderr(),
        "Wrote {} ({template} template).",
        target.display()
    );
    let absolute = std::path::absolute(&target).unwrap_or_else(|_| target.clone());
    let mut tree = format!("{}/\n", absolute.display());
    draw(&OsFs::new(&absolute), "", "", &mut tree);
    tree.push('\n');
    let _ = std::io::stdout().lock().write_all(tree.as_bytes());
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

pub(crate) fn scaffold(target: &Path, template: &str, name: Option<&str>) -> Result<()> {
    let name = match name {
        Some(n) => n.to_string(),
        None => std::path::absolute(target)
            .ok()
            .and_then(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
            .unwrap_or_else(|| "project".into()),
    };
    for (rel, bytes) in render(template, &name)? {
        let path = target.join(&rel);
        if KEPT.contains(&rel.as_str()) && path.exists() {
            continue;
        }
        write(&path, &bytes)?;
    }
    Ok(())
}

pub(crate) fn render(template: &str, name: &str) -> Result<Vec<(String, Vec<u8>)>> {
    let unknown = || Error::usage(format!("unknown template: {template}"));
    if !crate::templates::NAMES.contains(&template) {
        return Err(unknown());
    }
    let read = |e: std::io::Error| Error::internal(format!("embedded template {template}: {e}"));
    let mut files = vfs::files(&EmbeddedFs::new(&format!("templates/{template}"))).map_err(read)?;
    if files.is_empty() {
        return Err(unknown());
    }
    if template != "minimal" {
        files.extend(
            vfs::files(&EmbeddedFs::new("skills"))
                .map_err(read)?
                .into_iter()
                .map(|(p, b)| (format!(".agents/skills/{p}"), b)),
        );
    }
    Ok(files
        .into_iter()
        .map(|(rel, bytes)| {
            let rel = rel.strip_suffix(".scaffold").unwrap_or(&rel).to_string();
            let bytes = fill(&rel, bytes, name);
            if rel == "deploy.sql" {
                let mut text = format!(
                    "-- Scaffolded by pgmi {} from the {template} template.\n",
                    crate::VERSION
                )
                .into_bytes();
                text.extend_from_slice(&bytes);
                (rel, text)
            } else {
                (rel, bytes)
            }
        })
        .collect())
}

fn fill(rel: &str, bytes: Vec<u8>, name: &str) -> Vec<u8> {
    match std::str::from_utf8(&bytes) {
        Ok(text) if text.contains(PLACEHOLDER) => {
            let value = if rel.ends_with(".json") {
                let quoted = serde_json::to_string(name).unwrap_or_default();
                quoted
                    .strip_prefix('"')
                    .and_then(|q| q.strip_suffix('"'))
                    .unwrap_or_default()
                    .to_string()
            } else {
                name.to_string()
            };
            text.replace(PLACEHOLDER, &value).into_bytes()
        }
        _ => bytes,
    }
}

const AGENTS_NOTE: &str = "AGENTS.md and .agents/skills/ tell an AI assistant how this project works;\ncommit them, and edit them as your conventions change. Run \"pgmi ai\" for the\ndocs that match this pgmi version.\n";

fn next_steps(template: &str, target: &str, db: Option<&str>) -> String {
    let db = db.unwrap_or(if template == "advanced" {
        "myapp_dev"
    } else {
        "mydb"
    });
    match template {
        "minimal" => format!(
            "\nNext:\n  pgmi deploy {target} -d {db}\n\ndeploy.sql runs every .sql file in path order. Add files, then change its\nquery to decide what runs. Run \"pgmi ai\" for the docs that match this version.\n"
        ),
        "basic" => {
            format!("\nNext:\n  pgmi deploy {target} -d {db} --param env=dev\n\n{AGENTS_NOTE}")
        }
        _ => format!(
            "Managed-cloud note: the advanced template needs PostgreSQL 18 or newer and the\nuuid-ossp, pgcrypto, pg_trgm, hstore and btree_gist extensions. No superuser\nrequired: deploy as a role with CREATEROLE and CREATEDB. Details:\nhttps://github.com/vvka-141/pgmi/blob/main/docs/PRODUCTION.md#managed-cloud-postgresql\n\nNext:\n  pgmi deploy {target} -d {db} --param env=dev\n\nFor a shared database use env=test or env=prod, with the passwords in\nPGMI_PARAM_* environment variables or a --params-file.\n\n{AGENTS_NOTE}"
        ),
    }
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
    use crate::project::walk;
    use crate::vfs::MemFs;

    fn rendered(template: &str, name: &str) -> MemFs {
        MemFs::from_files(render(template, name).unwrap())
    }

    #[test]
    fn every_template_scaffolds_a_project_that_loads_in_memory() {
        for t in crate::templates::NAMES {
            let fs = rendered(t, "shop");
            let p = walk::load(&fs, walk::DEFAULT_MAX_FILE_SIZE)
                .unwrap_or_else(|e| panic!("{t}: {}", e.text()));
            assert!(
                p.deploy_sql.starts_with(&format!(
                    "-- Scaffolded by pgmi {} from the {t} template.\n",
                    crate::VERSION
                )),
                "{t}"
            );
            crate::project::metadata::collect(&p.sources)
                .unwrap_or_else(|e| panic!("{t}: {}", e.text()));
            let all = vfs::files(&fs).unwrap();
            assert!(
                all.iter().all(|(_, b)| !b
                    .windows(PLACEHOLDER.len())
                    .any(|w| w == PLACEHOLDER.as_bytes())),
                "{t}: placeholder left"
            );
            assert!(all.iter().all(|(p, _)| !p.ends_with(".scaffold")), "{t}");
        }
    }

    #[test]
    fn skills_ship_with_basic_and_advanced_only() {
        let has_skills = |t| {
            render(t, "x")
                .unwrap()
                .iter()
                .any(|(p, _)| p.starts_with(".agents/skills/"))
        };
        assert!(has_skills("basic"));
        assert!(has_skills("advanced"));
        assert!(!has_skills("minimal"));
    }

    #[test]
    fn the_project_name_is_json_escaped_in_json_files() {
        let fs = rendered("basic", "a\"b");
        let json = String::from_utf8(fs.read("project.json").unwrap()).unwrap();
        assert!(json.contains(r#""app_name": "a\"b""#), "{json}");
        let readme = String::from_utf8(fs.read("README.md").unwrap()).unwrap();
        assert!(readme.starts_with("# a\"b\n"), "{readme}");
    }

    #[test]
    fn scaffold_suffix_is_dropped() {
        let files = render("advanced", "x").unwrap();
        assert!(files.iter().any(|(p, _)| p == "gateways/go/go.mod"));
    }

    #[test]
    fn unknown_template_is_a_usage_error() {
        assert_eq!(render("nope", "x").unwrap_err().exit_code(), 2);
    }

    #[test]
    fn the_tree_summarises_skills_and_skips_git() {
        let fs = MemFs::default()
            .file("deploy.sql", "")
            .file(".git/HEAD", "")
            .file(".agents/skills/a/SKILL.md", "")
            .file(".agents/skills/b/SKILL.md", "")
            .file("lib/x.sql", "");
        let mut out = String::new();
        draw(&fs, "", "", &mut out);
        assert_eq!(
            out,
            "├── .agents/\n│   └── skills/  (2 skills)\n├── .git/\n├── deploy.sql\n└── lib/\n    └── x.sql\n"
        );
    }
}
