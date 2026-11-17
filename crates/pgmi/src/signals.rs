//! SIGINT, SIGTERM, SIGHUP, SIGQUIT, SIGUSR1 and SIGUSR2 (Ctrl-C on
//! Windows) for the commands that run until stopped: the first stops the
//! work, the second exits 130 at once.

use crate::error::Kind;

/// Runs `first` at the first stopping signal, and `second` at the next, then
/// exits the process with 130. Returns without exiting only when the signals
/// cannot be watched.
pub(crate) async fn first_stops_second_exits(first: impl FnOnce(), second: impl FnOnce()) {
    #[cfg(unix)]
    {
        use tokio::signal::unix::{SignalKind, signal};
        let Ok(mut watched) = [
            SignalKind::interrupt(),
            SignalKind::terminate(),
            SignalKind::hangup(),
            SignalKind::quit(),
            SignalKind::user_defined1(),
            SignalKind::user_defined2(),
        ]
        .into_iter()
        .map(signal)
        .collect::<std::io::Result<Vec<_>>>() else {
            return;
        };
        any(&mut watched).await;
        first();
        any(&mut watched).await;
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
    crate::output::drain();
    std::process::exit(i32::from(Kind::Interrupted.exit_code()));
}

#[cfg(unix)]
async fn any(signals: &mut [tokio::signal::unix::Signal]) {
    std::future::poll_fn(|cx| {
        for s in signals.iter_mut() {
            if s.poll_recv(cx).is_ready() {
                return std::task::Poll::Ready(());
            }
        }
        std::task::Poll::Pending
    })
    .await;
}
