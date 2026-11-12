use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::sync::OnceLock;
use std::time::Duration;

use jiff::Timestamp;
use reqwest::StatusCode;

use super::Options;
use super::azure;
use super::http::{client, encode};
use super::renew::{Fetched, Mint, Renewing};
use crate::env::Env;
use crate::error::{Error, Result, Secret};

const ATTEMPTS: u32 = 3;
const AZURE_HOSTS: [&str; 3] = [
    ".azuredatabricks.net",
    ".databricks.azure.cn",
    ".databricks.azure.us",
];

#[derive(Debug, Clone)]
enum Route {
    Pat(Secret),
    M2m { client_id: String, secret: Secret },
    Cli,
    Azure,
}

impl Route {
    fn name(&self) -> &'static str {
        match self {
            Route::Pat(_) => "pat",
            Route::M2m { .. } => "oauth-m2m",
            Route::Cli => "databricks-cli",
            Route::Azure => "azure",
        }
    }
}

#[derive(Default)]
struct Values {
    host: Option<String>,
    token: Option<String>,
    client_id: Option<String>,
    client_secret: Option<String>,
    auth_type: Option<String>,
}

impl Values {
    fn from_env(env: &Env) -> Result<Self> {
        let get = |n: &str| env.non_empty(n).map(|v| v.map(str::to_string));
        Ok(Self {
            host: get("DATABRICKS_HOST")?,
            token: get("DATABRICKS_TOKEN")?,
            client_id: get("DATABRICKS_CLIENT_ID")?,
            client_secret: get("DATABRICKS_CLIENT_SECRET")?,
            auth_type: get("DATABRICKS_AUTH_TYPE")?,
        })
    }

    fn fill(&mut self, file: &HashMap<String, String>) {
        let pick = |slot: &mut Option<String>, key: &str| {
            if slot.is_none() {
                *slot = file.get(key).filter(|v| !v.is_empty()).cloned();
            }
        };
        pick(&mut self.host, "host");
        pick(&mut self.token, "token");
        pick(&mut self.client_id, "client_id");
        pick(&mut self.client_secret, "client_secret");
        pick(&mut self.auth_type, "auth_type");
    }

    fn configured(&self) -> bool {
        self.host.is_some()
            || self.token.is_some()
            || self.client_id.is_some()
            || self.client_secret.is_some()
    }
}

pub(crate) struct Settings {
    host: String,
    endpoint: String,
    profile: Option<String>,
    routes: Vec<Route>,
}

impl std::fmt::Debug for Settings {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Settings")
            .field("host", &self.host)
            .field("endpoint", &self.endpoint)
            .field("profile", &self.profile)
            .field(
                "routes",
                &self.routes.iter().map(Route::name).collect::<Vec<_>>(),
            )
            .finish_non_exhaustive()
    }
}

pub(crate) fn settings(req: &Options, env: &Env) -> Result<Settings> {
    settings_with(req, env, |p| std::fs::read_to_string(p))
}

fn settings_with(
    req: &Options,
    env: &Env,
    read: impl Fn(&Path) -> std::io::Result<String>,
) -> Result<Settings> {
    let endpoint = endpoint(req, env)?;
    let mut values = Values::from_env(env)?;
    let explicit = env
        .non_empty("DATABRICKS_CONFIG_PROFILE")?
        .map(str::to_string);
    let mut profile = None;
    if (explicit.is_some() || !values.configured())
        && let Some((name, section)) = config_file(env, explicit.as_deref(), read)?
    {
        values.fill(&section);
        profile = Some(name);
    }
    let host = values.host.as_deref().ok_or_else(|| {
        Error::config(
            "Databricks Lakebase sign-in needs the workspace URL: set DATABRICKS_HOST or a profile in ~/.databrickscfg (DATABRICKS_CONFIG_PROFILE)",
        )
    })?;
    let host = workspace_url(host)?;
    let routes = routes(&values, &host)?;
    Ok(Settings {
        host,
        endpoint,
        profile,
        routes,
    })
}

fn endpoint(req: &Options, env: &Env) -> Result<String> {
    let value = match req
        .databricks_endpoint
        .as_deref()
        .filter(|v| !v.is_empty())
    {
        Some(v) => v.to_string(),
        None => env
            .non_empty("PGMI_DATABRICKS_ENDPOINT")?
            .map(str::to_string)
            .ok_or_else(|| {
                Error::config(
                    "Databricks Lakebase sign-in requires --databricks-endpoint (projects/<project>/branches/<branch>/endpoints/<endpoint>) or $PGMI_DATABRICKS_ENDPOINT",
                )
            })?,
    };
    let parts: Vec<&str> = value.split('/').collect();
    let shaped = matches!(
        parts.as_slice(),
        ["projects", p, "branches", b, "endpoints", e]
            if !p.is_empty() && !b.is_empty() && !e.is_empty()
    );
    if !shaped {
        return Err(Error::config(format!(
            "invalid Databricks endpoint {value:?}: expected projects/<project>/branches/<branch>/endpoints/<endpoint>"
        )));
    }
    Ok(value)
}

fn workspace_url(host: &str) -> Result<String> {
    let text = if host.contains("://") {
        host.to_string()
    } else {
        format!("https://{host}")
    };
    let url = reqwest::Url::parse(&text)
        .map_err(|e| Error::config(format!("invalid Databricks host {host:?}: {e}")))?;
    if url.scheme() != "https" {
        return Err(Error::config(format!(
            "invalid Databricks host {host:?}: the workspace URL must use https"
        )));
    }
    if url.host_str().is_none_or(str::is_empty) {
        return Err(Error::config(format!(
            "invalid Databricks host {host:?}: no host name"
        )));
    }
    Ok(url.origin().ascii_serialization())
}

fn is_azure(host: &str) -> bool {
    let name = host
        .trim_start_matches("https://")
        .split(':')
        .next()
        .unwrap_or_default()
        .to_ascii_lowercase();
    AZURE_HOSTS.iter().any(|s| name.ends_with(s))
}

fn routes(v: &Values, host: &str) -> Result<Vec<Route>> {
    let pat = v.token.as_deref().map(|t| Route::Pat(Secret::from(t)));
    let m2m = match (&v.client_id, &v.client_secret) {
        (Some(id), Some(secret)) => Some(Route::M2m {
            client_id: id.clone(),
            secret: Secret::from(secret.as_str()),
        }),
        _ => None,
    };
    let Some(kind) = v.auth_type.as_deref() else {
        if let Some(route) = pat.or(m2m) {
            return Ok(vec![route]);
        }
        let mut chain = vec![Route::Cli];
        if is_azure(host) {
            chain.push(Route::Azure);
        }
        return Ok(chain);
    };
    match (kind, pat, m2m) {
        ("pat", Some(route), _) | ("oauth-m2m", _, Some(route)) => Ok(vec![route]),
        ("pat", ..) => Err(Error::config(
            "DATABRICKS_AUTH_TYPE=pat needs DATABRICKS_TOKEN or a token in the profile",
        )),
        ("oauth-m2m", ..) => Err(Error::config(
            "DATABRICKS_AUTH_TYPE=oauth-m2m needs DATABRICKS_CLIENT_ID and DATABRICKS_CLIENT_SECRET",
        )),
        ("databricks-cli", ..) => Ok(vec![Route::Cli]),
        ("azure-cli" | "azure-msi" | "azure-client-secret", ..) => Ok(vec![Route::Azure]),
        (other, ..) => Err(Error::config(format!(
            "unsupported DATABRICKS_AUTH_TYPE {other:?}: pgmi supports pat, oauth-m2m, databricks-cli, azure-cli, azure-msi and azure-client-secret"
        ))),
    }
}

fn config_file(
    env: &Env,
    explicit: Option<&str>,
    read: impl Fn(&Path) -> std::io::Result<String>,
) -> Result<Option<(String, HashMap<String, String>)>> {
    let path = match env.non_empty("DATABRICKS_CONFIG_FILE")? {
        Some(p) => match (p.strip_prefix('~'), env.home()) {
            (Some(rest), Some(home)) => home.join(rest.trim_start_matches(['/', '\\'])),
            _ => PathBuf::from(p),
        },
        None => match env.home() {
            Some(home) => home.join(".databrickscfg"),
            None => return Ok(None),
        },
    };
    let text = match read(&path) {
        Ok(t) => t,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound && explicit.is_none() => {
            return Ok(None);
        }
        Err(e) => {
            return Err(Error::config(format!(
                "read Databricks config file {}: {e}",
                path.display()
            )));
        }
    };
    let mut sections = parse_ini(&text);
    let chosen = explicit.map(str::to_string).or_else(|| {
        sections
            .get("__settings__")
            .and_then(|s| s.get("default_profile"))
            .filter(|p| !p.is_empty())
            .cloned()
    });
    if chosen.as_deref() == Some("__settings__") {
        return Err(Error::config(format!(
            "{}: __settings__ is a reserved section name and cannot be used as a profile",
            path.display()
        )));
    }
    let name = chosen.clone().unwrap_or_else(|| "DEFAULT".to_string());
    match sections.remove(&name).filter(|s| !s.is_empty()) {
        Some(section) => Ok(Some((name, section))),
        None if chosen.is_none() => Ok(None),
        None => Err(Error::config(format!(
            "{} has no {name} profile configured",
            path.display()
        ))),
    }
}

fn parse_ini(text: &str) -> HashMap<String, HashMap<String, String>> {
    let mut sections: HashMap<String, HashMap<String, String>> = HashMap::new();
    let mut current = "DEFAULT".to_string();
    for line in text.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') || line.starts_with(';') {
            continue;
        }
        if let Some(name) = line.strip_prefix('[').and_then(|l| l.strip_suffix(']')) {
            current = name.trim().to_string();
            sections.entry(current.clone()).or_default();
            continue;
        }
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        let value = [" #", " ;"]
            .iter()
            .filter_map(|c| value.find(c))
            .min()
            .map_or(value, |i| value.get(..i).unwrap_or(value));
        sections
            .entry(current.clone())
            .or_default()
            .insert(key.trim().to_string(), value.trim().to_string());
    }
    sections
}

/// A Lakebase database credential, renewed before it expires.
pub(crate) type Lakebase = Renewing<Signin>;

/// The workspace sign-in that mints Lakebase credentials. The first route
/// that gives a bearer token is kept, so every later credential comes from
/// the same identity, as `azure::Signin` keeps its credential.
pub(crate) struct Signin {
    settings: Settings,
    azure: azure::Signin,
    pinned: OnceLock<Route>,
}

pub(crate) async fn lakebase(req: &Options, env: &Env) -> Result<Lakebase> {
    let (owned, vars) = (req.clone(), env.clone());
    let settings = crate::blocking::spawn(move || settings(&owned, &vars))
        .await
        .map_err(|e| Error::internal("reading the Databricks configuration").with_source(e))??;
    Renewing::start(Signin {
        settings,
        azure: azure::Signin::new(req.clone(), env.clone(), azure::DATABRICKS),
        pinned: OnceLock::new(),
    })
    .await
}

/// Why one sign-in route gave no bearer token.
#[derive(Debug)]
pub(crate) enum RouteError {
    M2m(Error),
    Cli(CliError),
    Azure(Error),
}

impl std::fmt::Display for RouteError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            RouteError::M2m(e) | RouteError::Azure(e) => e.fmt(f),
            RouteError::Cli(e) => e.fmt(f),
        }
    }
}

impl std::error::Error for RouteError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            RouteError::M2m(e) | RouteError::Azure(e) => Some(e),
            RouteError::Cli(e) => Some(e),
        }
    }
}

/// Every route tried gave no token. `Display` is the message; `source` is
/// the first route's error.
#[derive(Debug)]
pub(crate) struct NoRoute {
    host: String,
    failures: Vec<(&'static str, RouteError)>,
}

impl std::fmt::Display for NoRoute {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "failed to sign in to Databricks at {}: no credential worked; set DATABRICKS_TOKEN, or DATABRICKS_CLIENT_ID and DATABRICKS_CLIENT_SECRET, or run databricks auth login",
            self.host
        )?;
        for (route, e) in &self.failures {
            write!(f, "\n\t{route}: {e}")?;
        }
        Ok(())
    }
}

impl std::error::Error for NoRoute {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        self.failures.first().map(|(_, e)| e as _)
    }
}

impl From<NoRoute> for Error {
    fn from(e: NoRoute) -> Self {
        Error::connection(e.to_string()).with_cause(e)
    }
}

impl Mint for Signin {
    type Value = Secret;

    async fn mint(&self) -> Result<Fetched> {
        let bearer = self.bearer(|route| self.through(route)).await?;
        credential(&self.settings, &bearer).await
    }
}

impl Signin {
    /// The bearer token from the pinned route, or from the first route that
    /// gives one, which is then pinned. An OAuth M2M failure ends the search,
    /// as in the Databricks SDK.
    async fn bearer<G, F>(&self, through: G) -> Result<Secret>
    where
        G: Fn(Route) -> F,
        F: Future<Output = std::result::Result<Secret, RouteError>>,
    {
        let host = &self.settings.host;
        if let Some(route) = self.pinned.get() {
            return through(route.clone()).await.map_err(|e| match e {
                RouteError::M2m(e) => e,
                e => NoRoute {
                    host: host.clone(),
                    failures: vec![(route.name(), e)],
                }
                .into(),
            });
        }
        let mut failures = Vec::new();
        for route in &self.settings.routes {
            match through(route.clone()).await {
                Ok(t) => {
                    let _ = self.pinned.set(route.clone());
                    return Ok(t);
                }
                Err(RouteError::M2m(e)) => return Err(e),
                Err(e) => failures.push((route.name(), e)),
            }
        }
        Err(NoRoute {
            host: host.clone(),
            failures,
        }
        .into())
    }

    async fn through(&self, route: Route) -> std::result::Result<Secret, RouteError> {
        let s = &self.settings;
        match route {
            Route::Pat(token) => Ok(token),
            Route::M2m { client_id, secret } => m2m(&s.host, &client_id, &secret)
                .await
                .map_err(RouteError::M2m),
            Route::Cli => cli(s).await.map_err(RouteError::Cli),
            Route::Azure => self
                .azure
                .mint()
                .await
                .map(|f| f.value)
                .map_err(RouteError::Azure),
        }
    }
}

impl std::fmt::Debug for Signin {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "Lakebase({} {})",
            self.settings.host, self.settings.endpoint
        )
    }
}

async fn m2m(host: &str, id: &str, secret: &Secret) -> Result<Secret> {
    let url = format!("{host}/oidc/v1/token");
    let (status, body) = send(|http| {
        http.post(&url)
            .basic_auth(id, Some(secret.expose()))
            .header("Content-Type", "application/x-www-form-urlencoded")
            .header("Accept", "application/json")
            .body(format!(
                "grant_type=client_credentials&scope={}",
                encode("all-apis")
            ))
    })
    .await
    .map_err(|e| {
        Error::connection(format!("failed to sign in to Databricks: {url}")).with_source(e)
    })?;
    if !status.is_success() {
        return Err(Error::connection(format!(
            "failed to sign in to Databricks with OAuth M2M (client {id}): {url} answered {status}{}",
            reason(&body)
        )));
    }
    field(&body, "access_token")
        .map(Secret::from)
        .ok_or_else(|| {
            Error::connection(format!(
                "failed to sign in to Databricks with OAuth M2M: {url} returned no access_token"
            ))
        })
}

/// Why `databricks auth token` gave no token.
#[derive(Debug)]
pub(crate) enum CliError {
    NotInstalled,
    Spawn(std::io::Error),
    TimedOut,
    Wait(std::io::Error),
    Failed(String),
    NoToken,
}

impl std::fmt::Display for CliError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CliError::NotInstalled => f.write_str("databricks CLI not found on PATH"),
            CliError::Spawn(e) | CliError::Wait(e) => e.fmt(f),
            CliError::TimedOut => f.write_str("databricks auth token timed out"),
            CliError::Failed(stderr) => write!(f, "databricks auth token failed: {stderr}"),
            CliError::NoToken => f.write_str("databricks auth token printed no access_token"),
        }
    }
}

impl std::error::Error for CliError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            CliError::Spawn(e) | CliError::Wait(e) => Some(e),
            _ => None,
        }
    }
}

async fn cli(s: &Settings) -> std::result::Result<Secret, CliError> {
    let mut args = vec!["auth", "token"];
    match &s.profile {
        Some(p) => args.extend(["--profile", p.as_str()]),
        None => args.extend(["--host", s.host.as_str()]),
    }
    let child = tokio::process::Command::new("databricks")
        .args(&args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true)
        .spawn()
        .map_err(|e| match e.kind() {
            std::io::ErrorKind::NotFound => CliError::NotInstalled,
            _ => CliError::Spawn(e),
        })?;
    let out = tokio::time::timeout(Duration::from_secs(60), child.wait_with_output())
        .await
        .map_err(|_| CliError::TimedOut)?
        .map_err(CliError::Wait)?;
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr);
        return Err(CliError::Failed(stderr.trim().replace('\n', " ")));
    }
    field(&String::from_utf8_lossy(&out.stdout), "access_token")
        .map(Secret::from)
        .ok_or(CliError::NoToken)
}

async fn credential(s: &Settings, bearer: &Secret) -> Result<Fetched> {
    let url = format!("{}/api/2.0/postgres/credentials", s.host);
    let body = serde_json::json!({ "endpoint": s.endpoint }).to_string();
    let (status, text) = send(|http| {
        http.post(&url)
            .bearer_auth(bearer.expose())
            .header("Content-Type", "application/json")
            .header("Accept", "application/json")
            .body(body.clone())
    })
    .await
    .map_err(|e| {
        Error::connection(format!("failed to get a Lakebase credential from {url}")).with_source(e)
    })?;
    if !status.is_success() {
        return Err(Error::connection(refusal(
            status,
            &s.endpoint,
            &s.host,
            &text,
        )));
    }
    parse_credential(&text).map_err(|e| {
        Error::connection(format!("failed to get a Lakebase credential from {url}")).with_source(e)
    })
}

fn refusal(status: StatusCode, endpoint: &str, host: &str, body: &str) -> String {
    let why = reason(body);
    match status {
        StatusCode::UNAUTHORIZED => format!(
            "Databricks at {host} did not accept the sign-in ({status}){why}; check DATABRICKS_HOST and the token, client secret or CLI login"
        ),
        StatusCode::FORBIDDEN => format!(
            "the signed-in Databricks identity may not get a credential for {endpoint} ({status}){why}; check the token (Databricks answers 403 for an invalid one) and the identity's permissions on the Lakebase project"
        ),
        StatusCode::NOT_FOUND => format!(
            "Databricks at {host} has no Lakebase endpoint {endpoint} ({status}){why}; check --databricks-endpoint and DATABRICKS_HOST"
        ),
        _ if retryable(status) => format!(
            "Databricks at {host} could not give a Lakebase credential for {endpoint} ({status}){why}, after {ATTEMPTS} attempts"
        ),
        _ => format!(
            "Databricks at {host} refused a Lakebase credential for {endpoint} ({status}){why}"
        ),
    }
}

fn reason(body: &str) -> String {
    let message = serde_json::from_str::<serde_json::Value>(body)
        .ok()
        .and_then(|v| {
            ["message", "error_description", "error"]
                .iter()
                .find_map(|k| v.get(k)?.as_str().map(str::to_string))
        })
        .unwrap_or_default();
    let message: String = message.chars().take(300).collect();
    let message = message.trim().replace('\n', " ");
    if message.is_empty() {
        String::new()
    } else {
        format!(": {message}")
    }
}

fn field(body: &str, key: &str) -> Option<String> {
    serde_json::from_str::<serde_json::Value>(body.trim())
        .ok()?
        .get(key)?
        .as_str()
        .filter(|t| !t.is_empty())
        .map(str::to_string)
}

/// Why the credentials API's answer holds no usable credential.
#[derive(Debug)]
pub(crate) enum AnswerError {
    NotJson,
    NoToken,
    Expiry { text: String, error: jiff::Error },
}

impl std::fmt::Display for AnswerError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            AnswerError::NotJson => f.write_str("the answer is not JSON"),
            AnswerError::NoToken => f.write_str("the answer has no token"),
            AnswerError::Expiry { text, error } => {
                write!(f, "invalid expire_time {text:?}: {error}")
            }
        }
    }
}

impl std::error::Error for AnswerError {}

fn parse_credential(body: &str) -> std::result::Result<Fetched, AnswerError> {
    let value: serde_json::Value = serde_json::from_str(body).map_err(|_| AnswerError::NotJson)?;
    let token = value
        .get("token")
        .and_then(|t| t.as_str())
        .filter(|t| !t.is_empty())
        .ok_or(AnswerError::NoToken)?;
    let expires = match value.get("expire_time").and_then(|t| t.as_str()) {
        Some(t) => Some(
            t.parse::<Timestamp>()
                .map_err(|error| AnswerError::Expiry {
                    text: t.to_string(),
                    error,
                })?,
        ),
        None => None,
    };
    Ok(Fetched {
        value: Secret::from(token),
        expires,
    })
}

fn retryable(status: StatusCode) -> bool {
    status == StatusCode::TOO_MANY_REQUESTS || status.is_server_error()
}

/// A request that got no answer, after `attempts` tries.
#[derive(Debug)]
pub(crate) enum SendError {
    Client(Error),
    Http {
        error: reqwest::Error,
        attempts: u32,
    },
}

impl std::fmt::Display for SendError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            SendError::Client(e) => e.fmt(f),
            SendError::Http { error, attempts } => {
                f.write_str(&crate::error::chain(error))?;
                if *attempts > 1 {
                    write!(f, " (after {attempts} attempts)")?;
                }
                Ok(())
            }
        }
    }
}

impl std::error::Error for SendError {}

async fn send(
    build: impl Fn(&reqwest::Client) -> reqwest::RequestBuilder,
) -> std::result::Result<(StatusCode, String), SendError> {
    let http = client().map_err(SendError::Client)?;
    let mut attempt = 1;
    loop {
        let outcome = match build(&http).send().await {
            Ok(r) => {
                let status = r.status();
                r.text().await.map(|t| (status, t))
            }
            Err(e) => Err(e),
        };
        let again = match &outcome {
            Ok((status, _)) => retryable(*status),
            Err(_) => true,
        };
        if !again || attempt >= ATTEMPTS {
            return outcome.map_err(|error| SendError::Http {
                error,
                attempts: attempt,
            });
        }
        tokio::time::sleep(Duration::from_secs(u64::from(attempt))).await;
        attempt += 1;
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use super::*;

    fn signin(routes: Vec<Route>) -> Signin {
        Signin {
            settings: Settings {
                host: "https://adb-1.azuredatabricks.net".into(),
                endpoint: EP.into(),
                profile: None,
                routes,
            },
            azure: azure::Signin::new(Options::default(), Env::default(), azure::DATABRICKS),
            pinned: OnceLock::new(),
        }
    }

    async fn bearer(
        s: &Signin,
        tried: &Mutex<Vec<&'static str>>,
        works: &[&str],
    ) -> Result<Secret> {
        s.bearer(|route| {
            let name = route.name();
            tried.lock().unwrap().push(name);
            let ok = works.contains(&name);
            async move {
                if ok {
                    Ok(Secret::from(name))
                } else {
                    Err(RouteError::Cli(CliError::NoToken))
                }
            }
        })
        .await
    }

    #[tokio::test]
    async fn every_bearer_after_the_first_comes_from_the_route_that_worked() {
        let s = signin(vec![Route::Cli, Route::Azure]);
        let tried = Mutex::new(Vec::new());
        assert_eq!(
            bearer(&s, &tried, &["azure"]).await.unwrap().expose(),
            "azure"
        );
        assert_eq!(*tried.lock().unwrap(), ["databricks-cli", "azure"]);
        tried.lock().unwrap().clear();
        let e = bearer(&s, &tried, &["databricks-cli"]).await.unwrap_err();
        assert_eq!(*tried.lock().unwrap(), ["azure"]);
        assert_eq!(e.exit_code(), 11);
        assert!(
            e.text()
                .ends_with("\n\tazure: databricks auth token printed no access_token"),
            "{}",
            e.text()
        );
    }

    #[tokio::test]
    async fn no_route_names_each_failure_and_keeps_the_first_as_the_source() {
        let s = signin(vec![Route::Cli, Route::Azure]);
        let tried = Mutex::new(Vec::new());
        let e = bearer(&s, &tried, &[]).await.unwrap_err();
        assert_eq!(
            e.text(),
            "failed to sign in to Databricks at https://adb-1.azuredatabricks.net: no credential worked; set DATABRICKS_TOKEN, or DATABRICKS_CLIENT_ID and DATABRICKS_CLIENT_SECRET, or run databricks auth login\n\tdatabricks-cli: databricks auth token printed no access_token\n\tazure: databricks auth token printed no access_token"
        );
        let cause = std::error::Error::source(&e)
            .and_then(|c| c.downcast_ref::<NoRoute>())
            .and_then(std::error::Error::source)
            .and_then(|c| c.downcast_ref::<RouteError>());
        assert!(matches!(cause, Some(RouteError::Cli(CliError::NoToken))));
        tried.lock().unwrap().clear();
        bearer(&s, &tried, &["azure"]).await.unwrap();
        assert_eq!(*tried.lock().unwrap(), ["databricks-cli", "azure"]);
    }

    fn req(endpoint: Option<&str>) -> Options {
        Options {
            databricks_endpoint: endpoint.map(str::to_string),
            ..Options::default()
        }
    }

    const EP: &str = "projects/p/branches/b/endpoints/e";

    #[test]
    fn the_endpoint_comes_from_the_flag_then_the_environment_and_must_be_a_resource_name() {
        let env = Env::from_pairs(&[(
            "PGMI_DATABRICKS_ENDPOINT",
            "projects/x/branches/y/endpoints/z",
        )]);
        assert_eq!(endpoint(&req(Some(EP)), &env).unwrap(), EP);
        assert_eq!(
            endpoint(&req(None), &env).unwrap(),
            "projects/x/branches/y/endpoints/z"
        );
        let missing = endpoint(&req(None), &Env::default()).unwrap_err();
        assert_eq!(missing.exit_code(), 10);
        assert!(
            missing.text().contains("--databricks-endpoint"),
            "{}",
            missing.text()
        );
        for bad in [
            "p/b/e",
            "projects/p/branches/b/endpoints/",
            "projects//branches/b/endpoints/e",
            "projects/p/branches/b/endpoints/e/x",
            "projects/p/endpoints/b/branches/e",
        ] {
            let e = endpoint(&req(Some(bad)), &Env::default()).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{bad}");
            assert!(
                e.text().starts_with("invalid Databricks endpoint"),
                "{}",
                e.text()
            );
        }
    }

    #[test]
    fn the_workspace_url_is_https_and_keeps_only_the_origin() {
        assert_eq!(
            workspace_url("adb-1.2.azuredatabricks.net").unwrap(),
            "https://adb-1.2.azuredatabricks.net"
        );
        assert_eq!(
            workspace_url("https://dbc-1.cloud.databricks.com/").unwrap(),
            "https://dbc-1.cloud.databricks.com"
        );
        assert_eq!(
            workspace_url("https://localhost:8443/?o=1").unwrap(),
            "https://localhost:8443"
        );
        let e = workspace_url("http://dbc-1.cloud.databricks.com").unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("must use https"), "{}", e.text());
    }

    fn names(routes: &[Route]) -> Vec<&'static str> {
        routes.iter().map(Route::name).collect()
    }

    fn values(pairs: &[(&str, &str)]) -> Values {
        Values::from_env(&Env::from_pairs(pairs)).unwrap()
    }

    #[test]
    fn routes_follow_the_unified_auth_order() {
        let host = "https://dbc-1.cloud.databricks.com";
        let azure = "https://adb-1.2.azuredatabricks.net";
        let pat_and_m2m = values(&[
            ("DATABRICKS_TOKEN", "t"),
            ("DATABRICKS_CLIENT_ID", "c"),
            ("DATABRICKS_CLIENT_SECRET", "s"),
        ]);
        assert_eq!(names(&routes(&pat_and_m2m, host).unwrap()), ["pat"]);
        let m2m = values(&[
            ("DATABRICKS_CLIENT_ID", "c"),
            ("DATABRICKS_CLIENT_SECRET", "s"),
        ]);
        assert_eq!(names(&routes(&m2m, host).unwrap()), ["oauth-m2m"]);
        let id_only = values(&[("DATABRICKS_CLIENT_ID", "c")]);
        assert_eq!(names(&routes(&id_only, host).unwrap()), ["databricks-cli"]);
        assert_eq!(
            names(&routes(&Values::default(), azure).unwrap()),
            ["databricks-cli", "azure"]
        );
        assert_eq!(
            names(&routes(&Values::default(), "https://x.databricks.azure.us:443").unwrap()),
            ["databricks-cli", "azure"]
        );
    }

    #[test]
    fn an_auth_type_forces_one_route_or_is_refused() {
        let host = "https://dbc-1.cloud.databricks.com";
        let with = |t: &str, more: &[(&str, &str)]| {
            let mut pairs = vec![("DATABRICKS_AUTH_TYPE", t)];
            pairs.extend_from_slice(more);
            routes(&values(&pairs), host)
        };
        let all = [
            ("DATABRICKS_TOKEN", "t"),
            ("DATABRICKS_CLIENT_ID", "c"),
            ("DATABRICKS_CLIENT_SECRET", "s"),
        ];
        assert_eq!(names(&with("oauth-m2m", &all).unwrap()), ["oauth-m2m"]);
        assert_eq!(
            names(&with("databricks-cli", &all).unwrap()),
            ["databricks-cli"]
        );
        assert_eq!(names(&with("azure-cli", &all).unwrap()), ["azure"]);
        assert_eq!(names(&with("azure-msi", &[]).unwrap()), ["azure"]);
        assert_eq!(names(&with("pat", &all).unwrap()), ["pat"]);
        for (t, more) in [
            ("pat", &[][..]),
            ("oauth-m2m", &all[..1]),
            ("basic", &all[..]),
        ] {
            let e = with(t, more).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{t}");
            assert!(e.text().contains("DATABRICKS_"), "{}", e.text());
        }
    }

    #[test]
    fn the_ini_reader_keeps_sections_and_drops_comments() {
        let ini = parse_ini(
            "; top\nhost = https://a\n[dev]\nhost = https://b # inline\ntoken=dapi;x\n# c\n[__settings__]\ndefault_profile = dev\n",
        );
        assert_eq!(ini["DEFAULT"]["host"], "https://a");
        assert_eq!(ini["dev"]["host"], "https://b");
        assert_eq!(ini["dev"]["token"], "dapi;x");
        assert_eq!(ini["__settings__"]["default_profile"], "dev");
    }

    fn file(cfg: &'static str) -> impl Fn(&Path) -> std::io::Result<String> {
        move |p: &Path| {
            if p == Path::new("/home/a/.databrickscfg") {
                Ok(cfg.to_string())
            } else {
                Err(std::io::ErrorKind::NotFound.into())
            }
        }
    }

    #[test]
    fn a_profile_fills_what_the_environment_leaves_unset() {
        let cfg = file(
            "[DEFAULT]\nhost = dbc-default.cloud.databricks.com\ntoken = file-token\n[dev]\nhost = dbc-dev.cloud.databricks.com\nauth_type = databricks-cli\n",
        );
        let with = |pairs: &[(&str, &str)]| {
            let mut all = vec![("HOME", "/home/a")];
            all.extend_from_slice(pairs);
            settings_with(&req(Some(EP)), &Env::from_pairs(&all), &cfg)
        };
        let s = with(&[]).unwrap();
        assert_eq!(s.host, "https://dbc-default.cloud.databricks.com");
        assert_eq!(s.profile.as_deref(), Some("DEFAULT"));
        assert_eq!(names(&s.routes), ["pat"]);
        let s = with(&[("DATABRICKS_CONFIG_PROFILE", "dev")]).unwrap();
        assert_eq!(s.host, "https://dbc-dev.cloud.databricks.com");
        assert_eq!(s.profile.as_deref(), Some("dev"));
        assert_eq!(names(&s.routes), ["databricks-cli"]);
        let s = with(&[("DATABRICKS_HOST", "https://env.cloud.databricks.com")]).unwrap();
        assert_eq!(s.host, "https://env.cloud.databricks.com");
        assert!(s.profile.is_none());
        assert_eq!(names(&s.routes), ["databricks-cli"]);
        let e = with(&[("DATABRICKS_CONFIG_PROFILE", "prod")]).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("has no prod profile"), "{}", e.text());
        let e = with(&[
            ("DATABRICKS_CONFIG_PROFILE", "dev"),
            ("DATABRICKS_CONFIG_FILE", "/elsewhere"),
        ])
        .unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("/elsewhere"), "{}", e.text());
    }

    #[test]
    fn the_settings_section_names_the_default_profile() {
        let cfg = file("[a]\nhost = a.cloud.databricks.com\n[__settings__]\ndefault_profile = a\n");
        let s = settings_with(
            &req(Some(EP)),
            &Env::from_pairs(&[("HOME", "/home/a")]),
            &cfg,
        )
        .unwrap();
        assert_eq!(s.host, "https://a.cloud.databricks.com");
        assert_eq!(s.profile.as_deref(), Some("a"));
    }

    #[test]
    fn no_host_anywhere_is_refused_before_connecting() {
        let e = settings_with(
            &req(Some(EP)),
            &Env::from_pairs(&[("HOME", "/home/a")]),
            file(""),
        )
        .unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("DATABRICKS_HOST"), "{}", e.text());
    }

    #[test]
    fn the_credential_answer_is_read_as_the_api_reference_shapes_it() {
        let c =
            parse_credential(r#"{"token":"eyJ.a.b","expire_time":"2026-11-11T10:59:59.123456Z"}"#)
                .unwrap();
        assert_eq!(c.value.expose(), "eyJ.a.b");
        assert_eq!(
            c.expires,
            Some("2026-11-11T10:59:59.123456Z".parse().unwrap())
        );
        assert!(
            parse_credential(r#"{"token":"t"}"#)
                .unwrap()
                .expires
                .is_none()
        );
        let err = |body: &str| parse_credential(body).err().unwrap().to_string();
        assert_eq!(
            err(r#"{"expire_time":"2026-11-11T10:00:00Z"}"#),
            "the answer has no token"
        );
        assert!(
            err(r#"{"token":"t","expire_time":"soon"}"#)
                .starts_with("invalid expire_time \"soon\": ")
        );
        assert_eq!(err("<html>"), "the answer is not JSON");
    }

    #[test]
    fn refusals_name_the_cause_and_never_the_token() {
        let body = r#"{"error_code":"PERMISSION_DENIED","message":"no access"}"#;
        let ep = EP;
        let h = "https://dbc-1.cloud.databricks.com";
        let r401 = refusal(StatusCode::UNAUTHORIZED, ep, h, "{}");
        assert!(
            r401.contains("did not accept the sign-in (401 Unauthorized)"),
            "{r401}"
        );
        let r403 = refusal(StatusCode::FORBIDDEN, ep, h, body);
        assert!(
            r403.contains(&format!("credential for {ep} (403 Forbidden): no access")),
            "{r403}"
        );
        assert!(r403.contains("check the token"), "{r403}");
        let r404 = refusal(StatusCode::NOT_FOUND, ep, h, "not json");
        assert!(r404.contains("has no Lakebase endpoint"), "{r404}");
        assert!(r404.contains("--databricks-endpoint"), "{r404}");
        let r503 = refusal(StatusCode::SERVICE_UNAVAILABLE, ep, h, "");
        assert!(
            r503.contains("(503 Service Unavailable), after 3 attempts"),
            "{r503}"
        );
        assert!(retryable(StatusCode::TOO_MANY_REQUESTS) && retryable(StatusCode::BAD_GATEWAY));
        assert!(!retryable(StatusCode::FORBIDDEN));
    }

    #[test]
    fn debug_shows_no_secret() {
        let signin = Signin {
            settings: Settings {
                host: "https://h".into(),
                endpoint: EP.into(),
                profile: None,
                routes: vec![
                    Route::Pat(Secret::from("dapi-secret")),
                    Route::M2m {
                        client_id: "c".into(),
                        secret: Secret::from("m2m-secret"),
                    },
                ],
            },
            azure: azure::Signin::new(Options::default(), Env::default(), azure::DATABRICKS),
            pinned: OnceLock::new(),
        };
        let source = Lakebase::holding(
            signin,
            Fetched {
                value: Secret::from("lakebase-secret"),
                expires: None,
            },
        );
        let settings = &source.mint_source().settings;
        let text = format!("{source:?} {settings:?} {:?}", settings.routes);
        for secret in ["dapi-secret", "m2m-secret", "lakebase-secret"] {
            assert!(!text.contains(secret), "{text}");
        }
    }
}
