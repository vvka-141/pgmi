//! The project templates `pgmi init` scaffolds: their names, summaries and
//! descriptions, and the files a template renders to.

use std::fmt;
use std::io;
use std::str::FromStr;

use crate::vfs::{self, EmbeddedFs};

const PLACEHOLDER: &str = "{{PROJECT_NAME}}";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Template {
    Minimal,
    Basic,
    Advanced,
}

impl Template {
    pub(crate) const ALL: [Template; 3] = [Template::Minimal, Template::Basic, Template::Advanced];

    pub(crate) fn name(self) -> &'static str {
        match self {
            Template::Minimal => "minimal",
            Template::Basic => "basic",
            Template::Advanced => "advanced",
        }
    }

    pub(crate) fn summary(self) -> &'static str {
        match self {
            Template::Minimal => {
                "A deploy.sql loop, hello_world.sql and a .pgmiignore; nothing else"
            }
            Template::Basic => "Two layers: run-once tables, re-run views and functions",
            Template::Advanced => "Reference app: SQL-native REST/RPC/MCP, multi-tenant RLS, audit",
        }
    }

    pub(crate) fn best_for(self) -> &'static str {
        match self {
            Template::Minimal => "Building your own deployment from nothing",
            Template::Basic => "Start here. Deploying SQL — small, explicit, any managed provider",
            Template::Advanced => "SQL-native application stack — scaffold, read, trim to fit",
        }
    }

    pub(crate) fn description(self) -> &'static str {
        match self {
            Template::Minimal => include_str!("templates/minimal.txt"),
            Template::Basic => include_str!("templates/basic.txt"),
            Template::Advanced => include_str!("templates/advanced.txt"),
        }
    }

    /// What a scaffold writes, in the order `pgmi init -v` names it: the
    /// template's files by their embedded names, then the shared skills.
    pub(crate) fn sources(self) -> io::Result<Vec<Source>> {
        let names = |root: &str| -> io::Result<Vec<String>> {
            Ok(vfs::files(&EmbeddedFs::new(root))?
                .into_iter()
                .map(|(p, _)| p)
                .collect())
        };
        let mut out: Vec<Source> = names(&format!("templates/{self}"))?
            .into_iter()
            .map(Source::Template)
            .collect();
        if self != Template::Minimal {
            out.extend(
                names("skills")?
                    .into_iter()
                    .map(|p| Source::Shared(format!(".agents/skills/{p}"))),
            );
        }
        Ok(out)
    }

    /// The files of a new project named `project`, as `(relative path,
    /// bytes)`: the embedded template with `{{PROJECT_NAME}}` filled in,
    /// `.scaffold` suffixes dropped, a first line in `deploy.sql` naming
    /// `version` and the template, and the shared skills under
    /// `.agents/skills/` for every template but `minimal`.
    pub(crate) fn render(self, project: &str, version: &str) -> io::Result<Vec<(String, Vec<u8>)>> {
        let mut files = vfs::files(&EmbeddedFs::new(&format!("templates/{self}")))?;
        if files.is_empty() {
            return Err(io::ErrorKind::NotFound.into());
        }
        if self != Template::Minimal {
            files.extend(
                vfs::files(&EmbeddedFs::new("skills"))?
                    .into_iter()
                    .map(|(p, b)| (format!(".agents/skills/{p}"), b)),
            );
        }
        Ok(files
            .into_iter()
            .map(|(rel, bytes)| {
                let rel = rel.strip_suffix(".scaffold").unwrap_or(&rel).to_string();
                let bytes = fill(&rel, bytes, project);
                if rel == "deploy.sql" {
                    let mut text =
                        format!("-- Scaffolded by pgmi {version} from the {self} template.\n")
                            .into_bytes();
                    text.extend_from_slice(&bytes);
                    (rel, text)
                } else {
                    (rel, bytes)
                }
            })
            .collect())
    }
}

/// One file a scaffold writes, by the name it is embedded under.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Source {
    Template(String),
    Shared(String),
}

impl Source {
    pub(crate) fn name(&self) -> &str {
        match self {
            Source::Template(n) | Source::Shared(n) => n,
        }
    }

    /// The path the file is written to in the project.
    pub(crate) fn path(&self) -> &str {
        let name = self.name();
        name.strip_suffix(".scaffold").unwrap_or(name)
    }
}

impl fmt::Display for Template {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.name())
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct UnknownTemplate(pub(crate) String);

impl FromStr for Template {
    type Err = UnknownTemplate;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        Template::ALL
            .into_iter()
            .find(|t| t.name() == s)
            .ok_or_else(|| UnknownTemplate(s.to_string()))
    }
}

fn fill(rel: &str, bytes: Vec<u8>, name: &str) -> Vec<u8> {
    match std::str::from_utf8(&bytes) {
        Ok(text) if text.contains(PLACEHOLDER) => {
            let value = if rel.ends_with(".json") {
                let quoted = serde_json::to_string(name).unwrap_or_default();
                quoted
                    .strip_prefix('"')
                    .and_then(|q| q.strip_suffix('"'))
                    .unwrap_or_default()
                    .to_string()
            } else {
                name.to_string()
            };
            text.replace(PLACEHOLDER, &value).into_bytes()
        }
        _ => bytes,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::project::walk;
    use crate::vfs::{MemFs, Vfs};

    fn rendered(template: Template, name: &str) -> MemFs {
        MemFs::from_files(template.render(name, crate::VERSION).unwrap())
    }

    #[test]
    fn names_round_trip_and_unknown_names_are_refused() {
        for t in Template::ALL {
            assert_eq!(t.name().parse(), Ok(t));
            assert_eq!(t.to_string(), t.name());
        }
        assert_eq!(
            "Basic".parse::<Template>(),
            Err(UnknownTemplate("Basic".into()))
        );
    }

    #[test]
    fn every_template_has_a_summary_and_a_description() {
        for t in Template::ALL {
            assert!(!t.summary().is_empty(), "{t}");
            assert!(!t.best_for().is_empty(), "{t}");
            assert!(!t.description().trim().is_empty(), "{t}");
        }
    }

    #[test]
    fn every_template_scaffolds_a_project_that_loads_in_memory() {
        for t in Template::ALL {
            let fs = rendered(t, "shop");
            let p = walk::load(&fs, walk::DEFAULT_MAX_FILE_SIZE)
                .unwrap_or_else(|e| panic!("{t}: {}", e.text()));
            assert!(
                p.deploy_sql.starts_with(&format!(
                    "-- Scaffolded by pgmi {} from the {t} template.\n",
                    crate::VERSION
                )),
                "{t}"
            );
            crate::project::metadata::collect(&p.sources)
                .unwrap_or_else(|e| panic!("{t}: {}", e.text()));
            let all = vfs::files(&fs).unwrap();
            assert!(
                all.iter().all(|(_, b)| !b
                    .windows(PLACEHOLDER.len())
                    .any(|w| w == PLACEHOLDER.as_bytes())),
                "{t}: placeholder left"
            );
            assert!(all.iter().all(|(p, _)| !p.ends_with(".scaffold")), "{t}");
        }
    }

    #[test]
    fn deploy_sql_records_the_version_and_the_template() {
        for t in Template::ALL {
            let files = t.render("p", "1.2.3").unwrap();
            let (_, deploy) = files.iter().find(|(p, _)| p == "deploy.sql").unwrap();
            let first = deploy.split(|b| *b == b'\n').next().unwrap();
            assert_eq!(
                std::str::from_utf8(first).unwrap(),
                format!("-- Scaffolded by pgmi 1.2.3 from the {t} template.")
            );
        }
    }

    #[test]
    fn skills_ship_with_basic_and_advanced_only() {
        let has_skills = |t: Template| {
            t.render("x", "0")
                .unwrap()
                .iter()
                .any(|(p, _)| p.starts_with(".agents/skills/"))
        };
        assert!(has_skills(Template::Basic));
        assert!(has_skills(Template::Advanced));
        assert!(!has_skills(Template::Minimal));
    }

    #[test]
    fn the_project_name_is_json_escaped_in_json_files() {
        let fs = rendered(Template::Basic, "a\"b");
        let json = String::from_utf8(fs.read("project.json").unwrap()).unwrap();
        assert!(json.contains(r#""app_name": "a\"b""#), "{json}");
        let readme = String::from_utf8(fs.read("README.md").unwrap()).unwrap();
        assert!(readme.starts_with("# a\"b\n"), "{readme}");
        for name in [
            "myapp",
            "my\"app",
            "my\\app",
            "日本語プロジェクト",
            "my-app-🚀",
        ] {
            let fs = rendered(Template::Basic, name);
            let json: serde_json::Value = serde_json::from_slice(&fs.read("project.json").unwrap())
                .unwrap_or_else(|e| panic!("{name}: {e}"));
            assert_eq!(json["app_name"], name);
        }
    }

    #[test]
    fn scaffold_suffix_is_dropped() {
        let files = Template::Advanced.render("x", "0").unwrap();
        assert!(files.iter().any(|(p, _)| p == "gateways/go/go.mod"));
    }

    #[test]
    fn no_dot_directory_is_embedded_except_agents() {
        for (path, _) in pgmi_shared::dir("templates/") {
            let dirs: Vec<&str> = path.split('/').collect();
            if dirs.get(2) == Some(&".agents") {
                continue;
            }
            for dir in &dirs[..dirs.len() - 1] {
                assert!(
                    !dir.starts_with('.'),
                    "{path}: dot-directory {dir} is embedded; it is gitignored, so a CI build would not scaffold it"
                );
            }
        }
    }
}
