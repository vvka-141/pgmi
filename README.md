# pgmi

[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](https://opensource.org/licenses/MPL-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![CI](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml/badge.svg)](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml)
[![Watch Introduction](https://img.shields.io/badge/▶_Watch-Introduction-red?logo=youtube)](https://youtu.be/0txwCsGRyyE)

**Programmable PostgreSQL deployments, controlled entirely by SQL.**

pgmi loads your project files into one PostgreSQL session as queryable data, then runs the `deploy.sql` **you** write. Your deployment selects migrations, loads reference data, tests the changed database — and commits only if everything passes. A failing test rolls the whole deployment back.

Migration frameworks provide their own ordering, history, and transaction model. pgmi gives those decisions to your deploy.sql. (Architecturally it's an *execution fabric*, not a migration framework — [Why pgmi?](docs/WHY-PGMI.md) explains the distinction.)

![Test-gated deployment: apply files, test the changed database, commit only if tests pass — otherwise rollback, database unchanged](docs/diagrams/d00-test-gated-deploy.drawio.svg)

## See it work

Nothing running? Start a disposable PostgreSQL in Docker (already have one? point `PGMI_CONNECTION_STRING` at it and skip the first line):

```bash
docker run -d --name pgmi-demo -e POSTGRES_PASSWORD=postgres -p 5434:5432 postgres:17-alpine
export PGMI_CONNECTION_STRING="postgresql://postgres:postgres@127.0.0.1:5434/postgres"
# PowerShell: $env:PGMI_CONNECTION_STRING = "postgresql://postgres:postgres@127.0.0.1:5434/postgres"

pgmi init demo --template basic
pgmi deploy demo -d demo_db
```

```text
Database "demo_db" does not exist; creating
Preparing session: scanning files, loading parameters
Loaded 7 files
Loaded 1 parameters
Executing deploy.sql
[development] Deploying demo v1.0.0 (5 file(s) in project)
Dev seed: admin user ready (admin@example.com id=1)
[pgmi] Test suite started
[pgmi] Fixture: ./__test__/_setup.sql
[pgmi] Test: ./__test__/test_user_crud.sql
[pgmi] Test suite completed (3 steps)

  ___   ___  _  _ ___
 |   \ / _ \| \| | __|
 | |) | (_) | .` | _|
 |___/ \___/|_|\_|___|

✓ 7 files loaded, 1 test macro(s) expanded in 0.91s
```

Now the failure case: add a migration creating an `audit_log` table, and a test asserting it contains a `deploy` event (it won't — nothing inserts one):

<details>
<summary>The two files behind the failure demo</summary>

```sql
-- migrations/003_audit_log.sql
CREATE TABLE audit_log (
    event text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
```

```sql
-- __test__/test_audit_log.sql
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM audit_log WHERE event = 'deploy') THEN
        RAISE EXCEPTION 'audit_log must contain a deploy event';
    END IF;
END $$;
```

</details>

```text
[pgmi] Test suite started
[pgmi] Test: ./__test__/test_audit_log.sql
✗ Failed after 0.72s — see error above
pgmi: error: execution failed: ERROR: audit_log must contain a deploy event (SQLSTATE P0001)
```

pgmi exits with code `13`, the transaction aborts, and the `audit_log` table from the new migration **does not exist** — the database is exactly as it was before the deploy. Tests run inside the deployment transaction (each isolated in its own savepoint, so test data never persists), and only a fully verified deployment commits.

A complete, CI-verified version of this pattern lives in [`examples/test-gated-deploy/`](examples/test-gated-deploy/) — both paths run on every push.

> **Requirements:** PostgreSQL 11+ (advanced template 15+ — [compatibility matrix](docs/PRODUCTION.md#postgresql-compatibility)) over a **direct** connection or session-mode pooler. Transaction-mode poolers (PgBouncer txn mode, RDS Proxy) reassign connections between statements and destroy the session temp tables pgmi depends on — [details](docs/PRODUCTION.md#connection-requirements).

## The entire model in one file

`deploy.sql` is plain PostgreSQL. Your files are rows in a session view; you query them and decide what to execute:

```sql
-- deploy.sql
BEGIN;

DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE is_sql_file
        ORDER BY path
    ) LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

CALL pgmi_test();

COMMIT;
```

Filter by directory, branch on a `--param`, skip files whose checksum already ran, load JSON/XML/CSV reference data with PostgreSQL's built-in functions — it's your SQL. See the [deploy.sql Guide](docs/DEPLOY-GUIDE.md) for patterns and the [Session API](docs/session-api.md) for the views (`pgmi_source_view` for raw path-ordered access, `pgmi_plan_view` for metadata-driven ordering).

**Why it feels different:**

- **Your SQL owns the deploy** — transactions, execution order, idempotency, and retries live in `deploy.sql`, not in the tool.
- **The CLI is infrastructure-only** — connections, parameters, auth. No `--dry-run`, no `--rollback`, no orchestration flags to learn.
- **Built for coding agents** — the binary embeds machine-readable guidance (`pgmi ai`, llms.txt style) and can expose pgmi commands to agents over MCP (`pgmi serve`).

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

**Homebrew (macOS):**
```bash
brew install --cask vvka-141/pgmi/pgmi
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

Then follow the [Getting Started Guide](docs/QUICKSTART.md) for a complete walkthrough, or the [CI/CD Guide](docs/CICD.md) to deploy from a pipeline.

## Choose your path

- **Just deploying SQL?** → the **basic** template: a small, explicit migration scaffold. `pgmi init myapp --template basic`
- **Building a PostgreSQL-backed application?** → the **advanced** template: an editable reference system with role separation, audit logging, and REST/RPC/MCP patterns — yours to own and trim, not a framework to adopt wholesale. Privilege requirements and managed-provider notes: [Production Guide](docs/PRODUCTION.md#managed-cloud-postgresql).
- **Evaluating the approach first?** → read [Why pgmi?](docs/WHY-PGMI.md) and the honest [Tradeoffs](docs/TRADEOFFS.md).

Either template can be adapted for production; advanced provides *more infrastructure*, not a higher safety tier. The [Choosing a template](docs/QUICKSTART.md#choosing-a-template) section has a side-by-side decision table; `pgmi templates list` shows what's available.

## When pgmi makes sense — and when it doesn't

A good fit when you need:

- **Conditional deployment logic** — different behavior per environment, feature flags, custom phases
- **Test-gated deployments** — database tests inside the deployment transaction, rollback on failure ([Testing Guide](docs/TESTING.md))
- **Explicit transaction control** — you decide where `BEGIN` and `COMMIT` go
- **Data files alongside schema** — JSON config, XML reference data, CSV seeds in the same transaction as migrations
- **Multi-cloud PostgreSQL targets** — the same `deploy.sql` works on Azure, AWS, GCP with native auth

A poor fit when:

- **Your team avoids SQL/PL/pgSQL** — pgmi's power is writing deployment logic in PostgreSQL's language; without that fluency the advantage disappears
- **You want tool-managed migration history** — pgmi ships no version table; tracking state is a pattern you implement (or take from the advanced template), not a built-in
- **Your connection path is a transaction-mode pooler** — session temp tables can't survive it

[Tradeoffs](docs/TRADEOFFS.md) is the full honest list.

## Documentation

📖 Browse the full docs as a searchable site at **<https://vvka-141.github.io/pgmi/>** (built from this `docs/` directory). The links below open the same pages on GitHub.

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
| [Coming from Flyway/Liquibase/Sqitch](docs/COMING-FROM.md) | Migration guides |

**Reference**

| Guide | Description |
|-------|-------------|
| [CLI Reference](docs/CLI.md) | All commands, flags, exit codes |
| [Configuration](docs/CONFIGURATION.md) | pgmi.yaml reference and zero-flag deployments |
| [Session API](docs/session-api.md) | Temp tables and helper functions |
| [Connections](docs/CONNECTIONS.md) | Connection architecture: cloud auth, SSL, poolers, IaC |
| [Metadata](docs/METADATA.md) | Optional script tracking and ordering |
| [Security](docs/SECURITY.md) | Secrets and CI/CD patterns |
| [CI/CD](docs/CICD.md) | Deploy from GitHub Actions and other pipelines |
| [Production Guide](docs/PRODUCTION.md) | Performance, rollback, monitoring, [compatibility matrix](docs/PRODUCTION.md#postgresql-compatibility) |
| [Advanced-template MCP gateway](docs/MCP.md) | Expose your deployed application to AI assistants |

## AI assistant support

pgmi embeds machine-readable documentation directly in the binary, so coding agents can learn pgmi's conventions on demand:

```bash
pgmi ai                    # Overview for AI assistants (llms.txt style)
pgmi ai skills             # List embedded skills
pgmi ai skill pgmi-sql     # Print one skill's full content
pgmi ai contract           # Print the session API contract (views/functions)
pgmi ai setup              # Materialize a discoverable skill into the project
pgmi ai check              # Report whether that skill exists and is current
```

`pgmi serve` additionally exposes pgmi commands as MCP tools over stdio — see the [CLI reference](docs/CLI.md#pgmi-serve). (The advanced template's [MCP gateway](docs/MCP.md) is a separate surface: it exposes your *deployed application* to AI assistants.)

## Configuration and authentication

Store connection defaults and parameters in `pgmi.yaml` next to `deploy.sql` for zero-flag deployments (`pgmi deploy .`) — see [Configuration](docs/CONFIGURATION.md).

Beyond standard PostgreSQL auth (connection strings, `PGPASSWORD`, `.pgpass`), pgmi authenticates natively to **Azure Database for PostgreSQL** (Entra ID), **Amazon RDS** (IAM), and **Google Cloud SQL** (IAM) — passwordless, using each cloud's credential chain. Commands and setup: [Connections](docs/CONNECTIONS.md).

## Contributing

Contributions welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

[Mozilla Public License 2.0](LICENSE). Template code in `internal/scaffold/templates/` is [MIT licensed](internal/scaffold/templates/LICENSE)—code you generate is yours.

Copyright 2024-2026 Alexey Evlampiev
