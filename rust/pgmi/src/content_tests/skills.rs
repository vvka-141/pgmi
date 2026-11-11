use std::collections::BTreeSet;
use std::sync::LazyLock;

use regex_lite::Regex;
use serde::Deserialize;

use super::{embedded, read, scaffold, tree};
use crate::templates::Template;
use crate::vfs::{EmbeddedFs, FileKind, MemFs, Vfs};

const MAX_NAME: usize = 64;
const MAX_DESCRIPTION: usize = 1024;
const MAX_COMPATIBILITY: usize = 500;
const MAX_BODY_LINES: usize = 500;
const MAX_BODY_TOKENS: usize = 5000;
const TOC_THRESHOLD_LINES: usize = 100;
const OVERLAP_THRESHOLD: f64 = 0.3;
const SKILLS_ROOT: &str = ".agents/skills";
const RESERVED_NAME_WORDS: [&str; 2] = ["anthropic", "claude"];
const DESCRIPTION_STOP: &str =
    "a an and are as at be by for from how in into is it its of on or that the
this to use used uses using when with without your you any all can do does not";

static NAME: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"^[a-z0-9]+(-[a-z0-9]+)*$").unwrap());
static LINK: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"\]\(([^)#\s]+)").unwrap());
static XML_TAG: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"<[a-zA-Z/][^>]*>").unwrap());
static CONTENTS: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"(?im)^#+\s*contents\b").unwrap());
static WORD: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"[a-z0-9]+").unwrap());

#[derive(Default, Deserialize)]
struct Meta {
    #[serde(default)]
    name: String,
    #[serde(default)]
    description: String,
    #[serde(default)]
    compatibility: String,
}

fn read_skill(fs: &impl Vfs, dir: &str) -> Result<(Meta, String), String> {
    let raw = fs
        .read(&format!("{dir}/SKILL.md"))
        .map_err(|_| "no SKILL.md".to_string())?;
    let text = String::from_utf8_lossy(&raw).replace("\r\n", "\n");
    let rest = text
        .strip_prefix("---\n")
        .ok_or("SKILL.md must start with YAML frontmatter")?;
    let (front, body) = rest
        .split_once("\n---\n")
        .ok_or("SKILL.md frontmatter is not closed")?;
    let meta: Meta = serde_saphyr::from_str(front).map_err(|e| format!("frontmatter: {e}"))?;
    Ok((meta, body.to_string()))
}

fn clean_join(dir: &str, target: &str) -> String {
    let mut parts: Vec<&str> = Vec::new();
    for p in dir.split('/').chain(target.split('/')) {
        match p {
            "" | "." => {}
            ".." => {
                parts.pop();
            }
            p => parts.push(p),
        }
    }
    parts.join("/")
}

fn validate_skill(fs: &impl Vfs, dir: &str) -> Vec<String> {
    let mut problems = Vec::new();
    let mut fail = |msg: String| problems.push(format!("{dir}: {msg}"));
    let (meta, body) = match read_skill(fs, dir) {
        Ok(v) => v,
        Err(e) => return vec![format!("{dir}: {e}")],
    };
    let base = dir.rsplit('/').next().unwrap_or(dir);
    if meta.name.is_empty() {
        fail("name is required".into());
    } else if meta.name.len() > MAX_NAME {
        fail(format!(
            "name {:?} is longer than {MAX_NAME} characters",
            meta.name
        ));
    } else if !NAME.is_match(&meta.name) {
        fail(format!(
            "name {:?}: lowercase letters, digits and single inner hyphens only",
            meta.name
        ));
    } else if meta.name != base {
        fail(format!(
            "name {:?} must equal the directory name {base:?}",
            meta.name
        ));
    }
    for w in RESERVED_NAME_WORDS {
        if meta.name.contains(w) {
            fail(format!("name {:?} uses the reserved word {w:?}", meta.name));
        }
    }
    let d = meta.description.trim();
    if d.is_empty() || d.len() > MAX_DESCRIPTION {
        fail(format!(
            "description must be 1-{MAX_DESCRIPTION} characters, has {}",
            d.len()
        ));
    }
    if XML_TAG.is_match(&format!("{}{}", meta.name, meta.description)) {
        fail("XML tag in name or description".into());
    }
    if meta.compatibility.len() > MAX_COMPATIBILITY {
        fail(format!(
            "compatibility is longer than {MAX_COMPATIBILITY} characters"
        ));
    }
    let lines = body.trim_end_matches('\n').matches('\n').count() + 1;
    if lines > MAX_BODY_LINES {
        fail(format!(
            "SKILL.md body has {lines} lines, the limit is {MAX_BODY_LINES}"
        ));
    }
    let tokens = body.chars().count().div_ceil(4);
    if tokens > MAX_BODY_TOKENS {
        fail(format!(
            "SKILL.md body has ~{tokens} tokens, the budget is {MAX_BODY_TOKENS}"
        ));
    }
    for m in LINK.captures_iter(&body) {
        let target = &m[1];
        if target.contains("://") {
            continue;
        }
        if fs.stat(&clean_join(dir, target)).is_err() {
            fail(format!("links to {target}, which does not exist"));
        }
    }
    if let Ok(refs) = fs.read_dir(&format!("{dir}/references")) {
        for r in refs {
            let name = r.name.as_str();
            if r.stat.kind == FileKind::Dir {
                fail(format!(
                    "references/{name} is a directory: keep references one level deep"
                ));
                continue;
            }
            let content = fs
                .read(&format!("{dir}/references/{name}"))
                .unwrap_or_default();
            let text = String::from_utf8_lossy(&content).replace("\r\n", "\n");
            if text.matches('\n').count() > TOC_THRESHOLD_LINES && !CONTENTS.is_match(&text) {
                fail(format!(
                    "references/{name} is over {TOC_THRESHOLD_LINES} lines without a Contents section"
                ));
            }
            for m in LINK.captures_iter(&text) {
                if m[1].ends_with(".md") && !m[1].contains("://") {
                    fail(format!(
                        "references/{name} links to {}: a reference must not chain to another file",
                        &m[1]
                    ));
                }
            }
        }
    }
    problems
}

fn description_words(d: &str) -> BTreeSet<String> {
    let stop: Vec<&str> = DESCRIPTION_STOP.split_whitespace().collect();
    WORD.find_iter(&d.to_lowercase())
        .map(|m| m.as_str().to_string())
        .filter(|w| w.len() > 2 && !stop.contains(&w.as_str()))
        .collect()
}

fn description_overlap(a: &str, b: &str) -> f64 {
    let (wa, wb) = (description_words(a), description_words(b));
    if wa.is_empty() || wb.is_empty() {
        return 0.0;
    }
    let shared = wa.intersection(&wb).count();
    shared as f64 / (wa.len() + wb.len() - shared) as f64
}

fn skill_dirs(fs: &impl Vfs, root: &str) -> Vec<String> {
    fs.read_dir(root)
        .unwrap_or_else(|e| panic!("read {root}: {e}"))
        .into_iter()
        .filter(|e| e.stat.kind == FileKind::Dir)
        .map(|e| e.name.as_str().to_string())
        .collect()
}

#[test]
fn validate_skill_flags_each_spec_violation() {
    let skill = |front: &str, body: &str| format!("---\n{front}\n---\n{body}");
    let good = skill(
        "name: good-skill\ndescription: Does a thing. Use when the thing is needed.",
        "See [ref](references/one.md).\n",
    );
    let long_name = "a".repeat(65);
    let cases: Vec<(&str, MemFs, &str)> = vec![
        (
            "valid",
            MemFs::default()
                .file("good-skill/SKILL.md", &good)
                .file("good-skill/references/one.md", "plain reference\n"),
            "",
        ),
        (
            "no frontmatter",
            MemFs::default().file("good-skill/SKILL.md", "# just a body\n"),
            "must start with YAML frontmatter",
        ),
        (
            "name differs from directory",
            MemFs::default().file(
                "good-skill/SKILL.md",
                skill("name: other\ndescription: x", ""),
            ),
            "must equal the directory name",
        ),
        (
            "uppercase name",
            MemFs::default().file("Bad/SKILL.md", skill("name: Bad\ndescription: x", "")),
            "lowercase letters",
        ),
        (
            "consecutive hyphens",
            MemFs::default().file("a--b/SKILL.md", skill("name: a--b\ndescription: x", "")),
            "lowercase letters",
        ),
        (
            "trailing hyphen",
            MemFs::default().file("ab-/SKILL.md", skill("name: ab-\ndescription: x", "")),
            "lowercase letters",
        ),
        (
            "name too long",
            MemFs::default().file(
                &format!("{long_name}/SKILL.md"),
                skill(&format!("name: {long_name}\ndescription: x"), ""),
            ),
            "longer than 64",
        ),
        (
            "empty description",
            MemFs::default().file(
                "good-skill/SKILL.md",
                skill("name: good-skill\ndescription: \"\"", ""),
            ),
            "description must be",
        ),
        (
            "description too long",
            MemFs::default().file(
                "good-skill/SKILL.md",
                skill(
                    &format!("name: good-skill\ndescription: {}", "d".repeat(1025)),
                    "",
                ),
            ),
            "description must be",
        ),
        (
            "body too long",
            MemFs::default().file(
                "good-skill/SKILL.md",
                skill("name: good-skill\ndescription: x", &"line\n".repeat(501)),
            ),
            "the limit is 500",
        ),
        (
            "broken link",
            MemFs::default().file(
                "good-skill/SKILL.md",
                skill(
                    "name: good-skill\ndescription: x",
                    "[gone](references/gone.md)\n",
                ),
            ),
            "does not exist",
        ),
        (
            "nested references",
            MemFs::default()
                .file(
                    "good-skill/SKILL.md",
                    skill("name: good-skill\ndescription: x", ""),
                )
                .file("good-skill/references/deep/more.md", "x"),
            "one level deep",
        ),
        (
            "reserved word in name",
            MemFs::default().file(
                "claude-helper/SKILL.md",
                skill("name: claude-helper\ndescription: x", ""),
            ),
            "reserved word",
        ),
        (
            "XML tag in description",
            MemFs::default().file(
                "good-skill/SKILL.md",
                skill("name: good-skill\ndescription: Use <b>this</b> skill", ""),
            ),
            "XML tag",
        ),
        (
            "body over the token budget",
            MemFs::default().file(
                "good-skill/SKILL.md",
                skill(
                    "name: good-skill\ndescription: x",
                    &"x".repeat(4 * MAX_BODY_TOKENS + 1),
                ),
            ),
            "the budget is 5000",
        ),
        (
            "long reference without contents",
            MemFs::default()
                .file("good-skill/SKILL.md", &good)
                .file("good-skill/references/one.md", "line\n".repeat(101)),
            "without a Contents section",
        ),
        (
            "long reference with contents",
            MemFs::default().file("good-skill/SKILL.md", &good).file(
                "good-skill/references/one.md",
                format!("## Contents\n{}", "line\n".repeat(101)),
            ),
            "",
        ),
        (
            "reference chain",
            MemFs::default()
                .file("good-skill/SKILL.md", &good)
                .file("good-skill/references/one.md", "see [two](two.md)\n")
                .file("good-skill/references/two.md", "x"),
            "must not chain",
        ),
    ];
    for (name, fs, want) in cases {
        let dir = skill_dirs(&fs, "").remove(0);
        let problems = validate_skill(&fs, &dir).join("\n");
        if want.is_empty() {
            assert!(
                problems.is_empty(),
                "{name}: valid skill rejected:\n{problems}"
            );
        } else {
            assert!(
                problems.contains(want),
                "{name}: want a problem containing {want:?}, got:\n{problems}"
            );
        }
    }
}

#[test]
fn description_overlap_scores_near_copies_high_and_strangers_low() {
    let a = "Writes and reviews SQL for a pgmi project. Use when writing functions, views and migrations.";
    assert!(description_overlap(a, &format!("{a} Also triggers.")) >= OVERLAP_THRESHOLD);
    assert!(
        description_overlap(a, "Draws diagrams of network topology for slide decks.")
            < OVERLAP_THRESHOLD
    );
}

#[test]
fn shipped_skill_descriptions_do_not_overlap() {
    let fs = scaffold(Template::Advanced);
    let names = skill_dirs(&fs, SKILLS_ROOT);
    let descriptions: Vec<String> = names
        .iter()
        .map(|n| {
            read_skill(&fs, &format!("{SKILLS_ROOT}/{n}"))
                .unwrap_or_else(|e| panic!("{n}: {e}"))
                .0
                .description
        })
        .collect();
    let mut problems = Vec::new();
    for i in 0..names.len() {
        for j in i + 1..names.len() {
            let score = description_overlap(&descriptions[i], &descriptions[j]);
            if score >= OVERLAP_THRESHOLD {
                problems.push(format!(
                    "{} and {} share {:.0}% of their description words: they trigger on the same requests",
                    names[i],
                    names[j],
                    100.0 * score
                ));
            }
        }
    }
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}

#[test]
fn scaffold_writes_agent_guidance_that_passes_the_spec() {
    for t in [Template::Basic, Template::Advanced] {
        let fs = scaffold(t);
        assert_eq!(
            read(&fs, "CLAUDE.md").trim(),
            "@AGENTS.md",
            "{t}: CLAUDE.md must import AGENTS.md and nothing else"
        );
        let agents = read(&fs, "AGENTS.md");
        let skills = skill_dirs(&fs, SKILLS_ROOT);
        assert!(!skills.is_empty(), "{t}: no skills scaffolded");
        let mut problems = Vec::new();
        for name in &skills {
            problems.extend(validate_skill(&fs, &format!("{SKILLS_ROOT}/{name}")));
            if !agents.contains(&format!("{SKILLS_ROOT}/{name}/")) {
                problems.push(format!(
                    "{t}: AGENTS.md does not route to {SKILLS_ROOT}/{name}/"
                ));
            }
        }
        assert!(problems.is_empty(), "{t}:\n  {}", problems.join("\n  "));
    }
}

#[test]
fn basic_scaffold_gets_exactly_the_shared_skills() {
    let shared = skill_dirs(&EmbeddedFs::new("skills"), "");
    assert_eq!(skill_dirs(&scaffold(Template::Basic), SKILLS_ROOT), shared);
    let advanced = skill_dirs(&scaffold(Template::Advanced), SKILLS_ROOT);
    for name in &shared {
        assert!(
            advanced.contains(name),
            "advanced scaffold lacks shared skill {name}"
        );
    }
}

#[test]
fn shared_skills_name_no_advanced_only_identifiers() {
    let advanced_only = Regex::new(r"\bapi\.|\bmembership\.|\binputSchema\b|\bgateway\b").unwrap();
    let mut hits = Vec::new();
    for (path, text) in tree("skills") {
        for (i, line) in text.split('\n').enumerate() {
            if let Some(m) = advanced_only.find(line) {
                hits.push(format!(
                    "{path}:{} names advanced-only {:?}",
                    i + 1,
                    m.as_str()
                ));
            }
        }
    }
    assert!(hits.is_empty(), "{}", hits.join("\n"));
}

#[test]
fn agents_md_points_instead_of_restating() {
    let exit_code_row = Regex::new(r"(?m)^\|\s*1[0-6]\s*\|").unwrap();
    let view_columns = Regex::new(r"is_sql_file|pgmi_checksum|execution_order|sort_key").unwrap();
    for t in ["basic", "advanced"] {
        let text = embedded(&format!("templates/{t}/AGENTS.md"));
        assert!(
            text.contains("pgmi ai contract"),
            "{t}: AGENTS.md must send the agent to `pgmi ai contract` for the session API"
        );
        assert!(
            !exit_code_row.is_match(&text),
            "{t}: AGENTS.md carries an exit-code table; point to `pgmi ai` instead"
        );
        if let Some(m) = view_columns.find(&text) {
            panic!(
                "{t}: AGENTS.md names view column {:?}; point to `pgmi ai contract` instead",
                m.as_str()
            );
        }
    }
}

#[test]
fn scaffold_ai_files_are_embedded() {
    for t in ["basic", "advanced"] {
        for f in ["AGENTS.md", "CLAUDE.md"] {
            assert!(
                pgmi_shared::file(&format!("templates/{t}/{f}")).is_some(),
                "templates/{t}/{f} is not embedded"
            );
        }
    }
    assert!(pgmi_shared::dir("templates/advanced/.agents/skills/").count() > 0);
    assert!(pgmi_shared::dir("skills/").count() > 0);
}
