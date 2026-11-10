use clap::CommandFactory;
use clap_complete::Shell;

use super::stdout;
use crate::cli::{Cli, CompletionArgs};
use crate::error::{Error, Result};

pub fn run(a: &CompletionArgs) -> Result<()> {
    let shell = match a.shell.as_str() {
        "bash" => Shell::Bash,
        "zsh" => Shell::Zsh,
        "fish" => Shell::Fish,
        _ => Shell::PowerShell,
    };
    let mut buf = Vec::new();
    clap_complete::generate(shell, &mut Cli::command(), "pgmi", &mut buf);
    let text =
        String::from_utf8(buf).map_err(|e| Error::internal("completion script").with_source(e))?;
    stdout(&text)
}
