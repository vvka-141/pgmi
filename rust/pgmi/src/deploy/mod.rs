mod approve;
mod blame;
mod execute;
mod guard;
mod overwrite;
mod record;
mod request;
mod retry;
pub(crate) mod script;
mod secrets;
mod server;
mod session;
mod stop;

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::io::Write;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime};

use tokio::runtime::Runtime;
use tokio_postgres::error::ErrorPosition;

use crate::config::params::{self, Sources};
use crate::config::yaml::ProjectConfig;
use crate::conn::cloud::{self, Provider};
use crate::conn::dial::{Dialer, Session};
use crate::conn::overrides::{Override, overrides};
use crate::conn::resolve::{self, Source, Spec, TargetSessionAttrs};
use crate::env::Env;
use crate::error::redact;
use crate::error::{Error, Kind, Result, Secret, SqlDiagnostics};
use crate::output::{Notice, NoticeLog, Reporter, Summary, seconds};
use crate::project::{metadata, walk};
use crate::vfs::OsFs;

pub(crate) use approve::{Approver, ConfirmedName, Terminal};
pub(crate) use blame::redact_sql_passwords;
use guard::{Guard, deadline, limit, spawn_signals};
pub(crate) use record::DeployRecord;
use record::Tls;
pub(crate) use request::Request;
use secrets::Secrets;
pub(crate) use server::quote_ident;
use server::{check_server, client_encoding, create_database, first_value, hot_standby, release};
pub(crate) use stop::{Stop, StopReason};

/// The privilege a step needs, named in the error when the server answers
/// 42501.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Needs<'a> {
    Temporary { database: &'a str, role: &'a str },
    CreateDb,
}

fn privilege_hint(code: &str, needs: Option<Needs<'_>>) -> Option<String> {
    match (code, needs?) {
        ("42501", Needs::Temporary { database, role }) => Some(format!(
            "the login needs the TEMPORARY privilege on the target database: GRANT TEMPORARY ON DATABASE {} TO {}",
            quote_ident(database),
            quote_ident(role)
        )),
        ("42501", Needs::CreateDb) => Some(
            "the database does not exist and this role cannot create it: check the -d name, or grant the role CREATEDB".into(),
        ),
        _ => None,
    }
}

pub(super) fn created_elsewhere(e: Error) -> Error {
    match e.sql_diagnostics().and_then(|d| d.sqlstate.as_deref()) {
        Some("42P04" | "23505") => Error::new(
            Kind::Locked,
            "the database was created by another deploy while this one was starting",
        ),
        _ => e,
    }
}

pub(crate) fn sql_error(context: &str, e: tokio_postgres::Error) -> Error {
    sql_error_needing(context, e, None)
}

pub(crate) fn sql_error_needing(
    context: &str,
    e: tokio_postgres::Error,
    needs: Option<Needs<'_>>,
) -> Error {
    match e.as_db_error() {
        Some(db) => {
            let code = db.code().code().to_string();
            let mut message = format!("{}: {} (SQLSTATE {code})", db.severity(), db.message());
            if let Some(hint) = privilege_hint(&code, needs) {
                message.push('\n');
                message.push_str(&hint);
            }
            let (position, internal_position, internal_query) = match db.position() {
                Some(ErrorPosition::Original(p)) => (usize::try_from(*p).ok(), None, None),
                Some(ErrorPosition::Internal { position, query }) => {
                    (None, usize::try_from(*position).ok(), Some(query.clone()))
                }
                None => (None, None, None),
            };
            Error::sql(message).with_sql(SqlDiagnostics {
                sqlstate: Some(code),
                detail: crate::error::field(db.detail()),
                hint: crate::error::field(db.hint()),
                where_: crate::error::field(db.where_()),
                internal_query,
                position,
                internal_position,
            })
        }
        None => Error::sql(context).with_source(e),
    }
}

const PREPARING: &str = "Preparing session: scanning files, loading parameters";

/// Where `--params-file -` reads from: the process's stdin for the CLI;
/// none for `serve`, whose stdin carries the MCP session.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Stdin {
    Process,
    Unavailable,
}

pub(crate) struct Context {
    pub(crate) request: Request,
    pub(crate) verbose: bool,
    pub(crate) env: Env,
    pub(crate) dotenv_keys: Vec<String>,
    pub(crate) dotenv_refused: Vec<String>,
    /// `pgmi serve`: the hosts the operator's own credentials may go to.
    pub(crate) serve_hosts: Option<Vec<String>>,
    pub(crate) stdin: Stdin,
    pub(crate) approver: Box<dyn Approver>,
}

/// The session API version a deploy runs against.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum ContractVersion {
    V1,
}

impl ContractVersion {
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            ContractVersion::V1 => "1",
        }
    }

    fn of(compat: Option<&str>) -> Result<Self> {
        match compat {
            None | Some("1") => Ok(ContractVersion::V1),
            Some(c) => Err(Error::config(format!(
                "unsupported API version \"{c}\"; supported: 1"
            ))),
        }
    }
}

/// How far a deploy got before anything ran on the server: `Configured`
/// once the parameters are read, the point from which the summary line is
/// printed; `Deploying` once an overwrite was approved too, the point from
/// which the record counts files, tests and timings.
#[derive(Debug, Default, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub(crate) enum Phase {
    #[default]
    Preparing,
    Configured,
    Deploying,
}

/// How far a deploy got, one typed result per phase; the summary line and
/// the `--json` record are read from it.
#[derive(Default)]
pub(crate) struct Progress {
    contract: Option<ContractVersion>,
    phase: Phase,
    connection_source: Option<Source>,
    overrides: Vec<Override>,
    spec: Option<Spec>,
    loaded: Option<Loaded>,
    provision: Provision,
    server: Option<ServerInfo>,
    execution: Option<Execution>,
    attempts: Vec<Tried>,
    time_used: Option<Duration>,
    lock_holders: Vec<i32>,
}

/// One run of the deploy from the start: when it began, how long it took,
/// and how it failed.
struct Tried {
    started: SystemTime,
    duration: Duration,
    committed: usize,
    failure: Option<Ended>,
}

/// The error an attempt ended with, as the record lists it.
struct Ended {
    sqlstate: Option<String>,
    message: String,
}

impl Progress {
    fn reached(&self, phase: Phase) -> bool {
        self.phase >= phase
    }

    fn database(&self) -> Option<&str> {
        self.spec.as_ref().map(|s| s.target_db.as_str())
    }

    fn failure(&self) -> Option<&Failure> {
        self.execution.as_ref()?.failure.as_ref()
    }

    /// The line that ends a failure to connect, or to resolve the
    /// connection: where the connection came from, unless it was typed on
    /// the command line.
    fn connection_note(&self, e: &Error) -> Option<String> {
        let source = self.connection_source.filter(|s| !s.typed())?;
        (e.kind() == Kind::Connection || self.spec.is_none())
            .then(|| format!("(connection {})", source.origin()))
    }
}

/// The `-v` block naming where the deploy connects; never a password.
fn resolved(spec: &Spec, provider: Option<Provider>) -> String {
    let first = spec.first();
    let mut text = format!(
        "Connection resolved:\n  Host: {}\n  Port: {}\n  User: {}\n  Target Database: {}\n  Maintenance Database: {}\n  SSL Mode: {}",
        first.name,
        first.port,
        spec.user,
        spec.target_db,
        spec.maintenance_db,
        spec.sslmode()
    );
    for (label, value) in [
        ("SSL Cert", &spec.sslcert),
        ("SSL Key", &spec.sslkey),
        ("SSL Root Cert", &spec.sslrootcert),
    ] {
        if let Some(v) = value {
            let _ = write!(text, "\n  {label}: {v}");
        }
    }
    let auth = match provider {
        None => "Standard",
        Some(Provider::Azure) => "Azure Entra ID",
        Some(Provider::Aws) => "AWS IAM",
        Some(Provider::Google) => "Google IAM",
        Some(Provider::Databricks) => "Databricks",
    };
    let _ = write!(text, "\n  Auth Method: {auth}");
    text
}

/// The stderr lines that say where each connection setting came from: the
/// source (unless typed), the keys the project `.env` set, every `pgmi.yaml`
/// value another source replaced, and `PGOPTIONS`.
fn provenance(
    spec: &Spec,
    dotenv_path: &str,
    dotenv_keys: &[String],
    dotenv_refused: &[String],
    overrides: &[Override],
    pgoptions: bool,
) -> Vec<String> {
    let mut lines = Vec::new();
    if !spec.source.typed() {
        let set_by = spec
            .source
            .variable()
            .filter(|v| dotenv_keys.iter().any(|k| k == v))
            .map(|_| format!(" (set by {dotenv_path})"))
            .unwrap_or_default();
        lines.push(format!(
            "connection: {}{set_by} ({})",
            spec.source.origin(),
            spec.describe()
        ));
    }
    lines.extend(dotenv_line(dotenv_path, dotenv_keys, dotenv_refused));
    lines.extend(overrides.iter().map(ToString::to_string));
    if pgoptions {
        lines.push("session options: from PGOPTIONS".into());
    }
    lines
}

/// `pgmi serve`: keeps the operator's password, password file and cloud
/// sign-in from a server the operator did not allow: its environment's own
/// `host:port`, or a `PGMI_SERVE_HOSTS` entry (`host` for any port, or
/// `host:port`). A password typed in the tool call's own connection string
/// stays. Returns the first server not allowed, as `host:port`, when an
/// operator credential was withheld from it.
fn restrict_to_allowed(
    spec: &mut Spec,
    flags: &resolve::Flags,
    cloud: bool,
    allowed: &[String],
) -> Result<Option<String>> {
    let allows = |e: &resolve::Endpoint| {
        let with_port = [
            format!("{}:{}", e.name, e.port),
            format!("[{}]:{}", e.name, e.port),
        ];
        allowed.iter().any(|a| {
            a.eq_ignore_ascii_case(&e.name) || with_port.iter().any(|w| a.eq_ignore_ascii_case(w))
        })
    };
    let Some(host) = spec
        .hosts
        .endpoints()
        .find(|e| !allows(e))
        .map(|e| format!("{}:{}", e.name, e.port))
    else {
        return Ok(None);
    };
    if cloud {
        return Err(Error::config(format!(
            "pgmi serve signs in to a cloud only for hosts the operator allowed, and \"{host}\" is not one; add it to PGMI_SERVE_HOSTS where pgmi serve starts"
        )));
    }
    let typed = flags.connection.as_ref().is_some_and(|c| {
        crate::conn::parse::parse(c.expose()).is_ok_and(|f| f.contains_key("password"))
    });
    spec.ambient_passfile = false;
    if typed {
        return Ok(None);
    }
    spec.password = None;
    Ok(Some(host))
}

/// What the project `.env` set and which of its keys were refused.
fn dotenv_line(path: &str, applied: &[String], refused: &[String]) -> Option<String> {
    match (applied.is_empty(), refused.is_empty()) {
        (true, true) => None,
        (false, true) => Some(format!("{path} applied: {}", applied.join(", "))),
        (applied_none, false) => Some(format!(
            "{path} applied: {}; refused: {} (a project .env may not locate credentials, endpoints, proxies or trust roots; set them in the environment pgmi runs in)",
            if applied_none {
                "nothing".to_string()
            } else {
                applied.join(", ")
            },
            refused.join(", ")
        )),
    }
}

struct Loaded {
    files: usize,
    ignored: Vec<walk::Ignored>,
}

#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
enum Provision {
    #[default]
    Existing,
    Created,
    Recreated,
}

struct ServerInfo {
    version: Option<String>,
    tls: Tls,
}

pub(crate) struct Execution {
    test_macros: usize,
    units: usize,
    committed: usize,
    failure: Option<Failure>,
}

pub(crate) struct Failure {
    unit: usize,
    blame: blame::Blame,
    unknown: Option<Unknown>,
}

/// Why a failed unit may have committed although it failed.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Unknown {
    /// The unit was sent and the server never answered.
    NoAnswer,
    /// The unit is one DO block or CALL run outside a transaction block,
    /// which can COMMIT inside.
    CommitsInside,
    /// The unit ran PREPARE TRANSACTION: its work waits, holding its locks,
    /// for COMMIT PREPARED or ROLLBACK PREPARED.
    Prepared,
}

impl Failure {
    fn why_unknown(&self, units: usize) -> Option<String> {
        self.unknown.map(|u| match u {
            Unknown::NoAnswer => format!(
                "unit {} of {units} got no answer from the server, so whether it committed is unknown",
                self.unit
            ),
            Unknown::CommitsInside => format!(
                "unit {} of {units} is a DO block or CALL, which may have committed before it failed",
                self.unit
            ),
            Unknown::Prepared => format!(
                "unit {} of {units} ran PREPARE TRANSACTION, which may have left a prepared transaction",
                self.unit
            ),
        })
    }
}

/// How many notices a deploy printed, and how many of them were warnings.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct NoticeCounts {
    pub(crate) notices: u64,
    pub(crate) warnings: u64,
}

pub(crate) struct Run {
    st: Progress,
    log: Arc<Mutex<NoticeLog>>,
    secrets: Secrets,
    started: SystemTime,
    t0: Instant,
}

impl Run {
    pub(crate) fn start() -> Self {
        Run {
            st: Progress::default(),
            log: Arc::new(Mutex::new(NoticeLog::default())),
            secrets: Secrets::default(),
            started: SystemTime::now(),
            t0: Instant::now(),
        }
    }

    pub(crate) async fn deploy(
        &mut self,
        ctx: &Context,
        reporter: &Arc<Reporter>,
        stop: Stop,
    ) -> Result<()> {
        let contract = ContractVersion::of(ctx.request.compat.as_deref());
        self.st.contract = contract.as_ref().ok().copied();
        let project = ctx.request.project.clone();
        let yaml = guard::before_guard(&stop, move || {
            ProjectConfig::load(&OsFs::project(&project)?)
        })
        .await?;
        let yaml_timeout = yaml.as_ref().map(ProjectConfig::timeout).transpose()?;
        let asked = ctx.request.timeout.or(yaml_timeout.flatten());
        let timeout = limit(asked);
        let guard = Guard {
            stop,
            deadline: deadline(timeout),
            timeout,
        };
        let steps = Steps {
            ctx,
            guard: &guard,
            reporter,
            secrets: &self.secrets,
            retry: retry::Policy {
                enabled: yaml.as_ref().and_then(|y| y.retry) != Some(false),
                after_commit: yaml.as_ref().and_then(|y| y.retry_after_commit) == Some(true),
                cap: (asked == Some(Duration::ZERO)).then_some(retry::UNBOXED_ATTEMPTS),
            },
        };
        let boxed = Instant::now();
        let result = Box::pin(steps.deploy(&mut self.st, yaml.as_ref(), contract, &self.log)).await;
        if !self.st.attempts.is_empty() {
            self.st.time_used = Some(boxed.elapsed());
        }
        result
    }

    pub(crate) fn reached_deployer(&self) -> bool {
        self.st.reached(Phase::Deploying)
    }

    pub(crate) fn database(&self) -> Option<&str> {
        self.st.database()
    }

    pub(crate) fn notice_counts(&self) -> NoticeCounts {
        let log = self.log();
        NoticeCounts {
            notices: log.total,
            warnings: log.warning_total,
        }
    }

    /// The notices so far; a panic while one was logged leaves the log as it was.
    fn log(&self) -> std::sync::MutexGuard<'_, NoticeLog> {
        self.log
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
    }

    /// The lines psql would print under the error: DETAIL, HINT, WHERE, and
    /// where in the project the failure was placed.
    pub(crate) fn diagnostics(&self, e: &Error) -> String {
        let mut out = String::new();
        let Some(d) = e.sql_diagnostics() else {
            return out;
        };
        let blame = self.st.failure().map(|f| &f.blame);
        for (label, value) in [
            ("DETAIL", d.detail.as_deref()),
            ("HINT", d.hint.as_deref()),
            (
                "WHERE",
                blame
                    .and_then(|b| b.where_.as_deref())
                    .or(d.where_.as_deref()),
            ),
        ] {
            if let Some(v) = value.filter(|v| !v.is_empty()) {
                let _ = write!(out, "\n{label}: {v}");
            }
        }
        if let Some(b) = blame {
            b.render(&mut out);
        }
        blame::redact_sql_passwords(&out)
    }

    pub(crate) fn secrets(&self) -> Vec<Secret> {
        self.secrets.list()
    }

    pub(crate) fn record(&self, ctx: &Context, result: &Result<()>) -> DeployRecord {
        let secrets = self.secrets();
        let (notices, warnings) = {
            let log = self.log();
            (log.finish(), log.own_warnings())
        };
        DeployRecord::new(
            &self.st,
            result,
            record::Facts {
                dotenv_keys: &ctx.dotenv_keys,
                started: self.started,
                elapsed: self.t0.elapsed(),
                notices: Some(notices),
                warnings,
                secrets: &secrets,
            },
        )
    }
}

pub(crate) fn run(runtime: Runtime, ctx: &Context) -> u8 {
    let reporter = Reporter::new(ctx.verbose, &ctx.env);
    let mut run = Run::start();
    let stop = Stop::default();
    let caught = crate::error::catching(|| {
        runtime.block_on(async {
            spawn_signals(stop.clone(), reporter.clone());
            run.deploy(ctx, &reporter, stop.clone()).await
        })
    });
    let mut result = caught.unwrap_or_else(|p| Err(p.error()));
    if stop.reason() == Some(StopReason::Signal) {
        result = Err(match result {
            Err(e) if e.sql_diagnostics().is_some() => {
                let text = e.text();
                e.reworded(Kind::Interrupted, text)
            }
            Err(e) if e.kind() == Kind::Interrupted => e,
            _ => Error::new(Kind::Interrupted, "interrupted"),
        });
    }
    runtime.shutdown_timeout(Duration::from_millis(100));
    finish(ctx, &run, &result, &reporter)
}

/// The connection a deploy resolved before reading anything else.
struct Target {
    spec: Spec,
    cloud: Option<(Provider, cloud::Options)>,
}

/// The project as loaded from disk, with its checked metadata.
struct LoadedProject {
    project: walk::Project,
    meta: Vec<metadata::Meta>,
}

/// What every phase of one deploy shares.
struct Steps<'a> {
    ctx: &'a Context,
    guard: &'a Guard,
    reporter: &'a Arc<Reporter>,
    secrets: &'a Secrets,
    retry: retry::Policy,
}

/// What every attempt of one deploy runs with.
struct Plan<'a> {
    dialer: &'a Dialer,
    cloud: Option<&'a (Provider, cloud::Options)>,
    loaded: &'a LoadedProject,
    params: &'a BTreeMap<String, String>,
    contract: ContractVersion,
}

impl Steps<'_> {
    async fn deploy(
        &self,
        st: &mut Progress,
        yaml: Option<&ProjectConfig>,
        contract: Result<ContractVersion>,
        log: &Arc<Mutex<NoticeLog>>,
    ) -> Result<()> {
        let request = &self.ctx.request;
        let Target { mut spec, cloud } = self.target(st, yaml)?;
        self.report_provenance(st, &spec, cloud.as_ref().map(|(p, _)| *p))?;
        let params = self
            .params(yaml.map(ProjectConfig::param_values).unwrap_or_default())
            .await?;
        #[cfg(feature = "testhooks")]
        if self.ctx.env.non_empty("PGMI_TEST_PANIC")?.is_some() {
            test_panic();
        }
        st.phase = Phase::Configured;
        if request.overwrite {
            self.ctx.approver.before_connecting(&spec.target_db)?;
        }
        st.phase = Phase::Deploying;
        if let Some((provider, options)) = &cloud {
            cloud::check_started(*provider, options, &self.ctx.env)?;
        }
        let project_path = request.project.display();
        self.reporter
            .verbose(&format!("Deploying to database \"{}\"", spec.target_db));
        self.reporter
            .verbose(&format!("Source path: {project_path}"));
        self.reporter.verbose(&format!("Scanning {project_path}"));
        let loaded = self.load().await?;
        self.reporter
            .verbose(&format!("Found {} files to load", loaded.files()));
        Self::record_loaded(st, &loaded);
        let contract = contract?;
        let params = params.checked()?;
        if request.overwrite {
            overwrite::refuse_target(&spec.target_db, &spec.maintenance_db)?;
        }
        let dialer = self.dialer(log);
        let maintenance = if request.overwrite {
            if let Some((provider, options)) = &cloud {
                spec.token = Some(self.signed_in(*provider, options, &spec).await?);
            }
            Some(self.overwrite(st, &dialer, &spec).await?)
        } else {
            None
        };
        let plan = Plan {
            dialer: &dialer,
            cloud: cloud.as_ref(),
            loaded: &loaded,
            params: &params,
            contract,
        };
        let outcome = self.attempts(st, &plan, &mut spec).await;
        if let Some(m) = &maintenance {
            overwrite::unlock(m, &spec.target_db).await;
        }
        outcome
    }

    /// Runs the deploy from the start until an attempt succeeds, fails for
    /// good, or the time box ends (record 0025).
    async fn attempts(&self, st: &mut Progress, plan: &Plan<'_>, spec: &mut Spec) -> Result<()> {
        let guard = self.guard;
        let mut last: Option<Error> = None;
        let mut reached = false;
        loop {
            let n = st.attempts.len() + 1;
            st.execution = None;
            let (started, t0) = (SystemTime::now(), Instant::now());
            let mut seen = retry::Seen::default();
            let result = self.attempt(st, plan, spec, &mut seen).await;
            st.attempts.push(Tried {
                started,
                duration: t0.elapsed(),
                committed: st.execution.as_ref().map_or(0, |x| x.committed),
                failure: result.as_ref().err().map(|e| Ended {
                    sqlstate: e.sql_diagnostics().and_then(|d| d.sqlstate.clone()),
                    message: e.text(),
                }),
            });
            let Err(e) = result else { return Ok(()) };
            let expired = tokio::time::Instant::now() >= guard.deadline;
            if e.kind() == Kind::Timeout || (e.kind() == Kind::Connection && expired) {
                return Err(match &last {
                    Some(prev) => retry::used_up(e.kind(), guard.timeout, n, prev),
                    None => e,
                });
            }
            seen.in_test = st.failure().is_some_and(|f| f.blame.phase() == "test");
            reached |= seen.connected;
            let why = if self.retry.enabled && reached && !guard.stop.is_cancelled() {
                retry::transient(&e, seen)
            } else {
                None
            };
            let Some(why) = why else { return Err(e) };
            if let Some(reason) = not_retried(st.execution.as_ref(), self.retry.after_commit) {
                self.reporter.line(&format!(
                    "pgmi: attempt {n} failed ({why}); not retried: {reason}"
                ));
                return Err(e);
            }
            let attempts_left = match self.retry.cap {
                Some(cap) if n >= cap => return Err(retry::gave_up(n, e)),
                Some(cap) => Some(cap - n),
                None => None,
            };
            let wait = retry::backoff(n);
            let remaining = guard
                .deadline
                .saturating_duration_since(tokio::time::Instant::now());
            if wait >= remaining {
                return Err(retry::cut_short(guard.timeout, n, e));
            }
            let left = attempts_left.map_or(
                retry::Left::Time(remaining.saturating_sub(wait)),
                retry::Left::Attempts,
            );
            self.reporter
                .line(&retry::retrying_line(n, &why, wait, left));
            tokio::select! {
                () = tokio::time::sleep(wait) => {}
                () = guard.stop.cancelled() => return Err(guard.stop_error()),
            }
            last = Some(e);
        }
    }

    /// One run of the deploy: sign in when there is no token yet, connect,
    /// lock, load the session and run deploy.sql.
    async fn attempt(
        &self,
        st: &mut Progress,
        plan: &Plan<'_>,
        spec: &mut Spec,
        seen: &mut retry::Seen,
    ) -> Result<()> {
        if let (None, Some((provider, options))) = (&spec.token, plan.cloud) {
            spec.token = Some(self.signed_in(*provider, options, spec).await?);
        }
        let spec = &*spec;
        let session = self.open(st, plan.dialer, spec).await?;
        seen.connected = true;
        let outcome = async {
            self.settle(&session, spec).await?;
            if !self.ctx.request.overwrite {
                self.reporter.line(PREPARING);
            }
            self.lock(&session, &spec.target_db, &mut st.lock_holders)
                .await?;
            let server = st.server.insert(self.server_info(&session, spec).await?);
            execute::run(execute::Input {
                session: &session,
                guard: self.guard,
                role: &spec.user,
                reporter: self.reporter,
                project: &plan.loaded.project,
                meta: &plan.loaded.meta,
                params: plan.params,
                database: &spec.target_db,
                contract: plan.contract,
                tls: &server.tls,
                verbose: self.ctx.verbose,
                files_loaded: plan.loaded.files(),
                execution: &mut st.execution,
            })
            .await
        }
        .await;
        release(&session).await;
        if outcome
            .as_ref()
            .err()
            .and_then(|e| e.sql_diagnostics())
            .and_then(|d| d.sqlstate.as_deref())
            == Some("25006")
        {
            seen.hot_standby = !matches!(
                spec.target_session_attrs,
                TargetSessionAttrs::ReadOnly
                    | TargetSessionAttrs::Standby
                    | TargetSessionAttrs::PreferStandby
            ) && hot_standby(&session).await;
        }
        outcome
    }

    async fn signed_in(
        &self,
        provider: Provider,
        options: &cloud::Options,
        spec: &Spec,
    ) -> Result<cloud::Token> {
        let first = if self.ctx.request.overwrite {
            &spec.maintenance_db
        } else {
            &spec.target_db
        };
        self.sign_in(provider, options).await.map_err(|e| {
            if e.kind() == Kind::Connection {
                let text = format!("connect to \"{first}\": {}", e.text());
                e.reworded(Kind::Connection, text)
            } else {
                e
            }
        })
    }

    fn target(&self, st: &mut Progress, yaml: Option<&ProjectConfig>) -> Result<Target> {
        let (request, env) = (&self.ctx.request, &self.ctx.env);
        let yaml_connection = yaml.map(|y| &y.connection);
        st.connection_source = Some(resolve::source(&request.connection, env, yaml_connection)?);
        let cloud = cloud::select(&request.cloud, &request.cloud_request, env, yaml)?;
        st.overrides = overrides(&request.connection, env, yaml_connection)?;
        let mut spec = resolve::resolve(&request.connection, env, yaml_connection)?;
        for line in resolve::unsupported_env(env)? {
            self.reporter.line(&line);
        }
        if let Some((provider, options)) = &cloud {
            cloud::check(*provider, options, &mut spec, env)?;
        }
        if let Some(m) = &request.maintenance_db {
            spec.maintenance_db.clone_from(m);
        }
        if let Some(allowed) = &self.ctx.serve_hosts
            && let Some(host) =
                restrict_to_allowed(&mut spec, &request.connection, cloud.is_some(), allowed)?
        {
            self.reporter.line(&format!(
                "pgmi serve: \"{host}\" is not a host the operator allowed (its environment's host or PGMI_SERVE_HOSTS), so the operator's password, password file and cloud sign-in are not used"
            ));
        }
        st.spec = Some(spec.clone());
        self.secrets.resolved(&spec);
        Ok(Target { spec, cloud })
    }

    fn report_provenance(
        &self,
        st: &Progress,
        spec: &Spec,
        provider: Option<Provider>,
    ) -> Result<()> {
        if let Some(replaced) = &spec.replaced_db {
            self.reporter.verbose(&format!(
                "Using --database flag ({}) instead of connection string database ({replaced})",
                spec.target_db
            ));
        }
        for line in provenance(
            spec,
            &crate::config::dotenv_path(&self.ctx.request.project),
            &self.ctx.dotenv_keys,
            &self.ctx.dotenv_refused,
            &st.overrides,
            self.ctx.env.non_empty("PGOPTIONS")?.is_some(),
        ) {
            self.reporter.line(&line);
        }
        self.reporter.verbose(&resolved(spec, provider));
        Ok(())
    }

    async fn params(&self, yaml: Vec<(String, String)>) -> Result<params::Collected> {
        let request = &self.ctx.request;
        let (env, files, flags, stdin) = (
            self.ctx.env.clone(),
            request.params_files.clone(),
            request.params.clone(),
            self.ctx.stdin,
        );
        let (notes, verbose) = (self.reporter.clone(), self.ctx.verbose);
        let limit = if files.is_empty() {
            u64::MAX
        } else {
            walk::max_size(&self.ctx.env)?
        };
        let stop = self.guard.stop.token().clone();
        self.guard
            .interruptible(move || {
                let note = |n: String| {
                    if verbose {
                        notes.verbose(&n);
                    }
                };
                let sources = Sources {
                    yaml,
                    env: &env,
                    files: &files,
                    flags: &flags,
                    note: &note,
                };
                let mut process = std::io::stdin();
                let inputs = params::Inputs {
                    files: &OsFs::cwd(),
                    stdin: match stdin {
                        Stdin::Process => Some(&mut process),
                        Stdin::Unavailable => None,
                    },
                    limit,
                    stop: &stop,
                };
                params::collect(sources, inputs)
            })
            .await
    }

    async fn load(&self) -> Result<LoadedProject> {
        let project_path = &self.ctx.request.project;
        let max_size = walk::max_size(&self.ctx.env)?;
        let fs = OsFs::new(project_path.clone());
        let project = self
            .guard
            .blocking(move || walk::load(&fs, max_size))
            .await?;
        let scanned = metadata::scan(&project.sources);
        scanned
            .readable()
            .map_err(walk::scan_failed(Some(&project_path.display().to_string())))?;
        let meta = scanned.unique()?;
        Ok(LoadedProject { project, meta })
    }

    fn record_loaded(st: &mut Progress, loaded: &LoadedProject) {
        st.loaded = Some(Loaded {
            files: loaded.files(),
            ignored: loaded.project.ignored.clone(),
        });
    }

    fn dialer(&self, log: &Arc<Mutex<NoticeLog>>) -> Dialer {
        let (reporter, log, secrets) = (self.reporter.clone(), log.clone(), self.secrets.clone());
        let line_reporter = self.reporter.clone();
        let line_secrets = self.secrets.clone();
        let found = self.secrets.clone();
        let (warn_reporter, warned) = (self.reporter.clone(), log.clone());
        Dialer {
            notices: Arc::new(move |n: Notice| {
                let n = secrets.notice(n);
                reporter.notice(&n);
                log.lock()
                    .unwrap_or_else(std::sync::PoisonError::into_inner)
                    .push(&n);
            }),
            log: Arc::new(move |line: String| {
                line_reporter.line(&redact(&line, &line_secrets.list()));
            }),
            warn: Arc::new(move |line: String| {
                warn_reporter.line(&line);
                warned
                    .lock()
                    .unwrap_or_else(std::sync::PoisonError::into_inner)
                    .warn(line);
            }),
            secret: Arc::new(move |secret: &Secret| found.insert(secret)),
        }
    }

    async fn sign_in(&self, provider: Provider, options: &cloud::Options) -> Result<cloud::Token> {
        let (reporter, verbose, secrets) = (
            self.reporter.clone(),
            self.ctx.verbose,
            self.secrets.clone(),
        );
        let log: crate::conn::dial::LogSink = Arc::new(move |line: String| {
            if verbose {
                reporter.verbose(&redact(&line, &secrets.list()));
            }
        });
        self.guard
            .connecting(Box::pin(cloud::token(
                provider,
                options,
                &self.ctx.env,
                log,
            )))
            .await
    }

    async fn overwrite(&self, st: &mut Progress, dialer: &Dialer, spec: &Spec) -> Result<Session> {
        let (guard, db) = (self.guard, spec.target_db.as_str());
        self.reporter.verbose(&format!(
            "Connecting to maintenance database \"{}\"",
            spec.maintenance_db
        ));
        let maintenance = guard
            .connecting(dialer.connect(spec, &spec.maintenance_db))
            .await?;
        guard
            .running(&maintenance, check_server(&maintenance))
            .await?;
        let exists = guard
            .running(&maintenance, overwrite::claim(&maintenance, db))
            .await?;
        if let Err(e) = self.recreate_claimed(st, &maintenance, db, exists).await {
            overwrite::unlock(&maintenance, db).await;
            return Err(e);
        }
        Ok(maintenance)
    }

    async fn recreate_claimed(
        &self,
        st: &mut Progress,
        maintenance: &Session,
        db: &str,
        exists: bool,
    ) -> Result<()> {
        let guard = self.guard;
        let snapshot = if exists {
            self.reporter
                .verbose(&format!("Database \"{db}\" exists; requesting approval"));
            guard
                .waiting(
                    self.ctx
                        .approver
                        .approve_drop(db, self.reporter, guard.stop.token()),
                )
                .await
                .map_err(|e| match e.kind() {
                    Kind::Interrupted => Error::new(Kind::Interrupted, "overwrite cancelled"),
                    _ => e,
                })?;
            guard
                .running(maintenance, overwrite::snapshot(maintenance, db))
                .await?
                .inspect(|s| {
                    self.reporter.verbose(&format!(
                        "Preserving encoding {}, collation {} across the recreate",
                        s.encoding(),
                        s.collation()
                    ));
                })
        } else {
            None
        };
        guard.check()?;
        overwrite::recreate(
            maintenance,
            db,
            snapshot.as_ref(),
            self.reporter,
            &mut st.provision,
        )
        .await?;
        self.reporter.line(PREPARING);
        guard.check()
    }

    /// Connects to the target database, creating it first through the
    /// maintenance database when the server says it does not exist.
    async fn open(&self, st: &mut Progress, dialer: &Dialer, spec: &Spec) -> Result<Session> {
        let guard = self.guard;
        match guard
            .connecting(dialer.connect(spec, &spec.target_db))
            .await
        {
            Err(e) if e.sql_diagnostics().and_then(|d| d.sqlstate.as_deref()) == Some("3D000") => {}
            opened => {
                if opened.is_ok() && !self.ctx.request.overwrite {
                    self.reporter
                        .verbose(&format!("Database \"{}\" already exists", spec.target_db));
                }
                return opened;
            }
        }
        self.reporter.verbose(&format!(
            "Database \"{}\" not found; using the maintenance database to create it",
            spec.target_db
        ));
        self.reporter.verbose(&format!(
            "Connecting to maintenance database \"{}\"",
            spec.maintenance_db
        ));
        let maintenance = guard
            .connecting(dialer.connect(spec, &spec.maintenance_db))
            .await?;
        guard
            .running(&maintenance, check_server(&maintenance))
            .await?;
        self.reporter.line(&format!(
            "Database \"{}\" does not exist; creating",
            spec.target_db
        ));
        guard
            .running(&maintenance, create_database(&maintenance, &spec.target_db))
            .await?;
        st.provision = Provision::Created;
        self.reporter
            .verbose(&format!("Created database \"{}\"", spec.target_db));
        drop(maintenance);
        guard
            .connecting(dialer.connect(spec, &spec.target_db))
            .await
    }

    async fn settle(&self, session: &Session, spec: &Spec) -> Result<()> {
        let guard = self.guard;
        guard.running(session, check_server(session)).await?;
        guard
            .running(
                session,
                client_encoding(
                    session,
                    spec.client_encoding.as_deref(),
                    &spec.target_db,
                    &format!("{}:{}", spec.first().name, spec.first().port),
                ),
            )
            .await
    }

    async fn server_info(&self, session: &Session, spec: &Spec) -> Result<ServerInfo> {
        let guard = self.guard;
        let version = guard
            .running(session, async {
                Ok(first_value(session, "SHOW server_version")
                    .await
                    .ok()
                    .flatten())
            })
            .await?;
        Ok(ServerInfo {
            version,
            tls: Tls {
                enabled: session.tls_enabled(),
                sslmode: spec.sslmode(),
                version: session.tls_version,
            },
        })
    }

    /// Takes the deploy lock. A holder that is this deploy's own earlier
    /// session, orphaned by a connection the server has not noticed is gone,
    /// is ended so the retry can go on (PGMI-919).
    async fn lock(&self, session: &Session, db: &str, holders: &mut Vec<i32>) -> Result<()> {
        let (mut locked, pid) = self.guard.running(session, try_lock(session)).await?;
        if !locked {
            let holder = self.guard.running(session, lock_holder(session)).await?;
            let Some(earlier) = holder.filter(|h| holders.contains(h)) else {
                return Err(Error::new(
                    Kind::Locked,
                    format!("another pgmi deployment is already running against \"{db}\""),
                ));
            };
            self.reporter.line(&format!(
                "pgmi: the deploy lock is held by this deploy's earlier session (pid {earlier}), which the server has not noticed is gone; ending it"
            ));
            locked = self
                .guard
                .running(session, take_from(session, earlier))
                .await
                .map_err(|e| {
                    if e.kind() != Kind::Sql {
                        return e;
                    }
                    Error::new(
                        Kind::Locked,
                        format!(
                            "the deploy lock against \"{db}\" is held by this deploy's earlier session (pid {earlier}), which pgmi could not end: {}",
                            e.text()
                        ),
                    )
                })?;
            if !locked {
                return Err(Error::new(
                    Kind::Locked,
                    format!(
                        "the deploy lock against \"{db}\" is still held by this deploy's earlier session (pid {earlier}) after pgmi ended it"
                    ),
                ));
            }
        }
        holders.push(pid);
        Ok(())
    }
}

const LOCK_KEY: &str =
    "pg_catalog.hashtextextended('pgmi.deploy.' || pg_catalog.current_database(), 0)";

async fn try_lock(session: &Session) -> Result<(bool, i32)> {
    let sql =
        format!("SELECT pg_catalog.pg_try_advisory_lock({LOCK_KEY}), pg_catalog.pg_backend_pid()");
    let rows = session
        .client
        .query_typed(&sql, &[])
        .await
        .map_err(|e| sql_error("query the server", e))?;
    let row = rows
        .first()
        .ok_or_else(|| Error::new(Kind::Sql, "the lock query returned no row"))?;
    let locked = row
        .try_get(0)
        .map_err(|e| sql_error("query the server", e))?;
    let pid = row
        .try_get(1)
        .map_err(|e| sql_error("query the server", e))?;
    Ok((locked, pid))
}

async fn lock_holder(session: &Session) -> Result<Option<i32>> {
    let sql = format!(
        "SELECT l.pid FROM pg_catalog.pg_locks l \
         WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1 \
         AND l.database = (SELECT d.oid FROM pg_catalog.pg_database d WHERE d.datname = pg_catalog.current_database()) \
         AND ((l.classid::pg_catalog.int8 << 32) | l.objid::pg_catalog.int8) = {LOCK_KEY}"
    );
    let rows = session
        .client
        .query_typed(&sql, &[])
        .await
        .map_err(|e| sql_error("query the server", e))?;
    rows.first()
        .map(|r| r.try_get(0))
        .transpose()
        .map_err(|e| sql_error("query the server", e))
}

const TAKE_TRIES: u32 = 50;
const TAKE_PAUSE: Duration = Duration::from_millis(200);

async fn take_from(session: &Session, earlier: i32) -> Result<bool> {
    session
        .client
        .query_typed(
            "SELECT pg_catalog.pg_terminate_backend($1)",
            &[(&earlier, tokio_postgres::types::Type::INT4)],
        )
        .await
        .map_err(|e| sql_error("end the earlier session", e))?;
    for _ in 0..TAKE_TRIES {
        if try_lock(session).await?.0 {
            return Ok(true);
        }
        tokio::time::sleep(TAKE_PAUSE).await;
    }
    Ok(false)
}

impl LoadedProject {
    fn files(&self) -> usize {
        self.project.sources.len() + self.project.test_dir_files.len()
    }
}

#[cfg(feature = "testhooks")]
#[allow(clippy::panic)]
fn test_panic() -> ! {
    panic!("PGMI_TEST_PANIC is set")
}

/// A deploy stopped before it started: the stderr line and the `--json`
/// record any later refusal gets.
pub(crate) fn refused(ctx: &Context, e: Error) -> u8 {
    finish(
        ctx,
        &Run::start(),
        &Err(e),
        &Reporter::new(ctx.verbose, &ctx.env),
    )
}

fn finish(ctx: &Context, run: &Run, result: &Result<()>, reporter: &Reporter) -> u8 {
    let st = &run.st;
    let elapsed = run.t0.elapsed();
    let code = match result {
        Ok(()) => 0,
        Err(e) => e.exit_code(),
    };
    let secrets = run.secrets();
    if let Some(s) = summary(st, result, elapsed) {
        reporter.summary(&s);
    }
    if ctx.verbose && !ctx.request.json && st.server.is_some() {
        for i in st.loaded.iter().flat_map(|l| &l.ignored) {
            reporter.line(&format!("  ignored {} ({})", i.path, i.rule));
        }
    }
    let note = result.as_ref().err().and_then(|e| st.connection_note(e));
    if let Err(e) = result {
        let note = note
            .as_deref()
            .map(|n| format!("\n{n}"))
            .unwrap_or_default();
        let first = st
            .attempts
            .first()
            .filter(|_| st.attempts.len() > 1)
            .and_then(|a| a.failure.as_ref())
            .map(|f| {
                format!(
                    "\nattempt 1 of {} failed with: {}",
                    st.attempts.len(),
                    f.message
                )
            })
            .unwrap_or_default();
        let text = format!(
            "pgmi: error: {}{note}{}{first}",
            e.text(),
            run.diagnostics(e)
        );
        reporter.line(&redact(&blame::redact_sql_passwords(&text), &secrets));
    }
    if ctx.request.json {
        let mut record = run.record(ctx, result);
        if let Some(n) = note.filter(|_| !st.reached(Phase::Deploying)) {
            record.note_error(&n);
        }
        crate::output::drain();
        let mut out = std::io::stdout().lock();
        let text = serde_json::to_string_pretty(&record).unwrap_or_default();
        if let Err(e) = writeln!(out, "{text}").and_then(|()| out.flush()) {
            reporter.line(&format!(
                "pgmi: error: could not write the --json record: {e}"
            ));
            if code == 0 {
                return Kind::General.exit_code();
            }
        }
    }
    code
}

fn summary(p: &Progress, result: &Result<()>, elapsed: Duration) -> Option<Summary> {
    if !p.reached(Phase::Configured) {
        return None;
    }
    let db = p.database()?;
    if result.is_ok() {
        let (files, ignored) = p
            .loaded
            .as_ref()
            .map_or((0, 0), |l| (l.files, l.ignored.len()));
        let ignored = match ignored {
            0 => String::new(),
            n => format!(", {n} paths ignored"),
        };
        return Some(Summary::Done(format!(
            "{db}: {files} files loaded{ignored} in {}",
            seconds(elapsed)
        )));
    }
    let mut text = format!("{db}: failed after {}", seconds(elapsed));
    let committed = p.execution.as_ref().map_or(0, |x| x.committed);
    let earlier: Vec<String> = p
        .attempts
        .iter()
        .enumerate()
        .take(p.attempts.len().saturating_sub(1))
        .filter(|(_, a)| a.committed > 0)
        .map(|(i, a)| format!("attempt {} committed {} unit(s)", i + 1, a.committed))
        .collect();
    let mut notes = earlier.clone();
    if let Some(x) = &p.execution {
        match &x.failure {
            Some(f) if f.unknown.is_some() => {
                if committed > 0 {
                    let _ = write!(text, " ({committed} earlier unit(s) committed)");
                }
                notes.push(match f.unknown {
                    Some(Unknown::CommitsInside) => format!(
                        "unit {} of {} is a DO block or CALL, which may have committed before it failed; check the database",
                        f.unit, x.units
                    ),
                    Some(Unknown::Prepared) => format!(
                        "unit {} of {} ran PREPARE TRANSACTION; check pg_prepared_xacts, then COMMIT PREPARED or ROLLBACK PREPARED",
                        f.unit, x.units
                    ),
                    _ => format!(
                        "unit {} of {} got no answer from the server; whether it committed is unknown, check the database",
                        f.unit, x.units
                    ),
                });
            }
            Some(f) if committed > 0 => {
                let _ = write!(
                    text,
                    " (unit {} of {} failed; {committed} earlier unit(s) already committed)",
                    f.unit, x.units
                );
            }
            Some(_) if earlier.is_empty() => notes.push("nothing committed".to_string()),
            Some(_) => notes.push("the last attempt committed nothing".to_string()),
            None if committed > 0 => {
                let _ = write!(text, " (all {committed} unit(s) committed before the stop)");
            }
            None => {}
        }
    }
    let verb = match p.provision {
        Provision::Existing => None,
        Provision::Created => Some("created"),
        Provision::Recreated => Some("recreated"),
    };
    if let Some(verb) = verb.filter(|_| p.loaded.is_some()) {
        let unknown = p
            .execution
            .as_ref()
            .and_then(|x| x.failure.as_ref())
            .is_some_and(|f| f.unknown.is_some());
        let left = if committed == 0 && earlier.is_empty() && !unknown {
            ", left empty"
        } else {
            ""
        };
        notes.push(format!("{verb} database \"{db}\"{left}"));
    }
    if !notes.is_empty() {
        let _ = write!(text, " — {}", notes.join("; "));
    }
    Some(Summary::Failed(text))
}

/// Why an attempt that failed with a retried error ends the deploy instead:
/// a unit whose reply was lost (it may have committed), or committed units
/// without `retry_after_commit`.
fn not_retried(execution: Option<&Execution>, after_commit: bool) -> Option<String> {
    let x = execution?;
    if let Some(why) = x.failure.as_ref().and_then(|f| f.why_unknown(x.units)) {
        return Some(why);
    }
    (x.committed > 0 && !after_commit).then(|| {
        format!(
            "{} unit(s) already committed (pgmi.yaml retry_after_commit: true allows it)",
            x.committed
        )
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn notice_counts_survive_a_panic_while_the_log_was_held() {
        let run = Run::start();
        {
            let mut log = run.log();
            log.total = 3;
            log.warning_total = 1;
        }
        let log = run.log.clone();
        let _ = std::thread::spawn(move || {
            let _held = log.lock().unwrap();
            panic!("a notice sink failed");
        })
        .join();
        assert!(run.log.is_poisoned());
        assert_eq!(
            run.notice_counts(),
            NoticeCounts {
                notices: 3,
                warnings: 1
            }
        );
    }
    use serde_json::{Value, json};

    impl Run {
        fn at(&mut self, p: Progress) {
            if let Some(spec) = &p.spec {
                self.secrets.resolved(spec);
            }
            self.st = p;
        }
    }

    fn guard(timeout: Duration) -> Guard {
        Guard {
            stop: Stop::default(),
            deadline: deadline(timeout),
            timeout,
        }
    }

    async fn stuck(guard: &Guard) -> Error {
        let (release, wait) = std::sync::mpsc::channel::<()>();
        let stopped = tokio::time::timeout(
            Duration::from_secs(5),
            guard.blocking(move || {
                let _ = wait.recv();
                Ok(())
            }),
        )
        .await;
        drop(release);
        stopped
            .expect("blocking work must not hold up a stop")
            .unwrap_err()
    }

    #[tokio::test]
    async fn blocking_work_gives_way_to_the_first_signal() {
        let g = guard(Duration::from_secs(60));
        g.stop.signal();
        let e = stuck(&g).await;
        assert_eq!((e.exit_code(), e.text()), (130, "interrupted".to_string()));
    }

    #[tokio::test]
    async fn reading_stdin_gives_way_to_the_first_signal_but_not_to_the_timeout() {
        let g = guard(Duration::from_millis(20));
        let (release, wait) = std::sync::mpsc::channel::<()>();
        let read = g.interruptible(move || {
            let _ = wait.recv();
            Ok(())
        });
        tokio::pin!(read);
        assert!(
            tokio::time::timeout(Duration::from_millis(200), &mut read)
                .await
                .is_err(),
            "--timeout must not stop the stdin read"
        );
        g.stop.signal();
        let e = tokio::time::timeout(Duration::from_secs(5), read)
            .await
            .expect("a signal must stop the stdin read")
            .unwrap_err();
        drop(release);
        assert_eq!((e.exit_code(), e.text()), (130, "interrupted".to_string()));
    }

    #[tokio::test]
    async fn blocking_work_gives_way_to_the_timeout() {
        let e = stuck(&guard(Duration::from_millis(50))).await;
        assert_eq!(e.exit_code(), 16);
        assert_eq!(
            e.text(),
            "the deploy exceeded --timeout 50ms; raise it, or pass --timeout 0 for no limit"
        );
    }

    #[test]
    fn a_cancelled_deploy_is_not_a_timeout() {
        let guard = Guard {
            stop: Stop::default(),
            deadline: deadline(Duration::from_secs(60)),
            timeout: Duration::from_secs(60),
        };
        guard.stop.cancel();
        let e = guard.stop_error();
        assert!(!e.text().contains("--timeout"), "{}", e.text());
        assert_eq!(e.text(), "the deploy was cancelled");
        let signalled = Guard {
            stop: Stop::default(),
            deadline: deadline(Duration::from_secs(60)),
            timeout: Duration::from_secs(60),
        };
        signalled.stop.signal();
        assert_eq!(signalled.stop_error().text(), "interrupted");
    }

    #[tokio::test]
    async fn a_server_answering_fewer_columns_is_an_error_not_a_panic() {
        use crate::conn::dial::fake;
        let (listener, port) = fake::listen().await;
        tokio::spawn(fake::one_column(listener, "180000"));
        let spec = fake::spec_for(&format!(
            "host=127.0.0.1 port={port} user=u dbname=d sslmode=disable"
        ));
        let session = fake::quiet().connect(&spec, "d").await.unwrap();
        let e = check_server(&session).await.unwrap_err();
        assert_eq!(e.exit_code(), 13, "{}", e.text());
        assert!(
            e.text().starts_with("read the server version: "),
            "{}",
            e.text()
        );
        assert_eq!(
            first_value(&session, "SELECT 1").await.unwrap().as_deref(),
            Some("180000")
        );
    }

    #[test]
    fn a_password_the_dialer_found_is_redacted_like_the_specs() {
        let secrets = Secrets::default();
        secrets.resolved(&spec("d"));
        secrets.insert(&"from-pgpass".into());
        let n = secrets.notice(Notice {
            severity: crate::output::Severity::Notice,
            message: "a=from-pgpass b=s3cret-pw".into(),
            detail: Some("from-pgpass".into()),
            hint: None,
        });
        assert_eq!(n.message, "a=*** b=***");
        assert_eq!(n.detail.as_deref(), Some("***"));
        let run = Run {
            secrets,
            ..Run::start()
        };
        assert!(run.secrets().iter().any(|s| s.expose() == "from-pgpass"));
    }

    fn spec(db: &str) -> Spec {
        let flags = resolve::Flags {
            connection: Some(format!("postgresql://u:s3cret-pw@db.example:6543/{db}").into()),
            ..resolve::Flags::default()
        };
        resolve::resolve(&flags, &Env::default(), None).unwrap()
    }

    #[test]
    fn the_resolved_block_names_cert_paths_and_never_the_password() {
        let mut s = spec("app");
        s.sslcert = Some("/c.pem".into());
        let text = resolved(&s, Some(Provider::Aws));
        assert_eq!(
            text,
            "Connection resolved:\n  Host: db.example\n  Port: 6543\n  User: u\n  Target Database: app\n  Maintenance Database: postgres\n  SSL Mode: prefer\n  SSL Cert: /c.pem\n  Auth Method: AWS IAM"
        );
    }

    #[test]
    fn a_database_flag_names_the_strings_database_it_replaced() {
        let flags = resolve::Flags {
            connection: Some("postgresql://u@h/base".to_string().into()),
            database: Some("app".into()),
            ..resolve::Flags::default()
        };
        let s = resolve::resolve(&flags, &Env::default(), None).unwrap();
        assert_eq!(s.replaced_db.as_deref(), Some("base"));
        assert_eq!(spec("app").replaced_db, None);
    }

    fn loaded(db: &str) -> Progress {
        Progress {
            contract: Some(ContractVersion::V1),
            connection_source: Some(Source::ConnectionFlag),
            spec: Some(spec(db)),
            phase: Phase::Deploying,
            loaded: Some(Loaded {
                files: 3,
                ignored: Vec::new(),
            }),
            ..Progress::default()
        }
    }

    fn executed(committed: usize, failure: Option<Failure>) -> Option<Execution> {
        Some(Execution {
            test_macros: 1,
            units: 3,
            committed,
            failure,
        })
    }

    fn failure(unit: usize, blame: blame::Blame) -> Option<Failure> {
        Some(Failure {
            unit,
            blame,
            unknown: None,
        })
    }

    fn in_file(files: &[&str], line: Option<blame::Line>) -> blame::Blame {
        blame::Blame {
            candidates: files.iter().map(ToString::to_string).collect(),
            file: files.first().map(ToString::to_string),
            location: line.map(|l| blame::Location {
                script: files[0].to_string(),
                line: Some(l),
            }),
            where_: Some(format!("SQL statement in {}", files[0])),
        }
    }

    fn in_deploy_sql() -> blame::Blame {
        blame::Blame {
            file: Some("deploy.sql".into()),
            location: Some(blame::Location {
                script: "deploy.sql".into(),
                line: None,
            }),
            ..blame::Blame::default()
        }
    }

    fn summary_of(p: &Progress, result: &Result<()>) -> Option<Summary> {
        summary(p, result, Duration::from_millis(1234))
    }

    #[test]
    fn the_summary_counts_files_and_ignored_paths() {
        assert_eq!(
            summary_of(&loaded("app"), &Ok(())),
            Some(Summary::Done("app: 3 files loaded in 1.23s".into()))
        );
        let ignored = |path: &str| walk::Ignored {
            path: path.into(),
            rule: "built-in",
        };
        let p = Progress {
            loaded: Some(Loaded {
                files: 3,
                ignored: vec![ignored("a"), ignored("b")],
            }),
            ..loaded("app")
        };
        assert_eq!(
            summary_of(&p, &Ok(())).unwrap().text(),
            "app: 3 files loaded, 2 paths ignored in 1.23s"
        );
        assert_eq!(summary_of(&Progress::default(), &Ok(())), None);
    }

    #[test]
    fn a_failed_summary_says_what_was_committed() {
        let sql = Err(Error::sql("boom"));
        let atomic = Progress {
            execution: executed(0, failure(1, blame::Blame::default())),
            ..loaded("app")
        };
        assert_eq!(
            summary_of(&atomic, &sql),
            Some(Summary::Failed(
                "app: failed after 1.23s — nothing committed".into()
            ))
        );
        for stopped in [
            Error::new(Kind::Timeout, "the deploy exceeded --timeout 2s"),
            Error::new(Kind::Interrupted, "interrupted"),
        ] {
            assert_eq!(
                summary_of(&atomic, &Err(stopped)).unwrap().text(),
                "app: failed after 1.23s — nothing committed"
            );
        }
        let psql = Progress {
            execution: executed(2, failure(3, blame::Blame::default())),
            ..loaded("app")
        };
        assert_eq!(
            summary_of(&psql, &sql).unwrap().text(),
            "app: failed after 1.23s (unit 3 of 3 failed; 2 earlier unit(s) already committed)"
        );
        let created = Progress {
            provision: Provision::Created,
            ..loaded("app")
        };
        assert_eq!(
            summary_of(&created, &Err(Error::connection("refused")))
                .unwrap()
                .text(),
            "app: failed after 1.23s — created database \"app\", left empty"
        );
        let recreated = Progress {
            provision: Provision::Recreated,
            ..loaded("app")
        };
        assert_eq!(
            summary_of(
                &recreated,
                &Err(Error::new(Kind::Interrupted, "interrupted"))
            )
            .unwrap()
            .text(),
            "app: failed after 1.23s — recreated database \"app\", left empty"
        );
        let created_then_sql = Progress {
            execution: executed(0, failure(1, blame::Blame::default())),
            ..created
        };
        assert_eq!(
            summary_of(&created_then_sql, &sql).unwrap().text(),
            "app: failed after 1.23s — nothing committed; created database \"app\", left empty"
        );
    }

    #[test]
    fn a_lost_reply_is_never_retried_and_committed_units_only_with_the_opt_in() {
        let lost = |committed| {
            executed(
                committed,
                Some(Failure {
                    unit: 2,
                    blame: blame::Blame::default(),
                    unknown: Some(Unknown::NoAnswer),
                }),
            )
        };
        for after_commit in [false, true] {
            for committed in [0, 1] {
                assert_eq!(
                    not_retried(lost(committed).as_ref(), after_commit).as_deref(),
                    Some(
                        "unit 2 of 3 got no answer from the server, so whether it committed is unknown"
                    )
                );
            }
        }
        let inside = executed(
            0,
            Some(Failure {
                unit: 1,
                blame: blame::Blame::default(),
                unknown: Some(Unknown::CommitsInside),
            }),
        );
        for after_commit in [false, true] {
            assert_eq!(
                not_retried(inside.as_ref(), after_commit).as_deref(),
                Some(
                    "unit 1 of 3 is a DO block or CALL, which may have committed before it failed"
                )
            );
        }
        let refused = executed(1, failure(2, blame::Blame::default()));
        assert_eq!(
            not_retried(refused.as_ref(), false).as_deref(),
            Some("1 unit(s) already committed (pgmi.yaml retry_after_commit: true allows it)")
        );
        assert_eq!(not_retried(refused.as_ref(), true), None);
        assert_eq!(
            not_retried(
                executed(0, failure(1, blame::Blame::default())).as_ref(),
                false
            ),
            None
        );
        assert_eq!(not_retried(None, false), None);
    }

    #[test]
    fn a_unit_whose_reply_never_came_is_not_reported_as_rolled_back() {
        let lost = Err(Error::new(
            Kind::Timeout,
            "the deploy exceeded --timeout 4s",
        ));
        let head = Progress {
            execution: Some(Execution {
                test_macros: 0,
                units: 1,
                committed: 0,
                failure: Some(Failure {
                    unit: 1,
                    blame: blame::Blame::default(),
                    unknown: Some(Unknown::NoAnswer),
                }),
            }),
            ..loaded("app")
        };
        assert_eq!(
            summary_of(&head, &lost).unwrap().text(),
            "app: failed after 1.23s — unit 1 of 1 got no answer from the server; whether it committed is unknown, check the database"
        );
        let tail = Progress {
            execution: executed(
                1,
                Some(Failure {
                    unit: 2,
                    blame: blame::Blame::default(),
                    unknown: Some(Unknown::NoAnswer),
                }),
            ),
            ..loaded("app")
        };
        assert_eq!(
            summary_of(&tail, &lost).unwrap().text(),
            "app: failed after 1.23s (1 earlier unit(s) committed) — unit 2 of 3 got no answer from the server; whether it committed is unknown, check the database"
        );
    }

    #[test]
    fn a_stop_after_every_unit_committed_says_so() {
        let p = Progress {
            execution: executed(3, None),
            ..loaded("app")
        };
        assert_eq!(
            summary_of(&p, &Err(Error::new(Kind::Interrupted, "interrupted")))
                .unwrap()
                .text(),
            "app: failed after 1.23s (all 3 unit(s) committed before the stop)"
        );
    }

    #[test]
    fn a_retried_run_still_reports_what_an_earlier_attempt_committed() {
        let tried = |committed: usize| Tried {
            started: SystemTime::UNIX_EPOCH,
            duration: Duration::ZERO,
            committed,
            failure: Some(Ended {
                sqlstate: Some("55P03".into()),
                message: "ERROR: lock busy (SQLSTATE 55P03)".into(),
            }),
        };
        let p = Progress {
            execution: executed(0, failure(1, blame::Blame::default())),
            attempts: vec![tried(1), tried(0)],
            ..loaded("app")
        };
        assert_eq!(
            summary_of(&p, &Err(Error::sql("boom"))).unwrap().text(),
            "app: failed after 1.23s — attempt 1 committed 1 unit(s); the last attempt committed nothing"
        );
    }

    fn notice(severity: &str, message: &str) -> Notice {
        Notice {
            severity: severity.into(),
            message: message.into(),
            detail: None,
            hint: None,
        }
    }

    #[test]
    fn the_record_lists_only_warnings_for_a_text_only_client() {
        let mut run = Run::start();
        run.st = loaded("app");
        {
            let mut log = run.log.lock().unwrap();
            log.push(&notice("NOTICE", "  + tests passed"));
            log.push(&notice("WARNING", "a default password"));
            log.push(&notice("WARNING", "another"));
        }
        let record = run.record(&context(), &Ok(()));
        assert_eq!(
            record.warning_lines(),
            "WARNING: a default password\nWARNING: another"
        );
        assert_eq!(Run::start().record(&context(), &Ok(())).warning_lines(), "");
    }

    #[test]
    fn counters_are_null_before_the_deploy_and_zero_once_it_began() {
        let refused = Err(Error::config(
            "unsupported API version \"99\"; supported: 1",
        ));
        let before = serde_json::to_value(Run::start().record(&context(), &refused)).unwrap();
        for key in [
            "durationMs",
            "created",
            "recreated",
            "filesLoaded",
            "pathsIgnored",
            "testMacros",
        ] {
            assert_eq!(before[key], Value::Null, "{key}");
        }
        let mut run = Run::start();
        run.st = Progress {
            spec: Some(spec("app")),
            connection_source: Some(Source::ConnectionFlag),
            phase: Phase::Deploying,
            ..Progress::default()
        };
        let began = serde_json::to_value(run.record(&context(), &refused)).unwrap();
        assert_eq!(began["contractVersion"], Value::Null);
        assert_eq!(began["connectionSource"], "--connection");
        assert_eq!(began["target"]["database"], "app");
        for (key, want) in [
            ("created", json!(false)),
            ("recreated", json!(false)),
            ("filesLoaded", json!(0)),
            ("pathsIgnored", json!(0)),
            ("testMacros", json!(0)),
        ] {
            assert_eq!(began[key], want, "{key}");
        }
        assert!(began["durationMs"].is_u64());
        assert_eq!(began["database"], "app");
        let text = serde_json::to_string(&run.record(&context(), &refused)).unwrap();
        assert!(
            text.contains(r#""target":{"host":"#) && text.contains(r#","database":"app"}"#),
            "{text}"
        );
        run.st.phase = Phase::Configured;
        let configured = serde_json::to_value(run.record(&context(), &refused)).unwrap();
        assert_eq!(
            (&configured["target"], &configured["database"]),
            (&Value::Null, &Value::Null)
        );
    }

    #[test]
    fn a_stopped_statement_keeps_the_server_report_under_the_stop_kind() {
        let server = Error::sql("ERROR: canceling statement due to user request (SQLSTATE 57014)")
            .with_sql(SqlDiagnostics {
                sqlstate: Some("57014".into()),
                where_: Some("SQL statement in ./a.sql".into()),
                ..SqlDiagnostics::default()
            });
        let e = server.reworded(Kind::Timeout, "the deploy exceeded --timeout 2s");
        assert_eq!(e.exit_code(), 16);
        assert_eq!(e.text(), "the deploy exceeded --timeout 2s");
        let d = e.sql_diagnostics().unwrap();
        assert_eq!(d.sqlstate.as_deref(), Some("57014"));
        assert_eq!(d.where_.as_deref(), Some("SQL statement in ./a.sql"));
    }

    fn context() -> Context {
        Context {
            request: Request::default(),
            verbose: false,
            env: Env::default(),
            dotenv_keys: vec!["PGPASSWORD".into()],
            dotenv_refused: Vec::new(),
            serve_hosts: None,
            stdin: Stdin::Unavailable,
            approver: Box::new(ConfirmedName(None)),
        }
    }

    #[test]
    fn the_error_line_never_shows_the_password() {
        let mut run = Run::start();
        run.st = Progress {
            execution: executed(0, failure(1, blame::Blame::default())),
            ..loaded("app")
        };
        run.secrets.resolved(run.st.spec.as_ref().unwrap());
        let (reporter, out) = crate::output::Captured::reporter(false, crate::output::Style::Plain);
        let code = finish(
            &context(),
            &run,
            &Err(Error::sql("password s3cret-pw rejected")),
            &reporter,
        );
        assert_eq!(code, 13);
        let text = out.text();
        let lines: Vec<&str> = text.lines().collect();
        assert!(lines[0].starts_with("FAILED app: failed after "), "{text}");
        assert_eq!(lines[1], "pgmi: error: password *** rejected");
        assert_eq!(lines.len(), 2, "{text}");
    }

    #[test]
    fn the_error_is_followed_by_its_diagnostics_and_the_line_it_points_at() {
        let mut run = Run::start();
        let line = blame::Line {
            number: 2,
            column: 8,
            text: "CREATE ROLE r PASSWORD 'pw'; SELEC 1;".into(),
        };
        run.st = Progress {
            execution: executed(0, failure(1, in_file(&["./a.sql", "./b.sql"], Some(line)))),
            ..loaded("app")
        };
        let (reporter, out) = crate::output::Captured::reporter(false, crate::output::Style::Plain);
        finish(&context(), &run, &Err(sql_failure("boom")), &reporter);
        let text = out.text();
        let lines: Vec<&str> = text.lines().skip(1).collect();
        assert_eq!(
            lines,
            [
                "pgmi: error: boom",
                "DETAIL: d",
                "HINT: h",
                "WHERE: SQL statement in ./a.sql",
                "LOCATION: ./a.sql line 2, column 8",
                "LINE 2: CREATE ROLE r PASSWORD [redacted]; SELEC 1;",
                "               ^",
                "The same text is in ./a.sql, ./b.sql; the error is in whichever of them deploy.sql ran.",
            ],
            "{text}"
        );
    }

    fn sql_failure(message: &str) -> Error {
        Error::sql(message).with_sql(crate::error::SqlDiagnostics {
            sqlstate: Some("22012".into()),
            detail: Some("d".into()),
            hint: Some("h".into()),
            where_: Some("w".into()),
            ..crate::error::SqlDiagnostics::default()
        })
    }

    #[test]
    fn the_record_carries_pgmis_own_warnings_and_omits_the_key_without_any() {
        assert!(
            record_of(Progress::default(), &Ok(()))
                .get("warnings")
                .is_none()
        );
        let run = Run::start();
        run.log().warn("WARNING: plaintext".into());
        let record = serde_json::to_value(run.record(&context(), &Ok(()))).unwrap();
        assert_eq!(
            record["warnings"],
            serde_json::json!(["WARNING: plaintext"])
        );
    }

    fn record_of(p: Progress, result: &Result<()>) -> Value {
        let mut run = Run::start();
        run.at(p);
        let record = run.record(&context(), result);
        let text = serde_json::to_string(&record).unwrap();
        let sorted = serde_json::to_value(&record).unwrap();
        let mut expected = sorted.to_string();
        if let Some(t) = sorted.get("target").filter(|t| t.is_object()) {
            let go = format!(
                r#"{{"host":{},"port":{},"database":{}}}"#,
                t["host"], t["port"], t["database"]
            );
            expected = expected.replace(&t.to_string(), &go);
        }
        assert_eq!(
            text, expected,
            "keys must stay in byte order, target's in Go's order"
        );
        sorted
    }

    #[test]
    fn a_record_before_anything_ran_is_mostly_null() {
        let r = record_of(Progress::default(), &Err(Error::config("bad")));
        let keys: Vec<&str> = r.as_object().unwrap().keys().map(String::as_str).collect();
        assert_eq!(
            keys,
            [
                "connectionSource",
                "contractVersion",
                "created",
                "database",
                "dotenvKeys",
                "durationMs",
                "error",
                "exitCode",
                "filesLoaded",
                "finishedAt",
                "ignoredPaths",
                "notices",
                "overrides",
                "pathsIgnored",
                "pgmiVersion",
                "recreated",
                "schemaVersion",
                "serverVersion",
                "startedAt",
                "status",
                "target",
                "testMacros",
                "tls"
            ]
        );
        for key in [
            "connectionSource",
            "contractVersion",
            "created",
            "database",
            "filesLoaded",
            "pathsIgnored",
            "recreated",
            "serverVersion",
            "target",
            "testMacros",
            "tls",
        ] {
            assert!(r[key].is_null(), "{key}: {}", r[key]);
        }
        assert_eq!(r["status"], "failed");
        assert_eq!(r["exitCode"], 10);
        assert_eq!(r["schemaVersion"], 1);
        assert_eq!(r["ignoredPaths"], json!([]));
        assert_eq!(r["overrides"], json!([]));
        assert_eq!(r["notices"], json!([]));
        assert_eq!(r["dotenvKeys"], json!(["PGPASSWORD"]));
    }

    #[test]
    fn the_record_lists_every_attempt_and_the_time_used_with_secrets_hidden() {
        let at = SystemTime::UNIX_EPOCH + Duration::from_hours(500_000);
        let p = Progress {
            attempts: vec![
                Tried {
                    started: at,
                    duration: Duration::from_millis(1210),
                    committed: 0,
                    failure: Some(Ended {
                        sqlstate: Some("40P01".into()),
                        message: "ERROR: deadlock near s3cret-pw (SQLSTATE 40P01)".into(),
                    }),
                },
                Tried {
                    started: at + Duration::from_secs(2),
                    duration: Duration::from_millis(830),
                    committed: 0,
                    failure: None,
                },
            ],
            time_used: Some(Duration::from_millis(3050)),
            ..loaded("app")
        };
        let r = record_of(p, &Ok(()));
        assert_eq!(
            r["attempts"],
            json!([
                {
                    "durationMs": 1210,
                    "message": "ERROR: deadlock near *** (SQLSTATE 40P01)",
                    "sqlstate": "40P01",
                    "startedAt": "2027-01-15T08:00:00.000Z"
                },
                {"durationMs": 830, "startedAt": "2027-01-15T08:00:02.000Z"}
            ])
        );
        assert_eq!(r["timeUsedMs"], 3050);
        let none = record_of(loaded("app"), &Ok(()));
        assert!(none.get("attempts").is_none() && none.get("timeUsedMs").is_none());
    }

    #[test]
    fn a_successful_record_carries_the_phases_it_passed() {
        let p = Progress {
            provision: Provision::Recreated,
            server: Some(ServerInfo {
                version: Some("18.1".into()),
                tls: Tls {
                    enabled: true,
                    sslmode: crate::conn::resolve::SslMode::VerifyFull,
                    version: Some(crate::conn::tls::TlsVersion(
                        rustls::ProtocolVersion::TLSv1_3,
                    )),
                },
            }),
            execution: executed(3, None),
            ..loaded("app")
        };
        let r = record_of(p, &Ok(()));
        assert_eq!(r["status"], "success");
        assert_eq!(r["exitCode"], 0);
        assert_eq!(r["contractVersion"], "1");
        assert_eq!(r["database"], "app");
        assert_eq!(
            r["target"],
            json!({"database": "app", "host": "db.example", "port": 6543})
        );
        assert_eq!(r["connectionSource"], "--connection");
        assert_eq!(
            (r["created"].clone(), r["recreated"].clone()),
            (json!(false), json!(true))
        );
        assert_eq!(r["filesLoaded"], 3);
        assert_eq!(r["pathsIgnored"], 0);
        assert_eq!(r["testMacros"], 1);
        assert_eq!(r["executionUnits"], 3);
        assert_eq!(r["unitsCommitted"], 3);
        assert_eq!(r["serverVersion"], "18.1");
        assert_eq!(
            r["tls"],
            json!({"enabled": true, "sslmode": "verify-full", "version": "TLS 1.3"})
        );
        for key in ["error", "executionMode", "phase", "failedFile", "sqlstate"] {
            assert!(r.get(key).is_none(), "{key}");
        }
    }

    #[test]
    fn a_failure_record_names_the_file_mode_and_phase() {
        let line = blame::Line {
            number: 3,
            column: 7,
            text: "SELECT x PASSWORD 'pw'".into(),
        };
        let p = Progress {
            execution: executed(2, failure(3, in_file(&["./a.sql", "./b.sql"], Some(line)))),
            ..loaded("app")
        };
        let r = record_of(p, &Err(sql_failure("division by zero, password s3cret-pw")));
        assert_eq!(r["executionMode"], "psql");
        assert_eq!(r["phase"], "deploy");
        assert_eq!(r["failedFile"], "./a.sql");
        assert_eq!(r["script"], "./a.sql");
        assert_eq!(r["failedFileCandidates"], json!(["./a.sql", "./b.sql"]));
        assert_eq!(r["error"], "division by zero, password ***");
        assert_eq!(
            [
                &r["line"],
                &r["column"],
                &r["scriptExpanded"],
                &r["sourceLine"]
            ],
            [
                &json!(3),
                &json!(7),
                &json!(false),
                &json!("SELECT x PASSWORD [redacted]")
            ]
        );
        assert_eq!(
            [&r["sqlstate"], &r["detail"], &r["hint"], &r["where"]],
            [
                &json!("22012"),
                &json!("d"),
                &json!("h"),
                &json!("SQL statement in ./a.sql")
            ]
        );

        let p = Progress {
            execution: executed(0, failure(1, in_file(&["./only.sql"], None))),
            ..loaded("app")
        };
        let r = record_of(p, &Err(sql_failure("x")));
        assert_eq!(r["executionMode"], "atomic");
        assert_eq!(r["failedFile"], "./only.sql");
        for key in [
            "failedFileCandidates",
            "script",
            "line",
            "column",
            "sourceLine",
        ] {
            assert!(r.get(key).is_none(), "{key}");
        }

        let p = Progress {
            execution: executed(0, failure(1, in_deploy_sql())),
            ..loaded("app")
        };
        let r = record_of(p, &Err(sql_failure("x")));
        assert_eq!(r["failedFile"], "deploy.sql");
        assert_eq!(r["script"], "deploy.sql");
        assert_eq!(r["where"], "w");
        assert!(r.get("line").is_none() && r.get("scriptExpanded").is_none());

        let p = Progress {
            execution: executed(0, failure(1, in_file(&["./__test__/t.sql"], None))),
            ..loaded("app")
        };
        let r = record_of(p, &Err(sql_failure("x")));
        assert_eq!(r["phase"], "test");
        assert_eq!(r["failedFile"], "./__test__/t.sql");
    }

    #[test]
    fn a_failure_record_redacts_detail_hint_and_where() {
        let e = Error::sql("x").with_sql(crate::error::SqlDiagnostics {
            sqlstate: Some("P0001".into()),
            detail: Some("detail s3cret-pw".into()),
            hint: Some("hint s3cret-pw".into()),
            where_: Some("where s3cret-pw".into()),
            internal_query: None,
            position: None,
            internal_position: None,
        });
        let r = record_of(loaded("app"), &Err(e));
        assert_eq!(
            [&r["detail"], &r["hint"], &r["where"]],
            [
                &json!("detail ***"),
                &json!("hint ***"),
                &json!("where ***")
            ]
        );
        assert!(!r.to_string().contains("s3cret-pw"), "{r}");
    }

    #[test]
    fn a_failure_without_sql_diagnostics_has_no_phase() {
        let p = Progress {
            execution: executed(0, failure(3, blame::Blame::default())),
            ..loaded("app")
        };
        let r = record_of(
            p,
            &Err(Error::sql("deploy.sql ended inside an open transaction")),
        );
        assert_eq!(r["executionMode"], "atomic");
        assert!(r.get("phase").is_none() && r.get("failedFile").is_none());
        assert!(r.get("sqlstate").is_none());

        let r = record_of(loaded("app"), &Err(sql_failure("session tables")));
        assert_eq!(r["testMacros"], 0);
        assert!(r.get("executionMode").is_none() && r.get("phase").is_none());
        assert!(r.get("executionUnits").is_none());
        assert_eq!(r["sqlstate"], "22012");
    }

    #[test]
    fn a_huge_timeout_never_overflows_the_deadline() {
        let far = deadline(Duration::MAX);
        assert!(far > tokio::time::Instant::now() + Duration::from_hours(8760));
    }

    #[test]
    fn a_lost_create_race_is_a_concurrent_deploy() {
        let failed = |code: &str| {
            created_elsewhere(Error::sql("ERROR: x").with_sql(SqlDiagnostics {
                sqlstate: Some(code.into()),
                ..SqlDiagnostics::default()
            }))
        };
        for code in ["42P04", "23505"] {
            let e = failed(code);
            assert_eq!(e.kind(), Kind::Locked, "{code}");
            assert!(!e.text().contains("pg_database_datname_index"));
        }
        let e = failed("42501");
        assert_eq!(e.kind(), Kind::Sql);
        assert_eq!(e.text(), "ERROR: x");
        let hint = privilege_hint("42501", Some(Needs::CreateDb)).unwrap();
        assert!(hint.contains("-d") && hint.contains("CREATEDB"));
        assert_eq!(
            privilege_hint(
                "42501",
                Some(Needs::Temporary {
                    database: "app",
                    role: "a\"b"
                })
            )
            .unwrap(),
            "the login needs the TEMPORARY privilege on the target database: GRANT TEMPORARY ON DATABASE \"app\" TO \"a\"\"b\""
        );
        assert_eq!(privilege_hint("42P04", Some(Needs::CreateDb)), None);
        assert_eq!(privilege_hint("42501", None), None);
    }

    fn yaml_connection(text: &str) -> crate::config::yaml::Connection {
        ProjectConfig::parse(text).unwrap().connection
    }

    fn provenance_of(
        flags: &resolve::Flags,
        env: &[(&str, &str)],
        yaml: &str,
        dotenv_keys: &[&str],
    ) -> Vec<String> {
        let env = Env::from_pairs(env);
        let yaml = yaml_connection(yaml);
        let spec = resolve::resolve(flags, &env, Some(&yaml)).unwrap();
        let notes = overrides(flags, &env, Some(&yaml)).unwrap();
        let keys: Vec<String> = dotenv_keys.iter().map(ToString::to_string).collect();
        provenance(
            &spec,
            "./app/.env",
            &keys,
            &[],
            &notes,
            env.non_empty("PGOPTIONS").unwrap().is_some(),
        )
    }

    #[test]
    fn serve_keeps_the_operators_credentials_for_the_hosts_the_operator_allowed() {
        let resolved = |conn: &str, env: &[(&str, &str)]| {
            let flags = resolve::Flags {
                connection: Some(conn.into()),
                ..resolve::Flags::default()
            };
            (
                resolve::resolve(&flags, &Env::from_pairs(env), None).unwrap(),
                flags,
            )
        };
        let allowed = vec!["db.internal".to_string(), "127.0.0.1:5432".to_string()];
        let env = [("PGPASSWORD", "operator-pw")];

        let (mut spec, flags) = resolved("host=evil.example user=u dbname=d", &env);
        let refused = restrict_to_allowed(&mut spec, &flags, false, &allowed).unwrap();
        assert_eq!(refused.as_deref(), Some("evil.example:5432"));
        assert!(spec.password.is_none() && !spec.ambient_passfile);

        let (mut spec, flags) = resolved("host=evil.example user=u password=typed dbname=d", &env);
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, false, &allowed).unwrap(),
            None
        );
        assert_eq!(spec.password.as_ref().map(Secret::expose), Some("typed"));
        assert!(!spec.ambient_passfile);

        let (mut spec, flags) = resolved("host=DB.internal user=u dbname=d", &env);
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, false, &allowed).unwrap(),
            None
        );
        assert_eq!(
            spec.password.as_ref().map(Secret::expose),
            Some("operator-pw")
        );
        assert!(spec.ambient_passfile);

        let (mut spec, flags) = resolved("host=127.0.0.1 port=5432 user=u dbname=d", &env);
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, false, &allowed).unwrap(),
            None
        );
        let (mut spec, flags) = resolved("host=127.0.0.1 port=55902 user=u dbname=d", &env);
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, false, &allowed)
                .unwrap()
                .as_deref(),
            Some("127.0.0.1:55902")
        );
        assert!(spec.password.is_none());

        let (mut spec, flags) = resolved("host=evil.example user=u dbname=d", &env);
        let e = restrict_to_allowed(&mut spec, &flags, true, &allowed).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("PGMI_SERVE_HOSTS"), "{}", e.text());
    }

    #[test]
    fn the_dotenv_line_names_refused_keys() {
        let k = |v: &[&str]| v.iter().map(ToString::to_string).collect::<Vec<_>>();
        assert_eq!(dotenv_line("./.env", &[], &[]), None);
        assert_eq!(
            dotenv_line("./.env", &k(&["A"]), &[]).as_deref(),
            Some("./.env applied: A")
        );
        let both = dotenv_line("./.env", &k(&["A", "B"]), &k(&["SSL_CERT_FILE"])).unwrap();
        assert!(
            both.starts_with(
                "./.env applied: A, B; refused: SSL_CERT_FILE (a project .env may not"
            ),
            "{both}"
        );
        assert!(
            dotenv_line("./.env", &[], &k(&["https_proxy"]))
                .unwrap()
                .starts_with("./.env applied: nothing; refused: https_proxy")
        );
    }

    #[test]
    fn the_source_lines_name_dotenv_overrides_and_pgoptions() {
        let none = resolve::Flags::default();
        assert_eq!(
            provenance_of(
                &none,
                &[
                    (
                        "PGMI_CONNECTION_STRING",
                        "postgresql://app:pw@127.0.0.1:1/dotenv_db"
                    ),
                    ("PGMI_PARAM_TOKEN", "t"),
                    ("PGOPTIONS", "-c default_transaction_read_only=on"),
                ],
                "connection:\n  database: myapp\n",
                &["PGMI_CONNECTION_STRING", "PGMI_PARAM_TOKEN"],
            ),
            [
                "connection: from PGMI_CONNECTION_STRING (set by ./app/.env) (127.0.0.1:1/dotenv_db)",
                "./app/.env applied: PGMI_CONNECTION_STRING, PGMI_PARAM_TOKEN",
                "database: from PGMI_CONNECTION_STRING (dotenv_db), not pgmi.yaml (myapp)",
                "session options: from PGOPTIONS",
            ]
        );
        assert_eq!(
            provenance_of(
                &none,
                &[("PGHOST", "127.0.0.1"), ("PGSSLMODE", "disable")],
                "connection:\n  database: myapp\n  sslmode: verify-full\n",
                &["PGSSLMODE"],
            ),
            [
                "connection: from PGHOST (127.0.0.1:5432/myapp)",
                "./app/.env applied: PGSSLMODE",
                "sslmode: from PGSSLMODE (disable), not pgmi.yaml (verify-full)",
            ]
        );
        assert_eq!(
            provenance_of(
                &none,
                &[("DATABASE_URL", "postgresql://u@127.0.0.1/postgres")],
                "connection:\n  sslmode: verify-full\n",
                &[],
            ),
            [
                "connection: from DATABASE_URL (127.0.0.1:5432/postgres)",
                "sslmode: pgmi.yaml (verify-full) does not apply to the connection string from DATABASE_URL; set sslmode in the string",
            ]
        );
        assert_eq!(
            provenance_of(&none, &[], "connection:\n  database: myapp\n", &[]),
            ["connection: defaults (localhost:5432/myapp)"]
        );
        let typed = resolve::Flags {
            connection: Some("postgresql://u@127.0.0.1/postgres".into()),
            ..resolve::Flags::default()
        };
        assert_eq!(
            provenance_of(&typed, &[("PGOPTIONS", "-c a=b")], "", &[]),
            ["session options: from PGOPTIONS"]
        );
    }

    #[test]
    fn a_connection_failure_names_where_an_untyped_connection_came_from() {
        let resolved = Progress {
            connection_source: Some(Source::PgmiConnectionString),
            spec: Some(spec("app")),
            ..Progress::default()
        };
        let refused = Error::connection("connect to \"app\"")
            .with_source(std::io::Error::other("connection refused"));
        assert_eq!(
            resolved.connection_note(&refused).as_deref(),
            Some("(connection from PGMI_CONNECTION_STRING)")
        );
        assert_eq!(
            resolved.connection_note(&Error::config("invalid parameter format")),
            None
        );
        let resolving = Progress {
            connection_source: Some(Source::Defaults),
            ..Progress::default()
        };
        assert_eq!(
            resolving.connection_note(&Error::config("bad")).as_deref(),
            Some("(connection defaults)")
        );
        let typed = Progress {
            connection_source: Some(Source::ConnectionFlag),
            ..Progress::default()
        };
        assert_eq!(typed.connection_note(&Error::connection("x")), None);
        assert_eq!(
            Progress::default().connection_note(&Error::config("project path does not exist")),
            None
        );

        let mut run = Run::start();
        run.st = Progress {
            phase: Phase::Deploying,
            ..resolved
        };
        let (reporter, out) = crate::output::Captured::reporter(false, crate::output::Style::Plain);
        assert_eq!(finish(&context(), &run, &Err(refused), &reporter), 11);
        let text = out.text();
        let lines: Vec<&str> = text.lines().skip(1).collect();
        assert_eq!(
            lines,
            [
                "pgmi: error: connect to \"app\": connection refused",
                "(connection from PGMI_CONNECTION_STRING)"
            ],
            "{text}"
        );
    }

    #[test]
    fn the_note_ends_the_record_error_too() {
        let mut run = Run::start();
        let mut record = run.record(&context(), &Err(Error::config("bad")));
        record.note_error("(connection defaults)");
        let r = serde_json::to_value(&record).unwrap();
        assert_eq!(r["error"], "bad\n(connection defaults)");
        run.st.phase = Phase::Deploying;
        let mut ok = run.record(&context(), &Ok(()));
        ok.note_error("(connection defaults)");
        assert!(serde_json::to_value(&ok).unwrap().get("error").is_none());
    }

    #[test]
    fn the_record_names_the_source_and_every_override() {
        let env = Env::from_pairs(&[
            ("DATABASE_URL", "postgresql://app@127.0.0.1:1"),
            ("PGDATABASE", "fromenv"),
        ]);
        let yaml = yaml_connection("connection:\n  database: myapp\n");
        let p = Progress {
            connection_source: Some(Source::DatabaseUrl),
            overrides: overrides(&resolve::Flags::default(), &env, Some(&yaml)).unwrap(),
            ..Progress::default()
        };
        let mut run = Run::start();
        run.at(p);
        let text =
            serde_json::to_string(&run.record(&context(), &Err(Error::connection("refused"))))
                .unwrap();
        assert!(
            text.contains(r#""overrides":[{"field":"database","from":"PGDATABASE","value":"fromenv","notFrom":"pgmi.yaml","notValue":"myapp"}]"#),
            "{text}"
        );
        let r: Value = serde_json::from_str(&text).unwrap();
        assert_eq!(r["connectionSource"], "DATABASE_URL");
        assert_eq!(
            r["overrides"],
            json!([{"field": "database", "from": "PGDATABASE", "value": "fromenv", "notFrom": "pgmi.yaml", "notValue": "myapp"}])
        );
    }
}
