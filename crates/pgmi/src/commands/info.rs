use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::path::PathBuf;

use serde::Serialize;

use super::{json, stdout};
use crate::cli::InfoArgs;
use crate::config::yaml::ProjectConfig;
use crate::conn::resolve::{self, Flags, ResolveError};
use crate::env::Env;
use crate::error::Result;
use crate::project::{metadata, walk};
use crate::vfs::Vfs;

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct Info {
    path: String,
    template: String,
    deploy_sql: bool,
    config_file: String,
    total_files: usize,
    sql_files: usize,
    test_files: usize,
    metadata_with: usize,
    #[serde(skip_serializing_if = "Option::is_none")]
    scaffolded: Option<String>,
    directories: serde_json::Map<String, serde_json::Value>,
}

fn scaffolded(project: &impl Vfs) -> Option<(String, String)> {
    let text = project.read_to_string("deploy.sql").ok()?;
    let line = text.lines().next()?;
    let rest = line.strip_prefix("-- Scaffolded by ")?;
    let (by, tail) = rest.rsplit_once(" from the ")?;
    let template = tail.strip_suffix(" template.")?;
    Some((template.to_string(), format!("{by}, {template} template")))
}

fn directory(path: &str) -> String {
    match path.rfind('/') {
        Some(i) => path[..=i].to_string(),
        None => "./".to_string(),
    }
}

/// Loads `pgmi.yaml` and judges its values as deploy does, on their own:
/// no flag, variable or other source replaces one.
fn checked(project: &impl Vfs) -> Result<Option<ProjectConfig>> {
    let Some(cfg) = ProjectConfig::load(project)? else {
        return Ok(None);
    };
    cfg.timeout()?;
    let judge = |database: Option<String>| {
        let flags = Flags {
            database,
            ..Flags::default()
        };
        resolve::resolve(&flags, &Env::default(), Some(&cfg.connection))
    };
    match judge(None) {
        Err(ResolveError::NoDatabase) => judge(Some("postgres".into())),
        resolved => resolved,
    }?;
    Ok(Some(cfg))
}

fn config_file(project: &impl Vfs) -> String {
    match checked(project) {
        Ok(None) => "absent".to_string(),
        Ok(Some(_)) => "ok".to_string(),
        Err(e) => format!("error: {}", e.text()),
    }
}

pub(crate) fn run(a: &InfoArgs, env: &Env) -> Result<()> {
    let root = a.path.clone().unwrap_or_else(|| PathBuf::from("."));
    let fs = crate::vfs::OsFs::project(&root)?;
    let project = walk::scan(&fs, walk::max_size(env)?).map_err(walk::scan_failed(None))?;
    metadata::scan(&project.sources)
        .readable()
        .map_err(walk::scan_failed(None))?;
    let shown = std::path::absolute(&root).unwrap_or_else(|_| root.clone());
    let detected = scaffolded(&fs);
    let config_file = config_file(&fs);
    let mut dirs: BTreeMap<String, usize> = BTreeMap::new();
    let paths = project
        .sources
        .iter()
        .map(|f| &f.path)
        .chain(project.test_dir_files.iter());
    for p in paths.clone() {
        *dirs.entry(directory(p)).or_default() += 1;
    }
    let info = Info {
        path: shown.display().to_string(),
        template: detected
            .as_ref()
            .map_or_else(|| "unknown".to_string(), |d| d.0.clone()),
        deploy_sql: root.join("deploy.sql").is_file(),
        config_file,
        total_files: paths.clone().count(),
        sql_files: paths.clone().filter(|p| walk::is_sql(p)).count(),
        test_files: project.test_dir_files.len(),
        metadata_with: project
            .sources
            .iter()
            .chain(project.tests.iter())
            .filter(|f| walk::is_sql(&f.path) && !metadata::blocks(&f.content).is_empty())
            .count(),
        scaffolded: detected.map(|d| d.1),
        directories: dirs.iter().map(|(k, v)| (k.clone(), (*v).into())).collect(),
    };
    if a.json {
        return json(&info);
    }
    let mut text = format!("Project: {}\nTemplate: {}\n", info.path, info.template);
    if let Some(s) = &info.scaffolded {
        let _ = writeln!(text, "Scaffolded by: {s}");
    }
    text.push_str(if info.deploy_sql {
        "deploy.sql: ok present\n"
    } else {
        "deploy.sql: FAILED missing\n"
    });
    let _ = write!(
        text,
        "pgmi.yaml: {}\n\nFiles\n  Total:    {}\n  SQL:      {}\n  Tests:    {}\n  Metadata: {} / {}\n\n",
        info.config_file,
        info.total_files,
        info.sql_files,
        info.test_files,
        info.metadata_with,
        info.total_files
    );
    if !dirs.is_empty() {
        text.push_str("Directories\n");
    }
    for (dir, n) in &dirs {
        let _ = writeln!(text, "  {dir:<30} {n} files");
    }
    stdout(&text)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vfs::MemFs;

    #[test]
    fn pgmi_yaml_is_ok_only_when_deploy_would_accept_it() {
        assert_eq!(config_file(&MemFs::default()), "absent");
        for good in [
            "",
            "connection:\n  host: h\n  port: 5433\n",
            "timeout: 90s\n",
        ] {
            assert_eq!(
                config_file(&MemFs::default().file("pgmi.yaml", good)),
                "ok",
                "{good:?}"
            );
        }
        for (bad, says) in [
            ("connection:\n  port: abc\n", "connection.port"),
            ("connection:\n  port: 0\n", "connection.port"),
            ("connection:\n  sslmode: bogus\n", "verify-full"),
            ("timeout: 1d\n", "timeout"),
        ] {
            let status = config_file(&MemFs::default().file("pgmi.yaml", bad));
            assert!(
                status.starts_with("error: ") && status.contains(says),
                "{bad:?}: {status}"
            );
        }
    }
}
