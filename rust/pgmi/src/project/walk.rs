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
    pub test_dir_files: Vec<String>,
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

pub fn max_size(env: &crate::env::Env) -> Result<u64> {
    match env.non_empty("PGMI_MAX_FILE_SIZE")? {
        None => Ok(DEFAULT_MAX_FILE_SIZE),
        Some(v) => v.parse().ok().filter(|n| *n > 0).ok_or_else(|| {
            Error::config(format!(
                "PGMI_MAX_FILE_SIZE={v:?}: want a positive whole number of bytes, such as 20971520"
            ))
        }),
    }
}

fn rules(root: &Path) -> Result<Rules> {
    let path = root.join(".pgmiignore");
    match std::fs::read(&path) {
        Ok(bytes) => Rules::parse(&String::from_utf8_lossy(&bytes)).map_err(Error::config),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Rules::default()),
        Err(e) => Err(Error::config(format!(
            "could not read {}: {e}",
            path.display()
        ))),
    }
}

pub fn scan(root: &Path, max_size: u64) -> Result<Project> {
    check_root(root)?;
    let rules = rules(root)?;
    let mut project = Project::default();
    walk(root, "", false, &rules, max_size, &mut project)?;
    project.ignored.sort_by(|a, b| a.path.cmp(&b.path));
    Ok(project)
}

pub fn load(root: &Path, max_size: u64) -> Result<Project> {
    check_root(root)?;
    let rules = rules(root)?;
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
        let name = rel.rsplit('/').next().unwrap_or(rel);
        return Err(Error::config(format!(
            "failed to read ./{rel}: file {name} is {} bytes, exceeds {max_size}-byte limit (override via PGMI_MAX_FILE_SIZE)",
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
    let mut entries: Vec<(std::result::Result<String, String>, PathBuf)> = std::fs::read_dir(dir)
        .map_err(|e| Error::general(format!("read directory {}", dir.display())).with_source(e))?
        .filter_map(|e| e.ok())
        .map(|e| {
            let name = e
                .file_name()
                .into_string()
                .map_err(|n| n.to_string_lossy().into_owned());
            (name, e.path())
        })
        .collect();
    entries.sort();
    for (name, path) in entries {
        let join = |name: &str| {
            if rel.is_empty() {
                name.to_string()
            } else {
                format!("{rel}/{name}")
            }
        };
        let name = match name {
            Ok(n) => n,
            Err(lossy) => {
                let child = join(&lossy);
                if lossy.starts_with('.') || rules.ignored(&child, path.is_dir()) {
                    continue;
                }
                return Err(Error::config(format!(
                    "the name of ./{child} is not valid UTF-8; rename it, or hide it in .pgmiignore"
                )));
            }
        };
        let child = join(&name);
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
            if !test && name.len() > 4 && name.starts_with("__") && name.ends_with("__") {
                return Err(Error::config(format!(
                    "unsupported directory {name:?} in path \"./{child}/\"; only __test__ and __tests__ are allowed"
                )));
            }
            walk(&path, &child, test, rules, max_size, out)?;
            continue;
        }
        if in_test {
            out.test_dir_files.push(format!("./{child}"));
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

    fn project(tag: &str, files: &[(&std::ffi::OsStr, &[u8])]) -> PathBuf {
        let d = std::env::temp_dir().join(format!("pgmi-walk-{tag}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        std::fs::write(d.join("deploy.sql"), "SELECT 1;").unwrap();
        for (name, body) in files {
            std::fs::write(d.join(name), body).unwrap();
        }
        d
    }

    #[test]
    fn pgmiignore_is_read_lossily_and_read_errors_refuse() {
        let os = std::ffi::OsStr::new;
        let d = project(
            "lossy",
            &[
                (os(".pgmiignore"), b"# caf\xe9\nskip.sql\n"),
                (os("a.sql"), b"SELECT 1;"),
                (os("skip.sql"), b"SELECT 1;"),
            ],
        );
        let p = load(&d, DEFAULT_MAX_FILE_SIZE).unwrap();
        let paths: Vec<_> = p.sources.iter().map(|f| f.path.as_str()).collect();
        assert_eq!(paths, ["./a.sql"]);
        let d = project("dir", &[]);
        std::fs::create_dir(d.join(".pgmiignore")).unwrap();
        let e = load(&d, DEFAULT_MAX_FILE_SIZE).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains(".pgmiignore"), "{}", e.text());
    }

    #[cfg(unix)]
    #[test]
    fn non_utf8_file_name_is_refused_unless_hidden() {
        use std::os::unix::ffi::OsStrExt;
        let bad = std::ffi::OsStr::from_bytes(b"caf\xe9.sql");
        let d = project("bad", &[(bad, b"SELECT 1;")]);
        let e = load(&d, DEFAULT_MAX_FILE_SIZE).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("./caf\u{fffd}.sql"), "{}", e.text());
        let d = project(
            "hidden",
            &[
                (bad, b"SELECT 1;"),
                (std::ffi::OsStr::new(".pgmiignore"), b"caf*.sql\n"),
            ],
        );
        assert!(load(&d, DEFAULT_MAX_FILE_SIZE).unwrap().sources.is_empty());
    }

    #[test]
    fn sql_extensions() {
        assert!(is_sql("a.SQL"));
        assert!(is_sql("x.plpgsql"));
        assert!(!is_sql("a.txt"));
    }
}
