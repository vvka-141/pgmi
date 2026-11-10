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

pub fn collect(src: Sources<'_>, stdin: &mut dyn Read) -> Result<BTreeMap<String, String>> {
    let mut out = BTreeMap::new();
    for (k, v) in src.yaml {
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
            seen.insert(lower.clone(), k);
            out.insert(lower, v);
        }
    }
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
        out.insert(k.to_lowercase(), v.to_string());
    }
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
    }
}
