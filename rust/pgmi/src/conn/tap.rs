//! Reads the backend messages that pass through a connection to track the
//! transaction status and the auth policy. Public for the `tap_reader` fuzz
//! target only; not a stable API.

use std::fmt;
use std::future::Future;
use std::io;
use std::pin::Pin;
use std::str::FromStr;
use std::sync::atomic::{AtomicBool, AtomicU8, Ordering};
use std::sync::{Arc, OnceLock};
use std::task::{Context, Poll, ready};

use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
use tokio_postgres::tls::{ChannelBinding, TlsConnect, TlsStream};

use super::scram::{self, Binding, Client, ScramError};

/// Why the tap refused a connection: a `require_auth` value it cannot
/// read, a method the policy does not allow, or a malformed frame.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Error {
    MixedPolicy(String),
    UnknownMethod(String),
    AuthRequirement { policy: String, what: &'static str },
    Length,
    Oversized(usize),
    Scram(String),
    ChannelBinding,
    UnsupportedSasl,
    SaslUnfinished,
    SessionOversized { tag: u8, len: usize, max: usize },
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::MixedPolicy(text) => write!(
                f,
                "invalid require_auth value \"{text}\": negative and positive methods cannot be mixed"
            ),
            Error::UnknownMethod(name) => write!(f, "invalid require_auth method: \"{name}\""),
            Error::AuthRequirement { policy, what } => write!(
                f,
                "authentication method requirement \"{policy}\" failed: {what}"
            ),
            Error::Length => f.write_str("invalid message length from server"),
            Error::Oversized(n) => write!(
                f,
                "the server sent a {n}-byte message before authentication finished; pgmi refuses more than {STARTUP_FRAME_MAX} bytes there"
            ),
            Error::Scram(text) => f.write_str(text),
            Error::ChannelBinding => f.write_str("server did not use channel binding"),
            Error::UnsupportedSasl => f.write_str("unsupported SASL mechanism"),
            Error::SessionOversized { tag, len, max } => write!(
                f,
                "the server announced a {len}-byte '{}' message; pgmi refuses more than {max} bytes for it",
                char::from(*tag)
            ),
            Error::SaslUnfinished => f.write_str(
                "the server ended SCRAM authentication without proving it knows the password (expected AuthenticationSASLFinal)",
            ),
        }
    }
}

impl std::error::Error for Error {}

impl From<ScramError> for Error {
    fn from(e: ScramError) -> Self {
        Error::Scram(e.to_string())
    }
}

/// The largest message the tap accepts before the first `ReadyForQuery`:
/// startup traffic is small, and a hostile server could otherwise make the
/// client reserve gigabytes.
pub const STARTUP_FRAME_MAX: usize = 64 * 1024;
/// PostgreSQL's own limit on one allocation (`MaxAllocSize`), so on a row.
pub const DATA_FRAME_MAX: usize = 0x3fff_ffff;
pub const SESSION_FRAME_MAX: usize = 64 * 1024 * 1024;

/// Frames after sign-in: each one's announced length is checked against
/// its type's limit before the driver sees the header (and reserves room
/// for the body). COPY's own messages never reach the driver, which reads
/// deploy.sql's answers as a simple query: rows a `COPY ... TO STDOUT`
/// sends are dropped, and a `COPY ... FROM STDIN` is answered with
/// `CopyFail`.
#[derive(Default)]
struct Frames {
    header: [u8; 5],
    have: usize,
    remaining: usize,
    hidden: bool,
}

const COPY_FAIL: &str = "COPY FROM STDIN cannot run in deploy.sql: pgmi sends no COPY data; load the rows with INSERT, or COPY FROM a file on the server";

fn copy_fail() -> Vec<u8> {
    let mut body = COPY_FAIL.as_bytes().to_vec();
    body.push(0);
    frame(b'f', &body)
}

impl Frames {
    /// Compacts `bytes` to what the driver may see and returns its length,
    /// and whether the server asked for COPY data.
    fn pass(&mut self, bytes: &mut [u8]) -> Result<(usize, bool), Error> {
        let (mut r, mut w, mut copy_in) = (0, 0, false);
        while r < bytes.len() {
            if self.remaining == 0 {
                let b = bytes[r];
                if self.have == 0 {
                    self.hidden = matches!(b, b'H' | b'd' | b'c' | b'G');
                    copy_in |= b == b'G';
                }
                if !self.hidden {
                    bytes[w] = b;
                    w += 1;
                }
                self.header[self.have] = b;
                self.have += 1;
                r += 1;
                if self.have == 5 {
                    self.have = 0;
                    let [tag, a, b, c, d] = self.header;
                    let len = u32::from_be_bytes([a, b, c, d]) as usize;
                    let body = len.checked_sub(4).ok_or(Error::Length)?;
                    let max = if matches!(tag, b'D' | b'd') {
                        DATA_FRAME_MAX
                    } else {
                        SESSION_FRAME_MAX
                    };
                    if body > max {
                        return Err(Error::SessionOversized { tag, len, max });
                    }
                    self.remaining = body;
                }
                continue;
            }
            let take = self.remaining.min(bytes.len() - r);
            if !self.hidden {
                bytes.copy_within(r..r + take, w);
                w += take;
            }
            self.remaining -= take;
            r += take;
        }
        Ok((w, copy_in))
    }
}

/// What the tap signs in with when the server asks for SASL.
#[derive(Clone)]
pub(crate) struct Login {
    pub(crate) password: Vec<u8>,
    pub(crate) binding: BindingPolicy,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum BindingPolicy {
    Require,
    Prefer,
    Disable,
}

/// The transaction status of the last `ReadyForQuery`. A byte other than `I`
/// or `E` counts as a block in progress.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TxStatus {
    Idle,
    InBlock,
    Failed,
}

impl From<u8> for TxStatus {
    fn from(byte: u8) -> Self {
        match byte {
            b'I' => TxStatus::Idle,
            b'E' => TxStatus::Failed,
            _ => TxStatus::InBlock,
        }
    }
}

pub struct Shared {
    status: AtomicU8,
    policy: Option<Policy>,
    asked_password: AtomicBool,
    login: OnceLock<Login>,
}

impl Shared {
    pub fn new(require_auth: Option<&str>) -> Result<Arc<Self>, Error> {
        Ok(Self::with_policy(require_auth.map(str::parse).transpose()?))
    }

    #[must_use]
    pub fn with_policy(policy: Option<Policy>) -> Arc<Self> {
        Arc::new(Shared {
            status: AtomicU8::new(b'I'),
            policy,
            asked_password: AtomicBool::new(false),
            login: OnceLock::new(),
        })
    }

    /// The password the tap answers a SASL request with.
    pub(crate) fn sign_in_with(&self, login: Login) {
        let _ = self.login.set(login);
    }

    /// Whether the server asked for a password: cleartext, MD5 or SASL.
    #[must_use]
    pub fn asked_password(&self) -> bool {
        self.asked_password.load(Ordering::Acquire)
    }

    /// The status of the last `ReadyForQuery` read so far; `Idle` before any.
    pub fn status(&self) -> TxStatus {
        self.status.load(Ordering::Acquire).into()
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum AuthMethod {
    Password,
    Md5,
    Gss,
    Sspi,
    ScramSha256,
    None,
}

impl FromStr for AuthMethod {
    type Err = Error;

    fn from_str(name: &str) -> Result<Self, Error> {
        Ok(match name {
            "password" => AuthMethod::Password,
            "md5" => AuthMethod::Md5,
            "gss" => AuthMethod::Gss,
            "sspi" => AuthMethod::Sspi,
            "scram-sha-256" => AuthMethod::ScramSha256,
            "none" => AuthMethod::None,
            _ => return Err(Error::UnknownMethod(name.to_string())),
        })
    }
}

/// A parsed `require_auth` value.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Policy {
    text: String,
    negated: bool,
    methods: Vec<AuthMethod>,
}

impl FromStr for Policy {
    type Err = Error;

    fn from_str(text: &str) -> Result<Self, Error> {
        let items: Vec<&str> = text.split(',').map(str::trim).collect();
        let negated = items.first().is_some_and(|i| i.starts_with('!'));
        let mut methods = vec![];
        for item in items {
            if item.starts_with('!') != negated {
                return Err(Error::MixedPolicy(text.to_string()));
            }
            methods.push(item.strip_prefix('!').unwrap_or(item).parse()?);
        }
        Ok(Policy {
            text: text.to_string(),
            negated,
            methods,
        })
    }
}

impl Policy {
    fn allows(&self, method: AuthMethod) -> bool {
        self.methods.contains(&method) != self.negated
    }

    fn check(&self, code: u32, asked_before: bool) -> Result<(), Error> {
        let (method, what) = match code {
            0 if asked_before => return Ok(()),
            0 => (AuthMethod::None, "server did not complete authentication"),
            3 => (
                AuthMethod::Password,
                "server requested a cleartext password",
            ),
            5 => (AuthMethod::Md5, "server requested a hashed password"),
            7 | 8 => (AuthMethod::Gss, "server requested GSSAPI authentication"),
            9 => (AuthMethod::Sspi, "server requested SSPI authentication"),
            10..=12 => (
                AuthMethod::ScramSha256,
                "server requested SASL authentication",
            ),
            _ => return Ok(()),
        };
        if self.allows(method) {
            Ok(())
        } else {
            Err(Error::AuthRequirement {
                policy: self.text.clone(),
                what,
            })
        }
    }
}

#[derive(Default)]
pub struct Parser {
    header: [u8; 5],
    header_len: usize,
    tag: u8,
    remaining: usize,
    body: [u8; 4],
    body_len: usize,
    asked: bool,
}

impl Parser {
    pub fn feed(&mut self, mut bytes: &[u8], shared: &Shared) -> Result<(), Error> {
        while !bytes.is_empty() {
            if self.header_len < 5 {
                let take = (5 - self.header_len).min(bytes.len());
                self.header[self.header_len..self.header_len + take]
                    .copy_from_slice(&bytes[..take]);
                self.header_len += take;
                bytes = &bytes[take..];
                if self.header_len == 5 {
                    self.tag = self.header[0];
                    let [_, a, b, c, d] = self.header;
                    let len = u32::from_be_bytes([a, b, c, d]) as usize;
                    self.remaining = len.checked_sub(4).ok_or(Error::Length)?;
                    self.body_len = 0;
                    if self.remaining == 0 {
                        self.header_len = 0;
                    }
                }
                continue;
            }
            let take = self.remaining.min(bytes.len());
            let keep = (4 - self.body_len).min(take);
            self.body[self.body_len..self.body_len + keep].copy_from_slice(&bytes[..keep]);
            let before = self.body_len;
            self.body_len += keep;
            self.remaining -= take;
            bytes = &bytes[take..];
            match self.tag {
                b'Z' if before == 0 && self.body_len >= 1 => {
                    shared.status.store(self.body[0], Ordering::Release);
                }
                b'R' if before < 4 && self.body_len == 4 => {
                    let code = u32::from_be_bytes(self.body);
                    if matches!(code, 3 | 5 | 10) {
                        shared.asked_password.store(true, Ordering::Release);
                    }
                    if let Some(policy) = &shared.policy {
                        policy.check(code, self.asked)?;
                    }
                    self.asked |= code != 0;
                }
                _ => {}
            }
            if self.remaining == 0 {
                self.header_len = 0;
            }
        }
        Ok(())
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Start {
    Plain,
    Negotiated,
    Direct,
}

#[derive(PartialEq)]
enum Mode {
    Parsing,
    AwaitingSslAnswer,
    Dormant,
}

pub(crate) struct Tap<S> {
    inner: S,
    shared: Arc<Shared>,
    parser: Parser,
    mode: Mode,
    startup: Option<Startup>,
    end_point: Option<Vec<u8>>,
    frames: Frames,
    carry: Vec<u8>,
    inject: Vec<u8>,
}

/// The tap's part of authentication: whole frames until the first
/// `ReadyForQuery`, SASL answered here and kept from the driver, which
/// then sees a server that signed it in.
#[derive(Default)]
struct Startup {
    pending: Vec<u8>,
    deliver: Vec<u8>,
    send: Vec<u8>,
    flush: bool,
    sasl: Sasl,
    finished: bool,
}

#[derive(Default)]
enum Sasl {
    #[default]
    Idle,
    Sent(Box<Client>, bool),
    Computing(tokio::task::JoinHandle<scram::Final>, bool),
    Final(Vec<u8>, bool),
    Done(bool),
}

fn frame(tag: u8, body: &[u8]) -> Vec<u8> {
    let mut v = Vec::with_capacity(body.len() + 5);
    v.push(tag);
    v.extend_from_slice(
        &u32::try_from(body.len() + 4)
            .unwrap_or(u32::MAX)
            .to_be_bytes(),
    );
    v.extend_from_slice(body);
    v
}

fn next_frame(pending: &mut Vec<u8>) -> Result<Option<Vec<u8>>, Error> {
    let Some(header) = pending.get(..5) else {
        return Ok(None);
    };
    let len = u32::from_be_bytes([header[1], header[2], header[3], header[4]]) as usize;
    let body = len.checked_sub(4).ok_or(Error::Length)?;
    if body > STARTUP_FRAME_MAX {
        return Err(Error::Oversized(body));
    }
    if pending.len() < body + 5 {
        return Ok(None);
    }
    Ok(Some(pending.drain(..body + 5).collect()))
}

fn mechanisms(body: &[u8]) -> (bool, bool) {
    let names = body.split(|b| *b == 0).filter(|n| !n.is_empty());
    let (mut scram, mut plus) = (false, false);
    for n in names {
        scram |= n == scram::SCRAM_SHA_256.as_bytes();
        plus |= n == scram::SCRAM_SHA_256_PLUS.as_bytes();
    }
    (scram, plus)
}

impl Startup {
    fn handle(
        &mut self,
        f: &[u8],
        shared: &Shared,
        parser: &mut Parser,
        end_point: Option<&[u8]>,
    ) -> Result<(), Error> {
        parser.feed(f, shared)?;
        let login = shared.login.get();
        let code = (f[0] == b'R')
            .then(|| f.get(5..9))
            .flatten()
            .map(|c| u32::from_be_bytes([c[0], c[1], c[2], c[3]]));
        let data = f.get(9..).unwrap_or_default();
        let require = login.is_some_and(|l| l.binding == BindingPolicy::Require);
        match (code, login) {
            (Some(10), Some(login)) => {
                let (has_scram, has_plus) = mechanisms(data);
                let point = end_point.filter(|_| login.binding != BindingPolicy::Disable);
                let (plus, binding) = match (has_plus, has_scram, point) {
                    (true, _, Some(p)) => (true, Binding::EndPoint(p.to_vec())),
                    (true, _, None) | (false, true, None) => (false, Binding::Unsupported),
                    (false, true, Some(_)) => (false, Binding::Unrequested),
                    (false, false, _) => return Err(Error::UnsupportedSasl),
                };
                if !plus && require {
                    return Err(Error::ChannelBinding);
                }
                let client = Client::new(&login.password, binding);
                let first = client.first();
                let mechanism = if plus {
                    scram::SCRAM_SHA_256_PLUS
                } else {
                    scram::SCRAM_SHA_256
                };
                let mut body = mechanism.as_bytes().to_vec();
                body.push(0);
                body.extend_from_slice(
                    &i32::try_from(first.len()).unwrap_or(i32::MAX).to_be_bytes(),
                );
                body.extend_from_slice(&first);
                self.send.extend(frame(b'p', &body));
                self.sasl = Sasl::Sent(Box::new(client), plus);
                return Ok(());
            }
            (Some(11), Some(_)) => {
                if let Sasl::Sent(client, plus) = std::mem::take(&mut self.sasl) {
                    let first = client.server_first(data)?;
                    let job = crate::blocking::spawn(move || client.finish(&first));
                    self.sasl = Sasl::Computing(job, plus);
                    return Ok(());
                }
            }
            (Some(12), Some(_)) => {
                if let Sasl::Final(signature, plus) = std::mem::take(&mut self.sasl) {
                    scram::verify(&signature, data)?;
                    self.sasl = Sasl::Done(plus);
                    return Ok(());
                }
            }
            (Some(0), Some(_)) if !matches!(self.sasl, Sasl::Idle | Sasl::Done(_)) => {
                return Err(Error::SaslUnfinished);
            }
            (Some(0), Some(_)) if require && !matches!(self.sasl, Sasl::Done(true)) => {
                return Err(Error::ChannelBinding);
            }
            (Some(3 | 5), Some(_)) if require => return Err(Error::ChannelBinding),
            _ => {}
        }
        self.deliver.extend_from_slice(f);
        if f[0] == b'Z' {
            self.finished = true;
        }
        Ok(())
    }

    fn poll_read<S: AsyncRead + AsyncWrite + Unpin>(
        &mut self,
        inner: &mut S,
        shared: &Shared,
        parser: &mut Parser,
        end_point: Option<&[u8]>,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        loop {
            if !self.deliver.is_empty() {
                let n = self.deliver.len().min(buf.remaining());
                buf.put_slice(&self.deliver[..n]);
                self.deliver.drain(..n);
                return Poll::Ready(Ok(()));
            }
            if self.finished {
                return Poll::Ready(Ok(()));
            }
            while !self.send.is_empty() {
                let n = ready!(Pin::new(&mut *inner).poll_write(cx, &self.send))?;
                if n == 0 {
                    return Poll::Ready(Err(io::ErrorKind::WriteZero.into()));
                }
                self.send.drain(..n);
                self.flush = true;
            }
            if self.flush {
                ready!(Pin::new(&mut *inner).poll_flush(cx))?;
                self.flush = false;
            }
            if let Sasl::Computing(job, plus) = &mut self.sasl {
                let plus = *plus;
                let done = ready!(Pin::new(job).poll(cx)).map_err(io::Error::other)?;
                self.send.extend(frame(b'p', &done.message));
                self.sasl = Sasl::Final(done.server_signature, plus);
                continue;
            }
            if let Some(f) = next_frame(&mut self.pending).map_err(io::Error::other)? {
                self.handle(&f, shared, parser, end_point)
                    .map_err(io::Error::other)?;
                continue;
            }
            let mut chunk = [0u8; 8192];
            let mut read = ReadBuf::new(&mut chunk);
            ready!(Pin::new(&mut *inner).poll_read(cx, &mut read))?;
            if read.filled().is_empty() {
                self.deliver.append(&mut self.pending);
                self.finished = true;
                continue;
            }
            self.pending.extend_from_slice(read.filled());
        }
    }
}

impl<S: AsyncRead + AsyncWrite + Unpin> Tap<S> {
    fn poll_session(
        &mut self,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        loop {
            let _ = self.poll_inject(cx)?;
            if !self.carry.is_empty() {
                let n = self.carry.len().min(buf.remaining());
                let mut head: Vec<u8> = self.carry.drain(..n).collect();
                let kept = self.admit(&mut head)?;
                buf.put_slice(&head[..kept]);
                if kept > 0 {
                    return Poll::Ready(Ok(()));
                }
                continue;
            }
            let start = buf.filled().len();
            ready!(Pin::new(&mut self.inner).poll_read(cx, buf))?;
            if buf.filled().len() == start {
                return Poll::Ready(Ok(()));
            }
            let mut new = buf.filled()[start..].to_vec();
            let kept = self.admit(&mut new)?;
            buf.set_filled(start);
            buf.put_slice(&new[..kept]);
            if kept > 0 {
                return Poll::Ready(Ok(()));
            }
        }
    }

    fn admit(&mut self, bytes: &mut [u8]) -> io::Result<usize> {
        let (kept, copy_in) = self.frames.pass(bytes).map_err(io::Error::other)?;
        self.parser
            .feed(&bytes[..kept], &self.shared)
            .map_err(io::Error::other)?;
        if copy_in {
            self.inject.extend(copy_fail());
        }
        Ok(kept)
    }

    fn poll_inject(&mut self, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        while !self.inject.is_empty() {
            let n = ready!(Pin::new(&mut self.inner).poll_write(cx, &self.inject))?;
            self.inject.drain(..n);
        }
        Pin::new(&mut self.inner).poll_flush(cx)
    }
}

impl<S> Tap<S> {
    pub(crate) fn socket(inner: S, shared: Arc<Shared>, start: Start) -> Self {
        let mode = match start {
            Start::Plain => Mode::Parsing,
            Start::Negotiated => Mode::AwaitingSslAnswer,
            Start::Direct => Mode::Dormant,
        };
        Tap {
            inner,
            shared,
            parser: Parser::default(),
            mode,
            startup: Some(Startup::default()),
            end_point: None,
            frames: Frames::default(),
            carry: Vec::new(),
            inject: Vec::new(),
        }
    }

    fn tls(inner: S, shared: Arc<Shared>, end_point: Option<Vec<u8>>) -> Self {
        Tap {
            inner,
            shared,
            parser: Parser::default(),
            mode: Mode::Parsing,
            startup: Some(Startup::default()),
            end_point,
            frames: Frames::default(),
            carry: Vec::new(),
            inject: Vec::new(),
        }
    }
}

impl<S: AsyncRead + AsyncWrite + Unpin> AsyncRead for Tap<S> {
    fn poll_read(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        let this = self.get_mut();
        if this.mode == Mode::Parsing
            && let Some(startup) = &mut this.startup
        {
            if !(startup.finished && startup.deliver.is_empty()) {
                return startup.poll_read(
                    &mut this.inner,
                    &this.shared,
                    &mut this.parser,
                    this.end_point.as_deref(),
                    cx,
                    buf,
                );
            }
            this.carry = std::mem::take(&mut startup.pending);
            this.startup = None;
        }
        if this.mode == Mode::Parsing && this.startup.is_none() {
            return this.poll_session(cx, buf);
        }
        let start = buf.filled().len();
        ready!(Pin::new(&mut this.inner).poll_read(cx, buf))?;
        let mut new = &buf.filled()[start..];
        if this.mode == Mode::AwaitingSslAnswer {
            match new.first() {
                Some(b'S') => {
                    this.mode = Mode::Dormant;
                    new = &new[1..];
                }
                Some(b'N') => {
                    this.mode = Mode::Parsing;
                    new = &new[1..];
                }
                Some(_) => this.mode = Mode::Parsing,
                None => {}
            }
        }
        if this.mode == Mode::Parsing {
            this.parser
                .feed(new, &this.shared)
                .map_err(io::Error::other)?;
        }
        Poll::Ready(Ok(()))
    }
}

impl<S: AsyncRead + AsyncWrite + Unpin> AsyncWrite for Tap<S> {
    fn poll_write(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        let this = self.get_mut();
        ready!(this.poll_inject(cx))?;
        Pin::new(&mut this.inner).poll_write(cx, buf)
    }

    fn poll_flush(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.get_mut().inner).poll_flush(cx)
    }

    fn poll_shutdown(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.get_mut().inner).poll_shutdown(cx)
    }
}

impl<S: TlsStream + Unpin> TlsStream for Tap<S> {
    fn channel_binding(&self) -> ChannelBinding {
        self.inner.channel_binding()
    }
}

pub(crate) struct TapConnect<C> {
    pub(crate) inner: C,
    pub(crate) shared: Arc<Shared>,
}

/// The TLS end-point hash (`tls-server-end-point`) a SCRAM-SHA-256-PLUS
/// exchange binds to.
pub(crate) trait EndPoint {
    fn end_point(&self) -> Option<Vec<u8>>;
}

impl<S, C> TlsConnect<S> for TapConnect<C>
where
    C: TlsConnect<S>,
    C::Stream: EndPoint + Send + 'static,
    C::Future: Send + 'static,
{
    type Stream = Tap<C::Stream>;
    type Error = C::Error;
    type Future = Pin<Box<dyn Future<Output = Result<Self::Stream, Self::Error>> + Send>>;

    fn connect(self, stream: S) -> Self::Future {
        let shared = self.shared;
        let fut = self.inner.connect(stream);
        Box::pin(async move {
            let stream = fut.await?;
            let point = stream.end_point();
            Ok(Tap::tls(stream, shared, point))
        })
    }
}

#[cfg(test)]
mod startup_tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    fn auth(code: u32, data: &[u8]) -> Vec<u8> {
        let mut body = code.to_be_bytes().to_vec();
        body.extend_from_slice(data);
        frame(b'R', &body)
    }

    async fn read_err(
        server: impl FnOnce(tokio::io::DuplexStream) -> tokio::task::JoinHandle<()>,
        binding: BindingPolicy,
    ) -> io::Error {
        let (client, theirs) = tokio::io::duplex(1 << 16);
        let shared = Shared::new(None).unwrap();
        shared.sign_in_with(Login {
            password: b"pw".to_vec(),
            binding,
        });
        let job = server(theirs);
        let mut tap = Tap::socket(client, shared, Start::Plain);
        let mut buf = vec![0u8; 1024];
        let e = tokio::time::timeout(std::time::Duration::from_secs(5), tap.read(&mut buf))
            .await
            .expect("the tap answered in time")
            .unwrap_err();
        job.abort();
        e
    }

    fn tap_error(e: &io::Error) -> &Error {
        e.get_ref().and_then(|i| i.downcast_ref::<Error>()).unwrap()
    }

    #[tokio::test]
    async fn three_hundred_million_iterations_are_refused_before_any_key_derivation() {
        let e = read_err(
            |mut s| {
                tokio::spawn(async move {
                    s.write_all(&auth(10, b"SCRAM-SHA-256\0\0")).await.unwrap();
                    let mut head = [0u8; 5];
                    s.read_exact(&mut head).await.unwrap();
                    assert_eq!(head[0], b'p');
                    let len = u32::from_be_bytes([head[1], head[2], head[3], head[4]]) as usize;
                    let mut body = vec![0u8; len - 4];
                    s.read_exact(&mut body).await.unwrap();
                    let text = String::from_utf8_lossy(&body).into_owned();
                    let nonce = text.split("r=").nth(1).unwrap().to_string();
                    let first = format!("r={nonce}X,s=c2FsdA==,i=300000000");
                    s.write_all(&auth(11, first.as_bytes())).await.unwrap();
                    std::future::pending::<()>().await;
                })
            },
            BindingPolicy::Prefer,
        )
        .await;
        assert_eq!(
            tap_error(&e).to_string(),
            "the server asks for 300000000 SCRAM iterations; pgmi allows 1 to 10000000"
        );
    }

    #[tokio::test]
    async fn an_oversized_message_before_authentication_is_refused_unread() {
        let e = read_err(
            |mut s| {
                tokio::spawn(async move {
                    let mut huge = vec![b'E'];
                    huge.extend_from_slice(&0x7fff_fff0_u32.to_be_bytes());
                    s.write_all(&huge).await.unwrap();
                    std::future::pending::<()>().await;
                })
            },
            BindingPolicy::Prefer,
        )
        .await;
        assert_eq!(tap_error(&e), &Error::Oversized(0x7fff_ffec));
    }

    #[tokio::test]
    async fn channel_binding_require_refuses_scram_without_plus_and_a_cleartext_request() {
        for request in [auth(10, b"SCRAM-SHA-256\0\0"), auth(3, b"")] {
            let e = read_err(
                move |mut s| {
                    tokio::spawn(async move {
                        s.write_all(&request).await.unwrap();
                        std::future::pending::<()>().await;
                    })
                },
                BindingPolicy::Require,
            )
            .await;
            assert_eq!(tap_error(&e), &Error::ChannelBinding);
        }
    }

    #[tokio::test]
    async fn an_oversized_message_after_sign_in_is_refused_at_its_header() {
        let (client, mut theirs) = tokio::io::duplex(1 << 16);
        let shared = Shared::new(None).unwrap();
        let mut tap = Tap::socket(client, shared, Start::Plain);
        let mut first = auth(0, b"");
        first.extend(frame(b'Z', b"I"));
        first.push(b'T');
        first.extend_from_slice(&0x7fff_fff0_u32.to_be_bytes());
        theirs.write_all(&first).await.unwrap();
        let mut buf = vec![0u8; 1024];
        let mut seen = 0;
        let e = tokio::time::timeout(std::time::Duration::from_secs(5), async {
            loop {
                match tap.read(&mut buf).await {
                    Ok(n) => seen += n,
                    Err(e) => break e,
                }
            }
        })
        .await
        .expect("refused, not waited for");
        assert_eq!(seen, 15);
        assert_eq!(
            tap_error(&e),
            &Error::SessionOversized {
                tag: b'T',
                len: 0x7fff_fff0,
                max: SESSION_FRAME_MAX
            }
        );
    }

    #[test]
    fn copy_messages_never_reach_the_driver_and_copy_in_is_flagged() {
        let mut all = frame(b'H', &[0, 0, 0]);
        all.extend(frame(b'd', b"1\n"));
        all.extend(frame(b'c', b""));
        all.extend(frame(b'C', b"COPY 1\0"));
        all.extend(frame(b'G', &[0, 0, 0]));
        all.extend(frame(b'Z', b"I"));
        let mut want = frame(b'C', b"COPY 1\0");
        want.extend(frame(b'Z', b"I"));
        for cut in 0..all.len() {
            let mut f = Frames::default();
            let (mut a, mut b) = (all[..cut].to_vec(), all[cut..].to_vec());
            let (ka, ia) = f.pass(&mut a).unwrap();
            let (kb, ib) = f.pass(&mut b).unwrap();
            let mut seen = a[..ka].to_vec();
            seen.extend_from_slice(&b[..kb]);
            assert_eq!(seen, want, "cut at {cut}");
            assert!(ia || ib, "cut at {cut}");
        }
    }

    #[test]
    fn session_frames_are_capped_by_type_across_any_split() {
        let mut ok = frame(b'D', &[0; 10]);
        ok.extend(frame(b'N', &[1; 100]));
        for cut in 0..ok.len() {
            let mut f = Frames::default();
            f.pass(&mut ok.clone()[..cut]).unwrap();
            f.pass(&mut ok.clone()[cut..]).unwrap();
        }
        let mut f = Frames::default();
        let mut big_row = vec![b'D'];
        big_row.extend_from_slice(
            &u32::try_from(SESSION_FRAME_MAX + 100)
                .unwrap()
                .to_be_bytes(),
        );
        f.pass(&mut big_row).unwrap();
        let mut f = Frames::default();
        let mut big_notice = vec![b'N'];
        big_notice.extend_from_slice(
            &u32::try_from(SESSION_FRAME_MAX + 100)
                .unwrap()
                .to_be_bytes(),
        );
        assert!(f.pass(&mut big_notice).is_err());
    }

    async fn client_first(s: &mut tokio::io::DuplexStream) -> String {
        let mut head = [0u8; 5];
        s.read_exact(&mut head).await.unwrap();
        let len = u32::from_be_bytes([head[1], head[2], head[3], head[4]]) as usize;
        let mut body = vec![0u8; len - 4];
        s.read_exact(&mut body).await.unwrap();
        let text = String::from_utf8_lossy(&body).into_owned();
        text.split("r=").nth(1).unwrap().to_string()
    }

    #[tokio::test]
    async fn authentication_ok_before_the_server_proved_itself_is_refused() {
        for step in 0..3 {
            for binding in [BindingPolicy::Prefer, BindingPolicy::Disable] {
                let e = read_err(
                    move |mut s| {
                        tokio::spawn(async move {
                            s.write_all(&auth(10, b"SCRAM-SHA-256\0\0")).await.unwrap();
                            let nonce = client_first(&mut s).await;
                            if step > 0 {
                                let first = format!("r={nonce}X,s=c2FsdA==,i=4096");
                                s.write_all(&auth(11, first.as_bytes())).await.unwrap();
                            }
                            if step > 1 {
                                let mut head = [0u8; 5];
                                s.read_exact(&mut head).await.unwrap();
                                let len = u32::from_be_bytes([head[1], head[2], head[3], head[4]]);
                                let mut body = vec![0u8; len as usize - 4];
                                s.read_exact(&mut body).await.unwrap();
                            }
                            s.write_all(&auth(0, b"")).await.unwrap();
                            s.write_all(&frame(b'Z', b"I")).await.unwrap();
                            std::future::pending::<()>().await;
                        })
                    },
                    binding,
                )
                .await;
                assert_eq!(tap_error(&e), &Error::SaslUnfinished, "step {step}");
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use proptest::prelude::*;

    fn frame(tag: u8, body: &[u8]) -> Vec<u8> {
        let mut v = vec![tag];
        v.extend(u32::try_from(body.len() + 4).unwrap().to_be_bytes());
        v.extend(body);
        v
    }

    fn frames() -> Vec<u8> {
        let mut v = vec![];
        v.extend(frame(b'R', &[0, 0, 0, 0]));
        v.extend(frame(b'S', b"a\0b\0"));
        v.extend(frame(b'Z', b"T"));
        v.extend(frame(b'C', b""));
        v.extend(frame(b'Z', b"E"));
        v
    }

    #[test]
    fn status_survives_any_split() {
        let bytes = frames();
        for chunk in 1..=bytes.len() {
            let shared = Shared::new(None).unwrap();
            let mut p = Parser::default();
            for part in bytes.chunks(chunk) {
                p.feed(part, &shared).unwrap();
            }
            assert_eq!(shared.status(), TxStatus::Failed, "chunk size {chunk}");
        }
    }

    #[test]
    fn md5_refused_under_scram_before_the_frame_completes() {
        let shared = Shared::new(Some("scram-sha-256")).unwrap();
        let mut p = Parser::default();
        let md5 = [b'R', 0, 0, 0, 12, 0, 0, 0, 5, 1, 2, 3, 4];
        assert_eq!(
            p.feed(&md5[..9], &shared),
            Err(Error::AuthRequirement {
                policy: "scram-sha-256".into(),
                what: "server requested a hashed password"
            })
        );
    }

    #[test]
    fn negated_policy() {
        let shared = Shared::new(Some("!password")).unwrap();
        assert_eq!(
            Parser::default().feed(&[b'R', 0, 0, 0, 8, 0, 0, 0, 3], &shared),
            Err(Error::AuthRequirement {
                policy: "!password".into(),
                what: "server requested a cleartext password"
            })
        );
        assert_eq!(
            Error::AuthRequirement {
                policy: "!password".into(),
                what: "server requested a cleartext password"
            }
            .to_string(),
            "authentication method requirement \"!password\" failed: server requested a cleartext password"
        );
        assert!(
            Parser::default()
                .feed(&[b'R', 0, 0, 0, 8, 0, 0, 0, 5], &shared)
                .is_ok()
        );
    }

    #[test]
    fn short_length_is_an_error() {
        let shared = Shared::new(None).unwrap();
        assert_eq!(
            Parser::default().feed(&[b'Z', 0, 0, 0, 3], &shared),
            Err(Error::Length)
        );
        assert_eq!(
            Error::Length.to_string(),
            "invalid message length from server"
        );
    }

    fn message() -> impl Strategy<Value = (u8, Vec<u8>)> {
        (
            prop::sample::select(vec![b'Z', b'C', b'D', b'T', b'N', b'S', b'K', b'1', b'2']),
            prop::collection::vec(any::<u8>(), 0..40),
        )
    }

    proptest! {
        #[test]
        fn last_ready_for_query_wins(
            msgs in prop::collection::vec(message(), 1..30),
            cuts in prop::collection::vec(1usize..17, 1..64),
        ) {
            let mut bytes = vec![];
            let mut expected = b'I';
            for (tag, body) in &msgs {
                let body = if *tag == b'Z' {
                    let s = *body.first().unwrap_or(&b'T');
                    expected = s;
                    vec![s]
                } else {
                    body.clone()
                };
                bytes.extend(frame(*tag, &body));
            }
            let shared = Shared::new(None).unwrap();
            let mut p = Parser::default();
            let mut rest = &bytes[..];
            let mut i = 0;
            while !rest.is_empty() {
                let n = cuts[i % cuts.len()].min(rest.len());
                p.feed(&rest[..n], &shared).unwrap();
                rest = &rest[n..];
                i += 1;
            }
            prop_assert_eq!(shared.status(), TxStatus::from(expected));
        }

        #[test]
        fn garbage_never_panics(bytes in prop::collection::vec(any::<u8>(), 0..512), policy in prop::sample::select(vec![None, Some("scram-sha-256"), Some("!md5")])) {
            let shared = Shared::new(policy).unwrap();
            let mut p = Parser::default();
            let _ = p.feed(&bytes, &shared);
        }
    }
}
