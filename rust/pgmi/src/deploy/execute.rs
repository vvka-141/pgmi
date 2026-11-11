use std::collections::BTreeMap;
use std::sync::Arc;

use tokio_postgres::types::Type;

use super::script::{self, Unit};
use super::session::{self, Load};
use super::{Execution, Failure, Guard, sql_error};
use crate::VERSION;
use crate::conn::dial::Session;
use crate::conn::tap::IDLE;
use crate::error::{Error, Result};
use crate::output::Reporter;
use crate::project::metadata::Meta;
use crate::project::walk::Project;

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
    let execution = execution.insert(Execution {
        test_macros: count,
        units: units.len(),
        committed: 0,
        failure: None,
    });
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
            execution.failure = Some(Failure {
                unit: i + 1,
                test: macro_units[i] && is_test_failure(&e),
                files: failed_files(&e, project),
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
            test: false,
            files: Vec::new(),
        });
        return Err(Error::sql(
            "deploy.sql ended inside an open transaction, which was rolled back; end it with COMMIT",
        ));
    }
    Ok(())
}

fn executed_text(e: &Error) -> Option<String> {
    let d = e.sql_diagnostics()?;
    if let Some(q) = &d.internal_query {
        return Some(q.clone());
    }
    let w = d.where_.as_deref()?;
    let rest = w.strip_prefix("SQL statement \"")?;
    let end = rest.rfind("\"\n").or_else(|| rest.rfind('"'))?;
    Some(rest[..end].to_string())
}

fn failed_files(e: &Error, project: &Project) -> Vec<String> {
    let Some(text) = executed_text(e) else {
        return vec![];
    };
    let needle = text.trim();
    if needle.is_empty() {
        return vec![];
    }
    project
        .sources
        .iter()
        .chain(&project.tests)
        .filter(|f| crate::project::walk::is_sql(&f.path) && f.content.contains(needle))
        .map(|f| f.path.clone())
        .collect()
}

fn is_test_failure(e: &Error) -> bool {
    e.sql_diagnostics()
        .and_then(|d| d.where_.as_deref())
        .is_some_and(|w| w.contains("pgmi_test"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::error::SqlDiagnostics;
    use crate::project::walk::File;

    fn failed(where_: Option<&str>, internal_query: Option<&str>) -> Error {
        Error::sql("boom").with_sql(SqlDiagnostics {
            where_: where_.map(str::to_string),
            internal_query: internal_query.map(str::to_string),
            ..SqlDiagnostics::default()
        })
    }

    fn file(path: &str, content: &str) -> File {
        File {
            path: path.into(),
            content: content.into(),
        }
    }

    #[test]
    fn executed_text_prefers_the_internal_query() {
        let e = failed(
            Some(
                "SQL statement \"SELECT 2\"\nPL/pgSQL function inline_code_block line 3 at EXECUTE",
            ),
            Some("SELECT 1/0"),
        );
        assert_eq!(executed_text(&e).as_deref(), Some("SELECT 1/0"));
    }

    #[test]
    fn executed_text_reads_the_statement_out_of_where() {
        let e = failed(
            Some(
                "SQL statement \"SELECT \"q\" FROM t\"\nPL/pgSQL function inline_code_block line 3 at EXECUTE",
            ),
            None,
        );
        assert_eq!(executed_text(&e).as_deref(), Some("SELECT \"q\" FROM t"));
        let e = failed(Some("SQL statement \"SELECT 1\""), None);
        assert_eq!(executed_text(&e).as_deref(), Some("SELECT 1"));
        assert_eq!(
            executed_text(&failed(Some("PL/pgSQL function f() line 1"), None)),
            None
        );
        assert_eq!(executed_text(&failed(None, None)), None);
        assert_eq!(executed_text(&Error::sql("no diagnostics")), None);
    }

    #[test]
    fn failed_files_are_the_sql_files_that_hold_the_statement() {
        let project = Project {
            sources: vec![
                file("./a.sql", "CREATE TABLE a();\nSELECT 1/0;\n"),
                file("./b.sql", "SELECT 2;"),
                file("./notes.txt", "SELECT 1/0"),
            ],
            tests: vec![file("./__test__/t.sql", "  SELECT 1/0  ")],
            ..Project::default()
        };
        assert_eq!(
            failed_files(&failed(None, Some("  SELECT 1/0\n")), &project),
            ["./a.sql", "./__test__/t.sql"]
        );
        assert!(failed_files(&failed(None, Some("SELECT 3")), &project).is_empty());
        assert!(failed_files(&failed(None, Some("  \n")), &project).is_empty());
        assert!(failed_files(&Error::sql("x"), &project).is_empty());
    }

    #[test]
    fn a_test_failure_is_one_raised_under_pgmi_test() {
        assert!(is_test_failure(&failed(
            Some("PL/pgSQL function pg_temp.pgmi_test_callback(text) line 4 at RAISE"),
            None
        )));
        assert!(!is_test_failure(&failed(
            Some("PL/pgSQL function f() line 1"),
            None
        )));
        assert!(!is_test_failure(&failed(None, None)));
        assert!(!is_test_failure(&Error::sql("x")));
    }
}
