use std::collections::BTreeMap;
use std::path::Path;

use serde::Deserialize;

use crate::error::{Error, Result};

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct ProjectConfig {
    #[serde(default)]
    pub(crate) connection: Connection,
    #[serde(default)]
    pub(crate) params: BTreeMap<String, Option<String>>,
    pub(crate) timeout: Option<String>,
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
    pub(crate) sslcert: Option<String>,
    pub(crate) sslkey: Option<String>,
    pub(crate) sslrootcert: Option<String>,
    pub(crate) auth_method: Option<String>,
    pub(crate) azure_tenant_id: Option<String>,
    pub(crate) azure_client_id: Option<String>,
    pub(crate) aws_region: Option<String>,
    pub(crate) google_instance: Option<String>,
}

impl ProjectConfig {
    pub(crate) fn load(project: &Path) -> Result<Option<Self>> {
        let path = project.join("pgmi.yaml");
        let text = match std::fs::read_to_string(&path) {
            Ok(t) => t,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(e) => {
                return Err(Error::config("failed to load pgmi.yaml").with_source(e));
            }
        };
        Self::parse(&text).map(Some)
    }

    pub(crate) fn parse(text: &str) -> Result<Self> {
        if text.trim().is_empty() {
            return Ok(Self::default());
        }
        let cfg: ProjectConfig = serde_saphyr::from_str(text)
            .map_err(|e| Error::config(format!("failed to load pgmi.yaml: {e}")))?;
        if let Some(m) = &cfg.connection.auth_method
            && !matches!(m.as_str(), "azure" | "aws" | "google")
        {
            return Err(Error::config(format!(
                "failed to load pgmi.yaml: invalid auth_method {m:?}: expected azure, aws or google"
            )));
        }
        Ok(cfg)
    }

    pub(crate) fn timeout(&self) -> Result<Option<std::time::Duration>> {
        self.timeout
            .as_deref()
            .map(|t| {
                crate::cli::duration::parse(t)
                    .map_err(|e| Error::config(format!("failed to load pgmi.yaml: timeout: {e}")))
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
        assert!(e.text().contains("usernmae"), "{}", e.text());
        assert_eq!(e.exit_code(), 10);
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
}
