pub mod init;
pub mod select;

use std::io::IsTerminal;

use crate::env::Env;

pub fn interactive(env: &Env) -> bool {
    std::io::stdin().is_terminal()
        && std::io::stdout().is_terminal()
        && env.non_empty("CI").ok().flatten().is_none()
        && env.get("PGMI_NON_INTERACTIVE").ok().flatten() != Some("1")
}
