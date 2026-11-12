//! pgmi as a library: the binary, `tests/` and the fuzz targets link this
//! crate. The `pub` items exist for those three and are not a stable API;
//! `invariants` is built for tests and with the `fuzzing` feature only.

#![forbid(unsafe_code)]

mod blocking;
mod cli;
mod commands;
mod config;
pub mod conn;
#[cfg(test)]
mod content_tests;
mod deploy;
mod duration;
mod env;
mod error;
#[cfg(any(test, feature = "fuzzing"))]
pub mod invariants;
mod output;
mod project;
pub mod serve;
mod signals;
mod templates;
#[cfg(test)]
mod testdir;
mod tui;
mod vfs;

use std::ffi::OsString;
use std::io::Write;

use cli::usage::{clap_error, help};
use cli::{Cli, Command, Parsed};
pub use env::Env;
pub use error::install_panic_hook;
use error::{Error, Kind};

pub(crate) const VERSION: &str = env!("CARGO_PKG_VERSION");

/// The exit code of a run that panicked (record 0002).
pub const PANIC_EXIT_CODE: u8 = Kind::Internal.exit_code();

/// Runs pgmi with `args` (program name first) and returns the exit code.
///
/// `env` is the process environment, taken once by the caller. `export` puts
/// a project `.env` value into the process environment; `run` calls it
/// before it starts any thread (record 0016).
pub fn run(args: impl IntoIterator<Item = OsString>, env: Env, export: impl Fn(&str, &str)) -> u8 {
    let args: Vec<OsString> = args.into_iter().collect();
    if let Some(request) = args.get(1).and_then(|a| a.to_str()).filter(|a| {
        [
            commands::completion::REQUEST,
            commands::completion::REQUEST_NO_DESCRIPTIONS,
        ]
        .contains(a)
    }) {
        let words: Vec<String> = args[2..]
            .iter()
            .map(|a| a.to_string_lossy().into_owned())
            .collect();
        return match commands::completion::request(request, &words, &env) {
            Ok(()) => 0,
            Err(e) => report(&e),
        };
    }
    let cli = match Cli::parse_args(&args) {
        Ok(Parsed::Run(c)) => c,
        Ok(Parsed::Help(text)) => {
            let _ = std::io::stdout().write_all(text.as_bytes());
            return 0;
        }
        Err(e) => return clap_error(&e, &args),
    };
    if cli.version {
        return print_version(false);
    }
    let result = match cli.command {
        None => bare(&env),
        Some(Command::Version) => return print_version(true),
        Some(Command::Help { command }) => help(&command),
        Some(Command::Deploy(args)) => return run_deploy(*args, cli.verbose, env, export),
        Some(Command::Init(a)) => with_runtime(|rt| commands::init::run(&a, cli.verbose, &env, rt)),
        Some(Command::Info(a)) => commands::info::run(&a, &env),
        Some(Command::Templates(a)) => commands::templates::run(&a, || help(&["templates"])),
        Some(Command::Ai(a)) => commands::ai::run(&a, &env),
        Some(Command::Metadata(a)) => {
            commands::metadata::run(&a, cli.verbose, &env, || help(&["metadata"]))
        }
        Some(Command::Completion(a)) => commands::completion::run(&a),
        Some(Command::Serve) => runtime().and_then(|rt| serve::run(rt, cli.verbose, env)),
        Some(Command::Config(a)) => with_runtime(|rt| commands::config::run(&a, &env, rt)),
    };
    match result {
        Ok(()) => 0,
        Err(e) => report(&e),
    }
}

fn report(e: &Error) -> u8 {
    let _ = writeln!(std::io::stderr(), "pgmi: error: {}", e.text());
    e.exit_code()
}

/// The one tokio runtime of a run (record 0003). Built only after the
/// project `.env` is exported, because `export` must run while pgmi has a
/// single thread (record 0016).
fn runtime() -> error::Result<tokio::runtime::Runtime> {
    tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .map_err(|e| Error::general("start the runtime").with_source(e))
}

fn with_runtime(
    command: impl FnOnce(&tokio::runtime::Handle) -> error::Result<()>,
) -> error::Result<()> {
    let runtime = runtime()?;
    let result = command(runtime.handle());
    runtime.shutdown_background();
    result
}

fn run_deploy(
    args: cli::DeployArgs,
    verbose: bool,
    mut env: Env,
    export: impl Fn(&str, &str),
) -> u8 {
    conn::tls::install();
    let dotenv = match config::project_dotenv(&vfs::OsFs::new(&args.project), &env) {
        Ok(d) => d,
        Err(e) => {
            let _ = writeln!(std::io::stderr(), "pgmi: error: {}", e.text());
            return e.exit_code();
        }
    };
    for (k, v) in &dotenv.applied {
        export(k, v);
        env.insert(k, v);
    }
    let runtime = match runtime() {
        Ok(r) => r,
        Err(e) => {
            output::Reporter::new(verbose, &env).line(&format!("pgmi: error: {}", e.text()));
            return e.exit_code();
        }
    };
    let request = deploy::Request::from(args);
    let approver = Box::new(deploy::Terminal::new(request.force, &env));
    deploy::run(
        runtime,
        &deploy::Context {
            request,
            verbose,
            env,
            dotenv_keys: dotenv.applied.into_iter().map(|(k, _)| k).collect(),
            dotenv_refused: dotenv.refused,
            serve_hosts: None,
            stdin: deploy::Stdin::Process,
            approver,
        },
    )
}

/// `pgmi --version` and `pgmi version`, line for line as Go prints them: the
/// greppable first line, then commit, build time and platform in Go's names;
/// `version` adds the repository.
fn print_version(repository: bool) -> u8 {
    let os = match std::env::consts::OS {
        "macos" => "darwin",
        os => os,
    };
    let arch = match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        arch => arch,
    };
    let mut text = format!(
        "pgmi {VERSION} (compat 1)\nCommit: {}, Built: {}, Platform: {os}/{arch}\n",
        option_env!("PGMI_COMMIT").unwrap_or("unknown"),
        option_env!("PGMI_BUILD_DATE").unwrap_or("unknown"),
    );
    if repository {
        text.push_str("Repository: https://github.com/vvka-141/pgmi\n");
    }
    match std::io::stdout().lock().write_all(text.as_bytes()) {
        Ok(()) => 0,
        Err(_) => Kind::General.exit_code(),
    }
}

fn bare(env: &Env) -> error::Result<()> {
    if !tui::interactive(env) || env.non_empty("PGMI_NO_BANNER").ok().flatten().is_some() {
        return help::<&str>(&[]);
    }
    let logo = pgmi_shared::file("art/logo.txt")
        .and_then(|b| std::str::from_utf8(b).ok())
        .unwrap_or("pgmi");
    let text = format!(
        "{}\nPostgreSQL-native deployment driver.\n\n  pgmi init demo                Scaffold a starter project\n  pgmi deploy ./demo -d mydb    Run deploy.sql against mydb\n  pgmi --help                   Full reference\n",
        logo.trim_end()
    );
    std::io::stderr()
        .write_all(text.as_bytes())
        .map_err(|e| Error::general("write banner").with_source(e))
}
