use std::collections::BTreeMap;
use std::sync::Arc;

use tokio_postgres::types::Type;

use super::blame::{Blame, Sent};
use super::script::{self, LineMap, Unit};
use super::session::{self, Load};
use super::{Execution, Failure, Guard, sql_error};
use crate::VERSION;
use crate::conn::dial::Session;
use crate::conn::tap::IDLE;
use crate::error::{Error, Result};
use crate::output::Reporter;
use crate::project::metadata::Meta;
use crate::project::walk::{File, Project};

pub(crate) struct Run<'a> {
    pub(crate) session: &'a Session,
    pub(crate) guard: &'a Guard,
    pub(crate) reporter: &'a Arc<Reporter>,
    pub(crate) project: &'a Project,
    pub(crate) meta: &'a [Meta],
    pub(crate) params: &'a BTreeMap<String, String>,
    pub(crate) verbose: bool,
    pub(crate) files_loaded: usize,
    pub(crate) execution: &'a mut Option<Execution>,
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

pub(crate) async fn run(r: Run<'_>) -> Result<()> {
    let Run {
        session,
        guard,
        reporter,
        project,
        meta,
        params,
        verbose,
        files_loaded,
        execution,
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
    reporter.line(&format!("Loaded {files_loaded} files"));
    if !params.is_empty() {
        reporter.line(&format!("Loaded {} parameter(s)", params.len()));
    }
    if verbose {
        guard
            .running(session, async {
                session
                    .client
                    .batch_execute("SET client_min_messages = debug")
                    .await
                    .map_err(|e| sql_error("enable DEBUG notices", &e))
            })
            .await?;
    }
    let mut units: Vec<(Unit, LineMap)> = Vec::new();
    let mut count = 0;
    for unit in script::split(&project.deploy_sql) {
        let found = script::macros(&unit.text);
        if found.is_empty() {
            units.push((unit, LineMap::default()));
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
        let (text, lines) = script::expand(&unit.text, &found, &generated);
        units.push((
            Unit {
                text,
                start: unit.start,
            },
            lines,
        ));
    }
    let execution = execution.insert(Execution {
        test_macros: count,
        units: units.len(),
        committed: 0,
        failure: None,
    });
    reporter.line("Executing deploy.sql");
    if let Some(notice) = unused_tests_notice(project, count) {
        reporter.line(&notice);
    }
    for (i, (unit, lines)) in units.iter().enumerate() {
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
            let files: Vec<&File> = project.sources.iter().chain(&project.tests).collect();
            let sent = Sent {
                deploy_sql: &project.deploy_sql,
                start: unit.start,
                text: &unit.text,
                lines,
                tail: i > 0,
            };
            execution.failure = Some(Failure {
                unit: i + 1,
                blame: e
                    .sql_diagnostics()
                    .map(|d| Blame::new(d, &e.text(), &sent, &files))
                    .unwrap_or_default(),
            });
            return Err(e);
        }
        if session.tap.status() == IDLE {
            execution.committed = i + 1;
        }
    }
    if session.tap.status() != IDLE {
        let _ = session.client.batch_execute("ROLLBACK").await;
        execution.failure = Some(Failure {
            unit: units.len(),
            blame: Blame::default(),
        });
        return Err(Error::sql(
            "deploy.sql ended inside an open transaction, which was rolled back; end it with COMMIT",
        ));
    }
    Ok(())
}

fn unused_tests_notice(project: &Project, macros: usize) -> Option<String> {
    let n = project.tests.len();
    (macros == 0 && n > 0 && !project.deploy_sql.to_lowercase().contains("pgmi_test")).then(|| {
        format!("{n} test file(s) loaded; deploy.sql never calls pgmi_test(), so none of them run")
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn project(deploy_sql: &str, tests: &[&str]) -> Project {
        Project {
            deploy_sql: deploy_sql.into(),
            tests: tests
                .iter()
                .map(|p| File {
                    path: (*p).into(),
                    content: "SELECT 1;".into(),
                })
                .collect(),
            ..Project::default()
        }
    }

    #[test]
    fn unused_tests_are_noticed_only_when_nothing_could_run_them() {
        assert_eq!(
            unused_tests_notice(&project("SELECT 1;", &["./__test__/test_a.sql"]), 0).as_deref(),
            Some("1 test file(s) loaded; deploy.sql never calls pgmi_test(), so none of them run")
        );
        assert_eq!(
            unused_tests_notice(
                &project("BEGIN; CALL pgmi_test(); COMMIT;", &["./__test__/a.sql"]),
                1
            ),
            None
        );
        assert_eq!(
            unused_tests_notice(
                &project(
                    "SELECT count(*) FROM pg_temp.PGMI_TEST_SOURCE_VIEW;",
                    &["./__test__/a.sql"]
                ),
                0
            ),
            None
        );
        assert_eq!(unused_tests_notice(&project("SELECT 1;", &[]), 0), None);
    }
}
