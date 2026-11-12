use std::sync::{Arc, OnceLock};

use tokio_util::sync::{CancellationToken, WaitForCancellationFuture};

/// Why a deploy was stopped.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum StopReason {
    /// SIGINT or SIGTERM.
    Signal,
    /// Its caller cancelled it (serve's `notifications/cancelled`, the end
    /// of a session).
    Cancelled,
}

/// A deploy's cancellation and the reason it was stopped, set by whoever
/// stops it. Clones share both.
#[derive(Clone, Default)]
pub(crate) struct Stop {
    token: CancellationToken,
    reason: Arc<OnceLock<StopReason>>,
}

impl Stop {
    /// Stops when `token` is cancelled; a stop that came only through
    /// `token` reads as `Cancelled`.
    pub(crate) fn on(token: CancellationToken) -> Self {
        Stop {
            token,
            reason: Arc::default(),
        }
    }

    pub(crate) fn signal(&self) {
        self.stop(StopReason::Signal);
    }

    #[cfg(test)]
    pub(crate) fn cancel(&self) {
        self.stop(StopReason::Cancelled);
    }

    fn stop(&self, reason: StopReason) {
        let _ = self.reason.set(reason);
        self.token.cancel();
    }

    pub(crate) fn token(&self) -> &CancellationToken {
        &self.token
    }

    pub(crate) fn cancelled(&self) -> WaitForCancellationFuture<'_> {
        self.token.cancelled()
    }

    pub(crate) fn is_cancelled(&self) -> bool {
        self.token.is_cancelled()
    }

    /// Why the deploy was stopped; `None` while it runs.
    pub(crate) fn reason(&self) -> Option<StopReason> {
        self.token
            .is_cancelled()
            .then(|| self.reason.get().copied().unwrap_or(StopReason::Cancelled))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_first_reason_wins_and_a_bare_cancel_reads_as_cancelled() {
        let stop = Stop::default();
        assert_eq!(stop.reason(), None);
        stop.clone().signal();
        stop.cancel();
        assert_eq!(stop.reason(), Some(StopReason::Signal));

        let token = CancellationToken::new();
        let stop = Stop::on(token.clone());
        token.cancel();
        assert!(stop.is_cancelled());
        assert_eq!(stop.reason(), Some(StopReason::Cancelled));
    }
}
