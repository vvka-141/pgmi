use std::future::Future;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use jiff::{SignedDuration, Timestamp};

use crate::conn::dial::LogSink;
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
/// is unknown). One renewal runs at a time; a caller that waited for another
/// takes its result when it is good enough.
pub(crate) struct Renewing<M: Mint> {
    mint: M,
    current: Mutex<Option<Fetched<M::Value>>>,
    minting: tokio::sync::Mutex<()>,
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
        r.renew(|_| false).await?;
        Ok(r)
    }

    fn idle(mint: M) -> Self {
        Renewing {
            mint,
            current: Mutex::new(None),
            minting: tokio::sync::Mutex::new(()),
            ahead: Mutex::new(None),
        }
    }

    /// The last value while it has not expired, even inside the renewal
    /// margin; a new one only after it has (or when its expiry is unknown).
    pub(crate) async fn valid(&self) -> Result<M::Value> {
        let unexpired = |c: &Fetched<M::Value>| c.expires.is_some_and(|e| e > Timestamp::now());
        match self.cached(unexpired) {
            Some(v) => Ok(v),
            None => self.renew(unexpired).await,
        }
    }

    /// Renews in the background five minutes before the value expires, and
    /// 30 s after a renewal that failed, keeping the last value meanwhile
    /// and telling `log` why it failed. Ends when the last owner drops this.
    pub(crate) fn renew_ahead(self: &Arc<Self>, log: LogSink)
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
                let Some((wait, seen)) = due else { return };
                tokio::time::sleep(wait.max(RETRY)).await;
                let Some(r) = owner.upgrade() else { return };
                let renewed = r.renew(|c| c.expires.is_some_and(|e| e > seen)).await;
                drop(r);
                if let Err(e) = renewed {
                    log(format!(
                        "renewing the sign-in in the background failed; the current one is kept and the renewal retried: {}",
                        e.text()
                    ));
                    tokio::time::sleep(RETRY).await;
                }
            }
        });
        if let Ok(mut ahead) = self.ahead.lock() {
            *ahead = Some(task.abort_handle());
        }
    }

    /// How long until the value is due for renewal, and when it expires.
    fn due(&self, now: Timestamp) -> Option<(Duration, Timestamp)> {
        let expires = self.current.lock().ok()?.as_ref()?.expires?;
        let left = (expires - REFRESH_MARGIN).duration_since(now);
        Some((Duration::try_from(left).unwrap_or(Duration::ZERO), expires))
    }

    pub(crate) async fn current(&self) -> Result<M::Value> {
        self.current_at(Timestamp::now()).await
    }

    async fn current_at(&self, now: Timestamp) -> Result<M::Value> {
        let usable = |c: &Fetched<M::Value>| fresh(c.expires, now);
        match self.cached(usable) {
            Some(v) => Ok(v),
            None => self.renew(usable).await,
        }
    }

    fn cached(&self, usable: impl Fn(&Fetched<M::Value>) -> bool) -> Option<M::Value> {
        let current = self.current.lock().ok()?;
        current
            .as_ref()
            .filter(|c| usable(c))
            .map(|c| c.value.clone())
    }

    /// Mints a new value, one renewal at a time: after waiting for another,
    /// the value it left is taken when `usable` accepts it.
    async fn renew(&self, usable: impl Fn(&Fetched<M::Value>) -> bool) -> Result<M::Value> {
        let _turn = self.minting.lock().await;
        if let Some(v) = self.cached(usable) {
            return Ok(v);
        }
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
        r.renew_ahead(Arc::new(|_| {}));
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
        r.renew_ahead(Arc::new(|_| {}));
        let task = r.ahead.lock().unwrap().clone().unwrap();
        drop(r);
        tokio::task::yield_now().await;
        assert!(task.is_finished());
    }

    /// Takes a second per sign-in; `fail` makes every one fail.
    #[derive(Default)]
    struct Slow {
        calls: AtomicUsize,
        fail: bool,
    }

    impl Mint for Slow {
        type Value = Secret;
        fn mint(&self) -> impl Future<Output = Result<Fetched>> + Send {
            let n = self.calls.fetch_add(1, Ordering::SeqCst) + 1;
            let fail = self.fail;
            async move {
                tokio::time::sleep(Duration::from_secs(1)).await;
                if fail {
                    return Err(crate::error::Error::connection(
                        "the token endpoint is down",
                    ));
                }
                Ok(Fetched {
                    value: Secret::from(format!("token-{n}")),
                    expires: Some(Timestamp::now() + SignedDuration::from_hours(1)),
                })
            }
        }
    }

    fn slow_holding(mint: Slow, left: SignedDuration) -> Arc<Renewing<Slow>> {
        Arc::new(Renewing::holding(
            mint,
            Fetched {
                value: Secret::from("token-0"),
                expires: Some(Timestamp::now() + left),
            },
        ))
    }

    #[tokio::test(start_paused = true)]
    async fn connections_that_need_a_renewal_at_once_share_one_sign_in() {
        let r = slow_holding(Slow::default(), SignedDuration::from_mins(1));
        let (a, b) = tokio::join!(r.current(), r.current());
        assert_eq!(a.unwrap().expose(), "token-1");
        assert_eq!(b.unwrap().expose(), "token-1");
        assert_eq!(r.mint_source().calls.load(Ordering::SeqCst), 1);
    }

    #[tokio::test(start_paused = true)]
    async fn a_connection_during_a_background_renewal_takes_its_result() {
        let r = slow_holding(
            Slow::default(),
            SignedDuration::from_mins(5) + SignedDuration::from_secs(30),
        );
        r.renew_ahead(Arc::new(|_| {}));
        tokio::time::sleep(Duration::from_millis(30_500)).await;
        assert_eq!(r.mint_source().calls.load(Ordering::SeqCst), 1);
        let later = Timestamp::now() + SignedDuration::from_mins(10);
        assert_eq!(r.current_at(later).await.unwrap().expose(), "token-1");
        assert_eq!(r.mint_source().calls.load(Ordering::SeqCst), 1);
    }

    #[tokio::test(start_paused = true)]
    async fn a_failed_background_renewal_is_told_and_the_value_kept() {
        let r = slow_holding(
            Slow {
                fail: true,
                ..Slow::default()
            },
            SignedDuration::from_mins(5) + SignedDuration::from_secs(30),
        );
        let lines = Arc::new(Mutex::new(Vec::new()));
        let told = lines.clone();
        r.renew_ahead(Arc::new(move |line| told.lock().unwrap().push(line)));
        tokio::time::sleep(Duration::from_secs(32)).await;
        assert_eq!(
            *lines.lock().unwrap(),
            [
                "renewing the sign-in in the background failed; the current one is kept and the renewal retried: the token endpoint is down"
            ]
        );
        assert_eq!(r.valid().await.unwrap().expose(), "token-0");
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
