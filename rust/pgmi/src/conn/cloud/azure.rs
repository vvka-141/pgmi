use std::future::Future;
use std::process::Stdio;
use std::sync::{Arc, OnceLock};
use std::time::Duration;

use azure_core::credentials::{Secret, TokenCredential};
use azure_identity::{
    AzureCliCredential, AzureCliCredentialOptions, AzureDeveloperCliCredential,
    AzureDeveloperCliCredentialOptions, ClientAssertionCredential, ClientSecretCredential,
    ManagedIdentityCredential, ManagedIdentityCredentialOptions, UserAssignedId,
    WorkloadIdentityCredential, WorkloadIdentityCredentialOptions,
};
use base64ct::{Base64, Base64UrlUnpadded, Encoding};

use super::Options;
use super::renew::{Fetched, Mint};
use crate::env::Env;
use crate::error::{Error, Result};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Audience {
    scope: &'static str,
    resource: &'static str,
}

pub(crate) const POSTGRES: Audience = Audience {
    scope: "https://ossrdbms-aad.database.windows.net/.default",
    resource: "https://ossrdbms-aad.database.windows.net",
};

pub(crate) const DATABRICKS: Audience = Audience {
    scope: "2ff814a6-3304-4ab8-85cb-cd0e6f879c1d/.default",
    resource: "2ff814a6-3304-4ab8-85cb-cd0e6f879c1d",
};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Kind {
    Secret,
    Certificate,
    Workload,
    Managed,
    Cli,
    DeveloperCli,
    PowerShell,
}

impl Kind {
    fn name(self) -> &'static str {
        match self {
            Kind::Secret => "ClientSecretCredential",
            Kind::Certificate => "ClientCertificateCredential",
            Kind::Workload => "WorkloadIdentityCredential",
            Kind::Managed => "ManagedIdentityCredential",
            Kind::Cli => "AzureCLICredential",
            Kind::DeveloperCli => "AzureDeveloperCLICredential",
            Kind::PowerShell => "AzurePowerShellCredential",
        }
    }
}

#[derive(Clone, Debug)]
struct Ids {
    tenant: Option<String>,
    client: Option<String>,
}

fn ids(req: &Options, env: &Env) -> Result<Ids> {
    let pick = |flag: &Option<String>, name: &str| -> Result<Option<String>> {
        Ok(match flag.as_deref().filter(|v| !v.is_empty()) {
            Some(v) => Some(v.to_string()),
            None => env.non_empty(name)?.map(str::to_string),
        })
    };
    let tenant = pick(&req.tenant_id, "AZURE_TENANT_ID")?;
    if let Some(t) = &tenant {
        tenant_id(t)?;
    }
    Ok(Ids {
        tenant,
        client: pick(&req.client_id, "AZURE_CLIENT_ID")?,
    })
}

/// Refuses a tenant id that is not ASCII letters, digits, `.` and `-`, as
/// the Azure SDKs do: it reaches a token URL and a PowerShell process.
pub(super) fn tenant_id(t: &str) -> Result<()> {
    if t.bytes()
        .all(|b| b.is_ascii_alphanumeric() || b == b'.' || b == b'-')
    {
        return Ok(());
    }
    Err(Error::config(
        "invalid Azure tenant id: use only letters, digits, '.' and '-' (a directory GUID or domain)",
    ))
}

/// The credentials to try in order: those the settings name, or the
/// default chain, which probes for a managed identity and reports as
/// `DefaultAzureCredential`.
enum Plan {
    Named(Vec<Kind>),
    Chain(Vec<Kind>),
}

impl Plan {
    fn kinds(&self) -> &[Kind] {
        match self {
            Plan::Named(k) | Plan::Chain(k) => k,
        }
    }
}

fn plan(ids: &Ids, env: &Env) -> Result<Plan> {
    let set = |n: &str| env.non_empty(n).map(|v| v.is_some());
    if ids.tenant.is_some() && ids.client.is_some() {
        if set("AZURE_CLIENT_SECRET")? {
            return Ok(Plan::Named(vec![Kind::Secret]));
        }
        if set("AZURE_CLIENT_CERTIFICATE_PATH")? {
            return Ok(Plan::Named(vec![Kind::Certificate]));
        }
    }
    let federated = set("AZURE_FEDERATED_TOKEN_FILE")?;
    if ids.client.is_some() {
        let mut kinds = Vec::new();
        if federated {
            kinds.push(Kind::Workload);
        }
        kinds.push(Kind::Managed);
        return Ok(Plan::Named(kinds));
    }
    let mut chain = Vec::new();
    if federated && ids.tenant.is_some() {
        chain.push(Kind::Workload);
    }
    chain.extend([
        Kind::Managed,
        Kind::Cli,
        Kind::DeveloperCli,
        Kind::PowerShell,
    ]);
    let Some(selected) = env.non_empty("AZURE_TOKEN_CREDENTIALS")? else {
        return Ok(Plan::Chain(chain));
    };
    let lower = selected.to_ascii_lowercase();
    let picked: Vec<Kind> = match lower.as_str() {
        "dev" => vec![Kind::Cli, Kind::DeveloperCli, Kind::PowerShell],
        "prod" => vec![Kind::Workload, Kind::Managed],
        "environmentcredential" => vec![Kind::Secret, Kind::Certificate],
        other => chain
            .iter()
            .chain(&[Kind::Workload])
            .copied()
            .filter(|k| k.name().to_ascii_lowercase() == other)
            .take(1)
            .collect(),
    };
    if picked.is_empty() {
        return Err(Error::config(format!(
            "invalid AZURE_TOKEN_CREDENTIALS value {selected:?}. Valid values are \"dev\", \"prod\", or the name of any credential type in the default chain"
        )));
    }
    Ok(Plan::Chain(picked))
}

/// Why one credential of the chain gave no token, worded as the Azure SDKs
/// word it.
#[derive(Debug)]
pub(crate) enum CredentialError {
    Sdk(azure_core::Error),
    Token(azure_core::Error),
    NoManagedIdentityResponse,
    Msi(super::azure_msi::MsiError),
    PowerShellMissing,
    PowerShellSpawn(std::io::Error),
    PowerShellTimedOut,
    PowerShellWait(std::io::Error),
    AzAccountsMissing,
    PowerShellFailed(String),
    PowerShellNoToken,
    Certificate(certificate::CertificateError),
}

impl std::fmt::Display for CredentialError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CredentialError::Sdk(e) => e.fmt(f),
            CredentialError::Token(e) => f.write_str(&e.to_string().replace('\n', " ")),
            CredentialError::NoManagedIdentityResponse => {
                f.write_str("no response from the managed identity endpoint")
            }
            CredentialError::Msi(e) => e.fmt(f),
            CredentialError::PowerShellMissing => f.write_str("executable not found on path"),
            CredentialError::PowerShellSpawn(e) | CredentialError::PowerShellWait(e) => e.fmt(f),
            CredentialError::PowerShellTimedOut => f.write_str("Azure PowerShell timed out"),
            CredentialError::AzAccountsMissing => f.write_str("Az.Accounts module not found"),
            CredentialError::PowerShellFailed(stderr) => f.write_str(stderr),
            CredentialError::PowerShellNoToken => f.write_str("Azure PowerShell printed no token"),
            CredentialError::Certificate(e) => e.fmt(f),
        }
    }
}

impl std::error::Error for CredentialError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            CredentialError::Sdk(e) | CredentialError::Token(e) => Some(e),
            CredentialError::Msi(e) => Some(e),
            CredentialError::PowerShellSpawn(e) | CredentialError::PowerShellWait(e) => Some(e),
            CredentialError::Certificate(e) => Some(e),
            _ => None,
        }
    }
}

/// Signs in to Entra ID for one audience; each connection asks again unless
/// the last token has more than five minutes left (Go asks per connection).
/// The first credential of the chain that gives a token is kept, so every
/// later token comes from the same identity, as Go's `DefaultAzureCredential`
/// does.
pub(crate) struct Signin {
    request: Options,
    env: Env,
    audience: Audience,
    pinned: OnceLock<Kind>,
    assertion: tokio::sync::OnceCell<certificate::Assertion>,
}

impl Signin {
    pub(crate) fn new(request: Options, env: Env, audience: Audience) -> Self {
        Signin {
            request,
            env,
            audience,
            pinned: OnceLock::new(),
            assertion: tokio::sync::OnceCell::new(),
        }
    }

    /// The client certificate's signing key, read and decoded (a PKCS#12
    /// key derivation) once, on a blocking thread.
    async fn assertion(
        &self,
        ids: &Ids,
    ) -> std::result::Result<certificate::Assertion, certificate::CertificateError> {
        self.assertion
            .get_or_try_init(|| {
                let (ids, env) = (ids.clone(), self.env.clone());
                async move {
                    crate::blocking::spawn(move || certificate::Assertion::load(&ids, &env))
                        .await
                        .map_err(certificate::CertificateError::Task)?
                }
            })
            .await
            .cloned()
    }

    async fn sign_in<G, F>(&self, ids: &Ids, get: G) -> Result<Fetched>
    where
        G: Fn(Kind) -> F,
        F: Future<Output = std::result::Result<Fetched, CredentialError>>,
    {
        if let Some(&kind) = self.pinned.get() {
            return get(kind).await.map_err(|e| {
                Error::connection(format!("failed to acquire Azure token: {}", kind.name()))
                    .with_source(e)
            });
        }
        let plan = plan(ids, &self.env)?;
        let probing = matches!(plan, Plan::Chain(_))
            && self.env.non_empty("IDENTITY_ENDPOINT")?.is_none()
            && self.env.non_empty("MSI_ENDPOINT")?.is_none();
        let mut failures = Vec::new();
        let mut unavailable = None;
        for &kind in plan.kinds() {
            let attempt = get(kind);
            let result = if kind == Kind::Managed && probing {
                match tokio::time::timeout(Duration::from_secs(2), attempt).await {
                    Ok(r) => r,
                    Err(_) => Err(CredentialError::NoManagedIdentityResponse),
                }
            } else {
                attempt.await
            };
            match result {
                Ok(t) => {
                    let _ = self.pinned.set(kind);
                    return Ok(t);
                }
                Err(e) => {
                    failures.push(format!("\t{}: {e}", kind.name()));
                    if unavailable.is_none() && super::unavailable_within(&e) {
                        unavailable = Some(e);
                    }
                }
            }
        }
        let head = match plan {
            Plan::Chain(_) => {
                "DefaultAzureCredential: failed to acquire a token.\nAttempted credentials:"
            }
            Plan::Named(_) => "failed to acquire a token:",
        };
        let e = Error::connection(format!(
            "failed to acquire Azure token: {head}\n{}",
            failures.join("\n")
        ));
        Err(match unavailable {
            Some(cause) => e.with_cause(cause),
            None => e,
        })
    }
}

impl std::fmt::Debug for Signin {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Signin")
    }
}

impl Mint for Signin {
    type Value = crate::error::Secret;

    async fn mint(&self) -> Result<Fetched> {
        let ids = ids(&self.request, &self.env)?;
        self.sign_in(&ids, |kind| get(kind, &ids, self)).await
    }
}

fn expiry(unix_seconds: i64) -> Option<jiff::Timestamp> {
    jiff::Timestamp::from_second(unix_seconds).ok()
}

async fn get(
    kind: Kind,
    ids: &Ids,
    signin: &Signin,
) -> std::result::Result<Fetched, CredentialError> {
    let (env, audience) = (&signin.env, signin.audience);
    let text = CredentialError::Sdk;
    let credential: Arc<dyn TokenCredential> = match kind {
        Kind::PowerShell => return powershell(ids.tenant.as_deref(), audience).await,
        Kind::Secret => {
            let secret = env
                .non_empty("AZURE_CLIENT_SECRET")
                .ok()
                .flatten()
                .unwrap_or_default();
            ClientSecretCredential::new(
                ids.tenant.as_deref().unwrap_or_default(),
                ids.client.clone().unwrap_or_default(),
                Secret::new(secret.to_string()),
                None,
            )
            .map_err(text)?
        }
        Kind::Certificate => {
            let assertion = signin
                .assertion(ids)
                .await
                .map_err(CredentialError::Certificate)?;
            ClientAssertionCredential::new(
                ids.tenant.clone().unwrap_or_default(),
                ids.client.clone().unwrap_or_default(),
                assertion,
                None,
            )
            .map_err(text)?
        }
        Kind::Workload => {
            WorkloadIdentityCredential::new(Some(WorkloadIdentityCredentialOptions {
                client_id: ids.client.clone(),
                tenant_id: ids.tenant.clone(),
                ..Default::default()
            }))
            .map_err(text)?
        }
        Kind::Managed => {
            if let Some(source) = super::azure_msi::source(env) {
                return super::azure_msi::token(
                    source,
                    ids.client.as_deref(),
                    audience.resource,
                    env,
                    &super::azure_msi::RETRY,
                )
                .await
                .map_err(CredentialError::Msi);
            }
            ManagedIdentityCredential::new(Some(ManagedIdentityCredentialOptions {
                user_assigned_id: ids.client.clone().map(UserAssignedId::ClientId),
                ..Default::default()
            }))
            .map_err(text)?
        }
        Kind::Cli => AzureCliCredential::new(Some(AzureCliCredentialOptions {
            tenant_id: ids.tenant.clone(),
            ..Default::default()
        }))
        .map_err(text)?,
        Kind::DeveloperCli => {
            AzureDeveloperCliCredential::new(Some(AzureDeveloperCliCredentialOptions {
                tenant_id: ids.tenant.clone(),
                ..Default::default()
            }))
            .map_err(text)?
        }
    };
    credential
        .get_token(&[audience.scope], None)
        .await
        .map(|t| Fetched {
            value: crate::error::Secret::from(t.token.secret()),
            expires: expiry(t.expires_on.unix_timestamp()),
        })
        .map_err(CredentialError::Token)
}

const TENANT_VARIABLE: &str = "PGMI_AZURE_TENANT_ID";

fn powershell_script(tenant: bool, resource: &str) -> String {
    let tenant = if tenant {
        format!("$params['TenantId'] = $env:{TENANT_VARIABLE}\n")
    } else {
        String::new()
    };
    format!(
        "$ErrorActionPreference = 'Stop'\n\
$m = Import-Module Az.Accounts -MinimumVersion 2.2.0 -PassThru -ErrorAction SilentlyContinue\n\
if (! $m) {{ Write-Output 'VersionCheckFailed'; exit }}\n\
$params = @{{ ResourceUrl = '{resource}'; WarningAction = 'Ignore' }}\n\
{tenant}$token = Get-AzAccessToken @params\n\
$value = $token.Token\n\
if ($value -is [System.Security.SecureString]) {{ $value = $value | ConvertFrom-SecureString -AsPlainText }}\n\
$result = New-Object -TypeName psobject\n\
$result | Add-Member -MemberType NoteProperty -Name Token -Value $value\n\
$result | Add-Member -MemberType NoteProperty -Name ExpiresOn -Value $token.ExpiresOn.ToUnixTimeSeconds()\n\
Write-Output (ConvertTo-Json $result -Compress)\n"
    )
}

async fn powershell(
    tenant: Option<&str>,
    audience: Audience,
) -> std::result::Result<Fetched, CredentialError> {
    let script = powershell_script(tenant.is_some(), audience.resource);
    let utf16: Vec<u8> = script.encode_utf16().flat_map(u16::to_le_bytes).collect();
    let encoded = Base64::encode_string(&utf16);
    let mut command = tokio::process::Command::new("pwsh");
    command.env_remove(TENANT_VARIABLE);
    if let Some(t) = tenant {
        command.env(TENANT_VARIABLE, t);
    }
    let child = command
        .args([
            "-NoProfile",
            "-NonInteractive",
            "-OutputFormat",
            "Text",
            "-EncodedCommand",
            &encoded,
        ])
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true)
        .spawn()
        .map_err(|e| match e.kind() {
            std::io::ErrorKind::NotFound => CredentialError::PowerShellMissing,
            _ => CredentialError::PowerShellSpawn(e),
        })?;
    let out = tokio::time::timeout(Duration::from_secs(30), child.wait_with_output())
        .await
        .map_err(|_| CredentialError::PowerShellTimedOut)?
        .map_err(CredentialError::PowerShellWait)?;
    let stdout = String::from_utf8_lossy(&out.stdout);
    if stdout.contains("VersionCheckFailed") {
        return Err(CredentialError::AzAccountsMissing);
    }
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr);
        return Err(CredentialError::PowerShellFailed(stderr.trim().to_string()));
    }
    let value: serde_json::Value = stdout
        .lines()
        .rev()
        .find_map(|l| serde_json::from_str(l.trim()).ok())
        .ok_or(CredentialError::PowerShellNoToken)?;
    let token = value
        .get("Token")
        .and_then(|t| t.as_str())
        .ok_or(CredentialError::PowerShellNoToken)?;
    Ok(Fetched {
        value: crate::error::Secret::from(token),
        expires: value
            .get("ExpiresOn")
            .and_then(serde_json::Value::as_i64)
            .and_then(expiry),
    })
}

mod certificate {
    use std::time::SystemTime;

    use azure_core::http::ClientMethodOptions;
    use azure_identity::ClientAssertion;
    use rustls::SignatureScheme;
    use rustls_pki_types::pem::PemObject;
    use rustls_pki_types::{CertificateDer, PrivateKeyDer};
    use sha2::Digest;

    use super::{Base64UrlUnpadded, Encoding, Ids};
    use crate::env::Env;

    /// Why the certificate in `AZURE_CLIENT_CERTIFICATE_PATH` cannot sign in.
    #[derive(Debug)]
    pub(crate) enum CertificateError {
        Read {
            path: String,
            error: std::io::Error,
        },
        Pem(rustls_pki_types::pem::Error),
        NoPemKey(rustls_pki_types::pem::Error),
        Pkcs12(String),
        NoKey,
        NoCertificate,
        Key(rustls::Error),
        In {
            path: String,
            error: Box<CertificateError>,
        },
        NotRsa,
        Sign(rustls::Error),
        Task(tokio::task::JoinError),
    }

    impl std::fmt::Display for CertificateError {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            match self {
                CertificateError::Read { path, error } => write!(f, "read {path}: {error}"),
                CertificateError::Pem(e) => e.fmt(f),
                CertificateError::NoPemKey(e) => write!(f, "no PEM private key found ({e})"),
                CertificateError::Pkcs12(e) => write!(
                    f,
                    "neither a PEM certificate nor a PKCS#12 file this password opens ({e})"
                ),
                CertificateError::NoKey => f.write_str("the PKCS#12 file holds no private key"),
                CertificateError::NoCertificate => {
                    f.write_str("the PKCS#12 file holds no certificate for its key")
                }
                CertificateError::Key(e) | CertificateError::Sign(e) => e.fmt(f),
                CertificateError::In { path, error } => write!(f, "{path}: {error}"),
                CertificateError::NotRsa => f.write_str("the certificate key is not an RSA key"),
                CertificateError::Task(e) => write!(f, "reading the certificate: {e}"),
            }
        }
    }

    impl std::error::Error for CertificateError {
        fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
            match self {
                CertificateError::Read { error, .. } => Some(error),
                CertificateError::Pem(e) | CertificateError::NoPemKey(e) => Some(e),
                CertificateError::Key(e) | CertificateError::Sign(e) => Some(e),
                CertificateError::In { error, .. } => Some(error.as_ref()),
                CertificateError::Task(e) => Some(e),
                _ => None,
            }
        }
    }

    #[derive(Clone)]
    pub(crate) struct Assertion {
        key: std::sync::Arc<dyn rustls::sign::SigningKey>,
        thumbprint: String,
        audience: String,
        client: String,
    }

    impl std::fmt::Debug for Assertion {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            f.write_str("Assertion")
        }
    }

    pub(crate) fn read(
        bytes: &[u8],
        password: &str,
    ) -> std::result::Result<(CertificateDer<'static>, PrivateKeyDer<'static>), CertificateError>
    {
        if let Some(cert) = CertificateDer::pem_slice_iter(bytes).next() {
            let cert = cert.map_err(CertificateError::Pem)?;
            let key = PrivateKeyDer::from_pem_slice(bytes).map_err(CertificateError::NoPemKey)?;
            return Ok((cert, key));
        }
        let store = p12_keystore::KeyStore::from_pkcs12(
            bytes,
            password,
            p12_keystore::Pkcs12ImportPolicy::Relaxed,
        )
        .map_err(|e| CertificateError::Pkcs12(e.to_string()))?;
        let (_, chain) = store.private_key_chain().ok_or(CertificateError::NoKey)?;
        let cert = chain
            .certs()
            .first()
            .ok_or(CertificateError::NoCertificate)?;
        Ok((
            CertificateDer::from(cert.as_der().to_vec()),
            PrivateKeyDer::Pkcs8(chain.key().as_der().to_vec().into()),
        ))
    }

    impl Assertion {
        pub(crate) fn load(ids: &Ids, env: &Env) -> std::result::Result<Self, CertificateError> {
            let path = env
                .non_empty("AZURE_CLIENT_CERTIFICATE_PATH")
                .ok()
                .flatten()
                .unwrap_or_default()
                .to_string();
            let bytes = std::fs::read(&path).map_err(|error| CertificateError::Read {
                path: path.clone(),
                error,
            })?;
            let within = |error| CertificateError::In {
                path: path.clone(),
                error: Box::new(error),
            };
            let password = env
                .non_empty("AZURE_CLIENT_CERTIFICATE_PASSWORD")
                .ok()
                .flatten()
                .unwrap_or_default();
            let (cert, key) = read(&bytes, password).map_err(within)?;
            let key = crate::conn::tls::provider()
                .key_provider
                .load_private_key(key)
                .map_err(|e| within(CertificateError::Key(e)))?;
            let authority = env
                .non_empty("AZURE_AUTHORITY_HOST")
                .ok()
                .flatten()
                .unwrap_or("https://login.microsoftonline.com")
                .trim_end_matches('/')
                .to_string();
            let tenant = ids.tenant.clone().unwrap_or_default();
            Ok(Self {
                key,
                thumbprint: Base64UrlUnpadded::encode_string(&sha2::Sha256::digest(cert.as_ref())),
                audience: format!("{authority}/{tenant}/oauth2/v2.0/token"),
                client: ids.client.clone().unwrap_or_default(),
            })
        }

        fn sign(&self) -> std::result::Result<String, CertificateError> {
            let now = SystemTime::now()
                .duration_since(SystemTime::UNIX_EPOCH)
                .map(|d| d.as_secs())
                .unwrap_or_default();
            let header =
                serde_json::json!({"alg": "RS256", "typ": "JWT", "x5t#S256": self.thumbprint});
            let claims = serde_json::json!({
                "aud": self.audience,
                "iss": self.client,
                "sub": self.client,
                "jti": uuid::Uuid::new_v4().to_string(),
                "nbf": now,
                "iat": now,
                "exp": now + 600,
            });
            let input = format!(
                "{}.{}",
                Base64UrlUnpadded::encode_string(header.to_string().as_bytes()),
                Base64UrlUnpadded::encode_string(claims.to_string().as_bytes())
            );
            let signer = self
                .key
                .choose_scheme(&[SignatureScheme::RSA_PKCS1_SHA256])
                .ok_or(CertificateError::NotRsa)?;
            let signature = signer
                .sign(input.as_bytes())
                .map_err(CertificateError::Sign)?;
            Ok(format!(
                "{input}.{}",
                Base64UrlUnpadded::encode_string(&signature)
            ))
        }
    }

    #[async_trait::async_trait]
    impl ClientAssertion for Assertion {
        async fn secret(&self, _: Option<ClientMethodOptions<'_>>) -> azure_core::Result<String> {
            self.sign().map_err(|e| {
                azure_core::Error::with_message(
                    azure_core::error::ErrorKind::Credential,
                    e.to_string(),
                )
            })
        }
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        const PEM: &[u8] = include_bytes!("testdata/sp.pem");

        #[test]
        fn pem_and_pkcs12_give_the_same_certificate_and_a_working_key() {
            crate::conn::tls::install();
            let (want, _) = read(PEM, "").unwrap();
            for (bytes, password) in [
                (PEM, ""),
                (&include_bytes!("testdata/sp-aes.p12")[..], "p12-pass"),
                (&include_bytes!("testdata/sp-3des.p12")[..], "p12-pass"),
                (&include_bytes!("testdata/sp-nopass.p12")[..], ""),
            ] {
                let (cert, key) = read(bytes, password).unwrap();
                assert_eq!(cert, want);
                let assertion = Assertion {
                    key: crate::conn::tls::provider()
                        .key_provider
                        .load_private_key(key)
                        .unwrap(),
                    thumbprint: String::new(),
                    audience: "a".into(),
                    client: "c".into(),
                };
                assert_eq!(assertion.sign().unwrap().split('.').count(), 3);
            }
            let wrong = read(include_bytes!("testdata/sp-aes.p12"), "nope")
                .unwrap_err()
                .to_string();
            assert!(wrong.contains("PKCS#12"), "{wrong}");
        }

        #[test]
        fn a_pfx_with_an_absurd_mac_iteration_count_is_refused_at_once() {
            use x509_cert::der::{Decode, Encode};
            let mut pfx =
                pkcs12::pfx::Pfx::from_der(include_bytes!("testdata/sp-aes.p12")).unwrap();
            pfx.mac_data.as_mut().unwrap().iterations = i32::MAX;
            let absurd = pfx.to_der().unwrap();
            let started = std::time::Instant::now();
            let e = read(&absurd, "p12-pass").unwrap_err().to_string();
            assert!(e.contains("PKCS#12"), "{e}");
            assert!(started.elapsed() < std::time::Duration::from_secs(1));
        }
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use super::*;

    #[test]
    fn a_tenant_outside_letters_digits_dots_and_dashes_is_refused_before_any_sign_in() {
        for bad in [
            "x\u{2019}; New-Item PWNED; \u{2019}",
            "x'; y",
            "a b",
            "a/b",
            "t\u{0}",
        ] {
            let e = tenant_id(bad).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{bad:?}");
            let opts = Options {
                tenant_id: Some(bad.into()),
                ..Options::default()
            };
            assert_eq!(ids(&opts, &Env::default()).unwrap_err().exit_code(), 10);
        }
        for good in [
            "72f988bf-86f1-41af-91ab-2d7cd011db47",
            "contoso.onmicrosoft.com",
            "organizations",
        ] {
            tenant_id(good).unwrap();
        }
    }

    #[test]
    fn the_powershell_script_carries_no_value_from_input() {
        let script = powershell_script(true, "https://ossrdbms-aad.database.windows.net");
        assert!(script.contains("$env:PGMI_AZURE_TENANT_ID"), "{script}");
        assert!(
            !powershell_script(false, "r").contains("TenantId"),
            "no tenant line without a tenant"
        );
    }

    fn signin() -> Signin {
        Signin::new(
            Options::default(),
            Env::from_pairs(&[("IDENTITY_ENDPOINT", "http://127.0.0.1:1")]),
            POSTGRES,
        )
    }

    async fn sign_in(s: &Signin, tried: &Mutex<Vec<Kind>>, works: &[Kind]) -> Result<Fetched> {
        let ids = ids(&s.request, &s.env).unwrap();
        s.sign_in(&ids, |kind| {
            tried.lock().unwrap().push(kind);
            let ok = works.contains(&kind);
            async move {
                if ok {
                    Ok(Fetched {
                        value: crate::error::Secret::from(kind.name()),
                        expires: None,
                    })
                } else {
                    Err(CredentialError::PowerShellNoToken)
                }
            }
        })
        .await
    }

    #[tokio::test]
    async fn every_token_after_the_first_comes_from_the_credential_that_worked() {
        let s = signin();
        let tried = Mutex::new(Vec::new());
        let first = sign_in(&s, &tried, &[Kind::Cli, Kind::DeveloperCli])
            .await
            .unwrap();
        assert_eq!(first.value.expose(), "AzureCLICredential");
        assert_eq!(*tried.lock().unwrap(), [Kind::Managed, Kind::Cli]);
        tried.lock().unwrap().clear();
        let again = sign_in(&s, &tried, &[Kind::Managed, Kind::Cli])
            .await
            .unwrap();
        assert_eq!(again.value.expose(), "AzureCLICredential");
        assert_eq!(*tried.lock().unwrap(), [Kind::Cli]);
    }

    #[tokio::test]
    async fn a_pinned_credential_that_fails_is_not_replaced_by_another_identity() {
        let s = signin();
        let tried = Mutex::new(Vec::new());
        sign_in(&s, &tried, &[Kind::Cli]).await.unwrap();
        tried.lock().unwrap().clear();
        let e = sign_in(&s, &tried, &[Kind::Managed]).await.unwrap_err();
        assert_eq!(*tried.lock().unwrap(), [Kind::Cli]);
        assert_eq!(e.exit_code(), 11);
        assert_eq!(
            e.text(),
            "failed to acquire Azure token: AzureCLICredential: Azure PowerShell printed no token"
        );
    }

    #[tokio::test]
    async fn the_client_certificate_is_read_once() {
        crate::conn::tls::install();
        let dir = crate::testdir::TempDir::new("azure-certificate-once");
        let path = dir.join("sp.pem");
        std::fs::write(&path, include_bytes!("testdata/sp.pem")).unwrap();
        let path = path.display().to_string();
        let s = Signin::new(
            Options::default(),
            Env::from_pairs(&[
                ("AZURE_TENANT_ID", "t"),
                ("AZURE_CLIENT_ID", "c"),
                ("AZURE_CLIENT_CERTIFICATE_PATH", &path),
            ]),
            POSTGRES,
        );
        let ids = ids(&s.request, &s.env).unwrap();
        s.assertion(&ids).await.unwrap();
        std::fs::remove_file(&path).unwrap();
        s.assertion(&ids).await.unwrap();
    }

    #[tokio::test]
    async fn nothing_is_pinned_while_no_credential_works() {
        let s = signin();
        let tried = Mutex::new(Vec::new());
        let e = sign_in(&s, &tried, &[]).await.unwrap_err();
        assert!(
            e.text()
                .starts_with("failed to acquire Azure token: DefaultAzureCredential:"),
            "{}",
            e.text()
        );
        tried.lock().unwrap().clear();
        sign_in(&s, &tried, &[Kind::DeveloperCli]).await.unwrap();
        assert_eq!(
            *tried.lock().unwrap(),
            [Kind::Managed, Kind::Cli, Kind::DeveloperCli]
        );
    }
}
