use std::fmt;
use std::path::PathBuf;
use std::str::FromStr;
use std::time::Duration;

use super::parse::{self, Fields, ParseError};
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
    ConnectTimeout(String),
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
            ResolveError::ConnectTimeout(t) => write!(f, "invalid connect_timeout \"{t}\""),
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
            other => other,
        }
    }
}

/// The servers to try, in order; never empty.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Hosts {
    first: (String, u16),
    rest: Vec<(String, u16)>,
}

impl Hosts {
    pub(crate) fn first(&self) -> (&str, u16) {
        (self.first.0.as_str(), self.first.1)
    }

    pub(crate) fn iter(&self) -> impl Iterator<Item = (&str, u16)> {
        std::iter::once(&self.first)
            .chain(&self.rest)
            .map(|(h, p)| (h.as_str(), *p))
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

    let hosts = hosts(
        f.get("host").map_or("localhost", String::as_str),
        f.get("port").map_or("5432", String::as_str),
    )?;

    let system_roots = f.get("sslrootcert").map(String::as_str) == Some("system");
    let requested_sslmode = f
        .get("sslmode")
        .map(|m| setting::<SslMode>("sslmode", m))
        .transpose()?;
    if system_roots && let Some(mode) = requested_sslmode.filter(|m| *m != SslMode::VerifyFull) {
        return Err(ResolveError::WeakForSystemRoots(mode));
    }
    let sslmode = requested_sslmode.unwrap_or(if system_roots {
        SslMode::VerifyFull
    } else {
        SslMode::Prefer
    });
    let direct = match f
        .get("sslnegotiation")
        .map(String::as_str)
        .unwrap_or("postgres")
    {
        "postgres" => false,
        "direct" => true,
        o => return Err(value("sslnegotiation", o)),
    };
    if direct && sslmode.allows_plaintext() {
        return Err(ResolveError::WeakForDirect(sslmode));
    }
    let sslsni = match f.get("sslsni").map(String::as_str).unwrap_or("") {
        "" | "1" => true,
        "0" => false,
        o => return Err(value("sslsni", o)),
    };
    let sslcertmode_disable = match f.get("sslcertmode").map(String::as_str).unwrap_or("") {
        "" | "allow" => false,
        "disable" => true,
        "require" => return Err(ResolveError::CertModeRequire),
        o => return Err(value("sslcertmode", o)),
    };
    let tls_versions = tls_versions(
        f.get("ssl_min_protocol_version"),
        f.get("ssl_max_protocol_version"),
    )?;
    let channel_binding = f
        .get("channel_binding")
        .map_or(Ok(ChannelBinding::Prefer), |v| {
            setting("channel_binding", v)
        })?;
    let target_session_attrs = f
        .get("target_session_attrs")
        .map_or(Ok(TargetSessionAttrs::Any), |v| {
            setting("target_session_attrs", v)
        })?;
    let connect_timeout = match f.get("connect_timeout") {
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
    let user = match f.get("user") {
        Some(u) => u.clone(),
        None => env
            .non_empty("USER")?
            .or(env.non_empty("USERNAME")?)
            .unwrap_or("postgres")
            .to_string(),
    };
    Ok(Spec {
        hosts,
        user,
        password: f.get("password").map(|p| Secret::from(p.as_str())),
        passfile: f.get("passfile").map(PathBuf::from),
        maintenance_db,
        target_db,
        requested_sslmode,
        sslrootcert: f.get("sslrootcert").cloned().filter(|v| !v.is_empty()),
        sslcert: f.get("sslcert").cloned().filter(|v| !v.is_empty()),
        sslkey: f.get("sslkey").cloned().filter(|v| !v.is_empty()),
        sslpassword: f.get("sslpassword").map(|p| Secret::from(p.as_str())),
        sslnegotiation_direct: direct,
        sslsni,
        sslcertmode_disable,
        tls_versions,
        channel_binding,
        require_auth: f.get("require_auth").cloned().filter(|r| !r.is_empty()),
        application_name: f
            .get("application_name")
            .cloned()
            .or_else(|| f.get("fallback_application_name").cloned())
            .unwrap_or_else(|| "pgmi".into()),
        connect_timeout,
        options: f.get("options").cloned().filter(|o| !o.trim().is_empty()),
        timezone: env.non_empty("PGTZ")?.map(str::to_string),
        target_session_attrs,
        source,
        pg_files: env.pg_files(),
        token: None,
    })
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

fn hosts(host_text: &str, port_text: &str) -> Result<Hosts> {
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
    let host = |name: &str, port: u16| {
        let name = if name.is_empty() { "localhost" } else { name };
        (name.to_string(), port)
    };
    let rest = rest_names
        .iter()
        .enumerate()
        .map(|(i, n)| host(n, rest_ports.get(i).copied().unwrap_or(first_port)))
        .collect();
    Ok(Hosts {
        first: host(first_name, first_port),
        rest,
    })
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
