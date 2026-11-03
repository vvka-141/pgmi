// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own gateway that fits this project. The rules it keeps are in
// ../../PROTOCOL.md ("Connections").
//
// The process's one pool: bounded, with connect and acquire timeouts, every
// connection named, TLS as libpq's sslmode says.

use std::str::FromStr;
use std::time::Duration;

use deadpool_postgres::{Manager, ManagerConfig, Pool, RecyclingMethod, Runtime};
use native_tls::{Certificate, TlsConnector};
use postgres_native_tls::MakeTlsConnector;
use tokio_postgres::config::SslMode;

use crate::BoxError;

/// ADAPT: the name operators see in pg_stat_activity and the server log.
pub const APPLICATION_NAME: &str = "pgmi-gateway-rust";
pub const POOL_TIMEOUT: Duration = Duration::from_secs(5);

pub fn create_pool(database_url: &str, max_size: usize) -> Result<Pool, BoxError> {
    let (database_url, sslmode, rootcert) = split_tls(database_url);
    let mut config = tokio_postgres::Config::from_str(&database_url)?;
    config.application_name(APPLICATION_NAME).connect_timeout(POOL_TIMEOUT);
    let tls = tls_connector(&mut config, sslmode.as_deref(), rootcert.as_deref())?;
    let manager = Manager::from_config(config, tls, ManagerConfig { recycling_method: RecyclingMethod::Fast });
    // ADAPT: bounded; the server's max_connections is shared by every process.
    Ok(Pool::builder(manager)
        .max_size(max_size)
        .wait_timeout(Some(POOL_TIMEOUT))
        .create_timeout(Some(POOL_TIMEOUT))
        .runtime(Runtime::Tokio1)
        .build()?)
}

/// Takes sslmode and sslrootcert out of a postgresql:// URL: tokio-postgres
/// parses only disable, prefer and require, and no sslrootcert.
pub fn split_tls(database_url: &str) -> (String, Option<String>, Option<String>) {
    let Some((base, query)) = database_url.split_once('?') else {
        return (database_url.to_string(), None, None);
    };
    let (mut sslmode, mut rootcert, mut kept) = (None, None, Vec::new());
    for pair in query.split('&').filter(|p| !p.is_empty()) {
        match pair.split_once('=') {
            Some(("sslmode", v)) => sslmode = Some(percent_decode(v)),
            Some(("sslrootcert", v)) => rootcert = Some(percent_decode(v)),
            _ => kept.push(pair),
        }
    }
    let url = if kept.is_empty() { base.to_string() } else { format!("{base}?{}", kept.join("&")) };
    (url, sslmode, rootcert)
}

/// Percent-decoding as libpq reads a URI: `+` stays a plus.
fn percent_decode(s: &str) -> String {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%'
            && let Some(b) = s.get(i + 1..i + 3).and_then(|hex| u8::from_str_radix(hex, 16).ok())
        {
            out.push(b);
            i += 3;
            continue;
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// libpq sslmode: prefer and require encrypt without checking the certificate
/// (require with sslrootcert checks the chain), verify-ca checks the chain
/// against sslrootcert, verify-full also the host name. Anything else is
/// refused at startup: a dropped sslmode is a connection that still works.
fn tls_connector(config: &mut tokio_postgres::Config, sslmode: Option<&str>, rootcert: Option<&str>)
    -> Result<MakeTlsConnector, BoxError> {
    let mut builder = TlsConnector::builder();
    if let Some(path) = rootcert {
        builder.add_root_certificate(Certificate::from_pem(&std::fs::read(path)?)?);
    }
    match sslmode.unwrap_or("prefer") {
        "disable" => {
            config.ssl_mode(SslMode::Disable);
        }
        mode @ ("allow" | "prefer" | "require") => {
            config.ssl_mode(if mode == "require" { SslMode::Require } else { SslMode::Prefer });
            if mode != "require" || rootcert.is_none() {
                builder.danger_accept_invalid_certs(true);
            }
            builder.danger_accept_invalid_hostnames(true);
        }
        "verify-ca" => {
            config.ssl_mode(SslMode::Require);
            builder.danger_accept_invalid_hostnames(true);
        }
        "verify-full" => {
            config.ssl_mode(SslMode::Require);
        }
        other => return Err(format!("sslmode={other} is not a libpq sslmode").into()),
    }
    Ok(MakeTlsConnector::new(builder.build()?))
}
