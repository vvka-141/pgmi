pub(crate) mod dotenv;
pub(crate) mod edit;
pub(crate) mod params;
pub(crate) mod yaml;

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

/// The only keys a checked-out project's `.env` may set: deploy parameters,
/// the connection, the AWS region and keys, and three settings of pgmi's own
/// that reach no other program. Anything else could steer a process pgmi
/// starts (`BASH_ENV`, `LD_PRELOAD`, `PATH`) or point the operator's
/// credentials or proxies elsewhere, which only the operator decides. The
/// behaviour contract lists the same keys.
const ALLOWED_EXACT: [&str; 37] = [
    "PGMI_CONNECTION_STRING",
    "DATABASE_URL",
    "PGHOST",
    "PGHOSTADDR",
    "PGPORT",
    "PGDATABASE",
    "PGUSER",
    "PGPASSWORD",
    "PGPASSFILE",
    "PGSSLMODE",
    "PGSSLROOTCERT",
    "PGSSLCERT",
    "PGSSLKEY",
    "PGSSLPASSWORD",
    "PGSSLNEGOTIATION",
    "PGSSLSNI",
    "PGSSLCERTMODE",
    "PGMINPROTOCOLVERSION",
    "PGMAXPROTOCOLVERSION",
    "PGAPPNAME",
    "PGCONNECT_TIMEOUT",
    "PGOPTIONS",
    "PGTARGETSESSIONATTRS",
    "PGCHANNELBINDING",
    "PGREQUIREAUTH",
    "PGCLIENTENCODING",
    "PGTZ",
    "AWS_REGION",
    "AWS_DEFAULT_REGION",
    "AWS_ACCESS_KEY_ID",
    "AWS_SECRET_ACCESS_KEY",
    "AWS_SESSION_TOKEN",
    "AWS_MAX_ATTEMPTS",
    "AWS_RETRY_MODE",
    "PGMI_MAX_FILE_SIZE",
    "PGMI_NO_BANNER",
    "NO_COLOR",
];
const ALLOWED_PREFIX: &str = "PGMI_PARAM_";
const SERVICE: [&str; 2] = ["PGSERVICE", "PGSERVICEFILE"];

fn refused_in_dotenv(key: &str) -> bool {
    let upper = key.to_ascii_uppercase();
    !(upper.starts_with(ALLOWED_PREFIX) || ALLOWED_EXACT.contains(&upper.as_str()))
}

fn names_a_service(key: &str) -> bool {
    SERVICE.contains(&key.to_ascii_uppercase().as_str())
}

/// Why the deploy stops, for each kind of refused key, and what to do.
fn refusal(path: &str, keys: &[String]) -> String {
    let mut text = format!(
        "{path} sets {}, which a project .env may not set",
        keys.join(", ")
    );
    if keys.iter().any(|k| names_a_service(k)) {
        text.push_str(
            "\nPGSERVICE and PGSERVICEFILE name a connection service file, and pgmi reads none. Name the server with PGHOST, PGPORT, PGDATABASE and PGUSER, or with PGMI_CONNECTION_STRING.",
        );
    }
    if keys.iter().any(|k| !names_a_service(k)) {
        text.push_str(
            "\nA project .env may set only deploy parameters (PGMI_PARAM_*), the connection (PGMI_CONNECTION_STRING, DATABASE_URL and the PG* variables pgmi supports), the AWS region and keys, PGMI_MAX_FILE_SIZE, PGMI_NO_BANNER and NO_COLOR. pgmi passes its environment to the programs it starts for cloud sign-in, so any other key could change which credentials they use or what they run.\nA key that pgmi or a cloud sign-in needs: set it in the environment pgmi runs in. A key of an application: keep it in a file of another name, such as .env.app.",
        );
    }
    text
}

/// The project `.env`'s pairs that the environment does not set, read up
/// to `PGMI_MAX_FILE_SIZE`. A key the file may not set is an error, whether
/// or not the environment sets it too.
pub(crate) fn project_dotenv(project: &impl Vfs, env: &Env) -> Result<Vec<(String, String)>> {
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
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) if e.kind() == std::io::ErrorKind::NotADirectory => return Ok(Vec::new()),
        Err(e) => {
            return Err(
                Error::config(format!("failed to read {}", project.display(".env"))).with_source(e),
            );
        }
    };
    let pairs = dotenv::environment(&text).map_err(|e| {
        Error::config(format!(
            "{} could not be parsed at line {}; without its values the deploy could reach another server or run with defaults\nEvery line must be KEY=VALUE, a # comment, or blank.",
            project.display(".env"),
            e.line
        ))
    })?;
    let mut out: Vec<(String, String)> = Vec::new();
    let mut refused: Vec<String> = Vec::new();
    for (k, v) in pairs {
        if refused_in_dotenv(&k) {
            if !refused.contains(&k) {
                refused.push(k);
            }
            continue;
        }
        if env.contains(&k) {
            continue;
        }
        if let Some(slot) = out.iter_mut().find(|(n, _)| *n == k) {
            slot.1 = v;
        } else {
            out.push((k, v));
        }
    }
    if !refused.is_empty() {
        refused.sort();
        return Err(Error::config(refusal(&project.display(".env"), &refused)));
    }
    out.sort();
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vfs::MemFs;

    #[test]
    fn the_release_notes_state_the_dotenv_rule_the_binary_enforces() {
        let notes = std::fs::read_to_string(
            std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../RELEASES.md"),
        )
        .unwrap();
        let rule = notes
            .lines()
            .find(|l| l.contains("outside a short allow list"))
            .expect("RELEASES.md states the .env allow list");
        let (allowed, refused) = rule
            .split_once("Everything else is refused")
            .expect("the rule names what is refused");
        let named = |text: &str| -> Vec<String> {
            text.split('`')
                .skip(1)
                .step_by(2)
                .filter(|k| {
                    k.chars()
                        .all(|c| c.is_ascii_uppercase() || c == '_' || c == '*')
                })
                .map(str::to_string)
                .collect()
        };
        for key in named(allowed).into_iter().filter(|k| k != "PG*") {
            assert!(
                !refused_in_dotenv(&key.replace('*', "X")),
                "RELEASES.md calls {key} allowed"
            );
        }
        for key in ALLOWED_EXACT.iter().filter(|k| !k.starts_with("PG")) {
            assert!(allowed.contains(&format!("`{key}`")), "{key} is not named");
        }
        let refused = named(refused);
        assert!(refused.len() >= 4, "{refused:?}");
        for key in refused {
            assert!(refused_in_dotenv(&key), "RELEASES.md calls {key} refused");
        }
        for key in ["PGSSLROOTCERT", "PGPASSFILE"] {
            assert!(!refused_in_dotenv(key));
            assert!(
                notes
                    .lines()
                    .all(|l| !l.contains(key) || !l.contains("trust roots")),
                "RELEASES.md says a .env may not name {key}"
            );
        }
        assert!(notes.contains("a project `.env` that does not parse"));
    }

    #[test]
    fn project_dotenv_fills_only_unset_keys_last_value_wins() {
        let fs = MemFs::default().file(
            ".env",
            "PGMI_PARAM_B=1\nPGHOST=from-file\nPGMI_PARAM_A=x\nPGMI_PARAM_B=2\n",
        );
        let env = Env::from_pairs(&[("PGHOST", "")]);
        assert_eq!(
            project_dotenv(&fs, &env).unwrap(),
            [
                ("PGMI_PARAM_A".into(), "x".into()),
                ("PGMI_PARAM_B".into(), "2".into())
            ]
        );
    }

    #[track_caller]
    fn refused(text: &str, env: &Env) -> String {
        let e = project_dotenv(&MemFs::default().file(".env", text), env)
            .expect_err("the .env must be refused");
        assert_eq!(e.exit_code(), 10, "{}", e.text());
        e.text()
    }

    #[track_caller]
    fn names(text: &str, keys: &[&str]) {
        let mut keys = keys.to_vec();
        keys.sort_unstable();
        let path = std::path::Path::new("/mem").join(".env");
        let first = format!(
            "{} sets {}, which a project .env may not set\n",
            path.display(),
            keys.join(", ")
        );
        assert!(text.starts_with(&first), "want {first:?}, got:\n{text}");
    }

    const WHAT_TO_DO: &str = "A key that pgmi or a cloud sign-in needs: set it in the environment pgmi runs in. A key of an application: keep it in a file of another name, such as .env.app.";
    const SERVICE_FILES: &str = "pgmi reads none. Name the server with PGHOST, PGPORT, PGDATABASE and PGUSER, or with PGMI_CONNECTION_STRING.";

    #[test]
    fn a_project_env_cannot_point_credentials_endpoints_or_proxies_elsewhere() {
        const KEYS: [&str; 14] = [
            "AWS_CONFIG_FILE",
            "AWS_PROFILE",
            "AWS_ENDPOINT_URL_STS",
            "https_proxy",
            "SSL_CERT_FILE",
            "IDENTITY_ENDPOINT",
            "CLOUDSDK_API_ENDPOINT_OVERRIDES_SQLADMIN",
            "AZURE_TENANT_ID",
            "AZURE_CLIENT_ID",
            "AZURE_CLIENT_SECRET",
            "DATABRICKS_TOKEN",
            "PGSSLCRL",
            "PGSYSCONFDIR",
            "PGMI_PAGER",
        ];
        for key in KEYS {
            let text = refused(
                &format!("PGHOST=db\n{key}=./elsewhere\nPGMI_PARAM_X=1\n"),
                &Env::default(),
            );
            names(&text, &[key]);
            assert!(text.ends_with(WHAT_TO_DO), "{key}:\n{text}");
            assert!(!text.contains(SERVICE_FILES), "{key}:\n{text}");
        }
        let all = KEYS.join("=x\n") + "=x\n";
        names(&refused(&all, &Env::default()), &KEYS);
    }

    #[test]
    fn a_project_env_cannot_move_the_home_directory_the_credential_files_default_from() {
        const KEYS: [&str; 10] = [
            "HOME",
            "USERPROFILE",
            "HOMEDRIVE",
            "HOMEPATH",
            "APPDATA",
            "LOCALAPPDATA",
            "XDG_CONFIG_HOME",
            "GOOGLE_EXTERNAL_ACCOUNT_ALLOW_EXECUTABLES",
            "AWS_LOGIN_CACHE_DIRECTORY",
            "AZURE_CLIENT_CERTIFICATE_PATH",
        ];
        for lower in [false, true] {
            for key in KEYS {
                let key = if lower {
                    key.to_lowercase()
                } else {
                    key.to_string()
                };
                let text = refused(
                    &format!("{key}=./planted\nPGMI_PARAM_X=1\n"),
                    &Env::default(),
                );
                names(&text, &[&key]);
            }
        }
    }

    #[test]
    fn a_project_env_cannot_set_what_makes_a_helper_process_run_project_code() {
        for key in [
            "BASH_ENV",
            "ENV",
            "LD_PRELOAD",
            "LD_LIBRARY_PATH",
            "DYLD_INSERT_LIBRARIES",
            "PYTHONPATH",
            "PYTHONSTARTUP",
            "NODE_OPTIONS",
            "PERL5OPT",
            "RUBYOPT",
            "PATH",
            "TMPDIR",
            "XDG_DATA_HOME",
            "XDG_CACHE_HOME",
            "SOMETHING_NEW",
        ] {
            let text = refused(
                &format!("PGMI_CONNECTION_STRING=postgresql://h/d\n{key}=./evil\n"),
                &Env::default(),
            );
            names(&text, &[key]);
        }
    }

    #[test]
    fn a_refused_key_stops_the_deploy_even_when_the_environment_sets_it() {
        let env = Env::from_pairs(&[("PATH", "/usr/bin"), ("NODE_ENV", "production")]);
        names(&refused("PATH=./evil\n", &env), &["PATH"]);
        names(&refused("NODE_ENV=test\n", &env), &["NODE_ENV"]);
    }

    #[test]
    fn a_service_key_in_a_project_env_is_refused_with_what_to_set_instead() {
        for key in ["PGSERVICE", "pgservice", "PGSERVICEFILE"] {
            for env in [
                Env::default(),
                Env::from_pairs(&[("PGHOST", "operator-db")]),
            ] {
                let text = refused(&format!("{key}=prod\n"), &env);
                names(&text, &[key]);
                assert!(text.ends_with(SERVICE_FILES), "{key}:\n{text}");
                assert!(!text.contains("application"), "{key}:\n{text}");
            }
        }
        let text = refused("PGSERVICE=prod\nNODE_ENV=test\n", &Env::default());
        names(&text, &["NODE_ENV", "PGSERVICE"]);
        assert!(text.contains(SERVICE_FILES), "{text}");
        assert!(text.ends_with(WHAT_TO_DO), "{text}");
    }

    #[test]
    fn an_application_key_gets_a_reason_that_is_true_for_it() {
        let text = refused(
            "NODE_ENV=production\nPORT=3000\nNODE_ENV=test\nPGMI_PARAM_X=1\n",
            &Env::default(),
        );
        names(&text, &["NODE_ENV", "PORT"]);
        assert!(text.ends_with(WHAT_TO_DO), "{text}");
        for wrong in ["credentials, endpoints, proxies or trust roots", "locate"] {
            assert!(!text.contains(wrong), "{wrong:?} in:\n{text}");
        }
        for value in ["production", "3000"] {
            assert!(!text.contains(value), "the value {value:?} in:\n{text}");
        }
    }

    #[test]
    fn a_project_env_may_set_the_connection_files_and_pgmis_own_harmless_settings() {
        const KEYS: [&str; 7] = [
            "NO_COLOR",
            "PGMI_MAX_FILE_SIZE",
            "PGMI_NO_BANNER",
            "PGPASSFILE",
            "PGSSLCERT",
            "PGSSLKEY",
            "PGSSLROOTCERT",
        ];
        let text = KEYS.join("=1\n") + "=1\n";
        let applied =
            project_dotenv(&MemFs::default().file(".env", &text), &Env::default()).unwrap();
        assert_eq!(
            applied.iter().map(|(k, _)| k.as_str()).collect::<Vec<_>>(),
            KEYS
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
    fn an_absent_project_dotenv_applies_nothing() {
        assert!(
            project_dotenv(&MemFs::default(), &Env::default())
                .unwrap()
                .is_empty()
        );
    }

    #[test]
    fn a_project_dotenv_that_does_not_parse_stops_the_deploy_naming_the_line_and_no_text() {
        let path = std::path::Path::new("/mem").join(".env");
        for (text, line) in [
            ("PGHOST=db\nnot a pair\n", 2),
            (
                "PGSERVICE=prod\nNODE_ENV=x\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n",
                3,
            ),
            ("=x\n", 1),
            ("PGHOST=db\nPGPASSWORD=abc\0def\n", 2),
            ("PGHOST=db\nB\0C=2\n", 2),
            ("PGHOST=db\n\n# \0\n", 3),
        ] {
            let got = refused(text, &Env::default());
            assert_eq!(
                got,
                format!(
                    "{} could not be parsed at line {line}; without its values the deploy could reach another server or run with defaults\nEvery line must be KEY=VALUE, a # comment, or blank.",
                    path.display()
                ),
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
            project_dotenv(&fs, &env).unwrap(),
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
