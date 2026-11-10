# 0007: Azure credentials

Status: accepted by the owner with the Entra condition, 2026-11-10. Ticket: PGMI-829.
Checked on crates.io / docs.rs, 2026-11-10: azure_identity 1.0.0
(2026-11-06), azure_core 1.1.0 (2026-11-06), typespec_client_core 1.1.0.
Facts from the docs and feature tables: azure_identity offers
`AzureCliCredential`, `AzureDeveloperCliCredential`,
`DeveloperToolsCredential`, `ManagedIdentityCredential`,
`WorkloadIdentityCredential`, `ClientSecretCredential`,
`ClientCertificateCredential` (needs OpenSSL), `ClientAssertionCredential`,
`AzurePipelinesCredential`, and no default chain. `ManagedIdentityCredential`
covers only App Service and virtual machines (IMDS). azure_core's default
features turn on reqwest's `rustls` feature, which brings aws-lc-rs.

## Context

The contract: without a client ID, the default Azure credential chain (Azure
CLI among others); with a client ID (`--azure-client-id`, else
`AZURE_CLIENT_ID`), workload identity then managed identity; with a tenant
ID, a client ID and `AZURE_CLIENT_SECRET`, a service principal. The token is
asked for `https://ossrdbms-aad.database.windows.net/.default` and sent as
the password. The Rust binary adds certificate login for a service
principal (intended change, owner 2026-11-10).
Azure is the only cloud verified against the real service (PGMI-845).

## Options

1. **azure_identity**, with pgmi building the chain itself (the Rust SDK has
   none).
2. **Hand-written Azure sign-in** for the CLI call, IMDS, App Service,
   workload identity and the client-secret flow. About 600 lines of
   security-sensitive HTTP that Microsoft already maintains.

## Decision

Option 1, with `default-features = false` on both crates (`azure_core`:
`reqwest`, `tokio`; `azure_identity`: `tokio`), and pgmi enabling reqwest's
`rustls-no-provider`, so the ring provider of record 0006 is used and
aws-lc-rs stays out.

pgmi's routing, first match wins:

1. Tenant ID, client ID and `AZURE_CLIENT_SECRET` all set:
   `ClientSecretCredential`. Else tenant ID, client ID and
   `AZURE_CLIENT_CERTIFICATE_PATH`: a service principal with a certificate.
   azure_identity's `ClientCertificateCredential` needs OpenSSL (its
   `client_certificate` feature), which record 0006 bans, so pgmi uses
   `ClientAssertionCredential` with an assertion it signs itself: a JWT
   (RS256, `x5t#S256` header) signed with the certificate's key, the key read
   from PEM or PKCS#12 (`AZURE_CLIENT_CERTIFICATE_PASSWORD`) by pure-Rust
   crates. About 100 lines; the assertion is Microsoft's documented
   certificate-credential format.
2. A client ID: `WorkloadIdentityCredential` when
   `AZURE_FEDERATED_TOKEN_FILE` is set, then `ManagedIdentityCredential` with
   that client ID as a user-assigned identity.
3. Otherwise, in order: workload identity (when its variables are set),
   managed identity (system-assigned), Azure CLI, Azure Developer CLI. This
   is the order of the Azure SDK's `DefaultAzureCredential`, which
   `docs/CLI.md` promises.

Each failure is collected; the final error lists every credential tried, exit
11. The Azure CLI call is bounded by `--timeout`.

**Managed identity hosts the Rust SDK lacks.** The Azure SDK for Go also
covers Azure Arc, Cloud Shell, Service Fabric and Azure Machine Learning.
pgmi adds Cloud Shell (`MSI_ENDPOINT`, one HTTP call) and Azure Arc (a
challenge-file handshake against the local agent) as small sources of its
own, about 120 lines with fakes in the black-box runner, and Service Fabric
and Azure Machine Learning (about 150 lines) for parity, implemented but not
tested against the platform, as AWS and Google are.

## Supported Entra routes and their real-Azure coverage

Entra ID sign-in to Azure Database for PostgreSQL is must-ship (owner,
2026-11-10). PostgreSQL password authentication is a separate path and
always works.

| Route | How pgmi picks it | Go | Rust | Real-Azure run (PGMI-845) |
|---|---|---|---|---|
| Azure CLI (`az login`) | no client ID | yes | yes | yes, both binaries |
| Service principal, secret | tenant + client ID + `AZURE_CLIENT_SECRET` | yes | yes | yes, both binaries |
| Service principal, certificate | tenant + client ID + `AZURE_CLIENT_CERTIFICATE_PATH` | unreachable | yes | yes, Rust only (Go cannot reach it) |
| Managed identity, system-assigned | no client ID, on an Azure host | yes | yes | yes, from an Azure VM, both binaries |
| Managed identity, user-assigned | `--azure-client-id` on an Azure host | yes | yes | yes, same VM, both binaries |
| Workload identity (federated token) | client ID + `AZURE_FEDERATED_TOKEN_FILE` | yes | yes | yes, from GitHub Actions OIDC written to the token file, both binaries |
| Azure Developer CLI | no client ID, after the Azure CLI | yes | yes | no: same token flow as the Azure CLI, from another tool |
| Managed identity on Arc, Cloud Shell, Service Fabric, Azure ML | as system/user-assigned, on that host | yes | yes | no: each needs its platform; covered by fakes where the protocol allows |

## Consequences

- The `cloud/azure-cli` and `cloud/azure-managed-identity` cases carry over
  unchanged; the service principal stays a manual check (instance discovery
  always calls `login.microsoftonline.com`).
- Username/password (`AZURE_USERNAME`/`AZURE_PASSWORD`) is not carried
  over: it needs `AZURE_CLIENT_ID`, which routes away from it, so Go never
  reaches it either (contract, "Nothing that cannot be reached is carried
  over").

## Serves

Cloud sign-in; TLS (token sign-in refuses plaintext sslmodes); Nothing that
cannot be reached is carried over.
