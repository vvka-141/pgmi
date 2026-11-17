#![allow(clippy::disallowed_methods)]

use std::fmt::Write as _;
use std::path::{Path, PathBuf};
use std::{env, fs, io};

const DIRS: [&str; 7] = [
    "ai",
    "art",
    "contract",
    "manager",
    "session",
    "skills",
    "templates",
];

fn walk(root: &Path, dir: &Path, out: &mut Vec<PathBuf>) -> io::Result<()> {
    for entry in fs::read_dir(dir)? {
        let path = entry?.path();
        if path.is_dir() {
            walk(root, &path, out)?;
        } else {
            out.push(
                path.strip_prefix(root)
                    .map_err(io::Error::other)?
                    .to_path_buf(),
            );
        }
    }
    Ok(())
}

fn main() -> io::Result<()> {
    let root = PathBuf::from(
        env::var_os("CARGO_MANIFEST_DIR").ok_or_else(|| io::Error::other("CARGO_MANIFEST_DIR"))?,
    );
    let mut files = Vec::new();
    for dir in DIRS {
        println!("cargo::rerun-if-changed={dir}");
        walk(&root, &root.join(dir), &mut files)?;
    }
    let mut names: Vec<String> = files
        .iter()
        .map(|p| {
            p.components()
                .map(|c| c.as_os_str().to_string_lossy().into_owned())
                .collect::<Vec<_>>()
                .join("/")
        })
        .collect();
    names.sort();
    let mut code = String::from("pub static FILES: &[(&str, &[u8])] = &[\n");
    for name in &names {
        let abs = root.join(name);
        writeln!(
            code,
            "    ({name:?}, include_bytes!({:?})),",
            abs.display().to_string()
        )
        .map_err(io::Error::other)?;
    }
    code.push_str("];\n");
    let out = PathBuf::from(env::var_os("OUT_DIR").ok_or_else(|| io::Error::other("OUT_DIR"))?);
    fs::write(out.join("files.rs"), code)
}
