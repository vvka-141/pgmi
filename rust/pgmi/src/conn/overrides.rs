use std::fmt;

use serde::ser::{Serialize, SerializeStruct, Serializer};

use super::parse::{self, Fields};
use super::resolve::{Flags, ResolveError, connection_string};
use crate::config::yaml::Connection as YamlConnection;
use crate::env::Env;

/// One `pgmi.yaml` connection value another source replaced. `value` is
/// `None` when the yaml value was dropped: a connection string never takes
/// `sslmode` from `pgmi.yaml`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Override {
    field: &'static str,
    from: &'static str,
    value: Option<String>,
    not_from: &'static str,
    not_value: String,
}

impl Serialize for Override {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let mut o = s.serialize_struct("Override", 5)?;
        o.serialize_field("field", self.field)?;
        o.serialize_field("from", self.from)?;
        o.serialize_field("value", self.value.as_deref().unwrap_or_default())?;
        o.serialize_field("notFrom", self.not_from)?;
        o.serialize_field("notValue", &self.not_value)?;
        o.end()
    }
}

impl fmt::Display for Override {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match &self.value {
            None => write!(
                f,
                "{field}: pgmi.yaml ({}) does not apply to the connection string from {}; set {field} in the string",
                self.not_value,
                self.from,
                field = self.field
            ),
            Some(value) => write!(
                f,
                "{}: from {} ({value}), not {} ({})",
                self.field, self.from, self.not_from, self.not_value
            ),
        }
    }
}

/// A value that may replace the `pgmi.yaml` one, unless a flag named it.
struct Candidate<'a> {
    field: &'static str,
    from: &'static str,
    value: Option<String>,
    yaml: Option<&'a str>,
    flagged: bool,
}

impl Candidate<'_> {
    fn into_override(self) -> Option<Override> {
        let value = self.value.filter(|v| !v.is_empty())?;
        let yaml = self.yaml?;
        (!self.flagged && value != yaml).then(|| Override {
            field: self.field,
            from: self.from,
            value: Some(value),
            not_from: "pgmi.yaml",
            not_value: yaml.to_string(),
        })
    }
}

fn given(v: Option<&String>) -> Option<&str> {
    v.map(String::as_str).filter(|v| !v.is_empty())
}

/// Every `pgmi.yaml` connection value that a `PG*` variable or the connection
/// string replaces, so the replacement is never silent. With a connection
/// string `pgmi.yaml` supplies only the database and the certificate paths;
/// its `sslmode` never applies. A string that does not parse has no notes:
/// resolving it reports the error.
pub(crate) fn overrides(
    flags: &Flags,
    env: &Env,
    yaml: Option<&YamlConnection>,
) -> Result<Vec<Override>, ResolveError> {
    let none = YamlConnection::default();
    let y = yaml.unwrap_or(&none);
    let var = |name: &str| -> Result<Option<String>, ResolveError> {
        Ok(env.non_empty(name)?.map(str::to_string))
    };
    let string: Option<(Fields, &'static str)> = match connection_string(flags, env)? {
        Some((text, source)) => match parse::parse(&text) {
            Ok(fields) => Some((fields, source.label())),
            Err(_) => return Ok(Vec::new()),
        },
        None => None,
    };
    let flag_sslmode = flags.sslmode.map(super::resolve::SslMode::as_str);
    let mut notes = Vec::new();
    let mut candidates = Vec::new();

    let (database_from, database) = match &string {
        Some((fields, source)) if given(fields.get("dbname")).is_some() => {
            (*source, given(fields.get("dbname")).map(str::to_string))
        }
        _ => ("PGDATABASE", var("PGDATABASE")?),
    };
    candidates.push(Candidate {
        field: "database",
        from: database_from,
        value: database,
        yaml: given(y.database.as_ref()),
        flagged: given(flags.database.as_ref()).is_some(),
    });

    if let Some((fields, source)) = &string {
        let named = given(fields.get("sslmode"));
        if let (Some(flag), Some(named)) = (flag_sslmode, named)
            && flag != named
        {
            notes.push(Override {
                field: "sslmode",
                from: "--sslmode",
                value: Some(flag.to_string()),
                not_from: source,
                not_value: named.to_string(),
            });
        }
        if let Some(yaml_sslmode) = given(y.sslmode.as_ref())
            && flag_sslmode.is_none()
        {
            let (from, value) = match named {
                Some(n) => (*source, Some(n.to_string())),
                None => ("PGSSLMODE", var("PGSSLMODE")?),
            };
            if value.is_some() {
                candidates.push(Candidate {
                    field: "sslmode",
                    from,
                    value,
                    yaml: Some(yaml_sslmode),
                    flagged: false,
                });
            } else if yaml_sslmode != "prefer" {
                notes.push(Override {
                    field: "sslmode",
                    from: source,
                    value: None,
                    not_from: "pgmi.yaml",
                    not_value: yaml_sslmode.to_string(),
                });
            }
        }
    } else {
        for (field, variable, yaml_value, flagged) in [
            (
                "host",
                "PGHOST",
                &y.host,
                given(flags.host.as_ref()).is_some(),
            ),
            (
                "port",
                "PGPORT",
                &y.port,
                given(flags.port.as_ref()).is_some(),
            ),
            (
                "user",
                "PGUSER",
                &y.username,
                given(flags.username.as_ref()).is_some(),
            ),
            ("sslmode", "PGSSLMODE", &y.sslmode, flag_sslmode.is_some()),
        ] {
            candidates.push(Candidate {
                field,
                from: variable,
                value: var(variable)?,
                yaml: given(yaml_value.as_ref()),
                flagged,
            });
        }
    }

    for (field, variable, yaml_value, flag) in [
        ("sslcert", "PGSSLCERT", &y.sslcert, &flags.sslcert),
        ("sslkey", "PGSSLKEY", &y.sslkey, &flags.sslkey),
        (
            "sslrootcert",
            "PGSSLROOTCERT",
            &y.sslrootcert,
            &flags.sslrootcert,
        ),
    ] {
        let named = string
            .as_ref()
            .and_then(|(fields, source)| Some((*source, given(fields.get(field))?)));
        let (from, value) = match named {
            Some((source, v)) => (source, Some(v.to_string())),
            None => (variable, var(variable)?),
        };
        candidates.push(Candidate {
            field,
            from,
            value,
            yaml: given(yaml_value.as_ref()),
            flagged: given(flag.as_ref()).is_some(),
        });
    }

    notes.extend(candidates.into_iter().filter_map(Candidate::into_override));
    Ok(notes)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::conn::resolve::SslMode;

    fn lines(
        connection: Option<&str>,
        flags: &Flags,
        env: &[(&str, &str)],
        yaml: &YamlConnection,
    ) -> Vec<String> {
        let mut env = env.to_vec();
        if let Some(c) = connection {
            env.push(("DATABASE_URL", c));
        }
        overrides(flags, &Env::from_pairs(&env), Some(yaml))
            .unwrap()
            .iter()
            .map(ToString::to_string)
            .collect()
    }

    fn yaml() -> YamlConnection {
        YamlConnection {
            host: Some("yamlhost".into()),
            port: Some("5434".into()),
            username: Some("yamluser".into()),
            database: Some("myapp".into()),
            ..YamlConnection::default()
        }
    }

    fn tls_yaml() -> YamlConnection {
        YamlConnection {
            host: Some("h".into()),
            database: Some("myapp".into()),
            sslmode: Some("verify-full".into()),
            sslcert: Some("yaml.crt".into()),
            sslkey: Some("yaml.key".into()),
            sslrootcert: Some("yaml-ca.crt".into()),
            ..YamlConnection::default()
        }
    }

    #[track_caller]
    fn check(
        connection: Option<&str>,
        flags: &Flags,
        env: &[(&str, &str)],
        yaml: &YamlConnection,
        want: &[&str],
    ) {
        assert_eq!(lines(connection, flags, env, yaml), want);
    }

    #[test]
    fn env_overrides() {
        let y = yaml();
        let no = Flags::default;
        check(
            None,
            &no(),
            &[("PGDATABASE", "postgres")],
            &y,
            &["database: from PGDATABASE (postgres), not pgmi.yaml (myapp)"],
        );
        check(
            None,
            &no(),
            &[
                ("PGHOST", "envhost"),
                ("PGPORT", "6000"),
                ("PGUSER", "envuser"),
            ],
            &y,
            &[
                "host: from PGHOST (envhost), not pgmi.yaml (yamlhost)",
                "port: from PGPORT (6000), not pgmi.yaml (5434)",
                "user: from PGUSER (envuser), not pgmi.yaml (yamluser)",
            ],
        );
        check(
            None,
            &no(),
            &[("PGHOST", "yamlhost"), ("PGPORT", "5434")],
            &y,
            &[],
        );
        let flagged = Flags {
            database: Some("flagdb".into()),
            host: Some("flaghost".into()),
            ..Flags::default()
        };
        check(
            None,
            &flagged,
            &[("PGDATABASE", "postgres"), ("PGHOST", "envhost")],
            &y,
            &[],
        );
        check(
            Some("postgresql://u@h"),
            &no(),
            &[("PGDATABASE", "postgres"), ("PGHOST", "envhost")],
            &y,
            &["database: from PGDATABASE (postgres), not pgmi.yaml (myapp)"],
        );
        check(
            Some("postgresql://u@h/postgres"),
            &no(),
            &[("PGDATABASE", "other")],
            &y,
            &["database: from DATABASE_URL (postgres), not pgmi.yaml (myapp)"],
        );
        check(Some("postgresql://u@h/myapp"), &no(), &[], &y, &[]);
        let d = Flags {
            database: Some("myapp".into()),
            ..Flags::default()
        };
        check(Some("postgresql://u@h/postgres"), &d, &[], &y, &[]);
    }

    #[test]
    fn env_overrides_tls() {
        let y = tls_yaml();
        let no = Flags::default;
        check(
            None,
            &no(),
            &[("PGSSLMODE", "disable")],
            &y,
            &["sslmode: from PGSSLMODE (disable), not pgmi.yaml (verify-full)"],
        );
        let flag = Flags {
            sslmode: Some(SslMode::Require),
            ..Flags::default()
        };
        check(None, &flag, &[("PGSSLMODE", "disable")], &y, &[]);
        let env = [
            ("PGSSLCERT", "env.crt"),
            ("PGSSLKEY", "env.key"),
            ("PGSSLROOTCERT", "env-ca.crt"),
        ];
        check(
            None,
            &no(),
            &env,
            &y,
            &[
                "sslcert: from PGSSLCERT (env.crt), not pgmi.yaml (yaml.crt)",
                "sslkey: from PGSSLKEY (env.key), not pgmi.yaml (yaml.key)",
                "sslrootcert: from PGSSLROOTCERT (env-ca.crt), not pgmi.yaml (yaml-ca.crt)",
            ],
        );
        let certs = Flags {
            sslcert: Some("f.crt".into()),
            sslkey: Some("f.key".into()),
            sslrootcert: Some("f-ca.crt".into()),
            ..Flags::default()
        };
        check(None, &certs, &env, &y, &[]);
        check(
            Some("postgresql://u@h/myapp"),
            &no(),
            &[],
            &y,
            &[
                "sslmode: pgmi.yaml (verify-full) does not apply to the connection string from DATABASE_URL; set sslmode in the string",
            ],
        );
        check(
            Some("postgresql://u@h/myapp?sslmode=require"),
            &no(),
            &[],
            &y,
            &["sslmode: from DATABASE_URL (require), not pgmi.yaml (verify-full)"],
        );
        check(
            Some("postgresql://u@h/myapp"),
            &no(),
            &[("PGSSLMODE", "disable")],
            &y,
            &["sslmode: from PGSSLMODE (disable), not pgmi.yaml (verify-full)"],
        );
        check(
            Some("postgresql://u@h/myapp?sslmode=verify-full&sslcert=s.crt"),
            &no(),
            &[],
            &y,
            &["sslcert: from DATABASE_URL (s.crt), not pgmi.yaml (yaml.crt)"],
        );
    }

    #[test]
    fn sslmode_flag_over_the_string_comes_first() {
        let flag = Flags {
            sslmode: Some(SslMode::Require),
            ..Flags::default()
        };
        check(
            Some("postgresql://u@h/postgres?sslmode=disable"),
            &flag,
            &[],
            &yaml(),
            &[
                "sslmode: from --sslmode (require), not DATABASE_URL (disable)",
                "database: from DATABASE_URL (postgres), not pgmi.yaml (myapp)",
            ],
        );
    }

    #[test]
    fn default_sslmode_is_no_override() {
        let y = YamlConnection {
            database: Some("myapp".into()),
            sslmode: Some("prefer".into()),
            ..YamlConnection::default()
        };
        let env = Env::from_pairs(&[("PGMI_CONNECTION_STRING", "postgresql://u@h/myapp")]);
        assert_eq!(overrides(&Flags::default(), &env, Some(&y)).unwrap(), []);
    }

    #[test]
    fn string_database_from_every_source() {
        let y = YamlConnection {
            database: Some("mydb".into()),
            ..YamlConnection::default()
        };
        let uri = "postgresql://u:p@127.0.0.1:5480/postgres";
        for (flags, env, source) in [
            (
                Flags {
                    connection: Some(uri.into()),
                    ..Flags::default()
                },
                vec![],
                "--connection",
            ),
            (
                Flags::default(),
                vec![("PGMI_CONNECTION_STRING", uri)],
                "PGMI_CONNECTION_STRING",
            ),
            (
                Flags::default(),
                vec![("DATABASE_URL", uri)],
                "DATABASE_URL",
            ),
        ] {
            let got: Vec<String> = overrides(&flags, &Env::from_pairs(&env), Some(&y))
                .unwrap()
                .iter()
                .map(ToString::to_string)
                .collect();
            assert_eq!(
                got,
                [format!(
                    "database: from {source} (postgres), not pgmi.yaml (mydb)"
                )]
            );
        }
    }

    #[test]
    fn an_unparseable_string_or_no_yaml_has_no_notes() {
        let env = Env::from_pairs(&[("DATABASE_URL", "postgresql://[bad"), ("PGDATABASE", "x")]);
        assert_eq!(
            overrides(&Flags::default(), &env, Some(&yaml())).unwrap(),
            []
        );
        let env = Env::from_pairs(&[("PGDATABASE", "x"), ("PGSSLMODE", "disable")]);
        assert_eq!(overrides(&Flags::default(), &env, None).unwrap(), []);
    }

    #[test]
    fn the_json_shape_matches_go() {
        let dropped = Override {
            field: "sslmode",
            from: "DATABASE_URL",
            value: None,
            not_from: "pgmi.yaml",
            not_value: "verify-full".into(),
        };
        assert_eq!(
            serde_json::to_string(&dropped).unwrap(),
            r#"{"field":"sslmode","from":"DATABASE_URL","value":"","notFrom":"pgmi.yaml","notValue":"verify-full"}"#
        );
    }
}
