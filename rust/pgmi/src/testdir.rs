use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};

pub(crate) struct TempDir(PathBuf);

impl TempDir {
    pub(crate) fn new(tag: &str) -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "pgmi-{tag}-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        let _ = std::fs::remove_dir_all(&path);
        std::fs::create_dir_all(&path).unwrap();
        TempDir(path)
    }

    pub(crate) fn path(&self) -> &Path {
        &self.0
    }

    pub(crate) fn join(&self, p: impl AsRef<Path>) -> PathBuf {
        self.0.join(p)
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn removed_when_dropped_even_after_a_failed_assert() {
        let kept = std::panic::catch_unwind(|| {
            let d = TempDir::new("drop");
            std::fs::write(d.join("f"), "x").unwrap();
            let path = d.path().to_path_buf();
            assert!(path.join("f").is_file());
            panic!("{}", path.display());
        })
        .unwrap_err();
        let path = kept.downcast_ref::<String>().unwrap();
        assert!(!Path::new(path).exists(), "{path}");
    }
}
