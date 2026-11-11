use std::collections::BTreeMap;

use crate::error::{Error, Result};

pub(crate) type Fields = BTreeMap<String, String>;

pub(crate) const KNOWN: &[&str] = &[
    "host",
    "port",
    "dbname",
    "user",
    "password",
    "passfile",
    "channel_binding",
    "connect_timeout",
    "client_encoding",
    "options",
    "application_name",
    "fallback_application_name",
    "keepalives",
    "keepalives_idle",
    "keepalives_interval",
    "keepalives_count",
    "tcp_user_timeout",
    "sslmode",
    "sslnegotiation",
    "sslcompression",
    "sslcert",
    "sslkey",
    "sslpassword",
    "sslcertmode",
    "sslrootcert",
    "sslcrl",
    "sslcrldir",
    "sslsni",
    "requirepeer",
    "ssl_min_protocol_version",
    "ssl_max_protocol_version",
    "gssencmode",
    "krbsrvname",
    "gsslib",
    "gssdelegation",
    "target_session_attrs",
    "load_balance_hosts",
    "require_auth",
    "min_protocol_version",
    "max_protocol_version",
    "default_query_exec_mode",
    "statement_cache_capacity",
    "description_cache_capacity",
];

const HIDDEN: &str = "not shown, it may hold a password";

pub(crate) fn parse(s: &str) -> Result<Fields> {
    let t = s.trim();
    let fields = if t.starts_with("postgresql://") || t.starts_with("postgres://") {
        uri(t)?
    } else if !t.contains('=') {
        return Err(Error::config(
            "invalid connection string (not shown, it may hold a password): use postgresql://user@host:5432/dbname, host=... dbname=..., or Host=...;Database=...",
        ));
    } else if looks_like_ado(t) {
        ado(t)?
    } else {
        keyword(t)?
    };
    check(&fields)?;
    Ok(fields)
}

fn check(f: &Fields) -> Result<()> {
    if f.contains_key("service") {
        return Err(Error::config(
            "the connection string names service=, but pgmi reads no service files; spell out host, port and dbname",
        ));
    }
    if f.contains_key("hostaddr") {
        return Err(Error::config(
            "hostaddr is not supported; put the address in host",
        ));
    }
    for k in ["requirepeer", "sslcrl", "sslcrldir"] {
        if f.get(k).is_some_and(|v| !v.is_empty()) {
            return Err(Error::config(format!(
                "{k} is not supported; remove it from the connection string"
            )));
        }
    }
    for k in f.keys() {
        if !KNOWN.contains(&k.as_str()) {
            return Err(Error::config(format!("invalid connection option \"{k}\"")));
        }
    }
    if let Some(ports) = f.get("port")
        && ports
            .split(',')
            .map(str::trim)
            .any(|p| !p.is_empty() && p.parse::<u16>().ok().is_none_or(|n| n == 0))
    {
        return Err(Error::config(format!(
            "invalid port in connection string ({HIDDEN}): want a number from 1 to 65535"
        )));
    }
    Ok(())
}

fn looks_like_ado(s: &str) -> bool {
    let Some((first, _)) = s.split_once('=') else {
        return false;
    };
    s.contains(';') || first.contains(' ') || first.chars().any(|c| c.is_ascii_uppercase())
}

fn decode(s: &str) -> Result<String> {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' {
            let hex = s
                .get(i + 1..i + 3)
                .and_then(|h| u8::from_str_radix(h, 16).ok())
                .ok_or_else(|| Error::config("invalid percent-encoding in connection string"))?;
            out.push(hex);
            i += 3;
        } else {
            out.push(b[i]);
            i += 1;
        }
    }
    String::from_utf8(out).map_err(|_| Error::config("connection string is not valid UTF-8"))
}

fn uri(s: &str) -> Result<Fields> {
    let rest = s.split_once("://").map(|x| x.1).unwrap_or(s);
    let (before_query, query) = rest.split_once('?').unwrap_or((rest, ""));
    let (authority, path) = before_query.split_once('/').unwrap_or((before_query, ""));
    let (userinfo, hostlist) = match authority.rsplit_once('@') {
        Some((u, h)) => (u, h),
        None => ("", authority),
    };
    let mut out = Fields::new();
    if !userinfo.is_empty() {
        let (u, p) = match userinfo.split_once(':') {
            Some((u, p)) => (u, Some(p)),
            None => (userinfo, None),
        };
        if !u.is_empty() {
            out.insert("user".into(), decode(u)?);
        }
        if let Some(p) = p {
            out.insert("password".into(), decode(p)?);
        }
    }
    let (mut hosts, mut ports) = (vec![], vec![]);
    if !hostlist.is_empty() {
        for hp in hostlist.split(',') {
            let (h, p) = if let Some(r) = hp.strip_prefix('[') {
                let (h, after) = r
                    .split_once(']')
                    .ok_or_else(|| Error::config("unterminated [ in connection string host"))?;
                (
                    h.to_string(),
                    after.strip_prefix(':').unwrap_or("").to_string(),
                )
            } else {
                match hp.rsplit_once(':') {
                    Some((h, p)) => (decode(h)?, p.to_string()),
                    None => (decode(hp)?, String::new()),
                }
            };
            hosts.push(h);
            ports.push(p);
        }
        out.insert("host".into(), hosts.join(","));
        if ports.iter().any(|p| !p.is_empty()) {
            out.insert("port".into(), ports.join(","));
        }
    }
    if !path.is_empty() {
        out.insert("dbname".into(), decode(path)?);
    }
    for pair in query.split('&').filter(|p| !p.is_empty()) {
        let (k, v) = pair.split_once('=').unwrap_or((pair, ""));
        out.insert(decode(&k.replace('+', " "))?, decode(&v.replace('+', " "))?);
    }
    Ok(out)
}

fn keyword(s: &str) -> Result<Fields> {
    let mut out = Fields::new();
    let mut chars = s.chars().peekable();
    loop {
        while chars.next_if(|c| c.is_whitespace()).is_some() {}
        if chars.peek().is_none() {
            return Ok(out);
        }
        let key: String =
            std::iter::from_fn(|| chars.next_if(|c| *c != '=' && !c.is_whitespace())).collect();
        while chars.next_if(|c| c.is_whitespace()).is_some() {}
        if chars.next() != Some('=') {
            return Err(Error::config(format!(
                "invalid connection string ({HIDDEN}): a key has no \"=\" after it"
            )));
        }
        while chars.next_if(|c| c.is_whitespace()).is_some() {}
        let mut value = String::new();
        if chars.next_if_eq(&'\'').is_some() {
            loop {
                match chars.next() {
                    Some('\\') => value.extend(chars.next()),
                    Some('\'') => break,
                    Some(c) => value.push(c),
                    None => {
                        return Err(Error::config(
                            "invalid connection string: unterminated quoted value",
                        ));
                    }
                }
            }
        } else {
            while let Some(c) = chars.next_if(|c| !c.is_whitespace()) {
                if c == '\\' {
                    value.extend(chars.next());
                } else {
                    value.push(c);
                }
            }
        }
        out.insert(key, value);
    }
}

fn ado(s: &str) -> Result<Fields> {
    let mut out = Fields::new();
    for part in s.split(';').map(str::trim).filter(|p| !p.is_empty()) {
        let (k, v) = part.split_once('=').ok_or_else(|| {
            Error::config(format!(
                "invalid connection string ({HIDDEN}): a part has no \"=\""
            ))
        })?;
        let v = v.trim().to_string();
        let key = k.trim().to_ascii_lowercase().replace([' ', '_'], "");
        let name = match key.as_str() {
            "server" | "host" | "datasource" | "address" | "addr" => match v.rsplit_once(':') {
                Some((h, p)) if !h.contains(':') && p.chars().all(|c| c.is_ascii_digit()) => {
                    out.insert("host".into(), h.to_string());
                    out.insert("port".into(), p.to_string());
                    continue;
                }
                _ => "host",
            },
            "port" => "port",
            "userid" | "username" | "user" | "uid" => "user",
            "password" | "pwd" => "password",
            "database" | "initialcatalog" | "db" => "dbname",
            "sslmode" => {
                out.insert("sslmode".into(), npgsql_sslmode(&v)?);
                continue;
            }
            "applicationname" => "application_name",
            "timeout" | "connecttimeout" => "connect_timeout",
            "rootcertificate" | "sslrootcert" => "sslrootcert",
            "sslcertificate" | "sslcert" => "sslcert",
            "sslkey" => "sslkey",
            "sslpassword" => "sslpassword",
            "options" => "options",
            "targetsessionattributes" => "target_session_attrs",
            "channelbinding" => "channel_binding",
            _ => {
                return Err(Error::config(format!(
                    "invalid connection option \"{}\"",
                    k.trim()
                )));
            }
        };
        out.insert(name.into(), v);
    }
    Ok(out)
}

fn npgsql_sslmode(v: &str) -> Result<String> {
    let m = match v.to_ascii_lowercase().replace('-', "").as_str() {
        "disable" => "disable",
        "allow" => "allow",
        "prefer" => "prefer",
        "require" => "require",
        "verifyca" => "verify-ca",
        "verifyfull" => "verify-full",
        _ => return Err(Error::config(format!("invalid SSL Mode \"{v}\""))),
    };
    Ok(m.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn three_forms() {
        let u = parse("postgresql://u:p%40ss@h1:5432,[::1]:6000/db?sslmode=require").unwrap();
        assert_eq!(u["user"], "u");
        assert_eq!(u["password"], "p@ss");
        assert_eq!(u["host"], "h1,::1");
        assert_eq!(u["port"], "5432,6000");
        assert_eq!(u["dbname"], "db");
        assert_eq!(u["sslmode"], "require");
        let k = parse("host=h port=1 user=u password='a b' dbname=postgres").unwrap();
        assert_eq!(k["password"], "a b");
        let a = parse("Server=h;Port=7;User Id=u;Password=p;Database=postgres").unwrap();
        assert_eq!(
            (a["host"].as_str(), a["port"].as_str(), a["user"].as_str()),
            ("h", "7", "u")
        );
        let a = parse("Host=h:9;Username=u;SSL Mode=VerifyFull").unwrap();
        assert_eq!(
            (
                a["host"].as_str(),
                a["port"].as_str(),
                a["sslmode"].as_str()
            ),
            ("h", "9", "verify-full")
        );
    }

    #[test]
    fn refusals() {
        assert!(parse("service=x").unwrap_err().text().contains("service"));
        let e = parse("postgresql://h/db?bogus=1").unwrap_err();
        assert_eq!(e.text(), "invalid connection option \"bogus\"");
        assert!(parse("postgresql://h/db?statement_cache_capacity=0").is_ok());
        for k in ["requirepeer", "sslcrl", "sslcrldir"] {
            let e = parse(&format!("host=h {k}=x")).unwrap_err();
            assert!(e.text().starts_with(k), "{}", e.text());
            assert!(parse(&format!("host=h {k}=")).is_ok());
        }
    }

    fn fields(s: &str) -> Vec<(String, String)> {
        parse(s)
            .unwrap_or_else(|e| panic!("{s}: {}", e.text()))
            .into_iter()
            .collect()
    }

    fn pairs(p: &[(&str, &str)]) -> Vec<(String, String)> {
        let mut v: Vec<_> = p
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect();
        v.sort();
        v
    }

    #[test]
    fn ado_net_forms() {
        for (input, want) in [
            (
                "Host=localhost;Port=6000;Database=postgres;Username=postgres;Password=postgres",
                &[
                    ("host", "localhost"),
                    ("port", "6000"),
                    ("dbname", "postgres"),
                    ("user", "postgres"),
                    ("password", "postgres"),
                ][..],
            ),
            (
                "Server=localhost;Port=5432;Database=mydb;User Id=user;Pwd=pass",
                &[
                    ("host", "localhost"),
                    ("port", "5432"),
                    ("dbname", "mydb"),
                    ("user", "user"),
                    ("password", "pass"),
                ],
            ),
            (
                "Host=localhost;Database=mydb;Username=user;SSL Mode=require",
                &[
                    ("host", "localhost"),
                    ("dbname", "mydb"),
                    ("user", "user"),
                    ("sslmode", "require"),
                ],
            ),
            (
                "Host = localhost ; Port = 5432 ; Database = mydb ; Username = user",
                &[
                    ("host", "localhost"),
                    ("port", "5432"),
                    ("dbname", "mydb"),
                    ("user", "user"),
                ],
            ),
            (
                "Host=localhost;Database=mydb;Username=user;sslcert=/path/client.crt;sslkey=/path/client.key;sslrootcert=/path/ca.crt;sslpassword=keypass",
                &[
                    ("host", "localhost"),
                    ("dbname", "mydb"),
                    ("user", "user"),
                    ("sslcert", "/path/client.crt"),
                    ("sslkey", "/path/client.key"),
                    ("sslrootcert", "/path/ca.crt"),
                    ("sslpassword", "keypass"),
                ],
            ),
            (
                "Host=localhost;Database=mydb;SSL Cert=/path/client.crt;SSL Key=/path/client.key;SSL Root Cert=/path/ca.crt;SSL Password=keypass",
                &[
                    ("host", "localhost"),
                    ("dbname", "mydb"),
                    ("sslcert", "/path/client.crt"),
                    ("sslkey", "/path/client.key"),
                    ("sslrootcert", "/path/ca.crt"),
                    ("sslpassword", "keypass"),
                ],
            ),
            (
                "Password=it's;Host=db;Database=app",
                &[("password", "it's"), ("host", "db"), ("dbname", "app")],
            ),
        ] {
            assert_eq!(fields(input), pairs(want), "{input}");
        }
    }

    #[test]
    fn npgsql_ssl_mode_spellings() {
        for (given, want) in [
            ("Require", "require"),
            ("VerifyFull", "verify-full"),
            ("VerifyCA", "verify-ca"),
            ("Disable", "disable"),
            ("verify-full", "verify-full"),
        ] {
            let f = parse(&format!("Host=h;Database=d;SSL Mode={given}")).unwrap();
            assert_eq!(f["sslmode"], want, "{given}");
        }
        assert_eq!(
            parse("Host=h;SSL Mode=Sometimes").unwrap_err().text(),
            "invalid SSL Mode \"Sometimes\""
        );
        for key in ["Pooling", "Trust Server Certificate"] {
            let e = parse(&format!("Host=h;Database=d;{key}=true")).unwrap_err();
            assert_eq!(e.exit_code(), 10);
            assert_eq!(e.text(), format!("invalid connection option \"{key}\""));
        }
    }

    #[test]
    fn keyword_value_form() {
        assert_eq!(
            fields("host=db1 port=6000 dbname=app user=me password=secret sslmode=require"),
            pairs(&[
                ("host", "db1"),
                ("port", "6000"),
                ("dbname", "app"),
                ("user", "me"),
                ("password", "secret"),
                ("sslmode", "require"),
            ])
        );
        assert_eq!(
            fields(r"host=db1 password='a b\'c=d' dbname=app"),
            pairs(&[("host", "db1"), ("password", "a b'c=d"), ("dbname", "app")])
        );
        assert_eq!(
            fields("host=db1,db2 port=5432,6000 dbname=app")[1],
            ("host".to_string(), "db1,db2".to_string())
        );
        for bad in ["host=db1 dbname", "host=db1 password='unterminated"] {
            assert_eq!(parse(bad).unwrap_err().exit_code(), 10, "{bad}");
        }
        assert!(parse("host=db hostaddr=10.0.0.1 dbname=app").is_err());
    }

    #[test]
    fn keyword_value_refuses_an_unknown_key_by_name() {
        for (input, key) in [
            ("host=db bogus=1", "bogus"),
            ("host=db Password=x", "Password"),
            ("host=db dbanme=app", "dbanme"),
        ] {
            assert_eq!(
                parse(input).unwrap_err().text(),
                format!("invalid connection option \"{key}\"")
            );
        }
    }

    #[test]
    fn keyword_value_passes_libpq_keywords() {
        let f = parse("host=a,b target_session_attrs=read-write options='-c search_path=app' gssencmode=disable channel_binding=require keepalives_idle=30").unwrap();
        for (k, v) in [
            ("target_session_attrs", "read-write"),
            ("options", "-c search_path=app"),
            ("gssencmode", "disable"),
            ("channel_binding", "require"),
            ("keepalives_idle", "30"),
            ("host", "a,b"),
        ] {
            assert_eq!(f[k], v, "{k}");
        }
    }

    #[test]
    fn keyword_value_accepts_pgx_exec_options() {
        let f = parse(
            "host=db default_query_exec_mode=exec statement_cache_capacity=0 description_cache_capacity=0",
        )
        .unwrap();
        for (k, v) in [
            ("default_query_exec_mode", "exec"),
            ("statement_cache_capacity", "0"),
            ("description_cache_capacity", "0"),
        ] {
            assert_eq!(f[k], v, "{k}");
            assert!(KNOWN.contains(&k), "{k}");
        }
    }

    #[test]
    fn a_bad_port_is_refused() {
        for bad in [
            "postgresql://localhost:abc/mydb",
            "Host=localhost;Port=abc;Database=mydb",
        ] {
            let e = parse(bad).unwrap_err();
            assert!(
                e.text().starts_with("invalid port in connection string"),
                "{bad}: {}",
                e.text()
            );
        }
        assert!(parse("").is_err());
    }

    #[test]
    fn errors_never_echo_the_input() {
        for (input, secret) in [
            ("postgres:/admin:S3cretPw@db/x", "S3cretPw"),
            ("Host=h;Password=ab;cdSecret", "cdSecret"),
            ("postgresql://u:Qz/ss@h/db", "Qz"),
            ("postgresql://u:Secret9/x@h:5432/db", "Secret9"),
            ("postgresql://u@h:Secret9/db", "Secret9"),
            ("host=h password=S3cret junkSecret", "junkSecret"),
            ("host=h port=Secret9", "Secret9"),
            ("Host=h;Password=x;Port=Secret9", "Secret9"),
        ] {
            let e = parse(input).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{input}");
            assert!(!e.text().contains(secret), "{input}: {}", e.text());
        }
    }
}
