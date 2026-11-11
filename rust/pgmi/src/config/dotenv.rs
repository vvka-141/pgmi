#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Mode {
    Dotenv,
    Strict,
}

pub(crate) fn parse(text: &str, mode: Mode) -> Result<Vec<(String, String)>, String> {
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
            return Err(format!("line {start_line}: expected KEY=VALUE"));
        };
        let mut key = raw[..eq].trim();
        if let Some(rest) = key.strip_prefix("export ") {
            key = rest.trim();
        }
        if key.is_empty() || key.chars().any(char::is_whitespace) {
            return Err(format!("line {start_line}: invalid key {key:?}"));
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
                        return Err(format!("line {start_line}: unterminated {q} quote"));
                    };
                    if c == q {
                        break;
                    }
                    if c == '\n' {
                        line += 1;
                    }
                    if q == '"' && (c == '\\' || c == '$') && mode == Mode::Strict {
                        return Err(format!(
                            "line {start_line}: a backslash or $ inside double quotes is read differently by .env tools; use single quotes for a literal value"
                        ));
                    }
                    if q == '"' && c == '\\' {
                        k += 1;
                        match chars.get(k) {
                            Some('n') => v.push('\n'),
                            Some('r') => v.push('\r'),
                            Some('t') => v.push('\t'),
                            Some(&o) => v.push(o),
                            None => {
                                return Err(format!("line {start_line}: unterminated {q} quote"));
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
                i = e + 1;
                line += 1;
            }
            _ => {
                let rest: String = chars[j.min(end)..end].iter().collect();
                let rest = rest.strip_suffix('\r').unwrap_or(&rest);
                let mut v = rest.to_string();
                if let Some(pos) = find_comment(rest) {
                    if mode == Mode::Strict {
                        return Err(format!(
                            "line {start_line}: an unquoted # after a space starts a comment in some .env tools and not in others; use single quotes to keep it"
                        ));
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
        let e = parse("double=\"a \\n b\"\n", Mode::Strict).unwrap_err();
        assert!(e.contains("backslash or $ inside double quotes") && e.contains("single quotes"));
        let e = parse("inline=value # c\n", Mode::Strict).unwrap_err();
        assert!(e.contains("# after a space") && e.contains("single quotes"));
        assert!(parse("a=x#y\n", Mode::Strict).is_ok());
    }
}
