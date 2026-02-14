---
name: pgmi-connections
description: "Use when working with auth or connection providers"
user_invocable: true
---


**Use this skill when:**
- Understanding pgmi's connection architecture
- Working on authentication providers (standard, AWS IAM, Azure, GCP)
- Debugging connection issues
- Adding new connection methods
- Understanding connection string parsing
- Working with the connection factory pattern

---

## Connection Architecture

### Design Principles

**1. Factory Pattern:**
Connection creation abstracted behind interfaces and factories
```
ConnectionString → Parser → ConnectionConfig → Provider → Connector
```

**2. Interface-Based:**
All connectors implement `Connector` interface for uniform behavior

**3. Extensible:**
New authentication methods added without changing core code

**4. Single Session:**
One connection for entire deployment (session-centric model)

---

## Connection Flow

### High-Level Flow

```
1. User provides connection info (--connection flag or env var)
2. ConnectionStringParser parses format (PostgreSQL URI or ADO.NET)
3. Parser produces ConnectionConfig
4. ConnectionProvider factory selects appropriate Connector
5. Connector establishes session
6. Deployment proceeds in that session
```

### Code Locations

- `internal/db/parser.go` - Connection string parsing
- `internal/db/connector.go` - Standard connector implementation
- `internal/db/azure_connector.go` - Azure Entra ID connector
- `pkg/pgmi/connector.go` - `Connector` interface definition

---

## Connection String Formats

### Supported Formats

pgmi supports **two** connection string formats:

#### 1. PostgreSQL URI Format (Preferred)

**Syntax:**
```
postgresql://[user[:password]@][host][:port][/database][?param=value&...]
```

**Examples:**
```bash
# Basic
postgresql://postgres:password@localhost:5432/mydb

# Without password (prompt or use .pgpass)
postgresql://postgres@localhost:5432/mydb

# With SSL
postgresql://postgres:password@localhost:5432/mydb?sslmode=require

# Cloud providers
postgresql://user@myhost.postgres.database.azure.com:5432/mydb?sslmode=require

# With multiple parameters
postgresql://postgres@localhost/mydb?sslmode=disable&connect_timeout=10
```

**Advantages:**
- ✅ Standard PostgreSQL format
- ✅ Clear structure
- ✅ URL encoding supported
- ✅ Query parameters for SSL, timeouts, etc.

#### 2. ADO.NET Format (Alternative)

**Syntax:**
```
Key=Value;Key=Value;...
```

**Examples:**
```bash
# Basic
Host=localhost;Port=5432;Database=mydb;Username=postgres;Password=password

# Minimal
Host=localhost;Database=mydb;Username=postgres

# With SSL
Host=localhost;Database=mydb;Username=postgres;SSL Mode=Require
```

**Key names (case-insensitive):**
- `Host` or `Server`
- `Port` (default: 5432)
- `Database` or `Initial Catalog`
- `Username` or `User ID` or `UID`
- `Password`
- `SSL Mode` or `SslMode`

**Advantages:**
- ✅ Familiar to .NET developers
- ✅ No special character escaping needed
- ✅ Flexible key naming

---

## Connection String Parsing

### Parser Implementation

**Location:** `internal/db/parser.go`

**Detection Logic:**
```go
func ParseConnectionString(connStr string) (*ConnectionConfig, error) {
    // Detect format
    if strings.HasPrefix(connStr, "postgresql://") ||
       strings.HasPrefix(connStr, "postgres://") {
        return parsePostgreSQLURI(connStr)
    } else if strings.Contains(connStr, "=") {
        return parseADONET(connStr)
    } else {
        return nil, errors.New("unsupported connection string format")
    }
}
```

**Output: ConnectionConfig**
```go
type ConnectionConfig struct {
    Host             string
    Port             int
    Database         string
    Username         string
    Password         string
    SSLMode          string
    SSLCert          string            // Client certificate file path
    SSLKey           string            // Client private key file path
    SSLRootCert      string            // Root CA certificate file path
    SSLPassword      string            // Password for encrypted client key
    AdditionalParams map[string]string // Additional parameters
}
```

---

## Two-Database Pattern

### Maintenance Database vs Target Database

pgmi uses a **two-database pattern** when the `--db` flag is provided:

**Pattern:**
1. **Connect to maintenance database** (from connection string)
2. **Create/recreate target database** (from `--db` flag)
3. **Reconnect to target database**
4. **Perform deployment** in target database

**Why?**

PostgreSQL requires:
- ✅ `CREATE DATABASE` runs from **outside** the target database
- ✅ Cannot create a database while connected to it
- ✅ Cannot drop a database while connected to it

**Example:**
```bash
# Connect to 'postgres' (maintenance DB), deploy to 'myapp' (target DB)
pgmi deploy ./migrations \
  --connection "postgresql://postgres:password@localhost/postgres" \
  -d myapp
```

**Flow:**
```
1. Connect to 'postgres'
2. DROP DATABASE IF EXISTS myapp (if --overwrite)
3. CREATE DATABASE myapp
4. Disconnect from 'postgres'
5. Connect to 'myapp'
6. Execute deployment in 'myapp'
```

**When NOT to use `--db`:**
- Deploying to existing database (no creation needed)
- Don't have CREATE DATABASE privileges
- Database already exists and should not be recreated

**Example without `--db`:**
```bash
# Deploy directly to existing database
pgmi deploy ./migrations \
  --connection "postgresql://postgres:password@localhost/myapp"
```

---

## Connector Interface

### Interface Definition

**Location:** `pkg/pgmi/connector.go`

```go
// Connector is a unified interface for establishing database connections.
// Different implementations handle various authentication methods
// (standard credentials, certificates, cloud IAM, etc.).
type Connector interface {
    // Connect establishes a connection pool to the database.
    // The returned pool should be closed by the caller when done.
    Connect(ctx context.Context) (*pgxpool.Pool, error)
}
```

**Why Interface?**

- ✅ **Testability:** Mock connectors for unit tests
- ✅ **Extensibility:** Add new auth methods without changing deployer
- ✅ **Separation:** Connection logic isolated from deployment logic

---

## Standard Connector

### Implementation

**Location:** `internal/db/connector.go`

**Purpose:** Username/password authentication with automatic retry (most common)

```go
type StandardConnector struct {
    config        *pgmi.ConnectionConfig
    retryExecutor *retry.Executor
}

func NewStandardConnector(config *pgmi.ConnectionConfig) *StandardConnector {
    // Sets up retry with exponential backoff for transient failures
    return &StandardConnector{config: config, retryExecutor: executor}
}

func (c *StandardConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
    // Uses pgxpool for connection pooling
    // Configures notice handler for RAISE NOTICE output
    // Retries on transient connection failures
}
```

**Use Case:**
- Local development
- Traditional username/password authentication
- Connection poolers (PgBouncer, etc.)

---

## Connection Provider Factory

### Factory Pattern

**Location:** `internal/db/connection_provider.go`

**Purpose:** Select appropriate Connector implementation based on configuration

```go
type ConnectionProvider struct {
    config *ConnectionConfig
}

func NewConnectionProvider(config *ConnectionConfig) *ConnectionProvider {
    return &ConnectionProvider{config: config}
}

func (p *ConnectionProvider) GetConnector() (Connector, error) {
    switch p.config.AuthMethod {
    case pgmi.AuthMethodStandard:
        return NewStandardConnector(p.config), nil
    case pgmi.AuthMethodAzureEntraID:
        return NewAzureEntraIdConnector(p.config)
    case pgmi.AuthMethodAWSIAM:
        return NewAWSIAMConnector(p.config)
    // Roadmap: AuthMethodGoogleIAM
    default:
        return nil, fmt.Errorf("unsupported auth method: %s", p.config.AuthMethod)
    }
}
```

**Extensibility:**
When adding new auth methods:
1. Implement `Connector` interface
2. Add detection logic to factory
3. No changes to deployer code needed

---

## Cloud IAM Authentication

### Azure Entra ID Authentication

**Status:** ✅ Implemented

**How it works:**
1. Authenticate to Azure Entra ID using Service Principal or DefaultAzureCredential chain
2. Obtain OAuth access token for Azure Database for PostgreSQL
3. Use token as PostgreSQL password (token-as-password pattern)

**Implementation Files:**
- `internal/db/token_provider.go` - TokenProvider interface
- `internal/db/azure_token_provider.go` - Azure credential implementations
- `internal/db/azure_connector.go` - AzureEntraIDConnector
- `internal/db/resolver.go` - AzureFlags, environment variable loading

**Architecture:**
```go
// TokenProvider interface for extensibility
type TokenProvider interface {
    GetToken(ctx context.Context) (token string, expiresOn time.Time, err error)
    String() string
}

// Two implementations:
// 1. AzureServicePrincipalProvider - uses ClientSecretCredential (for CI/CD)
// 2. AzureDefaultCredentialProvider - uses DefaultAzureCredential chain (for dev)
```

**Environment Variables:**
```bash
export AZURE_TENANT_ID="your-tenant-id"
export AZURE_CLIENT_ID="your-client-id"
export AZURE_CLIENT_SECRET="your-client-secret"  # For Service Principal
```

**CLI Flags:**
```bash
# Managed Identity (system-assigned, no credentials needed)
pgmi deploy ./migrations \
  --host myserver.postgres.database.azure.com \
  --database mydb \
  --azure \
  --sslmode require

# Service Principal (env vars + flag overrides)
pgmi deploy ./migrations \
  --host myserver.postgres.database.azure.com \
  --database mydb \
  --azure \
  --azure-tenant-id "your-tenant-id" \
  --azure-client-id "your-client-id" \
  --sslmode require
```

**Authentication Methods:**

1. **Managed Identity** (`--azure` alone) — for Azure-hosted workloads (VMs, App Service, AKS). No credentials needed. For user-assigned Managed Identity, add `--azure-client-id`.

2. **Service Principal** (env vars + optional `--azure`) — for CI/CD pipelines. Requires `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET` env vars. When all three are set, `--azure` is auto-detected.

3. **DefaultAzureCredential Chain** (`--azure`) — automatic fallback order: environment vars → Managed Identity → Azure CLI → etc.

**Azure Setup Requirements:**
- Azure Database for PostgreSQL **Flexible Server** with Entra ID authentication enabled
- Service Principal or user assigned as PostgreSQL admin in Azure portal
- SSL mode `require` (Azure PostgreSQL requires encrypted connections)
- Username format: Service Principal name (e.g., `myapp-sp`)

**Activation:**
- Use `--azure` flag to explicitly enable Azure Entra ID authentication (required for Managed Identity where no env vars exist)
- Env vars alone still auto-detect: when all three `AZURE_*` env vars are set, pgmi activates Azure auth automatically
- Standard auth used when neither `--azure` flag nor Azure env vars are present

---

### TLS Client Certificates (mTLS)

**Status:** ✅ Implemented

**Design:** Certificate params are transport-layer configuration, NOT an authentication method. They are additive — combinable with `--connection`, granular flags, and any auth method. pgx v5 handles mTLS natively via `sslcert`/`sslkey`/`sslrootcert` connection string parameters. pgmi adds CLI ergonomics on top.

**Implementation Files:**
- `internal/db/resolver.go` - `CertFlags` struct, `applyCertParams()`, `EnvVars` with `PGSSLCERT`/`PGSSLKEY`/`PGSSLROOTCERT`/`PGSSLPASSWORD`
- `internal/db/parser.go` - cert param parsing in URI and ADO.NET formats
- `internal/config/config.go` - `sslcert`/`sslkey`/`sslrootcert` in pgmi.yaml
- `internal/cli/deploy.go` - `--sslcert`, `--sslkey`, `--sslrootcert` CLI flags

**Precedence (per parameter):**
```
CLI flag (--sslcert) > env var ($PGSSLCERT) > pgmi.yaml (connection.sslcert) > connection string
```

**`CertFlags` struct:**
```go
type CertFlags struct {
    SSLCert     string
    SSLKey      string
    SSLRootCert string
}
```

**No conflict with `--connection`:** Unlike granular flags (`-h`, `-p`, `-U`) which conflict with `--connection`, cert flags are additive. Users can combine:
```bash
pgmi deploy . \
  --connection "postgresql://user@host/db" \
  --sslcert /path/client.crt \
  --sslkey /path/client.key
```

**`SSLPassword`:** Available only via `PGSSLPASSWORD` env var (no CLI flag, no pgmi.yaml — security).

**PostgreSQL Server Config (for reference):**
```
# pg_hba.conf — require client certificate
hostssl all all 0.0.0.0/0 cert clientcert=verify-full
```

---

### AWS IAM Database Authentication

**Status:** ✅ Implemented

**How it works:**
1. Generate temporary auth token using AWS credentials (via default credential chain)
2. Token valid for 15 minutes
3. Use token as password for PostgreSQL connection

**Implementation Files:**
- `internal/db/aws_connector.go` - AWSIAMConnector implementation
- `internal/db/aws_token_provider.go` - Token acquisition via AWS SDK
- `internal/db/resolver.go` - AWSFlags, environment variable loading
- `internal/cli/deploy.go` - CLI flags (`--aws`, `--aws-region`)

**Architecture:**
```go
// TokenProvider interface (shared with Azure)
type TokenProvider interface {
    GetToken(ctx context.Context) (token string, expiresOn time.Time, err error)
    String() string
}

// AWSIAMTokenProvider acquires IAM authentication tokens for RDS
type AWSIAMTokenProvider struct {
    endpoint string // host:port
    region   string
    username string
}
```

**Environment Variables:**
```bash
export AWS_REGION="us-west-2"           # Required: AWS region
export AWS_ACCESS_KEY_ID="..."          # Optional: explicit credentials
export AWS_SECRET_ACCESS_KEY="..."      # Optional: explicit credentials
# Or use ~/.aws/credentials, IAM role, etc. (standard AWS credential chain)
```

**CLI Flags:**
```bash
# IAM role (EC2, ECS, Lambda — no credentials needed)
pgmi deploy ./migrations \
  --host mydb.abc123.us-west-2.rds.amazonaws.com \
  -d mydb -U myuser \
  --aws --aws-region us-west-2 \
  --sslmode require

# Region from environment
export AWS_REGION="us-west-2"
pgmi deploy ./migrations \
  --host mydb.abc123.us-west-2.rds.amazonaws.com \
  -d mydb -U myuser \
  --aws \
  --sslmode require
```

**Authentication Methods:**

1. **IAM Role** (`--aws` on EC2/ECS/Lambda) — for AWS-hosted workloads. No credentials needed, uses instance metadata.

2. **IAM User** (env vars or config file + `--aws`) — for local development or CI/CD. Requires `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` or `~/.aws/credentials`.

3. **Default Credential Chain** (`--aws`) — automatic fallback order: environment vars → shared credentials file → IAM role.

**RDS Setup Requirements:**
```sql
-- On RDS instance, enable IAM for user
CREATE USER myuser;
GRANT rds_iam TO myuser;
GRANT ALL ON DATABASE mydb TO myuser;
```

**IAM Policy:**
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "rds-db:connect",
      "Resource": "arn:aws:rds-db:region:account:dbuser:db-resource-id/myuser"
    }
  ]
}
```

**Activation:**
- Use `--aws` flag to explicitly enable AWS IAM authentication
- `--aws-region` specifies the region (or use `AWS_REGION`/`AWS_DEFAULT_REGION` env vars)
- Standard auth used when `--aws` flag is not present

---

### Google Cloud SQL IAM Authentication

**Status:** ✅ Implemented

**How it works:**
1. Create a Cloud SQL dialer with IAM authentication enabled
2. Configure pgx to use the Cloud SQL connector's custom dialer
3. Cloud SQL connector handles authentication, TLS, and connection management

**Implementation Files:**
- `internal/db/google_connector.go` - GoogleCloudSQLConnector implementation
- `internal/db/resolver.go` - GoogleFlags, applyGoogleAuth()
- `internal/cli/deploy.go` - CLI flags (`--google`, `--google-instance`)

**Architecture:**
```go
// GoogleCloudSQLConnector wraps the Cloud SQL Go Connector
type GoogleCloudSQLConnector struct {
    config   *pgmi.ConnectionConfig
    instance string // project:region:instance
}

func (c *GoogleCloudSQLConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
    // Create dialer with IAM auth
    dialer, _ := cloudsqlconn.NewDialer(ctx, cloudsqlconn.WithIAMAuthN())

    // Configure custom DialFunc
    poolConfig.ConnConfig.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
        return dialer.Dial(ctx, c.instance)
    }

    return pgxpool.NewWithConfig(ctx, poolConfig)
}
```

**CLI Flags:**
```bash
# Service account (GCE, GKE, Cloud Run — no credentials needed)
pgmi deploy ./migrations \
  -d mydb -U myuser@myproject.iam \
  --google --google-instance myproject:us-central1:myinstance

# Local development with gcloud auth
gcloud auth application-default login
pgmi deploy ./migrations \
  -d mydb -U myuser@myproject.iam \
  --google --google-instance myproject:us-central1:myinstance

# With service account key file
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/key.json"
pgmi deploy ./migrations \
  -d mydb -U myuser@myproject.iam \
  --google --google-instance myproject:us-central1:myinstance
```

**Authentication Methods:**

1. **Service Account** (GCE, GKE, Cloud Run) — for GCP-hosted workloads. No credentials needed, uses instance metadata.

2. **Application Default Credentials** (`gcloud auth application-default login`) — for local development. Uses `gcloud` CLI credentials.

3. **Service Account Key** (`GOOGLE_APPLICATION_CREDENTIALS`) — for CI/CD or explicit service account. Points to JSON key file.

**Cloud SQL Setup Requirements:**
```sql
-- Create IAM user (email format)
CREATE USER "myuser@myproject.iam" WITH LOGIN;
GRANT ALL ON DATABASE mydb TO "myuser@myproject.iam";
```

**GCP Configuration:**
- Cloud SQL Admin API must be enabled
- Cloud SQL instance must have IAM database authentication enabled
- Service account needs `Cloud SQL Client` role
- Instance connection name format: `project:region:instance`

**Instance Connection Name:**
The `--google-instance` flag requires the full instance connection name:
```
myproject:us-central1:myinstance
└─project─┘└─region──┘└instance─┘
```
Find this in GCP Console → Cloud SQL → Instance Overview.

---

## Connection Security

### SSL/TLS Configuration

**SSL Modes (PostgreSQL standard):**

| Mode | Eavesdropping Protection | MITM Protection | Description |
|------|--------------------------|-----------------|-------------|
| `disable` | ❌ No | ❌ No | No SSL (local dev only) |
| `allow` | ⚠️ Maybe | ❌ No | Try SSL, fallback to plain |
| `prefer` | ⚠️ Maybe | ❌ No | Try SSL first (default) |
| `require` | ✅ Yes | ❌ No | SSL required, no cert check |
| `verify-ca` | ✅ Yes | ⚠️ Partial | SSL + verify CA |
| `verify-full` | ✅ Yes | ✅ Yes | SSL + verify CA + hostname |

**Recommendations:**

**Development:**
```bash
# Local PostgreSQL, SSL overhead not needed
postgresql://postgres:password@localhost:5432/mydb?sslmode=disable
```

**Staging:**
```bash
# SSL required, but self-signed cert acceptable
postgresql://postgres:password@staging.db:5432/mydb?sslmode=require
```

**Production:**
```bash
# Full verification (recommended)
postgresql://postgres:password@prod.db:5432/mydb?sslmode=verify-full
```

**Production with mTLS:**
```bash
# Client certificate authentication (via CLI flags)
pgmi deploy . \
  --host prod.db -d myapp \
  --sslmode verify-full \
  --sslcert /path/to/client.crt \
  --sslkey /path/to/client.key \
  --sslrootcert /path/to/ca.crt

# Or via pgmi.yaml
# connection:
#   sslcert: /path/to/client.crt
#   sslkey: /path/to/client.key
#   sslrootcert: /path/to/ca.crt

# Or via environment variables
export PGSSLCERT=/path/to/client.crt
export PGSSLKEY=/path/to/client.key
export PGSSLROOTCERT=/path/to/ca.crt
export PGSSLPASSWORD=keypass  # if key is encrypted
```

**Cloud Providers:**
- **AWS RDS:** `sslmode=require` minimum (IAM requires SSL)
- **Azure Database:** `sslmode=require` minimum
- **Google Cloud SQL:** SSL enforced by default

### Connection Pooling

**pgmi's Approach:**
- pgmi does NOT use connection pooling
- Single session for entire deployment
- Session closed when deployment completes

**Why No Pooling?**
- ✅ **Session-centric model:** Temporary tables and session variables require single session
- ✅ **Simplicity:** No pool lifecycle management
- ✅ **Deployment use case:** Short-lived operations (minutes), not long-running service

**When You Need Pooling:**
- Use PgBouncer or similar in front of PostgreSQL
- pgmi connects to pooler, pooler connects to PostgreSQL
- Session pooling mode recommended (transaction mode breaks temp tables)

---

## Timeout Configuration

### Timeout Hierarchy

**1. CLI Timeout (`--timeout` flag)**
```bash
pgmi deploy ./migrations -d mydb --timeout 30m
```
- **Purpose:** Catastrophic failure protection
- **Default:** 3 minutes
- **Scope:** Entire deployment operation
- **Behavior:** Hard stop if exceeded

**2. PostgreSQL Connection Timeout**
```bash
postgresql://postgres@localhost/mydb?connect_timeout=10
```
- **Purpose:** Prevent hanging on connection
- **Default:** System default (often unlimited)
- **Scope:** Initial connection establishment
- **Behavior:** Fail if connection not established

**3. PostgreSQL Statement Timeout**
```sql
-- In deploy.sql or SQL files
SET statement_timeout = '60s';
```
- **Purpose:** Prevent runaway queries
- **Default:** Disabled (no limit)
- **Scope:** Each SQL statement
- **Behavior:** Cancel statement if exceeded

**4. PostgreSQL Lock Timeout**
```sql
-- In deploy.sql
SET lock_timeout = '10s';
```
- **Purpose:** Fail fast if lock unavailable
- **Default:** Disabled (wait forever)
- **Scope:** Lock acquisition
- **Behavior:** Error if lock not acquired

**Recommended Configuration:**

**Development:**
```bash
# Short timeout to catch issues fast
pgmi deploy ./migrations -d mydb --timeout 3m
```

**Production:**
```sql
-- In deploy.sql
-- Long-running DDL expected, explicit timeout
SET statement_timeout = '5min';
SET lock_timeout = '10s'; -- Fail fast if can't get lock
```

```bash
# CLI timeout covers full deployment
pgmi deploy ./migrations -d mydb --timeout 30m
```

---

## Environment Variables

### Supported Variables

**PGMI_CONNECTION_STRING:**
```bash
# Set connection string via env var
export PGMI_CONNECTION_STRING="postgresql://postgres:password@localhost:5432/postgres"
pgmi deploy ./migrations -d mydb
```
- Overridden by `--connection` flag if provided

**Standard PostgreSQL Variables:**
- `PGHOST` - Database host
- `PGPORT` - Database port (default: 5432)
- `PGDATABASE` - Database name
- `PGUSER` - Username
- `PGPASSWORD` - Password (⚠️ less secure, prefer .pgpass)
- `PGSSLMODE` - SSL mode
- `PGSSLCERT` - Client certificate path
- `PGSSLKEY` - Client private key path
- `PGSSLROOTCERT` - Root CA certificate path
- `PGSSLPASSWORD` - Password for encrypted client key

**Azure Variables:**
- `AZURE_TENANT_ID` - Azure AD tenant/directory ID
- `AZURE_CLIENT_ID` - Azure AD application/client ID
- `AZURE_CLIENT_SECRET` - Azure AD client secret (Service Principal auth)

---

## Troubleshooting

### Connection Issues

**1. "connection refused"**
```
Error: dial tcp 127.0.0.1:5432: connect: connection refused
```
**Causes:**
- PostgreSQL not running
- Wrong host/port
- Firewall blocking

**Solutions:**
```bash
# Check PostgreSQL running
pg_isready -h localhost -p 5432

# Check listening ports
netstat -an | grep 5432

# Test connection
psql -h localhost -p 5432 -U postgres -d postgres
```

**2. "password authentication failed"**
```
Error: pq: password authentication failed for user "postgres"
```
**Causes:**
- Wrong password
- Wrong username
- `pg_hba.conf` not allowing connection

**Solutions:**
```bash
# Check pg_hba.conf
# Look for: host all all 0.0.0.0/0 md5

# Test credentials
psql "postgresql://postgres:password@localhost:5432/postgres"

# Check user exists
psql -U postgres -c "SELECT usename FROM pg_user;"
```

**3. "SSL connection required"**
```
Error: pq: SSL is required
```
**Cause:** Server requires SSL but client not using SSL

**Solution:**
```bash
# Add sslmode=require
pgmi deploy ./migrations \
  --connection "postgresql://postgres:password@localhost:5432/mydb?sslmode=require"
```

**4. "database does not exist"**
```
Error: pq: database "mydb" does not exist
```
**Causes:**
- Target database not created
- Connected to wrong database

**Solutions:**
```bash
# Use --db flag to create database
pgmi deploy ./migrations \
  --connection "postgresql://postgres:password@localhost:5432/postgres" \
  -d mydb --overwrite --force

# Or create manually
psql -U postgres -c "CREATE DATABASE mydb;"
```

**5. "too many connections"**
```
Error: pq: sorry, too many clients already
```
**Causes:**
- PostgreSQL max_connections exceeded
- Connection leak in application

**Solutions:**
```sql
-- Check current connections
SELECT count(*) FROM pg_stat_activity;

-- Check limit
SHOW max_connections;

-- Increase limit (restart required)
ALTER SYSTEM SET max_connections = 200;

-- Or kill idle connections
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE state = 'idle' AND query_start < now() - interval '10 minutes';
```

---

## Quick Reference

### Connection String Examples

**Local Development:**
```bash
postgresql://postgres:password@localhost:5432/mydb
```

**Cloud Providers:**
```bash
# AWS RDS (standard auth)
postgresql://myuser@mydb.abc123.us-east-1.rds.amazonaws.com:5432/mydb?sslmode=require

# AWS RDS (IAM auth)
pgmi deploy ./migrations \
  --host mydb.abc123.us-west-2.rds.amazonaws.com \
  -d mydb -U myuser \
  --aws --aws-region us-west-2 \
  --sslmode require

# Azure Database for PostgreSQL (Entra ID)
# Use --azure flag (required for Managed Identity, auto-detected with env vars)
pgmi deploy ./migrations \
  --host myserver.postgres.database.azure.com \
  --database mydb --azure \
  --sslmode require

# Google Cloud SQL (IAM auth)
pgmi deploy ./migrations \
  -d mydb -U myuser@myproject.iam \
  --google --google-instance myproject:us-central1:myinstance
```

**ADO.NET Format:**
```bash
Host=localhost;Port=5432;Database=mydb;Username=postgres;Password=password
```

### Common CLI Patterns

**Create database and deploy:**
```bash
pgmi deploy ./migrations \
  --connection "postgresql://postgres:password@localhost/postgres" \
  -d mydb \
  --overwrite --force
```

**Deploy to existing database:**
```bash
pgmi deploy ./migrations \
  --connection "postgresql://postgres:password@localhost/mydb"
```

**Use environment variable:**
```bash
export PGMI_CONNECTION_STRING="postgresql://postgres:password@localhost/postgres"
pgmi deploy ./migrations -d mydb
```

**Azure Entra ID authentication:**
```bash
# Managed Identity (system-assigned, no credentials needed)
pgmi deploy ./migrations \
  --host myserver.postgres.database.azure.com \
  --database mydb --azure \
  --sslmode require

# Service Principal (env vars + --azure flag)
export AZURE_TENANT_ID="your-tenant-id"
export AZURE_CLIENT_ID="your-client-id"
export AZURE_CLIENT_SECRET="your-client-secret"
pgmi deploy ./migrations \
  --host myserver.postgres.database.azure.com \
  --database mydb --azure \
  --sslmode require

# Flag overrides for tenant/client
pgmi deploy ./migrations \
  --host myserver.postgres.database.azure.com \
  --database mydb --azure \
  --azure-tenant-id "other-tenant" \
  --azure-client-id "other-client" \
  --sslmode require
```

**AWS IAM authentication:**
```bash
# IAM role (EC2, ECS, Lambda — no credentials needed)
pgmi deploy ./migrations \
  --host mydb.abc123.us-west-2.rds.amazonaws.com \
  -d mydb -U myuser \
  --aws --aws-region us-west-2 \
  --sslmode require

# IAM user (env vars or ~/.aws/credentials)
export AWS_ACCESS_KEY_ID="your-access-key"
export AWS_SECRET_ACCESS_KEY="your-secret-key"
export AWS_REGION="us-west-2"
pgmi deploy ./migrations \
  --host mydb.abc123.us-west-2.rds.amazonaws.com \
  -d mydb -U myuser \
  --aws \
  --sslmode require
```

**Google Cloud SQL IAM authentication:**
```bash
# Service account (GCE, GKE, Cloud Run — no credentials needed)
pgmi deploy ./migrations \
  -d mydb -U myuser@myproject.iam \
  --google --google-instance myproject:us-central1:myinstance

# Local development with gcloud auth
gcloud auth application-default login
pgmi deploy ./migrations \
  -d mydb -U myuser@myproject.iam \
  --google --google-instance myproject:us-central1:myinstance

# With service account key file
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/key.json"
pgmi deploy ./migrations \
  -d mydb -U myuser@myproject.iam \
  --google --google-instance myproject:us-central1:myinstance
```

**mTLS client certificate authentication:**
```bash
# Cert flags (additive, work with any connection method)
pgmi deploy ./migrations -d myapp \
  --sslmode verify-full \
  --sslcert /path/to/client.crt \
  --sslkey /path/to/client.key \
  --sslrootcert /path/to/ca.crt

# Combined with connection string (no conflict)
pgmi deploy ./migrations \
  --connection "postgresql://user@host/postgres" -d myapp \
  --sslcert /path/to/client.crt \
  --sslkey /path/to/client.key

# Via environment variables
export PGSSLCERT=/path/to/client.crt
export PGSSLKEY=/path/to/client.key
export PGSSLROOTCERT=/path/to/ca.crt
pgmi deploy ./migrations -d myapp --sslmode verify-full
```

---

## See Also

- **pgmi-deployment skill:** Session-centric model, execution flow
- **pgmi-cli skill:** CLI design philosophy, flags, parameters
- **CLAUDE.md:** Two-database pattern, CLI design implications
- **PostgreSQL Connection Strings:** https://www.postgresql.org/docs/current/libpq-connect.html

