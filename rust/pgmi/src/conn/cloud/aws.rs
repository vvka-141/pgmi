use std::fmt::Write as _;
use std::time::{Duration, SystemTime};

use aws_config::{BehaviorVersion, Region};
use aws_credential_types::Credentials;
use aws_credential_types::provider::{ProvideCredentials, SharedCredentialsProvider};
use aws_sigv4::http_request::{
    SignableBody, SignableRequest, SignatureLocation, SigningSettings, sign,
};
use aws_sigv4::sign::v4;
use aws_smithy_runtime_api::client::http::{
    HttpClient, HttpConnector, HttpConnectorFuture, HttpConnectorSettings, SharedHttpClient,
    SharedHttpConnector,
};
use aws_smithy_runtime_api::client::orchestrator::{HttpRequest, HttpResponse};
use aws_smithy_runtime_api::client::result::ConnectorError;
use aws_smithy_runtime_api::client::runtime_components::RuntimeComponents;
use aws_smithy_types::body::SdkBody;

use super::http::{client, encode};
use super::renew::{Fetched, Mint, Renewing};
use super::{Options, TokenError};
use crate::env::Env;
use crate::error::{Error, Result, Secret};

/// The RDS auth token could not be signed.
#[derive(Debug)]
pub(crate) struct SignError(Box<dyn std::error::Error + Send + Sync>);

impl std::fmt::Display for SignError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "failed to build RDS auth token: {}", self.0)
    }
}

impl std::error::Error for SignError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(self.0.as_ref())
    }
}

fn signing(e: impl std::error::Error + Send + Sync + 'static) -> SignError {
    SignError(Box::new(e))
}

/// Signs an RDS auth token for each connection with the default credential
/// chain's credentials, asking the chain again once they are within five
/// minutes of expiring (always, for credentials without an expiry), as Go
/// loads them per connection.
pub(crate) struct Signer {
    credentials: Renewing<Chain>,
    region: String,
}

/// The default credential chain's provider.
pub(crate) struct Chain(SharedCredentialsProvider);

impl Mint for Chain {
    type Value = Credentials;

    async fn mint(&self) -> Result<Fetched<Credentials>> {
        let credentials = self
            .0
            .provide_credentials()
            .await
            .map_err(|e| Error::connection("failed to acquire AWS IAM token").with_source(e))?;
        Ok(Fetched {
            expires: credentials
                .expiry()
                .and_then(|t| jiff::Timestamp::try_from(t).ok()),
            value: credentials,
        })
    }
}

pub(crate) fn region(req: &Options, env: &Env) -> Result<String> {
    if let Some(r) = req.aws_region.as_deref().filter(|r| !r.is_empty()) {
        return Ok(r.to_string());
    }
    for name in ["AWS_REGION", "AWS_DEFAULT_REGION"] {
        if let Some(r) = env.non_empty(name)? {
            return Ok(r.to_string());
        }
    }
    Err(Error::config(
        "AWS IAM auth requires region (use --aws-region or $AWS_REGION)",
    ))
}

pub(crate) async fn signer(req: &Options, env: &Env) -> Result<Signer> {
    let region = region(req, env)?;
    let http = SharedHttpClient::new(Reqwest(client()?));
    let config = aws_config::defaults(BehaviorVersion::latest())
        .region(Region::new(region.clone()))
        .http_client(http)
        .load()
        .await;
    let provider = config.credentials_provider().ok_or_else(|| {
        Error::connection("failed to acquire AWS IAM token: no credentials provider")
    })?;
    Ok(Signer {
        credentials: Renewing::start(Chain(provider)).await?,
        region,
    })
}

impl Signer {
    pub(crate) async fn token(
        &self,
        host: &str,
        port: u16,
        user: &str,
    ) -> std::result::Result<Secret, TokenError> {
        let credentials = self
            .credentials
            .current()
            .await
            .map_err(TokenError::SignIn)?;
        sign_token(
            &credentials,
            &self.region,
            host,
            port,
            user,
            SystemTime::now(),
        )
        .map_err(TokenError::Aws)
    }
}

fn sign_token(
    credentials: &Credentials,
    region: &str,
    host: &str,
    port: u16,
    user: &str,
    time: SystemTime,
) -> std::result::Result<Secret, SignError> {
    let query = format!("Action=connect&DBUser={}", encode(user));
    let url = format!("https://{host}:{port}/?{query}");
    let mut settings = SigningSettings::default();
    settings.expires_in = Some(Duration::from_mins(15));
    settings.signature_location = SignatureLocation::QueryParams;
    let identity = credentials.clone().into();
    let params = v4::SigningParams::builder()
        .identity(&identity)
        .region(region)
        .name("rds-db")
        .time(time)
        .settings(settings)
        .build()
        .map_err(signing)?;
    let signable = SignableRequest::new("GET", &url, std::iter::empty(), SignableBody::Bytes(&[]))
        .map_err(signing)?;
    let (instructions, _) = sign(signable, &params.into())
        .map_err(signing)?
        .into_parts();
    let mut token = format!("{host}:{port}?{query}");
    for (name, value) in instructions.params() {
        let _ = write!(token, "&{name}={}", encode(value));
    }
    Ok(Secret::from(token))
}

#[derive(Debug, Clone)]
struct Reqwest(reqwest::Client);

impl HttpClient for Reqwest {
    fn http_connector(
        &self,
        _: &HttpConnectorSettings,
        _: &RuntimeComponents,
    ) -> SharedHttpConnector {
        SharedHttpConnector::new(Connector(self.0.clone()))
    }
}

#[derive(Debug)]
struct Connector(reqwest::Client);

impl HttpConnector for Connector {
    fn call(&self, request: HttpRequest) -> HttpConnectorFuture {
        let client = self.0.clone();
        HttpConnectorFuture::new(async move {
            let other =
                |e: Box<dyn std::error::Error + Send + Sync>| ConnectorError::other(e, None);
            let req = request.try_into_http1x().map_err(|e| other(e.into()))?;
            let (parts, body) = req.into_parts();
            let bytes = body.bytes().map(<[u8]>::to_vec).unwrap_or_default();
            let resp = client
                .request(parts.method, parts.uri.to_string())
                .headers(parts.headers)
                .body(bytes)
                .send()
                .await
                .map_err(|e| {
                    if e.is_timeout() {
                        ConnectorError::timeout(e.into())
                    } else {
                        ConnectorError::io(e.into())
                    }
                })?;
            let status = resp.status();
            let headers = resp.headers().clone();
            let body = resp
                .bytes()
                .await
                .map_err(|e| ConnectorError::io(e.into()))?;
            let mut out = http::Response::builder()
                .status(status)
                .body(SdkBody::from(body))
                .map_err(|e| other(e.into()))?;
            *out.headers_mut() = headers;
            HttpResponse::try_from(out).map_err(|e| other(e.into()))
        })
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;
    use std::sync::atomic::{AtomicUsize, Ordering};

    use aws_credential_types::provider::future;

    use super::*;

    #[test]
    fn token_shape() {
        let t = sign_token(
            &Credentials::new("AKIDEXAMPLE", "secret", None, None, "test"),
            "us-west-2",
            "127.0.0.1",
            5432,
            "iam user",
            SystemTime::UNIX_EPOCH + Duration::from_secs(1_700_000_000),
        )
        .unwrap();
        let t = t.expose();
        assert!(
            t.starts_with("127.0.0.1:5432?Action=connect&DBUser=iam%20user&X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIDEXAMPLE%2F20231114%2Fus-west-2%2Frds-db%2Faws4_request&X-Amz-Date=20231114T221320Z&X-Amz-Expires=900&X-Amz-SignedHeaders=host&X-Amz-Signature="),
            "{t}"
        );
    }

    #[derive(Debug)]
    struct Rotating {
        calls: AtomicUsize,
        lifetime: Option<Duration>,
    }

    impl ProvideCredentials for Rotating {
        fn provide_credentials<'a>(&'a self) -> future::ProvideCredentials<'a>
        where
            Self: 'a,
        {
            let n = self.calls.fetch_add(1, Ordering::SeqCst) + 1;
            future::ProvideCredentials::ready(Ok(Credentials::new(
                format!("AKID{n}"),
                "secret",
                None,
                self.lifetime.map(|l| SystemTime::now() + l),
                "test",
            )))
        }
    }

    async fn signer(lifetime: Option<Duration>) -> (Signer, Arc<Rotating>) {
        let rotating = Arc::new(Rotating {
            calls: AtomicUsize::new(0),
            lifetime,
        });
        let chain = Chain(SharedCredentialsProvider::new(SharedRotating(
            rotating.clone(),
        )));
        let signer = Signer {
            credentials: Renewing::start(chain).await.unwrap(),
            region: "us-west-2".into(),
        };
        (signer, rotating)
    }

    #[derive(Debug)]
    struct SharedRotating(Arc<Rotating>);

    impl ProvideCredentials for SharedRotating {
        fn provide_credentials<'a>(&'a self) -> future::ProvideCredentials<'a>
        where
            Self: 'a,
        {
            self.0.provide_credentials()
        }
    }

    async fn key_of(s: &Signer) -> String {
        let t = s.token("h", 5432, "u").await.unwrap();
        let t = t.expose();
        let start = t.find("X-Amz-Credential=").unwrap() + "X-Amz-Credential=".len();
        t[start..].split("%2F").next().unwrap().to_string()
    }

    #[tokio::test]
    async fn credentials_close_to_expiry_are_fetched_again_for_the_next_connection() {
        let (s, rotating) = signer(Some(Duration::from_mins(4))).await;
        assert_eq!(key_of(&s).await, "AKID2");
        assert_eq!(key_of(&s).await, "AKID3");
        assert_eq!(rotating.calls.load(Ordering::SeqCst), 3);
    }

    #[tokio::test]
    async fn credentials_with_time_left_are_reused() {
        let (s, rotating) = signer(Some(Duration::from_hours(1))).await;
        assert_eq!(key_of(&s).await, "AKID1");
        assert_eq!(key_of(&s).await, "AKID1");
        assert_eq!(rotating.calls.load(Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn credentials_without_an_expiry_are_fetched_for_every_connection() {
        let (s, _) = signer(None).await;
        assert_eq!(key_of(&s).await, "AKID2");
        assert_eq!(key_of(&s).await, "AKID3");
    }

    #[tokio::test]
    async fn a_chain_failure_keeps_the_sdk_error_as_its_source() {
        #[derive(Debug)]
        struct Refusing;
        impl ProvideCredentials for Refusing {
            fn provide_credentials<'a>(&'a self) -> future::ProvideCredentials<'a>
            where
                Self: 'a,
            {
                future::ProvideCredentials::ready(Err(
                    aws_credential_types::provider::error::CredentialsError::not_loaded(
                        "no profile",
                    ),
                ))
            }
        }
        let e = Chain(SharedCredentialsProvider::new(Refusing))
            .mint()
            .await
            .err()
            .unwrap();
        assert_eq!(e.exit_code(), 11);
        assert!(std::error::Error::source(&e).is_some());
        assert!(
            e.text().starts_with("failed to acquire AWS IAM token: "),
            "{}",
            e.text()
        );
    }
}
