use super::script::LineMap;
use crate::error::SqlDiagnostics;
use crate::project::walk::{File, is_sql};
use std::fmt::Write as _;

/// The execution unit a failure happened in: its text as sent, where it
/// starts in `deploy.sql`, and how its lines map back after macro expansion.
pub(crate) struct Sent<'a> {
    pub(crate) deploy_sql: &'a str,
    pub(crate) start: usize,
    pub(crate) text: &'a str,
    pub(crate) lines: &'a LineMap,
    pub(crate) tail: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Line {
    pub(crate) number: usize,
    pub(crate) column: usize,
    pub(crate) text: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Location {
    pub(crate) script: String,
    pub(crate) line: Option<Line>,
}

/// Where a failed statement came from, as far as the server's diagnostics
/// and the project's files tell.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(crate) struct Blame {
    pub(crate) candidates: Vec<String>,
    pub(crate) file: Option<String>,
    pub(crate) location: Option<Location>,
    pub(crate) where_: Option<String>,
}

impl Blame {
    pub(crate) fn new(d: &SqlDiagnostics, message: &str, sent: &Sent<'_>, files: &[&File]) -> Self {
        let where_ = d.where_.as_deref().unwrap_or_default();
        let failed = failed_files(d, files);
        let location = locate(d, where_, sent, failed.first().copied());
        let file = failed
            .first()
            .map(|f| f.path.clone())
            .or_else(|| failed_in(message).map(str::to_string))
            .or_else(|| location.as_ref().map(|l| l.script.clone()));
        Blame {
            candidates: failed.iter().map(|f| f.path.clone()).collect(),
            file,
            location,
            where_: d.where_.as_deref().map(|w| named_where(w, files)),
        }
    }

    pub(crate) fn phase(&self) -> &'static str {
        if self.file.as_deref().is_some_and(is_test_path) {
            "test"
        } else {
            "deploy"
        }
    }

    pub(crate) fn render(&self, out: &mut String) {
        match &self.location {
            Some(loc) => match &loc.line {
                None => {
                    let _ = write!(out, "\nLOCATION: {}", loc.script);
                }
                Some(l) => {
                    let _ = write!(out, "\nLOCATION: {} line {}", loc.script, l.number);
                    if l.column > 0 {
                        let _ = write!(out, ", column {}", l.column);
                    }
                    if !l.text.is_empty() {
                        let prefix = format!("LINE {}: ", l.number);
                        let _ = write!(out, "\n{prefix}{}", l.text);
                        if l.column > 0 {
                            let pad = prefix.chars().count() + l.column - 1;
                            let _ = write!(out, "\n{}^", " ".repeat(pad));
                        }
                    }
                }
            },
            None => {
                if let Some(f) = &self.file {
                    let _ = write!(out, "\nLOCATION: {f}");
                }
            }
        }
        if self.candidates.len() > 1 {
            let _ = write!(
                out,
                "\nThe same text is in {}; the error is in whichever of them deploy.sql ran.",
                self.candidates.join(", ")
            );
        }
    }
}

fn quoted(content: &str) -> String {
    format!("SQL statement \"{content}\"")
}

fn failed_files<'a>(d: &SqlDiagnostics, files: &[&'a File]) -> Vec<&'a File> {
    let sql = || files.iter().copied().filter(|f| is_sql(&f.path));
    let where_ = d.where_.as_deref().unwrap_or_default();
    let by_query = d
        .internal_query
        .as_deref()
        .filter(|q| !q.is_empty() && sql().any(|f| f.content == *q));
    let text = by_query.or_else(|| {
        sql()
            .filter(|f| !f.content.trim().is_empty() && where_.contains(&quoted(&f.content)))
            .map(|f| f.content.as_str())
            .fold(None, |best: Option<&str>, c| match best {
                Some(b) if b.len() >= c.len() => Some(b),
                _ => Some(c),
            })
    });
    match text {
        Some(t) => sql().filter(|f| f.content == t).collect(),
        None => Vec::new(),
    }
}

fn failed_in(message: &str) -> Option<&str> {
    const MARK: &str = "Failed in ";
    let mut rest = message;
    while let Some(i) = rest.find(MARK) {
        let after = &rest[i + MARK.len()..];
        let run = &after[..after
            .find([' ', '\t', '\n', '\x0c', '\r'])
            .unwrap_or(after.len())];
        if let Some(end) = run.rfind(".sql").filter(|e| *e > 0) {
            return Some(&run[..end + 4]);
        }
        rest = &rest[i + 1..];
    }
    None
}

pub(crate) fn is_test_path(path: &str) -> bool {
    let mut i = 0;
    while let Some(off) = path[i..].find("/__") {
        let seg_start = i + off + 1;
        let seg_end = path[seg_start..]
            .find('/')
            .map_or(path.len(), |e| seg_start + e);
        let seg = &path[seg_start..seg_end];
        if seg_end < path.len() && seg.len() >= 5 && seg.ends_with("__") {
            if matches!(&seg[2..seg.len() - 2], "test" | "tests") {
                return true;
            }
            i = seg_end + 1;
        } else {
            i = seg_start;
        }
    }
    false
}

fn named_where(where_: &str, files: &[&File]) -> String {
    files
        .iter()
        .filter(|f| is_sql(&f.path) && !f.content.trim().is_empty())
        .fold(where_.to_string(), |w, f| {
            w.replace(&quoted(&f.content), &format!("SQL statement in {}", f.path))
        })
}

fn locate(
    d: &SqlDiagnostics,
    where_: &str,
    sent: &Sent<'_>,
    file: Option<&File>,
) -> Option<Location> {
    if let Some(position) = d.position.filter(|p| *p > 0) {
        let (line, column) = resolve(sent.text, position)?;
        return Some(sent.locate(line, column));
    }
    if let Some(f) = file {
        return locate_in_file(d, where_, f);
    }
    if sent.tail {
        return Some(sent.locate(tail_line(sent.text, where_), 0));
    }
    Some(sent.locate_head(where_))
}

fn locate_in_file(d: &SqlDiagnostics, where_: &str, f: &File) -> Option<Location> {
    let content = f.content.as_str();
    if let (Some(query), Some(position)) = (d.internal_query.as_deref(), d.internal_position)
        && position > 0
        && !query.is_empty()
        && let Some(idx) = content.find(query)
    {
        let (number, column) = resolve(content, content[..idx].chars().count() + position)?;
        return Some(Location {
            script: f.path.clone(),
            line: Some(Line {
                number,
                column,
                text: nth_line(content, number),
            }),
        });
    }
    let number = file_line(content, where_);
    if number == 0 || number > newlines(content) + 1 {
        return None;
    }
    Some(Location {
        script: f.path.clone(),
        line: Some(Line {
            number,
            column: 0,
            text: nth_line(content, number),
        }),
    })
}

impl Sent<'_> {
    fn locate(&self, line: usize, column: usize) -> Location {
        let (original, in_macro) = self.lines.original(line);
        let mut column = if in_macro { 0 } else { column };
        let line_start = self.deploy_sql[..self.start]
            .rfind('\n')
            .map_or(0, |i| i + 1);
        if original == 1 && column > 0 {
            column += self.deploy_sql[line_start..self.start].chars().count();
        }
        let number = newlines(&self.deploy_sql[..self.start]) + original;
        Location {
            script: "deploy.sql".to_string(),
            line: (number > 0).then(|| Line {
                number,
                column,
                text: nth_line(self.deploy_sql, number),
            }),
        }
    }

    fn locate_head(&self, where_: &str) -> Location {
        let blocks: Vec<usize> = line_starts(self.text)
            .filter(|s| {
                let rest = &self.text[*s..];
                let indent = rest.len() - rest.trim_start_matches([' ', '\t']).len();
                do_block_start(&rest[indent..])
            })
            .collect();
        match blocks.as_slice() {
            [start] if !inline_lines(where_).is_empty() => self.locate(
                newlines(&self.text[..*start])
                    + do_block_error_line(&self.text[*start..], where_, true),
                0,
            ),
            _ => Location {
                script: "deploy.sql".to_string(),
                line: None,
            },
        }
    }
}

fn line_starts(s: &str) -> impl Iterator<Item = usize> + '_ {
    std::iter::once(0).chain(s.match_indices('\n').map(|(i, _)| i + 1))
}

fn newlines(s: &str) -> usize {
    s.bytes().filter(|b| *b == b'\n').count()
}

fn nth_line(s: &str, number: usize) -> String {
    number
        .checked_sub(1)
        .and_then(|i| s.split('\n').nth(i))
        .map(|l| l.trim_end_matches('\r').to_string())
        .unwrap_or_default()
}

fn resolve(text: &str, position: usize) -> Option<(usize, usize)> {
    if position > text.chars().count() + 1 {
        return None;
    }
    Some(
        text.chars()
            .take(position.saturating_sub(1))
            .fold((1, 1), |(line, column), c| {
                if c == '\n' {
                    (line + 1, 1)
                } else {
                    (line, column + 1)
                }
            }),
    )
}

fn is_space(b: u8) -> bool {
    matches!(b, b' ' | b'\t' | b'\n' | b'\r' | b'\x0c')
}

fn is_word(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'_'
}

fn keyword_then_space<'a>(s: &'a [u8], keyword: &str) -> Option<&'a [u8]> {
    let rest = s.get(keyword.len()..)?;
    if !s[..keyword.len()].eq_ignore_ascii_case(keyword.as_bytes()) {
        return None;
    }
    let n = rest.iter().take_while(|b| is_space(**b)).count();
    (n > 0).then(|| &rest[n..])
}

fn dollar_tag(s: &[u8]) -> bool {
    let Some(rest) = s.strip_prefix(b"$") else {
        return false;
    };
    let n = rest.iter().take_while(|b| is_word(**b)).count();
    rest.get(n) == Some(&b'$')
}

fn do_block_start(s: &str) -> bool {
    let Some(rest) = keyword_then_space(s.as_bytes(), "DO") else {
        return false;
    };
    if dollar_tag(rest) {
        return true;
    }
    let Some(rest) = keyword_then_space(rest, "LANGUAGE") else {
        return false;
    };
    let n = rest.iter().take_while(|b| is_word(**b)).count();
    let spaces = rest[n..].iter().take_while(|b| is_space(**b)).count();
    n > 0 && spaces > 0 && dollar_tag(&rest[n + spaces..])
}

fn inline_lines(where_: &str) -> Vec<&str> {
    const MARK: &str = "PL/pgSQL function inline_code_block line ";
    where_
        .match_indices(MARK)
        .filter_map(|(i, _)| {
            let digits = &where_[i + MARK.len()..];
            let n = digits.bytes().take_while(u8::is_ascii_digit).count();
            (n > 0).then(|| &digits[..n])
        })
        .collect()
}

fn do_block_error_line(unit: &str, where_: &str, outermost: bool) -> usize {
    let start = statement_start(unit);
    let lines = inline_lines(where_);
    let chosen = if outermost {
        lines.last()
    } else {
        lines.first()
    };
    let (true, Some(body)) = (do_block_start(&unit[start..]), chosen) else {
        return 0;
    };
    let Some(body) = body.parse::<usize>().ok().filter(|b| *b >= 1) else {
        return 0;
    };
    let tag = unit[start..].find('$').unwrap_or(0);
    newlines(&unit[..start + tag]) + body
}

fn file_line(content: &str, where_: &str) -> usize {
    match where_.find(&format!("SQL statement \"{content}")) {
        Some(i) => do_block_error_line(content, &where_[..i], true),
        None => do_block_error_line(content, where_, false),
    }
}

fn tail_line(unit: &str, where_: &str) -> usize {
    match do_block_error_line(unit, where_, true) {
        0 => newlines(&unit[..statement_start(unit)]) + 1,
        line => line,
    }
}

fn statement_start(unit: &str) -> usize {
    let b = unit.as_bytes();
    let mut i = 0;
    while i < b.len() {
        if matches!(b[i], b' ' | b'\t' | b'\r' | b'\n') {
            i += 1;
        } else if b[i..].starts_with(b"--") {
            match b[i..].iter().position(|c| *c == b'\n') {
                Some(end) => i += end + 1,
                None => return b.len(),
            }
        } else if b[i..].starts_with(b"/*") {
            let mut depth = 0usize;
            while i < b.len() {
                if b[i..].starts_with(b"/*") {
                    depth += 1;
                    i += 2;
                } else if b[i..].starts_with(b"*/") {
                    depth = depth.saturating_sub(1);
                    i += 2;
                    if depth == 0 {
                        break;
                    }
                } else {
                    i += 1;
                }
            }
        } else {
            return i;
        }
    }
    i
}

/// Replaces the literal of a SQL `PASSWORD '...'` clause, which a statement
/// quoted in an error's context or source line can carry.
pub(crate) fn redact_sql_passwords(text: &str) -> String {
    const WORD: &str = "password";
    let b = text.as_bytes();
    let mut out = String::with_capacity(text.len());
    let mut at = 0;
    let mut i = 0;
    while i + WORD.len() <= b.len() {
        let hit = b[i..i + WORD.len()].eq_ignore_ascii_case(WORD.as_bytes())
            && (i == 0 || !is_word(b[i - 1]));
        if let Some(end) = hit
            .then(|| password_literal_end(b, i + WORD.len()))
            .flatten()
        {
            out.push_str(&text[at..i]);
            out.push_str("PASSWORD [redacted]");
            at = end;
            i = end;
        } else {
            i += 1;
        }
    }
    out.push_str(&text[at..]);
    out
}

fn password_literal_end(b: &[u8], from: usize) -> Option<usize> {
    let spaces = b[from..].iter().take_while(|c| is_space(**c)).count();
    if spaces == 0 {
        return None;
    }
    let mut i = from + spaces;
    let escaped = matches!(b.get(i), Some(b'E' | b'e')) && b.get(i + 1) == Some(&b'\'');
    if escaped {
        i += 1;
    }
    if b.get(i) != Some(&b'\'') {
        return None;
    }
    i += 1;
    while i < b.len() {
        match b[i] {
            b'\'' if b.get(i + 1) == Some(&b'\'') => i += 2,
            b'\'' => return Some(i + 1),
            b'\\' if escaped && i + 1 < b.len() => i += 2,
            _ => i += 1,
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::super::script;
    use super::*;

    fn file(path: &str, content: &str) -> File {
        File {
            path: path.into(),
            content: content.into(),
        }
    }

    fn diag(where_: Option<&str>) -> SqlDiagnostics {
        SqlDiagnostics {
            where_: where_.map(str::to_string),
            ..SqlDiagnostics::default()
        }
    }

    fn at(position: usize) -> SqlDiagnostics {
        SqlDiagnostics {
            position: Some(position),
            ..SqlDiagnostics::default()
        }
    }

    fn internal(query: &str, position: usize, where_: &str) -> SqlDiagnostics {
        SqlDiagnostics {
            internal_query: Some(query.into()),
            internal_position: Some(position),
            where_: Some(where_.into()),
            ..SqlDiagnostics::default()
        }
    }

    fn blame(
        d: &SqlDiagnostics,
        deploy_sql: &str,
        unit: (usize, &str, bool),
        files: &[File],
    ) -> Blame {
        let lines = LineMap::default();
        let sent = Sent {
            deploy_sql,
            start: unit.0,
            text: unit.1,
            lines: &lines,
            tail: unit.2,
        };
        let refs: Vec<&File> = files.iter().collect();
        Blame::new(d, "ERROR: boom (SQLSTATE XX000)", &sent, &refs)
    }

    fn line(script: &str, number: usize, column: usize, text: &str) -> Option<Location> {
        Some(Location {
            script: script.into(),
            line: Some(Line {
                number,
                column,
                text: text.into(),
            }),
        })
    }

    fn chars_before(text: &str, needle: &str) -> usize {
        text[..text.find(needle).unwrap()].chars().count()
    }

    const EXEC: &str = "PL/pgSQL function inline_code_block line 5 at EXECUTE";

    #[test]
    fn a_syntax_error_in_a_file_resolves_to_its_line_and_column() {
        let content = "CREATE TABLE p (id int);\n\n-- line 3\n  SELECT 'жж', SELEC 1;\n";
        let files = [file("./m/a.sql", content), file("./notes.md", content)];
        let pos = chars_before(content, "1;") + 1;
        let b = blame(&internal(content, pos, EXEC), "x", (0, "x", false), &files);
        assert_eq!(b.file.as_deref(), Some("./m/a.sql"));
        assert_eq!(b.candidates, ["./m/a.sql"]);
        assert_eq!(
            b.location,
            line("./m/a.sql", 4, 22, "  SELECT 'жж', SELEC 1;")
        );
        assert_eq!(b.phase(), "deploy");
    }

    #[test]
    fn a_routine_body_error_is_placed_inside_the_file() {
        let body = "\nBEGIN\n  RETURN nope;\nEND;\n";
        let content = format!(
            "SELECT 1;\r\nCREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $f${body}$f$;\r\n"
        );
        let where_ = format!(
            "compilation of PL/pgSQL function \"f\" near line 2\nSQL statement \"{content}\"\n{EXEC}"
        );
        let pos = chars_before(body, "nope") + 1;
        let files = [file("./f.sql", &content)];
        let b = blame(&internal(body, pos, &where_), "x", (0, "x", false), &files);
        assert_eq!(b.location, line("./f.sql", 4, 10, "  RETURN nope;"));
        assert_eq!(
            b.where_,
            Some(format!(
                "compilation of PL/pgSQL function \"f\" near line 2\nSQL statement in ./f.sql\n{EXEC}"
            ))
        );
    }

    #[test]
    fn a_runtime_error_in_a_files_do_block_names_its_line() {
        let raised =
            "SQL statement \"SELECT 1/0\"\nPL/pgSQL function inline_code_block line 3 at PERFORM";
        let content = "SELECT 1;\nDO $x$\nBEGIN\n  PERFORM 1/0;\nEND $x$;\n";
        let where_ = format!("{raised}\nSQL statement \"{content}\"\n{EXEC}");
        let files = [file("./a.sql", content), file("./b.sql", content)];
        let b = blame(&diag(Some(&where_)), "x", (0, "x", false), &files);
        assert_eq!(b.candidates, ["./a.sql", "./b.sql"]);
        assert_eq!(
            b.location, None,
            "the DO block is not the file's first statement"
        );

        let content = "-- header\n/* c */\nDO $x$\nBEGIN\n  PERFORM 1/0;\nEND $x$;\n";
        let where_ = format!("{raised}\nSQL statement \"{content}\"\n{EXEC}");
        let files = [file("./__test__/t.sql", content)];
        let b = blame(&diag(Some(&where_)), "x", (0, "x", false), &files);
        assert_eq!(b.location, line("./__test__/t.sql", 5, 0, "  PERFORM 1/0;"));
        assert_eq!(b.phase(), "test");
    }

    #[test]
    fn a_runtime_error_without_a_line_names_the_file_only() {
        let content = "SELECT 1;\nSELECT 1/0;\n";
        let where_ = format!("SQL statement \"{content}\"\n{EXEC}");
        let files = [file("./a.sql", content), file("./a.sql.bak", content)];
        let b = blame(&diag(Some(&where_)), "x", (0, "x", false), &files);
        assert_eq!(b.candidates, ["./a.sql"]);
        assert_eq!(b.file.as_deref(), Some("./a.sql"));
        assert_eq!(b.location, None);
        assert_eq!(b.where_, Some(format!("SQL statement in ./a.sql\n{EXEC}")));
    }

    #[test]
    fn the_longest_quoted_file_wins() {
        let files = [
            file("./short.sql", "SELECT 1/0;"),
            file("./long.sql", "SELECT 1;\nSELECT 1/0;"),
            file("./blank.sql", "  "),
        ];
        let where_ = format!("SQL statement \"SELECT 1;\nSELECT 1/0;\"\n{EXEC}");
        let b = blame(&diag(Some(&where_)), "x", (0, "x", false), &files);
        assert_eq!(b.file.as_deref(), Some("./long.sql"));
        assert_eq!(b.candidates, ["./long.sql"]);
    }

    #[test]
    fn a_handler_that_reraises_is_read_from_its_message() {
        assert_eq!(
            failed_in("ERROR: Failed in ./m/a.sql: boom (SQLSTATE P0001)"),
            Some("./m/a.sql")
        );
        assert_eq!(
            failed_in("Failed in nothing, Failed in x.sql"),
            Some("x.sql")
        );
        assert_eq!(failed_in("Failed in .sql"), None);
        assert_eq!(failed_in("boom"), None);
        let b = blame(
            &diag(None),
            "x",
            (0, "x", false),
            &[file("./a.sql", "SELECT 1;")],
        );
        assert_eq!(b.file.as_deref(), Some("deploy.sql"));
    }

    #[test]
    fn a_position_in_deploy_sql_resolves_in_head_and_tail() {
        let src = "SELECT 'ü';\nSELECT 'жж', SELEC 2;\n";
        let b = blame(&at(chars_before(src, "2;") + 1), src, (0, src, false), &[]);
        assert_eq!(
            b.location,
            line("deploy.sql", 2, 20, "SELECT 'жж', SELEC 2;")
        );
        assert_eq!(b.file.as_deref(), Some("deploy.sql"));

        let src = "BEGIN;\nCOMMIT; SELEC 2;\n\n  SELEC 3;\n";
        let start = src.find(" SELEC 2").unwrap();
        let unit = &src[start..start + " SELEC 2;".len()];
        let b = blame(&at(2), src, (start, unit, true), &[]);
        assert_eq!(b.location, line("deploy.sql", 2, 9, "COMMIT; SELEC 2;"));
        let start = src.find("\n\n  SELEC").unwrap();
        let unit = &src[start..];
        let b = blame(&at(5), src, (start, unit, true), &[]);
        assert_eq!(b.location, line("deploy.sql", 4, 3, "  SELEC 3;"));
        assert_eq!(
            blame(&at(999), src, (start, unit, true), &[]).location,
            None
        );
    }

    #[test]
    fn a_runtime_error_in_deploy_sql_finds_its_do_block() {
        let src = "SELECT 1;\nDO $$\nBEGIN\n  PERFORM 1/0;\nEND $$;\n";
        let where_ =
            "SQL statement \"SELECT 1/0\"\nPL/pgSQL function inline_code_block line 3 at PERFORM";
        let b = blame(&diag(Some(where_)), src, (0, src, false), &[]);
        assert_eq!(b.location, line("deploy.sql", 4, 0, "  PERFORM 1/0;"));

        let two = format!("{src}  do language plpgsql $a$ BEGIN END $a$;\n");
        let no_line = Some(Location {
            script: "deploy.sql".into(),
            line: None,
        });
        assert_eq!(
            blame(&diag(Some(where_)), &two, (0, &two, false), &[]).location,
            no_line
        );
        assert_eq!(
            blame(&diag(None), src, (0, src, false), &[]).location,
            no_line
        );

        let tail = "COMMIT;\n-- c\nSELECT 1/0;\n";
        let b = blame(&diag(None), tail, (7, &tail[7..], true), &[]);
        assert_eq!(b.location, line("deploy.sql", 3, 0, "SELECT 1/0;"));
        let tail = "COMMIT;\n/* a /* b */ */ DO $$\nBEGIN\n  PERFORM 1/0;\nEND $$;\n";
        let b = blame(&diag(Some(where_)), tail, (7, &tail[7..], true), &[]);
        assert_eq!(b.location, line("deploy.sql", 4, 0, "  PERFORM 1/0;"));
    }

    #[test]
    fn lines_inside_a_macro_expansion_map_to_the_macro() {
        let src = "BEGIN;\nCALL pgmi_test();\nSELEC 1;\n";
        let (text, lines) = script::expand(src, &script::macros(src), &["A\nB".into()]);
        let sent = Sent {
            deploy_sql: src,
            start: 0,
            text: &text,
            lines: &lines,
            tail: false,
        };
        let b = Blame::new(&at(chars_before(&text, "SELEC") + 1), "x", &sent, &[]);
        assert_eq!(b.location, line("deploy.sql", 3, 1, "SELEC 1;"));
        let b = Blame::new(&at(chars_before(&text, "B;") + 1), "x", &sent, &[]);
        assert_eq!(b.location, line("deploy.sql", 2, 0, "CALL pgmi_test();"));
    }

    #[test]
    fn test_paths_are_the_ones_under_a_test_directory() {
        for (path, want) in [
            ("./__test__/a.sql", true),
            ("./x/__tests__/deep/a.sql", true),
            ("./__test__.sql", false),
            ("__test__/a.sql", false),
            ("./__a__/__test__/a.sql", false),
            ("./m/__test__", false),
            ("./m/a.sql", false),
        ] {
            assert_eq!(is_test_path(path), want, "{path}");
        }
    }

    #[test]
    fn do_blocks_are_recognised_as_go_does() {
        for (unit, want) in [
            ("DO $$ BEGIN END $$;", true),
            ("do\n$tag$ BEGIN END $tag$;", true),
            ("DO LANGUAGE plpgsql $$ $$;", true),
            ("DO 'BEGIN END';", false),
            ("DOMAIN", false),
            ("DO\x0b$$ $$", false),
            ("DO LANGUAGE $$ $$", false),
        ] {
            assert_eq!(do_block_start(unit), want, "{unit:?}");
        }
        assert_eq!(statement_start("  -- a\n/* b /* c */ d */\tX"), 25);
        assert_eq!(statement_start("-- open"), 7);
    }

    #[test]
    fn sql_password_literals_are_redacted() {
        for (text, want) in [
            (
                "ALTER ROLE a PASSWORD 'x''y' VALID",
                "ALTER ROLE a PASSWORD [redacted] VALID",
            ),
            ("password\nE'a\\'b' x", "PASSWORD [redacted] x"),
            ("sslpassword 'x'", "sslpassword 'x'"),
            ("PASSWORD NULL", "PASSWORD NULL"),
            ("PASSWORD 'open", "PASSWORD 'open"),
            ("é PASSWORD 'ж' é", "é PASSWORD [redacted] é"),
        ] {
            assert_eq!(redact_sql_passwords(text), want, "{text:?}");
        }
    }
}
