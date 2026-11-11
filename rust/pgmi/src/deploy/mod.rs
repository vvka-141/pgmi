mod approve;
mod blame;
mod execute;
mod overwrite;
mod record;
mod request;
pub(crate) mod script;
mod session;

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
use crate::conn::dial::{Dialer, Session};
use crate::conn::resolve::{self, Spec};
use crate::conn::tls;
use crate::env::Env;
use crate::error::redact;
use crate::error::{Error, Kind, Result, SqlDiagnostics};
use crate::output::{Notice, NoticeLog, Reporter, seconds};
use crate::project::{metadata, walk};

pub(crate) use approve::{Approver, ConfirmedName, Terminal};
pub(crate) use record::DeployRecord;
use record::Tls;
pub(crate) use request::Request;

fn privilege_hint(context: &str, code: &str) -> Option<&'static str> {
    match (code, context) {
        ("42501", "create the session tables") => Some(
            "the login needs the TEMPORARY privilege on the target database: GRANT TEMPORARY ON DATABASE <db> TO <role>",
        ),
        ("42501", "create the target database") => Some(
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

pub(crate) fn sql_error(context: &str, e: &tokio_postgres::Error) -> Error {
    match e.as_db_error() {
        Some(db) => {
            let code = db.code().code().to_string();
            let mut message = format!("ERROR: {} (SQLSTATE {code})", db.message());
            if let Some(hint) = privilege_hint(context, &code) {
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
        None => Error::connection(format!("{context}: {}", crate::error::chain(e))),
    }
}

pub(crate) struct Context {
    pub(crate) request: Request,
    pub(crate) verbose: bool,
    pub(crate) env: Env,
    pub(crate) dotenv_keys: Vec<String>,
    pub(crate) stdin: bool,
    pub(crate) approver: Box<dyn Approver>,
}

/// How far a deploy got, one typed result per phase; the summary line and
/// the `--json` record are read from it.
#[derive(Default)]
pub(crate) struct Progress {
    contract: bool,
    spec: Option<Spec>,
    deployer: bool,
    loaded: Option<Loaded>,
    provision: Provision,
    server: Option<ServerInfo>,
    execution: Option<Execution>,
}

impl Progress {
    fn database(&self) -> Option<&str> {
        self.spec.as_ref().map(|s| s.target_db.as_str())
    }

    fn failure(&self) -> Option<&Failure> {
        self.execution.as_ref()?.failure.as_ref()
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

    fn timeout_error(&self) -> Error {
        Error::new(
            Kind::Timeout,
            format!(
                "the deploy exceeded --timeout {}; the running statement was cancelled",
                crate::duration::render(self.timeout)
            ),
        )
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
            _ = self.token.cancelled() => Err(self.stop_error()),
            _ = tokio::time::sleep_until(self.deadline) => Err(self.expired_error()),
        }
    }

    async fn blocking<T: Send + 'static>(
        &self,
        work: impl FnOnce() -> Result<T> + Send + 'static,
    ) -> Result<T> {
        self.waiting(async {
            tokio::task::spawn_blocking(work)
                .await
                .map_err(|e| Error::internal(format!("blocking task: {e}")))?
        })
        .await
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
            _ = self.token.cancelled() => Err(self.stop_error()),
            _ = tokio::time::sleep_until(self.deadline) => Err(Error::connection("--timeout ran out while connecting")),
        }
    }

    async fn running<T>(
        &self,
        session: &Session,
        fut: impl Future<Output = Result<T>>,
    ) -> Result<T> {
        tokio::pin!(fut);
        let stopped = tokio::select! {
            r = &mut fut => return r,
            _ = self.token.cancelled() => self.stop_error(),
            _ = tokio::time::sleep_until(self.deadline) => self.timeout_error(),
        };
        session.cancel().await;
        let _ = tokio::time::timeout(Duration::from_secs(2), &mut fut).await;
        Err(stopped)
    }
}

pub(crate) struct Run {
    st: Progress,
    log: Arc<Mutex<NoticeLog>>,
    started: SystemTime,
    t0: Instant,
}

impl Run {
    pub(crate) fn start() -> Self {
        Run {
            st: Progress::default(),
            log: Arc::new(Mutex::new(NoticeLog::default())),
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
        let timeout = timeout_of(ctx);
        let guard = Guard {
            token,
            deadline: deadline(timeout),
            timeout,
            interrupted,
        };
        deploy(ctx, &mut self.st, &guard, reporter, self.log.clone()).await
    }

    pub(crate) fn reached_deployer(&self) -> bool {
        self.st.deployer
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
                out.push_str(&format!("\n{label}: {v}"));
            }
        }
        if let Some(b) = blame {
            b.render(&mut out);
        }
        blame::redact_sql_passwords(&out)
    }

    pub(crate) fn secrets(&self) -> Vec<String> {
        self.st
            .spec
            .iter()
            .flat_map(|s| {
                [&s.password, &s.sslpassword]
                    .into_iter()
                    .flatten()
                    .map(|p| p.expose().to_string())
                    .chain(s.token.iter().flat_map(|t| t.secrets()))
            })
            .collect()
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

pub(crate) fn run(runtime: Runtime, ctx: Context) -> u8 {
    let reporter = Reporter::new(ctx.verbose, &ctx.env);
    let mut run = Run::start();
    let interrupted = Arc::new(AtomicBool::new(false));
    let caught = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        runtime.block_on(async {
            let token = CancellationToken::new();
            spawn_signals(token.clone(), interrupted.clone());
            run.deploy(&ctx, &reporter, token, interrupted.clone())
                .await
        })
    }));
    let mut result = caught.unwrap_or_else(|_| Err(crate::error::panic_error().0));
    if interrupted.load(Ordering::SeqCst) {
        result = Err(Error::new(Kind::Interrupted, "interrupted"));
    }
    runtime.shutdown_timeout(Duration::from_millis(100));
    finish(&ctx, &run, result, &reporter)
}

const CLEANUP: Duration = Duration::from_secs(3);

fn deadline(timeout: Duration) -> TokioInstant {
    let now = TokioInstant::now();
    now.checked_add(timeout)
        .or_else(|| now.checked_add(Duration::from_secs(100 * 365 * 24 * 3600)))
        .unwrap_or(now)
}

fn timeout_of(ctx: &Context) -> Duration {
    ctx.request
        .timeout
        .or_else(|| {
            ProjectConfig::load(&crate::vfs::OsFs::new(&ctx.request.project))
                .ok()
                .flatten()
                .and_then(|y| y.timeout().ok().flatten())
        })
        .map(|t| {
            if t.is_zero() {
                Duration::from_secs(100 * 365 * 24 * 3600)
            } else {
                t
            }
        })
        .unwrap_or(Duration::from_secs(180))
}

fn spawn_signals(token: CancellationToken, flag: Arc<AtomicBool>) {
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
            flag.store(true, Ordering::SeqCst);
            token.cancel();
            tokio::select! {
                _ = int.recv() => {}
                _ = term.recv() => {}
            }
            std::process::exit(i32::from(Kind::Interrupted.exit_code()));
        }
        #[cfg(not(unix))]
        {
            if tokio::signal::ctrl_c().await.is_ok() {
                flag.store(true, Ordering::SeqCst);
                token.cancel();
            }
            if tokio::signal::ctrl_c().await.is_ok() {
                std::process::exit(i32::from(Kind::Interrupted.exit_code()));
            }
        }
    });
}

async fn deploy(
    ctx: &Context,
    st: &mut Progress,
    guard: &Guard,
    reporter: &Arc<Reporter>,
    log: Arc<Mutex<NoticeLog>>,
) -> Result<()> {
    let a = &ctx.request;
    if let Some(c) = &a.compat
        && c != "1"
    {
        return Err(Error::config(format!(
            "unsupported API version \"{c}\"; supported: 1"
        )));
    }
    st.contract = true;
    let fs = crate::vfs::OsFs::project(&a.project)?;
    let yaml = ProjectConfig::load(&fs)?;
    if let Some(y) = &yaml {
        y.timeout()?;
    }
    let cloud = crate::conn::cloud::select(&a.cloud, &a.cloud_request, yaml.as_ref())?;
    let sources = Sources {
        yaml: yaml.as_ref().map(|y| y.param_values()).unwrap_or_default(),
        env: &ctx.env,
        files: &a.params_files,
        flags: &a.params,
    };
    let params = if ctx.stdin {
        params::collect(sources, &mut std::io::stdin())?
    } else {
        params::collect(sources, &mut NoStdin)?
    };
    let mut spec = resolve::resolve(
        &a.connection,
        &ctx.env,
        yaml.as_ref().map(|y| &y.connection),
    )?;
    if let Some((provider, req)) = &cloud {
        crate::conn::cloud::check(*provider, req, &mut spec, &ctx.env)?;
    }
    if let Some(m) = &a.maintenance_db {
        spec.maintenance_db = m.clone();
    }
    st.spec = Some(spec.clone());
    #[cfg(feature = "testhooks")]
    if ctx.env.non_empty("PGMI_TEST_PANIC")?.is_some() {
        test_panic();
    }
    if a.overwrite {
        ctx.approver.before_connecting(&spec.target_db)?;
    }
    st.deployer = true;
    let max_size = walk::max_size(&ctx.env)?;
    let fs = crate::vfs::OsFs::new(a.project.clone());
    let project = guard.blocking(move || walk::load(&fs, max_size)).await?;
    let meta = metadata::collect(&project.sources)?;
    let files_loaded = project.sources.len() + project.test_dir_files.len();
    st.loaded = Some(Loaded {
        files: files_loaded,
        ignored: project.ignored.clone(),
    });
    if ctx.verbose {
        for i in &project.ignored {
            reporter.line(&format!("ignored {} ({})", i.path, i.rule));
        }
    }
    if !spec.source.typed() {
        let label = spec.source.label();
        if label == "defaults" {
            reporter.line(&format!("connection: defaults ({})", spec.describe()));
        } else {
            reporter.line(&format!("connection: from {label} ({})", spec.describe()));
        }
    }
    if a.overwrite {
        overwrite::refuse_maintenance(&spec.target_db, &spec.maintenance_db)?;
    }
    let sink_reporter = reporter.clone();
    let sink_log = log.clone();
    let dialer = Dialer {
        notices: Arc::new(move |n: Notice| {
            sink_reporter.notice(&n);
            if let Ok(mut l) = sink_log.lock() {
                l.push(&n);
            }
        }),
        log: {
            let r = reporter.clone();
            Arc::new(move |line: String| r.line(&line))
        },
    };
    if let Some((provider, req)) = &cloud {
        let token = guard
            .connecting(crate::conn::cloud::token(*provider, req, &ctx.env))
            .await?;
        spec.token = Some(token);
        st.spec = Some(spec.clone());
    }
    let _overwrite = if a.overwrite {
        Some(overwrite(ctx, st, guard, reporter, &dialer, &spec).await?)
    } else {
        None
    };
    let session = match guard
        .connecting(dialer.connect(&spec, &spec.target_db))
        .await
    {
        Ok(s) => s,
        Err(e) if e.sql_diagnostics().and_then(|d| d.sqlstate.as_deref()) == Some("3D000") => {
            let maintenance = guard
                .connecting(dialer.connect(&spec, &spec.maintenance_db))
                .await?;
            guard
                .running(&maintenance, check_server(&maintenance))
                .await?;
            reporter.line(&format!(
                "Database \"{}\" does not exist; creating",
                spec.target_db
            ));
            guard
                .running(&maintenance, async {
                    maintenance
                        .client
                        .batch_execute(&format!("CREATE DATABASE {}", quote_ident(&spec.target_db)))
                        .await
                        .map_err(|e| created_elsewhere(sql_error("create the target database", &e)))
                })
                .await?;
            st.provision = Provision::Created;
            drop(maintenance);
            guard
                .connecting(dialer.connect(&spec, &spec.target_db))
                .await?
        }
        Err(e) => return Err(e),
    };
    guard.running(&session, check_server(&session)).await?;
    guard
        .running(
            &session,
            client_encoding(&session, spec.client_encoding.as_deref()),
        )
        .await?;
    let version = guard
        .running(&session, async {
            Ok(first_value(&session, "SHOW server_version")
                .await
                .ok()
                .flatten())
        })
        .await?;
    st.server = Some(ServerInfo {
        version,
        tls: Tls {
            enabled: session.tls_enabled(),
            sslmode: tls::effective_mode(&spec),
            version: session.tls_version.clone(),
        },
    });
    let locked = guard
        .running(&session, async {
            first_value(
                &session,
                "SELECT pg_try_advisory_lock(hashtextextended('pgmi.deploy.' || current_database(), 0))::text",
            )
            .await
        })
        .await?;
    if locked.as_deref() != Some("true") {
        return Err(Error::new(
            Kind::Locked,
            format!(
                "another pgmi deploy is already running against \"{}\"; wait for it to finish",
                spec.target_db
            ),
        ));
    }
    let outcome = execute::run(execute::Run {
        session: &session,
        guard,
        reporter,
        project: &project,
        meta: &meta,
        params: &params,
        verbose: ctx.verbose,
        files_loaded,
        execution: &mut st.execution,
    })
    .await;
    let cleanup = async {
        if session.tap.status() != crate::conn::tap::IDLE {
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
    outcome
}

async fn overwrite(
    ctx: &Context,
    st: &mut Progress,
    guard: &Guard,
    reporter: &Reporter,
    dialer: &Dialer,
    spec: &Spec,
) -> Result<Session> {
    let db = spec.target_db.as_str();
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
            .waiting(ctx.approver.approve_drop(db, reporter, &guard.token))
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
        reporter,
        &mut st.provision,
    )
    .await?;
    guard.check()?;
    Ok(maintenance)
}

#[cfg(feature = "testhooks")]
#[allow(clippy::panic)]
fn test_panic() -> ! {
    panic!("PGMI_TEST_PANIC is set")
}

struct NoStdin;

impl std::io::Read for NoStdin {
    fn read(&mut self, _: &mut [u8]) -> std::io::Result<usize> {
        Err(std::io::Error::other("stdin is not available here"))
    }
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
            .map_err(|e| sql_error("match the client encoding to the database", &e));
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
        .map_err(|e| sql_error("read the server version", &e))?;
    let (num, text) = rows
        .iter()
        .find_map(|m| match m {
            tokio_postgres::SimpleQueryMessage::Row(r) => Some((
                r.get(0).unwrap_or_default().to_string(),
                r.get(1).unwrap_or_default().to_string(),
            )),
            _ => None,
        })
        .unwrap_or_default();
    supported_server(&num, &text)
}

async fn first_value(session: &Session, sql: &str) -> Result<Option<String>> {
    let rows = session
        .client
        .simple_query(sql)
        .await
        .map_err(|e| sql_error("query the server", &e))?;
    Ok(rows.iter().find_map(|m| match m {
        tokio_postgres::SimpleQueryMessage::Row(r) => r.get(0).map(str::to_string),
        _ => None,
    }))
}

fn finish(ctx: &Context, run: &Run, result: Result<()>, reporter: &Reporter) -> u8 {
    let st = &run.st;
    let elapsed = run.t0.elapsed();
    let code = match &result {
        Ok(()) => 0,
        Err(e) => e.exit_code(),
    };
    let secrets = run.secrets();
    if let Some((ok, text)) = summary(st, &result, elapsed) {
        reporter.summary(ok, &text);
    }
    if let Err(e) = &result {
        let text = format!("pgmi: error: {}{}", e.text(), run.diagnostics(e));
        reporter.line(&redact(&blame::redact_sql_passwords(&text), &secrets));
    }
    if ctx.request.json {
        let record = run.record(ctx, &result);
        let mut out = std::io::stdout().lock();
        let text = serde_json::to_string_pretty(&record).unwrap_or_default();
        let _ = writeln!(out, "{text}");
    }
    code
}

fn summary(p: &Progress, result: &Result<()>, elapsed: Duration) -> Option<(bool, String)> {
    let db = p.database()?;
    let e = match result {
        Ok(()) => {
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
        Err(e) => e,
    };
    let mut text = format!("{db}: failed after {}", seconds(elapsed));
    let committed = p.execution.as_ref().map_or(0, |x| x.committed);
    let mut notes = Vec::new();
    match &p.execution {
        Some(x) if committed > 0 && x.failure.is_some() => {
            let failed = x.failure.as_ref().map_or(0, |f| f.unit);
            text.push_str(&format!(
                " (unit {failed} of {} failed; {committed} earlier unit(s) already committed)",
                x.units
            ));
        }
        Some(_) if e.kind() == Kind::Sql => notes.push("nothing committed".to_string()),
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
        text.push_str(&format!(" — {}", notes.join("; ")));
    }
    Some((false, text))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::{Value, json};

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

    fn spec(db: &str) -> Spec {
        let flags = resolve::Flags {
            connection: Some(format!("postgresql://u:s3cret-pw@db.example:6543/{db}").into()),
            ..resolve::Flags::default()
        };
        resolve::resolve(&flags, &Env::default(), None).unwrap()
    }

    fn loaded(db: &str) -> Progress {
        Progress {
            contract: true,
            spec: Some(spec(db)),
            deployer: true,
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
            candidates: files.iter().map(|f| f.to_string()).collect(),
            file: files.first().map(|f| f.to_string()),
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

    fn context() -> Context {
        Context {
            request: Request::default(),
            verbose: false,
            env: Env::default(),
            dotenv_keys: vec!["PGPASSWORD".into()],
            stdin: false,
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
        let (reporter, out) = crate::output::Captured::reporter(false, false, false);
        let code = finish(
            &context(),
            &run,
            Err(Error::sql("password s3cret-pw rejected")),
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
        finish(&context(), &run, Err(sql_failure("boom")), &reporter);
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
        run.st = p;
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
        assert!(far > TokioInstant::now() + Duration::from_secs(365 * 24 * 3600));
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
        let hint = privilege_hint("create the target database", "42501").unwrap();
        assert!(hint.contains("-d") && hint.contains("CREATEDB"));
        assert!(
            privilege_hint("create the session tables", "42501")
                .unwrap()
                .contains("TEMPORARY")
        );
        assert_eq!(privilege_hint("create the target database", "42P04"), None);
        assert_eq!(privilege_hint("execute deploy.sql", "42501"), None);
    }
}
