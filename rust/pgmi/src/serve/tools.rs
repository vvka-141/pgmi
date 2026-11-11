use std::fmt::Write as _;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;

use serde::Serialize;
use serde_json::value::{RawValue, to_raw_value};
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;

use super::wire::{self, Members};
use crate::commands::{init, metadata};
use crate::conn::resolve::Flags;
use crate::deploy::{ConfirmedName, Context, Request, Run};
use crate::env::Env;
use crate::error::Error;
use crate::error::redact;
use crate::output::Reporter;
use crate::templates::Template;

/// The tools a server lists and calls.
pub(crate) trait Toolbox: Send + Sync + 'static {
    type Tool: Send + 'static;

    /// `tools/list`'s array as JSON text, as clients see it.
    fn list(&self) -> &str;

    fn tool(&self, name: &str) -> Option<Self::Tool>;

    /// Runs one call on its own task; a panic becomes the call's answer.
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
        Some(match name {
            "deploy" => Tool::Deploy,
            "init" => Tool::Init,
            "metadata_plan" => Tool::MetadataPlan,
            "metadata_validate" => Tool::MetadataValidate,
            "templates_list" => Tool::TemplatesList,
            "ai_overview" => Tool::AiOverview,
            "ai_contract" => Tool::AiContract,
            _ => return None,
        })
    }

    pub(crate) async fn call(
        self,
        arguments: Option<Box<RawValue>>,
        env: Env,
        verbose: bool,
        token: CancellationToken,
    ) -> Outcome {
        if let Tool::Deploy = self {
            return match deploy_context(arguments.as_deref(), &env, verbose) {
                Ok(ctx) => run_deploy(ctx, token).await,
                Err(e) => Outcome::failed(&e),
            };
        }
        match tokio::task::spawn_blocking(move || self.answer(arguments.as_deref(), &env)).await {
            Ok(o) => o,
            Err(e) if e.is_panic() => std::panic::resume_unwind(e.into_panic()),
            Err(_) => Outcome::failed(&Error::internal("the tool call was aborted")),
        }
    }

    fn answer(self, arguments: Option<&RawValue>, env: &Env) -> Outcome {
        match self {
            Tool::Deploy => Outcome::failed(&Error::internal("deploy runs on the runtime")),
            Tool::Init => match init(arguments) {
                Ok(v) => Outcome::json(&v),
                Err(e) => Outcome::failed(&e),
            },
            Tool::MetadataPlan => {
                match path_argument(arguments, &["path"], "cli.mcpPathArgs")
                    .and_then(|(p, _)| metadata::plan_report(std::path::Path::new(&p), env))
                {
                    Ok(plan) => Outcome::json(&plan),
                    Err(e) => Outcome::failed(&e),
                }
            }
            Tool::MetadataValidate => {
                match path_argument(arguments, &["path"], "cli.mcpPathArgs")
                    .and_then(|(p, _)| metadata::validate_report(std::path::Path::new(&p), env))
                {
                    Ok(metadata::Validated::Report(v)) => Outcome::json(&v),
                    Ok(metadata::Validated::Unreadable(errors)) => {
                        Outcome::failed(&Error::config(errors.join("\n")))
                    }
                    Err(e) => Outcome::failed(&e),
                }
            }
            Tool::TemplatesList => {
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
            Tool::AiOverview => {
                let text = pgmi_shared::file("ai/overview.md")
                    .map(|b| String::from_utf8_lossy(b).into_owned())
                    .unwrap_or_default();
                Outcome {
                    text,
                    structured: None,
                    is_error: false,
                }
            }
            Tool::AiContract => Outcome {
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

    pub(crate) fn panicked() -> Self {
        let (e, stack) = crate::error::panic_error();
        Self::failure(&e.text(), e.exit_code(), Some(&stack))
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

    pub(crate) fn render(&self, stateless: bool) -> String {
        let result = ToolResult {
            content: [Text {
                kind: "text",
                text: self.text.clone(),
            }],
            structured: self.structured.as_deref(),
            is_error: self.is_error,
        };
        if !stateless {
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
    match wire::kind(raw) {
        "null" => Ok(Vec::new()),
        "object" => {
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

fn value_kind(v: &Value) -> &'static str {
    match v {
        Value::Null => "null",
        Value::Bool(_) => "bool",
        Value::Number(_) => "number",
        Value::String(_) => "string",
        Value::Array(_) => "array",
        Value::Object(_) => "object",
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
            value_kind(got),
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
    let mut params = Vec::new();
    match a.get("params") {
        None => {}
        Some(Value::Object(m)) => {
            for (k, v) in m {
                match v {
                    Value::String(s) => params.push(format!("{k}={s}")),
                    Value::Null => {}
                    other => return Err(a.type_error(other, &format!("params.{k}"), "string")),
                }
            }
        }
        Some(v) => return Err(a.type_error(v, "params", "map[string]string")),
    }
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
        stdin: false,
        approver: Box::new(ConfirmedName(confirm)),
    })
}

async fn run_deploy(ctx: Context, token: CancellationToken) -> Outcome {
    let reporter = Reporter::new(ctx.verbose, &ctx.env);
    let mut run = Run::start();
    let result = run
        .deploy(&ctx, &reporter, token, Arc::new(AtomicBool::new(false)))
        .await;
    let secrets = run.secrets();
    let mut record = run.record(&ctx, &result);
    match &result {
        Ok(()) => {
            let (notices, warnings) = run.notice_counts();
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
        Err(e) if !run.reached_deployer() => {
            Outcome::failure(&redact(&e.text(), &secrets), e.exit_code(), None)
        }
        Err(e) => {
            let message = redact(&e.text(), &secrets);
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
        let e = err("{\"path\":\"/\",\"timeout\":\"soon\"}");
        assert!(e.text().starts_with("invalid timeout"), "{}", e.text());
        assert_eq!(e.exit_code(), 10);
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
        assert_eq!(r.params, ["k=v"]);
        assert_eq!(r.params_files, ["a", "b"]);
        assert!(r.overwrite && !r.force && !r.json && r.compat.is_none() && r.cloud.is_empty());
        assert!(!ctx.stdin);
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
        let failed = Outcome::failed(&Error::config("path is required")).render(false);
        assert_eq!(
            failed,
            "{\"content\":[{\"type\":\"text\",\"text\":\"path is required\\nexit code 10\"}],\"structuredContent\":{\"message\":\"path is required\",\"exitCode\":10},\"isError\":true}"
        );
        let stateless = Outcome::failed(&Error::config("x")).render(true);
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
