mod assertions;
mod contract;
mod gateways;
mod leak;
mod skills;
mod snippets;
mod templates;

use crate::templates::Template;
use crate::vfs::{self, EmbeddedFs, MemFs, Vfs};

fn tree(prefix: &str) -> Vec<(String, String)> {
    let files = vfs::files(&EmbeddedFs::new(prefix)).unwrap();
    assert!(!files.is_empty(), "nothing embedded under {prefix}");
    files
        .into_iter()
        .map(|(p, b)| {
            (
                format!("{prefix}/{p}"),
                String::from_utf8_lossy(&b).into_owned(),
            )
        })
        .collect()
}

fn embedded(path: &str) -> String {
    String::from_utf8(
        pgmi_shared::file(path)
            .unwrap_or_else(|| panic!("{path} is not embedded"))
            .to_vec(),
    )
    .unwrap()
}

fn scaffold(t: Template) -> MemFs {
    MemFs::from_files(t.render("proj", crate::VERSION).unwrap())
}

fn read(fs: &impl Vfs, rel: &str) -> String {
    fs.read_to_string(rel)
        .unwrap_or_else(|e| panic!("{rel}: {e}"))
}

fn repo_file(rel: &str) -> String {
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .join(rel);
    std::fs::read_to_string(&path)
        .unwrap_or_else(|e| panic!("{}: {e}", path.display()))
        .replace("\r\n", "\n")
}

fn line_of(text: &str, at: usize) -> usize {
    text[..at].matches('\n').count() + 1
}
