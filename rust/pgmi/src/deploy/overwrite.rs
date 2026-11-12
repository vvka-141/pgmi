use std::fmt::Write as _;
use std::time::Duration;

use tokio_postgres::Row;
use tokio_postgres::types::Type;

use super::{Needs, Provision, quote_ident, sql_error, sql_error_needing};
use crate::conn::dial::Session;
use crate::error::{Error, Kind, Result};
use crate::output::Reporter;

const LIST_GUCS: [&str; 6] = [
    "local_preload_libraries",
    "search_path",
    "session_preload_libraries",
    "shared_preload_libraries",
    "temp_tablespaces",
    "unix_socket_directories",
];

/// What an existing target looked like before the drop: put back after the
/// recreate.
pub(crate) struct Snapshot {
    locale: Option<String>,
    owner: String,
    connlimit: i32,
    settings: Vec<String>,
    comment: Option<String>,
    role_settings: Vec<RoleSettings>,
    acl: Option<Vec<Grant>>,
}

/// One privilege on the database; no grantee is PUBLIC.
struct Grant {
    grantee: Option<String>,
    privilege: String,
    grantable: bool,
}

#[derive(Debug, serde::Deserialize)]
struct RoleSettings {
    role: String,
    options: Vec<String>,
}

fn manager_sql(name: &str) -> Result<&'static str> {
    pgmi_shared::file(&format!("manager/{name}.sql"))
        .and_then(|b| std::str::from_utf8(b).ok())
        .map(|s| s.trim().trim_end_matches(';'))
        .ok_or_else(|| Error::internal(format!("embedded manager/{name}.sql is missing")))
}

async fn rows(client: &tokio_postgres::Client, sql: &str, db: &str) -> Result<Vec<Row>> {
    client
        .query_typed(sql, &[(&db, Type::TEXT)])
        .await
        .map_err(|e| sql_error("read the target database", e))
}

fn get<'a, T: tokio_postgres::types::FromSql<'a>>(
    r: &'a Row,
    i: impl tokio_postgres::row::RowIndex + std::fmt::Display,
) -> Result<T> {
    r.try_get(i)
        .map_err(|e| sql_error("read the target database", e))
}

fn text(r: &Row, column: &str) -> Result<String> {
    Ok(get::<Option<String>>(r, column)?.unwrap_or_default())
}

/// How a database stores and sorts text, as `CREATE DATABASE` takes it.
#[derive(Debug, PartialEq, Eq)]
struct Locale {
    encoding: String,
    collate: String,
    ctype: String,
    provider: String,
    locale: String,
    icu_rules: String,
}

impl Locale {
    fn read(r: &Row, prefix: &str) -> Result<Self> {
        let column = |name: &str| text(r, &format!("{prefix}{name}"));
        Ok(Locale {
            encoding: column("encoding")?,
            collate: column("lc_collate")?,
            ctype: column("lc_ctype")?,
            provider: column("provider")?,
            locale: column("locale")?,
            icu_rules: column("icu_rules")?,
        })
    }
}

fn role_settings(json: Option<String>, db: &str) -> Result<Vec<RoleSettings>> {
    let unreadable = || {
        Error::general(format!(
            "--overwrite could not read the role settings of \"{db}\"; nothing was dropped"
        ))
    };
    serde_json::from_str(&json.ok_or_else(unreadable)?).map_err(|e| unreadable().with_source(e))
}

fn first<T: for<'a> tokio_postgres::types::FromSql<'a>>(rows: &[Row]) -> Result<Option<T>> {
    rows.first().map(|r| get(r, 0)).transpose()
}

pub(crate) fn refuse_maintenance(target: &str, maintenance: &str) -> Result<()> {
    if target == maintenance {
        return Err(Error::config(format!(
            "cannot overwrite maintenance database \"{target}\"\npgmi connects to it for CREATE/DROP DATABASE; pick a different target with -d"
        )));
    }
    Ok(())
}

/// Takes the lock that serialises `--overwrite` runs on `db`, held until
/// `maintenance` closes, and reports whether `db` exists.
pub(crate) async fn claim(maintenance: &Session, db: &str) -> Result<bool> {
    let c = &maintenance.client;
    let held = rows(
        c,
        "SELECT pg_try_advisory_lock(hashtextextended('pgmi.overwrite.' || $1, 0))",
        db,
    )
    .await?;
    if first::<bool>(&held)? != Some(true) {
        return Err(Error::new(
            Kind::Locked,
            format!("another pgmi --overwrite of \"{db}\" is running"),
        ));
    }
    let template = rows(
        c,
        "SELECT datistemplate FROM pg_database WHERE datname = $1",
        db,
    )
    .await?;
    match first::<bool>(&template)? {
        Some(true) => Err(Error::config(format!(
            "cannot drop template database \"{db}\" (template0/template1 are protected by PostgreSQL)"
        ))),
        found => Ok(found.is_some()),
    }
}

/// Reads what the recreate must put back, and refuses when it could not, or
/// when a deploy is running against `db`. `None`: `db` is gone already.
pub(crate) async fn snapshot(maintenance: &Session, db: &str) -> Result<Option<Snapshot>> {
    let c = &maintenance.client;
    let Some(r) = rows(c, manager_sql("settings")?, db)
        .await?
        .into_iter()
        .next()
    else {
        return Ok(None);
    };
    let snapshot = capture(c, &r, db).await?;
    let gaps: Vec<String> =
        first(&rows(c, manager_sql("restore_gaps")?, db).await?)?.unwrap_or_default();
    if !gaps.is_empty() {
        return Err(Error::config(format!(
            "--overwrite would drop \"{db}\" and could not restore, as the deploying role:\n  {}\nnothing was dropped; deploy as the database owner or a superuser, or remove these first",
            gaps.join("\n  ")
        )));
    }
    let locked = rows(c, manager_sql("deploy_locked")?, db).await?;
    if first::<bool>(&locked)? == Some(true) {
        return Err(Error::new(
            Kind::Locked,
            format!(
                "another pgmi deploy is running against \"{db}\"; --overwrite does not drop it"
            ),
        ));
    }
    Ok(Some(snapshot))
}

async fn capture(c: &tokio_postgres::Client, r: &Row, db: &str) -> Result<Snapshot> {
    let own = Locale::read(r, "")?;
    let template = Locale::read(r, "template_")?;
    let role_settings = role_settings(
        first::<String>(
            &rows(
                c,
                &format!(
                    "SELECT v::text FROM ({}) AS q(v)",
                    manager_sql("role_settings")?
                ),
                db,
            )
            .await?,
        )?,
        db,
    )?;
    let acl_rows = rows(c, manager_sql("acl")?, db).await?;
    let acl = match acl_rows.first() {
        Some(r) if get::<bool>(r, 0)? => {
            let grantees: Vec<String> = get(r, 1)?;
            let privileges: Vec<String> = get(r, 2)?;
            let grantable: Vec<bool> = get(r, 3)?;
            Some(
                grantees
                    .into_iter()
                    .zip(privileges)
                    .zip(grantable)
                    .map(|((g, privilege), grantable)| Grant {
                        grantee: (!g.is_empty()).then_some(g),
                        privilege,
                        grantable,
                    })
                    .collect(),
            )
        }
        _ => None,
    };
    Ok(Snapshot {
        locale: (own != template).then(|| locale_clause(&own)),
        owner: text(r, "owner")?,
        connlimit: get(r, "connlimit")?,
        settings: get(r, "settings")?,
        comment: get(r, "comment")?,
        role_settings,
        acl,
    })
}

fn literal(s: &str) -> String {
    format!("E'{}'", s.replace('\\', "\\\\").replace('\'', "''"))
}

fn locale_clause(own: &Locale) -> String {
    let mut out = format!(
        " TEMPLATE template0 ENCODING {} LC_COLLATE {} LC_CTYPE {}",
        literal(&own.encoding),
        literal(&own.collate),
        literal(&own.ctype)
    );
    match own.provider.as_str() {
        "i" => {
            let _ = write!(
                out,
                " LOCALE_PROVIDER icu ICU_LOCALE {}",
                literal(&own.locale)
            );
            if !own.icu_rules.is_empty() {
                let _ = write!(out, " ICU_RULES {}", literal(&own.icu_rules));
            }
        }
        "b" => {
            let _ = write!(
                out,
                " LOCALE_PROVIDER builtin BUILTIN_LOCALE {}",
                literal(&own.locale)
            );
        }
        _ => out.push_str(" LOCALE_PROVIDER libc"),
    }
    out
}

fn split_list(value: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut chars = value.chars().peekable();
    loop {
        while chars.peek().is_some_and(|c| c.is_whitespace()) {
            chars.next();
        }
        let mut item = String::new();
        if chars.peek() == Some(&'"') {
            chars.next();
            while let Some(c) = chars.next() {
                match c {
                    '"' if chars.peek() == Some(&'"') => {
                        chars.next();
                        item.push('"');
                    }
                    '"' => break,
                    _ => item.push(c),
                }
            }
            while chars.peek().is_some_and(|c| *c != ',') {
                chars.next();
            }
        } else {
            while let Some(c) = chars.peek().copied().filter(|c| *c != ',') {
                item.push(c);
                chars.next();
            }
            item.truncate(item.trim_end().len());
        }
        out.push(item);
        if chars.next().is_none() {
            return out;
        }
    }
}

fn set_clause(option: &str) -> String {
    let (name, value) = option.split_once('=').unwrap_or((option, ""));
    let value = if LIST_GUCS.contains(&name.to_lowercase().as_str()) {
        split_list(value)
            .iter()
            .map(|v| literal(v))
            .collect::<Vec<_>>()
            .join(", ")
    } else {
        literal(value)
    };
    format!("SET {} = {value}", quote_ident(name))
}

fn create_sql(db: &str, snapshot: Option<&Snapshot>) -> String {
    let mut sql = format!("CREATE DATABASE {}", quote_ident(db));
    if let Some(s) = snapshot {
        if let Some(l) = &s.locale {
            sql.push_str(l);
        }
        let _ = write!(sql, " OWNER {}", quote_ident(&s.owner));
        if s.connlimit >= 0 {
            let _ = write!(sql, " CONNECTION LIMIT {}", s.connlimit);
        }
    }
    sql
}

fn restore_sql(db: &str, s: &Snapshot) -> Vec<(String, String)> {
    let d = quote_ident(db);
    let name = |o: &str| o.split('=').next().unwrap_or_default().to_string();
    let mut out = Vec::new();
    for o in &s.settings {
        out.push((
            format!("setting {} on the database", name(o)),
            format!("ALTER DATABASE {d} {}", set_clause(o)),
        ));
    }
    for r in &s.role_settings {
        for o in &r.options {
            out.push((
                format!("setting {} of role {} in the database", name(o), r.role),
                format!(
                    "ALTER ROLE {} IN DATABASE {d} {}",
                    quote_ident(&r.role),
                    set_clause(o)
                ),
            ));
        }
    }
    if let Some(c) = &s.comment {
        out.push((
            "the comment on the database".into(),
            format!("COMMENT ON DATABASE {d} IS {}", literal(c)),
        ));
    }
    if let Some(acl) = &s.acl {
        let mut grants = vec![format!("REVOKE ALL ON DATABASE {d} FROM PUBLIC")];
        for g in acl {
            let to = g
                .grantee
                .as_deref()
                .map_or_else(|| "PUBLIC".to_string(), quote_ident);
            let option = if g.grantable {
                " WITH GRANT OPTION"
            } else {
                ""
            };
            grants.push(format!(
                "GRANT {} ON DATABASE {d} TO {to}{option}",
                g.privilege
            ));
        }
        out.push(("the privileges on the database".into(), grants.join(";\n")));
    }
    out
}

const RECREATE: Duration = Duration::from_secs(60);

/// Drops `db` when there is a snapshot, creates it and puts the snapshot
/// back. Nothing cancels it once it has begun, so a database is never left
/// dropped or half restored; it has its own bound instead, and cancels the
/// statement on the server when that runs out. `provision` records the
/// create as soon as it has happened.
pub(crate) async fn recreate(
    maintenance: &Session,
    db: &str,
    snapshot: Option<&Snapshot>,
    reporter: &Reporter,
    provision: &mut Provision,
) -> Result<()> {
    let work = async {
        let c = &maintenance.client;
        if snapshot.is_some() {
            c.batch_execute(&format!("DROP DATABASE {} WITH (FORCE)", quote_ident(db)))
                .await
                .map_err(|e| sql_error("drop the target database", e))?;
        }
        c.batch_execute(&create_sql(db, snapshot))
            .await
            .map_err(|e| {
                sql_error_needing("create the target database", e, Some(Needs::CreateDb))
            })?;
        *provision = if snapshot.is_some() {
            Provision::Recreated
        } else {
            Provision::Created
        };
        let mut failed = Vec::new();
        if let Some(s) = snapshot {
            for (item, sql) in restore_sql(db, s) {
                if let Err(e) = c.batch_execute(&sql).await {
                    failed.push(format!("{item}: {}", sql_error("restore", e).text()));
                }
            }
        }
        if !failed.is_empty() {
            return Err(Error::sql(format!(
                "--overwrite recreated \"{db}\" but could not restore:\n  {}",
                failed.join("\n  ")
            )));
        }
        reporter.line(&if snapshot.is_some() {
            format!("Recreated database \"{db}\"")
        } else {
            format!("Database \"{db}\" does not exist; creating")
        });
        Ok(())
    };
    if let Ok(r) = tokio::time::timeout(RECREATE, work).await {
        r
    } else {
        maintenance.cancel().await;
        Err(Error::new(
            Kind::Timeout,
            format!("--overwrite of \"{db}\" did not finish within 60s"),
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn list_settings_are_split_and_quoted() {
        assert_eq!(
            set_clause("search_path=app, \"with space\", public"),
            "SET \"search_path\" = E'app', E'with space', E'public'"
        );
        assert_eq!(
            set_clause("DateStyle=ISO, DMY"),
            "SET \"DateStyle\" = E'ISO, DMY'"
        );
        assert_eq!(set_clause("x=it's"), "SET \"x\" = E'it''s'");
        assert_eq!(set_clause("x=a\\b"), "SET \"x\" = E'a\\\\b'");
        assert_eq!(split_list("\"a\"\"b\", c"), ["a\"b", "c"]);
    }

    #[test]
    fn create_keeps_locale_owner_and_limit() {
        let own = Locale {
            encoding: "UTF8".into(),
            collate: "C".into(),
            ctype: "C".into(),
            provider: "i".into(),
            locale: "de-DE".into(),
            icu_rules: "&a < b".into(),
        };
        let s = Snapshot {
            locale: Some(locale_clause(&own)),
            owner: "o\"w".into(),
            connlimit: 3,
            settings: vec![],
            comment: None,
            role_settings: vec![],
            acl: None,
        };
        assert_eq!(
            create_sql("d", Some(&s)),
            "CREATE DATABASE \"d\" TEMPLATE template0 ENCODING E'UTF8' LC_COLLATE E'C' LC_CTYPE E'C' LOCALE_PROVIDER icu ICU_LOCALE E'de-DE' ICU_RULES E'&a < b' OWNER \"o\"\"w\" CONNECTION LIMIT 3"
        );
        assert_eq!(create_sql("d", None), "CREATE DATABASE \"d\"");
    }

    #[test]
    fn role_settings_that_do_not_decode_stop_the_overwrite() {
        let ok = role_settings(Some(r#"[{"role":"r","options":["a=b"]}]"#.into()), "d").unwrap();
        assert_eq!(
            (ok[0].role.as_str(), ok[0].options.as_slice()),
            ("r", &["a=b".to_string()][..])
        );
        for bad in [Some("{\"role\":1}".to_string()), None] {
            let e = role_settings(bad, "d").unwrap_err();
            assert_eq!(e.kind(), Kind::General);
            assert!(
                e.text().starts_with(
                    "--overwrite could not read the role settings of \"d\"; nothing was dropped"
                ),
                "{}",
                e.text()
            );
        }
    }

    #[test]
    fn an_invalid_database_comes_back_without_a_connection_limit() {
        let s = Snapshot {
            locale: None,
            owner: "o".into(),
            connlimit: -2,
            settings: vec![],
            comment: None,
            role_settings: vec![],
            acl: None,
        };
        assert_eq!(
            create_sql("d", Some(&s)),
            "CREATE DATABASE \"d\" OWNER \"o\""
        );
    }

    #[test]
    fn privileges_are_reset_then_granted() {
        let s = Snapshot {
            locale: None,
            owner: "o".into(),
            connlimit: -1,
            settings: vec!["statement_timeout=5s".into()],
            comment: Some("c".into()),
            role_settings: vec![RoleSettings {
                role: "r".into(),
                options: vec!["work_mem=8MB".into()],
            }],
            acl: Some(vec![
                Grant {
                    grantee: None,
                    privilege: "CONNECT".into(),
                    grantable: false,
                },
                Grant {
                    grantee: Some("r".into()),
                    privilege: "TEMPORARY".into(),
                    grantable: true,
                },
            ]),
        };
        let sql: Vec<String> = restore_sql("d", &s).into_iter().map(|(_, s)| s).collect();
        assert_eq!(
            sql,
            [
                "ALTER DATABASE \"d\" SET \"statement_timeout\" = E'5s'",
                "ALTER ROLE \"r\" IN DATABASE \"d\" SET \"work_mem\" = E'8MB'",
                "COMMENT ON DATABASE \"d\" IS E'c'",
                "REVOKE ALL ON DATABASE \"d\" FROM PUBLIC;\nGRANT CONNECT ON DATABASE \"d\" TO PUBLIC;\nGRANT TEMPORARY ON DATABASE \"d\" TO \"r\" WITH GRANT OPTION",
            ]
        );
    }

    #[test]
    fn the_maintenance_database_is_never_the_overwrite_target() {
        let e = refuse_maintenance("postgres", "postgres").unwrap_err();
        assert_eq!(e.kind(), Kind::Config);
        assert!(
            e.text()
                .starts_with("cannot overwrite maintenance database \"postgres\"")
        );
        assert_eq!(
            refuse_maintenance("admin_db", "admin_db")
                .unwrap_err()
                .kind(),
            Kind::Config
        );
        assert!(refuse_maintenance("myapp", "postgres").is_ok());
        assert!(refuse_maintenance("POSTGRES", "postgres").is_ok());
    }
}
