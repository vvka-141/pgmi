pub(crate) mod dotenv;
pub(crate) mod edit;
pub(crate) mod params;
pub(crate) mod yaml;

use std::io::Write;

use crate::env::Env;
use crate::error::{Error, Result};
use crate::vfs::Vfs;

pub(crate) fn project_dotenv(project: &impl Vfs, env: &Env) -> Result<Vec<(String, String)>> {
    let text = match project.read_to_string(".env") {
        Ok(t) => t,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(vec![]),
        Err(e) if e.kind() == std::io::ErrorKind::NotADirectory => return Ok(vec![]),
        Err(e) => {
            return Err(
                Error::config(format!("failed to read {}", project.display(".env"))).with_source(e),
            );
        }
    };
    let Ok(pairs) = dotenv::parse(&text, dotenv::Mode::Dotenv) else {
        let _ = writeln!(
            std::io::stderr(),
            "WARNING: {} could not be parsed, so none of its values were applied",
            project.display(".env")
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vfs::MemFs;

    #[test]
    fn project_dotenv_fills_only_unset_keys_last_value_wins() {
        let fs = MemFs::default().file(".env", "B=1\nPGHOST=from-file\nA=x\nB=2\n");
        let env = Env::from_pairs(&[("PGHOST", "")]);
        assert_eq!(
            project_dotenv(&fs, &env).unwrap(),
            [("A".into(), "x".into()), ("B".into(), "2".into())]
        );
    }

    #[test]
    fn project_dotenv_absent_or_unparseable_applies_nothing() {
        let env = Env::default();
        assert!(project_dotenv(&MemFs::default(), &env).unwrap().is_empty());
        let fs = MemFs::default().file(".env", "A=1\nnot a pair\n");
        assert!(project_dotenv(&fs, &env).unwrap().is_empty());
    }

    #[test]
    fn project_dotenv_unreadable_is_invalid_config() {
        let fs = MemFs::default().unreadable_file(".env");
        let e = project_dotenv(&fs, &Env::default()).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(
            e.text().starts_with("failed to read /mem/.env: "),
            "{}",
            e.text()
        );
    }
}
