use crate::error::{Error, Result};
use crate::project::lexer::{self, Token, TokenKind};

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Unit {
    pub(crate) text: String,
    pub(crate) start: usize,
}

fn words<'a>(src: &'a [u8], toks: &[Token]) -> Vec<&'a [u8]> {
    toks.iter()
        .filter(|t| !t.is_trivia() && t.kind != TokenKind::Semicolon)
        .map(|t| t.text(src))
        .collect()
}

fn eq(a: &[u8], b: &str) -> bool {
    a.eq_ignore_ascii_case(b.as_bytes())
}

pub(crate) fn is_terminator(stmt: &[&[u8]]) -> bool {
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

pub(crate) fn head_commits(head: &str) -> bool {
    let bytes = head.as_bytes();
    statements(head)
        .iter()
        .rev()
        .map(|(s, e)| words(&bytes[*s..*e], &lexer::tokens(&bytes[*s..*e])))
        .find(|w| !w.is_empty())
        .is_some_and(|w| (eq(w[0], "COMMIT") || eq(w[0], "END")) && is_terminator(&w))
}

pub(crate) fn statements(src: &str) -> Vec<(usize, usize)> {
    let bytes = src.as_bytes();
    let toks = lexer::tokens(bytes);
    let mut out = Vec::new();
    let mut start = 0;
    let mut atomic = 0usize;
    let mut parens = 0usize;
    let mut prev_word: Option<&[u8]> = None;
    for t in &toks {
        match t.kind {
            TokenKind::LParen => parens += 1,
            TokenKind::RParen => parens = parens.saturating_sub(1),
            _ => {}
        }
        if t.kind == TokenKind::Word {
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
        if t.kind == TokenKind::Semicolon && atomic == 0 && parens == 0 {
            out.push((start, t.end));
            start = t.end;
        }
    }
    if start < bytes.len() {
        out.push((start, bytes.len()));
    }
    out
}

pub(crate) fn split(src: &str) -> Vec<Unit> {
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
            .any(|t| !t.is_trivia() && t.kind != TokenKind::Semicolon)
    };
    let tail_from = match head_end {
        None => {
            if has_code(0, bytes.len()) {
                units.push(Unit {
                    text: src.to_string(),
                    start: 0,
                });
            }
            return units;
        }
        Some(i) => {
            let end = stmts[i].1;
            units.push(Unit {
                text: src[..end].to_string(),
                start: 0,
            });
            i + 1
        }
    };
    for (s, e) in &stmts[tail_from..] {
        if has_code(*s, *e) {
            units.push(Unit {
                text: src[*s..*e].to_string(),
                start: *s,
            });
        }
    }
    units
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Macro {
    pub(crate) start: usize,
    pub(crate) end: usize,
    pub(crate) pattern: Option<String>,
    pub(crate) callback: Option<String>,
}

fn literal(src: &[u8], t: &Token) -> Option<String> {
    let text = std::str::from_utf8(t.text(src)).ok()?;
    let body = text.strip_prefix('\'')?.strip_suffix('\'')?;
    Some(body.replace("''", "'"))
}

pub(crate) fn macros(src: &str) -> Vec<Macro> {
    let bytes = src.as_bytes();
    let toks: Vec<Token> = lexer::tokens(bytes);
    let sig: Vec<&Token> = toks
        .iter()
        .filter(|t| match t.kind {
            TokenKind::Space => t.text(bytes).contains(&0x0b),
            TokenKind::LineComment | TokenKind::BlockComment => false,
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
        if dot.kind != TokenKind::Other || dot.text(src) != b"." {
            return None;
        }
        j += 2;
    }
    if !sig.get(j)?.is_word(src, "pgmi_test") {
        return None;
    }
    j += 1;
    if sig.get(j)?.kind != TokenKind::LParen {
        return None;
    }
    j += 1;
    let mut args: Vec<Option<String>> = Vec::new();
    loop {
        let t = sig.get(j)?;
        match t.kind {
            TokenKind::RParen => break,
            TokenKind::Str => args.push(Some(literal(src, t)?)),
            TokenKind::Word if t.is_word(src, "NULL") => args.push(None),
            _ => return None,
        }
        j += 1;
        let t = sig.get(j)?;
        match t.kind {
            TokenKind::RParen => break,
            TokenKind::Comma if args.len() < 2 => j += 1,
            _ => return None,
        }
    }
    let close = sig.get(j)?;
    let mut it = args.into_iter();
    Some((
        Macro {
            start: sig[i].start,
            end: close.end,
            pattern: it.next().flatten().filter(|p| !p.is_empty()),
            callback: it.next().flatten().filter(|c| !c.is_empty()),
        },
        j + 1,
    ))
}

pub(crate) fn check_callback(callback: &str) -> Result<()> {
    let parts: Vec<&str> = callback.split('.').collect();
    let refuse = |why: String| {
        Err(Error::config(format!(
            "invalid callback {callback:?}: {why}"
        )))
    };
    if parts.len() > 2 {
        return refuse("expected [schema.]function format".into());
    }
    for part in parts {
        let mut chars = part.chars();
        match chars.next() {
            None => return refuse("empty identifier".into()),
            Some(_) if part.len() > 63 => {
                return refuse(format!("identifier {part:?} exceeds 63 character limit"));
            }
            Some(c)
                if (c.is_ascii_alphabetic() || c == '_')
                    && chars.all(|c| c.is_ascii_alphanumeric() || c == '_') => {}
            Some(_) => return refuse(format!("{part:?} is not a valid identifier")),
        }
    }
    Ok(())
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct Span {
    line: usize,
    first: usize,
    last: usize,
    shift_after: isize,
}

/// Maps a line of an expanded unit back to the line of the unit as written;
/// a line inside a macro's expansion maps to the macro's own line.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(crate) struct LineMap(Vec<Span>);

/// Where a line of an expanded unit came from: a line as written, or the
/// line of the macro whose expansion holds it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Origin {
    Written(usize),
    Macro(usize),
}

impl Origin {
    pub(crate) fn line(self) -> usize {
        match self {
            Origin::Written(l) | Origin::Macro(l) => l,
        }
    }
}

impl LineMap {
    pub(crate) fn original(&self, expanded: usize) -> Origin {
        let mut shift = 0;
        for span in &self.0 {
            if expanded < span.first {
                break;
            }
            if expanded <= span.last {
                return Origin::Macro(span.line);
            }
            shift = span.shift_after;
        }
        Origin::Written(expanded.saturating_add_signed(-shift))
    }
}

fn newlines(s: &str) -> usize {
    s.bytes().filter(|b| *b == b'\n').count()
}

pub(crate) fn expand(src: &str, found: &[Macro], generated: &[String]) -> (String, LineMap) {
    let mut out = String::with_capacity(src.len());
    let mut spans = Vec::new();
    let mut shift: isize = 0;
    let mut at = 0;
    for (m, g) in found.iter().zip(generated) {
        out.push_str(&src[at..m.start]);
        out.push_str(g);
        at = m.end;
        let line = newlines(&src[..m.start]) + 1;
        let span = newlines(g);
        let first = line.saturating_add_signed(shift);
        shift = shift
            .saturating_add_unsigned(span)
            .saturating_sub_unsigned(newlines(&src[m.start..m.end]));
        spans.push(Span {
            line,
            first,
            last: first + span,
            shift_after: shift,
        });
    }
    out.push_str(&src[at..]);
    (out, LineMap(spans))
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
    fn a_semicolon_inside_parentheses_does_not_end_a_tail_statement() {
        let src = "BEGIN;\nCOMMIT;\nCREATE RULE r AS ON INSERT TO t DO ALSO (INSERT INTO u VALUES (1); INSERT INTO u VALUES (2));\nSELECT 1;\n";
        let t = texts(src);
        assert_eq!(t.len(), 3, "{t:?}");
        assert!(
            t[1].starts_with("CREATE RULE") && t[1].ends_with("(2));"),
            "{t:?}"
        );
        assert_eq!(texts("BEGIN; COMMIT; SELECT ';' , (1); SELECT 2;").len(), 3);
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
            )
            .0,
            "a X b"
        );
        let src = "BEGIN;\nCALL pgmi_test();\nSELECT 1;\nCALL pgmi_test(\n'x');\nSELEC 2;\n";
        let (text, lines) = expand(src, &macros(src), &["A\nB\nC".into(), "D".into()]);
        assert_eq!(text, "BEGIN;\nA\nB\nC;\nSELECT 1;\nD;\nSELEC 2;\n");
        let mapped: Vec<_> = (1..=7).map(|l| lines.original(l)).collect();
        assert_eq!(
            mapped,
            [
                Origin::Written(1),
                Origin::Macro(2),
                Origin::Macro(2),
                Origin::Macro(2),
                Origin::Written(3),
                Origin::Macro(4),
                Origin::Written(6)
            ]
        );
        assert_eq!(LineMap::default().original(9), Origin::Written(9));
        assert!(macros("CALL pgmi_test('a','b','c')").is_empty());
        assert_eq!(
            macros("CALL /* c */ pgmi_test -- c\n( '/a/' /* c */ ) ;").len(),
            1
        );
        assert!(macros("CALL\x0bpgmi_test();").is_empty());
        assert!(macros("CALL\u{a0}pgmi_test();").is_empty());
        assert_eq!(macros("CALL\x0c\tpgmi_test\r\n();").len(), 1);
    }

    #[test]
    fn crlf_splits_like_lf() {
        for (src, want) in [
            ("BEGIN;\nSELECT 1;\nCOMMIT;\nSELECT 2;\n", 2),
            ("BEGIN;\nSELECT 1;\nCOMMIT;\n", 1),
            ("COMMIT;\nSELECT 1;\nSELECT 2;\n", 3),
        ] {
            let crlf = src.replace('\n', "\r\n");
            assert_eq!(split(src).len(), want, "{src:?}");
            assert_eq!(split(&crlf).len(), want, "{crlf:?}");
        }
    }

    #[test]
    fn every_unit_knows_where_it_starts_in_the_file() {
        let src = "BEGIN;\nSELECT 1;\nCOMMIT;\nSELECT 'a';\nSELECT 'b';\n";
        let units = split(src);
        assert_eq!(units.len(), 3);
        for u in &units {
            assert_eq!(&src[u.start..u.start + u.text.len()], u.text);
        }
        for (u, stmt) in units[1..].iter().zip(["SELECT 'a';", "SELECT 'b';"]) {
            assert_eq!(
                u.start + u.text.find("SELECT").unwrap(),
                src.find(stmt).unwrap()
            );
        }
    }

    #[test]
    fn a_tail_heavy_script_splits_in_linear_space() {
        let mut src = String::from("BEGIN;\nSELECT 1;\nCOMMIT;\n");
        for _ in 0..4000 {
            src.push_str("UPDATE t SET c = c + 1 WHERE id = 00000000;\n");
        }
        let units = split(&src);
        assert_eq!(units.len(), 4001);
        assert!(units.iter().map(|u| u.text.len()).sum::<usize>() <= src.len());
    }

    #[test]
    fn the_template_shape_is_a_head_and_one_banner_unit() {
        let src = "BEGIN;\nSELECT pg_temp.deploy();\nSAVEPOINT _tests;\nROLLBACK TO SAVEPOINT _tests;\nCOMMIT;\nDO $$\nBEGIN\n    RAISE NOTICE 'done';\nEND $$;\n";
        let units = split(src);
        assert_eq!(units.len(), 2);
        assert!(units[0].text.trim_end().ends_with("COMMIT;"));
        assert!(units[1].text.contains("RAISE NOTICE"));
    }

    #[test]
    fn head_commits_only_on_commit_or_end() {
        for (src, want) in [
            ("BEGIN; CREATE TABLE t(id int); COMMIT; SELECT 1;", true),
            (
                "BEGIN; INSERT INTO t VALUES (1); COMMIT AND CHAIN; SELECT 1/0;",
                true,
            ),
            (
                "BEGIN; INSERT INTO t VALUES (1); END AND CHAIN; SELECT 1;",
                true,
            ),
            ("BEGIN; INSERT INTO t VALUES (1); END; SELECT 1;", true),
            (
                "BEGIN; INSERT INTO t VALUES (1); ROLLBACK AND CHAIN; SELECT 1;",
                false,
            ),
            ("BEGIN; INSERT INTO t VALUES (1); ABORT; SELECT 1;", false),
            (
                "BEGIN;\nINSERT INTO t VALUES (1); -- a comment; COMMIT\nCOMMIT WORK AND CHAIN;\nSELECT 1;",
                true,
            ),
        ] {
            let units = split(src);
            assert!(units.len() >= 2, "{src:?}");
            assert_eq!(head_commits(&units[0].text), want, "{src:?}");
        }
        assert!(!head_commits("BEGIN; SELECT 1;"));
        assert!(!head_commits("-- COMMIT;\nSELECT 1;"));
    }

    #[test]
    fn a_macro_in_the_head_expands_inside_the_head() {
        let expansion = "SAVEPOINT __pgmi_d1__;\nDO $__pgmi__$ BEGIN EXECUTE 'SELECT 1'; END $__pgmi__$;\nROLLBACK TO SAVEPOINT __pgmi_d1__;\nRELEASE SAVEPOINT __pgmi_d1__;";
        let units = split("BEGIN;\nCALL pgmi_test();\nCOMMIT;\nSELECT 'tail';\n");
        assert_eq!(units.len(), 2);
        assert!(macros(&units[1].text).is_empty());
        let (head, _) = expand(&units[0].text, &macros(&units[0].text), &[expansion.into()]);
        assert!(head.trim_end().ends_with("COMMIT;"));
        assert!(
            head.contains("SAVEPOINT __pgmi_d1__;")
                && head.contains("ROLLBACK TO SAVEPOINT __pgmi_d1__;")
        );
        assert_eq!(split(&head).len(), 1);
    }

    #[test]
    fn a_macro_in_the_tail_is_one_unit_and_its_savepoints_stay_together() {
        let units = split("BEGIN;\nSELECT 1;\nCOMMIT;\nCALL pgmi_test();\n");
        assert_eq!(units.len(), 2);
        let found = macros(&units[1].text);
        assert_eq!(found.len(), 1);
        let (tail, _) = expand(
            &units[1].text,
            &found,
            &["SAVEPOINT a;\nSELECT 1;\nRELEASE SAVEPOINT a;".into()],
        );
        assert_eq!(
            tail.trim(),
            "SAVEPOINT a;\nSELECT 1;\nRELEASE SAVEPOINT a;;"
        );
    }

    #[test]
    fn an_empty_expansion_leaves_the_rest_in_place() {
        let src = "BEGIN;\nCALL pgmi_test();\nCOMMIT;";
        let found = macros(src);
        assert_eq!(found.len(), 1);
        assert_eq!(
            expand(src, &found, &[String::new()]).0,
            "BEGIN;\n;\nCOMMIT;"
        );
        assert_eq!(expand("", &macros(""), &[]).0, "");
    }

    #[test]
    fn an_empty_pattern_or_callback_means_the_default() {
        let m = macros("CALL pgmi_test('', '');");
        assert_eq!(m.len(), 1);
        assert_eq!(
            (m[0].pattern.as_deref(), m[0].callback.as_deref()),
            (None, None)
        );
    }

    #[test]
    fn line_map_maps_expanded_lines_back_to_the_file() {
        let src = "BEGIN;\nSAVEPOINT s;\nCALL pgmi_test();\nROLLBACK TO SAVEPOINT s;\nCOMMIT;\nSELECT 1/0;\n";
        let (_, lines) = expand(src, &macros(src), &["A;\nB;\nC;\nD;".into()]);
        for (expanded, want) in [
            (1, Origin::Written(1)),
            (2, Origin::Written(2)),
            (3, Origin::Macro(3)),
            (6, Origin::Macro(3)),
            (7, Origin::Written(4)),
            (9, Origin::Written(6)),
        ] {
            assert_eq!(lines.original(expanded), want, "line {expanded}");
        }
        let src = "CALL pgmi_test();\nX;\nCALL pgmi_test();\nY;\n";
        let (_, lines) = expand(src, &macros(src), &["a;\nb;".into(), "c;\nd;\ne;".into()]);
        for (expanded, want) in [(3, 2), (4, 3), (6, 3), (7, 4)] {
            assert_eq!(lines.original(expanded).line(), want, "line {expanded}");
        }
    }

    #[test]
    fn callback_names_are_bare_or_schema_qualified_identifiers() {
        for ok in [
            "my_callback",
            "pg_temp.my_callback",
            "myschema.my_func",
            "_private",
            "Func123",
            "UPPER_CASE",
        ] {
            assert!(check_callback(ok).is_ok(), "{ok}");
        }
        for (bad, why) in [
            ("schema.func.extra", "expected [schema.]function format"),
            (".", "empty identifier"),
            ("schema.", "empty identifier"),
            (".func", "empty identifier"),
            ("123start", "\"123start\" is not a valid identifier"),
            ("has-dash", "\"has-dash\" is not a valid identifier"),
            ("has space", "\"has space\" is not a valid identifier"),
            (
                "func;DROP TABLE",
                "\"func;DROP TABLE\" is not a valid identifier",
            ),
            (
                "DROP TABLE users; --",
                "\"DROP TABLE users; --\" is not a valid identifier",
            ),
        ] {
            let e = check_callback(bad).unwrap_err();
            assert_eq!(e.kind(), crate::error::Kind::Config, "{bad}");
            assert_eq!(e.text(), format!("invalid callback {bad:?}: {why}"));
        }
        let long = "a".repeat(64);
        assert_eq!(
            check_callback(&long).unwrap_err().text(),
            format!("invalid callback {long:?}: identifier {long:?} exceeds 63 character limit")
        );
        assert!(check_callback(&"a".repeat(63)).is_ok());
    }

    #[test]
    fn a_non_ascii_character_glued_to_call_is_part_of_the_word() {
        for src in [
            "\u{a0}CALL pgmi_test();",
            "\u{2014}CALL pgmi_test();",
            "\u{4e00}CALL pgmi_test();",
        ] {
            assert!(macros(src).is_empty(), "{src:?}");
        }
        let src = "\u{4e00} CALL pgmi_test();";
        let m = macros(src);
        assert_eq!(m.len(), 1);
        assert_eq!(&src[m[0].start..m[0].end], "CALL pgmi_test()");
    }

    #[test]
    fn only_a_complete_call_outside_literals_is_a_macro() {
        for src in [
            "",
            "SELECT * FROM users;",
            "CALL pgmi_testing();",
            "CALL pgmi_test",
            "CALL pgmi_test(",
            "SELECT pgmi_test()",
            "PERFORM pgmi_test()",
            "pgmi_test()",
            "EXECUTE 'CALL pgmi_test();';",
            "EXECUTE E'x\\'; CALL pgmi_test();';",
            "SELECT 1 AS \"CALL pgmi_test();\";",
            "CREATE FUNCTION f() RETURNS void AS $body$ SELECT 'CALL pgmi_test()' $body$ LANGUAGE sql;",
            "DO $$ BEGIN CALL pgmi_test(); END $$;",
            "-- CALL pgmi_test('./api/**');\nSELECT 1;",
            "/* a /* nested */ CALL pgmi_test(); */ SELECT 1;",
        ] {
            assert!(macros(src).is_empty(), "{src:?}");
        }
        let src = "SELECT 'x';CALL pgmi_test('./api/**');SELECT 'y'; CALL pgmi_test(NULL)";
        let m = macros(src);
        assert_eq!(m.len(), 2);
        assert_eq!(m[0].pattern.as_deref(), Some("./api/**"));
        assert_eq!(m[1].pattern, None);
        assert!(m[0].start < m[1].start);
        assert_eq!(&src[m[1].start..m[1].end], "CALL pgmi_test(NULL)");
    }

    #[test]
    fn every_macro_form_the_contract_publishes_is_detected() {
        let contract: serde_json::Value =
            serde_json::from_str(crate::commands::ai::CONTRACT).unwrap();
        let forms = contract["macros"].as_array().unwrap();
        assert!(!forms.is_empty());
        for form in forms.iter().map(|m| m["form"].as_str().unwrap()) {
            let src = format!("BEGIN;\n{form};\nCOMMIT;\n");
            let found = macros(&src);
            assert_eq!(found.len(), 1, "{form}");
            assert_eq!(&src[found[0].start..found[0].end], form);
            let args: Vec<&str> = form.split('\'').skip(1).step_by(2).collect();
            assert_eq!(found[0].pattern.as_deref(), args.first().copied(), "{form}");
            assert_eq!(found[0].callback.as_deref(), args.get(1).copied(), "{form}");
        }
        assert_eq!(
            macros("BEGIN;\nCALL pg_temp.pgmi_test();\nCOMMIT;\n").len(),
            1
        );
    }
}
