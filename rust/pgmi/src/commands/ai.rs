use std::io::{IsTerminal, Write};
use std::process::{Command, Stdio};

use super::stdout;
use crate::cli::{AiArgs, AiCommand};
use crate::env::Env;
use crate::error::Result;

pub const CONTRACT: &str = include_str!("contract.json");

pub fn run(a: &AiArgs, env: &Env) -> Result<()> {
    let text = match a.command {
        None => pgmi_shared::file("ai/overview.md")
            .and_then(|b| std::str::from_utf8(b).ok())
            .unwrap_or_default(),
        Some(AiCommand::Contract) => CONTRACT,
    };
    page(text, env)
}

fn pager(env: &Env) -> Result<Option<Vec<String>>> {
    if !std::io::stdin().is_terminal() || !std::io::stdout().is_terminal() {
        return Ok(None);
    }
    let command = match env.non_empty("PGMI_PAGER")? {
        Some(p) => p,
        None => env.non_empty("PAGER")?.unwrap_or("less -R"),
    };
    let words: Vec<String> = command.split_whitespace().map(str::to_string).collect();
    Ok(match words.first().map(String::as_str) {
        None | Some("cat") => None,
        Some(_) => Some(words),
    })
}

pub fn page(text: &str, env: &Env) -> Result<()> {
    let Some(words) = pager(env)? else {
        return stdout(text);
    };
    let child = Command::new(&words[0])
        .args(&words[1..])
        .stdin(Stdio::piped())
        .spawn();
    let Ok(mut child) = child else {
        return stdout(text);
    };
    if let Some(mut input) = child.stdin.take() {
        let _ = input.write_all(text.as_bytes());
    }
    let _ = child.wait();
    Ok(())
}
