//! Blocking work on tokio's pool that keeps its caller's place until the
//! work really ends, not only until the caller stops waiting for it.

use std::future::Future;
use std::sync::Arc;

use tokio::sync::OwnedSemaphorePermit;
use tokio::task::JoinHandle;

/// One of a bounded number of places for concurrent work (serve's
/// `MAX_CALLS`); freed when the last clone is dropped.
#[derive(Clone)]
pub(crate) struct Slot {
    _permit: Arc<OwnedSemaphorePermit>,
}

impl From<OwnedSemaphorePermit> for Slot {
    fn from(permit: OwnedSemaphorePermit) -> Self {
        Slot {
            _permit: Arc::new(permit),
        }
    }
}

tokio::task_local! {
    static HELD: Slot;
}

impl Slot {
    /// Runs `work` holding this slot. Blocking work it starts through
    /// [`spawn`] holds the slot too, until that work returns.
    pub(crate) async fn hold<F: Future>(self, work: F) -> F::Output {
        HELD.scope(self, work).await
    }
}

/// Runs `work` on the blocking pool. Inside [`Slot::hold`] the thread keeps
/// the slot until `work` returns, also when the caller stops waiting.
pub(crate) fn spawn<T: Send + 'static>(work: impl FnOnce() -> T + Send + 'static) -> JoinHandle<T> {
    let held = HELD.try_with(Slot::clone).ok();
    #[expect(
        clippy::disallowed_methods,
        reason = "the one place blocking work starts"
    )]
    tokio::task::spawn_blocking(move || {
        let _held = held;
        work()
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;
    use tokio::sync::Semaphore;

    #[tokio::test]
    async fn blocking_work_keeps_the_slot_after_its_caller_stops_waiting() {
        let places = Arc::new(Semaphore::new(1));
        let slot = Slot::from(places.clone().try_acquire_owned().unwrap());
        let (release, stalled) = std::sync::mpsc::channel::<()>();
        let caller = tokio::spawn(slot.hold(async move {
            spawn(move || {
                let _ = stalled.recv_timeout(Duration::from_secs(30));
            })
            .await
        }));
        tokio::time::sleep(Duration::from_millis(50)).await;
        caller.abort();
        let _ = caller.await;
        assert_eq!(places.available_permits(), 0);
        drop(release);
        tokio::time::timeout(Duration::from_secs(5), places.acquire())
            .await
            .expect("the slot was not freed when the work ended")
            .unwrap()
            .forget();
    }

    #[tokio::test]
    async fn work_outside_a_slot_holds_nothing() {
        assert_eq!(spawn(|| 7).await.unwrap(), 7);
    }
}
