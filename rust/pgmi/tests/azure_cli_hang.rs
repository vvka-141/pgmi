#![cfg(all(test, target_os = "linux"))]

use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

const HUNG_AZ: &str = "#!/bin/sh
echo $$ > \"$PIDS/az\"
sleep 60 &
echo $! > \"$PIDS/sleep\"
wait
";

struct Hung {
    root: PathBuf,
    child: Child,
    started: Instant,
}

fn deploy_with_a_hung_az(name: &str) -> std::io::Result<Hung> {
    let root = std::env::temp_dir().join(format!("pgmi-az-hang-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    for dir in ["project", "bin", "pids"] {
        std::fs::create_dir_all(root.join(dir))?;
    }
    std::fs::write(root.join("project/deploy.sql"), "SELECT 1;\n")?;
    let az = root.join("bin/az");
    std::fs::write(&az, HUNG_AZ)?;
    std::fs::set_permissions(&az, std::fs::Permissions::from_mode(0o755))?;
    let child = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(root.join("project"))
        .args(["--azure", "--timeout", "0"])
        .args([
            "--connection",
            "host=127.0.0.1 port=1 user=someone dbname=postgres sslmode=require",
        ])
        .args(["-d", "app"])
        .env_clear()
        .env(
            "PATH",
            format!("{}:/usr/bin:/bin", root.join("bin").display()),
        )
        .env("HOME", &root)
        .env("PIDS", root.join("pids"))
        .env("AZURE_TOKEN_CREDENTIALS", "dev")
        .env("PGMI_NO_BANNER", "1")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()?;
    Ok(Hung {
        root,
        child,
        started: Instant::now(),
    })
}

impl Hung {
    fn pid(&self, name: &str) -> std::io::Result<String> {
        let file = self.root.join("pids").join(name);
        loop {
            let text = std::fs::read_to_string(&file).unwrap_or_default();
            if !text.trim().is_empty() {
                return Ok(text.trim().to_string());
            }
            if self.started.elapsed() > Duration::from_secs(8) {
                return Err(std::io::Error::other("the stub az never started"));
            }
            std::thread::sleep(Duration::from_millis(50));
        }
    }

    fn exit_within(&mut self, limit: Duration) -> std::io::Result<(Option<i32>, String)> {
        use std::io::Read;
        let waited = Instant::now();
        let code = loop {
            if let Some(status) = self.child.try_wait()? {
                break status.code();
            }
            if waited.elapsed() > limit {
                let _ = self.child.kill();
                let _ = self.child.wait();
                break None;
            }
            std::thread::sleep(Duration::from_millis(100));
        };
        let mut stderr = String::new();
        if let Some(mut pipe) = self.child.stderr.take() {
            let _ = pipe.read_to_string(&mut stderr);
        }
        Ok((code, stderr))
    }
}

fn gone(pid: &str) -> bool {
    let deadline = Instant::now() + Duration::from_secs(3);
    loop {
        let stat = std::fs::read_to_string(Path::new("/proc").join(pid).join("stat"));
        let alive = stat.is_ok_and(|s| !s.contains(") Z "));
        if !alive {
            return true;
        }
        if Instant::now() > deadline {
            return false;
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}

#[test]
fn a_hung_az_is_abandoned_after_ten_seconds_and_killed() {
    let mut run = deploy_with_a_hung_az("abandoned").unwrap();
    let (az, sleep) = (run.pid("az").unwrap(), run.pid("sleep").unwrap());
    let (code, stderr) = run.exit_within(Duration::from_secs(25)).unwrap();
    assert_eq!(code, Some(11), "{stderr}");
    assert!(
        stderr.contains("did not answer within 10 s and was ended"),
        "{stderr}"
    );
    assert!(gone(&az), "the stub az (pid {az}) outlived pgmi");
    assert!(gone(&sleep), "az's child (pid {sleep}) outlived pgmi");
}

#[test]
fn a_signal_kills_a_hung_az() {
    let mut run = deploy_with_a_hung_az("signalled").unwrap();
    let (az, sleep) = (run.pid("az").unwrap(), run.pid("sleep").unwrap());
    std::thread::sleep(Duration::from_secs(2));
    let sent = Command::new("kill")
        .args(["-INT", &run.child.id().to_string()])
        .status()
        .expect("run kill");
    assert!(sent.success());
    let (code, stderr) = run.exit_within(Duration::from_secs(5)).unwrap();
    assert_eq!(code, Some(130), "{stderr}");
    assert!(gone(&az), "the stub az (pid {az}) outlived pgmi");
    assert!(gone(&sleep), "az's child (pid {sleep}) outlived pgmi");
}
