use std::ffi::OsString;
use std::io::Write;

use clap::CommandFactory;
use clap::error::{ContextKind, ContextValue, ErrorKind};

use super::Cli;
use crate::error::{Error, Kind, Result};

pub(crate) fn help<S: AsRef<str>>(command: &[S]) -> Result<()> {
    let mut target = Cli::command_line();
    target.build();
    for name in command {
        let name = name.as_ref();
        target = match target.find_subcommand(name) {
            Some(c) => c.clone(),
            None => {
                return Err(Error::usage(format!(
                    "unknown command {name:?} for \"pgmi\""
                )));
            }
        };
    }
    let text = target.render_long_help().to_string();
    std::io::stdout()
        .write_all(text.as_bytes())
        .map_err(|e| Error::general("write help").with_source(e))
}

pub(crate) fn clap_error(e: &clap::Error, args: &[OsString]) -> u8 {
    match e.kind() {
        ErrorKind::DisplayHelp | ErrorKind::DisplayVersion => {
            let _ = write!(std::io::stdout(), "{}", e.render());
            0
        }
        ErrorKind::InvalidValue if matches!(e.get(ContextKind::InvalidArg), Some(ContextValue::String(a)) if a.starts_with("--sslmode")) =>
        {
            let value = match e.get(ContextKind::InvalidValue) {
                Some(ContextValue::String(v)) => v.as_str(),
                _ => "",
            };
            let _ = writeln!(
                std::io::stderr(),
                "pgmi: error: invalid --sslmode {value:?} (valid: disable, allow, prefer, require, verify-ca, verify-full)"
            );
            Kind::Config.exit_code()
        }
        _ => {
            let _ = writeln!(std::io::stderr(), "pgmi: error: {}", usage_text(e, args));
            Kind::Usage.exit_code()
        }
    }
}

fn usage_text(e: &clap::Error, args: &[OsString]) -> String {
    let value = |kind| match e.get(kind) {
        Some(ContextValue::String(s)) => Some(s.clone()),
        _ => None,
    };
    match e.kind() {
        ErrorKind::InvalidSubcommand => {
            if let Some(cmd) = value(ContextKind::InvalidSubcommand) {
                let parent = parent_command(args);
                return format!(
                    "unknown command \"{cmd}\" for \"{parent}\"\nRun '{parent} --help' for usage."
                );
            }
        }
        ErrorKind::UnknownArgument => {
            if let Some(arg) = value(ContextKind::InvalidArg) {
                if !arg.starts_with('-') {
                    return format!("unknown command \"{arg}\" for \"{}\"", parent_command(args));
                }
                let short = arg.strip_prefix('-').filter(|r| !r.starts_with('-'));
                return match short.and_then(|r| r.chars().next()) {
                    Some(c) => format!("unknown shorthand flag: '{c}' in {arg}"),
                    None => format!("unknown flag: {arg}"),
                };
            }
        }
        _ => {}
    }
    let text = e.render().to_string();
    let text = text
        .trim_end()
        .strip_prefix("error: ")
        .unwrap_or(text.trim_end())
        .to_string();
    if value(ContextKind::InvalidArg).is_some_and(|a| a.starts_with("--host")) {
        return format!("{text}\n-h is --host on deploy, as in psql; use --help for help");
    }
    text
}

fn parent_command(args: &[OsString]) -> String {
    let mut cmd = Cli::command();
    let mut path = vec!["pgmi".to_string()];
    for arg in args
        .iter()
        .skip(1)
        .map(|a| a.to_string_lossy())
        .filter(|a| !a.starts_with('-'))
    {
        match cmd.find_subcommand(arg.as_ref()) {
            Some(sub) => {
                path.push(arg.into_owned());
                cmd = sub.clone();
            }
            None => break,
        }
    }
    path.join(" ")
}
