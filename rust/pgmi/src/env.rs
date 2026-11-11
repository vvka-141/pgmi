use std::collections::{BTreeMap, BTreeSet};
use std::ffi::OsString;
use std::path::PathBuf;

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

    pub fn home(&self) -> Option<PathBuf> {
        self.home_for(cfg!(windows))
    }

    pub fn pg_files(&self) -> Option<PgFiles> {
        self.pg_files_for(cfg!(windows))
    }

    fn home_for(&self, windows: bool) -> Option<PathBuf> {
        let mut names = vec!["HOME"];
        if windows {
            names.push("USERPROFILE");
        }
        names
            .into_iter()
            .find_map(|n| self.non_empty(n).ok().flatten())
            .map(PathBuf::from)
    }

    fn pg_files_for(&self, windows: bool) -> Option<PgFiles> {
        if windows {
            let dir = PathBuf::from(self.non_empty("APPDATA").ok().flatten()?).join("postgresql");
            return Some(PgFiles {
                pgpass: dir.join("pgpass.conf"),
                dir,
            });
        }
        let home = self.home_for(false)?;
        Some(PgFiles {
            dir: home.join(".postgresql"),
            pgpass: home.join(".pgpass"),
        })
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PgFiles {
    pub dir: PathBuf,
    pub pgpass: PathBuf,
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

    #[test]
    fn postgres_files_follow_the_platform() {
        let env = Env::from_pairs(&[
            ("HOME", "/h"),
            ("APPDATA", "C:/Users/u/AppData/Roaming"),
            ("USERPROFILE", "C:/Users/u"),
        ]);
        let unix = env.pg_files_for(false).unwrap();
        assert_eq!(unix.dir, PathBuf::from("/h/.postgresql"));
        assert_eq!(unix.pgpass, PathBuf::from("/h/.pgpass"));
        let win = env.pg_files_for(true).unwrap();
        let roaming = PathBuf::from("C:/Users/u/AppData/Roaming/postgresql");
        assert_eq!(win.pgpass, roaming.join("pgpass.conf"));
        assert_eq!(win.dir, roaming);
        let no_appdata = Env::from_pairs(&[("HOME", "/h")]);
        assert_eq!(no_appdata.pg_files_for(true), None);
        let profile = Env::from_pairs(&[("USERPROFILE", "C:/Users/u")]);
        assert_eq!(profile.home_for(true), Some(PathBuf::from("C:/Users/u")));
        assert_eq!(profile.home_for(false), None);
    }
}
