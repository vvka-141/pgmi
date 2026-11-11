pub(crate) mod commands;
pub(crate) mod usage;

use std::path::PathBuf;
use std::time::Duration;

use clap::{ArgAction, Args, Parser, Subcommand};

use crate::conn::cloud::{self, Provider};
use crate::conn::resolve::Flags;
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
    #[arg(short = 'v', long, global = true, action = ArgAction::SetTrue)]
    pub(crate) verbose: bool,
    #[arg(long, global = true, action = ArgAction::Help)]
    pub(crate) help: Option<bool>,
    #[arg(long, action = ArgAction::SetTrue)]
    pub(crate) version: bool,
    #[command(subcommand)]
    pub(crate) command: Option<Command>,
}

#[derive(Debug, Subcommand)]
pub(crate) enum Command {
    #[command(about = "Deploy a project: load it into a session and run its deploy.sql", after_help = exit_codes_help())]
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
    #[arg(long)]
    pub(crate) connection: Option<Secret>,
    #[arg(short = 'h', long)]
    pub(crate) host: Option<String>,
    #[arg(short = 'p', long)]
    pub(crate) port: Option<String>,
    #[arg(short = 'U', long)]
    pub(crate) username: Option<String>,
    #[arg(short = 'd', long)]
    pub(crate) database: Option<String>,
    #[arg(long, value_parser = ["disable", "allow", "prefer", "require", "verify-ca", "verify-full"])]
    pub(crate) sslmode: Option<String>,
    #[arg(long)]
    pub(crate) sslcert: Option<String>,
    #[arg(long)]
    pub(crate) sslkey: Option<String>,
    #[arg(long)]
    pub(crate) sslrootcert: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub(crate) overwrite: bool,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub(crate) force: bool,
    #[arg(long, value_parser = crate::duration::parse, help = "Catastrophic failure protection, a Go duration such as 90s or 1h30m; 0 turns it off (default 3m0s)")]
    pub(crate) timeout: Option<Duration>,
    #[arg(long)]
    pub(crate) compat: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub(crate) json: bool,
    #[arg(long = "param")]
    pub(crate) params: Vec<String>,
    #[arg(long = "params-file")]
    pub(crate) params_files: Vec<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub(crate) azure: bool,
    #[arg(long)]
    pub(crate) azure_tenant_id: Option<String>,
    #[arg(long)]
    pub(crate) azure_client_id: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub(crate) aws: bool,
    #[arg(long)]
    pub(crate) aws_region: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub(crate) google: bool,
    #[arg(long)]
    pub(crate) google_instance: Option<String>,
}

impl From<DeployArgs> for crate::deploy::Request {
    fn from(a: DeployArgs) -> Self {
        let cloud = [
            (a.azure, Provider::Azure),
            (a.aws, Provider::Aws),
            (a.google, Provider::Google),
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
            cloud_request: cloud::Request {
                tenant_id: a.azure_tenant_id,
                client_id: a.azure_client_id,
                aws_region: a.aws_region,
                google_instance: a.google_instance,
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
        out.push_str(&format!("  {:<4} {text}\n", kind.exit_code()));
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
        assert_eq!(d.timeout, Some(Duration::from_secs(5400)));
        assert!(Cli::try_parse_from(["pgmi", "deploy", ".", "--timeout", "banana"]).is_err());
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
            d.connection.as_ref().map(|c| c.expose()),
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
                &c.sslmode,
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
                Some("require"),
                Some("c"),
                Some("k"),
                Some("r")
            ]
        );
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
        let r = request(&[".", "--azure", "--aws", "--google"]);
        assert_eq!(r.cloud, [Provider::Azure, Provider::Aws, Provider::Google]);
        assert!(request(&["."]).cloud.is_empty());
    }
}
