// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own gateway that fits this project.
//
// What the sign-in (login.rs) and the OAuth resource server
// (resource_server.rs) share: an issuer's discovery document, its key set
// with a small cache, and the token checks the jsonwebtoken crate leaves to
// the caller. Signature checks themselves are jsonwebtoken's: a reference
// others copy must not hand-roll cryptography.

use std::collections::HashMap;
use std::time::Duration;

use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use jsonwebtoken::jwk::Jwk;
use jsonwebtoken::{Algorithm, DecodingKey, Validation};
use serde::Deserialize;
use serde_json::{Map, Value};
use tokio::sync::Mutex;
use tokio::time::Instant;

// Standard OAuth/OIDC token verification, not pgmi-specific: replace it with
// your own stack's verifier. The same three algorithms in every gateway.
/// The asymmetric algorithms a token may be signed with. `none` and the HMAC
/// family are refused before any key is looked at: HS256 "signed" with a
/// public key is the classic forgery.
pub const ALGORITHMS: [Algorithm; 3] = [Algorithm::RS256, Algorithm::PS256, Algorithm::ES256];
pub const LEEWAY_SECONDS: u64 = 60;
/// A fetched key set is used this long before it is fetched again.
const KEYS_LIFESPAN: Duration = Duration::from_secs(300);

#[derive(Debug)]
pub enum FetchError {
    /// The provider answered with this HTTP status.
    Refused(u16),
    /// No answer, a timeout, or not the JSON expected.
    Unavailable(String),
}

pub async fn get_json<T: serde::de::DeserializeOwned>(request: reqwest::RequestBuilder) -> Result<T, FetchError> {
    let response = request
        .header(reqwest::header::ACCEPT, "application/json")
        .send()
        .await
        .map_err(|e| FetchError::Unavailable(e.to_string()))?;
    if !response.status().is_success() {
        return Err(FetchError::Refused(response.status().as_u16()));
    }
    response.json().await.map_err(|e| FetchError::Unavailable(e.to_string()))
}

#[derive(Clone, Debug, Deserialize)]
pub struct Discovery {
    pub issuer: String,
    pub authorization_endpoint: Option<String>,
    pub token_endpoint: Option<String>,
    pub jwks_uri: String,
}

/// `<issuer>/.well-known/openid-configuration`; a document naming another
/// issuer is never trusted.
pub async fn discover(http: &reqwest::Client, issuer: &str, timeout: Duration) -> Result<Discovery, FetchError> {
    let url = format!("{}/.well-known/openid-configuration", issuer.trim_end_matches('/'));
    let discovery: Discovery = get_json(http.get(url).timeout(timeout)).await?;
    if discovery.issuer != issuer {
        return Err(FetchError::Unavailable("discovery names another issuer".into()));
    }
    Ok(discovery)
}

#[derive(Debug, PartialEq, Eq)]
pub enum KeyError {
    /// The key set could not be read.
    Unavailable,
    /// No key with that id, even after the refetch the cooldown allows.
    Unknown,
}

/// An issuer's JWKS, cached for KEYS_LIFESPAN. A key id not in the cache
/// refetches the set, at most once per `cooldown`: the provider rotated its
/// key, or someone sends made-up key ids. The lock is held across the fetch,
/// so a burst of requests fetches once.
pub struct KeySet {
    http: reqwest::Client,
    jwks_uri: String,
    cooldown: Duration,
    timeout: Duration,
    cached: Mutex<Cached>,
}

#[derive(Default)]
struct Cached {
    keys: HashMap<String, DecodingKey>,
    fetched: Option<Instant>,
}

impl KeySet {
    pub fn new(http: reqwest::Client, jwks_uri: String, cooldown: Duration, timeout: Duration) -> Self {
        KeySet { http, jwks_uri, cooldown, timeout, cached: Mutex::default() }
    }

    pub async fn key(&self, kid: &str) -> Result<DecodingKey, KeyError> {
        let mut cached = self.cached.lock().await;
        let age = cached.fetched.map(|at| at.elapsed());
        let fresh = age.is_some_and(|age| age < KEYS_LIFESPAN);
        if fresh {
            if let Some(key) = cached.keys.get(kid) {
                return Ok(key.clone());
            }
            if age.is_some_and(|age| age < self.cooldown) {
                return Err(KeyError::Unknown);
            }
        }
        let set: Value = get_json(self.http.get(&self.jwks_uri).timeout(self.timeout))
            .await
            .map_err(|_| KeyError::Unavailable)?;
        cached.keys = set
            .get("keys")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .filter_map(|key| serde_json::from_value::<Jwk>(key.clone()).ok())
            .filter_map(|jwk| Some((jwk.common.key_id.clone()?, DecodingKey::from_jwk(&jwk).ok()?)))
            .collect();
        cached.fetched = Some(Instant::now());
        cached.keys.get(kid).cloned().ok_or(KeyError::Unknown)
    }
}

/// The header and claims of a token, NOT verified: only to pick the issuer,
/// the key and to refuse cheaply before any key is fetched.
pub fn peek(token: &str) -> Option<(jsonwebtoken::Header, Map<String, Value>)> {
    let header = jsonwebtoken::decode_header(token).ok()?;
    let payload = token.split('.').nth(1)?;
    let claims = serde_json::from_slice(&URL_SAFE_NO_PAD.decode(payload).ok()?).ok()?;
    Some((header, claims))
}

/// Signature, iss, exp and nbf (with leeway), and the claims every token here
/// must carry. The audience is the caller's: the two uses check it differently.
pub fn validation(algorithm: Algorithm, issuer: &str) -> Validation {
    let mut validation = Validation::new(algorithm);
    validation.leeway = LEEWAY_SECONDS;
    validation.validate_nbf = true;
    validation.validate_aud = false;
    validation.set_issuer(&[issuer]);
    validation.set_required_spec_claims(&["iss", "sub", "aud", "exp"]);
    validation
}

/// jsonwebtoken does not look at iat: it must be there, and not in the future.
pub fn check_iat(claims: &Map<String, Value>) -> Result<(), &'static str> {
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map_or(0, |d| d.as_secs());
    match claims.get("iat").and_then(Value::as_f64) {
        None => Err("iat is missing"),
        Some(iat) if iat > (now + LEEWAY_SECONDS) as f64 => Err("iat is in the future"),
        Some(_) => Ok(()),
    }
}
