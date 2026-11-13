#![cfg(all(test, unix))]

use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::process::{Command, Stdio};

fn modes(root: &Path, out: &mut Vec<(String, u32)>) {
    for entry in std::fs::read_dir(root).unwrap() {
        let path = entry.unwrap().path();
        let meta = std::fs::symlink_metadata(&path).unwrap();
        out.push((
            path.display().to_string(),
            meta.permissions().mode() & 0o777,
        ));
        if meta.is_dir() {
            modes(&path, out);
        }
    }
}

#[test]
fn a_scaffold_under_umask_zero_is_0644_and_0755() {
    let dir = std::env::temp_dir().join(format!("pgmi-init-modes-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    let project = dir.join("p");
    let status = Command::new("sh")
        .arg("-c")
        .arg("umask 000; exec \"$0\" init \"$1\" -t advanced")
        .arg(env!("CARGO_BIN_EXE_pgmi"))
        .arg(&project)
        .env("PGMI_NON_INTERACTIVE", "1")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .status()
        .expect("run pgmi init");
    assert!(status.success());
    let mut found = Vec::new();
    modes(&dir, &mut found);
    assert!(found.len() > 10, "{found:?}");
    for (path, mode) in found {
        let want = if Path::new(&path).is_dir() {
            0o755
        } else {
            0o644
        };
        assert_eq!(mode, want, "{path}: {mode:o}");
    }
    let _ = std::fs::remove_dir_all(&dir);
}
