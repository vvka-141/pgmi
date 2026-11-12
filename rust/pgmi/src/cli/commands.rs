use std::ffi::OsStr;
use std::path::PathBuf;

use clap::builder::{PossibleValue, StringValueParser, TypedValueParser};
use clap::{Args, Subcommand};

use super::{go_bool, help_text};

/// Any text, so `templates describe` keeps its own refusal of an unknown
/// name; the template names are offered to shell completion.
#[derive(Clone)]
struct TemplateName;

impl TypedValueParser for TemplateName {
    type Value = String;

    fn parse_ref(
        &self,
        cmd: &clap::Command,
        arg: Option<&clap::Arg>,
        value: &OsStr,
    ) -> Result<String, clap::Error> {
        StringValueParser::new().parse_ref(cmd, arg, value)
    }

    fn possible_values(&self) -> Option<Box<dyn Iterator<Item = PossibleValue> + '_>> {
        Some(Box::new(
            crate::templates::Template::ALL
                .iter()
                .map(|t| PossibleValue::new(t.name())),
        ))
    }
}

#[derive(Debug, Args)]
pub(crate) struct InfoArgs {
    pub(crate) path: Option<PathBuf>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Emit structured JSON to stdout")]
    pub(crate) json: bool,
}

#[derive(Debug, Args)]
pub(crate) struct TemplatesArgs {
    #[command(subcommand)]
    pub(crate) command: Option<TemplatesCommand>,
}

#[derive(Debug, Subcommand)]
pub(crate) enum TemplatesCommand {
    #[command(about = "List available templates", long_about = help_text::TEMPLATES_LIST)]
    List,
    #[command(
        about = "Show the structure and features of a template",
        long_about = help_text::TEMPLATES_DESCRIBE,
        after_help = help_text::TEMPLATES_DESCRIBE_EXAMPLES
    )]
    Describe {
        #[arg(value_parser = TemplateName, hide_possible_values = true)]
        name: String,
    },
}

#[derive(Debug, Args)]
pub(crate) struct AiArgs {
    #[command(subcommand)]
    pub(crate) command: Option<AiCommand>,
}

#[derive(Debug, Subcommand)]
pub(crate) enum AiCommand {
    #[command(about = "Print the machine-readable session-API contract", long_about = help_text::AI_CONTRACT)]
    Contract,
}

#[derive(Debug, Args)]
pub(crate) struct MetadataArgs {
    #[command(subcommand)]
    pub(crate) command: Option<MetadataCommand>,
}

#[derive(Debug, Subcommand)]
pub(crate) enum MetadataCommand {
    #[command(about = "Show the rows pgmi_plan_view will hold, in order", long_about = help_text::METADATA_PLAN)]
    Plan(MetadataReport),
    #[command(about = "Check <pgmi-meta> XML validity and uniqueness", long_about = help_text::METADATA_VALIDATE)]
    Validate(MetadataReport),
    #[command(about = "Add <pgmi-meta> blocks to files that lack one", long_about = help_text::METADATA_SCAFFOLD)]
    Scaffold(ScaffoldArgs),
}

#[derive(Debug, Args)]
pub(crate) struct MetadataReport {
    pub(crate) path: PathBuf,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Emit structured JSON to stdout")]
    pub(crate) json: bool,
}

#[derive(Debug, Args)]
pub(crate) struct ScaffoldArgs {
    pub(crate) path: PathBuf,
    #[arg(
        long,
        help = "Sort-key prefix for the generated keys, e.g. 005/core/ (default: the prefix of existing metadata in the same directory)"
    )]
    pub(crate) sort_key: Option<String>,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "true", value_parser = go_bool, help = "Mark generated scripts as idempotent")]
    pub(crate) idempotent: bool,
    #[arg(long, num_args = 0..=1, require_equals = true, default_missing_value = "true", default_value = "false", value_parser = go_bool, help = "Write generated metadata to files (default: preview only)")]
    pub(crate) write: bool,
}

#[derive(Debug, Args)]
pub(crate) struct CompletionArgs {
    #[arg(value_parser = ["bash", "zsh", "fish", "powershell"])]
    pub(crate) shell: String,
}

#[derive(Debug, Args)]
pub(crate) struct ConfigArgs {
    pub(crate) path: Option<PathBuf>,
}
