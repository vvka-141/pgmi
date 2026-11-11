use std::path::{Path, PathBuf};

use super::stderr;
use crate::cli::ConfigArgs;
use crate::config::edit;
use crate::config::yaml::ProjectConfig;
use crate::env::Env;
use crate::error::{Error, Kind, Result};
use crate::tui::connection::{self, Done, Settings};

pub(crate) fn run(a: &ConfigArgs, env: &Env) -> Result<()> {
    if !crate::tui::interactive(env) {
        return Err(Error::usage(
            "config requires an interactive terminal\nfor CI, write pgmi.yaml by hand or use PG* environment variables",
        ));
    }
    let target = a.path.clone().unwrap_or_else(|| PathBuf::from("."));
    let path = target.join("pgmi.yaml");
    let mut initial = None;
    let mut replace = false;
    match std::fs::read_to_string(&path) {
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => return Err(Error::config("failed to load pgmi.yaml").with_source(e)),
        Ok(text) => match ProjectConfig::parse(&text) {
            Ok(cfg) => {
                stderr("pgmi.yaml exists.\n");
                if !crate::tui::confirm("Overwrite connection settings?") {
                    return Err(stopped("config", false));
                }
                initial = Some(cfg.connection);
            }
            Err(e) => {
                let text = e.text();
                let reason = text
                    .strip_prefix("failed to load pgmi.yaml: ")
                    .unwrap_or(&text);
                stderr(&format!(
                    "pgmi.yaml exists but could not be parsed: {reason}\n"
                ));
                if !crate::tui::confirm("Overwrite the unparseable pgmi.yaml?") {
                    return Err(stopped("config", false));
                }
                replace = true;
            }
        },
    }
    let settings = match connection::run(initial, env)? {
        Done::Finished(s) => s,
        Done::Cancelled => return Err(stopped("config", false)),
        Done::Interrupted => return Err(stopped("config", true)),
    };
    save(&target, &settings, replace)?;
    stderr(&format!("Wrote {}\n", path.display()));
    offer_pgpass(&settings, env);
    Ok(())
}

pub(crate) fn stopped(what: &str, interrupted: bool) -> Error {
    if interrupted {
        Error::new(Kind::Interrupted, format!("{what} interrupted"))
    } else {
        Error::new(Kind::Denied, format!("{what} cancelled"))
    }
}

pub(crate) fn save(target: &Path, s: &Settings, replace: bool) -> Result<()> {
    let path = target.join("pgmi.yaml");
    let values = s.yaml();
    let text = match std::fs::read_to_string(&path) {
        Ok(existing) if !replace => edit::set_connection(&existing, &values),
        _ => edit::fresh(&values),
    };
    edit::write_atomic(&path, &text)
        .map_err(|e| Error::general(format!("failed to save config: {}", e.text())))
}

pub(crate) fn offer_pgpass(s: &Settings, env: &Env) {
    if s.password.expose().is_empty() {
        return;
    }
    stderr("\n");
    if !crate::tui::confirm("Save password to .pgpass for future sessions?") {
        stderr("Tip: provide password via $PGPASSWORD, .pgpass, or connection string.\n");
        return;
    }
    let path = env
        .non_empty("PGPASSFILE")
        .ok()
        .flatten()
        .map(PathBuf::from)
        .or_else(|| env.pg_files().map(|f| f.pgpass));
    let Some(path) = path else {
        stderr(
            "WARNING: failed to save .pgpass: cannot determine where the password file goes; set PGPASSFILE\nTip: provide password via $PGPASSWORD or connection string.\n",
        );
        return;
    };
    let mut dbs = vec![s.database.as_str()];
    if s.maintenance != s.database {
        dbs.push(s.maintenance.as_str());
    }
    match crate::conn::pgpass::save(
        &path,
        &s.host,
        s.port,
        &dbs,
        &s.username,
        s.password.expose(),
    ) {
        Ok(()) => stderr(&format!("Saved to {}\n", path.display())),
        Err(e) => stderr(&format!(
            "WARNING: failed to save .pgpass: {}\nTip: provide password via $PGPASSWORD or connection string.\n",
            e.text()
        )),
    }
}
