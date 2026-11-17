use std::collections::{BTreeMap, BTreeSet};

use regex_lite::Regex;
use serde::Deserialize;

use super::{repo_file, repo_tree, tree};

const SITE: &str = "https://vvka-141.github.io/pgmi/";

#[derive(Default)]
struct Page {
    paths: Vec<String>,
    anchors: BTreeSet<String>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FrontMatter {
    slug: Option<String>,
    url: Option<String>,
    aliases: Vec<String>,
}

fn split_front_matter(body: &str) -> (FrontMatter, &str) {
    let Some(rest) = body.strip_prefix("---\n") else {
        return (FrontMatter::default(), body);
    };
    let Some(end) = rest.find("\n---\n") else {
        return (FrontMatter::default(), body);
    };
    let fm = serde_saphyr::from_str(&rest[..end]).unwrap_or_else(|e| panic!("front matter: {e}"));
    (fm, &rest[end + 5..])
}

fn clean(path: &str) -> String {
    let mut parts: Vec<&str> = Vec::new();
    for p in path.split('/') {
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

fn anchor(heading: &str) -> String {
    heading
        .to_lowercase()
        .chars()
        .filter_map(|c| match c {
            ' ' => Some('-'),
            c if c.is_alphanumeric() || c == '-' || c == '_' => Some(c),
            _ => None,
        })
        .collect()
}

fn heading_anchors(content: &str) -> BTreeSet<String> {
    let heading = Regex::new(r"^#{1,6}\s+(.+?)\s*#*\s*$").unwrap();
    let mut in_fence = false;
    let mut anchors = BTreeSet::new();
    for line in content.lines() {
        if line.trim().starts_with("```") {
            in_fence = !in_fence;
        } else if !in_fence && let Some(c) = heading.captures(line) {
            anchors.insert(anchor(&c[1]));
        }
    }
    anchors
}

fn article_paths(stem: &str, fm: &FrontMatter) -> Vec<String> {
    let mut paths = vec![format!("articles/{}", fm.slug.as_deref().unwrap_or(stem))];
    for a in &fm.aliases {
        paths.push(if a.starts_with('/') {
            clean(a)
        } else {
            clean(&format!("articles/{a}"))
        });
    }
    paths
}

fn articles() -> Vec<Page> {
    let pages: Vec<Page> = repo_tree("website/content/articles")
        .into_iter()
        .filter_map(|(path, body)| {
            let stem = path
                .strip_prefix("website/content/articles/")?
                .strip_suffix(".md")?;
            if stem == "_index" || stem.contains('/') {
                return None;
            }
            let (fm, content) = split_front_matter(&body);
            Some(Page {
                paths: article_paths(stem, &fm),
                anchors: heading_anchors(content),
            })
        })
        .collect();
    assert!(!pages.is_empty(), "no articles in website/content/articles");
    pages
}

fn article_link_problems(docs: &[(String, String)], articles: &[Page]) -> Vec<String> {
    let link =
        Regex::new(r#"https://vvka-141\.github\.io/pgmi/articles/([^/#)\s"'>]+)/?(#[^)\s"'>]*)?"#)
            .unwrap();
    let known: BTreeMap<&str, &Page> = articles
        .iter()
        .flat_map(|a| a.paths.iter().map(move |p| (p.as_str(), a)))
        .collect();
    let mut problems = Vec::new();
    for (file, text) in docs {
        for c in link.captures_iter(text) {
            let url = &c[0];
            let Some(a) = known.get(format!("articles/{}", &c[1]).as_str()) else {
                problems.push(format!(
                    "{file} links to {url}, which no article in website/content/articles answers; add the old path to the renamed article's aliases"
                ));
                continue;
            };
            let frag = c.get(2).map_or("", |m| &m.as_str()[1..]);
            if !frag.is_empty() && !a.anchors.contains(frag) {
                problems.push(format!(
                    "{file} links to {url}, but the article has no heading #{frag}: link to the article, not the heading, or keep the heading"
                ));
            }
        }
    }
    problems
}

#[test]
fn article_links_resolve() {
    let docs: Vec<(String, String)> = repo_tree("docs")
        .into_iter()
        .filter(|(p, _)| p.ends_with(".md"))
        .chain([("README.md".to_string(), repo_file("README.md"))])
        .chain(
            tree("templates")
                .into_iter()
                .filter(|(p, _)| p.matches('/').count() == 2 && p.ends_with("/README.md")),
        )
        .collect();
    let problems = article_link_problems(&docs, &articles());
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}

#[test]
fn article_link_problems_catch_renames_without_alias_and_missing_headings() {
    let doc = [(
        "README.md".to_string(),
        "See [it](https://vvka-141.github.io/pgmi/articles/old-name/).".to_string(),
    )];
    let renamed = [Page {
        paths: vec!["articles/new-name".into()],
        ..Page::default()
    }];
    let problems = article_link_problems(&doc, &renamed);
    assert!(
        problems.len() == 1
            && problems[0].contains("README.md")
            && problems[0].contains("articles/old-name"),
        "{problems:?}"
    );
    for alias in ["/articles/old-name", "/articles/old-name/", "old-name"] {
        let fm = FrontMatter {
            aliases: vec![alias.into()],
            ..FrontMatter::default()
        };
        let with_alias = [Page {
            paths: article_paths("new-name", &fm),
            ..Page::default()
        }];
        assert!(
            article_link_problems(&doc, &with_alias).is_empty(),
            "alias {alias:?} should keep the old link working"
        );
    }
    let anchored = [(
        "README.md".to_string(),
        "[x](https://vvka-141.github.io/pgmi/articles/a/#gone-heading)".to_string(),
    )];
    let with_heading = [Page {
        paths: vec!["articles/a".into()],
        anchors: heading_anchors("## Kept heading\n```\n# gone heading\n```\n"),
    }];
    let problems = article_link_problems(&anchored, &with_heading);
    assert!(
        problems.len() == 1 && problems[0].contains("link to the article, not the heading"),
        "{problems:?}"
    );
}

#[test]
fn heading_anchors_follow_hugo() {
    let got =
        heading_anchors("# The `--compat` flag\n## Exit Codes ##\n### pgmi_plan_view, ordered\n");
    let want: BTreeSet<String> = ["the---compat-flag", "exit-codes", "pgmi_plan_view-ordered"]
        .iter()
        .map(|s| (*s).to_string())
        .collect();
    assert_eq!(got, want);
}

#[test]
fn llms_txt_links_resolve() {
    let mut pages: BTreeMap<String, String> = BTreeMap::new();
    for (path, body) in repo_tree("docs") {
        if !path.ends_with(".md") {
            continue;
        }
        let rel = path.trim_start_matches("docs/").to_lowercase();
        let slug = rel
            .strip_suffix("/_index.md")
            .or_else(|| rel.strip_suffix("/readme.md"))
            .or_else(|| rel.strip_suffix(".md"))
            .unwrap_or(&rel)
            .to_string();
        let (fm, _) = split_front_matter(&body);
        if let Some(url) = fm.url {
            let pinned = url.trim_matches('/');
            pages.insert(
                pinned.strip_prefix("docs/").unwrap_or(pinned).to_string(),
                path.clone(),
            );
        }
        pages.insert(slug, path);
    }
    let llms = repo_file("website/static/llms.txt");
    let link = Regex::new(r"\]\((https?://[^)\s]+)\)").unwrap();
    let mut checked = 0;
    let mut problems = Vec::new();
    for c in link.captures_iter(&llms) {
        let url = &c[1];
        let Some(rest) = url.strip_prefix(SITE) else {
            continue;
        };
        let (path, frag) = rest.split_once('#').unwrap_or((rest, ""));
        let Some(slug) = path.trim_matches('/').strip_prefix("docs/") else {
            continue;
        };
        let Some(src) = pages.get(slug) else {
            problems.push(format!(
                "llms.txt links {url} but no docs/*.md maps to {slug:?}"
            ));
            continue;
        };
        checked += 1;
        if !frag.is_empty() && !heading_anchors(&repo_file(src)).contains(frag) {
            problems.push(format!(
                "llms.txt links {url}: no heading anchors to #{frag} in {src}"
            ));
        }
    }
    assert!(checked > 0, "no docs-site links checked in llms.txt");
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}
