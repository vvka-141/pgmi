use std::ffi::OsString;
use std::io::Write;

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
        _ => {
            let _ = writeln!(std::io::stderr(), "pgmi: error: {}", usage_text(e, args));
            Kind::Usage.exit_code()
        }
    }
}

/// The command a command line names, its path from `pgmi`, and the
/// arguments after the command's own name.
struct Invocation {
    command: clap::Command,
    path: String,
    rest: Vec<String>,
}

impl Invocation {
    fn of(args: &[OsString]) -> Self {
        let mut command = Cli::command_line();
        command.build();
        let args: Vec<String> = args
            .iter()
            .skip(1)
            .map(|a| a.to_string_lossy().into_owned())
            .collect();
        let mut path = vec!["pgmi".to_string()];
        let mut at = 0;
        for (i, arg) in args.iter().enumerate() {
            if arg == "--" {
                break;
            }
            if arg.starts_with('-') {
                continue;
            }
            match command.find_subcommand(arg) {
                Some(sub) => {
                    path.push(arg.clone());
                    command = sub.clone();
                    at = i + 1;
                }
                None => break,
            }
        }
        Invocation {
            command,
            path: path.join(" "),
            rest: args.get(at..).map(<[String]>::to_vec).unwrap_or_default(),
        }
    }

    fn arg_named(&self, name: &str) -> Option<&clap::Arg> {
        self.command.get_arguments().find(|a| {
            name.strip_prefix("--").map_or_else(
                || a.get_short().map(|s| format!("-{s}")).as_deref() == Some(name),
                |l| a.get_long() == Some(l),
            )
        })
    }

    fn consumes_next(&self, flag: &str) -> bool {
        self.arg_named(flag).is_some_and(|a| {
            !a.is_positional() && a.get_action().takes_values() && !a.is_require_equals_set()
        })
    }

    fn positionals_given(&self) -> usize {
        let mut given = 0;
        let mut words = self.rest.iter();
        while let Some(word) = words.next() {
            if word == "--" {
                given += words.count();
                break;
            }
            if word.starts_with("--") {
                if !word.contains('=') && self.consumes_next(word) {
                    words.next();
                }
            } else if word.len() > 1 && word.starts_with('-') {
                let shorts: Vec<char> = word.chars().skip(1).collect();
                if let Some(i) = shorts
                    .iter()
                    .position(|c| self.consumes_next(&format!("-{c}")))
                    && i + 1 == shorts.len()
                {
                    words.next();
                }
            } else {
                given += 1;
            }
        }
        given
    }

    fn arity(&self, given: usize) -> Option<String> {
        let positionals: Vec<&clap::Arg> = self.command.get_positionals().collect();
        if positionals.is_empty() {
            return None;
        }
        let max = positionals.len();
        let at_most = if positionals.iter().all(|a| a.is_required_set()) {
            ""
        } else {
            "at most "
        };
        Some(format!("accepts {at_most}{max} arg(s), received {given}"))
    }
}

fn string(e: &clap::Error, kind: ContextKind) -> Option<&str> {
    match e.get(kind) {
        Some(ContextValue::String(s)) => Some(s),
        _ => None,
    }
}

fn strings(e: &clap::Error, kind: ContextKind) -> Vec<&str> {
    match e.get(kind) {
        Some(ContextValue::Strings(s)) => s.iter().map(String::as_str).collect(),
        Some(ContextValue::String(s)) => vec![s.as_str()],
        _ => Vec::new(),
    }
}

fn usage_text(e: &clap::Error, args: &[OsString]) -> String {
    let at = Invocation::of(args);
    match e.kind() {
        ErrorKind::InvalidSubcommand => {
            if let Some(cmd) = string(e, ContextKind::InvalidSubcommand) {
                let mut text = format!("unknown command \"{cmd}\" for \"{}\"", at.path);
                let suggested = strings(e, ContextKind::SuggestedSubcommand);
                if at.path == "pgmi" && !suggested.is_empty() {
                    text.push_str("\n\nDid you mean this?\n");
                    for s in suggested {
                        text.push('\t');
                        text.push_str(s);
                        text.push('\n');
                    }
                }
                return text;
            }
        }
        ErrorKind::UnknownArgument => {
            if let Some(arg) = string(e, ContextKind::InvalidArg) {
                let positional = !arg.starts_with('-')
                    || at.rest.iter().skip_while(|w| *w != "--").any(|w| w == arg);
                let given = at.positionals_given();
                if positional
                    && given > at.command.get_positionals().count()
                    && let Some(text) = at.arity(given)
                {
                    return text;
                }
                if !arg.starts_with('-') {
                    return format!("unknown command \"{arg}\" for \"{}\"", at.path);
                }
                let short = arg.strip_prefix('-').filter(|r| !r.starts_with('-'));
                return match short.and_then(|r| r.chars().next()) {
                    Some(c) => format!("unknown shorthand flag: '{c}' in {arg}"),
                    None => format!("unknown flag: {arg}"),
                };
            }
        }
        ErrorKind::MissingRequiredArgument => {
            let missing = strings(e, ContextKind::InvalidArg);
            if missing.iter().any(|m| matches!(*m, "<PROJECT>" | "<PATH>")) {
                let database = if at.arg_named("--database").is_some() {
                    " -d mydb"
                } else {
                    ""
                };
                return format!(
                    "missing required argument: <project_path>\n\nUsage: {path} <project_path> [flags]\n\nExample:\n  {path} .{database}",
                    path = at.path
                );
            }
            if missing.contains(&"<NAME>") {
                return format!(
                    "missing required argument: <template_name>\n\nUsage: {path} <template_name> [flags]\n\nExample:\n  {path} basic\n\nUse 'pgmi templates list' to see available templates.",
                    path = at.path
                );
            }
            if !missing.iter().any(|m| m.starts_with('<')) {
                let text = e.render().to_string();
                let text = text.trim_end();
                return text.strip_prefix("error: ").unwrap_or(text).to_string();
            }
            if let Some(text) = at.arity(at.positionals_given()) {
                return text;
            }
        }
        ErrorKind::InvalidValue | ErrorKind::ValueValidation => {
            if let Some(arg) = string(e, ContextKind::InvalidArg) {
                return invalid_value(e, &at, arg);
            }
        }
        _ => {}
    }
    first_line(e)
}

fn invalid_value(e: &clap::Error, at: &Invocation, arg: &str) -> String {
    let value = string(e, ContextKind::InvalidValue).unwrap_or_default();
    if arg.starts_with('<') {
        return format!("invalid argument \"{value}\" for \"{}\"", at.path);
    }
    let long = arg.split([' ', '[', '=']).next().unwrap_or(arg).to_string();
    let found = at.arg_named(&long);
    let short = found.and_then(clap::Arg::get_short);
    if value.is_empty() && e.kind() != ErrorKind::ValueValidation {
        let typed_short = short.filter(|s| at.rest.last().is_some_and(|w| *w == format!("-{s}")));
        let mut text = match typed_short {
            Some(s) => format!("flag needs an argument: '{s}' in -{s}"),
            None => format!("flag needs an argument: {long}"),
        };
        if long == "--host" {
            text.push_str(" (-h is --host here; use --help)");
        }
        return text;
    }
    let name = match short {
        Some(s) => format!("-{s}, {long}"),
        None => long,
    };
    let valid = strings(e, ContextKind::ValidValue);
    let reason = std::error::Error::source(e).map_or_else(
        || (!valid.is_empty()).then(|| format!("valid values are {}", valid.join(", "))),
        |why| Some(why.to_string()),
    );
    match reason {
        Some(reason) => format!("invalid argument \"{value}\" for \"{name}\" flag: {reason}"),
        None => format!("invalid argument \"{value}\" for \"{name}\" flag"),
    }
}

fn first_line(e: &clap::Error) -> String {
    let text = e.render().to_string();
    let line = text.lines().next().unwrap_or_default();
    line.strip_prefix("error: ").unwrap_or(line).to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn refused(line: &[&str]) -> String {
        let args: Vec<OsString> = std::iter::once("pgmi")
            .chain(line.iter().copied())
            .map(OsString::from)
            .collect();
        match Cli::parse_args(&args) {
            Err(e) => usage_text(&e, &args),
            Ok(_) => panic!("{line:?} parsed"),
        }
    }

    #[test]
    fn usage_errors_read_as_go_writes_them() {
        assert_eq!(
            refused(&["deplyo"]),
            "unknown command \"deplyo\" for \"pgmi\"\n\nDid you mean this?\n\tdeploy\n"
        );
        assert_eq!(
            refused(&["xyzzy"]),
            "unknown command \"xyzzy\" for \"pgmi\""
        );
        assert_eq!(
            refused(&["metadata", "plna"]),
            "unknown command \"plna\" for \"pgmi metadata\""
        );
        for line in [
            &["deploy", "a", "b"][..],
            &["deploy", "p", "-d", "x", "--", "--weird"],
            &["deploy", "-d", "x", "a", "--force", "b"],
            &["metadata", "plan", "a", "b"],
            &["completion", "bash", "x"],
        ] {
            assert_eq!(refused(line), "accepts 1 arg(s), received 2", "{line:?}");
        }
        assert_eq!(
            refused(&["init", "a", "b"]),
            "accepts at most 1 arg(s), received 2"
        );
        assert_eq!(
            refused(&["version", "x"]),
            "unknown command \"x\" for \"pgmi version\""
        );
        assert_eq!(refused(&["completion"]), "accepts 1 arg(s), received 0");
        assert_eq!(
            refused(&["templates", "describe"]),
            "missing required argument: <template_name>\n\nUsage: pgmi templates describe <template_name> [flags]\n\nExample:\n  pgmi templates describe basic\n\nUse 'pgmi templates list' to see available templates."
        );
        assert_eq!(
            refused(&["deploy"]),
            "missing required argument: <project_path>\n\nUsage: pgmi deploy <project_path> [flags]\n\nExample:\n  pgmi deploy . -d mydb"
        );
        assert_eq!(
            refused(&["metadata", "plan"]),
            "missing required argument: <project_path>\n\nUsage: pgmi metadata plan <project_path> [flags]\n\nExample:\n  pgmi metadata plan ."
        );
        assert_eq!(
            refused(&["completion", "tcsh"]),
            "invalid argument \"tcsh\" for \"pgmi completion\""
        );
        assert_eq!(
            refused(&["deploy", "p", "--connection"]),
            "flag needs an argument: --connection"
        );
        assert_eq!(
            refused(&["deploy", "p", "-h"]),
            "flag needs an argument: 'h' in -h (-h is --host here; use --help)"
        );
        assert!(
            refused(&["deploy", "p", "--timeout", "abc"])
                .starts_with("invalid argument \"abc\" for \"--timeout\" flag: ")
        );
        assert!(
            refused(&["deploy", "p", "-p", "abc"])
                .starts_with("invalid argument \"abc\" for \"-p, --port\" flag: ")
        );
        assert_eq!(
            refused(&["deploy", "p", "-x"]),
            "unknown shorthand flag: 'x' in -x"
        );
        assert_eq!(
            refused(&["deploy", "p", "--conection", "x"]),
            "unknown flag: --conection"
        );
    }
}
