use sha2::{Digest, Sha256};
use std::fmt::Write as _;

use super::lexer;

pub(crate) fn raw(content: &[u8]) -> String {
    hex(&Sha256::digest(content))
}

pub(crate) fn normalized(content: &[u8]) -> String {
    hex(&Sha256::digest(normalize(content)))
}

fn hex(bytes: &[u8]) -> String {
    bytes
        .iter()
        .fold(String::with_capacity(bytes.len() * 2), |mut s, b| {
            let _ = write!(s, "{b:02x}");
            s
        })
}

/// The text `pgmi_checksum` hashes: comments dropped, whitespace runs as
/// one space, ASCII letters folded outside quotes. It must stay byte for
/// byte what the Go binary hashed, or a redeploy reads run-once files as
/// edited, so it has its own scanner rather than the SQL lexer.
pub(crate) fn normalize(src: &[u8]) -> Vec<u8> {
    let out = scan(src);
    match String::from_utf8(out) {
        Ok(s) => s.trim_matches(char::is_whitespace).as_bytes().to_vec(),
        Err(e) => {
            let b = e.into_bytes();
            let start = b
                .iter()
                .position(|c| !lexer::is_space(*c))
                .unwrap_or(b.len());
            let end = b
                .iter()
                .rposition(|c| !lexer::is_space(*c))
                .map_or(start, |p| p + 1);
            b[start..end].to_vec()
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum State {
    Normal,
    LineComment,
    BlockComment { depth: usize },
    Quote { escapes: bool },
    QuotedIdent,
    Dollar { tag_start: usize, tag_len: usize },
}

struct Scanner<'a> {
    src: &'a [u8],
    out: Vec<u8>,
    state: State,
    last_space: bool,
    spaced: bool,
    i: usize,
}

fn scan(src: &[u8]) -> Vec<u8> {
    let mut s = Scanner {
        src,
        out: Vec::with_capacity(src.len()),
        state: State::Normal,
        last_space: false,
        spaced: false,
        i: 0,
    };
    while s.i < src.len() {
        let written = s.out.len();
        s.spaced = false;
        match s.state {
            State::Normal => s.normal(),
            State::LineComment => s.line_comment(),
            State::BlockComment { depth } => s.block_comment(depth),
            State::Quote { escapes } => s.quoted(b'\'', escapes),
            State::QuotedIdent => s.quoted(b'"', false),
            State::Dollar { tag_start, tag_len } => s.dollar(tag_start, tag_len),
        }
        if s.out.len() != written && !s.spaced {
            s.last_space = false;
        }
    }
    s.out
}

impl Scanner<'_> {
    fn next(&self) -> Option<u8> {
        self.src.get(self.i + 1).copied()
    }

    fn space(&mut self) {
        self.spaced = true;
        if !self.last_space {
            self.out.push(b' ');
            self.last_space = true;
        }
    }

    fn normal(&mut self) {
        let ch = self.src[self.i];
        let next = self.next();
        if ch == b'-' && next == Some(b'-') {
            self.state = State::LineComment;
            self.space();
            self.i += 2;
        } else if ch == b'/' && next == Some(b'*') {
            self.state = State::BlockComment { depth: 1 };
            self.space();
            self.i += 2;
        } else if lexer::is_space(ch) {
            self.space();
            self.i += 1;
        } else if ch == b'\'' {
            self.state = State::Quote {
                escapes: escape_prefixed(self.src, self.i),
            };
            self.out.push(ch);
            self.i += 1;
        } else if ch == b'"' {
            self.state = State::QuotedIdent;
            self.out.push(ch);
            self.i += 1;
        } else if let Some(len) = (ch == b'$' && !continues_identifier(self.src, self.i))
            .then(|| dollar_tag(self.src, self.i))
            .flatten()
        {
            self.state = State::Dollar {
                tag_start: self.i,
                tag_len: len,
            };
            self.out.extend_from_slice(&self.src[self.i..self.i + len]);
            self.i += len;
        } else {
            self.out.push(ch.to_ascii_lowercase());
            self.i += 1;
        }
    }

    fn line_comment(&mut self) {
        let ch = self.src[self.i];
        if ch == b'\n' {
            self.space();
            self.state = State::Normal;
            self.i += 1;
        } else if ch == b'\r' && self.next() == Some(b'\n') {
            self.space();
            self.state = State::Normal;
            self.i += 2;
        } else {
            self.i += 1;
        }
    }

    fn block_comment(&mut self, depth: usize) {
        let (ch, next) = (self.src[self.i], self.next());
        if ch == b'/' && next == Some(b'*') {
            self.state = State::BlockComment { depth: depth + 1 };
            self.i += 2;
        } else if ch == b'*' && next == Some(b'/') {
            self.state = if depth == 1 {
                State::Normal
            } else {
                State::BlockComment { depth: depth - 1 }
            };
            self.i += 2;
        } else {
            self.i += 1;
        }
    }

    fn quoted(&mut self, quote: u8, escapes: bool) {
        let ch = self.src[self.i];
        if escapes && ch == b'\\' && self.i + 1 < self.src.len() {
            self.out.extend_from_slice(&self.src[self.i..self.i + 2]);
            self.i += 2;
            return;
        }
        self.out.push(ch);
        if ch != quote {
            self.i += 1;
        } else if self.next() == Some(quote) {
            self.out.push(quote);
            self.i += 2;
        } else {
            self.state = State::Normal;
            self.i += 1;
        }
    }

    fn dollar(&mut self, tag_start: usize, tag_len: usize) {
        let tag = &self.src[tag_start..tag_start + tag_len];
        if self.src[self.i..].starts_with(tag) {
            self.out.extend_from_slice(tag);
            self.i += tag_len;
            self.state = State::Normal;
        } else {
            self.out.push(self.src[self.i]);
            self.i += 1;
        }
    }
}

fn is_identifier_byte(b: u8) -> bool {
    b == b'_' || b == b'$' || b >= 0x80 || b.is_ascii_alphanumeric()
}

fn escape_prefixed(src: &[u8], i: usize) -> bool {
    i > 0 && matches!(src[i - 1], b'E' | b'e') && (i == 1 || !is_identifier_byte(src[i - 2]))
}

fn continues_identifier(src: &[u8], i: usize) -> bool {
    let start = src[..i]
        .iter()
        .rposition(|b| !is_identifier_byte(*b))
        .map_or(0, |p| p + 1);
    start != i && {
        let first = src[start];
        first == b'_' || first >= 0x80 || first.is_ascii_alphabetic()
    }
}

fn dollar_tag(src: &[u8], i: usize) -> Option<usize> {
    for (n, &b) in src.iter().enumerate().skip(i + 1) {
        if b == b'$' {
            return Some(n + 1 - i);
        }
        let ok = if n == i + 1 {
            b.is_ascii_alphabetic() || b == b'_'
        } else {
            b.is_ascii_alphanumeric() || b == b'_'
        };
        if !ok {
            return None;
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    const GO_PGMI_CHECKSUMS: &[(&str, &str)] = &[
        (
            "SELECT $1$a$ X  Y $a$",
            "f80f7ecc42e12393a40c3143f39ed529f1d2ac1974905f677910b1d503d40ff7",
        ),
        (
            "SELECT 1$a$ X  Y $a$",
            "beced7df0ed0f977badee6246a80d5de72f1a0d3a2a1faa15adaf0a7b5feb82f",
        ),
        (
            "SELECT $é$ X  -- c\n Y $é$",
            "cd3cd14d4076fdef9c92f17b5cc96df2a1f348b39a35fdc9dcf8a2c0e3ab14b8",
        ),
        (
            "CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $тело$ SELECT  1 -- Комментарий\n $тело$;",
            "8b42784046ddd15d91f4417543ece15dee86703bcd8cbf91ff93a47d0bba85ce",
        ),
        (
            "SELECT $E'a\\'B -- x'",
            "8be4c11f70caf712df511411dc13c3f6d001e150bb3d3ad887de2426ca6b379c",
        ),
        (
            "SELECT x$a$ Y $a$",
            "7406fc965f397eec67d6f512511edc7c27c35cfacf6dba262baf1a1dce0926d5",
        ),
        (
            "$$$$$A$",
            "79a9c392a7266e4823925de7816ac3d1421d9fd93f2b463134a7d43d7ff97b96",
        ),
        (
            "SELECT E'x\\'y -- Z'",
            "6adacdf125f3c13cbaf4140c16286025d5ac7666a8a84117ed437d47b9209b0b",
        ),
        (
            "SELECT 'unterminated -- C",
            "06549fb4abb466e2d9ac7ff0f991ce08ca94188d98215439bd45e0c031accf50",
        ),
        (
            "SELECT $a$ never closed -- C",
            "75c311d8d761fc4589ce5566f77115d4afb48166c161161953613ce6f69e191c",
        ),
        (
            "/* unterminated ",
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
        ),
        (
            "SELECT \"Q\"\"Q\" , U&'A' FROM T",
            "d21dc6f7979b00e858ee986c0f115ae039de7feef2d1b39c3e5b3f80513f3661",
        ),
        (
            "SELECT ab$c$ D $c$",
            "25956496ca432917dbd4fc32fd5e11c014d95ee1b07dca631486ae8336989584",
        ),
    ];

    #[test]
    fn golden() {
        let cases: &[(&str, &str, &str)] = &[
            (
                "SELECT id FROM users WHERE name = 'ANN';\n",
                "099005953775757bcf8c421726dacb65fa00b3ea826767b84d92ca2b3ee98fc5",
                "df33159839169ebb98ba4e60805d157581460e4ef3933dbeb177bb519e840b0a",
            ),
            (
                "SELECT id FROM \"Users\" WHERE name = 'Ann';\n",
                "5114864a85f2d88007088a747ce5dcf5105bc260c4cf182edb9a8c64327570c0",
                "66f6443832a5f5c792eab005df078ae58f5a4dda3cc001cfe36019d18699b5c9",
            ),
            (
                "-- nothing here\n/* or here */\n",
                "6232c0377550f240588b9d475eab816dd791800490dd839f95027f34cf677fb0",
                "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            ),
            (
                "DO $body$ BEGIN RAISE NOTICE '-- not a comment /* nor this */ KEEP Case'; END $body$;\n",
                "834bd46c1e63b3faa26594d26c8de6da541b665ab852a2265cfa20f65d2f9cb7",
                "ec1967dddfc430b4500b885c0ab59961717b305e3d4b0e720a87c853e49f7396",
            ),
            (
                "",
                "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
                "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            ),
            (
                "SELECT E'It\\'s -- here', 'it''s /* here */';\n",
                "098f6d73f2f5d13a16bb55dfcbdb7ef83db5da43589c643937ac2087ebcd2df5",
                "d2d84b7be8836cf42fbd3fef9a52ebfa7c5bd6499ab0d0f8206c1527a52d98c0",
            ),
            (
                "SELECT 1;",
                "17db4fd369edb9244b9f91d9aeed145c3d04ad8ba6e95d06247f07a63527d11a",
                "354b7196c9ba5fb4b21cf615bb6ec4cd5c07503c34229feef033fc081a8c03f4",
            ),
            (
                "\n\n\tSELECT\t1\t;\n\n\n",
                "d252149182c1f9d959a206f57cc8a2bd50431659845deae711acebf7b2ef71f2",
                "afe03bd0c288ce3de58beb5f60cace51119c9cff02c7c45dc7e0d96da13d96fd",
            ),
            (
                "SELECT U&'d\\0061t\\+000061' AS \"Mixed\";\n",
                "5f8ff760790ef3b1630931d323c67cd9ec8848e054a7e6a7ea862b3b6292a0c3",
                "24e4b422018cbe77caa8553d85f3aa05c8df579d3d981c6463791a4e02a6a018",
            ),
            (
                "SELECT Ärger, İstanbul, ß FROM Straße;\n",
                "8d58d51d0be674e2a927f04e6a375d2d4d092ca038a1c07f89d98986f3162e47",
                "62b0ba5c66b8c37d83c28fe514452e63e87fd8c778f2c1b78cf54c476f961502",
            ),
            (
                "SELECT 'Ärger İstanbul ß';\n",
                "9f7b7f18e7d850ffca384925d17d1e27a1627dcab2f0a1c52f64762931c29981",
                "3a6620aea968f83c022bf1b4edb84a22ffa04321b282e4443e4cc0b3198501f0",
            ),
            (
                "SELECT id FROM users WHERE name = 'Ann';\n",
                "a394888eeef6a2b25cde0ecc483d61955af720cf0569acc7bdcffa0afcfc029c",
                "670f963c902a0eb71e7361b17f860ff224b1534d3e2e270c1f6f7b0e79547154",
            ),
            (
                "select   ID\n\tfrom USERS where NAME = 'Ann';\n",
                "1cf40c786f2e5a72c0ade58764967d670337362b39da785a999323978f8ea8b0",
                "670f963c902a0eb71e7361b17f860ff224b1534d3e2e270c1f6f7b0e79547154",
            ),
            (
                "-- leading comment\nSELECT id /* inline */ FROM users -- trailing\nWHERE name = 'Ann';",
                "55edae4900c824e27af58a37e80f6b4f4714a65d4f643ff5690eb46ed371d256",
                "670f963c902a0eb71e7361b17f860ff224b1534d3e2e270c1f6f7b0e79547154",
            ),
            (
                "SELECT id FROM users\r\nWHERE name = 'Ann';\r\n",
                "296c8a60374e4a3d57bb517dc019a69da09b5fb8f66c0dfa315b90e41130ebce",
                "670f963c902a0eb71e7361b17f860ff224b1534d3e2e270c1f6f7b0e79547154",
            ),
            (
                "/* outer /* inner */ still comment */ SELECT id FROM users WHERE name = 'Ann';\n",
                "b47f89f2dfbdeeb10283089d99cb0fff7f4b412a8fc1ee7daa577230d37f8345",
                "670f963c902a0eb71e7361b17f860ff224b1534d3e2e270c1f6f7b0e79547154",
            ),
        ];
        for (text, pgmi) in GO_PGMI_CHECKSUMS {
            assert_eq!(
                normalized(text.as_bytes()),
                *pgmi,
                "pgmi_checksum of {text:?} differs from the Go binary's: {:?}",
                String::from_utf8_lossy(&normalize(text.as_bytes()))
            );
        }
        for (text, sum, pgmi) in cases {
            assert_eq!(raw(text.as_bytes()), *sum, "checksum of {text:?}");
            assert_eq!(
                normalized(text.as_bytes()),
                *pgmi,
                "pgmi_checksum of {text:?}: {:?}",
                String::from_utf8_lossy(&normalize(text.as_bytes()))
            );
        }
    }
}
