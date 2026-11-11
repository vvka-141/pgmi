use std::future::poll_fn;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context as TaskContext, Poll};
use std::time::Duration;

use rand::seq::SliceRandom;
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
use tokio_postgres::config::{
    ChannelBinding as PgChannelBinding, SslMode as PgSslMode, SslNegotiation,
};
use tokio_postgres::error::DbError;
use tokio_postgres::{AsyncMessage, CancelToken, Client, Config, NoTls};

use super::pgpass;
use super::resolve::{ChannelBinding, LoadBalanceHosts, Spec, SslMode, TargetSessionAttrs};
use super::retry::{self, Cause};
use super::socket::Tcp;
use super::tap::{self, Start, Tap, TapConnect};
use super::tls::{self, Connect};
use crate::error::{Error, Result};
use crate::output::{Notice, Severity};

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
        Peer::Socket(_) => Err(std::io::Error::other("Unix sockets are not supported here")),
        Peer::Google(token) => match token.google() {
            Some(g) => Ok(Stream::CloudSql(Box::new(g.dial(tcp).await?))),
            None => Err(std::io::Error::other("no Cloud SQL dialer")),
        },
    }
}

enum Failure {
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

fn classify_pg(e: &tokio_postgres::Error, host: &str, passfile: &str) -> Failure {
    if let Some(db) = e.as_db_error() {
        let code = db.code().code();
        let text = format!("{} (SQLSTATE {code})", db.message());
        if retry::transient(Cause::Sqlstate(code)) {
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
                .and_then(|i| i.downcast_ref::<tap::Error>())
                .is_some_and(refuses_auth)
            {
                return Failure::Fatal(io.to_string());
            }
            if io.kind() == std::io::ErrorKind::ConnectionRefused {
                return Failure::ConnectionRefused;
            }
            if classify_io(io) {
                return Failure::Transient(io.to_string());
            }
        }
        if c.downcast_ref::<tap::Error>().is_some_and(refuses_auth) {
            return Failure::Fatal(crate::error::chain(e));
        }
        cur = c.source();
    }
    let text = crate::error::chain(e);
    if text.contains("password missing") {
        return Failure::Fatal(format!(
            "no password supplied: the server asked for one and none was found in the connection string, PGPASSWORD or {passfile}"
        ));
    }
    Failure::Fatal(tls::describe_failure(&text, host))
}

fn refused_token(
    f: Failure,
    e: &tokio_postgres::Error,
    spec: &Spec,
    db_name: Option<&str>,
) -> Failure {
    let (Some(token), Some(db)) = (&spec.token, e.as_db_error()) else {
        return f;
    };
    if !db.code().code().starts_with("28") {
        return f;
    }
    Failure::Fatal(refused_token_text(
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
    let token = match &spec.token {
        Some(t) => Some(
            t.password(a.name, a.port, &spec.user)
                .await
                .map_err(Failure::Fatal)?,
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
        config.password(p);
    }
    let mut options = spec.options.clone().unwrap_or_default();
    if let Some(tz) = &spec.timezone {
        options.push_str(&format!(" -c TimeZone={tz}"));
    }
    if let Some(t) = spec.tcp.user_timeout {
        options.push_str(&format!(" -c tcp_user_timeout={}", t.as_millis()));
    }
    if !options.trim().is_empty() {
        config.options(options.trim());
    }
    let mode = if unix { SslMode::Disable } else { a.tls_mode };
    config.ssl_mode(sslmode_of(mode));
    if spec.sslnegotiation_direct && !unix {
        config.ssl_negotiation(SslNegotiation::Direct);
    }
    config.channel_binding(match spec.channel_binding {
        ChannelBinding::Require => PgChannelBinding::Require,
        ChannelBinding::Disable => PgChannelBinding::Disable,
        ChannelBinding::Prefer => PgChannelBinding::Prefer,
    });
    let shared = tap::Shared::new(spec.require_auth.as_deref())
        .map_err(|e| Failure::Config(e.to_string()))?;
    let start = match sslmode_of(mode) {
        PgSslMode::Disable => Start::Plain,
        _ if spec.sslnegotiation_direct => Start::Direct,
        _ => Start::Negotiated,
    };
    let passfile_text = passfile
        .as_ref()
        .map(|p| p.display().to_string())
        .unwrap_or_else(|| "~/.pgpass".into());
    let tls_config = if matches!(sslmode_of(mode), PgSslMode::Disable) {
        None
    } else {
        Some(tls::config(spec).map_err(|e| Failure::Config(e.text()))?)
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
                Failure::ConnectionRefused
            } else if classify_io(&e) {
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
        tcp: spec.tcp,
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
    let read_only = row.get(0) == Some("on");
    let recovery = row.get(1) == Some("true");
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
}

impl Dialer {
    /// Connects to `db`: every host in order (shuffled under
    /// `load_balance_hosts=random`), every address of each, `sslmode=allow`
    /// as plaintext then TLS, `prefer-standby` as two passes, until one
    /// satisfies `target_session_attrs`. A round whose failures were all
    /// transient is retried with backoff (`conn::retry`).
    pub(crate) async fn connect(&self, spec: &Spec, db: &str) -> Result<Session> {
        if spec.sslmode() != SslMode::Disable {
            super::tls::require_cpu()?;
        }
        let mut attempt_no = 0;
        loop {
            attempt_no += 1;
            match self.connect_once(spec, db).await {
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
                            notices: self.notices.clone(),
                            log: self.log.clone(),
                        })
                        .await;
                        match r {
                            Err(Failure::Refused(t)) if mode == SslMode::Allow => {
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
                        Some(Err(Failure::ConnectionRefused)) => {
                            errors.push(format!("connection refused to {host}:{port}"));
                        }
                        Some(Err(Failure::Transient(t))) => {
                            errors.push(format!("{label}: {t}"));
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
        let error = connect_error(db, &errors, &codes, spec.first());
        Err((error, all_transient && !errors.is_empty()))
    }
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
        message.push_str(&format!(
            "\nis PostgreSQL running? check: pg_isready -h {h} -p {p}\nor point pgmi elsewhere: --connection, PGMI_CONNECTION_STRING, pgmi.yaml"
        ));
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
mod tests {
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
            let codes: Vec<String> = codes.iter().map(|c| c.to_string()).collect();
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
        let errors = |e: &[&str]| e.iter().map(|s| s.to_string()).collect::<Vec<_>>();
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
