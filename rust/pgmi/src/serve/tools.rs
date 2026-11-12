use std::fmt::Write as _;
use std::path::PathBuf;

use serde::Serialize;
use serde_json::value::{RawValue, to_raw_value};
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;

use super::wire::{JsonKind, Members};
use crate::commands::{init, metadata};
use crate::config::params::{Param, check_key};
use crate::conn::resolve::Flags;
use crate::deploy::{ConfirmedName, Context, Request, Run, Stdin, Stop, redact_sql_passwords};
use crate::env::Env;
use crate::error::redact;
use crate::error::{Error, Kind};
use crate::output::Reporter;
use crate::templates::Template;

/// The tools a server lists and calls.
pub(crate) trait Toolbox: Send + Sync + 'static {
    type Tool: Send + 'static;

    /// `tools/list`'s array as JSON text, as clients see it.
    fn list(&self) -> &str;

    fn tool(&self, name: &str) -> Option<Self::Tool>;

    /// Runs one call on its own task, inside [`crate::blocking::Slot::hold`];
    /// a panic becomes the call's answer. Blocking work starts through
    /// [`crate::blocking::spawn`], so it keeps the call's place until it ends.
    fn call(
        &self,
        tool: Self::Tool,
        arguments: Option<Box<RawValue>>,
        token: CancellationToken,
    ) -> impl std::future::Future<Output = Outcome> + Send;
}

/// pgmi's own tools.
pub(crate) struct Tools {
    pub(crate) env: Env,
    pub(crate) verbose: bool,
}

impl Toolbox for Tools {
    type Tool = Tool;

    fn list(&self) -> &str {
        super::TOOLS
    }

    fn tool(&self, name: &str) -> Option<Tool> {
        Tool::named(name)
    }

    fn call(
        &self,
        tool: Tool,
        arguments: Option<Box<RawValue>>,
        token: CancellationToken,
    ) -> impl std::future::Future<Output = Outcome> + Send {
        tool.call(arguments, self.env.clone(), self.verbose, token)
    }
}

#[derive(Clone, Copy)]
pub(crate) enum Tool {
    Deploy,
    Blocking(Blocking),
}

/// The tools that answer from files on a blocking thread.
#[derive(Clone, Copy)]
pub(crate) enum Blocking {
    Init,
    MetadataPlan,
    MetadataValidate,
    TemplatesList,
    AiOverview,
    AiContract,
}

const DEPLOY_ARGS: [&str; 10] = [
    "confirmDatabaseName",
    "connection",
    "database",
    "maintenanceDatabase",
    "overwrite",
    "params",
    "paramsFile",
    "path",
    "timeout",
    "verbose",
];

impl Tool {
    pub(crate) fn named(name: &str) -> Option<Tool> {
        if name == "deploy" {
            return Some(Tool::Deploy);
        }
        Some(Tool::Blocking(match name {
            "init" => Blocking::Init,
            "metadata_plan" => Blocking::MetadataPlan,
            "metadata_validate" => Blocking::MetadataValidate,
            "templates_list" => Blocking::TemplatesList,
            "ai_overview" => Blocking::AiOverview,
            "ai_contract" => Blocking::AiContract,
            _ => return None,
        }))
    }

    pub(crate) async fn call(
        self,
        arguments: Option<Box<RawValue>>,
        env: Env,
        verbose: bool,
        token: CancellationToken,
    ) -> Outcome {
        let blocking = match self {
            Tool::Deploy => {
                return match deploy_context(arguments.as_deref(), &env, verbose) {
                    Ok(ctx) => run_deploy(ctx, token).await,
                    Err(e) => Outcome::failed(&e),
                };
            }
            Tool::Blocking(b) => b,
        };
        let work = on_blocking_thread(move || blocking.answer(arguments.as_deref(), &env));
        if blocking.writes() {
            return work.await;
        }
        until_cancelled(&token, work).await
    }
}

/// Waits for `work` unless the call is cancelled first.
async fn until_cancelled(
    token: &CancellationToken,
    work: impl std::future::Future<Output = Outcome>,
) -> Outcome {
    tokio::select! {
        biased;
        () = token.cancelled() => Outcome::failed(&Error::new(Kind::Interrupted, "the tool call was cancelled")),
        done = work => done,
    }
}

async fn on_blocking_thread(work: impl FnOnce() -> Outcome + Send + 'static) -> Outcome {
    match crate::blocking::spawn(move || crate::error::catching(work)).await {
        Ok(Ok(o)) => o,
        Ok(Err(p)) => Outcome::panicked(&p),
        Err(_) => Outcome::failed(&Error::internal("the tool call was aborted")),
    }
}

impl Blocking {
    /// A tool that writes files finishes even when its call is cancelled,
    /// so a session never ends on a half-written project. The others are
    /// left to run out on their own.
    fn writes(self) -> bool {
        matches!(self, Blocking::Init)
    }

    fn answer(self, arguments: Option<&RawValue>, env: &Env) -> Outcome {
        match self {
            Blocking::Init => match init(arguments) {
                Ok(v) => Outcome::json(&v),
                Err(e) => Outcome::failed(&e),
            },
            Blocking::MetadataPlan => {
                match path_argument(arguments, &["path"], "cli.mcpPathArgs")
                    .and_then(|(p, _)| metadata::plan_report(std::path::Path::new(&p), env))
                {
                    Ok(plan) => Outcome::json(&plan),
                    Err(e) => Outcome::failed(&e),
                }
            }
            Blocking::MetadataValidate => {
                match path_argument(arguments, &["path"], "cli.mcpPathArgs")
                    .and_then(|(p, _)| metadata::validate_report(std::path::Path::new(&p), env))
                {
                    Ok(metadata::Validated::Report(v)) => Outcome::json(&v),
                    Ok(metadata::Validated::Unreadable(errors)) => {
                        Outcome::failed(&Error::config(metadata::unreadable(&errors).join("\n")))
                    }
                    Err(e) => Outcome::failed(&e),
                }
            }
            Blocking::TemplatesList => {
                let mut list = Template::ALL;
                list.sort_by_key(|t| t.name());
                let templates: Vec<Value> = list
                    .iter()
                    .map(|t| {
                        json!({"bestFor": t.best_for(), "description": t.summary(), "name": t.name()})
                    })
                    .collect();
                Outcome::json(&json!({ "templates": templates }))
            }
            Blocking::AiOverview => {
                let text = pgmi_shared::file("ai/overview.md")
                    .map(|b| String::from_utf8_lossy(b).into_owned())
                    .unwrap_or_default();
                Outcome {
                    text,
                    structured: None,
                    is_error: false,
                }
            }
            Blocking::AiContract => Outcome {
                text: crate::commands::ai::CONTRACT.trim_end().to_string(),
                structured: None,
                is_error: false,
            },
        }
    }
}

fn init(arguments: Option<&RawValue>) -> Result<Value, Error> {
    let (path, args) = path_argument(arguments, &["name", "path", "template"], "cli.mcpInitArgs")?;
    let template = args.string("template")?.filter(|t| !t.is_empty());
    let template = template.as_deref().unwrap_or("basic");
    let target = std::path::Path::new(&path);
    init::check_empty(&crate::vfs::OsFs::new(target))?;
    init::scaffold(
        target,
        init::template(template)?,
        args.string("name")?.as_deref(),
        &|_| {},
    )?;
    Ok(json!({"created": true, "path": path, "template": template}))
}

#[derive(Serialize)]
struct Text {
    #[serde(rename = "type")]
    kind: &'static str,
    text: String,
}

#[derive(Serialize)]
struct ToolResult<'a> {
    content: [Text; 1],
    #[serde(rename = "structuredContent", skip_serializing_if = "Option::is_none")]
    structured: Option<&'a RawValue>,
    #[serde(rename = "isError", skip_serializing_if = "std::ops::Not::not")]
    is_error: bool,
}

#[derive(Serialize)]
struct Failure<'a> {
    message: &'a str,
    #[serde(rename = "exitCode")]
    exit_code: u8,
    #[serde(skip_serializing_if = "Option::is_none")]
    stack: Option<&'a str>,
}

pub(crate) struct Outcome {
    text: String,
    structured: Option<Box<RawValue>>,
    is_error: bool,
}

impl Outcome {
    pub(crate) fn json(v: &impl Serialize) -> Self {
        Outcome {
            text: serde_json::to_string_pretty(v).unwrap_or_default(),
            structured: to_raw_value(v).ok(),
            is_error: false,
        }
    }

    pub(crate) fn failed(e: &Error) -> Self {
        Self::failure(&e.text(), e.exit_code(), None)
    }

    pub(crate) fn panicked(p: &crate::error::Panic) -> Self {
        let e = p.error();
        Self::failure(&e.text(), e.exit_code(), Some(p.stack()))
    }

    fn failure(message: &str, exit_code: u8, stack: Option<&str>) -> Self {
        Outcome {
            text: format!("{message}\nexit code {exit_code}"),
            structured: to_raw_value(&Failure {
                message,
                exit_code,
                stack,
            })
            .ok(),
            is_error: true,
        }
    }

    pub(crate) fn render(&self, era: super::Era) -> String {
        let result = ToolResult {
            content: [Text {
                kind: "text",
                text: self.text.clone(),
            }],
            structured: self.structured.as_deref(),
            is_error: self.is_error,
        };
        if era == super::Era::Session {
            return serde_json::to_string(&result).unwrap_or_default();
        }
        let mut v = serde_json::to_value(&result).unwrap_or_default();
        if let Value::Object(m) = &mut v {
            m.insert(
                "_meta".into(),
                json!({ super::META_SERVER: super::server_info() }),
            );
            m.insert("resultType".into(), json!("complete"));
        }
        v.to_string()
    }
}

fn members(arguments: Option<&RawValue>, value_type: &str) -> Result<Vec<(String, Value)>, Error> {
    let Some(raw) = arguments else {
        return Ok(Vec::new());
    };
    match JsonKind::of(raw) {
        JsonKind::Null => Ok(Vec::new()),
        JsonKind::Object => {
            let m: Members = serde_json::from_str(raw.get())
                .map_err(|e| Error::general(format!("json: {e}")))?;
            Ok(m.0
                .into_iter()
                .map(|(k, v)| (k, serde_json::from_str(v.get()).unwrap_or(Value::Null)))
                .collect())
        }
        k => Err(Error::general(format!(
            "json: cannot unmarshal {k} into Go value of type {value_type}"
        ))),
    }
}

fn check_known(members: &[(String, Value)], accepted: &[&str]) -> Result<(), Error> {
    match members
        .iter()
        .find(|(k, _)| !accepted.contains(&k.as_str()))
    {
        Some((k, _)) => Err(Error::config(format!(
            "unknown argument {k:?}; accepted: {}",
            accepted.join(", ")
        ))),
        None => Ok(()),
    }
}

struct Args {
    members: Vec<(String, Value)>,
    struct_name: String,
}

impl Args {
    fn get(&self, key: &str) -> Option<&Value> {
        self.members
            .iter()
            .rev()
            .find(|(k, v)| k == key && !v.is_null())
            .map(|(_, v)| v)
    }

    fn type_error(&self, got: &Value, field: &str, ty: &str) -> Error {
        Error::general(format!(
            "json: cannot unmarshal {} into Go struct field {}.{field} of type {ty}",
            JsonKind::of_value(got),
            self.struct_name
        ))
    }

    fn string(&self, key: &str) -> Result<Option<String>, Error> {
        match self.get(key) {
            None => Ok(None),
            Some(Value::String(s)) => Ok(Some(s.clone())),
            Some(v) => Err(self.type_error(v, key, "string")),
        }
    }

    fn bool(&self, key: &str) -> Result<bool, Error> {
        match self.get(key) {
            None => Ok(false),
            Some(Value::Bool(b)) => Ok(*b),
            Some(v) => Err(self.type_error(v, key, "bool")),
        }
    }
}

fn path_argument(
    arguments: Option<&RawValue>,
    accepted: &[&str],
    value_type: &str,
) -> Result<(String, Args), Error> {
    let members = members(arguments, value_type)?;
    check_known(&members, accepted)?;
    let args = Args {
        members,
        struct_name: value_type.trim_start_matches("cli.").to_string(),
    };
    for key in accepted {
        args.string(key)?;
    }
    match args.string("path")? {
        Some(p) if !p.is_empty() => Ok((p, args)),
        _ => Err(Error::config("path is required")),
    }
}

/// The servers the operator's own credentials may go to: the `host:port`s
/// its environment resolves to, and the comma-separated `PGMI_SERVE_HOSTS`
/// (`host` for any port, or `host:port`).
fn operator_hosts(env: &Env) -> Result<Vec<String>, Error> {
    let mut hosts: Vec<String> = env
        .non_empty("PGMI_SERVE_HOSTS")?
        .map(|v| {
            v.split(',')
                .map(str::trim)
                .filter(|h| !h.is_empty())
                .map(str::to_string)
                .collect()
        })
        .unwrap_or_default();
    if let Ok(spec) = crate::conn::resolve::resolve(&Flags::default(), env, None) {
        hosts.extend(
            spec.hosts
                .endpoints()
                .map(|e| format!("{}:{}", e.name, e.port)),
        );
    }
    Ok(hosts)
}

/// A connection from a tool call may not point the operator's password
/// file elsewhere, or connect to an address other than the host whose
/// `.pgpass` entry it gets.
fn refuse_operator_keys(connection: &str) -> Result<(), Error> {
    let fields = crate::conn::parse::parse(connection)?;
    match ["hostaddr", "passfile"]
        .into_iter()
        .find(|k| fields.contains_key(*k))
    {
        Some(key) => Err(Error::config(format!(
            "connection key \"{key}\" is not accepted from a tool call; set it in the environment pgmi serve runs in"
        ))),
        None => Ok(()),
    }
}

fn deploy_context(
    arguments: Option<&RawValue>,
    env: &Env,
    verbose: bool,
) -> Result<Context, Error> {
    let members = members(arguments, "cli.mcpDeployArgs")?;
    check_known(&members, &DEPLOY_ARGS)?;
    let a = Args {
        members,
        struct_name: "mcpDeployArgs".into(),
    };
    let path = a.string("path")?;
    let connection = a.string("connection")?;
    let database = a.string("database")?;
    let maintenance = a.string("maintenanceDatabase")?;
    let timeout = a.string("timeout")?;
    let confirm = a.string("confirmDatabaseName")?;
    let overwrite = a.bool("overwrite")?;
    let verbose = a.bool("verbose")? || verbose;
    let mut pairs = Vec::new();
    match a.get("params") {
        None => {}
        Some(Value::Object(m)) => {
            for (k, v) in m {
                match v {
                    Value::String(s) => pairs.push((k, s)),
                    Value::Null => {}
                    other => return Err(a.type_error(other, &format!("params.{k}"), "string")),
                }
            }
        }
        Some(v) => return Err(a.type_error(v, "params", "map[string]string")),
    }
    let params = pairs
        .into_iter()
        .map(|(k, v)| check_key(k).map(|()| Param::Pair(k.clone(), v.clone())))
        .collect::<Result<Vec<_>, Error>>()?;
    let params_files = match a.get("paramsFile") {
        None => Vec::new(),
        Some(Value::String(s)) => vec![s.clone()],
        Some(Value::Array(items)) => items
            .iter()
            .map(|i| i.as_str().map(str::to_string))
            .collect::<Option<Vec<_>>>()
            .ok_or_else(|| Error::general("must be a string or an array of strings"))?,
        Some(_) => return Err(Error::general("must be a string or an array of strings")),
    };
    let path = match path {
        Some(p) if !p.is_empty() => PathBuf::from(p),
        _ => return Err(Error::config("path is required")),
    };
    if let Some(c) = connection.as_deref() {
        refuse_operator_keys(c)?;
    }
    if !path.exists() {
        return Err(Error::config(format!(
            "project path {:?} does not exist",
            path.display().to_string()
        )));
    }
    let timeout = match timeout {
        None => None,
        Some(t) => Some(
            crate::duration::parse(&t)
                .map_err(|e| Error::config(format!("invalid timeout: time: {e}")))?,
        ),
    };
    Ok(Context {
        request: Request {
            project: path,
            connection: Flags {
                connection: connection.map(Into::into),
                database,
                ..Flags::default()
            },
            maintenance_db: maintenance,
            params,
            params_files,
            timeout,
            overwrite,
            ..Request::default()
        },
        verbose,
        env: env.clone(),
        dotenv_keys: Vec::new(),
        dotenv_refused: Vec::new(),
        serve_hosts: Some(operator_hosts(env)?),
        stdin: Stdin::Unavailable,
        approver: Box::new(ConfirmedName(confirm)),
    })
}

async fn run_deploy(ctx: Context, token: CancellationToken) -> Outcome {
    let reporter = Reporter::unstamped(ctx.verbose, &ctx.env);
    let mut run = Run::start();
    let result = run.deploy(&ctx, &reporter, Stop::on(token)).await;
    let secrets = run.secrets();
    let mut record = run.record(&ctx, &result);
    match &result {
        Ok(()) => {
            let crate::deploy::NoticeCounts { notices, warnings } = run.notice_counts();
            let mut text = format!(
                "deployed {}: {} files loaded, {} paths ignored, {notices} notices ({warnings} warnings) in {}ms; the full record is in structuredContent",
                run.database().unwrap_or_default(),
                record.files_loaded.unwrap_or(0),
                record.paths_ignored.unwrap_or(0),
                record.duration_ms.unwrap_or(0),
            );
            let warning_lines = record.warning_lines();
            if !warning_lines.is_empty() {
                let _ = write!(text, "\n{warning_lines}");
            }
            Outcome {
                text,
                structured: to_raw_value(&record).ok(),
                is_error: false,
            }
        }
        Err(e) if !run.reached_deployer() => Outcome::failure(
            &redact(&redact_sql_passwords(&e.text()), &secrets),
            e.exit_code(),
            None,
        ),
        Err(e) => {
            let message = redact(&redact_sql_passwords(&e.text()), &secrets);
            let mut text = message.clone();
            text.push_str(&redact(&run.diagnostics(e), &secrets));
            let _ = write!(text, "\nexit code {}", e.exit_code());
            if let Some(phase) = record.phase {
                let _ = write!(text, ", phase {phase}");
            }
            let warning_lines = record.warning_lines();
            if !warning_lines.is_empty() {
                let _ = write!(text, "\n{warning_lines}");
            }
            record.message = Some(message);
            Outcome {
                text,
                structured: to_raw_value(&record).ok(),
                is_error: true,
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn slots(
        n: usize,
    ) -> (
        std::sync::Arc<tokio::sync::Semaphore>,
        crate::blocking::Slot,
    ) {
        let slots = std::sync::Arc::new(tokio::sync::Semaphore::new(n));
        let slot = crate::blocking::Slot::from(slots.clone().try_acquire_owned().unwrap());
        (slots, slot)
    }

    #[tokio::test]
    async fn a_cancelled_blocking_call_stops_waiting_but_keeps_its_slot() {
        let token = CancellationToken::new();
        let (slots, slot) = slots(1);
        let (release, stalled) = std::sync::mpsc::channel::<()>();
        let call = tokio::spawn({
            let token = token.clone();
            slot.hold(async move {
                let work = on_blocking_thread(move || {
                    let _ = stalled.recv_timeout(std::time::Duration::from_secs(30));
                    Outcome::json(&json!("finished"))
                });
                until_cancelled(&token, work).await
            })
        });
        tokio::time::sleep(std::time::Duration::from_millis(50)).await;
        token.cancel();
        let outcome = tokio::time::timeout(std::time::Duration::from_secs(2), call)
            .await
            .expect("the call waited for its blocking thread")
            .unwrap();
        assert!(outcome.is_error);
        assert!(outcome.text.contains("exit code 130"), "{}", outcome.text);
        assert_eq!(
            slots.available_permits(),
            0,
            "the slot was freed while the thread ran"
        );
        drop(release);
        let freed = tokio::time::timeout(std::time::Duration::from_secs(5), slots.acquire())
            .await
            .expect("the slot never came back");
        assert!(freed.is_ok());
    }

    #[tokio::test]
    async fn a_cancelled_init_still_writes_the_whole_project() {
        let dir = crate::testdir::TempDir::new("serve-init-cancelled");
        let target = dir.path().join("project");
        let token = CancellationToken::new();
        token.cancel();
        let (slots, slot) = slots(1);
        let arguments = raw(&json!({"path": target, "template": "basic"}).to_string());
        let outcome = slot
            .hold(Tool::Blocking(Blocking::Init).call(
                Some(arguments),
                Env::default(),
                false,
                token,
            ))
            .await;
        assert!(!outcome.is_error, "{}", outcome.text);
        assert!(target.join("deploy.sql").is_file());
        assert_eq!(slots.available_permits(), 1);
    }

    fn raw(s: &str) -> Box<RawValue> {
        RawValue::from_string(s.to_string()).unwrap()
    }

    fn err(args: &str) -> Error {
        match deploy_context(Some(&raw(args)), &Env::default(), false) {
            Err(e) => e,
            Ok(_) => panic!("{args}"),
        }
    }

    #[test]
    fn deploy_arguments_fail_as_go_does() {
        assert_eq!(err("{}").text(), "path is required");
        assert_eq!(err("{}").exit_code(), 10);
        let e = err("{\"path\":\"/x\",\"bogus\":1}");
        assert!(
            e.text().starts_with(
                "unknown argument \"bogus\"; accepted: confirmDatabaseName, connection,"
            )
        );
        assert_eq!(
            err("\"x\"").text(),
            "json: cannot unmarshal string into Go value of type cli.mcpDeployArgs"
        );
        assert_eq!(err("\"x\"").exit_code(), 1);
        assert_eq!(
            err("{\"path\":5}").text(),
            "json: cannot unmarshal number into Go struct field mcpDeployArgs.path of type string"
        );
        assert_eq!(
            err("{\"path\":\"/\",\"params\":{\"a\":1}}").text(),
            "json: cannot unmarshal number into Go struct field mcpDeployArgs.params.a of type string"
        );
        assert_eq!(
            err("{\"path\":\"/nonexistent-pgmi\"}").text(),
            "project path \"/nonexistent-pgmi\" does not exist"
        );
        let e = err("{\"path\":\"/\",\"params\":{\"env=prod\":\"x\"}}");
        assert_eq!(
            e.text(),
            "invalid parameter key 'env=prod': must start with a letter or underscore and contain only letters, digits and underscores, 1-63 characters (PostgreSQL identifier limit)"
        );
        assert_eq!(e.exit_code(), 10);
        let e = err("{\"path\":\"/\",\"timeout\":\"soon\"}");
        assert!(e.text().starts_with("invalid timeout"), "{}", e.text());
        assert_eq!(e.exit_code(), 10);
    }

    #[test]
    fn a_tool_call_cannot_pick_the_address_or_the_password_file() {
        for connection in [
            "host=prod.internal hostaddr=203.0.113.9 dbname=app",
            "postgresql://prod.internal/app?hostaddr=203.0.113.9",
            "host=db passfile=/home/op/.ssh/known_hosts dbname=app",
        ] {
            let e = err(&format!(
                "{{\"path\":\"/\",\"connection\":{}}}",
                serde_json::to_string(connection).unwrap()
            ));
            assert_eq!(e.exit_code(), 10, "{connection}");
            assert!(e.text().contains("from a tool call"), "{}", e.text());
        }
        deploy_context(
            Some(&raw(
                "{\"path\":\"/\",\"connection\":\"host=db dbname=app\"}",
            )),
            &Env::default(),
            false,
        )
        .unwrap();
    }

    #[test]
    fn deploy_arguments_map_to_flags() {
        let ctx = deploy_context(
            Some(&raw(
                "{\"path\":\"/\",\"database\":\"-d\",\"timeout\":\"2s\",\"params\":{\"k\":\"v\"},\"paramsFile\":[\"a\",\"b\"],\"overwrite\":true,\"confirmDatabaseName\":\"-d\"}",
            )),
            &Env::default(),
            false,
        )
        .unwrap();
        let r = &ctx.request;
        assert_eq!(r.project, std::path::Path::new("/"));
        assert_eq!(r.connection.database.as_deref(), Some("-d"));
        assert_eq!(r.timeout, Some(std::time::Duration::from_secs(2)));
        assert_eq!(r.params, [Param::Pair("k".into(), "v".into())]);
        assert_eq!(r.params_files, ["a", "b"]);
        assert!(r.overwrite && !r.force && !r.json && r.compat.is_none() && r.cloud.is_empty());
        assert_eq!(ctx.stdin, Stdin::Unavailable);
        ctx.approver.before_connecting("-d").unwrap();
        let e = ctx.approver.before_connecting("d").unwrap_err();
        assert_eq!(e.exit_code(), 12);
        assert_eq!(
            e.text(),
            "confirmDatabaseName \"-d\" does not match database \"d\"; nothing was deployed and no database was dropped"
        );
    }

    #[test]
    fn results_keep_go_field_order() {
        let failed =
            Outcome::failed(&Error::config("path is required")).render(super::super::Era::Session);
        assert_eq!(
            failed,
            "{\"content\":[{\"type\":\"text\",\"text\":\"path is required\\nexit code 10\"}],\"structuredContent\":{\"message\":\"path is required\",\"exitCode\":10},\"isError\":true}"
        );
        let stateless = Outcome::failed(&Error::config("x")).render(super::super::Era::Stateless);
        assert!(stateless.starts_with("{\"_meta\":{\"io.modelcontextprotocol/serverInfo\":"));
        assert!(stateless.contains("\"resultType\":\"complete\""));
    }

    #[test]
    fn deploy_record_has_the_keys_the_output_schema_requires() {
        let tools: Vec<Value> = serde_json::from_str(super::super::TOOLS).unwrap();
        let schema =
            &tools.iter().find(|t| t["name"] == "deploy").unwrap()["outputSchema"]["anyOf"];
        let ctx = deploy_context(Some(&raw("{\"path\":\"/\"}")), &Env::default(), false).unwrap();
        let mut record = serde_json::to_value(Run::start().record(&ctx, &Ok(()))).unwrap();
        record["database"] = json!("db");
        record["filesLoaded"] = json!(0);
        for key in schema[0]["required"].as_array().unwrap() {
            assert!(!record[key.as_str().unwrap()].is_null(), "{key}");
        }
        let failure: Value = serde_json::from_str(
            Outcome::failed(&Error::config("x"))
                .structured
                .unwrap()
                .get(),
        )
        .unwrap();
        for key in schema[1]["required"].as_array().unwrap() {
            assert!(!failure[key.as_str().unwrap()].is_null(), "{key}");
        }
    }

    fn conforms(schema: &Value, v: &Value) -> Result<(), String> {
        if let Some(any) = schema.get("anyOf").and_then(Value::as_array) {
            let errors: Vec<String> = any.iter().filter_map(|s| conforms(s, v).err()).collect();
            return if errors.len() < any.len() {
                Ok(())
            } else {
                Err(format!("no variant admits {v}: {}", errors.join("; ")))
            };
        }
        if let Some(t) = schema.get("type").and_then(Value::as_str) {
            let ok = match t {
                "object" => v.is_object(),
                "array" => v.is_array(),
                "string" => v.is_string(),
                "integer" => v.is_i64() || v.is_u64(),
                "number" => v.is_number(),
                "boolean" => v.is_boolean(),
                _ => false,
            };
            if !ok {
                return Err(format!("{v} is not of type {t}"));
            }
        }
        for key in schema
            .get("required")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
        {
            let key = key.as_str().unwrap_or_default();
            if v.get(key).is_none() {
                return Err(format!("{key} is required"));
            }
        }
        let props = schema.get("properties").and_then(Value::as_object);
        for (k, val) in v.as_object().into_iter().flatten() {
            match (
                props.and_then(|p| p.get(k)),
                schema.get("additionalProperties"),
            ) {
                (Some(s), _) => conforms(s, val).map_err(|e| format!("{k}: {e}"))?,
                (None, Some(Value::Bool(false))) => return Err(format!("{k} is not declared")),
                (None, Some(extra @ Value::Object(_))) => {
                    conforms(extra, val).map_err(|e| format!("{k}: {e}"))?;
                }
                (None, _) => {}
            }
        }
        if let (Some(items), Some(arr)) = (schema.get("items"), v.as_array()) {
            for (i, x) in arr.iter().enumerate() {
                conforms(items, x).map_err(|e| format!("[{i}]: {e}"))?;
            }
        }
        Ok(())
    }

    fn output_schema(tool: &str) -> Value {
        let tools: Vec<Value> = serde_json::from_str(super::super::TOOLS).unwrap();
        tools.into_iter().find(|t| t["name"] == tool).unwrap()["outputSchema"].clone()
    }

    const SCHEMA_TOOLS: [&str; 5] = [
        "templates_list",
        "metadata_plan",
        "metadata_validate",
        "init",
        "deploy",
    ];

    async fn call(tool: &str, args: Value) -> (bool, Value) {
        let o = Tool::named(tool)
            .unwrap()
            .call(
                Some(raw(&args.to_string())),
                Env::default(),
                false,
                CancellationToken::new(),
            )
            .await;
        let structured = o.structured.unwrap_or_else(|| {
            panic!("{tool} declares an outputSchema and sent no structuredContent")
        });
        (o.is_error, serde_json::from_str(structured.get()).unwrap())
    }

    #[tokio::test]
    async fn results_conform_to_the_declared_output_schema() {
        let dir = crate::testdir::TempDir::new("serve-schema");
        let project = dir.join("proj").display().to_string();
        let missing = dir.join("proj").join("nope").display().to_string();
        for (tool, args) in [
            ("init", json!({"path": project})),
            ("templates_list", json!({})),
            ("metadata_plan", json!({"path": project})),
            ("metadata_validate", json!({"path": project})),
        ] {
            let (is_error, v) = call(tool, args).await;
            assert!(!is_error, "{tool}: {v}");
            conforms(&output_schema(tool), &v).unwrap_or_else(|e| panic!("{tool}: {e}\n{v}"));
        }
        for (tool, args) in [
            ("metadata_plan", json!({"path": missing})),
            ("metadata_validate", json!({"path": missing})),
            (
                "init",
                json!({"path": project, "template": "no-such-template"}),
            ),
            ("deploy", json!({"path": missing})),
            (
                "deploy",
                json!({"path": project, "overwrite": true, "database": "d"}),
            ),
        ] {
            let (is_error, v) = call(tool, args).await;
            assert!(is_error, "{tool}: {v}");
            conforms(&output_schema(tool), &v).unwrap_or_else(|e| panic!("{tool}: {e}\n{v}"));
        }
    }

    #[test]
    fn every_declared_schema_admits_the_error_shape() {
        let minimal = json!({"message": "boom", "exitCode": 1});
        let full = json!({"message": "execution failed", "exitCode": 13, "sqlstate": "22012",
            "detail": "d", "hint": "h", "where": "w", "failedFile": "./migrations/001.sql",
            "script": "deploy.sql", "line": 12, "column": 3, "sourceLine": "SELECT 1/0;",
            "scriptExpanded": true, "notices": ["NOTICE:  applying"], "noticesTruncated": 2});
        for tool in SCHEMA_TOOLS {
            for shape in [&minimal, &full] {
                conforms(&output_schema(tool), shape).unwrap_or_else(|e| panic!("{tool}: {e}"));
            }
        }
    }

    #[test]
    fn the_conformance_check_rejects_mismatches() {
        let schema = output_schema("init");
        let wrong = json!({"created": "yes", "path": "/tmp/x", "template": "basic"});
        assert!(
            conforms(&schema, &wrong)
                .is_err_and(|e| e.contains("created: \"yes\" is not of type boolean"))
        );
        let partial = json!({"path": "/tmp/x"});
        assert!(
            conforms(&schema, &partial).is_err_and(
                |e| e.contains("created is required") && e.contains("message is required")
            )
        );
    }
}
