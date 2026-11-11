mod execute;
mod overwrite;
pub(crate) mod script;
mod session;

use std::future::Future;
use std::io::Write;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime};

use serde_json::{Map, Value, json};
use tokio::time::Instant as TokioInstant;
use tokio_util::sync::CancellationToken;

use crate::VERSION;
use crate::cli::DeployArgs;
use crate::config::params::{self, Sources};
use crate::config::yaml::ProjectConfig;
use crate::conn::dial::{Dialer, Session};
use crate::conn::resolve::{self, Flags, Spec};
use crate::conn::tls;
use crate::env::Env;
use crate::error::redact;
use crate::error::{Error, Kind, Result, SqlDiagnostics};
use crate::output::{Notice, NoticeLog, Reporter, seconds, timestamp};
use crate::project::{metadata, walk};

pub(crate) fn sql_error(context: &str, e: &tokio_postgres::Error) -> Error {
    match e.as_db_error() {
        Some(db) => {
            let code = db.code().code().to_string();
            let mut message = format!("ERROR: {} (SQLSTATE {code})", db.message());
            if code == "42501" && context == "create the session tables" {
                message.push_str(
                    "\nthe login needs the TEMPORARY privilege on the target database: GRANT TEMPORARY ON DATABASE <db> TO <role>",
                );
            }
            Error::sql(message).with_sql(SqlDiagnostics {
                sqlstate: Some(code),
                detail: db.detail().map(str::to_string),
                hint: db.hint().map(str::to_string),
                where_: db.where_().map(str::to_string),
                internal_query: match db.position() {
                    Some(tokio_postgres::error::ErrorPosition::Internal { query, .. }) => {
                        Some(query.clone())
                    }
                    _ => None,
                },
            })
        }
        None => {
            let mut text = e.to_string();
            let mut cur = std::error::Error::source(e);
            while let Some(c) = cur {
                text.push_str(&format!(": {c}"));
                cur = c.source();
            }
            Error::connection(format!("{context}: {text}"))
        }
    }
}

pub(crate) struct Context {
    pub(crate) args: DeployArgs,
    pub(crate) verbose: bool,
    pub(crate) env: Env,
    pub(crate) dotenv_keys: Vec<String>,
    pub(crate) maintenance_db: Option<String>,
    pub(crate) stdin: bool,
    pub(crate) approval: Approval,
}

pub(crate) enum Approval {
    Prompt,
    Confirm(Option<String>),
}

#[derive(Default)]
struct State {
    spec: Option<Spec>,
    database: Option<String>,
    server_version: Option<String>,
    tls: Option<Value>,
    created: Option<bool>,
    recreated: Option<bool>,
    files_loaded: Option<usize>,
    paths_ignored: Option<Value>,
    test_macros: Option<usize>,
    units: Option<usize>,
    committed: Option<usize>,
    failed_unit: Option<usize>,
    contract: bool,
    phase_test: bool,
    failed_files: Vec<String>,
    deployer: bool,
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
    st: State,
    log: Arc<Mutex<NoticeLog>>,
    started: SystemTime,
    t0: Instant,
}

impl Run {
    pub(crate) fn start() -> Self {
        Run {
            st: State::default(),
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
        self.st.database.as_deref()
    }

    pub(crate) fn notice_counts(&self) -> (u64, u64) {
        self.log
            .lock()
            .map(|l| (l.total, l.warning_total))
            .unwrap_or_default()
    }

    pub(crate) fn location(&self, e: &Error) -> Option<&str> {
        if e.kind() != Kind::Sql || self.st.failed_unit.is_none() {
            return None;
        }
        Some(match self.st.failed_files.first() {
            Some(f) => f.as_str(),
            None if self.st.phase_test => "test",
            None => "deploy.sql",
        })
    }

    pub(crate) fn secrets(&self) -> Vec<String> {
        self.st
            .spec
            .iter()
            .flat_map(|s| {
                [s.password.clone(), s.sslpassword.clone()]
                    .into_iter()
                    .flatten()
                    .chain(s.token.iter().flat_map(|t| t.secrets()))
            })
            .collect()
    }

    pub(crate) fn record(&self, ctx: &Context, result: &Result<()>) -> Value {
        let code = match result {
            Ok(()) => 0,
            Err(e) => e.exit_code(),
        };
        record(
            ctx,
            &self.st,
            result,
            code,
            self.started,
            self.t0.elapsed(),
            &self.log,
            &self.secrets(),
        )
    }
}

pub(crate) fn run(ctx: Context) -> u8 {
    let reporter = Reporter::new(ctx.verbose, &ctx.env);
    let runtime = match tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
    {
        Ok(r) => r,
        Err(e) => {
            reporter.line(&format!("pgmi: error: start the runtime: {e}"));
            return Kind::General.exit_code();
        }
    };
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
    ctx.args
        .timeout
        .or_else(|| {
            ProjectConfig::load(&ctx.args.project)
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
    st: &mut State,
    guard: &Guard,
    reporter: &Arc<Reporter>,
    log: Arc<Mutex<NoticeLog>>,
) -> Result<()> {
    let a = &ctx.args;
    if let Some(c) = &a.compat
        && c != "1"
    {
        return Err(Error::config(format!(
            "unsupported API version \"{c}\"; supported: 1"
        )));
    }
    st.contract = true;
    crate::vfs::OsFs::project(&a.project)?;
    let yaml = ProjectConfig::load(&a.project)?;
    if let Some(y) = &yaml {
        y.timeout()?;
    }
    let cloud = crate::conn::cloud::select(a, yaml.as_ref())?;
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
    let flags = Flags {
        connection: a.connection.clone(),
        host: a.host.clone(),
        port: a.port.clone(),
        username: a.username.clone(),
        database: a.database.clone(),
        sslmode: a.sslmode.clone(),
        sslcert: a.sslcert.clone(),
        sslkey: a.sslkey.clone(),
        sslrootcert: a.sslrootcert.clone(),
    };
    let mut spec = resolve::resolve(&flags, &ctx.env, yaml.as_ref().map(|y| &y.connection))?;
    if let Some((provider, req)) = &cloud {
        crate::conn::cloud::check(*provider, req, &mut spec, &ctx.env)?;
    }
    if let Some(m) = &ctx.maintenance_db {
        spec.maintenance_db = m.clone();
    }
    st.database = Some(spec.target_db.clone());
    st.spec = Some(spec.clone());
    #[cfg(feature = "testhooks")]
    if ctx.env.non_empty("PGMI_TEST_PANIC")?.is_some() {
        test_panic();
    }
    if a.overwrite
        && let Approval::Confirm(confirm) = &ctx.approval
    {
        let db = &spec.target_db;
        match confirm {
            None => {
                return Err(Error::new(
                    Kind::Denied,
                    format!(
                        "overwrite=true drops database \"{db}\": pass confirmDatabaseName=\"{db}\" to confirm"
                    ),
                ));
            }
            Some(c) if c != db => {
                return Err(Error::new(
                    Kind::Denied,
                    format!(
                        "confirmDatabaseName \"{c}\" does not match database \"{db}\"; nothing was deployed and no database was dropped"
                    ),
                ));
            }
            Some(_) => {}
        }
    }
    st.deployer = true;
    let max_size = walk::max_size(&ctx.env)?;
    let fs = crate::vfs::OsFs::new(a.project.clone());
    let project = tokio::task::spawn_blocking(move || walk::load(&fs, max_size))
        .await
        .map_err(|e| Error::internal(format!("project loader: {e}")))??;
    let meta = metadata::collect(&project.sources)?;
    st.files_loaded = Some(project.sources.len() + project.test_dir_files.len());
    st.paths_ignored = Some(json!(
        project
            .ignored
            .iter()
            .map(|i| json!({"path": i.path, "rule": i.rule}))
            .collect::<Vec<_>>()
    ));
    st.test_macros = Some(0);
    if ctx.verbose {
        for i in &project.ignored {
            reporter.line(&format!("ignored {} ({})", i.path, i.rule));
        }
    }
    st.created = Some(false);
    st.recreated = Some(false);
    if !spec.source.typed() {
        let label = spec.source.label();
        if label == "defaults" {
            reporter.line(&format!("connection: defaults ({})", spec.describe()));
        } else {
            reporter.line(&format!("connection: from {label} ({})", spec.describe()));
        }
    }
    if a.overwrite {
        overwrite::refuse_maintenance(&spec)?;
    }
    if a.overwrite && matches!(ctx.approval, Approval::Prompt) {
        overwrite::approve(a, &spec, &ctx.env, reporter, &guard.token).await?;
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
    if a.overwrite {
        let plan = guard.connecting(overwrite::prepare(&dialer, &spec)).await?;
        let recreated = guard
            .running(&plan.maintenance, overwrite::recreate(&plan, reporter))
            .await?;
        st.recreated = Some(recreated);
        st.created = Some(!recreated);
    }
    let session = match guard
        .connecting(dialer.connect(&spec, &spec.target_db))
        .await
    {
        Ok(s) => s,
        Err(e) if e.sql_diagnostics().and_then(|d| d.sqlstate.as_deref()) == Some("3D000") => {
            let maintenance = guard
                .connecting(dialer.connect(&spec, &spec.maintenance_db))
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
                        .map_err(|e| sql_error("create the target database", &e))
                })
                .await?;
            st.created = Some(true);
            drop(maintenance);
            guard
                .connecting(dialer.connect(&spec, &spec.target_db))
                .await?
        }
        Err(e) => return Err(e),
    };
    let version = guard
        .running(&session, async {
            Ok(first_value(&session, "SHOW server_version")
                .await
                .ok()
                .flatten())
        })
        .await?;
    st.server_version = version;
    st.tls = Some(json!({
        "enabled": session.tls_enabled(),
        "version": session.tls_version,
        "sslmode": tls::effective_mode(&spec),
    }));
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
        state: st,
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
    if let Some(db) = &st.database {
        match &result {
            Ok(()) => reporter.summary(
                true,
                &format!(
                    "{db}: {} files loaded{} in {}",
                    st.files_loaded.unwrap_or(0),
                    match st
                        .paths_ignored
                        .as_ref()
                        .and_then(Value::as_array)
                        .map_or(0, Vec::len)
                    {
                        0 => String::new(),
                        n => format!(", {n} paths ignored"),
                    },
                    seconds(elapsed)
                ),
            ),
            Err(e) => {
                let mut text = format!("{db}: failed after {}", seconds(elapsed));
                let committed = st.committed.unwrap_or(0);
                let mut notes = Vec::new();
                match (st.units, st.failed_unit) {
                    (Some(units), Some(failed)) if committed > 0 => text.push_str(&format!(
                        " (unit {failed} of {units} failed; {committed} earlier unit(s) already committed)"
                    )),
                    (Some(_), _) if e.kind() == Kind::Sql => notes.push("nothing committed".to_string()),
                    _ => {}
                }
                if st.created == Some(true) && committed == 0 {
                    notes.push(format!("created database \"{db}\", left empty"));
                }
                if !notes.is_empty() {
                    text.push_str(&format!(" — {}", notes.join("; ")));
                }
                reporter.summary(false, &text);
            }
        }
    }
    if let Err(e) = &result {
        reporter.line(&redact(&format!("pgmi: error: {}", e.text()), &secrets));
        if let Some(location) = run.location(e) {
            reporter.line(&format!("LOCATION: {location}"));
        }
    }
    if ctx.args.json {
        let record = run.record(ctx, &result);
        let mut out = std::io::stdout().lock();
        let text = serde_json::to_string_pretty(&record).unwrap_or_default();
        let _ = writeln!(out, "{text}");
    }
    code
}

#[allow(clippy::too_many_arguments)]
fn record(
    ctx: &Context,
    st: &State,
    result: &Result<()>,
    code: u8,
    started: SystemTime,
    elapsed: Duration,
    log: &Mutex<NoticeLog>,
    secrets: &[String],
) -> Value {
    let mut r = Map::new();
    r.insert(
        "status".into(),
        json!(if code == 0 { "success" } else { "failed" }),
    );
    r.insert("exitCode".into(), json!(code));
    r.insert("schemaVersion".into(), json!(1));
    r.insert("pgmiVersion".into(), json!(VERSION));
    r.insert(
        "contractVersion".into(),
        if st.contract { json!("1") } else { Value::Null },
    );
    r.insert("serverVersion".into(), json!(st.server_version));
    r.insert("tls".into(), st.tls.clone().unwrap_or(Value::Null));
    r.insert(
        "target".into(),
        match &st.spec {
            Some(s) => {
                let (h, p) = s.first();
                json!({"host": h, "port": p, "database": s.target_db})
            }
            None => Value::Null,
        },
    );
    r.insert(
        "connectionSource".into(),
        json!(st.spec.as_ref().map(|s| s.source.label())),
    );
    r.insert("overrides".into(), json!([]));
    r.insert("dotenvKeys".into(), json!(ctx.dotenv_keys));
    r.insert("startedAt".into(), json!(timestamp(started)));
    r.insert("finishedAt".into(), json!(timestamp(started + elapsed)));
    r.insert("database".into(), json!(st.database));
    r.insert("created".into(), json!(st.created));
    r.insert("recreated".into(), json!(st.recreated));
    r.insert("durationMs".into(), json!(elapsed.as_millis() as u64));
    r.insert("filesLoaded".into(), json!(st.files_loaded));
    let ignored = st.paths_ignored.clone().unwrap_or_else(|| json!([]));
    r.insert(
        "pathsIgnored".into(),
        if st.paths_ignored.is_some() {
            json!(ignored.as_array().map_or(0, Vec::len))
        } else {
            Value::Null
        },
    );
    r.insert("ignoredPaths".into(), ignored);
    r.insert("testMacros".into(), json!(st.test_macros));
    if let Ok(l) = log.lock() {
        let (notices, dropped) = l.finish();
        r.insert("notices".into(), notices);
        if let Some(d) = dropped {
            r.insert("noticesTruncated".into(), json!(d));
        }
    }
    if let Some(u) = st.units {
        r.insert("executionUnits".into(), json!(u));
        r.insert("unitsCommitted".into(), json!(st.committed.unwrap_or(0)));
    }
    if let Err(e) = result {
        if st.units.is_some() && st.failed_unit.is_some() {
            r.insert(
                "executionMode".into(),
                json!(if st.committed.unwrap_or(0) == 0 {
                    "atomic"
                } else {
                    "psql"
                }),
            );
        }
        r.insert("error".into(), json!(redact(&e.text(), secrets)));
        if let Some(d) = e.sql_diagnostics() {
            if let Some(s) = &d.sqlstate {
                r.insert("sqlstate".into(), json!(s));
            }
            if let Some(s) = &d.detail {
                r.insert("detail".into(), json!(s));
            }
            if let Some(s) = &d.hint {
                r.insert("hint".into(), json!(s));
            }
            if let Some(s) = &d.where_ {
                r.insert("where".into(), json!(s));
            }
            if st.failed_unit.is_some() {
                r.insert(
                    "phase".into(),
                    json!(if st.phase_test { "test" } else { "deploy" }),
                );
                match st.failed_files.as_slice() {
                    [] if !st.phase_test => {
                        r.insert("failedFile".into(), json!("deploy.sql"));
                        r.insert("script".into(), json!("deploy.sql"));
                    }
                    [] => {}
                    files => {
                        r.insert("failedFile".into(), json!(files[0]));
                        r.insert("script".into(), json!(files[0]));
                        if files.len() > 1 {
                            r.insert("failedFileCandidates".into(), json!(files));
                        }
                    }
                }
            }
        }
    }
    Value::Object(r)
}

#[cfg(test)]
mod tests {
    use super::*;

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

    #[test]
    fn a_huge_timeout_never_overflows_the_deadline() {
        let far = deadline(Duration::MAX);
        assert!(far > TokioInstant::now() + Duration::from_secs(365 * 24 * 3600));
    }
}
