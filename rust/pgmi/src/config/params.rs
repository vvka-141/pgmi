use std::collections::BTreeMap;
use std::io::Read;

use super::dotenv::{self, Mode};
use crate::env::Env;
use crate::error::{Error, Result};

pub const ENV_PREFIX: &str = "PGMI_PARAM_";

pub struct Sources<'a> {
    pub yaml: Vec<(String, String)>,
    pub env: &'a Env,
    pub files: &'a [String],
    pub flags: &'a [String],
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

pub fn collect(src: Sources<'_>, stdin: &mut dyn Read) -> Result<BTreeMap<String, String>> {
    let mut out = BTreeMap::new();
    let mut keys: Vec<String> = Vec::new();
    for (k, v) in src.yaml {
        keys.push(k.clone());
        out.insert(k.to_lowercase(), v);
    }
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
    let stdin_count = src.files.iter().filter(|f| *f == "-").count();
    if stdin_count > 1 {
        return Err(Error::config(format!(
            "--params-file - (stdin) given {stdin_count} times; stdin can be read only once"
        )));
    }
    for file in src.files {
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
            out.insert(lower, v);
        }
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
    check(&out, &keys)?;
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

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
    fn refusals() {
        let env = Env::from_pairs(&[("PGMI_PARAM_TOKEN", "")]);
        let run = |env: &Env, files: &[String], flags: &[String], input: &str| {
            collect(
                Sources {
                    yaml: vec![],
                    env,
                    files,
                    flags,
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
}
