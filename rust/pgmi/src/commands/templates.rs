use super::{stderr, stdout};
use crate::cli::{TemplatesArgs, TemplatesCommand};
use crate::error::{Error, Result};
use crate::templates::{LIST, NAMES, describe};

pub(crate) fn unknown(name: &str) -> Error {
    let mut names = NAMES.to_vec();
    names.sort_unstable();
    Error::usage(format!(
        "unknown template {name:?} (available: {})\nrun `pgmi templates list` for descriptions",
        names.join(", ")
    ))
}

pub(crate) fn run(a: &TemplatesArgs, help: impl FnOnce() -> Result<()>) -> Result<()> {
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
            let text = describe(name).ok_or_else(|| unknown(name))?;
            stdout(text)?;
            stderr(&format!("\nScaffold with: pgmi init <path> -t {name}\n"));
            Ok(())
        }
    }
}
