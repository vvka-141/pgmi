mod approve;
mod blame;
mod execute;
mod overwrite;
mod record;
mod request;
pub(crate) mod script;
mod session;

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::future::Future;
use std::io::Write;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime};

use tokio::runtime::Runtime;
use tokio::time::Instant as TokioInstant;
use tokio_postgres::error::ErrorPosition;
use tokio_util::sync::CancellationToken;

use crate::config::params::{self, Sources};
use crate::config::yaml::ProjectConfig;
use crate::conn::cloud::{self, Provider};
use crate::conn::dial::{Dialer, Session};
use crate::conn::overrides::{Override, overrides};
use crate::conn::resolve::{self, Source, Spec};
use crate::env::Env;
use crate::error::redact;
use crate::error::{Error, Kind, Result, SqlDiagnostics};
use crate::output::{Notice, NoticeLog, Reporter, seconds};
use crate::project::{metadata, walk};
use crate::vfs::OsFs;

pub(crate) use approve::{Approver, ConfirmedName, Terminal};
pub(crate) use blame::redact_sql_passwords;
pub(crate) use record::DeployRecord;
use record::Tls;
pub(crate) use request::Request;

/// The privilege a step needs, named in the error when the server answers
/// 42501.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Needs {
    Temporary,
    CreateDb,
}

fn privilege_hint(code: &str, needs: Option<Needs>) -> Option<&'static str> {
    match (code, needs?) {
        ("42501", Needs::Temporary) => Some(
            "the login needs the TEMPORARY privilege on the target database: GRANT TEMPORARY ON DATABASE <db> TO <role>",
        ),
        ("42501", Needs::CreateDb) => Some(
            "the database does not exist and this role cannot create it: check the -d name, or grant the role CREATEDB",
        ),
        _ => None,
    }
}

fn created_elsewhere(e: Error) -> Error {
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
    needs: Option<Needs>,
) -> Error {
    match e.as_db_error() {
        Some(db) => {
            let code = db.code().code().to_string();
            let mut message = format!("ERROR: {} (SQLSTATE {code})", db.message());
            if let Some(hint) = privilege_hint(&code, needs) {
                message.push('\n');
                message.push_str(hint);
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
                detail: db.detail().map(str::to_string),
                hint: db.hint().map(str::to_string),
                where_: db.where_().map(str::to_string),
                internal_query,
                position,
                internal_position,
            })
        }
        None => Error::connection(context).with_source(e),
    }
}

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

/// How far a deploy got before anything ran on the server: `Deploying`
/// once the parameters are read and an overwrite was approved, the point
/// from which the record counts files, tests and timings.
#[derive(Debug, Default, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub(crate) enum Phase {
    #[default]
    Preparing,
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

/// The stderr lines that say where each connection setting came from: the
/// source (unless typed), the keys the project `.env` set, every `pgmi.yaml`
/// value another source replaced, and `PGOPTIONS`.
fn provenance(
    spec: &Spec,
    dotenv_path: &str,
    dotenv_keys: &[String],
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
    if !dotenv_keys.is_empty() {
        lines.push(format!("{dotenv_path} applied: {}", dotenv_keys.join(", ")));
    }
    lines.extend(overrides.iter().map(ToString::to_string));
    if pgoptions {
        lines.push("session options: from PGOPTIONS".into());
    }
    lines
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
}

struct Guard {
    token: CancellationToken,
    deadline: TokioInstant,
    timeout: Duration,
    interrupted: Arc<AtomicBool>,
}

impl Guard {
    fn stop_error(&self) -> Error {
        if self.interrupted.load(Ordering::SeqCst) {
            Error::new(Kind::Interrupted, "interrupted")
        } else {
            Error::new(Kind::Interrupted, "the deploy was cancelled")
        }
    }

    fn expired_error(&self) -> Error {
        Error::new(
            Kind::Timeout,
            format!(
                "the deploy exceeded --timeout {}; raise it, or pass --timeout 0 for no limit",
                crate::duration::render(self.timeout)
            ),
        )
    }

    async fn waiting<T>(&self, fut: impl Future<Output = Result<T>>) -> Result<T> {
        tokio::select! {
            r = fut => r,
            () = self.token.cancelled() => Err(self.stop_error()),
            () = tokio::time::sleep_until(self.deadline) => Err(self.expired_error()),
        }
    }

    async fn blocking<T: Send + 'static>(
        &self,
        work: impl FnOnce() -> Result<T> + Send + 'static,
    ) -> Result<T> {
        self.waiting(async {
            tokio::task::spawn_blocking(work)
                .await
                .map_err(|e| Error::internal("blocking task").with_source(e))?
        })
        .await
    }

    /// Like `blocking`, but only a signal or a cancel stops it: `--timeout`
    /// does not bound reading `--params-file -`, in Go either.
    async fn interruptible<T: Send + 'static>(
        &self,
        work: impl FnOnce() -> Result<T> + Send + 'static,
    ) -> Result<T> {
        tokio::select! {
            r = tokio::task::spawn_blocking(work) => {
                r.map_err(|e| Error::internal("blocking task").with_source(e))?
            }
            () = self.token.cancelled() => Err(self.stop_error()),
        }
    }

    fn check(&self) -> Result<()> {
        if self.token.is_cancelled() {
            Err(self.stop_error())
        } else if TokioInstant::now() >= self.deadline {
            Err(self.expired_error())
        } else {
            Ok(())
        }
    }

    async fn connecting<T>(&self, fut: impl Future<Output = Result<T>>) -> Result<T> {
        tokio::select! {
            r = fut => r,
            () = self.token.cancelled() => Err(self.stop_error()),
            () = tokio::time::sleep_until(self.deadline) => Err(Error::connection("--timeout ran out while connecting")),
        }
    }

    /// Runs `fut` on `session` until it ends, the deploy is stopped or the
    /// deadline passes. On a stop it sends PostgreSQL's cancel request,
    /// then gives `fut` 2 s to finish before it returns the stop's error.
    async fn running<T>(
        &self,
        session: &Session,
        fut: impl Future<Output = Result<T>>,
    ) -> Result<T> {
        tokio::pin!(fut);
        let stopped = tokio::select! {
            r = &mut fut => return r,
            () = self.token.cancelled() => self.stop_error(),
            () = tokio::time::sleep_until(self.deadline) => self.expired_error(),
        };
        session.cancel().await;
        let reported = tokio::time::timeout(Duration::from_secs(2), &mut fut).await;
        Err(match reported {
            Ok(Err(e)) if e.sql_diagnostics().is_some() => {
                let text = match stopped.kind() {
                    Kind::Timeout => stopped.text(),
                    _ => e.text(),
                };
                e.reworded(stopped.kind(), text)
            }
            _ => stopped,
        })
    }
}

/// The secrets of a deploy, each added where it becomes known: the
/// connection's passwords once it is resolved, and every password or
/// sign-in token the dialer presents, renewed tokens included. Shared with
/// the dialer's callbacks, so an append-only set behind a short lock.
#[derive(Clone, Default)]
struct Secrets(Arc<Mutex<Vec<String>>>);

impl Secrets {
    fn insert(&self, secret: &str) {
        if let Ok(mut s) = self.0.lock()
            && !s.iter().any(|f| f == secret)
        {
            s.push(secret.to_string());
        }
    }

    fn resolved(&self, spec: &Spec) {
        for p in [&spec.password, &spec.sslpassword].into_iter().flatten() {
            self.insert(p.expose());
        }
    }

    fn list(&self) -> Vec<String> {
        self.0.lock().map(|s| s.clone()).unwrap_or_default()
    }

    fn notice(&self, mut n: Notice) -> Notice {
        let secrets = self.list();
        n.message = redact(&n.message, &secrets);
        n.detail = n.detail.map(|d| redact(&d, &secrets));
        n.hint = n.hint.map(|h| redact(&h, &secrets));
        n
    }
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
        token: CancellationToken,
        interrupted: Arc<AtomicBool>,
    ) -> Result<()> {
        let contract = ContractVersion::of(ctx.request.compat.as_deref());
        self.st.contract = contract.as_ref().ok().copied();
        let yaml = ProjectConfig::load(&OsFs::project(&ctx.request.project)?)?;
        let yaml_timeout = yaml.as_ref().map(ProjectConfig::timeout).transpose()?;
        let timeout = limit(ctx.request.timeout.or(yaml_timeout.flatten()));
        let guard = Guard {
            token,
            deadline: deadline(timeout),
            timeout,
            interrupted,
        };
        let steps = Steps {
            ctx,
            guard: &guard,
            reporter,
            secrets: &self.secrets,
        };
        Box::pin(steps.deploy(&mut self.st, yaml.as_ref(), contract, &self.log)).await
    }

    pub(crate) fn reached_deployer(&self) -> bool {
        self.st.reached(Phase::Deploying)
    }

    pub(crate) fn database(&self) -> Option<&str> {
        self.st.database()
    }

    pub(crate) fn notice_counts(&self) -> (u64, u64) {
        self.log
            .lock()
            .map(|l| (l.total, l.warning_total))
            .unwrap_or_default()
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

    pub(crate) fn secrets(&self) -> Vec<String> {
        self.secrets.list()
    }

    pub(crate) fn record(&self, ctx: &Context, result: &Result<()>) -> DeployRecord {
        let secrets = self.secrets();
        DeployRecord::new(
            &self.st,
            result,
            record::Facts {
                dotenv_keys: &ctx.dotenv_keys,
                started: self.started,
                elapsed: self.t0.elapsed(),
                notices: self.log.lock().ok().map(|l| l.finish()),
                secrets: &secrets,
            },
        )
    }
}

pub(crate) fn run(runtime: Runtime, ctx: &Context) -> u8 {
    let reporter = Reporter::new(ctx.verbose, &ctx.env);
    let mut run = Run::start();
    let interrupted = Arc::new(AtomicBool::new(false));
    let caught = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        runtime.block_on(async {
            let token = CancellationToken::new();
            spawn_signals(token.clone(), interrupted.clone(), reporter.clone());
            run.deploy(ctx, &reporter, token, interrupted.clone()).await
        })
    }));
    let mut result = caught.unwrap_or_else(|_| Err(crate::error::panic_error().0));
    if interrupted.load(Ordering::SeqCst) {
        result = Err(match result {
            Err(e) if e.sql_diagnostics().is_some() => {
                let text = e.text();
                e.reworded(Kind::Interrupted, text)
            }
            _ => Error::new(Kind::Interrupted, "interrupted"),
        });
    }
    runtime.shutdown_timeout(Duration::from_millis(100));
    finish(ctx, &run, &result, &reporter)
}

const CLEANUP: Duration = Duration::from_secs(3);

fn deadline(timeout: Duration) -> TokioInstant {
    let now = TokioInstant::now();
    now.checked_add(timeout)
        .or_else(|| now.checked_add(Duration::from_hours(876_000)))
        .unwrap_or(now)
}

fn limit(timeout: Option<Duration>) -> Duration {
    timeout.map_or(Duration::from_secs(180), |t| {
        if t.is_zero() {
            Duration::from_hours(876_000)
        } else {
            t
        }
    })
}

const INTERRUPTED: &str = "pgmi: interrupted, cancelling deployment... (again to quit at once)";
const INTERRUPTED_AGAIN: &str = "pgmi: interrupted again, quitting";

fn spawn_signals(token: CancellationToken, flag: Arc<AtomicBool>, reporter: Arc<Reporter>) {
    tokio::spawn(async move {
        #[cfg(unix)]
        {
            use tokio::signal::unix::{SignalKind, signal};
            let (Ok(mut int), Ok(mut term)) = (
                signal(SignalKind::interrupt()),
                signal(SignalKind::terminate()),
            ) else {
                return;
            };
            tokio::select! {
                _ = int.recv() => {}
                _ = term.recv() => {}
            }
            reporter.line(INTERRUPTED);
            flag.store(true, Ordering::SeqCst);
            token.cancel();
            tokio::select! {
                _ = int.recv() => {}
                _ = term.recv() => {}
            }
            reporter.line(INTERRUPTED_AGAIN);
            std::process::exit(i32::from(Kind::Interrupted.exit_code()));
        }
        #[cfg(not(unix))]
        {
            if tokio::signal::ctrl_c().await.is_ok() {
                reporter.line(INTERRUPTED);
                flag.store(true, Ordering::SeqCst);
                token.cancel();
            }
            if tokio::signal::ctrl_c().await.is_ok() {
                reporter.line(INTERRUPTED_AGAIN);
                std::process::exit(i32::from(Kind::Interrupted.exit_code()));
            }
        }
    });
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
        self.report_provenance(st, &spec)?;
        let params = self
            .params(yaml.map(ProjectConfig::param_values).unwrap_or_default())
            .await?;
        #[cfg(feature = "testhooks")]
        if self.ctx.env.non_empty("PGMI_TEST_PANIC")?.is_some() {
            test_panic();
        }
        if request.overwrite {
            self.ctx.approver.before_connecting(&spec.target_db)?;
        }
        st.phase = Phase::Deploying;
        let loaded = self.load().await?;
        contract?;
        self.record_loaded(st, &loaded);
        if request.overwrite {
            overwrite::refuse_maintenance(&spec.target_db, &spec.maintenance_db)?;
        }
        let dialer = self.dialer(log);
        if let Some((provider, options)) = &cloud {
            spec.token = Some(self.sign_in(*provider, options).await?);
        }
        let _maintenance = if request.overwrite {
            Some(self.overwrite(st, &dialer, &spec).await?)
        } else {
            None
        };
        let session = self.open(st, &dialer, &spec).await?;
        st.server = Some(self.server_info(&session, &spec).await?);
        self.lock(&session, &spec.target_db).await?;
        let outcome = execute::run(execute::Input {
            session: &session,
            guard: self.guard,
            reporter: self.reporter,
            project: &loaded.project,
            meta: &loaded.meta,
            params: &params,
            verbose: self.ctx.verbose,
            files_loaded: loaded.files(),
            execution: &mut st.execution,
        })
        .await;
        release(&session).await;
        outcome
    }

    fn target(&self, st: &mut Progress, yaml: Option<&ProjectConfig>) -> Result<Target> {
        let (request, env) = (&self.ctx.request, &self.ctx.env);
        let cloud = cloud::select(&request.cloud, &request.cloud_request, env, yaml)?;
        let yaml_connection = yaml.map(|y| &y.connection);
        st.connection_source = Some(resolve::source(&request.connection, env, yaml_connection)?);
        st.overrides = overrides(&request.connection, env, yaml_connection)?;
        let mut spec = resolve::resolve(&request.connection, env, yaml_connection)?;
        if let Some((provider, options)) = &cloud {
            cloud::check(*provider, options, &mut spec, env)?;
        }
        if let Some(m) = &request.maintenance_db {
            spec.maintenance_db.clone_from(m);
        }
        st.spec = Some(spec.clone());
        self.secrets.resolved(&spec);
        Ok(Target { spec, cloud })
    }

    fn report_provenance(&self, st: &Progress, spec: &Spec) -> Result<()> {
        for line in provenance(
            spec,
            &crate::config::dotenv_path(&self.ctx.request.project),
            &self.ctx.dotenv_keys,
            &st.overrides,
            self.ctx.env.non_empty("PGOPTIONS")?.is_some(),
        ) {
            self.reporter.line(&line);
        }
        Ok(())
    }

    async fn params(&self, yaml: Vec<(String, String)>) -> Result<BTreeMap<String, String>> {
        let request = &self.ctx.request;
        let (env, files, flags, stdin) = (
            self.ctx.env.clone(),
            request.params_files.clone(),
            request.params.clone(),
            self.ctx.stdin,
        );
        let (notes, verbose) = (self.reporter.clone(), self.ctx.verbose);
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
                match stdin {
                    Stdin::Process => params::collect(sources, Some(&mut std::io::stdin())),
                    Stdin::Unavailable => params::collect(sources, None),
                }
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

    fn record_loaded(&self, st: &mut Progress, loaded: &LoadedProject) {
        st.loaded = Some(Loaded {
            files: loaded.files(),
            ignored: loaded.project.ignored.clone(),
        });
        if self.ctx.verbose {
            for i in &loaded.project.ignored {
                self.reporter
                    .line(&format!("ignored {} ({})", i.path, i.rule));
            }
        }
    }

    fn dialer(&self, log: &Arc<Mutex<NoticeLog>>) -> Dialer {
        let (reporter, log, secrets) = (self.reporter.clone(), log.clone(), self.secrets.clone());
        let line_reporter = self.reporter.clone();
        let line_secrets = self.secrets.clone();
        let found = self.secrets.clone();
        Dialer {
            notices: Arc::new(move |n: Notice| {
                let n = secrets.notice(n);
                reporter.notice(&n);
                if let Ok(mut l) = log.lock() {
                    l.push(&n);
                }
            }),
            log: Arc::new(move |line: String| {
                line_reporter.line(&redact(&line, &line_secrets.list()));
            }),
            secret: Arc::new(move |secret: &str| found.insert(secret)),
        }
    }

    async fn sign_in(&self, provider: Provider, options: &cloud::Options) -> Result<cloud::Token> {
        let token = self
            .guard
            .connecting(Box::pin(cloud::token(provider, options, &self.ctx.env)))
            .await?;
        for secret in token.secrets() {
            self.secrets.insert(&secret);
        }
        Ok(token)
    }

    async fn overwrite(&self, st: &mut Progress, dialer: &Dialer, spec: &Spec) -> Result<Session> {
        let (guard, db) = (self.guard, spec.target_db.as_str());
        let maintenance = guard
            .connecting(dialer.connect(spec, &spec.maintenance_db))
            .await?;
        guard
            .running(&maintenance, check_server(&maintenance))
            .await?;
        let exists = guard
            .running(&maintenance, overwrite::claim(&maintenance, db))
            .await?;
        let snapshot = if exists {
            guard
                .waiting(
                    self.ctx
                        .approver
                        .approve_drop(db, self.reporter, &guard.token),
                )
                .await?;
            guard
                .running(&maintenance, overwrite::snapshot(&maintenance, db))
                .await?
        } else {
            None
        };
        guard.check()?;
        overwrite::recreate(
            &maintenance,
            db,
            snapshot.as_ref(),
            self.reporter,
            &mut st.provision,
        )
        .await?;
        guard.check()?;
        Ok(maintenance)
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
            opened => return opened,
        }
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
        drop(maintenance);
        guard
            .connecting(dialer.connect(spec, &spec.target_db))
            .await
    }

    async fn server_info(&self, session: &Session, spec: &Spec) -> Result<ServerInfo> {
        let guard = self.guard;
        guard.running(session, check_server(session)).await?;
        guard
            .running(
                session,
                client_encoding(session, spec.client_encoding.as_deref()),
            )
            .await?;
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
                version: session.tls_version.clone(),
            },
        })
    }

    async fn lock(&self, session: &Session, db: &str) -> Result<()> {
        let locked = self
            .guard
            .running(session, async {
                session
                    .client
                    .query_typed(
                        "SELECT pg_try_advisory_lock(hashtextextended('pgmi.deploy.' || current_database(), 0))",
                        &[],
                    )
                    .await
                    .and_then(|rows| rows.first().map(|r| r.try_get::<_, bool>(0)).transpose())
                    .map_err(|e| sql_error("query the server", e))
            })
            .await?;
        if locked != Some(true) {
            return Err(Error::new(
                Kind::Locked,
                format!(
                    "another pgmi deploy is already running against \"{db}\"; wait for it to finish"
                ),
            ));
        }
        Ok(())
    }
}

impl LoadedProject {
    fn files(&self) -> usize {
        self.project.sources.len() + self.project.test_dir_files.len()
    }
}

async fn create_database(maintenance: &Session, db: &str) -> Result<()> {
    maintenance
        .client
        .batch_execute(&format!("CREATE DATABASE {}", quote_ident(db)))
        .await
        .map_err(|e| {
            created_elsewhere(sql_error_needing(
                "create the target database",
                e,
                Some(Needs::CreateDb),
            ))
        })
}

/// Rolls back an open transaction and releases the deploy lock; a session
/// that does not answer within `CLEANUP` is closed instead.
async fn release(session: &Session) {
    let cleanup = async {
        if session.tap.status() != crate::conn::tap::TxStatus::Idle {
            session.client.batch_execute("ROLLBACK").await?;
        }
        session
            .client
            .batch_execute("SELECT pg_advisory_unlock_all()")
            .await
    };
    if !matches!(tokio::time::timeout(CLEANUP, cleanup).await, Ok(Ok(()))) {
        session.disconnect();
    }
}

#[cfg(feature = "testhooks")]
#[allow(clippy::panic)]
fn test_panic() -> ! {
    panic!("PGMI_TEST_PANIC is set")
}

pub(crate) fn quote_ident(s: &str) -> String {
    format!("\"{}\"", s.replace('"', "\"\""))
}

async fn client_encoding(session: &Session, named: Option<&str>) -> Result<()> {
    let Some(encoding) = named else {
        return session
            .client
            .batch_execute(
                "SELECT set_config('client_encoding', current_setting('server_encoding'), false)",
            )
            .await
            .map_err(|e| sql_error("match the client encoding to the database", e));
    };
    session
        .client
        .query_typed(
            "SELECT set_config('client_encoding', $1, false)",
            &[(&encoding, tokio_postgres::types::Type::TEXT)],
        )
        .await
        .map(drop)
        .map_err(|e| {
            let reason = e
                .as_db_error()
                .map_or_else(|| crate::error::chain(&e), |d| d.message().to_string());
            Error::connection(format!("client_encoding \"{encoding}\": {reason}"))
        })
}

const MIN_SERVER_VERSION_NUM: u32 = 150_000;

fn supported_server(num: &str, text: &str) -> Result<()> {
    match num.trim().parse::<u32>() {
        Ok(n) if n >= MIN_SERVER_VERSION_NUM => Ok(()),
        _ => Err(Error::config(format!(
            "this server reports PostgreSQL {text}, and pgmi requires {} or newer",
            MIN_SERVER_VERSION_NUM / 10_000
        ))),
    }
}

async fn check_server(session: &Session) -> Result<()> {
    let rows = session
        .client
        .simple_query(
            "SELECT current_setting('server_version_num'), current_setting('server_version')",
        )
        .await
        .map_err(|e| sql_error("read the server version", e))?;
    let (num, text) = match first_row(&rows) {
        Some(r) => {
            let read = |i| {
                r.try_get(i)
                    .map(|v| v.unwrap_or_default().to_string())
                    .map_err(|e| sql_error("read the server version", e))
            };
            (read(0)?, read(1)?)
        }
        None => Default::default(),
    };
    supported_server(&num, &text)
}

fn first_row(
    messages: &[tokio_postgres::SimpleQueryMessage],
) -> Option<&tokio_postgres::SimpleQueryRow> {
    messages.iter().find_map(|m| match m {
        tokio_postgres::SimpleQueryMessage::Row(r) => Some(r),
        _ => None,
    })
}

async fn first_value(session: &Session, sql: &str) -> Result<Option<String>> {
    let rows = session
        .client
        .simple_query(sql)
        .await
        .map_err(|e| sql_error("query the server", e))?;
    first_row(&rows)
        .map(|r| r.try_get(0).map_err(|e| sql_error("query the server", e)))
        .transpose()
        .map(|v| v.flatten().map(str::to_string))
}

fn finish(ctx: &Context, run: &Run, result: &Result<()>, reporter: &Reporter) -> u8 {
    let st = &run.st;
    let elapsed = run.t0.elapsed();
    let code = match result {
        Ok(()) => 0,
        Err(e) => e.exit_code(),
    };
    let secrets = run.secrets();
    if let Some((ok, text)) = summary(st, result, elapsed) {
        reporter.summary(ok, &text);
    }
    let note = result.as_ref().err().and_then(|e| st.connection_note(e));
    if let Err(e) = result {
        let note = note
            .as_deref()
            .map(|n| format!("\n{n}"))
            .unwrap_or_default();
        let text = format!("pgmi: error: {}{note}{}", e.text(), run.diagnostics(e));
        reporter.line(&redact(&blame::redact_sql_passwords(&text), &secrets));
    }
    if ctx.request.json {
        let mut record = run.record(ctx, result);
        if let Some(n) = note.filter(|_| !st.reached(Phase::Deploying)) {
            record.note_error(&n);
        }
        let mut out = std::io::stdout().lock();
        let text = serde_json::to_string_pretty(&record).unwrap_or_default();
        let _ = writeln!(out, "{text}");
    }
    code
}

fn summary(p: &Progress, result: &Result<()>, elapsed: Duration) -> Option<(bool, String)> {
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
        return Some((
            true,
            format!(
                "{db}: {files} files loaded{ignored} in {}",
                seconds(elapsed)
            ),
        ));
    }
    let mut text = format!("{db}: failed after {}", seconds(elapsed));
    let committed = p.execution.as_ref().map_or(0, |x| x.committed);
    let mut notes = Vec::new();
    match &p.execution {
        Some(x) if committed > 0 && x.failure.is_some() => {
            let failed = x.failure.as_ref().map_or(0, |f| f.unit);
            let _ = write!(
                text,
                " (unit {failed} of {} failed; {committed} earlier unit(s) already committed)",
                x.units
            );
        }
        Some(x) if x.failure.is_some() => notes.push("nothing committed".to_string()),
        _ => {}
    }
    let verb = match p.provision {
        Provision::Existing => None,
        Provision::Created => Some("created"),
        Provision::Recreated => Some("recreated"),
    };
    if let Some(verb) = verb.filter(|_| p.loaded.is_some()) {
        let left = if committed == 0 { ", left empty" } else { "" };
        notes.push(format!("{verb} database \"{db}\"{left}"));
    }
    if !notes.is_empty() {
        let _ = write!(text, " — {}", notes.join("; "));
    }
    Some((false, text))
}

#[cfg(test)]
mod tests {
    use super::*;
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
            token: CancellationToken::new(),
            deadline: deadline(timeout),
            timeout,
            interrupted: Arc::new(AtomicBool::new(false)),
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
        g.interrupted.store(true, Ordering::SeqCst);
        g.token.cancel();
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
        g.interrupted.store(true, Ordering::SeqCst);
        g.token.cancel();
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
            token: CancellationToken::new(),
            deadline: deadline(Duration::from_secs(60)),
            timeout: Duration::from_secs(60),
            interrupted: Arc::new(AtomicBool::new(false)),
        };
        let e = guard.stop_error();
        assert!(!e.text().contains("--timeout"), "{}", e.text());
        guard.interrupted.store(true, Ordering::SeqCst);
        assert_eq!(guard.stop_error().text(), "interrupted");
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
        assert_eq!(e.exit_code(), 11, "{}", e.text());
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
        secrets.insert("from-pgpass");
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
        assert!(run.secrets().iter().any(|s| s == "from-pgpass"));
    }

    fn spec(db: &str) -> Spec {
        let flags = resolve::Flags {
            connection: Some(format!("postgresql://u:s3cret-pw@db.example:6543/{db}").into()),
            ..resolve::Flags::default()
        };
        resolve::resolve(&flags, &Env::default(), None).unwrap()
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
        Some(Failure { unit, blame })
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

    fn summary_of(p: &Progress, result: &Result<()>) -> Option<(bool, String)> {
        summary(p, result, Duration::from_millis(1234))
    }

    #[test]
    fn the_summary_counts_files_and_ignored_paths() {
        assert_eq!(
            summary_of(&loaded("app"), &Ok(())),
            Some((true, "app: 3 files loaded in 1.23s".into()))
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
            summary_of(&p, &Ok(())).unwrap().1,
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
            Some((false, "app: failed after 1.23s — nothing committed".into()))
        );
        for stopped in [
            Error::new(Kind::Timeout, "the deploy exceeded --timeout 2s"),
            Error::new(Kind::Interrupted, "interrupted"),
        ] {
            assert_eq!(
                summary_of(&atomic, &Err(stopped)).unwrap().1,
                "app: failed after 1.23s — nothing committed"
            );
        }
        let psql = Progress {
            execution: executed(2, failure(3, blame::Blame::default())),
            ..loaded("app")
        };
        assert_eq!(
            summary_of(&psql, &sql).unwrap().1,
            "app: failed after 1.23s (unit 3 of 3 failed; 2 earlier unit(s) already committed)"
        );
        let created = Progress {
            provision: Provision::Created,
            ..loaded("app")
        };
        assert_eq!(
            summary_of(&created, &Err(Error::connection("refused")))
                .unwrap()
                .1,
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
            .1,
            "app: failed after 1.23s — recreated database \"app\", left empty"
        );
        let created_then_sql = Progress {
            execution: executed(0, failure(1, blame::Blame::default())),
            ..created
        };
        assert_eq!(
            summary_of(&created_then_sql, &sql).unwrap().1,
            "app: failed after 1.23s — nothing committed; created database \"app\", left empty"
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
        let (reporter, out) = crate::output::Captured::reporter(false, false, false);
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
        let (reporter, out) = crate::output::Captured::reporter(false, false, false);
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

    fn record_of(p: Progress, result: &Result<()>) -> Value {
        let mut run = Run::start();
        run.at(p);
        let record = run.record(&context(), result);
        let text = serde_json::to_string(&record).unwrap();
        let sorted = serde_json::to_value(&record).unwrap();
        assert_eq!(text, sorted.to_string(), "keys must stay in byte order");
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
    fn a_successful_record_carries_the_phases_it_passed() {
        let p = Progress {
            provision: Provision::Recreated,
            server: Some(ServerInfo {
                version: Some("18.1".into()),
                tls: Tls {
                    enabled: true,
                    sslmode: crate::conn::resolve::SslMode::VerifyFull,
                    version: Some("TLSv1.3".into()),
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
            json!({"enabled": true, "sslmode": "verify-full", "version": "TLSv1.3"})
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
        assert!(far > TokioInstant::now() + Duration::from_hours(8760));
    }

    #[test]
    fn a_server_below_postgres_15_is_refused_as_invalid_config() {
        for (num, text) in [
            ("110016", "11.16 (Debian 11.16-1.pgdg90+1)"),
            ("140023", "14.23"),
            ("", ""),
        ] {
            let e = supported_server(num, text).unwrap_err();
            assert_eq!(e.kind(), Kind::Config, "{text}");
            assert_eq!(
                e.text(),
                format!("this server reports PostgreSQL {text}, and pgmi requires 15 or newer")
            );
        }
        for (num, text) in [
            ("150000", "15.0"),
            ("150018", "15.18 (Debian 15.18-1.pgdg120+1)"),
            ("180004", "18.4"),
        ] {
            assert!(supported_server(num, text).is_ok(), "{text}");
        }
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
        assert!(
            privilege_hint("42501", Some(Needs::Temporary))
                .unwrap()
                .contains("TEMPORARY")
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
            &notes,
            env.non_empty("PGOPTIONS").unwrap().is_some(),
        )
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
        let (reporter, out) = crate::output::Captured::reporter(false, false, false);
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
