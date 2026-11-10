pub mod duration;

use std::ffi::OsString;
use std::path::PathBuf;
use std::time::Duration;

use clap::{ArgAction, Args, Parser, Subcommand};

use crate::error::Kind;

pub const VERSION: &str = env!("CARGO_PKG_VERSION");

#[derive(Debug, Parser)]
#[command(
    name = "pgmi",
    about = "PostgreSQL-native execution fabric",
    disable_help_flag = true,
    disable_version_flag = true,
    disable_help_subcommand = true
)]
pub struct Cli {
    #[arg(short = 'v', long, global = true, action = ArgAction::SetTrue)]
    pub verbose: bool,
    #[arg(long, global = true, action = ArgAction::Help)]
    pub help: Option<bool>,
    #[arg(long, action = ArgAction::SetTrue)]
    pub version: bool,
    #[command(subcommand)]
    pub command: Option<Command>,
}

#[derive(Debug, Subcommand)]
pub enum Command {
    #[command(about = "Deploy a project: load it into a session and run its deploy.sql", after_help = exit_codes_help())]
    Deploy(Box<DeployArgs>),
    #[command(about = "Write a project from a template")]
    Init(InitArgs),
    #[command(about = "Print the version")]
    Version,
    #[command(about = "Show help for a command")]
    Help { command: Option<String> },
    #[command(about = "Summarise a project (not implemented in this build)")]
    Info(Rest),
    #[command(about = "Inspect <pgmi-meta> blocks (not implemented in this build)")]
    Metadata(Rest),
    #[command(about = "List and describe templates (not implemented in this build)")]
    Templates(Rest),
    #[command(about = "Documentation for AI assistants (not implemented in this build)")]
    Ai(Rest),
    #[command(about = "MCP server over stdio (not implemented in this build)")]
    Serve(Rest),
    #[command(about = "Connection wizard (not implemented in this build)")]
    Config(Rest),
    #[command(about = "Shell completion scripts (not implemented in this build)")]
    Completion(Rest),
}

#[derive(Debug, Args)]
pub struct Rest {
    #[arg(trailing_var_arg = true, allow_hyphen_values = true, hide = true)]
    pub args: Vec<OsString>,
}

#[derive(Debug, Args)]
pub struct InitArgs {
    pub path: Option<PathBuf>,
    #[arg(short = 't', long, value_parser = ["minimal", "basic", "advanced"])]
    pub template: Option<String>,
}

#[derive(Debug, Args)]
pub struct DeployArgs {
    pub project: PathBuf,
    #[arg(long)]
    pub connection: Option<String>,
    #[arg(short = 'h', long)]
    pub host: Option<String>,
    #[arg(short = 'p', long)]
    pub port: Option<String>,
    #[arg(short = 'U', long)]
    pub username: Option<String>,
    #[arg(short = 'd', long)]
    pub database: Option<String>,
    #[arg(long, value_parser = ["disable", "allow", "prefer", "require", "verify-ca", "verify-full"])]
    pub sslmode: Option<String>,
    #[arg(long)]
    pub sslcert: Option<String>,
    #[arg(long)]
    pub sslkey: Option<String>,
    #[arg(long)]
    pub sslrootcert: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub overwrite: bool,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub force: bool,
    #[arg(long, value_parser = duration::parse)]
    pub timeout: Option<Duration>,
    #[arg(long)]
    pub compat: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub json: bool,
    #[arg(long = "param")]
    pub params: Vec<String>,
    #[arg(long = "params-file")]
    pub params_files: Vec<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub azure: bool,
    #[arg(long)]
    pub azure_tenant_id: Option<String>,
    #[arg(long)]
    pub azure_client_id: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub aws: bool,
    #[arg(long)]
    pub aws_region: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool)]
    pub google: bool,
    #[arg(long)]
    pub google_instance: Option<String>,
}

pub fn go_bool(s: &str) -> Result<bool, String> {
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
