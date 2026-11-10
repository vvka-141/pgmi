use super::{stderr, stdout};
use crate::cli::{TemplatesArgs, TemplatesCommand};
use crate::error::{Error, Result};

pub const NAMES: [&str; 3] = ["minimal", "basic", "advanced"];

pub const LIST: [(&str, &str, &str); 3] = [
    (
        "minimal",
        "A deploy.sql loop, hello_world.sql and a .pgmiignore; nothing else",
        "Building your own deployment from nothing",
    ),
    (
        "basic",
        "Two layers: run-once tables, re-run views and functions",
        "Start here. Deploying SQL — small, explicit, any managed provider",
    ),
    (
        "advanced",
        "Reference app: SQL-native REST/RPC/MCP, multi-tenant RLS, audit",
        "SQL-native application stack — scaffold, read, trim to fit",
    ),
];

fn describe_text(name: &str) -> Option<&'static str> {
    match name {
        "minimal" => Some(include_str!("templates/minimal.txt")),
        "basic" => Some(include_str!("templates/basic.txt")),
        "advanced" => Some(include_str!("templates/advanced.txt")),
        _ => None,
    }
}

pub fn unknown(name: &str) -> Error {
    let mut names = NAMES.to_vec();
    names.sort_unstable();
    Error::usage(format!(
        "unknown template {name:?} (available: {})\nrun `pgmi templates list` for descriptions",
        names.join(", ")
    ))
}

pub fn run(a: &TemplatesArgs, help: impl FnOnce() -> Result<()>) -> Result<()> {
    match &a.command {
        None => help(),
        Some(TemplatesCommand::List) => {
            let mut text = String::new();
            for (name, summary, for_) in LIST {
                text.push_str(&format!(
                    "{name:<12} {summary}\n               for: {for_}\n"
                ));
            }
            stdout(&text)?;
            stderr("\nScaffold one with: pgmi init <path> -t <template>\n");
            Ok(())
        }
        Some(TemplatesCommand::Describe { name }) => {
            let text = describe_text(name).ok_or_else(|| unknown(name))?;
            stdout(text)?;
            stderr(&format!("\nScaffold with: pgmi init <path> -t {name}\n"));
            Ok(())
        }
    }
}
