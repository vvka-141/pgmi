use std::path::PathBuf;
use std::time::Duration;

use super::parse::{self, Fields};
use crate::config::yaml::Connection as YamlConnection;
use crate::env::Env;
use crate::error::{Error, Result};

#[derive(Debug, Default, Clone)]
pub(crate) struct Flags {
    pub(crate) connection: Option<String>,
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

    pub(crate) fn typed(self) -> bool {
        matches!(self, Source::ConnectionFlag | Source::Flags)
    }
}

#[derive(Debug, Clone)]
pub(crate) struct Spec {
    pub(crate) hosts: Vec<(String, u16)>,
    pub(crate) user: String,
    pub(crate) password: Option<String>,
    pub(crate) passfile: Option<PathBuf>,
    pub(crate) maintenance_db: String,
    pub(crate) target_db: String,
    pub(crate) sslmode: String,
    pub(crate) sslrootcert: Option<String>,
    pub(crate) sslcert: Option<String>,
    pub(crate) sslkey: Option<String>,
    pub(crate) sslpassword: Option<String>,
    pub(crate) sslnegotiation_direct: bool,
    pub(crate) sslsni: bool,
    pub(crate) sslcertmode_disable: bool,
    pub(crate) tls_versions: Vec<&'static rustls::SupportedProtocolVersion>,
    pub(crate) channel_binding: String,
    pub(crate) require_auth: Option<String>,
    pub(crate) application_name: String,
    pub(crate) connect_timeout: Option<Duration>,
    pub(crate) options: Option<String>,
    pub(crate) timezone: Option<String>,
    pub(crate) target_session_attrs: String,
    pub(crate) source: Source,
    pub(crate) pg_files: Option<crate::env::PgFiles>,
    pub(crate) sslmode_set: bool,
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
        return Err(Error::config(
            "PGSERVICE is set, but pgmi reads no service files; unset it and name the server with --connection, PGHOST or pgmi.yaml",
        ));
    }
    let granular: Vec<&str> = [
        (flags.host.is_some(), "-h"),
        (flags.port.is_some(), "-p"),
        (flags.username.is_some(), "-U"),
    ]
    .into_iter()
    .filter_map(|(set, name)| set.then_some(name))
    .collect();
    let string = if let Some(c) = &flags.connection {
        if let Some(first) = granular.first() {
            return Err(Error::config(format!(
                "cannot combine --connection with {first}; put the value in the connection string"
            )));
        }
        if flags.sslmode.is_some() {
            return Err(Error::config(
                "cannot combine --connection with --sslmode; put sslmode in the connection string",
            ));
        }
        Some((c.clone(), Source::ConnectionFlag))
    } else if let Some(c) = env.non_empty("PGMI_CONNECTION_STRING")? {
        Some((c.to_string(), Source::PgmiConnectionString))
    } else {
        env.non_empty("DATABASE_URL")?
            .map(|c| (c.to_string(), Source::DatabaseUrl))
    };
    if let (Some((_, source)), Some(first)) = (&string, granular.first()) {
        return Err(Error::config(format!(
            "cannot combine {first} with {}; a connection string from the environment is used whole or not at all, so unset it or pass --connection",
            source.label()
        )));
    }

    let mut f = Fields::new();
    let mut string_db = None;
    let source;
    if let Some((text, src)) = &string {
        f = parse::parse(text)
            .map_err(|e| Error::config(format!("{}: {}", src.label(), e.text())))?;
        string_db = f.remove("dbname").filter(|d| !d.is_empty());
        source = *src;
    } else {
        let y = yaml.cloned().unwrap_or_default();
        source = if flags.host.is_some() {
            Source::Flags
        } else if env.non_empty("PGHOST")?.is_some() {
            Source::PgHost
        } else if y.host.is_some() {
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
        .ok_or_else(|| {
            Error::config(
                "database name is required\nProvide via:\n  1. --database/-d flag: pgmi deploy . -d mydb\n  2. Connection string: pgmi deploy . --connection \"postgresql://user@host/mydb\"\n  3. pgmi.yaml: connection.database: mydb\n  4. Environment variable: export PGDATABASE=mydb, or a database in PGMI_CONNECTION_STRING / DATABASE_URL",
            )
        })?;
    let maintenance_db = if flags.database.is_some() {
        string_db.clone()
    } else {
        None
    }
    .or_else(|| yaml.and_then(|y| y.maintenance_database.clone()))
    .unwrap_or_else(|| "postgres".into());

    let host_text = f.get("host").cloned().unwrap_or_else(|| "localhost".into());
    let hosts: Vec<String> = host_text.split(',').map(|h| h.trim().to_string()).collect();
    let port_text = f.get("port").cloned().unwrap_or_else(|| "5432".into());
    let ports: Vec<u16> = port_text
        .split(',')
        .map(|p| {
            let p = p.trim();
            if p.is_empty() {
                return Ok(5432);
            }
            p.parse::<u16>()
                .ok()
                .filter(|n| *n > 0)
                .ok_or_else(|| Error::config(format!("invalid port \"{p}\"")))
        })
        .collect::<Result<_>>()?;
    if ports.len() != 1 && ports.len() != hosts.len() {
        return Err(Error::config(format!(
            "could not match {} port numbers to {} hosts",
            ports.len(),
            hosts.len()
        )));
    }
    let hosts = hosts
        .into_iter()
        .enumerate()
        .map(|(i, h)| {
            let h = if h.is_empty() {
                "localhost".to_string()
            } else {
                h
            };
            (h, if ports.len() == 1 { ports[0] } else { ports[i] })
        })
        .collect();

    let system_roots = f.get("sslrootcert").map(String::as_str) == Some("system");
    let sslmode = f.get("sslmode").cloned().unwrap_or_else(|| {
        if system_roots {
            "verify-full".into()
        } else {
            "prefer".into()
        }
    });
    if !matches!(
        sslmode.as_str(),
        "disable" | "allow" | "prefer" | "require" | "verify-ca" | "verify-full"
    ) {
        return Err(Error::config(format!(
            "invalid sslmode value: \"{sslmode}\""
        )));
    }
    if system_roots && sslmode != "verify-full" {
        return Err(Error::config(format!(
            "weak sslmode \"{sslmode}\" may not be used with sslrootcert=system (use \"verify-full\")"
        )));
    }
    let sslnegotiation = f
        .get("sslnegotiation")
        .map(String::as_str)
        .unwrap_or("postgres");
    let direct = match sslnegotiation {
        "postgres" => false,
        "direct" => true,
        o => {
            return Err(Error::config(format!(
                "invalid sslnegotiation value: \"{o}\""
            )));
        }
    };
    if direct && matches!(sslmode.as_str(), "disable" | "allow" | "prefer") {
        return Err(Error::config(format!(
            "weak sslmode \"{sslmode}\" may not be used with sslnegotiation=direct (use \"require\", \"verify-ca\", or \"verify-full\")"
        )));
    }
    let sslsni = match f.get("sslsni").map(String::as_str).unwrap_or("") {
        "" | "1" => true,
        "0" => false,
        o => return Err(Error::config(format!("invalid sslsni value: \"{o}\""))),
    };
    let sslcertmode_disable = match f.get("sslcertmode").map(String::as_str).unwrap_or("") {
        "" | "allow" => false,
        "disable" => true,
        "require" => {
            return Err(Error::config(
                "sslcertmode=require is not supported; use allow or disable",
            ));
        }
        o => {
            return Err(Error::config(format!("invalid sslcertmode value: \"{o}\"")));
        }
    };
    let tls_versions = tls_versions(
        f.get("ssl_min_protocol_version"),
        f.get("ssl_max_protocol_version"),
    )?;
    let channel_binding = f
        .get("channel_binding")
        .cloned()
        .unwrap_or_else(|| "prefer".into());
    if !matches!(channel_binding.as_str(), "disable" | "prefer" | "require") {
        return Err(Error::config(format!(
            "invalid channel_binding value: \"{channel_binding}\""
        )));
    }
    let tsa = f
        .get("target_session_attrs")
        .cloned()
        .unwrap_or_else(|| "any".into());
    if !matches!(
        tsa.as_str(),
        "any" | "read-write" | "read-only" | "primary" | "standby" | "prefer-standby"
    ) {
        return Err(Error::config(format!(
            "invalid target_session_attrs value: \"{tsa}\""
        )));
    }
    let connect_timeout = match f.get("connect_timeout") {
        None => None,
        Some(t) => {
            let n: i64 = t
                .trim()
                .parse()
                .map_err(|_| Error::config(format!("invalid connect_timeout \"{t}\"")))?;
            (n > 0).then(|| Duration::from_secs(n.max(2) as u64))
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
        password: f.get("password").cloned(),
        passfile: f.get("passfile").map(PathBuf::from),
        maintenance_db,
        target_db,
        sslmode,
        sslrootcert: f.get("sslrootcert").cloned().filter(|v| !v.is_empty()),
        sslcert: f.get("sslcert").cloned().filter(|v| !v.is_empty()),
        sslkey: f.get("sslkey").cloned().filter(|v| !v.is_empty()),
        sslpassword: f.get("sslpassword").cloned(),
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
        target_session_attrs: tsa,
        source,
        pg_files: env.pg_files(),
        sslmode_set: f.contains_key("sslmode"),
        token: None,
    })
}

fn tls_versions(
    min: Option<&String>,
    max: Option<&String>,
) -> Result<Vec<&'static rustls::SupportedProtocolVersion>> {
    let rank = |key: &str, value: Option<&String>, default: u8| -> Result<u8> {
        match value.map(String::as_str).filter(|v| !v.is_empty()) {
            None => Ok(default),
            Some("TLSv1") => Ok(0),
            Some("TLSv1.1") => Ok(1),
            Some("TLSv1.2") => Ok(2),
            Some("TLSv1.3") => Ok(3),
            Some(o) => Err(Error::config(format!("invalid {key} value: \"{o}\""))),
        }
    };
    let low = rank("ssl_min_protocol_version", min, 2)?;
    let high = rank("ssl_max_protocol_version", max, 3)?;
    if low > high {
        return Err(Error::config(
            "invalid SSL protocol version range: ssl_min_protocol_version is above ssl_max_protocol_version",
        ));
    }
    let versions: Vec<_> = [(2, &rustls::version::TLS12), (3, &rustls::version::TLS13)]
        .into_iter()
        .filter(|(r, _)| low <= *r && *r <= high)
        .map(|(_, v)| v)
        .collect();
    if versions.is_empty() {
        return Err(Error::config(
            "ssl_max_protocol_version is below TLSv1.2; pgmi speaks TLSv1.2 and TLSv1.3",
        ));
    }
    Ok(versions)
}

impl Spec {
    pub(crate) fn first(&self) -> (&str, u16) {
        self.hosts
            .first()
            .map(|(h, p)| (h.as_str(), *p))
            .unwrap_or(("localhost", 5432))
    }

    pub(crate) fn describe(&self) -> String {
        let (h, p) = self.first();
        format!("{h}:{p}/{}", self.target_db)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn flags() -> Flags {
        Flags::default()
    }

    #[test]
    fn string_and_target() {
        let env = Env::from_pairs(&[("PGDATABASE", "fromenv")]);
        let mut fl = flags();
        fl.connection = Some("postgresql://u:p@h:6000/postgres".into());
        let s = resolve(&fl, &env, None).unwrap();
        assert_eq!(s.target_db, "postgres");
        fl.database = Some("app".into());
        let s = resolve(&fl, &env, None).unwrap();
        assert_eq!(
            (s.target_db.as_str(), s.maintenance_db.as_str()),
            ("app", "postgres")
        );
        assert_eq!(s.hosts, vec![("h".to_string(), 6000)]);
        assert_eq!(s.password.as_deref(), Some("p"));
    }

    #[test]
    fn refusals() {
        let env = Env::from_pairs(&[("DATABASE_URL", "postgresql://h/db")]);
        let mut fl = flags();
        fl.host = Some("x".into());
        let e = resolve(&fl, &env, None).unwrap_err();
        assert!(
            e.text().contains("cannot combine -h with DATABASE_URL"),
            "{}",
            e.text()
        );
        let mut fl = flags();
        fl.connection = Some("postgresql://h/db".into());
        fl.host = Some("x".into());
        assert!(
            resolve(&fl, &Env::default(), None)
                .unwrap_err()
                .text()
                .contains("cannot combine --connection with -h")
        );
        let env = Env::from_pairs(&[("PGSERVICE", "x")]);
        assert!(
            resolve(&flags(), &env, None)
                .unwrap_err()
                .text()
                .contains("PGSERVICE")
        );
    }

    #[test]
    fn errors_redact_the_password() {
        let mut fl = flags();
        fl.database = Some("d".into());
        fl.connection = Some("host=h password=Sekrit1 sslmode=Sekrit1".into());
        let e = resolve(&fl, &Env::default(), None).unwrap_err();
        assert!(!e.text().contains("Sekrit1"), "{}", e.text());
        fl.connection = Some("host=h".into());
        let env = Env::from_pairs(&[("PGPASSWORD", "Sekrit2"), ("PGSSLMODE", "Sekrit2")]);
        let e = resolve(&fl, &env, None).unwrap_err();
        assert!(!e.text().contains("Sekrit2"), "{}", e.text());
    }

    #[test]
    fn tls_keys_are_read_or_refused() {
        let mut fl = flags();
        fl.database = Some("d".into());
        let spec = |fl: &Flags, s: &str| {
            let mut fl = fl.clone();
            fl.connection = Some(format!("host=h {s}"));
            resolve(&fl, &Env::default(), None)
        };
        let s = spec(&fl, "").unwrap();
        assert!(s.sslsni && !s.sslcertmode_disable);
        assert_eq!(s.tls_versions.len(), 2);
        let s = spec(
            &fl,
            "ssl_min_protocol_version=TLSv1.3 sslsni=0 sslcertmode=disable",
        )
        .unwrap();
        assert_eq!(s.tls_versions, [&rustls::version::TLS13]);
        assert!(!s.sslsni && s.sslcertmode_disable);
        let s = spec(&fl, "ssl_max_protocol_version=TLSv1.2").unwrap();
        assert_eq!(s.tls_versions, [&rustls::version::TLS12]);
        for bad in [
            "ssl_max_protocol_version=TLSv1.1",
            "ssl_min_protocol_version=TLSv1.3 ssl_max_protocol_version=TLSv1.2",
            "ssl_min_protocol_version=SSLv3",
            "sslsni=2",
            "sslcertmode=require",
            "sslcertmode=sometimes",
        ] {
            assert_eq!(spec(&fl, bad).unwrap_err().exit_code(), 10, "{bad}");
        }
    }

    #[test]
    fn system_roots_mean_verify_full() {
        let mut fl = flags();
        fl.database = Some("d".into());
        fl.connection = Some("host=h sslrootcert=system".into());
        let s = resolve(&fl, &Env::default(), None).unwrap();
        assert_eq!(s.sslmode, "verify-full");
        for weak in ["disable", "allow", "prefer", "require", "verify-ca"] {
            fl.connection = Some(format!("host=h sslrootcert=system sslmode={weak}"));
            let e = resolve(&fl, &Env::default(), None).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{weak}");
            assert!(e.text().contains("sslrootcert=system"), "{}", e.text());
        }
        let env = Env::from_pairs(&[("PGSSLROOTCERT", "system"), ("PGSSLMODE", "require")]);
        fl.connection = Some("host=h".into());
        assert_eq!(resolve(&fl, &env, None).unwrap_err().exit_code(), 10);
    }

    #[test]
    fn ports_per_host() {
        let env = Env::from_pairs(&[("PGHOST", "a,b"), ("PGPORT", "1,2"), ("PGDATABASE", "d")]);
        let s = resolve(&flags(), &env, None).unwrap();
        assert_eq!(s.hosts, vec![("a".into(), 1), ("b".into(), 2)]);
        assert_eq!(s.source, Source::PgHost);
        let env = Env::from_pairs(&[("PGHOST", "a,b,c"), ("PGPORT", "1,2"), ("PGDATABASE", "d")]);
        assert_eq!(resolve(&flags(), &env, None).unwrap_err().exit_code(), 10);
    }
}
