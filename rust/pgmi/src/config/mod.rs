pub mod dotenv;
pub mod params;
pub mod yaml;

use std::io::Write;
use std::path::Path;

use crate::env::Env;
use crate::error::{Error, Result};

pub fn project_dotenv(project: &Path, env: &Env) -> Result<Vec<(String, String)>> {
    let path = project.join(".env");
    let text = match std::fs::read_to_string(&path) {
        Ok(t) => t,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(vec![]),
        Err(e) if e.kind() == std::io::ErrorKind::NotADirectory => return Ok(vec![]),
        Err(e) => {
            return Err(Error::config(format!("failed to read {}", path.display())).with_source(e));
        }
    };
    let Ok(pairs) = dotenv::parse(&text, dotenv::Mode::Dotenv) else {
        let _ = writeln!(
            std::io::stderr(),
            "WARNING: {} could not be parsed, so none of its values were applied",
            path.display()
        );
        return Ok(vec![]);
    };
    let mut out: Vec<(String, String)> = Vec::new();
    for (k, v) in pairs {
        if env.contains(&k) {
            continue;
        }
        if let Some(slot) = out.iter_mut().find(|(n, _)| *n == k) {
            slot.1 = v;
        } else {
            out.push((k, v));
        }
    }
    out.sort();
    Ok(out)
}
