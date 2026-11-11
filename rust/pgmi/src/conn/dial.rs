use std::fmt::Write as _;
use std::future::poll_fn;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context as TaskContext, Poll};
use std::time::Duration;

use rand::seq::SliceRandom;
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
use tokio_postgres::config::{
    ChannelBinding as PgChannelBinding, SslMode as PgSslMode, SslNegotiation as PgSslNegotiation,
};
use tokio_postgres::error::DbError;
use tokio_postgres::{AsyncMessage, CancelToken, Client, Config, NoTls};

use super::pgpass;
use super::resolve::{
    ChannelBinding, LoadBalanceHosts, Spec, SslMode, SslNegotiation, TargetSessionAttrs,
};
use super::retry::{self, Cause};
use super::socket::Tcp;
use super::tap::{self, Start, Tap, TapConnect};
use super::tls::{self, Connect};
use crate::error::{Error, Result};
use crate::output::{Notice, Severity};
use rustls::ClientConfig;

enum Stream {
    Tcp(tokio::net::TcpStream),
    #[cfg(unix)]
    Unix(tokio::net::UnixStream),
    CloudSql(Box<tokio_rustls::client::TlsStream<tokio::net::TcpStream>>),
}

macro_rules! each_stream {
    ($self:ident, $s:ident => $e:expr) => {
        match $self.get_mut() {
            Stream::Tcp($s) => $e,
            #[cfg(unix)]
            Stream::Unix($s) => $e,
            Stream::CloudSql($s) => $e,
        }
    };
}

impl AsyncRead for Stream {
    fn poll_read(
        self: Pin<&mut Self>,
        cx: &mut TaskContext<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<std::io::Result<()>> {
        each_stream!(self, s => Pin::new(s).poll_read(cx, buf))
    }
}

impl AsyncWrite for Stream {
    fn poll_write(
        self: Pin<&mut Self>,
        cx: &mut TaskContext<'_>,
        buf: &[u8],
    ) -> Poll<std::io::Result<usize>> {
        each_stream!(self, s => Pin::new(s).poll_write(cx, buf))
    }

    fn poll_flush(self: Pin<&mut Self>, cx: &mut TaskContext<'_>) -> Poll<std::io::Result<()>> {
        each_stream!(self, s => Pin::new(s).poll_flush(cx))
    }

    fn poll_shutdown(self: Pin<&mut Self>, cx: &mut TaskContext<'_>) -> Poll<std::io::Result<()>> {
        each_stream!(self, s => Pin::new(s).poll_shutdown(cx))
    }
}

pub(crate) type NoticeSink = Arc<dyn Fn(Notice) + Send + Sync>;
pub(crate) type LogSink = Arc<dyn Fn(String) + Send + Sync>;
pub(crate) type SecretSink = Arc<dyn Fn(&str) + Send + Sync>;

pub(crate) struct Session {
    pub(crate) client: Client,
    pub(crate) tap: Arc<tap::Shared>,
    pub(crate) tls_version: Option<String>,
    cancel: CancelToken,
    tls: Option<Connect>,
    peer: Peer,
    tcp: Tcp,
    connection: tokio::task::AbortHandle,
}

const CLOSE_GRACE: Duration = Duration::from_secs(1);

impl Drop for Session {
    fn drop(&mut self) {
        let driver = self.connection.clone();
        match tokio::runtime::Handle::try_current() {
            Ok(runtime) => {
                runtime.spawn(async move {
                    tokio::time::sleep(CLOSE_GRACE).await;
                    driver.abort();
                });
            }
            Err(_) => driver.abort(),
        }
    }
}

#[derive(Clone)]
enum Peer {
    Tcp(std::net::SocketAddr),
    Socket(String),
    Google(crate::conn::cloud::Token),
}

pub(crate) fn notice_of(db: &DbError) -> Notice {
    let severity = match db.parsed_severity() {
        Some(s) => Severity::from(s.to_string().as_str()),
        None => Severity::from(db.severity()),
    };
    Notice {
        severity,
        message: db.message().to_string(),
        detail: db.detail().map(str::to_string),
        hint: db.hint().map(str::to_string),
    }
}

async fn open(host: &str, port: u16, tcp: &Tcp) -> std::io::Result<(Stream, Peer)> {
    if host.starts_with('/') {
        let path = format!("{host}/.s.PGSQL.{port}");
        let peer = Peer::Socket(path);
        return Ok((dial(&peer, tcp).await?, peer));
    }
    let stream = tokio::net::TcpStream::connect((host, port)).await?;
    tcp.apply(&stream)?;
    let peer = Peer::Tcp(stream.peer_addr()?);
    Ok((Stream::Tcp(stream), peer))
}

async fn dial(peer: &Peer, tcp: &Tcp) -> std::io::Result<Stream> {
    match peer {
        Peer::Tcp(addr) => {
            let stream = tokio::net::TcpStream::connect(addr).await?;
            tcp.apply(&stream)?;
            Ok(Stream::Tcp(stream))
        }
        #[cfg(unix)]
        Peer::Socket(path) => Ok(Stream::Unix(tokio::net::UnixStream::connect(path).await?)),
        #[cfg(not(unix))]
        Peer::Socket(path) => Err(std::io::Error::other(format!(
            "Unix sockets are not supported here: {path}"
        ))),
        Peer::Google(token) => match token.google() {
            Some(g) => Ok(Stream::CloudSql(Box::new(g.dial(tcp).await?))),
            None => Err(std::io::Error::other("no Cloud SQL dialer")),
        },
    }
}

enum DialFailure {
    Transient(String),
    ConnectionRefused,
    Fatal(String),
    Server(String, String),
    Config(String),
    Refused(String),
}

fn classify_io(e: &std::io::Error) -> bool {
    retry::transient(Cause::Io(e.kind()))
}

fn refuses_auth(e: &tap::Error) -> bool {
    matches!(e, tap::Error::AuthRequirement { .. })
}

fn classify_pg(e: &tokio_postgres::Error, host: &str, passfile: &str) -> DialFailure {
    if let Some(db) = e.as_db_error() {
        let code = db.code().code();
        let text = format!("{} (SQLSTATE {code})", db.message());
        if retry::transient(Cause::Sqlstate(code)) {
            return DialFailure::Transient(text);
        }
        if code == "28000" {
            return DialFailure::Refused(text);
        }
        return DialFailure::Server(text, code.to_string());
    }
    let mut cur: Option<&(dyn std::error::Error + 'static)> = Some(e);
    while let Some(c) = cur {
        if let Some(io) = c.downcast_ref::<std::io::Error>() {
            if io
                .get_ref()
                .and_then(|i| i.downcast_ref::<tap::Error>())
                .is_some_and(refuses_auth)
            {
                return DialFailure::Fatal(io.to_string());
            }
            if io.kind() == std::io::ErrorKind::ConnectionRefused {
                return DialFailure::ConnectionRefused;
            }
            if classify_io(io) {
                return DialFailure::Transient(io.to_string());
            }
        }
        if c.downcast_ref::<tap::Error>().is_some_and(refuses_auth) {
            return DialFailure::Fatal(crate::error::chain(e));
        }
        cur = c.source();
    }
    let text = crate::error::chain(e);
    if text.contains("password missing") {
        return DialFailure::Fatal(format!(
            "no password supplied: the server asked for one and none was found in the connection string, PGPASSWORD or {passfile}"
        ));
    }
    DialFailure::Fatal(tls::describe_failure(&text, host))
}

fn refused_token(
    f: DialFailure,
    e: &tokio_postgres::Error,
    spec: &Spec,
    db_name: Option<&str>,
) -> DialFailure {
    let (Some(token), Some(db)) = (&spec.token, e.as_db_error()) else {
        return f;
    };
    if !db.code().code().starts_with("28") {
        return f;
    }
    DialFailure::Fatal(refused_token_text(
        token.provider().label(),
        &spec.user,
        db_name.unwrap_or(&spec.target_db),
        db.message(),
        db.code().code(),
    ))
}

fn refused_token_text(provider: &str, user: &str, db: &str, message: &str, code: &str) -> String {
    format!(
        "the {provider} token was refused for user \"{user}\" on database \"{db}\": {message} (SQLSTATE {code}); check that the signed-in identity has a database role and that --username matches it"
    )
}

fn sslmode_of(mode: SslMode) -> PgSslMode {
    match mode {
        SslMode::Disable | SslMode::Allow => PgSslMode::Disable,
        SslMode::Prefer => PgSslMode::Prefer,
        SslMode::Require | SslMode::VerifyCa | SslMode::VerifyFull => PgSslMode::Require,
    }
}

struct Attempt<'a> {
    spec: &'a Spec,
    db: &'a str,
    name: &'a str,
    host: &'a str,
    port: u16,
    tls_mode: SslMode,
    tls: Option<Arc<ClientConfig>>,
    want: TargetSessionAttrs,
    notices: NoticeSink,
    log: LogSink,
    secret: SecretSink,
}

async fn attempt(a: Attempt<'_>) -> std::result::Result<Session, DialFailure> {
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
    let token = match &spec.token {
        Some(t) => Some(
            t.password(a.name, a.port, &spec.user)
                .await
                .map_err(DialFailure::Fatal)?,
        ),
        None => None,
    };
    let password = match token.or_else(|| spec.password.as_ref().map(|p| p.expose().to_string())) {
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
        (a.secret)(p);
        config.password(p);
    }
    let mut options = spec.options.clone().unwrap_or_default();
    if let Some(tz) = &spec.timezone {
        let _ = write!(options, " -c TimeZone={tz}");
    }
    if let Some(t) = spec.tcp.user_timeout {
        let _ = write!(options, " -c tcp_user_timeout={}", t.as_millis());
    }
    if !options.trim().is_empty() {
        config.options(options.trim());
    }
    let mode = if unix { SslMode::Disable } else { a.tls_mode };
    config.ssl_mode(sslmode_of(mode));
    if spec.sslnegotiation == SslNegotiation::Direct && !unix {
        config.ssl_negotiation(PgSslNegotiation::Direct);
    }
    config.channel_binding(match spec.channel_binding {
        ChannelBinding::Require => PgChannelBinding::Require,
        ChannelBinding::Disable => PgChannelBinding::Disable,
        ChannelBinding::Prefer => PgChannelBinding::Prefer,
    });
    let shared = tap::Shared::with_policy(
        spec.require_auth
            .policy()
            .map_err(|e| DialFailure::Config(e.to_string()))?
            .cloned(),
    );
    let start = match sslmode_of(mode) {
        PgSslMode::Disable => Start::Plain,
        _ if spec.sslnegotiation == SslNegotiation::Direct => Start::Direct,
        _ => Start::Negotiated,
    };
    let passfile_text = passfile
        .as_ref()
        .map_or_else(|| "~/.pgpass".into(), |p| p.display().to_string());
    let tls_config = if matches!(sslmode_of(mode), PgSslMode::Disable) {
        None
    } else {
        Some(a.tls.clone().ok_or_else(|| {
            DialFailure::Config("TLS setup: no TLS configuration for this host".into())
        })?)
    };
    let version = Arc::new(Mutex::new(None));
    let run = async {
        let opened = match &spec.token {
            Some(token) if google.is_some() => {
                let peer = Peer::Google(token.clone());
                dial(&peer, &spec.tcp).await.map(|s| (s, peer))
            }
            _ => open(a.host, a.port, &spec.tcp).await,
        };
        let (socket, peer) = opened.map_err(|e| {
            if e.kind() == std::io::ErrorKind::ConnectionRefused {
                DialFailure::ConnectionRefused
            } else if classify_io(&e) {
                DialFailure::Transient(e.to_string())
            } else {
                DialFailure::Fatal(e.to_string())
            }
        })?;
        let stream = Tap::socket(socket, shared.clone(), start);
        let notices = a.notices.clone();
        match &tls_config {
            None => {
                let (client, connection) =
                    config.connect_raw(stream, NoTls).await.map_err(|e| {
                        refused_token(
                            classify_pg(&e, a.name, &passfile_text),
                            &e,
                            spec,
                            config.get_dbname(),
                        )
                    })?;
                let task = drive(connection, notices);
                Ok((client, None, peer, task))
            }
            Some(t) => {
                let connect = Connect {
                    config: t.clone(),
                    host: a.name.to_string(),
                    version: version.clone(),
                };
                let tapped = TapConnect {
                    inner: connect.clone(),
                    shared: shared.clone(),
                };
                let (client, connection) =
                    config.connect_raw(stream, tapped).await.map_err(|e| {
                        refused_token(
                            classify_pg(&e, a.name, &passfile_text),
                            &e,
                            spec,
                            config.get_dbname(),
                        )
                    })?;
                let task = drive(connection, notices);
                Ok((client, Some(connect), peer, task))
            }
        }
    };
    let deadline = spec
        .connect_timeout
        .map(|t| tokio::time::Instant::now() + t);
    let (client, tls, peer, connection) = match deadline {
        Some(d) => tokio::time::timeout_at(d, run)
            .await
            .map_err(|_| DialFailure::Transient("timeout expired".into()))??,
        None => run.await?,
    };
    let tls_version = version.lock().ok().and_then(|v| v.clone());
    let session = Session {
        cancel: client.cancel_token(),
        client,
        tap: shared,
        tls: tls.filter(|_| tls_version.is_some()),
        tls_version,
        peer,
        tcp: spec.tcp,
        connection,
    };
    let probe = matches_attrs(&session.client, a.want);
    let satisfied = match deadline {
        Some(d) => tokio::time::timeout_at(d, probe)
            .await
            .map_err(|_| DialFailure::Fatal("timeout expired".into()))?,
        None => probe.await,
    };
    match satisfied {
        Ok(true) => Ok(session),
        Ok(false) => Err(DialFailure::Fatal(format!(
            "server does not satisfy target_session_attrs={}",
            a.want
        ))),
        Err(e) => Err(DialFailure::Fatal(e)),
    }
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

struct Unresolved {
    text: String,
    transient: bool,
}

async fn addresses(host: &str, port: u16) -> std::result::Result<Vec<String>, Unresolved> {
    if host.starts_with('/') || host.parse::<std::net::IpAddr>().is_ok() {
        return Ok(vec![host.to_string()]);
    }
    let found = tokio::net::lookup_host((host, port))
        .await
        .map_err(|e| Unresolved {
            text: format!("could not resolve host \"{host}\": {e}"),
            transient: retry::transient_lookup(&e),
        })?;
    let mut out: Vec<String> = Vec::new();
    for a in found {
        let ip = a.ip().to_string();
        if !out.contains(&ip) {
            out.push(ip);
        }
    }
    Ok(out)
}

async fn matches_attrs(c: &Client, attrs: TargetSessionAttrs) -> std::result::Result<bool, String> {
    if attrs == TargetSessionAttrs::Any {
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
    let read_only = row.try_get(0).map_err(|e| e.to_string())? == Some("on");
    let recovery = row.try_get(1).map_err(|e| e.to_string())? == Some("true");
    Ok(match attrs {
        TargetSessionAttrs::Any => true,
        TargetSessionAttrs::ReadWrite => !read_only,
        TargetSessionAttrs::ReadOnly => read_only,
        TargetSessionAttrs::Primary => !recovery,
        TargetSessionAttrs::Standby | TargetSessionAttrs::PreferStandby => recovery,
    })
}

pub(crate) struct Dialer {
    pub(crate) notices: NoticeSink,
    pub(crate) log: LogSink,
    pub(crate) secret: SecretSink,
}

impl Dialer {
    /// Connects to `db`: every host in order (shuffled under
    /// `load_balance_hosts=random`), every address of each, `sslmode=allow`
    /// as plaintext then TLS, `prefer-standby` as two passes, until one
    /// satisfies `target_session_attrs`. A round whose failures were all
    /// transient is retried with backoff (`conn::retry`).
    pub(crate) async fn connect(&self, spec: &Spec, db: &str) -> Result<Session> {
        spec.require_auth
            .policy()
            .map_err(|e| Error::config(e.to_string()))?;
        if spec.sslmode() != SslMode::Disable {
            super::tls::require_cpu()?;
        }
        let tls = if needs_tls(spec) {
            let owned = spec.clone();
            let built = tokio::task::spawn_blocking(move || tls::config(&owned))
                .await
                .map_err(|e| Error::internal("building the TLS configuration").with_source(e))??;
            Some(built.config)
        } else {
            None
        };
        let mut attempt_no = 0;
        loop {
            attempt_no += 1;
            match self.connect_once(spec, db, tls.as_ref()).await {
                Ok(s) => return Ok(s),
                Err((e, transient)) => {
                    let Some(wait) = retry::delay(attempt_no).filter(|_| transient) else {
                        return Err(e);
                    };
                    (self.log)(format!(
                        "Connection attempt {attempt_no} failed: {}; retrying in {}ms",
                        e.text(),
                        wait.as_millis()
                    ));
                    tokio::time::sleep(wait).await;
                }
            }
        }
    }

    async fn connect_once(
        &self,
        spec: &Spec,
        db: &str,
        tls: Option<&Arc<ClientConfig>>,
    ) -> std::result::Result<Session, (Error, bool)> {
        let passes = match spec.target_session_attrs {
            TargetSessionAttrs::PreferStandby => {
                vec![TargetSessionAttrs::Standby, TargetSessionAttrs::Any]
            }
            attrs => vec![attrs],
        };
        let mut errors: Vec<String> = Vec::new();
        let mut codes: Vec<String> = Vec::new();
        let mut all_transient = true;
        for want in passes {
            let mut endpoints: Vec<_> = spec.hosts.endpoints().collect();
            balance(&mut endpoints, spec.load_balance_hosts);
            for endpoint in endpoints {
                let (host, port) = (endpoint.name.as_str(), endpoint.port);
                let resolved = if spec.token.as_ref().and_then(|t| t.google()).is_some() {
                    Ok(vec![host.to_string()])
                } else if let Some(addr) = endpoint.addr {
                    Ok(vec![addr.to_string()])
                } else {
                    addresses(host, port).await
                };
                let mut addrs = match resolved {
                    Ok(a) => a,
                    Err(u) => {
                        all_transient &= u.transient;
                        errors.push(u.text);
                        continue;
                    }
                };
                balance(&mut addrs, spec.load_balance_hosts);
                for addr in addrs {
                    let label = if addr == host {
                        format!("{host}:{port}")
                    } else {
                        format!("{host} ({addr}):{port}")
                    };
                    let mut modes = vec![spec.sslmode()];
                    if spec.sslmode() == SslMode::Allow {
                        modes.push(SslMode::Require);
                    }
                    let mut outcome = None;
                    for mode in modes {
                        let r = attempt(Attempt {
                            spec,
                            db,
                            name: host,
                            host: &addr,
                            port,
                            tls_mode: mode,
                            tls: tls.cloned(),
                            want,
                            notices: self.notices.clone(),
                            log: self.log.clone(),
                            secret: self.secret.clone(),
                        })
                        .await;
                        match r {
                            Err(DialFailure::Refused(t)) if mode == SslMode::Allow => {
                                outcome = Some(Err(DialFailure::Refused(t)));
                            }
                            other => {
                                outcome = Some(other);
                                break;
                            }
                        }
                    }
                    match outcome {
                        Some(Ok(s)) => return Ok(s),
                        Some(Err(DialFailure::Config(t))) => {
                            return Err((Error::config(t), false));
                        }
                        Some(Err(DialFailure::ConnectionRefused)) => {
                            errors.push(format!("connection refused to {host}:{port}"));
                        }
                        Some(Err(DialFailure::Transient(t))) => {
                            errors.push(format!("{label}: {t}"));
                        }
                        Some(Err(DialFailure::Server(t, code))) => {
                            all_transient = false;
                            codes.push(code);
                            errors.push(format!("{label}: {t}"));
                        }
                        Some(Err(DialFailure::Fatal(t) | DialFailure::Refused(t))) => {
                            all_transient = false;
                            errors.push(format!("{label}: {t}"));
                        }
                        None => {}
                    }
                }
            }
        }
        let error = connect_error(db, &errors, &codes, spec.first());
        Err((error, all_transient && !errors.is_empty()))
    }
}

fn needs_tls(spec: &Spec) -> bool {
    spec.sslmode() != SslMode::Disable
        && spec.token.as_ref().and_then(|t| t.google()).is_none()
        && spec
            .hosts
            .endpoints()
            .any(|e| e.addr.is_some() || !e.name.starts_with('/'))
}

fn balance<T>(items: &mut [T], mode: LoadBalanceHosts) {
    if mode == LoadBalanceHosts::Random {
        items.shuffle(&mut rand::rng());
    }
}

fn connect_error(db: &str, errors: &[String], codes: &[String], first: (&str, u16)) -> Error {
    let joined = errors.join("; ");
    let mut message = format!("connect to \"{db}\": {joined}");
    if joined.contains("connection refused") {
        let (h, p) = first;
        let _ = write!(
            message,
            "\nis PostgreSQL running? check: pg_isready -h {h} -p {p}\nor point pgmi elsewhere: --connection, PGMI_CONNECTION_STRING, pgmi.yaml"
        );
    }
    if ["x509", "TLS", "certificate"]
        .iter()
        .any(|w| joined.contains(w))
    {
        message.push_str("\ncheck --sslmode, --sslcert, --sslkey, --sslrootcert");
    }
    let error = Error::connection(message);
    match codes.first().filter(|c| codes.iter().all(|o| o == *c)) {
        Some(code) => error.with_sql(crate::error::SqlDiagnostics {
            sqlstate: Some(code.clone()),
            ..Default::default()
        }),
        None => error,
    }
}

impl Session {
    /// Sends PostgreSQL's cancel request over a new socket to the same peer,
    /// with the session's TLS; best effort, at most 2 s, errors ignored.
    pub(crate) async fn cancel(&self) {
        let fut = async {
            let Ok(socket) = dial(&self.peer, &self.tcp).await else {
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

    /// Drops the connection without a goodbye: aborts the task that drives it.
    pub(crate) fn disconnect(&self) {
        self.connection.abort();
    }

    pub(crate) fn tls_enabled(&self) -> bool {
        self.tls_version.is_some()
    }
}

#[cfg(test)]
pub(crate) mod fake {
    use std::sync::Arc;

    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::{TcpListener, TcpStream};

    use super::Dialer;
    use crate::conn::resolve::{Flags, Spec, resolve};

    pub(crate) fn quiet() -> Dialer {
        Dialer {
            notices: Arc::new(|_| {}),
            log: Arc::new(|_| {}),
            secret: Arc::new(|_| {}),
        }
    }

    pub(crate) fn spec_for(conn: &str) -> Spec {
        let flags = Flags {
            connection: Some(conn.into()),
            ..Default::default()
        };
        resolve(&flags, &crate::env::Env::default(), None).unwrap()
    }

    pub(crate) async fn listen() -> (TcpListener, u16) {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        (listener, port)
    }

    pub(crate) async fn signed_in(listener: &TcpListener) -> std::io::Result<TcpStream> {
        let (mut socket, _) = listener.accept().await?;
        let len = socket.read_u32().await?;
        let mut startup = vec![0; usize::try_from(len).unwrap_or(0).saturating_sub(4)];
        socket.read_exact(&mut startup).await?;
        socket
            .write_all(&[b'R', 0, 0, 0, 8, 0, 0, 0, 0, b'Z', 0, 0, 0, 5, b'I'])
            .await?;
        Ok(socket)
    }

    fn frame(tag: u8, body: &[u8]) -> Vec<u8> {
        let mut out = vec![tag];
        out.extend_from_slice(&u32::try_from(body.len() + 4).unwrap().to_be_bytes());
        out.extend_from_slice(body);
        out
    }

    /// Answers every simple query with one text column holding `value`.
    pub(crate) async fn one_column(listener: TcpListener, value: &'static str) {
        let Ok(mut socket) = signed_in(&listener).await else {
            return;
        };
        let mut description = vec![0, 1, b'x', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 25, 0xff, 0xff];
        description.extend_from_slice(&[0xff, 0xff, 0xff, 0xff, 0, 0]);
        let mut row = vec![0, 1];
        row.extend_from_slice(&u32::try_from(value.len()).unwrap().to_be_bytes());
        row.extend_from_slice(value.as_bytes());
        let answer = [
            frame(b'T', &description),
            frame(b'D', &row),
            frame(b'C', b"SELECT 1\0"),
            frame(b'Z', b"I"),
        ]
        .concat();
        loop {
            let Ok(tag) = socket.read_u8().await else {
                return;
            };
            let Ok(len) = socket.read_u32().await else {
                return;
            };
            let mut body = vec![0; usize::try_from(len).unwrap_or(0).saturating_sub(4)];
            if socket.read_exact(&mut body).await.is_err() || tag == b'X' {
                return;
            }
            if tag == b'Q' && socket.write_all(&answer).await.is_err() {
                return;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::fake::{quiet, spec_for};
    use super::*;

    fn sqlstate(e: &Error) -> Option<&str> {
        e.sql_diagnostics().and_then(|d| d.sqlstate.as_deref())
    }

    #[test]
    fn random_load_balancing_shuffles_and_disable_keeps_the_order() {
        let given: Vec<u8> = (0..8).collect();
        let mut kept = given.clone();
        balance(&mut kept, LoadBalanceHosts::Disable);
        assert_eq!(kept, given);
        let orders: std::collections::BTreeSet<Vec<u8>> = (0..20)
            .map(|_| {
                let mut v = given.clone();
                balance(&mut v, LoadBalanceHosts::Random);
                let mut sorted = v.clone();
                sorted.sort_unstable();
                assert_eq!(sorted, given);
                v
            })
            .collect();
        assert!(orders.len() > 1, "20 shuffles of 8 hosts gave one order");
    }

    #[test]
    fn a_connect_error_keeps_the_sqlstate_every_host_agreed_on() {
        let one = |codes: &[&str]| {
            let codes: Vec<String> = codes.iter().map(ToString::to_string).collect();
            connect_error("d", &["h:5432: refused".into()], &codes, ("h", 5432))
        };
        let e = one(&["42501"]);
        assert_eq!(e.exit_code(), 11);
        assert_eq!(sqlstate(&e), Some("42501"));
        assert_eq!(sqlstate(&one(&["53300", "53300"])), Some("53300"));
        assert_eq!(sqlstate(&one(&["53300", "28P01"])), None);
        assert_eq!(sqlstate(&one(&[])), None);
    }

    #[test]
    fn a_connect_error_names_every_host_and_hints_at_the_cause() {
        let errors = |e: &[&str]| e.iter().map(ToString::to_string).collect::<Vec<_>>();
        let e = connect_error(
            "app",
            &errors(&["connection refused to a:1", "b:2: timeout expired"]),
            &[],
            ("a", 1),
        );
        assert_eq!(
            e.text(),
            "connect to \"app\": connection refused to a:1; b:2: timeout expired\nis PostgreSQL running? check: pg_isready -h a -p 1\nor point pgmi elsewhere: --connection, PGMI_CONNECTION_STRING, pgmi.yaml"
        );
        for tls in [
            "x509: unknown authority",
            "TLS handshake",
            "bad certificate",
        ] {
            let e = connect_error("app", &errors(&[tls]), &[], ("a", 1));
            assert!(
                e.text()
                    .ends_with("\ncheck --sslmode, --sslcert, --sslkey, --sslrootcert"),
                "{}",
                e.text()
            );
        }
        let e = connect_error(
            "app",
            &errors(&["h:5432: password authentication failed (SQLSTATE 28P01)"]),
            &["28P01".into()],
            ("h", 5432),
        );
        assert_eq!(
            e.text(),
            "connect to \"app\": h:5432: password authentication failed (SQLSTATE 28P01)"
        );
    }

    #[test]
    fn a_refused_token_points_at_the_database_role() {
        let t = refused_token_text(
            "Azure Entra ID",
            "app",
            "shop",
            "password authentication failed",
            "28P01",
        );
        assert!(
            t.starts_with(
                "the Azure Entra ID token was refused for user \"app\" on database \"shop\""
            ),
            "{t}"
        );
        assert!(t.ends_with(
            "check that the signed-in identity has a database role and that --username matches it"
        ));
    }

    async fn silent_after_startup(listener: tokio::net::TcpListener) -> std::io::Result<usize> {
        use tokio::io::AsyncReadExt;
        let mut socket = super::fake::signed_in(&listener).await?;
        let mut rest = Vec::new();
        socket.read_to_end(&mut rest).await
    }

    #[tokio::test]
    async fn connect_timeout_bounds_the_session_attrs_probe_and_the_socket_is_closed() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        let server = tokio::spawn(silent_after_startup(listener));
        let spec = spec_for(&format!(
            "host=127.0.0.1 port={port} user=u dbname=d sslmode=disable target_session_attrs=read-write connect_timeout=2"
        ));
        let started = std::time::Instant::now();
        let outcome =
            tokio::time::timeout(Duration::from_secs(20), quiet().connect(&spec, "d")).await;
        let Ok(Err(e)) = outcome else {
            panic!("the probe was not bounded by connect_timeout")
        };
        assert!(e.text().contains("timeout expired"), "{}", e.text());
        assert_eq!(e.exit_code(), 11);
        assert!(started.elapsed() < Duration::from_secs(10));
        let closed = tokio::time::timeout(CLOSE_GRACE * 5, server).await;
        assert!(
            matches!(closed, Ok(Ok(Ok(_)))),
            "the connection outlived its dropped session"
        );
    }

    #[tokio::test(flavor = "current_thread")]
    async fn opening_an_encrypted_key_leaves_the_runtime_free() {
        let dir = crate::testdir::TempDir::new("slow-key");
        let (cert, key) = (dir.join("client.crt"), dir.join("client.key"));
        std::fs::write(&cert, include_str!("cloud/testdata/sp.crt")).unwrap();
        std::fs::write(
            &key,
            include_str!("cloud/testdata/client-ec-pbes2-slow.pem"),
        )
        .unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&key, std::fs::Permissions::from_mode(0o600)).unwrap();
        }
        let spec = spec_for(&format!(
            "host=127.0.0.1 port=1 user=u dbname=d sslmode=require sslcert={} sslkey={} sslpassword=wrong",
            cert.display(),
            key.display()
        ));
        let started = std::time::Instant::now();
        let _ = tokio::time::timeout(Duration::from_millis(50), quiet().connect(&spec, "d")).await;
        assert!(
            started.elapsed() < Duration::from_millis(500),
            "{:?}",
            started.elapsed()
        );
    }

    #[tokio::test]
    async fn a_probe_answered_with_too_few_columns_is_a_failure_not_a_panic() {
        let (listener, port) = super::fake::listen().await;
        tokio::spawn(super::fake::one_column(listener, "off"));
        let spec = spec_for(&format!(
            "host=127.0.0.1 port={port} user=u dbname=d sslmode=disable target_session_attrs=read-write"
        ));
        let e = quiet().connect(&spec, "d").await.err().unwrap();
        assert_eq!(e.exit_code(), 11, "{}", e.text());
        assert!(e.text().contains("127.0.0.1:"), "{}", e.text());
    }

    #[tokio::test]
    async fn a_mistyped_require_auth_is_refused_before_any_lookup() {
        let spec = spec_for("host=db.invalid user=u dbname=d require_auth=scram-sha265");
        let e = quiet().connect(&spec, "d").await.err().unwrap();
        assert_eq!(e.exit_code(), 10, "{}", e.text());
        assert_eq!(e.text(), "invalid require_auth method: \"scram-sha265\"");
    }

    #[tokio::test]
    async fn the_cancel_goes_to_the_address_that_answered() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let (_, peer) = open("localhost", addr.port(), &Tcp::default())
            .await
            .unwrap();
        let Peer::Tcp(seen) = &peer else {
            panic!("not TCP")
        };
        assert_eq!(*seen, addr);
        let _ = listener.accept().await.unwrap();
        let _cancel = dial(&peer, &Tcp::default()).await.unwrap();
        let (_, from) = listener.accept().await.unwrap();
        assert_eq!(from.ip(), addr.ip());
    }
}
