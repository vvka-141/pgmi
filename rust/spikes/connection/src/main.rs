mod split;
mod tap;

use std::collections::BTreeMap;
use std::io::BufReader;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use futures_util::future::poll_fn;
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::client::WebPkiServerVerifier;
use rustls::crypto::{CryptoProvider, ring};
use rustls::pki_types::{CertificateDer, PrivateKeyDer, ServerName, UnixTime};
use rustls::{CertificateError, ClientConfig, DigitallySignedStruct, RootCertStore, SignatureScheme};
use tokio::io::{AsyncRead, AsyncWrite};
use tokio_postgres::config::{ChannelBinding, SslMode, SslNegotiation};
use tokio_postgres::tls::MakeTlsConnect;
use tokio_postgres::{AsyncMessage, Client, Config, NoTls};
use tokio_postgres_rustls::MakeRustlsConnect;

trait Io: AsyncRead + AsyncWrite + Unpin + Send {}
impl<T: AsyncRead + AsyncWrite + Unpin + Send> Io for T {}
type Stream = Box<dyn Io>;

#[derive(Debug)]
enum Failure {
    Usage(String),
    Config(String),
    Connect(String),
    Sql(String),
    Concurrent(String),
    Timeout,
    Interrupted,
}

impl Failure {
    fn code(&self) -> i32 {
        match self {
            Failure::Usage(_) => 2,
            Failure::Config(_) => 10,
            Failure::Connect(_) => 11,
            Failure::Sql(_) => 13,
            Failure::Concurrent(_) => 15,
            Failure::Timeout => 16,
            Failure::Interrupted => 130,
        }
    }
    fn message(&self) -> String {
        match self {
            Failure::Usage(m) | Failure::Config(m) | Failure::Connect(m) | Failure::Sql(m) | Failure::Concurrent(m) => m.clone(),
            Failure::Timeout => "the deploy exceeded --timeout".into(),
            Failure::Interrupted => "interrupted".into(),
        }
    }
}

struct Args {
    project: PathBuf,
    connection: Option<String>,
    database: Option<String>,
    host: Option<String>,
    port: Option<String>,
    user: Option<String>,
    ssl: BTreeMap<String, String>,
    json: bool,
    timeout: Duration,
}

fn parse_duration(s: &str) -> Option<Duration> {
    let mut total = 0f64;
    let mut num = String::new();
    let mut chars = s.chars().peekable();
    while let Some(c) = chars.next() {
        if c.is_ascii_digit() || c == '.' {
            num.push(c);
            continue;
        }
        let mut unit = c.to_string();
        if c == 'm' && chars.peek() == Some(&'s') {
            unit.push(chars.next().unwrap());
        }
        let n: f64 = num.parse().ok()?;
        num.clear();
        total += n * match unit.as_str() {
            "h" => 3600.0,
            "m" => 60.0,
            "s" => 1.0,
            "ms" => 0.001,
            _ => return None,
        };
    }
    if !num.is_empty() {
        return None;
    }
    Some(Duration::from_secs_f64(total))
}

fn parse_args() -> Result<Args, Failure> {
    let raw: Vec<String> = std::env::args().skip(1).collect();
    if raw.first().map(String::as_str) != Some("deploy") {
        return Err(Failure::Usage("the spike only implements deploy".into()));
    }
    let mut it = raw.into_iter().skip(1);
    let mut args = Args {
        project: PathBuf::new(),
        connection: None,
        database: None,
        host: None,
        port: None,
        user: None,
        ssl: BTreeMap::new(),
        json: false,
        timeout: Duration::from_secs(180),
    };
    let mut project = None;
    while let Some(a) = it.next() {
        let mut value = |name: &str| it.next().ok_or_else(|| Failure::Usage(format!("{name} needs a value")));
        match a.as_str() {
            "--connection" => args.connection = Some(value("--connection")?),
            "-d" | "--database" => args.database = Some(value("-d")?),
            "-h" | "--host" => args.host = Some(value("-h")?),
            "-p" | "--port" => args.port = Some(value("-p")?),
            "-U" | "--username" => args.user = Some(value("-U")?),
            "--sslmode" | "--sslrootcert" | "--sslcert" | "--sslkey" => {
                let v = value(&a)?;
                args.ssl.insert(a.trim_start_matches("--").to_string(), v);
            }
            "--json" => args.json = true,
            "--timeout" => {
                let v = value("--timeout")?;
                args.timeout = parse_duration(&v).ok_or_else(|| Failure::Usage(format!("invalid --timeout {v}")))?;
            }
            s if s.starts_with('-') => return Err(Failure::Usage(format!("unknown flag: {s}"))),
            _ if project.is_none() => project = Some(PathBuf::from(&a)),
            _ => return Err(Failure::Usage("too many arguments".into())),
        }
    }
    args.project = project.ok_or_else(|| Failure::Usage("deploy needs a project path".into()))?;
    Ok(args)
}

const ENV_KEYS: &[(&str, &str)] = &[
    ("PGHOST", "host"),
    ("PGPORT", "port"),
    ("PGUSER", "user"),
    ("PGPASSWORD", "password"),
    ("PGDATABASE", "dbname"),
    ("PGSSLMODE", "sslmode"),
    ("PGSSLROOTCERT", "sslrootcert"),
    ("PGSSLCERT", "sslcert"),
    ("PGSSLKEY", "sslkey"),
    ("PGSSLPASSWORD", "sslpassword"),
    ("PGSSLNEGOTIATION", "sslnegotiation"),
    ("PGAPPNAME", "application_name"),
    ("PGCONNECT_TIMEOUT", "connect_timeout"),
    ("PGPASSFILE", "passfile"),
    ("PGOPTIONS", "options"),
    ("PGTARGETSESSIONATTRS", "target_session_attrs"),
    ("PGCHANNELBINDING", "channel_binding"),
    ("PGREQUIREAUTH", "require_auth"),
];

fn parse_keyword(s: &str) -> Result<BTreeMap<String, String>, Failure> {
    let mut out = BTreeMap::new();
    let mut chars = s.chars().peekable();
    loop {
        while chars.peek().is_some_and(|c| c.is_whitespace()) {
            chars.next();
        }
        if chars.peek().is_none() {
            return Ok(out);
        }
        let key: String = std::iter::from_fn(|| chars.next_if(|c| *c != '=' && !c.is_whitespace())).collect();
        while chars.peek().is_some_and(|c| c.is_whitespace()) {
            chars.next();
        }
        if chars.next() != Some('=') {
            return Err(Failure::Config(format!("missing = after {key}")));
        }
        while chars.peek().is_some_and(|c| c.is_whitespace()) {
            chars.next();
        }
        let mut value = String::new();
        if chars.peek() == Some(&'\'') {
            chars.next();
            loop {
                match chars.next() {
                    Some('\\') => value.extend(chars.next()),
                    Some('\'') => break,
                    Some(c) => value.push(c),
                    None => return Err(Failure::Config("unterminated quote".into())),
                }
            }
        } else {
            while let Some(c) = chars.next_if(|c| !c.is_whitespace()) {
                if c == '\\' {
                    value.extend(chars.next());
                } else {
                    value.push(c);
                }
            }
        }
        out.insert(key, value);
    }
}

fn parse_uri(s: &str) -> Result<BTreeMap<String, String>, Failure> {
    let rest = s.split_once("://").map(|x| x.1).unwrap_or(s);
    let (authority_path, query) = rest.split_once('?').unwrap_or((rest, ""));
    let (authority, path) = authority_path.split_once('/').unwrap_or((authority_path, ""));
    let (userinfo, hostlist) = authority.rsplit_once('@').map_or(("", authority), |(u, h)| (u, h));
    let dec = |v: &str| url::form_urlencoded::parse(format!("x={v}").as_bytes()).next().map(|(_, v)| v.into_owned()).unwrap_or_default();
    let mut out = BTreeMap::new();
    if !userinfo.is_empty() {
        let (u, p) = userinfo.split_once(':').map_or((userinfo, None), |(u, p)| (u, Some(p)));
        out.insert("user".into(), dec(u));
        if let Some(p) = p {
            out.insert("password".into(), dec(p));
        }
    }
    let (mut hosts, mut ports) = (vec![], vec![]);
    for hp in hostlist.split(',').filter(|x| !x.is_empty()) {
        let (h, p) = hp.rsplit_once(':').map_or((hp, ""), |(h, p)| (h, p));
        hosts.push(dec(h));
        ports.push(p.to_string());
    }
    if !hosts.is_empty() {
        out.insert("host".into(), hosts.join(","));
        if ports.iter().any(|p| !p.is_empty()) {
            out.insert("port".into(), ports.join(","));
        }
    }
    if !path.is_empty() {
        out.insert("dbname".into(), dec(path));
    }
    for (k, v) in url::form_urlencoded::parse(query.as_bytes()) {
        out.insert(k.into_owned(), v.into_owned());
    }
    Ok(out)
}

fn resolve(args: &Args) -> Result<BTreeMap<String, String>, Failure> {
    if std::env::var_os("PGSERVICE").is_some() {
        return Err(Failure::Config("PGSERVICE is set: service files are not supported".into()));
    }
    let mut s: BTreeMap<String, String> = BTreeMap::new();
    for (env, key) in ENV_KEYS {
        if let Ok(v) = std::env::var(env) {
            s.insert((*key).into(), v);
        }
    }
    let conn = args.connection.clone().or_else(|| std::env::var("PGMI_CONNECTION_STRING").ok()).or_else(|| std::env::var("DATABASE_URL").ok());
    if args.connection.is_some() && args.ssl.contains_key("sslmode") {
        return Err(Failure::Config("cannot combine --connection with --sslmode; put sslmode in the connection string".into()));
    }
    if conn.is_some() && (args.host.is_some() || args.port.is_some() || args.user.is_some()) {
        return Err(Failure::Config("cannot combine a connection string with -h, -p or -U".into()));
    }
    let mut conn_dbname = None;
    if let Some(c) = conn {
        let parsed = if c.contains("://") { parse_uri(&c)? } else { parse_keyword(&c)? };
        conn_dbname = parsed.get("dbname").cloned();
        if parsed.contains_key("service") {
            return Err(Failure::Config("service= is not supported".into()));
        }
        s.extend(parsed);
    }
    for (flag, key) in [(&args.host, "host"), (&args.port, "port"), (&args.user, "user")] {
        if let Some(v) = flag {
            s.insert(key.into(), v.clone());
        }
    }
    s.extend(args.ssl.clone());
    s.entry("host".into()).or_insert("localhost".into());
    s.entry("port".into()).or_insert("5432".into());
    let target_from_env = s.remove("dbname");
    if let Some(c) = &conn_dbname {
        s.insert("dbname".into(), c.clone());
    }
    s.entry("dbname".into()).or_insert("postgres".into());
    if let Some(t) = target_from_env.filter(|_| conn_dbname.is_none()) {
        s.insert("target_dbname".into(), t);
    }
    s.entry("sslmode".into()).or_insert("prefer".into());
    s.entry("application_name".into()).or_insert("pgmi".into());
    if let Some(user) = std::env::var("USER").ok().filter(|_| !s.contains_key("user")) {
        s.insert("user".into(), user);
    }
    Ok(s)
}

fn home() -> PathBuf {
    PathBuf::from(std::env::var("HOME").unwrap_or_default())
}

fn pgpass(s: &BTreeMap<String, String>, host: &str, port: &str, db: &str) -> Option<String> {
    let path = s.get("passfile").map(PathBuf::from).unwrap_or_else(|| home().join(".pgpass"));
    let meta = std::fs::metadata(&path).ok()?;
    if meta.permissions().mode() & 0o077 != 0 {
        eprintln!("WARNING: password file \"{}\" has group or world access; permissions should be u=rw (0600) or less", path.display());
        return None;
    }
    let user = s.get("user").cloned().unwrap_or_default();
    let text = std::fs::read_to_string(path).ok()?;
    for line in text.lines().filter(|l| !l.starts_with('#')) {
        let mut fields = vec![String::new()];
        let mut chars = line.chars();
        while let Some(c) = chars.next() {
            match c {
                '\\' => fields.last_mut().unwrap().extend(chars.next()),
                ':' if fields.len() < 5 => fields.push(String::new()),
                c => fields.last_mut().unwrap().push(c),
            }
        }
        if fields.len() == 5 && [host, port, db, user.as_str()].iter().zip(&fields).all(|(want, got)| got == "*" || got == want) {
            return Some(fields[4].clone());
        }
    }
    None
}

fn read_certs(path: &Path) -> Result<Vec<CertificateDer<'static>>, Failure> {
    let file = std::fs::File::open(path).map_err(|e| Failure::Config(format!("{}: {e}", path.display())))?;
    rustls_pemfile::certs(&mut BufReader::new(file)).collect::<Result<_, _>>().map_err(|e| Failure::Config(format!("{}: {e}", path.display())))
}

#[derive(Debug)]
struct Verifier {
    inner: Option<Arc<WebPkiServerVerifier>>,
    check_name: bool,
    provider: Arc<CryptoProvider>,
}

impl ServerCertVerifier for Verifier {
    fn verify_server_cert(&self, ee: &CertificateDer<'_>, im: &[CertificateDer<'_>], name: &ServerName<'_>, ocsp: &[u8], now: UnixTime) -> Result<ServerCertVerified, rustls::Error> {
        let Some(inner) = &self.inner else { return Ok(ServerCertVerified::assertion()) };
        match inner.verify_server_cert(ee, im, name, ocsp, now) {
            Err(rustls::Error::InvalidCertificate(CertificateError::NotValidForName | CertificateError::NotValidForNameContext { .. })) if !self.check_name => Ok(ServerCertVerified::assertion()),
            other => other,
        }
    }
    fn verify_tls12_signature(&self, m: &[u8], c: &CertificateDer<'_>, d: &DigitallySignedStruct) -> Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls12_signature(m, c, d, &self.provider.signature_verification_algorithms)
    }
    fn verify_tls13_signature(&self, m: &[u8], c: &CertificateDer<'_>, d: &DigitallySignedStruct) -> Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls13_signature(m, c, d, &self.provider.signature_verification_algorithms)
    }
    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.provider.signature_verification_algorithms.supported_schemes()
    }
}

fn tls_config(s: &BTreeMap<String, String>) -> Result<(ClientConfig, String), Failure> {
    let provider = Arc::new(ring::default_provider());
    let pg = home().join(".postgresql");
    let root = s.get("sslrootcert").cloned().or_else(|| pg.join("root.crt").is_file().then(|| pg.join("root.crt").display().to_string()));
    let mut mode = s.get("sslmode").cloned().unwrap_or_default();
    if mode == "require" && root.is_some() {
        mode = "verify-ca".into();
    }
    let verify = matches!(mode.as_str(), "verify-ca" | "verify-full");
    let inner = if verify {
        let mut roots = RootCertStore::empty();
        match root.as_deref() {
            Some("system") => roots.add_parsable_certificates(rustls_native_certs::load_native_certs().certs),
            Some(path) => roots.add_parsable_certificates(read_certs(Path::new(path))?),
            None => return Err(Failure::Config(format!("sslmode {mode} needs sslrootcert"))),
        };
        Some(WebPkiServerVerifier::builder_with_provider(Arc::new(roots), provider.clone()).build().map_err(|e| Failure::Config(e.to_string()))?)
    } else {
        None
    };
    let verifier = Arc::new(Verifier { inner, check_name: mode == "verify-full", provider: provider.clone() });
    let builder = ClientConfig::builder_with_provider(provider)
        .with_safe_default_protocol_versions()
        .map_err(|e| Failure::Config(e.to_string()))?
        .dangerous()
        .with_custom_certificate_verifier(verifier);
    let cert = s.get("sslcert").map(PathBuf::from).or_else(|| pg.join("postgresql.crt").is_file().then(|| pg.join("postgresql.crt")));
    let key = s.get("sslkey").map(PathBuf::from).or_else(|| pg.join("postgresql.key").is_file().then(|| pg.join("postgresql.key")));
    let mut config = match (cert, key) {
        (Some(cert), Some(key)) => {
            let pem = std::fs::read(&key).map_err(|e| Failure::Config(format!("{}: {e}", key.display())))?;
            if String::from_utf8_lossy(&pem).contains("ENCRYPTED") {
                return Err(Failure::Config("encrypted client keys are not implemented in the spike".into()));
            }
            let key: PrivateKeyDer = rustls_pemfile::private_key(&mut &pem[..]).ok().flatten().ok_or_else(|| Failure::Config("no private key".into()))?;
            builder.with_client_auth_cert(read_certs(&cert)?, key).map_err(|e| Failure::Config(e.to_string()))?
        }
        _ => builder.with_no_client_auth(),
    };
    config.alpn_protocols = vec![b"postgresql".to_vec()];
    Ok((config, mode))
}

async fn open(host: &str, port: u16) -> std::io::Result<Stream> {
    if host.starts_with('/') {
        Ok(Box::new(tokio::net::UnixStream::connect(format!("{host}/.s.PGSQL.{port}")).await?))
    } else {
        Ok(Box::new(tokio::net::TcpStream::connect((host, port)).await?))
    }
}

struct Connected {
    client: Client,
    tap: Arc<tap::Shared>,
    host: String,
    port: u16,
}

fn spawn_driver(mut connection: tokio_postgres::Connection<tap::Tap<Stream>, impl AsyncRead + AsyncWrite + Unpin + Send + 'static>) {
    tokio::spawn(async move {
        while let Some(message) = poll_fn(|cx| connection.poll_message(cx)).await {
            match message {
                Ok(AsyncMessage::Notice(n)) => {
                    let sev = n.severity();
                    if sev == "NOTICE" || sev == "INFO" { eprintln!("{}", n.message()) } else { eprintln!("{sev}: {}", n.message()) }
                    if let Some(d) = n.detail() { eprintln!("DETAIL: {d}") }
                    if let Some(h) = n.hint() { eprintln!("HINT: {h}") }
                }
                Ok(_) => {}
                Err(_) => break,
            }
        }
    });
}

async fn connect_one(s: &BTreeMap<String, String>, host: &str, port: u16, db: &str) -> Result<Connected, String> {
    let mut config = Config::new();
    let user = s.get("user").cloned().unwrap_or_default();
    config.user(&user).dbname(db).application_name(s["application_name"].as_str());
    let password = s.get("password").cloned().or_else(|| pgpass(s, host, &port.to_string(), db));
    if let Some(p) = &password {
        config.password(p);
    }
    let mut options = s.get("options").cloned().unwrap_or_default();
    if let Ok(tz) = std::env::var("PGTZ") {
        options.push_str(&format!(" -c TimeZone={tz}"));
    }
    const KNOWN: &[&str] = &["host", "port", "user", "password", "dbname", "sslmode", "sslrootcert", "sslcert", "sslkey", "sslpassword", "sslnegotiation", "application_name", "connect_timeout", "passfile", "options", "target_session_attrs", "channel_binding", "require_auth", "target_dbname"];
    for (k, v) in s.iter().filter(|(k, _)| !KNOWN.contains(&k.as_str())) {
        options.push_str(&format!(" -c {k}={v}"));
    }
    if !options.trim().is_empty() {
        config.options(options.trim());
    }
    let (tls, mode) = tls_config(s).map_err(|f| f.message())?;
    config.ssl_mode(match mode.as_str() {
        "disable" | "allow" => SslMode::Disable,
        "prefer" => SslMode::Prefer,
        _ => SslMode::Require,
    });
    if s.get("sslnegotiation").map(String::as_str) == Some("direct") {
        config.ssl_negotiation(SslNegotiation::Direct);
    }
    config.channel_binding(match s.get("channel_binding").map(String::as_str) {
        Some("require") => ChannelBinding::Require,
        Some("disable") => ChannelBinding::Disable,
        _ => ChannelBinding::Prefer,
    });
    let timeout = s.get("connect_timeout").and_then(|t| t.parse::<u64>().ok()).filter(|t| *t > 0).map(Duration::from_secs).unwrap_or(Duration::from_secs(3600));
    let shared = tap::Shared::new(s.get("require_auth").map(String::as_str))?;
    let direct = s.get("sslnegotiation").map(String::as_str) == Some("direct");
    let attempt = async {
        let stream = tap::Tap::socket(open(host, port).await.map_err(|e| chain(&e))?, shared.clone(), direct);
        if host.starts_with('/') {
            let (client, connection) = config.connect_raw(stream, NoTls).await.map_err(|e| chain(&e))?;
            spawn_driver(connection);
            return Ok::<Client, String>(client);
        }
        let mut make = MakeRustlsConnect::new(tls);
        let inner = <MakeRustlsConnect as MakeTlsConnect<tap::Tap<Stream>>>::make_tls_connect(&mut make, host).map_err(|e| e.to_string())?;
        let connector = tap::TapConnect { inner, shared: shared.clone() };
        let (client, connection) = config.connect_raw(stream, connector).await.map_err(|e| chain(&e))?;
        spawn_driver(connection);
        Ok::<Client, String>(client)
    };
    let client = tokio::time::timeout(timeout, attempt).await.map_err(|_| "timeout expired".to_string())??;
    Ok(Connected { client, tap: shared, host: host.into(), port })
}

async fn matches_attrs(c: &Client, attrs: &str) -> Result<bool, String> {
    if attrs == "any" {
        return Ok(true);
    }
    let row = c.query_one("SELECT current_setting('transaction_read_only') = 'on', pg_is_in_recovery()", &[]).await.map_err(|e| e.to_string())?;
    let (read_only, recovery): (bool, bool) = (row.get(0), row.get(1));
    Ok(match attrs {
        "read-write" => !read_only,
        "read-only" => read_only,
        "primary" => !recovery,
        "standby" | "prefer-standby" => recovery,
        _ => false,
    })
}

async fn connect(s: &BTreeMap<String, String>, db: &str) -> Result<Connected, Failure> {
    let hosts: Vec<&str> = s["host"].split(',').collect();
    let ports: Vec<&str> = s["port"].split(',').collect();
    let attrs = s.get("target_session_attrs").map(String::as_str).unwrap_or("any");
    let mut errors = vec![];
    let passes = if attrs == "prefer-standby" { vec!["standby", "any"] } else { vec![attrs] };
    for want in passes {
        for (i, host) in hosts.iter().enumerate() {
            let port_text = ports.get(i).or(ports.first()).copied().unwrap_or("5432");
            let port: u16 = port_text.parse().map_err(|_| Failure::Config(format!("invalid port {port_text}")))?;
            match connect_one(s, host, port, db).await {
                Ok(c) => match matches_attrs(&c.client, want).await {
                    Ok(true) => return Ok(c),
                    Ok(false) => errors.push(format!("{host}:{port}: server does not satisfy target_session_attrs={want}")),
                    Err(e) => errors.push(format!("{host}:{port}: {e}")),
                },
                Err(e) => errors.push(format!("{host}:{port}: {e}")),
            }
        }
    }
    let mut message = format!("connect to \"{db}\": {}", errors.join("; "));
    if message.contains("TLS") || message.contains("x509") {
        message.push_str("\ncheck --sslmode, --sslcert, --sslkey, --sslrootcert");
    }
    Err(Failure::Connect(message))
}

async fn deploy(args: &Args, out: &mut serde_json::Map<String, serde_json::Value>) -> Result<(), Failure> {
    let s = resolve(args)?;
    let target = args.database.clone().or_else(|| s.get("target_dbname").cloned()).unwrap_or_else(|| s["dbname"].clone());
    let deploy_sql = std::fs::read_to_string(args.project.join("deploy.sql")).map_err(|_| Failure::Config("deploy.sql not found".into()))?;
    let started = tokio::time::Instant::now();
    let connecting = |f| async move {
        match tokio::time::timeout_at(started + args.timeout, f).await {
            Ok(r) => r,
            Err(_) => Err(Failure::Connect("--timeout ran out while connecting".into())),
        }
    };
    let maintenance = connecting(connect(&s, &s["dbname"])).await?;
    let exists = maintenance.client.query_opt("SELECT 1 FROM pg_database WHERE datname = $1", &[&target]).await.map_err(|e| Failure::Sql(e.to_string()))?;
    if exists.is_none() {
        eprintln!("Database \"{target}\" does not exist; creating");
        maintenance.client.batch_execute(&format!("CREATE DATABASE \"{}\"", target.replace('"', "\"\""))).await.map_err(|e| Failure::Sql(e.to_string()))?;
    }
    let target_settings = {
        let mut t = s.clone();
        t.insert("host".into(), maintenance.host.clone());
        t.insert("port".into(), maintenance.port.to_string());
        t
    };
    let conn = connecting(connect(&target_settings, &target)).await?;
    let row = conn.client.query_opt("SELECT ssl, version FROM pg_stat_ssl WHERE pid = pg_backend_pid()", &[]).await.map_err(|e| Failure::Sql(e.to_string()))?;
    let (enabled, version): (bool, Option<String>) = row.map(|r| (r.get(0), r.get(1))).unwrap_or((false, None));
    let version = version.map(|v| v.replace("TLSv", "TLS "));
    out.insert("tls".into(), serde_json::json!({"enabled": enabled, "version": version, "sslmode": s["sslmode"]}));
    conn.client.batch_execute("CREATE TEMP TABLE _pgmi_spike_session ()").await.map_err(|e| Failure::Sql(format!("ERROR: {}", db_message(&e))))?;
    let locked: bool = conn.client.query_one("SELECT pg_try_advisory_lock(hashtextextended('pgmi.deploy.' || current_database(), 0))", &[]).await.map_err(|e| Failure::Sql(e.to_string()))?.get(0);
    if !locked {
        return Err(Failure::Concurrent(format!("another pgmi deployment is already running against \"{target}\"")));
    }
    let cancel = conn.client.cancel_token();
    let units = split::units(&deploy_sql);
    let work = async {
        let mut committed = 0;
        let mut failure = None;
        for (i, unit) in units.iter().enumerate() {
            if let Err(e) = conn.client.batch_execute(unit).await {
                failure = Some(Failure::Sql(format!("ERROR: {}", db_message(&e))));
                break;
            }
            if conn.tap.status() == b'I' || (i == 0 && split::head_commits(unit)) {
                committed = i + 1;
            }
        }
        out.insert("transactionStatus".into(), serde_json::json!((conn.tap.status() as char).to_string()));
        out.insert("unitsCommitted".into(), serde_json::json!(committed));
        out.insert("executionMode".into(), serde_json::json!(if committed == 0 { "atomic" } else { "psql" }));
        let result = match failure {
            Some(f) => Err(f),
            None if committed < units.len() => Err(Failure::Sql("deploy.sql ended inside an open transaction, which was rolled back; end it with COMMIT".into())),
            None => Ok(()),
        };
        if conn.tap.status() != b'I' {
            let _ = conn.client.batch_execute("ROLLBACK").await;
        }
        let _ = conn.client.batch_execute("SELECT pg_advisory_unlock_all()").await;
        result
    };
    let outcome = tokio::select! {
        r = work => r,
        _ = tokio::time::sleep_until(started + args.timeout) => Err(Failure::Timeout),
        _ = tokio::signal::ctrl_c() => Err(Failure::Interrupted),
    };
    if matches!(outcome, Err(Failure::Timeout | Failure::Interrupted)) {
        let (tls, _) = tls_config(&target_settings)?;
        let mut make = MakeRustlsConnect::new(tls);
        if let Ok(stream) = open(&conn.host, conn.port).await {
            if conn.host.starts_with('/') {
                let _ = cancel.cancel_query_raw(stream, NoTls).await;
            } else if let Ok(c) = <MakeRustlsConnect as MakeTlsConnect<Stream>>::make_tls_connect(&mut make, &conn.host) {
                let _ = tokio::time::timeout(Duration::from_secs(2), cancel.cancel_query_raw(stream, c)).await;
            }
        }
    }
    outcome
}

fn chain(e: &(dyn std::error::Error + 'static)) -> String {
    let mut parts = vec![e.to_string()];
    let mut cur = e.source();
    while let Some(c) = cur {
        parts.push(c.to_string());
        cur = c.source();
    }
    if let Some(db) = e.downcast_ref::<tokio_postgres::Error>().and_then(|e| e.as_db_error()) {
        return format!("{} (SQLSTATE {})", db.message(), db.code().code());
    }
    let text = parts.join(": ");
    text.replace("invalid peer certificate: UnknownIssuer", "x509: certificate signed by unknown authority")
        .replace("invalid peer certificate: NotValidForName", "x509: certificate is not valid for this host name")
}

fn db_message(e: &tokio_postgres::Error) -> String {
    match e.as_db_error() {
        Some(db) => format!("{} (SQLSTATE {})", db.message(), db.code().code()),
        None => e.to_string(),
    }
}

#[tokio::main]
async fn main() {
    let args = match parse_args() {
        Ok(a) => a,
        Err(f) => {
            eprintln!("pgmi: error: {}", f.message());
            std::process::exit(f.code());
        }
    };
    let mut out = serde_json::Map::new();
    let result = tokio::time::timeout(args.timeout + Duration::from_secs(3), deploy(&args, &mut out)).await.unwrap_or(Err(Failure::Timeout));
    let code = result.as_ref().err().map_or(0, Failure::code);
    out.insert("status".into(), serde_json::json!(if code == 0 { "success" } else { "failed" }));
    out.insert("exitCode".into(), serde_json::json!(code));
    if let Err(f) = &result {
        out.insert("error".into(), serde_json::json!(f.message()));
        eprintln!("FAILED");
        eprintln!("pgmi: error: {}", f.message());
    } else {
        eprintln!("ok");
    }
    if args.json {
        println!("{}", serde_json::to_string_pretty(&out).unwrap());
    }
    std::process::exit(code);
}
