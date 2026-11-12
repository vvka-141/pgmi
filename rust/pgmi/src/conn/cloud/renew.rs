use std::future::Future;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use jiff::{SignedDuration, Timestamp};

use crate::error::{Result, Secret};

const REFRESH_MARGIN: SignedDuration = SignedDuration::from_mins(5);
const RETRY: Duration = Duration::from_secs(30);

/// A sign-in credential (a token by default) and when it expires, if its
/// issuer said.
#[derive(Debug)]
pub(crate) struct Fetched<T = Secret> {
    pub(crate) value: T,
    pub(crate) expires: Option<Timestamp>,
}

/// Where a provider's credentials come from.
pub(crate) trait Mint: Send + Sync {
    type Value: Clone + Send + Sync;
    fn mint(&self) -> impl Future<Output = Result<Fetched<Self::Value>>> + Send;
}

/// A provider's credential for each connection: the last one while it has
/// more than five minutes left, a new one otherwise (always, when its expiry
/// is unknown).
pub(crate) struct Renewing<M: Mint> {
    mint: M,
    current: Mutex<Option<Fetched<M::Value>>>,
    ahead: Mutex<Option<tokio::task::AbortHandle>>,
}

impl<M: Mint> Drop for Renewing<M> {
    fn drop(&mut self) {
        if let Ok(mut ahead) = self.ahead.lock()
            && let Some(task) = ahead.take()
        {
            task.abort();
        }
    }
}

pub(crate) fn fresh(expires: Option<Timestamp>, now: Timestamp) -> bool {
    expires.is_some_and(|e| e.duration_since(now) > REFRESH_MARGIN)
}

impl<M: Mint> Renewing<M> {
    /// Fetches the first token, so a sign-in that fails does so before
    /// anything connects.
    pub(crate) async fn start(mint: M) -> Result<Self> {
        let r = Self::idle(mint);
        r.renew().await?;
        Ok(r)
    }

    fn idle(mint: M) -> Self {
        Renewing {
            mint,
            current: Mutex::new(None),
            ahead: Mutex::new(None),
        }
    }

    /// The last value while it has not expired, even inside the renewal
    /// margin; a new one only after it has (or when its expiry is unknown).
    pub(crate) async fn valid(&self) -> Result<M::Value> {
        let now = Timestamp::now();
        let cached = self.current.lock().ok().and_then(|c| {
            c.as_ref()
                .filter(|c| c.expires.is_some_and(|e| e > now))
                .map(|c| c.value.clone())
        });
        match cached {
            Some(v) => Ok(v),
            None => self.renew().await,
        }
    }

    /// Renews in the background five minutes before the value expires, and
    /// 30 s after a renewal that failed, keeping the last value meanwhile.
    /// Ends when the last owner drops this.
    pub(crate) fn renew_ahead(self: &Arc<Self>)
    where
        M: 'static,
    {
        let owner = Arc::downgrade(self);
        let task = tokio::spawn(async move {
            loop {
                let due = match owner.upgrade() {
                    Some(r) => r.due(Timestamp::now()),
                    None => return,
                };
                let Some(due) = due else { return };
                tokio::time::sleep(due.max(RETRY)).await;
                let Some(r) = owner.upgrade() else { return };
                if r.renew().await.is_err() {
                    drop(r);
                    tokio::time::sleep(RETRY).await;
                }
            }
        });
        if let Ok(mut ahead) = self.ahead.lock() {
            *ahead = Some(task.abort_handle());
        }
    }

    fn due(&self, now: Timestamp) -> Option<Duration> {
        let expires = self.current.lock().ok()?.as_ref()?.expires?;
        let left = (expires - REFRESH_MARGIN).duration_since(now);
        Some(Duration::try_from(left).unwrap_or(Duration::ZERO))
    }

    pub(crate) async fn current(&self) -> Result<M::Value> {
        self.current_at(Timestamp::now()).await
    }

    async fn current_at(&self, now: Timestamp) -> Result<M::Value> {
        let cached = self.current.lock().ok().and_then(|c| {
            c.as_ref()
                .filter(|c| fresh(c.expires, now))
                .map(|c| c.value.clone())
        });
        match cached {
            Some(v) => Ok(v),
            None => self.renew().await,
        }
    }

    async fn renew(&self) -> Result<M::Value> {
        let fetched = self.mint.mint().await?;
        let value = fetched.value.clone();
        if let Ok(mut c) = self.current.lock() {
            *c = Some(fetched);
        }
        Ok(value)
    }

    #[cfg(test)]
    pub(crate) fn mint_source(&self) -> &M {
        &self.mint
    }

    #[cfg(test)]
    pub(crate) fn holding(mint: M, fetched: Fetched<M::Value>) -> Self {
        let r = Self::idle(mint);
        if let Ok(mut c) = r.current.lock() {
            *c = Some(fetched);
        }
        r
    }
}

impl<M: Mint + std::fmt::Debug> std::fmt::Debug for Renewing<M> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        self.mint.fmt(f)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    struct Counting {
        calls: AtomicUsize,
        lifetime: Option<SignedDuration>,
        now: Timestamp,
    }

    impl Mint for Counting {
        type Value = Secret;
        fn mint(&self) -> impl Future<Output = Result<Fetched>> + Send {
            let n = self.calls.fetch_add(1, Ordering::SeqCst) + 1;
            std::future::ready(Ok(Fetched {
                value: Secret::from(format!("token-{n}")),
                expires: self.lifetime.map(|l| self.now + l),
            }))
        }
    }

    fn at(s: &str) -> Timestamp {
        s.parse().unwrap()
    }

    #[test]
    fn a_token_is_reused_until_five_minutes_before_it_expires() {
        let now = at("2026-11-11T10:00:00Z");
        let some = |s: &str| Some(at(s));
        assert!(fresh(some("2026-11-11T11:00:00Z"), now));
        assert!(fresh(some("2026-11-11T10:05:01Z"), now));
        assert!(!fresh(some("2026-11-11T10:05:00Z"), now));
        assert!(!fresh(some("2026-11-11T09:59:00Z"), now));
        assert!(!fresh(None, now));
    }

    #[tokio::test]
    async fn a_connection_after_the_token_expired_gets_a_new_one() {
        let now = at("2026-11-11T10:00:00Z");
        let r = Renewing::start(Counting {
            calls: AtomicUsize::new(0),
            lifetime: Some(SignedDuration::from_hours(1)),
            now,
        })
        .await
        .unwrap();
        assert_eq!(r.current_at(now).await.unwrap().expose(), "token-1");
        assert_eq!(
            r.current_at(at("2026-11-11T10:50:00Z"))
                .await
                .unwrap()
                .expose(),
            "token-1"
        );
        assert_eq!(
            r.current_at(at("2026-11-11T11:30:00Z"))
                .await
                .unwrap()
                .expose(),
            "token-2"
        );
    }

    #[tokio::test(start_paused = true)]
    async fn the_value_is_renewed_in_the_background_before_it_expires() {
        let now = Timestamp::now();
        let r = Arc::new(Renewing::holding(
            Counting {
                calls: AtomicUsize::new(0),
                lifetime: Some(SignedDuration::from_hours(1)),
                now,
            },
            Fetched {
                value: Secret::from("token-0"),
                expires: Some(now + SignedDuration::from_mins(6)),
            },
        ));
        r.renew_ahead();
        tokio::time::sleep(Duration::from_secs(30)).await;
        assert_eq!(r.mint_source().calls.load(Ordering::SeqCst), 0);
        tokio::time::sleep(Duration::from_secs(60)).await;
        assert_eq!(r.mint_source().calls.load(Ordering::SeqCst), 1);
        assert_eq!(r.current().await.unwrap().expose(), "token-1");
        assert_eq!(r.mint_source().calls.load(Ordering::SeqCst), 1);
    }

    #[tokio::test(start_paused = true)]
    async fn background_renewal_ends_with_its_owner() {
        let now = Timestamp::now();
        let r = Arc::new(Renewing::holding(
            Counting {
                calls: AtomicUsize::new(0),
                lifetime: Some(SignedDuration::from_hours(1)),
                now,
            },
            Fetched {
                value: Secret::from("token-0"),
                expires: Some(now + SignedDuration::from_mins(6)),
            },
        ));
        r.renew_ahead();
        let task = r.ahead.lock().unwrap().clone().unwrap();
        drop(r);
        tokio::task::yield_now().await;
        assert!(task.is_finished());
    }

    #[tokio::test]
    async fn a_token_without_an_expiry_is_fetched_for_every_connection() {
        let now = at("2026-11-11T10:00:00Z");
        let r = Renewing::start(Counting {
            calls: AtomicUsize::new(0),
            lifetime: None,
            now,
        })
        .await
        .unwrap();
        assert_eq!(r.current_at(now).await.unwrap().expose(), "token-2");
        assert_eq!(r.current_at(now).await.unwrap().expose(), "token-3");
        assert_eq!(r.mint_source().calls.load(Ordering::SeqCst), 3);
    }
}
