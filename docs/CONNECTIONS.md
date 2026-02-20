# Connection Architecture

How pgmi connects to every PostgreSQL target — from localhost to multi-cloud managed services.

For CLI flag details, see [CLI.md](CLI.md#connection-flags). For CI/CD pipeline examples, see [PRODUCTION.md](PRODUCTION.md#cicd-patterns).

---

## Connection provider factory

pgmi resolves CLI flags and environment variables into a concrete connector at startup:

| Auth method | Connector | How tokens work |
|-------------|-----------|-----------------|
| Standard | `StandardConnector` | Username/password via pgx, with retry (3 attempts, exponential backoff) |
| Azure Entra ID | `TokenBasedConnector` | `azidentity.NewDefaultAzureCredential` chain, OAuth token as password |
| AWS IAM | `TokenBasedConnector` | `rds/auth.BuildAuthToken` via default AWS credential chain |
| Google Cloud SQL IAM | `GoogleCloudSQLConnector` | `cloud.google.com/go/cloudsqlconn` with IAM auth and internal TLS |

All providers produce a `*pgx.Conn` — the rest of pgmi doesn't know or care which auth method was used.

---

## Standard PostgreSQL connections

The default. Works with any PostgreSQL instance that accepts password authentication.

```bash
# Granular flags
pgmi deploy . --host db.example.com -p 5432 -U deployer -d myapp

# Connection string (PostgreSQL URI)
pgmi deploy . --connection "postgresql://deployer:secret@db.example.com:5432/postgres" -d myapp

# ADO.NET format (common in .NET ecosystems)
pgmi deploy . --connection "Host=db.example.com;Port=5432;Database=postgres;Username=deployer;Password=secret" -d myapp
```

Passwords are never passed as CLI flags. Use `PGPASSWORD`, `.pgpass`, or embed in the connection string.

---

## Azure Entra ID

Passwordless authentication to Azure Database for PostgreSQL Flexible Server.

```bash
# Managed Identity (no credentials needed on Azure VMs, App Service, Functions)
pgmi deploy . --azure \
    --host mydb.postgres.database.azure.com \
    -d myapp --sslmode require

# Service Principal (credentials via environment variables)
export AZURE_TENANT_ID="..."
export AZURE_CLIENT_ID="..."
export AZURE_CLIENT_SECRET="..."
pgmi deploy . --azure \
    --host mydb.postgres.database.azure.com \
    -d myapp --sslmode require
```

**Credential chain** (tried in order):

1. Environment variables (`AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`)
2. Workload Identity (Kubernetes)
3. Managed Identity (VMs, App Service, Azure Functions)
4. Azure CLI (`az login`)
5. Azure Developer CLI (`azd auth login`)
6. Azure PowerShell (`Connect-AzAccount`)

**Token characteristics:**
- User tokens: 4-hour expiry
- Service principal tokens: 24-hour expiry
- OAuth scope: `https://ossrdbms-aad.database.windows.net/.default`

---

## AWS IAM

Token-based authentication to Amazon RDS and Aurora PostgreSQL.

```bash
# IAM role (EC2, ECS, Lambda — no credentials needed)
pgmi deploy . --aws --aws-region us-west-2 \
    --host mydb.cluster-xyz.us-west-2.rds.amazonaws.com \
    -U iam_deploy_user -d myapp --sslmode require

# IAM user (credentials via env vars or ~/.aws/credentials)
export AWS_ACCESS_KEY_ID="..."
export AWS_SECRET_ACCESS_KEY="..."
pgmi deploy . --aws --aws-region us-west-2 \
    --host mydb.cluster-xyz.us-west-2.rds.amazonaws.com \
    -U iam_deploy_user -d myapp --sslmode require
```

**Token characteristics:**
- Token validity: 15 minutes
- Token format: Signature Version 4 signed request
- New connection rate limit: 200 connections per second per instance
- Database memory overhead: 300–1,000 MiB per instance for IAM auth

---

## Google Cloud SQL IAM

Passwordless authentication via the Cloud SQL Go Connector (`cloud.google.com/go/cloudsqlconn`).

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
- The Cloud SQL connector handles TLS internally — set `sslmode=disable` in the pgx DSN (the connector wraps the connection with its own TLS)
- The `--google-instance` flag is required and uses the format `project:region:instance`

---

## Connection string formats

pgmi accepts two connection string formats:

**PostgreSQL URI:**
```
postgresql://user:pass@host:5432/dbname?sslmode=require
```

**ADO.NET:**
```
Host=myhost;Port=5432;Database=mydb;Username=user;Password=pass;SSL Mode=Require
```

**Environment variable precedence:**
1. `PGMI_CONNECTION_STRING` (highest)
2. `DATABASE_URL`

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

See [CONFIGURATION.md](CONFIGURATION.md) for the full pgmi.yaml schema.

---

## SSL and mutual TLS

pgmi supports all six PostgreSQL SSL modes:

| Mode | Encryption | Server identity verified |
|------|-----------|--------------------------|
| `disable` | No | No |
| `allow` | If server supports | No |
| `prefer` (default) | If server supports | No |
| `require` | Yes | No |
| `verify-ca` | Yes | CA certificate checked |
| `verify-full` | Yes | CA + hostname checked |

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
pgmi deploy . --connection "postgresql://user:pass@db-server:5432/mydb"

# Application traffic uses pooler as usual
# postgresql://user:pass@pgbouncer:6432/mydb
```

---

## The `--overwrite` lifecycle

When you use `--overwrite`, pgmi follows a 9-step sequence:

1. Connect to the **maintenance database** (from connection string, default: `postgres`)
2. Show safety prompt (interactive confirmation or 5-second countdown with `--force`)
3. Terminate existing connections to the target database
4. `DROP DATABASE IF EXISTS target_db`
5. `CREATE DATABASE target_db`
6. Disconnect from maintenance database
7. Connect to the target database
8. Create session tables and views
9. Execute `deploy.sql`

The maintenance database is the one in your connection string. The target database is the `-d` flag. See [CLI.md](CLI.md#the-two-database-pattern).

---

## PostgreSQL compatibility test

pgmi runs a 7-line compatibility check on every connection to verify the target is a real PostgreSQL instance:

```sql
SELECT
    version(),
    current_database(),
    current_user,
    pg_backend_pid(),
    inet_server_addr(),
    inet_server_port(),
    current_setting('server_version_num')::int
```

This catches connection issues early (wrong database, wrong user, non-PostgreSQL target) before pgmi creates session tables.

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
      -d myapp --param env=production --force
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

## See also

- [CLI Reference](CLI.md) — All connection flags, authentication flags, examples
- [Configuration](CONFIGURATION.md) — pgmi.yaml schema and precedence
- [Production Guide](PRODUCTION.md) — CI/CD patterns, deployment strategies
- [Security](SECURITY.md) — Secrets handling and parameter flow
- [Tradeoffs](TRADEOFFS.md) — Connection pooler limitations in depth
