// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own gateway that fits this project. The rules it keeps are in
// ../../PROTOCOL.md ("MCP clients with OAuth", "Rate limits").
//
// The OAuth 2.1 resource server in front of /mcp, and the rate limits. An MCP
// client signs in at the identity provider and sends
// `Authorization: Bearer <access token>` on every request. The token is
// checked here, against the provider's published keys, and the database is
// handed only what it proved: the issuer, the subject and the client id. The
// token itself is never forwarded and never logged. The database decides who
// that is and whether the client may act yet.
//
//     GET /.well-known/oauth-protected-resource[/mcp]   metadata (RFC 9728)
//     POST /mcp without a valid token                    401 with
//         WWW-Authenticate: Bearer resource_metadata="..."

use std::collections::HashMap;
use std::hash::Hash;
use std::net::{IpAddr, Ipv4Addr, SocketAddr};
use std::num::NonZeroUsize;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::Json;
use axum::extract::{ConnectInfo, FromRequestParts, Request, State};
use axum::http::request::Parts;
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use lru::LruCache;
use serde_json::{Map, Value, json};
use tokio::sync::OnceCell;
use tokio::time::Instant;

use crate::Gateway;
use crate::error::GatewayError;
use crate::oidc::{self, KeyError, KeySet};

const MAX_TOKEN_LENGTH: usize = 8192;
const PROVIDER_TIMEOUT: Duration = Duration::from_secs(5);
/// Unlike the sign-in, these tokens come from any client: a token naming an
/// unknown key refetches the key set at most once per this long per issuer,
/// so a flood of made-up key ids cannot flood the provider. A rotated key is
/// picked up within this long.
const UNKNOWN_KEY_COOLDOWN: Duration = Duration::from_secs(10);

/// What a valid access token proves, and all the database receives.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct Identity {
    pub issuer: String,
    pub subject: String,
    pub client_id: String,
}

impl Identity {
    /// The MCP context api.mcp_handle_request resolves (and nothing else).
    pub fn context(&self) -> Value {
        json!({"client": {"issuer": self.issuer, "subject": self.subject, "clientId": self.client_id}})
    }
}

#[derive(Debug)]
pub enum TokenError {
    /// 401 without an error code: RFC 6750 §3.1 sends none to a request
    /// without credentials.
    Missing,
    /// 401 error="invalid_token". The reason goes to the log, never the token.
    Invalid(&'static str),
    /// 503: the identity provider's discovery or keys could not be read.
    ProviderUnavailable,
}

pub fn is_loopback(host: &str) -> bool {
    host == "localhost" || host.trim_matches(['[', ']']).parse::<IpAddr>().is_ok_and(|ip| ip.is_loopback())
}

/// https, or http on a loopback host.
pub fn https_or_loopback(value: &str) -> bool {
    reqwest::Url::parse(value).is_ok_and(|url| {
        url.scheme() == "https" || (url.scheme() == "http" && url.host_str().is_some_and(is_loopback))
    })
}

fn checked_url(value: &str, name: &str) -> Result<String, String> {
    let url = reqwest::Url::parse(value).map_err(|_| format!("{name} is not a URL: {value}"))?;
    if !https_or_loopback(value) {
        return Err(format!("{name} must be https (http only on a loopback host): {value}"));
    }
    if url.query().is_some() || url.fragment().is_some() {
        return Err(format!("{name} must have no query and no fragment: {value}"));
    }
    Ok(value.to_owned())
}

/// Scheme and host lower-cased, the rest as written: the MCP spec asks
/// servers to accept an upper-case scheme or host in an audience.
pub fn canonical(uri: &str) -> String {
    let Some((scheme, rest)) = uri.split_once("://") else { return uri.to_owned() };
    let (host, path) = rest.find('/').map_or((rest, ""), |i| rest.split_at(i));
    format!("{}://{}{path}", scheme.to_lowercase(), host.to_lowercase())
}

/// One authorization server: discovery once, then its keys, cached.
struct Issuer {
    issuer: String,
    keys: OnceCell<KeySet>,
}

pub struct ResourceServer {
    pub resource: String,
    issuers: HashMap<String, Issuer>,
    scopes: Vec<String>,
    refused_clients: Vec<String>,
    pub metadata_paths: Vec<String>,
    pub metadata_url: String,
    http: reqwest::Client,
}

impl ResourceServer {
    pub fn new(resource: &str, authorization_servers: &[String], scopes: Vec<String>, login_client_id: Option<&str>,
               http: reqwest::Client) -> Result<Self, String> {
        let resource = checked_url(resource, "MCP_RESOURCE")?;
        if authorization_servers.is_empty() {
            return Err("MCP_RESOURCE is set but MCP_AUTHORIZATION_SERVERS is not".into());
        }
        let mut issuers = HashMap::new();
        for server in authorization_servers {
            let issuer = checked_url(server, "MCP_AUTHORIZATION_SERVERS")?;
            issuers.insert(issuer.clone(), Issuer { issuer, keys: OnceCell::new() });
        }
        let url = reqwest::Url::parse(&resource).map_err(|e| e.to_string())?;
        let path = url.path().trim_end_matches('/');
        let metadata_url = format!("{}/.well-known/oauth-protected-resource{path}", url.origin().ascii_serialization());
        Ok(ResourceServer {
            metadata_paths: vec!["/.well-known/oauth-protected-resource".into(), format!("/.well-known/oauth-protected-resource{path}")],
            resource,
            issuers,
            scopes,
            // ADAPT: every client id that is not an MCP client. A token issued
            // to the web sign-in is a person's browser session, never an agent.
            refused_clients: login_client_id.map(str::to_owned).into_iter().collect(),
            metadata_url,
            http,
        })
    }

    /// RFC 9728 §2. The MCP spec requires authorization_servers.
    pub fn metadata(&self) -> Value {
        let mut servers: Vec<&String> = self.issuers.keys().collect();
        servers.sort();
        json!({
            "resource": self.resource,
            "authorization_servers": servers,
            "bearer_methods_supported": ["header"],
            "scopes_supported": self.scopes,
        })
    }

    pub fn challenge(&self, error: Option<&str>) -> String {
        let mut value = format!(r#"Bearer resource_metadata="{}""#, self.metadata_url);
        if !self.scopes.is_empty() {
            value.push_str(&format!(r#", scope="{}""#, self.scopes.join(" ")));
        }
        if let Some(error) = error {
            value.push_str(&format!(r#", error="{error}""#));
        }
        value
    }

    fn is_audience(&self, audience: Option<&Value>) -> bool {
        let audience = match audience {
            Some(Value::Array(values)) if values.len() == 1 => values[0].as_str(),
            Some(value) => value.as_str(),
            None => None,
        };
        audience.is_some_and(|a| canonical(a) == canonical(&self.resource))
    }

    async fn keys<'a>(&'a self, issuer: &'a Issuer) -> Result<&'a KeySet, TokenError> {
        issuer
            .keys
            .get_or_try_init(|| async {
                let discovery = oidc::discover(&self.http, &issuer.issuer, PROVIDER_TIMEOUT).await
                    .map_err(|_| TokenError::ProviderUnavailable)?;
                Ok(KeySet::new(self.http.clone(), discovery.jwks_uri, UNKNOWN_KEY_COOLDOWN, PROVIDER_TIMEOUT))
            })
            .await
    }

    // Standard OAuth/OIDC token verification, not pgmi-specific: replace it with
    // your own stack's verifier.
    /// Cheap checks first: a token that is not a JWT, names an issuer not
    /// configured or another audience is refused before any key is fetched.
    pub async fn verify(&self, authorization: Option<&str>) -> Result<Identity, TokenError> {
        let authorization = authorization.ok_or(TokenError::Missing)?;
        let (scheme, token) = authorization.split_once(' ').unwrap_or((authorization, ""));
        let token = token.trim();
        if !scheme.eq_ignore_ascii_case("bearer") || token.is_empty() || token.len() > MAX_TOKEN_LENGTH {
            return Err(TokenError::Invalid("not a bearer token"));
        }
        let (header, unverified) = oidc::peek(token).ok_or(TokenError::Invalid("not a JWT"))?;
        let issuer = unverified
            .get("iss")
            .and_then(Value::as_str)
            .and_then(|iss| self.issuers.get(iss))
            .ok_or(TokenError::Invalid("issuer not accepted"))?;
        if !oidc::ALGORITHMS.contains(&header.alg) {
            return Err(TokenError::Invalid("algorithm not accepted"));
        }
        let kid = header.kid.as_deref().ok_or(TokenError::Invalid("no key id"))?;
        if !self.is_audience(unverified.get("aud")) {
            return Err(TokenError::Invalid("audience is not this resource"));
        }

        let key = match self.keys(issuer).await?.key(kid).await {
            Ok(key) => key,
            Err(KeyError::Unknown) => return Err(TokenError::Invalid("unknown key")),
            Err(KeyError::Unavailable) => return Err(TokenError::ProviderUnavailable),
        };
        let claims = jsonwebtoken::decode::<Map<String, Value>>(token, &key, &oidc::validation(header.alg, &issuer.issuer))
            .map_err(|_| TokenError::Invalid("signature or claims"))?
            .claims;
        oidc::check_iat(&claims).map_err(TokenError::Invalid)?;
        // RFC 8707: issued for this resource and only this one. A token that
        // also names another audience could be replayed here by that server.
        if !self.is_audience(claims.get("aud")) {
            return Err(TokenError::Invalid("audience is not this resource"));
        }
        let client_id = claims
            .get("client_id")
            .or_else(|| claims.get("azp"))
            .and_then(Value::as_str)
            .filter(|id| !id.is_empty() && !self.refused_clients.iter().any(|refused| refused == id))
            .ok_or(TokenError::Invalid("no MCP client id"))?;
        let subject = claims.get("sub").and_then(Value::as_str).filter(|s| !s.is_empty())
            .ok_or(TokenError::Invalid("sub is not a string"))?;
        Ok(Identity { issuer: issuer.issuer.clone(), subject: subject.to_owned(), client_id: client_id.to_owned() })
    }
}

pub async fn metadata(State(gateway): State<Arc<Gateway>>) -> Response {
    match &gateway.resource {
        Some(resource) => Json(resource.metadata()).into_response(),
        None => GatewayError::from(crate::error::Problem::not_found()).into_response(),
    }
}

/// The MCP context of the caller: `{"client": ...}` from a verified token,
/// `{"connection": handle}` from `Authorization: Bearer cn_...` or, with no
/// other Authorization, X-Connection, or None (anonymous). An axum
/// extractor, so the MCP handler receives a caller that is already checked.
///
/// With MCP_RESOURCE set, any other bearer token is verified, and a request
/// with neither is 401, so a client learns where to get a token. The handle
/// is the database's to resolve: no header ever names a person or an
/// organization.
pub struct McpCaller(pub Option<Value>);

/// A connection handle sent as `Authorization: Bearer cn_...`. A cn_ handle
/// is not an OAuth token and never reaches the verifier.
pub fn bearer_handle(authorization: &str) -> Option<&str> {
    let (scheme, credentials) = authorization.split_once(' ')?;
    let credentials = credentials.trim();
    (scheme.eq_ignore_ascii_case("bearer") && credentials.starts_with("cn_")).then_some(credentials)
}

impl FromRequestParts<Arc<Gateway>> for McpCaller {
    type Rejection = GatewayError;

    async fn from_request_parts(parts: &mut Parts, gateway: &Arc<Gateway>) -> Result<Self, Self::Rejection> {
        let header = |name: &str| parts.headers.get(name).and_then(|v| v.to_str().ok());
        let authorization = header("authorization");
        let handle = authorization.and_then(bearer_handle).or_else(|| {
            (gateway.resource.is_none() || authorization.is_none()).then(|| header("x-connection")).flatten()
        }).filter(|h| !h.is_empty());
        let Some(resource) = &gateway.resource else {
            return Ok(McpCaller(handle.map(|h| json!({"connection": h}))));
        };
        if let Some(handle) = handle {
            return Ok(McpCaller(Some(json!({"connection": handle}))));
        }
        let identity = match resource.verify(authorization).await {
            Ok(identity) => identity,
            Err(TokenError::Missing) => return Err(GatewayError::Unauthorized { challenge: resource.challenge(None) }),
            Err(TokenError::Invalid(reason)) => {
                tracing::info!(reason, "token refused");
                return Err(GatewayError::Unauthorized { challenge: resource.challenge(Some("invalid_token")) });
            }
            Err(TokenError::ProviderUnavailable) => {
                tracing::warn!("identity provider unavailable");
                return Err(GatewayError::IdentityProviderUnavailable);
            }
        };
        gateway.per_client.allow(identity.clone())?;
        Ok(McpCaller(Some(identity.context())))
    }
}

// -- rate limits ------------------------------------------------------------------

// Standard web plumbing, not pgmi-specific: replace it with your framework's own.
/// A token bucket per key, refilled continuously to `per_minute` a minute.
///
/// Per process: N gateway processes allow N times the limit. Thread-safe
/// behind one std Mutex, never held across an await; memory is bounded by an
/// LRU of MAX_KEYS, the least recently seen key going first (a key that comes
/// back starts full, which only an attacker holding more than MAX_KEYS
/// addresses gains from). 0 turns the limit off.
pub struct RateLimiter<K: Hash + Eq> {
    per_minute: u32,
    buckets: Mutex<LruCache<K, (f64, Instant)>>,
}

impl<K: Hash + Eq> RateLimiter<K> {
    pub const MAX_KEYS: usize = 10_000;

    pub fn new(per_minute: u32) -> Self {
        Self::with_capacity(per_minute, Self::MAX_KEYS)
    }

    fn with_capacity(per_minute: u32, max_keys: usize) -> Self {
        let capacity = NonZeroUsize::new(max_keys).expect("at least one key");
        RateLimiter { per_minute, buckets: Mutex::new(LruCache::new(capacity)) }
    }

    /// Ok, or Err(RateLimited) with the seconds until the next request is allowed.
    pub fn allow(&self, key: K) -> Result<(), GatewayError> {
        if self.per_minute == 0 {
            return Ok(());
        }
        let (full, rate) = (f64::from(self.per_minute), f64::from(self.per_minute) / 60.0);
        let now = Instant::now();
        let mut buckets = self.buckets.lock().expect("never poisoned: no code panics under it");
        let (tokens, seen) = buckets.get(&key).copied().unwrap_or((full, now));
        let tokens = full.min(tokens + now.duration_since(seen).as_secs_f64() * rate);
        let allowed = tokens >= 1.0;
        buckets.put(key, (if allowed { tokens - 1.0 } else { tokens }, now));
        if allowed {
            Ok(())
        } else {
            Err(GatewayError::RateLimited { retry_after: ((1.0 - tokens) / rate).ceil().max(1.0) as u64 })
        }
    }

    #[cfg(test)]
    fn len(&self) -> usize {
        self.buckets.lock().unwrap().len()
    }
}

/// Counted first on /mcp and /auth, before anything costs: a flood of forged
/// tokens stops here and never reaches the identity provider, and a flood of
/// /auth/login cannot push real sign-ins out of the pending store.
///
/// ADAPT: behind a reverse proxy the peer address is the proxy's; key on the
/// address your proxy forwards, from a header only it can set, or limit at
/// the proxy and set RATE_LIMIT_PER_IP=0 here.
pub async fn limit_per_ip(State(gateway): State<Arc<Gateway>>, request: Request, next: Next) -> Response {
    let peer = request
        .extensions()
        .get::<ConnectInfo<SocketAddr>>()
        .map_or(IpAddr::V4(Ipv4Addr::UNSPECIFIED), |info| info.0.ip());
    match gateway.per_ip.allow(peer) {
        Ok(()) => next.run(request).await,
        Err(refused) => refused.into_response(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn server() -> ResourceServer {
        ResourceServer::new("https://MCP.example.com/mcp", &["https://login.example.com".into()], vec!["openid".into()],
                            Some("web"), reqwest::Client::new()).unwrap()
    }

    #[test]
    fn metadata_and_challenge() {
        let server = server();
        assert_eq!(server.metadata()["resource"], "https://MCP.example.com/mcp");
        assert_eq!(server.metadata_url, "https://mcp.example.com/.well-known/oauth-protected-resource/mcp");
        assert_eq!(server.challenge(Some("invalid_token")),
                   r#"Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp", scope="openid", error="invalid_token""#);
    }

    #[test]
    fn a_bearer_cn_value_is_a_connection_handle() {
        assert_eq!(bearer_handle("Bearer cn_abc"), Some("cn_abc"));
        assert_eq!(bearer_handle("bearer  cn_abc "), Some("cn_abc"));
        assert_eq!(bearer_handle("Bearer eyJhbGciOi"), None);
        assert_eq!(bearer_handle("Basic cn_abc"), None);
        assert_eq!(bearer_handle(""), None);
    }

    #[test]
    fn configuration_is_checked() {
        let http = reqwest::Client::new();
        assert!(ResourceServer::new("http://mcp.example.com/mcp", &["https://a".into()], vec![], None, http.clone()).is_err());
        assert!(ResourceServer::new("https://mcp.example.com/mcp?x=1", &["https://a".into()], vec![], None, http.clone()).is_err());
        assert!(ResourceServer::new("https://mcp.example.com/mcp", &[], vec![], None, http.clone()).is_err());
        assert!(ResourceServer::new("http://127.0.0.1:8080/mcp", &["http://localhost:9".into()], vec![], None, http).is_ok());
    }

    #[test]
    fn the_audience_is_one_and_compared_case_free_in_scheme_and_host() {
        let server = server();
        assert!(server.is_audience(Some(&json!("HTTPS://mcp.EXAMPLE.com/mcp"))));
        assert!(server.is_audience(Some(&json!(["https://mcp.example.com/mcp"]))));
        assert!(!server.is_audience(Some(&json!("https://mcp.example.com/MCP"))));
        assert!(!server.is_audience(Some(&json!(["https://mcp.example.com/mcp", "https://other"]))));
        assert!(!server.is_audience(None));
    }

    #[tokio::test]
    async fn cheap_refusals_never_fetch_a_key() {
        // login.example.com is never resolved: each of these is refused first.
        let server = server();
        let forge = |claims: Value| {
            let key = jsonwebtoken::EncodingKey::from_secret(b"k");
            let header = jsonwebtoken::Header { kid: Some("k1".into()), ..jsonwebtoken::Header::new(jsonwebtoken::Algorithm::HS256) };
            jsonwebtoken::encode(&header, &claims, &key).unwrap()
        };
        let aud = "https://mcp.example.com/mcp";
        for (token, reason) in [
            ("not-a-jwt".to_owned(), "not a JWT"),
            (forge(json!({"iss": "https://other.example", "aud": aud})), "issuer not accepted"),
            (forge(json!({"iss": "https://login.example.com", "aud": aud})), "algorithm not accepted"),
        ] {
            match server.verify(Some(&format!("Bearer {token}"))).await {
                Err(TokenError::Invalid(r)) => assert_eq!(r, reason),
                other => panic!("{reason}: {other:?}"),
            }
        }
        assert!(matches!(server.verify(None).await, Err(TokenError::Missing)));
        assert!(matches!(server.verify(Some("Basic abc")).await, Err(TokenError::Invalid(_))));
    }

    #[test]
    fn a_burst_then_retry_after_then_zero_is_off() {
        let limiter = RateLimiter::new(2);
        assert!(limiter.allow("a").is_ok());
        assert!(limiter.allow("a").is_ok());
        assert!(matches!(limiter.allow("a"), Err(GatewayError::RateLimited { retry_after: 30 })));
        assert!(limiter.allow("b").is_ok());
        let off = RateLimiter::new(0);
        assert!((0..100).all(|_| off.allow("a").is_ok()));
    }

    #[test]
    fn memory_is_bounded() {
        let limiter = RateLimiter::with_capacity(5, 100);
        for key in 0..1000 {
            limiter.allow(key).unwrap();
        }
        assert_eq!(limiter.len(), 100);
    }

    #[test]
    fn concurrent_requests_never_exceed_the_limit() {
        let limiter = Arc::new(RateLimiter::new(50));
        let allowed: usize = std::thread::scope(|scope| {
            let handles: Vec<_> = (0..8)
                .map(|_| scope.spawn(|| (0..20).filter(|_| limiter.allow("shared").is_ok()).count()))
                .collect();
            handles.into_iter().map(|h| h.join().unwrap()).sum()
        });
        assert_eq!(allowed, 50);
    }
}
