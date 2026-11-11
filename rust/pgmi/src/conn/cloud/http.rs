use std::fmt::Write as _;
use std::sync::OnceLock;
use std::time::Duration;

use crate::error::{Error, Result};

pub(crate) fn client() -> Result<reqwest::Client> {
    static CLIENT: OnceLock<reqwest::Client> = OnceLock::new();
    if let Some(c) = CLIENT.get() {
        return Ok(c.clone());
    }
    let built = reqwest::Client::builder()
        .connect_timeout(Duration::from_secs(10))
        .timeout(Duration::from_secs(60))
        .build()
        .map_err(|e| Error::connection("build the HTTPS client").with_source(e))?;
    Ok(CLIENT.get_or_init(|| built).clone())
}

pub(crate) fn encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            let _ = write!(out, "%{b:02X}");
        }
    }
    out
}

#[cfg(test)]
mod tests {
    #[test]
    fn encodes_like_aws() {
        assert_eq!(
            super::encode("AKID/2026/us-west-2 x"),
            "AKID%2F2026%2Fus-west-2%20x"
        );
    }
}
