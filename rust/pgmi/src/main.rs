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
    std::panic::set_hook(Box::new(|info| {
        let _ = writeln!(std::io::stderr(), "pgmi: internal error: {info}");
    }));
    let code = std::panic::catch_unwind(run).unwrap_or(Kind::Internal.exit_code());
    ExitCode::from(code)
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
        None => help(None),
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

fn clap_error(e: &clap::Error) -> u8 {
    match e.kind() {
        ErrorKind::DisplayHelp | ErrorKind::DisplayVersion => {
            let _ = write!(std::io::stdout(), "{}", e.render());
            0
        }
        _ => {
            let text = e.render().to_string();
            let text = text.strip_prefix("error: ").unwrap_or(&text);
            let _ = write!(std::io::stderr(), "pgmi: error: {text}");
            Kind::Usage.exit_code()
        }
    }
}
