mod cli;
mod config;
mod conn;
mod deploy;
mod env;
mod error;
mod init;
mod output;
mod project;

use std::io::Write;
use std::process::ExitCode;

use clap::Parser;
use clap::error::ErrorKind;

use cli::{Cli, Command};
use env::Env;
use error::{Error, Kind};

fn main() -> ExitCode {
    #[cfg(unix)]
    restore_sigpipe();
    error::install_panic_hook();
    let code = std::panic::catch_unwind(run).unwrap_or(Kind::Internal.exit_code());
    ExitCode::from(code)
}

#[cfg(unix)]
#[allow(unsafe_code)]
fn restore_sigpipe() {
    // SAFETY: called first in main, before any thread exists; SIG_DFL is a valid disposition (record 0002).
    unsafe {
        libc::signal(libc::SIGPIPE, libc::SIG_DFL);
    }
}

fn run() -> u8 {
    let cli = match Cli::try_parse() {
        Ok(c) => c,
        Err(e) => return clap_error(&e),
    };
    if cli.version {
        return print_version(false);
    }
    let result = match cli.command {
        None => bare(),
        Some(Command::Version) => return print_version(true),
        Some(Command::Help { command }) => help(command.as_deref()),
        Some(Command::Deploy(args)) => return run_deploy(*args, cli.verbose),
        Some(Command::Init(a)) => init::run(&a),
        Some(
            Command::Info(_)
            | Command::Metadata(_)
            | Command::Templates(_)
            | Command::Ai(_)
            | Command::Serve(_)
            | Command::Config(_)
            | Command::Completion(_),
        ) => Err(Error::usage(
            "this command is not implemented in this build",
        )),
    };
    match result {
        Ok(()) => 0,
        Err(e) => {
            let _ = writeln!(std::io::stderr(), "pgmi: error: {}", e.text());
            e.exit_code()
        }
    }
}

fn run_deploy(args: cli::DeployArgs, verbose: bool) -> u8 {
    conn::tls::install();
    let mut env = Env::snapshot();
    let dotenv = match config::project_dotenv(&args.project, &env) {
        Ok(d) => d,
        Err(e) => {
            let _ = writeln!(std::io::stderr(), "pgmi: error: {}", e.text());
            return e.exit_code();
        }
    };
    for (k, v) in &dotenv {
        export(k, v);
        env.insert(k, v);
    }
    deploy::run(deploy::Context {
        args,
        verbose,
        env,
        dotenv_keys: dotenv.into_iter().map(|(k, _)| k).collect(),
    })
}

#[allow(unsafe_code, clippy::disallowed_methods)]
fn export(key: &str, value: &str) {
    // SAFETY: called from main before the tokio runtime or any other thread exists (record 0016).
    unsafe { std::env::set_var(key, value) }
}

fn print_version(long: bool) -> u8 {
    let mut out = std::io::stdout().lock();
    let mut text = format!("pgmi {} (compat 1)\n", cli::VERSION);
    if long {
        text.push_str(&format!(
            "Commit: {}\n",
            option_env!("PGMI_COMMIT").unwrap_or("unknown")
        ));
        text.push_str(&format!(
            "Platform: {}/{}\n",
            std::env::consts::OS,
            std::env::consts::ARCH
        ));
    }
    match out.write_all(text.as_bytes()) {
        Ok(()) => 0,
        Err(_) => Kind::General.exit_code(),
    }
}

fn bare() -> error::Result<()> {
    use std::io::IsTerminal;
    let env = Env::snapshot();
    let banner =
        std::io::stderr().is_terminal() && env.non_empty("PGMI_NO_BANNER").ok().flatten().is_none();
    if !banner {
        return help(None);
    }
    let logo = pgmi_shared::file("art/logo.txt")
        .and_then(|b| std::str::from_utf8(b).ok())
        .unwrap_or("pgmi");
    let text = format!(
        "{}\n\n  pgmi deploy <path>   Deploy a project\n  pgmi init <path>     Start a project from a template\n  pgmi --help          Full reference\n",
        logo.trim_end()
    );
    std::io::stderr()
        .write_all(text.as_bytes())
        .map_err(|e| Error::general("write banner").with_source(e))
}

fn help(command: Option<&str>) -> error::Result<()> {
    use clap::CommandFactory;
    let mut root = Cli::command();
    let mut target = match command {
        None => root,
        Some(name) => match root.find_subcommand_mut(name) {
            Some(c) => c.clone(),
            None => {
                return Err(Error::usage(format!(
                    "unknown command {name:?} for \"pgmi\""
                )));
            }
        },
    };
    let text = target.render_long_help().to_string();
    std::io::stdout()
        .write_all(text.as_bytes())
        .map_err(|e| Error::general("write help").with_source(e))
}

fn usage_text(e: &clap::Error) -> String {
    use clap::error::{ContextKind, ContextValue};
    let value = |kind| match e.get(kind) {
        Some(ContextValue::String(s)) => Some(s.clone()),
        _ => None,
    };
    match e.kind() {
        ErrorKind::InvalidSubcommand => {
            if let Some(cmd) = value(ContextKind::InvalidSubcommand) {
                return format!(
                    "unknown command \"{cmd}\" for \"pgmi\"\nRun 'pgmi --help' for usage."
                );
            }
        }
        ErrorKind::UnknownArgument => {
            if let Some(arg) = value(ContextKind::InvalidArg) {
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

fn clap_error(e: &clap::Error) -> u8 {
    match e.kind() {
        ErrorKind::DisplayHelp | ErrorKind::DisplayVersion => {
            let _ = write!(std::io::stdout(), "{}", e.render());
            0
        }
        _ => {
            let _ = writeln!(std::io::stderr(), "pgmi: error: {}", usage_text(e));
            Kind::Usage.exit_code()
        }
    }
}
