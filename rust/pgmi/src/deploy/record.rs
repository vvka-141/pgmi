use std::time::{Duration, SystemTime};

use serde::Serialize;
use serde_json::Value;

use super::blame::redact_sql_passwords;
use super::{ContractVersion, Failure, Phase, Progress, Provision};
use crate::VERSION;
use crate::conn::overrides::Override;
use crate::conn::resolve::Source;
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
    #[serde(skip_serializing_if = "Option::is_none")]
    column: Option<usize>,
    connection_source: Option<&'static str>,
    contract_version: Option<&'static str>,
    created: Option<bool>,
    database: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    detail: Option<String>,
    dotenv_keys: Vec<String>,
    pub(crate) duration_ms: Option<u64>,
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
    line: Option<usize>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) message: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    notices: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    notices_truncated: Option<u64>,
    overrides: Vec<Override>,
    pub(crate) paths_ignored: Option<usize>,
    pgmi_version: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) phase: Option<&'static str>,
    recreated: Option<bool>,
    schema_version: u8,
    #[serde(skip_serializing_if = "Option::is_none")]
    script: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    script_expanded: Option<bool>,
    server_version: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    source_line: Option<String>,
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
    pub(crate) version: Option<crate::conn::tls::TlsVersion>,
}

/// The parts of a record that do not come from the deploy's progress.
pub(crate) struct Facts<'a> {
    pub(crate) dotenv_keys: &'a [String],
    pub(crate) started: SystemTime,
    pub(crate) elapsed: Duration,
    pub(crate) notices: Option<(Value, Option<u64>)>,
    pub(crate) secrets: &'a [crate::error::Secret],
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
            column: None,
            connection_source: p.connection_source.map(Source::label),
            contract_version: p.contract.map(ContractVersion::as_str),
            created: loaded
                .map(|_| p.provision == Provision::Created)
                .or(p.reached(Phase::Deploying).then_some(false)),
            database: loaded.and(p.database()).map(str::to_string),
            detail: None,
            dotenv_keys: facts.dotenv_keys.to_vec(),
            duration_ms: p
                .reached(Phase::Deploying)
                .then(|| u64::try_from(facts.elapsed.as_millis()).unwrap_or(u64::MAX)),
            error: None,
            execution_mode: None,
            execution_units: execution.map(|x| x.units),
            exit_code,
            failed_file: None,
            failed_file_candidates: None,
            files_loaded: loaded
                .map(|l| l.files)
                .or(p.reached(Phase::Deploying).then_some(0)),
            finished_at: timestamp(facts.started + facts.elapsed),
            hint: None,
            ignored_paths: loaded.map(|l| l.ignored.clone()).unwrap_or_default(),
            line: None,
            message: None,
            notices,
            notices_truncated,
            overrides: p.overrides.clone(),
            paths_ignored: loaded
                .map(|l| l.ignored.len())
                .or(p.reached(Phase::Deploying).then_some(0)),
            pgmi_version: VERSION,
            phase: None,
            recreated: loaded
                .map(|_| p.provision == Provision::Recreated)
                .or(p.reached(Phase::Deploying).then_some(false)),
            schema_version: 1,
            script: None,
            script_expanded: None,
            server_version: p.server.as_ref().and_then(|s| s.version.clone()),
            source_line: None,
            sqlstate: None,
            started_at: timestamp(facts.started),
            status: if exit_code == 0 { "success" } else { "failed" },
            target: p.spec.as_ref().map(|s| {
                let first = s.first();
                Target {
                    database: s.target_db.clone(),
                    host: first.name.clone(),
                    port: first.port,
                }
            }),
            test_macros: execution
                .map(|x| x.test_macros)
                .or((loaded.is_some() || p.reached(Phase::Deploying)).then_some(0)),
            tls: p.server.as_ref().map(|s| s.tls.clone()),
            units_committed: execution.map(|x| x.committed),
            where_: None,
        };
        if let Err(e) = result {
            record.fail(e, execution.and_then(|x| x.failure.as_ref()), facts.secrets);
        }
        record
    }

    /// Each kept `WARNING` notice as `WARNING: message`, one per line, for
    /// the text a text-only MCP client shows.
    pub(crate) fn warning_lines(&self) -> String {
        let Some(Value::Array(notices)) = &self.notices else {
            return String::new();
        };
        notices
            .iter()
            .filter(|n| n["severity"] == "WARNING")
            .filter_map(|n| n["message"].as_str())
            .map(|m| format!("WARNING: {m}"))
            .collect::<Vec<_>>()
            .join("\n")
    }

    /// Adds a line to the end of `error`.
    pub(crate) fn note_error(&mut self, note: &str) {
        if let Some(e) = &mut self.error {
            e.push('\n');
            e.push_str(note);
        }
    }

    fn fail(
        &mut self,
        e: &crate::error::Error,
        failure: Option<&Failure>,
        secrets: &[crate::error::Secret],
    ) {
        if failure.is_some() {
            self.execution_mode = Some(if self.units_committed == Some(0) {
                "atomic"
            } else {
                "psql"
            });
        }
        let clean = |t: &str| redact(&redact_sql_passwords(t), secrets);
        self.error = Some(clean(&e.text()));
        let Some(d) = e.sql_diagnostics() else {
            return;
        };
        self.sqlstate.clone_from(&d.sqlstate);
        self.detail = d.detail.as_deref().map(clean);
        self.hint = d.hint.as_deref().map(clean);
        self.where_ = d.where_.as_deref().map(clean);
        let Some(b) = failure.map(|f| &f.blame) else {
            return;
        };
        self.phase = Some(b.phase());
        if let Some(w) = &b.where_ {
            self.where_ = Some(clean(w));
        }
        self.failed_file.clone_from(&b.file);
        if b.candidates.len() > 1 {
            self.failed_file_candidates = Some(b.candidates.clone());
        }
        let Some(loc) = &b.location else {
            return;
        };
        self.script = Some(loc.script.clone());
        if let Some(l) = &loc.line {
            self.line = Some(l.number);
            self.column = Some(l.column);
            self.script_expanded = Some(false);
            self.source_line = Some(clean(&l.text)).filter(|t| !t.is_empty());
        }
    }
}
