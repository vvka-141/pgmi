use std::collections::BTreeMap;

use std::fmt;

use crate::error::{Error, Kind};

pub(crate) type Fields = BTreeMap<String, String>;

pub(crate) const KNOWN: &[&str] = &[
    "host",
    "hostaddr",
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

/// Why a connection string was refused. The text never shows the string
/// itself, which may hold a password.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum ParseError {
    UnknownForm,
    ServiceFile,
    Unsupported(&'static str),
    UnknownKey(String),
    InvalidPort,
    PercentEncoding,
    NotUtf8,
    UnterminatedBracket,
    KeyWithoutEquals,
    UnterminatedQuote,
    TextAfterQuote,
    PartWithoutEquals,
    SslMode(String),
}

impl fmt::Display for ParseError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ParseError::UnknownForm => write!(
                f,
                "invalid connection string ({HIDDEN}): use postgresql://user@host:5432/dbname, host=... dbname=..., or Host=...;Database=..."
            ),
            ParseError::ServiceFile => f.write_str(
                "the connection string names service=, but pgmi reads no service files; spell out host, port and dbname",
            ),
            ParseError::Unsupported(k) => {
                write!(f, "{k} is not supported; remove it from the connection string")
            }
            ParseError::UnknownKey(k) => write!(f, "invalid connection option \"{k}\""),
            ParseError::InvalidPort => write!(
                f,
                "invalid port in connection string ({HIDDEN}): want a number from 1 to 65535"
            ),
            ParseError::PercentEncoding => {
                f.write_str("invalid percent-encoding in connection string")
            }
            ParseError::NotUtf8 => f.write_str("connection string is not valid UTF-8"),
            ParseError::UnterminatedBracket => {
                f.write_str("unterminated [ in connection string host")
            }
            ParseError::KeyWithoutEquals => write!(
                f,
                "invalid connection string ({HIDDEN}): a key has no \"=\" after it"
            ),
            ParseError::UnterminatedQuote => {
                f.write_str("invalid connection string: unterminated quoted value")
            }
            ParseError::TextAfterQuote => write!(
                f,
                "invalid connection string ({HIDDEN}): text after a closing quote; put the whole value inside the quotes"
            ),
            ParseError::PartWithoutEquals => write!(
                f,
                "invalid connection string ({HIDDEN}): a part has no \"=\""
            ),
            ParseError::SslMode(v) => write!(f, "invalid SSL Mode \"{v}\""),
        }
    }
}

impl std::error::Error for ParseError {}

impl From<ParseError> for Error {
    fn from(e: ParseError) -> Self {
        Error::new(Kind::Config, e.to_string())
    }
}

type Result<T> = std::result::Result<T, ParseError>;

pub(crate) fn parse(s: &str) -> Result<Fields> {
    let t = s.trim();
    let fields = if t.starts_with("postgresql://") || t.starts_with("postgres://") {
        uri(t)?
    } else if !t.contains('=') {
        return Err(ParseError::UnknownForm);
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
        return Err(ParseError::ServiceFile);
    }
    for k in ["requirepeer", "sslcrl", "sslcrldir"] {
        if f.get(k).is_some_and(|v| !v.is_empty()) {
            return Err(ParseError::Unsupported(k));
        }
    }
    if let Some(k) = f.keys().find(|k| !KNOWN.contains(&k.as_str())) {
        return Err(ParseError::UnknownKey(k.clone()));
    }
    if let Some(ports) = f.get("port")
        && ports
            .split(',')
            .map(str::trim)
            .any(|p| !p.is_empty() && p.parse::<u16>().ok().is_none_or(|n| n == 0))
    {
        return Err(ParseError::InvalidPort);
    }
    Ok(())
}

fn looks_like_ado(s: &str) -> bool {
    let Some((first, _)) = s.split_once('=') else {
        return false;
    };
    let first = first.trim();
    unquoted_semicolon(s) || first.contains(' ') || first.chars().any(|c| c.is_ascii_uppercase())
}

fn unquoted_semicolon(s: &str) -> bool {
    let mut chars = s.chars();
    let mut value_start = false;
    while let Some(c) = chars.next() {
        match c {
            ';' => return true,
            '=' => value_start = true,
            '\'' if value_start => {
                while let Some(q) = chars.next() {
                    match q {
                        '\\' => {
                            chars.next();
                        }
                        '\'' => break,
                        _ => {}
                    }
                }
                value_start = false;
            }
            c if c.is_whitespace() => {}
            '\\' => {
                value_start = false;
                chars.next();
            }
            _ => value_start = false,
        }
    }
    false
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
                .ok_or(ParseError::PercentEncoding)?;
            out.push(hex);
            i += 3;
        } else {
            out.push(b[i]);
            i += 1;
        }
    }
    String::from_utf8(out).map_err(|_| ParseError::NotUtf8)
}

fn uri(s: &str) -> Result<Fields> {
    let rest = s.split_once("://").map_or(s, |x| x.1);
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
                let (h, after) = r.split_once(']').ok_or(ParseError::UnterminatedBracket)?;
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
            return Err(ParseError::KeyWithoutEquals);
        }
        while chars.next_if(|c| c.is_whitespace()).is_some() {}
        let mut value = String::new();
        if chars.next_if_eq(&'\'').is_some() {
            loop {
                match chars.next() {
                    Some('\\') => value.extend(chars.next()),
                    Some('\'') => break,
                    Some(c) => value.push(c),
                    None => return Err(ParseError::UnterminatedQuote),
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

fn ado_pairs(s: &str) -> Result<Vec<(String, String)>> {
    let mut out = Vec::new();
    let mut chars = s.chars().peekable();
    loop {
        while chars.next_if(|c| c.is_whitespace() || *c == ';').is_some() {}
        if chars.peek().is_none() {
            return Ok(out);
        }
        let key: String =
            std::iter::from_fn(|| chars.next_if(|c| *c != '=' && *c != ';')).collect();
        if chars.next() != Some('=') {
            return Err(ParseError::PartWithoutEquals);
        }
        while chars.next_if(|c| c.is_whitespace()).is_some() {}
        let value = match chars.next_if(|c| *c == '\'' || *c == '"') {
            Some(quote) => {
                let mut value = String::new();
                loop {
                    match chars.next() {
                        Some(c) if c == quote => {
                            if chars.next_if_eq(&quote).is_none() {
                                break;
                            }
                            value.push(quote);
                        }
                        Some(c) => value.push(c),
                        None => return Err(ParseError::UnterminatedQuote),
                    }
                }
                while chars.next_if(|c| c.is_whitespace()).is_some() {}
                if chars.next_if(|c| *c != ';').is_some() {
                    return Err(ParseError::TextAfterQuote);
                }
                value
            }
            None => std::iter::from_fn(|| chars.next_if(|c| *c != ';'))
                .collect::<String>()
                .trim()
                .to_string(),
        };
        out.push((key.trim().to_string(), value));
    }
}

fn ado(s: &str) -> Result<Fields> {
    let mut out = Fields::new();
    let mut host_port = None;
    for (k, v) in ado_pairs(s)? {
        let key = k.to_ascii_lowercase().replace([' ', '_'], "");
        let name = match key.as_str() {
            "server" | "host" | "datasource" | "address" | "addr" => match v.rsplit_once(':') {
                Some((h, p))
                    if !h.contains([':', ',']) && p.chars().all(|c| c.is_ascii_digit()) =>
                {
                    out.insert("host".into(), h.to_string());
                    host_port = Some(p.to_string());
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
            _ => return Err(ParseError::UnknownKey(k)),
        };
        out.insert(name.into(), v);
    }
    if let Some(p) = host_port {
        out.insert("port".into(), p);
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
        _ => return Err(ParseError::SslMode(v.to_string())),
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
        assert_eq!(parse("service=x"), Err(ParseError::ServiceFile));
        assert!(ParseError::ServiceFile.to_string().contains("service"));
        let e = parse("postgresql://h/db?bogus=1").unwrap_err();
        assert_eq!(e, ParseError::UnknownKey("bogus".into()));
        assert_eq!(e.to_string(), "invalid connection option \"bogus\"");
        assert!(parse("postgresql://h/db?statement_cache_capacity=0").is_ok());
        for k in ["requirepeer", "sslcrl", "sslcrldir"] {
            let e = parse(&format!("host=h {k}=x")).unwrap_err();
            assert_eq!(e, ParseError::Unsupported(k));
            assert!(e.to_string().starts_with(k), "{e}");
            assert!(parse(&format!("host=h {k}=")).is_ok());
        }
    }

    fn fields(s: &str) -> Vec<(String, String)> {
        parse(s)
            .unwrap_or_else(|e| panic!("{s}: {e}"))
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
    fn ado_net_quoted_values_keep_semicolons_and_doubled_quotes() {
        assert_eq!(
            fields("Host=localhost;Database=mydb;Username=user;Password='p;a=ss'"),
            pairs(&[
                ("host", "localhost"),
                ("dbname", "mydb"),
                ("user", "user"),
                ("password", "p;a=ss"),
            ])
        );
        assert_eq!(
            fields(r#"Host=localhost;Database=mydb;Username=user;Password="a""b;c""#),
            pairs(&[
                ("host", "localhost"),
                ("dbname", "mydb"),
                ("user", "user"),
                ("password", r#"a"b;c"#),
            ])
        );
        assert_eq!(
            fields("Password='p;a=ss'"),
            pairs(&[("password", "p;a=ss")])
        );
        assert_eq!(
            parse("Host=h;Password='p;a"),
            Err(ParseError::UnterminatedQuote)
        );
        assert_eq!(
            parse("Host=h;Password='pw'tail;Database=d"),
            Err(ParseError::TextAfterQuote)
        );
    }

    #[test]
    fn a_quoted_ado_net_value_is_never_read_as_a_key_or_echoed() {
        for (input, secret) in [
            ("Host=h;Database=x;Password='p;SecretTail=ss'", None),
            (
                "Host=h;Database=x;Password='p;SecretTail=ss",
                Some("SecretTail"),
            ),
            (
                "Host=h;Database=x;Password='p;SecretTail=ss'junk",
                Some("SecretTail"),
            ),
            (
                "Host=h;Database=x;Password=\"p;SecretTail\"\"=ss",
                Some("SecretTail"),
            ),
        ] {
            match (parse(input), secret) {
                (Ok(f), None) => assert_eq!(f["password"], "p;SecretTail=ss"),
                (Err(e), Some(secret)) => {
                    let text = Error::from(e).text();
                    assert!(!text.contains(secret), "{input}: {text}");
                }
                (r, _) => panic!("{input}: {r:?}"),
            }
        }
    }

    #[test]
    fn a_port_in_the_ado_net_host_wins_wherever_port_stands() {
        for input in [
            "Host=127.0.0.1:5433;Port=5999;Database=d;Username=u",
            "Port=5999;Host=127.0.0.1:5433;Database=d;Username=u",
        ] {
            let f = parse(input).unwrap();
            assert_eq!(
                (f["host"].as_str(), f["port"].as_str()),
                ("127.0.0.1", "5433"),
                "{input}"
            );
        }
        let f = parse("Host=a,b:5433;Database=d").unwrap();
        assert_eq!(f["host"], "a,b:5433");
        assert!(!f.contains_key("port"));
    }

    #[test]
    fn keyword_value_allows_spaces_around_equals_and_semicolons_in_quotes() {
        assert_eq!(
            fields("host = db1   dbname = app"),
            pairs(&[("host", "db1"), ("dbname", "app")])
        );
        assert_eq!(
            fields(r"host=db1 password='a b\'c;d' dbname=app"),
            pairs(&[("host", "db1"), ("password", "a b'c;d"), ("dbname", "app")])
        );
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
        let e = parse("Host=h;SSL Mode=Sometimes").unwrap_err();
        assert_eq!(e, ParseError::SslMode("Sometimes".into()));
        assert_eq!(e.to_string(), "invalid SSL Mode \"Sometimes\"");
        for key in ["Pooling", "Trust Server Certificate"] {
            let e = parse(&format!("Host=h;Database=d;{key}=true")).unwrap_err();
            assert_eq!(e, ParseError::UnknownKey(key.into()));
            assert_eq!(Error::from(e).exit_code(), 10);
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
        assert_eq!(parse("host=db1 dbname"), Err(ParseError::KeyWithoutEquals));
        assert_eq!(
            parse("host=db1 password='unterminated"),
            Err(ParseError::UnterminatedQuote)
        );
        assert_eq!(
            fields("host=db hostaddr=10.0.0.1 dbname=app"),
            pairs(&[("host", "db"), ("hostaddr", "10.0.0.1"), ("dbname", "app")])
        );
    }

    #[test]
    fn keyword_value_refuses_an_unknown_key_by_name() {
        for (input, key) in [
            ("host=db bogus=1", "bogus"),
            ("host=db Password=x", "Password"),
            ("host=db dbanme=app", "dbanme"),
        ] {
            assert_eq!(
                parse(input).unwrap_err().to_string(),
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
            assert_eq!(parse(bad), Err(ParseError::InvalidPort), "{bad}");
        }
        assert_eq!(parse(""), Err(ParseError::UnknownForm));
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
            let text = e.to_string();
            assert_eq!(Error::from(e).exit_code(), 10, "{input}");
            assert!(!text.contains(secret), "{input}: {text}");
        }
    }
}
