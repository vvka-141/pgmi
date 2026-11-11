use std::fmt;
use std::net::IpAddr;
use std::path::PathBuf;
use std::str::FromStr;
use std::time::Duration;

use super::parse::{self, Fields, ParseError};
use super::socket::{Keepalive, Tcp};
use crate::config::yaml::Connection as YamlConnection;
use crate::env::Env;
use crate::error::{Error, Kind, Secret};

type Result<T> = std::result::Result<T, ResolveError>;

#[derive(Debug, Default, Clone)]
pub(crate) struct Flags {
    pub(crate) connection: Option<Secret>,
    pub(crate) host: Option<String>,
    pub(crate) port: Option<String>,
    pub(crate) username: Option<String>,
    pub(crate) database: Option<String>,
    pub(crate) sslmode: Option<String>,
    pub(crate) sslcert: Option<String>,
    pub(crate) sslkey: Option<String>,
    pub(crate) sslrootcert: Option<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Source {
    ConnectionFlag,
    PgmiConnectionString,
    DatabaseUrl,
    Flags,
    PgHost,
    Yaml,
    Defaults,
}

impl Source {
    pub(crate) fn label(self) -> &'static str {
        match self {
            Source::ConnectionFlag => "--connection",
            Source::PgmiConnectionString => "PGMI_CONNECTION_STRING",
            Source::DatabaseUrl => "DATABASE_URL",
            Source::Flags => "flags",
            Source::PgHost => "PGHOST",
            Source::Yaml => "pgmi.yaml",
            Source::Defaults => "defaults",
        }
    }

    /// Whether the server was named on the command line rather than taken
    /// from the environment, pgmi.yaml or the defaults.
    pub(crate) fn typed(self) -> bool {
        matches!(self, Source::ConnectionFlag | Source::Flags)
    }
}

/// A value a connection setting does not accept.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Unknown;

macro_rules! keywords {
    ($(#[$meta:meta])* $name:ident { $($variant:ident = $text:literal),+ $(,)? }) => {
        $(#[$meta])*
        #[derive(Debug, Clone, Copy, PartialEq, Eq)]
        pub(crate) enum $name {
            $($variant),+
        }

        impl $name {
            pub(crate) const fn as_str(self) -> &'static str {
                match self {
                    $($name::$variant => $text),+
                }
            }
        }

        impl FromStr for $name {
            type Err = Unknown;

            fn from_str(s: &str) -> std::result::Result<Self, Unknown> {
                match s {
                    $($text => Ok($name::$variant),)+
                    _ => Err(Unknown),
                }
            }
        }

        impl fmt::Display for $name {
            fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str(self.as_str())
            }
        }
    };
}

keywords!(SslMode {
    Disable = "disable",
    Allow = "allow",
    Prefer = "prefer",
    Require = "require",
    VerifyCa = "verify-ca",
    VerifyFull = "verify-full",
});

keywords!(ChannelBinding {
    Disable = "disable",
    Prefer = "prefer",
    Require = "require",
});

keywords!(TargetSessionAttrs {
    Any = "any",
    ReadWrite = "read-write",
    ReadOnly = "read-only",
    Primary = "primary",
    Standby = "standby",
    PreferStandby = "prefer-standby",
});

keywords!(LoadBalanceHosts {
    Disable = "disable",
    Random = "random",
});

impl SslMode {
    /// Whether the mode may fall back to, or start with, a plaintext session.
    pub(crate) fn allows_plaintext(self) -> bool {
        matches!(self, SslMode::Disable | SslMode::Allow | SslMode::Prefer)
    }
}

impl serde::Serialize for SslMode {
    fn serialize<S: serde::Serializer>(&self, s: S) -> std::result::Result<S::Ok, S::Error> {
        s.serialize_str(self.as_str())
    }
}

/// Why the connection settings were refused before connecting. Every
/// variant but `Env` is exit 10; `Env` keeps the kind the environment
/// snapshot chose.
#[derive(Debug)]
pub(crate) enum ResolveError {
    Env(Error),
    ServiceEnv,
    ConnectionWith(&'static str),
    ConnectionWithSslmode,
    FlagWithEnvString { flag: &'static str, source: Source },
    Parse { source: Source, error: ParseError },
    NoDatabase,
    Port(String),
    PortCount { ports: usize, hosts: usize },
    Value { key: &'static str, value: String },
    WeakForSystemRoots(SslMode),
    WeakForDirect(SslMode),
    CertModeRequire,
    TlsRange,
    TlsTooOld,
    ProtocolUnsupported(String),
    ConnectTimeout(String),
    HostaddrCount { addrs: usize, hosts: usize },
    NotSupported { setting: String, why: &'static str },
    Unhandled(String),
}

impl fmt::Display for ResolveError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ResolveError::Env(e) => f.write_str(&e.text()),
            ResolveError::ServiceEnv => f.write_str(
                "PGSERVICE is set, but pgmi reads no service files; unset it and name the server with --connection, PGHOST or pgmi.yaml",
            ),
            ResolveError::ConnectionWith(flag) => write!(
                f,
                "cannot combine --connection with {flag}; put the value in the connection string"
            ),
            ResolveError::ConnectionWithSslmode => f.write_str(
                "cannot combine --connection with --sslmode; put sslmode in the connection string",
            ),
            ResolveError::FlagWithEnvString { flag, source } => write!(
                f,
                "cannot combine {flag} with {}; a connection string from the environment is used whole or not at all, so unset it or pass --connection",
                source.label()
            ),
            ResolveError::Parse { source, error } => write!(f, "{}: {error}", source.label()),
            ResolveError::NoDatabase => f.write_str(
                "database name is required\nProvide via:\n  1. --database/-d flag: pgmi deploy . -d mydb\n  2. Connection string: pgmi deploy . --connection \"postgresql://user@host/mydb\"\n  3. pgmi.yaml: connection.database: mydb\n  4. Environment variable: export PGDATABASE=mydb, or a database in PGMI_CONNECTION_STRING / DATABASE_URL",
            ),
            ResolveError::Port(p) => write!(f, "invalid port \"{p}\""),
            ResolveError::PortCount { ports, hosts } => {
                write!(f, "could not match {ports} port numbers to {hosts} hosts")
            }
            ResolveError::Value { key, value } => write!(f, "invalid {key} value: \"{value}\""),
            ResolveError::WeakForSystemRoots(mode) => write!(
                f,
                "weak sslmode \"{mode}\" may not be used with sslrootcert=system (use \"verify-full\")"
            ),
            ResolveError::WeakForDirect(mode) => write!(
                f,
                "weak sslmode \"{mode}\" may not be used with sslnegotiation=direct (use \"require\", \"verify-ca\", or \"verify-full\")"
            ),
            ResolveError::CertModeRequire => {
                f.write_str("sslcertmode=require is not supported; use allow or disable")
            }
            ResolveError::TlsRange => f.write_str(
                "invalid SSL protocol version range: ssl_min_protocol_version is above ssl_max_protocol_version",
            ),
            ResolveError::TlsTooOld => f.write_str(
                "ssl_max_protocol_version is below TLSv1.2; pgmi speaks TLSv1.2 and TLSv1.3",
            ),
            ResolveError::ProtocolUnsupported(v) => write!(
                f,
                "min_protocol_version={v}: protocol {v} is not supported yet; pgmi speaks protocol 3.0, so remove min_protocol_version or set it to 3.0"
            ),
            ResolveError::ConnectTimeout(t) => write!(f, "invalid connect_timeout \"{t}\""),
            ResolveError::HostaddrCount { addrs, hosts } => {
                write!(f, "could not match {hosts} host names to {addrs} hostaddr values")
            }
            ResolveError::NotSupported { setting, why } => {
                write!(f, "{setting} is not supported: {why}")
            }
            ResolveError::Unhandled(k) => write!(
                f,
                "connection option \"{k}\" is accepted but not handled; this is a pgmi bug, please report it"
            ),
        }
    }
}

impl std::error::Error for ResolveError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            ResolveError::Parse { error, .. } => Some(error),
            _ => None,
        }
    }
}

impl From<Error> for ResolveError {
    fn from(e: Error) -> Self {
        ResolveError::Env(e)
    }
}

impl From<ResolveError> for Error {
    fn from(e: ResolveError) -> Self {
        match e {
            ResolveError::Env(e) => e,
            other => Error::new(Kind::Config, other.to_string()),
        }
    }
}

impl ResolveError {
    fn redact(self, secrets: &[String]) -> Self {
        let hide = |s: String| crate::error::redact(&s, secrets);
        match self {
            ResolveError::Env(e) => ResolveError::Env(e.redact(secrets)),
            ResolveError::Port(p) => ResolveError::Port(hide(p)),
            ResolveError::Value { key, value } => ResolveError::Value {
                key,
                value: hide(value),
            },
            ResolveError::ConnectTimeout(t) => ResolveError::ConnectTimeout(hide(t)),
            ResolveError::NotSupported { setting, why } => ResolveError::NotSupported {
                setting: hide(setting),
                why,
            },
            other => other,
        }
    }
}

/// One server to try: the name used for TLS, `.pgpass` and messages, and,
/// from `hostaddr`, the address to connect to instead of looking the name up.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Endpoint {
    pub(crate) name: String,
    pub(crate) port: u16,
    pub(crate) addr: Option<IpAddr>,
}

/// The servers to try, in order; never empty.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Hosts {
    first: Endpoint,
    rest: Vec<Endpoint>,
}

impl Hosts {
    pub(crate) fn first(&self) -> (&str, u16) {
        (self.first.name.as_str(), self.first.port)
    }

    pub(crate) fn endpoints(&self) -> impl Iterator<Item = &Endpoint> {
        std::iter::once(&self.first).chain(&self.rest)
    }
}

#[derive(Debug, Clone)]
pub(crate) struct Spec {
    pub(crate) hosts: Hosts,
    pub(crate) user: String,
    pub(crate) password: Option<Secret>,
    pub(crate) passfile: Option<PathBuf>,
    pub(crate) maintenance_db: String,
    pub(crate) target_db: String,
    /// The mode asked for; `sslmode()` is the one used.
    pub(crate) requested_sslmode: Option<SslMode>,
    /// Where `requested_sslmode` came from: `--sslmode`, `PGSSLMODE`, the
    /// connection string's source or pgmi.yaml.
    pub(crate) sslmode_source: &'static str,
    pub(crate) sslrootcert: Option<String>,
    pub(crate) sslcert: Option<String>,
    pub(crate) sslkey: Option<String>,
    pub(crate) sslpassword: Option<Secret>,
    pub(crate) sslnegotiation_direct: bool,
    pub(crate) sslsni: bool,
    pub(crate) sslcertmode_disable: bool,
    pub(crate) tls_versions: Vec<&'static rustls::SupportedProtocolVersion>,
    pub(crate) channel_binding: ChannelBinding,
    pub(crate) require_auth: Option<String>,
    pub(crate) application_name: String,
    pub(crate) connect_timeout: Option<Duration>,
    pub(crate) options: Option<String>,
    pub(crate) timezone: Option<String>,
    pub(crate) target_session_attrs: TargetSessionAttrs,
    pub(crate) load_balance_hosts: LoadBalanceHosts,
    pub(crate) tcp: Tcp,
    /// The session's `client_encoding` when the user named one; otherwise
    /// the deploy matches the database's encoding.
    pub(crate) client_encoding: Option<String>,
    pub(crate) source: Source,
    pub(crate) pg_files: Option<crate::env::PgFiles>,
    pub(crate) token: Option<crate::conn::cloud::Token>,
}

const ENV_DEFAULTS: &[(&str, &str)] = &[
    ("PGHOST", "host"),
    ("PGPORT", "port"),
    ("PGUSER", "user"),
    ("PGPASSWORD", "password"),
    ("PGPASSFILE", "passfile"),
    ("PGSSLMODE", "sslmode"),
    ("PGSSLROOTCERT", "sslrootcert"),
    ("PGSSLCERT", "sslcert"),
    ("PGSSLKEY", "sslkey"),
    ("PGSSLPASSWORD", "sslpassword"),
    ("PGSSLNEGOTIATION", "sslnegotiation"),
    ("PGAPPNAME", "application_name"),
    ("PGCONNECT_TIMEOUT", "connect_timeout"),
    ("PGOPTIONS", "options"),
    ("PGTARGETSESSIONATTRS", "target_session_attrs"),
    ("PGCHANNELBINDING", "channel_binding"),
    ("PGREQUIREAUTH", "require_auth"),
    ("PGCLIENTENCODING", "client_encoding"),
];

pub(crate) fn resolve(flags: &Flags, env: &Env, yaml: Option<&YamlConnection>) -> Result<Spec> {
    if env.non_empty("PGSERVICE")?.is_some() {
        return Err(ResolveError::ServiceEnv);
    }
    let granular: Vec<&'static str> = [
        (flags.host.is_some(), "-h"),
        (flags.port.is_some(), "-p"),
        (flags.username.is_some(), "-U"),
    ]
    .into_iter()
    .filter_map(|(set, name)| set.then_some(name))
    .collect();
    let string = if let Some(c) = &flags.connection {
        if let Some(first) = granular.first() {
            return Err(ResolveError::ConnectionWith(first));
        }
        if flags.sslmode.is_some() {
            return Err(ResolveError::ConnectionWithSslmode);
        }
        Some((c.expose().to_string(), Source::ConnectionFlag))
    } else if let Some(c) = env.non_empty("PGMI_CONNECTION_STRING")? {
        Some((c.to_string(), Source::PgmiConnectionString))
    } else {
        env.non_empty("DATABASE_URL")?
            .map(|c| (c.to_string(), Source::DatabaseUrl))
    };
    if let (Some((_, source)), Some(flag)) = (&string, granular.first()) {
        return Err(ResolveError::FlagWithEnvString {
            flag,
            source: *source,
        });
    }

    let mut f = Fields::new();
    let mut string_db = None;
    let source;
    if let Some((text, src)) = &string {
        f = parse::parse(text).map_err(|error| ResolveError::Parse {
            source: *src,
            error,
        })?;
        string_db = f.remove("dbname").filter(|d| !d.is_empty());
        source = *src;
    } else {
        source = if flags.host.is_some() {
            Source::Flags
        } else if env.non_empty("PGHOST")?.is_some() {
            Source::PgHost
        } else if yaml.is_some_and(|y| y.host.is_some()) {
            Source::Yaml
        } else {
            Source::Defaults
        };
        for (k, v) in [
            ("host", &flags.host),
            ("port", &flags.port),
            ("user", &flags.username),
        ] {
            if let Some(v) = v {
                f.insert(k.into(), v.clone());
            }
        }
    }
    let mut secrets: Vec<String> = ["password", "sslpassword"]
        .iter()
        .filter_map(|k| f.get(*k).cloned())
        .collect();
    for name in ["PGPASSWORD", "PGSSLPASSWORD"] {
        secrets.extend(env.non_empty(name)?.map(str::to_string));
    }
    finish(flags, env, yaml, f, string_db, source, string.is_none()).map_err(|e| e.redact(&secrets))
}

fn finish(
    flags: &Flags,
    env: &Env,
    yaml: Option<&YamlConnection>,
    mut f: Fields,
    string_db: Option<String>,
    source: Source,
    no_string: bool,
) -> Result<Spec> {
    let sslmode_source = if flags.sslmode.is_some() {
        "--sslmode"
    } else if f.contains_key("sslmode") {
        source.label()
    } else if env.non_empty("PGSSLMODE")?.is_some() {
        "PGSSLMODE"
    } else {
        "pgmi.yaml"
    };
    for (env_name, key) in ENV_DEFAULTS {
        if !f.contains_key(*key)
            && let Some(v) = env.non_empty(env_name)?
        {
            f.insert((*key).into(), v.to_string());
        }
    }
    if let Some(y) = yaml.filter(|_| no_string) {
        for (k, v) in [
            ("host", &y.host),
            ("port", &y.port),
            ("user", &y.username),
            ("sslmode", &y.sslmode),
            ("sslcert", &y.sslcert),
            ("sslkey", &y.sslkey),
            ("sslrootcert", &y.sslrootcert),
        ] {
            if let Some(v) = v {
                f.entry(k.into()).or_insert_with(|| v.clone());
            }
        }
    }
    for (k, v) in [
        ("sslmode", &flags.sslmode),
        ("sslcert", &flags.sslcert),
        ("sslkey", &flags.sslkey),
        ("sslrootcert", &flags.sslrootcert),
    ] {
        if let Some(v) = v {
            f.insert(k.into(), v.clone());
        }
    }

    let target_db = flags
        .database
        .clone()
        .or_else(|| string_db.clone())
        .or(env.non_empty("PGDATABASE")?.map(str::to_string))
        .or_else(|| yaml.and_then(|y| y.database.clone()))
        .filter(|d| !d.is_empty())
        .ok_or(ResolveError::NoDatabase)?;
    let maintenance_db = if flags.database.is_some() {
        string_db.clone()
    } else {
        None
    }
    .or_else(|| yaml.and_then(|y| y.maintenance_database.clone()))
    .unwrap_or_else(|| "postgres".into());

    let hostaddr = f.remove("hostaddr").filter(|a| !a.trim().is_empty());
    let host_text = f.remove("host").or_else(|| hostaddr.clone());
    let hosts = hosts(
        host_text.as_deref().unwrap_or("localhost"),
        f.remove("port").as_deref().unwrap_or("5432"),
        hostaddr.as_deref(),
    )?;

    let sslrootcert = f.remove("sslrootcert").filter(|v| !v.is_empty());
    let system_roots = sslrootcert.as_deref() == Some("system");
    let requested_sslmode = f
        .remove("sslmode")
        .map(|m| setting::<SslMode>("sslmode", &m))
        .transpose()?;
    if system_roots && let Some(mode) = requested_sslmode.filter(|m| *m != SslMode::VerifyFull) {
        return Err(ResolveError::WeakForSystemRoots(mode));
    }
    let sslmode = requested_sslmode.unwrap_or(if system_roots {
        SslMode::VerifyFull
    } else {
        SslMode::Prefer
    });
    let direct = match f.remove("sslnegotiation").as_deref().unwrap_or("postgres") {
        "postgres" => false,
        "direct" => true,
        o => return Err(value("sslnegotiation", o)),
    };
    if direct && sslmode.allows_plaintext() {
        return Err(ResolveError::WeakForDirect(sslmode));
    }
    let sslsni = match f.remove("sslsni").as_deref().unwrap_or("") {
        "" | "1" => true,
        "0" => false,
        o => return Err(value("sslsni", o)),
    };
    let sslcertmode_disable = match f.remove("sslcertmode").as_deref().unwrap_or("") {
        "" | "allow" => false,
        "disable" => true,
        "require" => return Err(ResolveError::CertModeRequire),
        o => return Err(value("sslcertmode", o)),
    };
    let tls_versions = tls_versions(
        f.remove("ssl_min_protocol_version").as_ref(),
        f.remove("ssl_max_protocol_version").as_ref(),
    )?;
    let min_protocol = f.remove("min_protocol_version");
    for (key, given) in [
        ("min_protocol_version", &min_protocol),
        ("max_protocol_version", &f.remove("max_protocol_version")),
    ] {
        match given.as_deref() {
            None | Some("3.0" | "3.2" | "latest") => {}
            Some(o) => return Err(value(key, o)),
        }
    }
    if let Some(v) = min_protocol.filter(|v| v.as_str() != "3.0") {
        return Err(ResolveError::ProtocolUnsupported(v));
    }
    let channel_binding = f
        .remove("channel_binding")
        .map_or(Ok(ChannelBinding::Prefer), |v| {
            setting("channel_binding", &v)
        })?;
    let target_session_attrs = f
        .remove("target_session_attrs")
        .map_or(Ok(TargetSessionAttrs::Any), |v| {
            setting("target_session_attrs", &v)
        })?;
    let load_balance_hosts = f
        .remove("load_balance_hosts")
        .map_or(Ok(LoadBalanceHosts::Disable), |v| {
            setting("load_balance_hosts", &v)
        })?;
    let connect_timeout = match f.remove("connect_timeout") {
        None => None,
        Some(t) => {
            let n: i64 = t
                .trim()
                .parse()
                .map_err(|_| ResolveError::ConnectTimeout(t.clone()))?;
            u64::try_from(n)
                .ok()
                .filter(|n| *n > 0)
                .map(|n| Duration::from_secs(n.max(2)))
        }
    };
    let tcp = tcp(&mut f)?;
    let client_encoding = f.remove("client_encoding").filter(|e| !e.trim().is_empty());
    if client_encoding
        .as_deref()
        .is_some_and(|e| e.trim().eq_ignore_ascii_case("auto"))
    {
        return Err(ResolveError::NotSupported {
            setting: "client_encoding=auto".into(),
            why: "pgmi does not derive an encoding from the locale; name one, such as UTF8 or LATIN1, or remove it to use the database's encoding",
        });
    }
    without_gssapi(&mut f)?;
    session_neutral(&mut f)?;
    for k in ["requirepeer", "sslcrl", "sslcrldir"] {
        f.remove(k);
    }
    let user = match f.remove("user") {
        Some(u) => u,
        None => env
            .non_empty("USER")?
            .or(env.non_empty("USERNAME")?)
            .unwrap_or("postgres")
            .to_string(),
    };
    let application_name = f.remove("application_name");
    let fallback_application_name = f.remove("fallback_application_name");
    let spec = Spec {
        hosts,
        user,
        password: f.remove("password").map(Secret::from),
        passfile: f.remove("passfile").map(PathBuf::from),
        maintenance_db,
        target_db,
        requested_sslmode,
        sslmode_source,
        sslrootcert,
        sslcert: f.remove("sslcert").filter(|v| !v.is_empty()),
        sslkey: f.remove("sslkey").filter(|v| !v.is_empty()),
        sslpassword: f.remove("sslpassword").map(Secret::from),
        sslnegotiation_direct: direct,
        sslsni,
        sslcertmode_disable,
        tls_versions,
        channel_binding,
        require_auth: f.remove("require_auth").filter(|r| !r.is_empty()),
        application_name: application_name
            .or(fallback_application_name)
            .unwrap_or_else(|| "pgmi".into()),
        connect_timeout,
        options: f.remove("options").filter(|o| !o.trim().is_empty()),
        timezone: env.non_empty("PGTZ")?.map(str::to_string),
        target_session_attrs,
        load_balance_hosts,
        tcp,
        client_encoding,
        source,
        pg_files: env.pg_files(),
        token: None,
    };
    match f.into_keys().next() {
        Some(k) => Err(ResolveError::Unhandled(k)),
        None => Ok(spec),
    }
}

fn whole(f: &mut Fields, key: &'static str) -> Result<Option<u32>> {
    let Some(v) = f.remove(key).filter(|v| !v.trim().is_empty()) else {
        return Ok(None);
    };
    v.trim()
        .parse::<i32>()
        .ok()
        .and_then(|n| u32::try_from(n).ok())
        .map(Some)
        .ok_or_else(|| value(key, &v))
}

fn tcp(f: &mut Fields) -> Result<Tcp> {
    let on = whole(f, "keepalives")?;
    let idle = whole(f, "keepalives_idle")?;
    let interval = whole(f, "keepalives_interval")?;
    let count = whole(f, "keepalives_count")?;
    let user_timeout = whole(f, "tcp_user_timeout")?;
    let strict = [on, idle, interval, count, user_timeout]
        .iter()
        .any(Option::is_some);
    let given = |v: Option<u32>| v.filter(|n| *n > 0);
    let d = Keepalive::default();
    let secs = |v, d| given(v).map_or(d, |n| Duration::from_secs(n.into()));
    Ok(Tcp {
        keepalive: (on != Some(0)).then(|| Keepalive {
            idle: secs(idle, d.idle),
            interval: secs(interval, d.interval),
            count: given(count).unwrap_or(d.count),
        }),
        user_timeout: given(user_timeout).map(|n| Duration::from_millis(n.into())),
        strict,
    })
}

const NO_GSSAPI: &str = "pgmi has no GSSAPI (Kerberos) support; remove it";

fn without_gssapi(f: &mut Fields) -> Result<()> {
    let refuse = |setting: String| ResolveError::NotSupported {
        setting,
        why: NO_GSSAPI,
    };
    match f.remove("gssencmode").as_deref() {
        None | Some("disable" | "prefer") => {}
        Some("require") => {
            return Err(ResolveError::NotSupported {
                setting: "gssencmode=require".into(),
                why: "pgmi has no GSSAPI encryption; use gssencmode=disable or prefer, and sslmode for an encrypted connection",
            });
        }
        Some(o) => return Err(value("gssencmode", o)),
    }
    match f.remove("gssdelegation").as_deref() {
        None | Some("" | "0") => {}
        Some("1") => return Err(refuse("gssdelegation=1".into())),
        Some(o) => return Err(value("gssdelegation", o)),
    }
    if let Some(lib) = f.remove("gsslib").filter(|v| !v.is_empty()) {
        return Err(refuse(format!("gsslib={lib}")));
    }
    f.remove("krbsrvname");
    Ok(())
}

fn session_neutral(f: &mut Fields) -> Result<()> {
    match f.remove("sslcompression").as_deref() {
        None | Some("" | "0") => {}
        Some("1") => {
            return Err(ResolveError::NotSupported {
                setting: "sslcompression=1".into(),
                why: "pgmi's TLS never compresses, which is the safe choice; remove it or set 0",
            });
        }
        Some(o) => return Err(value("sslcompression", o)),
    }
    match f.remove("default_query_exec_mode").as_deref() {
        None
        | Some(
            "cache_statement" | "cache_describe" | "describe_exec" | "exec" | "simple_protocol",
        ) => {}
        Some(o) => return Err(value("default_query_exec_mode", o)),
    }
    for key in ["statement_cache_capacity", "description_cache_capacity"] {
        if let Some(v) = f.remove(key)
            && v.parse::<i32>().is_err()
        {
            return Err(value(key, &v));
        }
    }
    Ok(())
}

fn value(key: &'static str, value: &str) -> ResolveError {
    ResolveError::Value {
        key,
        value: value.to_string(),
    }
}

fn setting<T: FromStr<Err = Unknown>>(key: &'static str, text: &str) -> Result<T> {
    text.parse().map_err(|Unknown| value(key, text))
}

fn split_list(text: &str) -> (&str, Vec<&str>) {
    let mut parts = text.split(',').map(str::trim);
    (parts.next().unwrap_or_default(), parts.collect())
}

fn hosts(host_text: &str, port_text: &str, addr_text: Option<&str>) -> Result<Hosts> {
    let port = |p: &str| -> Result<u16> {
        if p.is_empty() {
            return Ok(5432);
        }
        p.parse::<u16>()
            .ok()
            .filter(|n| *n > 0)
            .ok_or_else(|| ResolveError::Port(p.to_string()))
    };
    let (first_name, rest_names) = split_list(host_text);
    let (first_port, rest_ports) = split_list(port_text);
    let first_port = port(first_port)?;
    let rest_ports = rest_ports
        .into_iter()
        .map(port)
        .collect::<Result<Vec<_>>>()?;
    if !rest_ports.is_empty() && rest_ports.len() != rest_names.len() {
        return Err(ResolveError::PortCount {
            ports: rest_ports.len() + 1,
            hosts: rest_names.len() + 1,
        });
    }
    let mut addrs = match addr_text {
        None => Vec::new(),
        Some(text) => text
            .split(',')
            .map(str::trim)
            .map(|a| {
                (!a.is_empty())
                    .then(|| a.parse::<IpAddr>().map_err(|_| value("hostaddr", a)))
                    .transpose()
            })
            .collect::<Result<Vec<_>>>()?,
    }
    .into_iter();
    if addr_text.is_some() && addrs.len() != rest_names.len() + 1 {
        return Err(ResolveError::HostaddrCount {
            addrs: addrs.len(),
            hosts: rest_names.len() + 1,
        });
    }
    let mut host = |name: &str, port: u16| Endpoint {
        name: if name.is_empty() { "localhost" } else { name }.to_string(),
        port,
        addr: addrs.next().flatten(),
    };
    let first = host(first_name, first_port);
    let rest = rest_names
        .iter()
        .enumerate()
        .map(|(i, n)| host(n, rest_ports.get(i).copied().unwrap_or(first_port)))
        .collect();
    Ok(Hosts { first, rest })
}

fn tls_versions(
    min: Option<&String>,
    max: Option<&String>,
) -> Result<Vec<&'static rustls::SupportedProtocolVersion>> {
    let rank = |key: &'static str, given: Option<&String>, default: u8| -> Result<u8> {
        match given.map(String::as_str).filter(|v| !v.is_empty()) {
            None => Ok(default),
            Some("TLSv1") => Ok(0),
            Some("TLSv1.1") => Ok(1),
            Some("TLSv1.2") => Ok(2),
            Some("TLSv1.3") => Ok(3),
            Some(o) => Err(value(key, o)),
        }
    };
    let low = rank("ssl_min_protocol_version", min, 2)?;
    let high = rank("ssl_max_protocol_version", max, 3)?;
    if low > high {
        return Err(ResolveError::TlsRange);
    }
    let versions: Vec<_> = [(2, &rustls::version::TLS12), (3, &rustls::version::TLS13)]
        .into_iter()
        .filter(|(r, _)| low <= *r && *r <= high)
        .map(|(_, v)| v)
        .collect();
    if versions.is_empty() {
        return Err(ResolveError::TlsTooOld);
    }
    Ok(versions)
}

impl Spec {
    pub(crate) fn first(&self) -> (&str, u16) {
        self.hosts.first()
    }

    /// The mode the session uses: the one asked for, else `verify-full` with
    /// `sslrootcert=system`, else `prefer`.
    pub(crate) fn sslmode(&self) -> SslMode {
        self.requested_sslmode
            .unwrap_or(if self.sslrootcert.as_deref() == Some("system") {
                SslMode::VerifyFull
            } else {
                SslMode::Prefer
            })
    }

    pub(crate) fn describe(&self) -> String {
        let (h, p) = self.first();
        format!("{h}:{p}/{}", self.target_db)
    }
}
