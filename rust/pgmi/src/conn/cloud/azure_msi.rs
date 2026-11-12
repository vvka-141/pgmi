use std::fmt::Write as _;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::{DigitallySignedStruct, SignatureScheme};
use rustls_pki_types::{CertificateDer, ServerName, UnixTime};
use sha1::Digest;

use super::http::{client, encode};
use super::renew::Fetched;
use crate::env::Env;
use crate::error::{Error, Secret};

/// Why a managed identity endpoint gave no token.
#[derive(Debug)]
pub(crate) enum MsiError {
    UserAssigned(Source),
    Client(Error),
    Tls(rustls::Error),
    Http(reqwest::Error),
    ArcChallenge(reqwest::StatusCode),
    ArcKey(ArcKeyError),
    Status(Source, reqwest::StatusCode),
    NoToken(Source),
}

impl std::fmt::Display for MsiError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            MsiError::UserAssigned(s) => write!(
                f,
                "{} doesn't support user-assigned managed identities",
                s.name()
            ),
            MsiError::Client(e) => e.fmt(f),
            MsiError::Tls(e) => e.fmt(f),
            MsiError::Http(e) => e.fmt(f),
            MsiError::ArcChallenge(status) => write!(
                f,
                "expected a 401 response from the Azure Arc endpoint, got {status}"
            ),
            MsiError::ArcKey(e) => e.fmt(f),
            MsiError::Status(s, status) => {
                write!(
                    f,
                    "{} managed identity endpoint answered {status}",
                    s.name()
                )
            }
            MsiError::NoToken(s) => write!(
                f,
                "{} managed identity endpoint returned no access_token",
                s.name()
            ),
        }
    }
}

impl std::error::Error for MsiError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            MsiError::Client(e) => Some(e),
            MsiError::Tls(e) => Some(e),
            MsiError::Http(e) => Some(e),
            MsiError::ArcKey(e) => Some(e),
            _ => None,
        }
    }
}

/// Why the Azure Arc key file named in the challenge cannot be used.
#[derive(Debug)]
pub(crate) enum ArcKeyError {
    NotNamed,
    Elsewhere {
        path: PathBuf,
        dir: PathBuf,
    },
    NotAKey(PathBuf),
    TooLarge(PathBuf),
    Read {
        path: PathBuf,
        error: std::io::Error,
    },
}

impl std::fmt::Display for ArcKeyError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ArcKeyError::NotNamed => {
                f.write_str("the Azure Arc endpoint sent no key file in WWW-Authenticate")
            }
            ArcKeyError::Elsewhere { path, dir } => write!(
                f,
                "the Azure Arc key file {} is not in {}",
                path.display(),
                dir.display()
            ),
            ArcKeyError::NotAKey(path) => write!(
                f,
                "the Azure Arc key file {} has no .key extension",
                path.display()
            ),
            ArcKeyError::TooLarge(path) => write!(
                f,
                "the Azure Arc key file {} is larger than 4096 bytes",
                path.display()
            ),
            ArcKeyError::Read { path, error } => write!(f, "{}: {error}", path.display()),
        }
    }
}

impl std::error::Error for ArcKeyError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            ArcKeyError::Read { error, .. } => Some(error),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Source {
    Arc,
    CloudShell,
    ServiceFabric,
    MachineLearning,
}

impl Source {
    fn name(self) -> &'static str {
        match self {
            Source::Arc => "Azure Arc",
            Source::CloudShell => "Cloud Shell",
            Source::ServiceFabric => "Service Fabric",
            Source::MachineLearning => "Azure ML",
        }
    }
}

pub(crate) fn source(env: &Env) -> Option<Source> {
    let set = |n: &str| env.non_empty(n).ok().flatten().is_some();
    if set("IDENTITY_ENDPOINT") {
        if set("IDENTITY_HEADER") {
            return set("IDENTITY_SERVER_THUMBPRINT").then_some(Source::ServiceFabric);
        }
        return set("IMDS_ENDPOINT").then_some(Source::Arc);
    }
    if set("MSI_ENDPOINT") {
        return Some(if set("MSI_SECRET") {
            Source::MachineLearning
        } else {
            Source::CloudShell
        });
    }
    None
}

fn var(env: &Env, name: &str) -> String {
    env.non_empty(name)
        .ok()
        .flatten()
        .unwrap_or_default()
        .to_string()
}

fn query(base: &str, pairs: &[(&str, &str)]) -> String {
    let joined: Vec<String> = pairs
        .iter()
        .map(|(k, v)| format!("{k}={}", encode(v)))
        .collect();
    let sep = if base.contains('?') { '&' } else { '?' };
    format!("{base}{sep}{}", joined.join("&"))
}

pub(crate) async fn token(
    source: Source,
    client_id: Option<&str>,
    resource: &str,
    env: &Env,
) -> std::result::Result<Fetched, MsiError> {
    if let (Some(_), Source::Arc | Source::CloudShell | Source::ServiceFabric) = (client_id, source)
    {
        return Err(MsiError::UserAssigned(source));
    }
    let http = client().map_err(MsiError::Client)?;
    let request = match source {
        Source::CloudShell => {
            let body = format!("resource={}", encode(resource));
            http.post(var(env, "MSI_ENDPOINT"))
                .header("Metadata", "true")
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(body)
        }
        Source::MachineLearning => {
            let id =
                client_id.map_or_else(|| var(env, "DEFAULT_IDENTITY_CLIENT_ID"), str::to_string);
            let url = query(
                &var(env, "MSI_ENDPOINT"),
                &[
                    ("api-version", "2017-09-01"),
                    ("resource", resource),
                    ("clientid", &id),
                ],
            );
            http.get(url).header("secret", var(env, "MSI_SECRET"))
        }
        Source::ServiceFabric => {
            let url = query(
                &var(env, "IDENTITY_ENDPOINT"),
                &[
                    ("api-version", "2019-07-01-preview"),
                    ("resource", resource),
                ],
            );
            pinned(&var(env, "IDENTITY_SERVER_THUMBPRINT"))?
                .get(url)
                .header("Secret", var(env, "IDENTITY_HEADER"))
        }
        Source::Arc => {
            let url = query(
                &var(env, "IDENTITY_ENDPOINT"),
                &[("api-version", "2020-06-01"), ("resource", resource)],
            );
            let challenge = http
                .get(&url)
                .header("Metadata", "true")
                .send()
                .await
                .map_err(MsiError::Http)?;
            if challenge.status() != reqwest::StatusCode::UNAUTHORIZED {
                return Err(MsiError::ArcChallenge(challenge.status()));
            }
            let header = challenge
                .headers()
                .get("WWW-Authenticate")
                .and_then(|h| h.to_str().ok())
                .unwrap_or_default()
                .to_string();
            let key = arc_key(&header, &arc_dir(env)).map_err(MsiError::ArcKey)?;
            http.get(url)
                .header("Metadata", "true")
                .header("Authorization", format!("Basic {key}"))
        }
    };
    let answer = request.send().await.map_err(MsiError::Http)?;
    let status = answer.status();
    let text = answer.text().await.map_err(MsiError::Http)?;
    if !status.is_success() {
        return Err(MsiError::Status(source, status));
    }
    let value = serde_json::from_str::<serde_json::Value>(&text).ok();
    let token = value
        .as_ref()
        .and_then(|v| v.get("access_token")?.as_str())
        .ok_or(MsiError::NoToken(source))?;
    Ok(Fetched {
        value: Secret::from(token),
        expires: value.as_ref().and_then(expires_on),
    })
}

fn expires_on(answer: &serde_json::Value) -> Option<jiff::Timestamp> {
    let v = answer.get("expires_on")?;
    let seconds = v
        .as_i64()
        .or_else(|| v.as_str().and_then(|s| s.parse().ok()))?;
    jiff::Timestamp::from_second(seconds).ok()
}

fn arc_dir(env: &Env) -> std::path::PathBuf {
    if cfg!(windows) {
        Path::new(&var(env, "ProgramData")).join("AzureConnectedMachineAgent\\Tokens")
    } else {
        Path::new("/var/opt/azcmagent/tokens").to_path_buf()
    }
}

fn arc_key(header: &str, dir: &Path) -> std::result::Result<String, ArcKeyError> {
    let path = header
        .split_once('=')
        .map(|(_, p)| p.trim().trim_matches('"'))
        .filter(|p| !p.is_empty())
        .ok_or(ArcKeyError::NotNamed)?;
    let path = Path::new(path);
    if path.parent() != Some(dir) {
        return Err(ArcKeyError::Elsewhere {
            path: path.to_path_buf(),
            dir: dir.to_path_buf(),
        });
    }
    if path.extension().and_then(|e| e.to_str()) != Some("key") {
        return Err(ArcKeyError::NotAKey(path.to_path_buf()));
    }
    let unreadable = |error| ArcKeyError::Read {
        path: path.to_path_buf(),
        error,
    };
    let meta = std::fs::metadata(path).map_err(unreadable)?;
    if meta.len() > 4096 {
        return Err(ArcKeyError::TooLarge(path.to_path_buf()));
    }
    std::fs::read_to_string(path)
        .map(|k| k.trim().to_string())
        .map_err(unreadable)
}

fn pinned(thumbprint: &str) -> std::result::Result<reqwest::Client, MsiError> {
    let provider = crate::conn::tls::provider();
    let config = rustls::ClientConfig::builder_with_provider(provider.clone())
        .with_safe_default_protocol_versions()
        .map_err(MsiError::Tls)?
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(Thumbprint {
            want: thumbprint.to_ascii_uppercase(),
            provider,
        }))
        .with_no_client_auth();
    reqwest::Client::builder()
        .connect_timeout(Duration::from_secs(10))
        .timeout(Duration::from_secs(60))
        .tls_backend_preconfigured(config)
        .build()
        .map_err(MsiError::Http)
}

#[derive(Debug)]
struct Thumbprint {
    want: String,
    provider: Arc<rustls::crypto::CryptoProvider>,
}

fn sha1_hex(der: &[u8]) -> String {
    sha1::Sha1::digest(der)
        .iter()
        .fold(String::with_capacity(40), |mut s, b| {
            let _ = write!(s, "{b:02X}");
            s
        })
}

impl ServerCertVerifier for Thumbprint {
    fn verify_server_cert(
        &self,
        ee: &CertificateDer<'_>,
        _: &[CertificateDer<'_>],
        _: &ServerName<'_>,
        _: &[u8],
        _: UnixTime,
    ) -> std::result::Result<ServerCertVerified, rustls::Error> {
        if sha1_hex(ee.as_ref()) == self.want {
            Ok(ServerCertVerified::assertion())
        } else {
            Err(rustls::Error::General(
                "the Service Fabric endpoint's certificate does not match IDENTITY_SERVER_THUMBPRINT"
                    .into(),
            ))
        }
    }

    fn verify_tls12_signature(
        &self,
        m: &[u8],
        c: &CertificateDer<'_>,
        d: &DigitallySignedStruct,
    ) -> std::result::Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls12_signature(
            m,
            c,
            d,
            &self.provider.signature_verification_algorithms,
        )
    }

    fn verify_tls13_signature(
        &self,
        m: &[u8],
        c: &CertificateDer<'_>,
        d: &DigitallySignedStruct,
    ) -> std::result::Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls13_signature(
            m,
            c,
            d,
            &self.provider.signature_verification_algorithms,
        )
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.provider
            .signature_verification_algorithms
            .supported_schemes()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detects_the_source_like_the_sdks() {
        let s = |pairs: &[(&str, &str)]| source(&Env::from_pairs(pairs));
        assert_eq!(s(&[]), None);
        assert_eq!(
            s(&[("IDENTITY_ENDPOINT", "e"), ("IDENTITY_HEADER", "h")]),
            None
        );
        assert_eq!(
            s(&[
                ("IDENTITY_ENDPOINT", "e"),
                ("IDENTITY_HEADER", "h"),
                ("IDENTITY_SERVER_THUMBPRINT", "t")
            ]),
            Some(Source::ServiceFabric)
        );
        assert_eq!(
            s(&[("IDENTITY_ENDPOINT", "e"), ("IMDS_ENDPOINT", "i")]),
            Some(Source::Arc)
        );
        assert_eq!(s(&[("MSI_ENDPOINT", "m")]), Some(Source::CloudShell));
        assert_eq!(
            s(&[("MSI_ENDPOINT", "m"), ("MSI_SECRET", "s")]),
            Some(Source::MachineLearning)
        );
    }

    #[test]
    fn arc_key_file_must_be_a_small_key_in_the_agent_folder() {
        let dir = crate::testdir::TempDir::new("arc");
        let key = dir.join("abc.key");
        std::fs::write(&key, "secret-key\n").unwrap();
        let header = format!("Basic realm={}", key.display());
        assert_eq!(arc_key(&header, dir.path()).unwrap(), "secret-key");
        let elsewhere = Path::new("/elsewhere");
        assert_eq!(
            arc_key(&header, elsewhere).unwrap_err().to_string(),
            format!(
                "the Azure Arc key file {} is not in {}",
                key.display(),
                elsewhere.display()
            )
        );
        let other = dir.join("abc.txt");
        std::fs::write(&other, "x").unwrap();
        assert_eq!(
            arc_key(&format!("Basic realm={}", other.display()), dir.path())
                .unwrap_err()
                .to_string(),
            format!(
                "the Azure Arc key file {} has no .key extension",
                other.display()
            )
        );
        std::fs::write(&key, vec![b'k'; 5000]).unwrap();
        assert_eq!(
            arc_key(&header, dir.path()).unwrap_err().to_string(),
            format!(
                "the Azure Arc key file {} is larger than 4096 bytes",
                key.display()
            )
        );
        assert_eq!(
            arc_key("Basic", dir.path()).unwrap_err().to_string(),
            "the Azure Arc endpoint sent no key file in WWW-Authenticate"
        );
        let missing = dir.join("gone.key");
        let e = arc_key(&format!("Basic realm={}", missing.display()), dir.path())
            .unwrap_err()
            .to_string();
        assert!(e.starts_with(&format!("{}: ", missing.display())), "{e}");
    }

    #[test]
    fn query_appends() {
        assert_eq!(
            query("http://h/p", &[("a", "b c"), ("r", "https://x")]),
            "http://h/p?a=b%20c&r=https%3A%2F%2Fx"
        );
        assert_eq!(query("http://h/p?x=1", &[("a", "b")]), "http://h/p?x=1&a=b");
    }

    #[test]
    fn thumbprint_is_upper_hex_sha1() {
        assert_eq!(sha1_hex(b"abc"), "A9993E364706816ABA3E25717850C26C9CD0D89D");
    }
}
