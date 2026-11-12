use std::fmt::Write as _;
use std::time::{Duration, SystemTime};

use aws_config::{BehaviorVersion, Region};
use aws_credential_types::Credentials;
use aws_credential_types::provider::ProvideCredentials;
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

use super::Options;
use super::http::{client, encode};
use crate::env::Env;
use crate::error::{Error, Result};

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

pub(crate) struct Signer {
    credentials: Credentials,
    region: String,
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
    let credentials = provider.provide_credentials().await.map_err(|e| {
        Error::connection(format!(
            "failed to acquire AWS IAM token: {}",
            crate::error::chain(&e)
        ))
    })?;
    Ok(Signer {
        credentials,
        region,
    })
}

impl Signer {
    pub(crate) fn token(
        &self,
        host: &str,
        port: u16,
        user: &str,
    ) -> std::result::Result<String, SignError> {
        self.token_at(host, port, user, SystemTime::now())
    }

    fn token_at(
        &self,
        host: &str,
        port: u16,
        user: &str,
        time: SystemTime,
    ) -> std::result::Result<String, SignError> {
        let query = format!("Action=connect&DBUser={}", encode(user));
        let url = format!("https://{host}:{port}/?{query}");
        let mut settings = SigningSettings::default();
        settings.expires_in = Some(Duration::from_mins(15));
        settings.signature_location = SignatureLocation::QueryParams;
        let identity = self.credentials.clone().into();
        let params = v4::SigningParams::builder()
            .identity(&identity)
            .region(&self.region)
            .name("rds-db")
            .time(time)
            .settings(settings)
            .build()
            .map_err(signing)?;
        let signable =
            SignableRequest::new("GET", &url, std::iter::empty(), SignableBody::Bytes(&[]))
                .map_err(signing)?;
        let (instructions, _) = sign(signable, &params.into())
            .map_err(signing)?
            .into_parts();
        let mut token = format!("{host}:{port}?{query}");
        for (name, value) in instructions.params() {
            let _ = write!(token, "&{name}={}", encode(value));
        }
        Ok(token)
    }
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
    use super::*;

    #[test]
    fn token_shape() {
        let s = Signer {
            credentials: Credentials::new("AKIDEXAMPLE", "secret", None, None, "test"),
            region: "us-west-2".into(),
        };
        let t = s
            .token_at(
                "127.0.0.1",
                5432,
                "iam user",
                SystemTime::UNIX_EPOCH + Duration::from_secs(1_700_000_000),
            )
            .unwrap();
        assert!(
            t.starts_with("127.0.0.1:5432?Action=connect&DBUser=iam%20user&X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIDEXAMPLE%2F20231114%2Fus-west-2%2Frds-db%2Faws4_request&X-Amz-Date=20231114T221320Z&X-Amz-Expires=900&X-Amz-SignedHeaders=host&X-Amz-Signature="),
            "{t}"
        );
    }
}
