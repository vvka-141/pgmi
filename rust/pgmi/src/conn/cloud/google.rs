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

use super::Request;
use super::http::client;
use crate::conn::socket::Tcp;
use crate::conn::tls::Names;
use crate::env::Env;
use crate::error::{Error, Result};

const ADMIN_SCOPE: &str = "https://www.googleapis.com/auth/sqlservice.admin";
const LOGIN_SCOPE: &str = "https://www.googleapis.com/auth/sqlservice.login";
const PORT: u16 = 3307;
const ADC_FILE: &str = "application_default_credentials.json";

pub(crate) struct Dialer {
    address: String,
    config: Arc<ClientConfig>,
    server_name: ServerName<'static>,
    login_token: String,
}

impl Dialer {
    pub(crate) fn password(&self) -> String {
        self.login_token.clone()
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

fn failed(e: impl std::fmt::Display) -> Error {
    Error::connection(format!("failed to create Cloud SQL dialer: {e}"))
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

async fn access_token(scope: &str, env: &Env) -> Result<String> {
    let adc_file = env.non_empty("GOOGLE_APPLICATION_CREDENTIALS")?.is_some()
        || adc_well_known(env, cfg!(windows)).is_some_and(|p| p.is_file());
    let creds = google_cloud_auth::credentials::Builder::default()
        .with_scopes([scope])
        .build_access_token_credentials()
        .map_err(|e| failed(format!("failed to create default credentials: {e}")))?;
    let fetched = tokio::time::timeout(
        Duration::from_secs(if adc_file { 60 } else { 3 }),
        creds.access_token(),
    )
    .await;
    match fetched {
        Ok(Ok(t)) => Ok(t.token),
        Ok(Err(e)) if adc_file => Err(failed(format!("failed to get an access token: {e}"))),
        _ => Err(failed(
            "failed to create default credentials: credentials: could not find default credentials. See https://cloud.google.com/docs/authentication/external/set-up-adc for more information",
        )),
    }
}

pub(crate) async fn dialer(req: &Request, env: &Env) -> Result<Dialer> {
    let instance = req.google_instance.clone().unwrap_or_default();
    let (project, _region, name) = instance_parts(&instance)?;
    let admin = access_token(ADMIN_SCOPE, env).await?;
    let login = access_token(LOGIN_SCOPE, env).await?;
    let base = env
        .non_empty("CLOUDSDK_API_ENDPOINT_OVERRIDES_SQLADMIN")?
        .unwrap_or("https://sqladmin.googleapis.com/")
        .trim_end_matches('/')
        .to_string();
    let api = format!("{base}/sql/v1beta4/projects/{project}/instances/{name}");
    let http = client()?;
    let settings: ConnectSettings = http
        .get(format!("{api}/connectSettings"))
        .bearer_auth(&admin)
        .send()
        .await
        .and_then(reqwest::Response::error_for_status)
        .map_err(|e| failed(format!("connectSettings: {e}")))?
        .json()
        .await
        .map_err(|e| failed(format!("connectSettings: {e}")))?;
    let address = settings
        .ip_addresses
        .iter()
        .find(|a| a.kind == "PRIMARY")
        .map(|a| a.ip_address.clone())
        .ok_or_else(|| failed(format!("instance {instance} has no public IP address")))?;
    let key = tokio::task::spawn_blocking(|| PrivateKey::new(&mut rand::rng(), 2048))
        .await
        .map_err(|e| Error::internal(format!("RSA key: {e}")))?
        .map_err(|e| failed(format!("RSA key: {e}")))?;
    let public = key
        .to_public_key()
        .to_public_key_pem(LineEnding::LF)
        .map_err(|e| failed(format!("RSA key: {e}")))?;
    let private = key
        .to_pkcs8_der()
        .map_err(|e| failed(format!("RSA key: {e}")))?;
    let cert: EphemeralCert = http
        .post(format!("{api}:generateEphemeralCert"))
        .bearer_auth(&admin)
        .json(&serde_json::json!({"public_key": public, "access_token": login}))
        .send()
        .await
        .and_then(reqwest::Response::error_for_status)
        .map_err(|e| failed(format!("generateEphemeralCert: {e}")))?
        .json()
        .await
        .map_err(|e| failed(format!("generateEphemeralCert: {e}")))?;
    let client_cert = CertificateDer::from_pem_slice(cert.ephemeral_cert.cert.as_bytes())
        .map_err(|e| failed(format!("ephemeral certificate: {e}")))?;
    let ca_pem = settings
        .server_ca_cert
        .map(|c| c.cert)
        .ok_or_else(|| failed("connectSettings has no server CA certificate"))?;
    let mut roots = RootCertStore::empty();
    for c in CertificateDer::pem_slice_iter(ca_pem.as_bytes()) {
        let c = c.map_err(|e| failed(format!("server CA: {e}")))?;
        roots
            .add(c)
            .map_err(|e| failed(format!("server CA: {e}")))?;
    }
    let provider = crate::conn::tls::provider();
    let cas = settings.server_ca_mode.contains("CAS") && !settings.dns_name.is_empty();
    let chain = WebPkiServerVerifier::builder_with_provider(Arc::new(roots), provider.clone())
        .build()
        .map_err(|e| failed(format!("server CA: {e}")))?;
    let verifier = crate::conn::tls::Verifier::new(
        Some(chain),
        if cas {
            Names::Check
        } else {
            Names::CommonName(format!("{project}:{name}"))
        },
    );
    let config = ClientConfig::builder_with_provider(provider)
        .with_protocol_versions(&[&rustls::version::TLS13])
        .map_err(|e| failed(format!("TLS: {e}")))?
        .dangerous()
        .with_custom_certificate_verifier(verifier)
        .with_client_auth_cert(
            vec![client_cert],
            PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(private.as_bytes().to_vec())),
        )
        .map_err(|e| failed(format!("TLS: {e}")))?;
    let host = if cas {
        settings.dns_name.trim_end_matches('.').to_string()
    } else {
        address.clone()
    };
    let server_name =
        ServerName::try_from(host).map_err(|e| failed(format!("server name: {e}")))?;
    Ok(Dialer {
        address,
        config: Arc::new(config),
        server_name,
        login_token: login,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn instance_names() {
        assert_eq!(
            instance_parts("example.com:proj:us-central1:db").unwrap(),
            ("example.com:proj".into(), "us-central1".into(), "db".into())
        );
        assert!(instance_parts("proj:db").is_err());
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
