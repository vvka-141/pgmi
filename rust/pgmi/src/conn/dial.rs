use std::fmt::Write as _;
use std::future::{Future, poll_fn};
use std::path::{Path, PathBuf};
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context as TaskContext, Poll};
use std::time::Duration;

use rand::seq::SliceRandom;
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
use tokio::time::Instant;
use tokio_postgres::config::{
    ChannelBinding as PgChannelBinding, SslMode as PgSslMode, SslNegotiation as PgSslNegotiation,
};
use tokio_postgres::error::DbError;
use tokio_postgres::{AsyncMessage, CancelToken, Client, Config, NoTls};

use super::cloud::{self, TokenError};
use super::pgpass;
use super::resolve::{
    ChannelBinding, Endpoint, LoadBalanceHosts, Spec, SslMode, SslNegotiation, TargetSessionAttrs,
};
use super::retry::{self, Cause};
use super::socket::Tcp;
use super::tap::{self, Start, Tap, TapConnect};
use super::tls::{self, Connect, TlsVersion};
use crate::error::{Error, Result, Secret};
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
pub(crate) type SecretSink = Arc<dyn Fn(&Secret) + Send + Sync>;

pub(crate) struct Session {
    pub(crate) client: Client,
    pub(crate) tap: Arc<tap::Shared>,
    pub(crate) tls_version: Option<TlsVersion>,
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
    Google(cloud::CloudSql),
}

/// The server's own DEBUG messages (index builds, implicit indexes) stay
/// back; DEBUG raised by PL/pgSQL `RAISE` reaches the user, as in Go.
fn forwarded(db: &DbError) -> bool {
    db.parsed_severity() != Some(tokio_postgres::error::Severity::Debug)
        || db.routine() == Some("exec_stmt_raise")
}

pub(crate) fn notice_of(db: &DbError) -> Notice {
    let severity = match db.parsed_severity() {
        Some(s) => Severity::from(s.to_string().as_str()),
        None => Severity::from(db.severity()),
    };
    Notice {
        severity,
        message: db.message().to_string(),
        detail: crate::error::field(db.detail()),
        hint: crate::error::field(db.hint()),
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
        Peer::Google(route) => Ok(Stream::CloudSql(Box::new(route.dial(tcp).await?))),
    }
}

/// A server's refusal: its message, SQLSTATE and DETAIL.
#[derive(Debug, Clone, PartialEq, Eq)]
struct Answer {
    message: String,
    code: String,
    detail: Option<String>,
}

impl Answer {
    fn of(db: &DbError) -> Self {
        Answer {
            message: db.message().to_string(),
            code: db.code().code().to_string(),
            detail: crate::error::field(db.detail()),
        }
    }
}

impl std::fmt::Display for Answer {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{} (SQLSTATE {})", self.message, self.code)
    }
}

/// Whether a failed attempt is worth another round.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Class {
    Refused,
    Transient,
    Fatal,
}

/// The typed error behind a failed attempt.
#[derive(Debug)]
enum Origin {
    Io(std::io::Error),
    Driver(tokio_postgres::Error),
    Token(TokenError),
}

impl Origin {
    fn closed_early(&self) -> bool {
        match self {
            Origin::Io(e) => e.kind() == std::io::ErrorKind::UnexpectedEof,
            Origin::Driver(e) => e.is_closed(),
            Origin::Token(_) => false,
        }
    }

    fn error(&self) -> &(dyn std::error::Error + 'static) {
        match self {
            Origin::Io(e) => e,
            Origin::Driver(e) => e,
            Origin::Token(e) => e,
        }
    }
}

#[derive(Debug)]
enum DialFailure {
    Broken {
        text: String,
        class: Class,
        origin: Origin,
    },
    Server(Answer),
    TokenRefused {
        text: String,
        origin: tokio_postgres::Error,
    },
    Timeout,
    ProbeTimeout,
    NotTarget(TargetSessionAttrs),
}

impl DialFailure {
    fn class(&self) -> Class {
        match self {
            DialFailure::Broken { class, .. } => *class,
            DialFailure::Server(answer) if retry::transient(Cause::Sqlstate(&answer.code)) => {
                Class::Transient
            }
            DialFailure::Server(_)
            | DialFailure::Timeout
            | DialFailure::TokenRefused { .. }
            | DialFailure::ProbeTimeout
            | DialFailure::NotTarget(_) => Class::Fatal,
        }
    }

    fn auth_refused(&self) -> bool {
        matches!(self, DialFailure::Server(answer) if answer.code == "28000")
    }

    fn code(&self) -> Option<&str> {
        match self {
            DialFailure::Server(answer) if !self.auth_refused() && self.class() == Class::Fatal => {
                Some(&answer.code)
            }
            _ => None,
        }
    }

    fn origin(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            DialFailure::Broken { origin, .. } => Some(origin.error()),
            DialFailure::TokenRefused { origin, .. } => Some(origin),
            DialFailure::Server(_)
            | DialFailure::Timeout
            | DialFailure::ProbeTimeout
            | DialFailure::NotTarget(_) => None,
        }
    }
}

impl std::fmt::Display for DialFailure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            DialFailure::Broken { text, .. } | DialFailure::TokenRefused { text, .. } => {
                f.write_str(text)
            }
            DialFailure::Server(answer) => answer.fmt(f),
            DialFailure::Timeout | DialFailure::ProbeTimeout => f.write_str("timeout expired"),
            DialFailure::NotTarget(want) => {
                write!(f, "server does not satisfy target_session_attrs={want}")
            }
        }
    }
}

/// One server that gave no session, and why.
#[derive(Debug)]
enum Failed {
    Lookup(Unresolved),
    At {
        host: String,
        addr: String,
        port: u16,
        failure: DialFailure,
    },
}

impl Failed {
    fn code(&self) -> Option<&str> {
        match self {
            Failed::At { failure, .. } => failure.code(),
            Failed::Lookup(_) => None,
        }
    }

    fn answer(&self) -> Option<&str> {
        match self {
            Failed::At {
                failure: DialFailure::Server(a),
                ..
            } => Some(&a.code),
            _ => None,
        }
    }

    fn closed_early(&self) -> bool {
        matches!(self, Failed::At { failure: DialFailure::Broken { origin, .. }, .. } if origin.closed_early())
    }

    fn token_refused(&self) -> bool {
        matches!(
            self,
            Failed::At {
                failure: DialFailure::TokenRefused { .. },
                ..
            }
        )
    }

    fn connection_refused(&self) -> bool {
        matches!(self, Failed::At { failure, .. } if failure.class() == Class::Refused)
    }

    fn transient(&self) -> bool {
        match self {
            Failed::Lookup(u) => u.transient,
            Failed::At { failure, .. } => failure.class() != Class::Fatal,
        }
    }

    fn origin(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Failed::Lookup(u) => u.origin.as_ref().map(|e| e as _),
            Failed::At { failure, .. } => failure.origin(),
        }
    }
}

impl std::fmt::Display for Failed {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Failed::Lookup(u) => f.write_str(&u.text),
            Failed::At {
                host,
                port,
                failure,
                ..
            } if failure.class() == Class::Refused => {
                write!(f, "connection refused to {host}:{port}")
            }
            Failed::At {
                failure: failure @ DialFailure::TokenRefused { .. },
                ..
            } => failure.fmt(f),
            Failed::At {
                host,
                addr,
                port,
                failure,
            } if addr == host => write!(f, "{host}:{port}: {failure}"),
            Failed::At {
                host,
                addr,
                port,
                failure,
            } => write!(f, "{host} ({addr}):{port}: {failure}"),
        }
    }
}

fn io_class(e: &std::io::Error) -> Class {
    if e.kind() == std::io::ErrorKind::ConnectionRefused {
        Class::Refused
    } else if retry::transient(Cause::Io(e.kind())) {
        Class::Transient
    } else {
        Class::Fatal
    }
}

fn refuses_auth(e: &tap::Error) -> bool {
    matches!(
        e,
        tap::Error::AuthRequirement { .. }
            | tap::Error::Scram(_)
            | tap::Error::ChannelBinding
            | tap::Error::UnsupportedSasl
            | tap::Error::Oversized(_)
    )
}

fn broken(text: String, class: Class, e: tokio_postgres::Error) -> DialFailure {
    DialFailure::Broken {
        text,
        class,
        origin: Origin::Driver(e),
    }
}

/// What a failed handshake says about this attempt: `no_password` is
/// whether the server asked for a password the attempt did not have.
fn classify_pg(
    e: tokio_postgres::Error,
    host: &str,
    passfile: &str,
    no_password: bool,
) -> DialFailure {
    if let Some(db) = e.as_db_error() {
        return DialFailure::Server(Answer::of(db));
    }
    let mut cur: Option<&(dyn std::error::Error + 'static)> = Some(&e);
    while let Some(c) = cur {
        if let Some(io) = c.downcast_ref::<std::io::Error>() {
            if io
                .get_ref()
                .and_then(|i| i.downcast_ref::<tap::Error>())
                .is_some_and(refuses_auth)
            {
                return broken(crate::error::os_text(io), Class::Fatal, e);
            }
            let class = io_class(io);
            if class != Class::Fatal {
                return broken(crate::error::os_text(io), class, e);
            }
        }
        if c.downcast_ref::<tap::Error>().is_some_and(refuses_auth) {
            return broken(crate::error::chain(&e), Class::Fatal, e);
        }
        cur = c.source();
    }
    if no_password {
        return broken(
            format!(
                "no password supplied: the server asked for one and none was found in the connection string, PGPASSWORD or {passfile}"
            ),
            Class::Fatal,
            e,
        );
    }
    broken(tls::describe_failure(&e, host), Class::Fatal, e)
}

/// The refusal of a sign-in token, which names the database role to check.
fn token_refused(e: &tokio_postgres::Error, spec: &Spec, db: &str) -> Option<String> {
    let (Some(token), Some(answer)) = (&spec.token, e.as_db_error()) else {
        return None;
    };
    answer
        .code()
        .code()
        .starts_with("28")
        .then(|| refused_token_text(token.provider().label(), &spec.user, db))
}

fn refused_token_text(provider: &str, user: &str, db: &str) -> String {
    format!("the {provider} token was refused for user \"{user}\" on database \"{db}\"")
}

fn sslmode_of(mode: SslMode) -> PgSslMode {
    match mode {
        SslMode::Disable | SslMode::Allow => PgSslMode::Disable,
        SslMode::Prefer => PgSslMode::Prefer,
        SslMode::Require | SslMode::VerifyCa | SslMode::VerifyFull => PgSslMode::Require,
    }
}

/// When a step started now under `connect_timeout` must end; `None` when
/// there is no timeout or the deadline lies beyond the clock.
fn deadline(timeout: Option<Duration>) -> Option<Instant> {
    timeout.and_then(|t| Instant::now().checked_add(t))
}

/// `f`'s output, or `None` when `deadline` passed first.
async fn by<F: Future>(deadline: Option<Instant>, f: F) -> Option<F::Output> {
    match deadline {
        Some(d) => tokio::time::timeout_at(d, f).await.ok(),
        None => Some(f.await),
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

struct Opened {
    client: Client,
    tls: Option<Connect>,
    peer: Peer,
    connection: tokio::task::AbortHandle,
}

impl Attempt<'_> {
    /// Opens a session within `connect_timeout`, counted from before the
    /// password or sign-in token is fetched to the end of the
    /// `target_session_attrs` probe.
    async fn run(self) -> std::result::Result<Session, DialFailure> {
        let deadline = deadline(self.spec.connect_timeout);
        let policy = self.spec.require_auth.policy().ok().flatten().cloned();
        let shared = tap::Shared::with_policy(policy);
        let version = Arc::new(Mutex::new(None));
        let opened = by(deadline, Box::pin(self.handshake(&shared, &version)))
            .await
            .ok_or(DialFailure::Timeout)??;
        let tls_version = version.lock().ok().and_then(|v| *v);
        let session = Session {
            cancel: opened.client.cancel_token(),
            client: opened.client,
            tap: shared,
            tls: opened.tls.filter(|_| tls_version.is_some()),
            tls_version,
            peer: opened.peer,
            tcp: self.spec.tcp,
            connection: opened.connection,
        };
        match by(deadline, matches_attrs(&session.client, self.want)).await {
            None => Err(DialFailure::ProbeTimeout),
            Some(Ok(true)) => Ok(session),
            Some(Ok(false)) => Err(DialFailure::NotTarget(self.want)),
            Some(Err(e)) => Err(broken(e.to_string(), Class::Fatal, e)),
        }
    }

    fn passfile(&self) -> Option<PathBuf> {
        if !self.spec.ambient_passfile {
            return None;
        }
        self.spec
            .passfile
            .clone()
            .or_else(|| self.spec.pg_files.as_ref().map(|f| f.pgpass.clone()))
    }

    /// What this attempt presents: a sign-in token (with the Cloud SQL
    /// route it opens), the password given, the matching `.pgpass` line
    /// (read on a blocking thread), or nothing.
    async fn login(
        &self,
        passfile: Option<&Path>,
    ) -> std::result::Result<Option<cloud::Login>, DialFailure> {
        let spec = self.spec;
        if let Some(t) = &spec.token {
            return t
                .login(self.name, self.port, &spec.user)
                .await
                .map(Some)
                .map_err(|e| DialFailure::Broken {
                    text: e.to_string(),
                    class: Class::Fatal,
                    origin: Origin::Token(e),
                });
        }
        let password = match (&spec.password, passfile) {
            (Some(p), _) => Some(p.clone()),
            (None, None) => None,
            (None, Some(path)) => {
                let (path, host, db, user) = (
                    path.to_path_buf(),
                    self.name.to_string(),
                    self.db.to_string(),
                    spec.user.clone(),
                );
                let port = self.port;
                let found =
                    crate::blocking::spawn(move || pgpass::lookup(&path, &host, port, &db, &user))
                        .await
                        .map_err(|e| {
                            let e = std::io::Error::other(e);
                            DialFailure::Broken {
                                text: e.to_string(),
                                class: Class::Fatal,
                                origin: Origin::Io(e),
                            }
                        })?;
                if let Some(w) = found.warning() {
                    (self.log)(w);
                }
                found.password()
            }
        };
        Ok(password.map(cloud::Login::password))
    }

    fn startup(&self, mode: SslMode, password: Option<&str>) -> Config {
        let spec = self.spec;
        let mut config = Config::new();
        config
            .user(&spec.user)
            .dbname(self.db)
            .application_name(&spec.application_name);
        if let Some(p) = password {
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
        config.ssl_mode(sslmode_of(mode));
        if spec.sslnegotiation == SslNegotiation::Direct && mode != SslMode::Disable {
            config.ssl_negotiation(PgSslNegotiation::Direct);
        }
        config.channel_binding(match spec.channel_binding {
            ChannelBinding::Require if password.is_some() => PgChannelBinding::Prefer,
            ChannelBinding::Require => PgChannelBinding::Require,
            ChannelBinding::Disable => PgChannelBinding::Disable,
            ChannelBinding::Prefer => PgChannelBinding::Prefer,
        });
        config
    }

    async fn handshake(
        &self,
        shared: &Arc<tap::Shared>,
        version: &Arc<Mutex<Option<TlsVersion>>>,
    ) -> std::result::Result<Opened, DialFailure> {
        let spec = self.spec;
        let passfile = self.passfile();
        let (password, cloud_sql) = match self.login(passfile.as_deref()).await? {
            Some(login) => (Some(login.password), login.cloud_sql),
            None => (None, None),
        };
        if let Some(p) = &password {
            (self.secret)(p);
            shared.sign_in_with(tap::Login {
                password: p.expose().as_bytes().to_vec(),
                binding: match spec.channel_binding {
                    ChannelBinding::Require => tap::BindingPolicy::Require,
                    ChannelBinding::Prefer => tap::BindingPolicy::Prefer,
                    ChannelBinding::Disable => tap::BindingPolicy::Disable,
                },
            });
        }
        let mode = if self.host.starts_with('/') || cloud_sql.is_some() {
            SslMode::Disable
        } else {
            self.tls_mode
        };
        let config = self.startup(mode, password.as_ref().map(Secret::expose));
        let start = match sslmode_of(mode) {
            PgSslMode::Disable => Start::Plain,
            _ if spec.sslnegotiation == SslNegotiation::Direct => Start::Direct,
            _ => Start::Negotiated,
        };
        let connect = match (sslmode_of(mode), &self.tls) {
            (PgSslMode::Disable, _) => None,
            (_, Some(config)) => Some(Connect {
                config: config.clone(),
                host: self.name.to_string(),
                version: version.clone(),
            }),
            (_, None) => {
                let e = std::io::Error::other("TLS setup: no TLS configuration for this host");
                return Err(DialFailure::Broken {
                    text: e.to_string(),
                    class: Class::Fatal,
                    origin: Origin::Io(e),
                });
            }
        };
        let opened = match cloud_sql {
            Some(route) => {
                let peer = Peer::Google(route);
                dial(&peer, &spec.tcp).await.map(|s| (s, peer))
            }
            None => open(self.host, self.port, &spec.tcp).await,
        };
        let (socket, peer) = opened.map_err(|e| DialFailure::Broken {
            text: crate::error::os_text(&e),
            class: io_class(&e),
            origin: Origin::Io(e),
        })?;
        let stream = Tap::socket(socket, shared.clone(), start);
        let started = match &connect {
            None => started(config.connect_raw(stream, NoTls).await, &self.notices),
            Some(c) => {
                let tapped = TapConnect {
                    inner: c.clone(),
                    shared: shared.clone(),
                };
                started(config.connect_raw(stream, tapped).await, &self.notices)
            }
        };
        let (client, connection) = started.map_err(|e| {
            let passfile = passfile
                .as_ref()
                .map_or_else(|| "~/.pgpass".into(), |p| p.display().to_string());
            let no_password = password.is_none() && shared.asked_password();
            match token_refused(&e, spec, self.db) {
                Some(text) => DialFailure::TokenRefused { text, origin: e },
                None => classify_pg(e, self.name, &passfile, no_password),
            }
        })?;
        Ok(Opened {
            client,
            tls: connect,
            peer,
            connection,
        })
    }
}

fn started<S, T>(
    connected: std::result::Result<
        (Client, tokio_postgres::Connection<S, T>),
        tokio_postgres::Error,
    >,
    notices: &NoticeSink,
) -> std::result::Result<(Client, tokio::task::AbortHandle), tokio_postgres::Error>
where
    S: AsyncRead + AsyncWrite + Unpin + Send + 'static,
    T: AsyncRead + AsyncWrite + Unpin + Send + 'static,
{
    let (client, connection) = connected?;
    Ok((client, drive(connection, notices.clone())))
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
                Ok(AsyncMessage::Notice(n)) if forwarded(&n) => notices(notice_of(&n)),
                Ok(_) => {}
                Err(_) => break,
            }
        }
    })
    .abort_handle()
}

#[derive(Debug)]
struct Unresolved {
    text: String,
    transient: bool,
    origin: Option<std::io::Error>,
}

async fn addresses(
    host: &str,
    port: u16,
    deadline: Option<Instant>,
) -> std::result::Result<Vec<String>, Unresolved> {
    if host.starts_with('/') || host.parse::<std::net::IpAddr>().is_ok() {
        return Ok(vec![host.to_string()]);
    }
    let found = by(deadline, tokio::net::lookup_host((host, port)))
        .await
        .ok_or_else(|| Unresolved {
            text: format!("could not resolve host \"{host}\": timeout expired"),
            transient: true,
            origin: None,
        })?
        .map_err(|e| Unresolved {
            text: format!("could not resolve host \"{host}\": {e}"),
            transient: retry::transient_lookup(&e),
            origin: Some(e),
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

async fn matches_attrs(
    c: &Client,
    attrs: TargetSessionAttrs,
) -> std::result::Result<bool, tokio_postgres::Error> {
    if attrs == TargetSessionAttrs::Any {
        return Ok(true);
    }
    let rows = c
        .simple_query("SELECT current_setting('transaction_read_only'), pg_is_in_recovery()::text")
        .await?;
    let row = rows.iter().find_map(|m| match m {
        tokio_postgres::SimpleQueryMessage::Row(r) => Some(r),
        _ => None,
    });
    let Some(row) = row else { return Ok(false) };
    let read_only = row.try_get(0)? == Some("on");
    let recovery = row.try_get(1)? == Some("true");
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
    /// transient is retried with backoff (`conn::retry`). A failure's
    /// source is the last round, [`Unconnected`].
    pub(crate) async fn connect(&self, spec: &Spec, db: &str) -> Result<Session> {
        spec.require_auth
            .policy()
            .map_err(|e| Error::config(e.to_string()))?;
        let missing = super::tls::missing_cpu();
        let plain = without_tls(spec, &missing)?;
        if plain.is_some() {
            (self.log)(format!(
                "This CPU lacks {} needed for TLS; sslmode={} connects without it",
                missing.join(", "),
                spec.sslmode().as_str()
            ));
        }
        let spec = plain.as_ref().unwrap_or(spec);
        let tls = if needs_tls(spec) {
            let owned = spec.clone();
            let built = crate::blocking::spawn(move || tls::config(&owned))
                .await
                .map_err(|e| Error::internal("building the TLS configuration").with_source(e))??;
            Some(built)
        } else {
            None
        };
        let mut attempt_no = 0;
        loop {
            attempt_no += 1;
            let round = match self.connect_once(spec, db, tls.as_ref()).await {
                Ok(s) => return Ok(s),
                Err(round) => round,
            };
            let Some(wait) = retry::delay(attempt_no).filter(|_| round.transient()) else {
                return Err(round.into());
            };
            (self.log)(format!(
                "Connection attempt {attempt_no} failed: {}; retrying in {}ms",
                round.reason(),
                wait.as_millis()
            ));
            tokio::time::sleep(wait).await;
        }
    }

    /// One round: each pass of `target_session_attrs`, each endpoint, each
    /// of its addresses, until one gives a session.
    async fn connect_once(
        &self,
        spec: &Spec,
        db: &str,
        tls: Option<&Arc<ClientConfig>>,
    ) -> std::result::Result<Session, Unconnected> {
        let passes = match spec.target_session_attrs {
            TargetSessionAttrs::PreferStandby => {
                vec![TargetSessionAttrs::Standby, TargetSessionAttrs::Any]
            }
            attrs => vec![attrs],
        };
        let mut failed: Vec<Failed> = Vec::new();
        for want in passes {
            let round = Round {
                dialer: self,
                spec,
                db,
                tls,
                want,
            };
            let mut endpoints: Vec<_> = spec.hosts.endpoints().collect();
            balance(&mut endpoints, spec.load_balance_hosts);
            for endpoint in &endpoints {
                if let Some(session) = round.endpoint(endpoint, &mut failed).await {
                    return Ok(session);
                }
            }
        }
        let first = spec.first();
        Err(Unconnected {
            db: db.to_string(),
            maintenance: db == spec.maintenance_db && db != spec.target_db,
            user: spec.user.clone(),
            failed,
            first: (first.name.clone(), first.port),
        })
    }
}

/// One pass over the servers for one `target_session_attrs`.
struct Round<'a> {
    dialer: &'a Dialer,
    spec: &'a Spec,
    db: &'a str,
    tls: Option<&'a Arc<ClientConfig>>,
    want: TargetSessionAttrs,
}

impl Round<'_> {
    /// Tries every address of `endpoint`, recording each failure.
    async fn endpoint(&self, endpoint: &Endpoint, failed: &mut Vec<Failed>) -> Option<Session> {
        let mut addrs = match self.addresses(endpoint).await {
            Ok(a) => a,
            Err(u) => {
                failed.push(Failed::Lookup(u));
                return None;
            }
        };
        balance(&mut addrs, self.spec.load_balance_hosts);
        for addr in addrs {
            match self.address(endpoint, &addr).await {
                Ok(session) => return Some(session),
                Err(failure) => failed.push(Failed::At {
                    host: endpoint.name.clone(),
                    addr,
                    port: endpoint.port,
                    failure,
                }),
            }
        }
        None
    }

    /// The addresses to dial: the Cloud SQL instance name as is, a given
    /// `hostaddr`, or the host's lookup.
    async fn addresses(&self, endpoint: &Endpoint) -> std::result::Result<Vec<String>, Unresolved> {
        if self
            .spec
            .token
            .as_ref()
            .is_some_and(cloud::Token::cloud_sql)
        {
            return Ok(vec![endpoint.name.clone()]);
        }
        if let Some(addr) = endpoint.addr {
            return Ok(vec![addr.to_string()]);
        }
        addresses(
            &endpoint.name,
            endpoint.port,
            deadline(self.spec.connect_timeout),
        )
        .await
    }

    /// Connects to one address; `sslmode=allow` tries TLS when the server
    /// refuses the plaintext login.
    async fn address(
        &self,
        endpoint: &Endpoint,
        addr: &str,
    ) -> std::result::Result<Session, DialFailure> {
        let mode = self.spec.sslmode();
        let first = self.attempt(endpoint, addr, mode).await;
        if mode == SslMode::Allow && first.as_ref().is_err_and(DialFailure::auth_refused) {
            return self.attempt(endpoint, addr, SslMode::Require).await;
        }
        first
    }

    async fn attempt(
        &self,
        endpoint: &Endpoint,
        addr: &str,
        tls_mode: SslMode,
    ) -> std::result::Result<Session, DialFailure> {
        Attempt {
            spec: self.spec,
            db: self.db,
            name: &endpoint.name,
            host: addr,
            port: endpoint.port,
            tls_mode,
            tls: self.tls.cloned(),
            want: self.want,
            notices: self.dialer.notices.clone(),
            log: self.dialer.log.clone(),
            secret: self.dialer.secret.clone(),
        }
        .run()
        .await
    }
}

/// On a CPU the TLS library cannot run on, `allow` and `prefer` connect
/// without TLS, as libpq built without SSL does; a mode that needs TLS is
/// refused.
fn without_tls(spec: &Spec, missing: &[&str]) -> Result<Option<Spec>> {
    if missing.is_empty() || !needs_tls(spec) {
        return Ok(None);
    }
    match spec.sslmode() {
        SslMode::Allow | SslMode::Prefer => {
            let mut plain = spec.clone();
            plain.requested_sslmode = Some(SslMode::Disable);
            Ok(Some(plain))
        }
        _ => Err(super::tls::cpu_error(missing)),
    }
}

fn needs_tls(spec: &Spec) -> bool {
    spec.sslmode() != SslMode::Disable
        && !spec.token.as_ref().is_some_and(cloud::Token::cloud_sql)
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

/// A round over every host that gave no session. `Display` is the message
/// pgmi prints; `source` is the first typed cause (a sign-in
/// [`TokenError`], an `io::Error` or the driver's error).
#[derive(Debug)]
pub(crate) struct Unconnected {
    db: String,
    maintenance: bool,
    user: String,
    failed: Vec<Failed>,
    first: (String, u16),
}

impl Unconnected {
    pub(crate) fn transient(&self) -> bool {
        !self.failed.is_empty() && self.failed.iter().all(Failed::transient)
    }

    fn answer(&self) -> Option<&str> {
        let mut codes = self.failed.iter().filter_map(Failed::answer);
        let first = codes.next();
        first.filter(|c| codes.all(|o| o == *c))
    }

    fn sqlstate(&self) -> Option<&str> {
        let mut codes = self.failed.iter().filter_map(Failed::code);
        let first = codes.next();
        first.filter(|c| codes.all(|o| o == *c))
    }

    fn detail(&self) -> Option<&str> {
        let code = self.answer()?;
        self.failed.iter().find_map(|f| match f {
            Failed::At {
                failure: DialFailure::Server(a),
                ..
            } if a.code == code => a.detail.as_deref(),
            _ => None,
        })
    }
}

impl Unconnected {
    fn reason(&self) -> Reason<'_> {
        Reason(self)
    }

    fn write(&self, f: &mut std::fmt::Formatter<'_>, prefixed: bool) -> std::fmt::Result {
        if self.maintenance && self.answer() == Some("3D000") {
            return write!(
                f,
                "maintenance database \"{}\" does not exist\npgmi connects to it to create or drop the target; point it at an existing database such as postgres: the database in the connection string, or connection.maintenance_database in pgmi.yaml",
                self.db
            );
        }
        let joined = self
            .failed
            .iter()
            .map(ToString::to_string)
            .collect::<Vec<_>>()
            .join("; ");
        if prefixed {
            write!(f, "connect to \"{}\": ", self.db)?;
        }
        f.write_str(&joined)?;
        if self.failed.iter().any(Failed::connection_refused) {
            let (h, p) = &self.first;
            write!(
                f,
                "\nis PostgreSQL running? check: pg_isready -h {h} -p {p}\nor point pgmi elsewhere: --connection, PGMI_CONNECTION_STRING, pgmi.yaml"
            )?;
        }
        match self.answer() {
            Some("53300") => {
                f.write_str("\nserver is at max_connections; close idle backends or wait")?;
            }
            Some("57P03") => f.write_str("\nit is starting up or recovering; retry shortly")?,
            Some("28P01") => {
                f.write_str("\ncheck $PGPASSWORD, ~/.pgpass, or the connection string")?;
            }
            Some("28000") => {
                f.write_str("\nno pg_hba.conf entry matches this host, user, and method")?;
            }
            Some("42501") => write!(
                f,
                "\ngrant access with: GRANT CONNECT ON DATABASE {} TO {}",
                self.db, self.user
            )?,
            _ => {}
        }
        if self.failed.iter().any(|x| {
            matches!(
                x,
                Failed::At {
                    failure: DialFailure::Timeout,
                    ..
                }
            )
        }) {
            f.write_str("\ncheck network reachability and server load; raise --timeout if needed")?;
        }
        if self.failed.iter().any(Failed::token_refused) {
            f.write_str(
                "\ncheck that the signed-in identity has a database role and that --username matches it",
            )?;
        }
        if self
            .failed
            .iter()
            .any(|x| matches!(x, Failed::Lookup(u) if !u.transient))
        {
            f.write_str("\ncheck the host name's spelling, DNS, and $PGHOST")?;
        }
        if self.failed.iter().any(Failed::closed_early) {
            let (h, p) = &self.first;
            write!(
                f,
                "\nthe server closed the connection; it may still be starting up; check: pg_isready -h {h} -p {p}"
            )?;
        } else if self.answer() != Some("28000")
            && ["x509", "TLS", "certificate"]
                .iter()
                .any(|w| joined.contains(w))
        {
            f.write_str("\ncheck --sslmode, --sslcert, --sslkey, --sslrootcert")?;
        }
        Ok(())
    }
}

impl std::fmt::Display for Unconnected {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        self.write(f, true)
    }
}

/// A round's failure as a retry line reads it: without the `connect to` prefix.
struct Reason<'a>(&'a Unconnected);

impl std::fmt::Display for Reason<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        self.0.write(f, false)
    }
}

impl std::error::Error for Unconnected {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        self.failed.iter().find_map(Failed::origin)
    }
}

impl From<Unconnected> for Error {
    fn from(u: Unconnected) -> Self {
        let error = Error::connection(u.to_string());
        let (sqlstate, detail) = (u.sqlstate(), u.detail());
        let error = if sqlstate.is_some() || detail.is_some() {
            error.with_sql(crate::error::SqlDiagnostics {
                sqlstate: sqlstate.map(str::to_string),
                detail: detail.map(str::to_string),
                ..Default::default()
            })
        } else {
            error
        };
        error.with_cause(u)
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
    fn a_cpu_without_the_tls_features_connects_without_tls_only_where_tls_is_optional() {
        let old = ["avx2", "adx"];
        for mode in ["allow", "prefer"] {
            let spec = spec_for(&format!("postgresql://u@db.example/app?sslmode={mode}"));
            let plain = without_tls(&spec, &old).unwrap().expect("plaintext");
            assert_eq!(plain.sslmode(), SslMode::Disable, "{mode}");
            assert!(without_tls(&spec, &[]).unwrap().is_none(), "{mode}");
        }
        for mode in ["require", "verify-ca", "verify-full"] {
            let spec = spec_for(&format!("postgresql://u@db.example/app?sslmode={mode}"));
            let e = without_tls(&spec, &old).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{mode}");
            assert!(e.text().starts_with("this CPU lacks avx2, adx"), "{mode}");
        }
        for conn in [
            "postgresql://u@/app?host=/run/postgresql&sslmode=require",
            "postgresql://u@db.example/app?sslmode=disable",
        ] {
            assert!(
                without_tls(&spec_for(conn), &old).unwrap().is_none(),
                "{conn}"
            );
        }
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

    fn at(host: &str, port: u16, failure: DialFailure) -> Failed {
        Failed::At {
            host: host.into(),
            port,
            addr: host.into(),
            failure,
        }
    }

    fn server(code: &str) -> Failed {
        at(
            "h",
            5432,
            DialFailure::Server(Answer {
                message: "refused".into(),
                code: code.into(),
                detail: Some(format!("detail of {code}")),
            }),
        )
    }

    fn unconnected(maintenance: bool, failed: Vec<Failed>) -> String {
        Unconnected {
            db: "m".into(),
            maintenance,
            user: "u".into(),
            failed,
            first: ("h".into(), 5432),
        }
        .to_string()
    }

    #[test]
    fn a_missing_maintenance_database_says_what_it_is_and_where_to_set_it() {
        let text = unconnected(true, vec![server("3D000")]);
        assert!(
            text.starts_with("maintenance database \"m\" does not exist\n"),
            "{text}"
        );
        assert!(
            text.contains("connection.maintenance_database in pgmi.yaml"),
            "{text}"
        );
        let target = unconnected(false, vec![server("3D000")]);
        assert_eq!(target, "connect to \"m\": h:5432: refused (SQLSTATE 3D000)");
    }

    #[test]
    fn a_missing_connect_privilege_says_what_to_grant() {
        let text = unconnected(false, vec![server("42501")]);
        assert!(
            text.ends_with("\ngrant access with: GRANT CONNECT ON DATABASE m TO u"),
            "{text}"
        );
    }

    #[test]
    fn a_full_or_starting_server_and_an_early_close_get_a_next_step() {
        let full = unconnected(false, vec![server("53300")]);
        assert!(
            full.ends_with("\nserver is at max_connections; close idle backends or wait"),
            "{full}"
        );
        assert!(
            unconnected(false, vec![server("57P03")])
                .ends_with("\nit is starting up or recovering; retry shortly")
        );
        let eof = std::io::Error::new(
            std::io::ErrorKind::UnexpectedEof,
            "TLS handshake: early eof",
        );
        let closed = unconnected(
            false,
            vec![at(
                "h",
                5432,
                DialFailure::Broken {
                    text: eof.to_string(),
                    class: Class::Transient,
                    origin: Origin::Io(eof),
                },
            )],
        );
        assert!(closed.ends_with("\nthe server closed the connection; it may still be starting up; check: pg_isready -h h -p 5432"), "{closed}");
        assert!(!closed.contains("--sslmode"), "{closed}");
    }

    fn fatal(text: &str) -> DialFailure {
        DialFailure::Broken {
            text: text.into(),
            class: Class::Fatal,
            origin: Origin::Io(std::io::Error::other(text.to_string())),
        }
    }

    fn refused() -> DialFailure {
        let e = std::io::Error::from(std::io::ErrorKind::ConnectionRefused);
        DialFailure::Broken {
            text: e.to_string(),
            class: io_class(&e),
            origin: Origin::Io(e),
        }
    }

    fn connect_error(db: &str, failed: Vec<Failed>, first: (&str, u16)) -> Error {
        Unconnected {
            db: db.into(),
            maintenance: false,
            user: "u".into(),
            failed,
            first: (first.0.into(), first.1),
        }
        .into()
    }

    #[test]
    fn a_connect_error_keeps_the_sqlstate_every_host_agreed_on() {
        let one = |failed: Vec<Failed>| connect_error("d", failed, ("h", 5432));
        let e = one(vec![server("42501")]);
        assert_eq!(e.exit_code(), 11);
        assert_eq!(sqlstate(&e), Some("42501"));
        assert_eq!(
            e.text(),
            "connect to \"d\": h:5432: refused (SQLSTATE 42501)\ngrant access with: GRANT CONNECT ON DATABASE d TO u"
        );
        assert_eq!(
            sqlstate(&one(vec![server("3D000"), server("3D000")])),
            Some("3D000")
        );
        assert_eq!(sqlstate(&one(vec![server("3D000"), server("28P01")])), None);
        assert_eq!(sqlstate(&one(vec![at("h", 1, fatal("x"))])), None);
    }

    #[test]
    fn a_connect_error_keeps_the_server_detail() {
        let detail = |failed: Vec<Failed>| {
            connect_error("d", failed, ("h", 5432))
                .sql_diagnostics()
                .and_then(|d| d.detail.clone())
        };
        assert_eq!(
            detail(vec![server("42501")]).as_deref(),
            Some("detail of 42501")
        );
        assert_eq!(detail(vec![server("3D000"), server("28P01")]), None);
        assert_eq!(detail(vec![at("h", 1, fatal("x"))]), None);
    }

    #[test]
    fn a_connect_error_names_every_host_and_hints_at_the_cause() {
        let e = connect_error(
            "app",
            vec![at("a", 1, refused()), at("b", 2, DialFailure::Timeout)],
            ("a", 1),
        );
        assert_eq!(
            e.text(),
            "connect to \"app\": connection refused to a:1; b:2: timeout expired\nis PostgreSQL running? check: pg_isready -h a -p 1\nor point pgmi elsewhere: --connection, PGMI_CONNECTION_STRING, pgmi.yaml\ncheck network reachability and server load; raise --timeout if needed"
        );
        for tls in [
            "x509: unknown authority",
            "TLS handshake",
            "bad certificate",
        ] {
            let e = connect_error("app", vec![at("a", 1, fatal(tls))], ("a", 1));
            assert!(
                e.text()
                    .ends_with("\ncheck --sslmode, --sslcert, --sslkey, --sslrootcert"),
                "{}",
                e.text()
            );
        }
        let e = connect_error(
            "app",
            vec![at(
                "h",
                5432,
                DialFailure::Server(Answer {
                    message: "password authentication failed".into(),
                    code: "28P01".into(),
                    detail: None,
                }),
            )],
            ("h", 5432),
        );
        assert_eq!(
            e.text(),
            "connect to \"app\": h:5432: password authentication failed (SQLSTATE 28P01)\ncheck $PGPASSWORD, ~/.pgpass, or the connection string"
        );
    }

    #[tokio::test]
    async fn a_server_asking_for_a_password_none_was_given_for_is_named() {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let (listener, port) = super::fake::listen().await;
        tokio::spawn(async move {
            let Ok((mut socket, _)) = listener.accept().await else {
                return;
            };
            let Ok(len) = socket.read_u32().await else {
                return;
            };
            let mut startup = vec![0; usize::try_from(len).unwrap_or(0).saturating_sub(4)];
            let _ = socket.read_exact(&mut startup).await;
            let _ = socket.write_all(&[b'R', 0, 0, 0, 8, 0, 0, 0, 3]).await;
            let _ = socket.read_u8().await;
        });
        let spec = spec_for(&format!(
            "host=127.0.0.1 port={port} user=u dbname=d sslmode=disable passfile=/nowhere/pgpass"
        ));
        let Err(e) = quiet().connect(&spec, "d").await else {
            panic!("a password was not needed")
        };
        assert_eq!(e.exit_code(), 11);
        assert_eq!(
            e.text(),
            "connect to \"d\": 127.0.0.1:{port}: no password supplied: the server asked for one and none was found in the connection string, PGPASSWORD or /nowhere/pgpass"
                .replace("{port}", &port.to_string())
        );
    }

    #[test]
    fn only_transient_failures_make_a_round_worth_retrying() {
        let lookup = |transient| {
            Failed::Lookup(Unresolved {
                text: "could not resolve host".into(),
                transient,
                origin: None,
            })
        };
        assert!(lookup(true).transient());
        assert!(!lookup(false).transient());
        assert!(at("h", 1, refused()).transient());
        assert!(!at("h", 1, DialFailure::Timeout).transient());
        assert!(server("57P03").transient());
        assert!(server("53300").transient());
        assert!(!server("3D000").transient());
        assert!(!at("h", 1, DialFailure::ProbeTimeout).transient());
        assert!(!at("h", 1, fatal("f")).transient());
    }

    #[test]
    fn a_refused_token_names_the_user_and_database() {
        let t = refused_token_text("Azure Entra ID", "app", "shop");
        assert_eq!(
            t,
            "the Azure Entra ID token was refused for user \"app\" on database \"shop\""
        );
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

    #[test]
    fn a_deadline_beyond_the_clock_is_no_deadline() {
        assert_eq!(deadline(Some(Duration::MAX)), None);
        assert_eq!(deadline(None), None);
        assert!(deadline(Some(Duration::from_secs(9_223_372_036))).is_some());
    }

    #[tokio::test]
    async fn a_stalled_sign_in_counts_against_connect_timeout_and_the_next_host_is_tried() {
        let (stalled, stalled_port) = super::fake::listen().await;
        let (good, good_port) = super::fake::listen().await;
        tokio::spawn(async move {
            let _keep = stalled;
            let _ = super::fake::signed_in(&good).await;
            std::future::pending::<()>().await;
        });
        let mut spec = spec_for(&format!(
            "host=127.0.0.1,127.0.0.1 port={stalled_port},{good_port} user=u dbname=d sslmode=disable connect_timeout=2"
        ));
        spec.token = Some(crate::conn::cloud::Token::Stalled(
            crate::conn::cloud::Provider::Azure,
            stalled_port,
        ));
        let started = std::time::Instant::now();
        let outcome =
            tokio::time::timeout(Duration::from_secs(20), quiet().connect(&spec, "d")).await;
        let Ok(Ok(session)) = outcome else {
            panic!("the stalled sign-in held the connection past connect_timeout")
        };
        assert!(started.elapsed() < Duration::from_secs(10));
        session.disconnect();
    }

    #[tokio::test]
    async fn a_failed_sign_in_stays_the_typed_source_of_the_connect_error() {
        let mut spec = spec_for("host=127.0.0.1 port=1 user=u dbname=d sslmode=disable");
        spec.token = Some(crate::conn::cloud::Token::Failing(
            crate::conn::cloud::Provider::Azure,
        ));
        let e = quiet().connect(&spec, "d").await.err().unwrap();
        assert_eq!(e.exit_code(), 11);
        assert_eq!(e.text(), "connect to \"d\": 127.0.0.1:1: sign-in refused");
        let mut cur = std::error::Error::source(&e);
        let mut typed = None;
        while let Some(c) = cur {
            typed = typed.or(c.downcast_ref::<TokenError>());
            cur = c.source();
        }
        assert!(
            matches!(typed, Some(TokenError::SignIn(_))),
            "no TokenError in the source chain"
        );
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
