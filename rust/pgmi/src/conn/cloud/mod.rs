mod aws;
mod azure;
mod azure_msi;
mod databricks;
mod google;
mod http;

use std::sync::Arc;

use crate::config::yaml::ProjectConfig;
use crate::conn::resolve::{Spec, SslMode};
use crate::env::Env;
use crate::error::{Error, Result, Secret};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Provider {
    Azure,
    Aws,
    Google,
    Databricks,
}

impl Provider {
    /// The name `pgmi.yaml`'s `auth_method` uses.
    pub(crate) fn key(self) -> &'static str {
        match self {
            Provider::Azure => "azure",
            Provider::Aws => "aws",
            Provider::Google => "google",
            Provider::Databricks => "databricks",
        }
    }

    pub(crate) fn label(self) -> &'static str {
        match self {
            Provider::Azure => "Azure Entra ID",
            Provider::Aws => "AWS IAM",
            Provider::Google => "Google Cloud SQL IAM",
            Provider::Databricks => "Databricks Lakebase",
        }
    }
}

#[derive(Debug, Clone, Default)]
pub(crate) struct Options {
    pub(crate) tenant_id: Option<String>,
    pub(crate) client_id: Option<String>,
    pub(crate) aws_region: Option<String>,
    pub(crate) google_instance: Option<String>,
    pub(crate) databricks_endpoint: Option<String>,
}

#[derive(Clone)]
pub(crate) enum Token {
    Fixed(Provider, Secret),
    Aws(Arc<aws::Signer>),
    Google(Arc<google::Dialer>),
    Databricks(Arc<databricks::Lakebase>),
}

impl std::fmt::Debug for Token {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Token({})", self.provider().label())
    }
}

impl Token {
    pub(crate) fn provider(&self) -> Provider {
        match self {
            Token::Fixed(p, _) => *p,
            Token::Aws(_) => Provider::Aws,
            Token::Google(_) => Provider::Google,
            Token::Databricks(_) => Provider::Databricks,
        }
    }

    pub(crate) async fn password(
        &self,
        host: &str,
        port: u16,
        user: &str,
    ) -> std::result::Result<String, String> {
        match self {
            Token::Fixed(_, t) => Ok(t.expose().to_string()),
            Token::Aws(s) => s.token(host, port, user),
            Token::Google(d) => Ok(d.password()),
            Token::Databricks(l) => l.password().await,
        }
    }

    pub(crate) fn secrets(&self) -> Vec<String> {
        match self {
            Token::Fixed(_, t) => vec![t.expose().to_string()],
            Token::Aws(s) => s.minted(),
            Token::Google(d) => vec![d.password()],
            Token::Databricks(l) => l.minted(),
        }
    }

    pub(crate) fn google(&self) -> Option<&Arc<google::Dialer>> {
        match self {
            Token::Google(d) => Some(d),
            _ => None,
        }
    }
}

pub(crate) fn select(
    asked: &[Provider],
    req: &Options,
    env: &Env,
    yaml: Option<&ProjectConfig>,
) -> Result<Option<(Provider, Options)>> {
    if asked.len() > 1 {
        let choices = if asked.contains(&Provider::Databricks) {
            "--azure, --aws, --google, or --databricks"
        } else {
            "--azure, --aws, or --google"
        };
        return Err(Error::config(format!(
            "cannot use multiple cloud authentication methods; choose one of {choices}"
        )));
    }
    let y = yaml.map(|y| y.connection.clone()).unwrap_or_default();
    let Some(provider) = asked.first().copied().or(y.auth_method) else {
        return Ok(None);
    };
    let pick = |flag: &Option<String>, names: &[&str], yaml: Option<String>| -> Result<_> {
        if let Some(v) = flag.as_deref().filter(|v| !v.is_empty()) {
            return Ok(Some(v.to_string()));
        }
        for name in names {
            if let Some(v) = env.non_empty(name)? {
                return Ok(Some(v.to_string()));
            }
        }
        Ok(yaml)
    };
    Ok(Some((
        provider,
        Options {
            tenant_id: pick(&req.tenant_id, &["AZURE_TENANT_ID"], y.azure_tenant_id)?,
            client_id: pick(&req.client_id, &["AZURE_CLIENT_ID"], y.azure_client_id)?,
            aws_region: pick(
                &req.aws_region,
                &["AWS_REGION", "AWS_DEFAULT_REGION"],
                y.aws_region,
            )?,
            google_instance: req.google_instance.clone().or(y.google_instance),
            databricks_endpoint: req.databricks_endpoint.clone(),
        },
    )))
}

pub(crate) fn check(provider: Provider, req: &Options, spec: &mut Spec, env: &Env) -> Result<()> {
    match provider {
        Provider::Google => {
            if req.google_instance.as_deref().is_none_or(str::is_empty) {
                return Err(Error::config(
                    "Google Cloud SQL IAM auth requires --google-instance (project:region:instance)",
                ));
            }
            return Ok(());
        }
        Provider::Aws => {
            aws::region(req, env)?;
        }
        Provider::Databricks => {
            databricks::settings(req, env)?;
        }
        Provider::Azure => {}
    }
    match spec.requested_sslmode {
        None => {
            spec.requested_sslmode = Some(match provider {
                Provider::Aws if spec.sslrootcert.is_none() => SslMode::Require,
                _ => SslMode::VerifyFull,
            });
            Ok(())
        }
        Some(mode) if mode.allows_plaintext() => {
            Err(Error::config(match spec.sslmode_source.yaml_line() {
                Some(line) => format!(
                    "sslmode \"{mode}\" from pgmi.yaml line {line} is not allowed with {} authentication: it can send the token without TLS; change that line to \"sslmode: verify-full\", or delete it to use the default for token sign-in",
                    provider.label()
                ),
                None => format!(
                    "sslmode \"{mode}\" from {} is not allowed with {} authentication: it can send the token without TLS; use require, verify-ca or verify-full",
                    spec.sslmode_source,
                    provider.label()
                ),
            }))
        }
        Some(_) => Ok(()),
    }
}

pub(crate) async fn token(provider: Provider, req: &Options, env: &Env) -> Result<Token> {
    crate::conn::tls::require_cpu()?;
    match provider {
        Provider::Azure => azure::token(req, env, azure::POSTGRES)
            .await
            .map(|t| Token::Fixed(Provider::Azure, Secret::from(t))),
        Provider::Aws => aws::signer(req, env).await.map(|s| Token::Aws(Arc::new(s))),
        Provider::Google => google::dialer(req, env)
            .await
            .map(|d| Token::Google(Arc::new(d))),
        Provider::Databricks => databricks::lakebase(req, env)
            .await
            .map(|l| Token::Databricks(Arc::new(l))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn yaml(text: &str) -> ProjectConfig {
        ProjectConfig::parse(text).unwrap()
    }

    #[test]
    fn two_providers_are_refused() {
        let e = select(
            &[Provider::Azure, Provider::Aws],
            &Options::default(),
            &Env::default(),
            None,
        )
        .unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert_eq!(
            e.text(),
            "cannot use multiple cloud authentication methods; choose one of --azure, --aws, or --google"
        );
        let e = select(
            &[Provider::Databricks, Provider::Google],
            &Options::default(),
            &Env::default(),
            None,
        )
        .unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(
            e.text()
                .ends_with("choose one of --azure, --aws, --google, or --databricks"),
            "{}",
            e.text()
        );
    }

    #[test]
    fn no_provider_anywhere_is_none() {
        assert!(
            select(&[], &Options::default(), &Env::default(), None)
                .unwrap()
                .is_none()
        );
        let y = yaml("connection:\n  aws_region: eu-west-1\n");
        assert!(
            select(&[], &Options::default(), &Env::default(), Some(&y))
                .unwrap()
                .is_none()
        );
    }

    #[test]
    fn a_flag_beats_the_yaml_auth_method_and_flag_values_beat_yaml_values() {
        let y = yaml(
            "connection:\n  auth_method: azure\n  aws_region: from-yaml\n  azure_tenant_id: t\n",
        );
        let asked = Options {
            aws_region: Some("from-flag".into()),
            ..Options::default()
        };
        let (p, r) = select(&[Provider::Aws], &asked, &Env::default(), Some(&y))
            .unwrap()
            .unwrap();
        assert_eq!(p, Provider::Aws);
        assert_eq!(r.aws_region.as_deref(), Some("from-flag"));
        assert_eq!(r.tenant_id.as_deref(), Some("t"));
    }

    #[test]
    fn the_yaml_auth_method_applies_without_a_flag() {
        let y = yaml("connection:\n  auth_method: google\n  google_instance: p:r:i\n");
        let (p, r) = select(&[], &Options::default(), &Env::default(), Some(&y))
            .unwrap()
            .unwrap();
        assert_eq!(p, Provider::Google);
        assert_eq!(r.google_instance.as_deref(), Some("p:r:i"));
    }

    #[test]
    fn the_environment_beats_yaml_values_and_a_flag_beats_both() {
        let y = yaml(
            "connection:\n  auth_method: azure\n  azure_tenant_id: yaml-t\n  azure_client_id: yaml-c\n  aws_region: yaml-r\n",
        );
        let mut env = Env::default();
        env.insert("AZURE_TENANT_ID", "env-t");
        env.insert("AZURE_CLIENT_ID", "env-c");
        env.insert("AWS_DEFAULT_REGION", "env-default-r");
        let (_, r) = select(&[], &Options::default(), &env, Some(&y))
            .unwrap()
            .unwrap();
        assert_eq!(r.tenant_id.as_deref(), Some("env-t"));
        assert_eq!(r.client_id.as_deref(), Some("env-c"));
        assert_eq!(r.aws_region.as_deref(), Some("env-default-r"));
        env.insert("AWS_REGION", "env-r");
        let asked = Options {
            tenant_id: Some("flag-t".into()),
            client_id: Some(String::new()),
            ..Options::default()
        };
        let (_, r) = select(&[], &asked, &env, Some(&y)).unwrap().unwrap();
        assert_eq!(r.tenant_id.as_deref(), Some("flag-t"));
        assert_eq!(r.client_id.as_deref(), Some("env-c"));
        assert_eq!(r.aws_region.as_deref(), Some("env-r"));
    }
}
