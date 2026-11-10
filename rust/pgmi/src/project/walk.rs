use std::path::{Path, PathBuf};

use super::ignore::Rules;
use crate::error::{Error, Kind, Result};

pub const DEFAULT_MAX_FILE_SIZE: u64 = 10 * 1024 * 1024;

#[derive(Debug, Clone)]
pub struct File {
    pub path: String,
    pub content: String,
}

#[derive(Debug, Clone)]
pub struct Ignored {
    pub path: String,
    pub rule: &'static str,
}

#[derive(Debug, Default)]
pub struct Project {
    pub deploy_sql: String,
    pub sources: Vec<File>,
    pub tests: Vec<File>,
    pub test_dir_files: usize,
    pub ignored: Vec<Ignored>,
}

pub fn is_sql(name: &str) -> bool {
    let lower = name.to_ascii_lowercase();
    [
        ".sql", ".ddl", ".dml", ".dql", ".dcl", ".psql", ".pgsql", ".plpgsql",
    ]
    .iter()
    .any(|e| lower.ends_with(e))
}

pub fn check_root(root: &Path) -> Result<()> {
    let display = root.display();
    match std::fs::metadata(root) {
        Err(_) => Err(Error::config(format!(
            "project path \"{display}\" does not exist"
        ))),
        Ok(m) if !m.is_dir() => Err(Error::config(format!(
            "\"{display}\" is a file; pass the project directory"
        ))),
        Ok(_) => Ok(()),
    }
}

pub fn load(root: &Path, max_size: u64) -> Result<Project> {
    check_root(root)?;
    let rules = match std::fs::read_to_string(root.join(".pgmiignore")) {
        Ok(text) => Rules::parse(&text).map_err(Error::config)?,
        Err(_) => Rules::default(),
    };
    if rules.ignored("deploy.sql", false) {
        return Err(Error::config(
            ".pgmiignore hides deploy.sql; add !deploy.sql after the pattern that matches it",
        ));
    }
    let deploy_path = root.join("deploy.sql");
    if !deploy_path.is_file() {
        return Err(Error::new(
            Kind::NoDeploySql,
            format!("deploy.sql not found in {}", root.display()),
        ));
    }
    let mut project = Project {
        deploy_sql: read_text(&deploy_path, "deploy.sql", max_size)?,
        ..Project::default()
    };
    walk(root, "", false, &rules, max_size, &mut project)?;
    project.ignored.sort_by(|a, b| a.path.cmp(&b.path));
    Ok(project)
}

fn read_text(path: &Path, rel: &str, max_size: u64) -> Result<String> {
    let meta = std::fs::metadata(path)
        .map_err(|e| Error::general(format!("read {rel}")).with_source(e))?;
    if meta.len() > max_size {
        return Err(Error::config(format!(
            "file ./{rel} is {} bytes, over the {max_size}-byte limit (PGMI_MAX_FILE_SIZE)",
            meta.len()
        )));
    }
    let bytes =
        std::fs::read(path).map_err(|e| Error::general(format!("read {rel}")).with_source(e))?;
    let bytes = bytes.strip_prefix(b"\xef\xbb\xbf").unwrap_or(&bytes);
    if let Some(at) = bytes.iter().position(|b| *b == 0) {
        return Err(Error::config(format!(
            "file ./{rel} has a NUL byte at offset {at}, so it is not text; exclude it in .pgmiignore or move it out of the project path"
        )));
    }
    String::from_utf8(bytes.to_vec()).map_err(|e| {
        let at = e.utf8_error().valid_up_to();
        if rel == "deploy.sql" {
            Error::config(format!("deploy.sql is not valid UTF-8 at byte offset {at}"))
        } else {
            Error::config(format!(
                "file ./{rel} is not valid UTF-8 (byte offset {at}); exclude it in .pgmiignore or move it out of the project path"
            ))
        }
    })
}

fn walk(
    dir: &Path,
    rel: &str,
    in_test: bool,
    rules: &Rules,
    max_size: u64,
    out: &mut Project,
) -> Result<()> {
    let mut entries: Vec<(String, PathBuf)> = std::fs::read_dir(dir)
        .map_err(|e| Error::general(format!("read directory {}", dir.display())).with_source(e))?
        .filter_map(|e| e.ok())
        .filter_map(|e| e.file_name().into_string().ok().map(|n| (n, e.path())))
        .collect();
    entries.sort();
    for (name, path) in entries {
        let child = if rel.is_empty() {
            name.clone()
        } else {
            format!("{rel}/{name}")
        };
        let Ok(link) = std::fs::symlink_metadata(&path) else {
            continue;
        };
        let is_link = link.file_type().is_symlink();
        let is_dir = if is_link {
            std::fs::metadata(&path).is_ok_and(|m| m.is_dir())
        } else {
            link.is_dir()
        };
        let shown = if is_dir {
            format!("./{child}/")
        } else {
            format!("./{child}")
        };
        if rel.is_empty() && name == "deploy.sql" && !is_dir {
            continue;
        }
        if name.starts_with('.') || (is_dir && (name == "node_modules" || name == "__pycache__")) {
            out.ignored.push(Ignored {
                path: shown,
                rule: "built-in",
            });
            continue;
        }
        if rules.ignored(&child, is_dir) {
            out.ignored.push(Ignored {
                path: shown,
                rule: ".pgmiignore",
            });
            continue;
        }
        if is_link {
            return Err(Error::config(format!(
                "./{child} is a symlink; pgmi does not follow symbolic links, so hide it in .pgmiignore or replace it with the file"
            )));
        }
        if is_dir {
            let test = in_test || name == "__test__" || name == "__tests__";
            walk(&path, &child, test, rules, max_size, out)?;
            continue;
        }
        if in_test {
            out.test_dir_files += 1;
            if is_sql(&name) {
                let content = read_text(&path, &child, max_size)?;
                out.tests.push(File {
                    path: format!("./{child}"),
                    content,
                });
            }
            continue;
        }
        let content = read_text(&path, &child, max_size)?;
        out.sources.push(File {
            path: format!("./{child}"),
            content,
        });
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sql_extensions() {
        assert!(is_sql("a.SQL"));
        assert!(is_sql("x.plpgsql"));
        assert!(!is_sql("a.txt"));
    }
}
