use std::sync::{Arc, Mutex};

use crate::conn::resolve::Spec;
use crate::error::{Secret, redact};
use crate::output::Notice;

/// The secrets of a deploy, each added where it becomes known: the
/// connection's passwords once it is resolved, and every password or
/// sign-in token the dialer presents, renewed tokens included. Shared with
/// the dialer's callbacks, so an append-only set behind a short lock.
#[derive(Clone, Default)]
pub(super) struct Secrets(Arc<Mutex<Vec<Secret>>>);

impl Secrets {
    pub(super) fn insert(&self, secret: &Secret) {
        let mut s = self
            .0
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        if !s.contains(secret) {
            s.push(secret.clone());
        }
    }

    pub(super) fn resolved(&self, spec: &Spec) {
        for p in [&spec.password, &spec.sslpassword].into_iter().flatten() {
            self.insert(p);
        }
    }

    pub(super) fn list(&self) -> Vec<Secret> {
        self.0
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .clone()
    }

    pub(super) fn notice(&self, mut n: Notice) -> Notice {
        let secrets = self.list();
        n.message = redact(&n.message, &secrets);
        n.detail = n.detail.map(|d| redact(&d, &secrets));
        n.hint = n.hint.map(|h| redact(&h, &secrets));
        n
    }
}
