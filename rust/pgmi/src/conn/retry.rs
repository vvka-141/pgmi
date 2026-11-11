use std::io;
use std::time::Duration;

/// What a failed connection attempt reports, as far as retrying cares.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Cause<'a> {
    Sqlstate(&'a str),
    Io(io::ErrorKind),
}

/// Whether trying again may succeed: SQLSTATE class 08, 53300 and 57P03,
/// and a socket refused, reset, aborted, broken, closed early, unreachable or
/// timed out.
pub(crate) fn transient(cause: Cause<'_>) -> bool {
    match cause {
        Cause::Sqlstate(code) => code.starts_with("08") || code == "53300" || code == "57P03",
        Cause::Io(kind) => matches!(
            kind,
            io::ErrorKind::ConnectionRefused
                | io::ErrorKind::ConnectionReset
                | io::ErrorKind::ConnectionAborted
                | io::ErrorKind::BrokenPipe
                | io::ErrorKind::UnexpectedEof
                | io::ErrorKind::TimedOut
                | io::ErrorKind::HostUnreachable
                | io::ErrorKind::NetworkUnreachable
        ),
    }
}

/// Whether a failed host-name lookup may succeed when tried again: the
/// resolver answered "try again" (`EAI_AGAIN`; `WSATRY_AGAIN` on Windows) or
/// timed out. A name that does not exist is final.
pub(crate) fn transient_lookup(e: &io::Error) -> bool {
    const WSATRY_AGAIN: i32 = 11002;
    if e.kind() == io::ErrorKind::TimedOut
        || (cfg!(windows) && e.raw_os_error() == Some(WSATRY_AGAIN))
    {
        return true;
    }
    let text = e.to_string().to_ascii_lowercase();
    ["temporary failure in name resolution", "try again"]
        .iter()
        .any(|m| text.contains(m))
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
            BrokenPipe,
            UnexpectedEof,
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
    fn a_temporary_lookup_failure_is_retried_and_an_unknown_name_is_not() {
        let lookup = |detail: &str| {
            io::Error::other(format!("failed to lookup address information: {detail}"))
        };
        for detail in ["Temporary failure in name resolution", "Try again"] {
            assert!(transient_lookup(&lookup(detail)), "{detail}");
        }
        assert!(transient_lookup(&io::Error::from(io::ErrorKind::TimedOut)));
        for detail in [
            "Name or service not known",
            "Name does not resolve",
            "nodename nor servname provided, or not known",
            "No address associated with hostname",
        ] {
            assert!(!transient_lookup(&lookup(detail)), "{detail}");
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
