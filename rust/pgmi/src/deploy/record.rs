use std::time::{Duration, SystemTime};

use serde::Serialize;
use serde_json::Value;

use super::{Failure, Progress, Provision};
use crate::VERSION;
use crate::error::{Result, redact};
use crate::output::timestamp;
use crate::project::walk::Ignored;

/// The `--json` record and the `serve` deploy result (record 0019). Fields
/// are declared in byte order of their JSON names, the key order pgmi has
/// always printed; `Option` fields without `skip_serializing_if` print
/// `null`.
#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct DeployRecord {
    connection_source: Option<&'static str>,
    contract_version: Option<&'static str>,
    created: Option<bool>,
    database: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    detail: Option<String>,
    dotenv_keys: Vec<String>,
    pub(crate) duration_ms: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    execution_mode: Option<&'static str>,
    #[serde(skip_serializing_if = "Option::is_none")]
    execution_units: Option<usize>,
    exit_code: u8,
    #[serde(skip_serializing_if = "Option::is_none")]
    failed_file: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    failed_file_candidates: Option<Vec<String>>,
    pub(crate) files_loaded: Option<usize>,
    finished_at: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    hint: Option<String>,
    ignored_paths: Vec<Ignored>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) message: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    notices: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    notices_truncated: Option<u64>,
    overrides: [(); 0],
    pub(crate) paths_ignored: Option<usize>,
    pgmi_version: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) phase: Option<&'static str>,
    recreated: Option<bool>,
    schema_version: u8,
    #[serde(skip_serializing_if = "Option::is_none")]
    script: Option<String>,
    server_version: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    sqlstate: Option<String>,
    started_at: String,
    status: &'static str,
    target: Option<Target>,
    test_macros: Option<usize>,
    tls: Option<Tls>,
    #[serde(skip_serializing_if = "Option::is_none")]
    units_committed: Option<usize>,
    #[serde(rename = "where", skip_serializing_if = "Option::is_none")]
    where_: Option<String>,
}

#[derive(Debug, Serialize)]
struct Target {
    database: String,
    host: String,
    port: u16,
}

/// What the deploy session saw of TLS.
#[derive(Debug, Clone, Serialize)]
pub(crate) struct Tls {
    pub(crate) enabled: bool,
    pub(crate) sslmode: crate::conn::resolve::SslMode,
    pub(crate) version: Option<String>,
}

/// The parts of a record that do not come from the deploy's progress.
pub(crate) struct Facts<'a> {
    pub(crate) dotenv_keys: &'a [String],
    pub(crate) started: SystemTime,
    pub(crate) elapsed: Duration,
    pub(crate) notices: Option<(Value, Option<u64>)>,
    pub(crate) secrets: &'a [String],
}

impl DeployRecord {
    pub(crate) fn new(p: &Progress, result: &Result<()>, facts: Facts<'_>) -> Self {
        let exit_code = match result {
            Ok(()) => 0,
            Err(e) => e.exit_code(),
        };
        let loaded = p.loaded.as_ref();
        let execution = p.execution.as_ref();
        let (notices, notices_truncated) = match facts.notices {
            Some((n, t)) => (Some(n), t),
            None => (None, None),
        };
        let mut record = DeployRecord {
            connection_source: p.spec.as_ref().map(|s| s.source.label()),
            contract_version: p.contract.then_some("1"),
            created: loaded.map(|_| p.provision == Provision::Created),
            database: p.database().map(str::to_string),
            detail: None,
            dotenv_keys: facts.dotenv_keys.to_vec(),
            duration_ms: u64::try_from(facts.elapsed.as_millis()).unwrap_or(u64::MAX),
            error: None,
            execution_mode: None,
            execution_units: execution.map(|x| x.units),
            exit_code,
            failed_file: None,
            failed_file_candidates: None,
            files_loaded: loaded.map(|l| l.files),
            finished_at: timestamp(facts.started + facts.elapsed),
            hint: None,
            ignored_paths: loaded.map(|l| l.ignored.clone()).unwrap_or_default(),
            message: None,
            notices,
            notices_truncated,
            overrides: [],
            paths_ignored: loaded.map(|l| l.ignored.len()),
            pgmi_version: VERSION,
            phase: None,
            recreated: loaded.map(|_| p.provision == Provision::Recreated),
            schema_version: 1,
            script: None,
            server_version: p.server.as_ref().and_then(|s| s.version.clone()),
            sqlstate: None,
            started_at: timestamp(facts.started),
            status: if exit_code == 0 { "success" } else { "failed" },
            target: p.spec.as_ref().map(|s| {
                let (host, port) = s.first();
                Target {
                    database: s.target_db.clone(),
                    host: host.to_string(),
                    port,
                }
            }),
            test_macros: execution.map(|x| x.test_macros).or(loaded.map(|_| 0)),
            tls: p.server.as_ref().map(|s| s.tls.clone()),
            units_committed: execution.map(|x| x.committed),
            where_: None,
        };
        if let Err(e) = result {
            record.fail(e, execution.and_then(|x| x.failure.as_ref()), facts.secrets);
        }
        record
    }

    fn fail(&mut self, e: &crate::error::Error, failure: Option<&Failure>, secrets: &[String]) {
        if failure.is_some() {
            self.execution_mode = Some(if self.units_committed == Some(0) {
                "atomic"
            } else {
                "psql"
            });
        }
        self.error = Some(redact(&e.text(), secrets));
        let Some(d) = e.sql_diagnostics() else {
            return;
        };
        self.sqlstate.clone_from(&d.sqlstate);
        self.detail.clone_from(&d.detail);
        self.hint.clone_from(&d.hint);
        self.where_.clone_from(&d.where_);
        let Some(f) = failure else {
            return;
        };
        self.phase = Some(if f.test { "test" } else { "deploy" });
        let file = match f.files.as_slice() {
            [] if f.test => return,
            [] => "deploy.sql".to_string(),
            [first, ..] => first.clone(),
        };
        if f.files.len() > 1 {
            self.failed_file_candidates = Some(f.files.clone());
        }
        self.failed_file = Some(file.clone());
        self.script = Some(file);
    }
}
