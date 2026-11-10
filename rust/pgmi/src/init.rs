use std::io::Write;
use std::path::{Path, PathBuf};

use crate::cli::InitArgs;
use crate::error::{Error, Result};

const KEPT: &[&str] = &["pgmi.yaml", ".env", ".git"];

pub fn run(a: &InitArgs) -> Result<()> {
    let target = a.path.clone().unwrap_or_else(|| PathBuf::from("."));
    let template = a.template.as_deref().unwrap_or("minimal");
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
    let mut out = std::io::stdout().lock();
    let _ = writeln!(out, "Wrote {} ({template} template).", target.display());
    Ok(())
}

fn write(path: &Path, bytes: &[u8]) -> Result<()> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)
            .map_err(|e| Error::general(format!("create {}", dir.display())).with_source(e))?;
    }
    std::fs::write(path, bytes)
        .map_err(|e| Error::general(format!("write {}", path.display())).with_source(e))
}
