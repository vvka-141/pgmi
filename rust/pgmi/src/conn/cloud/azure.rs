use std::process::Stdio;
use std::sync::Arc;
use std::time::Duration;

use azure_core::credentials::{Secret, TokenCredential};
use azure_identity::{
    AzureCliCredential, AzureCliCredentialOptions, AzureDeveloperCliCredential,
    AzureDeveloperCliCredentialOptions, ClientAssertionCredential, ClientSecretCredential,
    ManagedIdentityCredential, ManagedIdentityCredentialOptions, UserAssignedId,
    WorkloadIdentityCredential, WorkloadIdentityCredentialOptions,
};
use base64::Engine;
use base64::engine::general_purpose::{STANDARD, URL_SAFE_NO_PAD};

use super::Request;
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

struct Ids {
    tenant: Option<String>,
    client: Option<String>,
}

fn ids(req: &Request, env: &Env) -> Result<Ids> {
    let pick = |flag: &Option<String>, name: &str| -> Result<Option<String>> {
        Ok(match flag.as_deref().filter(|v| !v.is_empty()) {
            Some(v) => Some(v.to_string()),
            None => env.non_empty(name)?.map(str::to_string),
        })
    };
    Ok(Ids {
        tenant: pick(&req.tenant_id, "AZURE_TENANT_ID")?,
        client: pick(&req.client_id, "AZURE_CLIENT_ID")?,
    })
}

fn plan(ids: &Ids, env: &Env) -> Result<(Vec<Kind>, bool)> {
    let set = |n: &str| env.non_empty(n).map(|v| v.is_some());
    if ids.tenant.is_some() && ids.client.is_some() {
        if set("AZURE_CLIENT_SECRET")? {
            return Ok((vec![Kind::Secret], false));
        }
        if set("AZURE_CLIENT_CERTIFICATE_PATH")? {
            return Ok((vec![Kind::Certificate], false));
        }
    }
    let federated = set("AZURE_FEDERATED_TOKEN_FILE")?;
    if ids.client.is_some() {
        let mut kinds = Vec::new();
        if federated {
            kinds.push(Kind::Workload);
        }
        kinds.push(Kind::Managed);
        return Ok((kinds, false));
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
        return Ok((chain, true));
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
    Ok((picked, true))
}

pub(crate) async fn token(req: &Request, env: &Env, audience: Audience) -> Result<String> {
    let ids = ids(req, env)?;
    let (kinds, chain) = plan(&ids, env)?;
    let probing = chain
        && env.non_empty("IDENTITY_ENDPOINT")?.is_none()
        && env.non_empty("MSI_ENDPOINT")?.is_none();
    let mut failures = Vec::new();
    for kind in kinds {
        let attempt = get(kind, &ids, env, audience);
        let result = if kind == Kind::Managed && probing {
            match tokio::time::timeout(Duration::from_secs(2), attempt).await {
                Ok(r) => r,
                Err(_) => Err("no response from the managed identity endpoint".into()),
            }
        } else {
            attempt.await
        };
        match result {
            Ok(t) => return Ok(t),
            Err(e) => failures.push(format!("\t{}: {e}", kind.name())),
        }
    }
    let head = if chain {
        "DefaultAzureCredential: failed to acquire a token.\nAttempted credentials:"
    } else {
        "failed to acquire a token:"
    };
    Err(Error::connection(format!(
        "failed to acquire Azure token: {head}\n{}",
        failures.join("\n")
    )))
}

async fn get(
    kind: Kind,
    ids: &Ids,
    env: &Env,
    audience: Audience,
) -> std::result::Result<String, String> {
    let text = |e: azure_core::Error| e.to_string();
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
            let assertion = certificate::Assertion::load(ids, env)?;
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
                )
                .await;
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
        .map(|t| t.token.secret().to_string())
        .map_err(|e| e.to_string().replace('\n', " "))
}

fn powershell_script(tenant: Option<&str>, resource: &str) -> String {
    let tenant = tenant
        .map(|t| format!("$params['TenantId'] = '{}'\n", t.replace('\'', "''")))
        .unwrap_or_default();
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
) -> std::result::Result<String, String> {
    let script = powershell_script(tenant, audience.resource);
    let utf16: Vec<u8> = script.encode_utf16().flat_map(u16::to_le_bytes).collect();
    let encoded = STANDARD.encode(utf16);
    let child = tokio::process::Command::new("pwsh")
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
            std::io::ErrorKind::NotFound => "executable not found on path".to_string(),
            _ => e.to_string(),
        })?;
    let out = tokio::time::timeout(Duration::from_secs(30), child.wait_with_output())
        .await
        .map_err(|_| "Azure PowerShell timed out".to_string())?
        .map_err(|e| e.to_string())?;
    let stdout = String::from_utf8_lossy(&out.stdout);
    if stdout.contains("VersionCheckFailed") {
        return Err("Az.Accounts module not found".into());
    }
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr);
        return Err(stderr.trim().to_string());
    }
    let value: serde_json::Value = stdout
        .lines()
        .rev()
        .find_map(|l| serde_json::from_str(l.trim()).ok())
        .ok_or_else(|| "Azure PowerShell printed no token".to_string())?;
    value
        .get("Token")
        .and_then(|t| t.as_str())
        .map(str::to_string)
        .ok_or_else(|| "Azure PowerShell printed no token".to_string())
}

mod certificate {
    use std::time::SystemTime;

    use azure_core::http::ClientMethodOptions;
    use azure_identity::ClientAssertion;
    use rustls::SignatureScheme;
    use rustls_pki_types::pem::PemObject;
    use rustls_pki_types::{CertificateDer, PrivateKeyDer};
    use sha2::Digest;

    use super::{Ids, URL_SAFE_NO_PAD};
    use crate::env::Env;
    use base64::Engine;

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
    ) -> std::result::Result<(CertificateDer<'static>, PrivateKeyDer<'static>), String> {
        if let Some(cert) = CertificateDer::pem_slice_iter(bytes).next() {
            let cert = cert.map_err(|e| e.to_string())?;
            let key = PrivateKeyDer::from_pem_slice(bytes)
                .map_err(|e| format!("no PEM private key found ({e})"))?;
            return Ok((cert, key));
        }
        let store = p12_keystore::KeyStore::from_pkcs12(
            bytes,
            password,
            p12_keystore::Pkcs12ImportPolicy::Relaxed,
        )
        .map_err(|e| {
            format!("neither a PEM certificate nor a PKCS#12 file this password opens ({e})")
        })?;
        let (_, chain) = store
            .private_key_chain()
            .ok_or("the PKCS#12 file holds no private key")?;
        let cert = chain
            .certs()
            .first()
            .ok_or("the PKCS#12 file holds no certificate for its key")?;
        Ok((
            CertificateDer::from(cert.as_der().to_vec()),
            PrivateKeyDer::Pkcs8(chain.key().as_der().to_vec().into()),
        ))
    }

    impl Assertion {
        pub(crate) fn load(ids: &Ids, env: &Env) -> std::result::Result<Self, String> {
            let path = env
                .non_empty("AZURE_CLIENT_CERTIFICATE_PATH")
                .ok()
                .flatten()
                .unwrap_or_default()
                .to_string();
            let bytes = std::fs::read(&path).map_err(|e| format!("read {path}: {e}"))?;
            let password = env
                .non_empty("AZURE_CLIENT_CERTIFICATE_PASSWORD")
                .ok()
                .flatten()
                .unwrap_or_default();
            let (cert, key) = read(&bytes, password).map_err(|e| format!("{path}: {e}"))?;
            let key = crate::conn::tls::provider()
                .key_provider
                .load_private_key(key)
                .map_err(|e| format!("{path}: {e}"))?;
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
                thumbprint: URL_SAFE_NO_PAD.encode(sha2::Sha256::digest(cert.as_ref())),
                audience: format!("{authority}/{tenant}/oauth2/v2.0/token"),
                client: ids.client.clone().unwrap_or_default(),
            })
        }

        fn sign(&self) -> std::result::Result<String, String> {
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
                URL_SAFE_NO_PAD.encode(header.to_string()),
                URL_SAFE_NO_PAD.encode(claims.to_string())
            );
            let signer = self
                .key
                .choose_scheme(&[SignatureScheme::RSA_PKCS1_SHA256])
                .ok_or("the certificate key is not an RSA key")?;
            let signature = signer.sign(input.as_bytes()).map_err(|e| e.to_string())?;
            Ok(format!("{input}.{}", URL_SAFE_NO_PAD.encode(signature)))
        }
    }

    #[async_trait::async_trait]
    impl ClientAssertion for Assertion {
        async fn secret(&self, _: Option<ClientMethodOptions<'_>>) -> azure_core::Result<String> {
            self.sign().map_err(|e| {
                azure_core::Error::with_message(azure_core::error::ErrorKind::Credential, e)
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
            let wrong = read(include_bytes!("testdata/sp-aes.p12"), "nope").unwrap_err();
            assert!(wrong.contains("PKCS#12"), "{wrong}");
        }
    }
}
