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
        "" | "0" | "f" | "F" | "FALSE" | "false" | "False" => Some(false),
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Some(true),
        _ => None,
    }
}

const FORMAT_HINT: &str = "Format reference: .agents/skills/pgmi-metadata/SKILL.md (scaffolded by pgmi init) or https://github.com/vvka-141/pgmi/blob/main/docs/METADATA.md";

fn xml_error(path: &str, e: &roxmltree::Error) -> String {
    let pos = e.pos();
    let text = match e {
        roxmltree::Error::UnexpectedEndOfStream => "unexpected EOF".to_string(),
        roxmltree::Error::UnknownEntityReference(name, _) => {
            format!("invalid character entity &{name};")
        }
        other => {
            let t = other.to_string();
            match t.rfind(" at ") {
                Some(i) => t[..i].to_string(),
                None => t,
            }
        }
    };
    format!(
        "metadata error in {path} (line {}): {text}\n\nHint: Check that all XML tags are properly closed and attributes are quoted.",
        pos.row
    )
}

pub fn parse(path: &str, content: &str) -> std::result::Result<Option<Meta>, String> {
    let found = blocks(content);
    let Some(xml) = found.first().copied() else {
        if content.contains("<pgmi-meta") && unclosed_block(content) {
            return Err(xml_error(path, &roxmltree::Error::UnexpectedEndOfStream));
        }
        return Ok(None);
    };
    if found.len() > 1 {
        return Err(format!(
            "metadata error in {path}: Multiple metadata blocks found\n\nHint: Only one <pgmi-meta> block is allowed per file. Remove duplicates."
        ));
    }
    if xml.len() > MAX_BLOCK {
        return Err(format!(
            "metadata error in {path}: <pgmi-meta> block is {} bytes, over the {MAX_BLOCK}-byte limit",
            xml.len()
        ));
    }
    let doc = roxmltree::Document::parse(xml).map_err(|e| xml_error(path, &e))?;
    let root = doc.root_element();
    let mut problems = Vec::new();
    let idempotent = match root.attribute("idempotent") {
        Some(v) => Some(parse_bool(v).ok_or_else(|| {
            format!(
                "metadata error in {path}: idempotent attribute must be true or false, got {v:?}\n\nHint: {FORMAT_HINT}"
            )
        })?),
        None => {
            problems.push("idempotent attribute is required (idempotent=\"true\" or idempotent=\"false\").\n  true: the script is safe to rerun every deploy (CREATE OR REPLACE, IF NOT EXISTS).\n  false: the script runs once and its id is recorded.".to_string());
            None
        }
    };
    let id = match root.attribute("id").unwrap_or("") {
        "" => None,
        text => {
            let id = uuid::Uuid::parse_str(text).map_err(|e| {
                format!(
                    "metadata error in {path}: id attribute is not a UUID: {e}\n\nHint: Generate one with SELECT gen_random_uuid();\n{FORMAT_HINT}"
                )
            })?;
            (!id.is_nil()).then_some(id)
        }
    };
    if id.is_none() {
        problems.push("id attribute is required and cannot be the nil UUID (00000000-0000-0000-0000-000000000000).\n  Each script must have a unique identifier.".to_string());
    }
    let mut sort_keys = Vec::new();
    let mut description = None;
    for child in root.children().filter(|c| c.is_element()) {
        match child.tag_name().name() {
            "description" => {
                let text = element_text(child);
                if text.trim().is_empty() && !text.is_empty() {
                    problems.push("description element contains only whitespace.\n  Consider removing it or providing a meaningful description.".to_string());
                }
                description = Some(text);
            }
            "sortKeys" => {
                for key in child.children().filter(|k| k.has_tag_name("key")) {
                    let text = element_text(key);
                    if text.trim().is_empty() {
                        problems.push(format!("sortKeys[{}] cannot be empty or whitespace-only.\n  Provide meaningful sort keys for execution ordering.", sort_keys.len()));
                    }
                    sort_keys.push(text);
                }
            }
            _ => {}
        }
    }
    let (Some(id), Some(idempotent), true) = (id, idempotent, problems.is_empty()) else {
        let list: Vec<String> = problems
            .iter()
            .enumerate()
            .map(|(i, p)| format!("  {}. {p}", i + 1))
            .collect();
        return Err(format!(
            "invalid PGMI metadata in {path}:\n{}",
            list.join("\n")
        ));
    };
    Ok(Some(Meta {
        path: path.to_string(),
        id: id.hyphenated().to_string(),
        idempotent,
        sort_keys,
        description,
    }))
}

fn element_text(node: roxmltree::Node<'_, '_>) -> String {
    node.children()
        .filter(|c| c.is_text())
        .filter_map(|c| c.text())
        .collect()
}

fn unclosed_block(content: &str) -> bool {
    lexer::tokens(content.as_bytes())
        .into_iter()
        .filter(|t| t.kind == Kind::BlockComment)
        .any(|t| {
            content
                .get(t.start..t.end)
                .is_some_and(|c| c.contains("<pgmi-meta"))
        })
}

pub fn fallback_id(path: &str) -> String {
    use md5::Digest;
    let digest = md5::Md5::digest(path.as_bytes());
    let mut bytes = [0u8; 16];
    bytes.copy_from_slice(&digest);
    uuid::Uuid::from_bytes(bytes).hyphenated().to_string()
}

pub fn outside_comment(content: &str, meta: Option<&Meta>) -> bool {
    meta.is_none() && content.contains("<pgmi-meta") && !unclosed_block(content)
}

#[derive(Debug, Default)]
pub struct Scan {
    pub metas: Vec<Meta>,
    pub errors: Vec<String>,
    pub duplicates: Vec<(String, Vec<String>)>,
}

pub fn scan(files: &[File]) -> Scan {
    let mut out = Scan::default();
    for f in files.iter().filter(|f| is_sql(&f.path)) {
        match parse(&f.path, &f.content) {
            Ok(Some(m)) => out.metas.push(m),
            Ok(None) => {}
            Err(e) => out.errors.push(e),
        }
    }
    let mut by_id: BTreeMap<&str, Vec<String>> = BTreeMap::new();
    for m in &out.metas {
        by_id.entry(m.id.as_str()).or_default().push(m.path.clone());
    }
    out.duplicates = by_id
        .into_iter()
        .filter(|(_, p)| p.len() > 1)
        .map(|(id, mut p)| {
            p.sort();
            (id.to_string(), p)
        })
        .collect();
    out
}

pub fn duplicate_line(id: &str, paths: &[String]) -> String {
    format!("{id}: {}", paths.join(", "))
}

pub fn collect(files: &[File]) -> Result<Vec<Meta>> {
    let scan = scan(files);
    if !scan.errors.is_empty() {
        return Err(Error::config(scan.errors.join("\n")));
    }
    if !scan.duplicates.is_empty() {
        let lines: Vec<String> = scan
            .duplicates
            .iter()
            .map(|(id, p)| format!("  {}", duplicate_line(id, p)))
            .collect();
        return Err(Error::config(format!(
            "duplicate <pgmi-meta id> across files; each script must have a unique id:\n{}",
            lines.join("\n")
        )));
    }
    Ok(scan.metas)
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
        let meta = |body: &str| {
            parse(
                "./a.sql",
                &format!(
                    "/* <pgmi-meta id=\"0d3b6f0e-5a8e-4f64-9a3e-6a1c2b7d9e01\" idempotent=\"true\">{body}</pgmi-meta> */"
                ),
            )
        };
        let m =
            meta("<sortKeys><key> 3 </key></sortKeys><description>a &amp; b &#65;</description>")
                .unwrap()
                .unwrap();
        assert_eq!(m.sort_keys, vec![" 3 "]);
        assert_eq!(m.description.as_deref(), Some("a & b A"));
        assert!(
            meta("<sortKeys><key>  </key></sortKeys>")
                .unwrap_err()
                .contains("sortKeys[0] cannot be empty")
        );
        assert!(
            meta("<description> </description>")
                .unwrap_err()
                .contains("only whitespace")
        );
        assert!(
            meta("<description>&nbsp;</description>")
                .unwrap_err()
                .contains("nbsp")
        );
        assert!(
            parse("./a.sql", "/* <pgmi-meta id=\"0d3b6f0e-5a8e-4f64-9a3e-6a1c2b7d9e01\" idempotent=\"true\"><sortKeys> */")
                .unwrap_err()
                .contains("unexpected EOF")
        );
        assert!(
            parse(
                "./a.sql",
                "/* <pgmi-meta id=\"00000000-0000-0000-0000-000000000000\" idempotent=\"true\"/> */"
            )
            .unwrap_err()
            .contains("nil UUID")
        );
        assert_eq!(
            fallback_id("./plain.sql"),
            "1d622beb-19db-dd1d-f3e3-8c353019e16a"
        );
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
        assert_eq!(
            scan(&[f("./two.sql"), f("./one.sql")]).duplicates[0].1,
            vec!["./one.sql", "./two.sql"]
        );
    }
}
