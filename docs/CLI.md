# CLI Reference

Complete reference for all pgmi commands. Every example is copy-paste ready.

For a guided walkthrough, see [QUICKSTART.md](QUICKSTART.md).

---

## Global Flags

These flags work with every command:

| Flag | Description |
|------|-------------|
| `-v, --verbose` | Enable verbose output (also shows PostgreSQL `RAISE DEBUG` messages) |
| `-h, --help` | Show help for any command |

---

## pgmi deploy

Execute a database deployment.

```bash
pgmi deploy <project_path> [flags]
```

pgmi connects to PostgreSQL, loads your project files into session temp tables, then runs `deploy.sql` which directly executes your files.

### Connection Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--connection` | `$PGMI_CONNECTION_STRING` or `$DATABASE_URL` | Full connection string (PostgreSQL URI or ADO.NET). Mutually exclusive with granular flags. |
| `--host` | `$PGHOST` or `localhost` | PostgreSQL server host |
| `-p, --port` | `$PGPORT` or `5432` | PostgreSQL server port |
| `-U, --username` | `$PGUSER` or OS user | PostgreSQL user |
| `-d, --database` | `$PGDATABASE` or from connection string | Target database name |
| `--sslmode` | `$PGSSLMODE` or `prefer` | SSL mode: `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full` |
| `--sslcert` | `$PGSSLCERT` | Path to client SSL certificate file |
| `--sslkey` | `$PGSSLKEY` | Path to client SSL private key file |
| `--sslrootcert` | `$PGSSLROOTCERT` | Path to root CA certificate for server verification |

### Deployment Flags

| Flag | Description |
|------|-------------|
| `--overwrite` | Drop and recreate the target database before deploying. **Local development only.** |
| `--force` | Replace interactive confirmation with 5-second countdown. Still shows warning, still cancellable with Ctrl+C. |
| `--timeout` | Catastrophic failure protection (default: `3m`). Examples: `30s`, `5m`, `1h30m` |
| `--compat` | API compatibility version (default: latest). Pin to a specific version for stable CI/CD pipelines. |

#### Understanding `--compat` (API Versioning)

The `--compat` flag pins your deployment to a specific pgmi session API version. This ensures your `deploy.sql` continues working even when pgmi upgrades introduce new features or internal changes.

**Currently supported versions:**

| Version | Status | Notes |
|---------|--------|-------|
| `1` | **Current / Latest** | Initial stable API |

**When to use `--compat`:**

```bash
# CI/CD pipelines: pin to a specific version for reproducibility
pgmi deploy . -d myapp --compat=1

# Local development: use latest (default, no flag needed)
pgmi deploy . -d myapp
```

**What the API version controls:**
- Session views: `pg_temp.pgmi_source_view`, `pg_temp.pgmi_plan_view`, `pg_temp.pgmi_parameter_view`, etc.
- Public functions: `pg_temp.pgmi_test_plan()`, `pg_temp.pgmi_test_generate()`
- Column names and types in views

**What it does NOT control:**
- CLI flags and behavior (CLI versioning is separate)
- Internal tables (`_pgmi_*`) — these are implementation details

**Error handling:**

```bash
# Invalid version returns clear error with supported versions
$ pgmi deploy . --compat=99
Error: unsupported API version "99"; supported: [1]
```

**Best practice:** Pin `--compat` in CI/CD pipelines for stability. When upgrading pgmi, test with the new default version before updating your pinned version.

#### API Version Changelog

**Version 1** (Current)
- Initial stable API release
- Views: `pgmi_source_view`, `pgmi_plan_view`, `pgmi_parameter_view`, `pgmi_test_source_view`, `pgmi_test_directory_view`, `pgmi_source_metadata_view`
- Functions: `pgmi_test_plan()`, `pgmi_test_generate()`, `pgmi_is_sql_file()`, `pgmi_persist_test_plan()`
- Preprocessor macro: `CALL pgmi_test()`

See [session-api.md](session-api.md) for complete API documentation.

#### Understanding `--overwrite` Safety

The `--overwrite` flag triggers a **destructive operation**: the target database is dropped and recreated. pgmi provides safety mechanisms to prevent accidents:

**Without `--force` (interactive mode):**
```
⚠️  WARNING: You are about to DROP and RECREATE the database 'myapp'
This will permanently delete all data in this database!

To confirm, type the database name 'myapp' and press Enter: _
```
You must type the exact database name. Typos cancel the operation.

**With `--force` (countdown mode):**
```
╔═══════════════════════════════════════════════════════════════════════╗
      ______
   .-'      '-.
  /            \           ⚠️  DANGER: DESTRUCTIVE OPERATION ⚠️
 |,  .-.  .-.  ,|       Database 'myapp' will be PERMANENTLY DELETED
 | )(_o/  \o_)( |                ALL DATA WILL BE LOST
  \__|IIIIII|__/
╚═══════════════════════════════════════════════════════════════════════╝

Dropping in: 5 seconds... (Press Ctrl+C to cancel)
```
A 5-second countdown gives you time to cancel with Ctrl+C.

**When to use `--overwrite`:**
- Local development with disposable databases
- CI/CD pipelines deploying to **ephemeral test databases** (not production!)
- Never on production or staging databases with real data

### Parameter Flags

| Flag | Description |
|------|-------------|
| `--param key=value` | Set a parameter (repeatable). Accessible in SQL via `current_setting('pgmi.key')` |
| `--params-file path` | Load parameters from `.env` file (repeatable, later files override earlier ones) |

### Azure Entra ID Flags

| Flag | Description |
|------|-------------|
| `--azure` | Enable Azure Entra ID authentication. Uses `DefaultAzureCredential` chain (Managed Identity, Azure CLI, etc.) |
| `--azure-tenant-id` | Azure AD tenant/directory ID (overrides `$AZURE_TENANT_ID`) |
| `--azure-client-id` | Azure AD application/client ID (overrides `$AZURE_CLIENT_ID`) |

### AWS IAM Flags

| Flag | Description |
|------|-------------|
| `--aws` | Enable AWS IAM database authentication. Uses default AWS credential chain (env vars, config file, IAM role, etc.) |
| `--aws-region` | AWS region for RDS endpoint (overrides `$AWS_REGION`) |

### Google Cloud SQL IAM Flags

| Flag | Description |
|------|-------------|
| `--google` | Enable Google Cloud SQL IAM database authentication. Uses Application Default Credentials (gcloud auth, service account, etc.) |
| `--google-instance` | Cloud SQL instance connection name (format: `project:region:instance`). Required when `--google` is specified. |

### Password

Passwords are never passed as CLI flags. Use one of:

```bash
# Environment variable
export PGPASSWORD="your-password"

# Connection string
pgmi deploy . --connection "postgresql://user:pass@localhost:5432/postgres" -d myapp

# .pgpass file (PostgreSQL standard)
# ~/.pgpass format: hostname:port:database:username:password
```

### Examples

```bash
# Deploy (creates the database if new, deploys incrementally if it exists)
pgmi deploy ./myproject -d myapp

# Recreate database for local development (shows 5-second countdown)
pgmi deploy ./myproject -d myapp_dev --overwrite --force

# Full connection string
pgmi deploy ./myproject --connection "postgresql://postgres:secret@db.example.com:5432/postgres" -d myapp

# Pin to specific API version for CI/CD stability
pgmi deploy ./myproject -d myapp --compat=1

# With parameters
pgmi deploy ./myproject -d myapp --param env=production --param version=2.1.0

# Parameters from file + CLI override
pgmi deploy ./myproject -d myapp \
  --params-file base.env \
  --params-file prod.env \
  --param version=2.1.0

# Longer timeout for large deployments
pgmi deploy ./myproject -d myapp --timeout 30m

# Verbose output (see RAISE DEBUG messages)
pgmi deploy ./myproject -d myapp --verbose

# Azure Entra ID with Managed Identity (no credentials needed)
pgmi deploy ./myproject -d myapp --azure \
  --host myserver.postgres.database.azure.com \
  --sslmode require

# Azure Entra ID with Service Principal
pgmi deploy ./myproject -d myapp \
  --azure-tenant-id "your-tenant-id" \
  --azure-client-id "your-client-id"

# mTLS with client certificate
pgmi deploy ./myproject -d myapp \
  --sslmode verify-full \
  --sslcert /path/to/client.crt \
  --sslkey /path/to/client.key \
  --sslrootcert /path/to/ca.crt

# mTLS combined with connection string
pgmi deploy ./myproject \
  --connection "postgresql://user@host/postgres" -d myapp \
  --sslcert /path/to/client.crt \
  --sslkey /path/to/client.key
```

### The Two-Database Pattern

The connection string specifies the **maintenance database** (used to run `CREATE DATABASE`). The `-d` flag specifies the **target database** (the one being created/deployed to):

```bash
# Connect to 'postgres' (maintenance), create and deploy to 'myapp' (target)
pgmi deploy . --connection "postgresql://user@host/postgres" -d myapp
```

---

## pgmi init

Scaffold a new pgmi project.

```bash
pgmi init <target_path> [flags]
```

Creates a ready-to-deploy project structure with `deploy.sql`, directory layout, and README.

| Flag | Default | Description |
|------|---------|-------------|
| `-t, --template` | `basic` | Template to use (`basic` or `advanced`) |

Use `pgmi templates list` to see all available templates with descriptions.

### Templates

| Template | Purpose |
|----------|---------|
| `basic` | Minimal structure for learning. Simple `migrations/` folder with `deploy.sql`. |
| `advanced` | Production-ready. 4-schema architecture, role hierarchy, metadata-driven deployment. |

### Examples

```bash
# Create a project in the current directory
pgmi init .

# Create a named project with the basic template
pgmi init myapp

# Production-ready project
pgmi init myapp --template advanced

# See available templates
pgmi templates list
```

---

## pgmi metadata

Offline metadata operations (no database connection required).

### pgmi metadata scaffold

Generate `<pgmi-meta>` blocks for SQL files that lack them.

```bash
pgmi metadata scaffold <project_path> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--write` | | Write metadata to files (without this flag, dry-run only) |
| `--dry-run` | `true` | Preview changes without modifying files |
| `--idempotent` | `true` | Mark generated scripts as idempotent |

```bash
# Preview what would be generated
pgmi metadata scaffold ./myproject

# Write metadata to files
pgmi metadata scaffold ./myproject --write
```

### pgmi metadata validate

Check metadata for syntax, schema compliance, and duplicate IDs.

```bash
pgmi metadata validate <project_path> [flags]
```

| Flag | Description |
|------|-------------|
| `--json` | Output results as JSON |

```bash
pgmi metadata validate ./myproject
pgmi metadata validate ./myproject --json
```

### pgmi metadata plan

Show the execution plan derived from metadata sort keys.

```bash
pgmi metadata plan <project_path> [flags]
```

| Flag | Description |
|------|-------------|
| `--json` | Output plan as JSON |

```bash
pgmi metadata plan ./myproject
pgmi metadata plan ./myproject --json
```

---

## pgmi templates

Browse and inspect available project templates.

### pgmi templates list

```bash
pgmi templates list
```

### pgmi templates describe

```bash
pgmi templates describe <template_name>
```

```bash
# See what the advanced template includes
pgmi templates describe advanced
```

---

## pgmi version

```bash
pgmi version
```

---

## pgmi ai

AI-digestible documentation for coding assistants. Outputs structured markdown that AI tools can parse and learn from.

### pgmi ai (overview)

```bash
pgmi ai
```

Outputs an overview document similar to llms.txt format, explaining:
- What pgmi is and its philosophy
- Core concepts (session tables, deploy.sql pattern)
- Quick start commands
- Available skills and when to use them
- Key SQL conventions

### pgmi ai skills

```bash
pgmi ai skills
```

Lists all embedded skills with descriptions:

```
# Available pgmi Skills

| Skill | Description |
|-------|-------------|
| `pgmi-sql` | Use when writing SQL/PL/pgSQL or deploy.sql |
| `pgmi-philosophy` | Architectural decisions, execution fabric vs migration framework |
| `pgmi-cli` | Use when adding CLI commands or flags |
...
```

### pgmi ai skill

```bash
pgmi ai skill <name>
```

Outputs the full content of a specific skill. Use this to load detailed conventions for a particular domain:

```bash
# Load SQL conventions
pgmi ai skill pgmi-sql

# Load CLI design patterns
pgmi ai skill pgmi-cli

# Load testing patterns
pgmi ai skill pgmi-testing-review
```

### pgmi ai templates

```bash
pgmi ai templates
```

Lists available template documentation.

### pgmi ai template

```bash
pgmi ai template <name>
```

Outputs AI-focused documentation for a specific template:

```bash
# Basic template guide
pgmi ai template basic

# Advanced template architecture
pgmi ai template advanced
```

### AI Workflow Example

When an AI assistant encounters "use pgmi for my project":

```bash
# Step 1: Discover AI documentation exists
pgmi --help | grep ai

# Step 2: Get overview
pgmi ai

# Step 3: List available skills
pgmi ai skills

# Step 4: Load relevant skill
pgmi ai skill pgmi-sql

# Step 5: AI now understands pgmi conventions
```

---

## Environment Variables

pgmi respects standard PostgreSQL environment variables and its own:

| Variable | Used by | Description |
|----------|---------|-------------|
| `PGMI_CONNECTION_STRING` | `deploy` | Full connection string (highest priority) |
| `DATABASE_URL` | `deploy` | Full connection string (fallback) |
| `PGHOST` | `deploy` | Server host |
| `PGPORT` | `deploy` | Server port |
| `PGUSER` | `deploy` | Username |
| `PGPASSWORD` | `deploy` | Password |
| `PGDATABASE` | `deploy` | Database name |
| `PGSSLMODE` | `deploy` | SSL mode |
| `PGSSLCERT` | `deploy` | Client SSL certificate path |
| `PGSSLKEY` | `deploy` | Client SSL private key path |
| `PGSSLROOTCERT` | `deploy` | Root CA certificate path |
| `PGSSLPASSWORD` | `deploy` | Password for encrypted client key |
| `AZURE_TENANT_ID` | `deploy` | Azure AD tenant ID |
| `AZURE_CLIENT_ID` | `deploy` | Azure AD client ID |
| `AZURE_CLIENT_SECRET` | `deploy` | Azure AD client secret |
| `AWS_REGION` | `deploy` | AWS region for RDS IAM auth |
| `AWS_DEFAULT_REGION` | `deploy` | Fallback AWS region |

### Precedence

```
CLI flags  >  environment variables  >  pgmi.yaml  >  built-in defaults
```

---

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General error |
| `2` | CLI usage error (invalid arguments or flags) |
| `3` | Panic or unexpected system error |
| `10` | Invalid configuration or parameters |
| `11` | Database connection failed |
| `12` | User denied overwrite approval |
| `13` | SQL execution failed |
| `14` | `deploy.sql` not found |

---

## Common Error Messages

### Connection Errors (Exit Code 11)

| Error | Cause | Solution |
|-------|-------|----------|
| `connection refused` | PostgreSQL not running or wrong port | Check `pg_isready -h <host> -p <port>` |
| `password authentication failed` | Wrong credentials | Verify username/password, check `pg_hba.conf` |
| `database "X" does not exist` | Database not created | Create with `createdb X` or use `--overwrite` for fresh setup |
| `SSL connection required` | Server requires SSL | Add `?sslmode=require` to connection string |
| `no pg_hba.conf entry` | Client IP not allowed | Add entry to `pg_hba.conf` or use SSH tunnel |

### SQL Execution Errors (Exit Code 13)

| Error | Cause | Solution |
|-------|-------|----------|
| `relation "X" does not exist` | Table/view not found | Check execution order, ensure dependencies run first |
| `function "X" does not exist` | Missing function | Run schema files before files that call functions |
| `permission denied for schema` | Role lacks privileges | Grant permissions or run as superuser for setup |
| `current transaction is aborted` | Earlier error in transaction | Fix the root cause; check `RAISE EXCEPTION` in your SQL |
| `syntax error at or near` | Invalid SQL | Check the file path in error message, fix syntax |

### Configuration Errors (Exit Code 10)

| Error | Cause | Solution |
|-------|-------|----------|
| `missing required parameter` | CLI param not provided | Add `--param key=value` |
| `unknown parameter` | Param not declared in session.xml | Declare in session.xml or remove from CLI |
| `invalid regex pattern` | Bad pattern in `pgmi_test()` | Fix POSIX regex syntax |
| `unsupported API version` | Invalid `--compat` value | Use `--compat=1` (currently only v1 supported) |

### File Errors (Exit Code 14)

| Error | Cause | Solution |
|-------|-------|----------|
| `deploy.sql not found` | Missing orchestrator | Run `pgmi init` or create deploy.sql manually |
| `no SQL files found` | Empty project | Add `.sql` files to your project directory |

### Debugging Tips

1. **Add `--verbose`** to see DEBUG-level PostgreSQL notices
2. **Check the file path** in error messages — it tells you which file failed
3. **Run deploy.sql manually** with `psql -f deploy.sql` to isolate issues
4. **Use `RAISE NOTICE`** in your SQL to trace execution flow

---

## Quick Recipes

### CI/CD Pipeline (Production)

**Never use `--overwrite` in production.** Deploy incrementally to existing databases:

```bash
# Production deployment - incremental, no database recreation
pgmi deploy ./myproject \
  --host db.example.com \
  --username deployer \
  -d myapp_prod \
  --param env=production \
  --timeout 15m
```

### CI/CD Pipeline (Ephemeral Test Database)

For CI pipelines that create fresh test databases per run:

```bash
# Create ephemeral test database, run tests, then tear down
pgmi deploy ./myproject \
  --host db.example.com \
  --username deployer \
  -d "myapp_ci_${CI_JOB_ID}" \
  --overwrite --force \
  --param env=ci \
  --timeout 10m

# Tests run via CALL pgmi_test() in deploy.sql
# If all tests pass, deployment commits
# If any test fails, deployment rolls back

# Clean up: drop the ephemeral database after tests
# (Use your CI platform's cleanup mechanism)
```

### Local Development

```bash
export PGPASSWORD="postgres"
# Deploy with tests (pgmi_test() in deploy.sql gates the commit)
pgmi deploy . -d myapp_dev --overwrite --force
```

### mTLS Client Certificate

```bash
# CLI flags (additive — works with connection string or granular flags)
pgmi deploy ./myproject -d myapp \
  --sslmode verify-full \
  --sslcert /path/to/client.crt \
  --sslkey /path/to/client.key \
  --sslrootcert /path/to/ca.crt

# Combined with connection string
pgmi deploy ./myproject \
  --connection "postgresql://user@host/postgres" -d myapp \
  --sslcert /path/to/client.crt \
  --sslkey /path/to/client.key \
  --sslrootcert /path/to/ca.crt

# Via environment variables
export PGSSLCERT=/path/to/client.crt
export PGSSLKEY=/path/to/client.key
export PGSSLROOTCERT=/path/to/ca.crt
export PGSSLPASSWORD=keypass  # if key is encrypted
pgmi deploy ./myproject -d myapp --sslmode verify-full

# Via pgmi.yaml (committed, paths are not secrets)
# connection:
#   sslcert: /path/to/client.crt
#   sslkey: /path/to/client.key
#   sslrootcert: /path/to/ca.crt
```

### Azure Entra ID (Passwordless)

```bash
# System-assigned Managed Identity (no credentials needed)
pgmi deploy ./myproject \
  --host myserver.postgres.database.azure.com \
  -d myapp --azure \
  --sslmode require

# User-assigned Managed Identity (specify client ID)
pgmi deploy ./myproject \
  --host myserver.postgres.database.azure.com \
  -d myapp --azure \
  --azure-client-id "your-managed-identity-client-id" \
  --sslmode require

# Service Principal (credentials via env vars)
export AZURE_TENANT_ID="your-tenant-id"
export AZURE_CLIENT_ID="your-client-id"
export AZURE_CLIENT_SECRET="your-client-secret"
pgmi deploy ./myproject \
  --host myserver.postgres.database.azure.com \
  -d myapp --azure \
  --sslmode require
```

### AWS IAM (RDS)

```bash
# IAM role (EC2, ECS, Lambda — no credentials needed)
pgmi deploy ./myproject \
  --host mydb.abc123.us-west-2.rds.amazonaws.com \
  -d myapp -U myuser \
  --aws --aws-region us-west-2 \
  --sslmode require

# IAM user (credentials via env vars or ~/.aws/credentials)
export AWS_ACCESS_KEY_ID="your-access-key"
export AWS_SECRET_ACCESS_KEY="your-secret-key"
pgmi deploy ./myproject \
  --host mydb.abc123.us-west-2.rds.amazonaws.com \
  -d myapp -U myuser \
  --aws --aws-region us-west-2 \
  --sslmode require

# Region from environment
export AWS_REGION="us-west-2"
pgmi deploy ./myproject \
  --host mydb.abc123.us-west-2.rds.amazonaws.com \
  -d myapp -U myuser \
  --aws \
  --sslmode require
```

### Google Cloud SQL IAM

```bash
# Service account (GCE, GKE, Cloud Run — no credentials needed)
pgmi deploy ./myproject \
  -d myapp -U myuser@myproject.iam \
  --google --google-instance myproject:us-central1:myinstance

# Local development with gcloud auth
gcloud auth application-default login
pgmi deploy ./myproject \
  -d myapp -U myuser@myproject.iam \
  --google --google-instance myproject:us-central1:myinstance

# With service account key file
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/key.json"
pgmi deploy ./myproject \
  -d myapp -U myuser@myproject.iam \
  --google --google-instance myproject:us-central1:myinstance
```
