use std::sync::{Arc, Mutex};
use std::time::Duration;

use futures_util::future::poll_fn;
use tokio::io::{AsyncRead, AsyncWrite};
use tokio_postgres::config::{ChannelBinding, SslMode, SslNegotiation};
use tokio_postgres::error::DbError;
use tokio_postgres::{AsyncMessage, CancelToken, Client, Config, NoTls};

use super::pgpass;
use super::resolve::Spec;
use super::tap::{self, Start, Tap, TapConnect};
use super::tls::{self, Connect};
use crate::error::{Error, Result};

pub(crate) trait Io: AsyncRead + AsyncWrite + Unpin + Send {}
impl<T: AsyncRead + AsyncWrite + Unpin + Send> Io for T {}
type Stream = Box<dyn Io>;

#[derive(Debug, Clone)]
pub(crate) struct Notice {
    pub(crate) severity: String,
    pub(crate) message: String,
    pub(crate) detail: Option<String>,
    pub(crate) hint: Option<String>,
}

pub(crate) type NoticeSink = Arc<dyn Fn(Notice) + Send + Sync>;
pub(crate) type LogSink = Arc<dyn Fn(String) + Send + Sync>;

pub(crate) struct Session {
    pub(crate) client: Client,
    pub(crate) tap: Arc<tap::Shared>,
    pub(crate) tls_version: Option<String>,
    cancel: CancelToken,
    tls: Option<Connect>,
    peer: Peer,
    connection: tokio::task::AbortHandle,
}

#[derive(Clone)]
enum Peer {
    Tcp(std::net::SocketAddr),
    Socket(String),
    Google(crate::cloud::Token),
}

pub(crate) fn notice_of(db: &DbError) -> Notice {
    let severity = db
        .parsed_severity()
        .map(|s| s.to_string())
        .unwrap_or_else(|| db.severity().to_string());
    Notice {
        severity,
        message: db.message().to_string(),
        detail: db.detail().map(str::to_string),
        hint: db.hint().map(str::to_string),
    }
}

async fn open(host: &str, port: u16) -> std::io::Result<(Stream, Peer)> {
    if host.starts_with('/') {
        let path = format!("{host}/.s.PGSQL.{port}");
        return Ok((dial(&Peer::Socket(path.clone())).await?, Peer::Socket(path)));
    }
    let stream = tokio::net::TcpStream::connect((host, port)).await?;
    let _ = stream.set_nodelay(true);
    let peer = Peer::Tcp(stream.peer_addr()?);
    Ok((Box::new(stream), peer))
}

async fn dial(peer: &Peer) -> std::io::Result<Stream> {
    match peer {
        Peer::Tcp(addr) => {
            let stream = tokio::net::TcpStream::connect(addr).await?;
            let _ = stream.set_nodelay(true);
            Ok(Box::new(stream))
        }
        #[cfg(unix)]
        Peer::Socket(path) => Ok(Box::new(tokio::net::UnixStream::connect(path).await?)),
        #[cfg(not(unix))]
        Peer::Socket(_) => Err(std::io::Error::other("Unix sockets are not supported here")),
        Peer::Google(token) => match token.google() {
            Some(g) => Ok(Box::new(g.dial().await?)),
            None => Err(std::io::Error::other("no Cloud SQL dialer")),
        },
    }
}

enum Failure {
    Transient(String),
    Fatal(String),
    Server(String, String),
    Config(String),
    Refused(String),
}

fn classify_io(e: &std::io::Error) -> bool {
    use std::io::ErrorKind::*;
    matches!(
        e.kind(),
        ConnectionRefused
            | ConnectionReset
            | ConnectionAborted
            | TimedOut
            | HostUnreachable
            | NetworkUnreachable
    )
}

fn chain(e: &(dyn std::error::Error + 'static)) -> String {
    let mut parts = vec![e.to_string()];
    let mut cur = e.source();
    while let Some(c) = cur {
        let t = c.to_string();
        if !parts.iter().any(|p| p.contains(&t)) {
            parts.push(t);
        }
        cur = c.source();
    }
    parts.join(": ")
}

fn classify_pg(e: &tokio_postgres::Error, host: &str, passfile: &str) -> Failure {
    if let Some(db) = e.as_db_error() {
        let code = db.code().code();
        let text = format!("{} (SQLSTATE {code})", db.message());
        if code.starts_with("08") || code == "53300" || code == "57P03" {
            return Failure::Transient(text);
        }
        if code == "28000" {
            return Failure::Refused(text);
        }
        return Failure::Server(text, code.to_string());
    }
    let mut cur: Option<&(dyn std::error::Error + 'static)> = Some(e);
    while let Some(c) = cur {
        if let Some(io) = c.downcast_ref::<std::io::Error>() {
            if io
                .get_ref()
                .is_some_and(|i| i.to_string().contains("authentication method requirement"))
            {
                return Failure::Fatal(io.to_string());
            }
            if classify_io(io) {
                return Failure::Transient(io.to_string());
            }
        }
        cur = c.source();
    }
    let text = chain(e);
    if text.contains("password missing") {
        return Failure::Fatal(format!(
            "no password supplied: the server asked for one and none was found in the connection string, PGPASSWORD or {passfile}"
        ));
    }
    if text.contains("authentication method requirement") {
        return Failure::Fatal(text);
    }
    Failure::Fatal(tls::describe_failure(&text, host))
}

fn refused_token(f: Failure, e: &tokio_postgres::Error, spec: &Spec) -> Failure {
    let (Some(token), Some(db)) = (&spec.token, e.as_db_error()) else {
        return f;
    };
    if !db.code().code().starts_with("28") {
        return f;
    }
    Failure::Fatal(format!(
        "{} token was refused for user \"{}\": {} (SQLSTATE {}); if the database role has another name, pass it with --username",
        token.provider().label(),
        spec.user,
        db.message(),
        db.code().code()
    ))
}

fn sslmode_of(mode: &str) -> SslMode {
    match mode {
        "disable" | "allow" => SslMode::Disable,
        "prefer" => SslMode::Prefer,
        _ => SslMode::Require,
    }
}

struct Attempt<'a> {
    spec: &'a Spec,
    db: &'a str,
    name: &'a str,
    host: &'a str,
    port: u16,
    tls_mode: &'a str,
    notices: NoticeSink,
    log: LogSink,
}

async fn attempt(a: Attempt<'_>) -> std::result::Result<Session, Failure> {
    let spec = a.spec;
    let google = spec.token.as_ref().and_then(|t| t.google()).cloned();
    let unix = a.host.starts_with('/') || google.is_some();
    let mut config = Config::new();
    config
        .user(&spec.user)
        .dbname(a.db)
        .application_name(&spec.application_name);
    let passfile = spec
        .passfile
        .clone()
        .or_else(|| spec.pg_files.as_ref().map(|f| f.pgpass.clone()));
    let token = spec
        .token
        .as_ref()
        .map(|t| t.password(a.name, a.port, &spec.user))
        .transpose()
        .map_err(Failure::Fatal)?;
    let password = match token.or_else(|| spec.password.clone()) {
        Some(p) => Some(p),
        None => match &passfile {
            Some(path) => {
                let found = pgpass::lookup(path, a.name, a.port, a.db, &spec.user);
                if let Some(w) = found.warning {
                    (a.log)(w);
                }
                found.password
            }
            None => None,
        },
    };
    if let Some(p) = &password {
        config.password(p);
    }
    let mut options = spec.options.clone().unwrap_or_default();
    if let Some(tz) = &spec.timezone {
        options.push_str(&format!(" -c TimeZone={tz}"));
    }
    if !options.trim().is_empty() {
        config.options(options.trim());
    }
    let mode = if unix { "disable" } else { a.tls_mode };
    config.ssl_mode(sslmode_of(mode));
    if spec.sslnegotiation_direct && !unix {
        config.ssl_negotiation(SslNegotiation::Direct);
    }
    config.channel_binding(match spec.channel_binding.as_str() {
        "require" => ChannelBinding::Require,
        "disable" => ChannelBinding::Disable,
        _ => ChannelBinding::Prefer,
    });
    let shared = tap::Shared::new(spec.require_auth.as_deref()).map_err(Failure::Config)?;
    let start = match sslmode_of(mode) {
        SslMode::Disable => Start::Plain,
        _ if spec.sslnegotiation_direct => Start::Direct,
        _ => Start::Negotiated,
    };
    let passfile_text = passfile
        .as_ref()
        .map(|p| p.display().to_string())
        .unwrap_or_else(|| "~/.pgpass".into());
    let tls_config = if matches!(sslmode_of(mode), SslMode::Disable) {
        None
    } else {
        Some(tls::config(spec).map_err(|e| Failure::Config(e.text()))?)
    };
    let version = Arc::new(Mutex::new(None));
    let run = async {
        let opened = match &spec.token {
            Some(token) if google.is_some() => {
                let peer = Peer::Google(token.clone());
                dial(&peer).await.map(|s| (s, peer))
            }
            _ => open(a.host, a.port).await,
        };
        let (socket, peer) = opened.map_err(|e| {
            if classify_io(&e) {
                Failure::Transient(e.to_string())
            } else {
                Failure::Fatal(e.to_string())
            }
        })?;
        let stream = Tap::socket(socket, shared.clone(), start);
        let notices = a.notices.clone();
        match &tls_config {
            None => {
                let (client, connection) =
                    config.connect_raw(stream, NoTls).await.map_err(|e| {
                        refused_token(classify_pg(&e, a.name, &passfile_text), &e, spec)
                    })?;
                let task = drive(connection, notices);
                Ok((client, None, peer, task))
            }
            Some(t) => {
                let connect = Connect {
                    config: t.config.clone(),
                    host: a.name.to_string(),
                    version: version.clone(),
                };
                let tapped = TapConnect {
                    inner: connect.clone(),
                    shared: shared.clone(),
                };
                let (client, connection) =
                    config.connect_raw(stream, tapped).await.map_err(|e| {
                        refused_token(classify_pg(&e, a.name, &passfile_text), &e, spec)
                    })?;
                let task = drive(connection, notices);
                Ok((client, Some(connect), peer, task))
            }
        }
    };
    let (client, tls, peer, connection) = match spec.connect_timeout {
        Some(t) => tokio::time::timeout(t, run)
            .await
            .map_err(|_| Failure::Transient("timeout expired".into()))??,
        None => run.await?,
    };
    let tls_version = version.lock().ok().and_then(|v| v.clone());
    Ok(Session {
        cancel: client.cancel_token(),
        client,
        tap: shared,
        tls: tls.filter(|_| tls_version.is_some()),
        tls_version,
        peer,
        connection,
    })
}

fn drive<S, T>(
    mut connection: tokio_postgres::Connection<S, T>,
    notices: NoticeSink,
) -> tokio::task::AbortHandle
where
    S: AsyncRead + AsyncWrite + Unpin + Send + 'static,
    T: AsyncRead + AsyncWrite + Unpin + Send + 'static,
{
    tokio::spawn(async move {
        while let Some(message) = poll_fn(|cx| connection.poll_message(cx)).await {
            match message {
                Ok(AsyncMessage::Notice(n)) => notices(notice_of(&n)),
                Ok(_) => {}
                Err(_) => break,
            }
        }
    })
    .abort_handle()
}

async fn addresses(host: &str, port: u16) -> std::result::Result<Vec<String>, String> {
    if host.starts_with('/') || host.parse::<std::net::IpAddr>().is_ok() {
        return Ok(vec![host.to_string()]);
    }
    let found = tokio::net::lookup_host((host, port))
        .await
        .map_err(|e| format!("could not resolve host \"{host}\": {e}"))?;
    let mut out: Vec<String> = Vec::new();
    for a in found {
        let ip = a.ip().to_string();
        if !out.contains(&ip) {
            out.push(ip);
        }
    }
    Ok(out)
}

async fn matches_attrs(c: &Client, attrs: &str) -> std::result::Result<bool, String> {
    if attrs == "any" {
        return Ok(true);
    }
    let rows = c
        .simple_query("SELECT current_setting('transaction_read_only'), pg_is_in_recovery()::text")
        .await
        .map_err(|e| e.to_string())?;
    let row = rows.iter().find_map(|m| match m {
        tokio_postgres::SimpleQueryMessage::Row(r) => Some(r),
        _ => None,
    });
    let Some(row) = row else { return Ok(false) };
    let read_only = row.get(0) == Some("on");
    let recovery = row.get(1) == Some("true");
    Ok(match attrs {
        "read-write" => !read_only,
        "read-only" => read_only,
        "primary" => !recovery,
        "standby" | "prefer-standby" => recovery,
        _ => false,
    })
}

pub(crate) struct Dialer {
    pub(crate) notices: NoticeSink,
    pub(crate) log: LogSink,
}

impl Dialer {
    pub(crate) async fn connect(&self, spec: &Spec, db: &str) -> Result<Session> {
        if spec.sslmode != "disable" {
            super::tls::require_cpu()?;
        }
        let delays = [100u64, 200, 400];
        let mut attempt_no = 0;
        loop {
            attempt_no += 1;
            match self.connect_once(spec, db).await {
                Ok(s) => return Ok(s),
                Err((e, transient)) => {
                    if !transient || attempt_no > delays.len() {
                        return Err(e);
                    }
                    let wait = delays[attempt_no - 1];
                    (self.log)(format!(
                        "Connection attempt {attempt_no} failed: {}; retrying in {wait}ms",
                        e.text()
                    ));
                    tokio::time::sleep(Duration::from_millis(wait)).await;
                }
            }
        }
    }

    async fn connect_once(
        &self,
        spec: &Spec,
        db: &str,
    ) -> std::result::Result<Session, (Error, bool)> {
        let attrs = spec.target_session_attrs.as_str();
        let passes: Vec<&str> = if attrs == "prefer-standby" {
            vec!["standby", "any"]
        } else {
            vec![attrs]
        };
        let mut errors: Vec<String> = Vec::new();
        let mut codes: Vec<String> = Vec::new();
        let mut all_transient = true;
        let hint_tls =
            |s: &str| s.contains("x509") || s.contains("TLS") || s.contains("certificate");
        for want in passes {
            for (host, port) in &spec.hosts {
                let resolved = if spec.token.as_ref().and_then(|t| t.google()).is_some() {
                    Ok(vec![host.clone()])
                } else {
                    addresses(host, *port).await
                };
                let addrs = match resolved {
                    Ok(a) => a,
                    Err(e) => {
                        all_transient = false;
                        errors.push(e);
                        continue;
                    }
                };
                for addr in addrs {
                    let label = if addr == *host {
                        format!("{host}:{port}")
                    } else {
                        format!("{host} ({addr}):{port}")
                    };
                    let mut modes = vec![spec.sslmode.as_str()];
                    if spec.sslmode == "allow" {
                        modes.push("require");
                    }
                    let mut outcome = None;
                    for mode in modes {
                        let r = attempt(Attempt {
                            spec,
                            db,
                            name: host,
                            host: &addr,
                            port: *port,
                            tls_mode: mode,
                            notices: self.notices.clone(),
                            log: self.log.clone(),
                        })
                        .await;
                        match r {
                            Err(Failure::Refused(t)) if mode == "allow" => {
                                outcome = Some(Err(Failure::Refused(t)));
                            }
                            other => {
                                outcome = Some(other);
                                break;
                            }
                        }
                    }
                    match outcome {
                        Some(Ok(s)) => match matches_attrs(&s.client, want).await {
                            Ok(true) => return Ok(s),
                            Ok(false) => {
                                all_transient = false;
                                errors.push(format!(
                                    "{label}: server does not satisfy target_session_attrs={want}"
                                ));
                            }
                            Err(e) => {
                                all_transient = false;
                                errors.push(format!("{label}: {e}"));
                            }
                        },
                        Some(Err(Failure::Config(t))) => {
                            return Err((Error::config(t), false));
                        }
                        Some(Err(Failure::Transient(t))) => {
                            let t = if t.contains("refused") || t.contains("Connection refused") {
                                format!("connection refused to {host}:{port}")
                            } else {
                                t
                            };
                            errors.push(if t.contains(&format!("{host}:{port}")) {
                                t
                            } else {
                                format!("{label}: {t}")
                            });
                        }
                        Some(Err(Failure::Server(t, code))) => {
                            all_transient = false;
                            codes.push(code);
                            errors.push(format!("{label}: {t}"));
                        }
                        Some(Err(Failure::Fatal(t) | Failure::Refused(t))) => {
                            all_transient = false;
                            errors.push(format!("{label}: {t}"));
                        }
                        None => {}
                    }
                }
            }
        }
        let joined = errors.join("; ");
        let mut message = format!("connect to \"{db}\": {joined}");
        if joined.contains("connection refused") {
            let (h, p) = spec.first();
            message.push_str(&format!(
                "\nis PostgreSQL running? check: pg_isready -h {h} -p {p}\nor point pgmi elsewhere: --connection, PGMI_CONNECTION_STRING, pgmi.yaml"
            ));
        }
        if hint_tls(&joined) {
            message.push_str("\ncheck --sslmode, --sslcert, --sslkey, --sslrootcert");
        }
        let mut error = Error::connection(message);
        if let Some(code) = codes.first().filter(|c| codes.iter().all(|o| o == *c)) {
            error = error.with_sql(crate::error::SqlDiagnostics {
                sqlstate: Some(code.clone()),
                ..Default::default()
            });
        }
        Err((error, all_transient && !errors.is_empty()))
    }
}

impl Session {
    pub(crate) async fn cancel(&self) {
        let fut = async {
            let Ok(socket) = dial(&self.peer).await else {
                return;
            };
            match &self.tls {
                Some(t) => {
                    let _ = self.cancel.cancel_query_raw(socket, t.clone()).await;
                }
                None => {
                    let _ = self.cancel.cancel_query_raw(socket, NoTls).await;
                }
            }
        };
        let _ = tokio::time::timeout(Duration::from_secs(2), fut).await;
    }

    pub(crate) fn disconnect(&self) {
        self.connection.abort();
    }

    pub(crate) fn tls_enabled(&self) -> bool {
        self.tls_version.is_some()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn the_cancel_goes_to_the_address_that_answered() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let (_, peer) = open("localhost", addr.port()).await.unwrap();
        let Peer::Tcp(seen) = &peer else {
            panic!("not TCP")
        };
        assert_eq!(*seen, addr);
        let _ = listener.accept().await.unwrap();
        let _cancel = dial(&peer).await.unwrap();
        let (_, from) = listener.accept().await.unwrap();
        assert_eq!(from.ip(), addr.ip());
    }
}
