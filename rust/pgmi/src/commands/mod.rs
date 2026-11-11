pub mod ai;
pub mod completion;
pub mod config;
pub mod info;
pub mod metadata;
pub mod templates;

use std::io::Write;

use crate::error::{Error, Result};

pub fn stdout(text: &str) -> Result<()> {
    std::io::stdout()
        .lock()
        .write_all(text.as_bytes())
        .map_err(|e| Error::general("write output").with_source(e))
}

pub fn stderr(text: &str) {
    let _ = std::io::stderr().lock().write_all(text.as_bytes());
}

pub fn json(value: &impl serde::Serialize) -> Result<()> {
    let text = serde_json::to_string_pretty(value)
        .map_err(|e| Error::internal("encode JSON").with_source(e))?;
    stdout(&format!("{text}\n"))
}
