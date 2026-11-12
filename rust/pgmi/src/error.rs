use std::fmt;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Kind {
    General,
    Usage,
    Internal,
    Config,
    Connection,
    Denied,
    Sql,
    NoDeploySql,
    Locked,
    Timeout,
    Interrupted,
}

impl Kind {
    pub(crate) const fn exit_code(self) -> u8 {
        match self {
            Kind::General => 1,
            Kind::Usage => 2,
            Kind::Internal => 3,
            Kind::Config => 10,
            Kind::Connection => 11,
            Kind::Denied => 12,
            Kind::Sql => 13,
            Kind::NoDeploySql => 14,
            Kind::Locked => 15,
            Kind::Timeout => 16,
            Kind::Interrupted => 130,
        }
    }

    pub(crate) const TABLE: [(Kind, &'static str); 11] = [
        (Kind::General, "general error"),
        (Kind::Usage, "usage error"),
        (Kind::Internal, "internal error (panic)"),
        (
            Kind::Config,
            "invalid configuration, refused before connecting",
        ),
        (Kind::Connection, "connection failed"),
        (Kind::Denied, "overwrite not approved"),
        (Kind::Sql, "SQL execution failed"),
        (Kind::NoDeploySql, "deploy.sql not found"),
        (Kind::Locked, "another deploy holds the target"),
        (Kind::Timeout, "--timeout exceeded"),
        (Kind::Interrupted, "interrupted (SIGINT or SIGTERM)"),
    ];
}

#[derive(Debug, Default, Clone)]
pub(crate) struct SqlDiagnostics {
    pub(crate) sqlstate: Option<String>,
    pub(crate) detail: Option<String>,
    pub(crate) hint: Option<String>,
    pub(crate) where_: Option<String>,
    pub(crate) internal_query: Option<String>,
    pub(crate) position: Option<usize>,
    pub(crate) internal_position: Option<usize>,
}

#[derive(Debug)]
pub(crate) struct Error {
    kind: Kind,
    message: String,
    source: Option<Box<dyn std::error::Error + Send + Sync>>,
    cause: Option<Box<dyn std::error::Error + Send + Sync>>,
    sql: Option<Box<SqlDiagnostics>>,
    secrets: Vec<String>,
}

impl Error {
    pub(crate) fn new(kind: Kind, message: impl Into<String>) -> Self {
        Error {
            kind,
            message: message.into(),
            source: None,
            cause: None,
            sql: None,
            secrets: Vec::new(),
        }
    }

    pub(crate) fn usage(m: impl Into<String>) -> Self {
        Self::new(Kind::Usage, m)
    }
    pub(crate) fn config(m: impl Into<String>) -> Self {
        Self::new(Kind::Config, m)
    }
    pub(crate) fn connection(m: impl Into<String>) -> Self {
        Self::new(Kind::Connection, m)
    }
    pub(crate) fn general(m: impl Into<String>) -> Self {
        Self::new(Kind::General, m)
    }
    pub(crate) fn sql(m: impl Into<String>) -> Self {
        Self::new(Kind::Sql, m)
    }
    pub(crate) fn internal(m: impl Into<String>) -> Self {
        Self::new(Kind::Internal, m)
    }

    pub(crate) fn with_source(mut self, e: impl std::error::Error + Send + Sync + 'static) -> Self {
        self.source = Some(Box::new(e));
        self
    }

    /// Keeps `e` as the source without rendering it: the message already
    /// says what `e` says.
    pub(crate) fn with_cause(mut self, e: impl std::error::Error + Send + Sync + 'static) -> Self {
        self.cause = Some(Box::new(e));
        self
    }

    pub(crate) fn with_sql(mut self, d: SqlDiagnostics) -> Self {
        self.sql = Some(Box::new(d));
        self
    }

    /// The same error, server diagnostics kept, under another kind and message.
    pub(crate) fn reworded(self, kind: Kind, message: impl Into<String>) -> Self {
        Error {
            kind,
            message: message.into(),
            source: None,
            cause: None,
            sql: self.sql,
            secrets: self.secrets,
        }
    }

    /// Hides `secrets` wherever the error is rendered: the message and every
    /// source in the chain.
    pub(crate) fn redact(mut self, secrets: &[String]) -> Self {
        self.secrets.extend_from_slice(secrets);
        self
    }

    pub(crate) fn kind(&self) -> Kind {
        self.kind
    }

    pub(crate) fn exit_code(&self) -> u8 {
        self.kind.exit_code()
    }

    pub(crate) fn sql_diagnostics(&self) -> Option<&SqlDiagnostics> {
        self.sql.as_deref()
    }

    pub(crate) fn text(&self) -> String {
        let text = match &self.source {
            Some(s) => format!("{}: {}", self.message, chain(s.as_ref())),
            None => self.message.clone(),
        };
        redact(&text, &self.secrets)
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.text())
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        self.source
            .as_deref()
            .or(self.cause.as_deref())
            .map(|e| e as &(dyn std::error::Error + 'static))
    }
}

pub(crate) type Result<T> = std::result::Result<T, Error>;

/// A password, token or connection string. `Debug` shows `[redacted]`;
/// there is no `Display`, so the value reaches text only through `expose`.
#[derive(Clone, Default, PartialEq, Eq)]
pub(crate) struct Secret(String);

impl Secret {
    pub(crate) fn expose(&self) -> &str {
        &self.0
    }
}

impl From<String> for Secret {
    fn from(s: String) -> Self {
        Secret(s)
    }
}

impl From<&str> for Secret {
    fn from(s: &str) -> Self {
        Secret(s.to_string())
    }
}

impl std::str::FromStr for Secret {
    type Err = std::convert::Infallible;

    fn from_str(s: &str) -> std::result::Result<Self, Self::Err> {
        Ok(Secret(s.to_string()))
    }
}

impl fmt::Debug for Secret {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("[redacted]")
    }
}

/// Replaces every secret of three or more bytes in `text` with `***`.
pub(crate) fn redact(text: &str, secrets: &[String]) -> String {
    let mut out = text.to_string();
    for s in secrets.iter().filter(|s| s.len() >= 3) {
        out = out.replace(s.as_str(), "***");
    }
    out
}

/// Joins an error and its sources with `: `, leaving out a source whose text
/// an earlier one already contains. An [`Error`] renders its own sources,
/// redacted, so the walk stops there.
pub(crate) fn chain(e: &(dyn std::error::Error + 'static)) -> String {
    let mut parts = vec![e.to_string()];
    let mut cur = e.source().filter(|_| !e.is::<Error>());
    while let Some(c) = cur {
        let t = c.to_string();
        if !parts.iter().any(|p| p.contains(&t)) {
            parts.push(t);
        }
        cur = c.source().filter(|_| !c.is::<Error>());
    }
    parts.join(": ")
}

thread_local! {
    static PANICKED: std::cell::RefCell<Option<Panic>> = const { std::cell::RefCell::new(None) };
}

/// A panic caught by [`catching`]: what the hook printed and the stack at
/// the panic, for the `--json` record and the MCP answer.
#[derive(Debug, Default)]
pub(crate) struct Panic {
    message: String,
    stack: String,
}

impl Panic {
    pub(crate) fn error(&self) -> Error {
        let message = if self.message.is_empty() {
            "panic"
        } else {
            &self.message
        };
        Error::internal(format!("internal error (panic): {message}"))
    }

    pub(crate) fn stack(&self) -> &str {
        &self.stack
    }
}

/// Prints a panic as `pgmi: internal error: ...` and leaves it on the
/// panicking thread for the [`catching`] that unwinds there.
pub fn install_panic_hook() {
    std::panic::set_hook(Box::new(|info| {
        use std::io::Write;
        let stack = std::backtrace::Backtrace::force_capture().to_string();
        let _ = writeln!(std::io::stderr(), "pgmi: internal error: {info}");
        let _ = PANICKED.try_with(|p| {
            if let Ok(mut p) = p.try_borrow_mut() {
                *p = Some(Panic {
                    message: info.to_string(),
                    stack,
                });
            }
        });
    }));
}

/// Runs `f`, turning a panic in it into the [`Panic`] the hook saw on this
/// thread.
pub(crate) fn catching<T>(f: impl FnOnce() -> T) -> std::result::Result<T, Panic> {
    std::panic::catch_unwind(std::panic::AssertUnwindSafe(f)).map_err(|_| {
        PANICKED
            .try_with(|p| p.try_borrow_mut().ok().and_then(|mut p| p.take()))
            .ok()
            .flatten()
            .unwrap_or_default()
    })
}

/// [`catching`] for a future: each poll runs inside it, on the thread that
/// polls, so the panic and its message never cross threads.
pub(crate) async fn catching_async<F: std::future::Future>(
    fut: F,
) -> std::result::Result<F::Output, Panic> {
    let mut fut = std::pin::pin!(fut);
    std::future::poll_fn(|cx| match catching(|| fut.as_mut().poll(cx)) {
        Ok(std::task::Poll::Ready(v)) => std::task::Poll::Ready(Ok(v)),
        Ok(std::task::Poll::Pending) => std::task::Poll::Pending,
        Err(p) => std::task::Poll::Ready(Err(p)),
    })
    .await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn exit_codes() {
        assert_eq!(Kind::Config.exit_code(), 10);
        assert_eq!(Kind::Interrupted.exit_code(), 130);
        let e = Error::connection("connect").with_source(std::io::Error::other("refused"));
        assert_eq!(e.text(), "connect: refused");
        assert_eq!(e.exit_code(), 11);
    }

    #[test]
    fn a_redacted_error_hides_the_secret_in_its_sources_too() {
        let inner = Error::config("bad value pw=hunter2");
        let e = Error::connection("cannot connect")
            .with_source(inner)
            .redact(&["hunter2".into()]);
        assert_eq!(e.text(), "cannot connect: bad value pw=***");
        assert_eq!(e.to_string(), e.text());
        let outer = Error::general("deploy failed").with_source(e);
        assert!(!outer.text().contains("hunter2"), "{}", outer.text());
    }

    #[test]
    fn redact_hides_secrets_of_three_bytes_or_more() {
        assert_eq!(
            redact("postgresql://u:secret@h", &["secret".into()]),
            "postgresql://u:***@h"
        );
        assert_eq!(redact("a:ab@h", &["ab".into()]), "a:ab@h");
    }

    #[test]
    fn chain_skips_a_source_already_in_the_text() {
        #[derive(Debug)]
        struct Outer(std::io::Error);
        impl fmt::Display for Outer {
            fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                write!(f, "dial: {}", self.0)
            }
        }
        impl std::error::Error for Outer {
            fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
                Some(&self.0)
            }
        }
        assert_eq!(
            chain(&Outer(std::io::Error::other("refused"))),
            "dial: refused"
        );
        let e = std::io::Error::other(Outer(std::io::Error::other("refused")));
        assert_eq!(chain(&e), "dial: refused");
    }

    #[test]
    #[allow(clippy::panic)]
    fn a_caught_panic_carries_the_hook_message_of_its_own_thread() {
        install_panic_hook();
        let threads: Vec<_> = ["one", "two"]
            .into_iter()
            .map(|word| {
                std::thread::spawn(move || {
                    catching(|| panic!("{word}")).unwrap_err().error().text()
                })
            })
            .collect();
        let texts: Vec<String> = threads.into_iter().map(|t| t.join().unwrap()).collect();
        assert!(
            texts[0].starts_with("internal error (panic): panicked at "),
            "{}",
            texts[0]
        );
        assert!(texts[0].ends_with(":\none"), "{}", texts[0]);
        assert!(texts[1].ends_with(":\ntwo"), "{}", texts[1]);
        assert_eq!(catching(|| 7).unwrap(), 7);
    }

    #[tokio::test]
    #[allow(clippy::panic)]
    async fn a_panicking_future_is_caught_where_it_is_polled() {
        install_panic_hook();
        let p = catching_async(async {
            tokio::task::yield_now().await;
            panic!("late");
        })
        .await
        .unwrap_err();
        assert!(
            p.error().text().ends_with(":\nlate"),
            "{}",
            p.error().text()
        );
        assert!(!p.stack().is_empty());
    }
}
