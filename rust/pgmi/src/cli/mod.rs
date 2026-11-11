pub(crate) mod commands;
pub(crate) mod duration;
pub(crate) mod usage;

use std::path::PathBuf;
use std::time::Duration;

use clap::{ArgAction, Args, Parser, Subcommand};

use crate::error::Kind;
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
    #[arg(short = 't', long, value_parser = ["minimal", "basic", "advanced"])]
    pub(crate) template: Option<String>,
}

#[derive(Debug, Args)]
pub(crate) struct DeployArgs {
    pub(crate) project: PathBuf,
    #[arg(long)]
    pub(crate) connection: Option<String>,
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
    #[arg(long, value_parser = duration::parse, help = "Catastrophic failure protection, a Go duration such as 90s or 1h30m; 0 turns it off (default 3m0s)")]
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
}
