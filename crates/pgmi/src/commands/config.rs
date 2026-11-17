use std::path::{Path, PathBuf};

use super::stderr;
use crate::cli::ConfigArgs;
use crate::config::edit;
use crate::config::yaml::{ProjectConfig, YamlError};
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
                    return Err(cancelled("config"));
                }
                initial = Some(cfg.connection);
            }
            Err(e) => {
                stderr(&format!("{}\n", unparseable(e).text()));
                if !crate::tui::confirm("Overwrite the unparseable pgmi.yaml?") {
                    return Err(cancelled("config"));
                }
                replace = true;
            }
        },
    }
    let settings = match connection::run(initial, env, runtime)? {
        Done::Finished(s) => s,
        Done::Cancelled => return Err(cancelled("config")),
        Done::Interrupted => return Err(interrupted("config")),
    };
    save(&target, &settings, replace)?;
    stderr(&format!("Wrote {}\n", path.display()));
    offer_pgpass(&settings, env);
    Ok(())
}

/// A wizard the user left with Esc or "no": exit 12, as Go.
pub(crate) fn cancelled(what: &str) -> Error {
    Error::new(Kind::Denied, format!("{what} cancelled"))
}

/// A wizard left with Ctrl-C: exit 130.
pub(crate) fn interrupted(what: &str) -> Error {
    Error::new(Kind::Interrupted, format!("{what} interrupted"))
}

fn unparseable(e: YamlError) -> Error {
    Error::config("pgmi.yaml exists but could not be parsed").with_source(e)
}

/// Writes the connection settings into `target/pgmi.yaml`, keeping the rest
/// of an existing file. An existing file that cannot be read or does not
/// parse is replaced only when `replace` says the user agreed to it;
/// otherwise it is refused and left as it is.
pub(crate) fn save(target: &Path, s: &Settings, replace: bool) -> Result<()> {
    let path = target.join("pgmi.yaml");
    let values = s.yaml();
    let text = match std::fs::read_to_string(&path) {
        _ if replace => edit::fresh(&values),
        Ok(existing) => {
            ProjectConfig::parse(&existing).map_err(unparseable)?;
            edit::set_connection(&existing, &values)?
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => edit::fresh(&values),
        Err(e) => {
            return Err(Error::config("pgmi.yaml exists but could not be read").with_source(e));
        }
    };
    edit::write_atomic(&path, &text)
        .map_err(|e| Error::general("failed to save config").with_source(e))
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
    fn save_refuses_an_unparseable_pgmi_yaml_unless_replacing_was_confirmed() {
        let dir = TempDir::new("config-unparseable");
        let path = dir.join("pgmi.yaml");
        let broken = "params:\n  env: dev\nconnection: [\n";
        std::fs::write(&path, broken).unwrap();
        let e = save(dir.path(), &settings("appdb", "postgres"), false).unwrap_err();
        assert_eq!(e.kind(), Kind::Config);
        assert!(
            e.text()
                .starts_with("pgmi.yaml exists but could not be parsed: "),
            "{}",
            e.text()
        );
        let reason = ProjectConfig::parse(broken).unwrap_err().to_string();
        assert_eq!(
            e.text(),
            format!("pgmi.yaml exists but could not be parsed: {reason}")
        );
        assert!(
            std::error::Error::source(&e)
                .and_then(|s| s.downcast_ref::<YamlError>())
                .is_some()
        );
        assert_eq!(std::fs::read_to_string(&path).unwrap(), broken);
        save(dir.path(), &settings("appdb", "postgres"), true).unwrap();
        let cfg = ProjectConfig::parse(&std::fs::read_to_string(&path).unwrap()).unwrap();
        assert_eq!(cfg.connection.database.as_deref(), Some("appdb"));
    }

    #[test]
    fn a_failed_save_keeps_the_write_error_as_its_source() {
        let dir = TempDir::new("config-unwritable");
        let missing = dir.join("missing");
        let e = save(&missing, &settings("appdb", "postgres"), false).unwrap_err();
        assert!(
            e.text().starts_with("failed to save config: "),
            "{}",
            e.text()
        );
        let source = std::error::Error::source(&e)
            .and_then(|s| s.downcast_ref::<Error>())
            .expect("the write error is the source");
        assert_eq!(
            e.text(),
            format!("failed to save config: {}", source.text())
        );
    }

    #[test]
    fn save_keeps_a_pgmi_yaml_it_cannot_read() {
        let dir = TempDir::new("config-unreadable");
        let path = dir.join("pgmi.yaml");
        let utf16: &[u8] = b"\xff\xfec\x00o\x00n\x00:\x00\n\x00";
        for bytes in [utf16, b"connection:\n  host: \xe9\n".as_slice()] {
            std::fs::write(&path, bytes).unwrap();
            let e = save(dir.path(), &settings("appdb", "postgres"), false).unwrap_err();
            assert_eq!(e.kind(), Kind::Config);
            assert!(
                e.text()
                    .starts_with("pgmi.yaml exists but could not be read: "),
                "{}",
                e.text()
            );
            assert_eq!(std::fs::read(&path).unwrap(), bytes);
        }
    }

    #[test]
    fn a_wizard_left_is_exit_12_and_an_interrupted_one_130() {
        assert_eq!(cancelled("config").exit_code(), 12);
        assert_eq!(cancelled("config").text(), "config cancelled");
        assert_eq!(interrupted("init").exit_code(), 130);
        assert_eq!(interrupted("init").text(), "init interrupted");
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
