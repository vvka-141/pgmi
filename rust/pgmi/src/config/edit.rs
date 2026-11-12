use std::fmt::Write as _;
use std::io::Write;
use std::path::Path;
use std::str::FromStr;

use yaml_edit::Document;

use crate::error::{Error, Result};

pub(crate) const KEYS: [&str; 14] = [
    "host",
    "port",
    "username",
    "database",
    "maintenance_database",
    "sslmode",
    "sslcert",
    "sslkey",
    "sslrootcert",
    "auth_method",
    "azure_tenant_id",
    "azure_client_id",
    "aws_region",
    "google_instance",
];

pub(crate) fn fresh(values: &[(&str, Option<String>)]) -> String {
    let mut out = String::from("connection:\n");
    for (k, v) in values.iter().filter_map(|(k, v)| Some((k, v.as_ref()?))) {
        let _ = writeln!(out, "  {k}: {}", scalar(v));
    }
    out
}

fn scalar(v: &str) -> String {
    let plain = !v.is_empty()
        && v.chars()
            .all(|c| c.is_ascii_alphanumeric() || "._-/@".contains(c))
        && !v.starts_with(['-', '.', '@'])
        && !matches!(
            v.to_ascii_lowercase().as_str(),
            "true" | "false" | "yes" | "no" | "on" | "off" | "null" | "y" | "n"
        );
    if plain {
        v.to_string()
    } else {
        format!("'{}'", v.replace('\'', "''"))
    }
}

pub(crate) fn set_connection(text: &str, values: &[(&str, Option<String>)]) -> Result<String> {
    if text.trim().is_empty() {
        return Ok(fresh(values));
    }
    let doc = Document::from_str(text)
        .map_err(|e| Error::config("pgmi.yaml could not be edited").with_source(e))?;
    let Some(root) = doc.as_mapping() else {
        return Err(Error::config(
            "pgmi.yaml could not be edited: its top level is not a mapping",
        ));
    };
    let Some(section) = root.get_mapping("connection") else {
        root.remove("connection");
        return Ok(format!("{}{}", fresh(values), doc));
    };
    let mut added = Vec::new();
    for key in KEYS {
        match values
            .iter()
            .find_map(|(k, v)| v.as_ref().filter(|_| *k == key))
        {
            Some(v) => {
                if !section.contains_key(key) {
                    added.push(key);
                }
                if v.parse::<u16>().is_ok() && key == "port" {
                    section.set(key, v.parse::<i64>().unwrap_or_default());
                } else {
                    section.set(key, v.as_str());
                }
            }
            None => {
                section.remove(key);
            }
        }
    }
    let out = doc.to_string();
    Ok(out
        .split_inclusive('\n')
        .filter(|line| !added.iter().any(|k| placeholder(line, k)))
        .collect())
}

fn placeholder(line: &str, key: &str) -> bool {
    line.starts_with([' ', '\t'])
        && line
            .trim_start()
            .strip_prefix('#')
            .is_some_and(|rest| rest.trim_start().starts_with(&format!("{key}:")))
}

pub(crate) fn write_atomic(path: &Path, text: &str) -> Result<()> {
    write(path, text, true)
}

pub(crate) fn write_private(path: &Path, text: &str) -> Result<()> {
    write(path, text, false)
}

fn write(path: &Path, text: &str, keep_mode: bool) -> Result<()> {
    let fail = |what: &str, e: std::io::Error| {
        Error::general(format!("{what} {}", path.display())).with_source(e)
    };
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
        let kept = std::fs::metadata(path)
            .ok()
            .filter(|_| keep_mode)
            .map(|m| m.permissions().mode() & 0o777);
        options.mode(kept.unwrap_or(0o600));
    }
    #[cfg(not(unix))]
    let _ = keep_mode;
    let (tmp, file) = open_temp(path, &options).map_err(|e| fail("write", e))?;
    let written = {
        let mut f = file;
        f.write_all(text.as_bytes()).and_then(|()| f.sync_all())
    };
    if let Err(e) = written {
        let _ = std::fs::remove_file(&tmp);
        return Err(fail("write", e));
    }
    std::fs::rename(&tmp, path).map_err(|e| {
        let _ = std::fs::remove_file(&tmp);
        fail("update", e)
    })
}

fn open_temp(
    path: &Path,
    options: &std::fs::OpenOptions,
) -> std::io::Result<(std::path::PathBuf, std::fs::File)> {
    static NEXT: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    let mut tries = 0;
    loop {
        let n = NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let mut tmp = path.as_os_str().to_owned();
        tmp.push(format!(".pgmi-tmp.{}.{n}", std::process::id()));
        let tmp = std::path::PathBuf::from(tmp);
        match options.open(&tmp) {
            Ok(f) => return Ok((tmp, f)),
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists && tries < 100 => tries += 1,
            Err(e) => return Err(e),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::yaml::ProjectConfig;

    fn local() -> Vec<(&'static str, Option<String>)> {
        vec![
            ("host", Some("127.0.0.1".into())),
            ("port", Some("55611".into())),
            ("username", Some("postgres".into())),
            ("database", Some("app".into())),
            ("maintenance_database", Some("postgres".into())),
            ("sslmode", Some("prefer".into())),
        ]
    }

    #[test]
    fn keeps_comments_and_params() {
        let out = set_connection(
            "# Team notes\nparams:\n  env: dev   # inline note\n",
            &local(),
        )
        .unwrap();
        assert!(out.contains("# Team notes"), "{out}");
        assert!(out.contains("env: dev   # inline note"), "{out}");
        assert!(out.contains("host: 127.0.0.1\n"), "{out}");
        assert!(out.contains("port: 55611\n"), "{out}");
        let cfg = ProjectConfig::parse(&out).unwrap();
        assert_eq!(cfg.connection.database.as_deref(), Some("app"));
        assert_eq!(cfg.connection.port.as_deref(), Some("55611"));
        assert_eq!(cfg.param_values(), vec![("env".into(), "dev".into())]);
    }

    #[test]
    fn edits_template_in_place() {
        let template = std::str::from_utf8(pgmi_shared::file("templates/basic/pgmi.yaml").unwrap())
            .unwrap()
            .to_string();
        let out = set_connection(&template, &local()).unwrap();
        assert!(out.contains("# pgmi project configuration"), "{out}");
        assert!(out.contains("# Target database"), "{out}");
        assert!(!out.contains("# username:"), "{out}");
        assert!(out.contains("timeout: 3m"), "{out}");
        let cfg = ProjectConfig::parse(&out).unwrap();
        assert_eq!(cfg.connection.host.as_deref(), Some("127.0.0.1"));
        assert_eq!(cfg.connection.database.as_deref(), Some("app"));
        assert_eq!(cfg.connection.username.as_deref(), Some("postgres"));
        assert_eq!(
            cfg.connection.maintenance_database.as_deref(),
            Some("postgres")
        );
        assert_eq!(cfg.timeout.as_deref(), Some("3m"));
    }

    #[test]
    fn a_top_level_that_is_not_a_mapping_is_refused_not_replaced() {
        let e = set_connection("- a\n- b\n", &local()).unwrap_err();
        assert_eq!(e.kind(), crate::error::Kind::Config);
        assert!(e.text().contains("not a mapping"), "{}", e.text());
    }

    #[test]
    fn removes_keys_left_empty() {
        let out = set_connection(
            "connection:\n  host: old\n  aws_region: eu-west-1\n  database: x\n",
            &[
                ("google_instance", Some("p:r:i".into())),
                ("database", Some("d".into())),
            ],
        )
        .unwrap();
        let cfg = ProjectConfig::parse(&out).unwrap();
        assert_eq!(cfg.connection.host, None);
        assert_eq!(cfg.connection.aws_region, None);
        assert_eq!(cfg.connection.google_instance.as_deref(), Some("p:r:i"));
        assert_eq!(cfg.connection.database.as_deref(), Some("d"));
    }

    #[test]
    fn odd_values_round_trip() {
        let values = vec![
            ("host", Some("my host:1".into())),
            ("database", Some("it's".into())),
            ("username", Some("true".into())),
            ("sslmode", Some("require".into())),
        ];
        for text in ["", "params:\n  a: b\n", "connection: ~\ntimeout: 1m\n"] {
            let out = set_connection(text, &values).unwrap();
            let cfg = ProjectConfig::parse(&out).unwrap_or_else(|e| panic!("{out}\n{}", e.text()));
            assert_eq!(cfg.connection.host.as_deref(), Some("my host:1"), "{out}");
            assert_eq!(cfg.connection.database.as_deref(), Some("it's"), "{out}");
            assert_eq!(cfg.connection.username.as_deref(), Some("true"), "{out}");
        }
    }

    #[test]
    fn a_write_leaves_another_writers_temp_file_alone_and_none_of_its_own() {
        let dir = crate::testdir::TempDir::new("edit-race");
        let path = dir.join("pgmi.yaml");
        let theirs = dir.join("pgmi.yaml.pgmi-tmp");
        std::fs::write(&theirs, "half written").unwrap();
        let ours = dir.join(format!("pgmi.yaml.pgmi-tmp.{}.0", std::process::id()));
        std::fs::write(&ours, "planted").unwrap();
        write_atomic(&path, "a: 1\n").unwrap();
        write_atomic(&path, "a: 2\n").unwrap();
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "a: 2\n");
        assert_eq!(std::fs::read_to_string(&theirs).unwrap(), "half written");
        assert_eq!(std::fs::read_to_string(&ours).unwrap(), "planted");
        let mut names: Vec<String> = std::fs::read_dir(dir.path())
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
            .collect();
        names.sort();
        assert_eq!(names.len(), 3, "{names:?}");
    }

    #[test]
    fn atomic_write_keeps_mode() {
        let dir = crate::testdir::TempDir::new("edit");
        let path = dir.join("pgmi.yaml");
        write_atomic(&path, "a: 1\n").unwrap();
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "a: 1\n");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
                0o600
            );
            std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).unwrap();
            write_atomic(&path, "a: 2\n").unwrap();
            assert_eq!(
                std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
                0o644
            );
        }
    }
}
