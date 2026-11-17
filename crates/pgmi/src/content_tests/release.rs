use std::process::Command;

use regex_lite::Regex;

use super::{repo_file, repo_root};
use crate::testdir::TempDir;

#[test]
fn cicd_guide_pins_the_newest_release() {
    let releases = repo_file("RELEASES.md");
    let newest = Regex::new(r"(?m)^## (v\d+\.\d+\.\d+) — (\d{4}-\d{2}-\d{2}|TBD)")
        .unwrap()
        .captures(&releases)
        .expect("no release heading in RELEASES.md")[1]
        .to_string();
    let guide = repo_file("docs/CICD.md");
    let stale: Vec<&str> = Regex::new(r"v\d+\.\d+\.\d+")
        .unwrap()
        .find_iter(&guide)
        .map(|m| m.as_str())
        .filter(|pin| *pin != newest)
        .collect();
    assert!(
        stale.is_empty(),
        "docs/CICD.md pins {stale:?}; the newest release in RELEASES.md is {newest}"
    );
}

#[test]
fn github_yaml_parses() {
    for path in [
        ".github/workflows/docs.yml",
        ".github/ISSUE_TEMPLATE/bug.yml",
        ".github/ISSUE_TEMPLATE/feature.yml",
        ".github/ISSUE_TEMPLATE/question.yml",
        ".github/ISSUE_TEMPLATE/config.yml",
    ] {
        if let Err(e) = serde_saphyr::from_str::<serde_json::Value>(&repo_file(path)) {
            panic!("{path}: {e}");
        }
    }
}

const DATED: &str = "# Releases\n\n## v1.2.3 — 2026-07-26\n\nWhat changed, for a stranger.\n\n## v1.2.2 — 2026-01-01\n\nOlder.\n";

#[cfg(unix)]
#[test]
fn release_notes_script_refuses_unfinished_sections() {
    if Command::new("bash").arg("-c").arg("true").output().is_err() {
        eprintln!("skipped: bash is not on PATH");
        return;
    }
    let script = repo_root().join("scripts/release-notes.sh");
    let draft_preamble = "# Releases\n\n> **Writing a release?** The tag build fails if the heading carries no\n> `YYYY-MM-DD` date, or if a `<!-- DRAFT -->` marker still sits above it.\n\n## v1.2.3 — 2026-07-26\n\nWhat changed, for a stranger.\n";
    let cases: [(&str, &str, &str, Option<&str>); 11] = [
        ("a dated, final section", DATED, "v1.2.3", None),
        (
            "a final section under its release candidate",
            "# Releases\n\n## v1.2.3-rc.1 — 2026-07-25\n\nCandidate.\n\n## v1.2.3 — 2026-07-26\n\nWhat changed, for a stranger.\n",
            "v1.2.3",
            None,
        ),
        (
            "a draft marker above the heading",
            "# Releases\n\n<!-- DRAFT — set the real date when tagging. -->\n## v1.2.3 — 2026-07-26\n\nBody.\n",
            "v1.2.3",
            Some("still marked a draft"),
        ),
        (
            "a TBD date",
            "# Releases\n\n## v1.2.3 — TBD\n\nBody.\n",
            "v1.2.3",
            Some("no release date"),
        ),
        (
            "no date at all",
            "# Releases\n\n## v1.2.3\n\nBody.\n",
            "v1.2.3",
            Some("no release date"),
        ),
        ("a missing section", DATED, "v9.9.9", Some("has no section")),
        (
            "an empty section",
            "# Releases\n\n## v1.2.3 — 2026-07-26\n\n## v1.2.2 — 2026-01-01\n\nOlder.\n",
            "v1.2.3",
            Some("has no section"),
        ),
        (
            "a blank line between draft marker and heading",
            "# Releases\n\n<!-- DRAFT -->\n\n## v1.2.3 — 2026-07-26\n\nBody.\n",
            "v1.2.3",
            Some("still marked a draft"),
        ),
        (
            "a draft marker inside the section body",
            "# Releases\n\n## v1.2.3 — 2026-07-26\n\n<!-- DRAFT -->\nBody.\n",
            "v1.2.3",
            Some("still marked a draft"),
        ),
        (
            "instructional prose quoting the marker",
            draft_preamble,
            "v1.2.3",
            None,
        ),
        (
            "an indented draft marker",
            "# Releases\n\n  <!-- DRAFT -->\n## v1.2.3 — 2026-07-26\n\nBody.\n",
            "v1.2.3",
            Some("still marked a draft"),
        ),
    ];
    let dir = TempDir::new("release-notes");
    for (i, (name, releases, tag, refusal)) in cases.into_iter().enumerate() {
        let fixture = dir.join(format!("RELEASES-{i}.md"));
        std::fs::write(&fixture, releases).unwrap();
        let out = Command::new("bash")
            .arg(&script)
            .arg(tag)
            .env("PGMI_RELEASES_FILE", &fixture)
            .output()
            .unwrap();
        let text = format!(
            "{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
        match refusal {
            None => assert!(
                out.status.success()
                    && text.contains("What changed")
                    && !text.contains("Candidate"),
                "{name}: expected the section body, got:\n{text}"
            ),
            Some(reason) => assert!(
                !out.status.success() && text.contains(reason),
                "{name}: expected a refusal naming {reason:?}, got:\n{text}"
            ),
        }
    }
}
