use std::io::{BufRead, IsTerminal};
use std::time::Duration;

use tokio_util::sync::CancellationToken;

use super::{quote_ident, sql_error};
use crate::cli::DeployArgs;
use crate::conn::dial::Dialer;
use crate::conn::resolve::Spec;
use crate::env::Env;
use crate::error::{Error, Kind, Result};
use crate::output::Reporter;

fn interactive(env: &Env) -> bool {
    std::io::stdin().is_terminal()
        && std::io::stderr().is_terminal()
        && env.non_empty("CI").ok().flatten().is_none()
        && env.get("PGMI_NON_INTERACTIVE").ok().flatten() != Some("1")
}

fn banner(db: &str) -> String {
    let art = pgmi_shared::file("art/skull.txt")
        .and_then(|b| std::str::from_utf8(b).ok())
        .unwrap_or("")
        .replace("${dbname}", db);
    let bar = "═".repeat(71);
    let blank = format!("║{}║", " ".repeat(71));
    format!("╔{bar}╗\n{blank}\n{}\n{blank}\n╚{bar}╝", art.trim_end())
}

pub async fn approve(
    a: &DeployArgs,
    spec: &Spec,
    env: &Env,
    reporter: &Reporter,
    token: &CancellationToken,
) -> Result<()> {
    let db = spec.target_db.clone();
    let interrupted = || Error::new(Kind::Interrupted, "interrupted");
    if a.force {
        if !interactive(env) {
            reporter.line(&format!("--force: dropping and recreating \"{db}\""));
            return Ok(());
        }
        if env.non_empty("PGMI_NO_BANNER").ok().flatten().is_none() {
            reporter.line(&banner(&db));
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
    if !interactive(env) {
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
    let read = tokio::task::spawn_blocking(|| {
        let mut line = String::new();
        std::io::stdin()
            .lock()
            .read_line(&mut line)
            .map(|n| (n, line))
    });
    let (n, line) = tokio::select! {
        r = read => r
            .map_err(|e| Error::internal(format!("prompt: {e}")))?
            .map_err(|e| Error::new(Kind::Denied, "approval denied").with_source(e))?,
        _ = token.cancelled() => return Err(interrupted()),
    };
    if n == 0 {
        return Err(Error::new(
            Kind::Denied,
            "approval denied: No input available. Cancelled.",
        ));
    }
    let typed = line.trim_end_matches(['\r', '\n']);
    if typed != db {
        return Err(Error::new(
            Kind::Denied,
            format!("approval denied: \"{typed}\" does not match \"{db}\". Cancelled."),
        ));
    }
    reporter.line("Confirmed.");
    Ok(())
}

pub async fn recreate(dialer: &Dialer, spec: &Spec, reporter: &Reporter) -> Result<bool> {
    let db = &spec.target_db;
    let maintenance = dialer.connect(spec, &spec.maintenance_db).await?;
    let locked_sql =
        std::str::from_utf8(pgmi_shared::file("manager/deploy_locked.sql").unwrap_or_default())
            .map_err(|_| Error::internal("embedded manager/deploy_locked.sql is not UTF-8"))?;
    let rows = maintenance
        .client
        .query_typed(
            &format!(
                "SELECT ({})::text, EXISTS (SELECT FROM pg_database WHERE datname = $1)::text",
                locked_sql.trim().trim_end_matches(';')
            ),
            &[(db, tokio_postgres::types::Type::TEXT)],
        )
        .await
        .map_err(|e| sql_error("check the target database", &e))?;
    let (locked, exists) = rows
        .first()
        .map(|r| {
            (
                r.get::<_, String>(0) == "true",
                r.get::<_, String>(1) == "true",
            )
        })
        .unwrap_or((false, false));
    if locked {
        return Err(Error::new(
            Kind::Locked,
            format!(
                "another pgmi deploy is running against \"{db}\"; --overwrite does not drop it"
            ),
        ));
    }
    if exists {
        maintenance
            .client
            .batch_execute(&format!("DROP DATABASE {}", quote_ident(db)))
            .await
            .map_err(|e| sql_error("drop the target database", &e))?;
    }
    maintenance
        .client
        .batch_execute(&format!("CREATE DATABASE {}", quote_ident(db)))
        .await
        .map_err(|e| sql_error("create the target database", &e))?;
    reporter.line(&if exists {
        format!("Recreated database \"{db}\"")
    } else {
        format!("Database \"{db}\" does not exist; creating")
    });
    Ok(exists)
}
