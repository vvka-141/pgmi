use std::path::PathBuf;

use clap::{Args, Subcommand};

use super::go_bool;

#[derive(Debug, Args)]
pub struct InfoArgs {
    pub path: Option<PathBuf>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Emit structured JSON to stdout")]
    pub json: bool,
}

#[derive(Debug, Args)]
pub struct TemplatesArgs {
    #[command(subcommand)]
    pub command: Option<TemplatesCommand>,
}

#[derive(Debug, Subcommand)]
pub enum TemplatesCommand {
    #[command(about = "List available templates")]
    List,
    #[command(about = "Show the structure and features of a template")]
    Describe { name: String },
}

#[derive(Debug, Args)]
pub struct AiArgs {
    #[command(subcommand)]
    pub command: Option<AiCommand>,
}

#[derive(Debug, Subcommand)]
pub enum AiCommand {
    #[command(about = "Print the machine-readable session-API contract")]
    Contract,
}

#[derive(Debug, Args)]
pub struct MetadataArgs {
    #[command(subcommand)]
    pub command: Option<MetadataCommand>,
}

#[derive(Debug, Subcommand)]
pub enum MetadataCommand {
    #[command(about = "Show the execution plan pgmi_plan_view will hold")]
    Plan(MetadataReport),
    #[command(about = "Check every <pgmi-meta> block and refuse bad or duplicate ids")]
    Validate(MetadataReport),
    #[command(about = "Add <pgmi-meta> blocks to SQL files that have none")]
    Scaffold(ScaffoldArgs),
}

#[derive(Debug, Args)]
pub struct MetadataReport {
    pub path: PathBuf,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Emit structured JSON to stdout")]
    pub json: bool,
}

#[derive(Debug, Args)]
pub struct ScaffoldArgs {
    pub path: PathBuf,
    #[arg(
        long,
        help = "Sort-key prefix for the generated keys, e.g. 005/core/ (default: the prefix of existing metadata in the same directory)"
    )]
    pub sort_key: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "true", value_parser = go_bool, help = "Mark generated scripts as idempotent")]
    pub idempotent: bool,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Write generated metadata to files (default: preview only)")]
    pub write: bool,
}

#[derive(Debug, Args)]
pub struct CompletionArgs {
    #[arg(value_parser = ["bash", "zsh", "fish", "powershell"])]
    pub shell: String,
}

#[derive(Debug, Args)]
pub struct ConfigArgs {
    pub path: Option<PathBuf>,
}
