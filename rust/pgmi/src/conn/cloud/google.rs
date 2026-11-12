use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use rsa::PrivateKey;
use rsa::pkcs8::{EncodePrivateKey, EncodePublicKey, LineEnding};
use rustls::client::WebPkiServerVerifier;
use rustls::{ClientConfig, RootCertStore};
use rustls_pki_types::pem::PemObject;
use rustls_pki_types::{CertificateDer, PrivateKeyDer, PrivatePkcs8KeyDer, ServerName};
use serde::Deserialize;

use super::Options;
use super::http::client;
use super::renew::{Fetched, Mint};
use crate::conn::socket::Tcp;
use crate::conn::tls::Names;
use crate::env::Env;
use crate::error::{Error, Kind, Result, Secret};

const ADMIN_SCOPE: &str = "https://www.googleapis.com/auth/sqlservice.admin";
const LOGIN_SCOPE: &str = "https://www.googleapis.com/auth/sqlservice.login";
const PORT: u16 = 3307;
const ADC_FILE: &str = "application_default_credentials.json";

/// A Cloud SQL connection's credentials: the instance address, the TLS
/// configuration with the ephemeral client certificate, and the IAM login
/// token that is the password.
pub(crate) struct Session {
    address: String,
    config: Arc<ClientConfig>,
    server_name: ServerName<'static>,
    login_token: Secret,
}

impl Session {
    pub(crate) fn password(&self) -> &Secret {
        &self.login_token
    }

    pub(crate) async fn dial(
        &self,
        options: &Tcp,
    ) -> std::io::Result<tokio_rustls::client::TlsStream<tokio::net::TcpStream>> {
        let tcp = tokio::net::TcpStream::connect((self.address.as_str(), PORT)).await?;
        options.apply(&tcp)?;
        tokio_rustls::TlsConnector::from(self.config.clone())
            .connect(self.server_name.clone(), tcp)
            .await
    }
}

/// The step of creating a Cloud SQL session that failed.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Step {
    ConnectSettings,
    GenerateEphemeralCert,
    RsaKey,
    EphemeralCertificate,
    ServerCa,
    Tls,
    ServerName,
}

impl std::fmt::Display for Step {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            Step::ConnectSettings => "connectSettings",
            Step::GenerateEphemeralCert => "generateEphemeralCert",
            Step::RsaKey => "RSA key",
            Step::EphemeralCertificate => "ephemeral certificate",
            Step::ServerCa => "server CA",
            Step::Tls => "TLS",
            Step::ServerName => "server name",
        })
    }
}

/// Why the Cloud SQL dialer could not be created.
#[derive(Debug)]
pub(crate) enum DialerError {
    NoDefaultCredentials,
    Credentials(google_cloud_auth::build_errors::Error),
    AccessToken(google_cloud_auth::errors::CredentialsError),
    NoPublicIp(String),
    NoServerCa,
    KeyTask(tokio::task::JoinError),
    Failed {
        step: Step,
        source: Box<dyn std::error::Error + Send + Sync>,
    },
}

impl std::fmt::Display for DialerError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            DialerError::NoDefaultCredentials => f.write_str(
                "failed to create default credentials: credentials: could not find default credentials. See https://cloud.google.com/docs/authentication/external/set-up-adc for more information",
            ),
            DialerError::Credentials(_) => f.write_str("failed to create default credentials"),
            DialerError::AccessToken(_) => f.write_str("failed to get an access token"),
            DialerError::NoPublicIp(instance) => {
                write!(f, "instance {instance} has no public IP address")
            }
            DialerError::NoServerCa => f.write_str("connectSettings has no server CA certificate"),
            DialerError::KeyTask(_) => Step::RsaKey.fmt(f),
            DialerError::Failed { step, .. } => step.fmt(f),
        }
    }
}

impl std::error::Error for DialerError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            DialerError::Credentials(e) => Some(e),
            DialerError::AccessToken(e) => Some(e),
            DialerError::KeyTask(e) => Some(e),
            DialerError::Failed { source, .. } => Some(source.as_ref()),
            DialerError::NoDefaultCredentials
            | DialerError::NoPublicIp(_)
            | DialerError::NoServerCa => None,
        }
    }
}

impl From<DialerError> for Error {
    fn from(e: DialerError) -> Self {
        let kind = match e {
            DialerError::KeyTask(_) => Kind::Internal,
            _ => Kind::Connection,
        };
        Error::new(kind, "failed to create Cloud SQL dialer").with_source(e)
    }
}

fn failed<E>(step: Step) -> impl FnOnce(E) -> DialerError
where
    E: Into<Box<dyn std::error::Error + Send + Sync>>,
{
    move |e| DialerError::Failed {
        step,
        source: e.into(),
    }
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct ConnectSettings {
    #[serde(default)]
    ip_addresses: Vec<IpAddress>,
    server_ca_cert: Option<PemCert>,
    #[serde(default)]
    dns_name: String,
    #[serde(default)]
    server_ca_mode: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct IpAddress {
    #[serde(rename = "type", default)]
    kind: String,
    ip_address: String,
}

#[derive(Deserialize)]
struct PemCert {
    cert: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct EphemeralCert {
    ephemeral_cert: PemCert,
}

fn instance_parts(name: &str) -> Result<(String, String, String)> {
    let mut parts: Vec<&str> = name.rsplitn(3, ':').collect();
    parts.reverse();
    match parts.as_slice() {
        [p, r, i] if !p.is_empty() && !r.is_empty() && !i.is_empty() => {
            Ok((p.to_string(), r.to_string(), i.to_string()))
        }
        _ => Err(Error::config(format!(
            "invalid --google-instance {name:?}: want project:region:instance"
        ))),
    }
}

fn adc_well_known(env: &Env, windows: bool) -> Option<PathBuf> {
    let root = Path::new(
        env.non_empty(if windows { "APPDATA" } else { "HOME" })
            .ok()??,
    );
    Some(if windows {
        root.join("gcloud").join(ADC_FILE)
    } else {
        root.join(".config").join("gcloud").join(ADC_FILE)
    })
}

async fn access_token(scope: &str, adc_file: bool) -> std::result::Result<Secret, DialerError> {
    let creds = google_cloud_auth::credentials::Builder::default()
        .with_scopes([scope])
        .build_access_token_credentials()
        .map_err(DialerError::Credentials)?;
    let fetched = tokio::time::timeout(
        Duration::from_secs(if adc_file { 60 } else { 3 }),
        creds.access_token(),
    )
    .await;
    match fetched {
        Ok(Ok(t)) => Ok(Secret::from(t.token)),
        Ok(Err(e)) if adc_file => Err(DialerError::AccessToken(e)),
        _ => Err(DialerError::NoDefaultCredentials),
    }
}

/// Opens Cloud SQL sessions for one instance. A session lasts until its
/// ephemeral certificate expires (about an hour, and never past the login
/// token it was issued for), so each connection reuses it while it has
/// more than five minutes left and asks for a new one otherwise.
pub(crate) struct Signin {
    instance: String,
    project: String,
    name: String,
    api: String,
    adc_file: bool,
}

impl std::fmt::Debug for Signin {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "CloudSql({})", self.instance)
    }
}

impl Signin {
    pub(crate) fn new(req: &Options, env: &Env) -> Result<Self> {
        let instance = req.google_instance.clone().unwrap_or_default();
        let (project, _region, name) = instance_parts(&instance)?;
        let adc_file = env.non_empty("GOOGLE_APPLICATION_CREDENTIALS")?.is_some()
            || adc_well_known(env, cfg!(windows)).is_some_and(|p| p.is_file());
        let base = env
            .non_empty("CLOUDSDK_API_ENDPOINT_OVERRIDES_SQLADMIN")?
            .unwrap_or("https://sqladmin.googleapis.com/")
            .trim_end_matches('/')
            .to_string();
        let api = format!("{base}/sql/v1beta4/projects/{project}/instances/{name}");
        Ok(Signin {
            instance,
            project,
            name,
            api,
            adc_file,
        })
    }

    async fn session(&self) -> std::result::Result<Fetched<Arc<Session>>, DialerError> {
        let admin = access_token(ADMIN_SCOPE, self.adc_file).await?;
        let login = access_token(LOGIN_SCOPE, self.adc_file).await?;
        let http = client().map_err(failed(Step::ConnectSettings))?;
        let settings: ConnectSettings = http
            .get(format!("{}/connectSettings", self.api))
            .bearer_auth(admin.expose())
            .send()
            .await
            .and_then(reqwest::Response::error_for_status)
            .map_err(failed(Step::ConnectSettings))?
            .json()
            .await
            .map_err(failed(Step::ConnectSettings))?;
        let address = settings
            .ip_addresses
            .iter()
            .find(|a| a.kind == "PRIMARY")
            .map(|a| a.ip_address.clone())
            .ok_or_else(|| DialerError::NoPublicIp(self.instance.clone()))?;
        let key = tokio::task::spawn_blocking(|| PrivateKey::new(&mut rand::rng(), 2048))
            .await
            .map_err(DialerError::KeyTask)?
            .map_err(failed(Step::RsaKey))?;
        let public = key
            .to_public_key()
            .to_public_key_pem(LineEnding::LF)
            .map_err(failed(Step::RsaKey))?;
        let private = key.to_pkcs8_der().map_err(failed(Step::RsaKey))?;
        let cert: EphemeralCert = http
            .post(format!("{}:generateEphemeralCert", self.api))
            .bearer_auth(admin.expose())
            .json(&serde_json::json!({"public_key": public, "access_token": login.expose()}))
            .send()
            .await
            .and_then(reqwest::Response::error_for_status)
            .map_err(failed(Step::GenerateEphemeralCert))?
            .json()
            .await
            .map_err(failed(Step::GenerateEphemeralCert))?;
        let client_cert = CertificateDer::from_pem_slice(cert.ephemeral_cert.cert.as_bytes())
            .map_err(failed(Step::EphemeralCertificate))?;
        let expires = not_after(&client_cert)?;
        let ca_pem = settings
            .server_ca_cert
            .map(|c| c.cert)
            .ok_or(DialerError::NoServerCa)?;
        let mut roots = RootCertStore::empty();
        for c in CertificateDer::pem_slice_iter(ca_pem.as_bytes()) {
            let c = c.map_err(failed(Step::ServerCa))?;
            roots.add(c).map_err(failed(Step::ServerCa))?;
        }
        let provider = crate::conn::tls::provider();
        let cas = settings.server_ca_mode.contains("CAS") && !settings.dns_name.is_empty();
        let chain = WebPkiServerVerifier::builder_with_provider(Arc::new(roots), provider.clone())
            .build()
            .map_err(failed(Step::ServerCa))?;
        let verifier = crate::conn::tls::Verifier::new(
            Some(chain),
            if cas {
                Names::Check
            } else {
                Names::CommonName(format!("{}:{}", self.project, self.name))
            },
        );
        let config = ClientConfig::builder_with_provider(provider)
            .with_protocol_versions(&[&rustls::version::TLS13])
            .map_err(failed(Step::Tls))?
            .dangerous()
            .with_custom_certificate_verifier(verifier)
            .with_client_auth_cert(
                vec![client_cert],
                PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(private.as_bytes().to_vec())),
            )
            .map_err(failed(Step::Tls))?;
        let host = if cas {
            settings.dns_name.trim_end_matches('.').to_string()
        } else {
            address.clone()
        };
        let server_name = ServerName::try_from(host).map_err(failed(Step::ServerName))?;
        Ok(Fetched {
            value: Arc::new(Session {
                address,
                config: Arc::new(config),
                server_name,
                login_token: login,
            }),
            expires: Some(expires),
        })
    }
}

impl Mint for Signin {
    type Value = Arc<Session>;

    async fn mint(&self) -> Result<Fetched<Arc<Session>>> {
        Ok(self.session().await?)
    }
}

fn not_after(cert: &CertificateDer<'_>) -> std::result::Result<jiff::Timestamp, DialerError> {
    use x509_cert::der::Decode;
    let parsed =
        x509_cert::Certificate::from_der(cert).map_err(failed(Step::EphemeralCertificate))?;
    let since_epoch = parsed
        .tbs_certificate()
        .validity()
        .not_after
        .to_unix_duration();
    jiff::SignedDuration::try_from(since_epoch)
        .map_err(failed(Step::EphemeralCertificate))
        .and_then(|d| jiff::Timestamp::from_duration(d).map_err(failed(Step::EphemeralCertificate)))
}

#[cfg(test)]
pub(crate) mod tests {
    use std::sync::atomic::{AtomicUsize, Ordering};

    use super::*;

    /// Mints sessions for 127.0.0.1, counting each.
    #[derive(Default)]
    pub(crate) struct Sessions {
        pub(crate) minted: AtomicUsize,
    }

    pub(crate) fn session() -> Arc<Session> {
        let config = ClientConfig::builder_with_provider(crate::conn::tls::provider())
            .with_protocol_versions(&[&rustls::version::TLS13])
            .unwrap()
            .with_root_certificates(RootCertStore::empty())
            .with_no_client_auth();
        Arc::new(Session {
            address: "127.0.0.1".into(),
            config: Arc::new(config),
            server_name: ServerName::try_from("localhost").unwrap(),
            login_token: Secret::from("login"),
        })
    }

    impl Mint for Sessions {
        type Value = Arc<Session>;

        fn mint(&self) -> impl std::future::Future<Output = Result<Fetched<Arc<Session>>>> + Send {
            self.minted.fetch_add(1, Ordering::SeqCst);
            std::future::ready(Ok(Fetched {
                value: session(),
                expires: Some(jiff::Timestamp::now() + jiff::SignedDuration::from_hours(1)),
            }))
        }
    }

    #[test]
    fn a_session_expires_with_its_ephemeral_certificate() {
        let pem = include_bytes!("testdata/sp.crt");
        let cert = CertificateDer::from_pem_slice(pem).unwrap();
        assert_eq!(
            not_after(&cert).unwrap(),
            "2126-10-18T00:15:41Z".parse::<jiff::Timestamp>().unwrap()
        );
        let e = not_after(&CertificateDer::from(vec![1, 2, 3])).unwrap_err();
        assert!(matches!(
            e,
            DialerError::Failed {
                step: Step::EphemeralCertificate,
                ..
            }
        ));
    }

    #[test]
    fn dialer_errors_keep_their_cause_as_the_source() {
        let e = Error::from(failed(Step::ConnectSettings)(std::io::Error::other(
            "503 Service Unavailable",
        )));
        assert_eq!(e.exit_code(), 11);
        assert_eq!(
            e.text(),
            "failed to create Cloud SQL dialer: connectSettings: 503 Service Unavailable"
        );
        let dialer = std::error::Error::source(&e)
            .and_then(|s| s.downcast_ref::<DialerError>())
            .unwrap();
        assert!(std::error::Error::source(dialer).is_some());
        let e = Error::from(DialerError::NoDefaultCredentials);
        assert_eq!(e.exit_code(), 11);
        assert!(
            e.text().starts_with(
                "failed to create Cloud SQL dialer: failed to create default credentials: credentials: could not find default credentials."
            ),
            "{}",
            e.text()
        );
    }

    #[test]
    fn instance_names() {
        assert_eq!(
            instance_parts("example.com:proj:us-central1:db").unwrap(),
            ("example.com:proj".into(), "us-central1".into(), "db".into())
        );
        let e = instance_parts("proj:db").unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert_eq!(
            e.text(),
            "invalid --google-instance \"proj:db\": want project:region:instance"
        );
    }

    #[test]
    fn adc_file_on_windows_is_under_appdata() {
        let env = Env::from_pairs(&[
            ("APPDATA", r"C:\Users\a\AppData\Roaming"),
            ("HOME", "/c/Users/a"),
            ("USERPROFILE", r"C:\Users\a"),
        ]);
        assert_eq!(
            adc_well_known(&env, true),
            Some(
                Path::new(r"C:\Users\a\AppData\Roaming")
                    .join("gcloud")
                    .join("application_default_credentials.json")
            )
        );
        assert_eq!(
            adc_well_known(&Env::from_pairs(&[("HOME", "/c/Users/a")]), true),
            None
        );
    }

    #[test]
    fn adc_file_on_unix_is_under_home_config() {
        let env = Env::from_pairs(&[("HOME", "/home/a"), ("APPDATA", "/ignored")]);
        assert_eq!(
            adc_well_known(&env, false),
            Some(PathBuf::from(
                "/home/a/.config/gcloud/application_default_credentials.json"
            ))
        );
        assert_eq!(
            adc_well_known(&Env::from_pairs(&[("APPDATA", "/ignored")]), false),
            None
        );
    }
}
