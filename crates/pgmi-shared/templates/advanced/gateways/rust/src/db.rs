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

/// The pool, and its TLS connector for the cancel requests (channel.rs),
/// which open a connection of their own and must encrypt it the same way.
pub fn create_pool(database_url: &str, max_size: usize) -> Result<(Pool, MakeTlsConnector), BoxError> {
    let (database_url, sslmode, rootcert) = split_tls(database_url);
    let (sslmode, rootcert) = with_environment(sslmode, rootcert, |name| std::env::var(name).ok());
    let mut config = tokio_postgres::Config::from_str(&database_url)?;
    config.application_name(APPLICATION_NAME).connect_timeout(POOL_TIMEOUT);
    let tls = tls_connector(&mut config, sslmode.as_deref(), rootcert.as_deref())?;
    let manager = Manager::from_config(config, tls.clone(), ManagerConfig { recycling_method: RecyclingMethod::Fast });
    // ADAPT: bounded; the server's max_connections is shared by every process.
    let pool = Pool::builder(manager)
        .max_size(max_size)
        .wait_timeout(Some(POOL_TIMEOUT))
        .create_timeout(Some(POOL_TIMEOUT))
        .runtime(Runtime::Tokio1)
        .build()?;
    Ok((pool, tls))
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

/// libpq's fallbacks: what the URL leaves out comes from PGSSLMODE and
/// PGSSLROOTCERT. tokio-postgres reads neither.
fn with_environment(
    sslmode: Option<String>,
    rootcert: Option<String>,
    lookup: impl Fn(&str) -> Option<String>,
) -> (Option<String>, Option<String>) {
    let set = |name| lookup(name).filter(|v: &String| !v.is_empty());
    (sslmode.or_else(|| set("PGSSLMODE")), rootcert.or_else(|| set("PGSSLROOTCERT")))
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
///
/// sslrootcert is the only trust anchor, as in libpq: native-tls adds a root
/// to the system's, so the built-in roots are switched off, or any public CA
/// could vouch for the database. sslrootcert=system means the system roots,
/// with verify-full only.
fn tls_connector(config: &mut tokio_postgres::Config, sslmode: Option<&str>, rootcert: Option<&str>)
    -> Result<MakeTlsConnector, BoxError> {
    let mut builder = TlsConnector::builder();
    let system = rootcert == Some("system");
    let mode = sslmode.unwrap_or(if system { "verify-full" } else { "prefer" });
    if system && mode != "verify-full" {
        return Err(format!("sslrootcert=system needs sslmode=verify-full, not {mode}").into());
    }
    if let Some(path) = rootcert.filter(|_| !system) {
        builder.add_root_certificate(Certificate::from_pem(&std::fs::read(path)?)?);
        builder.disable_built_in_roots(true);
    } else if matches!(mode, "verify-ca" | "verify-full") && !system {
        return Err(format!("sslmode={mode} needs sslrootcert in DATABASE_URL: the CA file to trust, or system").into());
    }
    match mode {
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

#[cfg(test)]
mod tests {
    use super::*;

    fn refusal(sslmode: Option<&str>, rootcert: Option<&str>) -> Option<String> {
        tls_connector(&mut tokio_postgres::Config::new(), sslmode, rootcert).err().map(|e| e.to_string())
    }

    #[test]
    fn a_verifying_sslmode_needs_a_trust_anchor() {
        for mode in ["verify-ca", "verify-full"] {
            assert_eq!(
                refusal(Some(mode), None).as_deref(),
                Some(format!("sslmode={mode} needs sslrootcert in DATABASE_URL: the CA file to trust, or system").as_str())
            );
        }
        assert_eq!(
            refusal(Some("verify-ca"), Some("system")).as_deref(),
            Some("sslrootcert=system needs sslmode=verify-full, not verify-ca")
        );
        assert_eq!(refusal(Some("verify-full"), Some("system")), None);
        assert_eq!(refusal(Some("require"), None), None);
        assert_eq!(refusal(None, None), None);
    }

    #[test]
    fn pgsslmode_and_pgsslrootcert_fill_what_the_url_leaves_out() {
        let mode_only = |name: &str| (name == "PGSSLMODE").then(|| "verify-full".to_string());
        let (mode, root) = with_environment(None, None, mode_only);
        assert_eq!(
            refusal(mode.as_deref(), root.as_deref()).as_deref(),
            Some("sslmode=verify-full needs sslrootcert in DATABASE_URL: the CA file to trust, or system")
        );

        let both = |name: &str| match name {
            "PGSSLMODE" => Some("verify-full".to_string()),
            "PGSSLROOTCERT" => Some("system".to_string()),
            _ => None,
        };
        let (mode, root) = with_environment(None, None, both);
        assert_eq!((mode.as_deref(), root.as_deref()), (Some("verify-full"), Some("system")));
        assert_eq!(refusal(mode.as_deref(), root.as_deref()), None);

        let (mode, root) = with_environment(Some("require".into()), Some("ca.pem".into()), both);
        assert_eq!((mode.as_deref(), root.as_deref()), (Some("require"), Some("ca.pem")));
    }
}
