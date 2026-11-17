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

/// The SDK's retry settings from `AWS_MAX_ATTEMPTS` and `AWS_RETRY_MODE`,
/// read here because aws-config panics on a value it cannot use.
fn retries(env: &Env) -> Result<aws_config::retry::RetryConfig> {
    use aws_config::retry::RetryConfig;
    let config = match env.non_empty("AWS_RETRY_MODE")? {
        None => RetryConfig::standard(),
        Some(mode) if mode.eq_ignore_ascii_case("standard") => RetryConfig::standard(),
        Some(mode) if mode.eq_ignore_ascii_case("adaptive") => RetryConfig::adaptive(),
        Some(mode) => {
            return Err(Error::config(format!(
                "AWS_RETRY_MODE \"{mode}\" is not a retry mode; use standard or adaptive, or unset it"
            )));
        }
    };
    match env.non_empty("AWS_MAX_ATTEMPTS")? {
        None => Ok(config),
        Some(text) => match text.parse::<u32>() {
            Ok(attempts) if attempts > 0 => Ok(config.with_max_attempts(attempts)),
            _ => Err(Error::config(format!(
                "AWS_MAX_ATTEMPTS \"{text}\" is not a number of attempts; use 1 or more, or unset it"
            ))),
        },
    }
}

pub(crate) async fn signer(req: &Options, env: &Env) -> Result<Signer> {
    let region = region(req, env)?;
    let retries = retries(env)?;
    let http = SharedHttpClient::new(Reqwest(client()?));
    let loader = || {
        aws_config::defaults(BehaviorVersion::latest())
            .region(Region::new(region.clone()))
            .http_client(http.clone())
            .retry_config(retries.clone())
    };
    let config = match profile_process(env).await {
        Some(chain) => {
            let process = Process {
                command: chain.command,
                patience: process_patience(env),
            };
            let from_profile = if chain.roles.is_empty() {
                SharedCredentialsProvider::new(process)
            } else {
                let sts = loader().no_credentials().load().await;
                let mut provider = SharedCredentialsProvider::new(process);
                for role in chain.roles.into_iter().rev() {
                    provider =
                        SharedCredentialsProvider::new(role.assumed_by(provider, &sts).await);
                }
                provider
            };
            loader()
                .credentials_provider(
                    aws_config::meta::credentials::CredentialsProviderChain::first_try(
                        "Environment",
                        aws_config::environment::EnvironmentVariableCredentialsProvider::new(),
                    )
                    .or_else("CredentialProcess", from_profile),
                )
                .load()
                .await
        }
        None => loader().load().await,
    };
    let provider = config.credentials_provider().ok_or_else(|| {
        Error::connection("failed to acquire AWS IAM token: no credentials provider")
    })?;
    Ok(Signer {
        credentials: Renewing::start(Chain(provider)).await?,
        region,
    })
}

/// How long a profile's `credential_process` may run, as in Go's SDK.
const PROCESS_PATIENCE: Duration = Duration::from_mins(1);

#[cfg(feature = "testhooks")]
fn process_patience(env: &Env) -> Duration {
    env.non_empty("PGMI_TEST_AWS_PROCESS_SECONDS")
        .ok()
        .flatten()
        .and_then(|s| s.parse().ok())
        .map_or(PROCESS_PATIENCE, Duration::from_secs)
}

#[cfg(not(feature = "testhooks"))]
fn process_patience(_: &Env) -> Duration {
    PROCESS_PATIENCE
}

/// A `credential_process` the selected profile's credentials start from,
/// and the roles to assume with them, the selected profile's last.
struct ProcessChain {
    command: String,
    roles: Vec<Role>,
}

/// One profile's `role_arn` with what the SDK passes along when it assumes
/// the role for a profile.
struct Role {
    arn: String,
    external_id: Option<String>,
    session_name: Option<String>,
}

impl Role {
    async fn assumed_by(
        self,
        base: SharedCredentialsProvider,
        sts: &aws_config::SdkConfig,
    ) -> aws_config::sts::AssumeRoleProvider {
        let session_name = self.session_name.unwrap_or_else(|| {
            let since_epoch = SystemTime::now()
                .duration_since(SystemTime::UNIX_EPOCH)
                .unwrap_or_default();
            format!("assume-role-from-profile-{}", since_epoch.as_millis())
        });
        let mut builder = aws_config::sts::AssumeRoleProvider::builder(self.arn)
            .configure(sts)
            .session_name(session_name);
        if let Some(id) = self.external_id {
            builder = builder.external_id(id);
        }
        builder.build_from_provider(base).await
    }
}

/// The `credential_process` the SDK would run for the selected profile, as
/// its profile chain finds it: the profile's own, or the one its roles'
/// `source_profile`s end at. The SDK runs the command with neither a time
/// limit nor a kill, so pgmi runs it, and assumes the roles on top. `None`
/// leaves every other profile, and every mistake in one, to the SDK.
async fn profile_process(env: &Env) -> Option<ProcessChain> {
    let names = [
        "AWS_CONFIG_FILE",
        "AWS_SHARED_CREDENTIALS_FILE",
        "AWS_PROFILE",
        "HOME",
        "USERPROFILE",
        "HOMEDRIVE",
        "HOMEPATH",
    ];
    let vars: Vec<(&str, &str)> = names
        .iter()
        .filter_map(|n| env.get(n).ok().flatten().map(|v| (*n, v)))
        .collect();
    let profiles = aws_config::profile::load(
        &aws_types::os_shim_internal::Fs::real(),
        &aws_types::os_shim_internal::Env::from_slice(&vars),
        &aws_runtime::env_config::file::EnvConfigFiles::default(),
        None,
    )
    .await
    .ok()?;
    process_chain(profiles.selected_profile(), |name| {
        let profile = profiles.get_profile(name)?;
        Some(|key: &str| profile.get(key).map(str::to_string))
    })
}

/// Walks the profiles as the SDK's `resolve_chain` does: `profile` gives a
/// profile's values by key, or `None` when there is no such profile.
fn process_chain<P: Fn(&str) -> Option<String>>(
    selected: &str,
    profile: impl Fn(&str) -> Option<P>,
) -> Option<ProcessChain> {
    const NOT_A_PROCESS: [&str; 8] = [
        "credential_source",
        "web_identity_token_file",
        "sso_session",
        "sso_start_url",
        "sso_account_id",
        "sso_region",
        "sso_role_name",
        "login_session",
    ];
    let mut name = selected.to_string();
    let mut visited: Vec<String> = Vec::new();
    let mut roles = Vec::new();
    let base = loop {
        let value = profile(&name)?;
        if visited.contains(&name) {
            return None;
        }
        visited.push(name.clone());
        let keys_rank_first = visited.len() > 1
            && value("aws_access_key_id").is_some()
            && value("aws_secret_access_key").is_some();
        if keys_rank_first {
            return None;
        }
        let role = value("role_arn").filter(|_| value("web_identity_token_file").is_none());
        let Some(arn) = role else { break value };
        if value("credential_source").is_some() {
            return None;
        }
        let next = value("source_profile")?;
        roles.push(Role {
            arn,
            external_id: value("external_id"),
            session_name: value("role_session_name"),
        });
        if next == name {
            break value;
        }
        name = next;
    };
    if NOT_A_PROCESS.iter().any(|key| base(key).is_some()) {
        return None;
    }
    Some(ProcessChain {
        command: base("credential_process")?,
        roles,
    })
}

/// A profile's `credential_process`, run in its own process group, given
/// `patience` and then killed with everything it started.
#[derive(Debug)]
struct Process {
    command: String,
    patience: Duration,
}

#[derive(serde::Deserialize)]
#[serde(rename_all = "PascalCase")]
struct ProcessCredentials {
    version: u8,
    access_key_id: String,
    secret_access_key: String,
    session_token: Option<String>,
    expiration: Option<String>,
}

impl Process {
    async fn run(&self) -> aws_credential_types::provider::Result {
        use aws_credential_types::provider::error::CredentialsError;
        #[cfg(windows)]
        let command = {
            let mut c = tokio::process::Command::new("cmd.exe");
            c.arg("/C").raw_arg(format!("\"{}\"", self.command));
            c
        };
        #[cfg(not(windows))]
        let command = {
            let mut c = tokio::process::Command::new("sh");
            c.args(["-c", self.command.as_str()]);
            c
        };
        let output = tokio::time::timeout(self.patience, super::child::output(command))
            .await
            .map_err(|_| {
                CredentialsError::provider_error(format!(
                    "the profile's credential_process did not answer within {} s and was ended",
                    self.patience.as_secs()
                ))
            })?
            .map_err(|e| {
                CredentialsError::provider_error(format!(
                    "the profile's credential_process could not run: {e}"
                ))
            })?;
        if !output.status.success() {
            return Err(CredentialsError::provider_error(format!(
                "the profile's credential_process exited with {}: {}",
                output.status,
                String::from_utf8_lossy(&output.stderr).trim()
            )));
        }
        let parsed: ProcessCredentials = serde_json::from_slice(&output.stdout).map_err(|e| {
            CredentialsError::provider_error(format!(
                "the profile's credential_process printed no credentials JSON: {e}"
            ))
        })?;
        if parsed.version != 1 {
            return Err(CredentialsError::provider_error(format!(
                "the profile's credential_process printed Version {}; only 1 is known",
                parsed.version
            )));
        }
        let expiry = match parsed.expiration.as_deref() {
            None => None,
            Some(text) => Some(SystemTime::from(text.parse::<jiff::Timestamp>().map_err(
                |e| {
                    CredentialsError::provider_error(format!(
                        "the profile's credential_process printed an Expiration that is not a time: {e}"
                    ))
                },
            )?)),
        };
        Ok(Credentials::new(
            parsed.access_key_id,
            parsed.secret_access_key,
            parsed.session_token,
            expiry,
            "CredentialProcess",
        ))
    }
}

impl ProvideCredentials for Process {
    fn provide_credentials<'a>(
        &'a self,
    ) -> aws_credential_types::provider::future::ProvideCredentials<'a>
    where
        Self: 'a,
    {
        aws_credential_types::provider::future::ProvideCredentials::new(self.run())
    }
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
            let body = super::http::read(resp).await.map_err(|e| match e {
                super::http::ReadError::Http(e) => ConnectorError::io(e.into()),
                oversized @ super::http::ReadError::Oversized => other(oversized.into()),
            })?;
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
    fn the_sdk_retry_variables_are_honoured_when_valid_and_refused_when_not() {
        use aws_config::retry::RetryMode;
        let of = |pairs: &[(&str, &str)]| retries(&Env::from_pairs(pairs));
        let default = of(&[]).unwrap();
        assert_eq!(
            (default.mode(), default.max_attempts()),
            (RetryMode::Standard, 3)
        );
        let tuned = of(&[("AWS_MAX_ATTEMPTS", "7"), ("AWS_RETRY_MODE", "Adaptive")]).unwrap();
        assert_eq!(
            (tuned.mode(), tuned.max_attempts()),
            (RetryMode::Adaptive, 7)
        );
        for (name, value) in [
            ("AWS_MAX_ATTEMPTS", "0"),
            ("AWS_MAX_ATTEMPTS", "abc"),
            ("AWS_MAX_ATTEMPTS", "-1"),
            ("AWS_RETRY_MODE", "bogus"),
        ] {
            let e = of(&[(name, value)]).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{name}={value}");
            assert!(
                e.text().starts_with(&format!("{name} \"{value}\"")),
                "{}",
                e.text()
            );
        }
    }

    fn chain_of(
        selected: &str,
        config: &[(&str, &[(&str, &str)])],
    ) -> Option<(String, Vec<String>)> {
        process_chain(selected, |name| {
            let (_, keys) = config.iter().find(|(n, _)| *n == name)?;
            Some(|key: &str| {
                keys.iter()
                    .find(|(k, _)| *k == key)
                    .map(|(_, v)| (*v).to_string())
            })
        })
        .map(|c| {
            let roles = c
                .roles
                .iter()
                .map(|r| {
                    format!(
                        "{} {} {}",
                        r.arn,
                        r.session_name.as_deref().unwrap_or("-"),
                        r.external_id.as_deref().unwrap_or("-")
                    )
                })
                .collect();
            (c.command, roles)
        })
    }

    #[test]
    fn the_credential_process_a_profile_chain_ends_at_is_found_with_its_roles_selected_first() {
        let process = ("credential_process", "helper");
        let config: &[(&str, &[(&str, &str)])] = &[
            ("own", &[process]),
            (
                "beside-keys",
                &[
                    ("aws_access_key_id", "k"),
                    ("aws_secret_access_key", "s"),
                    process,
                ],
            ),
            ("one", &[("role_arn", "r1"), ("source_profile", "own")]),
            (
                "two",
                &[
                    ("role_arn", "r2"),
                    ("source_profile", "one"),
                    ("role_session_name", "name"),
                    ("external_id", "id"),
                ],
            ),
            (
                "self",
                &[("role_arn", "rs"), ("source_profile", "self"), process],
            ),
        ];
        let found = |selected| chain_of(selected, config);
        assert_eq!(found("own"), Some(("helper".into(), vec![])));
        assert_eq!(found("beside-keys"), Some(("helper".into(), vec![])));
        assert_eq!(found("one"), Some(("helper".into(), vec!["r1 - -".into()])));
        assert_eq!(
            found("two"),
            Some(("helper".into(), vec!["r2 name id".into(), "r1 - -".into()]))
        );
        assert_eq!(
            found("self"),
            Some(("helper".into(), vec!["rs - -".into()]))
        );
    }

    #[test]
    fn a_profile_chain_the_sdk_resolves_another_way_or_refuses_is_left_to_the_sdk() {
        let process = ("credential_process", "helper");
        let role = ("role_arn", "r");
        let config: &[(&str, &[(&str, &str)])] = &[
            (
                "keys",
                &[("aws_access_key_id", "k"), ("aws_secret_access_key", "s")],
            ),
            (
                "keys-and-process",
                &[
                    ("aws_access_key_id", "k"),
                    ("aws_secret_access_key", "s"),
                    process,
                ],
            ),
            ("over-keys", &[role, ("source_profile", "keys-and-process")]),
            (
                "named-source",
                &[role, ("credential_source", "Environment"), process],
            ),
            (
                "both-sources",
                &[
                    role,
                    ("source_profile", "process"),
                    ("credential_source", "Environment"),
                ],
            ),
            ("no-source", &[role, process]),
            (
                "web-identity",
                &[role, ("web_identity_token_file", "/t"), process],
            ),
            (
                "over-web-identity",
                &[role, ("source_profile", "web-identity")],
            ),
            ("sso", &[("sso_session", "s"), process]),
            ("sso-part", &[("sso_region", "eu"), process]),
            ("login", &[("login_session", "s"), process]),
            ("over-sso", &[role, ("source_profile", "sso")]),
            ("missing-source", &[role, ("source_profile", "nowhere")]),
            ("loop-a", &[role, ("source_profile", "loop-b")]),
            ("loop-b", &[role, ("source_profile", "loop-a")]),
            ("process", &[process]),
            ("half-keys", &[("aws_access_key_id", "k"), process]),
            ("over-half-keys", &[role, ("source_profile", "half-keys")]),
        ];
        for selected in [
            "keys",
            "over-keys",
            "named-source",
            "both-sources",
            "no-source",
            "web-identity",
            "over-web-identity",
            "sso",
            "sso-part",
            "login",
            "over-sso",
            "missing-source",
            "loop-a",
            "nowhere",
        ] {
            assert_eq!(chain_of(selected, config), None, "{selected}");
        }
        assert_eq!(
            chain_of("over-half-keys", config),
            Some(("helper".into(), vec!["r - -".into()]))
        );
    }

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
