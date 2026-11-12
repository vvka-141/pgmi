use super::ignore::Rules;
use crate::error::{Error, Kind, Result};
use crate::vfs::{FileKind, NOT_REGULAR, Name, Vfs};

pub(crate) const DEFAULT_MAX_FILE_SIZE: u64 = 10 * 1024 * 1024;

#[derive(Debug, Clone)]
pub(crate) struct File {
    pub(crate) path: String,
    pub(crate) content: String,
}

#[derive(Debug, Clone, serde::Serialize)]
pub(crate) struct Ignored {
    pub(crate) path: String,
    pub(crate) rule: &'static str,
}

#[derive(Debug, Default)]
pub(crate) struct Project {
    pub(crate) deploy_sql: String,
    pub(crate) sources: Vec<File>,
    pub(crate) tests: Vec<File>,
    pub(crate) test_dir_files: Vec<String>,
    pub(crate) ignored: Vec<Ignored>,
}

pub(crate) fn is_sql(name: &str) -> bool {
    let lower = name.to_ascii_lowercase();
    [
        ".sql", ".ddl", ".dml", ".dql", ".dcl", ".psql", ".pgsql", ".plpgsql",
    ]
    .iter()
    .any(|e| lower.ends_with(e))
}

pub(crate) fn max_size(env: &crate::env::Env) -> Result<u64> {
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
    let rules = match fs.read(".pgmiignore") {
        Ok(bytes) => Rules::parse(&String::from_utf8_lossy(&bytes)).map_err(Error::config)?,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Rules::default(),
        Err(e) => {
            return Err(Error::config(format!(
                "could not read {}: {e}",
                fs.display(".pgmiignore")
            )));
        }
    };
    if rules.ignored("deploy.sql", false) {
        return Err(Error::config(
            ".pgmiignore excludes deploy.sql; pgmi cannot deploy without it. Remove the pattern, or add !deploy.sql after it",
        ));
    }
    Ok(rules)
}

/// Prefixes a refusal met while scanning the project as Go does:
/// `failed to scan directory "<path>"` in deploy, without the path elsewhere.
pub(crate) fn scan_failed(path: Option<&str>) -> impl Fn(Error) -> Error + use<> {
    let message = path.map_or_else(
        || "failed to scan directory".to_string(),
        |p| format!("failed to scan directory \"{p}\""),
    );
    move |e| Error::new(e.kind(), message.clone()).with_source(e)
}

pub(crate) fn scan(fs: &impl Vfs, max_size: u64) -> Result<Project> {
    let rules = rules(fs)?;
    let mut project = Project::default();
    walk(fs, "", false, &rules, max_size, &mut project)?;
    project.ignored.sort_by(|a, b| a.path.cmp(&b.path));
    Ok(project)
}

pub(crate) fn load(fs: &impl Vfs, max_size: u64) -> Result<Project> {
    let scanning = scan_failed(Some(&fs.display("")));
    let rules = rules(fs).map_err(&scanning)?;
    let deploy = fs.stat("deploy.sql");
    if deploy
        .as_ref()
        .is_ok_and(|s| s.kind == FileKind::Other && !s.link)
    {
        return Err(Error::config(format!(
            "{} is {NOT_REGULAR}",
            fs.display("deploy.sql")
        )));
    }
    if !deploy.is_ok_and(|s| s.kind == FileKind::File) {
        return Err(Error::new(
            Kind::NoDeploySql,
            format!(
                "deploy.sql not found in {}\nexpected: {} — run `pgmi init` to scaffold one",
                fs.display(""),
                fs.display("deploy.sql")
            ),
        ));
    }
    let mut project = Project {
        deploy_sql: read_text(fs, "deploy.sql", max_size)?,
        ..Project::default()
    };
    walk(fs, "", false, &rules, max_size, &mut project).map_err(scanning)?;
    project.ignored.sort_by(|a, b| a.path.cmp(&b.path));
    Ok(project)
}

fn read_text(fs: &impl Vfs, rel: &str, max_size: u64) -> Result<String> {
    let unreadable = |e: std::io::Error| {
        let why = match e.kind() {
            std::io::ErrorKind::PermissionDenied => "permission denied".to_string(),
            _ => e.to_string(),
        };
        Error::config(format!(
            "failed to read ./{rel}: cannot read {rel}: {why}; exclude it in .pgmiignore or make it readable"
        ))
    };
    let too_big = |len: u64| {
        let name = rel.rsplit('/').next().unwrap_or(rel);
        Error::config(format!(
            "failed to read ./{rel}: file {name} is {len} bytes, exceeds {max_size}-byte limit (override via PGMI_MAX_FILE_SIZE)"
        ))
    };
    let stat = fs.stat(rel).map_err(unreadable)?;
    if stat.len > max_size {
        return Err(too_big(stat.len));
    }
    let mut bytes = fs.read_bounded(rel, max_size).map_err(unreadable)?;
    if bytes.len() as u64 > max_size {
        return Err(too_big(
            fs.stat(rel)
                .map_or(stat.len, |s| s.len.max(bytes.len() as u64)),
        ));
    }
    if bytes.starts_with(b"\xef\xbb\xbf") {
        bytes.drain(..3);
    }
    if bytes.contains(&0) {
        return Err(Error::config(format!(
            "file ./{rel} contains a NUL byte; pgmi loads project files as text, so exclude it in .pgmiignore or move it out of the project path"
        )));
    }
    String::from_utf8(bytes).map_err(|e| {
        let at = e.utf8_error().valid_up_to();
        if rel == "deploy.sql" {
            Error::config(format!("deploy.sql is not valid UTF-8 (first invalid byte at offset {at}); re-save the file as UTF-8 without BOM"))
        } else {
            Error::config(format!(
                "file ./{rel} is not valid UTF-8; pgmi loads project files as text, so exclude it in .pgmiignore or move it out of the project path"
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
        Error::config(format!(
            "cannot read {}: {e}; exclude it in .pgmiignore or make it readable",
            fs.display(rel)
        ))
    })?;
    for entry in entries {
        let is_dir = entry.stat.kind == FileKind::Dir;
        let join = |name: &str| {
            if rel.is_empty() {
                name.to_string()
            } else {
                format!("{rel}/{name}")
            }
        };
        let (name, child) = match entry.name {
            Name::Utf8(name) => {
                let child = join(&name);
                (name, child)
            }
            Name::Lossy(name) => {
                let child = join(&name);
                if name.starts_with('.') || rules.ignored(&child, is_dir) {
                    continue;
                }
                return Err(Error::config(format!(
                    "the name of ./{child} is not valid UTF-8; rename it, or hide it in .pgmiignore"
                )));
            }
        };
        let shown = if is_dir {
            format!("./{child}/")
        } else {
            format!("./{child}")
        };
        if rel.is_empty() && name.eq_ignore_ascii_case("deploy.sql") && !is_dir {
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
                "failed to read ./{child}: refusing to read symlink {child}: pgmi does not follow symlinks; exclude it in .pgmiignore or replace it with the file"
            )));
        }
        if entry.stat.kind == FileKind::Other {
            return Err(Error::config(format!(
                "failed to read ./{child}: refusing to read {child}: {NOT_REGULAR}; exclude it in .pgmiignore or remove it"
            )));
        }
        if is_dir {
            let test = in_test || name == "__test__" || name == "__tests__";
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
        if let Some(dir) = unsupported_dunder(&child) {
            return Err(Error::config(format!(
                "unsupported directory \"{dir}\" in path \"./{child}\"; only __test__ and __tests__ are allowed"
            )));
        }
        out.sources.push(File {
            path: format!("./{child}"),
            content,
        });
    }
    Ok(())
}

fn unsupported_dunder(path: &str) -> Option<&str> {
    let (dirs, _) = path.rsplit_once('/')?;
    dirs.split('/').find(|d| {
        d.len() > 4
            && d.starts_with("__")
            && d.ends_with("__")
            && !matches!(*d, "__test__" | "__tests__")
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vfs::{EmbeddedFs, MemFs};

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
        for pattern in ["deploy.sql", "*.sql", "*"] {
            let text = refused(&project().file(".pgmiignore", format!("{pattern}\n")));
            assert!(text.contains("!deploy.sql"), "{pattern}: {text}");
        }
    }

    #[test]
    fn a_scan_refuses_a_pgmiignore_hiding_deploy_sql_as_a_load_does() {
        let e = scan(&project().file(".pgmiignore", "*.sql\n"), MAX).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert_eq!(
            e.text(),
            ".pgmiignore excludes deploy.sql; pgmi cannot deploy without it. Remove the pattern, or add !deploy.sql after it"
        );
    }

    #[test]
    fn pgmiignore_may_re_include_deploy_sql() {
        let fs = project()
            .file("scratch.sql", "SELECT 1;")
            .file(".pgmiignore", "*.sql\n!deploy.sql\n");
        let p = load(&fs, MAX).unwrap();
        assert_eq!(p.deploy_sql, "SELECT 1;");
        assert!(p.sources.is_empty(), "{:?}", paths(&p.sources));
    }

    #[test]
    fn an_ignored_directory_is_not_read() {
        let fs = project()
            .file("clients/go/bin/tool", b"\0\x01 not text")
            .unreadable_dir("clients/locked")
            .file(".pgmiignore", "clients/\n");
        let p = load(&fs, MAX).unwrap();
        assert!(p.sources.is_empty());
        let clients: Vec<_> = p
            .ignored
            .iter()
            .filter(|i| i.path.starts_with("./clients"))
            .map(|i| (i.path.as_str(), i.rule))
            .collect();
        assert_eq!(clients, [("./clients/", ".pgmiignore")]);
    }

    #[test]
    fn built_in_exclusions_are_not_read_and_apply_at_any_depth() {
        let fs = project()
            .file("migrations/001_users.sql", "CREATE TABLE users (id int);")
            .file("__test__/test_users.sql", "SELECT 1;")
            .file(".git/index", b"DIRC\0\0\0\x02binary")
            .unreadable_file(".git/locked")
            .file(".venv/lib/site.py", "import sys")
            .file("node_modules/pkg/index.js", "module.exports = {};")
            .file("api/__pycache__/gen.cpython-312.pyc", b"\x03\xf3\r\n")
            .file("api/handlers.sql", "SELECT 1;");
        let p = load(&fs, MAX).unwrap();
        assert_eq!(
            paths(&p.sources),
            ["./api/handlers.sql", "./migrations/001_users.sql"]
        );
        assert_eq!(paths(&p.tests), ["./__test__/test_users.sql"]);
        let ignored: Vec<_> = p
            .ignored
            .iter()
            .map(|i| (i.path.as_str(), i.rule))
            .collect();
        assert_eq!(
            ignored,
            [
                ("./.git/", "built-in"),
                ("./.venv/", "built-in"),
                ("./api/__pycache__/", "built-in"),
                ("./node_modules/", "built-in"),
            ]
        );
    }

    #[test]
    fn a_project_with_only_deploy_sql_has_no_sources() {
        let p = load(&project(), MAX).unwrap();
        assert!(p.sources.is_empty() && p.tests.is_empty() && p.ignored.is_empty());
    }

    #[test]
    fn the_root_deploy_file_is_never_a_source_whatever_its_case() {
        let fs = project()
            .file("Deploy.sql", "SELECT 2;")
            .file("lib/Deploy.sql", "SELECT 3;");
        let p = load(&fs, MAX).unwrap();
        assert_eq!(paths(&p.sources), ["./lib/Deploy.sql"]);
    }

    #[test]
    fn root_level_test_files_keep_their_path() {
        let fs = project()
            .file("migrations/001_users.sql", "CREATE TABLE users();")
            .file("__test__/00_fixture.sql", "INSERT INTO users VALUES (1);")
            .file("__test__/test_users.sql", "SELECT * FROM users;");
        let p = load(&fs, MAX).unwrap();
        assert_eq!(paths(&p.sources), ["./migrations/001_users.sql"]);
        assert_eq!(
            paths(&p.tests),
            ["./__test__/00_fixture.sql", "./__test__/test_users.sql"]
        );
    }

    #[test]
    fn deploy_sql_loses_its_bom_and_must_be_utf8() {
        let fs = MemFs::default().file("deploy.sql", b"\xef\xbb\xbfSELECT 1;");
        assert_eq!(load(&fs, MAX).unwrap().deploy_sql, "SELECT 1;");
        let text = refused(&MemFs::default().file("deploy.sql", b"SELECT \xff;"));
        assert_eq!(
            text,
            "deploy.sql is not valid UTF-8 (first invalid byte at offset 7); re-save the file as UTF-8 without BOM"
        );
    }

    #[test]
    fn errors_name_the_file_exactly_once() {
        let text = refused(&project().file("data/blob.bin", b"ok\0binary"));
        assert_eq!(text.matches("data/blob.bin").count(), 1, "{text}");
        assert!(text.contains("./data/blob.bin"), "{text}");
        for (path, content) in [
            (
                "migrations/001_users.sql",
                "/*\n<pgmi-meta idempotent=\"false\">\n</pgmi-meta>\n*/\nSELECT 1;",
            ),
            (
                "migrations/002_orders.sql",
                "/*\n<pgmi-meta id=\"550e8400-e29b-41d4-a716-446655440000\" idempotent=\"false\">\n  <description>unclosed\n</pgmi-meta>\n*/\nSELECT 1;",
            ),
        ] {
            let p = load(&project().file(path, content), MAX).unwrap();
            let e = super::super::metadata::collect(&p.sources).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{}", e.text());
            assert_eq!(e.text().matches(path).count(), 1, "{}", e.text());
            assert!(e.text().contains(&format!("./{path}")), "{}", e.text());
        }
    }

    #[test]
    fn max_file_size_comes_from_the_environment() {
        let env = |v: &str| crate::env::Env::from_pairs(&[("PGMI_MAX_FILE_SIZE", v)]);
        assert_eq!(max_size(&crate::env::Env::default()).unwrap(), MAX);
        assert_eq!(max_size(&env("")).unwrap(), MAX);
        assert_eq!(max_size(&env("20971520")).unwrap(), 20_971_520);
        for bad in ["0", "-1", "10MB", "1.5"] {
            let e = max_size(&env(bad)).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{bad}");
            assert!(e.text().contains("PGMI_MAX_FILE_SIZE"), "{}", e.text());
        }
    }

    #[test]
    fn deploy_sql_missing_or_a_directory_is_exit_14() {
        for fs in [MemFs::default(), MemFs::default().dir("deploy.sql")] {
            let e = load(&fs, MAX).unwrap_err();
            assert_eq!(e.exit_code(), 14, "{}", e.text());
            assert_eq!(
                e.text(),
                "deploy.sql not found in /mem\nexpected: /mem/deploy.sql — run `pgmi init` to scaffold one"
            );
        }
        assert!(scan(&MemFs::default(), MAX).is_ok());
    }

    #[test]
    fn symlinks_are_refused_unless_hidden() {
        for (fs, name) in [
            (project().link_to_file("l.sql"), "l.sql"),
            (project().link_to_dir("l"), "l"),
            (project().dangling_link("l"), "l"),
        ] {
            assert_eq!(
                refused(&fs),
                format!(
                    "failed to scan directory \"/mem\": failed to read ./{name}: refusing to read symlink {name}: pgmi does not follow symlinks; exclude it in .pgmiignore or replace it with the file"
                )
            );
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
        assert_eq!(
            refused(&project().file("a.sql", b"SELECT\0")),
            "failed to scan directory \"/mem\": file ./a.sql contains a NUL byte; pgmi loads project files as text, so exclude it in .pgmiignore or move it out of the project path"
        );
        assert!(
            refused(&project().file("a.sql", b"SELECT \xff"))
                .ends_with("file ./a.sql is not valid UTF-8; pgmi loads project files as text, so exclude it in .pgmiignore or move it out of the project path")
        );
        let big = project().file("a.sql", "x".repeat(11));
        let e = load(&big, 10).unwrap_err();
        assert!(e.text().contains("exceeds 10-byte limit"), "{}", e.text());
        assert!(load(&big, 11).is_ok());
    }

    struct Grown(MemFs);

    impl Vfs for Grown {
        fn root(&self) -> &std::path::Path {
            self.0.root()
        }
        fn stat(&self, rel: &str) -> std::io::Result<crate::vfs::Stat> {
            self.0.stat(rel).map(|s| crate::vfs::Stat { len: 1, ..s })
        }
        fn read(&self, rel: &str) -> std::io::Result<Vec<u8>> {
            self.0.read(rel)
        }
        fn read_dir(&self, rel: &str) -> std::io::Result<Vec<crate::vfs::Entry>> {
            self.0.read_dir(rel)
        }
    }

    #[test]
    fn a_file_that_grows_past_the_limit_after_its_stat_is_refused() {
        let grown = Grown(project().file("a.sql", "x".repeat(100)));
        let e = read_text(&grown, "a.sql", 10).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("exceeds 10-byte limit"), "{}", e.text());
    }

    #[test]
    fn unreadable_files_and_directories_are_invalid_config() {
        for (fs, path) in [
            (project().unreadable_file("secret.sql"), "./secret.sql"),
            (project().unreadable_dir("locked"), "locked"),
            (
                project().unreadable_file("__test__/t.sql"),
                "./__test__/t.sql",
            ),
        ] {
            let text = refused(&fs);
            assert!(text.contains(path), "{text}");
            assert!(text.contains(".pgmiignore"), "{text}");
        }
        assert!(
            load(
                &project()
                    .unreadable_dir("locked")
                    .file(".pgmiignore", "locked/\n"),
                MAX
            )
            .is_ok()
        );
    }

    #[test]
    fn a_named_pipe_socket_or_device_is_refused_before_it_is_read() {
        for (fs, rel) in [
            (project().special("pipe.sql"), "pipe.sql"),
            (
                project().special("__test__/notes.txt"),
                "__test__/notes.txt",
            ),
        ] {
            assert_eq!(
                refused(&fs),
                format!(
                    "failed to scan directory \"/mem\": failed to read ./{rel}: refusing to read {rel}: not a regular file (a named pipe, socket or device); exclude it in .pgmiignore or remove it"
                )
            );
            assert_eq!(
                format!(
                    "failed to scan directory \"/mem\": {}",
                    scan(&fs, MAX).unwrap_err().text()
                ),
                refused(&fs)
            );
        }
        assert!(
            load(
                &project()
                    .special("pipe.sql")
                    .file(".pgmiignore", "pipe.sql\n"),
                MAX
            )
            .is_ok()
        );
        let fs = MemFs::default().special("deploy.sql");
        assert_eq!(
            refused(&fs),
            format!(
                "{} is not a regular file (a named pipe, socket or device)",
                std::path::Path::new("/mem").join("deploy.sql").display()
            )
        );
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

    #[test]
    fn every_embedded_template_has_checksummed_sql_without_a_disk() {
        use super::super::checksum;
        let keywords = [
            "SELECT",
            "CREATE",
            "INSERT",
            "UPDATE",
            "DELETE",
            "ALTER",
            "DO $$",
            "BEGIN",
            "RAISE NOTICE",
        ];
        for name in crate::templates::Template::ALL {
            let fs = EmbeddedFs::new(&format!("templates/{name}"));
            let p = load(&fs, MAX).unwrap_or_else(|e| panic!("{name}: {}", e.text()));
            assert!(p.deploy_sql.contains("pg_temp.pgmi"), "{name}");
            let sql: Vec<_> = p
                .sources
                .iter()
                .chain(&p.tests)
                .filter(|f| is_sql(&f.path))
                .collect();
            assert!(!sql.is_empty(), "{name}");
            for f in sql {
                let upper = f.content.trim().to_ascii_uppercase();
                assert!(
                    keywords.iter().any(|k| upper.contains(k)),
                    "{name}: {} has no SQL",
                    f.path
                );
                let raw = checksum::raw(f.content.as_bytes());
                let normalized = checksum::normalized(f.content.as_bytes());
                for sum in [&raw, &normalized] {
                    assert!(
                        sum.len() == 64 && sum.bytes().all(|b| b.is_ascii_hexdigit()),
                        "{name}: {} checksum {sum}",
                        f.path
                    );
                }
            }
        }
        for (name, dirs) in [
            ("basic", ["physical", "semantic"]),
            ("advanced", ["api", "lib"]),
        ] {
            let fs = EmbeddedFs::new(&format!("templates/{name}"));
            assert!(!fs.read("README.md").unwrap().is_empty(), "{name}");
            for dir in dirs {
                assert_eq!(fs.stat(dir).unwrap().kind, FileKind::Dir, "{name}/{dir}");
            }
        }
    }

    #[cfg(all(unix, not(target_os = "macos")))]
    #[test]
    fn non_utf8_file_name_is_refused_unless_hidden() {
        use std::os::unix::ffi::OsStrExt;
        let os_project = |files: &[(&std::ffi::OsStr, &[u8])]| {
            let d = crate::testdir::TempDir::new("walk");
            std::fs::write(d.join("deploy.sql"), "SELECT 1;").unwrap();
            for (name, body) in files {
                std::fs::write(d.join(name), body).unwrap();
            }
            let fs = crate::vfs::OsFs::new(d.path());
            (d, fs)
        };
        let bad = std::ffi::OsStr::from_bytes(b"caf\xe9.sql");
        let (_d, fs) = os_project(&[(bad, b"SELECT 1;")]);
        let e = load(&fs, MAX).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("./caf\u{fffd}.sql"), "{}", e.text());
        let (_d, fs) = os_project(&[
            (bad, b"SELECT 1;"),
            (std::ffi::OsStr::new(".pgmiignore"), b"caf*.sql\n"),
        ]);
        assert!(load(&fs, MAX).unwrap().sources.is_empty());
    }

    #[test]
    fn sql_extensions() {
        assert!(is_sql("a.SQL"));
        assert!(is_sql("x.plpgsql"));
        assert!(!is_sql("a.txt"));
    }
}
