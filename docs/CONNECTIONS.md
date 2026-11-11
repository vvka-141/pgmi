---
title: "Connections"
description: "Understand pgmi connection resolution, cloud authentication, SSL, poolers, and the two-database deployment pattern."
weight: 60
---

# Connections

How to connect pgmi to a PostgreSQL server: a password, a cloud identity
(Azure, AWS, Google Cloud), TLS, and poolers. Start with the section for your
target; how pgmi builds the connection is at the end.

For CLI flag details, see [CLI reference](CLI.md#connection-flags). For CI/CD pipeline examples, see [Production](PRODUCTION.md#cicd).

---

## Connect with a password

The default. Works with any PostgreSQL instance that accepts password authentication.

```bash
# Granular flags
pgmi deploy . --host db.example.com -p 5432 -U deployer -d myapp

# Connection string (PostgreSQL URI) from the environment: the password comes
# from your secret store, so it reaches neither `ps` nor your shell history
PGMI_CONNECTION_STRING="postgresql://deployer:${DB_PASSWORD}@db.example.com:5432/postgres" pgmi deploy . -d myapp

# ADO.NET format (common in .NET ecosystems)
PGMI_CONNECTION_STRING="Host=db.example.com;Port=5432;Database=postgres;Username=deployer;Password=${DB_PASSWORD}" pgmi deploy . -d myapp
```

Never put a password on the command line: a password inside `--connection` is visible in `ps`, shell history and CI logs. Use `PGPASSWORD`, `.pgpass`, or `PGMI_CONNECTION_STRING` filled from a secret.

---

## Azure Entra ID

Passwordless authentication to Azure Database for PostgreSQL Flexible Server.
Tested by hand against a real Flexible Server, not in CI. Last checked
2026-11-11: PostgreSQL 18, `--sslmode verify-full`, a basic-template deploy
with its tests, signed in through the Azure CLI, a service principal with a
secret, a system-assigned and a user-assigned managed identity on an Azure VM,
workload identity from a GitHub Actions OIDC token, and `--azure-tenant-id`
overriding `AZURE_TENANT_ID`.

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
```

**Which identity signs in:**

- **Tenant, client ID and `AZURE_CLIENT_SECRET` all set:** that service principal.
- **A client ID without a secret** (`--azure-client-id`, `AZURE_CLIENT_ID` or
  `azure_client_id` in pgmi.yaml): that identity and nothing else. pgmi tries
  workload identity for that client ID (Kubernetes), then the user-assigned
  managed identity with that client ID. The Azure CLI is not tried: its
  signed-in user is not the identity you named.
- **No client ID:** Azure's default credential chain, tried in order, in the
  tenant `--azure-tenant-id` names when given:
  1. Environment variables (`AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`)
  2. Workload Identity (Kubernetes)
  3. Managed Identity (system-assigned on VMs, App Service, Azure Functions)
  4. Azure CLI (`az login`)
  5. Azure Developer CLI (`azd auth login`)
  6. Azure PowerShell (`Connect-AzAccount`)

**Token characteristics:**
- User tokens (interactive/CLI): up to 1-hour expiry
- Managed identity tokens: up to 24-hour expiry
- OAuth scope: `https://ossrdbms-aad.database.windows.net/.default`
- pgmi acquires a fresh token at connection time via the Azure SDK credential chain; token lifetime only matters for long-running connections, not typical deployments

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

Passwordless authentication via the Cloud SQL Go Connector (`cloud.google.com/go/cloudsqlconn`).
Implemented and unit-tested, not tested against a real cloud.

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
- The Cloud SQL connector handles TLS itself, so `--sslmode` does not apply: pgmi sets `sslmode=disable` on the inner connection, which the connector wraps in its own TLS
- The `--google-instance` flag is required and uses the format `project:region:instance`

---

## Connection string formats

pgmi accepts three connection string formats:

**PostgreSQL URI:**
```
postgresql://user:pass@host:5432/dbname?sslmode=require
```

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
lists (`host=db1,db2 port=5432,5433`). `hostaddr` is rejected: pgx, which
pgmi connects through, does not support it, so put the address in `host`.
Keys are lowercase libpq keywords, as in libpq, plus three pgx options a URI
also accepts: `default_query_exec_mode`, `statement_cache_capacity` and
`description_cache_capacity`. A server that keeps one session for every
connection, such as the PGlite socket server, needs
`default_query_exec_mode=exec` (or `simple_protocol`) for a second deploy:
pgx's default caches prepared statements by name, and the names collide
with the previous connection's. Any other key, such as a
typo or an ADO.NET-style `Password=`, is refused with
`invalid connection option "..."` (exit 10) rather than sent to the server.
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
variable, and with a string `pgmi.yaml` supplies only the database. Every
variable pgmi, the driver and the cloud SDKs read is in the
[environment reference](CICD.md#environment-reference).

An environment variable that replaces a pgmi.yaml value is never silent: pgmi
prints one line naming both values, for TLS too, e.g.
`sslmode: from PGSSLMODE (disable), not pgmi.yaml (verify-full)`. With a
connection string, pgmi.yaml's `sslmode` does not apply at all, and pgmi says so.

See [Configuration](CONFIGURATION.md) for the full pgmi.yaml schema.

---

## SSL and mutual TLS

pgmi supports all six PostgreSQL SSL modes:

| Mode | Encryption | Server identity verified |
|------|-----------|--------------------------|
| `disable` | No | No |
| `allow` | If server supports | No |
| `prefer` (default, except with `--azure`/`--aws`) | If server supports | No |
| `require` | Yes | No |
| `verify-ca` | Yes | CA certificate checked |
| `verify-full` | Yes | CA + hostname checked |

### TLS with token authentication

With `--azure` or `--aws` the password is a bearer token, so pgmi never lets the connection fall back to plaintext:

- `disable`, `allow` and `prefer` are refused before connecting (exit 10), whichever source set them: `--sslmode`, `PGSSLMODE`, the connection string, or `connection.sslmode` in pgmi.yaml. The error names the source.
- `require`, `verify-ca` and `verify-full` are accepted.
- When nothing sets sslmode, the default is `verify-full` for Azure, and for AWS when a root certificate is configured (`--sslrootcert`, `PGSSLROOTCERT`, pgmi.yaml, or `sslrootcert` in the connection string). AWS without one defaults to `require`.
- The pgmi.yaml that `pgmi init` writes for the basic and advanced templates sets `sslmode: prefer` for local development, so these commands are refused from a fresh project until you pass `--sslmode verify-full` (`require` for AWS without a root certificate) or change that line.

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

For encrypted private keys, use the `PGSSLPASSWORD` environment variable (no CLI flag — by design).

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
PGMI_CONNECTION_STRING="postgresql://deployer:${DB_PASSWORD}@db-server:5432/mydb" pgmi deploy .

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
- **The Go library**: `DeploymentConfig.MaintenanceDatabase` (empty means
  `postgres`); the connection string's database plays no part.

The target database is the `-d` flag. See [CLI reference](CLI.md#the-two-database-pattern).

---

## PostgreSQL compatibility check

After connecting, pgmi pings the pool and then probes the server version:

```sql
SELECT current_setting('server_version_num')::int,
       current_setting('server_version')
```

Servers below PostgreSQL 15 (`server_version_num < 150000`) are rejected with exit code 10 before any session tables are created.

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

## Internals: connection provider factory

pgmi resolves CLI flags and environment variables into a concrete connector at startup:

![The connection factory: connection string, flags, and environment resolve into a ConnectionConfig; NewConnector picks the concrete connector by auth method; cloud connectors mint short-lived tokens](diagrams/d07-connection-factory.drawio.svg)

| Auth method | Connector | How tokens work |
|-------------|-----------|-----------------|
| Standard | `StandardConnector` | Username/password via pgx |
| Azure Entra ID | `TokenBasedConnector` | service principal, the named managed or workload identity, or `azidentity.NewDefaultAzureCredential`; OAuth token as password |
| AWS IAM | `TokenBasedConnector` | `rds/auth.BuildAuthToken` via default AWS credential chain |
| Google Cloud SQL IAM | `GoogleCloudSQLConnector` | `cloud.google.com/go/cloudsqlconn` with IAM auth and internal TLS |

The Standard, Azure and AWS connectors retry a failed connection attempt up to three times (four attempts in total) with exponential backoff. The Google Cloud SQL connector does not.

All providers produce a `*pgx.Conn` — the rest of pgmi doesn't know or care which auth method was used.

---

## See also

- [CLI Reference](CLI.md) — All connection flags, authentication flags, examples
- [Configuration](CONFIGURATION.md) — pgmi.yaml schema and precedence
- [Production Guide](PRODUCTION.md) — CI/CD patterns, deployment strategies
- [Security](SECURITY.md) — Secrets handling and parameter flow
- [Tradeoffs](TRADEOFFS.md) — Connection pooler limitations in depth
