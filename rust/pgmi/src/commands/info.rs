use std::collections::BTreeMap;
use std::path::PathBuf;

use serde::Serialize;

use super::{json, stdout};
use crate::cli::InfoArgs;
use crate::config::yaml::ProjectConfig;
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

pub(crate) fn run(a: &InfoArgs, env: &Env) -> Result<()> {
    let root = a.path.clone().unwrap_or_else(|| PathBuf::from("."));
    let fs = crate::vfs::OsFs::project(&root)?;
    let project = walk::scan(&fs, walk::max_size(env)?)?;
    let shown = std::path::absolute(&root).unwrap_or_else(|_| root.clone());
    let detected = scaffolded(&fs);
    let config_file = match ProjectConfig::load(&fs) {
        Ok(None) => "absent".to_string(),
        Ok(Some(_)) => "ok".to_string(),
        Err(e) => format!("error: {}", e.text()),
    };
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
        text.push_str(&format!("Scaffolded by: {s}\n"));
    }
    text.push_str(if info.deploy_sql {
        "deploy.sql: ok present\n"
    } else {
        "deploy.sql: FAILED missing\n"
    });
    text.push_str(&format!(
        "pgmi.yaml: {}\n\nFiles\n  Total:    {}\n  SQL:      {}\n  Tests:    {}\n  Metadata: {} / {}\n\nDirectories\n",
        info.config_file,
        info.total_files,
        info.sql_files,
        info.test_files,
        info.metadata_with,
        info.total_files
    ));
    for (dir, n) in &dirs {
        text.push_str(&format!("  {dir:<30} {n} files\n"));
    }
    stdout(&text)
}
