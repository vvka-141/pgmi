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
        .redirect(reqwest::redirect::Policy::none())
        .build()
        .map_err(|e| Error::connection("build the HTTPS client").with_source(e))?;
    Ok(CLIENT.get_or_init(|| built).clone())
}

/// How a sign-in endpoint is retried: the attempts, which answers earn
/// another one, and the wait after attempt `n` (from 1).
pub(crate) struct Retry {
    pub(crate) attempts: u32,
    pub(crate) status: fn(reqwest::StatusCode) -> bool,
    pub(crate) wait: fn(u32) -> Duration,
}

/// The most a sign-in answer may hold.
pub(crate) const ANSWER_MAX: usize = 1 << 20;

/// Why an answer could not be read.
#[derive(Debug)]
pub(crate) enum ReadError {
    Http(reqwest::Error),
    Oversized,
}

impl std::fmt::Display for ReadError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ReadError::Http(e) => f.write_str(&crate::error::chain(e)),
            ReadError::Oversized => f.write_str("the answer is larger than 1 MiB"),
        }
    }
}

impl std::error::Error for ReadError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            ReadError::Http(e) => e.source(),
            ReadError::Oversized => None,
        }
    }
}

/// The body of `r`, refused past [`ANSWER_MAX`] without reading the rest.
pub(crate) async fn read(mut r: reqwest::Response) -> std::result::Result<Vec<u8>, ReadError> {
    let mut out = Vec::new();
    while let Some(chunk) = r.chunk().await.map_err(ReadError::Http)? {
        if out.len() + chunk.len() > ANSWER_MAX {
            return Err(ReadError::Oversized);
        }
        out.extend_from_slice(&chunk);
    }
    Ok(out)
}

pub(crate) async fn read_text(r: reqwest::Response) -> std::result::Result<String, ReadError> {
    read(r)
        .await
        .map(|b| String::from_utf8_lossy(&b).into_owned())
}

/// A request that got no answer, after `attempts` tries.
#[derive(Debug)]
pub(crate) struct Unanswered {
    pub(crate) error: ReadError,
    pub(crate) attempts: u32,
}

impl std::fmt::Display for Unanswered {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&crate::error::chain(&self.error))?;
        if self.attempts > 1 {
            write!(f, " (after {} attempts)", self.attempts)?;
        }
        Ok(())
    }
}

impl std::error::Error for Unanswered {}

/// An endpoint that kept answering a status its retry names, through every
/// attempt.
#[derive(Debug)]
pub(crate) struct Exhausted {
    what: &'static str,
    status: reqwest::StatusCode,
    attempts: u32,
}

impl Exhausted {
    pub(crate) fn new(what: &'static str, status: reqwest::StatusCode, attempts: u32) -> Self {
        Exhausted {
            what,
            status,
            attempts,
        }
    }
}

impl std::fmt::Display for Exhausted {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "{} answered {} (after {} attempts)",
            self.what, self.status, self.attempts
        )
    }
}

impl std::error::Error for Exhausted {}

/// Sends the request `build` makes, again after a failed connection or an
/// answer `retry` names, and returns the last answer's status and body.
pub(crate) async fn send(
    http: &reqwest::Client,
    retry: &Retry,
    build: impl Fn(&reqwest::Client) -> reqwest::RequestBuilder,
) -> std::result::Result<(reqwest::StatusCode, String), Unanswered> {
    let mut attempt = 1;
    loop {
        let outcome = match build(http).send().await {
            Ok(r) => {
                let status = r.status();
                read_text(r).await.map(|t| (status, t))
            }
            Err(e) => Err(ReadError::Http(e)),
        };
        let again = match &outcome {
            Ok((status, _)) => (retry.status)(*status),
            Err(ReadError::Http(_)) => true,
            Err(ReadError::Oversized) => false,
        };
        if !again || attempt >= retry.attempts {
            return outcome.map_err(|error| Unanswered {
                error,
                attempts: attempt,
            });
        }
        tokio::time::sleep((retry.wait)(attempt)).await;
        attempt += 1;
    }
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
pub(crate) mod tests {
    use std::sync::Arc;
    use std::sync::atomic::{AtomicUsize, Ordering};

    use reqwest::StatusCode;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    use super::*;

    #[test]
    fn encodes_like_aws() {
        assert_eq!(
            super::encode("AKID/2026/us-west-2 x"),
            "AKID%2F2026%2Fus-west-2%20x"
        );
    }

    /// Answers one connection per status in `script`, then stops listening.
    pub(crate) async fn scripted(script: Vec<(u16, &'static str)>) -> (String, Arc<AtomicUsize>) {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let served = Arc::new(AtomicUsize::new(0));
        let count = served.clone();
        tokio::spawn(async move {
            for (status, body) in script {
                let (mut s, _) = listener.accept().await.unwrap();
                let mut buf = [0u8; 8192];
                let _ = s.read(&mut buf).await;
                count.fetch_add(1, Ordering::SeqCst);
                let reply = format!(
                    "HTTP/1.1 {status} X\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
                let _ = s.write_all(reply.as_bytes()).await;
            }
        });
        (url, served)
    }

    fn no_wait(_: u32) -> Duration {
        Duration::ZERO
    }

    fn server_error(status: StatusCode) -> bool {
        status.is_server_error()
    }

    const SERVER_ERRORS: Retry = Retry {
        attempts: 3,
        status: server_error,
        wait: no_wait,
    };

    async fn get(url: &str) -> std::result::Result<(StatusCode, String), Unanswered> {
        crate::conn::tls::install();
        send(&reqwest::Client::new(), &SERVER_ERRORS, |h| h.get(url)).await
    }

    #[tokio::test]
    async fn a_retryable_answer_is_tried_again() {
        let (url, served) = scripted(vec![(503, ""), (200, "ok")]).await;
        let (status, body) = get(&url).await.unwrap();
        assert_eq!((status.as_u16(), body.as_str()), (200, "ok"));
        assert_eq!(served.load(Ordering::SeqCst), 2);
    }

    #[tokio::test]
    async fn any_other_answer_is_final() {
        let (url, served) = scripted(vec![(400, "no"), (200, "ok")]).await;
        let (status, _) = get(&url).await.unwrap();
        assert_eq!(status.as_u16(), 400);
        assert_eq!(served.load(Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn the_last_answer_stands_after_every_attempt() {
        let (url, served) = scripted(vec![(503, ""), (502, ""), (504, "late"), (200, "")]).await;
        let (status, body) = get(&url).await.unwrap();
        assert_eq!((status.as_u16(), body.as_str()), (504, "late"));
        assert_eq!(served.load(Ordering::SeqCst), 3);
    }

    /// Answers every connection with `reply` and counts the requests.
    async fn answering(reply: String) -> (String, Arc<AtomicUsize>) {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let served = Arc::new(AtomicUsize::new(0));
        let count = served.clone();
        tokio::spawn(async move {
            while let Ok((mut s, _)) = listener.accept().await {
                let mut buf = [0u8; 8192];
                let _ = s.read(&mut buf).await;
                count.fetch_add(1, Ordering::SeqCst);
                let _ = s.write_all(reply.as_bytes()).await;
            }
        });
        (url, served)
    }

    #[tokio::test]
    async fn a_redirect_is_an_answer_not_followed() {
        let (target, reached) = scripted(vec![(200, r#"{"access_token":"stolen"}"#)]).await;
        let reply = format!(
            "HTTP/1.1 307 X\r\nLocation: {target}/token\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
        );
        let (redirecting, _) = answering(reply).await;
        crate::conn::tls::install();
        let http = client().unwrap();
        let (status, _) = send(&http, &SERVER_ERRORS, |h| h.get(&redirecting))
            .await
            .unwrap();
        assert_eq!(status.as_u16(), 307);
        assert_eq!(reached.load(Ordering::SeqCst), 0);
    }

    #[tokio::test]
    async fn an_answer_over_the_cap_is_refused_and_not_retried() {
        let body = "x".repeat(ANSWER_MAX + 1);
        let reply = format!(
            "HTTP/1.1 200 X\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
            body.len()
        );
        let (url, served) = answering(reply).await;
        let e = get(&url).await.unwrap_err();
        assert!(e.to_string().contains("larger than 1 MiB"), "{e}");
        assert_eq!(served.load(Ordering::SeqCst), 1);
        let small = "y".repeat(ANSWER_MAX);
        let reply = format!(
            "HTTP/1.1 200 X\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{small}",
            small.len()
        );
        let (url, _) = answering(reply).await;
        assert_eq!(get(&url).await.unwrap().1.len(), ANSWER_MAX);
    }

    #[tokio::test]
    async fn no_answer_is_retried_and_reports_the_attempts() {
        let port = {
            let l = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            l.local_addr().unwrap().port()
        };
        let e = get(&format!("http://127.0.0.1:{port}")).await.unwrap_err();
        assert_eq!(e.attempts, 3);
        assert!(e.to_string().ends_with("(after 3 attempts)"), "{e}");
    }
}
