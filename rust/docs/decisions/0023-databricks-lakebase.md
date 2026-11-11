# 0023: Databricks Lakebase sign-in

Status: proposed, 2026-11-11. Ticket: PGMI-849 (supersedes PGMI-761).
Scope decided by the owner on 2026-11-10: new in the Rust binary, done last,
proven on real Lakebase. Builds on 0006 (TLS), 0007 (Azure), 0021 (pure
Rust), 0022 (layout and seams).

## Context

Today a Lakebase deploy needs a `databricks postgres
generate-database-credential` token pasted as the password. It is valid for
one hour, so every unattended run needs a wrapper, and a deploy that
reconnects after the hour (the target database after the maintenance one, a
retry) presents an expired token. A plain Entra token is refused by Lakebase
(lab log, PGMI-761), so `--azure` is no answer. Databricks publishes no Rust
SDK; the community crates say they never ran against a live workspace
(research `research-cloud-auth.md`, section 2.4).

## Facts, checked 2026-11-11

The ticket marked the endpoint and its answer UNVERIFIED. Checked against:

- The REST reference, Postgres API "Generate Database Credential"
  (`https://docs.databricks.com/api/postgres/v1/generate-database-credential.md`,
  GA, API scope `postgres`): `POST /api/2.0/postgres/credentials`; body
  `endpoint` (required,
  `projects/{project_id}/branches/{branch_id}/endpoints/{endpoint_id}`),
  optional `ttl` and `expire_time` (300 s to 1 h), `group_name`, `claims`;
  answer `{"token": string, "expire_time": string}`, `expire_time` a UTC
  timestamp. The older provisioned-instance API,
  `POST /api/2.0/database/credentials`, answers `expiration_time` instead and
  is not used.
- databricks-sdk-go `main`, `service/postgres/impl.go`
  (`GenerateDatabaseCredential`: POST, that path, `Accept` and
  `Content-Type: application/json`, `X-Databricks-Workspace-Id` only when a
  workspace ID is configured) and `service/postgres/model.go`
  (`GenerateDatabaseCredentialRequest`, `DatabaseCredential{ExpireTime
  *time.Time, Token string}`: RFC 3339 in JSON).
- Unified auth order, databricks-sdk-go `config/auth_default.go`
  (`DefaultCredentials`): PAT, basic, OAuth M2M, `databricks-cli` (U2M token
  cache), metadata service, GitHub/Azure DevOps/env/file OIDC, Azure GitHub
  OIDC, Azure MSI, Azure client secret, Azure CLI, Google. PAT and M2M are
  chosen when configured, not tried and passed over.
- OAuth M2M: `config/auth_m2m.go` (skipped without client ID and secret;
  client credentials in the `Authorization` header; scopes default
  `["all-apis"]`, `config/config.go`) and the Databricks docs page
  "Authorize service principal access with OAuth"
  (`https://docs.databricks.com/aws/en/dev-tools/auth/oauth-m2m`):
  `POST https://<workspace>/oidc/v1/token`, `--user id:secret`,
  `grant_type=client_credentials&scope=all-apis`, answer `access_token`,
  `token_type`, `expires_in`.
- The CLI token: `config/cli_token_source.go`: `databricks auth token
  --profile <p>` when a profile is set, else `--host <host>`; JSON
  `access_token`, `token_type`, `expiry`; a missing CLI skips the route.
- Profiles: `config/config_file.go`: `~/.databrickscfg` or
  `DATABRICKS_CONFIG_FILE`; profile from `DATABRICKS_CONFIG_PROFILE`, else
  `[__settings__] default_profile`, else `DEFAULT`; the file is read only
  when a profile is named or the environment configures no host and no
  credential; environment values win over the file.
- Environment names: `config/config.go` (`DATABRICKS_HOST`,
  `DATABRICKS_TOKEN`, `DATABRICKS_CLIENT_ID`, `DATABRICKS_CLIENT_SECRET`,
  `DATABRICKS_AUTH_TYPE`, `DATABRICKS_CONFIG_PROFILE`,
  `DATABRICKS_CONFIG_FILE`).
- Azure Databricks resource: Microsoft Learn "Get Microsoft Entra ID tokens
  manually"
  (`https://learn.microsoft.com/azure/databricks/dev-tools/auth/aad-token-manual`):
  resource `2ff814a6-3304-4ab8-85cb-cd0e6f879c1d`, scope
  `2ff814a6-3304-4ab8-85cb-cd0e6f879c1d/.default`, the Entra token used as
  the workspace bearer.

## Options

1. **A community Databricks crate.** Unproven against a workspace, one
   owner each; rejected in the research.
2. **Shell out to `databricks postgres generate-database-credential`.**
   Needs the CLI everywhere, including CI and jobs.
3. **Hand-written: one REST call and the auth routes pgmi users need.**
   About 500 lines, on the reqwest client the cloud code already shares.

## Decision

Option 3, in `conn/cloud/databricks.rs`.

- Flags `--databricks` and `--databricks-endpoint <NAME>`, the endpoint
  falling back to `PGMI_DATABRICKS_ENDPOINT` (pgmi's prefix, so it never
  collides with a future SDK variable). The endpoint must have the
  resource-name shape; `--databricks-endpoint` alone is a usage error. Not
  in `pgmi.yaml` (`auth_method` stays `azure|aws|google`) and not in `serve`
  for now; both can follow without a design change.
- Settings resolved in `cloud::check`, before connecting, so a missing
  endpoint, a missing or non-`https` host, an unknown `DATABRICKS_AUTH_TYPE`
  or a bad profile is exit 10. The host keeps only its origin. The profile
  reader takes a `read` function (the seam), so unit tests touch no disk.
- Routes, first match (the SDK's order, trimmed): PAT; OAuth M2M; else the
  Databricks CLI, then pgmi's Azure sign-in (record 0007, with the Azure
  Databricks audience) when the host is Azure Databricks
  (`.azuredatabricks.net`, `.databricks.azure.cn`, `.databricks.azure.us`).
  `DATABRICKS_AUTH_TYPE` forces one: `pat`, `oauth-m2m`, `databricks-cli`,
  `azure-cli`/`azure-msi`/`azure-client-secret` (all three mean pgmi's Azure
  chain, which reads `AZURE_*`, not the SDK's `ARM_*`). Left out: basic
  auth, the metadata service, OIDC federation, Google, account hosts and
  `X-Databricks-Workspace-Id`.
- The request: `{"endpoint": ...}` only; the service picks the lifetime (an
  hour today), as the documented CLI path does.
- `Token::Databricks(Arc<Lakebase>)`. `Token::password` became `async`; the
  Lakebase source keeps the current credential and its `expire_time` under a
  `std::sync::Mutex` that is never held across `.await` (two concurrent
  dials may both mint; harmless). A credential is reused while more than
  five minutes are left, else a new one is minted before the dial. A missing
  `expire_time` means "mint every time". The first credential is minted
  before connecting, so a refusal is reported once, as exit 11.
- Workspace answers: 401, 403, 404 each name their cause (sign-in, permission,
  endpoint) with the API's `message`, capped at 300 characters; 429 and 5xx
  and transport errors are tried three times (1 s, then 2 s apart). All are
  exit 11; a credential the database refuses gets the shared hint naming the
  provider, the user and `--username` (`dial::refused_token`).
- Secrets: the PAT, client secret, workspace bearer and credential are
  `Secret`s; `Settings` and `Lakebase` print host, endpoint and routes only;
  every minted credential is in `Token::secrets()`, so `--json` and the
  error line redact it.
- Default sslmode `verify-full` (the `_` arm of `cloud::check`), with the
  system trust store when no root is set (record 0006); plaintext modes
  exit 10.

## Consequences

- Pure Rust: no new crate; `jiff` parses `expire_time`, `serde_json` the
  answers, `tokio::process` runs the CLI.
- The black-box cases are `impl: rust` (runner key added for this record):
  `cloud/databricks-*` against a fake workspace on the runner's HTTPS fake,
  trusted through `SSL_CERT_FILE`, and the `tls` server's `lakebaseuser`
  whose password is the fake credential.
- To verify on real Lakebase (PGMI-849 step 6): that `all-apis` covers the
  `postgres` API scope; that the default credential lives an hour and
  `expire_time` parses; that `verify-full` passes against the endpoint's
  certificate with the system store (else the docs keep `sslmode=require`);
  the M2M route from a job's service principal and the Azure route; and
  whether a pgmi subprocess inside a serverless job sees credentials at all
  (research open risk).
