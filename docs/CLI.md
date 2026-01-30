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

pgmi connects to PostgreSQL, loads your project files into session temp tables, runs `deploy.sql` to build an execution plan, then executes the plan.

### Connection Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--connection` | `$PGMI_CONNECTION_STRING` or `$DATABASE_URL` | Full connection string (PostgreSQL URI or ADO.NET). Mutually exclusive with granular flags. |
| `--host` | `$PGHOST` or `localhost` | PostgreSQL server host |
| `-p, --port` | `$PGPORT` or `5432` | PostgreSQL server port |
| `-U, --username` | `$PGUSER` or OS user | PostgreSQL user |
| `-d, --database` | `$PGDATABASE` or from connection string | Target database name |
| `--sslmode` | `$PGSSLMODE` or `prefer` | SSL mode: `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full` |

### Deployment Flags

| Flag | Description |
|------|-------------|
| `--overwrite` | Drop and recreate the target database before deploying |
| `--force` | Skip interactive confirmation prompt (use with `--overwrite` for CI/CD) |
| `--timeout` | Catastrophic failure protection (default: `3m`). Examples: `30s`, `5m`, `1h30m` |

### Parameter Flags

| Flag | Description |
|------|-------------|
| `--param key=value` | Set a parameter (repeatable). Accessible in SQL via `current_setting('pgmi.key')` |
| `--params-file path` | Load parameters from `.env` file (repeatable, later files override earlier ones) |

### Azure Entra ID Flags

| Flag | Description |
|------|-------------|
| `--azure-tenant-id` | Azure AD tenant/directory ID (overrides `$AZURE_TENANT_ID`) |
| `--azure-client-id` | Azure AD application/client ID (overrides `$AZURE_CLIENT_ID`) |

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
# Deploy to a new database
pgmi deploy ./myproject -d myapp

# Recreate database and deploy (no confirmation prompt)
pgmi deploy ./myproject -d myapp --overwrite --force

# Full connection string
pgmi deploy ./myproject --connection "postgresql://postgres:secret@db.example.com:5432/postgres" -d myapp

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

# Azure Entra ID (passwordless)
pgmi deploy ./myproject -d myapp \
  --azure-tenant-id "your-tenant-id" \
  --azure-client-id "your-client-id"
```

### The Two-Database Pattern

The connection string specifies the **maintenance database** (used to run `CREATE DATABASE`). The `-d` flag specifies the **target database** (the one being created/deployed to):

```bash
# Connect to 'postgres' (maintenance), create and deploy to 'myapp' (target)
pgmi deploy . --connection "postgresql://user@host/postgres" -d myapp
```

---

## pgmi test

Execute database unit tests.

```bash
pgmi test <project_path> [flags]
```

Runs test files discovered from `__test__/` directories. The database must already exist — use `pgmi deploy` first.

### Test-Specific Flags

| Flag | Description |
|------|-------------|
| `--filter` | POSIX regex to select tests (default: `.*` matches all) |
| `--list` | List matching tests without executing (dry-run) |

All [connection flags](#connection-flags), [parameter flags](#parameter-flags), and [Azure flags](#azure-entra-id-flags) from `deploy` also apply.

### Test Discovery

Tests are SQL files inside `__test__/` directories anywhere in your project:

```
myproject/
├── schema/
│   ├── tables.sql
│   └── __test__/
│       ├── _setup.sql          ← runs before tests in this directory
│       └── test_tables.sql     ← test file
└── functions/
    ├── api.sql
    └── __test__/
        └── test_api.sql
```

### Examples

```bash
# Run all tests
pgmi test ./myproject -d test_db

# Filter by path pattern
pgmi test ./myproject -d test_db --filter "/auth/"

# Only integration tests
pgmi test ./myproject -d test_db --filter ".*_integration\.sql$"

# List tests without executing
pgmi test ./myproject -d test_db --list

# Pass parameters to tests
pgmi test ./myproject -d test_db --param test_user_id=123
```

### Typical Workflow

```bash
# Deploy first, then test
pgmi deploy ./myproject -d test_db --overwrite --force
pgmi test ./myproject -d test_db
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
| `--list` | | List available templates |

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
pgmi init --list
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

## pgmi completion

Generate shell completion scripts.

```bash
# Bash
pgmi completion bash > /etc/bash_completion.d/pgmi

# Zsh
pgmi completion zsh > ~/.zsh/completions/_pgmi

# Fish
pgmi completion fish > ~/.config/fish/completions/pgmi.fish

# PowerShell
pgmi completion powershell > pgmi.ps1
```

---

## Environment Variables

pgmi respects standard PostgreSQL environment variables and its own:

| Variable | Used by | Description |
|----------|---------|-------------|
| `PGMI_CONNECTION_STRING` | `deploy`, `test` | Full connection string (highest priority) |
| `DATABASE_URL` | `deploy`, `test` | Full connection string (fallback) |
| `PGHOST` | `deploy`, `test` | Server host |
| `PGPORT` | `deploy`, `test` | Server port |
| `PGUSER` | `deploy`, `test` | Username |
| `PGPASSWORD` | `deploy`, `test` | Password |
| `PGDATABASE` | `deploy`, `test` | Database name |
| `PGSSLMODE` | `deploy`, `test` | SSL mode |
| `AZURE_TENANT_ID` | `deploy`, `test` | Azure AD tenant ID |
| `AZURE_CLIENT_ID` | `deploy`, `test` | Azure AD client ID |
| `AZURE_CLIENT_SECRET` | `deploy`, `test` | Azure AD client secret |

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

## Quick Recipes

### CI/CD Pipeline

```bash
export PGPASSWORD="$DB_PASSWORD"
pgmi deploy ./myproject \
  --host db.example.com \
  --username deployer \
  -d myapp \
  --overwrite --force \
  --param env=production \
  --timeout 15m
pgmi test ./myproject \
  --host db.example.com \
  --username deployer \
  -d myapp
```

### Local Development

```bash
export PGPASSWORD="postgres"
pgmi deploy . -d myapp_dev --overwrite --force
pgmi test . -d myapp_dev
```

### Azure Entra ID (Passwordless)

```bash
export AZURE_TENANT_ID="your-tenant-id"
export AZURE_CLIENT_ID="your-client-id"
export AZURE_CLIENT_SECRET="your-client-secret"
pgmi deploy ./myproject \
  --host myserver.postgres.database.azure.com \
  --username "your-client-id" \
  -d myapp \
  --sslmode require
```
