pub(crate) mod commands;
mod help_text;
pub(crate) mod usage;

use std::path::PathBuf;
use std::time::Duration;

use clap::{ArgAction, Args, Parser, Subcommand};

use crate::config::params::Param;
use crate::conn::cloud::{self, Provider};
use crate::conn::resolve::{Flags, SslMode};
use crate::error::Secret;
pub(crate) use commands::*;

#[derive(Debug, Parser)]
#[command(
    name = "pgmi",
    about = "PostgreSQL-native deployment driver",
    long_about = help_text::ROOT,
    disable_help_flag = true,
    disable_version_flag = true,
    disable_help_subcommand = true,
    subcommand_help_heading = "Available Commands"
)]
pub(crate) struct Cli {
    #[arg(short = 'v', long, global = true, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Enable verbose output for all commands")]
    pub(crate) verbose: bool,
    #[arg(long, global = true, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Show help for this command")]
    pub(crate) help: bool,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub(crate) version: bool,
    #[command(subcommand)]
    pub(crate) command: Option<Command>,
}

impl Cli {
    /// The command line as Go reads it: `-h` is help on every command except
    /// `deploy`, where it is `--host`; a repeated flag takes its last value;
    /// a flag that takes a value takes the next argument, dash or not.
    pub(crate) fn command_line() -> clap::Command {
        fn go_flags(c: clap::Command) -> clap::Command {
            let c = if c.get_name() == "deploy" {
                c
            } else {
                c.arg(
                    clap::Arg::new("short_help")
                        .short('h')
                        .action(ArgAction::HelpLong)
                        .hide(true),
                )
            };
            c.args_override_self(true)
                .mut_args(|a| {
                    if !a.is_positional()
                        && a.get_action().takes_values()
                        && !a.is_require_equals_set()
                    {
                        a.allow_hyphen_values(true)
                    } else {
                        a
                    }
                })
                .mut_subcommands(go_flags)
        }
        go_flags(<Self as clap::CommandFactory>::command())
    }

    /// Parses `args` as Go does: a flag value error refuses before `--help`,
    /// and `--help` wins over a missing argument or a conflict.
    pub(crate) fn parse_args(args: &[std::ffi::OsString]) -> Result<Parsed, clap::Error> {
        let mut cmd = Self::command_line();
        let matches = match cmd.try_get_matches_from_mut(args) {
            Ok(m) => m,
            Err(e) if checked_after_values(e.kind()) => {
                fn lenient(c: clap::Command) -> clap::Command {
                    c.ignore_errors(true).mut_subcommands(lenient)
                }
                let help = lenient(cmd.clone())
                    .try_get_matches_from(args)
                    .ok()
                    .and_then(|m| asked_help(&cmd, &m));
                return help.map(Parsed::Help).ok_or(e);
            }
            Err(e) => return Err(e),
        };
        if let Some(text) = asked_help(&cmd, &matches) {
            return Ok(Parsed::Help(text));
        }
        <Self as clap::FromArgMatches>::from_arg_matches(&matches).map(Parsed::Run)
    }
}

#[derive(Debug)]
pub(crate) enum Parsed {
    Run(Cli),
    Help(String),
}

fn checked_after_values(kind: clap::error::ErrorKind) -> bool {
    use clap::error::ErrorKind as K;
    matches!(
        kind,
        K::MissingRequiredArgument
            | K::MissingSubcommand
            | K::ArgumentConflict
            | K::DisplayHelpOnMissingArgumentOrSubcommand
    )
}

fn asked_help(cmd: &clap::Command, m: &clap::ArgMatches) -> Option<String> {
    match m.subcommand() {
        Some((name, sub)) => asked_help(cmd.find_subcommand(name)?, sub),
        None => m
            .try_get_one::<bool>("help")
            .ok()
            .flatten()
            .copied()
            .unwrap_or(false)
            .then(|| cmd.clone().render_long_help().to_string()),
    }
}

#[derive(Debug, Subcommand)]
pub(crate) enum Command {
    #[command(about = "Run deploy.sql against a target database", long_about = help_text::DEPLOY)]
    Deploy(Box<DeployArgs>),
    #[command(about = "Scaffold a new pgmi project", long_about = help_text::INIT)]
    Init(InitArgs),
    #[command(about = "Print version, commit, build date", long_about = help_text::VERSION)]
    Version,
    #[command(about = "Help about any command", long_about = help_text::HELP)]
    Help { command: Vec<String> },
    #[command(about = "Show project structure summary (no database required)", long_about = help_text::INFO)]
    Info(InfoArgs),
    #[command(about = "Inspect and scaffold <pgmi-meta> blocks (no DB connection)", long_about = help_text::METADATA)]
    Metadata(MetadataArgs),
    #[command(about = "List or describe project templates", long_about = help_text::TEMPLATES)]
    Templates(TemplatesArgs),
    #[command(about = "Print pgmi documentation for AI assistants (llms.txt-style)", long_about = help_text::AI)]
    Ai(AiArgs),
    #[command(about = "Run pgmi as an MCP (Model Context Protocol) server over stdio", long_about = help_text::SERVE)]
    Serve,
    #[command(about = "Run the connection wizard and save to pgmi.yaml", long_about = help_text::CONFIG)]
    Config(ConfigArgs),
    #[command(about = "Print a shell completion script to stdout", long_about = help_text::COMPLETION)]
    Completion(CompletionArgs),
}

const CONNECTION_HELP: &str =
    "PostgreSQL connection string: URI, libpq keyword/value (host=... dbname=...), or ADO.NET.
Its database is the maintenance database for CREATE DATABASE, and also the
deploy target unless -d says otherwise. It outranks pgmi.yaml's
connection.database, so a string ending in /postgres deploys into postgres.
Fields it omits come from $PGHOST, $PGPORT, $PGDATABASE, $PGUSER, $PGAPPNAME
and $PGCONNECT_TIMEOUT, as in libpq, then from the defaults (localhost:5432/postgres).
service= and $PGSERVICE are refused: pgmi reads no service files.
Mutually exclusive with granular flags (--host, --port, --username).
Alternative: Use PGMI_CONNECTION_STRING or DATABASE_URL environment variable.
Example: postgresql://user:pass@localhost:5432/postgres";

const DATABASE_HELP: &str =
    "Target database name (optional if specified in connection string, or $PGDATABASE)
Examples:
  -d myapp                          # Deploy to 'myapp' database
  --connection postgresql://user@host/myapp  # Database from connection string
  --connection postgresql://user@host/postgres -d myapp  # Override";

#[derive(Debug, Args)]
pub(crate) struct InitArgs {
    pub(crate) path: Option<PathBuf>,
    #[arg(short = 't', long, help = "Template to use (minimal, basic, advanced)")]
    pub(crate) template: Option<String>,
}

#[derive(Debug, Args)]
pub(crate) struct DeployArgs {
    pub(crate) project: PathBuf,
    #[arg(long, help = CONNECTION_HELP)]
    pub(crate) connection: Option<Secret>,
    #[arg(
        short = 'h',
        long,
        help = "PostgreSQL server host\nPrecedence: --host > $PGHOST > pgmi.yaml > localhost"
    )]
    pub(crate) host: Option<String>,
    #[arg(
        short = 'p',
        long,
        help = "PostgreSQL server port\nPrecedence: --port > $PGPORT > pgmi.yaml > 5432"
    )]
    #[arg(value_parser = go_int)]
    pub(crate) port: Option<i64>,
    #[arg(
        short = 'U',
        long,
        help = "PostgreSQL user (default: $PGUSER or current OS user)"
    )]
    pub(crate) username: Option<String>,
    #[arg(short = 'd', long, help = DATABASE_HELP)]
    pub(crate) database: Option<String>,
    #[arg(
        long,
        help = "SSL mode: disable|allow|prefer|require|verify-ca|verify-full\n(default: prefer, or $PGSSLMODE)"
    )]
    pub(crate) sslmode: Option<String>,
    #[arg(
        long,
        help = "Path to client SSL certificate file\nPrecedence: --sslcert > $PGSSLCERT > pgmi.yaml"
    )]
    pub(crate) sslcert: Option<String>,
    #[arg(
        long,
        help = "Path to client SSL private key file\nPrecedence: --sslkey > $PGSSLKEY > pgmi.yaml"
    )]
    pub(crate) sslkey: Option<String>,
    #[arg(
        long,
        help = "Path to root CA certificate for server verification\nPrecedence: --sslrootcert > $PGSSLROOTCERT > pgmi.yaml"
    )]
    pub(crate) sslrootcert: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Drop and recreate the database\nRequires interactive confirmation unless --force is used\nWithout a terminal and without --force, pgmi refuses (exit 12) rather than prompting")]
    pub(crate) overwrite: bool,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Never block on an interactive prompt (for CI/CD; no-op when nothing would prompt)\nWith --overwrite: replaces the approval prompt with a 5-second countdown (Ctrl-C aborts)\nOnly affects confirmation dialogs, not deployment behavior")]
    pub(crate) force: bool,
    #[arg(long, value_parser = timeout_flag, help = "Catastrophic failure protection timeout (default 3m)\nPrevents indefinite hangs from network issues or deadlocks\nUse 0 to disable the limit\nFor query-level timeouts, use SET statement_timeout in SQL\nExamples: 30s, 5m, 1h30m, 0 (default 3m0s)")]
    pub(crate) timeout: Option<Duration>,
    #[arg(
        long,
        help = "Compatibility level (default: latest)\nPin to a specific pgmi session interface version"
    )]
    pub(crate) compat: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Emit structured JSON to stdout after deployment")]
    pub(crate) json: bool,
    #[arg(
        long = "param",
        help = "Parameters as key=value pairs (can be specified multiple times)\nAvailable as session variables: current_setting('pgmi.key') during deployment\nExample: --param env=prod --param region=us-west"
    )]
    pub(crate) params: Vec<std::ffi::OsString>,
    #[arg(
        long = "params-file",
        help = "Load parameters from key=value files, e.g. prod.params (can be specified multiple times)\nUse - to read stdin (once). Later files override earlier ones, CLI --param overrides all"
    )]
    pub(crate) params_files: Vec<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Enable Azure Entra ID authentication\nUses DefaultAzureCredential chain (Managed Identity, Azure CLI, etc.)")]
    pub(crate) azure: bool,
    #[arg(
        long,
        help = "Azure AD tenant/directory ID (overrides $AZURE_TENANT_ID)"
    )]
    pub(crate) azure_tenant_id: Option<String>,
    #[arg(
        long,
        help = "Azure AD application/client ID (overrides $AZURE_CLIENT_ID)"
    )]
    pub(crate) azure_client_id: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Enable AWS IAM database authentication\nUses default AWS credential chain (env vars, config file, IAM role, etc.)")]
    pub(crate) aws: bool,
    #[arg(long, help = "AWS region for RDS endpoint (overrides $AWS_REGION)")]
    pub(crate) aws_region: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Enable Google Cloud SQL IAM database authentication\nUses Application Default Credentials (gcloud auth, service account, etc.)")]
    pub(crate) google: bool,
    #[arg(
        long,
        help = "Cloud SQL instance connection name (format: project:region:instance)\nRequired when --google is specified"
    )]
    pub(crate) google_instance: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Sign in to Databricks Lakebase: a database credential from the workspace in DATABRICKS_HOST or ~/.databrickscfg, refreshed before it expires")]
    pub(crate) databricks: bool,
    #[arg(
        long,
        requires = "databricks",
        value_name = "NAME",
        help = "Lakebase endpoint, projects/<project>/branches/<branch>/endpoints/<endpoint> (default $PGMI_DATABRICKS_ENDPOINT)"
    )]
    pub(crate) databricks_endpoint: Option<String>,
}

impl From<DeployArgs> for crate::deploy::Request {
    fn from(a: DeployArgs) -> Self {
        let cloud = [
            (a.azure, Provider::Azure),
            (a.aws, Provider::Aws),
            (a.google, Provider::Google),
            (a.databricks, Provider::Databricks),
        ]
        .into_iter()
        .filter_map(|(asked, p)| asked.then_some(p))
        .collect();
        crate::deploy::Request {
            project: a.project,
            connection: Flags {
                connection: a.connection,
                host: given(a.host),
                port: a.port.filter(|p| *p != 0),
                username: given(a.username),
                database: given(a.database),
                sslmode: a.sslmode.as_deref().and_then(|m| m.parse().ok()),
                invalid_sslmode: a.sslmode.filter(|m| m.parse::<SslMode>().is_err()),
                sslcert: given(a.sslcert),
                sslkey: given(a.sslkey),
                sslrootcert: given(a.sslrootcert),
            },
            maintenance_db: None,
            cloud,
            cloud_request: cloud::Options {
                tenant_id: a.azure_tenant_id,
                client_id: a.azure_client_id,
                aws_region: a.aws_region,
                google_instance: a.google_instance,
                databricks_endpoint: a.databricks_endpoint,
            },
            params: a.params.into_iter().map(param_arg).collect(),
            params_files: a.params_files,
            timeout: a.timeout,
            overwrite: a.overwrite,
            force: a.force,
            compat: a.compat,
            json: a.json,
        }
    }
}

fn given(flag: Option<String>) -> Option<String> {
    flag.filter(|v| !v.is_empty())
}

pub(crate) fn go_bool(s: &str) -> Result<bool, String> {
    match s {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
        _ => Err("must be true or false".into()),
    }
}

fn timeout_flag(s: &str) -> Result<std::time::Duration, String> {
    crate::duration::parse(s).map_err(|e| {
        if e.ends_with("must not be negative") {
            e
        } else {
            r#"must be a duration like "30s" or "5m""#.into()
        }
    })
}

/// An integer as Go's `strconv.ParseInt(s, 0, 64)` reads it: a sign, then
/// decimal, `0x`, `0o`, `0b` or a leading `0` for octal, `_` between digits.
fn go_int(s: &str) -> Result<i64, String> {
    let err = || String::from("must be an integer");
    let (negative, body) = match s.strip_prefix('-') {
        Some(rest) => (true, rest),
        None => (false, s.strip_prefix('+').unwrap_or(s)),
    };
    let prefix = body.get(..2).map(str::to_ascii_lowercase);
    let (radix, digits, prefixed) = match prefix.as_deref() {
        Some("0x") => (16, &body[2..], true),
        Some("0o") => (8, &body[2..], true),
        Some("0b") => (2, &body[2..], true),
        _ if body.len() > 1 && body.starts_with('0') => (8, &body[1..], true),
        _ => (10, body, false),
    };
    let separated =
        !digits.contains("__") && !digits.ends_with('_') && (prefixed || !digits.starts_with('_'));
    let plain: String = digits.chars().filter(|c| *c != '_').collect();
    if !separated || plain.is_empty() || !plain.chars().all(|c| c.is_ascii_alphanumeric()) {
        return Err(err());
    }
    let magnitude = i128::from(u64::from_str_radix(&plain, radix).map_err(|_| err())?);
    i64::try_from(if negative { -magnitude } else { magnitude }).map_err(|_| err())
}

fn param_arg(arg: std::ffi::OsString) -> Param {
    match arg.into_string() {
        Ok(text) => Param::Flag(text),
        Err(raw) => {
            let lossy = raw.to_string_lossy();
            Param::NotUtf8(
                lossy
                    .split_once('=')
                    .map_or(&*lossy, |(k, _)| k)
                    .to_string(),
            )
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use clap::CommandFactory;

    #[test]
    fn definition_is_valid() {
        Cli::command().debug_assert();
        Cli::command_line().debug_assert();
    }

    #[test]
    fn deploy_has_no_orchestration_flags() {
        let cli = Cli::command();
        let deploy = cli.find_subcommand("deploy").unwrap();
        let flags: Vec<&str> = deploy
            .get_arguments()
            .filter_map(|a| a.get_long())
            .collect();
        assert!(flags.contains(&"overwrite"), "{flags:?}");
        for flag in flags {
            assert!(
                !["dry-run", "quiet", "rollback", "test-only"].contains(&flag),
                "--{flag} controls deployment behaviour; that belongs in deploy.sql"
            );
            assert!(
                !["skip-", "only-", "no-"]
                    .iter()
                    .any(|p| flag.starts_with(p)),
                "--{flag} filters execution; that belongs in a deploy.sql WHERE clause"
            );
        }
    }

    #[test]
    fn deploy_flags() {
        let cli = Cli::try_parse_from([
            "pgmi",
            "deploy",
            ".",
            "-h",
            "db",
            "--force=false",
            "--json",
            "--timeout",
            "1h30m",
            "--param",
            "a=b",
        ])
        .unwrap();
        let Some(Command::Deploy(d)) = cli.command else {
            panic!()
        };
        assert_eq!(d.host.as_deref(), Some("db"));
        assert!(!d.force && d.json);
        assert_eq!(d.timeout, Some(Duration::from_mins(90)));
        let e = Cli::try_parse_from(["pgmi", "deploy", ".", "--timeout", "banana"]).unwrap_err();
        assert_eq!(e.kind(), clap::error::ErrorKind::ValueValidation);
        assert!(
            e.to_string()
                .contains(r#"must be a duration like "30s" or "5m""#),
            "{e}"
        );
    }

    #[test]
    fn deploy_args_never_format_the_connection_string() {
        let cli = Cli::try_parse_from([
            "pgmi",
            "deploy",
            ".",
            "--connection",
            "postgresql://deployer:pw-must-not-appear@db.internal/app",
        ])
        .unwrap();
        let Some(Command::Deploy(d)) = cli.command else {
            panic!()
        };
        for text in [format!("{d:?}"), format!("{d:#?}")] {
            assert!(!text.contains("pw-must-not-appear"), "{text}");
            assert!(text.contains("[redacted]"), "{text}");
        }
        assert_eq!(
            d.connection.as_ref().map(Secret::expose),
            Some("postgresql://deployer:pw-must-not-appear@db.internal/app")
        );
    }

    fn request(args: &[&str]) -> crate::deploy::Request {
        let cli = Cli::try_parse_from(["pgmi", "deploy"].iter().chain(args)).unwrap();
        let Some(Command::Deploy(d)) = cli.command else {
            panic!()
        };
        (*d).into()
    }

    #[test]
    fn deploy_flags_become_a_request() {
        let r = request(&[
            "proj",
            "--connection",
            "postgres://x",
            "-h",
            "h",
            "-p",
            "6000",
            "-U",
            "u",
            "-d",
            "db",
            "--sslmode",
            "require",
            "--sslcert",
            "c",
            "--sslkey",
            "k",
            "--sslrootcert",
            "r",
            "--overwrite",
            "--force",
            "--compat",
            "1",
            "--json",
            "--param",
            "a=b",
            "--params-file",
            "f",
            "--aws",
            "--aws-region",
            "eu",
            "--azure-tenant-id",
            "t",
            "--azure-client-id",
            "c",
            "--google-instance",
            "p:r:i",
        ]);
        assert_eq!(r.project, std::path::Path::new("proj"));
        let c = &r.connection;
        assert_eq!(
            c.connection.as_ref().map(Secret::expose),
            Some("postgres://x")
        );
        assert_eq!(
            [
                &c.host,
                &c.username,
                &c.database,
                &c.sslcert,
                &c.sslkey,
                &c.sslrootcert
            ]
            .map(|v| v.as_deref()),
            [
                Some("h"),
                Some("u"),
                Some("db"),
                Some("c"),
                Some("k"),
                Some("r")
            ]
        );
        assert_eq!(c.port, Some(6000));
        assert_eq!(c.sslmode, Some(SslMode::Require));
        assert!(r.overwrite && r.force && r.json);
        assert_eq!(r.compat.as_deref(), Some("1"));
        assert_eq!(r.params, [Param::Flag("a=b".into())]);
        assert_eq!(r.params_files, ["f"]);
        assert_eq!(r.cloud, [Provider::Aws]);
        assert_eq!(r.cloud_request.aws_region.as_deref(), Some("eu"));
        assert_eq!(r.cloud_request.tenant_id.as_deref(), Some("t"));
        assert_eq!(r.cloud_request.client_id.as_deref(), Some("c"));
        assert_eq!(r.cloud_request.google_instance.as_deref(), Some("p:r:i"));
        assert!(r.maintenance_db.is_none() && r.timeout.is_none());
    }

    #[test]
    fn every_cloud_flag_is_kept_so_select_can_refuse_two() {
        let r = request(&[".", "--azure", "--aws", "--google", "--databricks"]);
        assert_eq!(
            r.cloud,
            [
                Provider::Azure,
                Provider::Aws,
                Provider::Google,
                Provider::Databricks
            ]
        );
        assert!(request(&["."]).cloud.is_empty());
    }

    #[test]
    fn the_databricks_endpoint_needs_databricks() {
        let r = request(&[
            ".",
            "--databricks",
            "--databricks-endpoint",
            "projects/p/branches/b/endpoints/e",
        ]);
        assert_eq!(r.cloud, [Provider::Databricks]);
        assert_eq!(
            r.cloud_request.databricks_endpoint.as_deref(),
            Some("projects/p/branches/b/endpoints/e")
        );
        let e =
            Cli::try_parse_from(["pgmi", "deploy", ".", "--databricks-endpoint", "x"]).unwrap_err();
        assert_eq!(e.kind(), clap::error::ErrorKind::MissingRequiredArgument);
    }

    fn parse(args: &[&str]) -> Result<Parsed, clap::Error> {
        let args: Vec<std::ffi::OsString> = ["pgmi"].iter().chain(args).map(Into::into).collect();
        Cli::parse_args(&args)
    }

    fn run(args: &[&str]) -> Cli {
        match parse(args) {
            Ok(Parsed::Run(cli)) => cli,
            other => panic!("{args:?}: {other:?}"),
        }
    }

    fn deploy(args: &[&str]) -> DeployArgs {
        match run(&[&["deploy"], args].concat()).command {
            Some(Command::Deploy(d)) => *d,
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn a_repeated_flag_takes_its_last_value_and_a_list_flag_keeps_every_value() {
        let d = deploy(&[
            ".",
            "-d",
            "a",
            "-d",
            "b",
            "--timeout",
            "1s",
            "--timeout",
            "2s",
            "--json",
            "--json=false",
            "--force=false",
            "--force",
            "-p",
            "1",
            "--port",
            "2",
            "--param",
            "a=1",
            "--param",
            "b=2",
        ]);
        assert_eq!(d.database.as_deref(), Some("b"));
        assert_eq!(d.timeout, Some(Duration::from_secs(2)));
        assert!(!d.json && d.force);
        assert_eq!(d.port, Some(2));
        assert_eq!(d.params, ["a=1", "b=2"]);
        let i = run(&["init", "-t", "basic", "-t", "minimal"]);
        let Some(Command::Init(i)) = i.command else {
            panic!()
        };
        assert_eq!(i.template.as_deref(), Some("minimal"));
    }

    #[test]
    fn an_empty_text_flag_is_unset_as_in_go() {
        let request = crate::deploy::Request::from(deploy(&[
            ".",
            "-h",
            "",
            "-U",
            "",
            "-d",
            "",
            "--sslcert",
            "",
            "--sslkey",
            "",
            "--sslrootcert",
            "",
        ]));
        let c = request.connection;
        assert_eq!(
            (c.host, c.username, c.database),
            (None, None, None),
            "empty -h, -U and -d"
        );
        assert_eq!((c.sslcert, c.sslkey, c.sslrootcert), (None, None, None));
    }

    #[test]
    fn init_help_describes_the_template_flag() {
        let Ok(Parsed::Help(text)) = parse(&["init", "--help"]) else {
            panic!()
        };
        assert!(
            text.contains("Template to use (minimal, basic, advanced)"),
            "{text}"
        );
    }

    #[test]
    fn every_boolean_takes_go_forms() {
        for (args, verbose) in [
            (&["--verbose=false", "version"][..], false),
            (&["-v=false", "version"], false),
            (&["-v", "-v", "version"], true),
            (&["-vv", "version"], true),
            (&["version", "-v=true"], true),
            (&["--verbose=1", "serve"], true),
            (&["serve", "--verbose=false"], false),
        ] {
            assert_eq!(run(args).verbose, verbose, "{args:?}");
        }
        assert!(!run(&["--version=false"]).version);
        assert!(run(&["--version", "--version"]).version);
        for args in [&["-v=x", "version"][..], &["info", "--json=yes"]] {
            assert!(parse(args).is_err(), "{args:?}");
        }
    }

    #[test]
    fn help_is_a_boolean_that_wins_over_a_missing_argument_but_not_over_a_bad_value() {
        assert!(matches!(
            run(&["--help=false", "version"]).command,
            Some(Command::Version)
        ));
        let Ok(Parsed::Help(text)) = parse(&["--help=true", "version"]) else {
            panic!()
        };
        assert!(text.contains("pgmi version"), "{text}");
        let Ok(Parsed::Help(text)) = parse(&["deploy", "--help"]) else {
            panic!()
        };
        assert!(text.contains("pgmi deploy"), "{text}");
        let e = parse(&["deploy", "--timeout", "banana", "--help"]).unwrap_err();
        assert_eq!(e.kind(), clap::error::ErrorKind::ValueValidation);
    }

    #[test]
    fn every_command_prints_its_long_description_for_help_and_h() {
        for (line, text) in [
            (&["--help"][..], "PostgreSQL-native deployment driver."),
            (&["-h"], "You write the deployment in SQL"),
            (
                &["metadata", "plan", "--help"],
                "every loaded non-test file once per sort key",
            ),
            (&["metadata", "plan", "-h"], "Use this to verify ordering"),
            (
                &["templates", "describe", "--help"],
                "Examples:\n  pgmi templates describe basic",
            ),
            (&["info", "-h"], "Shows file counts by directory"),
            (&["serve", "--help"], "MCP-capable assistants"),
        ] {
            let help = match parse(line) {
                Ok(Parsed::Help(help)) => help,
                Err(e) if e.kind() == clap::error::ErrorKind::DisplayHelp => e.render().to_string(),
                other => panic!("{line:?}: {other:?}"),
            };
            assert!(help.contains(text), "{line:?}: {help}");
        }
    }

    #[test]
    fn a_value_flag_takes_the_next_argument_even_with_a_dash() {
        let d = deploy(&[".", "-d", "-x", "-p", "-1", "--param", "--json"]);
        assert_eq!(d.database.as_deref(), Some("-x"));
        assert_eq!(d.port, Some(-1));
        assert_eq!(d.params, ["--json"]);
        assert!(!d.json);
        let e = parse(&["deploy", ".", "--timeout", "-1s"]).unwrap_err();
        assert!(e.to_string().contains("must not be negative"), "{e}");
    }

    #[test]
    fn the_port_is_an_integer_as_go_reads_one() {
        for (text, n) in [
            ("5432", 5432),
            ("+5", 5),
            ("-1", -1),
            ("0", 0),
            ("010", 8),
            ("0x10", 16),
            ("0X1f", 31),
            ("0o17", 15),
            ("0b101", 5),
            ("1_0", 10),
            ("0_7", 7),
            ("0x_1", 1),
            ("70000", 70000),
            ("-9223372036854775808", i64::MIN),
        ] {
            assert_eq!(go_int(text), Ok(n), "{text}");
        }
        for text in [
            "",
            "abc",
            " 1",
            "1 ",
            "1,2",
            "_1",
            "1_",
            "1__0",
            "08",
            "0x",
            "+-1",
            "--1",
            "1e3",
            "9223372036854775808",
            "99999999999999999999",
        ] {
            assert!(go_int(text).is_err(), "{text:?}");
        }
        assert_eq!(request(&[".", "-p", "0"]).connection.port, None);
        assert_eq!(request(&[".", "-p", "0x10"]).connection.port, Some(16));
        let e = parse(&["deploy", ".", "-p", "1,2"]).unwrap_err();
        assert_eq!(e.kind(), clap::error::ErrorKind::ValueValidation);
    }

    proptest::proptest! {
        #[test]
        fn every_integer_reads_back_in_each_base(n in proptest::num::i64::ANY) {
            let (sign, m) = (if n < 0 { "-" } else { "" }, n.unsigned_abs());
            for text in [
                n.to_string(),
                format!("{sign}0x{m:x}"),
                format!("{sign}0o{m:o}"),
                format!("{sign}0b{m:b}"),
                format!("{sign}0{m:o}"),
            ] {
                proptest::prop_assert_eq!(go_int(&text), Ok(n), "{}", text);
            }
        }

        #[test]
        fn any_text_is_an_integer_or_a_refusal(text in "\\PC{0,12}") {
            let _ = go_int(&text);
        }
    }
}
