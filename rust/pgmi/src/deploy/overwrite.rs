use std::io::{BufRead, IsTerminal};

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

pub fn approve(a: &DeployArgs, spec: &Spec, env: &Env, reporter: &Reporter) -> Result<()> {
    let db = &spec.target_db;
    if a.force {
        reporter.line(&format!("--force: dropping and recreating \"{db}\""));
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
    let mut line = String::new();
    std::io::stdin()
        .lock()
        .read_line(&mut line)
        .map_err(|e| Error::new(Kind::Denied, "approval denied").with_source(e))?;
    if line.trim_end_matches(['\r', '\n']) != db {
        return Err(Error::new(
            Kind::Denied,
            format!("approval denied: the name typed does not match \"{db}\""),
        ));
    }
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
    reporter.line(&format!(
        "Database \"{db}\" {}; creating",
        if exists { "dropped" } else { "does not exist" }
    ));
    maintenance
        .client
        .batch_execute(&format!("CREATE DATABASE {}", quote_ident(db)))
        .await
        .map_err(|e| sql_error("create the target database", &e))?;
    Ok(exists)
}
