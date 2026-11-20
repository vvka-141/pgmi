use std::collections::BTreeMap;

use serde::{Deserialize, Deserializer};

use crate::conn::cloud::Provider;
use crate::error::{Error, Result};
use crate::vfs::Vfs;

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct ProjectConfig {
    #[serde(default, deserialize_with = "null_is_empty")]
    pub(crate) connection: Connection,
    #[serde(default, deserialize_with = "null_is_empty")]
    pub(crate) params: BTreeMap<String, Option<String>>,
    pub(crate) timeout: Option<String>,
    pub(crate) retry: Option<bool>,
    pub(crate) retry_after_commit: Option<bool>,
}

#[derive(Debug, Default, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Connection {
    #[serde(default, deserialize_with = "null_is_blank")]
    pub(crate) host: Option<String>,
    #[serde(default, deserialize_with = "null_is_blank")]
    pub(crate) port: Option<String>,
    #[serde(default, deserialize_with = "null_is_blank")]
    pub(crate) username: Option<String>,
    #[serde(default, deserialize_with = "null_is_blank")]
    pub(crate) database: Option<String>,
    #[serde(default, deserialize_with = "null_is_blank")]
    pub(crate) maintenance_database: Option<String>,
    #[serde(default, deserialize_with = "null_is_blank")]
    pub(crate) sslmode: Option<String>,
    /// The line of `sslmode:` in the block form, for error messages.
    #[serde(skip)]
    pub(crate) sslmode_line: Option<usize>,
    #[serde(default, deserialize_with = "null_is_blank")]
    pub(crate) sslcert: Option<String>,
    #[serde(default, deserialize_with = "null_is_blank")]
    pub(crate) sslkey: Option<String>,
    #[serde(default, deserialize_with = "null_is_blank")]
    pub(crate) sslrootcert: Option<String>,
    /// `auth_method` as written; `parse` turns it into `auth_method`.
    #[serde(rename = "auth_method", default, deserialize_with = "null_is_blank")]
    pub(crate) auth_method_text: Option<String>,
    #[serde(skip)]
    pub(crate) auth_method: Option<Provider>,
    pub(crate) azure_tenant_id: Option<String>,
    pub(crate) azure_client_id: Option<String>,
    pub(crate) aws_region: Option<String>,
    pub(crate) google_instance: Option<String>,
}

fn null_is_empty<'de, D, T>(d: D) -> std::result::Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

fn null_is_blank<'de, D: Deserializer<'de>>(d: D) -> std::result::Result<Option<String>, D::Error> {
    Ok(Some(Option::<String>::deserialize(d)?.unwrap_or_default()))
}

/// A port as Go's YAML reads an integer: `5432.0`, `0x1538` and `5_432`
/// are 5432; anything else is kept as written for the resolver to judge.
fn yaml_port(written: &str) -> std::result::Result<String, YamlError> {
    if written.is_empty() {
        return Err(YamlError::Port(written.to_string()));
    }
    let digits = written.replace('_', "");
    let read = match digits.strip_prefix("0x") {
        Some(hex) => u16::from_str_radix(hex, 16).ok(),
        None => digits
            .strip_suffix(".0")
            .unwrap_or(&digits)
            .parse::<u16>()
            .ok(),
    };
    Ok(read.map_or_else(|| written.to_string(), |p| p.to_string()))
}

/// Why `pgmi.yaml`'s text is not a project configuration.
#[derive(Debug)]
pub(crate) enum YamlError {
    Syntax(serde_saphyr::Error),
    /// A value the parser refused, reworded to name its key and the fix.
    Key {
        message: String,
        error: Box<serde_saphyr::Error>,
    },
    AuthMethod(String),
    Port(String),
}

impl std::fmt::Display for YamlError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            YamlError::Syntax(e) => {
                let text = e.to_string();
                let first = text.lines().next().unwrap_or_default();
                f.write_str(
                    &first
                        .strip_prefix("error: ")
                        .unwrap_or(first)
                        .replace(", set DuplicateKeyPolicy in Options if acceptable", "")
                        .replace(
                            "; use from_str_multiple or from_str_multiple_with_options",
                            "",
                        ),
                )
            }
            YamlError::Key { message, .. } => f.write_str(message),
            YamlError::Port(text) => write!(
                f,
                "connection.port: invalid port {text:?}: expected a number"
            ),
            YamlError::AuthMethod(text) => {
                write!(
                    f,
                    "auth_method {text:?} is not one of azure, aws, google (omit it for password authentication)"
                )
            }
        }
    }
}

impl std::error::Error for YamlError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            YamlError::Syntax(e) => e.source(),
            YamlError::Key { error, .. } => error.source(),
            YamlError::AuthMethod(_) | YamlError::Port(_) => None,
        }
    }
}

impl From<YamlError> for Error {
    fn from(e: YamlError) -> Self {
        Error::config("failed to load pgmi.yaml").with_source(e)
    }
}

fn auth_method(text: &str) -> std::result::Result<Provider, YamlError> {
    [Provider::Azure, Provider::Aws, Provider::Google]
        .into_iter()
        .find(|p| p.key() == text)
        .ok_or_else(|| YamlError::AuthMethod(text.to_string()))
}

fn original(e: &serde_saphyr::Error) -> &serde_saphyr::Error {
    match e.without_snippet() {
        serde_saphyr::Error::AliasError { error, .. } => original(error),
        other => other,
    }
}

/// The top-level key whose block holds `line`, and the key written on
/// `line` itself when it is nested under it.
fn keys_at(text: &str, line: usize) -> Option<(&str, Option<&str>)> {
    fn key(l: &str) -> Option<&str> {
        let body = l.trim_start().trim_start_matches('\u{feff}');
        if body.is_empty() || body.starts_with('#') {
            return None;
        }
        body.split_once(':').map(|(k, _)| k.trim())
    }
    let lines: Vec<&str> = text.lines().collect();
    let here = *lines.get(line.checked_sub(1)?)?;
    let top = lines[..line]
        .iter()
        .rposition(|l| !l.starts_with([' ', '\t']) && key(l).is_some())?;
    let nested = (top + 1 != line).then(|| key(here)).flatten();
    Some((key(lines[top])?, nested))
}

fn reworded(error: serde_saphyr::Error, text: &str) -> YamlError {
    use serde_saphyr::Error as E;
    use serde_saphyr::budget::BudgetBreach;
    let inner = original(&error);
    if let E::Budget {
        breach: BudgetBreach::AliasAnchorRatio { aliases, anchors },
        ..
    } = inner
    {
        let noun = if *anchors == 1 { "anchor" } else { "anchors" };
        let message = format!(
            "{aliases} aliases (*name) of {anchors} {noun} (&name), more than pgmi reads (at most 10 per anchor from 100 aliases on); write the repeated values out"
        );
        return YamlError::Key {
            message,
            error: Box::new(error),
        };
    }
    if matches!(
        inner,
        E::DuplicateMappingKey { .. }
            | E::SerdeUnknownField { .. }
            | E::MultipleDocuments { .. }
            | E::Budget { .. }
    ) {
        return YamlError::Syntax(error);
    }
    let Some(at) = inner.location() else {
        return YamlError::Syntax(error);
    };
    let Some((top, nested)) = usize::try_from(at.line())
        .ok()
        .and_then(|line| keys_at(text, line))
    else {
        return YamlError::Syntax(error);
    };
    let null_name = top == "params" && matches!(inner, E::NullIntoString { .. });
    let fix = match (top, nested) {
        ("retry" | "retry_after_commit", _) => "want true or false".to_string(),
        ("timeout", _) => "want a duration such as 30s or 5m".to_string(),
        ("params", _) if null_name => {
            "a parameter name is empty or ~; name the parameter".to_string()
        }
        ("connection", None) => {
            "want keys under it, one per line, such as \"host: db.internal\"".to_string()
        }
        ("params", None) => "want name: value pairs under it, one per line".to_string(),
        _ => {
            let said = inner.to_string();
            let first = said.lines().next().unwrap_or_default();
            let first = first.strip_prefix("error: ").unwrap_or(first);
            first
                .strip_prefix("line ")
                .and_then(|rest| rest.split_once(": "))
                .map_or(first, |(_, what)| what)
                .to_string()
        }
    };
    let name = match nested.filter(|_| !null_name) {
        Some(k) => format!("{top}.{k}"),
        None => top.to_string(),
    };
    let message = format!("{name}: {fix} (line {} column {})", at.line(), at.column());
    YamlError::Key {
        message,
        error: Box::new(error),
    }
}

fn sslmode_line(text: &str) -> Option<usize> {
    let mut in_connection = false;
    for (n, line) in text.lines().enumerate() {
        let body = line.trim_start();
        if body.is_empty() || body.starts_with('#') {
            continue;
        }
        if body.len() == line.len() {
            in_connection = line.starts_with("connection:");
        } else if in_connection && body.starts_with("sslmode:") {
            return Some(n + 1);
        }
    }
    None
}

/// The WARNING for a file beside `deploy.sql` that looks meant as
/// `pgmi.yaml` (`pgmi.yml`, `PGMI.yaml`) when `pgmi.yaml` itself is absent.
pub(crate) fn misnamed(project: &impl Vfs) -> Option<String> {
    if project.stat("pgmi.yaml").is_ok() {
        return None;
    }
    let entries = project.read_dir("").ok()?;
    let name = entries
        .iter()
        .map(|e| e.name.as_str())
        .find(|n| n.eq_ignore_ascii_case("pgmi.yaml") || n.eq_ignore_ascii_case("pgmi.yml"))?;
    Some(format!(
        "WARNING: {name} is not read; pgmi reads only pgmi.yaml, so rename the file to use it"
    ))
}

impl ProjectConfig {
    pub(crate) fn load(project: &impl Vfs) -> Result<Option<Self>> {
        crate::vfs::no_symlink(project, "pgmi.yaml")?;
        let text = match project.read_to_string("pgmi.yaml") {
            Ok(t) => t,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(e) if e.kind() == std::io::ErrorKind::IsADirectory => {
                return Err(Error::config(format!(
                    "failed to load pgmi.yaml: {} is a directory; replace it with a file",
                    project.display("pgmi.yaml")
                )));
            }
            Err(e) => {
                return Err(Error::config(format!(
                    "failed to load pgmi.yaml: read {}",
                    project.display("pgmi.yaml")
                ))
                .with_source(e));
            }
        };
        Self::parse(&text).map(Some).map_err(|e| match e {
            YamlError::Syntax(_) | YamlError::Key { .. } => Error::config(format!(
                "failed to load pgmi.yaml: parse {}",
                project.display("pgmi.yaml")
            ))
            .with_source(e),
            other => other.into(),
        })
    }

    pub(crate) fn parse(text: &str) -> std::result::Result<Self, YamlError> {
        if text.trim().is_empty() {
            return Ok(Self::default());
        }
        let Some(mut cfg) =
            serde_saphyr::from_str::<Option<ProjectConfig>>(text).map_err(|e| reworded(e, text))?
        else {
            return Ok(Self::default());
        };
        cfg.timeout = cfg.timeout.filter(|t| !t.is_empty());
        let c = &mut cfg.connection;
        c.port = c.port.as_deref().map(yaml_port).transpose()?;
        c.auth_method = c
            .auth_method_text
            .take()
            .as_deref()
            .map(auth_method)
            .transpose()?;
        c.sslmode_line = c.sslmode.as_ref().and_then(|_| sslmode_line(text));
        Ok(cfg)
    }

    pub(crate) fn timeout(&self) -> Result<Option<std::time::Duration>> {
        self.timeout
            .as_deref()
            .map(|t| {
                crate::duration::parse(t).map_err(|e| {
                    let go = if e.ends_with("must not be negative") {
                        ""
                    } else {
                        "time: "
                    };
                    Error::config(format!("invalid timeout in pgmi.yaml: {go}{e}"))
                })
            })
            .transpose()
    }

    pub(crate) fn param_values(&self) -> Vec<(String, String)> {
        self.params
            .iter()
            .map(|(k, v)| (k.clone(), v.clone().unwrap_or_default()))
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn null_documents_and_null_sections_mean_no_settings() {
        for text in [
            "---\n",
            "~\n",
            "connection:\n  # host: x\nparams:\n  # a: b\ntimeout:\n",
            "timeout: \"\"\n",
        ] {
            let cfg = ProjectConfig::parse(text).unwrap_or_else(|e| panic!("{text:?}: {e}"));
            assert!(
                cfg.params.is_empty() && cfg.connection.host.is_none() && cfg.timeout.is_none(),
                "{text:?}"
            );
        }
    }

    #[test]
    fn a_duplicate_key_names_its_line_without_the_library_s_advice() {
        let e = Error::from(
            ProjectConfig::parse("params:\n  greeting: one\n  greeting: two\n").unwrap_err(),
        );
        assert!(
            e.text().contains("greeting") && e.text().contains("line 3"),
            "{}",
            e.text()
        );
        assert!(!e.text().contains("DuplicateKeyPolicy"), "{}", e.text());
    }

    #[test]
    fn ports_go_reads_as_integers_are_read_the_same_and_an_empty_port_is_refused() {
        for (written, read) in [
            ("5432.0", "5432"),
            ("0x1538", "5432"),
            ("5_432", "5432"),
            ("\"5433\"", "5433"),
        ] {
            let cfg = ProjectConfig::parse(&format!("connection:\n  port: {written}\n")).unwrap();
            assert_eq!(cfg.connection.port.as_deref(), Some(read), "{written}");
        }
        let e = Error::from(ProjectConfig::parse("connection:\n  port: \"\"\n").unwrap_err());
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("port"), "{}", e.text());
    }

    #[test]
    fn a_connection_key_written_null_is_blank_and_a_missing_key_unset() {
        type Get = fn(&Connection) -> &Option<String>;
        let fields: [(&str, Get); 9] = [
            ("host", |c| &c.host),
            ("port", |c| &c.port),
            ("username", |c| &c.username),
            ("database", |c| &c.database),
            ("maintenance_database", |c| &c.maintenance_database),
            ("sslmode", |c| &c.sslmode),
            ("sslcert", |c| &c.sslcert),
            ("sslkey", |c| &c.sslkey),
            ("sslrootcert", |c| &c.sslrootcert),
        ];
        for (key, get) in fields {
            for null in ["", " ~", " null"] {
                let text = format!("connection:\n  {key}:{null}\n");
                if key == "port" {
                    let e = Error::from(ProjectConfig::parse(&text).unwrap_err());
                    assert_eq!(e.exit_code(), 10, "{text:?}");
                    assert!(
                        e.text().contains("pgmi.yaml") && e.text().contains("port"),
                        "{text:?}: {}",
                        e.text()
                    );
                    continue;
                }
                let cfg = ProjectConfig::parse(&text).unwrap_or_else(|e| panic!("{text:?}: {e}"));
                assert_eq!(get(&cfg.connection).as_deref(), Some(""), "{text:?}");
            }
            let cfg = ProjectConfig::parse("connection:\n  aws_region: r\n").unwrap();
            assert_eq!(get(&cfg.connection), &None, "{key} missing");
        }
        let e = Error::from(ProjectConfig::parse("connection:\n  auth_method:\n").unwrap_err());
        assert_eq!(
            e.text(),
            "failed to load pgmi.yaml: auth_method \"\" is not one of azure, aws, google (omit it for password authentication)"
        );
    }

    #[test]
    fn a_timeout_without_a_unit_says_so() {
        let cfg = ProjectConfig::parse("timeout: 30\n").unwrap();
        let e = cfg.timeout().unwrap_err();
        assert!(
            e.text().contains("missing unit") && e.text().contains("\"30\""),
            "{}",
            e.text()
        );
    }

    #[test]
    fn param_values_as_written() {
        let cfg = ProjectConfig::parse(
            "params:\n  nothing: ~\n  empty: \"\"\n  integer: 42\n  float: 5432.0\n  boolean: true\n  quoted: \"007\"\n  text: plain text\n  folded: >\n    folded\n    text\n  literal: |\n    line one\n    line two\n",
        )
        .unwrap();
        let v: BTreeMap<_, _> = cfg.param_values().into_iter().collect();
        assert_eq!(v["nothing"], "");
        assert_eq!(v["empty"], "");
        assert_eq!(v["integer"], "42");
        assert_eq!(v["float"], "5432.0");
        assert_eq!(v["boolean"], "true");
        assert_eq!(v["quoted"], "007");
        assert_eq!(v["folded"], "folded text\n");
        assert_eq!(v["literal"], "line one\nline two\n");
    }

    #[test]
    fn unknown_key_refused() {
        let e = ProjectConfig::parse("connection:\n  usernmae: someone\n").unwrap_err();
        assert!(matches!(e, YamlError::Syntax(_)), "{e:?}");
        let reason = e.to_string();
        let e = Error::from(e);
        assert!(e.text().contains("usernmae"), "{}", e.text());
        assert_eq!(e.text(), format!("failed to load pgmi.yaml: {reason}"));
        assert_eq!(e.exit_code(), 10);
        let source = std::error::Error::source(&e).unwrap();
        assert!(
            matches!(
                source.downcast_ref::<YamlError>(),
                Some(YamlError::Syntax(_))
            ),
            "{source:?}"
        );
    }

    #[test]
    fn an_unknown_auth_method_names_the_choices_and_password_sign_in() {
        let e = Error::from(ProjectConfig::parse("connection:\n  auth_method: gcp\n").unwrap_err());
        assert_eq!(
            e.text(),
            "failed to load pgmi.yaml: auth_method \"gcp\" is not one of azure, aws, google (omit it for password authentication)"
        );
    }

    #[test]
    fn load_reads_pgmi_yaml_from_the_project() {
        use crate::vfs::MemFs;
        assert!(ProjectConfig::load(&MemFs::default()).unwrap().is_none());
        let empty = ProjectConfig::load(&MemFs::default().file("pgmi.yaml", "  \n"))
            .unwrap()
            .unwrap();
        assert!(empty.connection.host.is_none() && empty.params.is_empty());
        let empty = ProjectConfig::load(&MemFs::default().file("pgmi.yaml", ""))
            .unwrap()
            .unwrap();
        assert!(
            empty.connection.host.is_none() && empty.params.is_empty() && empty.timeout.is_none()
        );
        let cfg = ProjectConfig::load(&MemFs::default().file("pgmi.yaml", "timeout: 90s\n"))
            .unwrap()
            .unwrap();
        assert_eq!(
            cfg.timeout().unwrap(),
            Some(std::time::Duration::from_secs(90))
        );
        for (fs, text) in [
            (
                MemFs::default().file("pgmi.yaml", "connection: [unclosed\n"),
                "failed to load pgmi.yaml: ",
            ),
            (
                MemFs::default().file("pgmi.yaml", "{{invalid"),
                "failed to load pgmi.yaml: ",
            ),
            (
                MemFs::default().file("pgmi.yaml", "connection:\n  auth_method: kerberos\n"),
                "failed to load pgmi.yaml: auth_method \"kerberos\" is not one of azure, aws, google (omit it for password authentication)",
            ),
            (
                MemFs::default().unreadable_file("pgmi.yaml"),
                "failed to load pgmi.yaml: ",
            ),
            (
                MemFs::default().file("pgmi.yaml", b"timeout: \xff\n"),
                "failed to load pgmi.yaml: read ",
            ),
        ] {
            let e = ProjectConfig::load(&fs).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{}", e.text());
            assert!(e.text().starts_with(text), "{}", e.text());
            if text.contains("auth_method") {
                assert_eq!(e.text(), text);
            }
        }
    }

    #[test]
    fn a_symlinked_pgmi_yaml_is_refused_before_it_is_read() {
        use crate::vfs::MemFs;
        for fs in [
            MemFs::default().link_to_file("pgmi.yaml"),
            MemFs::default().dangling_link("pgmi.yaml"),
        ] {
            let e = ProjectConfig::load(&fs).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{}", e.text());
            assert_eq!(
                e.text(),
                "failed to read ./pgmi.yaml: refusing to read symlink pgmi.yaml: pgmi does not follow symlinks; replace it with the file"
            );
        }
    }

    #[test]
    fn sslmode_line_is_found_in_the_connection_block() {
        let cfg = ProjectConfig::parse(
            "# c\nparams:\n  sslmode: x\nconnection:\n  host: h\n  # sslmode: verify-full\n  sslmode: prefer   # x\n",
        )
        .unwrap();
        assert_eq!(cfg.connection.sslmode_line, Some(7));
        let cfg = ProjectConfig::parse("connection: {sslmode: prefer}\n").unwrap();
        assert_eq!(cfg.connection.sslmode_line, None);
    }

    #[test]
    fn template_file() {
        let cfg = ProjectConfig::parse(
            "connection:\n  database: mydb # x\n  port: 5432\n  sslmode: prefer\ntimeout: 3m\n",
        )
        .unwrap();
        assert_eq!(cfg.connection.port.as_deref(), Some("5432"));
        assert_eq!(cfg.timeout.as_deref(), Some("3m"));
    }

    #[test]
    fn every_field_is_read() {
        let cfg = ProjectConfig::parse(
            "connection:\n  host: myhost\n  port: 5433\n  username: myuser\n  database: mydb\n  sslmode: require\n  sslcert: /path/client.crt\n  sslkey: /path/client.key\n  sslrootcert: /path/ca.crt\n\nparams:\n  env: production\n  region: us-west\n\ntimeout: 10m\n",
        )
        .unwrap();
        let c = &cfg.connection;
        assert_eq!(c.host.as_deref(), Some("myhost"));
        assert_eq!(c.port.as_deref(), Some("5433"));
        assert_eq!(c.username.as_deref(), Some("myuser"));
        assert_eq!(c.database.as_deref(), Some("mydb"));
        assert_eq!(c.sslmode.as_deref(), Some("require"));
        assert_eq!(c.sslcert.as_deref(), Some("/path/client.crt"));
        assert_eq!(c.sslkey.as_deref(), Some("/path/client.key"));
        assert_eq!(c.sslrootcert.as_deref(), Some("/path/ca.crt"));
        assert_eq!(
            cfg.param_values(),
            [
                ("env".to_string(), "production".to_string()),
                ("region".to_string(), "us-west".to_string())
            ]
        );
        assert_eq!(
            cfg.timeout().unwrap(),
            Some(std::time::Duration::from_secs(600))
        );
        let minimal = ProjectConfig::parse("params:\n  env: development\n").unwrap();
        assert!(minimal.connection.host.is_none() && minimal.connection.port.is_none());
        assert_eq!(
            minimal.param_values(),
            [("env".to_string(), "development".to_string())]
        );
    }

    #[test]
    fn a_value_of_the_wrong_kind_names_its_key_and_the_fix() {
        use std::fmt::Write as _;
        let mut aliases = String::from("params:\n  a: &a v\n");
        for i in 0..120 {
            let _ = writeln!(aliases, "  k{i}: *a");
        }
        for (text, want) in [
            ("retry: 0\n", "retry: want true or false (line 1 column 8)"),
            (
                "retry_after_commit: maybe\n",
                "retry_after_commit: want true or false (line 1",
            ),
            (
                "connection: x\n",
                "connection: want keys under it, one per line, such as \"host: db.internal\" (line 1 column 13)",
            ),
            (
                "params: {~: 1}\n",
                "params: a parameter name is empty or ~; name the parameter (line 1",
            ),
            (
                "params:\n  ~: 1\n",
                "params: a parameter name is empty or ~; name the parameter (line 2",
            ),
            (
                "timeout: [1s]\n",
                "timeout: want a duration such as 30s or 5m (line 1 column 10)",
            ),
            ("connection:\n  host: [a, b]\n", "connection.host: "),
            (
                aliases.as_str(),
                "120 aliases (*name) of 1 anchor (&name), more than pgmi reads (at most 10 per anchor from 100 aliases on); write the repeated values out",
            ),
        ] {
            let e = Error::from(ProjectConfig::parse(text).unwrap_err());
            assert_eq!(e.exit_code(), 10, "{text}");
            assert!(
                e.text()
                    .starts_with(&format!("failed to load pgmi.yaml: {want}")),
                "{text:?}: {}",
                e.text()
            );
        }
    }

    #[test]
    fn a_pgmi_yaml_that_is_a_directory_names_the_path_and_the_fix() {
        use crate::vfs::MemFs;
        let e = ProjectConfig::load(&MemFs::default().dir("pgmi.yaml")).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(
            e.text().contains("pgmi.yaml is a directory")
                && e.text().contains("replace it with a file"),
            "{}",
            e.text()
        );
    }

    #[test]
    fn a_misnamed_pgmi_yaml_is_named_once_with_the_file_pgmi_reads() {
        use crate::vfs::MemFs;
        for name in ["pgmi.yml", "PGMI.yaml", "Pgmi.YML"] {
            assert_eq!(
                misnamed(&MemFs::default().file(name, "timeout: 1s\n")).as_deref(),
                Some(
                    format!(
                        "WARNING: {name} is not read; pgmi reads only pgmi.yaml, so rename the file to use it"
                    )
                    .as_str()
                ),
                "{name}"
            );
        }
        let both = MemFs::default().file("pgmi.yml", "").file("PGMI.yaml", "");
        assert_eq!(
            misnamed(&both).map(|w| w.matches("WARNING").count()),
            Some(1)
        );
        for fs in [
            MemFs::default(),
            MemFs::default().file("pgmi.yaml", "").file("pgmi.yml", ""),
            MemFs::default().file("pgmi.yaml.bak", ""),
        ] {
            assert_eq!(misnamed(&fs), None);
        }
    }

    #[test]
    fn retry_keys_read_yaml_1_1_booleans_and_refuse_numbers() {
        for (written, read) in [
            ("true", true),
            ("True", true),
            ("TRUE", true),
            ("yes", true),
            ("Yes", true),
            ("YES", true),
            ("y", true),
            ("Y", true),
            ("on", true),
            ("On", true),
            ("ON", true),
            ("\"true\"", true),
            ("\"yes\"", true),
            ("false", false),
            ("False", false),
            ("no", false),
            ("NO", false),
            ("n", false),
            ("N", false),
            ("off", false),
            ("Off", false),
            ("\"no\"", false),
        ] {
            let cfg = ProjectConfig::parse(&format!(
                "retry: {written}\nretry_after_commit: {written}\n"
            ))
            .unwrap_or_else(|e| panic!("{written}: {e}"));
            assert_eq!(
                (cfg.retry, cfg.retry_after_commit),
                (Some(read), Some(read)),
                "{written}"
            );
        }
        for written in ["0", "1", "\"\"", "tru"] {
            assert!(
                ProjectConfig::parse(&format!("retry: {written}\n")).is_err(),
                "{written}"
            );
        }
    }
}
