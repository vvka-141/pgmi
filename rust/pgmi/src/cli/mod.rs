pub(crate) mod commands;
pub(crate) mod usage;

use std::fmt::Write as _;
use std::path::PathBuf;
use std::time::Duration;

use clap::{ArgAction, Args, Parser, Subcommand};

use crate::conn::cloud::{self, Provider};
use crate::conn::resolve::{Flags, SslMode};
use crate::error::{Kind, Secret};
pub(crate) use commands::*;

#[derive(Debug, Parser)]
#[command(
    name = "pgmi",
    about = "PostgreSQL-native execution fabric",
    disable_help_flag = true,
    disable_version_flag = true,
    disable_help_subcommand = true,
    subcommand_help_heading = "Available Commands"
)]
pub(crate) struct Cli {
    #[arg(short = 'v', long, global = true, action = ArgAction::SetTrue, help = "Enable verbose output for all commands")]
    pub(crate) verbose: bool,
    #[arg(long, global = true, action = ArgAction::Help, help = "Show help for this command")]
    pub(crate) help: Option<bool>,
    #[arg(long, action = ArgAction::SetTrue)]
    pub(crate) version: bool,
    #[command(subcommand)]
    pub(crate) command: Option<Command>,
}

impl Cli {
    /// The command line as Go reads it: `-h` is help on every command except
    /// `deploy`, where it is `--host`.
    pub(crate) fn command_line() -> clap::Command {
        fn short_help(c: clap::Command) -> clap::Command {
            let c = if c.get_name() == "deploy" {
                c
            } else {
                c.arg(
                    clap::Arg::new("short_help")
                        .short('h')
                        .action(ArgAction::Help)
                        .hide(true),
                )
            };
            c.mut_subcommands(short_help)
        }
        short_help(<Self as clap::CommandFactory>::command())
    }

    pub(crate) fn parse_args(args: &[std::ffi::OsString]) -> Result<Self, clap::Error> {
        let matches = Self::command_line().try_get_matches_from(args)?;
        <Self as clap::FromArgMatches>::from_arg_matches(&matches)
    }
}

#[derive(Debug, Subcommand)]
pub(crate) enum Command {
    #[command(about = "Deploy a project: load it into a session and run its deploy.sql", long_about = DEPLOY_HELP, after_help = exit_codes_help())]
    Deploy(Box<DeployArgs>),
    #[command(about = "Write a project from a template")]
    Init(InitArgs),
    #[command(about = "Print the version")]
    Version,
    #[command(about = "Show help for a command")]
    Help { command: Vec<String> },
    #[command(about = "Show project structure summary (no database required)")]
    Info(InfoArgs),
    #[command(about = "Inspect and scaffold <pgmi-meta> blocks (no DB connection)")]
    Metadata(MetadataArgs),
    #[command(about = "List or describe project templates")]
    Templates(TemplatesArgs),
    #[command(about = "Print pgmi documentation for AI assistants (llms.txt-style)")]
    Ai(AiArgs),
    #[command(about = "Expose pgmi's commands as MCP tools over stdio", long_about = SERVE_HELP)]
    Serve,
    #[command(about = "Run the connection wizard and save to pgmi.yaml", long_about = CONFIG_HELP)]
    Config(ConfigArgs),
    #[command(about = "Print a shell completion script to stdout")]
    Completion(CompletionArgs),
}

const DEPLOY_HELP: &str = "Run deploy.sql against a target database.

pgmi connects, loads project files into pg_temp tables, loads CLI parameters,
then executes deploy.sql. Transactions, ordering, and idempotency are decided
by deploy.sql, not by pgmi.

  pgmi deploy . -d mydb
  pgmi deploy . -d mydb --overwrite --force
  pgmi deploy . -d mydb --params-file prod.params
  pgmi deploy . -d mydb --param env=prod --param version=1.2.3
  ./fetch-secrets | pgmi deploy . -d mydb --params-file -

Password is never read from a flag. Use $PGPASSWORD, .pgpass, or a connection
string. Cloud auth: --azure, --aws, --google, --databricks (no password needed).

A project .env is loaded into the environment first (variables already set
win); pgmi serve does not read it.

Parameter precedence: --param > --params-file (later wins) > PGMI_PARAM_*
environment variables > pgmi.yaml. PGMI_PARAM_DB_PASSWORD=x becomes
current_setting('pgmi.db_password'); other environment variables are not read.
Secrets go in the environment or a params file, never in --param.

A --timeout that runs out while connecting is 11: the server was not reached.";

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

const CONFIG_HELP: &str = "Launch the connection wizard and write the result to pgmi.yaml.

  pgmi config              In the current directory
  pgmi config ./project    In ./project

The wizard handles local, Azure Entra ID, AWS IAM, and Google Cloud SQL
auth. Requires an interactive terminal — for CI, write pgmi.yaml by hand.";

const SERVE_HELP: &str = "Expose pgmi's commands as MCP tools over stdio (JSON-RPC 2.0).

Tools: deploy, init, metadata_plan, metadata_validate, templates_list,
ai_overview, ai_contract. deploy resolves its connection, database, timeout and
parameters like pgmi deploy: tool arguments, then the server's environment, then
the project's pgmi.yaml. The project .env is not read.

The server reads JSON-RPC from stdin and writes responses to stdout; all
diagnostics go to stderr. It exits cleanly on EOF or SIGINT.

A failing tool answers with isError and the session continues. A malformed
message gets a -32700 parse error with a null id and the session continues.
Tool calls run concurrently: ping is answered during a deploy, and
notifications/cancelled stops it. A deploy to a database another deploy
holds exits 15.

Add it to Claude Code with:

  claude mcp add pgmi -- pgmi serve";

#[derive(Debug, Args)]
pub(crate) struct InitArgs {
    pub(crate) path: Option<PathBuf>,
    #[arg(short = 't', long, value_parser = crate::templates::Template::ALL.map(|t| t.name()))]
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
    pub(crate) port: Option<String>,
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
        value_enum,
        hide_possible_values = true,
        help = "SSL mode: disable|allow|prefer|require|verify-ca|verify-full\n(default: prefer, or $PGSSLMODE)"
    )]
    pub(crate) sslmode: Option<SslMode>,
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
    #[arg(long, value_parser = crate::duration::parse, help = "Catastrophic failure protection timeout (default 3m)\nPrevents indefinite hangs from network issues or deadlocks\nUse 0 to disable the limit\nFor query-level timeouts, use SET statement_timeout in SQL\nExamples: 30s, 5m, 1h30m, 0 (default 3m0s)")]
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
    pub(crate) params: Vec<String>,
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
                host: a.host,
                port: a.port,
                username: a.username,
                database: a.database,
                sslmode: a.sslmode,
                sslcert: a.sslcert,
                sslkey: a.sslkey,
                sslrootcert: a.sslrootcert,
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
            params: a.params,
            params_files: a.params_files,
            timeout: a.timeout,
            overwrite: a.overwrite,
            force: a.force,
            compat: a.compat,
            json: a.json,
        }
    }
}

impl clap::ValueEnum for SslMode {
    fn value_variants<'a>() -> &'a [Self] {
        SslMode::ALL
    }

    fn to_possible_value(&self) -> Option<clap::builder::PossibleValue> {
        Some(clap::builder::PossibleValue::new(self.as_str()))
    }
}

pub(crate) fn go_bool(s: &str) -> Result<bool, String> {
    match s {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
        _ => Err(format!("invalid boolean {s:?}")),
    }
}

fn exit_codes_help() -> String {
    let mut out = String::from("Exit codes:\n  0    success\n");
    for (kind, text) in Kind::TABLE {
        let _ = writeln!(out, "  {:<4} {text}", kind.exit_code());
    }
    out
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
        assert!(e.to_string().contains("invalid duration \"banana\""), "{e}");
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
                &c.port,
                &c.username,
                &c.database,
                &c.sslcert,
                &c.sslkey,
                &c.sslrootcert
            ]
            .map(|v| v.as_deref()),
            [
                Some("h"),
                Some("6000"),
                Some("u"),
                Some("db"),
                Some("c"),
                Some("k"),
                Some("r")
            ]
        );
        assert_eq!(c.sslmode, Some(SslMode::Require));
        assert!(r.overwrite && r.force && r.json);
        assert_eq!(r.compat.as_deref(), Some("1"));
        assert_eq!(r.params, ["a=b"]);
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
}
