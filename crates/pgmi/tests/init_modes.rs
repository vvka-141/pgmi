#![cfg(all(test, unix))]

use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::process::{Command, Stdio};

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
    let mut pending: Vec<PathBuf> = vec![dir.clone()];
    let mut checked = 0;
    while let Some(next) = pending.pop() {
        for entry in std::fs::read_dir(&next).unwrap() {
            let path = entry.unwrap().path();
            let meta = std::fs::symlink_metadata(&path).unwrap();
            let want = if meta.is_dir() { 0o755 } else { 0o644 };
            let mode = meta.permissions().mode() & 0o777;
            assert_eq!(mode, want, "{}: {mode:o}", path.display());
            checked += 1;
            if meta.is_dir() {
                pending.push(path);
            }
        }
    }
    assert!(checked > 10, "{checked} entries under {}", dir.display());
    let _ = std::fs::remove_dir_all(&dir);
}
