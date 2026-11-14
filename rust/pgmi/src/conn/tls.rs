use std::future::Future;
use std::io;
use std::path::{Path, PathBuf};
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};

use rustls::client::WebPkiServerVerifier;
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::crypto::CryptoProvider;
use rustls::{
    CertificateError, ClientConfig, DigitallySignedStruct, ProtocolVersion, RootCertStore,
    SignatureScheme,
};
use rustls_pki_types::pem::PemObject;
use rustls_pki_types::{CertificateDer, PrivateKeyDer, ServerName, UnixTime};
use sha2::Digest;
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
use tokio_postgres::tls::{ChannelBinding, TlsConnect, TlsStream};

use super::resolve::{Spec, SslCertMode, SslMode, SslNegotiation};
use crate::error::{Error, Result, Secret};

pub(crate) fn provider() -> Arc<CryptoProvider> {
    Arc::new(rustls_graviola::default_provider())
}

pub(crate) fn install() {
    let _ = CryptoProvider::install_default(rustls_graviola::default_provider());
}

#[cfg(target_arch = "x86_64")]
const CPU_FEATURES: &[&str] = &[
    "aes",
    "pclmulqdq",
    "ssse3",
    "avx",
    "avx2",
    "bmi1",
    "bmi2",
    "adx",
];
#[cfg(target_arch = "aarch64")]
const CPU_FEATURES: &[&str] = &["neon", "aes", "pmull", "sha2"];
#[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
const CPU_FEATURES: &[&str] = &[];

#[cfg(target_arch = "x86_64")]
fn cpu_has(feature: &str) -> bool {
    match feature {
        "aes" => std::arch::is_x86_feature_detected!("aes"),
        "pclmulqdq" => std::arch::is_x86_feature_detected!("pclmulqdq"),
        "ssse3" => std::arch::is_x86_feature_detected!("ssse3"),
        "avx" => std::arch::is_x86_feature_detected!("avx"),
        "avx2" => std::arch::is_x86_feature_detected!("avx2"),
        "bmi1" => std::arch::is_x86_feature_detected!("bmi1"),
        "bmi2" => std::arch::is_x86_feature_detected!("bmi2"),
        "adx" => std::arch::is_x86_feature_detected!("adx"),
        _ => false,
    }
}

#[cfg(target_arch = "aarch64")]
fn cpu_has(feature: &str) -> bool {
    match feature {
        "neon" => std::arch::is_aarch64_feature_detected!("neon"),
        "aes" => std::arch::is_aarch64_feature_detected!("aes"),
        "pmull" => std::arch::is_aarch64_feature_detected!("pmull"),
        "sha2" => std::arch::is_aarch64_feature_detected!("sha2"),
        _ => false,
    }
}

#[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
fn cpu_has(_: &str) -> bool {
    true
}

fn missing_cpu_features(has: impl Fn(&str) -> bool) -> Vec<&'static str> {
    CPU_FEATURES.iter().copied().filter(|f| !has(f)).collect()
}

pub(crate) fn missing_cpu() -> Vec<&'static str> {
    missing_cpu_features(cpu_has)
}

pub(crate) fn require_cpu() -> Result<()> {
    let missing = missing_cpu();
    if missing.is_empty() {
        return Ok(());
    }
    Err(cpu_error(&missing))
}

pub(crate) fn cpu_error(missing: &[&str]) -> Error {
    Error::config(format!(
        "this CPU lacks {} needed for TLS and cloud sign-in; pgmi needs a CPU with {} (x86-64 from about 2014 on, or arm64 with the crypto extensions)",
        missing.join(", "),
        CPU_FEATURES.join(", ")
    ))
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Names {
    Ignore,
    Check,
    CommonName(String),
}

#[derive(Debug)]
pub(crate) struct Verifier {
    chain: Option<Arc<WebPkiServerVerifier>>,
    roots: Vec<CertificateDer<'static>>,
    names: Names,
    provider: Arc<CryptoProvider>,
}

impl Verifier {
    pub(crate) fn new(
        chain: Option<Arc<WebPkiServerVerifier>>,
        roots: Vec<CertificateDer<'static>>,
        names: Names,
    ) -> Arc<Self> {
        Arc::new(Verifier {
            chain,
            roots,
            names,
            provider: provider(),
        })
    }

    /// Go's x509 lets a CA certificate be the server's own: trusted when it
    /// is itself a root, or signed directly by one. webpki refuses both.
    fn ca_leaf(
        &self,
        ee: &CertificateDer<'_>,
        name: &ServerName<'_>,
        now: UnixTime,
    ) -> Option<std::result::Result<ServerCertVerified, rustls::Error>> {
        use x509_cert::der::Decode;
        use x509_cert::ext::pkix::{BasicConstraints, ExtendedKeyUsage};
        let cert = x509_cert::Certificate::from_der(ee.as_ref()).ok()?;
        let tbs = cert.tbs_certificate();
        let (_, BasicConstraints { ca: true, .. }) = tbs.get_extension().ok()?? else {
            return None;
        };
        let bad = |e: CertificateError| Some(Err(rustls::Error::InvalidCertificate(e)));
        let issuer = if self.roots.iter().any(|r| r.as_ref() == ee.as_ref()) {
            None
        } else {
            match self.root_of(&cert, ee) {
                Some(root) => Some(root),
                None => return bad(CertificateError::UnknownIssuer),
            }
        };
        let at = now.as_secs();
        let current = |v: &x509_cert::time::Validity| {
            at >= v.not_before.to_unix_duration().as_secs()
                && at <= v.not_after.to_unix_duration().as_secs()
        };
        if !current(tbs.validity())
            || issuer
                .as_ref()
                .is_some_and(|r| !current(r.tbs_certificate().validity()))
        {
            return bad(CertificateError::Expired);
        }
        if issuer
            .as_ref()
            .is_some_and(|r| !within_constraints(r, &cert))
        {
            return bad(CertificateError::NotValidForName);
        }
        const SERVER_AUTH: &str = "1.3.6.1.5.5.7.3.1";
        const ANY: &str = "2.5.29.37.0";
        if let Ok(Some((_, ExtendedKeyUsage(usages)))) = tbs.get_extension::<ExtendedKeyUsage>()
            && !usages
                .iter()
                .any(|u| matches!(u.to_string().as_str(), SERVER_AUTH | ANY))
        {
            return bad(CertificateError::InvalidPurpose);
        }
        let named = || {
            let parsed = rustls::server::ParsedCertificate::try_from(ee)?;
            rustls::client::verify_server_name(&parsed, name)
        };
        Some(match &self.names {
            Names::Ignore => Ok(ServerCertVerified::assertion()),
            Names::Check => named().map(|()| ServerCertVerified::assertion()),
            Names::CommonName(want) => {
                if named().is_ok() || common_name(ee.as_ref()).as_deref() == Some(want.as_str()) {
                    Ok(ServerCertVerified::assertion())
                } else {
                    Err(rustls::Error::InvalidCertificate(
                        CertificateError::NotValidForName,
                    ))
                }
            }
        })
    }

    /// webpki takes a root for its subject and key alone; Go also refuses
    /// one whose dates do not hold. False only when every root that signed
    /// the chain's last certificate is outside its validity.
    fn anchor_current(
        &self,
        ee: &CertificateDer<'_>,
        im: &[CertificateDer<'_>],
        now: UnixTime,
    ) -> bool {
        use x509_cert::der::{Decode, Encode};
        let top = im.last().unwrap_or(ee);
        let Some((tbs, sig_alg, signature)) = signed_parts(top.as_ref()) else {
            return true;
        };
        let Some(issuer) = x509_cert::Certificate::from_der(top.as_ref())
            .ok()
            .and_then(|c| c.tbs_certificate().issuer().to_der().ok())
        else {
            return true;
        };
        let at = now.as_secs();
        let mut signers = self.roots.iter().filter_map(|r| {
            let root = x509_cert::Certificate::from_der(r.as_ref()).ok()?;
            self.signed(&root, &issuer, tbs, sig_alg, signature)
                .then_some(root)
        });
        let mut any = false;
        let current = signers.any(|root| {
            any = true;
            let v = root.tbs_certificate().validity();
            at >= v.not_before.to_unix_duration().as_secs()
                && at <= v.not_after.to_unix_duration().as_secs()
        });
        current || !any
    }

    fn root_of(
        &self,
        cert: &x509_cert::Certificate,
        ee: &CertificateDer<'_>,
    ) -> Option<x509_cert::Certificate> {
        use x509_cert::der::{Decode, Encode};
        let (tbs, sig_alg, signature) = signed_parts(ee.as_ref())?;
        let issuer = cert.tbs_certificate().issuer().to_der().ok()?;
        self.roots.iter().find_map(|r| {
            let root = x509_cert::Certificate::from_der(r.as_ref()).ok()?;
            self.signed(&root, &issuer, tbs, sig_alg, signature)
                .then_some(root)
        })
    }

    fn signed(
        &self,
        root: &x509_cert::Certificate,
        issuer: &[u8],
        tbs: &[u8],
        sig_alg: &[u8],
        signature: &[u8],
    ) -> bool {
        use x509_cert::der::Encode;
        let rt = root.tbs_certificate();
        if rt.subject().to_der().ok().as_deref() != Some(issuer) {
            return false;
        }
        let spki = rt.subject_public_key_info();
        let Some(key_alg) = spki
            .algorithm
            .to_der()
            .ok()
            .and_then(|d| der_item(&d).map(|(_, body, _)| body.to_vec()))
        else {
            return false;
        };
        let key = spki.subject_public_key.raw_bytes();
        self.provider
            .signature_verification_algorithms
            .all
            .iter()
            .any(|alg| {
                alg.public_key_alg_id().as_ref() == key_alg.as_slice()
                    && alg.signature_alg_id().as_ref() == sig_alg
                    && alg.verify_signature(key, tbs, signature).is_ok()
            })
    }
}

/// Whether the root's name constraints allow every DNS name and IP address
/// the certificate it signed carries. A constraint of any other type, or one
/// that does not decode, allows nothing.
fn within_constraints(root: &x509_cert::Certificate, cert: &x509_cert::Certificate) -> bool {
    use x509_cert::ext::pkix::name::GeneralName;
    use x509_cert::ext::pkix::{NameConstraints, SubjectAltName};
    let constraints = match root.tbs_certificate().get_extension::<NameConstraints>() {
        Ok(None) => return true,
        Ok(Some((_, c))) => c,
        Err(_) => return false,
    };
    let names = match cert.tbs_certificate().get_extension::<SubjectAltName>() {
        Ok(Some((_, SubjectAltName(names)))) => names,
        Ok(None) => Vec::new(),
        Err(_) => return false,
    };
    let fits = |base: &GeneralName, name: &GeneralName| match (base, name) {
        (GeneralName::DnsName(c), GeneralName::DnsName(n)) => dns_within(c.as_ref(), n.as_ref()),
        (GeneralName::IpAddress(c), GeneralName::IpAddress(n)) => {
            ip_within(c.as_bytes(), n.as_bytes())
        }
        _ => false,
    };
    let same_type =
        |a: &GeneralName, b: &GeneralName| std::mem::discriminant(a) == std::mem::discriminant(b);
    let permitted = constraints.permitted_subtrees.unwrap_or_default();
    let excluded = constraints.excluded_subtrees.unwrap_or_default();
    let known = |g: &GeneralName| matches!(g, GeneralName::DnsName(_) | GeneralName::IpAddress(_));
    if !permitted.iter().chain(&excluded).all(|t| known(&t.base)) {
        return false;
    }
    names.iter().all(|n| {
        let allowed = !permitted.iter().any(|t| same_type(&t.base, n))
            || permitted.iter().any(|t| fits(&t.base, n));
        allowed && !excluded.iter().any(|t| fits(&t.base, n))
    })
}

fn dns_within(constraint: &str, name: &str) -> bool {
    let (c, n) = (constraint.to_ascii_lowercase(), name.to_ascii_lowercase());
    if c.is_empty() {
        return true;
    }
    if c.starts_with('.') {
        return n.ends_with(&c);
    }
    n == c || n.ends_with(&format!(".{c}"))
}

fn ip_within(constraint: &[u8], ip: &[u8]) -> bool {
    let half = constraint.len() / 2;
    let (net, mask) = constraint.split_at(half);
    ip.len() == half
        && ip
            .iter()
            .zip(net)
            .zip(mask)
            .all(|((i, n), m)| i & m == n & m)
}

/// The signed bytes (the whole tbsCertificate), the signature algorithm's
/// contents and the signature of a DER certificate.
fn signed_parts(der: &[u8]) -> Option<(&[u8], &[u8], &[u8])> {
    let (_, cert, _) = der_item(der)?;
    let (_, _, after_tbs) = der_item(cert)?;
    let tbs = cert.get(..cert.len() - after_tbs.len())?;
    let (_, alg, after_alg) = der_item(after_tbs)?;
    let (tag, bits, _) = der_item(after_alg)?;
    let (0x03, [0, signature @ ..]) = (tag, bits) else {
        return None;
    };
    Some((tbs, alg, signature))
}

fn common_name(der: &[u8]) -> Option<String> {
    use x509_cert::der::Decode;
    use x509_cert::ext::pkix::name::DirectoryString;
    let cert = x509_cert::Certificate::from_der(der).ok()?;
    match cert.tbs_certificate().subject().common_name().ok()?? {
        DirectoryString::PrintableString(s) => Some(s.to_string()),
        DirectoryString::Utf8String(s) => Some(s),
        _ => None,
    }
}

impl ServerCertVerifier for Verifier {
    fn verify_server_cert(
        &self,
        ee: &CertificateDer<'_>,
        im: &[CertificateDer<'_>],
        name: &ServerName<'_>,
        ocsp: &[u8],
        now: UnixTime,
    ) -> std::result::Result<ServerCertVerified, rustls::Error> {
        let Some(chain) = &self.chain else {
            return Ok(ServerCertVerified::assertion());
        };
        let result = chain
            .verify_server_cert(ee, im, name, ocsp, now)
            .and_then(|ok| {
                if self.anchor_current(ee, im, now) {
                    Ok(ok)
                } else {
                    Err(rustls::Error::InvalidCertificate(CertificateError::Expired))
                }
            });
        if result.is_err()
            && let Some(verdict) = self.ca_leaf(ee, name, now)
        {
            return verdict;
        }
        let wrong_name = matches!(
            result,
            Err(rustls::Error::InvalidCertificate(
                CertificateError::NotValidForName | CertificateError::NotValidForNameContext { .. },
            ))
        );
        match &self.names {
            Names::Ignore if wrong_name => Ok(ServerCertVerified::assertion()),
            Names::CommonName(want) if wrong_name => {
                if common_name(ee.as_ref()).as_deref() == Some(want.as_str()) {
                    Ok(ServerCertVerified::assertion())
                } else {
                    Err(rustls::Error::InvalidCertificate(
                        CertificateError::NotValidForName,
                    ))
                }
            }
            _ => result,
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

fn read_certs(path: &Path) -> Result<Vec<CertificateDer<'static>>> {
    let certs: Vec<_> = CertificateDer::pem_file_iter(path)
        .and_then(std::iter::Iterator::collect::<std::result::Result<Vec<_>, _>>)
        .map_err(|e| {
            Error::config(format!(
                "could not read certificate file \"{}\": {e}",
                path.display()
            ))
        })?;
    if certs.is_empty() {
        return Err(Error::config(format!(
            "no certificate found in \"{}\"",
            path.display()
        )));
    }
    Ok(certs)
}

fn read_key(path: &Path, password: Option<&str>) -> Result<PrivateKeyDer<'static>> {
    check_key_mode(path)?;
    let text = std::fs::read(path).map_err(|e| {
        Error::config(format!(
            "could not read private key file \"{}\"",
            path.display()
        ))
        .with_source(e)
    })?;
    if super::keys::is_encrypted(&text) {
        use super::keys::KeyError;
        return super::keys::decrypt(&text, password).map_err(|e| {
            Error::config(match e {
                KeyError::NeedsPassword => format!(
                    "private key \"{}\" is encrypted; give its password with sslpassword or PGSSLPASSWORD",
                    path.display()
                ),
                KeyError::WrongPassword => format!(
                    "private key \"{}\": decryption password incorrect",
                    path.display()
                ),
                KeyError::Malformed(m) => {
                    format!("could not read private key \"{}\": {m}", path.display())
                }
                KeyError::TooCostly(m) => format!(
                    "private key \"{}\": {m}; re-encrypt it with openssl's defaults: openssl pkcs8 -topk8 -v2 aes-256-cbc",
                    path.display()
                ),
            })
        });
    }
    PrivateKeyDer::from_pem_slice(&text).map_err(|e| {
        Error::config(format!(
            "could not read private key \"{}\": {e}",
            path.display()
        ))
    })
}

fn existing(p: PathBuf) -> Option<PathBuf> {
    p.is_file().then_some(p)
}

fn effective_mode(spec: &Spec) -> SslMode {
    match spec.sslmode() {
        SslMode::Require if root_path(spec).is_some() => SslMode::VerifyCa,
        mode => mode,
    }
}

fn root_path(spec: &Spec) -> Option<String> {
    spec.sslrootcert.clone().or_else(|| {
        spec.pg_files
            .as_ref()
            .and_then(|f| existing(f.dir.join("root.crt")))
            .map(|p| p.display().to_string())
    })
}

fn client_pair(spec: &Spec) -> Result<Option<(PathBuf, PathBuf)>> {
    if spec.sslcertmode == SslCertMode::Disable {
        return Ok(None);
    }
    let defaults = spec
        .pg_files
        .as_ref()
        .map(|f| (f.dir.join("postgresql.crt"), f.dir.join("postgresql.key")))
        .filter(|(c, k)| c.is_file() && k.is_file());
    let cert = spec
        .sslcert
        .as_ref()
        .map(PathBuf::from)
        .or_else(|| defaults.as_ref().map(|d| d.0.clone()));
    let key = spec
        .sslkey
        .as_ref()
        .map(PathBuf::from)
        .or_else(|| defaults.map(|d| d.1));
    match (cert, key) {
        (Some(cert), Some(key)) => Ok(Some((cert, key))),
        (None, None) => Ok(None),
        _ => Err(Error::config(
            "both \"sslcert\" and \"sslkey\" are required: give the client certificate and its private key together",
        )),
    }
}

#[cfg(unix)]
fn check_key_mode(path: &Path) -> Result<()> {
    use std::os::unix::fs::{MetadataExt, PermissionsExt};
    let Ok(meta) = std::fs::metadata(path) else {
        return Ok(());
    };
    let mode = meta.permissions().mode();
    let allowed = if meta.uid() == 0 { 0o037 } else { 0o077 };
    if mode & allowed != 0 {
        return Err(Error::config(format!(
            "private key file \"{}\" has group or world access; file must have permissions u=rw (0600) or less if owned by the current user, or permissions u=rw,g=r (0640) or less if owned by root",
            path.display()
        )));
    }
    Ok(())
}

#[cfg(not(unix))]
fn check_key_mode(_: &Path) -> Result<()> {
    Ok(())
}

/// Which names the server certificate must carry. `verify-ca` against the
/// system trust store also checks the host name: any public CA would
/// otherwise vouch for any server.
fn names_for(mode: SslMode, system_store: bool) -> Names {
    match mode {
        SslMode::VerifyFull => Names::Check,
        SslMode::VerifyCa if system_store => Names::Check,
        _ => Names::Ignore,
    }
}

pub(crate) fn config(spec: &Spec) -> Result<Arc<ClientConfig>> {
    config_with(spec, || rustls_native_certs::load_native_certs().certs)
}

fn config_with(
    spec: &Spec,
    system_roots: impl FnOnce() -> Vec<CertificateDer<'static>>,
) -> Result<Arc<ClientConfig>> {
    let provider = provider();
    let mode = effective_mode(spec);
    let verify = matches!(mode, SslMode::VerifyCa | SslMode::VerifyFull);
    let system_store = verify && root_path(spec).is_none();
    let mut trusted = Vec::new();
    let chain = if verify {
        let mut roots = RootCertStore::empty();
        match root_path(spec).as_deref() {
            Some(path) if path != "system" => {
                trusted = read_certs(Path::new(path))?;
                roots.add_parsable_certificates(trusted.iter().cloned());
            }
            named => {
                let found = system_roots();
                if found.is_empty() {
                    return Err(Error::config(if named.is_some() {
                        "sslrootcert=system: no certificates found in the system trust store"
                            .to_string()
                    } else {
                        format!(
                            "sslmode {mode}: no root certificate given and none found in the system trust store; set sslrootcert or put one in ~/.postgresql/root.crt"
                        )
                    }));
                }
                roots.add_parsable_certificates(found.iter().cloned());
                trusted = found;
            }
        }
        Some(
            WebPkiServerVerifier::builder_with_provider(Arc::new(roots), provider.clone())
                .build()
                .map_err(|e| Error::config(format!("root certificate: {e}")))?,
        )
    } else {
        None
    };
    let verifier = Verifier::new(chain, trusted, names_for(mode, system_store));
    let builder = ClientConfig::builder_with_provider(provider)
        .with_protocol_versions(&spec.tls_versions)
        .map_err(|e| Error::config(format!("TLS setup: {e}")))?
        .dangerous()
        .with_custom_certificate_verifier(verifier);
    let mut config = match client_pair(spec)? {
        Some((cert, key)) => builder
            .with_client_auth_cert(
                read_certs(&cert)?,
                read_key(&key, spec.sslpassword.as_ref().map(Secret::expose))?,
            )
            .map_err(|e| Error::config(format!("client certificate: {e}")))?,
        None => builder.with_no_client_auth(),
    };
    config.enable_sni = spec.sslsni;
    if spec.sslnegotiation == SslNegotiation::Direct {
        config.alpn_protocols = vec![b"postgresql".to_vec()];
    }
    Ok(Arc::new(config))
}

#[derive(Clone)]
pub(crate) struct Connect {
    pub(crate) config: Arc<ClientConfig>,
    pub(crate) host: String,
    pub(crate) version: Arc<Mutex<Option<TlsVersion>>>,
}

/// The TLS version a session negotiated; `TLS 1.3` in the `--json` record.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct TlsVersion(pub(crate) ProtocolVersion);

impl std::fmt::Display for TlsVersion {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self.0 {
            ProtocolVersion::TLSv1_3 => f.write_str("TLS 1.3"),
            ProtocolVersion::TLSv1_2 => f.write_str("TLS 1.2"),
            other => write!(f, "{other:?}"),
        }
    }
}

impl serde::Serialize for TlsVersion {
    fn serialize<S: serde::Serializer>(&self, s: S) -> std::result::Result<S::Ok, S::Error> {
        s.collect_str(self)
    }
}

impl<S> TlsConnect<S> for Connect
where
    S: AsyncRead + AsyncWrite + Unpin + Send + 'static,
{
    type Stream = PgTls<S>;
    type Error = io::Error;
    type Future = Pin<Box<dyn Future<Output = io::Result<PgTls<S>>> + Send>>;

    fn connect(self, stream: S) -> Self::Future {
        Box::pin(async move {
            let name = match self.host.parse::<std::net::IpAddr>() {
                Ok(ip) => ServerName::IpAddress(ip.into()),
                Err(_) => ServerName::try_from(self.host.clone())
                    .map_err(|e| io::Error::new(io::ErrorKind::InvalidInput, e))?,
            };
            let direct = !self.config.alpn_protocols.is_empty();
            let tls = tokio_rustls::TlsConnector::from(self.config)
                .connect(name, stream)
                .await?;
            if direct && tls.get_ref().1.alpn_protocol() != Some(b"postgresql") {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "direct TLS (sslnegotiation=direct): the server did not select the ALPN protocol \"postgresql\"",
                ));
            }
            let version = tls.get_ref().1.protocol_version().map(TlsVersion);
            if let Ok(mut slot) = self.version.lock() {
                *slot = version;
            }
            Ok(PgTls(tls))
        })
    }
}

pub(crate) struct PgTls<S>(tokio_rustls::client::TlsStream<S>);

impl<S: AsyncRead + AsyncWrite + Unpin> AsyncRead for PgTls<S> {
    fn poll_read(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        Pin::new(&mut self.get_mut().0).poll_read(cx, buf)
    }
}

impl<S: AsyncRead + AsyncWrite + Unpin> AsyncWrite for PgTls<S> {
    fn poll_write(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        Pin::new(&mut self.get_mut().0).poll_write(cx, buf)
    }

    fn poll_flush(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.get_mut().0).poll_flush(cx)
    }

    fn poll_shutdown(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.get_mut().0).poll_shutdown(cx)
    }
}

impl<S: AsyncRead + AsyncWrite + Unpin> TlsStream for PgTls<S> {
    fn channel_binding(&self) -> ChannelBinding {
        match super::tap::EndPoint::end_point(self) {
            Some(hash) => ChannelBinding::tls_server_end_point(hash),
            None => ChannelBinding::none(),
        }
    }
}

impl<S> super::tap::EndPoint for PgTls<S> {
    fn end_point(&self) -> Option<Vec<u8>> {
        self.0
            .get_ref()
            .1
            .peer_certificates()
            .and_then(|c| c.first())
            .map(|cert| end_point_hash(cert.as_ref()))
    }
}

fn end_point_hash(der: &[u8]) -> Vec<u8> {
    const SHA384: [&[u8]; 2] = [
        &[0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x0c],
        &[0x2a, 0x86, 0x48, 0xce, 0x3d, 0x04, 0x03, 0x03],
    ];
    const SHA512: [&[u8]; 2] = [
        &[0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x0d],
        &[0x2a, 0x86, 0x48, 0xce, 0x3d, 0x04, 0x03, 0x04],
    ];
    let oid = signature_oid(der).unwrap_or_default();
    if SHA384.contains(&oid) {
        sha2::Sha384::digest(der).to_vec()
    } else if SHA512.contains(&oid) {
        sha2::Sha512::digest(der).to_vec()
    } else {
        sha2::Sha256::digest(der).to_vec()
    }
}

fn der_item(b: &[u8]) -> Option<(u8, &[u8], &[u8])> {
    let tag = *b.first()?;
    let first = *b.get(1)?;
    let (len, head) = if first < 0x80 {
        (first as usize, 2)
    } else {
        let n = (first & 0x7f) as usize;
        if n == 0 || n > 4 {
            return None;
        }
        let mut len = 0usize;
        for i in 0..n {
            len = (len << 8) | *b.get(2 + i)? as usize;
        }
        (len, 2 + n)
    };
    let body = b.get(head..head.checked_add(len)?)?;
    Some((tag, body, b.get(head + len..)?))
}

fn signature_oid(der: &[u8]) -> Option<&[u8]> {
    let (_, cert, _) = der_item(der)?;
    let (_, _, after_tbs) = der_item(cert)?;
    let (_, alg, _) = der_item(after_tbs)?;
    let (tag, oid, _) = der_item(alg)?;
    (tag == 0x06).then_some(oid)
}

/// The error's text, with a certificate the server's chain or name failed
/// worded as Go's x509 package words it.
pub(crate) fn describe_failure(e: &(dyn std::error::Error + 'static), host: &str) -> String {
    let text = crate::error::chain(e);
    let Some(rustls::Error::InvalidCertificate(cause)) = rustls_error(e) else {
        return text;
    };
    let shown = rustls::Error::InvalidCertificate(cause.clone()).to_string();
    let wrong_name = format!("x509: certificate is not valid for {host}");
    let worded = match cause {
        CertificateError::UnknownIssuer => "x509: certificate signed by unknown authority".into(),
        CertificateError::NotValidForName => wrong_name,
        CertificateError::NotValidForNameContext { .. } => shown.replacen(
            "invalid peer certificate: certificate not valid for name",
            &wrong_name,
            1,
        ),
        CertificateError::Expired
        | CertificateError::ExpiredContext { .. }
        | CertificateError::NotValidYet
        | CertificateError::NotValidYetContext { .. } => {
            "x509: certificate has expired or is not yet valid".into()
        }
        CertificateError::InvalidPurpose | CertificateError::InvalidPurposeContext { .. } => {
            "x509: certificate specifies an incompatible key usage".into()
        }
        CertificateError::Revoked => "x509: certificate has been revoked".into(),
        CertificateError::BadSignature => "x509: certificate signature is invalid".into(),
        CertificateError::Other(other) => {
            format!("x509: certificate rejected: {}", words(&other.to_string()))
        }
        other => format!(
            "x509: certificate rejected: {}",
            words(&format!("{other:?}"))
        ),
    };
    text.replace(&shown, &worded)
}

fn words(name: &str) -> String {
    let name = name.split(['(', ' ', '{']).next().unwrap_or(name);
    let mut out = String::new();
    for c in name.chars() {
        if c.is_ascii_uppercase() && !out.is_empty() {
            out.push(' ');
        }
        out.push(c.to_ascii_lowercase());
    }
    out
}

/// The rustls error in `e`'s chain, also inside an `io::Error`, which does
/// not list its payload as a source.
fn rustls_error<'a>(e: &'a (dyn std::error::Error + 'static)) -> Option<&'a rustls::Error> {
    let mut cur = Some(e);
    while let Some(c) = cur {
        if let Some(r) = c.downcast_ref::<rustls::Error>() {
            return Some(r);
        }
        if let Some(r) = c
            .downcast_ref::<io::Error>()
            .and_then(io::Error::get_ref)
            .and_then(|i| i.downcast_ref::<rustls::Error>())
        {
            return Some(r);
        }
        cur = c.source();
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    fn spec(s: &str, home: Option<&Path>) -> Spec {
        let mut pairs = vec![];
        let h;
        if let Some(home) = home {
            h = home.display().to_string();
            pairs.extend([("HOME", h.as_str()), ("APPDATA", h.as_str())]);
        }
        let flags = super::super::resolve::Flags {
            connection: Some(format!("host=h dbname=d {s}").into()),
            ..Default::default()
        };
        super::super::resolve::resolve(&flags, &crate::env::Env::from_pairs(&pairs), None).unwrap()
    }

    #[test]
    fn missing_cpu_features_are_named_and_refused() {
        assert!(missing_cpu_features(|_| true).is_empty());
        assert_eq!(missing_cpu_features(|_| false), CPU_FEATURES);
        let lacking = CPU_FEATURES.first().copied();
        let missing = missing_cpu_features(|f| Some(f) != lacking);
        assert_eq!(missing, lacking.into_iter().collect::<Vec<_>>());
    }

    #[test]
    fn this_machine_runs_the_crypto_provider() {
        assert!(require_cpu().is_ok());
    }

    #[test]
    fn versions_and_sni_reach_the_config() {
        let t = config(&spec("sslmode=require", None)).unwrap();
        assert!(t.enable_sni);
        let t = config(&spec(
            "sslmode=require ssl_max_protocol_version=TLSv1.2 sslsni=0",
            None,
        ))
        .unwrap();
        assert!(!t.enable_sni);
    }

    async fn direct_tls_against(server_alpn: Vec<Vec<u8>>) -> io::Result<()> {
        install();
        let certs = a_root();
        let key = rustls_pki_types::PrivateKeyDer::from_pem_slice(include_bytes!(
            "cloud/testdata/sp.pem"
        ))
        .unwrap();
        let mut server = rustls::ServerConfig::builder_with_provider(provider())
            .with_safe_default_protocol_versions()
            .unwrap()
            .with_no_client_auth()
            .with_single_cert(certs, key)
            .unwrap();
        server.alpn_protocols = server_alpn;
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(server));
        tokio::spawn(async move {
            let (tcp, _) = listener.accept().await.unwrap();
            let _ = acceptor.accept(tcp).await;
        });
        let stream = tokio::net::TcpStream::connect(addr).await.unwrap();
        let connect = Connect {
            config: config(&spec("sslmode=require sslnegotiation=direct", None)).unwrap(),
            host: "localhost".into(),
            version: Arc::default(),
        };
        TlsConnect::connect(connect, stream).await.map(drop)
    }

    #[tokio::test]
    async fn direct_tls_needs_the_server_to_select_postgresql() {
        let e = direct_tls_against(vec![]).await.unwrap_err();
        assert!(e.to_string().contains("ALPN"), "{e}");
        direct_tls_against(vec![b"postgresql".to_vec()])
            .await
            .unwrap();
    }

    fn cert(pem: &[u8]) -> CertificateDer<'static> {
        CertificateDer::from_pem_slice(pem).unwrap()
    }

    fn verifying(root: &[u8], names: Names) -> Arc<Verifier> {
        let mut roots = rustls::RootCertStore::empty();
        roots.add(cert(root)).unwrap();
        let chain = WebPkiServerVerifier::builder_with_provider(Arc::new(roots), provider())
            .build()
            .unwrap();
        Verifier::new(Some(chain), vec![cert(root)], names)
    }

    fn verdict_on(
        v: &Verifier,
        leaf: &[u8],
        host: &str,
        hours: u64,
    ) -> std::result::Result<(), rustls::Error> {
        v.verify_server_cert(
            &cert(leaf),
            &[],
            &ServerName::try_from(host.to_string()).unwrap(),
            &[],
            UnixTime::since_unix_epoch(std::time::Duration::from_hours(hours)),
        )
        .map(drop)
    }

    const IN_2030: u64 = 525_960;
    const IN_2040: u64 = 613_620;

    #[test]
    fn a_self_signed_ca_certificate_is_its_own_root_as_go_trusts_it() {
        let ss = include_bytes!("testdata/self-signed-ca.crt");
        for names in [Names::Check, Names::Ignore] {
            verdict_on(&verifying(ss, names), ss, "db.example", IN_2030).unwrap();
        }
        assert_eq!(
            verdict_on(&verifying(ss, Names::Check), ss, "other.example", IN_2030),
            Err(rustls::Error::InvalidCertificate(
                CertificateError::NotValidForNameContext {
                    expected: ServerName::try_from("other.example").unwrap(),
                    presented: vec!["DnsName(\"db.example\")".into()],
                }
            ))
        );
        verdict_on(&verifying(ss, Names::Ignore), ss, "other.example", IN_2030).unwrap();
        assert_eq!(
            verdict_on(&verifying(ss, Names::Ignore), ss, "db.example", 2_000_000),
            Err(rustls::Error::InvalidCertificate(CertificateError::Expired))
        );
        assert_eq!(
            verdict_on(
                &verifying(include_bytes!("testdata/root.crt"), Names::Ignore),
                ss,
                "db.example",
                IN_2030
            ),
            Err(rustls::Error::InvalidCertificate(
                CertificateError::UnknownIssuer
            ))
        );
    }

    #[test]
    fn a_ca_certificate_signed_by_a_root_is_trusted_as_go_trusts_it() {
        let root = include_bytes!("testdata/ca-leaf-root.crt");
        let leaf = include_bytes!("testdata/ca-leaf.crt");
        verdict_on(&verifying(root, Names::Check), leaf, "db.example", IN_2030).unwrap();
        assert_eq!(
            verdict_on(
                &verifying(include_bytes!("testdata/other-root.crt"), Names::Check),
                leaf,
                "db.example",
                IN_2030
            ),
            Err(rustls::Error::InvalidCertificate(
                CertificateError::UnknownIssuer
            ))
        );
    }

    #[test]
    fn a_ca_server_certificate_obeys_its_roots_name_constraints_and_validity() {
        let constrained = include_bytes!("testdata/nc-root.crt");
        assert_eq!(
            verdict_on(
                &verifying(constrained, Names::Check),
                include_bytes!("testdata/nc-leaf.crt"),
                "db.example",
                IN_2030
            ),
            Err(rustls::Error::InvalidCertificate(
                CertificateError::NotValidForName
            ))
        );
        let short = include_bytes!("testdata/short-root.crt");
        for leaf in [
            &include_bytes!("testdata/short-leaf.crt")[..],
            &include_bytes!("testdata/short-plain-leaf.crt")[..],
        ] {
            verdict_on(&verifying(short, Names::Check), leaf, "db.example", IN_2030).unwrap();
            assert_eq!(
                verdict_on(&verifying(short, Names::Check), leaf, "db.example", IN_2040),
                Err(rustls::Error::InvalidCertificate(CertificateError::Expired))
            );
        }
    }

    #[test]
    fn name_constraints_match_as_rfc_5280_reads_them() {
        assert!(dns_within("example.com", "example.com"));
        assert!(dns_within("example.com", "db.Example.com"));
        assert!(!dns_within("example.com", "badexample.com"));
        assert!(dns_within(".example.com", "db.example.com"));
        assert!(!dns_within(".example.com", "example.com"));
        assert!(dns_within("", "anything"));
        assert!(ip_within(&[10, 0, 0, 0, 255, 0, 0, 0], &[10, 1, 2, 3]));
        assert!(!ip_within(&[10, 0, 0, 0, 255, 0, 0, 0], &[11, 1, 2, 3]));
        assert!(!ip_within(&[10, 0, 0, 0, 255, 0, 0, 0], &[0; 16]));
    }

    fn verdict(v: &Verifier, host: &str) -> std::result::Result<(), rustls::Error> {
        let leaf = cert(include_bytes!("testdata/db-example.crt"));
        let now = UnixTime::since_unix_epoch(std::time::Duration::from_hours(500_000));
        v.verify_server_cert(
            &leaf,
            &[],
            &ServerName::try_from(host.to_string()).unwrap(),
            &[],
            now,
        )
        .map(drop)
    }

    #[test]
    fn verify_full_refuses_a_certificate_for_another_host() {
        let root = include_bytes!("testdata/root.crt");
        verdict(&verifying(root, Names::Check), "db.example").unwrap();
        assert_eq!(
            verdict(&verifying(root, Names::Check), "other.example"),
            Err(rustls::Error::InvalidCertificate(
                CertificateError::NotValidForNameContext {
                    expected: ServerName::try_from("other.example").unwrap(),
                    presented: vec!["DnsName(\"db.example\")".into()],
                }
            ))
        );
        verdict(&verifying(root, Names::Ignore), "other.example").unwrap();
    }

    #[test]
    fn a_chain_error_is_never_ignored_whatever_the_name_rule() {
        let other = include_bytes!("testdata/other-root.crt");
        for names in [
            Names::Check,
            Names::Ignore,
            Names::CommonName("db.example".into()),
        ] {
            assert_eq!(
                verdict(&verifying(other, names), "db.example"),
                Err(rustls::Error::InvalidCertificate(
                    CertificateError::UnknownIssuer
                ))
            );
        }
    }

    fn a_root() -> Vec<CertificateDer<'static>> {
        CertificateDer::pem_slice_iter(include_bytes!("cloud/testdata/sp.crt"))
            .collect::<std::result::Result<_, _>>()
            .unwrap()
    }

    #[test]
    fn verify_ca_checks_the_host_name_only_when_the_root_is_the_system_store() {
        assert_eq!(names_for(SslMode::VerifyFull, false), Names::Check);
        assert_eq!(names_for(SslMode::VerifyFull, true), Names::Check);
        assert_eq!(names_for(SslMode::VerifyCa, true), Names::Check);
        assert_eq!(names_for(SslMode::VerifyCa, false), Names::Ignore);
        assert_eq!(names_for(SslMode::Require, false), Names::Ignore);
    }

    #[test]
    fn verification_without_a_root_uses_the_system_store() {
        let home = crate::testdir::TempDir::new("noroot");
        for mode in ["verify-ca", "verify-full"] {
            let s = spec(&format!("sslmode={mode}"), Some(home.path()));
            let asked = std::cell::Cell::new(false);
            config_with(&s, || {
                asked.set(true);
                a_root()
            })
            .unwrap();
            assert!(asked.get(), "{mode}: the system store was not read");
            let e = config_with(&s, Vec::new).unwrap_err();
            assert_eq!(e.exit_code(), 10);
            assert_eq!(
                e.text(),
                format!(
                    "sslmode {mode}: no root certificate given and none found in the system trust store; set sslrootcert or put one in ~/.postgresql/root.crt"
                )
            );
        }
        let system = spec("sslrootcert=system", Some(home.path()));
        assert_eq!(
            config_with(&system, Vec::new).unwrap_err().text(),
            "sslrootcert=system: no certificates found in the system trust store"
        );
        config_with(&system, a_root).unwrap();
        let given = spec("sslmode=require", Some(home.path()));
        config_with(&given, || panic!("require without a root reads no store")).unwrap();
    }

    #[test]
    fn certificate_failures_are_worded_as_go_words_them() {
        let wrap = |c: CertificateError| {
            std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                rustls::Error::InvalidCertificate(c),
            )
        };
        assert_eq!(
            describe_failure(&wrap(CertificateError::UnknownIssuer), "h"),
            "x509: certificate signed by unknown authority"
        );
        assert_eq!(
            describe_failure(&wrap(CertificateError::NotValidForName), "h"),
            "x509: certificate is not valid for h"
        );
        assert_eq!(
            describe_failure(&wrap(CertificateError::Expired), "h"),
            "x509: certificate has expired or is not yet valid"
        );
        assert_eq!(
            describe_failure(&wrap(CertificateError::InvalidPurpose), "h"),
            "x509: certificate specifies an incompatible key usage"
        );
        let webpki =
            rustls::server::ParsedCertificate::try_from(&CertificateDer::from(b"junk".to_vec()))
                .err()
                .unwrap();
        let rustls::Error::InvalidCertificate(bad) = webpki else {
            panic!("{webpki:?}")
        };
        let text = describe_failure(&wrap(bad), "h");
        assert!(text.starts_with("x509: certificate rejected: "), "{text}");
        assert_eq!(words("CaUsedAsEndEntity"), "ca used as end entity");
        assert!(
            !text.contains("Other(") && !text.contains("invalid peer certificate"),
            "{text}"
        );
        let plain = std::io::Error::other("invalid peer certificate: UnknownIssuer");
        assert_eq!(
            describe_failure(&plain, "h"),
            "invalid peer certificate: UnknownIssuer",
            "only a rustls error is reworded, never text that looks like one"
        );
    }

    fn pg_dir(home: &crate::testdir::TempDir) -> PathBuf {
        let dir = spec("", Some(home.path())).pg_files.unwrap().dir;
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn client_certificate_comes_in_pairs() {
        const PAIR: &str = "both \"sslcert\" and \"sslkey\" are required: give the client certificate and its private key together";
        for half in ["sslcert=/nowhere/c.crt", "sslkey=/nowhere/c.key"] {
            let Err(e) = config(&spec(&format!("sslmode=require {half}"), None)) else {
                panic!("{half}: half a pair was accepted")
            };
            assert_eq!(e.text(), PAIR, "{half}");
            assert_eq!(e.exit_code(), 10, "{half}");
        }
        let given = spec(
            "sslmode=require sslcertmode=disable sslcert=/nowhere/c.crt",
            None,
        );
        assert!(matches!(client_pair(&given), Ok(None)));
        assert!(config(&given).is_ok());

        let home = crate::testdir::TempDir::new("tls");
        let dir = pg_dir(&home);
        let (crt, key) = (dir.join("postgresql.crt"), dir.join("postgresql.key"));
        std::fs::write(&crt, "junk").unwrap();
        let one_default = spec("sslmode=require", Some(home.path()));
        assert!(matches!(client_pair(&one_default), Ok(None)));
        assert!(config(&one_default).is_ok());
        std::fs::write(&key, "junk").unwrap();
        let both = spec("sslmode=require", Some(home.path()));
        assert_eq!(client_pair(&both).unwrap(), Some((crt.clone(), key)));
        let Err(e) = config(&both) else {
            panic!("a junk default certificate was accepted")
        };
        assert_eq!(
            e.text(),
            format!("no certificate found in \"{}\"", crt.display())
        );
    }

    #[cfg(unix)]
    #[test]
    fn a_key_others_can_read_is_refused() {
        use std::os::unix::fs::PermissionsExt;
        let dir = crate::testdir::TempDir::new("key");
        let key = dir.join("key");
        std::fs::write(&key, "junk").unwrap();
        std::fs::set_permissions(&key, std::fs::Permissions::from_mode(0o644)).unwrap();
        let e = read_key(&key, None).unwrap_err();
        assert!(e.text().contains("group or world access"), "{}", e.text());
        std::fs::set_permissions(&key, std::fs::Permissions::from_mode(0o600)).unwrap();
        assert!(
            !read_key(&key, None)
                .unwrap_err()
                .text()
                .contains("group or world")
        );
    }

    #[test]
    fn a_key_whose_encryption_would_run_for_hours_is_refused_by_name() {
        let dir = crate::testdir::TempDir::new("costly-key");
        let key = dir.join("client.key");
        std::fs::write(
            &key,
            include_str!("cloud/testdata/client-ec-pbes2-runaway.pem"),
        )
        .unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&key, std::fs::Permissions::from_mode(0o600)).unwrap();
        }
        let e = read_key(&key, Some("key-passphrase")).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert_eq!(
            e.text(),
            format!(
                "private key \"{}\": PBKDF2 iteration count 2147483647 is above 1000000; re-encrypt it with openssl's defaults: openssl pkcs8 -topk8 -v2 aes-256-cbc",
                key.display()
            )
        );
    }

    #[test]
    fn der_walk() {
        let alg = [
            0x30, 0x0a, 0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x04, 0x03, 0x03,
        ];
        let tbs = [0x30, 0x01, 0x00];
        let mut cert_body = tbs.to_vec();
        cert_body.extend(alg);
        let mut der = vec![0x30, u8::try_from(cert_body.len()).unwrap()];
        der.extend(&cert_body);
        assert_eq!(signature_oid(&der), Some(&alg[4..]));
        assert_eq!(end_point_hash(&der).len(), 48);
        assert_eq!(end_point_hash(b"junk").len(), 32);
    }
}
