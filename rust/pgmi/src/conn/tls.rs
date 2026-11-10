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

use super::resolve::Spec;
use crate::error::{Error, Result};

pub fn provider() -> Arc<CryptoProvider> {
    Arc::new(rustls_graviola::default_provider())
}

pub fn install() {
    let _ = CryptoProvider::install_default(rustls_graviola::default_provider());
}

#[derive(Debug)]
struct PgVerifier {
    chain: Option<Arc<WebPkiServerVerifier>>,
    check_name: bool,
    provider: Arc<CryptoProvider>,
}

impl ServerCertVerifier for PgVerifier {
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
        match chain.verify_server_cert(ee, im, name, ocsp, now) {
            Err(rustls::Error::InvalidCertificate(
                CertificateError::NotValidForName | CertificateError::NotValidForNameContext { .. },
            )) if !self.check_name => Ok(ServerCertVerified::assertion()),
            other => other,
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

pub struct Tls {
    pub config: Arc<ClientConfig>,
}

fn read_certs(path: &Path) -> Result<Vec<CertificateDer<'static>>> {
    let certs: Vec<_> = CertificateDer::pem_file_iter(path)
        .and_then(|it| it.collect::<std::result::Result<Vec<_>, _>>())
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

fn read_key(path: &Path) -> Result<PrivateKeyDer<'static>> {
    let text = std::fs::read(path).map_err(|e| {
        Error::config(format!(
            "could not read private key file \"{}\"",
            path.display()
        ))
        .with_source(e)
    })?;
    if text.windows(9).any(|w| w == b"ENCRYPTED") {
        return Err(Error::config(format!(
            "private key \"{}\" is encrypted; encrypted client keys are not supported in this build",
            path.display()
        )));
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

pub fn effective_mode(spec: &Spec) -> String {
    let mut mode = spec.sslmode.clone();
    if mode == "require" && root_path(spec).is_some() {
        mode = "verify-ca".into();
    }
    mode
}

fn root_path(spec: &Spec) -> Option<String> {
    spec.sslrootcert.clone().or_else(|| {
        spec.home
            .as_ref()
            .and_then(|h| existing(h.join(".postgresql/root.crt")))
            .map(|p| p.display().to_string())
    })
}

pub fn config(spec: &Spec) -> Result<Tls> {
    let provider = provider();
    let mode = effective_mode(spec);
    let verify = matches!(mode.as_str(), "verify-ca" | "verify-full");
    let chain = if verify {
        let mut roots = RootCertStore::empty();
        match root_path(spec).as_deref() {
            Some("system") => {
                let found = rustls_native_certs::load_native_certs();
                if found.certs.is_empty() {
                    return Err(Error::config(
                        "sslrootcert=system: no certificates found in the system trust store",
                    ));
                }
                roots.add_parsable_certificates(found.certs);
            }
            Some(path) => {
                roots.add_parsable_certificates(read_certs(Path::new(path))?);
            }
            None => {
                return Err(Error::config(format!(
                    "sslmode {mode} needs a root certificate: set sslrootcert or put one in ~/.postgresql/root.crt"
                )));
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
    let verifier = Arc::new(PgVerifier {
        chain,
        check_name: mode == "verify-full",
        provider: provider.clone(),
    });
    let builder = ClientConfig::builder_with_provider(provider)
        .with_safe_default_protocol_versions()
        .map_err(|e| Error::config(format!("TLS setup: {e}")))?
        .dangerous()
        .with_custom_certificate_verifier(verifier);
    let pg = spec.home.as_ref().map(|h| h.join(".postgresql"));
    let cert = spec
        .sslcert
        .as_ref()
        .map(PathBuf::from)
        .or_else(|| pg.as_ref().and_then(|d| existing(d.join("postgresql.crt"))));
    let key = spec
        .sslkey
        .as_ref()
        .map(PathBuf::from)
        .or_else(|| pg.as_ref().and_then(|d| existing(d.join("postgresql.key"))));
    let mut config = match (cert, key) {
        (Some(cert), Some(key)) if cert.is_file() || spec.sslcert.is_some() => builder
            .with_client_auth_cert(read_certs(&cert)?, read_key(&key)?)
            .map_err(|e| Error::config(format!("client certificate: {e}")))?,
        _ => builder.with_no_client_auth(),
    };
    if spec.sslnegotiation_direct {
        config.alpn_protocols = vec![b"postgresql".to_vec()];
    }
    Ok(Tls {
        config: Arc::new(config),
    })
}

#[derive(Clone)]
pub struct Connect {
    pub config: Arc<ClientConfig>,
    pub host: String,
    pub version: Arc<Mutex<Option<String>>>,
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

pub struct PgTls<S>(tokio_rustls::client::TlsStream<S>);

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

pub fn describe_failure(text: &str, host: &str) -> String {
    text.replace(
        "invalid peer certificate: UnknownIssuer",
        "x509: certificate signed by unknown authority",
    )
    .replace(
        "invalid peer certificate: NotValidForName",
        &format!("x509: certificate is not valid for {host}"),
    )
    .replace(
        "invalid peer certificate: certificate not valid for name",
        &format!("x509: certificate is not valid for {host}"),
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn der_walk() {
        let alg = [
            0x30, 0x0a, 0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x04, 0x03, 0x03,
        ];
        let tbs = [0x30, 0x01, 0x00];
        let mut cert_body = tbs.to_vec();
        cert_body.extend(alg);
        let mut der = vec![0x30, cert_body.len() as u8];
        der.extend(&cert_body);
        assert_eq!(signature_oid(&der), Some(&alg[4..]));
        assert_eq!(end_point_hash(&der).len(), 48);
        assert_eq!(end_point_hash(b"junk").len(), 32);
    }
}
