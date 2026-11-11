use std::io;
use std::time::Duration;

/// What a failed connection attempt reports, as far as retrying cares.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Cause<'a> {
    Sqlstate(&'a str),
    Io(io::ErrorKind),
}

/// Whether trying again may succeed: SQLSTATE class 08, 53300 and 57P03,
/// and a socket refused, reset, aborted, unreachable or timed out.
pub(crate) fn transient(cause: Cause<'_>) -> bool {
    match cause {
        Cause::Sqlstate(code) => code.starts_with("08") || code == "53300" || code == "57P03",
        Cause::Io(kind) => matches!(
            kind,
            io::ErrorKind::ConnectionRefused
                | io::ErrorKind::ConnectionReset
                | io::ErrorKind::ConnectionAborted
                | io::ErrorKind::TimedOut
                | io::ErrorKind::HostUnreachable
                | io::ErrorKind::NetworkUnreachable
        ),
    }
}

const DELAYS: [Duration; 3] = [
    Duration::from_millis(100),
    Duration::from_millis(200),
    Duration::from_millis(400),
];

/// The wait after attempt `attempt` (from 1) failed transiently, or `None`
/// when it was the last.
pub(crate) fn delay(attempt: usize) -> Option<Duration> {
    DELAYS.get(attempt.checked_sub(1)?).copied()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn server_errors_retried_are_class_08_too_many_connections_and_starting_up() {
        for code in [
            "08000", "08001", "08003", "08004", "08006", "08P01", "53300", "57P03",
        ] {
            assert!(transient(Cause::Sqlstate(code)), "{code}");
        }
        for code in [
            "28000", "28P01", "3D000", "40001", "40P01", "55P03", "53200", "57014", "57P01",
            "57P04", "42601", "42P01", "23505", "23503", "42501", "XX000", "",
        ] {
            assert!(!transient(Cause::Sqlstate(code)), "{code}");
        }
    }

    #[test]
    fn socket_errors_retried_are_refused_reset_unreachable_and_timeout() {
        use io::ErrorKind::*;
        for kind in [
            ConnectionRefused,
            ConnectionReset,
            ConnectionAborted,
            TimedOut,
            HostUnreachable,
            NetworkUnreachable,
        ] {
            assert!(transient(Cause::Io(kind)), "{kind:?}");
        }
        for kind in [
            NotFound,
            PermissionDenied,
            InvalidInput,
            InvalidData,
            AddrNotAvailable,
            Unsupported,
            Other,
        ] {
            assert!(!transient(Cause::Io(kind)), "{kind:?}");
        }
    }

    #[test]
    fn four_attempts_waiting_100_200_400_ms() {
        let waits: Vec<_> = (1..).map_while(delay).collect();
        assert_eq!(
            waits,
            [100, 200, 400].map(Duration::from_millis),
            "a wait after attempts 1 to 3, none after the fourth"
        );
        assert_eq!(delay(0), None);
    }
}
