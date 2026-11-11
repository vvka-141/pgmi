use std::time::Duration;

use tokio_postgres::Row;
use tokio_postgres::types::Type;

use super::{quote_ident, sql_error};
use crate::conn::dial::{Dialer, Session};
use crate::conn::resolve::Spec;
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

pub(crate) struct Plan {
    pub(crate) maintenance: Session,
    db: String,
    snapshot: Option<Snapshot>,
}

impl Plan {
    pub(crate) fn drops_a_database(&self) -> bool {
        self.snapshot.is_some()
    }
}

struct Snapshot {
    locale: Option<String>,
    owner: String,
    connlimit: i32,
    settings: Vec<String>,
    comment: Option<String>,
    role_settings: Vec<RoleSettings>,
    acl: Option<Vec<(String, String, bool)>>,
}

#[derive(serde::Deserialize)]
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
        .map_err(|e| sql_error("read the target database", &e))
}

fn text(r: &Row, i: usize) -> String {
    r.get::<_, Option<String>>(i).unwrap_or_default()
}

pub(crate) fn refuse_maintenance(spec: &Spec) -> Result<()> {
    if spec.target_db == spec.maintenance_db {
        return Err(Error::config(format!(
            "cannot overwrite maintenance database \"{}\"\npgmi connects to it for CREATE/DROP DATABASE; pick a different target with -d",
            spec.target_db
        )));
    }
    Ok(())
}

pub(crate) async fn prepare(dialer: &Dialer, spec: &Spec) -> Result<Plan> {
    let db = spec.target_db.clone();
    let maintenance = dialer.connect(spec, &spec.maintenance_db).await?;
    let c = &maintenance.client;
    let template = rows(
        c,
        "SELECT datistemplate FROM pg_database WHERE datname = $1",
        &db,
    )
    .await?;
    if template.first().is_some_and(|r| r.get::<_, bool>(0)) {
        return Err(Error::config(format!(
            "cannot drop template database \"{db}\" (template0/template1 are protected by PostgreSQL)"
        )));
    }
    let snapshot = match rows(c, manager_sql("settings")?, &db).await?.first() {
        None => None,
        Some(r) => Some(snapshot(c, r, &db).await?),
    };
    if snapshot.is_some() {
        let gaps: Vec<String> = rows(c, manager_sql("restore_gaps")?, &db)
            .await?
            .first()
            .map(|r| r.get(0))
            .unwrap_or_default();
        if !gaps.is_empty() {
            return Err(Error::config(format!(
                "--overwrite would drop \"{db}\" and could not restore, as the deploying role:\n  {}\nnothing was dropped; deploy as the database owner or a superuser, or remove these first",
                gaps.join("\n  ")
            )));
        }
    }
    let locked = rows(
        c,
        &format!("SELECT ({})::text", manager_sql("deploy_locked")?),
        &db,
    )
    .await?;
    if locked.first().map(|r| r.get::<_, String>(0)).as_deref() == Some("true") {
        return Err(Error::new(
            Kind::Locked,
            format!(
                "another pgmi deploy is running against \"{db}\"; --overwrite does not drop it"
            ),
        ));
    }
    Ok(Plan {
        maintenance,
        db,
        snapshot,
    })
}

async fn snapshot(c: &tokio_postgres::Client, r: &Row, db: &str) -> Result<Snapshot> {
    let own: Vec<String> = (0..6).map(|i| text(r, i)).collect();
    let template: Vec<String> = (10..16).map(|i| text(r, i)).collect();
    let role_settings = rows(
        c,
        &format!(
            "SELECT v::text FROM ({}) AS q(v)",
            manager_sql("role_settings")?
        ),
        db,
    )
    .await?
    .first()
    .and_then(|r| serde_json::from_str(&r.get::<_, String>(0)).ok())
    .unwrap_or_default();
    let acl = rows(c, manager_sql("acl")?, db).await?;
    let acl = acl.first().and_then(|r| {
        r.get::<_, bool>(0).then(|| {
            let grantees: Vec<String> = r.get(1);
            let privileges: Vec<String> = r.get(2);
            let grantable: Vec<bool> = r.get(3);
            grantees
                .into_iter()
                .zip(privileges)
                .zip(grantable)
                .map(|((g, p), o)| (g, p, o))
                .collect()
        })
    });
    Ok(Snapshot {
        locale: (own != template).then(|| locale_clause(&own)),
        owner: text(r, 6),
        connlimit: r.get(7),
        settings: r.get(8),
        comment: r.get(9),
        role_settings,
        acl,
    })
}

fn literal(s: &str) -> String {
    format!("E'{}'", s.replace('\\', "\\\\").replace('\'', "''"))
}

fn locale_clause(own: &[String]) -> String {
    let field = |i: usize| own.get(i).map(String::as_str).unwrap_or_default();
    let mut out = format!(
        " TEMPLATE template0 ENCODING {} LC_COLLATE {} LC_CTYPE {}",
        literal(field(0)),
        literal(field(1)),
        literal(field(2))
    );
    match field(3) {
        "i" => {
            out.push_str(&format!(
                " LOCALE_PROVIDER icu ICU_LOCALE {}",
                literal(field(4))
            ));
            if !field(5).is_empty() {
                out.push_str(&format!(" ICU_RULES {}", literal(field(5))));
            }
        }
        "b" => out.push_str(&format!(
            " LOCALE_PROVIDER builtin BUILTIN_LOCALE {}",
            literal(field(4))
        )),
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
        sql.push_str(&format!(" OWNER {}", quote_ident(&s.owner)));
        if s.connlimit != -1 {
            sql.push_str(&format!(" CONNECTION LIMIT {}", s.connlimit));
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
        for (grantee, privilege, grantable) in acl {
            let to = if grantee.is_empty() {
                "PUBLIC".to_string()
            } else {
                quote_ident(grantee)
            };
            let option = if *grantable { " WITH GRANT OPTION" } else { "" };
            grants.push(format!("GRANT {privilege} ON DATABASE {d} TO {to}{option}"));
        }
        out.push(("the privileges on the database".into(), grants.join(";\n")));
    }
    out
}

pub(crate) async fn recreate(plan: &Plan, reporter: &Reporter) -> Result<bool> {
    let Plan {
        maintenance,
        db,
        snapshot,
    } = plan;
    let work = async {
        let c = &maintenance.client;
        if snapshot.is_some() {
            c.batch_execute(&format!("DROP DATABASE {} WITH (FORCE)", quote_ident(db)))
                .await
                .map_err(|e| sql_error("drop the target database", &e))?;
        }
        c.batch_execute(&create_sql(db, snapshot.as_ref()))
            .await
            .map_err(|e| sql_error("create the target database", &e))?;
        let mut failed = Vec::new();
        if let Some(s) = &snapshot {
            for (item, sql) in restore_sql(db, s) {
                if let Err(e) = c.batch_execute(&sql).await {
                    failed.push(format!("{item}: {}", sql_error("restore", &e).text()));
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
        Ok(snapshot.is_some())
    };
    match tokio::time::timeout(Duration::from_secs(60), work).await {
        Ok(r) => r,
        Err(_) => Err(Error::new(
            Kind::Timeout,
            format!("--overwrite of \"{db}\" did not finish within 60s"),
        )),
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
        let own: Vec<String> = ["UTF8", "C", "C", "i", "de-DE", "&a < b"]
            .map(String::from)
            .to_vec();
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
                ("".into(), "CONNECT".into(), false),
                ("r".into(), "TEMPORARY".into(), true),
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
}
