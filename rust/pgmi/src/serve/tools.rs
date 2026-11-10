use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;

use serde::Serialize;
use serde_json::value::{RawValue, to_raw_value};
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;

use super::wire::{self, Members};
use crate::cli::{Cli, Command, DeployArgs};
use crate::commands::metadata;
use crate::deploy::{Approval, Context, Run};
use crate::env::Env;
use crate::error::Error;
use crate::output::{Reporter, redact};

#[derive(Clone, Copy)]
pub enum Tool {
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
    pub fn named(name: &str) -> Option<Tool> {
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

    pub async fn call(
        self,
        arguments: Option<&RawValue>,
        env: &Env,
        verbose: bool,
        token: CancellationToken,
    ) -> Outcome {
        match self {
            Tool::Deploy => match deploy_context(arguments, env, verbose) {
                Ok(ctx) => run_deploy(ctx, token).await,
                Err(e) => Outcome::failed(&e),
            },
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
                    Ok(Ok(v)) => Outcome::json(&v),
                    Ok(Err(errors)) => Outcome::failed(&Error::config(errors.join("\n"))),
                    Err(e) => Outcome::failed(&e),
                }
            }
            Tool::TemplatesList => {
                let mut list = crate::commands::templates::LIST.to_vec();
                list.sort_by_key(|t| t.0);
                let templates: Vec<Value> = list
                    .iter()
                    .map(|(name, description, best)| {
                        json!({"bestFor": best, "description": description, "name": name})
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
    crate::init::check_empty(target)?;
    crate::init::scaffold(target, template, args.string("name")?.as_deref())?;
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

pub struct Outcome {
    text: String,
    structured: Option<Box<RawValue>>,
    is_error: bool,
}

impl Outcome {
    fn json(v: &impl Serialize) -> Self {
        Outcome {
            text: serde_json::to_string_pretty(v).unwrap_or_default(),
            structured: to_raw_value(v).ok(),
            is_error: false,
        }
    }

    pub fn failed(e: &Error) -> Self {
        Self::failure(&e.text(), e.exit_code(), None)
    }

    pub fn panicked() -> Self {
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

    pub fn render(&self, stateless: bool) -> String {
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
            crate::cli::duration::parse(&t)
                .map_err(|e| Error::config(format!("invalid timeout: time: {e}")))?,
        ),
    };
    let mut args = deploy_args(&path)?;
    args.connection = connection;
    args.database = database;
    args.timeout = timeout;
    args.overwrite = overwrite;
    args.params = params;
    args.params_files = params_files;
    Ok(Context {
        args,
        verbose,
        env: env.clone(),
        dotenv_keys: Vec::new(),
        maintenance_db: maintenance,
        stdin: false,
        approval: Approval::Confirm(confirm),
    })
}

async fn run_deploy(ctx: Context, token: CancellationToken) -> Outcome {
    let reporter = Reporter::new(ctx.verbose, &ctx.env);
    let mut run = Run::start();
    let result = run
        .deploy(&ctx, &reporter, token, Arc::new(AtomicBool::new(false)))
        .await;
    let secrets = run.secrets();
    let record = run.record(&ctx, &result);
    match &result {
        Ok(()) => {
            let (notices, warnings) = run.notice_counts();
            let text = format!(
                "deployed {}: {} files loaded, {} paths ignored, {notices} notices ({warnings} warnings) in {}ms; the full record is in structuredContent",
                run.database().unwrap_or_default(),
                record["filesLoaded"].as_u64().unwrap_or(0),
                record["pathsIgnored"].as_u64().unwrap_or(0),
                record["durationMs"].as_u64().unwrap_or(0),
            );
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
            if let Some(location) = run.location(e) {
                text.push_str(&format!("\nLOCATION: {location}"));
            }
            text.push_str(&format!("\nexit code {}", e.exit_code()));
            if let Some(phase) = record["phase"].as_str() {
                text.push_str(&format!(", phase {phase}"));
            }
            let mut structured = record;
            if let Value::Object(m) = &mut structured {
                m.insert("message".into(), json!(message));
            }
            Outcome {
                text,
                structured: to_raw_value(&structured).ok(),
                is_error: true,
            }
        }
    }
}

fn deploy_args(path: &std::path::Path) -> Result<DeployArgs, Error> {
    use clap::Parser;
    let parsed = Cli::try_parse_from([
        std::ffi::OsStr::new("pgmi"),
        std::ffi::OsStr::new("deploy"),
        std::ffi::OsStr::new("--"),
        path.as_os_str(),
    ])
    .map_err(|e| Error::internal(format!("map deploy arguments: {e}")))?;
    match parsed.command {
        Some(Command::Deploy(a)) => Ok(*a),
        _ => Err(Error::internal("map deploy arguments")),
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
        assert_eq!(ctx.args.database.as_deref(), Some("-d"));
        assert_eq!(ctx.args.timeout, Some(std::time::Duration::from_secs(2)));
        assert_eq!(ctx.args.params, ["k=v"]);
        assert_eq!(ctx.args.params_files, ["a", "b"]);
        assert!(ctx.args.overwrite && !ctx.stdin);
        assert!(matches!(ctx.approval, Approval::Confirm(Some(ref d)) if d == "-d"));
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
        let mut record = Run::start().record(&ctx, &Ok(()));
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
}
