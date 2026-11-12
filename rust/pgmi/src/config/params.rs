use std::collections::BTreeMap;
use std::io::Read;

use tokio_util::sync::CancellationToken;

use super::dotenv::{self, Mode};
use crate::env::Env;
use crate::error::{Error, Result};
use crate::vfs::Vfs;

pub(crate) const ENV_PREFIX: &str = "PGMI_PARAM_";

/// One parameter given on the deploy request.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Param {
    /// A `--param` argument as typed; `collect` splits it at the first `=`
    /// after reading the other sources, where Go refuses a malformed one.
    Flag(String),
    /// A key and a value given apart (serve's `params` object).
    Pair(String, String),
}

pub(crate) struct Sources<'a> {
    pub(crate) yaml: Vec<(String, String)>,
    pub(crate) env: &'a Env,
    pub(crate) files: &'a [String],
    pub(crate) flags: &'a [Param],
    /// Receives the `--verbose` lines: counts only, never keys or values.
    pub(crate) note: &'a dyn Fn(String),
}

/// Where `--params-file` reads: a named file through `files`, `-` from
/// `stdin`, each at most `limit` bytes. A wait for a pipe ends once `stop`
/// is cancelled.
pub(crate) struct Inputs<'a> {
    pub(crate) files: &'a dyn Vfs,
    pub(crate) stdin: Option<&'a mut dyn Read>,
    pub(crate) limit: u64,
    pub(crate) stop: &'a CancellationToken,
}

impl Inputs<'_> {
    /// A params file's display name and text.
    fn read(&mut self, file: &str) -> Result<(String, String)> {
        let limit = self.limit;
        let (name, bytes) = if file == "-" {
            let input = self.stdin.as_mut().ok_or_else(|| {
                Error::config("failed to read params file - (stdin): stdin is not available here")
            })?;
            let mut bytes = Vec::new();
            input
                .take(limit.saturating_add(1))
                .read_to_end(&mut bytes)
                .map_err(|e| {
                    Error::config("failed to read params file - (stdin)").with_source(e)
                })?;
            ("- (stdin)".to_string(), bytes)
        } else {
            let bytes = self.files.read_named(file, limit, self.stop).map_err(|e| {
                Error::config(format!("failed to read params file {file}")).with_source(e)
            })?;
            (file.to_string(), bytes)
        };
        if bytes.len() as u64 > limit {
            return Err(Error::config(format!(
                "failed to read params file {name}: exceeds {limit}-byte limit (override via PGMI_MAX_FILE_SIZE)"
            )));
        }
        let text = String::from_utf8(bytes).map_err(|_| {
            Error::config(format!("failed to read params file {name}")).with_source(
                std::io::Error::new(
                    std::io::ErrorKind::InvalidData,
                    "stream did not contain valid UTF-8",
                ),
            )
        })?;
        Ok((name, text))
    }
}

fn valid_key(key: &str) -> bool {
    let mut chars = key.chars();
    key.len() <= 63
        && chars
            .next()
            .is_some_and(|c| c.is_ascii_alphabetic() || c == '_')
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_')
}

pub(crate) fn check_key(k: &str) -> Result<()> {
    if valid_key(k) {
        return Ok(());
    }
    Err(Error::config(format!(
        "invalid parameter key '{k}': must start with a letter or underscore and contain only letters, digits and underscores, 1-63 characters (PostgreSQL identifier limit)"
    )))
}

fn check(out: &BTreeMap<String, String>, keys: &[String]) -> Result<()> {
    keys.iter().try_for_each(|k| check_key(k))?;
    if let Some((k, at)) = out.iter().find_map(|(k, v)| v.find('\0').map(|at| (k, at))) {
        return Err(Error::config(format!(
            "invalid parameter value for '{k}': contains NUL byte at offset {at}"
        )));
    }
    Ok(())
}

fn no_case_twins<'a>(source: &str, keys: impl IntoIterator<Item = &'a str>) -> Result<()> {
    let mut seen: BTreeMap<String, &str> = BTreeMap::new();
    for k in keys {
        if let Some(prev) = seen.insert(k.to_lowercase(), k)
            && prev != k
        {
            return Err(Error::config(format!(
                "{source}: parameters {prev:?} and {k:?} differ only in case; the session stores keys lowercased, so pass one"
            )));
        }
    }
    Ok(())
}

#[expect(
    clippy::too_many_lines,
    reason = "Go's precedence order, one source after another"
)]
pub(crate) fn collect(
    src: Sources<'_>,
    mut inputs: Inputs<'_>,
) -> Result<BTreeMap<String, String>> {
    let mut out = BTreeMap::new();
    let mut keys: Vec<String> = Vec::new();
    no_case_twins("pgmi.yaml params", src.yaml.iter().map(|(k, _)| k.as_str()))?;
    for (k, v) in src.yaml {
        keys.push(k.clone());
        out.insert(k.to_lowercase(), v);
    }
    if let Some(name) = src.env.not_utf8_with_prefix(ENV_PREFIX) {
        return Err(Error::config(format!(
            "environment variable {name} is not valid UTF-8"
        )));
    }
    no_case_twins(
        &format!("{ENV_PREFIX}* environment variables"),
        src.env
            .with_prefix(ENV_PREFIX)
            .map(|(name, _)| &name[ENV_PREFIX.len()..]),
    )?;
    for (name, value) in src.env.with_prefix(ENV_PREFIX) {
        if name.len() == ENV_PREFIX.len() {
            return Err(Error::config(format!(
                "environment variable {name} has no parameter name; name it {ENV_PREFIX}<NAME>"
            )));
        }
        if value.is_empty() {
            return Err(Error::config(format!(
                "environment variable {name} is set but empty; unset it or give it a value"
            )));
        }
        keys.push(name[ENV_PREFIX.len()..].to_string());
        out.insert(name[ENV_PREFIX.len()..].to_lowercase(), value.to_string());
    }
    let from_env = src.env.with_prefix(ENV_PREFIX).count();
    if from_env > 0 {
        (src.note)(format!(
            "Loaded {from_env} parameter(s) from {ENV_PREFIX}* environment variables"
        ));
    }
    let stdin_count = src.files.iter().filter(|f| *f == "-").count();
    if stdin_count > 1 {
        return Err(Error::config(format!(
            "--params-file - (stdin) given {stdin_count} times; stdin can be read only once"
        )));
    }
    let mut from_files = std::collections::BTreeSet::new();
    for file in src.files {
        (src.note)(format!("Loading parameters from file: {file}"));
        let (name, text) = inputs.read(file)?;
        let pairs = dotenv::parse(&text, Mode::Strict).map_err(|e| {
            Error::config(format!("failed to parse params file {name}")).with_source(e)
        })?;
        let mut seen: BTreeMap<String, String> = BTreeMap::new();
        for (k, v) in pairs {
            let lower = k.to_lowercase();
            if let Some(prev) = seen.get(&lower)
                && *prev != k
            {
                return Err(Error::config(format!(
                    "params file {name}: keys {prev:?} and {k:?} differ only in case"
                )));
            }
            seen.insert(lower.clone(), k.clone());
            keys.push(k);
            from_files.insert(lower.clone());
            out.insert(lower, v);
        }
        (src.note)(format!(
            "Loaded {} parameter(s) from file (total: {})",
            seen.len(),
            from_files.len()
        ));
    }
    let mut flagged: BTreeMap<String, &str> = BTreeMap::new();
    for p in src.flags {
        let (k, v) = match p {
            Param::Pair(k, v) => (k.as_str(), v.as_str()),
            Param::Flag(text) => match text.split_once('=') {
                Some((k, v)) if !k.is_empty() => (k, v),
                _ => {
                    return Err(Error::config(format!(
                        "invalid parameter format {text:?}: expected key=value"
                    )));
                }
            },
        };
        let lower = k.to_lowercase();
        if let Some(prev) = flagged.get(&lower)
            && *prev != k
        {
            return Err(Error::config(format!(
                "parameters {prev:?} and {k:?} differ only in case; the session stores keys lowercased, so pass one"
            )));
        }
        flagged.insert(lower.clone(), k);
        keys.push(k.to_string());
        out.insert(lower, v.to_string());
    }
    if !src.flags.is_empty() {
        (src.note)(format!(
            "CLI parameters override {} value(s)",
            src.flags.len()
        ));
    }
    check(&out, &keys)?;
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::error::Kind;
    use crate::vfs::MemFs;

    fn inputs(stdin: Option<&mut dyn Read>) -> Inputs<'_> {
        Inputs {
            files: Box::leak(Box::new(MemFs::default().file("p.env", "a=file\n"))),
            stdin,
            limit: 64,
            stop: Box::leak(Box::new(CancellationToken::new())),
        }
    }

    fn sources<'a>(env: &'a Env, files: &'a [String]) -> Sources<'a> {
        Sources {
            yaml: vec![],
            env,
            files,
            flags: &[],
            note: &|_| {},
        }
    }

    #[test]
    fn a_params_file_is_read_through_the_filesystem_and_refused_over_the_limit() {
        let env = Env::default();
        let files = ["p.env".to_string()];
        let out = collect(sources(&env, &files), inputs(None)).unwrap();
        assert_eq!(out["a"], "file");
        let big = format!("a={}\n", "x".repeat(64));
        let mut over = inputs(None);
        over.files = Box::leak(Box::new(MemFs::default().file("p.env", &big)));
        let e = collect(sources(&env, &files), over).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert_eq!(
            e.text(),
            "failed to read params file p.env: exceeds 64-byte limit (override via PGMI_MAX_FILE_SIZE)"
        );
        let stdin = ["-".to_string()];
        let e = collect(sources(&env, &stdin), inputs(Some(&mut big.as_bytes()))).unwrap_err();
        assert_eq!(
            e.text(),
            "failed to read params file - (stdin): exceeds 64-byte limit (override via PGMI_MAX_FILE_SIZE)"
        );
    }

    #[test]
    fn precedence() {
        let env = Env::from_pairs(&[
            ("PGMI_PARAM_A", "env"),
            ("PGMI_PARAM_B", "env"),
            ("PGMI_PARAM_C", "env"),
        ]);
        let flags = vec![Param::Flag("a=flag".into())];
        let files = vec!["-".to_string()];
        let out = collect(
            Sources {
                yaml: vec![("D".into(), "yaml".into()), ("c".into(), "yaml".into())],
                env: &env,
                files: &files,
                flags: &flags,
                note: &|_| {},
            },
            inputs(Some(&mut "a=file\nb=file\n".as_bytes())),
        )
        .unwrap();
        assert_eq!(out["a"], "flag");
        assert_eq!(out["b"], "file");
        assert_eq!(out["c"], "env");
        assert_eq!(out["d"], "yaml");
    }

    #[test]
    fn verbose_notes_count_parameters_and_never_show_keys_or_values() {
        let env = Env::from_pairs(&[("PGMI_PARAM_A", "secret-env"), ("PGMI_PARAM_B", "x")]);
        let flags = vec![Param::Flag("a=secret-flag".into())];
        let files = vec!["-".to_string()];
        let notes = std::cell::RefCell::new(Vec::new());
        collect(
            Sources {
                yaml: vec![],
                env: &env,
                files: &files,
                flags: &flags,
                note: &|n| notes.borrow_mut().push(n),
            },
            inputs(Some(&mut "a=secret-file\nc=1\nC2=2\n".as_bytes())),
        )
        .unwrap();
        assert_eq!(
            notes.into_inner(),
            [
                "Loaded 2 parameter(s) from PGMI_PARAM_* environment variables",
                "Loading parameters from file: -",
                "Loaded 3 parameter(s) from file (total: 3)",
                "CLI parameters override 1 value(s)",
            ]
        );
    }

    #[test]
    fn a_pair_is_taken_as_given_and_a_flag_is_split_at_its_first_equals_sign() {
        let flags = [
            Param::Pair("a".into(), "x=y".into()),
            Param::Flag("b=x=y".into()),
        ];
        let out = collect(
            Sources {
                yaml: vec![],
                env: &Env::default(),
                files: &[],
                flags: &flags,
                note: &|_| {},
            },
            inputs(None),
        )
        .unwrap();
        assert_eq!(out["a"], "x=y");
        assert_eq!(out["b"], "x=y");
        let flags = [Param::Pair("a=b".into(), "c".into())];
        let e = collect(
            Sources {
                yaml: vec![],
                env: &Env::default(),
                files: &[],
                flags: &flags,
                note: &|_| {},
            },
            inputs(None),
        )
        .unwrap_err();
        assert!(
            e.text().contains("invalid parameter key 'a=b'"),
            "{}",
            e.text()
        );
    }

    #[test]
    fn refusals() {
        let env = Env::from_pairs(&[("PGMI_PARAM_TOKEN", "")]);
        let run = |env: &Env, files: &[String], flags: &[String], input: &str| {
            let flags: Vec<Param> = flags.iter().cloned().map(Param::Flag).collect();
            collect(
                Sources {
                    yaml: vec![],
                    env,
                    files,
                    flags: &flags,
                    note: &|_| {},
                },
                inputs(Some(&mut input.as_bytes())),
            )
        };
        let e = run(&env, &[], &[], "").unwrap_err();
        assert!(e.text().contains("PGMI_PARAM_TOKEN"));
        let none = Env::default();
        let e = run(&none, &["-".into(), "-".into()], &[], "").unwrap_err();
        assert!(e.text().contains("stdin"));
        let e = run(&none, &[], &["noequals".into()], "").unwrap_err();
        assert!(e.text().contains("key=value"));
        let e = run(&none, &["-".into()], &[], "Token=1\ntoken=2\n").unwrap_err();
        assert_eq!(e.exit_code(), 10);
        for bad in ["a b=1", "1abc=1", "é=1", &format!("{}=1", "k".repeat(64))] {
            let e = run(&none, &[], &[bad.to_string()], "").unwrap_err();
            assert_eq!(e.exit_code(), 10, "{bad}");
            assert!(
                e.text().contains("invalid parameter key"),
                "{bad}: {}",
                e.text()
            );
        }
        let ok = run(
            &none,
            &[],
            &[format!("_{}=1", "k".repeat(62)), "a_1=x".into()],
            "",
        );
        assert!(ok.is_ok());
        let e = run(&none, &[], &["Env=a".into(), "env=b".into()], "").unwrap_err();
        assert!(e.text().contains("differ only in case"), "{}", e.text());
        let e = run(&none, &[], &["k=a\0b".into()], "").unwrap_err();
        assert!(e.text().contains("NUL byte at offset 1"), "{}", e.text());
        assert!(!e.text().contains("a\0b"));
        let env = Env::from_pairs(&[("PGMI_PARAM_A-B", "1")]);
        assert_eq!(run(&env, &[], &[], "").unwrap_err().exit_code(), 10);
    }

    fn run(
        yaml: &[(&str, &str)],
        env: &Env,
        files: &[String],
        flags: &[&str],
        stdin: &[u8],
    ) -> Result<BTreeMap<String, String>> {
        let flags: Vec<Param> = flags
            .iter()
            .map(|f| Param::Flag((*f).to_string()))
            .collect();
        collect(
            Sources {
                yaml: yaml
                    .iter()
                    .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
                    .collect(),
                env,
                files,
                flags: &flags,
                note: &|_| {},
            },
            inputs(Some(&mut &stdin[..])),
        )
    }

    fn key_check(key: &str) -> Result<BTreeMap<String, String>> {
        run(&[], &Env::default(), &[], &[&format!("{key}=v")], b"")
    }

    #[test]
    fn keys_that_are_not_postgres_identifiers_are_invalid_config() {
        for key in [
            "",
            "bad key",
            "bad-key",
            "bad.key",
            "key;DROP TABLE",
            "'; DROP TABLE users;--",
            "key\n",
            "key\t",
            "key@domain",
            "key()",
            "1abc",
            "123",
            &"a".repeat(64),
        ] {
            let e = key_check(key).unwrap_err();
            assert_eq!(e.kind(), Kind::Config, "{key:?}: {}", e.text());
        }
    }

    #[test]
    fn identifier_keys_are_accepted_and_folded() {
        for key in [
            "mykey",
            "MYKEY",
            "MyKey",
            "my_key",
            "key123",
            "key_123_test",
            "_private",
            "suffix_underscore_",
            "a1",
            "x",
            "A",
            &"a".repeat(63),
        ] {
            let out = key_check(key).unwrap();
            assert_eq!(out.get(&key.to_lowercase()).map(String::as_str), Some("v"));
        }
    }

    #[test]
    fn a_bad_value_error_names_the_key_and_not_the_value() {
        let e = run(&[], &Env::default(), &[], &["db_password=sec\0ret"], b"").unwrap_err();
        assert_eq!(e.kind(), Kind::Config);
        assert_eq!(
            e.text(),
            "invalid parameter value for 'db_password': contains NUL byte at offset 3"
        );
        let stdin = b"api_key=before\xff\xfeafter\n";
        let e = run(&[], &Env::default(), &["-".into()], &[], stdin).unwrap_err();
        assert_eq!(e.kind(), Kind::Config);
        assert!(!e.text().contains("before"), "{}", e.text());
    }

    #[cfg(unix)]
    #[test]
    fn a_non_utf8_environment_parameter_is_invalid_config() {
        use std::ffi::OsString;
        use std::os::unix::ffi::OsStringExt;
        let env = Env::from_os([(
            OsString::from("PGMI_PARAM_API_KEY"),
            OsString::from_vec(b"before\xff\xfeafter".to_vec()),
        )]);
        let e = run(&[], &env, &[], &[], b"").unwrap_err();
        assert_eq!(e.kind(), Kind::Config);
        assert_eq!(
            e.text(),
            "environment variable PGMI_PARAM_API_KEY is not valid UTF-8"
        );
        let env = Env::from_os([(
            OsString::from_vec(b"PGMI_PARAM_K\xff".to_vec()),
            OsString::from("v"),
        )]);
        assert_eq!(
            run(&[], &env, &[], &[], b"").unwrap_err().kind(),
            Kind::Config
        );
    }

    #[test]
    fn case_twins_within_one_source_are_invalid_config() {
        let none = Env::default();
        let e = run(&[("Region", "eu"), ("region", "us")], &none, &[], &[], b"").unwrap_err();
        assert_eq!(e.kind(), Kind::Config);
        assert!(e.text().starts_with("pgmi.yaml params: "), "{}", e.text());
        let e = run(&[], &none, &[], &["Env=a", "env=b"], b"").unwrap_err();
        assert_eq!(e.kind(), Kind::Config);
        let e = run(&[], &none, &["-".into()], &[], b"Abc=1\nabc=2\n").unwrap_err();
        assert_eq!(e.kind(), Kind::Config);
        assert!(e.text().contains("- (stdin)"), "{}", e.text());
    }

    #[cfg(not(windows))]
    #[test]
    fn environment_case_twins_are_invalid_config() {
        let env = Env::from_pairs(&[("PGMI_PARAM_Region", "eu"), ("PGMI_PARAM_REGION", "us")]);
        let e = run(&[], &env, &[], &[], b"").unwrap_err();
        assert_eq!(e.kind(), Kind::Config);
        assert!(
            e.text().starts_with("PGMI_PARAM_* environment variables: "),
            "{}",
            e.text()
        );
    }

    #[test]
    fn case_twins_across_sources_are_one_parameter_and_the_later_source_wins() {
        let out = run(
            &[("DB_Url", "yaml")],
            &Env::default(),
            &[],
            &["db_url=cli", "env=b"],
            b"",
        )
        .unwrap();
        assert_eq!(
            out,
            BTreeMap::from([("db_url".into(), "cli".into()), ("env".into(), "b".into())])
        );
    }

    #[test]
    fn case_twins_across_params_files_the_later_file_wins() {
        let fs = MemFs::default()
            .file("a.env", "Env=a")
            .file("b.env", "env=b")
            .file("c.env", "ENV=c");
        let env = Env::default();
        let read = |names: &[&str]| {
            let files: Vec<String> = names.iter().map(|n| (*n).to_string()).collect();
            let mut over = inputs(None);
            over.files = &fs;
            collect(sources(&env, &files), over).unwrap()
        };
        assert_eq!(
            read(&["a.env", "b.env"]),
            BTreeMap::from([("env".into(), "b".into())])
        );
        assert_eq!(
            read(&["b.env", "a.env", "c.env"]),
            BTreeMap::from([("env".into(), "c".into())])
        );
    }
}
