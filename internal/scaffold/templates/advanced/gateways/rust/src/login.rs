// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own gateway that fits this project. The rules it keeps are in
// ../../PROTOCOL.md ("Sign-in", "Identity") and the login transport contract,
// docs/advanced/LOGIN-TRANSPORT.md in the pgmi repository.
//
//     GET  /auth/login?returnTo=/path   start the authorization code flow (PKCE)
//     GET  /auth/callback               check state, exchange the code, verify
//                                       the ID token, api.open_login_session
//     POST /auth/logout                 api.close_login_session, clear the cookies
//     POST /auth/dev-login              DEV_LOGIN=1 only: api.open_dev_login_session
//
// Only the opaque login-session handle reaches the browser, in an HttpOnly
// cookie. The database decides who the caller is; nothing here names a person.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::body::Bytes;
use axum::extract::{Query, State};
use axum::http::header::{AUTHORIZATION, CACHE_CONTROL, CONTENT_TYPE, COOKIE, LOCATION, SET_COOKIE};
use axum::http::{HeaderMap, HeaderValue, Method, StatusCode};
use axum::response::{IntoResponse, Response};
use base64::Engine;
use base64::engine::general_purpose::{STANDARD, URL_SAFE_NO_PAD};
use hmac::{Hmac, KeyInit, Mac};
use serde_json::{Map, Value};
use sha2::{Digest, Sha256};
use tokio::sync::OnceCell;
use tokio::time::Instant;

use crate::error::{GatewayError, Problem};
use crate::oidc::{self, Discovery, FetchError, KeyError, KeySet};
use crate::{Gateway, OidcConfig};

// ADAPT: cookie names, lifetimes and the development issuer seeded by
// membership/dev/01-issuers.sql.
pub const SESSION_COOKIE: &str = "__Host-session";
pub const CSRF_COOKIE: &str = "__Host-csrf";
const LOGIN_COOKIE: &str = "__Host-login";
pub const CSRF_HEADER: &str = "x-csrf-token";
const SESSION_MAX_AGE: u32 = 7 * 24 * 3600;
const LOGIN_MAX_AGE: Duration = Duration::from_secs(600);
const DEV_ISSUER: &str = "dev";
const PROVIDER_TIMEOUT: Duration = Duration::from_secs(10);

fn refused(status: StatusCode, code: impl Into<std::borrow::Cow<'static, str>>, detail: impl Into<String>) -> GatewayError {
    Problem::new(status, "Sign-in failed", code).detail(detail).into()
}

fn unavailable(detail: impl Into<String>) -> GatewayError {
    refused(StatusCode::BAD_GATEWAY, "login.provider_unavailable", detail)
}

impl From<FetchError> for GatewayError {
    fn from(error: FetchError) -> Self {
        match error {
            FetchError::Refused(status) => {
                refused(StatusCode::UNAUTHORIZED, "login.provider_refused", format!("the provider answered {status}"))
            }
            FetchError::Unavailable(_) => unavailable("the provider could not be reached"),
        }
    }
}

// -- cookies and CSRF ----------------------------------------------------------

pub fn cookie<'a>(headers: &'a HeaderMap, name: &str) -> Option<&'a str> {
    headers
        .get_all(COOKIE)
        .iter()
        .filter_map(|v| v.to_str().ok())
        .flat_map(|v| v.split(';'))
        .filter_map(|pair| pair.trim().split_once('='))
        .find(|(key, _)| *key == name)
        .map(|(_, value)| value)
        .filter(|value| !value.is_empty())
}

fn set_cookie(name: &str, value: &str, max_age: u64, http_only: bool) -> HeaderValue {
    let mut cookie = format!("{name}={value}; Path=/; Max-Age={max_age}; Secure; SameSite=Lax");
    if http_only {
        cookie.push_str("; HttpOnly");
    }
    HeaderValue::from_str(&cookie).expect("cookie values are base64url")
}

fn clear_cookie(name: &str) -> HeaderValue {
    set_cookie(name, "", 0, name != CSRF_COOKIE)
}

fn csrf_mac(secret: &[u8], login_session: &str) -> Hmac<Sha256> {
    let mut mac = <Hmac<Sha256> as KeyInit>::new_from_slice(secret).expect("HMAC takes a key of any length");
    mac.update(b"csrf\0");
    mac.update(login_session.as_bytes());
    mac
}

pub fn csrf_token(secret: &[u8], login_session: &str) -> String {
    URL_SAFE_NO_PAD.encode(csrf_mac(secret, login_session).finalize().into_bytes())
}

/// A state-changing request riding on the session cookie must carry the
/// session's CSRF token, which a cross-site page cannot read. The comparison
/// is the MAC's own, in constant time.
pub fn csrf_ok(secret: &[u8], method: &Method, login_session: Option<&str>, presented: Option<&str>) -> bool {
    let Some(session) = login_session else { return true };
    if matches!(*method, Method::GET | Method::HEAD | Method::OPTIONS) {
        return true;
    }
    match presented.and_then(|p| URL_SAFE_NO_PAD.decode(p).ok()) {
        Some(tag) => csrf_mac(secret, session).verify_slice(&tag).is_ok(),
        None => false,
    }
}

/// Only a path on this site: no scheme, no `//host`, no backslash, no
/// control or non-ASCII character (a header injection or a browser that
/// reads `/\evil` as a host).
pub fn safe_return_to(value: Option<&str>) -> String {
    match value {
        Some(v) if v.starts_with('/') && !v.starts_with("//") && !v.contains('\\') && v.chars().all(|c| ('!'..='~').contains(&c)) => {
            v.to_owned()
        }
        _ => "/".to_owned(),
    }
}

fn random_token() -> String {
    URL_SAFE_NO_PAD.encode(rand::random::<[u8; 32]>())
}

fn session_cookies(headers: &mut HeaderMap, secret: &[u8], token: &str) {
    headers.append(SET_COOKIE, set_cookie(SESSION_COOKIE, token, SESSION_MAX_AGE.into(), true));
    headers.append(SET_COOKIE, set_cookie(CSRF_COOKIE, &csrf_token(secret, token), SESSION_MAX_AGE.into(), false));
}

fn no_content(set_cookies: impl FnOnce(&mut HeaderMap)) -> Response {
    let mut response = StatusCode::NO_CONTENT.into_response();
    set_cookies(response.headers_mut());
    response.headers_mut().insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
    response
}

fn redirect(status: StatusCode, location: &str, set_cookies: impl FnOnce(&mut HeaderMap)) -> Result<Response, GatewayError> {
    let mut response = status.into_response();
    let headers = response.headers_mut();
    headers.insert(LOCATION, HeaderValue::from_str(location).map_err(GatewayError::internal)?);
    headers.insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
    set_cookies(headers);
    Ok(response)
}

// -- the transport ---------------------------------------------------------------

/// Sign-ins between /auth/login and /auth/callback (state, nonce, PKCE
/// verifier), each usable once. Bounded: at most LIMIT, the oldest going first.
///
/// ADAPT: held in this process's memory. Several gateway processes need a
/// shared store (keyed the same way, taken atomically) or sticky sessions.
#[derive(Default)]
struct PendingLogins(Mutex<HashMap<String, (Instant, Attempt)>>);

#[derive(Clone)]
struct Attempt {
    state: String,
    nonce: String,
    verifier: String,
    return_to: String,
}

impl PendingLogins {
    const LIMIT: usize = 10_000;

    fn put(&self, attempt: Attempt) -> String {
        let key = random_token();
        let mut items = self.0.lock().expect("never poisoned: no code panics under it");
        items.retain(|_, (at, _)| at.elapsed() < LOGIN_MAX_AGE);
        while items.len() >= Self::LIMIT {
            let oldest = items.iter().min_by_key(|(_, (at, _))| *at).map(|(k, _)| k.clone());
            items.remove(&oldest.expect("not empty"));
        }
        items.insert(key.clone(), (Instant::now(), attempt));
        key
    }

    fn take(&self, key: Option<&str>) -> Option<Attempt> {
        let (at, attempt) = self.0.lock().expect("never poisoned").remove(key?)?;
        (at.elapsed() < LOGIN_MAX_AGE).then_some(attempt)
    }
}

/// One OIDC issuer: discovery (once, on first use), the code exchange and the
/// ID token checks.
struct Provider {
    config: OidcConfig,
    http: reqwest::Client,
    discovered: OnceCell<(Discovery, KeySet)>,
}

impl Provider {
    async fn discovered(&self) -> Result<&(Discovery, KeySet), GatewayError> {
        self.discovered
            .get_or_try_init(|| async {
                let discovery = oidc::discover(&self.http, &self.config.issuer, PROVIDER_TIMEOUT).await?;
                // An unknown kid refetches the key set at once (no cooldown):
                // after a key rotation the next sign-in must not wait. Safe
                // here, because ID tokens reach this gateway only from the
                // token endpoint, never from a client.
                let keys = KeySet::new(self.http.clone(), discovery.jwks_uri.clone(), Duration::ZERO, PROVIDER_TIMEOUT);
                Ok((discovery, keys))
            })
            .await
    }

    async fn authorization_url(&self, state: &str, nonce: &str, challenge: &str) -> Result<String, GatewayError> {
        let (discovery, _) = self.discovered().await?;
        let endpoint = discovery.authorization_endpoint.as_deref().ok_or_else(|| unavailable("no authorization endpoint"))?;
        let mut url = reqwest::Url::parse(endpoint).map_err(|_| unavailable("authorization endpoint is not a URL"))?;
        url.query_pairs_mut()
            .append_pair("response_type", "code")
            .append_pair("client_id", &self.config.client_id)
            .append_pair("redirect_uri", &self.config.redirect_uri)
            .append_pair("scope", &self.config.scopes)
            .append_pair("state", state)
            .append_pair("nonce", nonce)
            .append_pair("code_challenge", challenge)
            .append_pair("code_challenge_method", "S256");
        Ok(url.into())
    }

    /// The authorization code for an ID token, with the PKCE verifier. A
    /// confidential client authenticates with HTTP Basic, its id and secret
    /// form-encoded first (RFC 6749 §2.3.1).
    async fn exchange(&self, code: &str, verifier: &str) -> Result<String, GatewayError> {
        let (discovery, _) = self.discovered().await?;
        let endpoint = discovery.token_endpoint.as_deref().ok_or_else(|| unavailable("no token endpoint"))?;
        let mut form = vec![("grant_type", "authorization_code"), ("code", code),
                            ("redirect_uri", &self.config.redirect_uri), ("code_verifier", verifier)];
        let mut request = self.http.post(endpoint).timeout(PROVIDER_TIMEOUT)
            .header(CONTENT_TYPE, "application/x-www-form-urlencoded");
        match &self.config.client_secret {
            Some(secret) => {
                let encode = |s: &str| form_urlencoded::byte_serialize(s.as_bytes()).collect::<String>();
                let pair = format!("{}:{}", encode(&self.config.client_id), encode(secret));
                request = request.header(AUTHORIZATION, format!("Basic {}", STANDARD.encode(pair)));
            }
            None => form.push(("client_id", &self.config.client_id)),
        }
        let body = form_urlencoded::Serializer::new(String::new()).extend_pairs(form).finish();
        let tokens: Value = oidc::get_json(request.body(body)).await?;
        tokens
            .get("id_token")
            .and_then(Value::as_str)
            .map(str::to_owned)
            .ok_or_else(|| refused(StatusCode::UNAUTHORIZED, "login.token_invalid", "the provider returned no ID token"))
    }

    /// Signature through the issuer's JWKS, iss, aud, exp, iat and nonce, and
    /// azp when aud lists several clients.
    async fn verify(&self, id_token: &str, nonce: &str) -> Result<Map<String, Value>, GatewayError> {
        let invalid = |detail: &str| refused(StatusCode::UNAUTHORIZED, "login.token_invalid", detail.to_owned());
        let (_, keys) = self.discovered().await?;
        let header = jsonwebtoken::decode_header(id_token).map_err(|_| invalid("not a JWT"))?;
        if !oidc::ALGORITHMS.contains(&header.alg) {
            return Err(invalid("algorithm not accepted"));
        }
        let key = match keys.key(header.kid.as_deref().ok_or_else(|| invalid("no key id"))?).await {
            Ok(key) => key,
            Err(KeyError::Unknown) => return Err(invalid("unknown key")),
            Err(KeyError::Unavailable) => return Err(unavailable("the provider's keys could not be read")),
        };
        let mut validation = oidc::validation(header.alg, &self.config.issuer);
        validation.validate_aud = true;
        validation.set_audience(&[&self.config.client_id]);
        let claims = jsonwebtoken::decode::<Map<String, Value>>(id_token, &key, &validation)
            .map_err(|e| invalid(&e.to_string()))?
            .claims;
        oidc::check_iat(&claims).map_err(invalid)?;
        if let Some(Value::Array(audiences)) = claims.get("aud")
            && audiences.len() > 1
            && claims.get("azp").and_then(Value::as_str) != Some(&self.config.client_id)
        {
            return Err(invalid("azp does not name this client"));
        }
        let presented = claims.get("nonce").and_then(Value::as_str).ok_or_else(|| invalid("no nonce"))?;
        if !same(presented, nonce) {
            return Err(invalid("nonce mismatch"));
        }
        if !claims.get("sub").is_some_and(Value::is_string) {
            return Err(invalid("sub is not a string"));
        }
        Ok(claims)
    }
}

pub struct Login {
    provider: Option<Provider>,
    csrf_secret: Vec<u8>,
    pub dev_login: bool,
    pending: PendingLogins,
}

impl Login {
    /// None when neither OIDC nor DEV_LOGIN is configured: /api is then anonymous.
    pub fn new(oidc: Option<OidcConfig>, csrf_secret: Vec<u8>, dev_login: bool, http: reqwest::Client)
        -> Result<Option<Self>, String> {
        if oidc.is_none() && !dev_login {
            return Ok(None);
        }
        let provider = match oidc {
            Some(config) => {
                if !crate::resource_server::https_or_loopback(&config.issuer) {
                    return Err("OIDC_ISSUER must be https (http only on a loopback host)".into());
                }
                Some(Provider { config, http, discovered: OnceCell::new() })
            }
            None => None,
        };
        Ok(Some(Login { provider, csrf_secret, dev_login, pending: PendingLogins::default() }))
    }

    pub fn issuer(&self) -> Option<&str> {
        self.provider.as_ref().map(|p| p.config.issuer.as_str())
    }

    pub fn check_csrf(&self, method: &Method, headers: &HeaderMap, login_session: Option<&str>) -> Result<(), Problem> {
        let presented = headers.get(CSRF_HEADER).and_then(|v| v.to_str().ok());
        if csrf_ok(&self.csrf_secret, method, login_session, presented) {
            return Ok(());
        }
        Err(Problem::forbidden("pgmi.csrf_refused").detail(format!("send the {CSRF_COOKIE} value in X-CSRF-Token")))
    }
}

/// api.open_login_session arguments beside the verified issuer and subject.
#[derive(Debug, PartialEq)]
struct LoginArguments<'a> {
    email: Option<&'a str>,
    name: Option<&'a str>,
    email_verified: Option<bool>,
    amr: Option<Vec<&'a str>>,
}

/// email_verified and amr go through as the provider sent them; a value of
/// the wrong JSON type becomes NULL, which the database reads as unverified
/// and as no second factor. ADAPT: map your provider's second-factor values
/// to "mfa" here if it needs it.
fn login_arguments(claims: &Map<String, Value>) -> LoginArguments<'_> {
    LoginArguments {
        email: claims.get("email").and_then(Value::as_str),
        name: claims.get("name").and_then(Value::as_str),
        email_verified: claims.get("email_verified").and_then(Value::as_bool),
        amr: claims.get("amr").and_then(Value::as_array).and_then(|values| values.iter().map(Value::as_str).collect()),
    }
}

/// A handle the browser held before is closed: each sign-in gets a new one.
async fn close_previous(client: &deadpool_postgres::Client, headers: &HeaderMap) -> Result<(), GatewayError> {
    if let Some(previous) = cookie(headers, SESSION_COOKIE) {
        client.execute("SELECT api.close_login_session($1)", &[&previous]).await.map_err(GatewayError::internal)?;
    }
    Ok(())
}

fn configured(gateway: &Gateway) -> Result<(&Login, &Provider), GatewayError> {
    let login = gateway.login.as_ref().ok_or_else(Problem::not_found)?;
    Ok((login, login.provider.as_ref().ok_or_else(Problem::not_found)?))
}

pub async fn begin(State(gateway): State<Arc<Gateway>>, Query(query): Query<HashMap<String, String>>)
    -> Result<Response, GatewayError> {
    let (login, provider) = configured(&gateway)?;
    let verifier = random_token();
    let challenge = URL_SAFE_NO_PAD.encode(Sha256::digest(verifier.as_bytes()));
    let (state, nonce) = (random_token(), random_token());
    let location = provider.authorization_url(&state, &nonce, &challenge).await?;
    let return_to = safe_return_to(query.get("returnTo").map(String::as_str));
    let key = login.pending.put(Attempt { state, nonce, verifier, return_to });
    redirect(StatusCode::FOUND, &location, |h| {
        h.append(SET_COOKIE, set_cookie(LOGIN_COOKIE, &key, LOGIN_MAX_AGE.as_secs(), true));
    })
}

pub async fn callback(State(gateway): State<Arc<Gateway>>, Query(query): Query<HashMap<String, String>>,
                      headers: HeaderMap) -> Response {
    match complete(&gateway, &query, &headers).await {
        Ok(response) => response,
        Err(error) => {
            if let GatewayError::Problem(problem) = &error {
                tracing::info!(code = %problem.code, detail = ?problem.detail, "sign-in refused");
            }
            let mut response = error.into_response();
            response.headers_mut().append(SET_COOKIE, clear_cookie(LOGIN_COOKIE));
            response
        }
    }
}

/// The pending sign-in is consumed first, so a callback works once, and only
/// in the browser that started it.
async fn complete(gateway: &Gateway, query: &HashMap<String, String>, headers: &HeaderMap) -> Result<Response, GatewayError> {
    let (login, provider) = configured(gateway)?;
    let attempt = login.pending.take(cookie(headers, LOGIN_COOKIE));
    let state_invalid = |detail: &str| refused(StatusCode::BAD_REQUEST, "login.state_invalid", detail.to_owned());
    let attempt = match (attempt, query.get("state")) {
        (Some(attempt), Some(state)) if same(state, &attempt.state) => attempt,
        _ => return Err(state_invalid("unknown, expired or replayed sign-in")),
    };
    if let Some(error) = query.get("error") {
        return Err(refused(StatusCode::BAD_REQUEST, "login.provider_error", error.chars().take(100).collect::<String>()));
    }
    let code = query.get("code").ok_or_else(|| state_invalid("the callback carries no code"))?;

    let claims = provider.verify(&provider.exchange(code, &attempt.verifier).await?, &attempt.nonce).await?;
    let LoginArguments { email, name, email_verified, amr } = login_arguments(&claims);
    let (issuer, subject) = (claims["iss"].as_str(), claims["sub"].as_str());
    let client = gateway.pool.get().await?;
    let row = client
        .query_one("SELECT token, problem FROM api.open_login_session($1, $2, $3, $4, $5, $6)",
                   &[&issuer, &subject, &email, &name, &email_verified, &amr])
        .await
        .map_err(GatewayError::internal)?;
    let token: Option<String> = row.get("token");
    let Some(token) = token else {
        let code: Option<String> = row.get("problem");
        return Err(refused(StatusCode::FORBIDDEN, code.unwrap_or_else(|| "login.refused".into()),
                           "the database refused the sign-in"));
    };
    close_previous(&client, headers).await?;
    redirect(StatusCode::SEE_OTHER, &attempt.return_to, |h| {
        session_cookies(h, &login.csrf_secret, &token);
        h.append(SET_COOKIE, clear_cookie(LOGIN_COOKIE));
    })
}

/// Equality in constant time, for the state and the nonce: the time a
/// comparison takes must say nothing about how much of a guess was right.
/// MAC verification is constant-time; the key does not matter here.
fn same(presented: &str, expected: &str) -> bool {
    csrf_mac(b"compare", presented).verify_slice(&csrf_mac(b"compare", expected).finalize().into_bytes()).is_ok()
}

pub async fn logout(State(gateway): State<Arc<Gateway>>, method: Method, headers: HeaderMap) -> Result<Response, GatewayError> {
    let login = gateway.login.as_ref().ok_or_else(Problem::not_found)?;
    let login_session = cookie(&headers, SESSION_COOKIE);
    login.check_csrf(&method, &headers, login_session)?;
    if let Some(session) = login_session {
        let client = gateway.pool.get().await?;
        client.execute("SELECT api.close_login_session($1)", &[&session]).await.map_err(GatewayError::internal)?;
    }
    Ok(no_content(|h| {
        h.append(SET_COOKIE, clear_cookie(SESSION_COOKIE));
        h.append(SET_COOKIE, clear_cookie(CSRF_COOKIE));
    }))
}

#[derive(serde::Deserialize)]
#[serde(rename_all = "camelCase")]
struct DevLogin {
    subject: String,
    email: String,
    display_name: Option<String>,
}

/// Development sign-in through api.open_dev_login_session, in place of an
/// identity provider. 404 unless DEV_LOGIN=1; the database has the function
/// only in an env=dev deploy, so both must agree.
pub async fn dev_sign_in(State(gateway): State<Arc<Gateway>>, headers: HeaderMap, body: Bytes) -> Result<Response, GatewayError> {
    let login = gateway.login.as_ref().filter(|l| l.dev_login).ok_or_else(Problem::not_found)?;
    let input: DevLogin = serde_json::from_slice(if body.is_empty() { b"{}" } else { &body }).map_err(|_| {
        Problem::bad_request("login.request_invalid").detail(r#"send {"subject", "email", "displayName"} as JSON strings"#)
    })?;
    let client = gateway.pool.get().await?;
    let token: Option<String> = client
        .query_one("SELECT api.open_dev_login_session($1, $2, $3, $4)",
                   &[&DEV_ISSUER, &input.subject, &input.email, &input.display_name])
        .await
        .map_err(GatewayError::internal)?
        .get(0);
    let token = token.ok_or_else(|| refused(StatusCode::FORBIDDEN, "login.refused", "the database refused the sign-in"))?;
    close_previous(&client, &headers).await?;
    Ok(no_content(|h| session_cookies(h, &login.csrf_secret, &token)))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_state_changing_request_with_the_session_needs_its_csrf_token() {
        let secret = b"secret";
        assert!(csrf_ok(secret, &Method::GET, Some("ls_a"), None));
        assert!(csrf_ok(secret, &Method::POST, None, None));
        assert!(!csrf_ok(secret, &Method::POST, Some("ls_a"), None));
        assert!(!csrf_ok(secret, &Method::POST, Some("ls_a"), Some(&csrf_token(secret, "ls_b"))));
        assert!(csrf_ok(secret, &Method::POST, Some("ls_a"), Some(&csrf_token(secret, "ls_a"))));
    }

    #[test]
    fn return_to_stays_on_this_site() {
        for hostile in ["//evil.example", "/\\evil", "https://evil.example/", "/\r\nSet-Cookie: x=1", "/\tx", "/caf€", "x"] {
            assert_eq!(safe_return_to(Some(hostile)), "/", "{hostile:?}");
        }
        assert_eq!(safe_return_to(Some("/app?tab=2")), "/app?tab=2");
        assert_eq!(safe_return_to(None), "/");
    }

    #[test]
    fn claims_of_the_wrong_type_reach_the_database_as_null() {
        let claims: Map<String, Value> = serde_json::from_str(
            r#"{"email": "a@example.com", "email_verified": "true", "amr": ["pwd", 1]}"#).unwrap();
        assert_eq!(login_arguments(&claims),
                   LoginArguments { email: Some("a@example.com"), name: None, email_verified: None, amr: None });
        let claims: Map<String, Value> = serde_json::from_str(r#"{"email_verified": true, "amr": ["pwd", "mfa"]}"#).unwrap();
        assert_eq!(login_arguments(&claims),
                   LoginArguments { email: None, name: None, email_verified: Some(true), amr: Some(vec!["pwd", "mfa"]) });
    }

    #[test]
    fn a_pending_sign_in_is_taken_once() {
        let pending = PendingLogins::default();
        let key = pending.put(Attempt { state: "s".into(), nonce: "n".into(), verifier: "v".into(), return_to: "/".into() });
        assert!(pending.take(Some(&key)).is_some());
        assert!(pending.take(Some(&key)).is_none());
        assert!(pending.take(None).is_none());
    }
}
