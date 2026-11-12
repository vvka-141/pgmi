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
        match chars.get(j) {
            Some(&q @ ('\'' | '"')) if j < chars.len() => {
                let mut v = String::new();
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
                    if q == '"' && (c == '\\' || c == '$') && mode == Mode::Strict {
                        return Err(ParseError {
                            line: start_line,
                            reason: Reason::EscapeInDoubleQuotes,
                        });
                    }
                    if q == '"' && c == '\\' {
                        k += 1;
                        match chars.get(k) {
                            Some('n') => v.push('\n'),
                            Some('r') => v.push('\r'),
                            Some('t') => v.push('\t'),
                            Some(&o) => v.push(o),
                            None => {
                                return Err(ParseError {
                                    line: start_line,
                                    reason: Reason::Unterminated(q),
                                });
                            }
                        }
                    } else {
                        v.push(c);
                    }
                    k += 1;
                }
                value = if q == '"' && mode == Mode::Dotenv {
                    expand(&v, &out)
                } else {
                    v
                };
                let mut e = k + 1;
                while e < chars.len() && chars[e] != '\n' {
                    e += 1;
                }
                let after: String = chars[k + 1..e].iter().collect();
                if mode == Mode::Strict && (line != start_line || !after.trim().is_empty()) {
                    return Err(ambiguous(start_line, key, Ambiguity::TextAfterQuote));
                }
                i = e + 1;
                line += 1;
            }
            _ => {
                let rest: String = chars[j.min(end)..end].iter().collect();
                let rest = rest.strip_suffix('\r').unwrap_or(&rest);
                let mut v = rest.to_string();
                if mode == Mode::Strict && rest.trim_start().starts_with('#') {
                    return Err(ambiguous(start_line, key, Ambiguity::CommentAfterSpace));
                }
                if mode == Mode::Strict && rest.contains('$') {
                    return Err(ambiguous(start_line, key, Ambiguity::Variable));
                }
                if let Some(pos) = find_comment(rest) {
                    if mode == Mode::Strict {
                        return Err(ParseError {
                            line: start_line,
                            reason: Reason::UnquotedComment,
                        });
                    }
                    v.truncate(pos);
                }
                value = match mode {
                    Mode::Dotenv => expand(v.trim(), &out),
                    Mode::Strict => v.trim().to_string(),
                };
                i = end + 1;
                line += 1;
            }
        }
        out.push((key.to_string(), value));
    }
    Ok(out)
}

fn expand(s: &str, earlier: &[(String, String)]) -> String {
    let lookup = |name: &str| {
        earlier
            .iter()
            .rev()
            .find(|(k, _)| k == name)
            .map_or("", |(_, v)| v.as_str())
    };
    let is_name = |c: char| c.is_ascii_alphanumeric() || c == '_';
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(pos) = rest.find('$') {
        out.push_str(&rest[..pos]);
        let after = &rest[pos + 1..];
        if let Some(braced) = after.strip_prefix('{')
            && let Some(close) = braced.find('}')
        {
            out.push_str(lookup(&braced[..close]));
            rest = &braced[close + 1..];
            continue;
        }
        let len = after.find(|c: char| !is_name(c)).unwrap_or(after.len());
        if len == 0 {
            out.push('$');
        } else {
            out.push_str(lookup(&after[..len]));
        }
        rest = &after[len..];
    }
    out.push_str(rest);
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
