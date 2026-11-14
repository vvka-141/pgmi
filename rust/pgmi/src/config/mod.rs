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

/// The project `.env`'s pairs that the environment does not set, read up
/// to `PGMI_MAX_FILE_SIZE`.
/// What the project `.env` set, and the keys in it pgmi refused.
#[derive(Debug, Default, PartialEq, Eq)]
pub(crate) struct Dotenv {
    pub(crate) applied: Vec<(String, String)>,
    pub(crate) refused: Vec<String>,
}

/// Keys a checked-out project may not set: they locate credentials,
/// endpoints, proxies or trust roots, which only the operator decides.
/// The behaviour contract lists the same keys.
const REFUSED_EXACT: [&str; 21] = [
    "AWS_CONFIG_FILE",
    "AWS_SHARED_CREDENTIALS_FILE",
    "AWS_WEB_IDENTITY_TOKEN_FILE",
    "AWS_PROFILE",
    "AZURE_AUTHORITY_HOST",
    "AZURE_FEDERATED_TOKEN_FILE",
    "AZURE_CONFIG_DIR",
    "GOOGLE_APPLICATION_CREDENTIALS",
    "GCE_METADATA_HOST",
    "GCE_METADATA_IP",
    "DATABRICKS_CONFIG_FILE",
    "DATABRICKS_CONFIG_PROFILE",
    "DATABRICKS_HOST",
    "IMDS_ENDPOINT",
    "PGPASSFILE",
    "PGSERVICEFILE",
    "PGSYSCONFDIR",
    "PGSSLROOTCERT",
    "PGSSLCERT",
    "PGSSLKEY",
    "SSL_CERT_DIR",
];
const REFUSED_PREFIXES: [&str; 7] = [
    "AWS_CONTAINER_",
    "AWS_ENDPOINT_URL",
    "AWS_EC2_METADATA_",
    "IDENTITY_",
    "MSI_",
    "CLOUDSDK_",
    "SSL_CERT_",
];

pub(crate) fn refused_in_dotenv(key: &str) -> bool {
    let upper = key.to_ascii_uppercase();
    REFUSED_EXACT.contains(&upper.as_str())
        || REFUSED_PREFIXES.iter().any(|p| upper.starts_with(p))
        || upper.ends_with("_PROXY")
}

pub(crate) fn project_dotenv(project: &impl Vfs, env: &Env) -> Result<Dotenv> {
    let limit = crate::project::walk::max_size(env)?;
    let read = project.read_bounded(".env", limit).and_then(|bytes| {
        if bytes.len() as u64 > limit {
            return Err(std::io::Error::other(format!(
                "exceeds {limit}-byte limit (override via PGMI_MAX_FILE_SIZE)"
            )));
        }
        String::from_utf8(bytes).map_err(|_| {
            std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                "stream did not contain valid UTF-8",
            )
        })
    });
    let text = match read {
        Ok(t) => t,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Dotenv::default()),
        Err(e) if e.kind() == std::io::ErrorKind::NotADirectory => return Ok(Dotenv::default()),
        Err(e) => {
            return Err(
                Error::config(format!("failed to read {}", project.display(".env"))).with_source(e),
            );
        }
    };
    let Ok(pairs) = dotenv::environment(&text) else {
        let _ = writeln!(
            std::io::stderr(),
            "WARNING: {} could not be parsed, so none of its values were applied.\nEvery line must be KEY=VALUE, a # comment, or blank.",
            project.display(".env")
        );
        return Ok(Dotenv::default());
    };
    let mut out: Vec<(String, String)> = Vec::new();
    let mut refused: Vec<String> = Vec::new();
    for (k, v) in pairs {
        if env.contains(&k) {
            continue;
        }
        if refused_in_dotenv(&k) {
            if !refused.contains(&k) {
                refused.push(k);
            }
            continue;
        }
        if let Some(slot) = out.iter_mut().find(|(n, _)| *n == k) {
            slot.1 = v;
        } else {
            out.push((k, v));
        }
    }
    out.sort();
    refused.sort();
    Ok(Dotenv {
        applied: out,
        refused,
    })
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
            project_dotenv(&fs, &env).unwrap().applied,
            [("A".into(), "x".into()), ("B".into(), "2".into())]
        );
    }

    #[test]
    fn a_project_env_cannot_point_credentials_endpoints_proxies_or_trust_roots_elsewhere() {
        let fs = MemFs::default().file(
            ".env",
            "AWS_CONFIG_FILE=./aws\nAWS_ACCESS_KEY_ID=AKID\nhttps_proxy=http://evil\nPGHOST=db\n\
             SSL_CERT_FILE=./ca.pem\nAWS_ENDPOINT_URL_STS=http://evil\nPGMI_PARAM_X=1\n\
             IDENTITY_ENDPOINT=http://evil\nCLOUDSDK_API_ENDPOINT_OVERRIDES_SQLADMIN=http://evil\n",
        );
        let d = project_dotenv(&fs, &Env::default()).unwrap();
        assert_eq!(
            d.applied,
            [
                ("AWS_ACCESS_KEY_ID".into(), "AKID".into()),
                ("PGHOST".into(), "db".into()),
                ("PGMI_PARAM_X".into(), "1".into()),
            ]
        );
        assert_eq!(
            d.refused,
            [
                "AWS_CONFIG_FILE",
                "AWS_ENDPOINT_URL_STS",
                "CLOUDSDK_API_ENDPOINT_OVERRIDES_SQLADMIN",
                "IDENTITY_ENDPOINT",
                "SSL_CERT_FILE",
                "https_proxy",
            ]
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
        assert!(
            project_dotenv(&MemFs::default(), &env)
                .unwrap()
                .applied
                .is_empty()
        );
        let fs = MemFs::default().file(".env", "A=1\nnot a pair\n");
        assert!(project_dotenv(&fs, &env).unwrap().applied.is_empty());
    }

    #[test]
    fn a_nul_byte_anywhere_in_the_env_file_applies_nothing() {
        for text in ["A=1\nPGPASSWORD=abc\0def\n", "A=1\nB\0C=2\n", "A=1\n# \0\n"] {
            let fs = MemFs::default().file(".env", text);
            assert!(
                project_dotenv(&fs, &Env::default())
                    .unwrap()
                    .applied
                    .is_empty(),
                "{text:?}"
            );
        }
    }

    #[test]
    fn a_dotenv_over_the_file_size_limit_is_invalid_config() {
        let fs = MemFs::default().file(".env", "PGHOST=x\n");
        let env = Env::from_pairs(&[("PGMI_MAX_FILE_SIZE", "8")]);
        let e = project_dotenv(&fs, &env).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        let path = std::path::Path::new("/mem").join(".env");
        assert_eq!(
            e.text(),
            format!(
                "failed to read {}: exceeds 8-byte limit (override via PGMI_MAX_FILE_SIZE)",
                path.display()
            )
        );
        let env = Env::from_pairs(&[("PGMI_MAX_FILE_SIZE", "9")]);
        assert_eq!(
            project_dotenv(&fs, &env).unwrap().applied,
            [("PGHOST".into(), "x".into())]
        );
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
