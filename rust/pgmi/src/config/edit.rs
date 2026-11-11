use std::io::Write;
use std::path::Path;
use std::str::FromStr;

use yaml_edit::Document;

use crate::error::{Error, Result};

pub const KEYS: [&str; 14] = [
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

pub fn fresh(values: &[(&str, String)]) -> String {
    let mut out = String::from("connection:\n");
    for (k, v) in values.iter().filter(|(_, v)| !v.is_empty()) {
        out.push_str(&format!("  {k}: {}\n", scalar(v)));
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

pub fn set_connection(text: &str, values: &[(&str, String)]) -> String {
    if text.trim().is_empty() {
        return fresh(values);
    }
    let Some(root) = Document::from_str(text).ok().and_then(|d| {
        let m = d.as_mapping()?;
        Some((d, m))
    }) else {
        return fresh(values);
    };
    let (doc, root) = root;
    let Some(section) = root.get_mapping("connection") else {
        root.remove("connection");
        return format!("{}{}", fresh(values), doc);
    };
    let mut added = Vec::new();
    for key in KEYS {
        match values.iter().find(|(k, v)| *k == key && !v.is_empty()) {
            Some((_, v)) => {
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
    out.split_inclusive('\n')
        .filter(|line| !added.iter().any(|k| placeholder(line, k)))
        .collect()
}

fn placeholder(line: &str, key: &str) -> bool {
    line.starts_with([' ', '\t'])
        && line
            .trim_start()
            .strip_prefix('#')
            .is_some_and(|rest| rest.trim_start().starts_with(&format!("{key}:")))
}

pub fn write_atomic(path: &Path, text: &str) -> Result<()> {
    write(path, text, true)
}

pub fn write_private(path: &Path, text: &str) -> Result<()> {
    write(path, text, false)
}

fn write(path: &Path, text: &str, keep_mode: bool) -> Result<()> {
    let fail = |what: &str, e: std::io::Error| {
        Error::general(format!("{what} {}", path.display())).with_source(e)
    };
    let mut tmp = path.as_os_str().to_owned();
    tmp.push(".pgmi-tmp");
    let tmp = std::path::PathBuf::from(tmp);
    let _ = std::fs::remove_file(&tmp);
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
    let written = options.open(&tmp).and_then(|mut f| {
        f.write_all(text.as_bytes())?;
        f.sync_all()
    });
    if let Err(e) = written {
        let _ = std::fs::remove_file(&tmp);
        return Err(fail("write", e));
    }
    std::fs::rename(&tmp, path).map_err(|e| {
        let _ = std::fs::remove_file(&tmp);
        fail("update", e)
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::yaml::ProjectConfig;

    fn local() -> Vec<(&'static str, String)> {
        vec![
            ("host", "127.0.0.1".into()),
            ("port", "55611".into()),
            ("username", "postgres".into()),
            ("database", "app".into()),
            ("maintenance_database", "postgres".into()),
            ("sslmode", "prefer".into()),
        ]
    }

    #[test]
    fn keeps_comments_and_params() {
        let out = set_connection(
            "# Team notes\nparams:\n  env: dev   # inline note\n",
            &local(),
        );
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
        let out = set_connection(&template, &local());
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
    fn removes_keys_left_empty() {
        let out = set_connection(
            "connection:\n  host: old\n  aws_region: eu-west-1\n  database: x\n",
            &[
                ("google_instance", "p:r:i".into()),
                ("database", "d".into()),
            ],
        );
        let cfg = ProjectConfig::parse(&out).unwrap();
        assert_eq!(cfg.connection.host, None);
        assert_eq!(cfg.connection.aws_region, None);
        assert_eq!(cfg.connection.google_instance.as_deref(), Some("p:r:i"));
        assert_eq!(cfg.connection.database.as_deref(), Some("d"));
    }

    #[test]
    fn odd_values_round_trip() {
        let values = vec![
            ("host", "my host:1".into()),
            ("database", "it's".into()),
            ("username", "true".into()),
            ("sslmode", "require".into()),
        ];
        for text in ["", "params:\n  a: b\n", "connection: ~\ntimeout: 1m\n"] {
            let out = set_connection(text, &values);
            let cfg = ProjectConfig::parse(&out).unwrap_or_else(|e| panic!("{out}\n{}", e.text()));
            assert_eq!(cfg.connection.host.as_deref(), Some("my host:1"), "{out}");
            assert_eq!(cfg.connection.database.as_deref(), Some("it's"), "{out}");
            assert_eq!(cfg.connection.username.as_deref(), Some("true"), "{out}");
        }
    }

    #[test]
    fn atomic_write_keeps_mode() {
        let dir = std::env::temp_dir().join(format!("pgmi-edit-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
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
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
