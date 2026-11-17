use std::sync::{Arc, Mutex};

use crate::conn::resolve::Spec;
use crate::error::{Secret, redact};
use crate::output::Notice;

/// The secrets of a deploy, each added where it becomes known: the
/// connection's passwords once it is resolved, and every password or
/// sign-in token the dialer presents, renewed tokens included. Shared with
/// the dialer's callbacks, so an append-only set behind a short lock.
#[derive(Clone, Default)]
pub(super) struct Secrets(Arc<Mutex<Known>>);

#[derive(Default)]
struct Known {
    secrets: Vec<Secret>,
    names: Vec<String>,
}

impl Secrets {
    fn known(&self) -> std::sync::MutexGuard<'_, Known> {
        self.0
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
    }

    pub(super) fn insert(&self, secret: &Secret) {
        let mut k = self.known();
        if !k.secrets.contains(secret) {
            k.secrets.push(secret.clone());
        }
    }

    pub(super) fn resolved(&self, spec: &Spec) {
        for p in [&spec.password, &spec.sslpassword].into_iter().flatten() {
            self.insert(p);
        }
        let mut k = self.known();
        for name in [&spec.user, &spec.target_db, &spec.maintenance_db] {
            if !k.names.contains(name) {
                k.names.push(name.clone());
            }
        }
    }

    /// The secrets to hide; one equal to the login or a database name hides
    /// nothing, since pgmi prints those names anyway.
    pub(super) fn list(&self) -> Vec<Secret> {
        let k = self.known();
        k.secrets
            .iter()
            .filter(|s| !k.names.iter().any(|n| n == s.expose()))
            .cloned()
            .collect()
    }

    pub(super) fn notice(&self, mut n: Notice) -> Notice {
        let secrets = self.list();
        n.message = redact(&n.message, &secrets);
        n.detail = n.detail.map(|d| redact(&d, &secrets));
        n.hint = n.hint.map(|h| redact(&h, &secrets));
        n
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::conn::resolve::{Flags, resolve};
    use crate::env::Env;

    fn spec(conn: &str) -> Spec {
        let flags = Flags {
            connection: Some(Secret::from(conn.to_string())),
            ..Flags::default()
        };
        resolve(&flags, &Env::default(), None).unwrap()
    }

    #[test]
    fn a_password_equal_to_the_login_or_a_database_is_not_hidden() {
        let secrets = Secrets::default();
        secrets.resolved(&spec("postgresql://postgres:postgres@h/app"));
        assert!(secrets.list().is_empty());
        secrets.insert(&Secret::from("app".to_string()));
        assert!(secrets.list().is_empty());
        secrets.insert(&Secret::from("s3cret".to_string()));
        assert_eq!(
            redact("postgres reads app with s3cret", &secrets.list()),
            "postgres reads app with ***"
        );
    }

    #[test]
    fn a_notice_is_redacted_in_its_message_detail_and_hint() {
        let secrets = Secrets::default();
        secrets.insert(&Secret::from("s3cret".to_string()));
        let n = secrets.notice(Notice {
            severity: crate::output::Severity::Notice,
            message: "m s3cret".into(),
            detail: Some("d s3cret".into()),
            hint: Some("h s3cret".into()),
        });
        assert_eq!(
            (n.message.as_str(), n.detail.as_deref(), n.hint.as_deref()),
            ("m ***", Some("d ***"), Some("h ***"))
        );
    }
}
