use std::path::Path;
use std::sync::Arc;
use std::time::Duration;

use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::{DigitallySignedStruct, SignatureScheme};
use rustls_pki_types::{CertificateDer, ServerName, UnixTime};
use sha1::Digest;

use super::http::{client, encode};
use crate::env::Env;

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
) -> std::result::Result<String, String> {
    if let (Some(_), Source::Arc | Source::CloudShell | Source::ServiceFabric) = (client_id, source)
    {
        return Err(format!(
            "{} doesn't support user-assigned managed identities",
            source.name()
        ));
    }
    let http = client().map_err(|e| e.text())?;
    let request = match source {
        Source::CloudShell => {
            let body = format!("resource={}", encode(resource));
            http.post(var(env, "MSI_ENDPOINT"))
                .header("Metadata", "true")
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(body)
        }
        Source::MachineLearning => {
            let id = client_id
                .map(str::to_string)
                .unwrap_or_else(|| var(env, "DEFAULT_IDENTITY_CLIENT_ID"));
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
                .map_err(|e| e.to_string())?;
            if challenge.status() != reqwest::StatusCode::UNAUTHORIZED {
                return Err(format!(
                    "expected a 401 response from the Azure Arc endpoint, got {}",
                    challenge.status()
                ));
            }
            let header = challenge
                .headers()
                .get("WWW-Authenticate")
                .and_then(|h| h.to_str().ok())
                .unwrap_or_default()
                .to_string();
            let key = arc_key(&header, &arc_dir(env))?;
            http.get(url)
                .header("Metadata", "true")
                .header("Authorization", format!("Basic {key}"))
        }
    };
    let answer = request.send().await.map_err(|e| e.to_string())?;
    let status = answer.status();
    let text = answer.text().await.map_err(|e| e.to_string())?;
    if !status.is_success() {
        return Err(format!(
            "{} managed identity endpoint answered {status}",
            source.name()
        ));
    }
    serde_json::from_str::<serde_json::Value>(&text)
        .ok()
        .and_then(|v| v.get("access_token")?.as_str().map(str::to_string))
        .ok_or_else(|| {
            format!(
                "{} managed identity endpoint returned no access_token",
                source.name()
            )
        })
}

fn arc_dir(env: &Env) -> std::path::PathBuf {
    if cfg!(windows) {
        Path::new(&var(env, "ProgramData")).join("AzureConnectedMachineAgent\\Tokens")
    } else {
        Path::new("/var/opt/azcmagent/tokens").to_path_buf()
    }
}

fn arc_key(header: &str, dir: &Path) -> std::result::Result<String, String> {
    let path = header
        .split_once('=')
        .map(|(_, p)| p.trim().trim_matches('"'))
        .filter(|p| !p.is_empty())
        .ok_or("the Azure Arc endpoint sent no key file in WWW-Authenticate")?;
    let path = Path::new(path);
    if path.parent() != Some(dir) {
        return Err(format!(
            "the Azure Arc key file {} is not in {}",
            path.display(),
            dir.display()
        ));
    }
    if path.extension().and_then(|e| e.to_str()) != Some("key") {
        return Err(format!(
            "the Azure Arc key file {} has no .key extension",
            path.display()
        ));
    }
    let meta = std::fs::metadata(path).map_err(|e| format!("{}: {e}", path.display()))?;
    if meta.len() > 4096 {
        return Err(format!(
            "the Azure Arc key file {} is larger than 4096 bytes",
            path.display()
        ));
    }
    std::fs::read_to_string(path)
        .map(|k| k.trim().to_string())
        .map_err(|e| format!("{}: {e}", path.display()))
}

fn pinned(thumbprint: &str) -> std::result::Result<reqwest::Client, String> {
    let provider = crate::conn::tls::provider();
    let config = rustls::ClientConfig::builder_with_provider(provider.clone())
        .with_safe_default_protocol_versions()
        .map_err(|e| e.to_string())?
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
        .map_err(|e| e.to_string())
}

#[derive(Debug)]
struct Thumbprint {
    want: String,
    provider: Arc<rustls::crypto::CryptoProvider>,
}

fn sha1_hex(der: &[u8]) -> String {
    sha1::Sha1::digest(der)
        .iter()
        .map(|b| format!("{b:02X}"))
        .collect()
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
            arc_key(&header, elsewhere).unwrap_err(),
            format!(
                "the Azure Arc key file {} is not in {}",
                key.display(),
                elsewhere.display()
            )
        );
        let other = dir.join("abc.txt");
        std::fs::write(&other, "x").unwrap();
        assert_eq!(
            arc_key(&format!("Basic realm={}", other.display()), dir.path()).unwrap_err(),
            format!(
                "the Azure Arc key file {} has no .key extension",
                other.display()
            )
        );
        std::fs::write(&key, vec![b'k'; 5000]).unwrap();
        assert_eq!(
            arc_key(&header, dir.path()).unwrap_err(),
            format!(
                "the Azure Arc key file {} is larger than 4096 bytes",
                key.display()
            )
        );
        assert_eq!(
            arc_key("Basic", dir.path()).unwrap_err(),
            "the Azure Arc endpoint sent no key file in WWW-Authenticate"
        );
        let missing = dir.join("gone.key");
        let e = arc_key(&format!("Basic realm={}", missing.display()), dir.path()).unwrap_err();
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
