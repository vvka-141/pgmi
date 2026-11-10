use crate::project::lexer::{self, Kind, Token};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Unit {
    pub text: String,
    pub line: usize,
}

fn words<'a>(src: &'a [u8], toks: &[Token]) -> Vec<&'a [u8]> {
    toks.iter()
        .filter(|t| !t.is_trivia() && t.kind != Kind::Semicolon)
        .map(|t| t.text(src))
        .collect()
}

fn eq(a: &[u8], b: &str) -> bool {
    a.eq_ignore_ascii_case(b.as_bytes())
}

pub fn is_terminator(stmt: &[&[u8]]) -> bool {
    let Some((first, rest)) = stmt.split_first() else {
        return false;
    };
    if !["COMMIT", "END", "ROLLBACK", "ABORT"]
        .iter()
        .any(|w| eq(first, w))
    {
        return false;
    }
    let mut rest = rest;
    if let Some((w, r)) = rest.split_first()
        && (eq(w, "WORK") || eq(w, "TRANSACTION"))
    {
        rest = r;
    }
    match rest {
        [] => true,
        [and, chain] => eq(and, "AND") && eq(chain, "CHAIN"),
        [and, no, chain] => eq(and, "AND") && eq(no, "NO") && eq(chain, "CHAIN"),
        _ => false,
    }
}

pub fn statements(src: &str) -> Vec<(usize, usize)> {
    let bytes = src.as_bytes();
    let toks = lexer::tokens(bytes);
    let mut out = Vec::new();
    let mut start = 0;
    let mut atomic = 0usize;
    let mut prev_word: Option<&[u8]> = None;
    for t in &toks {
        if t.kind == Kind::Word {
            let w = t.text(bytes);
            if atomic == 0 {
                if eq(w, "ATOMIC") && prev_word.is_some_and(|p| eq(p, "BEGIN")) {
                    atomic = 1;
                }
            } else if eq(w, "CASE") || eq(w, "BEGIN") {
                atomic += 1;
            } else if eq(w, "END") {
                atomic -= 1;
            }
            prev_word = Some(w);
        } else if !t.is_trivia() {
            prev_word = None;
        }
        if t.kind == Kind::Semicolon && atomic == 0 {
            out.push((start, t.end));
            start = t.end;
        }
    }
    if start < bytes.len() {
        out.push((start, bytes.len()));
    }
    out
}

fn line_of(src: &str, pos: usize) -> usize {
    src.as_bytes()[..pos]
        .iter()
        .filter(|b| **b == b'\n')
        .count()
        + 1
}

pub fn split(src: &str) -> Vec<Unit> {
    let bytes = src.as_bytes();
    let stmts = statements(src);
    let mut units = Vec::new();
    let mut head_end = None;
    for (i, (s, e)) in stmts.iter().enumerate() {
        let toks = lexer::tokens(&bytes[*s..*e]);
        if is_terminator(&words(&bytes[*s..*e], &toks)) {
            head_end = Some(i);
            break;
        }
    }
    let has_code = |s: usize, e: usize| {
        lexer::tokens(&bytes[s..e])
            .iter()
            .any(|t| !t.is_trivia() && t.kind != Kind::Semicolon)
    };
    let tail_from = match head_end {
        None => {
            if has_code(0, bytes.len()) {
                units.push(Unit {
                    text: src.to_string(),
                    line: 1,
                });
            }
            return units;
        }
        Some(i) => {
            let end = stmts[i].1;
            units.push(Unit {
                text: src[..end].to_string(),
                line: 1,
            });
            i + 1
        }
    };
    for (s, e) in &stmts[tail_from..] {
        if has_code(*s, *e) {
            units.push(Unit {
                text: src[*s..*e].to_string(),
                line: line_of(src, *s),
            });
        }
    }
    units
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Macro {
    pub start: usize,
    pub end: usize,
    pub pattern: Option<String>,
    pub callback: Option<String>,
}

fn literal(src: &[u8], t: &Token) -> Option<String> {
    let text = std::str::from_utf8(t.text(src)).ok()?;
    let body = text.strip_prefix('\'')?.strip_suffix('\'')?;
    Some(body.replace("''", "'"))
}

pub fn macros(src: &str) -> Vec<Macro> {
    let bytes = src.as_bytes();
    let toks: Vec<Token> = lexer::tokens(bytes);
    let sig: Vec<&Token> = toks
        .iter()
        .filter(|t| match t.kind {
            Kind::Space => t.text(bytes).contains(&0x0b),
            Kind::LineComment | Kind::BlockComment => false,
            _ => true,
        })
        .collect();
    let mut out = Vec::new();
    let mut i = 0;
    while i < sig.len() {
        if let Some((m, next)) = macro_at(bytes, &sig, i) {
            out.push(m);
            i = next;
        } else {
            i += 1;
        }
    }
    out
}

fn macro_at(src: &[u8], sig: &[&Token], i: usize) -> Option<(Macro, usize)> {
    if !sig.get(i)?.is_word(src, "CALL") {
        return None;
    }
    let mut j = i + 1;
    if sig.get(j)?.is_word(src, "pg_temp") {
        let dot = sig.get(j + 1)?;
        if dot.kind != Kind::Other || dot.text(src) != b"." {
            return None;
        }
        j += 2;
    }
    if !sig.get(j)?.is_word(src, "pgmi_test") {
        return None;
    }
    j += 1;
    if sig.get(j)?.kind != Kind::LParen {
        return None;
    }
    j += 1;
    let mut args: Vec<Option<String>> = Vec::new();
    loop {
        let t = sig.get(j)?;
        match t.kind {
            Kind::RParen => break,
            Kind::Str => args.push(Some(literal(src, t)?)),
            Kind::Word if t.is_word(src, "NULL") => args.push(None),
            _ => return None,
        }
        j += 1;
        let t = sig.get(j)?;
        match t.kind {
            Kind::RParen => break,
            Kind::Comma if args.len() < 2 => j += 1,
            _ => return None,
        }
    }
    let close = sig.get(j)?;
    let mut it = args.into_iter();
    Some((
        Macro {
            start: sig[i].start,
            end: close.end,
            pattern: it.next().flatten(),
            callback: it.next().flatten(),
        },
        j + 1,
    ))
}

pub fn expand(src: &str, found: &[Macro], generated: &[String]) -> String {
    let mut out = String::with_capacity(src.len());
    let mut at = 0;
    for (m, g) in found.iter().zip(generated) {
        out.push_str(&src[at..m.start]);
        out.push_str(g);
        at = m.end;
    }
    out.push_str(&src[at..]);
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn texts(src: &str) -> Vec<String> {
        split(src)
            .into_iter()
            .map(|u| u.text.trim().to_string())
            .collect()
    }

    #[test]
    fn head_and_tail() {
        assert_eq!(
            texts("CREATE TABLE a(); SELECT 1/0;"),
            vec!["CREATE TABLE a(); SELECT 1/0;"]
        );
        assert_eq!(
            texts(
                "BEGIN;\nCREATE TABLE t (x int);\nCOMMIT;\nCREATE INDEX CONCURRENTLY t_x ON t (x);\n-- c\nSELECT 1;\n"
            ),
            vec![
                "BEGIN;\nCREATE TABLE t (x int);\nCOMMIT;",
                "CREATE INDEX CONCURRENTLY t_x ON t (x);",
                "-- c\nSELECT 1;"
            ]
        );
        assert_eq!(
            texts("BEGIN; ROLLBACK TO SAVEPOINT a; COMMIT PREPARED 'x'; SELECT 1;").len(),
            1
        );
        assert_eq!(texts("begin; commit work and no chain; select 1").len(), 2);
        assert_eq!(
            texts("SELECT 'COMMIT;'; $$ COMMIT; $$; COMMIT; x;").len(),
            2
        );
        assert!(texts("-- only\n").is_empty());
    }

    #[test]
    fn begin_atomic_is_stepped_over() {
        let src = "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT CASE WHEN true THEN 1 END; SELECT 2; END; COMMIT; SELECT 3;";
        let t = texts(src);
        assert_eq!(t.len(), 2);
        assert!(t[0].ends_with("COMMIT;"));
    }

    #[test]
    fn macro_forms() {
        let src = "BEGIN;\n-- CALL pgmi_test();\n/* CALL pgmi_test(); */\nSELECT 'CALL pgmi_test();';\nDO $x$ BEGIN RAISE NOTICE 'CALL pgmi_test();'; END $x$;\ncall PG_TEMP.PGMI_TEST ( '/a/' ) ;\nCOMMIT;\n";
        let m = macros(src);
        assert_eq!(m.len(), 1);
        assert_eq!(m[0].pattern.as_deref(), Some("/a/"));
        assert_eq!(
            &src[m[0].start..m[0].end],
            "call PG_TEMP.PGMI_TEST ( '/a/' )"
        );
        let m = macros(
            "CALL pgmi_test('it''s', 'pg_temp.cb'); CALL pgmi_test(NULL, 'x'); CALL pgmi_test();",
        );
        assert_eq!(m.len(), 3);
        assert_eq!(m[0].pattern.as_deref(), Some("it's"));
        assert_eq!(m[0].callback.as_deref(), Some("pg_temp.cb"));
        assert_eq!(m[1].pattern, None);
        assert_eq!(
            expand(
                "a CALL pgmi_test() b",
                &macros("a CALL pgmi_test() b"),
                &["X".into()]
            ),
            "a X b"
        );
        assert!(macros("CALL pgmi_test('a','b','c')").is_empty());
        assert_eq!(
            macros("CALL /* c */ pgmi_test -- c\n( '/a/' /* c */ ) ;").len(),
            1
        );
        assert!(macros("CALL\x0bpgmi_test();").is_empty());
        assert!(macros("CALL\u{a0}pgmi_test();").is_empty());
        assert_eq!(macros("CALL\x0c\tpgmi_test\r\n();").len(), 1);
    }
}
