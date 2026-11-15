#![cfg(all(test, target_os = "linux", feature = "testhooks"))]

use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

const HUNG_PROCESS: &str = "#!/bin/sh
echo $$ > \"$PIDS/helper\"
sleep 120 &
echo $! > \"$PIDS/sleep\"
wait
";

struct Hung {
    root: PathBuf,
    child: Child,
    started: Instant,
}

fn deploy_with_a_hung_credential_process(name: &str, args: &[&str]) -> std::io::Result<Hung> {
    let root = std::env::temp_dir().join(format!("pgmi-aws-hang-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    for dir in ["project", "pids"] {
        std::fs::create_dir_all(root.join(dir))?;
    }
    std::fs::write(root.join("project/deploy.sql"), "SELECT 1;\n")?;
    let helper = root.join("helper.sh");
    std::fs::write(&helper, HUNG_PROCESS)?;
    std::fs::set_permissions(&helper, std::fs::Permissions::from_mode(0o755))?;
    std::fs::write(
        root.join("config"),
        format!(
            "[profile hung]\ncredential_process = {}\n",
            helper.display()
        ),
    )?;
    let child = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(root.join("project"))
        .args(["--aws", "--aws-region", "us-east-1"])
        .args(args)
        .args([
            "--connection",
            "host=127.0.0.1 port=1 user=someone dbname=postgres sslmode=require",
        ])
        .args(["-d", "app"])
        .env_clear()
        .env("PATH", "/usr/bin:/bin")
        .env("HOME", &root)
        .env("PIDS", root.join("pids"))
        .env("AWS_CONFIG_FILE", root.join("config"))
        .env("AWS_PROFILE", "hung")
        .env("AWS_EC2_METADATA_DISABLED", "true")
        .env("PGMI_TEST_AWS_PROCESS_SECONDS", "2")
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
    fn pid(&self, name: &str) -> String {
        let file = self.root.join("pids").join(name);
        loop {
            let text = std::fs::read_to_string(&file).unwrap_or_default();
            if !text.trim().is_empty() {
                return text.trim().to_string();
            }
            assert!(
                self.started.elapsed() < Duration::from_secs(8),
                "the stub credential_process never started"
            );
            std::thread::sleep(Duration::from_millis(50));
        }
    }

    fn exit_within(&mut self, limit: Duration) -> std::io::Result<(Option<i32>, String, Duration)> {
        use std::io::Read;
        let code = loop {
            if let Some(status) = self.child.try_wait()? {
                break status.code();
            }
            if self.started.elapsed() > limit {
                let _ = self.child.kill();
                let _ = self.child.wait();
                break None;
            }
            std::thread::sleep(Duration::from_millis(100));
        };
        let took = self.started.elapsed();
        let mut stderr = String::new();
        if let Some(mut pipe) = self.child.stderr.take() {
            let _ = pipe.read_to_string(&mut stderr);
        }
        Ok((code, stderr, took))
    }
}

impl Drop for Hung {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.root);
    }
}

fn gone(pid: &str) -> bool {
    let deadline = Instant::now() + Duration::from_secs(3);
    loop {
        let stat = std::fs::read_to_string(Path::new("/proc").join(pid).join("stat"));
        if !stat.is_ok_and(|s| !s.contains(") Z ")) {
            return true;
        }
        if Instant::now() > deadline {
            return false;
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}

#[test]
fn a_hung_credential_process_is_ended_after_its_limit_even_with_no_time_box() {
    let mut run = deploy_with_a_hung_credential_process("limit", &["--timeout", "0"]).unwrap();
    let (helper, sleep) = (run.pid("helper"), run.pid("sleep"));
    let (code, stderr, took) = run.exit_within(Duration::from_secs(20)).unwrap();
    assert_eq!(code, Some(11), "{stderr}");
    assert!(
        stderr.contains("credential_process did not answer within 2 s and was ended"),
        "{stderr}"
    );
    assert!(took < Duration::from_secs(10), "took {took:?}: {stderr}");
    assert!(
        gone(&helper),
        "the credential_process (pid {helper}) outlived pgmi"
    );
    assert!(gone(&sleep), "its child (pid {sleep}) outlived pgmi");
}

#[test]
fn the_time_box_ends_a_hung_credential_process_with_everything_it_started() {
    let mut run = deploy_with_a_hung_credential_process("timebox", &["--timeout", "1s"]).unwrap();
    let (helper, sleep) = (run.pid("helper"), run.pid("sleep"));
    let (code, stderr, took) = run.exit_within(Duration::from_secs(20)).unwrap();
    assert!(code.is_some(), "pgmi did not exit: {stderr}");
    assert!(took < Duration::from_secs(5), "took {took:?}: {stderr}");
    assert!(
        gone(&helper),
        "the credential_process (pid {helper}) outlived pgmi"
    );
    assert!(gone(&sleep), "its child (pid {sleep}) outlived pgmi");
}
