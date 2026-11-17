use std::fmt::Write as _;

use super::parse::{Blank, KNOWN};
use super::resolve::*;
use crate::config::yaml::Connection;
use crate::env::Env;
use crate::error::Error;

const VALUES: [&str; 4] = ["", " ", "\t", " \u{3000}\n"];

const OPAQUE: [&str; 2] = ["password", "sslpassword"];

const SETTINGS: [(&str, &str, &str); 4] = [
    ("host", "Host", "h"),
    ("port", "Port", "6000"),
    ("user", "Username", "u"),
    ("dbname", "Database", "d"),
];

const ADO_NAMES: &[(&str, &[&str])] = &[
    (
        "host",
        &["Host", "Server", "Data Source", "Address", "Addr"],
    ),
    ("port", &["Port"]),
    ("user", &["User Id", "Username", "User", "UID"]),
    ("password", &["Password", "Pwd"]),
    ("dbname", &["Database", "Initial Catalog", "DB"]),
    ("sslmode", &["SSL Mode", "SslMode"]),
    ("application_name", &["Application Name"]),
    ("connect_timeout", &["Timeout", "Connect Timeout"]),
    ("sslrootcert", &["Root Certificate"]),
    ("sslcert", &["SSL Certificate"]),
    ("sslkey", &["SSL Key"]),
    ("sslpassword", &["SSL Password"]),
    ("options", &["Options"]),
    ("target_session_attrs", &["Target Session Attributes"]),
    ("channel_binding", &["Channel Binding"]),
];

fn how(value: &str) -> Blank {
    if value.is_empty() {
        Blank::Empty
    } else {
        Blank::Whitespace
    }
}

fn conn(s: &str) -> Flags {
    Flags {
        connection: Some(s.into()),
        ..Flags::default()
    }
}

fn to_database() -> Flags {
    Flags {
        database: Some("d".into()),
        ..Flags::default()
    }
}

fn yaml() -> Connection {
    Connection {
        host: Some("yamlhost".into()),
        port: Some("5434".into()),
        username: Some("yamluser".into()),
        database: Some("yamldb".into()),
        sslmode: Some("require".into()),
        ..Connection::default()
    }
}

fn percent(value: &str) -> String {
    value.bytes().fold(String::new(), |mut out, b| {
        let _ = write!(out, "%{b:02X}");
        out
    })
}

fn keyword_without(key: &str) -> String {
    SETTINGS
        .iter()
        .filter(|(k, ..)| *k != key)
        .map(|(k, _, v)| format!("{k}={v}"))
        .collect::<Vec<_>>()
        .join(" ")
}

fn ado_without(key: &str) -> String {
    SETTINGS
        .iter()
        .filter(|(k, ..)| *k != key)
        .map(|(_, name, v)| format!("{name}={v}"))
        .collect::<Vec<_>>()
        .join(";")
}

struct Form {
    name: String,
    text: String,
    how: Blank,
    /// What a password written this way is.
    kept: String,
}

fn forms(key: &str, value: &str) -> Vec<Form> {
    let form = |name: &str, text: String| Form {
        name: name.to_string(),
        text,
        how: how(value),
        kept: value.to_string(),
    };
    let mut out = vec![
        form(
            "keyword, quoted",
            format!("{} {key}='{value}'", keyword_without(key)),
        ),
        form(
            "URI query",
            format!("postgresql://u@h:6000/d?{key}={}", percent(value)),
        ),
    ];
    if value.is_empty() {
        out.push(form(
            "keyword, bare",
            format!("{} {key}=", keyword_without(key)),
        ));
    }
    let slot = match key {
        "host" => Some(format!("postgresql://u@{}:6000/d", percent(value))),
        "port" => Some(format!("postgresql://u@h:{value}/d")),
        "user" => Some(format!("postgresql://{}@h:6000/d", percent(value))),
        "password" => Some(format!("postgresql://u:{}@h:6000/d", percent(value))),
        "dbname" if !value.is_empty() => Some(format!("postgresql://u@h:6000/{}", percent(value))),
        _ => None,
    };
    out.extend(slot.map(|text| form("URI, in place", text)));
    let aliases = ADO_NAMES
        .iter()
        .find(|(k, _)| *k == key)
        .map_or(&[][..], |(_, names)| names);
    for name in aliases.iter().copied().chain([key]) {
        let rest = ado_without(key);
        out.push(Form {
            name: format!("ADO.NET {name}, bare"),
            text: format!("{rest};{name}={value};"),
            how: Blank::Empty,
            kept: String::new(),
        });
        out.push(Form {
            name: format!("ADO.NET {name}, bare, last"),
            text: format!("{rest};{name}={value}"),
            how: Blank::Empty,
            kept: String::new(),
        });
        for quote in ['\'', '"'] {
            out.push(form(
                &format!("ADO.NET {name}, quoted {quote}"),
                format!("{rest};{name}={quote}{value}{quote}"),
            ));
        }
    }
    out
}

fn outcome(r: &Result<Spec, ResolveError>) -> String {
    match r {
        Ok(s) => format!("resolved to {}", s.describe()),
        Err(e) => format!("refused: {e}"),
    }
}

fn is_blank(
    r: &Result<Spec, ResolveError>,
    key: &str,
    origin: &str,
    how: Blank,
) -> Result<(), String> {
    match r {
        Err(ResolveError::Blank {
            key: k,
            origin: o,
            how: h,
        }) if *k == key && *o == origin && *h == how => Ok(()),
        other => Err(format!(
            "want the {key} from {origin} refused as {}, got {}",
            how.as_str(),
            outcome(other)
        )),
    }
}

#[track_caller]
fn none(failures: &[String]) {
    assert!(
        failures.is_empty(),
        "{} failures:\n{}",
        failures.len(),
        failures.join("\n")
    );
}

#[test]
fn every_key_written_empty_or_blank_in_a_connection_string_is_refused_in_every_form() {
    let mut failures = Vec::new();
    let mut cases = 0;
    for key in KNOWN.iter().copied().filter(|k| !OPAQUE.contains(k)) {
        for value in VALUES {
            for form in forms(key, value) {
                cases += 1;
                let r = resolve(&conn(&form.text), &Env::default(), None);
                if let Err(e) = is_blank(&r, key, "--connection", form.how) {
                    failures.push(format!("{key} ({}) {:?}: {e}", form.name, form.text));
                }
            }
        }
    }
    assert!(cases > 1000, "{cases}");
    none(&failures);
}

#[test]
fn a_password_is_kept_as_written_in_every_form() {
    let mut failures = Vec::new();
    for key in OPAQUE {
        for value in VALUES {
            for form in forms(key, value) {
                let got = resolve(&conn(&form.text), &Env::default(), None).map(|s| {
                    let secret = if key == "password" {
                        s.password
                    } else {
                        s.sslpassword
                    };
                    secret.map(|p| p.expose().to_string())
                });
                if !matches!(&got, Ok(Some(p)) if *p == form.kept) {
                    failures.push(format!(
                        "{key} ({}) {:?}: want {:?} kept, got {:?}",
                        form.name,
                        form.text,
                        form.kept,
                        got.map_err(|e| e.to_string())
                    ));
                }
            }
        }
    }
    none(&failures);
}

fn variables() -> Vec<(&'static str, &'static str)> {
    ENV_DEFAULTS
        .iter()
        .copied()
        .chain([("PGDATABASE", "dbname")])
        .collect()
}

fn environment(name: &str, value: &str) -> Env {
    let mut pairs = vec![
        ("PGHOST", "h"),
        ("PGPORT", "6000"),
        ("PGUSER", "u"),
        ("PGDATABASE", "d"),
    ];
    pairs.retain(|(n, _)| *n != name);
    pairs.push((name, value));
    Env::from_pairs(&pairs)
}

#[test]
fn every_connection_variable_set_empty_or_blank_is_refused_where_it_would_apply() {
    let mut failures = Vec::new();
    for (name, key) in variables() {
        for value in VALUES {
            let env = environment(name, value);
            let beside_a_string = conn("fallback_application_name=x");
            for (context, flags, yaml) in [
                ("alone", Flags::default(), None),
                ("above pgmi.yaml", Flags::default(), Some(yaml())),
                ("under a connection string", beside_a_string, None),
            ] {
                let r = resolve(&flags, &env, yaml.as_ref());
                let verdict = if OPAQUE.contains(&key) {
                    r.as_ref()
                        .map(|_| ())
                        .map_err(|e| format!("want it accepted, got refused: {e}"))
                } else {
                    is_blank(&r, key, name, how(value))
                };
                if let Err(e) = verdict {
                    failures.push(format!("{name}={value:?} {context}: {e}"));
                }
            }
        }
    }
    none(&failures);
}

#[test]
fn a_blank_variable_that_a_nearer_source_replaces_is_not_read() {
    for value in VALUES {
        for (name, text) in [
            ("PGHOST", "host=h2 dbname=d"),
            ("PGPORT", "host=h port=1 dbname=d"),
            ("PGUSER", "host=h user=u2 dbname=d"),
            ("PGSSLMODE", "host=h dbname=d sslmode=require"),
            ("PGDATABASE", "host=h dbname=d2"),
            ("PGSSLROOTCERT", "host=h dbname=d sslrootcert=/r.crt"),
            ("PGMI_CONNECTION_STRING", "host=h dbname=d"),
            ("DATABASE_URL", "host=h dbname=d"),
        ] {
            let r = resolve(&conn(text), &environment(name, value), None);
            assert!(
                r.is_ok(),
                "{name}={value:?} under {text:?}: {}",
                outcome(&r)
            );
        }
        let host_flag = Flags {
            host: Some("h2".into()),
            ..to_database()
        };
        let r = resolve(&host_flag, &environment("PGHOST", value), None);
        assert!(r.is_ok(), "PGHOST={value:?} under -h: {}", outcome(&r));
        let r = resolve(&to_database(), &environment("PGDATABASE", value), None);
        assert!(r.is_ok(), "PGDATABASE={value:?} under -d: {}", outcome(&r));
        let env = Env::from_pairs(&[
            ("PGMI_CONNECTION_STRING", "host=h dbname=d"),
            ("DATABASE_URL", value),
        ]);
        let r = resolve(&Flags::default(), &env, None);
        assert!(
            r.is_ok(),
            "DATABASE_URL={value:?} under PGMI_CONNECTION_STRING: {}",
            outcome(&r)
        );
    }
}

#[test]
fn a_connection_string_variable_set_empty_or_blank_is_refused() {
    let mut failures = Vec::new();
    for name in ["PGMI_CONNECTION_STRING", "DATABASE_URL"] {
        for value in VALUES {
            let env = Env::from_pairs(&[(name, value), ("PGHOST", "h"), ("PGDATABASE", "d")]);
            for flags in [Flags::default(), to_database()] {
                let r = resolve(&flags, &env, None);
                if let Err(e) = is_blank(&r, "connection string", name, how(value)) {
                    failures.push(format!("{name}={value:?}: {e}"));
                }
            }
        }
    }
    none(&failures);
}

#[test]
fn every_flag_given_empty_or_blank_is_refused() {
    type Set = fn(&mut Flags, String);
    let flags: [(&str, &str, Set); 6] = [
        ("-h", "host", |f, v| f.host = Some(v)),
        ("-U", "user", |f, v| f.username = Some(v)),
        ("-d", "dbname", |f, v| f.database = Some(v)),
        ("--sslcert", "sslcert", |f, v| f.sslcert = Some(v)),
        ("--sslkey", "sslkey", |f, v| f.sslkey = Some(v)),
        ("--sslrootcert", "sslrootcert", |f, v| {
            f.sslrootcert = Some(v);
        }),
    ];
    let mut failures = Vec::new();
    for (flag, key, set) in flags {
        for value in VALUES {
            let mut given = to_database();
            set(&mut given, value.to_string());
            for yaml in [None, Some(yaml())] {
                let r = resolve(&given, &environment("PGAPPNAME", "x"), yaml.as_ref());
                if let Err(e) = is_blank(&r, key, flag, how(value)) {
                    failures.push(format!("{flag} {value:?}: {e}"));
                }
            }
        }
    }
    none(&failures);
    let r = resolve(&conn(""), &Env::default(), None);
    assert!(
        matches!(&r, Err(ResolveError::Parse { .. })),
        "{}",
        outcome(&r)
    );
}

#[test]
fn every_pgmi_yaml_value_written_empty_or_blank_is_refused_where_it_would_apply() {
    type Set = fn(&mut Connection, String);
    let fields: [(&str, Set); 9] = [
        ("host", |y, v| y.host = Some(v)),
        ("port", |y, v| y.port = Some(v)),
        ("user", |y, v| y.username = Some(v)),
        ("dbname", |y, v| y.database = Some(v)),
        ("maintenance_database", |y, v| {
            y.maintenance_database = Some(v);
        }),
        ("sslmode", |y, v| y.sslmode = Some(v)),
        ("sslcert", |y, v| y.sslcert = Some(v)),
        ("sslkey", |y, v| y.sslkey = Some(v)),
        ("sslrootcert", |y, v| y.sslrootcert = Some(v)),
    ];
    let mut failures = Vec::new();
    for (key, set) in fields {
        for value in VALUES {
            let mut y = yaml();
            set(&mut y, value.to_string());
            let r = resolve(&Flags::default(), &Env::default(), Some(&y));
            if let Err(e) = is_blank(&r, key, "pgmi.yaml", how(value)) {
                failures.push(format!("{key}: {value:?}: {e}"));
            }
        }
    }
    none(&failures);
    for value in VALUES {
        let y = Connection {
            host: Some(value.into()),
            database: Some(value.into()),
            ..yaml()
        };
        let named = Flags {
            host: Some("h".into()),
            ..to_database()
        };
        let r = resolve(&named, &Env::default(), Some(&y));
        assert!(r.is_ok(), "{value:?} under -h and -d: {}", outcome(&r));
        let r = resolve(&conn("host=h dbname=d"), &Env::default(), Some(&y));
        assert!(r.is_ok(), "{value:?} under a string: {}", outcome(&r));
    }
}

#[test]
fn an_empty_or_blank_entry_in_a_host_or_port_list_is_refused() {
    let mut failures = Vec::new();
    for (key, text, how) in [
        ("host", "host=h1,,h3 dbname=d", Blank::Empty),
        ("host", "host=,h2 dbname=d", Blank::Empty),
        ("host", "host=h1, dbname=d", Blank::Empty),
        ("host", "host=' ,h2' dbname=d", Blank::Whitespace),
        ("host", "host='h1, ,h3' dbname=d", Blank::Whitespace),
        ("host", "postgresql://u@,h2/d", Blank::Empty),
        ("host", "postgresql://u@h1,,h3/d", Blank::Empty),
        ("host", "postgresql://u@h1,/d", Blank::Empty),
        ("host", "postgresql://u@%20,h2/d", Blank::Whitespace),
        ("host", "postgresql://u@h1,%20/d", Blank::Whitespace),
        ("host", "Host=h1,,h3;Database=d", Blank::Empty),
        ("port", "host=h1,h2 port=6000, dbname=d", Blank::Empty),
        ("port", "host=h1,h2 port=,6000 dbname=d", Blank::Empty),
        (
            "port",
            "host=h1,h2 port='6000, ' dbname=d",
            Blank::Whitespace,
        ),
        ("port", "postgresql://u@h1:6000,h2:/d", Blank::Empty),
        ("port", "Host=h1,h2;Port=6000,;Database=d", Blank::Empty),
        ("port", "Host=h:;Database=d", Blank::Empty),
        ("hostaddr", "hostaddr=10.0.0.1, dbname=d", Blank::Empty),
        ("hostaddr", "hostaddr=,10.0.0.1 dbname=d", Blank::Empty),
        (
            "hostaddr",
            "host=h1,h2 hostaddr='10.0.0.1, ' dbname=d",
            Blank::Whitespace,
        ),
        ("hostaddr", "host=h hostaddr='' dbname=d", Blank::Empty),
    ] {
        let r = resolve(&conn(text), &Env::default(), None);
        if let Err(e) = is_blank(&r, key, "--connection", how) {
            failures.push(format!("{text:?}: {e}"));
        }
    }
    for (name, key, value) in [
        ("PGHOST", "host", "h1,,h3"),
        ("PGHOST", "host", ",h2"),
        ("PGPORT", "port", "6000,"),
    ] {
        let r = resolve(&to_database(), &environment(name, value), None);
        if let Err(e) = is_blank(&r, key, name, Blank::Empty) {
            failures.push(format!("{name}={value:?}: {e}"));
        }
    }
    none(&failures);
}

#[test]
fn a_host_the_string_leaves_to_the_name_is_not_a_blank_hostaddr() {
    let s = resolve(
        &conn("host=a,b hostaddr=10.0.0.1, dbname=d"),
        &Env::default(),
        None,
    )
    .unwrap();
    let addrs: Vec<_> = s.hosts.endpoints().map(|e| e.addr.is_some()).collect();
    assert_eq!(addrs, [true, false]);
}

#[test]
fn a_uri_that_writes_a_user_or_a_port_but_no_host_is_refused() {
    let elsewhere = Env::from_pairs(&[("PGHOST", "elsewhere"), ("PGPORT", "6001")]);
    let mut failures = Vec::new();
    for (key, text) in [
        ("host", "postgresql://u@:6000/d"),
        ("host", "postgresql://u:p@:6000/d"),
        ("host", "postgresql://:6000/d"),
        ("host", "postgresql://u@/d"),
        ("host", "postgresql://u@/d?port=6000"),
        ("host", "postgresql://[]:6000/d"),
        ("port", "postgresql://u@h:/d"),
        ("port", "postgresql://u@[::1]:/d"),
        ("user", "postgresql://@h/d"),
        ("user", "postgresql://:p@h/d"),
    ] {
        for env in [Env::default(), elsewhere.clone()] {
            let r = resolve(&conn(text), &env, None);
            if let Err(e) = is_blank(&r, key, "--connection", Blank::Empty) {
                failures.push(format!("{text:?}: {e}"));
            }
        }
    }
    none(&failures);
}

#[test]
fn a_uri_that_writes_no_host_at_all_takes_the_next_source() {
    for text in [
        "postgresql:///d",
        "postgresql://",
        "postgresql:///d?port=6000",
        "postgres:///?dbname=d",
    ] {
        let s = resolve(
            &Flags {
                database: Some("d".into()),
                ..conn(text)
            },
            &Env::from_pairs(&[("PGHOST", "elsewhere")]),
            None,
        )
        .unwrap();
        assert_eq!(s.first().name, "elsewhere", "{text}");
    }
    let socket = resolve(
        &conn("postgresql://u:p@/d?host=/var/run/postgresql"),
        &Env::default(),
        None,
    )
    .unwrap();
    assert_eq!(socket.first().name, "/var/run/postgresql");
    let slash = resolve(&conn("postgresql://u@h/"), &to_database_env(), None).unwrap();
    assert_eq!(slash.target_db, "d");
}

fn to_database_env() -> Env {
    Env::from_pairs(&[("PGDATABASE", "d")])
}

#[test]
fn a_uri_host_without_a_port_beside_one_with_a_port_uses_5432() {
    let s = resolve(
        &conn("postgresql://u@h1:6000,h2/d"),
        &Env::from_pairs(&[("PGPORT", "6001")]),
        None,
    )
    .unwrap();
    let ports: Vec<_> = s.hosts.endpoints().map(|e| e.port).collect();
    assert_eq!(ports, [6000, 5432]);
}

#[test]
fn the_refusal_names_the_setting_and_where_it_was_written() {
    let text = |r: Result<Spec, ResolveError>| {
        let e = Error::from(r.unwrap_err());
        assert_eq!(e.exit_code(), 10, "{}", e.text());
        e.text()
    };
    assert_eq!(
        text(resolve(
            &conn("Host=127.0.0.1;Port= ;Username=postgres;Database=d"),
            &Env::from_pairs(&[("PGPORT", "55961")]),
            None
        )),
        "the port from --connection is empty; write the port, or remove it so the next source applies"
    );
    assert_eq!(
        text(resolve(
            &to_database(),
            &Env::from_pairs(&[("PGHOST", " ")]),
            None
        )),
        "the host from PGHOST is only whitespace; write the host, or remove it so the next source applies"
    );
    let env = Env::from_pairs(&[("DATABASE_URL", "host=h sslmode='' dbname=d")]);
    assert_eq!(
        text(resolve(&Flags::default(), &env, None)),
        "the sslmode from DATABASE_URL is empty; write the sslmode, or remove it so the next source applies"
    );
}
