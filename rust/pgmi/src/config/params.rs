use std::collections::BTreeMap;
use std::io::Read;

use super::dotenv::{self, Mode};
use crate::env::Env;
use crate::error::{Error, Result};

pub(crate) const ENV_PREFIX: &str = "PGMI_PARAM_";

pub(crate) struct Sources<'a> {
    pub(crate) yaml: Vec<(String, String)>,
    pub(crate) env: &'a Env,
    pub(crate) files: &'a [String],
    pub(crate) flags: &'a [String],
    /// Receives the `--verbose` lines: counts only, never keys or values.
    pub(crate) note: &'a dyn Fn(String),
}

fn valid_key(key: &str) -> bool {
    let mut chars = key.chars();
    key.len() <= 63
        && chars
            .next()
            .is_some_and(|c| c.is_ascii_alphabetic() || c == '_')
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_')
}

fn check(out: &BTreeMap<String, String>, keys: &[String]) -> Result<()> {
    if let Some(k) = keys.iter().find(|k| !valid_key(k)) {
        return Err(Error::config(format!(
            "invalid parameter key '{k}': must start with a letter or underscore and contain only letters, digits and underscores, 1-63 characters (PostgreSQL identifier limit)"
        )));
    }
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

pub(crate) fn collect(src: Sources<'_>, stdin: &mut dyn Read) -> Result<BTreeMap<String, String>> {
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
        let (name, text) = if file == "-" {
            let mut s = String::new();
            stdin.read_to_string(&mut s).map_err(|e| {
                Error::config("failed to read params file - (stdin)").with_source(e)
            })?;
            ("- (stdin)".to_string(), s)
        } else {
            let t = std::fs::read_to_string(file).map_err(|e| {
                Error::config(format!("failed to read params file {file}")).with_source(e)
            })?;
            (file.clone(), t)
        };
        let pairs = dotenv::parse(&text, Mode::Strict)
            .map_err(|e| Error::config(format!("failed to parse params file {name}: {e}")))?;
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
        let Some((k, v)) = p.split_once('=') else {
            return Err(Error::config(format!(
                "invalid parameter format {p:?}: expected key=value"
            )));
        };
        if k.is_empty() {
            return Err(Error::config(format!(
                "invalid parameter format {p:?}: expected key=value"
            )));
        }
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

    #[test]
    fn precedence() {
        let env = Env::from_pairs(&[
            ("PGMI_PARAM_A", "env"),
            ("PGMI_PARAM_B", "env"),
            ("PGMI_PARAM_C", "env"),
        ]);
        let flags = vec!["a=flag".to_string()];
        let files = vec!["-".to_string()];
        let out = collect(
            Sources {
                yaml: vec![("D".into(), "yaml".into()), ("c".into(), "yaml".into())],
                env: &env,
                files: &files,
                flags: &flags,
                note: &|_| {},
            },
            &mut "a=file\nb=file\n".as_bytes(),
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
        let flags = vec!["a=secret-flag".to_string()];
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
            &mut "a=secret-file\nc=1\nC2=2\n".as_bytes(),
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
    fn refusals() {
        let env = Env::from_pairs(&[("PGMI_PARAM_TOKEN", "")]);
        let run = |env: &Env, files: &[String], flags: &[String], input: &str| {
            collect(
                Sources {
                    yaml: vec![],
                    env,
                    files,
                    flags,
                    note: &|_| {},
                },
                &mut input.as_bytes(),
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
        let flags: Vec<String> = flags.iter().map(|f| (*f).to_string()).collect();
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
            &mut &stdin[..],
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
        let dir = crate::testdir::TempDir::new("params");
        for (name, text) in [("a.env", "Env=a"), ("b.env", "env=b"), ("c.env", "ENV=c")] {
            std::fs::write(dir.join(name), text).unwrap();
        }
        let path = |n: &str| dir.join(n).display().to_string();
        let out = run(
            &[],
            &Env::default(),
            &[path("a.env"), path("b.env")],
            &[],
            b"",
        )
        .unwrap();
        assert_eq!(out, BTreeMap::from([("env".into(), "b".into())]));
        let files = [path("b.env"), path("a.env"), path("c.env")];
        let out = run(&[], &Env::default(), &files, &[], b"").unwrap();
        assert_eq!(out, BTreeMap::from([("env".into(), "c".into())]));
    }
}
