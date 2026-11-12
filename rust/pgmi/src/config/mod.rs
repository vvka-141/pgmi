pub(crate) mod dotenv;
pub(crate) mod edit;
pub(crate) mod params;
pub(crate) mod yaml;

use std::io::Write;
use std::path::{Component, Path, PathBuf};

use crate::env::Env;
use crate::error::{Error, Result};
use crate::vfs::Vfs;

/// The project `.env` as the deploy names it: the project path joined and
/// cleaned, with `./` before a relative path that does not climb out.
pub(crate) fn dotenv_path(project: &Path) -> String {
    let joined = project.join(".env");
    let mut parts: Vec<Component<'_>> = Vec::new();
    for c in joined.components() {
        match c {
            Component::CurDir => {}
            Component::ParentDir => match parts.last() {
                Some(Component::Normal(_)) => {
                    parts.pop();
                }
                Some(Component::RootDir | Component::Prefix(_)) => {}
                _ => parts.push(c),
            },
            _ => parts.push(c),
        }
    }
    let path: PathBuf = parts.iter().collect();
    let text = path.display().to_string();
    if path.is_absolute() || text.starts_with("..") {
        text
    } else {
        format!("./{text}")
    }
}

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
    let Ok(pairs) = dotenv::environment(&text) else {
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

    #[cfg(unix)]
    #[test]
    fn dotenv_path_reads_as_go_names_it() {
        for (project, want) in [
            (".", "./.env"),
            ("app", "./app/.env"),
            ("./app/", "./app/.env"),
            ("a/../b", "./b/.env"),
            ("..", "../.env"),
            ("../app", "../app/.env"),
            ("/srv/app/./x/..", "/srv/app/.env"),
        ] {
            assert_eq!(dotenv_path(Path::new(project)), want, "{project}");
        }
    }

    #[test]
    fn project_dotenv_absent_or_unparseable_applies_nothing() {
        let env = Env::default();
        assert!(project_dotenv(&MemFs::default(), &env).unwrap().is_empty());
        let fs = MemFs::default().file(".env", "A=1\nnot a pair\n");
        assert!(project_dotenv(&fs, &env).unwrap().is_empty());
    }

    #[test]
    fn a_nul_byte_anywhere_in_the_env_file_applies_nothing() {
        for text in ["A=1\nPGPASSWORD=abc\0def\n", "A=1\nB\0C=2\n", "A=1\n# \0\n"] {
            let fs = MemFs::default().file(".env", text);
            assert!(
                project_dotenv(&fs, &Env::default()).unwrap().is_empty(),
                "{text:?}"
            );
        }
    }

    #[test]
    fn project_dotenv_unreadable_is_invalid_config() {
        let fs = MemFs::default().unreadable_file(".env");
        let e = project_dotenv(&fs, &Env::default()).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        let path = std::path::Path::new("/mem").join(".env");
        assert!(
            e.text()
                .starts_with(&format!("failed to read {}: ", path.display())),
            "{}",
            e.text()
        );
    }
}
