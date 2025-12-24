# pgmi

[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](https://opensource.org/licenses/MPL-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev/)
[![CI](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml/badge.svg)](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml)

**pgmi** is a PostgreSQL-native execution fabric built in Go. Unlike traditional migration frameworks that impose rigid patterns, pgmi takes a session-centric approach: it loads SQL files and runtime parameters into PostgreSQL temporary tables and hands over complete control to your `deploy.sql`. From there, **you** orchestrate the deployment using PostgreSQL itself—no framework magic, no hidden behavior.

---

## ✨ Philosophy

**pgmi is an execution fabric, not a migration framework.**

- **PostgreSQL-first**: pgmi feels like a native extension of PostgreSQL, not an abstraction layer.
- **Minimal interference**: No enforced frameworks or hidden behaviors—*your SQL drives everything*.
- **Clear separation of concerns**: pgmi prepares the session and executes your plan. **You** control transactions, execution order, retry logic, and idempotency in `deploy.sql`.
- **Infrastructure, not orchestration**: pgmi provides connection management, parameter injection, and plan execution. **You** write the deployment logic in SQL.

---

## 🎯 Target Audience
pgmi is designed for advanced PostgreSQL users:
- Developers and DBAs who prefer direct SQL control.
- Architects needing robust, reproducible deployments.
- Teams finding ORM-based tools too restrictive.
- Platform/SRE teams building AI agents that must plan and execute database changes safely.

---

## 📥 Installation

### macOS (Homebrew)
```bash
brew tap vvka-141/pgmi
brew install pgmi
```

### Windows (Chocolatey)
```powershell
choco install pgmi
```

### Debian/Ubuntu (APT)
```bash
# Add repository (one-time)
curl -1sLf 'https://dl.cloudsmith.io/vvka-141/pgmi/setup.deb.sh' | sudo bash

# Install
sudo apt update && sudo apt install pgmi
```

### Direct Download
```bash
# Linux/macOS
curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | bash

# Or download from GitHub Releases
# https://github.com/vvka-141/pgmi/releases
```

### From Source
```bash
go install github.com/vvka-141/pgmi/cmd/pgmi@latest
```

---

## 💡 How pgmi is Different

**Traditional migration frameworks** (Flyway, Liquibase, etc.) make decisions for you:
- Framework decides transaction boundaries (`--single-transaction` flag)
- Framework decides execution order (filename conventions: `V001`, `V002`)
- Framework decides retry behavior (built-in retry policies)
- Framework decides idempotency (checksum-based skip logic)

**pgmi** gives you the infrastructure, **you** make the decisions:
- **You** write `BEGIN;` and `COMMIT;` in deploy.sql
- **You** query `pg_temp.pgmi_source` and order files however you want
- **You** write retry logic using PostgreSQL's `EXCEPTION` blocks
- **You** implement idempotency using PostgreSQL techniques (IF NOT EXISTS, ON CONFLICT, etc.)

**Example: Phased deployment with different transaction strategies**

```sql
-- deploy.sql: You control EVERYTHING
DO $$
BEGIN
    -- Phase 1: One transaction for foundations
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');
    PERFORM pg_temp.pgmi_plan_file('./pre-deployment/01-roles.sql');
    PERFORM pg_temp.pgmi_plan_file('./pre-deployment/02-extensions.sql');
    PERFORM pg_temp.pgmi_plan_command('COMMIT;');

    -- Phase 2: Separate transaction per migration (partial progress)
    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE directory = './migrations' ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_command('BEGIN;');
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
        PERFORM pg_temp.pgmi_plan_command('COMMIT;');
    END LOOP;

    -- Phase 3: No transaction wrapper for idempotent setup
    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE directory = './setup' ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);  -- Each file manages its own transactions
    END LOOP;
END $$;
```

No framework can give you this level of control without adding dozens of flags. With pgmi, it's just SQL.

---

## 🚀 Core Concepts
### The Session Model
When you run `pgmi deploy`, the tool:
1. Connects to PostgreSQL using your chosen method (password, certificates, IAM, Entra ID, etc.).
2. Prepares a session with temp tables:
   - `pg_temp.pgmi_source`: Metadata + contents of all user's SQL source files (excluding test files).
   - `pg_temp.pgmi_parameter`: Key/value parameters passed via CLI.
   - `pg_temp.pgmi_plan`: Execution plan built by deploy.sql.
   - `pg_temp.pgmi_unittest_script`: Test files from `__test__/` directories, isolated from deployment files.
3. Hands control to your `deploy.sql`, which drives the rest.

### deploy.sql: Your Deployment Orchestrator

**This is where YOU control everything.** `deploy.sql` is a PostgreSQL script that builds the execution plan by populating `pg_temp.pgmi_plan`. You decide:

- **Transaction boundaries**: Where to `BEGIN` and `COMMIT`
- **Execution order**: Which files run first, which run last
- **Conditional logic**: Different behavior for dev vs. production
- **Error handling**: Retry logic, savepoints, exception blocks
- **Idempotency**: How to handle re-runs

**Example deploy.sql:**
```sql
-- Step 1: Declare parameters (optional - for defaults and validation)
SELECT pg_temp.pgmi_declare_param(
    p_key => 'database_name',
    p_type => 'text',
    p_default_value => current_database()::TEXT,
    p_description => 'Target database name'
);

-- Step 2: Build execution plan
DO $$
DECLARE
    v_file RECORD;
BEGIN
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source
        WHERE directory ~* '^./migrations' AND is_sql_file
        ORDER BY path
    )
    LOOP
        PERFORM pg_temp.pgmi_plan_notice('Executing: %s', v_file.path);
        PERFORM pg_temp.pgmi_plan_command(v_file.content);
    END LOOP;

    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END;
$$;
```

After `deploy.sql` completes, pgmi reads commands from `pg_temp.pgmi_plan` and executes them sequentially.

---

## ⚡ What pgmi Provides

**Session Infrastructure:**
- **Session preparation**: Loads SQL files and parameters into PostgreSQL temp tables
- **Plan execution**: Executes commands from `pg_temp.pgmi_plan` sequentially
- **Deploy command**: Orchestrates the session lifecycle (connect → prepare → execute deploy.sql → execute plan)
- **Test command**: Execute database tests in isolated transactions with automatic rollback

**Developer Experience:**
- **Init command**: Scaffold new projects with templates (basic, advanced)
- **PostgreSQL-standard connection handling**: Supports standard credentials, environment variables, connection strings
- **Safe overwrite mode**: DBA-approved drop & recreate workflow with confirmation or `--force`
- **Parameter injection**: Pass parameters via CLI (`--param`) or files (`--params-file`)

**Flexibility:**
- **Pluggable design**: Distributed as both CLI and Go library
- **Cloud-ready auth**: Azure Entra ID (implemented), AWS IAM and GCP Cloud SQL (roadmap)
- **Your deployment logic**: Transaction boundaries, retry logic, and idempotency are controlled in `deploy.sql`, not by pgmi

---

## 🔧 CLI Usage

### Environment Setup
Set your database connection string using the `PGMI_CONNECTION_STRING` environment variable:

```bash
# Linux/Mac
export PGMI_CONNECTION_STRING="postgresql://user:pass@localhost:5432/postgres"

# Windows PowerShell
$env:PGMI_CONNECTION_STRING="postgresql://user:pass@localhost:5432/postgres"

# Windows CMD
set PGMI_CONNECTION_STRING=postgresql://user:pass@localhost:5432/postgres
```

Or use the `--connection` flag directly (not recommended for production):
```bash
pgmi deploy ./migrations --connection "postgresql://user:pass@localhost:5432/postgres" --database mydb
```

### Validate
Validate your project structure before deploying:

```bash
# Human-readable text output (default)
pgmi validate ./migrations

# JSON output for CI/CD pipelines
pgmi validate ./migrations --format json
```

### Deploy
Basic deployment:
```bash
pgmi deploy ./migrations --database mydb
```

Deploy with parameters:
```bash
pgmi deploy ./migrations \
  --database mydb \
  --param env=dev \
  --param api_key=secret \
  --verbose
```

Deploy with parameters from file:
```bash
# Load parameters from .env file
pgmi deploy ./migrations --database mydb --params-file production.env

# CLI parameters override file parameters
pgmi deploy ./migrations \
  --database mydb \
  --params-file base.env \
  --param environment=staging \
  --param version=1.2.3
```

Deploy with database recreation (use with caution):
```bash
pgmi deploy ./migrations --database mydb --overwrite --force
```

### Test
Run database tests against an existing database:

```bash
# Run all tests
pgmi test ./myapp --database test_db

# Run only specific tests using POSIX regex filter
pgmi test ./myapp --database test_db --filter "/auth/"

# Run pre-deployment tests only
pgmi test ./myapp --database test_db --filter ".*/pre-deployment/.*"

# List tests without executing (dry-run)
pgmi test ./myapp --database test_db --list

# Run tests with parameters
pgmi test ./myapp --database test_db --param test_user_id=123
```

**Test Command Characteristics:**
- Does NOT deploy code or modify database schema (use `deploy` for that)
- Executes ONLY test files from `__test__/` directories
- Runs tests in a transaction with automatic rollback (no side effects)
- Fails immediately on first test failure (PostgreSQL native fail-fast)
- Output is PostgreSQL `RAISE NOTICE` messages (minimal pgmi abstraction)
- Regex filtering automatically includes required setup/teardown scripts

**Typical workflow:**
```bash
# 1. Deploy to test database
pgmi deploy ./myapp --database test_db --overwrite --force

# 2. Run tests
pgmi test ./myapp --database test_db

# 3. Run only integration tests
pgmi test ./myapp --database test_db --filter ".*_integration\\.sql$"
```

### Init
Scaffold a new project:
```bash
pgmi init myapp --template basic
```

Available templates:
- `basic`: Minimal setup with simple migrations
- `advanced`: Production-ready template with metadata-driven deployment

This creates:
```
myapp/
├── deploy.sql        # Deployment orchestrator
├── migrations/       # SQL files
└── README.md
```

---

## 🔐 Authentication

### Standard Authentication
pgmi supports standard PostgreSQL authentication via connection strings or environment variables:

```bash
# Connection string with password
export PGMI_CONNECTION_STRING="postgresql://user:pass@localhost:5432/postgres"

# Or use PostgreSQL standard environment variables
export PGHOST=localhost
export PGPORT=5432
export PGUSER=myuser
export PGPASSWORD=mypassword
```

### Azure Entra ID Authentication

pgmi supports passwordless authentication to **Azure Database for PostgreSQL Flexible Server** using Azure Entra ID (formerly Azure AD).

**Environment Variables:**
```bash
export AZURE_TENANT_ID="your-tenant-id"
export AZURE_CLIENT_ID="your-client-id"
export AZURE_CLIENT_SECRET="your-client-secret"  # For Service Principal auth
```

**CLI Flags** (override environment variables):
```bash
pgmi deploy ./migrations \
  --host myserver.postgres.database.azure.com \
  --username myapp-sp \
  --database mydb \
  --sslmode require \
  --azure-tenant-id "your-tenant-id" \
  --azure-client-id "your-client-id"
```

**Authentication Methods:**

1. **Service Principal** (recommended for CI/CD):
   - Set all three env vars: `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`
   - pgmi acquires OAuth token and uses it as the PostgreSQL password

2. **DefaultAzureCredential Chain** (for local development):
   - Set only `AZURE_TENANT_ID` and `AZURE_CLIENT_ID` (no secret)
   - Uses Azure CLI credentials, Managed Identity, or other methods from the Azure SDK credential chain

**Azure Setup Requirements:**
- Azure Database for PostgreSQL Flexible Server with Entra ID authentication enabled
- Service Principal or user assigned as PostgreSQL admin in Azure portal
- SSL mode `require` (Azure PostgreSQL requires encrypted connections)

---

## 🛡️ Handling Secrets
- Secrets are passed via `--param` and stored in session-scoped tables.
- pgmi never logs or persists parameter values.
- Best practice: use secrets transiently, avoid logging or persisting them.

---

## 🗺️ Roadmap
- Configurable checksum normalization.
- Retry/error taxonomy for transient vs fatal errors.
- Pluggable secret managers (Vault, Azure Key Vault, AWS Secrets Manager).
- Built-in integration tests.

---

## 📦 Distribution
- Prebuilt binaries (Linux, macOS, Windows).
- Installable Go module for embedding pgmi logic into other systems.

---

## 🛠️ Development
Project structure:
```
cmd/pgmi/        # CLI entrypoints
internal/cli/        # Cobra commands
internal/services/   # Core services (Deployer implementation)
internal/db/         # Connection providers and parsers
internal/testing/    # Test helpers and utilities
pkg/pgmi/        # Public interfaces (Deployer, Connector, Approver)
```

### Testing

The project uses a layered testing approach:

**Unit Tests** (no database required):
```bash
go test -short ./...
```

**Integration Tests** (requires PostgreSQL):
```bash
# Set test database connection
export PGMI_TEST_CONN="postgresql://postgres:postgres@localhost:5433/postgres"

# Run all tests including integration
go test ./...

# Run integration tests only
go test ./internal/services ./internal/scaffold
```

**Test Organization**:
- **Pure unit tests**: `internal/checksum`, `internal/db`, `internal/params` - no external dependencies
- **Service integration tests**: `internal/services/deployer_integration_test.go` - tests Deployer interface with real database
- **Template tests**: `internal/scaffold/integration_test.go` - validates scaffold templates deploy correctly

**Test Helpers** (`internal/testing/dbhelper.go`):
- `RequireDatabase(t)` - Get test connection, skip if unavailable
- `NewTestDeployer(t)` - Create configured Deployer for testing
- `CleanupTestDB(t, connStr, dbName)` - Database cleanup
- `GetTestPool(t, connStr, dbName)` - Get connection pool for verification

**Best Practices**:
- Use `testing.Short()` to skip integration tests in CI fast paths
- Always clean up test databases with `t.Cleanup()`
- Test via public interfaces (`Deployer`, `Connector`) not CLI
- Use table-driven tests for comprehensive coverage

---

## 🤝 Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) for details on:
- Code of conduct
- Development setup
- Pull request process
- Coding standards

For security vulnerabilities, please see our [Security Policy](SECURITY.md).

---

## 📄 License

**pgmi** is licensed under the [Mozilla Public License 2.0 (MPL-2.0)](LICENSE).

**Template code** (in `internal/scaffold/templates/`) is licensed under the [MIT License](internal/scaffold/templates/LICENSE). Code generated by `pgmi init` becomes your property and may be used for any purpose, including proprietary and commercial applications.

Copyright 2024-2025 Alexey Evlampiev
