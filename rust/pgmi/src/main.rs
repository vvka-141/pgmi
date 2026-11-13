use std::process::ExitCode;

fn main() -> ExitCode {
    #[cfg(unix)]
    if !deploys(std::env::args_os()) {
        restore_sigpipe();
    }
    pgmi::install_panic_hook();
    let code =
        std::panic::catch_unwind(|| pgmi::run(std::env::args_os(), pgmi::Env::snapshot(), export))
            .unwrap_or(pgmi::PANIC_EXIT_CODE);
    ExitCode::from(code)
}

#[cfg(unix)]
fn deploys(args: impl Iterator<Item = std::ffi::OsString>) -> bool {
    args.skip(1)
        .find(|a| !a.to_string_lossy().starts_with('-'))
        .is_some_and(|a| a == "deploy")
}

#[cfg(unix)]
#[allow(unsafe_code)]
fn restore_sigpipe() {
    // SAFETY: called first in main, before any thread exists; SIG_DFL is a valid disposition (record 0002).
    unsafe {
        libc::signal(libc::SIGPIPE, libc::SIG_DFL);
    }
}

#[allow(unsafe_code, clippy::disallowed_methods)]
fn export(key: &str, value: &str) {
    // SAFETY: pgmi::run calls this after reading the project .env and before it starts the tokio runtime or any other thread (record 0016).
    unsafe { std::env::set_var(key, value) }
}
