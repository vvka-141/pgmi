use std::collections::BTreeMap;
use std::sync::Arc;

use tokio_postgres::types::Type;

use super::script::{self, Unit};
use super::session::{self, Load};
use super::{Guard, State, sql_error};
use crate::cli::VERSION;
use crate::conn::dial::Session;
use crate::conn::tap::IDLE;
use crate::error::{Error, Result};
use crate::output::Reporter;
use crate::project::metadata::Meta;
use crate::project::walk::Project;

pub struct Run<'a> {
    pub session: &'a Session,
    pub guard: &'a Guard,
    pub reporter: &'a Arc<Reporter>,
    pub project: &'a Project,
    pub meta: &'a [Meta],
    pub params: &'a BTreeMap<String, String>,
    pub verbose: bool,
    pub state: &'a mut State,
}

async fn generate(
    session: &Session,
    pattern: Option<&str>,
    callback: Option<&str>,
) -> Result<String> {
    let rows = session
        .client
        .query_typed(
            "SELECT pg_temp.pgmi_test_generate($1, coalesce($2, 'pg_temp.pgmi_test_callback'))",
            &[(&pattern, Type::TEXT), (&callback, Type::TEXT)],
        )
        .await
        .map_err(|e| sql_error("expand CALL pgmi_test()", &e))?;
    rows.first()
        .and_then(|r| r.try_get::<_, Option<String>>(0).ok().flatten())
        .ok_or_else(|| Error::sql("pgmi_test_generate() returned nothing"))
}

pub async fn run(r: Run<'_>) -> Result<()> {
    let Run {
        session,
        guard,
        reporter,
        project,
        meta,
        params,
        verbose,
        state,
    } = r;
    reporter.line("Preparing session: scanning files, loading parameters");
    guard
        .running(
            session,
            session::load(
                &session.client,
                Load {
                    version: VERSION,
                    params,
                    sources: &project.sources,
                    tests: &project.tests,
                    metadata: meta,
                },
            ),
        )
        .await?;
    reporter.line(&format!("Loaded {} files", state.files_loaded.unwrap_or(0)));
    if !params.is_empty() {
        reporter.line(&format!("Loaded {} parameter(s)", params.len()));
    }
    if verbose {
        session
            .client
            .batch_execute("SET client_min_messages = debug")
            .await
            .map_err(|e| sql_error("enable DEBUG notices", &e))?;
    }
    let mut units: Vec<Unit> = Vec::new();
    let mut macro_units = Vec::new();
    let mut count = 0;
    for unit in script::split(&project.deploy_sql) {
        let found = script::macros(&unit.text);
        if found.is_empty() {
            macro_units.push(false);
            units.push(unit);
            continue;
        }
        let mut generated = Vec::new();
        for m in &found {
            let sql = guard
                .running(
                    session,
                    generate(session, m.pattern.as_deref(), m.callback.as_deref()),
                )
                .await?;
            generated.push(sql);
        }
        count += found.len();
        macro_units.push(true);
        units.push(Unit {
            text: script::expand(&unit.text, &found, &generated),
            line: unit.line,
        });
    }
    state.test_macros = Some(count);
    state.units = Some(units.len());
    state.committed = Some(0);
    reporter.line("Executing deploy.sql");
    for (i, unit) in units.iter().enumerate() {
        let outcome = guard
            .running(session, async {
                session
                    .client
                    .batch_execute(&unit.text)
                    .await
                    .map_err(|e| sql_error("execute deploy.sql", &e))
            })
            .await;
        if let Err(e) = outcome {
            state.failed_unit = Some(i + 1);
            state.phase_test = macro_units[i] && is_test_failure(&e);
            return Err(e);
        }
        if session.tap.status() == IDLE {
            state.committed = Some(i + 1);
        }
    }
    if session.tap.status() != IDLE {
        let _ = session.client.batch_execute("ROLLBACK").await;
        state.failed_unit = Some(units.len());
        return Err(Error::sql(
            "deploy.sql ended inside an open transaction, which was rolled back; end it with COMMIT",
        ));
    }
    Ok(())
}

fn is_test_failure(e: &Error) -> bool {
    e.sql_diagnostics()
        .and_then(|d| d.where_.as_deref())
        .is_some_and(|w| w.contains("pgmi_test"))
}
