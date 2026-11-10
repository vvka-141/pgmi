use std::io::Write;
use std::path::{Path, PathBuf};

use crate::cli::InitArgs;
use crate::error::{Error, Result};

const KEPT: &[&str] = &["pgmi.yaml", ".env", ".git"];
const PLACEHOLDER: &str = "{{PROJECT_NAME}}";

pub fn run(a: &InitArgs) -> Result<()> {
    let target = a.path.clone().unwrap_or_else(|| PathBuf::from("."));
    if let Ok(entries) = std::fs::read_dir(&target) {
        let blocking: Vec<String> = entries
            .filter_map(|e| e.ok())
            .map(|e| e.file_name().to_string_lossy().into_owned())
            .filter(|n| !KEPT.contains(&n.as_str()))
            .collect();
        if !blocking.is_empty() {
            return Err(Error::usage(format!(
                "{} is not empty ({}); pgmi init writes into an empty directory",
                target.display(),
                blocking.join(", ")
            )));
        }
    }
    let mut connect = false;
    let template = match a.template.as_deref() {
        Some(t) => t,
        None if crate::tui::interactive(&crate::env::Env::snapshot()) => {
            match crate::tui::init::run(&target.display().to_string())? {
                crate::tui::init::Done::Finished {
                    template,
                    connect: c,
                } => {
                    connect = c;
                    template
                }
                crate::tui::init::Done::Cancelled => return Err(crate::tui::init::cancelled()),
            }
        }
        None => "minimal",
    };
    let name = std::path::absolute(&target)
        .ok()
        .and_then(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
        .unwrap_or_else(|| "project".into());
    let prefix = format!("templates/{template}/");
    let mut files: Vec<(String, &'static [u8])> = pgmi_shared::dir(&prefix)
        .map(|(p, b)| (p[prefix.len()..].to_string(), b))
        .collect();
    if template != "minimal" {
        files.extend(pgmi_shared::dir("skills/").map(|(p, b)| (format!(".agents/{p}"), b)));
    }
    if files.is_empty() {
        return Err(Error::usage(format!("unknown template {template:?}")));
    }
    for (rel, bytes) in files {
        let rel = rel.strip_suffix(".scaffold").unwrap_or(&rel).to_string();
        let path = target.join(&rel);
        if KEPT.iter().any(|k| *k == rel) && path.exists() {
            continue;
        }
        let name_bytes;
        let bytes = match std::str::from_utf8(bytes) {
            Ok(text) if text.contains(PLACEHOLDER) => {
                let value = if rel.ends_with(".json") {
                    let quoted = serde_json::to_string(&name).unwrap_or_default();
                    quoted
                        .strip_prefix('"')
                        .and_then(|q| q.strip_suffix('"'))
                        .unwrap_or_default()
                        .to_string()
                } else {
                    name.clone()
                };
                name_bytes = text.replace(PLACEHOLDER, &value).into_bytes();
                &name_bytes[..]
            }
            _ => bytes,
        };
        if rel == "deploy.sql" {
            let mut text = format!(
                "-- Scaffolded by pgmi {} from the {template} template.\n",
                crate::cli::VERSION
            )
            .into_bytes();
            text.extend_from_slice(bytes);
            write(&path, &text)?;
        } else {
            write(&path, bytes)?;
        }
    }
    let _ = writeln!(
        std::io::stderr(),
        "Wrote {} ({template} template).",
        target.display()
    );
    let absolute = std::path::absolute(&target).unwrap_or_else(|_| target.clone());
    let mut tree = format!("{}/\n", absolute.display());
    draw(&absolute, "", &mut tree);
    tree.push('\n');
    let _ = std::io::stdout().lock().write_all(tree.as_bytes());
    let _ =
        std::io::stderr().write_all(next_steps(template, &target.display().to_string()).as_bytes());
    if connect {
        let _ = writeln!(
            std::io::stderr(),
            "\nThe connection wizard is not in this build yet; set the connection in {} or pass --connection to pgmi deploy.",
            target.join("pgmi.yaml").display()
        );
    }
    Ok(())
}

const AGENTS_NOTE: &str = "AGENTS.md and .agents/skills/ tell an AI assistant how this project works;\ncommit them, and edit them as your conventions change. Run \"pgmi ai\" for the\ndocs that match this pgmi version.\n";

fn next_steps(template: &str, target: &str) -> String {
    match template {
        "minimal" => format!(
            "\nNext:\n  pgmi deploy {target} -d mydb\n\ndeploy.sql runs every .sql file in path order. Add files, then change its\nquery to decide what runs. Run \"pgmi ai\" for the docs that match this version.\n"
        ),
        "basic" => {
            format!("\nNext:\n  pgmi deploy {target} -d mydb --param env=dev\n\n{AGENTS_NOTE}")
        }
        _ => format!(
            "Managed-cloud note: the advanced template needs PostgreSQL 18 or newer and the\nuuid-ossp, pgcrypto, pg_trgm, hstore and btree_gist extensions. No superuser\nrequired: deploy as a role with CREATEROLE and CREATEDB. Details:\nhttps://github.com/vvka-141/pgmi/blob/main/docs/PRODUCTION.md#managed-cloud-postgresql\n\nNext:\n  pgmi deploy {target} -d myapp_dev --param env=dev\n\nFor a shared database use env=test or env=prod, with the passwords in\nPGMI_PARAM_* environment variables or a --params-file.\n\n{AGENTS_NOTE}"
        ),
    }
}

fn draw(dir: &Path, indent: &str, out: &mut String) {
    let mut entries: Vec<(String, bool)> = std::fs::read_dir(dir)
        .map(|rd| {
            rd.filter_map(|e| e.ok())
                .map(|e| {
                    let is_dir = e.file_type().is_ok_and(|t| t.is_dir());
                    (e.file_name().to_string_lossy().into_owned(), is_dir)
                })
                .collect()
        })
        .unwrap_or_default();
    entries.sort();
    let last = entries.len().saturating_sub(1);
    for (i, (name, is_dir)) in entries.iter().enumerate() {
        let (branch, more) = if i == last {
            ("└── ", "    ")
        } else {
            ("├── ", "│   ")
        };
        if !is_dir {
            out.push_str(&format!("{indent}{branch}{name}\n"));
            continue;
        }
        out.push_str(&format!("{indent}{branch}{name}/\n"));
        let path = dir.join(name);
        if name == ".git" {
            continue;
        }
        if name == ".agents" && indent.is_empty() {
            let skills = std::fs::read_dir(path.join("skills"))
                .map(|rd| {
                    rd.filter_map(|e| e.ok())
                        .filter(|e| e.path().is_dir())
                        .count()
                })
                .unwrap_or(0);
            out.push_str(&format!("{indent}{more}└── skills/  ({skills} skills)\n"));
            continue;
        }
        draw(&path, &format!("{indent}{more}"), out);
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
