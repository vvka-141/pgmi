use std::future::Future;
use std::sync::Mutex;

use jiff::{SignedDuration, Timestamp};

use crate::error::{Result, Secret};

const REFRESH_MARGIN: SignedDuration = SignedDuration::from_mins(5);

/// A sign-in token and when it expires, if its issuer said.
pub(crate) struct Fetched {
    pub(crate) token: Secret,
    pub(crate) expires: Option<Timestamp>,
}

/// Where a provider's tokens come from.
pub(crate) trait Mint: Send + Sync {
    fn mint(&self) -> impl Future<Output = Result<Fetched>> + Send;
}

/// A provider's token for each connection: the last one while it has more
/// than five minutes left, a new one otherwise (always, when its expiry is
/// unknown). Every token handed out is kept for redaction.
pub(crate) struct Renewing<M> {
    mint: M,
    current: Mutex<Option<Fetched>>,
    minted: Mutex<Vec<String>>,
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
            minted: Mutex::default(),
        }
    }

    pub(crate) async fn password(&self) -> Result<String> {
        self.password_at(Timestamp::now()).await
    }

    async fn password_at(&self, now: Timestamp) -> Result<String> {
        let cached = self.current.lock().ok().and_then(|c| {
            c.as_ref()
                .filter(|c| fresh(c.expires, now))
                .map(|c| c.token.expose().to_string())
        });
        match cached {
            Some(t) => Ok(t),
            None => self.renew().await,
        }
    }

    async fn renew(&self) -> Result<String> {
        let fetched = self.mint.mint().await?;
        let token = fetched.token.expose().to_string();
        if let Ok(mut m) = self.minted.lock() {
            m.push(token.clone());
        }
        if let Ok(mut c) = self.current.lock() {
            *c = Some(fetched);
        }
        Ok(token)
    }

    pub(crate) fn minted(&self) -> Vec<String> {
        self.minted.lock().map(|m| m.clone()).unwrap_or_default()
    }

    #[cfg(test)]
    pub(crate) fn mint_source(&self) -> &M {
        &self.mint
    }

    #[cfg(test)]
    pub(crate) fn holding(mint: M, fetched: Fetched) -> Self {
        let r = Self::idle(mint);
        if let Ok(mut m) = r.minted.lock() {
            m.push(fetched.token.expose().to_string());
        }
        if let Ok(mut c) = r.current.lock() {
            *c = Some(fetched);
        }
        r
    }
}

impl<M: std::fmt::Debug> std::fmt::Debug for Renewing<M> {
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
        fn mint(&self) -> impl Future<Output = Result<Fetched>> + Send {
            let n = self.calls.fetch_add(1, Ordering::SeqCst) + 1;
            std::future::ready(Ok(Fetched {
                token: Secret::from(format!("token-{n}")),
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
        assert_eq!(r.password_at(now).await.unwrap(), "token-1");
        assert_eq!(
            r.password_at(at("2026-11-11T10:50:00Z")).await.unwrap(),
            "token-1"
        );
        assert_eq!(
            r.password_at(at("2026-11-11T11:30:00Z")).await.unwrap(),
            "token-2"
        );
        assert_eq!(r.minted(), ["token-1", "token-2"]);
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
        assert_eq!(r.password_at(now).await.unwrap(), "token-2");
        assert_eq!(r.password_at(now).await.unwrap(), "token-3");
        assert_eq!(r.mint_source().calls.load(Ordering::SeqCst), 3);
    }
}
