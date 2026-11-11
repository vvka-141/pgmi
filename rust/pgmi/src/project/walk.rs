use super::ignore::Rules;
use crate::error::{Error, Kind, Result};
use crate::vfs::{Kind as FileKind, Vfs};

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

fn rules(fs: &impl Vfs) -> Result<Rules> {
    match fs.read(".pgmiignore") {
        Ok(bytes) => Rules::parse(&String::from_utf8_lossy(&bytes)).map_err(Error::config),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Rules::default()),
        Err(e) => Err(Error::config(format!(
            "could not read {}: {e}",
            fs.display(".pgmiignore")
        ))),
    }
}

pub fn scan(fs: &impl Vfs, max_size: u64) -> Result<Project> {
    let rules = rules(fs)?;
    let mut project = Project::default();
    walk(fs, "", false, &rules, max_size, &mut project)?;
    project.ignored.sort_by(|a, b| a.path.cmp(&b.path));
    Ok(project)
}

pub fn load(fs: &impl Vfs, max_size: u64) -> Result<Project> {
    let rules = rules(fs)?;
    if rules.ignored("deploy.sql", false) {
        return Err(Error::config(
            ".pgmiignore hides deploy.sql; add !deploy.sql after the pattern that matches it",
        ));
    }
    if !fs
        .stat("deploy.sql")
        .is_ok_and(|s| s.kind == FileKind::File)
    {
        return Err(Error::new(
            Kind::NoDeploySql,
            format!("deploy.sql not found in {}", fs.display("")),
        ));
    }
    let mut project = Project {
        deploy_sql: read_text(fs, "deploy.sql", max_size)?,
        ..Project::default()
    };
    walk(fs, "", false, &rules, max_size, &mut project)?;
    project.ignored.sort_by(|a, b| a.path.cmp(&b.path));
    Ok(project)
}

fn read_text(fs: &impl Vfs, rel: &str, max_size: u64) -> Result<String> {
    let stat = fs
        .stat(rel)
        .map_err(|e| Error::general(format!("read {rel}")).with_source(e))?;
    if stat.len > max_size {
        let name = rel.rsplit('/').next().unwrap_or(rel);
        return Err(Error::config(format!(
            "failed to read ./{rel}: file {name} is {} bytes, exceeds {max_size}-byte limit (override via PGMI_MAX_FILE_SIZE)",
            stat.len
        )));
    }
    let bytes = fs
        .read(rel)
        .map_err(|e| Error::general(format!("read {rel}")).with_source(e))?;
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
    fs: &impl Vfs,
    rel: &str,
    in_test: bool,
    rules: &Rules,
    max_size: u64,
    out: &mut Project,
) -> Result<()> {
    let entries = fs.read_dir(rel).map_err(|e| {
        Error::general(format!("read directory {}", fs.display(rel))).with_source(e)
    })?;
    for entry in entries {
        let name = entry.name;
        let child = if rel.is_empty() {
            name.clone()
        } else {
            format!("{rel}/{name}")
        };
        let is_dir = entry.stat.kind == FileKind::Dir;
        if !entry.utf8 {
            if name.starts_with('.') || rules.ignored(&child, is_dir) {
                continue;
            }
            return Err(Error::config(format!(
                "the name of ./{child} is not valid UTF-8; rename it, or hide it in .pgmiignore"
            )));
        }
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
        if entry.stat.link {
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
            walk(fs, &child, test, rules, max_size, out)?;
            continue;
        }
        if in_test {
            out.test_dir_files.push(format!("./{child}"));
            if is_sql(&name) {
                let content = read_text(fs, &child, max_size)?;
                out.tests.push(File {
                    path: format!("./{child}"),
                    content,
                });
            }
            continue;
        }
        let content = read_text(fs, &child, max_size)?;
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
    use crate::vfs::{EmbeddedFs, MemFs, OsFs};

    const MAX: u64 = DEFAULT_MAX_FILE_SIZE;

    fn project() -> MemFs {
        MemFs::default().file("deploy.sql", "SELECT 1;")
    }

    fn paths(files: &[File]) -> Vec<&str> {
        files.iter().map(|f| f.path.as_str()).collect()
    }

    fn refused(fs: &MemFs) -> String {
        let e = load(fs, MAX).unwrap_err();
        assert_eq!(e.exit_code(), 10, "{}", e.text());
        e.text()
    }

    #[test]
    fn sources_tests_and_test_files_are_split_in_path_order() {
        let fs = project()
            .file("b/2.sql", "SELECT 2;")
            .file("a.sql", "SELECT 1;")
            .file("b/__test__/t.sql", "SELECT 3;")
            .file("b/__test__/notes.md", "n")
            .file("README.md", "r");
        let p = load(&fs, MAX).unwrap();
        assert_eq!(p.deploy_sql, "SELECT 1;");
        assert_eq!(paths(&p.sources), ["./README.md", "./a.sql", "./b/2.sql"]);
        assert_eq!(paths(&p.tests), ["./b/__test__/t.sql"]);
        assert_eq!(
            p.test_dir_files,
            ["./b/__test__/notes.md", "./b/__test__/t.sql"]
        );
    }

    #[test]
    fn hidden_and_tool_directories_are_ignored_built_in() {
        let fs = project()
            .file(".git/config", "x")
            .file("node_modules/m.js", "x")
            .file("__pycache__/c.pyc", "x")
            .file("keep.sql", "SELECT 1;");
        let p = load(&fs, MAX).unwrap();
        assert_eq!(paths(&p.sources), ["./keep.sql"]);
        let ignored: Vec<_> = p
            .ignored
            .iter()
            .map(|i| (i.path.as_str(), i.rule))
            .collect();
        assert_eq!(
            ignored,
            [
                ("./.git/", "built-in"),
                ("./__pycache__/", "built-in"),
                ("./node_modules/", "built-in")
            ]
        );
    }

    #[test]
    fn pgmiignore_hides_files_and_is_read_lossily() {
        let fs = project()
            .file(".pgmiignore", b"# caf\xe9\nskip.sql\ndocs/\n")
            .file("a.sql", "SELECT 1;")
            .file("skip.sql", "SELECT 1;")
            .file("docs/x.md", "x");
        let p = load(&fs, MAX).unwrap();
        assert_eq!(paths(&p.sources), ["./a.sql"]);
        let rules: Vec<_> = p
            .ignored
            .iter()
            .map(|i| (i.path.as_str(), i.rule))
            .collect();
        assert!(rules.contains(&("./skip.sql", ".pgmiignore")));
        assert!(rules.contains(&("./docs/", ".pgmiignore")));
    }

    #[test]
    fn an_unreadable_pgmiignore_refuses() {
        let text = refused(&project().dir(".pgmiignore"));
        assert!(text.contains(".pgmiignore"), "{text}");
    }

    #[test]
    fn pgmiignore_may_not_hide_deploy_sql() {
        let text = refused(&project().file(".pgmiignore", "*.sql\n"));
        assert!(text.contains("!deploy.sql"), "{text}");
    }

    #[test]
    fn deploy_sql_missing_or_a_directory_is_exit_14() {
        for fs in [MemFs::default(), MemFs::default().dir("deploy.sql")] {
            let e = load(&fs, MAX).unwrap_err();
            assert_eq!(e.exit_code(), 14, "{}", e.text());
        }
        assert!(scan(&MemFs::default(), MAX).is_ok());
    }

    #[test]
    fn symlinks_are_refused_unless_hidden() {
        for fs in [
            project().link_to_file("l.sql"),
            project().link_to_dir("l"),
            project().dangling_link("l"),
        ] {
            assert!(refused(&fs).contains("symlink"));
        }
        let fs = project().link_to_dir("l").file(".pgmiignore", "l/\n");
        assert!(load(&fs, MAX).unwrap().sources.is_empty());
    }

    #[test]
    fn only_test_directories_may_use_dunder_names() {
        assert!(refused(&project().file("__init__/x.sql", "SELECT 1;")).contains("__init__"));
        let fs = project().file("__tests__/__deep__/x.sql", "SELECT 1;");
        assert_eq!(
            paths(&load(&fs, MAX).unwrap().tests),
            ["./__tests__/__deep__/x.sql"]
        );
    }

    #[test]
    fn text_rules_bom_nul_utf8_and_size() {
        let fs = project().file("a.sql", b"\xef\xbb\xbfSELECT 1;");
        assert_eq!(load(&fs, MAX).unwrap().sources[0].content, "SELECT 1;");
        assert!(refused(&project().file("a.sql", b"SELECT\0")).contains("NUL byte at offset 6"));
        assert!(refused(&project().file("a.sql", b"SELECT \xff")).contains("byte offset 7"));
        let big = project().file("a.sql", "x".repeat(11));
        let e = load(&big, 10).unwrap_err();
        assert!(e.text().contains("exceeds 10-byte limit"), "{}", e.text());
        assert!(load(&big, 11).is_ok());
    }

    #[test]
    fn non_text_files_in_test_directories_are_listed_not_read() {
        let fs = project().file("__test__/blob.bin", b"\0\0");
        let p = load(&fs, MAX).unwrap();
        assert!(p.tests.is_empty());
        assert_eq!(p.test_dir_files, ["./__test__/blob.bin"]);
    }

    #[test]
    fn every_embedded_template_loads_in_memory() {
        for name in ["basic", "advanced", "minimal"] {
            let fs = EmbeddedFs::new(&format!("templates/{name}"));
            let p = load(&fs, MAX).unwrap_or_else(|e| panic!("{name}: {}", e.text()));
            assert!(!p.deploy_sql.is_empty(), "{name}");
            super::super::metadata::collect(&p.sources)
                .unwrap_or_else(|e| panic!("{name}: {}", e.text()));
        }
    }

    #[cfg(unix)]
    #[test]
    fn non_utf8_file_name_is_refused_unless_hidden() {
        use std::os::unix::ffi::OsStrExt;
        let os_project = |tag: &str, files: &[(&std::ffi::OsStr, &[u8])]| {
            let d = std::env::temp_dir().join(format!("pgmi-walk-{tag}-{}", std::process::id()));
            let _ = std::fs::remove_dir_all(&d);
            std::fs::create_dir_all(&d).unwrap();
            std::fs::write(d.join("deploy.sql"), "SELECT 1;").unwrap();
            for (name, body) in files {
                std::fs::write(d.join(name), body).unwrap();
            }
            OsFs::new(d)
        };
        let bad = std::ffi::OsStr::from_bytes(b"caf\xe9.sql");
        let e = load(&os_project("bad", &[(bad, b"SELECT 1;")]), MAX).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("./caf\u{fffd}.sql"), "{}", e.text());
        let fs = os_project(
            "hidden",
            &[
                (bad, b"SELECT 1;"),
                (std::ffi::OsStr::new(".pgmiignore"), b"caf*.sql\n"),
            ],
        );
        assert!(load(&fs, MAX).unwrap().sources.is_empty());
    }

    #[test]
    fn sql_extensions() {
        assert!(is_sql("a.SQL"));
        assert!(is_sql("x.plpgsql"));
        assert!(!is_sql("a.txt"));
    }
}
