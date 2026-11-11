#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Kind {
    Space,
    LineComment,
    BlockComment,
    Str,
    QuotedIdent,
    Dollar,
    Word,
    Semicolon,
    LParen,
    RParen,
    Comma,
    Other,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Token {
    pub(crate) kind: Kind,
    pub(crate) start: usize,
    pub(crate) end: usize,
}

impl Token {
    pub(crate) fn text<'a>(&self, src: &'a [u8]) -> &'a [u8] {
        &src[self.start..self.end]
    }

    pub(crate) fn is_trivia(&self) -> bool {
        matches!(
            self.kind,
            Kind::Space | Kind::LineComment | Kind::BlockComment
        )
    }

    pub(crate) fn is_word(&self, src: &[u8], word: &str) -> bool {
        self.kind == Kind::Word && self.text(src).eq_ignore_ascii_case(word.as_bytes())
    }
}

pub(crate) fn is_space(b: u8) -> bool {
    matches!(b, b' ' | b'\t' | b'\n' | b'\r' | 0x0b | 0x0c)
}

fn is_ident_start(b: u8) -> bool {
    b.is_ascii_alphabetic() || b == b'_' || b >= 0x80
}

fn is_ident(b: u8) -> bool {
    is_ident_start(b) || b.is_ascii_digit() || b == b'$'
}

pub(crate) fn tokens(src: &[u8]) -> Vec<Token> {
    let mut out = Vec::new();
    let mut i = 0;
    while i < src.len() {
        let start = i;
        let b = src[i];
        let kind = if is_space(b) {
            while i < src.len() && is_space(src[i]) {
                i += 1;
            }
            Kind::Space
        } else if b == b'-' && src.get(i + 1) == Some(&b'-') {
            while i < src.len() && src[i] != b'\n' {
                i += 1;
            }
            Kind::LineComment
        } else if b == b'/' && src.get(i + 1) == Some(&b'*') {
            i += 2;
            let mut depth = 1;
            while i < src.len() && depth > 0 {
                if src[i] == b'/' && src.get(i + 1) == Some(&b'*') {
                    depth += 1;
                    i += 2;
                } else if src[i] == b'*' && src.get(i + 1) == Some(&b'/') {
                    depth -= 1;
                    i += 2;
                } else {
                    i += 1;
                }
            }
            Kind::BlockComment
        } else if b == b'\'' {
            i = quoted(src, i, b'\'', false);
            Kind::Str
        } else if b == b'"' {
            i = quoted(src, i, b'"', false);
            Kind::QuotedIdent
        } else if b == b'$' && dollar_tag(src, i).is_some() && !follows_ident(src, i) {
            let tag_end = dollar_tag(src, i).unwrap_or(i + 1);
            let tag = &src[i..tag_end];
            i = tag_end;
            loop {
                if i >= src.len() {
                    break;
                }
                if src[i..].starts_with(tag) {
                    i += tag.len();
                    break;
                }
                i += 1;
            }
            Kind::Dollar
        } else if is_ident_start(b) {
            while i < src.len() && is_ident(src[i]) {
                i += 1;
            }
            let word = &src[start..i];
            if (word == b"E" || word == b"e") && src.get(i) == Some(&b'\'') {
                i = quoted(src, i, b'\'', true);
                Kind::Str
            } else if (word == b"U" || word == b"u")
                && src.get(i) == Some(&b'&')
                && matches!(src.get(i + 1), Some(b'\'') | Some(b'"'))
            {
                let q = src[i + 1];
                i = quoted(src, i + 1, q, false);
                if q == b'"' {
                    Kind::QuotedIdent
                } else {
                    Kind::Str
                }
            } else {
                Kind::Word
            }
        } else {
            i += 1;
            match b {
                b';' => Kind::Semicolon,
                b'(' => Kind::LParen,
                b')' => Kind::RParen,
                b',' => Kind::Comma,
                _ => Kind::Other,
            }
        };
        out.push(Token {
            kind,
            start,
            end: i,
        });
    }
    out
}

fn follows_ident(src: &[u8], i: usize) -> bool {
    i > 0 && is_ident(src[i - 1])
}

fn dollar_tag(src: &[u8], i: usize) -> Option<usize> {
    let mut j = i + 1;
    if src.get(j) == Some(&b'$') {
        return Some(j + 1);
    }
    if !src.get(j).copied().is_some_and(is_ident_start) {
        return None;
    }
    while j < src.len() && (is_ident_start(src[j]) || src[j].is_ascii_digit()) {
        j += 1;
    }
    (src.get(j) == Some(&b'$')).then_some(j + 1)
}

fn quoted(src: &[u8], open: usize, q: u8, backslash: bool) -> usize {
    let mut i = open + 1;
    while i < src.len() {
        let c = src[i];
        if backslash && c == b'\\' {
            i += 2;
            continue;
        }
        if c == q {
            if src.get(i + 1) == Some(&q) {
                i += 2;
                continue;
            }
            return i + 1;
        }
        i += 1;
    }
    src.len()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn kinds(s: &str) -> Vec<(Kind, String)> {
        tokens(s.as_bytes())
            .into_iter()
            .filter(|t| t.kind != Kind::Space)
            .map(|t| {
                (
                    t.kind,
                    String::from_utf8_lossy(t.text(s.as_bytes())).into_owned(),
                )
            })
            .collect()
    }

    #[test]
    fn literals_and_comments() {
        let k = kinds(
            "SELECT E'a\\'b', 'c''d', \"x\"\"y\", $f$ ; $f$, $$x$$ -- c\n/* a /* b */ c */ a$b$ U&'x';",
        );
        assert_eq!(k[1], (Kind::Str, "E'a\\'b'".into()));
        assert_eq!(k[3], (Kind::Str, "'c''d'".into()));
        assert_eq!(k[5], (Kind::QuotedIdent, "\"x\"\"y\"".into()));
        assert_eq!(k[7], (Kind::Dollar, "$f$ ; $f$".into()));
        assert_eq!(k[9], (Kind::Dollar, "$$x$$".into()));
        assert_eq!(k[10].0, Kind::LineComment);
        assert_eq!(k[11], (Kind::BlockComment, "/* a /* b */ c */".into()));
        assert_eq!(k[12], (Kind::Word, "a$b$".into()));
        assert_eq!(k[13], (Kind::Str, "U&'x'".into()));
        assert_eq!(k[14].0, Kind::Semicolon);
    }

    #[test]
    fn plain_string_keeps_backslash_literal() {
        let k = kinds("'a\\' x");
        assert_eq!(k[0], (Kind::Str, "'a\\'".into()));
    }

    #[test]
    fn positional_parameter_is_not_a_dollar_quote() {
        let k = kinds("$1 + $2");
        assert_eq!(k[0], (Kind::Other, "$".into()));
    }
}
