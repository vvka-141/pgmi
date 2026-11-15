//! A sign-in helper process (`az`, `databricks`, a `credential_process`)
//! that never outlives its call: on Unix it runs in its own process group,
//! and the whole group is killed when the call is dropped unfinished.

use std::process::{Output, Stdio};

/// Runs `command` with no stdin and its output captured; dropping the
/// future kills the process and, on Unix, everything it started.
pub(crate) async fn output(mut command: tokio::process::Command) -> std::io::Result<Output> {
    command
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    #[cfg(unix)]
    command.process_group(0);
    let child = command.spawn()?;
    #[cfg(unix)]
    let mut group = ProcessGroup(child.id());
    let output = child.wait_with_output().await;
    #[cfg(unix)]
    group.0.take();
    output
}

/// A child's process group, killed on drop unless the child was waited for.
#[cfg(unix)]
struct ProcessGroup(Option<u32>);

#[cfg(unix)]
impl Drop for ProcessGroup {
    fn drop(&mut self) {
        let group = self
            .0
            .and_then(|id| i32::try_from(id).ok())
            .and_then(rustix::process::Pid::from_raw);
        if let Some(group) = group {
            let _ = rustix::process::kill_process_group(group, rustix::process::Signal::KILL);
        }
    }
}
