use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};

pub struct Fakes {
    child: Child,
    pub values: serde_json::Value,
}

impl Drop for Fakes {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

impl Fakes {
    pub fn start(root: &Path) -> std::io::Result<Self> {
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

    pub fn get(&self, key: &str) -> String {
        self.values[key].as_str().unwrap_or_default().to_string()
    }
}
