use sha2::{Digest, Sha256};
use unicode_normalization::UnicodeNormalization;

use super::tree;

const SALT: &str = "pgmi-no-leak:";

const DENYLIST: [&str; 20] = [
    "dc7db3f228d591261d965100b4f2ff5026f202826d5ded84e416923d2b666cc7",
    "e6c95557d2f65a7deb6fd743edad22a25a3e811e544686c4d556193074bbf642",
    "0ce4ae35360032521b63ef0521ebd6d6e95618f9960c0af7aae8f130e593c57f",
    "0a09f260b768edec68fabb062ae14003fc3d7e54699a0617f3a54945670897d5",
    "25d4841abf00cd1d9f0e49c7b0c45d1b9996944b8689132ec3e75118800fd314",
    "e4e2a76a51a54088bcd0faaca4ac865bb7d7ad98bd648bf222f0890509a8cd16",
    "b8e462aedba4b08f25a1d9ea6c6cb30b02d78ab9fa939e67495223d1cc86447f",
    "dfd1094b5d62725fcb8657018ea8abefdc1f699bcebb0d353b712c738aace121",
    "cd7f6da2cad56451afddc83705bf35c0c6877b65892e3fede7486adfdd566d52",
    "746bd32616289b074df4a54e8405e1e8347c06ff68b4829f26f08e50df3b38c9",
    "c448b118071af8abfc9d8ba14012ead25ba9055dca3e80227e8a50c3b6bed34a",
    "544a2a77cfd3cb5711931e334651399079edec9a4230c7e99878e00959bdd634",
    "f94017e9bc24a6df952f3e77cbcc2cb59950c75be54ca7067e501ab1e414471c",
    "c36f9cca9aa9f66790ef9232b138a9e6aeaf35b0ce561ca837a4b56e3d1bab57",
    "93cfe9c8ed504f31b11bb046312289e3b31b28456ddc28415d0edd41ac4be956",
    "96f1916458063414bc5b68f272c8d25c1c7332d9996e6791773962562210bb8d",
    "5f01e2822c47bc0878b79e10098be59a28fb2d77052c330d8bdc9a80348483d3",
    "dc225cdb37f8354e98aa3b6f628f92a59336b8271632a1e227a35591f93016db",
    "6a17e199b33042a5e959030fcd51ae45e87cc1db43ee3c3e2ce0b127e51f01aa",
    "d9a0a98596aad579e2e10dcb38ab4a0c4b29f70dab4588611cdcbd830601c1a3",
];

fn denylisted(token: &str) -> bool {
    let sum = Sha256::digest(format!("{SALT}{token}").as_bytes());
    let hex: String = sum.iter().map(|b| format!("{b:02x}")).collect();
    DENYLIST.contains(&hex.as_str())
}

fn cyrillic(c: char) -> bool {
    matches!(
        c,
        '\u{0400}'..='\u{0484}'
            | '\u{0487}'..='\u{052F}'
            | '\u{1C80}'..='\u{1C8A}'
            | '\u{1D2B}'
            | '\u{1D78}'
            | '\u{2DE0}'..='\u{2DFF}'
            | '\u{A640}'..='\u{A69F}'
            | '\u{FE2E}'..='\u{FE2F}'
            | '\u{1E030}'..='\u{1E06D}'
            | '\u{1E08F}'
    )
}

fn leak_lines(content: &str) -> Vec<usize> {
    let mut lines = Vec::new();
    let mut prev = String::new();
    for (n, raw) in content.lines().enumerate() {
        let line: String = raw.to_lowercase().nfkc().collect();
        let mut hit = line.chars().any(cyrillic);
        for word in line
            .split(|c: char| !c.is_alphabetic() && !c.is_numeric())
            .filter(|w| !w.is_empty())
        {
            hit = hit
                || denylisted(word)
                || (!prev.is_empty() && denylisted(&format!("{prev} {word}")));
            prev = word.to_string();
        }
        if hit {
            lines.push(n + 1);
        }
    }
    lines
}

#[test]
fn leak_lines_flags_cyrillic_and_ignores_plain_text() {
    assert_eq!(leak_lines("plain text\nкириллица\n"), [2]);
    assert!(leak_lines("SELECT 1;\n-- deploy the project\n").is_empty());
}

#[test]
fn shipped_content_carries_no_source_product_vocabulary() {
    let mut hits = Vec::new();
    for prefix in ["templates", "skills", "ai"] {
        for (path, text) in tree(prefix) {
            for line in leak_lines(&text) {
                hits.push(format!("{path}:{line}: denylisted term"));
            }
        }
    }
    assert!(hits.is_empty(), "{}", hits.join("\n"));
}
