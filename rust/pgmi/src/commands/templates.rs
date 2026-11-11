use super::{stderr, stdout};
use crate::cli::{TemplatesArgs, TemplatesCommand};
use crate::error::{Error, Result};
use crate::templates::Template;
use std::fmt::Write as _;

pub(crate) fn unknown(name: &str) -> Error {
    let mut names = Template::ALL.map(Template::name);
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
            for t in Template::ALL {
                let _ = write!(
                    text,
                    "{:<12} {}\n               for: {}\n",
                    t.name(),
                    t.summary(),
                    t.best_for()
                );
            }
            stdout(&text)?;
            stderr("\nScaffold one with: pgmi init <path> -t <template>\n");
            Ok(())
        }
        Some(TemplatesCommand::Describe { name }) => {
            let text = name
                .parse::<Template>()
                .map_err(|_| unknown(name))?
                .description();
            stdout(text)?;
            stderr(&format!("\nScaffold with: pgmi init <path> -t {name}\n"));
            Ok(())
        }
    }
}
