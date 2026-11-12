#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Mode {
    Dotenv,
    Strict,
}

/// A line of a `.env` or params file that cannot be read, and why.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct ParseError {
    pub(crate) line: usize,
    pub(crate) reason: Reason,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Reason {
    NoEquals,
    InvalidKey(String),
    Unterminated(char),
    EscapeInDoubleQuotes,
    UnquotedComment,
    Ambiguous { key: String, why: Ambiguity },
    Nul,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Ambiguity {
    TextAfterQuote,
    CommentAfterSpace,
    Variable,
}

impl std::fmt::Display for ParseError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "line {}: ", self.line)?;
        match &self.reason {
            Reason::NoEquals => f.write_str("expected KEY=VALUE"),
            Reason::Nul => f.write_str("a NUL byte cannot be put in the environment"),
            Reason::InvalidKey(key) => write!(f, "invalid key {key:?}"),
            Reason::Unterminated(q) => write!(f, "unterminated {q} quote"),
            Reason::EscapeInDoubleQuotes => f.write_str(
                "a backslash or $ inside double quotes is read differently by .env tools; use single quotes for a literal value",
            ),
            Reason::UnquotedComment => f.write_str(
                "an unquoted # after a space starts a comment in some .env tools and not in others; use single quotes to keep it",
            ),
            Reason::Ambiguous { key, why } => {
                let why = match why {
                    Ambiguity::TextAfterQuote => {
                        "has an unterminated quote or text after the closing one"
                    }
                    Ambiguity::CommentAfterSpace => "has # after a space, where a comment starts",
                    Ambiguity::Variable => "has $, where a variable expands",
                };
                write!(
                    f,
                    "the value of {key} {why}, so a .env file reads it differently; put it in single quotes"
                )
            }
        }
    }
}

impl std::error::Error for ParseError {}

/// Reads a project `.env` for the process environment: what `parse` reads
/// in dotenv mode, refused as a whole when the text holds a NUL byte, which
/// no environment variable can.
pub(crate) fn environment(text: &str) -> Result<Vec<(String, String)>, ParseError> {
    if let Some(pos) = text.find('\0') {
        return Err(ParseError {
            line: text[..pos].matches('\n').count() + 1,
            reason: Reason::Nul,
        });
    }
    parse(text, Mode::Dotenv)
}

pub(crate) fn parse(text: &str, mode: Mode) -> Result<Vec<(String, String)>, ParseError> {
    match mode {
        Mode::Dotenv => dotenv(text),
        Mode::Strict => strict(text),
    }
}

fn strict(text: &str) -> Result<Vec<(String, String)>, ParseError> {
    let text = text.strip_prefix('\u{feff}').unwrap_or(text);
    let chars: Vec<char> = text.chars().collect();
    let mut out = Vec::new();
    let mut i = 0;
    let mut line = 1;
    while i < chars.len() {
        let start_line = line;
        let mut end = i;
        while end < chars.len() && chars[end] != '\n' {
            end += 1;
        }
        let raw: String = chars[i..end].iter().collect();
        let trimmed = raw.trim();
        if trimmed.is_empty() || trimmed.starts_with('#') {
            i = end + 1;
            line += 1;
            continue;
        }
        let Some(eq) = raw.find('=') else {
            return Err(ParseError {
                line: start_line,
                reason: Reason::NoEquals,
            });
        };
        let mut key = raw[..eq].trim();
        if let Some(rest) = key.strip_prefix("export ") {
            key = rest.trim();
        }
        if key.is_empty() || key.chars().any(char::is_whitespace) {
            return Err(ParseError {
                line: start_line,
                reason: Reason::InvalidKey(key.to_string()),
            });
        }
        let eq_chars = raw[..eq].chars().count() + 1;
        let mut j = i + eq_chars;
        while j < end && matches!(chars[j], ' ' | '\t') {
            j += 1;
        }
        let value;
        if let Some(&q @ ('\'' | '"')) = chars.get(j) {
            let mut k = j + 1;
            loop {
                let Some(&c) = chars.get(k) else {
                    return Err(ParseError {
                        line: start_line,
                        reason: Reason::Unterminated(q),
                    });
                };
                if c == q {
                    break;
                }
                if c == '\n' {
                    line += 1;
                }
                if q == '"' && (c == '\\' || c == '$') {
                    return Err(ParseError {
                        line: start_line,
                        reason: Reason::EscapeInDoubleQuotes,
                    });
                }
                k += 1;
            }
            value = chars[j + 1..k].iter().collect();
            let mut e = k + 1;
            while e < chars.len() && chars[e] != '\n' {
                e += 1;
            }
            let after: String = chars[k + 1..e].iter().collect();
            if line != start_line || !after.trim().is_empty() {
                return Err(ambiguous(start_line, key, Ambiguity::TextAfterQuote));
            }
            i = e + 1;
        } else {
            let rest: String = chars[j.min(end)..end].iter().collect();
            let rest = rest.strip_suffix('\r').unwrap_or(&rest);
            if rest.trim_start().starts_with('#') {
                return Err(ambiguous(start_line, key, Ambiguity::CommentAfterSpace));
            }
            if rest.contains('$') {
                return Err(ambiguous(start_line, key, Ambiguity::Variable));
            }
            if find_comment(rest).is_some() {
                return Err(ParseError {
                    line: start_line,
                    reason: Reason::UnquotedComment,
                });
            }
            value = rest.trim().to_string();
            i = end + 1;
        }
        line += 1;
        out.push((key.to_string(), value));
    }
    Ok(out)
}

fn is_space(c: char) -> bool {
    matches!(
        c,
        '\t' | '\u{b}' | '\u{c}' | '\r' | ' ' | '\u{85}' | '\u{a0}'
    )
}

fn dotenv(text: &str) -> Result<Vec<(String, String)>, ParseError> {
    let text = text
        .strip_prefix('\u{feff}')
        .unwrap_or(text)
        .replace("\r\n", "\n");
    let s: Vec<char> = text.chars().collect();
    let at = |i: usize, f: fn(char) -> bool| s.get(i).is_some_and(|&c| f(c));
    let mut out: Vec<(String, String)> = Vec::new();
    let mut i = 0;
    loop {
        while at(i, char::is_whitespace) {
            i += 1;
        }
        let Some(&first) = s.get(i) else { break };
        if first == '#' {
            while at(i, |c| c != '\n') {
                i += 1;
            }
            continue;
        }
        let line = s[..i].iter().filter(|&&c| c == '\n').count() + 1;
        let fail = |reason| Err(ParseError { line, reason });
        if s[i..].starts_with(&['e', 'x', 'p', 'o', 'r', 't']) && at(i + 6, is_space) {
            i += 6;
            while at(i, is_space) {
                i += 1;
            }
        }
        let start = i;
        loop {
            match s.get(i) {
                Some('=' | ':') => break,
                Some(&c) if is_space(c) || c == '_' || c == '.' || c.is_ascii_alphanumeric() => {
                    i += 1;
                }
                None | Some('\n') => return fail(Reason::NoEquals),
                Some(_) => return fail(Reason::InvalidKey(s[start..=i].iter().collect())),
            }
        }
        let key: String = s[start..i].iter().collect();
        let key = key.trim_end().to_string();
        if key.is_empty() {
            return fail(Reason::InvalidKey(key));
        }
        i += 1;
        while at(i, is_space) {
            i += 1;
        }
        let value = match s.get(i) {
            Some(&q @ '\'') => {
                let Some(k) = (i + 1..s.len()).find(|&k| s[k] == q && s[k - 1] != '\\') else {
                    return fail(Reason::Unterminated(q));
                };
                let v: String = s[i + 1..k].iter().collect();
                i = k + 1;
                v.trim_end_matches(q).to_string()
            }
            Some(&q @ '"') => {
                let mut k = i + 1;
                loop {
                    match s.get(k) {
                        None => return fail(Reason::Unterminated(q)),
                        Some('\\') => k += 2,
                        Some(&c) if c == q => break,
                        Some(_) => k += 1,
                    }
                }
                let v = expand(&unescape(&s[i + 1..k]), &out);
                i = k + 1;
                v
            }
            _ => {
                let end = (i..s.len())
                    .find(|&k| matches!(s[k], '\n' | '\r'))
                    .unwrap_or(s.len());
                let stop = (i + 1..end)
                    .rev()
                    .find(|&p| s[p] == '#' && is_space(s[p - 1]))
                    .unwrap_or(end);
                let mut v = &s[i..stop];
                while let [rest @ .., c] = v
                    && is_space(*c)
                {
                    v = rest;
                }
                i = end;
                expand(v, &out)
            }
        };
        out.push((key, value));
    }
    Ok(out)
}

fn unescape(s: &[char]) -> Vec<char> {
    let mut decoded = Vec::with_capacity(s.len());
    let mut k = 0;
    while k < s.len() {
        match (s[k], s.get(k + 1)) {
            ('\\', Some('n')) => decoded.push('\n'),
            ('\\', Some('r')) => decoded.push('\r'),
            ('\\', Some(&c)) if c != '\n' => decoded.extend(['\\', c]),
            (c, _) => {
                decoded.push(c);
                k += 1;
                continue;
            }
        }
        k += 2;
    }
    let mut out = Vec::with_capacity(decoded.len());
    let mut k = 0;
    while k < decoded.len() {
        match (decoded[k], decoded.get(k + 1)) {
            ('\\', Some(&c)) if c != '$' => {
                out.push(c);
                k += 2;
            }
            (c, _) => {
                out.push(c);
                k += 1;
            }
        }
    }
    out
}

fn expand(s: &[char], earlier: &[(String, String)]) -> String {
    let lookup = |name: &str| {
        earlier
            .iter()
            .rev()
            .find(|(k, _)| k == name)
            .map_or("", |(_, v)| v.as_str())
    };
    let mut out = String::with_capacity(s.len());
    let mut k = 0;
    while k < s.len() {
        let escaped = s[k] == '\\' && s.get(k + 1) == Some(&'$');
        if !escaped && s[k] != '$' {
            out.push(s[k]);
            k += 1;
            continue;
        }
        let start = k;
        let mut p = k + usize::from(escaped) + 1;
        if s.get(p) == Some(&'(') {
            p += 1;
        }
        if s.get(p) == Some(&'{') {
            p += 1;
        }
        let name_start = p;
        while s
            .get(p)
            .is_some_and(|&c| c.is_ascii_uppercase() || c.is_ascii_digit() || c == '_')
        {
            p += 1;
        }
        let name: String = s[name_start..p].iter().collect();
        if s.get(p) == Some(&'}') {
            p += 1;
        }
        if escaped {
            out.extend(&s[start + 1..p]);
        } else if name.is_empty() {
            out.extend(&s[start..p]);
        } else {
            out.push_str(lookup(&name));
        }
        k = p;
    }
    out
}

fn ambiguous(line: usize, key: &str, why: Ambiguity) -> ParseError {
    ParseError {
        line,
        reason: Reason::Ambiguous {
            key: key.to_string(),
            why,
        },
    }
}

fn find_comment(s: &str) -> Option<usize> {
    let b = s.as_bytes();
    (1..b.len()).find(|&p| b[p] == b'#' && matches!(b[p - 1], b' ' | b'\t'))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn get(v: &[(String, String)], k: &str) -> String {
        v.iter()
            .find(|(a, _)| a == k)
            .map(|(_, b)| b.clone())
            .unwrap()
    }

    #[test]
    fn dotenv_reads_what_godotenv_reads() {
        type Pairs = &'static [(&'static str, &'static str)];
        let table: &[(&str, Option<Pairs>)] = &[
            ("A=\"p\\$word\"\n", Some(&[("A", "p$word")])),
            ("A=p\\$word\n", Some(&[("A", "p$word")])),
            ("A=\"a\\tb\"\n", Some(&[("A", "atb")])),
            ("A=\"a\\\\b\"\n", Some(&[("A", "a\\b")])),
            ("A=\"a\\\"b\"\n", Some(&[("A", "a\"b")])),
            ("A=\"\\a\\b\\n\\r\\\\n\"\n", Some(&[("A", "ab\n\r\\n")])),
            ("A=\"a\\\nb\"\n", Some(&[("A", "a\nb")])),
            ("A=\"a\\$\"\n", Some(&[("A", "a$")])),
            ("A='it''s'\n", None),
            ("A='it\\'s'\n", Some(&[("A", "it\\'s")])),
            ("A='x\\'\n", None),
            ("A='a\\nb'\n", Some(&[("A", "a\\nb")])),
            ("A: value\n", Some(&[("A", "value")])),
            ("A:x=y\n", Some(&[("A", "x=y")])),
            ("A=x:y\n", Some(&[("A", "x:y")])),
            ("export\tA=v\n", Some(&[("A", "v")])),
            ("export  A=v\n", Some(&[("A", "v")])),
            ("export=1\n", Some(&[("export", "1")])),
            ("exportA=1\n", Some(&[("exportA", "1")])),
            ("A B=1\n", Some(&[("A B", "1")])),
            ("A.B=1\n", Some(&[("A.B", "1")])),
            ("A-B=1\n", None),
            ("A\n", None),
            ("A=x # c # d\n", Some(&[("A", "x # c")])),
            ("A=x#y\n", Some(&[("A", "x#y")])),
            ("A=v\t#c\n", Some(&[("A", "v")])),
            ("A=v\u{a0}#c\n", Some(&[("A", "v")])),
            ("A=#c\n", Some(&[("A", "#c")])),
            ("A= #c\n", Some(&[("A", "#c")])),
            ("A=\"x\" # c\n", Some(&[("A", "x")])),
            ("A='x' # c\n", Some(&[("A", "x")])),
            ("A=\"x\" B=2\n", Some(&[("A", "x"), ("B", "2")])),
            ("A=\"x\"y\n", None),
            ("A=\nB=2\n", Some(&[("A", ""), ("B", "2")])),
            ("A=  \n", Some(&[("A", "")])),
            ("A=\"\"\n", Some(&[("A", "")])),
            ("A=''\n", Some(&[("A", "")])),
            ("A=\" spaced \"\n", Some(&[("A", " spaced ")])),
            ("A=\u{a0}v\u{a0}\n", Some(&[("A", "v")])),
            ("  # c\n\tA = v \t\n", Some(&[("A", "v")])),
            (
                "B=b\nA=$B${B}$b${b}$B_x$(B)${B\n",
                Some(&[("B", "b"), ("A", "bb$b${b}xb)b")]),
            ),
            ("B=b\nA=\"\\${B}\"\n", Some(&[("B", "b"), ("A", "${B}")])),
            ("B=b\nA=$B}\n", Some(&[("B", "b"), ("A", "b")])),
            (
                "B=1\nA=\"${B}\\$B$B\"\n",
                Some(&[("B", "1"), ("A", "1$B1")]),
            ),
            ("A=\\\\$B\n", Some(&[("A", "\\$B")])),
            ("A=$\n", Some(&[("A", "$")])),
            ("A=\"$\"\n", Some(&[("A", "$")])),
            ("A=x\\\n", Some(&[("A", "x\\")])),
            ("A=`echo x`\n", Some(&[("A", "`echo x`")])),
            ("A=\"l1\r\nl2\"\r\n", Some(&[("A", "l1\nl2")])),
            ("A=x\ry=1\n", Some(&[("A", "x"), ("y", "1")])),
            (
                "A=\"multi\nline \\n x\"\n",
                Some(&[("A", "multi\nline \n x")]),
            ),
            ("A='multi\nline'\n", Some(&[("A", "multi\nline")])),
            ("A=1\nA=2\n", Some(&[("A", "1"), ("A", "2")])),
            ("A='\n", None),
            ("A=\"\n", None),
        ];
        for (text, want) in table {
            let got = parse(text, Mode::Dotenv);
            match want {
                Some(want) => {
                    let want: Vec<(String, String)> = want
                        .iter()
                        .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
                        .collect();
                    assert_eq!(got.as_ref().ok(), Some(&want), "{text:?}");
                }
                None => assert!(got.is_err(), "{text:?} read as {got:?}"),
            }
        }
    }

    #[test]
    fn dotenv_departs_from_godotenv_where_it_misreads() {
        for (text, want) in [
            ("A=\"x\\\"\"\n", Some(vec![("A", "x\"")])),
            (
                "A=\"x\\\\\"\nB=\"y\"\n",
                Some(vec![("A", "x\\"), ("B", "y")]),
            ),
            ("\u{feff}A=1\n", Some(vec![("A", "1")])),
            ("=x\n", None),
            ("A", None),
        ] {
            let want = want.map(|w| {
                w.into_iter()
                    .map(|(k, v)| (k.to_string(), v.to_string()))
                    .collect::<Vec<_>>()
            });
            assert_eq!(parse(text, Mode::Dotenv).ok(), want, "{text:?}");
        }
    }

    #[test]
    fn dotenv_edges() {
        let text = "PLAIN=plain value\nexport EXPORTED=exported\nSINGLE='single $HOME \\n kept'\nDOUBLE=\"double \\n escape\"\nMULTI=\"line one\nline two\"\nEQUALS=a=b=c\nINLINE=value # inline comment\nHASH=\"quoted # not a comment\"\n# COMMENTED=never\n  SPACED  =  spaced  \nUNICODE=ünï ✓\nCRLF=crlf\r\n";
        let v = parse(text, Mode::Dotenv).unwrap();
        assert_eq!(get(&v, "PLAIN"), "plain value");
        assert_eq!(get(&v, "EXPORTED"), "exported");
        assert_eq!(get(&v, "SINGLE"), "single $HOME \\n kept");
        assert_eq!(get(&v, "DOUBLE"), "double \n escape");
        assert_eq!(get(&v, "MULTI"), "line one\nline two");
        assert_eq!(get(&v, "EQUALS"), "a=b=c");
        assert_eq!(get(&v, "INLINE"), "value");
        assert_eq!(get(&v, "HASH"), "quoted # not a comment");
        assert_eq!(get(&v, "SPACED"), "spaced");
        assert_eq!(get(&v, "UNICODE"), "ünï ✓");
        assert_eq!(get(&v, "CRLF"), "crlf");
        assert_eq!(v.len(), 11);
    }

    #[test]
    fn expansion_from_earlier_lines() {
        let v = parse(
            "BASE=b\nU=x$BASE\nB=x${BASE}y\nD=\"x${BASE}\"\nS='x${BASE}'\nE=a${NOPE}b\nP=5$\n",
            Mode::Dotenv,
        )
        .unwrap();
        assert_eq!(get(&v, "U"), "xb");
        assert_eq!(get(&v, "B"), "xby");
        assert_eq!(get(&v, "D"), "xb");
        assert_eq!(get(&v, "S"), "x${BASE}");
        assert_eq!(get(&v, "E"), "ab");
        assert_eq!(get(&v, "P"), "5$");
    }

    #[test]
    fn strict_refusals() {
        let e = parse("double=\"a \\n b\"\n", Mode::Strict)
            .unwrap_err()
            .to_string();
        assert!(e.contains("backslash or $ inside double quotes") && e.contains("single quotes"));
        let e = parse("inline=value # c\n", Mode::Strict)
            .unwrap_err()
            .to_string();
        assert!(e.contains("# after a space") && e.contains("single quotes"));
        assert!(parse("a=x#y\n", Mode::Strict).is_ok());
    }

    #[test]
    fn strict_refuses_what_go_refuses() {
        for (text, why) in [
            ("a=$X\n", "has $, where a variable expands"),
            ("a=\"x\" y\n", "text after the closing one"),
            ("a='x'y\n", "text after the closing one"),
            ("a='x\ny'\n", "unterminated quote"),
            ("a=#x\n", "has # after a space"),
        ] {
            let e = parse(text, Mode::Strict).unwrap_err().to_string();
            assert!(
                e.starts_with("line 1: the value of a ")
                    && e.contains(why)
                    && e.ends_with("put it in single quotes"),
                "{text:?}: {e}"
            );
        }
        assert_eq!(
            parse("a='$x' \nb=\"y\"\n", Mode::Strict).unwrap(),
            [("a".into(), "$x".into()), ("b".into(), "y".into())]
        );
    }
}
