//! Read-only view of a project tree, so loading, scanning and `.pgmiignore`
//! work the same over the disk, embedded templates and in-memory test trees.
//!
//! Paths are relative to the tree's root, `/`-separated, and `""` is the
//! root itself.

use std::io::{self, Read};
use std::path::{Path, PathBuf};

use crate::error::{Error, Result};

/// Why a named pipe, socket or device in a project is never read.
pub(crate) const NOT_REGULAR: &str = "not a regular file (a named pipe, socket or device)";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum FileKind {
    File,
    Dir,
    Other,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Stat {
    pub(crate) kind: FileKind,
    pub(crate) link: bool,
    pub(crate) len: u64,
}

/// A directory entry's name. `Lossy` is a name that is not valid UTF-8,
/// shown with U+FFFD in place of the bad bytes.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Name {
    Utf8(String),
    Lossy(String),
}

impl Name {
    pub(crate) fn as_str(&self) -> &str {
        match self {
            Name::Utf8(n) | Name::Lossy(n) => n,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Entry {
    pub(crate) name: Name,
    pub(crate) stat: Stat,
}

pub(crate) trait Vfs {
    fn root(&self) -> &Path;
    fn stat(&self, rel: &str) -> io::Result<Stat>;
    fn read(&self, rel: &str) -> io::Result<Vec<u8>>;
    fn read_dir(&self, rel: &str) -> io::Result<Vec<Entry>>;

    /// Reads at most `limit` + 1 bytes, so a caller learns that a file is
    /// over its limit without holding all of it.
    fn read_bounded(&self, rel: &str, limit: u64) -> io::Result<Vec<u8>> {
        let mut bytes = self.read(rel)?;
        bytes.truncate(usize::try_from(limit.saturating_add(1)).unwrap_or(usize::MAX));
        Ok(bytes)
    }

    /// Reads a file as UTF-8 text, failing as `std::fs::read_to_string` does.
    fn read_to_string(&self, rel: &str) -> io::Result<String> {
        String::from_utf8(self.read(rel)?).map_err(|_| {
            io::Error::new(
                io::ErrorKind::InvalidData,
                "stream did not contain valid UTF-8",
            )
        })
    }

    fn display(&self, rel: &str) -> String {
        if rel.is_empty() {
            self.root().display().to_string()
        } else {
            self.root().join(rel).display().to_string()
        }
    }
}

#[derive(Debug, Clone)]
pub(crate) struct OsFs {
    root: PathBuf,
}

impl OsFs {
    pub(crate) fn new(root: impl Into<PathBuf>) -> Self {
        OsFs { root: root.into() }
    }

    pub(crate) fn project(root: &Path) -> Result<Self> {
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
        Some(m) if m.is_dir() => FileKind::Dir,
        Some(m) if m.is_file() => FileKind::File,
        _ => FileKind::Other,
    };
    Ok(Stat {
        kind,
        link,
        len: target.map_or(0, |m| m.len()),
    })
}

fn open_regular(path: &Path) -> io::Result<std::fs::File> {
    let mut options = std::fs::OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    std::os::unix::fs::OpenOptionsExt::custom_flags(&mut options, libc::O_NONBLOCK);
    let file = options.open(path)?;
    let meta = file.metadata()?;
    if meta.is_dir() {
        return Err(io::ErrorKind::IsADirectory.into());
    }
    if !meta.is_file() {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, NOT_REGULAR));
    }
    Ok(file)
}

impl Vfs for OsFs {
    fn root(&self) -> &Path {
        &self.root
    }

    fn stat(&self, rel: &str) -> io::Result<Stat> {
        os_stat(&self.path(rel))
    }

    fn read(&self, rel: &str) -> io::Result<Vec<u8>> {
        let mut out = Vec::new();
        open_regular(&self.path(rel))?.read_to_end(&mut out)?;
        Ok(out)
    }

    fn read_bounded(&self, rel: &str, limit: u64) -> io::Result<Vec<u8>> {
        let mut out = Vec::new();
        open_regular(&self.path(rel))?
            .take(limit.saturating_add(1))
            .read_to_end(&mut out)?;
        Ok(out)
    }

    fn read_dir(&self, rel: &str) -> io::Result<Vec<Entry>> {
        let mut out = Vec::new();
        for entry in std::fs::read_dir(self.path(rel))? {
            let entry = entry?;
            let stat = os_stat(&entry.path())?;
            let name = match entry.file_name().into_string() {
                Ok(n) => Name::Utf8(n),
                Err(n) => Name::Lossy(n.to_string_lossy().into_owned()),
            };
            out.push(Entry { name, stat });
        }
        out.sort_by(|a, b| a.name.as_str().cmp(b.name.as_str()));
        Ok(out)
    }
}

pub(crate) fn files(fs: &impl Vfs) -> io::Result<Vec<(String, Vec<u8>)>> {
    fn walk(fs: &impl Vfs, rel: &str, out: &mut Vec<(String, Vec<u8>)>) -> io::Result<()> {
        for entry in fs.read_dir(rel)? {
            let name = entry.name.as_str();
            let child = if rel.is_empty() {
                name.to_string()
            } else {
                format!("{rel}/{name}")
            };
            if entry.stat.kind == FileKind::Dir {
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
    Unreadable(FileKind),
}

#[cfg(test)]
impl Node {
    fn stat(&self) -> Stat {
        let (target, link) = match self {
            Node::Link(t) => (t.as_deref(), true),
            n => (Some(n), false),
        };
        let (kind, len) = match target {
            Some(Node::File(b)) => (FileKind::File, b.len() as u64),
            Some(Node::Dir) => (FileKind::Dir, 0),
            Some(Node::Unreadable(k)) => (*k, 0),
            _ => (FileKind::Other, 0),
        };
        Stat { kind, link, len }
    }
}

#[cfg(test)]
#[derive(Debug, Clone)]
pub(crate) struct MemFs {
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
    pub(crate) fn from_files(files: impl IntoIterator<Item = (String, Vec<u8>)>) -> Self {
        files
            .into_iter()
            .fold(Self::default(), |fs, (rel, body)| fs.file(&rel, body))
    }

    #[must_use]
    pub(crate) fn file(mut self, rel: &str, body: impl AsRef<[u8]>) -> Self {
        self.insert(rel, Node::File(body.as_ref().to_vec()));
        self
    }

    #[must_use]
    pub(crate) fn dir(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Dir);
        self
    }

    #[must_use]
    pub(crate) fn link_to_file(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Link(Some(Box::new(Node::File(Vec::new())))));
        self
    }

    #[must_use]
    pub(crate) fn link_to_dir(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Link(Some(Box::new(Node::Dir))));
        self
    }

    #[must_use]
    pub(crate) fn unreadable_file(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Unreadable(FileKind::File));
        self
    }

    #[must_use]
    pub(crate) fn unreadable_dir(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Unreadable(FileKind::Dir));
        self
    }

    #[must_use]
    pub(crate) fn special(mut self, rel: &str) -> Self {
        self.insert(rel, Node::Unreadable(FileKind::Other));
        self
    }

    #[must_use]
    pub(crate) fn dangling_link(mut self, rel: &str) -> Self {
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
        if node.stat().kind != FileKind::Dir {
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
                name: Name::Utf8(k[prefix.len()..].to_string()),
                stat: n.stat(),
            })
            .collect())
    }
}

/// A tree embedded in the binary by `pgmi-shared`, such as `templates/basic`.
#[derive(Debug, Clone)]
pub(crate) struct EmbeddedFs {
    prefix: String,
    root: PathBuf,
}

impl EmbeddedFs {
    pub(crate) fn new(prefix: &str) -> Self {
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
                kind: FileKind::File,
                link: false,
                len: b.len() as u64,
            });
        }
        if self.children(rel).next().is_some() {
            return Ok(Stat {
                kind: FileKind::Dir,
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
                        kind: FileKind::Dir,
                        link: false,
                        len: 0,
                    },
                ),
                None => (
                    tail,
                    Stat {
                        kind: FileKind::File,
                        link: false,
                        len: body.len() as u64,
                    },
                ),
            };
            if out.last().is_some_and(|e| e.name.as_str() == name) {
                continue;
            }
            out.push(Entry {
                name: Name::Utf8(name.to_string()),
                stat,
            });
        }
        if out.is_empty() {
            self.stat(rel)?;
        }
        out.sort_by(|a, b| a.name.as_str().cmp(b.name.as_str()));
        Ok(out)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_bounded_read_stops_one_byte_past_the_limit() {
        let dir = crate::testdir::TempDir::new("vfs-bounded");
        std::fs::write(dir.join("a.sql"), "0123456789").unwrap();
        let fs = OsFs::new(dir.path());
        assert_eq!(fs.read_bounded("a.sql", 4).unwrap(), b"01234");
        assert_eq!(fs.read_bounded("a.sql", 10).unwrap(), b"0123456789");
        let mem = MemFs::default().file("a.sql", "0123456789");
        assert_eq!(mem.read_bounded("a.sql", 4).unwrap(), b"01234");
    }

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
        assert_eq!(
            names,
            ["a", "b.sql", "l"].map(|n| Name::Utf8(n.to_string()))
        );
        assert_eq!(fs.stat("a").unwrap().kind, FileKind::Dir);
        assert_eq!(fs.stat("a/z.sql").unwrap().len, 2);
        let l = fs.stat("l").unwrap();
        assert!(l.link && l.kind == FileKind::Dir);
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
                .any(|e| e.name.as_str() == "deploy.sql" && e.stat.kind == FileKind::File)
        );
        assert!(
            top.iter()
                .any(|e| e.name.as_str() == "physical" && e.stat.kind == FileKind::Dir)
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

    #[cfg(unix)]
    #[test]
    fn os_fs_refuses_a_named_pipe_instead_of_waiting_for_a_writer() {
        let dir = crate::testdir::TempDir::new("fifo");
        let made = std::process::Command::new("mkfifo")
            .arg(dir.join("pipe.sql"))
            .status()
            .unwrap();
        assert!(made.success());
        let fs = OsFs::new(dir.path());
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || tx.send(fs.read("pipe.sql")));
        let read = rx.recv_timeout(std::time::Duration::from_secs(5));
        let e = read
            .expect("reading a named pipe must not wait")
            .unwrap_err();
        assert_eq!(e.kind(), io::ErrorKind::InvalidInput);
        assert_eq!(e.to_string(), NOT_REGULAR);
    }

    #[test]
    fn os_fs_refuses_a_missing_or_file_root() {
        let missing = Path::new("/definitely/not/here");
        let e = OsFs::project(missing).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert_eq!(
            e.text(),
            "project path \"/definitely/not/here\" does not exist"
        );
        let file = Path::new(env!("CARGO_MANIFEST_DIR")).join("Cargo.toml");
        let e = OsFs::project(&file).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert_eq!(
            e.text(),
            format!(
                "\"{}\" is a file; pass the project directory",
                file.display()
            )
        );
    }
}
