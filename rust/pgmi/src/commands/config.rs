use std::path::{Path, PathBuf};

use super::stderr;
use crate::cli::ConfigArgs;
use crate::config::edit;
use crate::config::yaml::ProjectConfig;
use crate::env::Env;
use crate::error::{Error, Kind, Result, Secret};
use crate::tui::connection::{self, Done, Settings};

pub(crate) fn run(a: &ConfigArgs, env: &Env, runtime: &tokio::runtime::Handle) -> Result<()> {
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
    let settings = match connection::run(initial, env, runtime)? {
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
    if s.password.is_none() {
        return;
    }
    stderr("\n");
    if !crate::tui::confirm("Save password to .pgpass for future sessions?") {
        stderr("Tip: provide password via $PGPASSWORD, .pgpass, or connection string.\n");
        return;
    }
    match save_pgpass(s, env) {
        Ok(path) => stderr(&format!("Saved to {}\n", path.display())),
        Err(e) => stderr(&format!(
            "WARNING: failed to save .pgpass: {}\nTip: provide password via $PGPASSWORD or connection string.\n",
            e.text()
        )),
    }
}

fn save_pgpass(s: &Settings, env: &Env) -> Result<PathBuf> {
    let path = env
        .non_empty("PGPASSFILE")
        .ok()
        .flatten()
        .map(PathBuf::from)
        .or_else(|| env.pg_files().map(|f| f.pgpass))
        .ok_or_else(|| {
            Error::general("cannot determine where the password file goes; set PGPASSFILE")
        })?;
    let database = s.database.as_deref().unwrap_or_default();
    let mut dbs = vec![database];
    if s.maintenance != database {
        dbs.push(s.maintenance.as_str());
    }
    crate::conn::pgpass::save(
        &path,
        s.host.as_deref().unwrap_or_default(),
        s.port.unwrap_or_default(),
        &dbs,
        s.username.as_deref().unwrap_or_default(),
        s.password.as_ref().map(Secret::expose).unwrap_or_default(),
    )?;
    Ok(path)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testdir::TempDir;

    fn settings(database: &str, maintenance: &str) -> Settings {
        Settings {
            host: Some("127.0.0.1".into()),
            port: Some(55601),
            database: Some(database.into()),
            maintenance: maintenance.into(),
            username: Some("postgres".into()),
            password: Some("trial".into()),
            ..Settings::default()
        }
    }

    #[test]
    fn pgpass_gets_the_target_and_the_maintenance_database_once() {
        let dir = TempDir::new("pgpass-offer");
        let file = dir.join("pgpass.conf");
        let env = Env::from_pairs(&[("PGPASSFILE", file.to_str().unwrap())]);
        for _ in 0..2 {
            assert_eq!(
                save_pgpass(&settings("appdb", "postgres"), &env).unwrap(),
                file
            );
            assert_eq!(
                std::fs::read_to_string(&file).unwrap(),
                "127.0.0.1:55601:appdb:postgres:trial\n127.0.0.1:55601:postgres:postgres:trial\n"
            );
        }
        save_pgpass(&settings("postgres", "postgres"), &env).unwrap();
        assert_eq!(
            std::fs::read_to_string(&file).unwrap(),
            "127.0.0.1:55601:postgres:postgres:trial\n127.0.0.1:55601:appdb:postgres:trial\n"
        );
    }

    #[test]
    fn pgpass_location_follows_pgpassfile_then_the_platform() {
        let dir = TempDir::new("pgpass-where");
        let home = dir.path().to_str().unwrap();
        let env = Env::from_pairs(&[("HOME", home), ("APPDATA", home)]);
        let saved = save_pgpass(&settings("d", "d"), &env).unwrap();
        assert_eq!(saved, env.pg_files().unwrap().pgpass);
        assert!(saved.is_file());
        let custom = dir.join("sub").join("custom");
        let env = Env::from_pairs(&[
            ("HOME", home),
            ("APPDATA", home),
            ("PGPASSFILE", custom.to_str().unwrap()),
        ]);
        assert_eq!(save_pgpass(&settings("d", "d"), &env).unwrap(), custom);
        let e = save_pgpass(&settings("d", "d"), &Env::from_pairs(&[])).unwrap_err();
        assert_eq!(
            e.text(),
            "cannot determine where the password file goes; set PGPASSFILE"
        );
    }
}
