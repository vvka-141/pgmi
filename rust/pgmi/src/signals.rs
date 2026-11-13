//! SIGINT and SIGTERM (Ctrl-C on Windows) for the commands that run until
//! stopped: the first stops the work, the second exits 130 at once.

use crate::error::Kind;

/// Runs `first` at the first SIGINT, SIGTERM or SIGHUP, and `second` at the
/// next, then exits the process with 130. Returns without exiting only when the
/// signals cannot be watched.
pub(crate) async fn first_stops_second_exits(first: impl FnOnce(), second: impl FnOnce()) {
    #[cfg(unix)]
    {
        use tokio::signal::unix::{SignalKind, signal};
        let (Ok(mut int), Ok(mut term), Ok(mut hup)) = (
            signal(SignalKind::interrupt()),
            signal(SignalKind::terminate()),
            signal(SignalKind::hangup()),
        ) else {
            return;
        };
        tokio::select! {
            _ = int.recv() => {}
            _ = term.recv() => {}
            _ = hup.recv() => {}
        }
        first();
        tokio::select! {
            _ = int.recv() => {}
            _ = term.recv() => {}
            _ = hup.recv() => {}
        }
    }
    #[cfg(not(unix))]
    {
        if tokio::signal::ctrl_c().await.is_err() {
            return;
        }
        first();
        if tokio::signal::ctrl_c().await.is_err() {
            return;
        }
    }
    second();
    std::process::exit(i32::from(Kind::Interrupted.exit_code()));
}
