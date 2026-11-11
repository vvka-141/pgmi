use std::fmt;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
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
    pub fn exit_code(self) -> u8 {
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

    pub const TABLE: [(Kind, &'static str); 11] = [
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
pub struct SqlDiagnostics {
    pub sqlstate: Option<String>,
    pub detail: Option<String>,
    pub hint: Option<String>,
    pub where_: Option<String>,
    pub internal_query: Option<String>,
}

#[derive(Debug)]
pub struct Error {
    kind: Kind,
    message: String,
    source: Option<Box<dyn std::error::Error + Send + Sync>>,
    sql: Option<Box<SqlDiagnostics>>,
}

impl Error {
    pub fn new(kind: Kind, message: impl Into<String>) -> Self {
        Error {
            kind,
            message: message.into(),
            source: None,
            sql: None,
        }
    }

    pub fn usage(m: impl Into<String>) -> Self {
        Self::new(Kind::Usage, m)
    }
    pub fn config(m: impl Into<String>) -> Self {
        Self::new(Kind::Config, m)
    }
    pub fn connection(m: impl Into<String>) -> Self {
        Self::new(Kind::Connection, m)
    }
    pub fn general(m: impl Into<String>) -> Self {
        Self::new(Kind::General, m)
    }
    pub fn sql(m: impl Into<String>) -> Self {
        Self::new(Kind::Sql, m)
    }
    pub fn internal(m: impl Into<String>) -> Self {
        Self::new(Kind::Internal, m)
    }

    pub fn with_source(mut self, e: impl std::error::Error + Send + Sync + 'static) -> Self {
        self.source = Some(Box::new(e));
        self
    }

    pub fn with_sql(mut self, d: SqlDiagnostics) -> Self {
        self.sql = Some(Box::new(d));
        self
    }

    pub fn redact(mut self, secrets: &[String]) -> Self {
        self.message = crate::output::redact(&self.message, secrets);
        self
    }

    pub fn kind(&self) -> Kind {
        self.kind
    }

    pub fn exit_code(&self) -> u8 {
        self.kind.exit_code()
    }

    pub fn sql_diagnostics(&self) -> Option<&SqlDiagnostics> {
        self.sql.as_deref()
    }

    pub fn text(&self) -> String {
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

pub type Result<T> = std::result::Result<T, Error>;

static LAST_PANIC: std::sync::Mutex<Option<(String, String)>> = std::sync::Mutex::new(None);

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

pub fn panic_error() -> (Error, String) {
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
}
