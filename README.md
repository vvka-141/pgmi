# pgmi

[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](https://opensource.org/licenses/MPL-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev/)
[![CI](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml/badge.svg)](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml)
[![Watch Introduction](https://img.shields.io/badge/▶_Watch-Introduction-red?logo=youtube)](https://youtu.be/0txwCsGRyyE)

pgmi gives PostgreSQL a deployment session containing your project files, then runs the `deploy.sql` **you** write. Test inside the transaction, branch on environment, audit changes, and commit atomically.
Unlike migration frameworks that decide when to commit and what to run, pgmi hands control to your SQL: **you** own the transactions, order, and logic.

**Why it feels different:**

- **Your SQL owns the deploy** — transactions, execution order, idempotency, and retries live in `deploy.sql`, not in the tool.
- **The CLI is infrastructure-only** — connections, parameters, auth. No `--dry-run`, no `--rollback`, no orchestration flags to learn.
- **AI- and MCP-native** — the binary ships embedded skills (`pgmi ai`), and the advanced template includes a Model Context Protocol backend.

![pgmi deployment flow](pgmi-deploy.png)


## Quick example

```sql
-- deploy.sql
-- pg_temp is PostgreSQL's session-scoped schema; your files exist only
-- for this session and are automatically dropped when it ends.
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE is_sql_file
        ORDER BY path
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

COMMIT;
```

```bash
pgmi deploy ./myapp --database mydb
```

Your files are in a temp table. You query them with SQL. You decide what to execute. That's the entire model.

The quick example above shows the core pattern: query files, execute with `EXECUTE`. The **basic** scaffold template uses `pgmi_source_view` (raw access, path-ordered); the **advanced** template uses `pgmi_plan_view` (metadata-driven ordering). See [Session API](docs/session-api.md) for when to use each.

pgmi loads **all** project files — not just SQL. Your `deploy.sql` can read JSON configuration, XML reference data, and CSV seeds from the same session views, processing them with PostgreSQL's built-in JSON, XML, and string functions. See [deploy.sql Guide](docs/DEPLOY-GUIDE.md) for data ingestion patterns.

## Install

**macOS / Linux:**
```bash
curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | bash
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.ps1 | iex
```

Prefer a package manager or a checksum-verified binary (recommended for CI and production):

**Homebrew (macOS/Linux):**
```bash
brew install vvka-141/pgmi/pgmi
```

**Debian/Ubuntu (APT, GPG-verified):**
```bash
curl -1sLf 'https://dl.cloudsmith.io/public/vvka-141/pgmi/setup.deb.sh' | sudo bash
sudo apt update && sudo apt install pgmi
```

**Direct download:** grab an archive from [GitHub Releases](https://github.com/vvka-141/pgmi/releases) and verify it against the published `checksums.txt`.

**From source** (requires the Go toolchain):
```bash
go install github.com/vvka-141/pgmi/cmd/pgmi@latest
```

## Get started

The fastest path to your first deployment:

```bash
pgmi init myapp --template basic
cd myapp
pgmi deploy . --database mydb --overwrite --force
```

This creates a project with `deploy.sql`, runs it against a fresh database, and executes the SQL files in `migrations/`.

> **Requirement:** the pgmi CLI and the basic template need **PostgreSQL 11+** (the advanced template needs **15+** — see the [compatibility matrix](docs/PRODUCTION.md#postgresql-compatibility)) and a **direct** connection (or a session-mode pooler). Transaction-mode poolers — PgBouncer in transaction mode, AWS RDS Proxy — reassign connections between statements and destroy the session temp tables pgmi depends on. See [Connection Requirements](docs/PRODUCTION.md#connection-requirements).

See the [Getting Started Guide](docs/QUICKSTART.md) for a complete walkthrough, or the [CI/CD Guide](docs/CICD.md) to deploy from a pipeline.

## Choose your path

- **Just deploying SQL?** → the **basic** template: a small, explicit migration scaffold (`pgmi init myapp --template basic`).
- **Building an app, API, or MCP backend?** → the **advanced** template: a large, editable reference system (roles, RLS, audit logging, REST/RPC/MCP) you own and trim.
- **Evaluating the approach first?** → read [Why pgmi?](docs/WHY-PGMI.md) and the honest [Tradeoffs](docs/TRADEOFFS.md).

Not sure which template? The [Choosing a template](docs/QUICKSTART.md#choosing-a-template) section has a side-by-side decision table — both templates are production-capable; advanced is _more complete_, not _more production_. Check the [compatibility matrix](docs/PRODUCTION.md#postgresql-compatibility) for version requirements (CLI + basic: PostgreSQL 11+; advanced: 15+).

## When pgmi makes sense

pgmi is a good fit when you need:

- **Conditional deployment logic** — different behavior per environment, feature flags, custom phases
- **Explicit transaction control** — you decide where `BEGIN` and `COMMIT` go
- **Full PostgreSQL power** — use PL/pgSQL, query system catalogs, take advisory locks via `pg_advisory_lock`
- **Data files alongside schema** — load JSON config, XML reference data, CSV seeds in the same transaction as migrations
- **Multi-cloud PostgreSQL targets** — same `deploy.sql` works on Azure, AWS, GCP with native auth (Entra ID, IAM)

pgmi handles simple linear migrations out of the box — the basic template does exactly this. Its additional power is there when you need it.

See [Why pgmi?](docs/WHY-PGMI.md) for a detailed comparison with other tools.

## Test-gated deployments

Tests live in `__test__/` or `__tests__/` directories. The `CALL pgmi_test()` macro runs them inside your deployment transaction with automatic savepoint isolation — so a failing test aborts the whole deploy and your database stays untouched:

```sql
-- deploy.sql
BEGIN;

-- ... your migrations ...

-- Each test runs in its own savepoint and rolls back automatically;
-- a RAISE EXCEPTION fails the test and aborts the transaction.
CALL pgmi_test();

COMMIT;
```

The macro wraps each test in a savepoint, executes it, and rolls back—so **test data never persists** while your migrations do. If any test fails, the entire transaction aborts and your database remains unchanged.

Tests are pure PostgreSQL—use `RAISE EXCEPTION` to fail:

```sql
-- __test__/test_users.sql
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM users WHERE email = 'test@example.com') THEN
        RAISE EXCEPTION 'Expected user not found';
    END IF;
END $$;
```

See [Testing Guide](docs/TESTING.md) for fixtures, hierarchical setup, and the gated deployment pattern.

## Documentation

**Start here**

| Guide | Description |
|-------|-------------|
| [Getting Started](docs/QUICKSTART.md) | Your first deployment in 10 minutes (binary-first) |
| [deploy.sql Guide](docs/DEPLOY-GUIDE.md) | Authoring patterns: data ingestion, environment branching, multi-phase |
| [Testing](docs/TESTING.md) | Database tests with automatic rollback |

**Why pgmi exists** (deep dives)

| Essay | Description |
|-------|-------------|
| [Why pgmi?](docs/WHY-PGMI.md) | When pgmi's approach makes sense |
| [Tradeoffs](docs/TRADEOFFS.md) | Honest limitations and who should use pgmi |
| [Coming from Flyway/Liquibase](docs/COMING-FROM.md) | Migration guides |

**Reference**

| Guide | Description |
|-------|-------------|
| [CLI Reference](docs/CLI.md) | All commands, flags, exit codes |
| [Configuration](docs/CONFIGURATION.md) | pgmi.yaml reference |
| [Session API](docs/session-api.md) | Temp tables and helper functions |
| [Connections](docs/CONNECTIONS.md) | Connection architecture: cloud auth, SSL, poolers, IaC |
| [Metadata](docs/METADATA.md) | Optional script tracking and ordering |
| [Security](docs/SECURITY.md) | Secrets and CI/CD patterns |
| [CI/CD](docs/CICD.md) | Deploy from GitHub Actions and other pipelines |
| [Production Guide](docs/PRODUCTION.md) | Performance, rollback, monitoring, [compatibility matrix](docs/PRODUCTION.md#postgresql-compatibility) |
| [MCP Integration](docs/MCP.md) | Model Context Protocol for AI assistants |

## Templates

pgmi ships with ready-to-use project templates:

```bash
pgmi templates list              # See available templates
pgmi templates describe basic    # See what a template includes
pgmi init myapp --template basic # Create a project
```

| Template | Purpose |
|----------|---------|
| `basic` | A small, explicit migration scaffold — linear migrations, minimal structure. Production-capable. |
| `advanced` | Full PostgreSQL application template: multi-schema, role hierarchy, audit logging, MCP integration, metadata-driven ordering. A working reference system to own and adapt — not a framework to adopt wholesale. Requires a superuser for initial role setup — see the [Production Guide](docs/PRODUCTION.md) for managed-cloud caveats. |

## AI assistant support

pgmi embeds AI-digestible documentation directly in the binary. AI coding assistants (Claude Code, GitHub Copilot, Gemini CLI) can discover and learn pgmi patterns:

```bash
pgmi ai                    # Overview for AI assistants (llms.txt style)
pgmi ai skills             # List embedded skills
pgmi ai skill pgmi-sql     # Print one skill's full content
pgmi ai contract           # Print the session API contract (views/functions)
pgmi ai setup              # Materialize a discoverable skill into the project
pgmi ai check              # Report whether that skill exists and is current
```

When you tell an AI assistant "use pgmi for this project", it can query these commands to understand pgmi's philosophy, conventions, and best practices.

## Zero-flag deployments

Store connection defaults in `pgmi.yaml`:

```yaml
connection:
  host: localhost
  database: myapp

params:
  env: development
```

Then deploy with no flags:

```bash
pgmi deploy .
```

Override per-environment:

```bash
pgmi deploy . -d staging_db --param env=staging
```

## Authentication

pgmi supports:

- **Standard PostgreSQL** — connection strings, `PGPASSWORD`, `.pgpass`
- **Azure Entra ID** — passwordless auth to Azure Database for PostgreSQL
- **AWS IAM** — token-based auth to Amazon RDS
- **Google Cloud SQL IAM** — passwordless auth via Cloud SQL Go Connector

```bash
# Standard
export PGMI_CONNECTION_STRING="postgresql://user:pass@localhost/postgres"
pgmi deploy . -d mydb

# Azure Entra ID — Managed Identity (no credentials needed)
pgmi deploy . --host myserver.postgres.database.azure.com -d mydb --azure --sslmode require

# Azure Entra ID — Service Principal
export AZURE_TENANT_ID="..." AZURE_CLIENT_ID="..." AZURE_CLIENT_SECRET="..."
pgmi deploy . --host myserver.postgres.database.azure.com -d mydb --azure --sslmode require

# AWS IAM — uses default credential chain (env vars, ~/.aws/credentials, IAM role)
pgmi deploy . --host mydb.abc123.us-west-2.rds.amazonaws.com -d mydb -U myuser --aws --aws-region us-west-2 --sslmode require

# Google Cloud SQL — uses Application Default Credentials (gcloud auth, service account)
pgmi deploy . -d mydb -U myuser@myproject.iam --google --google-instance myproject:us-central1:myinstance
```

## Contributing

Contributions welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

[Mozilla Public License 2.0](LICENSE). Template code in `internal/scaffold/templates/` is [MIT licensed](internal/scaffold/templates/LICENSE)—code you generate is yours.

Copyright 2024-2026 Alexey Evlampiev
