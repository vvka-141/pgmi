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
}

#[derive(Debug)]
pub(crate) struct Error {
    kind: Kind,
    message: String,
    source: Option<Box<dyn std::error::Error + Send + Sync>>,
    sql: Option<Box<SqlDiagnostics>>,
}

impl Error {
    pub(crate) fn new(kind: Kind, message: impl Into<String>) -> Self {
        Error {
            kind,
            message: message.into(),
            source: None,
            sql: None,
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

    pub(crate) fn with_sql(mut self, d: SqlDiagnostics) -> Self {
        self.sql = Some(Box::new(d));
        self
    }

    pub(crate) fn redact(mut self, secrets: &[String]) -> Self {
        self.message = redact(&self.message, secrets);
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
        let mut out = self.message.clone();
        let mut cur: Option<&(dyn std::error::Error + 'static)> = match &self.source {
            Some(s) => Some(s.as_ref()),
            None => None,
        };
        while let Some(e) = cur {
            out.push_str(": ");
            out.push_str(&e.to_string());
            cur = e.source();
        }
        out
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.text())
    }
}

impl std::error::Error for Error {}

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
/// an earlier one already contains.
pub(crate) fn chain(e: &(dyn std::error::Error + 'static)) -> String {
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

static LAST_PANIC: std::sync::Mutex<Option<(String, String)>> = std::sync::Mutex::new(None);

/// Prints a panic as `pgmi: internal error: ...` and keeps it for the
/// `--json` record and the MCP answer.
pub fn install_panic_hook() {
    std::panic::set_hook(Box::new(|info| {
        use std::io::Write;
        let stack = std::backtrace::Backtrace::force_capture().to_string();
        let _ = writeln!(std::io::stderr(), "pgmi: internal error: {info}");
        if let Ok(mut last) = LAST_PANIC.lock() {
            *last = Some((info.to_string(), stack));
        }
    }));
}

pub(crate) fn panic_error() -> (Error, String) {
    let (message, stack) = LAST_PANIC
        .lock()
        .ok()
        .and_then(|mut l| l.take())
        .unwrap_or_else(|| ("panic".into(), String::new()));
    (
        Error::internal(format!("internal error (panic): {message}")),
        stack,
    )
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
}
