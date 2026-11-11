use sha2::{Digest, Sha256};

use super::lexer::{self, Kind};

pub(crate) fn raw(content: &[u8]) -> String {
    hex(&Sha256::digest(content))
}

pub(crate) fn normalized(content: &[u8]) -> String {
    hex(&Sha256::digest(normalize(content)))
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

pub(crate) fn normalize(src: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(src.len());
    let mut pending_space = false;
    for t in lexer::tokens(src) {
        match t.kind {
            Kind::Space | Kind::LineComment | Kind::BlockComment => pending_space = true,
            Kind::Str | Kind::QuotedIdent | Kind::Dollar => {
                if pending_space {
                    out.push(b' ');
                    pending_space = false;
                }
                let text = t.text(src);
                let quote = text
                    .iter()
                    .position(|b| matches!(b, b'\'' | b'"' | b'$'))
                    .unwrap_or(0);
                out.extend(text[..quote].iter().map(u8::to_ascii_lowercase));
                out.extend_from_slice(&text[quote..]);
            }
            _ => {
                if pending_space {
                    out.push(b' ');
                    pending_space = false;
                }
                out.extend(t.text(src).iter().map(u8::to_ascii_lowercase));
            }
        }
    }
    if pending_space {
        out.push(b' ');
    }
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

#[cfg(test)]
mod tests {
    use super::*;

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
