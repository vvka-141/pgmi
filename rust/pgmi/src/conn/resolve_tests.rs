use std::time::Duration;

use super::cloud::{self, Provider};
use super::parse::ParseError;
use super::resolve::*;
use crate::config::yaml::Connection;
use crate::env::Env;
use crate::error::{Error, Secret};

fn flags() -> Flags {
    Flags::default()
}

fn conn(s: &str) -> Flags {
    Flags {
        connection: Some(s.into()),
        ..Flags::default()
    }
}

fn hosts(s: &Spec) -> Vec<(String, u16)> {
    s.hosts
        .endpoints()
        .map(|e| (e.name.clone(), e.port))
        .collect()
}

#[track_caller]
fn refused(r: Result<Spec, ResolveError>) -> ResolveError {
    let e = r.unwrap_err();
    assert!(!matches!(e, ResolveError::Env(_)), "{e}");
    e
}

#[track_caller]
fn refused_before_connecting(r: crate::error::Result<Spec>) -> Error {
    let e = r.unwrap_err();
    assert_eq!(e.exit_code(), 10, "{}", e.text());
    e
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

#[test]
fn string_and_target() {
    let env = Env::from_pairs(&[("PGDATABASE", "fromenv")]);
    let mut fl = conn("postgresql://u:p@h:6000/postgres");
    let s = resolve(&fl, &env, None).unwrap();
    assert_eq!(s.target_db, "postgres");
    fl.database = Some("app".into());
    let s = resolve(&fl, &env, None).unwrap();
    assert_eq!(
        (s.target_db.as_str(), s.maintenance_db.as_str()),
        ("app", "postgres")
    );
    assert_eq!(hosts(&s), vec![("h".to_string(), 6000)]);
    assert_eq!(s.password.as_ref().map(Secret::expose), Some("p"));
}

#[test]
fn refusals() {
    let env = Env::from_pairs(&[("DATABASE_URL", "postgresql://h/db")]);
    let mut fl = flags();
    fl.host = Some("x".into());
    let e = refused(resolve(&fl, &env, None));
    assert!(
        matches!(
            e,
            ResolveError::FlagWithEnvString {
                flag: "-h",
                source: Source::DatabaseUrl
            }
        ),
        "{e}"
    );
    assert!(
        e.to_string()
            .contains("cannot combine -h with DATABASE_URL"),
        "{e}"
    );
    let mut fl = conn("postgresql://h/db");
    fl.host = Some("x".into());
    let e = refused(resolve(&fl, &Env::default(), None));
    assert!(matches!(e, ResolveError::ConnectionWith("-h")), "{e}");
    assert!(
        e.to_string()
            .contains("cannot combine --connection with -h")
    );
    let env = Env::from_pairs(&[("PGSERVICE", "x")]);
    let e = refused(resolve(&flags(), &env, None));
    assert!(matches!(e, ResolveError::ServiceEnv), "{e}");
    assert!(e.to_string().contains("PGSERVICE"));
}

#[test]
fn a_refusal_is_invalid_configuration_and_an_environment_error_keeps_its_kind() {
    let e = refused(resolve(&conn("host=h"), &Env::default(), None));
    assert!(matches!(e, ResolveError::NoDatabase), "{e}");
    assert_eq!(Error::from(e).exit_code(), 10);
    let e = refused(resolve(
        &conn("host=h dbname=d bogus=1"),
        &Env::default(),
        None,
    ));
    assert!(
        matches!(
            &e,
            ResolveError::Parse {
                source: Source::ConnectionFlag,
                error: ParseError::UnknownKey(k),
            } if k == "bogus"
        ),
        "{e}"
    );
    assert_eq!(
        std::error::Error::source(&e).map(ToString::to_string),
        Some("invalid connection option \"bogus\"".to_string())
    );
    let env = Error::new(crate::error::Kind::General, "env");
    assert_eq!(Error::from(ResolveError::Env(env)).exit_code(), 1);
}

#[test]
fn errors_redact_the_password() {
    let mut fl = conn("host=h password=Sekrit1 sslmode=Sekrit1");
    fl.database = Some("d".into());
    let e = resolve(&fl, &Env::default(), None).unwrap_err();
    assert!(!e.to_string().contains("Sekrit1"), "{e}");
    fl.connection = Some("host=h".into());
    let env = Env::from_pairs(&[("PGPASSWORD", "Sekrit2"), ("PGSSLMODE", "Sekrit2")]);
    let e = resolve(&fl, &env, None).unwrap_err();
    assert!(!e.to_string().contains("Sekrit2"), "{e}");
}

#[test]
fn tls_keys_are_read_or_refused() {
    let spec = |s: &str| {
        let mut fl = conn(&format!("host=h {s}"));
        fl.database = Some("d".into());
        resolve(&fl, &Env::default(), None)
    };
    let s = spec("").unwrap();
    assert!(s.sslsni && !s.sslcertmode_disable);
    assert_eq!(s.tls_versions.len(), 2);
    let s = spec("ssl_min_protocol_version=TLSv1.3 sslsni=0 sslcertmode=disable").unwrap();
    assert_eq!(s.tls_versions, [&rustls::version::TLS13]);
    assert!(!s.sslsni && s.sslcertmode_disable);
    let s = spec("ssl_max_protocol_version=TLSv1.2").unwrap();
    assert_eq!(s.tls_versions, [&rustls::version::TLS12]);
    for bad in [
        "ssl_max_protocol_version=TLSv1.1",
        "ssl_min_protocol_version=TLSv1.3 ssl_max_protocol_version=TLSv1.2",
        "ssl_min_protocol_version=SSLv3",
        "sslsni=2",
        "sslcertmode=require",
        "sslcertmode=sometimes",
    ] {
        refused(spec(bad));
    }
    assert!(matches!(
        refused(spec("ssl_max_protocol_version=TLSv1.1")),
        ResolveError::TlsRange
    ));
    assert!(matches!(
        refused(spec(
            "ssl_min_protocol_version=TLSv1 ssl_max_protocol_version=TLSv1.1"
        )),
        ResolveError::TlsTooOld
    ));
    assert!(matches!(
        refused(spec(
            "ssl_min_protocol_version=TLSv1.3 ssl_max_protocol_version=TLSv1.2"
        )),
        ResolveError::TlsRange
    ));
    assert!(matches!(
        refused(spec("ssl_min_protocol_version=SSLv3")),
        ResolveError::Value {
            key: "ssl_min_protocol_version",
            ..
        }
    ));
    assert!(matches!(
        refused(spec("sslcertmode=require")),
        ResolveError::CertModeRequire
    ));
}

#[test]
fn a_minimum_protocol_above_3_0_is_refused_until_the_layer_speaks_it() {
    let spec = |s: &str| {
        let mut fl = conn(&format!("host=h {s}"));
        fl.database = Some("d".into());
        resolve(&fl, &Env::default(), None)
    };
    for ok in [
        "",
        "min_protocol_version=3.0",
        "max_protocol_version=3.2",
        "max_protocol_version=latest",
        "min_protocol_version=3.0 max_protocol_version=3.2",
    ] {
        assert!(spec(ok).is_ok(), "{ok}");
    }
    for v in ["3.2", "latest"] {
        let e = refused(spec(&format!("min_protocol_version={v}")));
        assert!(matches!(&e, ResolveError::ProtocolUnsupported(got) if got == v));
        assert!(e.to_string().contains("not supported yet"), "{e}");
    }
    assert!(matches!(
        refused(spec("max_protocol_version=4")),
        ResolveError::Value {
            key: "max_protocol_version",
            ..
        }
    ));
}

#[test]
fn system_roots_mean_verify_full() {
    let mut fl = conn("host=h sslrootcert=system");
    fl.database = Some("d".into());
    let s = resolve(&fl, &Env::default(), None).unwrap();
    assert_eq!(s.sslmode(), SslMode::VerifyFull);
    assert_eq!(s.requested_sslmode, None);
    for weak in ["disable", "allow", "prefer", "require", "verify-ca"] {
        fl.connection = Some(format!("host=h sslrootcert=system sslmode={weak}").into());
        let e = refused(resolve(&fl, &Env::default(), None));
        assert!(matches!(e, ResolveError::WeakForSystemRoots(_)), "{e}");
        assert!(e.to_string().contains("sslrootcert=system"), "{e}");
    }
    let env = Env::from_pairs(&[("PGSSLROOTCERT", "system"), ("PGSSLMODE", "require")]);
    fl.connection = Some("host=h".into());
    refused(resolve(&fl, &env, None));
}

#[test]
fn ports_per_host() {
    let env = Env::from_pairs(&[("PGHOST", "a,b"), ("PGPORT", "1,2"), ("PGDATABASE", "d")]);
    let s = resolve(&flags(), &env, None).unwrap();
    assert_eq!(hosts(&s), vec![("a".into(), 1), ("b".into(), 2)]);
    assert_eq!(s.source, Source::PgHost);
    let env = Env::from_pairs(&[("PGHOST", "a,b,c"), ("PGPORT", "1,2"), ("PGDATABASE", "d")]);
    let e = refused(resolve(&flags(), &env, None));
    assert_eq!(e.to_string(), "could not match 2 port numbers to 3 hosts");
}

#[test]
fn one_port_serves_every_host_and_an_empty_host_is_localhost() {
    let s = resolve(&conn("host=a,,c port=7 dbname=d"), &Env::default(), None).unwrap();
    assert_eq!(
        hosts(&s),
        vec![("a".into(), 7), ("localhost".into(), 7), ("c".into(), 7)]
    );
    assert_eq!(s.first(), ("a", 7));
    assert_eq!(s.describe(), "a:7/d");
}

#[test]
fn settings_parse_to_their_values_and_refuse_others() {
    let spec = |s: &str| {
        resolve(
            &conn(&format!("host=h dbname=d {s}")),
            &Env::default(),
            None,
        )
    };
    let s = spec("").unwrap();
    assert_eq!(
        (s.sslmode(), s.channel_binding, s.target_session_attrs),
        (
            SslMode::Prefer,
            ChannelBinding::Prefer,
            TargetSessionAttrs::Any
        )
    );
    for (text, mode) in [
        ("disable", SslMode::Disable),
        ("allow", SslMode::Allow),
        ("prefer", SslMode::Prefer),
        ("require", SslMode::Require),
        ("verify-ca", SslMode::VerifyCa),
        ("verify-full", SslMode::VerifyFull),
    ] {
        let s = spec(&format!("sslmode={text}")).unwrap();
        assert_eq!((s.requested_sslmode, s.sslmode()), (Some(mode), mode));
        assert_eq!(mode.to_string(), text);
    }
    for (text, cb) in [
        ("disable", ChannelBinding::Disable),
        ("prefer", ChannelBinding::Prefer),
        ("require", ChannelBinding::Require),
    ] {
        assert_eq!(
            spec(&format!("channel_binding={text}"))
                .unwrap()
                .channel_binding,
            cb
        );
        assert_eq!(cb.as_str(), text);
    }
    for (text, tsa) in [
        ("any", TargetSessionAttrs::Any),
        ("read-write", TargetSessionAttrs::ReadWrite),
        ("read-only", TargetSessionAttrs::ReadOnly),
        ("primary", TargetSessionAttrs::Primary),
        ("standby", TargetSessionAttrs::Standby),
        ("prefer-standby", TargetSessionAttrs::PreferStandby),
    ] {
        assert_eq!(
            spec(&format!("target_session_attrs={text}"))
                .unwrap()
                .target_session_attrs,
            tsa
        );
        assert_eq!(tsa.as_str(), text);
    }
    for (bad, want) in [
        ("sslmode=Require", "invalid sslmode value: \"Require\""),
        (
            "channel_binding=on",
            "invalid channel_binding value: \"on\"",
        ),
        (
            "target_session_attrs=master",
            "invalid target_session_attrs value: \"master\"",
        ),
        (
            "sslnegotiation=direct sslmode=prefer",
            "weak sslmode \"prefer\" may not be used with sslnegotiation=direct (use \"require\", \"verify-ca\", or \"verify-full\")",
        ),
    ] {
        assert_eq!(refused(spec(bad)).to_string(), want, "{bad}");
    }
}

#[test]
fn connect_timeout_has_a_floor_of_two_seconds_and_zero_means_none() {
    let spec = |t: &str| {
        resolve(
            &conn(&format!("host=h dbname=d connect_timeout={t}")),
            &Env::default(),
            None,
        )
    };
    assert_eq!(
        spec("1").unwrap().connect_timeout,
        Some(Duration::from_secs(2))
    );
    assert_eq!(
        spec("7").unwrap().connect_timeout,
        Some(Duration::from_secs(7))
    );
    assert_eq!(spec("0").unwrap().connect_timeout, None);
    assert_eq!(spec("-3").unwrap().connect_timeout, None);
    assert_eq!(
        refused(spec("five")).to_string(),
        "invalid connect_timeout \"five\""
    );
}

#[test]
fn only_pg_host_port_or_user_in_the_environment() {
    for (env, host, port, user) in [
        (vec![("PGHOST", "customhost")], "customhost", 5432, None),
        (vec![("PGPORT", "6001")], "localhost", 6001, None),
        (
            vec![("PGUSER", "customuser")],
            "localhost",
            5432,
            Some("customuser"),
        ),
        (
            vec![("PGHOST", "dbserver"), ("PGPORT", "5434")],
            "dbserver",
            5434,
            None,
        ),
    ] {
        let mut pairs = env.clone();
        pairs.push(("PGDATABASE", "d"));
        let s = resolve(&flags(), &Env::from_pairs(&pairs), None).unwrap();
        assert_eq!(s.first(), (host, port), "{env:?}");
        if let Some(user) = user {
            assert_eq!(s.user, user);
        }
        assert_eq!(s.maintenance_db, "postgres");
    }
}

#[test]
fn the_sslmode_flag_beats_pgsslmode_which_beats_the_default() {
    let env = Env::from_pairs(&[("PGSSLMODE", "disable"), ("PGDATABASE", "d")]);
    let mut fl = flags();
    fl.sslmode = Some(SslMode::Require);
    assert_eq!(
        resolve(&fl, &env, None).unwrap().sslmode(),
        SslMode::Require
    );
    let env = Env::from_pairs(&[("PGSSLMODE", "verify-full"), ("PGDATABASE", "d")]);
    assert_eq!(
        resolve(&flags(), &env, None).unwrap().sslmode(),
        SslMode::VerifyFull
    );
    let env = Env::from_pairs(&[("PGDATABASE", "d")]);
    let s = resolve(&flags(), &env, None).unwrap();
    assert_eq!((s.requested_sslmode, s.sslmode()), (None, SslMode::Prefer));
}

#[test]
fn database_url_is_used_unless_a_connection_flag_names_another() {
    let env = Env::from_pairs(&[("DATABASE_URL", "postgresql://user:pass@dbhost:6002/mydb")]);
    let s = resolve(&flags(), &env, None).unwrap();
    assert_eq!(
        (s.first(), s.source),
        (("dbhost", 6002), Source::DatabaseUrl)
    );
    let env = Env::from_pairs(&[(
        "DATABASE_URL",
        "postgresql://user:pass@secondary:6003/backupdb",
    )]);
    let s = resolve(
        &conn("postgresql://user:pass@primary:5432/maindb"),
        &env,
        None,
    )
    .unwrap();
    assert_eq!(
        (s.first(), s.target_db.as_str()),
        (("primary", 5432), "maindb")
    );
    let env = Env::from_pairs(&[
        ("PGMI_CONNECTION_STRING", "postgresql://u@pgmi/a"),
        ("DATABASE_URL", "postgresql://u@url/b"),
    ]);
    let s = resolve(&flags(), &env, None).unwrap();
    assert_eq!(
        (s.first().0, s.source),
        ("pgmi", Source::PgmiConnectionString)
    );
}

#[test]
fn a_string_without_a_database_leaves_postgres_as_the_maintenance_database() {
    let mut fl = conn("postgresql://user:pass@localhost:5432");
    let e = refused(resolve(&fl, &Env::default(), None));
    assert!(matches!(e, ResolveError::NoDatabase), "{e}");
    assert!(
        e.to_string().starts_with("database name is required"),
        "{e}"
    );
    let y = yaml();
    let s = resolve(&fl, &Env::default(), Some(&y)).unwrap();
    assert_eq!(
        (s.target_db.as_str(), s.maintenance_db.as_str()),
        ("yamldb", "postgres")
    );
    fl.database = Some("app".into());
    let s = resolve(&fl, &Env::default(), None).unwrap();
    assert_eq!(
        (s.target_db.as_str(), s.maintenance_db.as_str()),
        ("app", "postgres")
    );
}

#[test]
fn pgport_edge_cases() {
    let port = |p: &str| {
        resolve(
            &flags(),
            &Env::from_pairs(&[("PGPORT", p), ("PGDATABASE", "d")]),
            None,
        )
    };
    assert_eq!(port("6004").unwrap().first().1, 6004);
    assert_eq!(port("").unwrap().first().1, 5432);
    assert_eq!(port(" 5432 ").unwrap().first().1, 5432);
    for bad in ["abc", "-1", "999999", "0", "not-a-number"] {
        assert_eq!(
            refused(port(bad)).to_string(),
            format!("invalid port \"{bad}\"")
        );
    }
}

#[test]
fn the_password_comes_from_pgpassword_when_nothing_else_names_one() {
    let env = Env::from_pairs(&[("PGPASSWORD", "secretpass"), ("PGDATABASE", "d")]);
    let s = resolve(&flags(), &env, None).unwrap();
    assert_eq!(s.password.as_ref().map(Secret::expose), Some("secretpass"));
    let s = resolve(&conn("postgresql://u:own@h/d"), &env, None).unwrap();
    assert_eq!(s.password.as_ref().map(Secret::expose), Some("own"));
}

#[test]
fn the_maintenance_database_is_the_strings_only_under_dash_d() {
    let mut fl = conn("postgresql://user@localhost/mydb");
    fl.database = Some("app".into());
    assert_eq!(
        resolve(&fl, &Env::default(), None).unwrap().maintenance_db,
        "mydb"
    );
    let s = resolve(
        &conn("postgresql://user@localhost/mydb"),
        &Env::default(),
        None,
    )
    .unwrap();
    assert_eq!(
        (s.target_db.as_str(), s.maintenance_db.as_str()),
        ("mydb", "postgres")
    );
    let mut fl = flags();
    fl.host = Some("localhost".into());
    fl.database = Some("app".into());
    assert_eq!(
        resolve(&fl, &Env::default(), None).unwrap().maintenance_db,
        "postgres"
    );
    let y = Connection {
        maintenance_database: Some("ops".into()),
        ..Connection::default()
    };
    assert_eq!(
        resolve(&fl, &Env::default(), Some(&y))
            .unwrap()
            .maintenance_db,
        "ops"
    );
}

#[test]
fn a_string_takes_what_it_omits_from_the_environment() {
    let env = Env::from_pairs(&[
        ("PGHOST", "db.example"),
        ("PGPORT", "6000"),
        ("PGDATABASE", "maint"),
        ("PGUSER", "alice"),
        ("PGAPPNAME", "ci-deploy"),
        ("PGCONNECT_TIMEOUT", "7"),
    ]);
    for s in [
        "sslmode=disable",
        "postgresql:///?sslmode=disable",
        "SSL Mode=Disable;",
    ] {
        let spec = resolve(&conn(s), &env, None).unwrap();
        assert_eq!(spec.first(), ("db.example", 6000), "{s}");
        assert_eq!(spec.target_db, "maint", "{s}");
        assert_eq!(spec.user, "alice", "{s}");
        assert_eq!(spec.application_name, "ci-deploy", "{s}");
        assert_eq!(spec.connect_timeout, Some(Duration::from_secs(7)), "{s}");
        assert_eq!(spec.sslmode(), SslMode::Disable, "{s}");
    }
}

#[test]
fn what_a_string_names_beats_the_environment() {
    let env = Env::from_pairs(&[
        ("PGHOST", "db.example"),
        ("PGPORT", "6000"),
        ("PGDATABASE", "maint"),
        ("PGUSER", "alice"),
        ("PGAPPNAME", "env-app"),
    ]);
    let s = resolve(
        &conn("host=own port=7000 dbname=mine user=bob application_name=own-app"),
        &env,
        None,
    )
    .unwrap();
    assert_eq!(s.first(), ("own", 7000));
    assert_eq!(
        (
            s.target_db.as_str(),
            s.user.as_str(),
            s.application_name.as_str()
        ),
        ("mine", "bob", "own-app")
    );
}

#[test]
fn defaults_without_an_environment() {
    let s = resolve(&conn("sslmode=disable dbname=d"), &Env::default(), None).unwrap();
    assert_eq!(s.first(), ("localhost", 5432));
    assert_eq!(
        (s.maintenance_db.as_str(), s.application_name.as_str()),
        ("postgres", "pgmi")
    );
    assert_eq!(s.source, Source::ConnectionFlag);
    let env = Env::from_pairs(&[("PGDATABASE", "d")]);
    let s = resolve(&flags(), &env, None).unwrap();
    assert_eq!(s.first(), ("localhost", 5432));
    assert_eq!(s.source, Source::Defaults);
    let env = Env::from_pairs(&[("PGDATABASE", "d"), ("USER", "os-user")]);
    assert_eq!(resolve(&flags(), &env, None).unwrap().user, "os-user");
    let env = Env::from_pairs(&[("PGDATABASE", "d"), ("USERNAME", "win-user")]);
    assert_eq!(resolve(&flags(), &env, None).unwrap().user, "win-user");
    let env = Env::from_pairs(&[("PGDATABASE", "d")]);
    assert_eq!(resolve(&flags(), &env, None).unwrap().user, "postgres");
}

#[test]
fn service_files_are_refused() {
    for (s, env) in [
        (Some("service=prod"), vec![]),
        (Some("postgresql:///?service=prod"), vec![]),
        (Some("host=db dbname=x"), vec![("PGSERVICE", "prod")]),
        (None, vec![("PGSERVICE", "prod")]),
    ] {
        let fl = s.map_or_else(flags, conn);
        let e = refused(resolve(&fl, &Env::from_pairs(&env), None));
        assert!(
            matches!(
                e,
                ResolveError::ServiceEnv
                    | ResolveError::Parse {
                        error: ParseError::ServiceFile,
                        ..
                    }
            ),
            "{s:?}: {e}"
        );
        assert!(e.to_string().contains("service"), "{s:?}: {e}");
    }
}

#[test]
fn a_flag_beats_the_environment_which_beats_the_default() {
    let mut fl = flags();
    fl.host = Some("flaghost".into());
    let env = Env::from_pairs(&[
        ("PGHOST", "envhost"),
        ("PGPORT", "6005"),
        ("PGUSER", "envuser"),
        ("PGDATABASE", "d"),
    ]);
    let s = resolve(&fl, &env, None).unwrap();
    assert_eq!(s.first(), ("flaghost", 6005));
    assert_eq!(s.user, "envuser");
    assert_eq!(s.source, Source::Flags);
}

#[test]
fn pgmi_yaml_fills_what_flags_and_the_environment_leave() {
    let y = yaml();
    let s = resolve(&flags(), &Env::default(), Some(&y)).unwrap();
    assert_eq!(s.first(), ("yamlhost", 5434));
    assert_eq!(
        (s.user.as_str(), s.target_db.as_str(), s.sslmode()),
        ("yamluser", "yamldb", SslMode::Require)
    );
    assert_eq!(s.source, Source::Yaml);

    let env = Env::from_pairs(&[("PGHOST", "envhost"), ("PGPORT", "6006")]);
    let s = resolve(&flags(), &env, Some(&y)).unwrap();
    assert_eq!(s.first(), ("envhost", 6006));
    assert_eq!(s.user, "yamluser");
    assert_eq!(s.source, Source::PgHost);

    let mut fl = flags();
    fl.host = Some("flaghost".into());
    let s = resolve(&fl, &Env::default(), Some(&y)).unwrap();
    assert_eq!(s.first(), ("flaghost", 5434));
}

#[test]
fn certificate_flags_beat_the_environment_which_beats_pgmi_yaml() {
    let mut fl = flags();
    fl.host = Some("localhost".into());
    fl.database = Some("d".into());
    fl.sslcert = Some("flag.crt".into());
    let env = Env::from_pairs(&[
        ("PGSSLCERT", "env.crt"),
        ("PGSSLKEY", "env.key"),
        ("PGSSLROOTCERT", "env-ca.crt"),
        ("PGSSLPASSWORD", "env-pass"),
    ]);
    let y = Connection {
        sslcert: Some("yaml.crt".into()),
        sslkey: Some("yaml.key".into()),
        sslrootcert: Some("yaml-ca.crt".into()),
        ..Connection::default()
    };
    let s = resolve(&fl, &env, Some(&y)).unwrap();
    assert_eq!(s.sslcert.as_deref(), Some("flag.crt"));
    assert_eq!(s.sslkey.as_deref(), Some("env.key"));
    assert_eq!(s.sslrootcert.as_deref(), Some("env-ca.crt"));
    assert_eq!(s.sslpassword.as_ref().map(Secret::expose), Some("env-pass"));
}

#[test]
fn a_strings_certificates_beat_the_environment() {
    let env = Env::from_pairs(&[
        ("PGSSLROOTCERT", "/from/env-ca.crt"),
        ("PGSSLCERT", "/from/env.crt"),
        ("PGSSLKEY", "/from/env.key"),
        ("PGSSLPASSWORD", "fromenv"),
    ]);
    let s = resolve(
        &conn("postgresql://u@h/db?sslmode=verify-full&sslrootcert=/from/conn-ca.crt&sslcert=/from/conn.crt&sslkey=/from/conn.key&sslpassword=fromconn"),
        &env,
        None,
    )
    .unwrap();
    assert_eq!(s.sslrootcert.as_deref(), Some("/from/conn-ca.crt"));
    assert_eq!(s.sslcert.as_deref(), Some("/from/conn.crt"));
    assert_eq!(s.sslkey.as_deref(), Some("/from/conn.key"));
    assert_eq!(s.sslpassword.as_ref().map(Secret::expose), Some("fromconn"));

    let s = resolve(
        &conn("postgresql://u@h/db?sslmode=verify-full&sslcert=/from/conn.crt"),
        &env,
        None,
    )
    .unwrap();
    assert_eq!(s.sslcert.as_deref(), Some("/from/conn.crt"));
    assert_eq!(s.sslrootcert.as_deref(), Some("/from/env-ca.crt"));
    assert_eq!(s.sslkey.as_deref(), Some("/from/env.key"));
    assert_eq!(s.sslpassword.as_ref().map(Secret::expose), Some("fromenv"));
}

#[test]
fn pgmi_yaml_certificates_apply_beside_a_connection_string() {
    let y = Connection {
        sslcert: Some("yaml.crt".into()),
        sslkey: Some("yaml.key".into()),
        sslrootcert: Some("yaml-ca.crt".into()),
        ..Connection::default()
    };
    let s = resolve(
        &conn("postgresql://u@h/db?sslmode=require"),
        &Env::default(),
        Some(&y),
    )
    .unwrap();
    assert_eq!(s.sslrootcert.as_deref(), Some("yaml-ca.crt"));
    assert_eq!(s.sslcert.as_deref(), Some("yaml.crt"));
    assert_eq!(s.sslkey.as_deref(), Some("yaml.key"));

    let env = Env::from_pairs(&[("PGSSLKEY", "env.key")]);
    let s = resolve(
        &conn("postgresql://u@h/db?sslcert=conn.crt&sslrootcert="),
        &env,
        Some(&y),
    )
    .unwrap();
    assert_eq!(s.sslcert.as_deref(), Some("conn.crt"));
    assert_eq!(s.sslkey.as_deref(), Some("env.key"));
    assert_eq!(s.sslrootcert.as_deref(), Some("yaml-ca.crt"));
}

#[test]
fn a_certificate_flag_beats_the_string() {
    let mut fl = conn("postgresql://u@h/db?sslmode=verify-full&sslrootcert=/from/conn-ca.crt");
    fl.sslrootcert = Some("/from/flag-ca.crt".into());
    let env = Env::from_pairs(&[("PGSSLROOTCERT", "/from/env-ca.crt")]);
    let s = resolve(&fl, &env, None).unwrap();
    assert_eq!(s.sslrootcert.as_deref(), Some("/from/flag-ca.crt"));
}

#[test]
fn a_string_replaces_the_pgmi_yaml_connection_block() {
    let y = yaml();
    let s = resolve(&conn("postgresql://stringhost"), &Env::default(), Some(&y)).unwrap();
    assert_eq!(s.first(), ("stringhost", 5432));
    assert_ne!(s.user, "yamluser");
    assert_eq!((s.requested_sslmode, s.sslmode()), (None, SslMode::Prefer));
    assert_eq!(s.target_db, "yamldb");
    let s = resolve(&flags(), &Env::default(), Some(&y)).unwrap();
    assert_eq!(s.first(), ("yamlhost", 5434));
    assert_eq!(s.sslmode(), SslMode::Require);
}

#[test]
fn a_server_flag_next_to_an_environment_string_is_refused() {
    let one = |fl: Flags, env: &[(&str, &str)], want: &str| {
        let e = refused(resolve(&fl, &Env::from_pairs(env), None));
        assert!(e.to_string().contains(want), "{e}");
        assert!(!e.to_string().contains("secret"), "{e}");
    };
    let mut fl = flags();
    fl.host = Some("flaghost".into());
    one(
        fl,
        &[("DATABASE_URL", "postgresql://u:p@envhost/envdb")],
        "cannot combine -h with DATABASE_URL",
    );
    let mut fl = flags();
    fl.port = Some("6007".into());
    one(
        fl,
        &[
            ("PGHOST", "pghost"),
            ("DATABASE_URL", "postgresql://u:p@urlhost/urldb"),
        ],
        "cannot combine -p with DATABASE_URL",
    );
    let fl = Flags {
        host: Some("h".into()),
        port: Some("5471".into()),
        username: Some("u".into()),
        sslmode: Some(SslMode::Require),
        ..Flags::default()
    };
    one(
        fl,
        &[(
            "PGMI_CONNECTION_STRING",
            "postgresql://u:secret@envhost:6008/envdb",
        )],
        "cannot combine -h with PGMI_CONNECTION_STRING",
    );
}

#[test]
fn the_sslmode_flag_applies_to_an_environment_string() {
    let mut fl = flags();
    fl.sslmode = Some(SslMode::Disable);
    let env = Env::from_pairs(&[(
        "DATABASE_URL",
        "postgresql://u:p@envhost:6009/envdb?sslmode=require",
    )]);
    let s = resolve(&fl, &env, None).unwrap();
    assert_eq!(s.first(), ("envhost", 6009));
    assert_eq!(
        (s.target_db.as_str(), s.sslmode()),
        ("envdb", SslMode::Disable)
    );
}

#[test]
fn the_connection_flag_and_the_sslmode_flag_are_refused_together() {
    let mut fl = conn("postgresql://u@h/db");
    fl.sslmode = Some(SslMode::Require);
    let e = refused(resolve(&fl, &Env::default(), None));
    assert_eq!(
        e.to_string(),
        "cannot combine --connection with --sslmode; put sslmode in the connection string"
    );
}

#[test]
fn an_unknown_key_in_a_string_is_refused_before_connecting() {
    let e = refused(resolve(&conn("host=db bogus=1"), &Env::default(), None));
    assert_eq!(
        e.to_string(),
        "--connection: invalid connection option \"bogus\""
    );
}

fn token_check(
    provider: Provider,
    fl: &Flags,
    env: &[(&str, &str)],
    y: Option<&Connection>,
) -> crate::error::Result<Spec> {
    let env = Env::from_pairs(env);
    let mut spec = resolve(fl, &env, y)?;
    let req = cloud::Options {
        aws_region: Some("us-east-1".into()),
        google_instance: Some("p:r:i".into()),
        ..cloud::Options::default()
    };
    cloud::check(provider, &req, &mut spec, &env)?;
    Ok(spec)
}

type SslSource = (
    &'static str,
    Flags,
    Vec<(&'static str, String)>,
    Option<Connection>,
);

fn sslmode_sources(mode: &str) -> Vec<SslSource> {
    let with_host = |sslmode: Option<&str>| Flags {
        host: Some("db.example.com".into()),
        database: Some("d".into()),
        sslmode: sslmode.map(|m| m.parse().unwrap()),
        ..Flags::default()
    };
    vec![
        ("flag", with_host(Some(mode)), vec![], None),
        (
            "PGSSLMODE with granular",
            with_host(None),
            vec![("PGSSLMODE", mode.to_string())],
            None,
        ),
        (
            "PGSSLMODE with connection string",
            conn("postgresql://u@db.example.com/postgres"),
            vec![("PGSSLMODE", mode.to_string())],
            None,
        ),
        (
            "connection string",
            conn(&format!(
                "postgresql://u@db.example.com/postgres?sslmode={mode}"
            )),
            vec![],
            None,
        ),
        (
            "PGMI_CONNECTION_STRING",
            flags(),
            vec![(
                "PGMI_CONNECTION_STRING",
                format!("postgresql://u@db.example.com/postgres?sslmode={mode}"),
            )],
            None,
        ),
        (
            "pgmi.yaml",
            Flags {
                database: Some("d".into()),
                ..Flags::default()
            },
            vec![("PGHOST", "db.example.com".to_string())],
            Some(Connection {
                sslmode: Some(mode.into()),
                ..Connection::default()
            }),
        ),
    ]
}

#[test]
fn token_auth_refuses_an_sslmode_that_allows_plaintext() {
    for mode in ["disable", "allow", "prefer"] {
        for (name, fl, env, y) in sslmode_sources(mode) {
            for provider in [Provider::Azure, Provider::Aws] {
                let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (*k, v.as_str())).collect();
                let e = refused_before_connecting(token_check(provider, &fl, &env, y.as_ref()));
                let text = e.to_string();
                let from = match name {
                    "flag" => "--sslmode",
                    "connection string" => "--connection",
                    n if n.starts_with("PGSSLMODE") => "PGSSLMODE",
                    n => n,
                };
                assert!(
                    text.starts_with(&format!("sslmode \"{mode}\" from {from} is not allowed")),
                    "{name}: {text}"
                );
                assert!(
                    text.contains(&format!("with {} authentication", provider.label())),
                    "{name}: {text}"
                );
            }
        }
    }
}

#[test]
fn a_refused_sslmode_from_pgmi_yaml_names_its_line_and_the_fix() {
    let fl = Flags {
        host: Some("db.example.com".into()),
        database: Some("d".into()),
        ..Flags::default()
    };
    let y = Connection {
        sslmode: Some("prefer".into()),
        sslmode_line: Some(9),
        ..Connection::default()
    };
    for provider in [Provider::Azure, Provider::Aws, Provider::Databricks] {
        let env: &[(&str, &str)] = &[
            (
                "PGMI_DATABRICKS_ENDPOINT",
                "projects/p/branches/b/endpoints/e",
            ),
            ("DATABRICKS_HOST", "https://w.example.com"),
        ];
        let e = refused_before_connecting(token_check(provider, &fl, env, Some(&y)));
        assert_eq!(
            e.to_string(),
            format!(
                "sslmode \"prefer\" from pgmi.yaml line 9 is not allowed with {} authentication: it can send the token without TLS; change that line to \"sslmode: verify-full\", or delete it to use the default for token sign-in",
                provider.label()
            )
        );
    }
}

#[test]
fn token_auth_accepts_a_tls_only_sslmode() {
    for mode in [SslMode::Require, SslMode::VerifyCa, SslMode::VerifyFull] {
        for (name, fl, env, y) in sslmode_sources(mode.as_str()) {
            for provider in [Provider::Azure, Provider::Aws] {
                let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (*k, v.as_str())).collect();
                let s = token_check(provider, &fl, &env, y.as_ref())
                    .unwrap_or_else(|e| panic!("{name}: {e}"));
                assert_eq!(s.sslmode(), mode, "{name}");
            }
        }
    }
}

#[test]
fn token_auth_defaults_to_a_verifying_sslmode() {
    let granular = Flags {
        host: Some("db.example.com".into()),
        database: Some("d".into()),
        ..Flags::default()
    };
    let string = conn("postgresql://u@db.example.com/postgres");
    for fl in [granular, string] {
        let s = token_check(Provider::Azure, &fl, &[], None).unwrap();
        assert_eq!(s.sslmode(), SslMode::VerifyFull);
        let s = token_check(Provider::Aws, &fl, &[], None).unwrap();
        assert_eq!(s.sslmode(), SslMode::Require);
        let mut fl = fl;
        fl.sslrootcert = Some("/etc/ssl/rds-global-bundle.pem".into());
        let s = token_check(Provider::Aws, &fl, &[], None).unwrap();
        assert_eq!(s.sslmode(), SslMode::VerifyFull);
    }
}

#[test]
fn without_a_token_the_prefer_default_and_weak_modes_stay() {
    let mut fl = flags();
    fl.host = Some("localhost".into());
    fl.database = Some("d".into());
    let s = resolve(&fl, &Env::default(), None).unwrap();
    assert_eq!(s.sslmode(), SslMode::Prefer);
    fl.sslmode = Some(SslMode::Disable);
    assert_eq!(
        resolve(&fl, &Env::default(), None).unwrap().sslmode(),
        SslMode::Disable
    );
    let google = Flags {
        database: Some("d".into()),
        ..Flags::default()
    };
    let s = token_check(Provider::Google, &google, &[], None).unwrap();
    assert_eq!(s.sslmode(), SslMode::Prefer);
    let mut fl = google;
    fl.sslmode = Some(SslMode::Disable);
    let s = token_check(Provider::Google, &fl, &[], None).unwrap();
    assert_eq!(s.sslmode(), SslMode::Disable);
}

#[test]
fn formatting_never_prints_secrets() {
    let mut fl = conn(
        "postgresql://deployer:pw-must-not-appear@db.internal:5432/app?sslpassword=sslpw-must-not-appear",
    );
    fl.database = Some("app".into());
    let mut spec = resolve(&fl, &Env::default(), None).unwrap();
    spec.token = Some(cloud::Token::Fixed(
        Provider::Azure,
        "token-must-not-appear".into(),
    ));
    let request = crate::deploy::Request {
        connection: fl.clone(),
        ..crate::deploy::Request::default()
    };
    let secrets = [
        "pw-must-not-appear",
        "sslpw-must-not-appear",
        "token-must-not-appear",
    ];
    for (what, text) in [
        ("Spec", format!("{spec:?}")),
        ("Spec pretty", format!("{spec:#?}")),
        ("Flags", format!("{fl:?}")),
        ("Request", format!("{request:#?}")),
    ] {
        for s in secrets {
            assert!(!text.contains(s), "{what} shows {s}: {text}");
        }
        assert!(text.contains("[redacted]"), "{what}: {text}");
    }
    let text = format!("{spec:?}");
    assert!(
        text.contains("db.internal") && text.contains("deployer"),
        "{text}"
    );
}

fn keyword(s: &str) -> Result<Spec, ResolveError> {
    resolve(
        &conn(&format!("host=h dbname=d {s}")),
        &Env::default(),
        None,
    )
}

#[track_caller]
fn not_supported(s: &str) -> String {
    let e = refused(keyword(s));
    assert!(matches!(e, ResolveError::NotSupported { .. }), "{s}: {e:?}");
    e.to_string()
}

#[track_caller]
fn invalid(s: &str, key: &str) {
    let e = refused(keyword(s));
    assert!(
        matches!(e, ResolveError::Value { key: k, .. } if k == key),
        "{s}: {e:?}"
    );
}

#[test]
fn every_accepted_key_is_consumed_or_refused() {
    const SAMPLES: &[(&str, &str)] = &[
        ("host", "db1"),
        ("hostaddr", "10.0.0.1"),
        ("port", "6000"),
        ("dbname", "app"),
        ("user", "me"),
        ("password", "pw"),
        ("passfile", "/p"),
        ("channel_binding", "disable"),
        ("connect_timeout", "5"),
        ("client_encoding", "UTF8"),
        ("options", "-cwork_mem=5MB"),
        ("application_name", "a"),
        ("fallback_application_name", "f"),
        ("keepalives", "1"),
        ("keepalives_idle", "30"),
        ("keepalives_interval", "5"),
        ("keepalives_count", "3"),
        ("tcp_user_timeout", "1000"),
        ("sslmode", "require"),
        ("sslnegotiation", "postgres"),
        ("sslcompression", "0"),
        ("sslcert", "/c"),
        ("sslkey", "/k"),
        ("sslpassword", "kp"),
        ("sslcertmode", "disable"),
        ("sslrootcert", "/r"),
        ("sslcrl", ""),
        ("sslcrldir", ""),
        ("sslsni", "0"),
        ("requirepeer", ""),
        ("ssl_min_protocol_version", "TLSv1.3"),
        ("ssl_max_protocol_version", "TLSv1.3"),
        ("gssencmode", "disable"),
        ("krbsrvname", "postgres"),
        ("gsslib", ""),
        ("gssdelegation", "0"),
        ("target_session_attrs", "read-write"),
        ("load_balance_hosts", "random"),
        ("require_auth", "scram-sha-256"),
        ("min_protocol_version", "3.0"),
        ("max_protocol_version", "3.2"),
        ("default_query_exec_mode", "exec"),
        ("statement_cache_capacity", "0"),
        ("description_cache_capacity", "0"),
    ];
    let mut sampled: Vec<&str> = SAMPLES.iter().map(|(k, _)| *k).collect();
    let mut known = super::parse::KNOWN.to_vec();
    sampled.sort_unstable();
    known.sort_unstable();
    assert_eq!(
        sampled, known,
        "every key parse::KNOWN accepts needs a sample here and a consumer or refusal in resolve::finish"
    );
    for (key, v) in SAMPLES {
        let r = keyword(&format!("{key}='{v}'"));
        assert!(
            !matches!(r, Err(ResolveError::Unhandled(_))),
            "{key} is accepted and ignored: {:?}",
            r.err()
        );
    }
}

#[test]
fn every_variable_the_go_driver_reads_is_read_or_refused() {
    const PGX: &[&str] = &[
        "PGHOST",
        "PGPORT",
        "PGDATABASE",
        "PGUSER",
        "PGPASSWORD",
        "PGPASSFILE",
        "PGAPPNAME",
        "PGCONNECT_TIMEOUT",
        "PGSSLMODE",
        "PGSSLKEY",
        "PGSSLCERT",
        "PGSSLCERTMODE",
        "PGSSLSNI",
        "PGSSLROOTCERT",
        "PGSSLPASSWORD",
        "PGSSLNEGOTIATION",
        "PGTARGETSESSIONATTRS",
        "PGSERVICE",
        "PGSERVICEFILE",
        "PGTZ",
        "PGOPTIONS",
        "PGMINPROTOCOLVERSION",
        "PGMAXPROTOCOLVERSION",
        "PGCHANNELBINDING",
        "PGREQUIREAUTH",
    ];
    const ELSEWHERE: &[&str] = &["PGDATABASE", "PGSERVICE", "PGSERVICEFILE", "PGTZ"];
    for name in PGX {
        assert!(
            ENV_DEFAULTS.iter().any(|(n, _)| n == name) || ELSEWHERE.contains(name),
            "{name} is read by Go (pgx) but not by resolve::ENV_DEFAULTS"
        );
    }
    for (name, key) in ENV_DEFAULTS {
        assert!(
            super::parse::KNOWN.contains(key),
            "{name} feeds {key}, which parse::KNOWN does not accept"
        );
    }
}

#[track_caller]
fn from_env(name: &str, value: &str) -> Result<Spec, ResolveError> {
    let mut env = Env::default();
    env.insert(name, value);
    resolve(&conn("host=h dbname=d"), &env, None)
}

#[test]
fn tls_and_protocol_variables_are_honoured() {
    let s = from_env("PGSSLSNI", "0").unwrap();
    assert!(!s.sslsni);
    let s = from_env("PGSSLCERTMODE", "disable").unwrap();
    assert!(s.sslcertmode_disable);
    from_env("PGMINPROTOCOLVERSION", "3.0").unwrap();
    from_env("PGMAXPROTOCOLVERSION", "3.2").unwrap();
    let mut env = Env::default();
    env.insert("PGSSLSNI", "0");
    let s = resolve(&conn("host=h dbname=d sslsni=1"), &env, None).unwrap();
    assert!(s.sslsni, "the connection string outranks PGSSLSNI");
}

#[test]
fn an_invalid_tls_or_protocol_variable_is_refused_like_its_key() {
    for (name, value, key) in [
        ("PGSSLSNI", "2", "sslsni"),
        ("PGSSLCERTMODE", "bogus", "sslcertmode"),
        ("PGSSLCERTMODE", "", "sslcertmode"),
        ("PGMINPROTOCOLVERSION", "bogus", "min_protocol_version"),
        ("PGMAXPROTOCOLVERSION", "9.9", "max_protocol_version"),
    ] {
        let e = refused(from_env(name, value));
        assert!(
            matches!(&e, ResolveError::Value { key: k, .. } if *k == key),
            "{name}={value}: {e:?}"
        );
        assert_eq!(Error::from(e).exit_code(), 10);
    }
    assert!(matches!(
        refused(from_env("PGSSLCERTMODE", "require")),
        ResolveError::CertModeRequire
    ));
    assert!(matches!(
        refused(from_env("PGMINPROTOCOLVERSION", "3.2")),
        ResolveError::ProtocolUnsupported(_)
    ));
    invalid("sslcertmode=''", "sslcertmode");
}

#[test]
fn an_unhandled_key_is_refused_before_connecting_as_a_bug() {
    let e = Error::from(ResolveError::Unhandled("newkey".into()));
    assert_eq!(e.exit_code(), 10);
    assert_eq!(
        e.text(),
        "connection option \"newkey\" is accepted but not handled; this is a pgmi bug, please report it"
    );
}

#[test]
fn keepalives_and_tcp_user_timeout_are_read_and_bad_values_refused() {
    use super::socket::{Keepalive, Tcp};
    assert_eq!(keyword("").unwrap().tcp, Tcp::default());
    assert_eq!(
        keyword(
            "keepalives_idle=30 keepalives_interval=5 keepalives_count=3 tcp_user_timeout=2500"
        )
        .unwrap()
        .tcp,
        Tcp {
            keepalive: Some(Keepalive {
                idle: Duration::from_secs(30),
                interval: Duration::from_secs(5),
                count: 3,
            }),
            user_timeout: Some(Duration::from_millis(2500)),
            strict: true,
        }
    );
    let off = keyword("keepalives=0 keepalives_idle=30").unwrap().tcp;
    assert_eq!((off.keepalive, off.strict), (None, true));
    let zeros = keyword("keepalives=1 keepalives_idle=0 tcp_user_timeout=0")
        .unwrap()
        .tcp;
    assert_eq!(
        (zeros.keepalive, zeros.user_timeout, zeros.strict),
        (Some(Keepalive::default()), None, true)
    );
    for key in [
        "keepalives",
        "keepalives_idle",
        "keepalives_interval",
        "keepalives_count",
        "tcp_user_timeout",
    ] {
        for bad in ["abc", "-1", "1.5", "4294967296"] {
            invalid(&format!("{key}={bad}"), key);
        }
    }
}

#[test]
fn load_balance_hosts_is_disable_or_random() {
    assert_eq!(
        keyword("").unwrap().load_balance_hosts,
        LoadBalanceHosts::Disable
    );
    assert_eq!(
        keyword("load_balance_hosts=random")
            .unwrap()
            .load_balance_hosts,
        LoadBalanceHosts::Random
    );
    invalid("load_balance_hosts=sometimes", "load_balance_hosts");
}

#[test]
fn hostaddr_skips_the_lookup_and_keeps_the_host_name() {
    let s = keyword("host=a,b port=1,2 hostaddr=10.0.0.1,").unwrap();
    let got: Vec<_> = s
        .hosts
        .endpoints()
        .map(|e| (e.name.as_str(), e.port, e.addr))
        .collect();
    assert_eq!(
        got,
        [("a", 1, Some("10.0.0.1".parse().unwrap())), ("b", 2, None)]
    );
    let only = resolve(&conn("hostaddr=::1 dbname=d"), &Env::default(), None).unwrap();
    assert_eq!(hosts(&only), [("::1".to_string(), 5432)]);
    assert_eq!(
        refused(keyword("hostaddr=10.0.0.1,10.0.0.2")).to_string(),
        "could not match 1 host names to 2 hostaddr values"
    );
    invalid("hostaddr=db.example", "hostaddr");
}

#[test]
fn gssapi_keys_are_honoured_without_gssapi_and_refused_when_they_need_it() {
    for ok in [
        "gssencmode=disable",
        "gssencmode=prefer",
        "gssdelegation=0",
        "gsslib=''",
        "krbsrvname=postgres",
    ] {
        assert!(keyword(ok).is_ok(), "{ok}");
    }
    assert_eq!(
        not_supported("gssencmode=require"),
        "gssencmode=require is not supported: pgmi has no GSSAPI encryption; use gssencmode=disable or prefer, and sslmode for an encrypted connection"
    );
    assert_eq!(
        not_supported("gssdelegation=1"),
        "gssdelegation=1 is not supported: pgmi has no GSSAPI (Kerberos) support; remove it"
    );
    assert_eq!(
        not_supported("gsslib=gssapi"),
        "gsslib=gssapi is not supported: pgmi has no GSSAPI (Kerberos) support; remove it"
    );
    invalid("gssencmode=maybe", "gssencmode");
    invalid("gssdelegation=yes", "gssdelegation");
    assert_eq!(
        Error::from(refused(keyword("gssencmode=require"))).exit_code(),
        10
    );
}

#[test]
fn sslcompression_and_pgx_keys_are_checked() {
    for ok in [
        "sslcompression=0",
        "default_query_exec_mode=simple_protocol",
        "statement_cache_capacity=-1",
    ] {
        assert!(keyword(ok).is_ok(), "{ok}");
    }
    assert_eq!(
        not_supported("sslcompression=1"),
        "sslcompression=1 is not supported: pgmi's TLS never compresses, which is the safe choice; remove it or set 0"
    );
    invalid("sslcompression=yes", "sslcompression");
    invalid("default_query_exec_mode=bogus", "default_query_exec_mode");
    invalid("statement_cache_capacity=x", "statement_cache_capacity");
    invalid("description_cache_capacity=x", "description_cache_capacity");
}

#[test]
fn a_named_client_encoding_beats_pgclientencoding_and_auto_is_refused() {
    let env = Env::from_pairs(&[("PGCLIENTENCODING", "LATIN1")]);
    let with = |s: &str, env: &Env| {
        resolve(&conn(&format!("host=h dbname=d {s}")), env, None).map(|s| s.client_encoding)
    };
    assert_eq!(with("", &Env::default()).unwrap(), None);
    assert_eq!(with("", &env).unwrap().as_deref(), Some("LATIN1"));
    assert_eq!(
        with("client_encoding=WIN1252", &env).unwrap().as_deref(),
        Some("WIN1252")
    );
    assert_eq!(with("client_encoding=''", &Env::default()).unwrap(), None);
    for auto in ["client_encoding=auto", "client_encoding=AUTO"] {
        let e = refused(keyword(auto));
        assert!(matches!(e, ResolveError::NotSupported { .. }), "{e:?}");
        assert!(
            e.to_string()
                .starts_with("client_encoding=auto is not supported: pgmi does not derive an encoding from the locale"),
            "{e}"
        );
    }
}
