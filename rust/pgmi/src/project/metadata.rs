use std::collections::BTreeMap;
use std::fmt;

use super::lexer::{self, TokenKind};
use super::walk::{File, is_sql};
use crate::error::{Error, Result};

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Meta {
    pub(crate) path: String,
    pub(crate) id: String,
    pub(crate) idempotent: bool,
    pub(crate) sort_keys: Vec<String>,
    pub(crate) description: Option<String>,
}

const MAX_BLOCK: usize = 10240;

pub(crate) fn blocks(content: &str) -> Vec<&str> {
    let src = content.as_bytes();
    lexer::tokens(src)
        .into_iter()
        .filter(|t| t.kind == TokenKind::BlockComment)
        .filter_map(|t| content.get(t.start + 2..t.end.saturating_sub(2)))
        .flat_map(elements)
        .collect()
}

fn elements(mut text: &str) -> Vec<&str> {
    let mut out = Vec::new();
    while let Some(start) = text.find("<pgmi-meta") {
        let rest = &text[start..];
        if let Some(end) = element_end(rest) {
            out.push(&rest[..end]);
            text = &rest[end..];
        } else {
            if !out.is_empty() {
                out.push(rest);
            }
            break;
        }
    }
    out
}

fn element_end(element: &str) -> Option<usize> {
    let close = element.find('>')?;
    if element[..close].ends_with('/') {
        return Some(close + 1);
    }
    element
        .find("</pgmi-meta>")
        .map(|e| e + "</pgmi-meta>".len())
}

fn parse_bool(s: &str) -> Option<bool> {
    match s.trim() {
        "" | "0" | "f" | "F" | "FALSE" | "false" | "False" => Some(false),
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Some(true),
        _ => None,
    }
}

const FORMAT_HINT: &str = "Format reference: .agents/skills/pgmi-metadata/SKILL.md (scaffolded by pgmi init) or https://github.com/vvka-141/pgmi/blob/main/docs/METADATA.md";

/// Why one file's `<pgmi-meta>` block was refused.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum MetaError {
    Xml {
        path: String,
        line: u32,
        text: String,
    },
    Multiple {
        path: String,
    },
    TooLarge {
        path: String,
        bytes: usize,
    },
    Idempotent {
        path: String,
        value: String,
    },
    NotUuid {
        path: String,
        reason: String,
    },
    Invalid {
        path: String,
        problems: Vec<String>,
    },
}

impl fmt::Display for MetaError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            MetaError::Xml { path, line, text } => write!(
                f,
                "metadata error in {path} (line {line}): {text}\n\nHint: Check that all XML tags are properly closed and attributes are quoted.\n\nExpected format:\n  <pgmi-meta id=\"UUID\" idempotent=\"true|false\">\n    <description>...</description>\n    <sortKeys><key>...</key></sortKeys>\n  </pgmi-meta>"
            ),
            MetaError::Multiple { path } => write!(
                f,
                "metadata error in {path}: Multiple metadata blocks found\n\nHint: Only one <pgmi-meta> block is allowed per file. Remove duplicates."
            ),
            MetaError::TooLarge { path, bytes } => write!(
                f,
                "metadata error in {path}: <pgmi-meta> block is {bytes} bytes, over the {MAX_BLOCK}-byte limit"
            ),
            MetaError::Idempotent { path, value } => write!(
                f,
                "metadata error in {path}: idempotent attribute must be true or false, got {value:?}\n\nHint: {FORMAT_HINT}"
            ),
            MetaError::NotUuid { path, reason } => write!(
                f,
                "metadata error in {path}: id attribute is not a UUID: {reason}\n\nHint: Generate one with SELECT gen_random_uuid();\n{FORMAT_HINT}"
            ),
            MetaError::Invalid { path, problems } => {
                write!(f, "invalid PGMI metadata in {path}:")?;
                for (i, p) in problems.iter().enumerate() {
                    write!(f, "\n  {}. {p}", i + 1)?;
                }
                write!(
                    f,
                    "\n\nSee metadata format documentation:\n  {FORMAT_HINT}\n  Generate template: pgmi metadata scaffold <path>"
                )
            }
        }
    }
}

impl std::error::Error for MetaError {}

fn xml_error(path: &str, e: &roxmltree::Error) -> MetaError {
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
    MetaError::Xml {
        path: path.to_string(),
        line: pos.row,
        text,
    }
}

pub(crate) fn parse(path: &str, content: &str) -> std::result::Result<Option<Meta>, MetaError> {
    let found = blocks(content);
    let Some(xml) = found.first().copied() else {
        if content.contains("<pgmi-meta") && unclosed_block(content) {
            return Err(xml_error(path, &roxmltree::Error::UnexpectedEndOfStream));
        }
        return Ok(None);
    };
    if found.len() > 1 {
        return Err(MetaError::Multiple {
            path: path.to_string(),
        });
    }
    if xml.len() > MAX_BLOCK {
        return Err(MetaError::TooLarge {
            path: path.to_string(),
            bytes: xml.len(),
        });
    }
    let doc = roxmltree::Document::parse(xml).map_err(|e| xml_error(path, &e))?;
    let root = doc.root_element();
    let mut problems = Vec::new();
    let idempotent = if let Some(v) = root.attribute("idempotent") {
        Some(parse_bool(v).ok_or_else(|| MetaError::Idempotent {
            path: path.to_string(),
            value: v.to_string(),
        })?)
    } else {
        problems.push("idempotent attribute is required (idempotent=\"true\" or idempotent=\"false\").\n  true: the script is safe to rerun every deploy (CREATE OR REPLACE, IF NOT EXISTS).\n  false: the script runs once per id (tracked one-time migration).".to_string());
        None
    };
    let id = match root.attribute("id").unwrap_or("") {
        "" => None,
        text => {
            let id = uuid::Uuid::parse_str(text).map_err(|e| MetaError::NotUuid {
                path: path.to_string(),
                reason: e.to_string(),
            })?;
            (!id.is_nil()).then_some(id)
        }
    };
    if id.is_none() {
        problems.push("id attribute is required and cannot be the nil UUID (00000000-0000-0000-0000-000000000000).\n  Each script must have a unique identifier.\n  Generate with: uuidgen (Linux/Mac), [guid]::NewGuid() (PowerShell), or https://www.uuidgenerator.net/".to_string());
    }
    let mut sort_keys = Vec::new();
    let mut description = None;
    for child in root.children().filter(roxmltree::Node::is_element) {
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
                        problems.push(format!("sortKeys[{}] cannot be empty or whitespace-only.\n  Provide meaningful sort keys for execution ordering.\n  Recommended format: \"phase/sequence\" (e.g., \"10-utils/0010\", \"30-core/2000\")", sort_keys.len()));
                    }
                    sort_keys.push(text);
                }
            }
            _ => {}
        }
    }
    let (Some(id), Some(idempotent), true) = (id, idempotent, problems.is_empty()) else {
        return Err(MetaError::Invalid {
            path: path.to_string(),
            problems,
        });
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
        .filter(roxmltree::Node::is_text)
        .filter_map(|c| c.text())
        .collect()
}

fn unclosed_block(content: &str) -> bool {
    lexer::tokens(content.as_bytes())
        .into_iter()
        .filter(|t| t.kind == TokenKind::BlockComment)
        .any(|t| {
            content
                .get(t.start..t.end)
                .is_some_and(|c| c.contains("<pgmi-meta"))
        })
}

pub(crate) fn fallback_id(path: &str) -> String {
    use md5::Digest;
    let digest = md5::Md5::digest(path.as_bytes());
    let mut bytes = [0u8; 16];
    bytes.copy_from_slice(&digest);
    uuid::Uuid::from_bytes(bytes).hyphenated().to_string()
}

pub(crate) fn outside_comment(content: &str, meta: Option<&Meta>) -> bool {
    meta.is_none() && content.contains("<pgmi-meta") && !unclosed_block(content)
}

#[derive(Debug, Default)]
pub(crate) struct Scan {
    pub(crate) metas: Vec<Meta>,
    pub(crate) errors: Vec<MetaError>,
    pub(crate) duplicates: Vec<(String, Vec<String>)>,
}

pub(crate) fn scan<'a>(files: impl IntoIterator<Item = &'a File>) -> Scan {
    let mut out = Scan::default();
    for f in files.into_iter().filter(|f| is_sql(&f.path)) {
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

pub(crate) fn duplicate_line(id: &str, paths: &[String]) -> String {
    format!("{id}: {}", paths.join(", "))
}

#[cfg(test)]
pub(crate) fn collect(files: &[File]) -> Result<Vec<Meta>> {
    let scan = scan(files);
    scan.readable()?;
    scan.unique()
}

impl Scan {
    /// Fails on blocks that could not be read.
    pub(crate) fn readable(&self) -> Result<()> {
        if self.errors.is_empty() {
            return Ok(());
        }
        let errors: Vec<String> = self.errors.iter().map(ToString::to_string).collect();
        Err(Error::config(errors.join("\n")))
    }

    /// The blocks, or an error naming every id two files share.
    pub(crate) fn unique(self) -> Result<Vec<Meta>> {
        if self.duplicates.is_empty() {
            return Ok(self.metas);
        }
        let lines: Vec<String> = self
            .duplicates
            .iter()
            .map(|(id, p)| format!("  {}", duplicate_line(id, p)))
            .collect();
        Err(Error::config(format!(
            "duplicate <pgmi-meta id> across files; each script must have a unique id:\n{}",
            lines.join("\n")
        )))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vfs::{MemFs, Vfs};

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
        let e = parse(
            "./f.sql",
            "/* <pgmi-meta id=\" 1b5e3a4c-2d6f-4a8b-9c0d-1e2f3a4b5c6d\" idempotent=\"true\"/> */",
        )
        .unwrap_err();
        assert!(
            matches!(&e, MetaError::NotUuid { path, .. } if path == "./f.sql"),
            "{e}"
        );
        let e = parse(
            "./bad.sql",
            "/* <pgmi-meta id=\"not-a-uuid\" idempotent=\"true\"></pgmi-meta> */",
        )
        .unwrap_err();
        assert!(matches!(&e, MetaError::NotUuid { path, .. } if path == "./bad.sql"));
        let text = e.to_string();
        assert!(text.contains("bad.sql") && text.contains("UUID"), "{text}");
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
        let problems = |e: MetaError| match e {
            MetaError::Invalid { problems, .. } => problems.join("\n"),
            other => panic!("not Invalid: {other}"),
        };
        assert!(
            problems(meta("<sortKeys><key>  </key></sortKeys>").unwrap_err())
                .contains("sortKeys[0] cannot be empty")
        );
        assert!(
            problems(meta("<description> </description>").unwrap_err()).contains("only whitespace")
        );
        let e = meta("<description>&nbsp;</description>").unwrap_err();
        assert!(matches!(e, MetaError::Xml { .. }), "{e}");
        assert!(e.to_string().contains("nbsp"), "{e}");
        let e = parse("./a.sql", "/* <pgmi-meta id=\"0d3b6f0e-5a8e-4f64-9a3e-6a1c2b7d9e01\" idempotent=\"true\"><sortKeys> */")
            .unwrap_err();
        assert!(
            matches!(&e, MetaError::Xml { text, .. } if text == "unexpected EOF"),
            "{e}"
        );
        assert!(
            problems(
                parse(
                    "./a.sql",
                    "/* <pgmi-meta id=\"00000000-0000-0000-0000-000000000000\" idempotent=\"true\"/> */"
                )
                .unwrap_err()
            )
            .contains("nil UUID")
        );
        assert_eq!(
            fallback_id("./plain.sql"),
            "1d622beb-19db-dd1d-f3e3-8c353019e16a"
        );
    }

    #[test]
    fn two_elements_in_one_comment_are_refused() {
        let one = "<pgmi-meta id=\"1b5e3a4c-2d6f-4a8b-9c0d-1e2f3a4b5c6d\" idempotent=\"true\"></pgmi-meta>";
        let two = "<pgmi-meta id=\"0d3b6f0e-5a8e-4f64-9a3e-6a1c2b7d9e01\" idempotent=\"false\"/>";
        for content in [
            format!("/* {one}\n{two} */\nSELECT 1;"),
            format!("/* {two} {one} */"),
            format!("/* {one} <pgmi-meta id=\"x\" */"),
            format!("/* {one} */ /* {two} */"),
        ] {
            let e = parse("./a.sql", &content).unwrap_err();
            assert!(
                matches!(&e, MetaError::Multiple { path } if path == "./a.sql"),
                "{content}: {e}"
            );
        }
        assert!(parse("./a.sql", &format!("/* {one} */")).unwrap().is_some());
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

    const ID: &str = "550e8400-e29b-41d4-a716-446655440000";
    const REFERENCE: [&str; 2] = [
        ".agents/skills/pgmi-metadata/SKILL.md",
        "https://github.com/vvka-141/pgmi/blob/main/docs/METADATA.md",
    ];

    fn block(attrs: &str, body: &str) -> String {
        format!("/*\n<pgmi-meta {attrs}>\n{body}\n</pgmi-meta>\n*/\nSELECT 1;\n")
    }

    fn problems(e: MetaError) -> Vec<String> {
        match e {
            MetaError::Invalid { problems, .. } => problems,
            other => panic!("not Invalid: {other}"),
        }
    }

    #[test]
    fn every_field_and_the_minimal_form_are_read() {
        let m = parse(
            "./test.sql",
            &block(
                &format!("\n    id=\"{ID}\"\n    idempotent=\"true\""),
                "  <description>Test script with all fields</description>\n  <sortKeys>\n    <key>00000000-0000-0000-0000-000000000000/0001</key>\n  </sortKeys>",
            ),
        )
        .unwrap()
        .unwrap();
        assert_eq!(
            m,
            Meta {
                path: "./test.sql".into(),
                id: ID.into(),
                idempotent: true,
                sort_keys: vec!["00000000-0000-0000-0000-000000000000/0001".into()],
                description: Some("Test script with all fields".into()),
            }
        );
        let m = parse(
            "./minimal.sql",
            &block(
                &format!("id=\"{ID}\" idempotent=\"false\""),
                "<sortKeys><key>001</key></sortKeys>",
            ),
        )
        .unwrap()
        .unwrap();
        assert!(!m.idempotent);
        assert_eq!(m.description, None);
        assert_eq!(m.sort_keys, ["001"]);
    }

    #[test]
    fn a_file_without_a_block_has_no_metadata() {
        for content in [
            "SELECT 1 FROM test;",
            "/* Just a regular comment */\nSELECT 1;\n",
            "/*\n\n\n*/\nSELECT 1;\n",
            &format!("/*\n<PGMI-META id=\"{ID}\" idempotent=\"true\">\n</PGMI-META>\n*/\n"),
            "/*",
            "/*x",
            "/*/",
        ] {
            assert_eq!(parse("./a.sql", content), Ok(None), "{content:?}");
        }
    }

    #[test]
    fn an_unterminated_comment_holding_a_block_is_an_xml_error() {
        let e = parse("./half.sql", "/* <pgmi-meta id=\"x\">").unwrap_err();
        assert!(
            matches!(&e, MetaError::Xml { path, .. } if path == "./half.sql"),
            "{e}"
        );
    }

    #[test]
    fn two_blocks_in_separate_comments_are_refused() {
        let one = block(
            &format!("id=\"{ID}\" idempotent=\"true\""),
            "<sortKeys><key>001</key></sortKeys>",
        );
        let two = block(
            "id=\"7603e3af-b8d9-46a5-8c4c-7f74d39e17f9\" idempotent=\"true\"",
            "<sortKeys><key>002</key></sortKeys>",
        );
        for content in [format!("{one}\n{two}"), format!("{one}{one}")] {
            let e = parse("./duplicate.sql", &content).unwrap_err();
            assert!(
                matches!(&e, MetaError::Multiple { path } if path == "./duplicate.sql"),
                "{e}"
            );
            assert!(e.to_string().contains("Multiple metadata blocks found"));
        }
    }

    #[test]
    fn a_block_over_10_kib_is_refused() {
        let content = block(
            &format!("id=\"{ID}\" idempotent=\"true\""),
            &format!("<description>{}</description>", "x".repeat(MAX_BLOCK)),
        );
        let e = parse("./large.sql", &content).unwrap_err();
        assert!(
            matches!(&e, MetaError::TooLarge { path, bytes } if path == "./large.sql" && *bytes > MAX_BLOCK),
            "{e}"
        );
        assert_eq!(MAX_BLOCK, 10 * 1024);
    }

    #[test]
    fn malformed_xml_is_an_xml_error() {
        for content in [
            format!(
                "/*\n<pgmi-meta id=\"{ID}\" idempotent=\"true\">\n  <description>Unclosed\n  <sortKeys><key>001</key></sortKeys>\n</pgmi-meta>\n*/"
            ),
            format!(
                "/*\n<pgmi-meta id=\"{ID}\" idempotent=\"true\">\n  <description>Test</description>\n  <sortKeys><key>001</key></sortKeys>\n*/"
            ),
            format!(
                "/*\n<pgmi-meta id={ID} idempotent=\"true\">\n  <sortKeys><key>001</key></sortKeys>\n</pgmi-meta>\n*/"
            ),
        ] {
            let e = parse("./invalid.sql", &content).unwrap_err();
            assert!(
                matches!(&e, MetaError::Xml { path, .. } if path == "./invalid.sql"),
                "{content}: {e}"
            );
            assert!(
                e.to_string().starts_with("metadata error in ./invalid.sql"),
                "{e}"
            );
        }
    }

    #[test]
    fn attribute_errors_name_the_attribute_and_the_reference() {
        let e = parse(
            "./a.sql",
            &block("id=\"not-a-valid-uuid\" idempotent=\"true\"", ""),
        )
        .unwrap_err();
        assert!(
            matches!(&e, MetaError::NotUuid { path, .. } if path == "./a.sql"),
            "{e}"
        );
        assert!(e.to_string().contains("id attribute is not a UUID"));
        let e = parse(
            "./a.sql",
            &block(
                "id=\"11111111-1111-4111-8111-111111111111\" idempotent=\"maybe\"",
                "",
            ),
        )
        .unwrap_err();
        assert_eq!(
            e,
            MetaError::Idempotent {
                path: "./a.sql".into(),
                value: "maybe".into()
            }
        );
        for e in [
            parse("./a.sql", &block("id=\"abc\" idempotent=\"true\"", "")).unwrap_err(),
            e,
            parse(
                "./a.sql",
                &block(
                    "id=\"00000000-0000-0000-0000-000000000000\" idempotent=\"true\"",
                    "",
                ),
            )
            .unwrap_err(),
        ] {
            let text = e.to_string();
            for want in REFERENCE {
                assert!(text.contains(want), "{text}");
            }
            assert!(
                !text.contains("pgmi ai skill") && !text.contains("schema.xsd"),
                "{text}"
            );
        }
        let fs = MemFs::from_files(
            crate::templates::Template::Basic
                .render("proj", crate::VERSION)
                .unwrap(),
        );
        assert!(
            fs.read_to_string(".agents/skills/pgmi-metadata/SKILL.md")
                .is_ok()
        );
    }

    #[test]
    fn idempotent_takes_the_boolean_spellings() {
        for (value, want) in [
            ("true", true),
            ("false", false),
            ("1", true),
            ("TRUE", true),
        ] {
            let m = parse(
                "./b.sql",
                &block(&format!("id=\"{ID}\" idempotent=\"{value}\""), ""),
            )
            .unwrap()
            .unwrap();
            assert_eq!(m.idempotent, want, "{value}");
        }
        let e = parse(
            "./b.sql",
            &block(&format!("id=\"{ID}\" idempotent=\"yes\""), ""),
        )
        .unwrap_err();
        assert!(matches!(e, MetaError::Idempotent { .. }), "{e}");
    }

    #[test]
    fn a_later_comment_can_hold_the_block() {
        let content = format!(
            "/* First comment - no metadata */\n\n{}",
            block(
                &format!("id=\"{ID}\" idempotent=\"true\""),
                "<description>Metadata in second comment</description>"
            )
        );
        let m = parse("./second.sql", &content).unwrap().unwrap();
        assert_eq!(m.description.as_deref(), Some("Metadata in second comment"));
    }

    #[test]
    fn literals_and_nested_comments_do_not_fool_the_reader() {
        let one = format!("/*\n<pgmi-meta id=\"{ID}\" idempotent=\"true\"></pgmi-meta>\n*/\n");
        for content in [
            format!(
                "/*\n<pgmi-meta id=\"{ID}\" idempotent=\"true\">\n  <description>handles /* and */ in input</description>\n</pgmi-meta>\n*/\nSELECT 1;"
            ),
            format!(
                "INSERT INTO doc VALUES ('/* <pgmi-meta id=\"x\" idempotent=\"true\"/> */');\n{one}SELECT 1;"
            ),
            format!(
                "DO $$ BEGIN RAISE NOTICE '/* <pgmi-meta id=\"x\" idempotent=\"true\"/> */'; END $$;\n{one}"
            ),
            format!("/* banner /* inner */ still banner */\n{one}SELECT 1;"),
            format!("CREATE TABLE \"/* <pgmi-meta id=\\\"x\\\"/> */\" (a int);\n{one}"),
        ] {
            let m = parse("./x.sql", &content).unwrap().unwrap();
            assert_eq!(m.id, ID, "{content}");
        }
    }

    #[test]
    fn validation_problems_are_all_listed_with_their_hints() {
        let attrs = format!("id=\"{ID}\" idempotent=\"true\"");
        let missing = problems(parse("./a.sql", &block(&format!("id=\"{ID}\""), "")).unwrap_err());
        assert_eq!(missing.len(), 1);
        assert!(missing[0].starts_with("idempotent attribute is required"));
        for key in ["", "   \t\n  "] {
            let p = problems(
                parse(
                    "./a.sql",
                    &block(&attrs, &format!("<sortKeys><key>{key}</key></sortKeys>")),
                )
                .unwrap_err(),
            );
            assert_eq!(p.len(), 1, "{key:?}");
            assert!(
                p[0].starts_with("sortKeys[0] cannot be empty or whitespace-only."),
                "{}",
                p[0]
            );
            assert!(p[0].contains("Recommended format"), "{}", p[0]);
        }
        let p = problems(
            parse(
                "./a.sql",
                &block(&attrs, "<description>   \n\t   </description>"),
            )
            .unwrap_err(),
        );
        assert_eq!(p.len(), 1);
        assert!(p[0].starts_with("description element contains only whitespace."));
        let nil = problems(
            parse(
                "./nil.sql",
                &block(
                    "id=\"00000000-0000-0000-0000-000000000000\" idempotent=\"true\"",
                    "",
                ),
            )
            .unwrap_err(),
        );
        assert_eq!(nil.len(), 1);
        assert!(
            nil[0].contains("nil UUID") && nil[0].contains("uuidgen"),
            "{}",
            nil[0]
        );
        let e = parse(
            "./multiple.sql",
            &block(
                "id=\"00000000-0000-0000-0000-000000000000\" idempotent=\"true\"",
                "<sortKeys><key></key></sortKeys><description>   </description>",
            ),
        )
        .unwrap_err();
        let text = e.to_string();
        let p = problems(e);
        assert_eq!(p.len(), 3);
        assert!(
            p[0].starts_with("id attribute")
                && p[1].starts_with("sortKeys[0]")
                && p[2].starts_with("description")
        );
        assert!(
            text.starts_with("invalid PGMI metadata in ./multiple.sql:\n  1. id attribute"),
            "{text}"
        );
        assert!(
            text.contains("\n  2. sortKeys[0]") && text.contains("\n  3. description"),
            "{text}"
        );
        assert!(
            text.ends_with("Generate template: pgmi metadata scaffold <path>"),
            "{text}"
        );
    }

    #[test]
    fn fallback_id_is_the_md5_of_the_path_as_a_uuid() {
        for (path, want) in [
            (
                "./migrations/001_users.sql",
                "8cb96d1a-b6fc-e524-d4f0-95b95baba1da",
            ),
            (
                "./migrations/002_products.sql",
                "6181c9f2-7bb4-745c-ae41-9defe40e815e",
            ),
            ("./setup/schema.sql", "82ec6a0f-18fa-4012-a152-9e40b2b60e26"),
            ("", "d41d8cd9-8f00-b204-e980-0998ecf8427e"),
            (
                "./migrations/001_Users.sql",
                "adfe0358-5851-fcf0-89d4-b5cdc7b2f152",
            ),
            (
                "migrations/001_users.sql",
                "c087268b-446c-067e-a694-6486424348e2",
            ),
        ] {
            assert_eq!(fallback_id(path), want, "{path:?}");
        }
        let ids: std::collections::BTreeSet<String> = [
            "./post-deployment/grants.sql",
            "./migrations/001_test with spaces.sql",
            "./migrations/002-test-with-dashes.sql",
            "./migrations/003_test_with_underscores.sql",
            "./migrations/004.test.with.dots.sql",
            "./migrations/005 (test).sql",
        ]
        .into_iter()
        .map(fallback_id)
        .collect();
        assert_eq!(ids.len(), 6);
        assert!(!ids.contains("00000000-0000-0000-0000-000000000000"));
    }

    #[test]
    fn unique_ids_and_files_without_metadata_pass() {
        let with = |p: &str, id: &str| File {
            path: p.into(),
            content: format!("/* <pgmi-meta id=\"{id}\" idempotent=\"true\"/> */"),
        };
        let plain = |p: &str| File {
            path: p.into(),
            content: "SELECT 1;".into(),
        };
        let ok = collect(&[
            with("./a.sql", "11111111-1111-4111-8111-111111111111"),
            with("./b.sql", "22222222-2222-4222-8222-222222222222"),
            plain("./c.sql"),
            plain("./d.txt"),
        ])
        .unwrap();
        assert_eq!(ok.len(), 2);
        let e = collect(&[
            with("./b.sql", "11111111-1111-4111-8111-111111111111"),
            with("./a.sql", "11111111-1111-4111-8111-111111111111"),
        ])
        .unwrap_err();
        assert_eq!(e.kind(), crate::error::Kind::Config);
        assert!(
            e.text()
                .contains("11111111-1111-4111-8111-111111111111: ./a.sql, ./b.sql"),
            "{}",
            e.text()
        );
    }
}
