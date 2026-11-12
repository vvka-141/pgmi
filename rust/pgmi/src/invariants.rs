//! Invariants of the parsers that read untrusted text: connection strings,
//! SQL (lexer, statement splitter, `pgmi_test` macros), `<pgmi-meta>` blocks,
//! `.env` files and `.pgpass` lines. The proptests below and the cargo-fuzz
//! targets in `rust/pgmi/fuzz` run the same checks. A check returns the first
//! invariant its input breaks; a panic inside one is a panic in the parser.

use std::fmt::Write as _;

use crate::config::dotenv::{self, Mode};
use crate::conn::{parse as conn, pgpass};
use crate::deploy::script;
use crate::error::Error;
use crate::project::{lexer, metadata};

pub type Checked = Result<(), String>;

macro_rules! ensure {
    ($cond:expr, $($fmt:tt)+) => {
        if !$cond {
            return Err(format!($($fmt)+));
        }
    };
}

/// Any text given as a connection string is parsed or refused with exit 10,
/// and an accepted string holds known keys only.
pub fn conn_text(input: &str) -> Checked {
    match conn::parse(input) {
        Ok(fields) => {
            let unknown = fields.keys().find(|k| !conn::KNOWN.contains(&k.as_str()));
            ensure!(unknown.is_none(), "accepted the unknown key {unknown:?}");
        }
        Err(e) => {
            let code = Error::from(e).exit_code();
            ensure!(
                code == 10,
                "a refused connection string exits {code}, not 10"
            );
        }
    }
    Ok(())
}

const SSLMODES: [&str; 6] = [
    "disable",
    "allow",
    "prefer",
    "require",
    "verify-ca",
    "verify-full",
];

/// The settings of one connection, written by [`conn_forms`] as a URI, as
/// libpq keyword/value pairs and as an ADO.NET string.
#[derive(Debug, Clone, Default)]
pub struct Conn {
    pub hosts: Vec<(String, Option<u16>)>,
    pub dbname: String,
    pub user: String,
    pub password: String,
    pub sslmode: Option<u8>,
    pub application_name: Option<String>,
}

impl Conn {
    fn normalized(&self) -> Conn {
        let mut hosts: Vec<(String, Option<u16>)> = self
            .hosts
            .iter()
            .take(3)
            .map(|(h, p)| {
                let h: String = h
                    .chars()
                    .filter(|c| {
                        c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, '.' | '-')
                    })
                    .collect();
                (
                    if h.is_empty() { "h".into() } else { h },
                    p.map(|p| p.max(1)),
                )
            })
            .collect();
        if hosts.is_empty() {
            hosts.push(("h".into(), None));
        }
        let or = |s: &str, d: &str| {
            if s.is_empty() {
                d.to_string()
            } else {
                s.to_string()
            }
        };
        Conn {
            hosts,
            dbname: or(&self.dbname, "d"),
            user: or(&self.user, "u"),
            password: format!("Zq{}qZ", self.password),
            sslmode: self
                .sslmode
                .map(|m| u8::try_from(SSLMODES.len()).map_or(m, |n| m % n)),
            application_name: self.application_name.clone(),
        }
    }

    fn sslmode(&self) -> Option<&'static str> {
        self.sslmode
            .map(|m| SSLMODES[usize::from(m) % SSLMODES.len()])
    }

    fn ports(&self) -> Option<String> {
        self.hosts.iter().any(|(_, p)| p.is_some()).then(|| {
            let ports: Vec<String> = self
                .hosts
                .iter()
                .map(|(_, p)| p.map(|p| p.to_string()).unwrap_or_default())
                .collect();
            ports.join(",")
        })
    }

    fn host(&self) -> String {
        let hosts: Vec<&str> = self.hosts.iter().map(|(h, _)| h.as_str()).collect();
        hosts.join(",")
    }

    fn expected(&self) -> conn::Fields {
        let mut f = conn::Fields::new();
        f.insert("host".into(), self.host());
        f.insert("dbname".into(), self.dbname.clone());
        f.insert("user".into(), self.user.clone());
        f.insert("password".into(), self.password.clone());
        if let Some(p) = self.ports() {
            f.insert("port".into(), p);
        }
        if let Some(m) = self.sslmode() {
            f.insert("sslmode".into(), m.into());
        }
        if let Some(a) = &self.application_name {
            f.insert("application_name".into(), a.clone());
        }
        f
    }

    fn uri(&self) -> String {
        let hosts: Vec<String> = self
            .hosts
            .iter()
            .map(|(h, p)| match p {
                Some(p) => format!("{}:{p}", percent(h)),
                None => percent(h),
            })
            .collect();
        let mut s = format!(
            "postgresql://{}:{}@{}/{}",
            percent(&self.user),
            percent(&self.password),
            hosts.join(","),
            percent(&self.dbname)
        );
        let mut query = Vec::new();
        if let Some(m) = self.sslmode() {
            query.push(format!("sslmode={m}"));
        }
        if let Some(a) = &self.application_name {
            query.push(format!("application_name={}", percent(a)));
        }
        if !query.is_empty() {
            let _ = write!(s, "?{}", query.join("&"));
        }
        s
    }

    fn keyword(&self) -> String {
        let q = |v: &str| format!("'{}'", v.replace('\\', "\\\\").replace('\'', "\\'"));
        let mut s = format!(
            "host={} user={} password={} dbname={}",
            q(&self.host()),
            q(&self.user),
            q(&self.password),
            q(&self.dbname)
        );
        if let Some(p) = self.ports() {
            let _ = write!(s, " port={}", q(&p));
        }
        if let Some(m) = self.sslmode() {
            let _ = write!(s, " sslmode={m}");
        }
        if let Some(a) = &self.application_name {
            let _ = write!(s, " application_name={}", q(a));
        }
        s
    }

    fn ado(&self) -> String {
        let q = |v: &str| format!("\"{}\"", v.replace('"', "\"\""));
        let mut s = format!(
            "Host={};Username={};Password={};Database={}",
            self.host(),
            q(&self.user),
            q(&self.password),
            q(&self.dbname)
        );
        if let Some(p) = self.ports() {
            let _ = write!(s, ";Port={p}");
        }
        if let Some(m) = self.sslmode() {
            let _ = write!(s, ";SSL Mode={m}");
        }
        if let Some(a) = &self.application_name {
            let _ = write!(s, ";Application Name={}", q(a));
        }
        s
    }
}

fn percent(s: &str) -> String {
    s.bytes().fold(String::new(), |mut out, b| {
        if b.is_ascii_alphanumeric() {
            out.push(char::from(b));
        } else {
            let _ = write!(out, "%{b:02X}");
        }
        out
    })
}

const SUFFIX: &str = "Zq7suffix";

/// The URI, keyword/value and ADO.NET forms of the same settings parse to the
/// same fields, and no refusal of a damaged form shows the password or a
/// key written after it, which may be the password's unquoted rest.
pub fn conn_forms(settings: &Conn) -> Checked {
    let c = settings.normalized();
    let want = c.expected();
    let (uri, keyword, ado) = (c.uri(), c.keyword(), c.ado());
    for (form, text) in [
        ("URI", &uri),
        ("keyword/value", &keyword),
        ("ADO.NET", &ado),
    ] {
        let got = conn::parse(text).map_err(|e| format!("{form} {text:?}: {e}"))?;
        ensure!(
            got == want,
            "{form} {text:?} parsed to {got:?}, want {want:?}"
        );
    }
    let sep = if uri.contains('?') { '&' } else { '?' };
    let refused = [
        format!("{uri}{sep}bogus=1"),
        format!("{uri}{sep}port=99999"),
        format!("{keyword} bogus=1"),
        format!("{keyword} port=99999"),
        format!("{keyword} dangling"),
        format!("{keyword} options='"),
        format!("{ado};Bogus=1"),
        format!("{ado};Port=99999"),
        format!("{ado};SSL Mode=Sometimes"),
        format!("{ado};Options=\""),
    ];
    let raw = [
        format!("postgresql://u:{}@h:99999/d", c.password),
        format!("host=h password={} dbname=d port=x", c.password),
        format!("Host=h;Password={};Database=d;Port=x", c.password),
        format!("postgresql://h/d?password={}&{SUFFIX}=x", c.password),
        format!("host=h dbname=d password={} {SUFFIX}=x", c.password),
        format!("Host=h;Database=d;Password={};{SUFFIX}=x", c.password),
    ];
    for (text, must_fail) in refused
        .iter()
        .map(|t| (t, true))
        .chain(raw.iter().map(|t| (t, false)))
    {
        match conn::parse(text) {
            Ok(f) => ensure!(!must_fail, "{text:?} was accepted as {f:?}"),
            Err(e) => {
                let shown = Error::from(e).text();
                ensure!(
                    !shown.contains(&c.password) && !shown.contains(SUFFIX),
                    "{text:?} was refused with the password in {shown:?}"
                );
            }
        }
    }
    Ok(())
}

/// Tokens tile the input; on UTF-8 input every token ends on a character
/// boundary, and the script checks of [`sql_text`] hold.
pub fn sql_bytes(src: &[u8]) -> Checked {
    let toks = lexer::tokens(src);
    let mut at = 0;
    for t in &toks {
        ensure!(
            t.start == at && t.end > t.start,
            "token {t:?} does not continue at {at}"
        );
        at = t.end;
    }
    ensure!(at == src.len(), "the tokens end at {at} of {}", src.len());
    let Ok(text) = std::str::from_utf8(src) else {
        return Ok(());
    };
    ensure!(
        toks.iter().all(|t| text.is_char_boundary(t.end)),
        "a token ends inside a character"
    );
    sql_text(text)
}

/// Statements tile the script and end at a semicolon; the units `split`
/// returns are slices of the script in order, the head first; `pgmi_test`
/// macros are ordered `CALL ...)` spans, and `expand` replaces exactly those
/// spans and maps every expanded line back to a line of the script.
pub fn sql_text(src: &str) -> Checked {
    let stmts = script::statements(src);
    let mut at = 0;
    for (i, (s, e)) in stmts.iter().enumerate() {
        ensure!(
            *s == at && e > s,
            "statement {i} ({s}..{e}) does not continue at {at}"
        );
        ensure!(
            i + 1 == stmts.len() || src.as_bytes().get(e - 1) == Some(&b';'),
            "statement {i} ends without a semicolon"
        );
        at = *e;
    }
    ensure!(
        at == src.len(),
        "the statements end at {at} of {}",
        src.len()
    );

    let units = script::split(src);
    let mut end = 0;
    for (i, u) in units.iter().enumerate() {
        ensure!(
            i > 0 || u.start == 0,
            "the first unit starts at {}",
            u.start
        );
        ensure!(
            u.start >= end,
            "unit {i} starts at {} inside the unit before",
            u.start
        );
        let slice = src.get(u.start..u.start + u.text.len());
        ensure!(
            slice == Some(u.text.as_str()),
            "unit {i} is not the script text at {}",
            u.start
        );
        end = u.start + u.text.len();
    }

    let found = script::macros(src);
    let mut end = 0;
    for m in &found {
        let text = src.get(m.start..m.end).unwrap_or_default();
        ensure!(
            m.start >= end && !text.is_empty(),
            "macro {m:?} overlaps the one before"
        );
        ensure!(
            text.get(..4)
                .is_some_and(|c| c.eq_ignore_ascii_case("CALL"))
                && text.ends_with(')'),
            "macro span {text:?} is not CALL ...)"
        );
        end = m.end;
    }
    let generated: Vec<String> = (0..found.len()).map(|i| format!("G{i}\nG")).collect();
    let (out, lines) = script::expand(src, &found, &generated);
    let mut want = String::new();
    let mut at = 0;
    for (m, g) in found.iter().zip(&generated) {
        want.push_str(&src[at..m.start]);
        want.push_str(g);
        at = m.end;
    }
    want.push_str(&src[at..]);
    ensure!(out == want, "expand produced {out:?}, want {want:?}");
    let src_lines = src.bytes().filter(|b| *b == b'\n').count() + 1;
    let out_lines = out.bytes().filter(|b| *b == b'\n').count() + 1;
    let mut prev = 0;
    for l in 1..=out_lines {
        let (orig, _) = lines.original(l);
        ensure!(
            (1..=src_lines).contains(&orig) && orig >= prev,
            "expanded line {l} maps to {orig} (script has {src_lines}, previous {prev})"
        );
        prev = orig;
    }
    Ok(())
}

struct Fragment {
    text: &'static str,
    call: usize,
    commits: bool,
}

const fn frag(text: &'static str, call: usize, commits: bool) -> Fragment {
    Fragment {
        text,
        call,
        commits,
    }
}

const FRAGMENTS: [Fragment; 21] = [
    frag("SELECT 1;", 0, false),
    frag("BEGIN;", 0, false),
    frag("CALL pgmi_test();", 16, false),
    frag("call pg_temp.PGMI_TEST('x''y', 'cb');", 36, false),
    frag("CALL pgmi_test(NULL)\n;", 20, false),
    frag("COMMIT;", 0, true),
    frag("end work and no chain;", 0, true),
    frag("ROLLBACK;", 0, true),
    frag("ROLLBACK TO SAVEPOINT a;", 0, false),
    frag("COMMIT PREPARED 'x';", 0, false),
    frag("SELECT 'CALL pgmi_test(); COMMIT;';", 0, false),
    frag("-- CALL pgmi_test(); COMMIT;\n", 0, false),
    frag(
        "/* CALL pgmi_test(); /* COMMIT; */ CALL pgmi_test(); */",
        0,
        false,
    ),
    frag("SELECT $q$ CALL pgmi_test(); COMMIT; $q$;", 0, false),
    frag("SELECT $$CALL pgmi_test();$$;", 0, false),
    frag("SELECT E'\\' CALL pgmi_test(); COMMIT;';", 0, false),
    frag("SELECT \"CALL pgmi_test(); COMMIT;\";", 0, false),
    frag("SELECT U&'CALL pgmi_test(); COMMIT;';", 0, false),
    frag(
        "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT CASE WHEN true THEN 1 END; COMMIT; END;",
        0,
        false,
    ),
    frag("DO $x$ BEGIN PERFORM 1; COMMIT; END $x$;", 0, false),
    frag("CALL pgmi_test('a', 'b', 'c');", 0, false),
];

const SEPARATORS: [&str; 5] = [" ", "\n", "\t", "\r\n", "\x0c"];

/// A script assembled from known statements, one per byte of `picks`: the
/// macros found are exactly the visible `CALL pgmi_test(...)` statements,
/// never one inside a literal, identifier or comment, and the head ends at
/// the first top-level transaction terminator.
pub fn sql_fragments(picks: &[u8]) -> Checked {
    let mut src = String::new();
    let mut calls = Vec::new();
    let mut head = None;
    for &b in picks {
        let f = &FRAGMENTS[usize::from(b) % FRAGMENTS.len()];
        let start = src.len();
        src.push_str(f.text);
        if f.call > 0 {
            calls.push((start, start + f.call));
        }
        if f.commits && head.is_none() {
            head = Some(src.len());
        }
        src.push_str(SEPARATORS[usize::from(b) / FRAGMENTS.len() % SEPARATORS.len()]);
    }
    let found: Vec<(usize, usize)> = script::macros(&src)
        .iter()
        .map(|m| (m.start, m.end))
        .collect();
    ensure!(
        found == calls,
        "{src:?}: macros at {found:?}, want {calls:?}"
    );
    let units = script::split(&src);
    if let Some(end) = head {
        let first = units.first().map(|u| (u.start, u.text.len()));
        ensure!(
            first == Some((0, end)),
            "{src:?}: head {first:?}, want (0, {end})"
        );
    } else {
        ensure!(
            units.len() <= 1,
            "{src:?}: {} units without a terminator",
            units.len()
        );
    }
    sql_text(&src)
}

const META_PATH: &str = "./f.sql";

/// A file is read as no block, one valid block, or an error naming the file.
pub fn metadata_text(content: &str) -> Checked {
    match metadata::parse(META_PATH, content) {
        Ok(None) => {}
        Ok(Some(m)) => {
            let id = uuid::Uuid::parse_str(&m.id).map_err(|e| format!("id {:?}: {e}", m.id))?;
            ensure!(
                !id.is_nil() && id.hyphenated().to_string() == m.id,
                "id {:?} is not a canonical, non-nil UUID",
                m.id
            );
            ensure!(
                m.sort_keys.iter().all(|k| !k.trim().is_empty()),
                "a blank sort key was accepted: {:?}",
                m.sort_keys
            );
        }
        Err(e) => {
            let text = e.to_string();
            ensure!(text.contains(META_PATH), "the error names no file: {text}");
        }
    }
    Ok(())
}

/// One `<pgmi-meta>` block, written by [`metadata_block`] into a comment.
#[derive(Debug, Clone, Default)]
pub struct Meta {
    pub id: u128,
    pub idempotent: bool,
    pub sort_keys: Vec<String>,
    pub description: Option<String>,
}

fn xml_text(s: &str) -> String {
    s.chars()
        .filter(|c| {
            matches!(c, '\t' | '\n' | '\u{20}'..='\u{d7ff}' | '\u{e000}'..='\u{fffd}' | '\u{10000}'..='\u{10ffff}')
                && *c != '*'
        })
        .collect()
}

fn xml_escape(s: &str) -> String {
    s.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
}

/// A block written from `meta` reads back as the same id, flag, sort keys and
/// description.
pub fn metadata_block(meta: &Meta) -> Checked {
    let id = uuid::Uuid::from_u128(meta.id.max(1));
    let keys: Vec<String> = meta
        .sort_keys
        .iter()
        .take(8)
        .map(|k| {
            let k: String = xml_text(k).chars().take(64).collect();
            if k.trim().is_empty() {
                format!("k{k}")
            } else {
                k
            }
        })
        .collect();
    let description = meta
        .description
        .as_deref()
        .map(|d| xml_text(d).chars().take(200).collect::<String>())
        .filter(|d| d.is_empty() || !d.trim().is_empty());
    let mut xml = format!(
        "<pgmi-meta id=\"{id}\" idempotent=\"{}\">\n<sortKeys>",
        meta.idempotent
    );
    for k in &keys {
        let _ = write!(xml, "<key>{}</key>", xml_escape(k));
    }
    xml.push_str("</sortKeys>");
    if let Some(d) = &description {
        let _ = write!(xml, "<description>{}</description>", xml_escape(d));
    }
    xml.push_str("\n</pgmi-meta>");
    let content = format!("SELECT 0;\n/*\n{xml}\n*/\nSELECT 1;\n");
    let got = metadata::parse(META_PATH, &content).map_err(|e| format!("{content:?}: {e}"))?;
    let want = metadata::Meta {
        path: META_PATH.into(),
        id: id.hyphenated().to_string(),
        idempotent: meta.idempotent,
        sort_keys: keys,
        description,
    };
    ensure!(
        got.as_ref() == Some(&want),
        "{content:?} read as {got:?}, want {want:?}"
    );
    metadata_text(&content)
}

/// `.env` text is read or refused at a line of the text in both modes; what
/// strict mode accepts, dotenv mode reads as the same keys, and as the same
/// values when nothing can expand. What a project `.env` puts in the process
/// environment is what dotenv mode reads, and every pair of it can be
/// exported.
pub fn dotenv_text(text: &str) -> Checked {
    let lines = text.bytes().filter(|b| *b == b'\n').count() + 1;
    let loose = dotenv::parse(text, Mode::Dotenv);
    let strict = dotenv::parse(text, Mode::Strict);
    let exported = dotenv::environment(text);
    if let Ok(pairs) = &exported {
        ensure!(
            loose.as_ref().ok() == Some(pairs),
            "the environment got {pairs:?}, dotenv mode read {loose:?}"
        );
        for (k, v) in pairs {
            ensure!(
                !k.is_empty() && !k.contains(['=', '\0']) && !v.contains('\0'),
                "{k:?}={v:?} cannot be exported"
            );
        }
    }
    for e in [&loose, &strict, &exported]
        .into_iter()
        .filter_map(|r| r.as_ref().err())
    {
        ensure!(
            (1..=lines).contains(&e.line)
                && e.to_string().starts_with(&format!("line {}: ", e.line)),
            "the error names no line of the text: {e}"
        );
    }
    if let Ok(s) = &strict {
        let l = loose.map_err(|e| format!("strict mode accepted what dotenv mode refused: {e}"))?;
        let keys = |v: &[(String, String)]| v.iter().map(|(k, _)| k.clone()).collect::<Vec<_>>();
        ensure!(
            keys(s) == keys(&l),
            "the modes read different keys: {s:?} and {l:?}"
        );
        ensure!(
            text.contains('$') || *s == l,
            "the modes read different values: {s:?} and {l:?}"
        );
    }
    Ok(())
}

/// Pairs written with single quotes read back unchanged in both modes (one
/// line each in strict mode, which refuses a quote across lines, as Go), and
/// pairs written with double quotes and escapes read back in dotenv mode.
pub fn dotenv_pairs(pairs: &[(String, String)]) -> Checked {
    let pairs: Vec<(String, String)> = pairs
        .iter()
        .map(|(k, v)| {
            let k: String = k
                .chars()
                .filter(|c| c.is_ascii_alphanumeric() || *c == '_')
                .collect();
            let k = if k.starts_with(|c: char| c.is_ascii_alphabetic() || c == '_') {
                k
            } else {
                format!("K{k}")
            };
            (k, v.clone())
        })
        .collect();
    let single: Vec<(String, String)> = pairs
        .iter()
        .map(|(k, v)| (k.clone(), v.replace('\'', "")))
        .collect();
    let quoted = |pairs: &[(String, String)]| {
        pairs.iter().fold(String::new(), |mut s, (k, v)| {
            let _ = writeln!(s, "{k}='{v}'");
            s
        })
    };
    let text = quoted(&single);
    let got = dotenv::parse(&text, Mode::Dotenv).map_err(|e| format!("{text:?}: {e}"))?;
    ensure!(got == single, "{text:?} read as {got:?}");
    let one_line: Vec<(String, String)> = single
        .iter()
        .map(|(k, v)| (k.clone(), v.replace(['\n', '\r'], "")))
        .collect();
    let text = quoted(&one_line);
    let got = dotenv::parse(&text, Mode::Strict).map_err(|e| format!("strict {text:?}: {e}"))?;
    ensure!(got == one_line, "strict {text:?} read as {got:?}");
    let double: Vec<(String, String)> = pairs
        .iter()
        .map(|(k, v)| (k.clone(), v.replace('$', "")))
        .collect();
    let text = double.iter().fold(String::new(), |mut s, (k, v)| {
        let v = v
            .replace('\\', "\\\\")
            .replace('"', "\\\"")
            .replace('\n', "\\n")
            .replace('\r', "\\r")
            .replace('\t', "\\t");
        let _ = write!(s, "export {k}=\"{v}\"\r\n");
        s
    });
    let got = dotenv::parse(&text, Mode::Dotenv).map_err(|e| format!("{text:?}: {e}"))?;
    ensure!(got == double, "{text:?} read as {got:?}");
    Ok(())
}

/// Looking up any `.pgpass` text never panics.
pub fn pgpass_text(text: &str, host: &str, port: &str, db: &str, user: &str) -> Checked {
    let _ = pgpass::find(text, host, port, db, user);
    Ok(())
}

/// A line written with `escape` matches its own host, port, database and
/// user before a later wildcard line, and yields the password unchanged.
/// A field cannot hold a line break, and a host cannot start with `#`, which
/// starts a comment line, as in libpq.
pub fn pgpass_entry(fields: [&str; 5]) -> Checked {
    let [host, port, db, user, password] = fields.map(|f| f.replace(['\n', '\r'], ""));
    let host = if host.starts_with('#') {
        format!("h{host}")
    } else {
        host
    };
    let line: Vec<String> = [&host, &port, &db, &user, &password]
        .iter()
        .map(|f| pgpass::escape(f))
        .collect();
    let text = format!("# c\na:b\n{}\n*:*:*:*:wild\n", line.join(":"));
    let got = pgpass::find(&text, &host, &port, &db, &user);
    ensure!(
        got.as_deref() == Some(password.as_str()),
        "{text:?} gave {got:?}"
    );
    Ok(())
}

#[cfg(test)]
mod tests {
    use proptest::prelude::*;

    use super::*;

    const WIDE: &[char] = &[
        'a', 'Z', '0', '9', '_', '-', '.', ' ', '\t', '\n', '\r', '\'', '"', '\\', ';', '=', ':',
        '@', '/', '?', '%', '&', '+', '#', '[', ']', ',', '$', '{', '}', '*', '<', '>', 'é', '✓',
        '\u{a0}', '\u{feff}', '\0',
    ];

    fn text(
        alphabet: &'static [char],
        len: std::ops::Range<usize>,
    ) -> impl Strategy<Value = String> {
        prop::collection::vec(prop::sample::select(alphabet), len)
            .prop_map(|v| v.into_iter().collect())
    }

    fn checked(r: Checked) -> Result<(), TestCaseError> {
        r.map_err(TestCaseError::fail)
    }

    fn conn_settings() -> impl Strategy<Value = Conn> {
        (
            prop::collection::vec(
                (text(&['a', 'b', '1', '.', '-'], 1..8), any::<Option<u16>>()),
                1..4,
            ),
            text(WIDE, 0..12),
            text(WIDE, 0..12),
            text(WIDE, 0..16),
            any::<Option<u8>>(),
            prop::option::of(text(WIDE, 0..12)),
        )
            .prop_map(
                |(hosts, dbname, user, password, sslmode, application_name)| Conn {
                    hosts,
                    dbname,
                    user,
                    password,
                    sslmode,
                    application_name,
                },
            )
    }

    const SQL: &[char] = &[
        'a', 'E', 'U', '&', 'B', 'C', 'x', '1', ' ', '\n', '\x0b', '\'', '"', '\\', ';', '$', '-',
        '/', '*', '(', ')', ',', '.', 'é',
    ];

    const SQL_WORDS: &[&str] = &[
        "BEGIN",
        "ATOMIC",
        "CASE",
        "END",
        "COMMIT",
        "ROLLBACK",
        "WORK",
        "AND",
        "NO",
        "CHAIN",
        "CALL",
        "pg_temp",
        ".",
        "pgmi_test",
        "(",
        ")",
        "NULL",
        "'p'",
        ",",
        ";",
        " ",
        "\n",
        "--",
        "/*",
        "*/",
        "$$",
        "$t$",
        "E'",
        "U&'",
        "\"",
        "'",
        "\\",
    ];

    fn sql_words() -> impl Strategy<Value = String> {
        prop::collection::vec(prop::sample::select(SQL_WORDS), 0..40).prop_map(|v| v.concat())
    }

    proptest! {
        #[test]
        fn conn_text_never_panics(s in text(WIDE, 0..60)) {
            checked(conn_text(&s))?;
        }

        #[test]
        fn conn_text_in_a_form_never_panics(prefix in prop::sample::select(&["postgresql://", "postgres://", "host=", "Host="][..]), s in text(WIDE, 0..60)) {
            checked(conn_text(&format!("{prefix}{s}")))?;
        }

        #[test]
        fn conn_forms_agree_and_hide_the_password(c in conn_settings()) {
            checked(conn_forms(&c))?;
        }

        #[test]
        fn sql_bytes_tile(b in prop::collection::vec(any::<u8>(), 0..200)) {
            checked(sql_bytes(&b))?;
        }

        #[test]
        fn sql_text_holds(s in text(SQL, 0..120)) {
            checked(sql_bytes(s.as_bytes()))?;
        }

        #[test]
        fn sql_words_hold(s in sql_words()) {
            checked(sql_bytes(s.as_bytes()))?;
        }

        #[test]
        fn macros_only_outside_literals_and_comments(picks in prop::collection::vec(any::<u8>(), 0..24)) {
            checked(sql_fragments(&picks))?;
        }

        #[test]
        fn metadata_text_never_panics(s in text(WIDE, 0..80)) {
            checked(metadata_text(&format!("/* <pgmi-meta {s} */")))?;
            checked(metadata_text(&s))?;
        }

        #[test]
        fn metadata_block_round_trips(
            id in any::<u128>(),
            idempotent in any::<bool>(),
            sort_keys in prop::collection::vec(text(WIDE, 0..12), 0..4),
            description in prop::option::of(text(WIDE, 0..20)),
        ) {
            checked(metadata_block(&Meta { id, idempotent, sort_keys, description }))?;
        }

        #[test]
        fn dotenv_text_holds(s in text(WIDE, 0..80)) {
            checked(dotenv_text(&s))?;
        }

        #[test]
        fn dotenv_pairs_round_trip(pairs in prop::collection::vec((text(&['A', 'b', '_', '1', ' '], 0..6), text(WIDE, 0..12)), 0..6)) {
            checked(dotenv_pairs(&pairs))?;
        }

        #[test]
        fn pgpass_text_never_panics(s in text(WIDE, 0..80), f in prop::collection::vec(text(WIDE, 0..4), 4)) {
            checked(pgpass_text(&s, &f[0], &f[1], &f[2], &f[3]))?;
        }

        #[test]
        fn pgpass_entry_round_trips(f in prop::collection::vec(text(WIDE, 0..10), 5)) {
            checked(pgpass_entry([&f[0], &f[1], &f[2], &f[3], &f[4]]))?;
        }
    }
}
