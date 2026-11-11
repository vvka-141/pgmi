include!(concat!(env!("OUT_DIR"), "/files.rs"));

/// The embedded file at `path`, relative to `shared/` with `/` separators
/// (`templates/basic/deploy.sql`); `None` when nothing is embedded there.
pub fn file(path: &str) -> Option<&'static [u8]> {
    FILES
        .binary_search_by(|(p, _)| p.cmp(&path))
        .ok()
        .map(|i| FILES[i].1)
}

/// Every embedded file whose path starts with `prefix`, in path order. The
/// prefix is matched as text: pass `templates/basic/`, with the slash, for
/// one directory.
pub fn dir(prefix: &str) -> impl Iterator<Item = (&'static str, &'static [u8])> + '_ {
    let start = FILES.partition_point(|(p, _)| *p < prefix);
    FILES[start..]
        .iter()
        .take_while(move |(p, _)| p.starts_with(prefix))
        .copied()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;

    #[test]
    /// Runs `git ls-files`, so it needs a git checkout of the repository.
    fn embedded_files_equal_tracked_files() {
        let out = Command::new("git")
            .args([
                "ls-files",
                "-z",
                "--",
                "ai",
                "art",
                "contract",
                "manager",
                "session",
                "skills",
                "templates",
            ])
            .current_dir(env!("CARGO_MANIFEST_DIR"))
            .output()
            .unwrap();
        let mut tracked: Vec<&str> = std::str::from_utf8(&out.stdout)
            .unwrap()
            .split('\0')
            .filter(|s| !s.is_empty())
            .collect();
        tracked.sort_unstable();
        let embedded: Vec<&str> = FILES.iter().map(|(p, _)| *p).collect();
        assert_eq!(embedded, tracked);
    }

    #[test]
    fn lookups() {
        assert!(file("session/schema.sql").is_some_and(|b| !b.is_empty()));
        assert!(file("templates/basic/deploy.sql").is_some());
        assert!(dir("templates/basic/").count() > 3);
        assert!(file("templates/advanced/gateways/go/main.go").is_some());
    }
}
