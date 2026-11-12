//! The deploy's guard: `--timeout`, signals and cancellation around every
//! step that waits.

use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use tokio::time::Instant as TokioInstant;

use super::{Stop, StopReason};
use crate::conn::dial::Session;
use crate::error::{Error, Kind, Result};
use crate::output::Reporter;

pub(super) struct Guard {
    pub(super) stop: Stop,
    pub(super) deadline: TokioInstant,
    pub(super) timeout: Duration,
}

fn stopped(stop: &Stop) -> Error {
    match stop.reason() {
        Some(StopReason::Signal) => Error::new(Kind::Interrupted, "interrupted"),
        _ => Error::new(Kind::Interrupted, "the deploy was cancelled"),
    }
}

/// Runs blocking `work` that comes before the guard exists (it reads what
/// the guard is built from) on the blocking pool; a signal or a cancel stops
/// waiting for it.
pub(super) async fn before_guard<T: Send + 'static>(
    stop: &Stop,
    work: impl FnOnce() -> Result<T> + Send + 'static,
) -> Result<T> {
    tokio::select! {
        biased;
        () = stop.cancelled() => Err(stopped(stop)),
        r = crate::blocking::spawn(work) => {
            r.map_err(|e| Error::internal("blocking task").with_source(e))?
        }
    }
}

impl Guard {
    pub(super) fn stop_error(&self) -> Error {
        stopped(&self.stop)
    }

    pub(super) fn expired_error(&self) -> Error {
        Error::new(
            Kind::Timeout,
            format!(
                "the deploy exceeded --timeout {}; raise it, or pass --timeout 0 for no limit",
                crate::duration::render(self.timeout)
            ),
        )
    }

    pub(super) async fn waiting<T>(&self, fut: impl Future<Output = Result<T>>) -> Result<T> {
        tokio::select! {
            r = fut => r,
            () = self.stop.cancelled() => Err(self.stop_error()),
            () = tokio::time::sleep_until(self.deadline) => Err(self.expired_error()),
        }
    }

    pub(super) async fn blocking<T: Send + 'static>(
        &self,
        work: impl FnOnce() -> Result<T> + Send + 'static,
    ) -> Result<T> {
        self.waiting(async {
            crate::blocking::spawn(work)
                .await
                .map_err(|e| Error::internal("blocking task").with_source(e))?
        })
        .await
    }

    /// Like `blocking`, but only a signal or a cancel stops it: `--timeout`
    /// does not bound reading `--params-file -`, in Go either.
    pub(super) async fn interruptible<T: Send + 'static>(
        &self,
        work: impl FnOnce() -> Result<T> + Send + 'static,
    ) -> Result<T> {
        tokio::select! {
            r = crate::blocking::spawn(work) => {
                r.map_err(|e| Error::internal("blocking task").with_source(e))?
            }
            () = self.stop.cancelled() => Err(self.stop_error()),
        }
    }

    pub(super) fn check(&self) -> Result<()> {
        if self.stop.is_cancelled() {
            Err(self.stop_error())
        } else if TokioInstant::now() >= self.deadline {
            Err(self.expired_error())
        } else {
            Ok(())
        }
    }

    pub(super) async fn connecting<T>(&self, fut: impl Future<Output = Result<T>>) -> Result<T> {
        tokio::select! {
            r = fut => r,
            () = self.stop.cancelled() => Err(self.stop_error()),
            () = tokio::time::sleep_until(self.deadline) => Err(Error::connection("--timeout ran out while connecting")),
        }
    }

    /// Runs `fut` on `session` until it ends, the deploy is stopped or the
    /// deadline passes. On a stop it sends PostgreSQL's cancel request,
    /// then gives `fut` 2 s to finish before it returns the stop's error.
    pub(super) async fn running<T>(
        &self,
        session: &Session,
        fut: impl Future<Output = Result<T>>,
    ) -> Result<T> {
        tokio::pin!(fut);
        let stopped = tokio::select! {
            r = &mut fut => return r,
            () = self.stop.cancelled() => self.stop_error(),
            () = tokio::time::sleep_until(self.deadline) => self.expired_error(),
        };
        session.cancel().await;
        let reported = tokio::time::timeout(Duration::from_secs(2), &mut fut).await;
        Err(match reported {
            Ok(Err(e)) if e.sql_diagnostics().is_some() => {
                let text = match stopped.kind() {
                    Kind::Timeout => stopped.text(),
                    _ => e.text(),
                };
                e.reworded(stopped.kind(), text)
            }
            _ => stopped,
        })
    }
}

pub(super) fn deadline(timeout: Duration) -> TokioInstant {
    let now = TokioInstant::now();
    now.checked_add(timeout)
        .or_else(|| now.checked_add(Duration::from_hours(876_000)))
        .unwrap_or(now)
}

pub(super) fn limit(timeout: Option<Duration>) -> Duration {
    timeout.map_or(Duration::from_secs(180), |t| {
        if t.is_zero() {
            Duration::from_hours(876_000)
        } else {
            t
        }
    })
}

const INTERRUPTED: &str = "pgmi: interrupted, cancelling deployment... (again to quit at once)";
const INTERRUPTED_AGAIN: &str = "pgmi: interrupted again, quitting";

pub(super) fn spawn_signals(stop: Stop, reporter: Arc<Reporter>) {
    tokio::spawn(async move {
        #[cfg(unix)]
        {
            use tokio::signal::unix::{SignalKind, signal};
            let (Ok(mut int), Ok(mut term)) = (
                signal(SignalKind::interrupt()),
                signal(SignalKind::terminate()),
            ) else {
                return;
            };
            tokio::select! {
                _ = int.recv() => {}
                _ = term.recv() => {}
            }
            reporter.line(INTERRUPTED);
            stop.signal();
            tokio::select! {
                _ = int.recv() => {}
                _ = term.recv() => {}
            }
            reporter.line(INTERRUPTED_AGAIN);
            std::process::exit(i32::from(Kind::Interrupted.exit_code()));
        }
        #[cfg(not(unix))]
        {
            if tokio::signal::ctrl_c().await.is_ok() {
                reporter.line(INTERRUPTED);
                stop.signal();
            }
            if tokio::signal::ctrl_c().await.is_ok() {
                reporter.line(INTERRUPTED_AGAIN);
                std::process::exit(i32::from(Kind::Interrupted.exit_code()));
            }
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn a_stop_ends_the_wait_for_work_before_the_guard() {
        let stop = Stop::default();
        let (release, stalled) = std::sync::mpsc::channel::<()>();
        let waiting = tokio::spawn({
            let stop = stop.clone();
            async move {
                before_guard(&stop, move || {
                    let _ = stalled.recv_timeout(Duration::from_secs(30));
                    Ok(())
                })
                .await
            }
        });
        tokio::time::sleep(Duration::from_millis(50)).await;
        stop.signal();
        let e = tokio::time::timeout(Duration::from_secs(2), waiting)
            .await
            .expect("the stop waited for the blocking read")
            .unwrap()
            .unwrap_err();
        assert_eq!(e.exit_code(), 130);
        assert_eq!(e.text(), "interrupted");
        drop(release);
    }
}
