mod aws;
mod azure;
mod azure_msi;
mod databricks;
mod google;
mod http;
mod renew;

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
    #[cfg(test)]
    Fixed(Provider, Secret),
    #[cfg(test)]
    Stalled(Provider, u16),
    #[cfg(test)]
    Failing(Provider),
    Azure(Arc<renew::Renewing<azure::Signin>>),
    Aws(Arc<aws::Signer>),
    Google(Arc<renew::Renewing<google::Signin>>),
    Databricks(Arc<databricks::Lakebase>),
}

impl std::fmt::Debug for Token {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Token({})", self.provider().label())
    }
}

/// What one connection presents: the password, and for Cloud SQL the
/// route its sockets are opened through.
pub(crate) struct Login {
    pub(crate) password: Secret,
    pub(crate) cloud_sql: Option<CloudSql>,
}

impl Login {
    pub(crate) fn password(password: Secret) -> Self {
        Login {
            password,
            cloud_sql: None,
        }
    }
}

/// Where a Cloud SQL socket is opened: through the session current when it
/// is opened, so a cancel request an hour into a deploy presents a renewed
/// client certificate, not the one the connection started with.
#[derive(Clone)]
pub(crate) enum CloudSql {
    Signin(Arc<renew::Renewing<google::Signin>>),
    #[cfg(test)]
    Minted(Arc<renew::Renewing<google::tests::Sessions>>),
}

impl CloudSql {
    async fn session(&self) -> Result<Arc<google::Session>> {
        match self {
            CloudSql::Signin(r) => r.valid().await,
            #[cfg(test)]
            CloudSql::Minted(r) => r.valid().await,
        }
    }

    /// Opens a TLS socket to the instance with the current session.
    pub(crate) async fn dial(
        &self,
        tcp: &crate::conn::socket::Tcp,
    ) -> std::io::Result<tokio_rustls::client::TlsStream<tokio::net::TcpStream>> {
        let session = self.session().await.map_err(std::io::Error::other)?;
        session.dial(tcp).await
    }
}

/// Why a sign-in token for one connection could not be had.
#[derive(Debug)]
pub(crate) enum TokenError {
    Aws(aws::SignError),
    SignIn(Error),
}

impl std::fmt::Display for TokenError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            TokenError::Aws(e) => e.fmt(f),
            TokenError::SignIn(e) => e.fmt(f),
        }
    }
}

impl std::error::Error for TokenError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            TokenError::Aws(e) => Some(e),
            TokenError::SignIn(e) => Some(e),
        }
    }
}

impl Token {
    pub(crate) fn provider(&self) -> Provider {
        match self {
            #[cfg(test)]
            Token::Fixed(p, _) | Token::Stalled(p, _) | Token::Failing(p) => *p,
            Token::Azure(_) => Provider::Azure,
            Token::Aws(_) => Provider::Aws,
            Token::Google(_) => Provider::Google,
            Token::Databricks(_) => Provider::Databricks,
        }
    }

    /// Signs in for one connection, renewing an expired credential first.
    pub(crate) async fn login(
        &self,
        host: &str,
        port: u16,
        user: &str,
    ) -> std::result::Result<Login, TokenError> {
        match self {
            #[cfg(test)]
            Token::Fixed(_, t) => Ok(Login::password(t.clone())),
            #[cfg(test)]
            Token::Stalled(_, stalled) if *stalled == port => std::future::pending().await,
            #[cfg(test)]
            Token::Stalled(..) => Ok(Login::password(Secret::from("token"))),
            #[cfg(test)]
            Token::Failing(_) => Err(TokenError::SignIn(Error::connection("sign-in refused"))),
            Token::Azure(a) => a
                .current()
                .await
                .map(Login::password)
                .map_err(TokenError::SignIn),
            Token::Aws(s) => s.token(host, port, user).await.map(Login::password),
            Token::Google(g) => {
                let session = g.current().await.map_err(TokenError::SignIn)?;
                Ok(Login {
                    password: session.password().clone(),
                    cloud_sql: Some(CloudSql::Signin(g.clone())),
                })
            }
            Token::Databricks(l) => l
                .current()
                .await
                .map(Login::password)
                .map_err(TokenError::SignIn),
        }
    }

    /// Whether connections go through the Cloud SQL dialer instead of the
    /// host and port.
    pub(crate) fn cloud_sql(&self) -> bool {
        matches!(self, Token::Google(_))
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
        Provider::Azure => renew::Renewing::start(azure::Signin::new(
            req.clone(),
            env.clone(),
            azure::POSTGRES,
        ))
        .await
        .map(|a| Token::Azure(Arc::new(a))),
        Provider::Aws => aws::signer(req, env).await.map(|s| Token::Aws(Arc::new(s))),
        Provider::Google => {
            let signin = Arc::new(renew::Renewing::start(google::Signin::new(req, env)?).await?);
            signin.renew_ahead();
            Ok(Token::Google(signin))
        }
        Provider::Databricks => databricks::lakebase(req, env)
            .await
            .map(|l| Token::Databricks(Arc::new(l))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    async fn minted_on_dial(expires: jiff::Timestamp) -> usize {
        crate::conn::tls::install();
        let renewing = Arc::new(renew::Renewing::holding(
            google::tests::Sessions::default(),
            renew::Fetched {
                value: google::tests::session(),
                expires: Some(expires),
            },
        ));
        let route = CloudSql::Minted(renewing.clone());
        let _ = route.dial(&crate::conn::socket::Tcp::default()).await;
        renewing
            .mint_source()
            .minted
            .load(std::sync::atomic::Ordering::SeqCst)
    }

    #[tokio::test]
    async fn a_cancel_uses_the_certificate_in_hand_until_it_expires() {
        let renewing = Arc::new(renew::Renewing::holding(
            google::tests::Sessions {
                stalled: true,
                ..Default::default()
            },
            renew::Fetched {
                value: google::tests::session(),
                expires: Some(jiff::Timestamp::now() + jiff::SignedDuration::from_mins(3)),
            },
        ));
        let route = CloudSql::Minted(renewing.clone());
        tokio::time::timeout(std::time::Duration::from_secs(1), route.session())
            .await
            .expect("the cancel waited for a new sign-in")
            .unwrap();
        assert_eq!(
            renewing
                .mint_source()
                .minted
                .load(std::sync::atomic::Ordering::SeqCst),
            0
        );
    }

    #[tokio::test]
    async fn a_cloud_sql_socket_opened_after_the_certificate_expired_uses_a_renewed_one() {
        let now = jiff::Timestamp::now();
        assert_eq!(
            minted_on_dial(now - jiff::SignedDuration::from_mins(1)).await,
            1
        );
        assert_eq!(
            minted_on_dial(now + jiff::SignedDuration::from_hours(1)).await,
            0
        );
    }

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
