fn statements(sql: &str) -> Vec<&str> {
    let b = sql.as_bytes();
    let (mut i, mut start, mut out) = (0, 0, vec![]);
    while i < b.len() {
        match b[i] {
            b'\'' | b'"' => {
                let q = b[i];
                i += 1;
                while i < b.len() && !(b[i] == q && b.get(i + 1) != Some(&q)) {
                    i += if b[i] == q { 2 } else { 1 };
                }
            }
            b'-' if b.get(i + 1) == Some(&b'-') => {
                while i < b.len() && b[i] != b'\n' {
                    i += 1;
                }
            }
            b'/' if b.get(i + 1) == Some(&b'*') => {
                let mut depth = 0;
                while i < b.len() {
                    if b[i..].starts_with(b"/*") {
                        depth += 1;
                        i += 1;
                    } else if b[i..].starts_with(b"*/") {
                        depth -= 1;
                        i += 1;
                        if depth == 0 {
                            break;
                        }
                    }
                    i += 1;
                }
            }
            b'$' => {
                if let Some(len) = b[i + 1..].iter().position(|&c| c == b'$').filter(|&n| b[i + 1..i + 1 + n].iter().all(|c| c.is_ascii_alphanumeric() || *c == b'_')) {
                    let tag = &sql[i..i + len + 2];
                    i += tag.len();
                    i = sql[i..].find(tag).map_or(b.len(), |p| i + p + tag.len() - 1);
                }
            }
            b';' => {
                out.push(&sql[start..=i]);
                start = i + 1;
            }
            _ => {}
        }
        i += 1;
    }
    if !sql[start..].trim().is_empty() {
        out.push(&sql[start..]);
    }
    out
}

fn keyword(stmt: &str) -> String {
    stmt.trim_start().split(|c: char| !c.is_ascii_alphabetic()).next().unwrap_or("").to_ascii_uppercase()
}

pub fn units(sql: &str) -> Vec<String> {
    let stmts = statements(sql);
    let Some(end) = stmts.iter().position(|s| ["COMMIT", "END", "ROLLBACK", "ABORT"].contains(&keyword(s).as_str())) else {
        return vec![sql.to_string()];
    };
    let mut units = vec![stmts[..=end].concat()];
    units.extend(stmts[end + 1..].iter().filter(|s| !s.trim().is_empty()).map(|s| s.to_string()));
    units
}

pub fn head_commits(unit: &str) -> bool {
    statements(unit).last().is_some_and(|s| ["COMMIT", "END"].contains(&keyword(s).as_str()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn splits_at_the_first_terminator() {
        let u = units("BEGIN;\nCREATE TABLE t ($$;$$);\nCOMMIT;\nSELECT ';';\n-- ; \nSELECT 2;\n");
        assert_eq!(u.len(), 3, "{u:?}");
        assert!(head_commits(&u[0]));
    }

    #[test]
    fn no_terminator_is_one_unit() {
        assert_eq!(units("CREATE TABLE one (); SELECT 1/0;").len(), 1);
    }
}
