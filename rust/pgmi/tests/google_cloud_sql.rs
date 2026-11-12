#![cfg(all(test, target_os = "linux"))]

use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};

struct Fakes {
    child: Child,
    values: serde_json::Value,
}

impl Drop for Fakes {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

impl Fakes {
    fn start(root: &Path) -> std::io::Result<Self> {
        let blackbox = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../blackbox");
        let mut child = Command::new("python3")
            .args(["-m", "pgmi_blackbox.fakes"])
            .arg(root)
            .current_dir(blackbox)
            .env("PYTHONDONTWRITEBYTECODE", "1")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .spawn()?;
        let mut line = String::new();
        if let Some(out) = child.stdout.take() {
            BufReader::new(out).read_line(&mut line)?;
        }
        let values = serde_json::from_str(&line)?;
        Ok(Self { child, values })
    }

    fn get(&self, key: &str) -> String {
        self.values[key].as_str().unwrap_or_default().to_string()
    }
}

struct Run {
    root: PathBuf,
    fakes: Fakes,
    stderr: String,
    code: Option<i32>,
}

fn deploy(name: &str, admin_path: &str) -> std::io::Result<Run> {
    let root = std::env::temp_dir().join(format!("pgmi-cloudsql-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    std::fs::create_dir_all(root.join("project"))?;
    std::fs::create_dir_all(root.join("home"))?;
    std::fs::write(root.join("project/deploy.sql"), "SELECT 1;\n")?;
    let fakes = Fakes::start(&root)?;
    assert!(
        !fakes.get("{google_instance}").is_empty(),
        "{}",
        fakes.values
    );
    let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
        .arg("deploy")
        .arg(root.join("project"))
        .args([
            "--google",
            "--google-instance",
            &fakes.get("{google_instance}"),
        ])
        .args(["-U", "sa@fake-project.iam", "-d", "app"])
        .env_clear()
        .env("HOME", root.join("home"))
        .env(
            "GOOGLE_APPLICATION_CREDENTIALS",
            fakes.get("{google_credentials}"),
        )
        .env(
            "CLOUDSDK_API_ENDPOINT_OVERRIDES_SQLADMIN",
            format!("{}{admin_path}", fakes.get("{fake_http}")),
        )
        .output()?;
    Ok(Run {
        root,
        fakes,
        stderr: String::from_utf8_lossy(&out.stderr).into_owned(),
        code: out.status.code(),
    })
}

impl Run {
    fn http_log(&self) -> std::io::Result<String> {
        std::fs::read_to_string(self.fakes.get("{fake_http_log}"))
    }

    fn reached_the_instance(&self) {
        assert_eq!(self.code, Some(11), "{}", self.stderr);
        assert!(
            self.stderr.contains("recorded by the black-box recorder"),
            "{}",
            self.stderr
        );
    }

    fn done(self) -> std::io::Result<()> {
        drop(self.fakes);
        std::fs::remove_dir_all(&self.root)
    }
}

#[test]
fn google_sign_in_reaches_the_instance_through_the_fake_admin_api() {
    let run = deploy("plain", "/").unwrap();
    run.reached_the_instance();
    let http = run.http_log().unwrap();
    let token = run.fakes.get("{google_token}");
    assert!(
        http.contains(&format!(
            "GET /sql/v1beta4/projects/fake-project/instances/fake-instance/connectSettings {{\"authorization\": \"Bearer {token}\""
        )),
        "{http}"
    );
    assert!(
        http.contains(
            "POST /sql/v1beta4/projects/fake-project/instances/fake-instance:generateEphemeralCert"
        ),
        "{http}"
    );
    let recorded: serde_json::Value = serde_json::from_str(
        std::fs::read_to_string(run.fakes.get("{recorder_log}"))
            .unwrap()
            .lines()
            .next()
            .unwrap(),
    )
    .unwrap();
    assert_eq!(recorded["user"], "sa@fake-project.iam");
    assert_eq!(recorded["password"], token.as_str());
    run.done().unwrap();
}

#[test]
fn admin_api_server_errors_are_asked_again_like_the_go_connector() {
    let run = deploy("flaky", "/flaky/503x2/").unwrap();
    run.reached_the_instance();
    let http = run.http_log().unwrap();
    for call in ["/connectSettings ", ":generateEphemeralCert "] {
        assert_eq!(
            http.lines().filter(|l| l.contains(call)).count(),
            3,
            "{call}: {http}"
        );
    }
    run.done().unwrap();
}
