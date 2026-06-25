# pgmi API Client — Rust

**Recommended generator:** `openapi-generator` with the `rust` target

```bash
openapi-generator generate -i http://localhost:8080/openapi.json -g rust -o pgmi-client
```

## Transport Core Skeleton

```rust
use reqwest::{Client, StatusCode};
use serde::Deserialize;

pub struct PgmiClient {
    client: Client,
    base_url: String,
    token: String,
}

#[derive(Debug, Deserialize)]
pub struct ProblemDetails {
    #[serde(rename = "type", default)]
    pub kind: String,
    pub title: String,
    pub status: u16,
    pub detail: Option<String>,
}

#[derive(Debug, thiserror::Error)]
pub enum PgmiError {
    #[error("{status} {title}: {}", detail.as_deref().unwrap_or(""))]
    Api { status: u16, title: String, detail: Option<String> },
    #[error(transparent)]
    Http(#[from] reqwest::Error),
}

impl PgmiClient {
    pub fn new(base_url: &str, token: &str) -> Self {
        Self {
            client: Client::new(),
            base_url: base_url.to_owned(),
            token: token.to_owned(),
        }
    }

    async fn request<T: serde::de::DeserializeOwned>(
        &self,
        req: reqwest::RequestBuilder,
    ) -> Result<T, PgmiError> {
        let resp = req
            .bearer_auth(&self.token)
            .header("Accept", "application/json")
            .send()
            .await?;

        if !resp.status().is_success() {
            let problem: ProblemDetails = resp.json().await?;
            return Err(PgmiError::Api {
                status: problem.status,
                title: problem.title,
                detail: problem.detail,
            });
        }
        Ok(resp.json().await?)
    }
}
```

## Anti-Copy Directive

**DO NOT copy this skeleton verbatim.** It demonstrates auth + error handling only. Generate your endpoint methods from the live `/openapi.json` spec using `openapi-generator`, then call them through the `request` method above. Do not hand-write endpoint URLs or request shapes from memory.
