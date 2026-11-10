use std::collections::{BTreeMap, BTreeSet};
use std::ffi::OsString;

use crate::error::{Error, Result};

#[derive(Debug, Default, Clone)]
pub struct Env {
    vars: BTreeMap<String, String>,
    not_utf8: BTreeSet<String>,
}

impl Env {
    pub fn snapshot() -> Self {
        Self::from_os(std::env::vars_os())
    }

    pub fn from_os(vars: impl IntoIterator<Item = (OsString, OsString)>) -> Self {
        let mut env = Env::default();
        for (k, v) in vars {
            let Ok(k) = k.into_string() else { continue };
            match v.into_string() {
                Ok(v) => {
                    env.vars.insert(norm(&k), v);
                }
                Err(_) => {
                    env.not_utf8.insert(norm(&k));
                }
            }
        }
        env
    }

    #[cfg(test)]
    pub fn from_pairs(pairs: &[(&str, &str)]) -> Self {
        Self::from_os(
            pairs
                .iter()
                .map(|(k, v)| (OsString::from(k), OsString::from(v))),
        )
    }

    pub fn get(&self, name: &str) -> Result<Option<&str>> {
        let key = norm(name);
        if self.not_utf8.contains(&key) {
            return Err(Error::config(format!(
                "environment variable {name} is not valid UTF-8"
            )));
        }
        Ok(self.vars.get(&key).map(String::as_str))
    }

    pub fn non_empty(&self, name: &str) -> Result<Option<&str>> {
        Ok(self.get(name)?.filter(|v| !v.is_empty()))
    }

    pub fn contains(&self, name: &str) -> bool {
        let key = norm(name);
        self.vars.contains_key(&key) || self.not_utf8.contains(&key)
    }

    pub fn insert(&mut self, name: &str, value: &str) {
        self.vars.insert(norm(name), value.to_string());
    }

    pub fn with_prefix<'a>(&'a self, prefix: &'a str) -> impl Iterator<Item = (&'a str, &'a str)> {
        self.vars
            .iter()
            .filter(move |(k, _)| k.starts_with(&norm(prefix)))
            .map(|(k, v)| (k.as_str(), v.as_str()))
    }

    pub fn home(&self) -> Option<std::path::PathBuf> {
        self.non_empty("HOME")
            .ok()
            .flatten()
            .map(std::path::PathBuf::from)
    }
}

#[cfg(windows)]
fn norm(name: &str) -> String {
    name.to_ascii_uppercase()
}

#[cfg(not(windows))]
fn norm(name: &str) -> String {
    name.to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lookups() {
        let env = Env::from_pairs(&[("PGMI_PARAM_A", "1"), ("PGHOST", "")]);
        assert_eq!(env.get("PGMI_PARAM_A").unwrap(), Some("1"));
        assert_eq!(env.non_empty("PGHOST").unwrap(), None);
        assert!(env.contains("PGHOST"));
        assert_eq!(env.with_prefix("PGMI_PARAM_").count(), 1);
    }
}
