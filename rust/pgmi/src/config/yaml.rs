use std::collections::BTreeMap;

use serde::{Deserialize, Deserializer};

use crate::conn::cloud::Provider;
use crate::error::{Error, Result};
use crate::vfs::Vfs;

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct ProjectConfig {
    #[serde(default, deserialize_with = "null_is_empty")]
    pub(crate) connection: Connection,
    #[serde(default, deserialize_with = "null_is_empty")]
    pub(crate) params: BTreeMap<String, Option<String>>,
    pub(crate) timeout: Option<String>,
    pub(crate) retry: Option<bool>,
}

#[derive(Debug, Default, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Connection {
    pub(crate) host: Option<String>,
    pub(crate) port: Option<String>,
    pub(crate) username: Option<String>,
    pub(crate) database: Option<String>,
    pub(crate) maintenance_database: Option<String>,
    pub(crate) sslmode: Option<String>,
    /// The line of `sslmode:` in the block form, for error messages.
    #[serde(skip)]
    pub(crate) sslmode_line: Option<usize>,
    pub(crate) sslcert: Option<String>,
    pub(crate) sslkey: Option<String>,
    pub(crate) sslrootcert: Option<String>,
    /// `auth_method` as written; `parse` turns it into `auth_method`.
    #[serde(rename = "auth_method")]
    pub(crate) auth_method_text: Option<String>,
    #[serde(skip)]
    pub(crate) auth_method: Option<Provider>,
    pub(crate) azure_tenant_id: Option<String>,
    pub(crate) azure_client_id: Option<String>,
    pub(crate) aws_region: Option<String>,
    pub(crate) google_instance: Option<String>,
}

fn null_is_empty<'de, D, T>(d: D) -> std::result::Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

/// A port as Go's YAML reads an integer: `5432.0`, `0x1538` and `5_432`
/// are 5432; anything else is kept as written for the resolver to judge.
fn yaml_port(written: &str) -> std::result::Result<String, YamlError> {
    if written.is_empty() {
        return Err(YamlError::Port(written.to_string()));
    }
    let digits = written.replace('_', "");
    let read = match digits.strip_prefix("0x") {
        Some(hex) => u16::from_str_radix(hex, 16).ok(),
        None => digits
            .strip_suffix(".0")
            .unwrap_or(&digits)
            .parse::<u16>()
            .ok(),
    };
    Ok(read.map_or_else(|| written.to_string(), |p| p.to_string()))
}

/// Why `pgmi.yaml`'s text is not a project configuration.
#[derive(Debug)]
pub(crate) enum YamlError {
    Syntax(serde_saphyr::Error),
    AuthMethod(String),
    Port(String),
}

impl std::fmt::Display for YamlError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            YamlError::Syntax(e) => {
                let text = e.to_string();
                let first = text.lines().next().unwrap_or_default();
                f.write_str(
                    &first
                        .strip_prefix("error: ")
                        .unwrap_or(first)
                        .replace(", set DuplicateKeyPolicy in Options if acceptable", "")
                        .replace(
                            "; use from_str_multiple or from_str_multiple_with_options",
                            "",
                        ),
                )
            }
            YamlError::Port(text) => write!(
                f,
                "connection.port: invalid port {text:?}: expected a number"
            ),
            YamlError::AuthMethod(text) => {
                write!(
                    f,
                    "invalid auth_method {text:?}: expected azure, aws or google"
                )
            }
        }
    }
}

impl std::error::Error for YamlError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            YamlError::Syntax(e) => e.source(),
            YamlError::AuthMethod(_) | YamlError::Port(_) => None,
        }
    }
}

impl From<YamlError> for Error {
    fn from(e: YamlError) -> Self {
        Error::config("failed to load pgmi.yaml").with_source(e)
    }
}

fn auth_method(text: &str) -> std::result::Result<Provider, YamlError> {
    [Provider::Azure, Provider::Aws, Provider::Google]
        .into_iter()
        .find(|p| p.key() == text)
        .ok_or_else(|| YamlError::AuthMethod(text.to_string()))
}

fn sslmode_line(text: &str) -> Option<usize> {
    let mut in_connection = false;
    for (n, line) in text.lines().enumerate() {
        let body = line.trim_start();
        if body.is_empty() || body.starts_with('#') {
            continue;
        }
        if body.len() == line.len() {
            in_connection = line.starts_with("connection:");
        } else if in_connection && body.starts_with("sslmode:") {
            return Some(n + 1);
        }
    }
    None
}

impl ProjectConfig {
    pub(crate) fn load(project: &impl Vfs) -> Result<Option<Self>> {
        let text = match project.read_to_string("pgmi.yaml") {
            Ok(t) => t,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(e) => {
                return Err(Error::config("failed to load pgmi.yaml").with_source(e));
            }
        };
        Self::parse(&text).map(Some).map_err(|e| match e {
            YamlError::Syntax(_) => Error::config(format!(
                "failed to load pgmi.yaml: parse {}",
                project.display("pgmi.yaml")
            ))
            .with_source(e),
            other => other.into(),
        })
    }

    pub(crate) fn parse(text: &str) -> std::result::Result<Self, YamlError> {
        if text.trim().is_empty() {
            return Ok(Self::default());
        }
        let Some(mut cfg) =
            serde_saphyr::from_str::<Option<ProjectConfig>>(text).map_err(YamlError::Syntax)?
        else {
            return Ok(Self::default());
        };
        cfg.timeout = cfg.timeout.filter(|t| !t.is_empty());
        let c = &mut cfg.connection;
        c.port = c.port.as_deref().map(yaml_port).transpose()?;
        c.auth_method = c
            .auth_method_text
            .take()
            .as_deref()
            .map(auth_method)
            .transpose()?;
        c.sslmode_line = c.sslmode.as_ref().and_then(|_| sslmode_line(text));
        Ok(cfg)
    }

    pub(crate) fn timeout(&self) -> Result<Option<std::time::Duration>> {
        self.timeout
            .as_deref()
            .map(|t| {
                crate::duration::parse(t).map_err(|e| {
                    let go = if e.ends_with("must not be negative") {
                        ""
                    } else {
                        "time: "
                    };
                    Error::config(format!("invalid timeout in pgmi.yaml: {go}{e}"))
                })
            })
            .transpose()
    }

    pub(crate) fn param_values(&self) -> Vec<(String, String)> {
        self.params
            .iter()
            .map(|(k, v)| (k.clone(), v.clone().unwrap_or_default()))
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn null_documents_and_null_sections_mean_no_settings() {
        for text in [
            "---\n",
            "~\n",
            "connection:\n  # host: x\nparams:\n  # a: b\ntimeout:\n",
            "timeout: \"\"\n",
        ] {
            let cfg = ProjectConfig::parse(text).unwrap_or_else(|e| panic!("{text:?}: {e}"));
            assert!(
                cfg.params.is_empty() && cfg.connection.host.is_none() && cfg.timeout.is_none(),
                "{text:?}"
            );
        }
    }

    #[test]
    fn a_duplicate_key_names_its_line_without_the_library_s_advice() {
        let e = Error::from(
            ProjectConfig::parse("params:\n  greeting: one\n  greeting: two\n").unwrap_err(),
        );
        assert!(
            e.text().contains("greeting") && e.text().contains("line 3"),
            "{}",
            e.text()
        );
        assert!(!e.text().contains("DuplicateKeyPolicy"), "{}", e.text());
    }

    #[test]
    fn ports_go_reads_as_integers_are_read_the_same_and_an_empty_port_is_refused() {
        for (written, read) in [
            ("5432.0", "5432"),
            ("0x1538", "5432"),
            ("5_432", "5432"),
            ("\"5433\"", "5433"),
        ] {
            let cfg = ProjectConfig::parse(&format!("connection:\n  port: {written}\n")).unwrap();
            assert_eq!(cfg.connection.port.as_deref(), Some(read), "{written}");
        }
        let e = Error::from(ProjectConfig::parse("connection:\n  port: \"\"\n").unwrap_err());
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("port"), "{}", e.text());
    }

    #[test]
    fn a_timeout_without_a_unit_says_so() {
        let cfg = ProjectConfig::parse("timeout: 30\n").unwrap();
        let e = cfg.timeout().unwrap_err();
        assert!(
            e.text().contains("missing unit") && e.text().contains("\"30\""),
            "{}",
            e.text()
        );
    }

    #[test]
    fn param_values_as_written() {
        let cfg = ProjectConfig::parse(
            "params:\n  nothing: ~\n  empty: \"\"\n  integer: 42\n  float: 5432.0\n  boolean: true\n  quoted: \"007\"\n  text: plain text\n  folded: >\n    folded\n    text\n  literal: |\n    line one\n    line two\n",
        )
        .unwrap();
        let v: BTreeMap<_, _> = cfg.param_values().into_iter().collect();
        assert_eq!(v["nothing"], "");
        assert_eq!(v["empty"], "");
        assert_eq!(v["integer"], "42");
        assert_eq!(v["float"], "5432.0");
        assert_eq!(v["boolean"], "true");
        assert_eq!(v["quoted"], "007");
        assert_eq!(v["folded"], "folded text\n");
        assert_eq!(v["literal"], "line one\nline two\n");
    }

    #[test]
    fn unknown_key_refused() {
        let e = ProjectConfig::parse("connection:\n  usernmae: someone\n").unwrap_err();
        assert!(matches!(e, YamlError::Syntax(_)), "{e:?}");
        let reason = e.to_string();
        let e = Error::from(e);
        assert!(e.text().contains("usernmae"), "{}", e.text());
        assert_eq!(e.text(), format!("failed to load pgmi.yaml: {reason}"));
        assert_eq!(e.exit_code(), 10);
        let source = std::error::Error::source(&e).unwrap();
        assert!(
            matches!(
                source.downcast_ref::<YamlError>(),
                Some(YamlError::Syntax(_))
            ),
            "{source:?}"
        );
    }

    #[test]
    fn an_unknown_auth_method_reads_as_before() {
        let e = Error::from(ProjectConfig::parse("connection:\n  auth_method: gcp\n").unwrap_err());
        assert_eq!(
            e.text(),
            "failed to load pgmi.yaml: invalid auth_method \"gcp\": expected azure, aws or google"
        );
    }

    #[test]
    fn load_reads_pgmi_yaml_from_the_project() {
        use crate::vfs::MemFs;
        assert!(ProjectConfig::load(&MemFs::default()).unwrap().is_none());
        let empty = ProjectConfig::load(&MemFs::default().file("pgmi.yaml", "  \n"))
            .unwrap()
            .unwrap();
        assert!(empty.connection.host.is_none() && empty.params.is_empty());
        let empty = ProjectConfig::load(&MemFs::default().file("pgmi.yaml", ""))
            .unwrap()
            .unwrap();
        assert!(
            empty.connection.host.is_none() && empty.params.is_empty() && empty.timeout.is_none()
        );
        let cfg = ProjectConfig::load(&MemFs::default().file("pgmi.yaml", "timeout: 90s\n"))
            .unwrap()
            .unwrap();
        assert_eq!(
            cfg.timeout().unwrap(),
            Some(std::time::Duration::from_secs(90))
        );
        for (fs, text) in [
            (
                MemFs::default().file("pgmi.yaml", "connection: [unclosed\n"),
                "failed to load pgmi.yaml: ",
            ),
            (
                MemFs::default().file("pgmi.yaml", "{{invalid"),
                "failed to load pgmi.yaml: ",
            ),
            (
                MemFs::default().file("pgmi.yaml", "connection:\n  auth_method: kerberos\n"),
                "failed to load pgmi.yaml: invalid auth_method \"kerberos\": expected azure, aws or google",
            ),
            (
                MemFs::default().unreadable_file("pgmi.yaml"),
                "failed to load pgmi.yaml: ",
            ),
            (
                MemFs::default().file("pgmi.yaml", b"timeout: \xff\n"),
                "failed to load pgmi.yaml: stream did not contain valid UTF-8",
            ),
        ] {
            let e = ProjectConfig::load(&fs).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{}", e.text());
            assert!(e.text().starts_with(text), "{}", e.text());
            if text.contains("auth_method") {
                assert_eq!(e.text(), text);
            }
        }
    }

    #[test]
    fn sslmode_line_is_found_in_the_connection_block() {
        let cfg = ProjectConfig::parse(
            "# c\nparams:\n  sslmode: x\nconnection:\n  host: h\n  # sslmode: verify-full\n  sslmode: prefer   # x\n",
        )
        .unwrap();
        assert_eq!(cfg.connection.sslmode_line, Some(7));
        let cfg = ProjectConfig::parse("connection: {sslmode: prefer}\n").unwrap();
        assert_eq!(cfg.connection.sslmode_line, None);
    }

    #[test]
    fn template_file() {
        let cfg = ProjectConfig::parse(
            "connection:\n  database: mydb # x\n  port: 5432\n  sslmode: prefer\ntimeout: 3m\n",
        )
        .unwrap();
        assert_eq!(cfg.connection.port.as_deref(), Some("5432"));
        assert_eq!(cfg.timeout.as_deref(), Some("3m"));
    }

    #[test]
    fn every_field_is_read() {
        let cfg = ProjectConfig::parse(
            "connection:\n  host: myhost\n  port: 5433\n  username: myuser\n  database: mydb\n  sslmode: require\n  sslcert: /path/client.crt\n  sslkey: /path/client.key\n  sslrootcert: /path/ca.crt\n\nparams:\n  env: production\n  region: us-west\n\ntimeout: 10m\n",
        )
        .unwrap();
        let c = &cfg.connection;
        assert_eq!(c.host.as_deref(), Some("myhost"));
        assert_eq!(c.port.as_deref(), Some("5433"));
        assert_eq!(c.username.as_deref(), Some("myuser"));
        assert_eq!(c.database.as_deref(), Some("mydb"));
        assert_eq!(c.sslmode.as_deref(), Some("require"));
        assert_eq!(c.sslcert.as_deref(), Some("/path/client.crt"));
        assert_eq!(c.sslkey.as_deref(), Some("/path/client.key"));
        assert_eq!(c.sslrootcert.as_deref(), Some("/path/ca.crt"));
        assert_eq!(
            cfg.param_values(),
            [
                ("env".to_string(), "production".to_string()),
                ("region".to_string(), "us-west".to_string())
            ]
        );
        assert_eq!(
            cfg.timeout().unwrap(),
            Some(std::time::Duration::from_secs(600))
        );
        let minimal = ProjectConfig::parse("params:\n  env: development\n").unwrap();
        assert!(minimal.connection.host.is_none() && minimal.connection.port.is_none());
        assert_eq!(
            minimal.param_values(),
            [("env".to_string(), "development".to_string())]
        );
    }
}
