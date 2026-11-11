mod aws;
mod azure;
mod azure_msi;
mod google;
mod http;

use std::sync::Arc;

use crate::cli::DeployArgs;
use crate::config::yaml::ProjectConfig;
use crate::conn::resolve::Spec;
use crate::env::Env;
use crate::error::{Error, Result};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Provider {
    Azure,
    Aws,
    Google,
}

impl Provider {
    pub fn label(self) -> &'static str {
        match self {
            Provider::Azure => "Azure Entra ID",
            Provider::Aws => "AWS IAM",
            Provider::Google => "Google Cloud SQL IAM",
        }
    }
}

#[derive(Debug, Clone, Default)]
pub struct Request {
    pub tenant_id: Option<String>,
    pub client_id: Option<String>,
    pub aws_region: Option<String>,
    pub google_instance: Option<String>,
}

#[derive(Clone)]
pub enum Token {
    Fixed(Provider, String),
    Aws(Arc<aws::Signer>),
    Google(Arc<google::Dialer>),
}

impl std::fmt::Debug for Token {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Token({})", self.provider().label())
    }
}

impl Token {
    pub fn provider(&self) -> Provider {
        match self {
            Token::Fixed(p, _) => *p,
            Token::Aws(_) => Provider::Aws,
            Token::Google(_) => Provider::Google,
        }
    }

    pub fn password(
        &self,
        host: &str,
        port: u16,
        user: &str,
    ) -> std::result::Result<String, String> {
        match self {
            Token::Fixed(_, t) => Ok(t.clone()),
            Token::Aws(s) => s.token(host, port, user),
            Token::Google(d) => Ok(d.password()),
        }
    }

    pub fn fixed(&self) -> Option<&str> {
        match self {
            Token::Fixed(_, t) => Some(t),
            _ => None,
        }
    }

    pub fn google(&self) -> Option<&Arc<google::Dialer>> {
        match self {
            Token::Google(d) => Some(d),
            _ => None,
        }
    }
}

pub fn select(a: &DeployArgs, yaml: Option<&ProjectConfig>) -> Result<Option<(Provider, Request)>> {
    let flags = [a.azure, a.aws, a.google].iter().filter(|f| **f).count();
    if flags > 1 {
        return Err(Error::config(
            "cannot use multiple cloud authentication methods; choose one of --azure, --aws, or --google",
        ));
    }
    let y = yaml.map(|y| y.connection.clone()).unwrap_or_default();
    let provider = if a.azure {
        Provider::Azure
    } else if a.aws {
        Provider::Aws
    } else if a.google {
        Provider::Google
    } else {
        match y.auth_method.as_deref() {
            Some("azure") => Provider::Azure,
            Some("aws") => Provider::Aws,
            Some("google") => Provider::Google,
            _ => return Ok(None),
        }
    };
    Ok(Some((
        provider,
        Request {
            tenant_id: a.azure_tenant_id.clone().or(y.azure_tenant_id),
            client_id: a.azure_client_id.clone().or(y.azure_client_id),
            aws_region: a.aws_region.clone().or(y.aws_region),
            google_instance: a.google_instance.clone().or(y.google_instance),
        },
    )))
}

pub fn check(provider: Provider, req: &Request, spec: &mut Spec, env: &Env) -> Result<()> {
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
        Provider::Azure => {}
    }
    if !spec.sslmode_set {
        spec.sslmode = match provider {
            Provider::Aws if spec.sslrootcert.is_none() => "require".into(),
            _ => "verify-full".into(),
        };
        return Ok(());
    }
    if matches!(spec.sslmode.as_str(), "disable" | "allow" | "prefer") {
        return Err(Error::config(format!(
            "sslmode \"{}\" from {} is not allowed with {} authentication: it can send the token without TLS; use require, verify-ca or verify-full",
            spec.sslmode,
            spec.source.label(),
            provider.label()
        )));
    }
    Ok(())
}

pub async fn token(provider: Provider, req: &Request, env: &Env) -> Result<Token> {
    match provider {
        Provider::Azure => azure::token(req, env)
            .await
            .map(|t| Token::Fixed(Provider::Azure, t)),
        Provider::Aws => aws::signer(req, env).await.map(|s| Token::Aws(Arc::new(s))),
        Provider::Google => google::dialer(req, env)
            .await
            .map(|d| Token::Google(Arc::new(d))),
    }
}
