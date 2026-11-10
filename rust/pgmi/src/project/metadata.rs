use std::collections::BTreeMap;

use super::lexer::{self, Kind};
use super::walk::{File, is_sql};
use crate::error::{Error, Result};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Meta {
    pub path: String,
    pub id: String,
    pub idempotent: bool,
    pub sort_keys: Vec<String>,
    pub description: Option<String>,
}

const MAX_BLOCK: usize = 10240;

pub fn blocks(content: &str) -> Vec<&str> {
    let src = content.as_bytes();
    lexer::tokens(src)
        .into_iter()
        .filter(|t| t.kind == Kind::BlockComment)
        .filter_map(|t| {
            let text = content.get(t.start + 2..t.end.saturating_sub(2))?;
            let start = text.find("<pgmi-meta")?;
            let rest = &text[start..];
            let end = rest
                .find("</pgmi-meta>")
                .map(|e| e + "</pgmi-meta>".len())
                .or_else(|| {
                    let close = rest.find('>')?;
                    (rest[..close].ends_with('/')).then_some(close + 1)
                })?;
            Some(&rest[..end])
        })
        .collect()
}

fn parse_bool(s: &str) -> Option<bool> {
    match s.trim() {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Some(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Some(false),
        _ => None,
    }
}

pub fn parse(path: &str, content: &str) -> std::result::Result<Option<Meta>, String> {
    let found = blocks(content);
    let Some(xml) = found.first().copied() else {
        return Ok(None);
    };
    if found.len() > 1 {
        return Err(format!("{path}: more than one <pgmi-meta> block"));
    }
    if xml.len() > MAX_BLOCK {
        return Err(format!(
            "{path}: <pgmi-meta> block is over {MAX_BLOCK} bytes"
        ));
    }
    let doc = roxmltree::Document::parse(xml)
        .map_err(|e| format!("{path}: invalid <pgmi-meta> XML: {e}"))?;
    let root = doc.root_element();
    let id_text = root
        .attribute("id")
        .ok_or_else(|| format!("{path}: <pgmi-meta> has no id attribute"))?;
    let id = uuid::Uuid::parse_str(id_text)
        .map_err(|e| format!("{path}: invalid id {id_text:?}: not a UUID ({e})"))?;
    let idempotent = match root.attribute("idempotent") {
        Some(v) => parse_bool(v).ok_or_else(|| {
            format!("{path}: invalid idempotent value {v:?}: expected true or false")
        })?,
        None => return Err(format!("{path}: <pgmi-meta> has no idempotent attribute")),
    };
    let mut sort_keys = Vec::new();
    let mut description = None;
    for child in root.children().filter(|c| c.is_element()) {
        match child.tag_name().name() {
            "description" => description = Some(child.text().unwrap_or("").trim().to_string()),
            "sortKeys" => {
                for key in child.children().filter(|k| k.has_tag_name("key")) {
                    sort_keys.push(key.text().unwrap_or("").trim().to_string());
                }
            }
            _ => {}
        }
    }
    Ok(Some(Meta {
        path: path.to_string(),
        id: id.hyphenated().to_string(),
        idempotent,
        sort_keys,
        description,
    }))
}

pub fn collect(files: &[File]) -> Result<Vec<Meta>> {
    let mut out = Vec::new();
    let mut errors = Vec::new();
    for f in files.iter().filter(|f| is_sql(&f.path)) {
        match parse(&f.path, &f.content) {
            Ok(Some(m)) => out.push(m),
            Ok(None) => {}
            Err(e) => errors.push(e),
        }
    }
    let mut by_id: BTreeMap<&str, Vec<&str>> = BTreeMap::new();
    for m in &out {
        by_id
            .entry(m.id.as_str())
            .or_default()
            .push(m.path.as_str());
    }
    for (id, paths) in by_id.iter().filter(|(_, p)| p.len() > 1) {
        errors.push(format!("Duplicate id: {id}: {}", paths.join(", ")));
    }
    if !errors.is_empty() {
        return Err(Error::config(format!(
            "invalid <pgmi-meta> blocks:\n  {}",
            errors.join("\n  ")
        )));
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_block() {
        let m = parse(
            "./a.sql",
            "/*\n<pgmi-meta id=\"5f0b8a3c-1e2d-4c6b-9a7f-0e1d2c3b4a59\" idempotent=\"true\">\n  <sortKeys><key>0000/first</key><key>9/x</key></sortKeys>\n</pgmi-meta>\n*/\nSELECT 0;",
        )
        .unwrap()
        .unwrap();
        assert_eq!(m.sort_keys, vec!["0000/first", "9/x"]);
        assert_eq!(m.description, None);
        assert!(m.idempotent);
        assert!(
            parse("./b.sql", "-- <pgmi-meta id=\"x\"/>\n")
                .unwrap()
                .is_none()
        );
        let forms = [
            "{1B5E3A4C-2D6F-4A8B-9C0D-1E2F3A4B5C6D}",
            "urn:uuid:1b5e3a4c-2d6f-4a8b-9c0d-1e2f3a4b5c6d",
            "1b5e3a4c2d6f4a8b9c0d1e2f3a4b5c6d",
        ];
        for id in forms {
            let m = parse(
                "./f.sql",
                &format!("/* <pgmi-meta id=\"{id}\" idempotent=\" True \"/> */"),
            )
            .unwrap()
            .unwrap();
            assert_eq!(m.id, "1b5e3a4c-2d6f-4a8b-9c0d-1e2f3a4b5c6d");
            assert!(m.idempotent);
        }
        assert!(parse("./f.sql", "/* <pgmi-meta id=\" 1b5e3a4c-2d6f-4a8b-9c0d-1e2f3a4b5c6d\" idempotent=\"true\"/> */").is_err());
        let e = parse(
            "./bad.sql",
            "/* <pgmi-meta id=\"not-a-uuid\" idempotent=\"true\"></pgmi-meta> */",
        )
        .unwrap_err();
        assert!(e.contains("bad.sql") && e.contains("UUID"));
    }

    #[test]
    fn duplicates() {
        let f = |p: &str| {
            File {
            path: p.into(),
            content: "/* <pgmi-meta id=\"1b5e3a4c-2d6f-4a8b-9c0d-1e2f3a4b5c6d\" idempotent=\"true\"></pgmi-meta> */".into(),
        }
        };
        let e = collect(&[f("./one.sql"), f("./two.sql")]).unwrap_err();
        assert!(e.text().contains("./one.sql, ./two.sql"));
    }
}
