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

pub(crate) fn require_cpu() -> Result<()> {
    let missing = missing_cpu_features(cpu_has);
    if missing.is_empty() {
        return Ok(());
    }
    Err(Error::config(format!(
        "this CPU lacks {} needed for TLS and cloud sign-in; pgmi needs a CPU with {} (x86-64 from about 2014 on, or arm64 with the crypto extensions)",
        missing.join(", "),
        CPU_FEATURES.join(", ")
    )))
}

#[derive(Debug)]
pub(crate) enum Names {
    Ignore,
    Check,
    CommonName(String),
}

#[derive(Debug)]
pub(crate) struct Verifier {
    chain: Option<Arc<WebPkiServerVerifier>>,
    names: Names,
    provider: Arc<CryptoProvider>,
}

impl Verifier {
    pub(crate) fn new(chain: Option<Arc<WebPkiServerVerifier>>, names: Names) -> Arc<Self> {
        Arc::new(Verifier {
            chain,
            names,
            provider: provider(),
        })
    }
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
        let result = chain.verify_server_cert(ee, im, name, ocsp, now);
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
    let chain = if verify {
        let mut roots = RootCertStore::empty();
        match root_path(spec).as_deref() {
            Some(path) if path != "system" => {
                roots.add_parsable_certificates(read_certs(Path::new(path))?);
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
                roots.add_parsable_certificates(found);
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
    let verifier = Verifier::new(
        chain,
        if mode == SslMode::VerifyFull {
            Names::Check
        } else {
            Names::Ignore
        },
    );
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
    pub(crate) version: Arc<Mutex<Option<String>>>,
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
            let tls = tokio_rustls::TlsConnector::from(self.config)
                .connect(name, stream)
                .await?;
            let version = match tls.get_ref().1.protocol_version() {
                Some(ProtocolVersion::TLSv1_3) => Some("TLS 1.3".to_string()),
                Some(ProtocolVersion::TLSv1_2) => Some("TLS 1.2".to_string()),
                Some(other) => Some(format!("{other:?}")),
                None => None,
            };
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
        match self
            .0
            .get_ref()
            .1
            .peer_certificates()
            .and_then(|c| c.first())
        {
            Some(cert) => ChannelBinding::tls_server_end_point(end_point_hash(cert.as_ref())),
            None => ChannelBinding::none(),
        }
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
        _ => return text,
    };
    text.replace(&shown, &worded)
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

    fn a_root() -> Vec<CertificateDer<'static>> {
        CertificateDer::pem_slice_iter(include_bytes!("cloud/testdata/sp.crt"))
            .collect::<std::result::Result<_, _>>()
            .unwrap()
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
        let other = wrap(CertificateError::Expired);
        assert_eq!(describe_failure(&other, "h"), other.to_string());
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
