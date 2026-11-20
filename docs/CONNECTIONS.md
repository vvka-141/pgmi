---
title: "Connections"
description: "Understand pgmi connection resolution, cloud authentication, SSL, poolers, and the two-database deployment pattern."
weight: 60
---

# Connections

How to connect pgmi to a PostgreSQL server: a password, a cloud identity
(Azure, AWS, Google Cloud, Databricks Lakebase), TLS, and poolers. Start with
the section for your target; how pgmi builds the connection is at the end.

For CLI flag details, see [CLI reference](CLI.md#connection-flags). For CI/CD pipeline examples, see [Production](PRODUCTION.md#cicd).

---

## Connect with a password

The default. Works with any PostgreSQL instance that accepts password authentication.

```bash
# Granular flags
pgmi deploy . --host db.example.com -p 5432 -U deployer -d myapp

# Connection string (PostgreSQL URI) from the environment: the password comes
# from your secret store, so it reaches neither `ps` nor your shell history
PGMI_CONNECTION_STRING="postgresql://deployer:${DB_PASSWORD}@db.example.com:5432/postgres?sslmode=verify-full&sslrootcert=/path/to/ca.crt" pgmi deploy . -d myapp

# ADO.NET format (common in .NET ecosystems)
PGMI_CONNECTION_STRING="Host=db.example.com;Port=5432;Database=postgres;Username=deployer;Password=${DB_PASSWORD}" pgmi deploy . -d myapp
```

Never put a password on the command line: a password inside `--connection` is visible in `ps`, shell history and CI logs. Use `PGPASSWORD`, `.pgpass`, or `PGMI_CONNECTION_STRING` filled from a secret.

Shell tracing (`set -x`, GitLab's `CI_DEBUG_TRACE`) prints the lines above with `${DB_PASSWORD}` expanded into the log; keep it off wherever a secret is expanded.

---

## Azure Entra ID

Passwordless authentication to Azure Database for PostgreSQL Flexible Server.
Tested by hand against a real Flexible Server, not in CI. Last checked
2026-11-11 with this binary: PostgreSQL 18, `--sslmode verify-full`, a
basic-template deploy with its tests, signed in through the Azure CLI, a
service principal with a secret and one with a certificate, a system-assigned
and a user-assigned managed identity on an Azure VM, workload identity from a
GitHub Actions OIDC token, and `--azure-tenant-id` overriding
`AZURE_TENANT_ID`.

```bash
# Managed Identity (no credentials needed on Azure VMs, App Service, Functions)
pgmi deploy . --azure \
    --host mydb.postgres.database.azure.com \
    -d myapp

# User-assigned managed identity (AKS workload identity, self-hosted runners)
pgmi deploy . --azure --azure-client-id "<identity client id>" \
    --host mydb.postgres.database.azure.com \
    -d myapp

# Service Principal (credentials via environment variables)
export AZURE_TENANT_ID="..."
export AZURE_CLIENT_ID="..."
export AZURE_CLIENT_SECRET="..."
pgmi deploy . --azure \
    --host mydb.postgres.database.azure.com \
    -d myapp

# Service Principal with a certificate (PEM or PKCS#12)
export AZURE_TENANT_ID="..."
export AZURE_CLIENT_ID="..."
export AZURE_CLIENT_CERTIFICATE_PATH=/secrets/sp.pem
pgmi deploy . --azure \
    --host mydb.postgres.database.azure.com \
    -d myapp
```

**Which identity signs in:**

- **Tenant, client ID and `AZURE_CLIENT_SECRET` all set:** that service principal.
  With `AZURE_CLIENT_CERTIFICATE_PATH` instead of a secret, the same service
  principal signs in with its certificate.
- **A client ID without a secret** (`--azure-client-id`, `AZURE_CLIENT_ID` or
  `azure_client_id` in pgmi.yaml): that identity and nothing else. pgmi tries
  workload identity for that client ID (Kubernetes), then the user-assigned
  managed identity with that client ID. The Azure CLI is not tried: its
  signed-in user is not the identity you named.
- **No client ID:** Azure's default credential chain, tried in order, in the
  tenant `--azure-tenant-id` names when given:
  1. Workload Identity (Kubernetes), when `AZURE_FEDERATED_TOKEN_FILE` and a tenant are set
  2. Managed Identity (system-assigned on VMs, App Service, Azure Functions, Arc, Cloud Shell, Service Fabric, Azure ML)
  3. Azure CLI (`az login`)
  4. Azure Developer CLI (`azd auth login`)
  5. Azure PowerShell (`Connect-AzAccount`)

  `AZURE_TOKEN_CREDENTIALS` narrows the chain as in the Azure SDKs: `dev`,
  `prod`, or the name of one credential type. A value that names none exits
  10. The first credential that gives a token is kept for the whole deploy.

**Token characteristics:**
- User tokens (interactive/CLI): up to 1-hour expiry
- Managed identity tokens: up to 24-hour expiry
- OAuth scope: `https://ossrdbms-aad.database.windows.net/.default`
- pgmi signs in before it connects, and reuses the token for each later connection of the deploy while it has more than five minutes left; a new connection after that gets a new token. Token lifetime only matters for long-running connections, not typical deployments

**TLS:** sslmode defaults to `verify-full`. Azure's server certificates chain to DigiCert Global Root G2 and Microsoft RSA Root CA 2017, which the system trust store already holds, so no `--sslrootcert` is needed. If a private endpoint or VNet DNS name does not match the certificate, use `--sslmode verify-ca`. See [TLS with token authentication](#tls-with-token-authentication).

---

## AWS IAM

Token-based authentication to Amazon RDS and Aurora PostgreSQL.
Implemented and unit-tested, not tested against a real cloud.

```bash
# IAM role (EC2, ECS, Lambda — no credentials needed)
pgmi deploy . --aws --aws-region us-west-2 \
    --host mydb.cluster-xyz.us-west-2.rds.amazonaws.com \
    -U iam_deploy_user -d myapp --sslrootcert global-bundle.pem

# IAM user (credentials via env vars or ~/.aws/credentials)
export AWS_ACCESS_KEY_ID="..."
export AWS_SECRET_ACCESS_KEY="..."
pgmi deploy . --aws --aws-region us-west-2 \
    --host mydb.cluster-xyz.us-west-2.rds.amazonaws.com \
    -U iam_deploy_user -d myapp --sslrootcert global-bundle.pem
```

**Token characteristics:**
- Token validity: 15 minutes
- Token format: Signature Version 4 signed request
- New connection rate limit: 200 connections per second per instance
- Database memory overhead: 300–1,000 MiB per instance for IAM auth

**TLS:** RDS server certificates chain to private Amazon RDS CAs, not to roots in the system trust store. Download the bundle once (`curl -O https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem`) and pass it with `--sslrootcert` (or `PGSSLROOTCERT`); sslmode then defaults to `verify-full`. Without a root certificate the default is `require`: encrypted, but the server's identity is not checked. RDS Proxy uses public ACM certificates, so `--sslmode verify-full` works there without a bundle. See [TLS with token authentication](#tls-with-token-authentication).

---

## Google Cloud SQL IAM

Passwordless authentication through pgmi's own Cloud SQL dialer: it asks the
Cloud SQL Admin API for the instance's address and an ephemeral client
certificate, then opens TLS to the instance itself.
Implemented and tested against a stand-in for Google, not against a real cloud.

```bash
# Service account (GCE, GKE, Cloud Run, Cloud Functions — no credentials needed)
pgmi deploy . --google \
    --google-instance myproject:us-central1:mydb \
    -U myuser@myproject.iam -d myapp

# Local development with gcloud auth
gcloud auth application-default login
pgmi deploy . --google \
    --google-instance myproject:us-central1:mydb \
    -U myuser@myproject.iam -d myapp
```

**How it differs from other providers:**
- The dialer handles TLS itself, so `--sslmode` does not apply: pgmi sets `sslmode=disable` on the inner connection, which the dialer wraps in its own TLS 1.3, trusting only the instance's server CA
- Without Application Default Credentials the sign-in fails with exit 11
- The `--google-instance` flag is required and uses the format `project:region:instance`

---

## Databricks Lakebase

`--databricks` gets a database credential from the Databricks workspace and
refreshes it before it expires; `--databricks-endpoint` (or
`PGMI_DATABRICKS_ENDPOINT`) names the Lakebase endpoint. Without an sslmode
pgmi uses `verify-full` against the system trust store. Setup, the sign-in
order and the roles Lakebase needs are in [Lakebase](LAKEBASE.md#sign-in-with---databricks).

---

## Connection string formats

pgmi accepts three connection string formats:

**PostgreSQL URI:**
```
postgresql://user:pass@host:5432/dbname?sslmode=verify-full&sslrootcert=/path/to/ca.crt
```
Percent-encode a `/`, `?`, `#` or `%` in the password (`%2F`, `%3F`, `%23`,
`%25`): unencoded, it ends the password early.

A URI may list several hosts, as libpq allows, for example a primary and a
standby:
```
postgresql://user:pass@db1:5432,db2:5432/dbname?target_session_attrs=read-write
```
As in libpq, a host without its own port uses `?port=` (or `PGPORT`, `-p`),
else 5432. IPv6 members are bracketed: `[::1]:5432,[::2]:5433`.

**libpq keyword/value**, as psql and the PostgreSQL docs write it:
```
host=myhost port=5432 dbname=mydb user=user sslmode=require
```
Quote a value containing spaces with single quotes, and escape a quote or
backslash inside it with `\`: `password='a b\'c'`. `host` and `port` may be
lists (`host=db1,db2 port=5432,5433`). `hostaddr` is honoured as in libpq:
pgmi connects to that address without a lookup, and `host` still names the
server for TLS and `.pgpass`. Keys are lowercase libpq keywords, as in libpq.
`default_query_exec_mode`, `statement_cache_capacity` and
`description_cache_capacity` are also accepted, and checked, but change
nothing: pgmi sends no named prepared statements, so a server that keeps one
session for every connection, such as the PGlite socket server, needs no
option for a second deploy. Any other key, such as a typo or an ADO.NET-style
`Password=`, is refused with `invalid connection option "..."` (exit 10)
rather than sent to the server. A key libpq knows that pgmi cannot honour
(`sslcrl`, `sslcrldir`, `requirepeer`, `sslcertmode=require`,
`gssencmode=require`, `gssdelegation=1`, `gsslib`, `sslcompression=1`,
`min_protocol_version=3.2` or `latest`) is refused before connecting, exit 10.
A parse error never quotes the string, which may hold a password.
A single quote opens a quoted value only right after `=`; elsewhere it is a
plain character, so `Password=it's;Host=db` is read as ADO.NET.

**ADO.NET:**
```
Host=myhost;Port=5432;Database=mydb;Username=user;Password=pass;SSL Mode=Require
```

A single `Host=myhost:5433` carries its port, which wins over `Port=`, as in
Npgsql. `SSL Mode` takes Npgsql's names in any case (`Disable`, `Allow`,
`Prefer`, `Require`, `VerifyCA`, `VerifyFull`) or libpq's (`verify-full`).
Other Npgsql client options, such as `Pooling` or `Trust Server
Certificate`, are refused with `invalid connection option "..."` (exit 10)
rather than sent to the server.

**Environment variable precedence:**
1. `PGMI_CONNECTION_STRING` (highest)
2. `DATABASE_URL`

With either one set, `-d` picks the target database and `--sslmode` replaces
the string's sslmode (pgmi prints `sslmode: from --sslmode (...), not
DATABASE_URL (...)`). `-h`, `-p` and `-U` are refused (exit 10): the string
names its own server and login, and pgmi neither drops it nor sends its
password to another host. Unset the variable to connect with the flags.

---

## Configuration precedence

```
CLI flags (highest)
  ↓
Environment variables ($PGHOST, $PGPORT, $PGUSER, etc.)
  ↓
pgmi.yaml
  ↓
PostgreSQL defaults (localhost, 5432)
```

A connection string is the exception: a field it names outranks its `PG*`
variable, and with a string `pgmi.yaml` supplies only the database, the
maintenance database and the TLS paths (`sslcert`, `sslkey`, `sslrootcert`).
Every
variable pgmi, the driver and the cloud SDKs read is in the
[environment reference](CICD.md#environment-reference).

An environment variable that replaces a pgmi.yaml value is never silent: pgmi
prints one line naming both values, for TLS too, e.g.
`sslmode: from PGSSLMODE (disable), not pgmi.yaml (verify-full)`. With a
connection string, pgmi.yaml's `sslmode` does not apply at all, and pgmi says so.

An empty value is not an unset one. A connection setting written empty or as
white space only stops the deploy before connecting, exit 10: a key of a
connection string in any form (`host=''`, `?sslmode=`, `Port=;`), a
connection flag (`-h ''`), a `PG*` variable (`PGHOST=`), `DATABASE_URL`,
`PGMI_CONNECTION_STRING`, a `pgmi.yaml` value written as `""` or with no value (`host:`, `host: ~`,
`host: null`), or a `pgmi serve` deploy call's `maintenanceDatabase`. The message
names the setting and where it was written:

```
pgmi: error: the host from PGHOST is empty; write the host, or remove it so the next source applies
```

To get the next source or the default, remove the setting (in pgmi.yaml, the
whole line) or unset the variable. A password may be empty; that means no password. A variable that a
nearer source replaces (`PGHOST=` next to `-h db`) is not read, so it is not
refused.

See [Configuration](CONFIGURATION.md) for the full pgmi.yaml schema.

---

## SSL and mutual TLS

pgmi supports all six PostgreSQL SSL modes:

| Mode | Encryption | Server identity verified |
|------|-----------|--------------------------|
| `disable` | No | No |
| `allow` | If server supports | No |
| `prefer` (default, except with `--azure`/`--aws`/`--databricks`) | If server supports | No |
| `require` | Yes | No |
| `verify-ca` | Yes | CA certificate checked |
| `verify-full` | Yes | CA + hostname checked |

`verify-ca` without `sslrootcert` trusts the system store, and then also
checks the server's host name, as `verify-full` does: any public CA would
otherwise vouch for any server. With an explicit root file `verify-ca` checks
no name. `sslrootcert=system` with a mode weaker than `verify-full` is
refused, exit 10.

pgmi's TLS needs CPU instructions present on x86-64 since about 2014 (AES-NI,
PCLMULQDQ, SSSE3, AVX, AVX2, BMI1, BMI2, ADX) and on arm64 NEON, AES, PMULL
and SHA2. On an older CPU every connection that would use TLS (`allow`, `prefer`,
which is the default, `require`, `verify-ca`, `verify-full`) and every cloud
sign-in stops with exit 10 and names the missing features: pgmi never drops
encryption on its own. `sslmode=disable` and Unix sockets use no TLS and
work on any CPU.

### TLS with token authentication

With `--azure`, `--aws` or `--databricks` the password is a bearer token, so pgmi never lets the connection fall back to plaintext:

- `disable`, `allow` and `prefer` are refused before connecting (exit 10), whichever source set them: `--sslmode`, `PGSSLMODE`, the connection string, or `connection.sslmode` in pgmi.yaml. The error names the source.
- `require`, `verify-ca` and `verify-full` are accepted.
- When nothing sets sslmode, the default is `verify-full` for Azure and Databricks, and for AWS when a root certificate is configured (`--sslrootcert`, `PGSSLROOTCERT`, pgmi.yaml, or `sslrootcert` in the connection string). AWS without one defaults to `require`.
- The pgmi.yaml that `pgmi init` writes sets no `sslmode`, so a new project works with these flags as it is. An older project whose pgmi.yaml still says `sslmode: prefer` is refused with an error naming that line and the fix.

Google Cloud SQL is not affected: its connector supplies TLS itself.

### Mutual TLS

**mTLS configuration** (client certificate authentication):

```bash
pgmi deploy . \
    --host secure-pg.internal \
    --sslmode verify-full \
    --sslcert /certs/client.crt \
    --sslkey /certs/client.key \
    --sslrootcert /certs/ca.crt \
    -d myapp
```

For encrypted private keys, use the `PGSSLPASSWORD` environment variable (no CLI flag — by design). Legacy encrypted PEM keys and encrypted PKCS#8 keys (PBES2, PBE-SHA1-3DES) open with it; a key whose encryption would take unreasonably long to open (a PBKDF2 or PKCS#12 iteration count above 1,000,000, a scrypt cost above 2^23) is refused, exit 10.

As in libpq, the key file must not be readable by group or others
(`chmod 0600`; `0640` when root owns it): otherwise pgmi refuses it, exit 10.
Windows makes no such check.

---

## Connection pooler compatibility

pgmi uses session-scoped temporary tables (`pg_temp`) that must survive for the entire deployment. Connection poolers that reassign backend connections between transactions will break deployments.

| Pooler | Session mode | Transaction mode | Statement mode |
|--------|-------------|------------------|----------------|
| PgBouncer | Works | **Incompatible** | **Incompatible** |
| Pgpool-II | Works | **Incompatible** | N/A |
| AWS RDS Proxy | Works (pinned) | **Incompatible** | N/A |
| Azure PgBouncer | Works | **Incompatible** | **Incompatible** |

**Why transaction mode fails:** pgmi creates temp tables in step 2, then your `deploy.sql` reads them in step 3. In transaction mode, the pooler may assign a different backend between these steps — the new backend has no `pg_temp` tables.

**Solution:** Use the direct endpoint (port 5432) for pgmi deployments, not the pooled endpoint (port 6432):

```bash
# Direct connection for pgmi (bypasses pooler)
PGMI_CONNECTION_STRING="postgresql://deployer:${DB_PASSWORD}@db-server:5432/mydb?sslmode=verify-full&sslrootcert=/path/to/ca.crt" pgmi deploy .

# Application traffic uses pooler as usual
# postgresql://user:pass@pgbouncer:6432/mydb
```

---

## The `--overwrite` lifecycle

When you use `--overwrite`, pgmi follows this sequence:

1. Connect to the **maintenance database** (see below; usually `postgres`)
2. Take the overwrite lock for the target, held until the deploy ends. A second
   `--overwrite` of the same target exits 15, also while this one waits at the
   prompt or creates a target that did not exist
3. If the target does not exist, create it and skip to step 8
4. Show safety prompt (interactive confirmation or 5-second countdown with `--force`)
5. Refuse with exit 15 if a deploy holds the target's deploy lock
6. `DROP DATABASE target_db WITH (FORCE)`, which ends the target's connections
7. `CREATE DATABASE target_db`
8. Connect to the target database
9. Create session tables and views
10. Execute `deploy.sql`
11. Release the overwrite lock and disconnect from the maintenance database

Steps 2 to 7 run on one maintenance connection, kept open until the deploy
ends, so `--overwrite` needs two connections in all: that one and the target
session. A role with `CONNECTION LIMIT 2` is enough.

The overwrite lock lives in the maintenance database, and PostgreSQL scopes
advisory locks to one database: two runs that use different maintenance
databases for the same target do not exclude each other.

A plain deploy that connects to the target between steps 5 and 6 is ended by
the drop rather than refused with exit 15: the two locks live in different
databases, and PostgreSQL scopes advisory locks to one database. That window
is milliseconds, the operator approved dropping the database, and the ended
deploy fails loudly with its transaction rolled back.

Where the maintenance database comes from:

- **A connection string** (`--connection`, `PGMI_CONNECTION_STRING`,
  `DATABASE_URL`): its database, or `postgres` when it names none. Without
  `-d`, the string's database is the target instead, and the maintenance
  database is `postgres`.
- **Flags, `PG*` variables and `pgmi.yaml`**: `connection.maintenance_database`
  in `pgmi.yaml`, else `postgres`. The target is `-d`, `PGDATABASE` or
  `pgmi.yaml`'s `database`.
- **`pgmi serve`**: the deploy tool's `maintenanceDatabase` argument, when
  given, overrides both.

The target database is the `-d` flag. See [CLI reference](CLI.md#the-two-database-pattern).

---

## PostgreSQL compatibility check

After connecting, pgmi reads the server version:

```sql
SELECT pg_catalog.current_setting('server_version_num'),
       pg_catalog.current_setting('server_version'),
       pg_catalog.pg_postmaster_start_time()
```

Servers below PostgreSQL 15 (`server_version_num < 150000`) are rejected with exit code 10 before any session tables are created. The start time tells a server restart, which a retry may follow, from a session an administrator ended.

---

## Where pgmi doesn't work

pgmi requires PostgreSQL-compatible features: temporary tables, PL/pgSQL, savepoints, `pg_temp` schema. These databases are not compatible:

| Database | Why |
|----------|-----|
| Amazon Aurora DSQL | No temporary tables, no PL/pgSQL |
| CockroachDB | Temporary tables are experimental; `pg_temp` behavior differs |
| YugabyteDB | Temporary tables supported but savepoint behavior differs in distributed mode |

pgmi works with any database that is **wire-compatible and feature-compatible** with PostgreSQL: vanilla PostgreSQL, Amazon RDS PostgreSQL, Amazon Aurora PostgreSQL (not DSQL), Azure Database for PostgreSQL, Google Cloud SQL for PostgreSQL, AlloyDB, Citus, TimescaleDB, Neon, Supabase.

---

## Infrastructure as Code integration

pgmi fits naturally into IaC pipelines. Terraform provisions the database; pgmi deploys the schema.

**Terraform + pgmi (Azure example):**

```hcl
resource "azurerm_postgresql_flexible_server" "main" {
  name                = "myapp-pg"
  resource_group_name = azurerm_resource_group.main.name
  location            = "westeurope"
  version             = "16"
  sku_name            = "GP_Standard_D2s_v3"

  authentication {
    active_directory_auth_enabled = true
    password_auth_enabled         = false
  }
}

output "pgmi_host" {
  value = azurerm_postgresql_flexible_server.main.fqdn
}
```

```yaml
# GitHub Actions: deploy schema after Terraform
- name: Deploy database schema
  run: |
    pgmi deploy ./project --azure \
      --host ${{ steps.terraform.outputs.pgmi_host }} \
      -d myapp --param env=prod --force
  env:
    AZURE_TENANT_ID: ${{ secrets.AZURE_TENANT_ID }}
    AZURE_CLIENT_ID: ${{ secrets.AZURE_CLIENT_ID }}
    AZURE_CLIENT_SECRET: ${{ secrets.AZURE_CLIENT_SECRET }}
```

**Multi-cloud pipeline** — same `deploy.sql`, four targets:

```yaml
jobs:
  # Test against ephemeral Docker database
  test:
    services:
      postgres:
        image: postgres:16
        env: { POSTGRES_PASSWORD: devpass }
        ports: ['5432:5432']
    steps:
      - run: pgmi deploy . --overwrite --force -h 127.0.0.1 -U postgres -d test
        env: { PGPASSWORD: devpass }

  # Production: Azure (Entra ID)
  deploy-azure:
    needs: test
    steps:
      - run: pgmi deploy . --azure --host $AZURE_HOST -d myapp --force
        env:
          AZURE_TENANT_ID: ${{ secrets.AZURE_TENANT_ID }}
          AZURE_CLIENT_ID: ${{ secrets.AZURE_CLIENT_ID }}
          AZURE_CLIENT_SECRET: ${{ secrets.AZURE_CLIENT_SECRET }}

  # Production: AWS (IAM)
  deploy-aws:
    needs: test
    steps:
      - run: pgmi deploy . --aws --aws-region us-west-2 --host $RDS_HOST -U iam_deploy -d myapp --force

  # Production: GCP (Cloud SQL IAM)
  deploy-gcp:
    needs: test
    steps:
      - run: pgmi deploy . --google --google-instance $INSTANCE -U $SA_EMAIL -d myapp --force
```

---

## Internals: how a connection is made

pgmi resolves the connection string, flags, environment and `pgmi.yaml` into
one connection setting before anything connects, then signs in when a cloud
flag asks for it:

| Auth method | How the password is found |
|-------------|---------------------------|
| Standard | the connection string, `PGPASSWORD`, or a password file |
| Azure Entra ID | a token from the service principal, the named managed or workload identity, or the default credential chain |
| AWS IAM | an RDS auth token signed with the default AWS credential chain |
| Databricks Lakebase | a database credential from the workspace, refreshed before it expires |
| Google Cloud SQL IAM | an IAM login token, over pgmi's own dialer and its TLS |

Every route retries a failed connection attempt up to three times (four
attempts in total), about 0.1, 0.2 and 0.4 s apart, inside each attempt of the
deploy. A host with several addresses reports every address's error; a
certificate failure among them is final. With several hosts and no
`connect_timeout`, each host but the last gets at most its share of the time
box (and at most 30 s) before the next one is tried. Past that, the deploy
itself may be retried: see [Time box and retries](CLI.md#time-box-and-retries).
The rest of pgmi does not depend on which auth method was used.

---

## See also

- [CLI Reference](CLI.md) — All connection flags, authentication flags, examples
- [Configuration](CONFIGURATION.md) — pgmi.yaml schema and precedence
- [Production Guide](PRODUCTION.md) — CI/CD patterns, deployment strategies
- [Security](SECURITY.md) — Secrets handling and parameter flow
- [Tradeoffs](TRADEOFFS.md) — Connection pooler limitations in depth
