use std::io::{BufRead, IsTerminal};
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use tokio_util::sync::CancellationToken;

use crate::env::Env;
use crate::error::{Error, Kind, Result};
use crate::output::Reporter;

/// Decides whether `--overwrite` may drop the target database.
#[async_trait(?Send)]
pub(crate) trait Approver {
    /// Refuses before anything connects, when the answer is already known.
    fn before_connecting(&self, _db: &str) -> Result<()> {
        Ok(())
    }

    /// Asks right before an existing database is dropped; `token` cancels
    /// the question.
    async fn approve_drop(
        &self,
        db: &str,
        reporter: &Reporter,
        token: &CancellationToken,
    ) -> Result<()>;
}

type Answer = Arc<dyn Fn() -> std::io::Result<Option<String>> + Send + Sync>;

/// The command line: a typed confirmation, or `--force` with a countdown
/// when a person is watching.
pub(crate) struct Terminal {
    force: bool,
    interactive: bool,
    banner: bool,
    answer: Answer,
}

impl Terminal {
    pub(crate) fn new(force: bool, env: &Env) -> Self {
        Terminal {
            force,
            interactive: std::io::stdin().is_terminal()
                && std::io::stderr().is_terminal()
                && env.non_empty("CI").ok().flatten().is_none()
                && env.get("PGMI_NON_INTERACTIVE").ok().flatten() != Some("1"),
            banner: env.non_empty("PGMI_NO_BANNER").ok().flatten().is_none(),
            answer: Arc::new(|| {
                let mut line = String::new();
                let n = std::io::stdin().lock().read_line(&mut line)?;
                Ok((n > 0).then_some(line))
            }),
        }
    }
}

fn interrupted() -> Error {
    Error::new(Kind::Interrupted, "interrupted")
}

fn banner(db: &str) -> String {
    let art = pgmi_shared::file("art/skull.txt")
        .and_then(|b| std::str::from_utf8(b).ok())
        .unwrap_or("")
        .replace("${dbname}", db);
    let lines: Vec<&str> = art.trim_end_matches('\n').lines().collect();
    let width = lines.iter().map(|l| l.chars().count()).max().unwrap_or(0);
    let rule = "═".repeat(width + 4);
    let mut out = format!("╔{rule}╗\n");
    for l in std::iter::once("").chain(lines).chain(std::iter::once("")) {
        let pad = " ".repeat(width - l.chars().count());
        out.push_str(&format!("║  {l}{pad}  ║\n"));
    }
    out.push_str(&format!("╚{rule}╝"));
    out
}

#[async_trait(?Send)]
impl Approver for Terminal {
    async fn approve_drop(
        &self,
        db: &str,
        reporter: &Reporter,
        token: &CancellationToken,
    ) -> Result<()> {
        if self.force {
            if !self.interactive {
                reporter.line(&format!("--force: dropping and recreating \"{db}\""));
                return Ok(());
            }
            if self.banner {
                reporter.line(&banner(db));
            }
            for left in (1..=5).rev() {
                reporter.line(&format!(
                    "--force: DROP DATABASE \"{db}\" in {left}s. Ctrl-C aborts."
                ));
                tokio::select! {
                    _ = tokio::time::sleep(Duration::from_secs(1)) => {}
                    _ = token.cancelled() => return Err(interrupted()),
                }
            }
            return Ok(());
        }
        if !self.interactive {
            return Err(Error::new(
                Kind::Denied,
                format!(
                    "approval denied: --overwrite must be confirmed at a terminal, and this is not one; pass --force to drop and recreate \"{db}\" from a script or CI job"
                ),
            ));
        }
        reporter.line(&format!(
            "WARNING: about to DROP and RECREATE database \"{db}\". This deletes all data.\nType the database name to confirm:"
        ));
        let answer = self.answer.clone();
        let read = tokio::task::spawn_blocking(move || answer());
        let line = tokio::select! {
            r = read => r
                .map_err(|e| Error::internal(format!("prompt: {e}")))?
                .map_err(|e| Error::new(Kind::Denied, "approval denied").with_source(e))?,
            _ = token.cancelled() => return Err(interrupted()),
        };
        let Some(line) = line else {
            return Err(Error::new(
                Kind::Denied,
                "approval denied: No input available. Cancelled.",
            ));
        };
        let typed = line.trim();
        if typed != db {
            return Err(Error::new(
                Kind::Denied,
                format!("approval denied: \"{typed}\" does not match \"{db}\". Cancelled."),
            ));
        }
        reporter.line("Confirmed.");
        Ok(())
    }
}

/// `serve`: the caller confirms by naming the database in the tool call.
pub(crate) struct ConfirmedName(pub(crate) Option<String>);

#[async_trait(?Send)]
impl Approver for ConfirmedName {
    fn before_connecting(&self, db: &str) -> Result<()> {
        match &self.0 {
            None => Err(Error::new(
                Kind::Denied,
                format!(
                    "overwrite=true drops database \"{db}\": pass confirmDatabaseName=\"{db}\" to confirm"
                ),
            )),
            Some(c) if c != db => Err(Error::new(
                Kind::Denied,
                format!(
                    "confirmDatabaseName \"{c}\" does not match database \"{db}\"; nothing was deployed and no database was dropped"
                ),
            )),
            Some(_) => Ok(()),
        }
    }

    async fn approve_drop(&self, _: &str, _: &Reporter, _: &CancellationToken) -> Result<()> {
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::output::Captured;

    fn reporter() -> (Reporter, Captured) {
        Captured::reporter(false, false, false)
    }

    fn terminal(force: bool, interactive: bool, typed: Option<&'static str>) -> Terminal {
        Terminal {
            force,
            interactive,
            banner: false,
            answer: Arc::new(move || Ok(typed.map(str::to_string))),
        }
    }

    async fn ask(t: &Terminal) -> (Result<()>, String) {
        let (r, out) = reporter();
        let result = t.approve_drop("app", &r, &CancellationToken::new()).await;
        (result, out.text())
    }

    #[tokio::test]
    async fn a_script_without_force_is_refused() {
        let (r, out) = ask(&terminal(false, false, Some("app\n"))).await;
        let e = r.unwrap_err();
        assert_eq!(e.exit_code(), 12);
        assert_eq!(
            e.text(),
            "approval denied: --overwrite must be confirmed at a terminal, and this is not one; pass --force to drop and recreate \"app\" from a script or CI job"
        );
        assert_eq!(out, "");
    }

    #[tokio::test]
    async fn the_typed_name_must_match() {
        let (r, out) = ask(&terminal(false, true, Some("app\r\n"))).await;
        r.unwrap();
        assert_eq!(
            out,
            "WARNING: about to DROP and RECREATE database \"app\". This deletes all data.\nType the database name to confirm:\nConfirmed.\n"
        );
        let (r, _) = ask(&terminal(false, true, Some("ap\n"))).await;
        let e = r.unwrap_err();
        assert_eq!(e.exit_code(), 12);
        assert_eq!(
            e.text(),
            "approval denied: \"ap\" does not match \"app\". Cancelled."
        );
    }

    #[tokio::test]
    async fn no_answer_is_a_denial() {
        let (r, _) = ask(&terminal(false, true, None)).await;
        let e = r.unwrap_err();
        assert_eq!(e.exit_code(), 12);
        assert_eq!(e.text(), "approval denied: No input available. Cancelled.");
    }

    #[tokio::test]
    async fn force_in_a_script_drops_without_waiting() {
        let (r, out) = ask(&terminal(true, false, None)).await;
        r.unwrap();
        assert_eq!(out, "--force: dropping and recreating \"app\"\n");
    }

    #[tokio::test(start_paused = true)]
    async fn force_at_a_terminal_counts_down_five_seconds() {
        let started = tokio::time::Instant::now();
        let (r, out) = ask(&terminal(true, true, None)).await;
        r.unwrap();
        assert_eq!(started.elapsed(), Duration::from_secs(5));
        let lines: Vec<&str> = out.lines().collect();
        assert_eq!(lines.len(), 5);
        assert_eq!(
            lines[0],
            "--force: DROP DATABASE \"app\" in 5s. Ctrl-C aborts."
        );
        assert_eq!(
            lines[4],
            "--force: DROP DATABASE \"app\" in 1s. Ctrl-C aborts."
        );
    }

    #[tokio::test(start_paused = true)]
    async fn cancelling_the_countdown_is_an_interrupt() {
        let t = terminal(true, true, None);
        let (r, out) = reporter();
        let token = CancellationToken::new();
        let cancel = async {
            tokio::time::sleep(Duration::from_millis(2500)).await;
            token.cancel();
        };
        let (result, ()) = tokio::join!(t.approve_drop("app", &r, &token), cancel);
        let e = result.unwrap_err();
        assert_eq!(e.exit_code(), 130);
        assert_eq!(e.text(), "interrupted");
        let text = out.text();
        assert!(text.contains("in 3s."), "{text}");
        assert!(!text.contains("in 2s."), "{text}");
    }

    #[tokio::test]
    async fn cancelling_the_prompt_is_an_interrupt() {
        let t = Terminal {
            answer: Arc::new(|| {
                std::thread::sleep(Duration::from_millis(200));
                Ok(Some("app\n".into()))
            }),
            ..terminal(false, true, None)
        };
        let (r, out) = reporter();
        let token = CancellationToken::new();
        token.cancel();
        let e = t.approve_drop("app", &r, &token).await.unwrap_err();
        assert_eq!(e.exit_code(), 130);
        assert!(!out.text().contains("Confirmed."));
    }

    #[tokio::test]
    async fn surrounding_whitespace_is_ignored_and_an_empty_line_is_a_denial() {
        let (r, _) = ask(&terminal(false, true, Some("  app  \n"))).await;
        r.unwrap();
        let (r, out) = ask(&terminal(false, true, Some("\n"))).await;
        let e = r.unwrap_err();
        assert_eq!(e.exit_code(), 12);
        assert_eq!(
            e.text(),
            "approval denied: \"\" does not match \"app\". Cancelled."
        );
        assert!(!out.contains("Confirmed."));
    }

    #[tokio::test]
    async fn a_failed_read_is_a_denial() {
        let t = Terminal {
            answer: Arc::new(|| Err(std::io::ErrorKind::UnexpectedEof.into())),
            ..terminal(false, true, None)
        };
        let (r, out) = ask(&t).await;
        let e = r.unwrap_err();
        assert_eq!(e.exit_code(), 12);
        assert!(e.text().starts_with("approval denied: "), "{}", e.text());
        assert!(!out.contains("Confirmed."));
    }

    #[test]
    fn the_banner_frames_every_line_and_names_the_database() {
        for db in [
            "x",
            "a_much_longer_database_name_than_the_frame_was_drawn_for",
        ] {
            let b = banner(db);
            assert!(b.contains(&format!("'{db}'")), "{b}");
            assert!(!b.contains("${dbname}"));
            let lines: Vec<&str> = b.lines().collect();
            let width = lines[0].chars().count();
            let last = lines.len() - 1;
            for (i, l) in lines.iter().enumerate() {
                let (first, end) = (l.chars().next(), l.chars().last());
                let framed = match i {
                    0 => (first, end) == (Some('╔'), Some('╗')),
                    i if i == last => (first, end) == (Some('╚'), Some('╝')),
                    _ => (first, end) == (Some('║'), Some('║')),
                };
                assert!(framed && l.chars().count() == width, "{db}: line {i}:\n{b}");
            }
        }
    }

    #[tokio::test]
    async fn serve_confirms_by_name_before_connecting() {
        let e = ConfirmedName(None).before_connecting("app").unwrap_err();
        assert_eq!(e.exit_code(), 12);
        assert_eq!(
            e.text(),
            "overwrite=true drops database \"app\": pass confirmDatabaseName=\"app\" to confirm"
        );
        let e = ConfirmedName(Some("other".into()))
            .before_connecting("app")
            .unwrap_err();
        assert_eq!(e.exit_code(), 12);
        assert_eq!(
            e.text(),
            "confirmDatabaseName \"other\" does not match database \"app\"; nothing was deployed and no database was dropped"
        );
        let named = ConfirmedName(Some("app".into()));
        named.before_connecting("app").unwrap();
        let (r, out) = reporter();
        named
            .approve_drop("app", &r, &CancellationToken::new())
            .await
            .unwrap();
        assert_eq!(out.text(), "");
        assert!(
            Terminal::new(false, &Env::default())
                .before_connecting("app")
                .is_ok()
        );
    }
}
