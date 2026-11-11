//! Reads the backend messages that pass through a connection to track the
//! transaction status and the auth policy. Public for the `tap_reader` fuzz
//! target only; not a stable API.

use std::fmt;
use std::future::Future;
use std::io;
use std::pin::Pin;
use std::str::FromStr;
use std::sync::Arc;
use std::sync::atomic::{AtomicU8, Ordering};
use std::task::{Context, Poll, ready};

use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
use tokio_postgres::tls::{ChannelBinding, TlsConnect, TlsStream};

/// Why the tap refused a connection: a `require_auth` value it cannot
/// read, a method the policy does not allow, or a malformed frame.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Error {
    MixedPolicy(String),
    UnknownMethod(String),
    AuthRequirement { policy: String, what: &'static str },
    Length,
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
        }
    }
}

impl std::error::Error for Error {}

/// The transaction status of the last ReadyForQuery. A byte other than `I`
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
}

impl Shared {
    pub fn new(require_auth: Option<&str>) -> Result<Arc<Self>, Error> {
        let policy = require_auth.map(Policy::parse).transpose()?;
        Ok(Arc::new(Shared {
            status: AtomicU8::new(b'I'),
            policy,
        }))
    }

    /// The status of the last ReadyForQuery read so far; `Idle` before any.
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

struct Policy {
    text: String,
    negated: bool,
    methods: Vec<AuthMethod>,
}

impl Policy {
    fn parse(text: &str) -> Result<Self, Error> {
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
                    shared.status.store(self.body[0], Ordering::Release)
                }
                b'R' if before < 4 && self.body_len == 4 => {
                    let code = u32::from_be_bytes(self.body);
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
        }
    }

    fn tls(inner: S, shared: Arc<Shared>) -> Self {
        Tap {
            inner,
            shared,
            parser: Parser::default(),
            mode: Mode::Parsing,
        }
    }
}

impl<S: AsyncRead + Unpin> AsyncRead for Tap<S> {
    fn poll_read(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        let this = self.get_mut();
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

impl<S: AsyncWrite + Unpin> AsyncWrite for Tap<S> {
    fn poll_write(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        Pin::new(&mut self.get_mut().inner).poll_write(cx, buf)
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

impl<S, C> TlsConnect<S> for TapConnect<C>
where
    C: TlsConnect<S>,
    C::Stream: Send + 'static,
    C::Future: Send + 'static,
{
    type Stream = Tap<C::Stream>;
    type Error = C::Error;
    type Future = Pin<Box<dyn Future<Output = Result<Self::Stream, Self::Error>> + Send>>;

    fn connect(self, stream: S) -> Self::Future {
        let shared = self.shared;
        let fut = self.inner.connect(stream);
        Box::pin(async move { Ok(Tap::tls(fut.await?, shared)) })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use proptest::prelude::*;

    fn frame(tag: u8, body: &[u8]) -> Vec<u8> {
        let mut v = vec![tag];
        v.extend(((body.len() + 4) as u32).to_be_bytes());
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
