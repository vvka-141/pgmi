//! What a deploy asks of the server outside the plan: its version, the
//! client encoding, creating the target database and releasing the session.

use std::time::Duration;

use super::{Needs, created_elsewhere, sql_error, sql_error_needing};
use crate::conn::dial::Session;
use crate::error::{Error, Result};

pub(super) const CLEANUP: Duration = Duration::from_secs(3);

pub(super) async fn create_database(maintenance: &Session, db: &str) -> Result<()> {
    maintenance
        .client
        .batch_execute(&format!("CREATE DATABASE {}", quote_ident(db)))
        .await
        .map_err(|e| {
            created_elsewhere(sql_error_needing(
                "create the target database",
                e,
                Some(Needs::CreateDb),
            ))
        })
}

/// Rolls back an open transaction and releases the deploy lock; a session
/// that does not answer within `CLEANUP` is closed instead.
pub(super) async fn release(session: &Session) {
    let cleanup = async {
        if session.tap.status() != crate::conn::tap::TxStatus::Idle {
            session.client.batch_execute("ROLLBACK").await?;
        }
        session
            .client
            .batch_execute("SELECT pg_catalog.pg_advisory_unlock_all()")
            .await
    };
    if !matches!(tokio::time::timeout(CLEANUP, cleanup).await, Ok(Ok(()))) {
        session.disconnect();
    }
}

pub(crate) fn quote_ident(s: &str) -> String {
    format!("\"{}\"", s.replace('"', "\"\""))
}

/// Sets the session's `client_encoding`; a value the server refuses is
/// reported as a refused connection to `db` at `addr`, as Go reports it.
pub(super) async fn client_encoding(
    session: &Session,
    named: Option<&str>,
    db: &str,
    addr: &str,
) -> Result<()> {
    let Some(encoding) = named else {
        return session
            .client
            .batch_execute(
                "SELECT pg_catalog.set_config('client_encoding', pg_catalog.current_setting('server_encoding'), false)",
            )
            .await
            .map_err(|e| sql_error("match the client encoding to the database", e));
    };
    session
        .client
        .query_typed(
            "SELECT pg_catalog.set_config('client_encoding', $1, false)",
            &[(&encoding, tokio_postgres::types::Type::TEXT)],
        )
        .await
        .map(drop)
        .map_err(|e| {
            let reason = e.as_db_error().map_or_else(
                || {
                    format!(
                        "client_encoding \"{encoding}\": {}",
                        crate::error::chain(&e)
                    )
                },
                |d| {
                    format!(
                        "server at {addr} refused the connection: {} (SQLSTATE {})",
                        d.message(),
                        d.code().code()
                    )
                },
            );
            Error::connection(format!("connect to \"{db}\": {reason}"))
        })
}

const MIN_SERVER_VERSION_NUM: u32 = 150_000;

pub(super) fn supported_server(num: &str, text: &str) -> Result<()> {
    match num.trim().parse::<u32>() {
        Ok(n) if n >= MIN_SERVER_VERSION_NUM => Ok(()),
        _ => Err(Error::config(format!(
            "this server reports PostgreSQL {text}, and pgmi requires {} or newer",
            MIN_SERVER_VERSION_NUM / 10_000
        ))),
    }
}

pub(super) async fn check_server(session: &Session) -> Result<()> {
    let rows = session
        .client
        .simple_query(
            "SELECT pg_catalog.current_setting('server_version_num'), pg_catalog.current_setting('server_version')",
        )
        .await
        .map_err(|e| sql_error("read the server version", e))?;
    let (num, text) = match first_row(&rows) {
        Some(r) => {
            let read = |i| {
                r.try_get(i)
                    .map(|v| v.unwrap_or_default().to_string())
                    .map_err(|e| sql_error("read the server version", e))
            };
            (read(0)?, read(1)?)
        }
        None => Default::default(),
    };
    supported_server(&num, &text)
}

pub(super) fn first_row(
    messages: &[tokio_postgres::SimpleQueryMessage],
) -> Option<&tokio_postgres::SimpleQueryRow> {
    messages.iter().find_map(|m| match m {
        tokio_postgres::SimpleQueryMessage::Row(r) => Some(r),
        _ => None,
    })
}

pub(super) async fn first_value(session: &Session, sql: &str) -> Result<Option<String>> {
    let rows = session
        .client
        .simple_query(sql)
        .await
        .map_err(|e| sql_error("query the server", e))?;
    first_row(&rows)
        .map(|r| r.try_get(0).map_err(|e| sql_error("query the server", e)))
        .transpose()
        .map(|v| v.flatten().map(str::to_string))
}

/// Whether the server says it is a hot standby; `false` when it does not answer.
pub(super) async fn hot_standby(session: &Session) -> bool {
    matches!(
        tokio::time::timeout(CLEANUP, first_value(session, "SHOW in_hot_standby")).await,
        Ok(Ok(Some(v))) if v == "on"
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::error::Kind;

    #[test]
    fn a_server_below_postgres_15_is_refused_as_invalid_config() {
        for (num, text) in [
            ("110016", "11.16 (Debian 11.16-1.pgdg90+1)"),
            ("140023", "14.23"),
            ("", ""),
        ] {
            let e = supported_server(num, text).unwrap_err();
            assert_eq!(e.kind(), Kind::Config, "{text}");
            assert_eq!(
                e.text(),
                format!("this server reports PostgreSQL {text}, and pgmi requires 15 or newer")
            );
        }
        for (num, text) in [
            ("150000", "15.0"),
            ("150018", "15.18 (Debian 15.18-1.pgdg120+1)"),
            ("180004", "18.4"),
        ] {
            assert!(supported_server(num, text).is_ok(), "{text}");
        }
    }
}
