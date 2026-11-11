//! Read-only view of a project tree, so loading, scanning and `.pgmiignore`
//! work the same over the disk, embedded templates and in-memory test trees.
//!
//! Paths are relative to the tree's root, `/`-separated, and `""` is the
//! root itself.

use std::io;
use std::path::{Path, PathBuf};

use crate::error::{Error, Result};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    File,
    Dir,
    Other,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Stat {
    pub kind: Kind,
    pub link: bool,
    pub len: u64,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Entry {
    pub name: String,
    pub utf8: bool,
    pub stat: Stat,
}

pub trait Vfs {
    fn root(&self) -> &Path;
    fn stat(&self, rel: &str) -> io::Result<Stat>;
    fn read(&self, rel: &str) -> io::Result<Vec<u8>>;
    fn read_dir(&self, rel: &str) -> io::Result<Vec<Entry>>;

    fn display(&self, rel: &str) -> String {
        if rel.is_empty() {
            self.root().display().to_string()
        } else {
            self.root().join(rel).display().to_string()
        }
    }
}

#[derive(Debug, Clone)]
pub struct OsFs {
    root: PathBuf,
}

impl OsFs {
    pub fn new(root: impl Into<PathBuf>) -> Self {
        OsFs { root: root.into() }
    }

    pub fn project(root: &Path) -> Result<Self> {
        let display = root.display();
        match std::fs::metadata(root) {
            Err(_) => Err(Error::config(format!(
                "project path \"{display}\" does not exist"
            ))),
            Ok(m) if !m.is_dir() => Err(Error::config(format!(
                "\"{display}\" is a file; pass the project directory"
            ))),
            Ok(_) => Ok(Self::new(root)),
        }
    }

    fn path(&self, rel: &str) -> PathBuf {
        if rel.is_empty() {
            self.root.clone()
        } else {
            self.root.join(rel)
        }
    }
}

fn os_stat(path: &Path) -> io::Result<Stat> {
    let meta = std::fs::symlink_metadata(path)?;
    let link = meta.file_type().is_symlink();
    let target = if link {
        std::fs::metadata(path).ok()
    } else {
        Some(meta)
    };
    let kind = match &target {
        Some(m) if m.is_dir() => Kind::Dir,
        Some(m) if m.is_file() => Kind::File,
        _ => Kind::Other,
    };
    Ok(Stat {
        kind,
        link,
        len: target.map_or(0, |m| m.len()),
    })
}

impl Vfs for OsFs {
    fn root(&self) -> &Path {
        &self.root
    }

    fn stat(&self, rel: &str) -> io::Result<Stat> {
        os_stat(&self.path(rel))
    }

    fn read(&self, rel: &str) -> io::Result<Vec<u8>> {
        std::fs::read(self.path(rel))
    }

    fn read_dir(&self, rel: &str) -> io::Result<Vec<Entry>> {
        let mut out = Vec::new();
        for entry in std::fs::read_dir(self.path(rel))? {
            let entry = entry?;
            let stat = os_stat(&entry.path())?;
            let (name, utf8) = match entry.file_name().into_string() {
                Ok(n) => (n, true),
                Err(n) => (n.to_string_lossy().into_owned(), false),
            };
            out.push(Entry { name, utf8, stat });
        }
        out.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(out)
    }
}

pub fn files(fs: &impl Vfs) -> io::Result<Vec<(String, Vec<u8>)>> {
    fn walk(fs: &impl Vfs, rel: &str, out: &mut Vec<(String, Vec<u8>)>) -> io::Result<()> {
        for entry in fs.read_dir(rel)? {
            let child = if rel.is_empty() {
                entry.name
            } else {
                format!("{rel}/{}", entry.name)
            };
            if entry.stat.kind == Kind::Dir {
                walk(fs, &child, out)?;
            } else {
                let body = fs.read(&child)?;
                out.push((child, body));
            }
        }
        Ok(())
    }
    let mut out = Vec::new();
    walk(fs, "", &mut out)?;
    Ok(out)
}

#[cfg(test)]
#[derive(Debug, Clone)]
enum Node {
    File(Vec<u8>),
    Dir,
    Link(Option<Box<Node>>),
    Unreadable(Kind),
}

#[cfg(test)]
impl Node {
    fn stat(&self) -> Stat {
        let (target, link) = match self {
            Node::Link(t) => (t.as_deref(), true),
            n => (Some(n), false),
        };
        let (kind, len) = match target {
            Some(Node::File(b)) => (Kind::File, b.len() as u64),
            Some(Node::Dir) => (Kind::Dir, 0),
            Some(Node::Unreadable(k)) => (*k, 0),
            _ => (Kind::Other, 0),
        };
        Stat { kind, link, len }
    }
}

#[cfg(test)]
#[derive(Debug, Clone)]
pub struct MemFs {
    root: PathBuf,
    nodes: std::collections::BTreeMap<String, Node>,
}

#[cfg(test)]
impl Default for MemFs {
    fn default() -> Self {
        let mut nodes = std::collections::BTreeMap::new();
        nodes.insert(String::new(), Node::Dir);
        MemFs {
            root: PathBuf::from("/mem"),
            nodes,
        }
    }
}

#[cfg(test)]
impl MemFs {
    pub fn from_files(files: impl IntoIterator<Item = (String, Vec<u8>)>) -> Self {
        files
            .into_iter()
            .fold(Self::default(), |fs, (rel, body)| fs.file(&rel, body))
    }

    #[must_use]
    pub fn file(mut self, rel: &str, body: impl AsRef<[u8]>) -> Self {
        self.insert(rel, Node::File(body.as_ref().to_vec()));
        self
    }

    #[must_use]
    pub fn dir(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Dir);
        self
    }

    #[must_use]
    pub fn link_to_file(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Link(Some(Box::new(Node::File(Vec::new())))));
        self
    }

    #[must_use]
    pub fn link_to_dir(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Link(Some(Box::new(Node::Dir))));
        self
    }

    #[must_use]
    pub fn unreadable_file(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Unreadable(Kind::File));
        self
    }

    #[must_use]
    pub fn unreadable_dir(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Unreadable(Kind::Dir));
        self
    }

    #[must_use]
    pub fn dangling_link(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Link(None));
        self
    }

    fn insert(&mut self, rel: &str, node: Node) {
        let mut parent = rel;
        while let Some((p, _)) = parent.rsplit_once('/') {
            self.nodes.entry(p.to_string()).or_insert(Node::Dir);
            parent = p;
        }
        self.nodes.insert(rel.to_string(), node);
    }

    fn node(&self, rel: &str) -> io::Result<&Node> {
        self.nodes
            .get(rel)
            .ok_or_else(|| io::Error::from(io::ErrorKind::NotFound))
    }
}

#[cfg(test)]
impl Vfs for MemFs {
    fn root(&self) -> &Path {
        &self.root
    }

    fn stat(&self, rel: &str) -> io::Result<Stat> {
        self.node(rel).map(Node::stat)
    }

    fn read(&self, rel: &str) -> io::Result<Vec<u8>> {
        match self.node(rel)? {
            Node::File(b) => Ok(b.clone()),
            Node::Link(Some(t)) => match &**t {
                Node::File(b) => Ok(b.clone()),
                _ => Err(io::ErrorKind::IsADirectory.into()),
            },
            Node::Link(None) => Err(io::ErrorKind::NotFound.into()),
            Node::Dir => Err(io::ErrorKind::IsADirectory.into()),
            Node::Unreadable(_) => Err(io::ErrorKind::PermissionDenied.into()),
        }
    }

    fn read_dir(&self, rel: &str) -> io::Result<Vec<Entry>> {
        let node = self.node(rel)?;
        if matches!(node, Node::Unreadable(_)) {
            return Err(io::ErrorKind::PermissionDenied.into());
        }
        if node.stat().kind != Kind::Dir {
            return Err(io::ErrorKind::NotADirectory.into());
        }
        let prefix = if rel.is_empty() {
            String::new()
        } else {
            format!("{rel}/")
        };
        Ok(self
            .nodes
            .range(prefix.clone()..)
            .take_while(|(k, _)| k.starts_with(&prefix))
            .filter(|(k, _)| k.len() > prefix.len() && !k[prefix.len()..].contains('/'))
            .map(|(k, n)| Entry {
                name: k[prefix.len()..].to_string(),
                utf8: true,
                stat: n.stat(),
            })
            .collect())
    }
}

/// A tree embedded in the binary by `pgmi-shared`, such as `templates/basic`.
#[derive(Debug, Clone)]
pub struct EmbeddedFs {
    prefix: String,
    root: PathBuf,
}

impl EmbeddedFs {
    pub fn new(prefix: &str) -> Self {
        EmbeddedFs {
            prefix: prefix.trim_end_matches('/').to_string(),
            root: PathBuf::from(prefix),
        }
    }

    fn key(&self, rel: &str) -> String {
        if rel.is_empty() {
            self.prefix.clone()
        } else {
            format!("{}/{rel}", self.prefix)
        }
    }

    fn children(&self, rel: &str) -> impl Iterator<Item = (&'static str, &'static [u8])> {
        let dir = format!("{}/", self.key(rel));
        let len = dir.len();
        pgmi_shared::dir(&dir)
            .map(move |(p, b)| (&p[len..], b))
            .collect::<Vec<_>>()
            .into_iter()
    }
}

impl Vfs for EmbeddedFs {
    fn root(&self) -> &Path {
        &self.root
    }

    fn stat(&self, rel: &str) -> io::Result<Stat> {
        if let Some(b) = pgmi_shared::file(&self.key(rel)) {
            return Ok(Stat {
                kind: Kind::File,
                link: false,
                len: b.len() as u64,
            });
        }
        if self.children(rel).next().is_some() {
            return Ok(Stat {
                kind: Kind::Dir,
                link: false,
                len: 0,
            });
        }
        Err(io::ErrorKind::NotFound.into())
    }

    fn read(&self, rel: &str) -> io::Result<Vec<u8>> {
        pgmi_shared::file(&self.key(rel))
            .map(<[u8]>::to_vec)
            .ok_or_else(|| {
                self.stat(rel)
                    .map_or(io::ErrorKind::NotFound, |_| io::ErrorKind::IsADirectory)
                    .into()
            })
    }

    fn read_dir(&self, rel: &str) -> io::Result<Vec<Entry>> {
        let mut out: Vec<Entry> = Vec::new();
        for (tail, body) in self.children(rel) {
            let (name, stat) = match tail.split_once('/') {
                Some((dir, _)) => (
                    dir,
                    Stat {
                        kind: Kind::Dir,
                        link: false,
                        len: 0,
                    },
                ),
                None => (
                    tail,
                    Stat {
                        kind: Kind::File,
                        link: false,
                        len: body.len() as u64,
                    },
                ),
            };
            if out.last().is_some_and(|e| e.name == name) {
                continue;
            }
            out.push(Entry {
                name: name.to_string(),
                utf8: true,
                stat,
            });
        }
        if out.is_empty() {
            self.stat(rel)?;
        }
        out.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(out)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mem_fs_lists_children_in_order_and_creates_parents() {
        let fs = MemFs::default()
            .file("b.sql", "x")
            .file("a/z.sql", "yy")
            .link_to_dir("l");
        let names: Vec<_> = fs
            .read_dir("")
            .unwrap()
            .into_iter()
            .map(|e| e.name)
            .collect();
        assert_eq!(names, ["a", "b.sql", "l"]);
        assert_eq!(fs.stat("a").unwrap().kind, Kind::Dir);
        assert_eq!(fs.stat("a/z.sql").unwrap().len, 2);
        let l = fs.stat("l").unwrap();
        assert!(l.link && l.kind == Kind::Dir);
        assert_eq!(
            fs.read("a").unwrap_err().kind(),
            io::ErrorKind::IsADirectory
        );
        assert_eq!(fs.read("nope").unwrap_err().kind(), io::ErrorKind::NotFound);
    }

    #[test]
    fn embedded_fs_reads_a_template_as_a_tree() {
        let fs = EmbeddedFs::new("templates/basic");
        let top = fs.read_dir("").unwrap();
        assert!(
            top.iter()
                .any(|e| e.name == "deploy.sql" && e.stat.kind == Kind::File)
        );
        assert!(
            top.iter()
                .any(|e| e.name == "physical" && e.stat.kind == Kind::Dir)
        );
        let names: Vec<_> = top.iter().map(|e| e.name.as_str()).collect();
        let mut sorted = names.clone();
        sorted.sort_unstable();
        sorted.dedup();
        assert_eq!(names, sorted);
        assert!(!fs.read("deploy.sql").unwrap().is_empty());
        assert_eq!(
            fs.read("physical").unwrap_err().kind(),
            io::ErrorKind::IsADirectory
        );
        assert_eq!(
            fs.stat("missing").unwrap_err().kind(),
            io::ErrorKind::NotFound
        );
    }

    #[test]
    fn os_fs_refuses_a_missing_or_file_root() {
        let e = OsFs::project(Path::new("/definitely/not/here")).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("does not exist"));
    }
}
