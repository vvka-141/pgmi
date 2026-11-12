//! Whole-deploy retry (record 0025): which failed attempts run the deploy
//! again from the start, how long to wait first, and the line that says so.

use std::time::Duration;

use crate::conn::cloud;
use crate::conn::dial::Unconnected;
use crate::conn::retry::{Cause, transient as transient_io};
use crate::error::{Error, Kind};

/// Whether a deploy retries, and how many attempts it gets with no time box.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) struct Policy {
    pub(super) enabled: bool,
    pub(super) cap: Option<usize>,
}

pub(super) const UNBOXED_ATTEMPTS: usize = 5;

/// Why a failed attempt may succeed when the deploy runs again.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) enum Transient {
    Server(String),
    ConnectionLost,
    Unreachable,
    SignIn,
}

impl std::fmt::Display for Transient {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Transient::Server(code) => write!(f, "{code} {}", server_reason(code)),
            Transient::ConnectionLost => f.write_str("connection lost"),
            Transient::Unreachable => f.write_str("could not connect"),
            Transient::SignIn => f.write_str("sign-in endpoint unavailable"),
        }
    }
}

fn server_reason(code: &str) -> &'static str {
    match code {
        "40001" => "serialization failure",
        "40P01" => "deadlock detected",
        "55P03" => "lock not available",
        "57P01" => "admin shutdown",
        "57P02" => "crash shutdown",
        "57P03" => "cannot connect now",
        "53300" => "too many connections",
        "25006" => "read-only transaction on a hot standby",
        _ => "connection exception",
    }
}

const SERVER: [&str; 7] = [
    "57P01", "57P02", "57P03", "53300", "40001", "40P01", "55P03",
];

/// What the failed attempt saw besides its error.
#[derive(Debug, Default, Clone, Copy)]
pub(super) struct Seen {
    pub(super) in_test: bool,
    pub(super) hot_standby: bool,
    pub(super) connected: bool,
}

/// Why `e` is worth another attempt, or `None` when it is final.
pub(super) fn transient(e: &Error, seen: Seen) -> Option<Transient> {
    if seen.in_test || !matches!(e.kind(), Kind::Sql | Kind::Connection) {
        return None;
    }
    if let Some(code) = e.sql_diagnostics().and_then(|d| d.sqlstate.as_deref()) {
        let retried = code.starts_with("08")
            || SERVER.contains(&code)
            || (code == "25006" && seen.hot_standby);
        return retried.then(|| Transient::Server(code.to_string()));
    }
    let mut cur: Option<&(dyn std::error::Error + 'static)> = Some(e);
    while let Some(c) = cur {
        if let Some(u) = c.downcast_ref::<Unconnected>() {
            return u.transient().then_some(Transient::Unreachable);
        }
        if cloud::unavailable(c) {
            return Some(Transient::SignIn);
        }
        if c.downcast_ref::<tokio_postgres::Error>()
            .is_some_and(|pg| pg.as_db_error().is_none() && pg.is_closed())
        {
            return Some(Transient::ConnectionLost);
        }
        if c.downcast_ref::<std::io::Error>()
            .is_some_and(|io| transient_io(Cause::Io(io.kind())))
        {
            return Some(Transient::ConnectionLost);
        }
        cur = c.source();
    }
    None
}

/// The wait after attempt `failed` (from 1): 1 s doubling to 30 s, varied
/// by up to 20% either way.
pub(super) fn backoff(failed: usize) -> Duration {
    let doublings = u32::try_from(failed.saturating_sub(1))
        .unwrap_or(u32::MAX)
        .min(5);
    Duration::from_secs(1u64 << doublings)
        .min(Duration::from_secs(30))
        .mul_f64(rand::random_range(0.8..=1.2))
}

/// What is left of the run after a wait: time in a box, attempts without one.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum Left {
    Time(Duration),
    Attempts(usize),
}

pub(super) fn retrying_line(attempt: usize, why: &Transient, wait: Duration, left: Left) -> String {
    let tenths = Duration::from_millis(
        u64::try_from(wait.as_millis().div_ceil(100) * 100).unwrap_or(u64::MAX),
    );
    let left = match left {
        Left::Time(t) => format!(
            "{} left",
            crate::duration::render(Duration::from_secs(t.as_secs()))
        ),
        Left::Attempts(1) => "1 attempt left".to_string(),
        Left::Attempts(n) => format!("{n} attempts left"),
    };
    format!(
        "pgmi: attempt {attempt} failed ({why}); retrying in {}, {left}",
        crate::duration::render(tenths)
    )
}

fn attempts(n: usize) -> String {
    if n == 1 {
        "1 attempt".into()
    } else {
        format!("{n} attempts")
    }
}

fn used_up_text(timeout: Duration, n: usize, last: &Error) -> String {
    format!(
        "time box of {} used up after {}; last error: {}",
        crate::duration::render(timeout),
        attempts(n),
        last.text()
    )
}

/// The error a run ends with when its time box ran out inside attempt `n`,
/// `last` the error of the attempt before.
pub(super) fn used_up(kind: Kind, timeout: Duration, n: usize, last: &Error) -> Error {
    Error::new(kind, used_up_text(timeout, n, last))
}

/// `last` as the error that ends a run whose next wait would pass the
/// deadline: its kind, diagnostics and failure record kept.
pub(super) fn cut_short(timeout: Duration, n: usize, last: Error) -> Error {
    let (kind, text) = (last.kind(), used_up_text(timeout, n, &last));
    last.reworded(kind, text)
}

/// `last` as the error that ends a run with no time box after `n` attempts.
pub(super) fn gave_up(n: usize, last: Error) -> Error {
    let text = format!(
        "gave up after {} with no time box (--timeout 0); last error: {}",
        attempts(n),
        last.text()
    );
    let kind = last.kind();
    last.reworded(kind, text)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::error::SqlDiagnostics;

    fn coded(kind: Kind, code: &str) -> Error {
        Error::new(kind, format!("ERROR: x (SQLSTATE {code})")).with_sql(SqlDiagnostics {
            sqlstate: Some(code.into()),
            ..SqlDiagnostics::default()
        })
    }

    #[test]
    fn the_transient_table_retries_these_server_errors() {
        for code in [
            "08000", "08006", "08P01", "57P01", "57P02", "57P03", "53300", "40001", "40P01",
            "55P03",
        ] {
            for kind in [Kind::Sql, Kind::Connection] {
                assert_eq!(
                    transient(&coded(kind, code), Seen::default()),
                    Some(Transient::Server(code.into())),
                    "{code}"
                );
            }
        }
    }

    #[test]
    fn other_server_errors_and_refused_sign_ins_are_final() {
        for code in [
            "42601", "23505", "42501", "P0001", "28P01", "28000", "3D000", "57014", "53200",
            "XX000", "25P02",
        ] {
            assert_eq!(
                transient(&coded(Kind::Sql, code), Seen::default()),
                None,
                "{code}"
            );
            assert_eq!(
                transient(&coded(Kind::Connection, code), Seen::default()),
                None,
                "{code}"
            );
        }
    }

    #[test]
    fn read_only_is_retried_only_on_a_hot_standby() {
        let e = coded(Kind::Sql, "25006");
        assert_eq!(transient(&e, Seen::default()), None);
        assert_eq!(
            transient(
                &e,
                Seen {
                    hot_standby: true,
                    ..Seen::default()
                }
            ),
            Some(Transient::Server("25006".into()))
        );
    }

    #[test]
    fn nothing_that_fails_inside_a_test_is_retried() {
        let seen = Seen {
            in_test: true,
            hot_standby: true,
            connected: true,
        };
        for code in ["40P01", "40001", "08006", "57P01"] {
            assert_eq!(transient(&coded(Kind::Sql, code), seen), None, "{code}");
        }
        let lost = Error::connection("execute deploy.sql")
            .with_source(std::io::Error::from(std::io::ErrorKind::ConnectionReset));
        assert_eq!(transient(&lost, seen), None);
    }

    #[test]
    fn stops_configuration_and_locks_are_final_whatever_their_cause() {
        for kind in [
            Kind::Config,
            Kind::Timeout,
            Kind::Interrupted,
            Kind::Locked,
            Kind::Denied,
            Kind::General,
            Kind::Internal,
        ] {
            assert_eq!(
                transient(&coded(kind, "40P01"), Seen::default()),
                None,
                "{kind:?}"
            );
        }
    }

    #[test]
    fn a_connection_lost_mid_run_is_retried_by_its_socket_error() {
        for kind in [
            std::io::ErrorKind::ConnectionReset,
            std::io::ErrorKind::BrokenPipe,
            std::io::ErrorKind::UnexpectedEof,
            std::io::ErrorKind::ConnectionAborted,
        ] {
            let e = Error::connection("execute deploy.sql").with_source(std::io::Error::from(kind));
            assert_eq!(
                transient(&e, Seen::default()),
                Some(Transient::ConnectionLost),
                "{kind:?}"
            );
        }
        let other = Error::connection("execute deploy.sql")
            .with_source(std::io::Error::other("require_auth refused md5"));
        assert_eq!(transient(&other, Seen::default()), None);
        let wrapped = Error::connection("execute deploy.sql")
            .with_source(std::io::Error::from(std::io::ErrorKind::ConnectionReset))
            .reworded(Kind::Connection, "lost");
        assert_eq!(
            transient(&wrapped, Seen::default()),
            Some(Transient::ConnectionLost)
        );
    }

    #[test]
    fn a_sign_in_endpoint_that_gave_up_after_its_own_retries_is_retried() {
        let e = Error::connection("failed to acquire a token")
            .with_cause(cloud::Exhausted::new(
                "the endpoint",
                reqwest::StatusCode::SERVICE_UNAVAILABLE,
                4,
            ))
            .reworded(
                Kind::Connection,
                "connect to \"d\": failed to acquire a token",
            );
        assert_eq!(transient(&e, Seen::default()), Some(Transient::SignIn));
        assert_eq!(
            transient(&Error::connection("401 Unauthorized"), Seen::default()),
            None
        );
    }

    #[test]
    fn waits_double_from_one_second_to_thirty_within_twenty_percent() {
        for (failed, base) in [
            (1, 1.0),
            (2, 2.0),
            (3, 4.0),
            (4, 8.0),
            (5, 16.0),
            (6, 30.0),
            (7, 30.0),
            (60, 30.0),
        ] {
            let waits: Vec<f64> = (0..200).map(|_| backoff(failed).as_secs_f64()).collect();
            assert!(
                waits
                    .iter()
                    .all(|w| (base * 0.8 - 1e-9..=base * 1.2 + 1e-9).contains(w)),
                "{failed}: {waits:?}"
            );
            assert!(
                waits.iter().any(|w| (w - waits[0]).abs() > 1e-3),
                "no jitter: {waits:?}"
            );
        }
    }

    #[test]
    fn the_retry_line_names_the_attempt_the_reason_the_wait_and_what_is_left() {
        assert_eq!(
            retrying_line(
                2,
                &Transient::Server("40P01".into()),
                Duration::from_millis(4_050),
                Left::Time(Duration::from_millis(151_900))
            ),
            "pgmi: attempt 2 failed (40P01 deadlock detected); retrying in 4.1s, 2m31s left"
        );
        assert_eq!(
            retrying_line(
                1,
                &Transient::ConnectionLost,
                Duration::from_millis(1_000),
                Left::Attempts(4)
            ),
            "pgmi: attempt 1 failed (connection lost); retrying in 1s, 4 attempts left"
        );
    }

    #[test]
    fn a_used_up_box_counts_the_attempts_and_keeps_the_kind() {
        let last = coded(Kind::Sql, "40001");
        let e = used_up(Kind::Timeout, Duration::from_secs(600), 3, &last);
        assert_eq!(e.exit_code(), 16);
        assert_eq!(
            e.text(),
            "time box of 10m0s used up after 3 attempts; last error: ERROR: x (SQLSTATE 40001)"
        );
        let cut = cut_short(Duration::from_secs(5), 1, coded(Kind::Sql, "40001"));
        assert_eq!(cut.exit_code(), 13);
        assert_eq!(
            cut.sql_diagnostics().and_then(|d| d.sqlstate.as_deref()),
            Some("40001")
        );
        assert!(
            cut.text()
                .starts_with("time box of 5s used up after 1 attempt; "),
            "{}",
            cut.text()
        );
        let unboxed = gave_up(5, Error::connection("connect to \"d\": refused"));
        assert_eq!(unboxed.exit_code(), 11);
        assert_eq!(
            unboxed.text(),
            "gave up after 5 attempts with no time box (--timeout 0); last error: connect to \"d\": refused"
        );
    }
}
