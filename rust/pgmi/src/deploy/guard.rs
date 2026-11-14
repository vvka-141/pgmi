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
        self.expired(Kind::Timeout)
    }

    fn expired(&self, kind: Kind) -> Error {
        Error::new(
            kind,
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
            () = tokio::time::sleep_until(self.deadline) => Err(self.expired(Kind::Connection)),
        }
    }

    /// Runs `fut` on `session` until it ends, the deploy is stopped or the
    /// deadline passes; once stopped, `fut` is not started. On a stop it
    /// sends PostgreSQL's cancel request and gives `fut` 2 s from the stop
    /// to finish: work that still completes (a COMMIT the cancel came too
    /// late for) keeps its result, and the next step reports the stop. A
    /// session that stays silent is dropped, so cleanup does not wait on it.
    pub(super) async fn running<T>(
        &self,
        session: &Session,
        fut: impl Future<Output = Result<T>>,
    ) -> Result<T> {
        self.check()?;
        tokio::pin!(fut);
        let stopped = tokio::select! {
            r = &mut fut => return r,
            () = self.stop.cancelled() => self.stop_error(),
            () = tokio::time::sleep_until(self.deadline) => self.expired_error(),
        };
        let cancel = session.cancel();
        let grace = tokio::time::sleep(Duration::from_secs(2));
        tokio::pin!(cancel, grace);
        let mut cancel_sent = false;
        let reported = loop {
            tokio::select! {
                r = &mut fut => break Some(r),
                () = &mut cancel, if !cancel_sent => cancel_sent = true,
                () = &mut grace => break None,
            }
        };
        if reported.is_none() {
            session.disconnect();
        }
        if let Some(Ok(done)) = reported {
            return Ok(done);
        }
        Err(match reported {
            Some(Err(e)) if e.sql_diagnostics().is_some() => {
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
    let again = reporter.clone();
    tokio::spawn(crate::signals::first_stops_second_exits(
        move || {
            stop.signal();
            reporter.line(INTERRUPTED);
        },
        move || again.line(INTERRUPTED_AGAIN),
    ));
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn a_statement_that_completes_after_the_deadline_keeps_its_result() {
        use crate::conn::dial::fake;
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let (listener, port) = fake::listen().await;
        let server = tokio::spawn(async move {
            let mut s = fake::signed_in(&listener).await.unwrap();
            assert_eq!(s.read_u8().await.unwrap(), b'Q');
            let len = s.read_u32().await.unwrap();
            let mut body = vec![0; usize::try_from(len).unwrap() - 4];
            s.read_exact(&mut body).await.unwrap();
            tokio::time::sleep(Duration::from_millis(400)).await;
            s.write_all(b"C\0\0\0\x0bCOMMIT\0Z\0\0\0\x05I")
                .await
                .unwrap();
            let _ = listener.accept().await;
            s
        });
        let spec = fake::spec_for(&format!(
            "host=127.0.0.1 port={port} user=u dbname=d sslmode=disable"
        ));
        let session = fake::quiet().connect(&spec, "d").await.unwrap();
        let guard = Guard {
            stop: Stop::default(),
            deadline: TokioInstant::now() + Duration::from_millis(100),
            timeout: Duration::from_millis(100),
        };
        let committed = guard
            .running(&session, async {
                session
                    .client
                    .batch_execute("COMMIT")
                    .await
                    .map_err(|e| crate::deploy::sql_error("execute deploy.sql", e))
            })
            .await;
        assert!(committed.is_ok(), "{:?}", committed.err().map(|e| e.text()));
        let next = guard.running(&session, async { Ok(()) }).await;
        assert_eq!(next.unwrap_err().exit_code(), 16);
        drop(server);
    }

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
