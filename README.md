# pgmi

[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](https://opensource.org/licenses/MPL-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev/)
[![CI](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml/badge.svg)](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml)

pgmi runs your PostgreSQL deployments—but **you** control the transactions, order, and logic.

![pgmi deployment flow](pgmi-deploy.png)

Unlike migration frameworks that decide when to commit and what to run, pgmi loads your files into PostgreSQL temp tables and runs your `deploy.sql`—a script **you** write in SQL that controls everything.

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
        SELECT path, content FROM pg_temp.pgmi_source
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

For production projects, pgmi provides [plan functions](docs/session-api.md) that give you explicit control over transaction boundaries, execution phases, and testing—see the [Session API](docs/session-api.md).

## Install

**Go (all platforms):**
```bash
go install github.com/vvka-141/pgmi/cmd/pgmi@latest
```

**Homebrew (macOS/Linux):**
```bash
brew install vvka-141/pgmi/pgmi
```

**Debian/Ubuntu:**
```bash
curl -1sLf 'https://dl.cloudsmith.io/vvka-141/pgmi/setup.deb.sh' | sudo bash
sudo apt update && sudo apt install pgmi
```

**Windows:** Download from [GitHub Releases](https://github.com/vvka-141/pgmi/releases) or use `go install` above.

## Get started

The fastest path to your first deployment:

```bash
pgmi init myapp --template basic
cd myapp
pgmi deploy . --database mydb --overwrite --force
```

This creates a project with `deploy.sql`, runs it against a fresh database, and executes any SQL files it finds.

See the [Getting Started Guide](docs/QUICKSTART.md) for a complete walkthrough.

## When pgmi makes sense

pgmi is a good fit when you need:

- **Conditional deployment logic** — different behavior per environment, feature flags, custom phases
- **Explicit transaction control** — you decide where `BEGIN` and `COMMIT` go
- **Full PostgreSQL power** — use PL/pgSQL, query system catalogs, leverage `pg_advisory_lock`

Consider simpler tools if you only need linear numbered migrations with framework-managed transactions.

See [Why pgmi?](docs/WHY-PGMI.md) for a detailed comparison with other tools.

## Documentation

| Guide | Description |
|-------|-------------|
| [Getting Started](docs/QUICKSTART.md) | Your first deployment in 10 minutes |
| [Why pgmi?](docs/WHY-PGMI.md) | When pgmi's approach makes sense |
| [Coming from Flyway/Liquibase](docs/COMING-FROM.md) | Migration guides |
| [CLI Reference](docs/CLI.md) | All commands, flags, exit codes |
| [Configuration](docs/CONFIGURATION.md) | pgmi.yaml reference |
| [Session API](docs/session-api.md) | Temp tables and helper functions |
| [Testing](docs/TESTING.md) | Database tests with automatic rollback |
| [Metadata](docs/METADATA.md) | Optional script tracking and ordering |
| [Security](docs/SECURITY.md) | Secrets and CI/CD patterns |
| [Production Guide](docs/PRODUCTION.md) | Performance, rollback, monitoring |

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

## Built-in testing

Tests live in `__test__/` or `__tests__/` directories and run in transactions with automatic rollback:

```bash
pgmi test ./myapp -d test_db
```

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

## Authentication

pgmi supports:

- **Standard PostgreSQL** — connection strings, `PGPASSWORD`, `.pgpass`
- **Azure Entra ID** — passwordless auth to Azure Database for PostgreSQL

```bash
# Standard
export PGMI_CONNECTION_STRING="postgresql://user:pass@localhost/postgres"
pgmi deploy . -d mydb

# Azure Entra ID
export AZURE_TENANT_ID="..."
export AZURE_CLIENT_ID="..."
pgmi deploy . --host myserver.postgres.database.azure.com -d mydb
```

AWS IAM and GCP Cloud SQL support is on the roadmap.

## Contributing

Contributions welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

[Mozilla Public License 2.0](LICENSE). Template code in `internal/scaffold/templates/` is [MIT licensed](internal/scaffold/templates/LICENSE)—code you generate is yours.

Copyright 2024-2025 Alexey Evlampiev
