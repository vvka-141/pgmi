pub(crate) mod ai;
pub(crate) mod completion;
pub(crate) mod config;
pub(crate) mod info;
pub(crate) mod init;
pub(crate) mod metadata;
pub(crate) mod templates;

use std::io::Write;

use crate::error::{Error, Result};

pub(crate) fn stdout(text: &str) -> Result<()> {
    crate::output::drain();
    std::io::stdout()
        .lock()
        .write_all(text.as_bytes())
        .map_err(|e| Error::general("write output").with_source(e))
}

pub(crate) fn stderr(text: &str) {
    crate::output::drain();
    let _ = std::io::stderr().lock().write_all(text.as_bytes());
}

pub(crate) fn json(value: &impl serde::Serialize) -> Result<()> {
    let text = serde_json::to_string_pretty(value)
        .map_err(|e| Error::internal("encode JSON").with_source(e))?;
    stdout(&format!("{text}\n"))
}
