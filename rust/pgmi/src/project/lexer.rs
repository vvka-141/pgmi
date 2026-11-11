#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum TokenKind {
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
    pub(crate) kind: TokenKind,
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
            TokenKind::Space | TokenKind::LineComment | TokenKind::BlockComment
        )
    }

    pub(crate) fn is_word(&self, src: &[u8], word: &str) -> bool {
        self.kind == TokenKind::Word && self.text(src).eq_ignore_ascii_case(word.as_bytes())
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
            TokenKind::Space
        } else if b == b'-' && src.get(i + 1) == Some(&b'-') {
            while i < src.len() && src[i] != b'\n' {
                i += 1;
            }
            TokenKind::LineComment
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
            TokenKind::BlockComment
        } else if b == b'\'' {
            i = quoted(src, i, b'\'', false);
            TokenKind::Str
        } else if b == b'"' {
            i = quoted(src, i, b'"', false);
            TokenKind::QuotedIdent
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
            TokenKind::Dollar
        } else if is_ident_start(b) {
            while i < src.len() && is_ident(src[i]) {
                i += 1;
            }
            let word = &src[start..i];
            if (word == b"E" || word == b"e") && src.get(i) == Some(&b'\'') {
                i = quoted(src, i, b'\'', true);
                TokenKind::Str
            } else if (word == b"U" || word == b"u")
                && src.get(i) == Some(&b'&')
                && matches!(src.get(i + 1), Some(b'\'') | Some(b'"'))
            {
                let q = src[i + 1];
                i = quoted(src, i + 1, q, false);
                if q == b'"' {
                    TokenKind::QuotedIdent
                } else {
                    TokenKind::Str
                }
            } else {
                TokenKind::Word
            }
        } else {
            i += 1;
            match b {
                b';' => TokenKind::Semicolon,
                b'(' => TokenKind::LParen,
                b')' => TokenKind::RParen,
                b',' => TokenKind::Comma,
                _ => TokenKind::Other,
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

    fn kinds(s: &str) -> Vec<(TokenKind, String)> {
        tokens(s.as_bytes())
            .into_iter()
            .filter(|t| t.kind != TokenKind::Space)
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
        assert_eq!(k[1], (TokenKind::Str, "E'a\\'b'".into()));
        assert_eq!(k[3], (TokenKind::Str, "'c''d'".into()));
        assert_eq!(k[5], (TokenKind::QuotedIdent, "\"x\"\"y\"".into()));
        assert_eq!(k[7], (TokenKind::Dollar, "$f$ ; $f$".into()));
        assert_eq!(k[9], (TokenKind::Dollar, "$$x$$".into()));
        assert_eq!(k[10].0, TokenKind::LineComment);
        assert_eq!(k[11], (TokenKind::BlockComment, "/* a /* b */ c */".into()));
        assert_eq!(k[12], (TokenKind::Word, "a$b$".into()));
        assert_eq!(k[13], (TokenKind::Str, "U&'x'".into()));
        assert_eq!(k[14].0, TokenKind::Semicolon);
    }

    #[test]
    fn plain_string_keeps_backslash_literal() {
        let k = kinds("'a\\' x");
        assert_eq!(k[0], (TokenKind::Str, "'a\\'".into()));
    }

    #[test]
    fn positional_parameter_is_not_a_dollar_quote() {
        let k = kinds("$1 + $2");
        assert_eq!(k[0], (TokenKind::Other, "$".into()));
    }

    #[test]
    fn only_real_comments_are_comments() {
        let comments = |s: &str| -> Vec<String> {
            kinds(s)
                .into_iter()
                .filter(|(k, _)| matches!(k, TokenKind::LineComment | TokenKind::BlockComment))
                .map(|(_, t)| t)
                .collect()
        };
        for (src, want) in [
            ("SELECT 1; -- comment", &["-- comment"][..]),
            ("--\nSELECT 1;", &["--"]),
            (
                "-- first\nSELECT 1; -- second\n-- third",
                &["-- first", "-- second", "-- third"],
            ),
            (
                "/* c1 */SELECT/* c2 */ 1;/* c3 */",
                &["/* c1 */", "/* c2 */", "/* c3 */"],
            ),
            ("SELECT /* line1\nline2 */ 1;", &["/* line1\nline2 */"]),
            (
                "/* a /* b /* c */ b */ a */SELECT 1;",
                &["/* a /* b /* c */ b */ a */"],
            ),
            ("/* /* nested */ */", &["/* /* nested */ */"]),
            ("/* $$not string$$ */SELECT 1;", &["/* $$not string$$ */"]),
            ("SELECT '--not comment', '/* no */', 'it''s -- no';", &[]),
            ("SELECT $$--not comment$$, $tag$/*nope*/$tag$;", &[]),
            ("SELECT $a$ content $b$ -- inner $b$ more $a$;", &[]),
            (
                "CREATE FUNCTION f() AS $$ SELECT '--'; $$ LANGUAGE sql; -- c",
                &["-- c"],
            ),
            ("SELECT $1$--comment$1$;", &["--comment$1$;"]),
            ("SELECT 1-2, 1/2, 1*2;", &[]),
            ("SELECT 'unclosed -- x", &[]),
            ("", &[]),
        ] {
            assert_eq!(comments(src), want, "{src:?}");
        }
    }
}
