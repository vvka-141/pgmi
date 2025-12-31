# pgmi

[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](https://opensource.org/licenses/MPL-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev/)
[![CI](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml/badge.svg)](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml)

**pgmi** puts PostgreSQL in control of your database deployments.

Instead of an external runtime deciding what runs and when, pgmi loads all your project files into PostgreSQL temp tables and hands control to your `deploy.sql` script. You write the deployment logic in SQL. PostgreSQL executes it.

---

## ✨ Philosophy

**pgmi is an execution fabric, not a migration framework.**

- **PostgreSQL-first**: pgmi feels like a native extension of PostgreSQL, not an abstraction layer.
- **Minimal interference**: No enforced frameworks or hidden behaviors—*your SQL drives everything*.
- **Clear separation of concerns**: pgmi prepares the session and executes your plan. **You** control transactions, execution order, retry logic, and idempotency in `deploy.sql`.
- **Infrastructure, not orchestration**: pgmi provides connection management, parameter injection, and plan execution. **You** write the deployment logic in SQL.

---

## 🎯 Target Audience

pgmi is designed for teams that want direct control over their PostgreSQL deployments:

- **Developers and DBAs** who prefer writing deployment logic in SQL rather than configuring a framework
- **Architects** needing reproducible deployments with explicit transaction control and dependency ordering
- **Teams outgrowing traditional tools** who need conditional logic, environment-specific behavior, or custom execution patterns
- **Platform and SRE teams** building automation or AI agents that require deterministic, inspectable database operations

---

## 🧭 When to Use pgmi

**pgmi is a good fit when:**

- You need deployment logic beyond simple linear migrations—conditional execution, environment-specific behavior, or custom phases
- You want PostgreSQL, not a framework, to control transactions and execution order
- You're building automation that benefits from deterministic, inspectable deployments
- Your team is comfortable with SQL/PL/pgSQL and prefers direct database interaction

**Consider simpler tools if:**

- You only need linear, numbered migrations with no conditional logic
- Your team prefers framework-managed transactions and execution
- You want a larger ecosystem of GUI tools and third-party integrations

pgmi prioritizes control and transparency over convenience. It works well alongside teams who view SQL as a first-class language, not an implementation detail.

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

## 🚀 Quick Start

You don't need templates or scaffolding to use pgmi. A single `deploy.sql` file is enough.

### Step 1: Create a minimal project

```bash
mkdir hello-pgmi && cd hello-pgmi
```

Create `deploy.sql`:
```sql
DO $$
DECLARE
    v_file RECORD;
    v_count INT;
BEGIN
    SELECT COUNT(*) INTO v_count FROM pg_temp.pgmi_source;
    RAISE NOTICE '';
    RAISE NOTICE 'Hello from pgmi!';
    RAISE NOTICE 'I found % SQL file(s) in this session:', v_count;
    RAISE NOTICE '';

    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source ORDER BY path)
    LOOP
        RAISE NOTICE '  → %', v_file.path;
    END LOOP;
END $$;
```

### Step 2: Run it

```bash
pgmi deploy . --connection "postgresql://postgres:password@localhost/postgres" \
    --database hello_db
```

Output:
```
Hello from pgmi!
I found 0 SQL file(s) in this session:
```

No files yet—just `deploy.sql` itself, which pgmi executes directly.

### Step 3: Add a SQL file

Create `001_create_table.sql`:
```sql
CREATE TABLE greetings (
    id SERIAL PRIMARY KEY,
    message TEXT NOT NULL
);

INSERT INTO greetings (message) VALUES ('Hello, World!');
```

Run again:
```
Hello from pgmi!
I found 1 SQL file(s) in this session:

  → ./001_create_table.sql
```

Your file appeared in `pg_temp.pgmi_source`. It's now **inside PostgreSQL**, queryable with SQL.

### Step 4: Execute it

Update `deploy.sql` to execute the files you found:
```sql
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source
        WHERE path != './deploy.sql'
        ORDER BY path
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

Run it—your table is created.

### The key insight

During deployment, your SQL files aren't on the filesystem (from PostgreSQL's perspective). They're **in a temp table**. You query them with SQL. You decide what to execute, in what order, with what conditions. That's the entire model.

### Using templates

For production projects, `pgmi init` scaffolds well-structured starting points:

```bash
pgmi init myapp --template basic      # Simple structure
pgmi init myapp --template advanced   # Metadata-driven deployment
```

Templates provide proven patterns, but they're optional. You can always start with just `deploy.sql`.

---

## 💡 How pgmi is Different

### The Paradigm Shift

Most deployment tools treat PostgreSQL as a **target**—they run deployment logic externally and send commands to the database. pgmi takes a different approach: PostgreSQL itself becomes the **deployment engine**.

| Approach | How it works |
|----------|--------------|
| Traditional tools | External runtime (Java/Python) executes logic → sends SQL to PostgreSQL |
| pgmi | Loads files into temp tables → PostgreSQL executes `deploy.sql` → PostgreSQL builds the plan |

This means your deployment logic is written in PostgreSQL's native language. You can use PL/pgSQL, query system catalogs, leverage `pg_advisory_lock` for coordination, or implement any pattern PostgreSQL supports.

### What You Control

Traditional migration frameworks make certain decisions for you. pgmi takes a different approach—it provides infrastructure and leaves decisions to your SQL:

| Aspect | Traditional approach | pgmi approach |
|--------|---------------------|---------------|
| Transaction boundaries | Framework flag (`--single-transaction`) | You write `BEGIN`/`COMMIT` in deploy.sql |
| Execution order | Filename convention (`V001_`, `V002_`) | You query `pg_temp.pgmi_source` and sort as needed |
| Retry logic | Framework's built-in policy | You implement with `EXCEPTION` blocks |
| Idempotency | Framework's checksum-based skipping | You implement with `IF NOT EXISTS`, `ON CONFLICT`, etc. |
| Conditional logic | Limited or via framework DSL | Full PL/pgSQL, query `pg_catalog`, any PostgreSQL feature |

### Example: Phased Deployment

```sql
-- deploy.sql: You control the deployment strategy
DO $$
BEGIN
    -- Phase 1: One transaction for foundations
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');
    PERFORM pg_temp.pgmi_plan_file('./pre-deployment/01-roles.sql');
    PERFORM pg_temp.pgmi_plan_file('./pre-deployment/02-extensions.sql');
    PERFORM pg_temp.pgmi_plan_command('COMMIT;');

    -- Phase 2: Separate transaction per migration (allows partial progress)
    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE directory = './migrations' ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_command('BEGIN;');
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
        PERFORM pg_temp.pgmi_plan_command('COMMIT;');
    END LOOP;

    -- Phase 3: No transaction wrapper for idempotent setup
    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE directory = './setup' ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;
END $$;
```

### For Automation and AI Agents

pgmi's architecture is well-suited for automated workflows:

- **Deterministic**: Same folder contents + same parameters = same behavior. No hidden state between runs.
- **Inspectable**: Before execution, query `pg_temp.pgmi_source` to see exactly what will run. The plan is data, not opaque logic.
- **Native interface**: Automation generates SQL, not a proprietary DSL. Any system that can produce SQL files can use pgmi.
- **Clear signals**: Success = clean exit. Failure = PostgreSQL exception with standard error output. No ambiguous states.
- **Single session**: All deployment happens in one database session. No connection pool complexity, no distributed state.

### Environment-Aware Deployments

pgmi loads **all files** from your project—not just SQL. This enables powerful DevOps patterns where infrastructure provisioning outputs (Terraform, Kubernetes, etc.) feed directly into database configuration:

```
myapp/
├── deploy.sql
├── migrations/
│   └── 001_schema.sql
├── config/
│   └── environment.json    ← Terraform output, K8s ConfigMap, etc.
└── setup/
    └── 01_configure.sql
```

Your `deploy.sql` can read and process configuration files:

```sql
DO $$
DECLARE
    v_config JSONB;
BEGIN
    -- Load environment config from JSON file
    SELECT content::jsonb INTO v_config
    FROM pg_temp.pgmi_source
    WHERE path = './config/environment.json';

    -- Populate service registry from infrastructure output
    INSERT INTO app.service_endpoints (name, url, region)
    SELECT
        svc->>'name',
        svc->>'url',
        svc->>'region'
    FROM jsonb_array_elements(v_config->'services') AS svc
    ON CONFLICT (name) DO UPDATE
    SET url = EXCLUDED.url, region = EXCLUDED.region;

    RAISE NOTICE 'Configured % services', jsonb_array_length(v_config->'services');
END $$;
```

This pattern enables your database to be fully environment-aware, supporting use cases like service discovery, feature flags, and dynamic configuration—all driven by SQL.

### Portability

pgmi adds minimal abstraction over your SQL:

- Your SQL files remain standard PostgreSQL SQL
- `deploy.sql` is standard PL/pgSQL
- No proprietary annotations required in your migration files
- The helper functions (`pgmi_plan_file`, etc.) are conveniences—you can insert directly into `pg_temp.pgmi_plan` if preferred

If you later choose a different approach, your SQL files work unchanged.

---

## 🧪 Built-in Testing

pgmi treats tests as first-class citizens in your deployment project, not a separate concern.

### The Gated Deployment Pattern

Traditional workflow separates deployment from testing:
```
1. Run migration tool → deploy scripts
2. Run test framework → execute tests
3. If tests fail → figure out rollback
```

pgmi enables a different pattern—tests run **inside** the deployment transaction:

```sql
-- deploy.sql: Schedule deployment and tests in one transaction
DO $$
DECLARE
    v_file RECORD;
BEGIN
    -- Schedule transaction start
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    -- Schedule all scripts for execution
    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE is_sql_file ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;

    -- Schedule tests (run inside the same transaction)
    PERFORM pg_temp.pgmi_plan_tests();

    -- Schedule commit (only reached if everything passes)
    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
```

**Key insight**: The `pgmi_plan_*` functions don't execute immediately—they schedule commands for later execution. After `deploy.sql` completes, pgmi executes the plan sequentially. By scheduling `BEGIN` and `COMMIT` as commands, the actual file execution and tests run inside a single transaction.

If any test fails, PostgreSQL raises an exception and the entire transaction rolls back. **Successful deployment implies functional verification.**

### Test Isolation

Tests reside in `__test__/` directories within your project:

```
migrations/
  001_create_users.sql
  002_add_roles.sql
  __test__/
    _setup.sql              ← Shared fixtures (wrapped in SAVEPOINT)
    test_user_creation.sql
    test_role_assignment.sql
```

**Physical isolation**: During session setup, pgmi automatically separates test files from deployment files. Test files move to `pg_temp.pgmi_unittest_script`, preventing accidental execution during deployment.

**Transactional isolation**: All tests run inside a transaction with automatic rollback. Tests cannot leave side effects—no cleanup scripts needed.

### Hierarchical Fixtures

The `_setup.sql` files provide shared fixtures with automatic savepoint isolation:

```
__test__/
  _setup.sql                 ← Creates SAVEPOINT, runs fixtures
  test_a.sql
  test_b.sql
  integration/
    _setup.sql               ← Nested SAVEPOINT for integration tests
    test_full_flow.sql
```

**Savepoint-based cleanup**: pgmi creates a `SAVEPOINT` before each `_setup.sql` and automatically rolls back after tests complete. No teardown scripts needed—PostgreSQL's transactional semantics handle cleanup.

### Pure PostgreSQL Testing

pgmi does not provide an assertion framework. Tests are pure PostgreSQL:

```sql
-- __test__/test_email_validation.sql
DO $$
BEGIN
    -- Test valid email
    IF NOT validate_email('user@example.com') THEN
        RAISE EXCEPTION 'Valid email was rejected';
    END IF;

    -- Test invalid email should fail
    BEGIN
        PERFORM validate_email('not-an-email');
        RAISE EXCEPTION 'Invalid email was accepted';
    EXCEPTION WHEN OTHERS THEN
        -- Expected: validation error
        NULL;
    END;

    RAISE NOTICE 'Email validation tests passed';
END $$;
```

Tests succeed silently (or with `RAISE NOTICE`), fail loudly with `RAISE EXCEPTION`. First failure stops execution.

### Running Tests

After deployment, run tests separately with filtering:

```bash
# Run all tests
pgmi test ./myapp -d test_db

# Run only authentication tests
pgmi test ./myapp -d test_db --filter "/auth/"

# List tests without executing
pgmi test ./myapp -d test_db --list
```

The `pgmi test` command runs tests in an isolated transaction with automatic rollback—useful for verification without re-deploying.

---

## 🚀 Core Concepts
### The Session Model
When you run `pgmi deploy`, the tool:
1. Connects to PostgreSQL using your chosen method (password, certificates, IAM, Entra ID, etc.).
2. Prepares a session with temp tables:
   - `pg_temp.pgmi_source`: Metadata + contents of all project files (SQL, JSON, YAML, etc.) (excluding test files).
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
- **Session preparation**: Loads all project files (SQL, config, data) and parameters into PostgreSQL temp tables
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
- Regex filtering automatically includes required `_setup.sql` fixtures

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

**Cloud Authentication:**
- AWS IAM Database Authentication (RDS, Aurora)
- GCP Cloud SQL IAM Authentication

**Deployment Features:**
- Plan/dry-run mode: list pending actions without executing
- Placeholder rendering: deterministic substitution with validation
- Retry/error taxonomy for transient vs fatal errors
- Configurable checksum normalization

**Integrations:**
- Pluggable secret managers (Vault, Azure Key Vault, AWS Secrets Manager)
- Structured telemetry (JSON events for observability)

---

## 📦 Distribution

pgmi is available through multiple package managers:
- **Homebrew** (macOS/Linux): `brew install vvka-141/pgmi/pgmi`
- **APT** (Debian/Ubuntu): via Cloudsmith repository
- **Chocolatey** (Windows): `choco install pgmi`
- **Direct download**: Prebuilt binaries from GitHub Releases
- **Go module**: `go install github.com/vvka-141/pgmi/cmd/pgmi@latest`

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

## 💬 Getting Help

- **Questions**: Open a [GitHub Discussion](https://github.com/vvka-141/pgmi/discussions)
- **Bug reports**: File a [GitHub Issue](https://github.com/vvka-141/pgmi/issues)
- **Security issues**: See [SECURITY.md](SECURITY.md) (do not open public issues)

---

## 📄 License

**pgmi** is licensed under the [Mozilla Public License 2.0 (MPL-2.0)](LICENSE).

**Template code** (in `internal/scaffold/templates/`) is licensed under the [MIT License](internal/scaffold/templates/LICENSE). Code generated by `pgmi init` becomes your property and may be used for any purpose, including proprietary and commercial applications.

Copyright 2024-2025 Alexey Evlampiev
