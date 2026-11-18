//! `pgmi ai check`: whether the skills in `.agents/skills/` are where Claude
//! Code finds them. Claude Code reads `.claude/skills/` in the directory it
//! starts in and its parents up to the repository root, never
//! `.agents/skills/`; a `.claude/skills/` below the start directory loads
//! only once Claude reads a file there.

use std::fmt::Write as _;
use std::path::{Path, PathBuf};

use crate::error::{Error, Result};
use crate::templates::{CLAUDE_SKILLS, SKILLS};
use crate::vfs::{self, FileKind, OsFs, Vfs};

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Problem {
    NoSkillFile(String),
    Missing(String),
    NotADirectory(String),
    Differs(String),
}

impl Problem {
    fn skill(&self) -> &str {
        match self {
            Problem::NoSkillFile(n)
            | Problem::Missing(n)
            | Problem::NotADirectory(n)
            | Problem::Differs(n) => n,
        }
    }

    fn fixable(&self) -> bool {
        !matches!(self, Problem::NoSkillFile(_))
    }

    fn text(&self) -> String {
        match self {
            Problem::NoSkillFile(n) => {
                format!("{n}: {SKILLS}{n}/ has no SKILL.md, so no agent loads it")
            }
            Problem::Missing(n) => {
                format!("{n}: {CLAUDE_SKILLS}{n}/ is missing, so Claude Code cannot see it")
            }
            Problem::NotADirectory(n) => format!(
                "{n}: {CLAUDE_SKILLS}{n} is not a directory (a symlink checked out as a file?), so Claude Code cannot see it"
            ),
            Problem::Differs(n) => format!(
                "{n}: {CLAUDE_SKILLS}{n}/ differs from {SKILLS}{n}/, so Claude Code reads an old copy"
            ),
        }
    }
}

#[derive(Debug, Default, PartialEq, Eq)]
pub(crate) struct Report {
    pub(crate) skills: Vec<String>,
    pub(crate) problems: Vec<Problem>,
}

/// The skills under `.agents/skills/` of `project` and what keeps Claude
/// Code from reading each one as it is there.
pub(crate) fn check(project: &impl Vfs) -> std::io::Result<Report> {
    let source = SKILLS.trim_end_matches('/');
    let entries = match project.read_dir(source) {
        Ok(e) => e,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Report::default()),
        Err(e) => return Err(e),
    };
    let mut report = Report::default();
    for entry in entries {
        if entry.stat.kind != FileKind::Dir {
            continue;
        }
        let name = entry.name.as_str().to_string();
        report.skills.push(name.clone());
        let skill = format!("{SKILLS}{name}");
        if !project
            .stat(&format!("{skill}/SKILL.md"))
            .is_ok_and(|s| s.kind == FileKind::File)
        {
            report.problems.push(Problem::NoSkillFile(name));
            continue;
        }
        let copy = format!("{CLAUDE_SKILLS}{name}");
        let problem = match project.stat(&copy) {
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Some(Problem::Missing(name)),
            Err(e) => return Err(e),
            Ok(s) if s.kind != FileKind::Dir => Some(Problem::NotADirectory(name)),
            Ok(_) => {
                (tree(project, &skill)? != tree(project, &copy)?).then_some(Problem::Differs(name))
            }
        };
        report.problems.extend(problem);
    }
    Ok(report)
}

fn tree(project: &impl Vfs, rel: &str) -> std::io::Result<Vec<(String, Vec<u8>)>> {
    let mut files = vfs::files(&Sub {
        fs: project,
        prefix: rel,
    })?;
    files.sort();
    Ok(files)
}

struct Sub<'a, V> {
    fs: &'a V,
    prefix: &'a str,
}

impl<V: Vfs> Sub<'_, V> {
    fn rel(&self, rel: &str) -> String {
        if rel.is_empty() {
            self.prefix.to_string()
        } else {
            format!("{}/{rel}", self.prefix)
        }
    }
}

impl<V: Vfs> Vfs for Sub<'_, V> {
    fn root(&self) -> &Path {
        self.fs.root()
    }
    fn stat(&self, rel: &str) -> std::io::Result<vfs::Stat> {
        self.fs.stat(&self.rel(rel))
    }
    fn read(&self, rel: &str) -> std::io::Result<Vec<u8>> {
        self.fs.read(&self.rel(rel))
    }
    fn read_dir(&self, rel: &str) -> std::io::Result<Vec<vfs::Entry>> {
        self.fs.read_dir(&self.rel(rel))
    }
}

/// The root of the git work tree `dir` is in, when that is not `dir` itself.
pub(crate) fn enclosing_repo(dir: &Path) -> Option<PathBuf> {
    let dir = std::path::absolute(dir).ok()?;
    let root = dir.ancestors().find(|a| a.join(".git").exists())?;
    (root != dir).then(|| root.to_path_buf())
}

/// What a Claude Code session started at `repo` cannot see at its start:
/// the skills with no `.claude/skills/<name>` at the repository root.
fn hidden_from_root(repo: &Path, skills: &[String]) -> usize {
    skills
        .iter()
        .filter(|n| !repo.join(CLAUDE_SKILLS).join(n.as_str()).is_dir())
        .count()
}

fn replace_copy(project: &Path, name: &str) -> Result<()> {
    let copy = project.join(CLAUDE_SKILLS).join(name);
    let failed = |what: &str, e: std::io::Error| {
        Error::general(format!("{what} {}", copy.display())).with_source(e)
    };
    match std::fs::symlink_metadata(&copy) {
        Ok(m) if m.is_dir() => std::fs::remove_dir_all(&copy).map_err(|e| failed("remove", e))?,
        Ok(_) => std::fs::remove_file(&copy).map_err(|e| failed("remove", e))?,
        Err(_) => {}
    }
    let source = OsFs::new(project.join(SKILLS).join(name));
    for (rel, bytes) in vfs::files(&source).map_err(|e| failed("read the source of", e))? {
        let path = copy.join(&rel);
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| failed("create", e))?;
        }
        std::fs::write(&path, bytes).map_err(|e| failed("write", e))?;
    }
    Ok(())
}

/// `pgmi ai check`: one line per problem and a summary, and the error to
/// exit with when Claude Code cannot see a skill or reads an old copy. With
/// `fix`, first copies `.agents/skills/<name>/` over every missing, broken or
/// old copy.
pub(crate) fn run(path: &Path, fix: bool) -> Result<(String, Option<Error>)> {
    let project = OsFs::project(path)?;
    let read = |e: std::io::Error| {
        Error::general(format!("read {}", path.join(SKILLS).display())).with_source(e)
    };
    let mut report = check(&project).map_err(read)?;
    let mut out = String::new();
    if fix {
        for p in report.problems.iter().filter(|p| p.fixable()) {
            replace_copy(path, p.skill())?;
            let _ = writeln!(out, "copied {SKILLS}{0}/ to {CLAUDE_SKILLS}{0}/", p.skill());
        }
        report = check(&project).map_err(read)?;
    }
    if report.skills.is_empty() {
        let _ = writeln!(out, "no skills in {}", path.join(SKILLS).display());
        return Ok((out, None));
    }
    for p in &report.problems {
        out.push_str(&p.text());
        out.push('\n');
    }
    if let Some(repo) = enclosing_repo(path) {
        let hidden = hidden_from_root(&repo, &report.skills);
        if hidden > 0 {
            let rel = std::path::absolute(path)
                .ok()
                .and_then(|p| p.strip_prefix(&repo).ok().map(Path::to_path_buf))
                .unwrap_or_else(|| path.to_path_buf());
            let _ = write!(
                out,
                "note: this project is in {rel}/ of the repository {repo}. A Claude Code session started at\nthe repository root sees {hidden} of these skills only after it reads a file in {rel}/.\nStart Claude Code in {rel}/, or run /add-dir {rel} in the session.\n",
                rel = rel.display(),
                repo = repo.display(),
            );
        }
    }
    let n = report.skills.len();
    if report.problems.is_empty() {
        let _ = writeln!(out, "{n} skills; Claude Code finds every one.");
        return Ok((out, None));
    }
    let broken = report.problems.len();
    let hint = if report.problems.iter().any(Problem::fixable) {
        "; pgmi ai check --fix copies .agents/skills/ over the broken copies"
    } else {
        ""
    };
    let failure = Error::general(format!("{broken} of {n} skills have a problem{hint}"));
    Ok((out, Some(failure)))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vfs::MemFs;

    fn skill(fs: MemFs, root: &str, name: &str, body: &str) -> MemFs {
        fs.file(&format!("{root}{name}/SKILL.md"), body)
            .file(&format!("{root}{name}/references/a.md"), "ref")
    }

    #[test]
    fn a_copy_that_matches_is_no_problem() {
        let fs = skill(
            skill(MemFs::default(), SKILLS, "a", "x"),
            CLAUDE_SKILLS,
            "a",
            "x",
        );
        let r = check(&fs).unwrap();
        assert_eq!(r.skills, ["a"]);
        assert!(r.problems.is_empty(), "{:?}", r.problems);
    }

    #[test]
    fn a_missing_broken_or_old_copy_is_a_problem() {
        let fs = skill(MemFs::default(), SKILLS, "missing", "x");
        let fs =
            skill(fs, SKILLS, "file", "x").file(".claude/skills/file", "../../.agents/skills/file");
        let fs = skill(skill(fs, SKILLS, "old", "new"), CLAUDE_SKILLS, "old", "old");
        let fs = fs.file(".agents/skills/empty/notes.md", "");
        let fs = fs.file(".agents/skills/README.md", "not a skill");
        let r = check(&fs).unwrap();
        assert_eq!(r.skills, ["empty", "file", "missing", "old"]);
        assert_eq!(
            r.problems,
            [
                Problem::NoSkillFile("empty".into()),
                Problem::NotADirectory("file".into()),
                Problem::Missing("missing".into()),
                Problem::Differs("old".into()),
            ]
        );
    }

    #[test]
    fn a_linked_copy_counts_as_its_target() {
        let fs = skill(MemFs::default(), SKILLS, "a", "x").link_to_dir(".claude/skills/a");
        let r = check(&fs).unwrap();
        assert_eq!(r.problems, [Problem::Differs("a".into())]);
    }

    #[test]
    fn a_project_without_skills_reports_none() {
        assert_eq!(check(&MemFs::default()).unwrap(), Report::default());
    }

    #[test]
    fn fix_replaces_missing_old_and_linked_copies_and_then_passes() {
        let dir = crate::testdir::TempDir::new("ai-check-fix");
        let p = dir.path();
        for n in ["a", "b", "c"] {
            std::fs::create_dir_all(p.join(SKILLS).join(n).join("references")).unwrap();
            std::fs::write(
                p.join(SKILLS).join(n).join("SKILL.md"),
                format!("skill {n}"),
            )
            .unwrap();
            std::fs::write(p.join(SKILLS).join(n).join("references/r.md"), "r").unwrap();
        }
        std::fs::create_dir_all(p.join(CLAUDE_SKILLS).join("b")).unwrap();
        std::fs::write(p.join(CLAUDE_SKILLS).join("b/SKILL.md"), "stale").unwrap();
        std::fs::write(p.join(CLAUDE_SKILLS).join("c"), "../../.agents/skills/c").unwrap();
        let (_, e) = run(p, false).unwrap();
        let e = e.unwrap();
        assert_eq!(e.exit_code(), 1);
        assert!(
            e.text().starts_with("3 of 3 skills have a problem"),
            "{}",
            e.text()
        );
        let (out, e) = run(p, true).unwrap();
        assert!(e.is_none());
        assert!(
            out.contains("copied .agents/skills/a/ to .claude/skills/a/"),
            "{out}"
        );
        assert!(
            out.ends_with("3 skills; Claude Code finds every one.\n"),
            "{out}"
        );
        assert_eq!(
            std::fs::read_to_string(p.join(CLAUDE_SKILLS).join("b/SKILL.md")).unwrap(),
            "skill b"
        );
        assert_eq!(
            std::fs::read_to_string(p.join(CLAUDE_SKILLS).join("c/references/r.md")).unwrap(),
            "r"
        );
    }

    #[test]
    fn a_project_in_a_subdirectory_of_a_repository_gets_a_note() {
        let dir = crate::testdir::TempDir::new("ai-check-nested");
        std::fs::create_dir_all(dir.join(".git")).unwrap();
        let db = dir.join("db");
        crate::commands::init::scaffold(&db, crate::templates::Template::Basic, Some("p"), &|_| {})
            .unwrap();
        let (out, e) = run(&db, false).unwrap();
        assert!(e.is_none(), "{out}");
        assert!(
            out.contains("note: this project is in db/ of the repository"),
            "{out}"
        );
        assert!(out.contains("run /add-dir db in the session"), "{out}");
        assert_eq!(enclosing_repo(dir.path()), None);
        assert_eq!(enclosing_repo(&db), Some(dir.path().to_path_buf()));
    }
}
