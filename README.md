# pgmi

[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](https://opensource.org/licenses/MPL-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![CI](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml/badge.svg)](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml)
[![Watch Introduction](https://img.shields.io/badge/▶_Watch-Introduction-red?logo=youtube)](https://youtu.be/0txwCsGRyyE)

**PostgreSQL deployments written in SQL, tested before they commit.**

pgmi loads your project files into one PostgreSQL session as queryable data, then
runs the `deploy.sql` **you** write. Your deployment selects migrations, loads
reference data, tests the changed database — and commits only if everything
passes. A failing test aborts the deployment transaction.

Migration frameworks decide ordering, history, and the transaction model for you.
With pgmi those decisions are SQL in your `deploy.sql` ([Why pgmi?](docs/WHY-PGMI.md)).

pgmi supports two uses, each with a template available through `pgmi init`:

- **basic** — deploy a schema with a small project: table files that run once
  (tracked in a ledger), views and functions that re-run on every deploy, and
  tests that gate the commit.
- **advanced** — build the application itself in PostgreSQL with a complete
  application layer, as code you own: REST, JSON-RPC and MCP handlers, a live
  OpenAPI document, and multi-tenant row-level security. Reference gateways in
  five languages serve it over HTTP.

Both templates write an `AGENTS.md` and skills into the project for coding agents.
In the v0.14 evaluation, all 20 fresh Claude Code and Codex runs deployed and
passed their own tests, and runs with a checklist defect fell from seven under
v0.13.1 to none ([method](docs/design/ai-guidance-in-the-scaffold.md#how-success-is-measured)).

![Test-gated deployment: apply files, test the changed database, commit only if tests pass — otherwise roll back transactional changes](docs/diagrams/d00-test-gated-deploy.drawio.svg)

## See it work

Install pgmi first using one of the commands in [Install](#install). Then, if
nothing is running, start a disposable PostgreSQL in Docker (already have one?
skip this block and point `PGMI_CONNECTION_STRING` at it):

```bash
docker run -d --name pgmi-demo -e POSTGRES_PASSWORD=postgres -p 5434:5432 postgres:17-alpine
docker exec pgmi-demo timeout 60 sh -c 'until pg_isready -h 127.0.0.1 -q; do sleep 1; done'
```

If either command prints an error — say, port 5434 is already in use — stop
here: run `docker rm pgmi-demo` and pick another port, in both `-p` and the
connection string below. Otherwise the deploy reaches whatever server holds it.

```bash
export PGMI_CONNECTION_STRING="postgresql://postgres:postgres@127.0.0.1:5434/postgres"
# PowerShell: $env:PGMI_CONNECTION_STRING = "postgresql://postgres:postgres@127.0.0.1:5434/postgres"

pgmi init demo --template basic
pgmi deploy demo -d demo_db
```

```text
Database "demo_db" does not exist; creating
Preparing session: scanning files, loading parameters
Loaded 14 files
Loaded 1 parameter(s)
Executing deploy.sql
[dev] Deploying demo v1.0.0
[pgmi] Test suite started
[pgmi] Test: ./__test__/test_function_privileges.sql
[pgmi] Test: ./physical/__test__/test_constraints.sql
[pgmi] Fixture: ./semantic/__test__/_setup.sql
[pgmi] Test: ./semantic/__test__/test_project.sql
[pgmi] Test: ./semantic/__test__/test_task.sql
[pgmi] Test suite passed

  ___   ___  _  _ ___
 |   \ / _ \| \| | __|
 | |) | (_) | .` | _|
 |___/ \___/|_|\_|___|

✓ demo_db: 14 files loaded, 2 paths ignored in 0.09s
```

Now the failure case: add a run-once file creating an `audit_log` table, and a test asserting it contains a `deploy` event (it won't — nothing inserts one):

<details>
<summary>The two files behind the failure demo</summary>

```sql
-- physical/003_audit_log.sql
/*
<pgmi-meta id="5b0e7c1a-9f3d-4e2b-8a61-0c4d2e7f9b13" idempotent="false">
</pgmi-meta>
*/
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
✗ demo_db: failed after 0.09s
pgmi: error: execution failed: ERROR: audit_log must contain a deploy event (SQLSTATE P0001)
WHERE: PL/pgSQL function inline_code_block line 4 at RAISE
SQL statement in ./__test__/test_audit_log.sql
PL/pgSQL function inline_code_block line 1 at EXECUTE
LOCATION: ./__test__/test_audit_log.sql
```

pgmi exits with code `13`, the transaction aborts, and the `audit_log` table from
the new file **does not exist**. Tests run inside the deployment transaction
(each isolated in its own savepoint), and only a deployment whose tests pass
commits. PostgreSQL does not roll back sequence advances or effects outside the
transaction.

A complete, CI-verified version of this pattern lives in [`examples/test-gated-deploy/`](examples/test-gated-deploy/) — both paths run on every push.

> **Status:** pgmi is pre-1.0. The session API may change in a minor release. Every change is listed under "Upgrading" in the release notes, and a snapshot test makes each change deliberate. Pin the pgmi version in CI.

> **Requirements:** PostgreSQL 15+ (advanced template 18+ — [compatibility matrix](docs/PRODUCTION.md#postgresql-compatibility)) over a **direct** connection or session-mode pooler. Transaction-mode poolers (PgBouncer txn mode, RDS Proxy) reassign connections between statements and destroy the session temp tables pgmi depends on — [details](docs/PRODUCTION.md#connection-requirements).

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

This minimal version runs every file on every deploy. pgmi keeps no history of its
own: what has run, and what may run again, is your SQL's decision too. The basic
template's `deploy.sql` records applied run-once files in a ledger table and skips
them on later deploys. If an applied file has changed, it refuses the deploy and
names the file.

Filter by directory, branch on a `--param`, skip files whose checksum already ran, load JSON/XML/CSV reference data with PostgreSQL's built-in functions — it's your SQL. See the [deploy.sql Guide](docs/DEPLOY-GUIDE.md) for patterns and the [Session API](docs/session-api.md) for the views (`pgmi_source_view` for raw path-ordered access, `pgmi_plan_view` for metadata-driven ordering).

**Why it feels different:**

- **Your SQL owns the deploy** — transactions, execution order, idempotency, and retries live in `deploy.sql`, not in the tool.
- **The CLI is infrastructure-only** — connections, parameters, auth. No `--dry-run`, no `--rollback`, no orchestration flags to learn.
- **One transaction unless you say otherwise** — everything through your first top-level `COMMIT` runs as one transaction; without a top-level `COMMIT`, the whole file is atomic. Later statements autocommit one at a time, as in psql, so top-level commands such as `CREATE INDEX CONCURRENTLY` work inside a deploy. See [the execution contract](docs/DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract).
- **Built for coding agents** — `pgmi init` writes the agent guidance into the project, the binary prints the session API that matches its version (`pgmi ai contract`), and `pgmi serve` exposes pgmi commands over MCP. See [AI assistant support](#ai-assistant-support).

## Install

**macOS / Linux:**
```bash
curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | bash
```
It installs to `/usr/local/bin`. Without root or sudo, choose a directory you own:
```bash
curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | INSTALL_DIR="$HOME/.local/bin" bash
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

**Direct download:** grab an archive or a Debian/Ubuntu `.deb` package from [GitHub Releases](https://github.com/vvka-141/pgmi/releases) and verify it against the published `checksums.txt`. Install a `.deb` with `sudo apt install ./pgmi_<version>_linux_amd64.deb`.

**From source** (requires the Go toolchain):
```bash
go install github.com/vvka-141/pgmi/cmd/pgmi@latest
```

Then follow the [Getting Started Guide](docs/QUICKSTART.md) for a complete walkthrough, or the [CI/CD Guide](docs/CICD.md) to deploy from a pipeline.

## Choose your path

- **Just deploying SQL?** → the **basic** template: a small two-layer project, run-once tables and re-run views and functions. `pgmi init myapp --template basic`
- **Building a PostgreSQL-backed application?** → the **advanced** template: about 18,000 lines of SQL and 15,000 lines of tests, scaffolded into your project as code you own. One handler registry drives REST, JSON-RPC, MCP tools and a live OpenAPI 3.1 document. Each route declares its isolation level and read-only mode, resolved before `BEGIN`. Sign-in sessions, multi-tenant membership with RLS, and audit trails of deployments and requests come with it. Reference gateways in Python, TypeScript, Go, C# and Rust serve it over HTTP; adapt the one in your language — [full capability tour](docs/advanced/_index.md). Requires PostgreSQL 18; privilege requirements: [Production Guide](docs/PRODUCTION.md#managed-cloud-postgresql).
- **Evaluating the approach first?** → read [Why pgmi?](docs/WHY-PGMI.md) and the honest [Tradeoffs](docs/TRADEOFFS.md).

Either template can be adapted for production. Choose advanced when the application needs its API, sign-in and tenancy infrastructure. The [Choosing a template](docs/QUICKSTART.md#choosing-a-template) section has a side-by-side decision table; `pgmi templates list` shows what's available.

## When pgmi makes sense — and when it doesn't

A good fit when you need:

- **Conditional deployment logic** — different behavior per environment, feature flags, custom phases
- **Test-gated deployments** — database tests inside the deployment transaction, rollback on failure ([Testing Guide](docs/TESTING.md))
- **Explicit transaction control** — you decide where `BEGIN` and `COMMIT` go
- **Data files alongside schema** — JSON config, XML reference data, CSV seeds in the same transaction as migrations
- **Multi-cloud PostgreSQL targets** — the same `deploy.sql` works on Azure, AWS, GCP with native auth

A poor fit when:

- **Your team avoids SQL/PL/pgSQL** — pgmi's power is writing deployment logic in PostgreSQL's language; without that fluency the advantage disappears
- **You want tool-managed migration history** — the binary keeps no version table; each template's `deploy.sql` tracks what ran, as code you own and can change, not as a built-in
- **Your connection path is a transaction-mode pooler** — session temp tables can't survive it

[Tradeoffs](docs/TRADEOFFS.md) is the full honest list.

## Documentation

📖 Browse the full docs as a searchable site at **<https://vvka-141.github.io/pgmi/>** (built from this `docs/` directory). The links below open the same pages on GitHub.

**Start here**

| Guide | Description |
|-------|-------------|
| [Getting Started](docs/QUICKSTART.md) | Your first deployment in 10 minutes (binary-first) |
| [deploy.sql Guide](docs/DEPLOY-GUIDE.md) | Authoring patterns: data ingestion, environment branching, multi-phase |
| [Testing](docs/TESTING.md) | Database tests with savepoint isolation and deploy gates |

**Why pgmi exists** (deep dives)

| Essay | Description |
|-------|-------------|
| [Why pgmi?](docs/WHY-PGMI.md) | When pgmi's approach makes sense |
| [Highlights](docs/HIGHLIGHTS.md) | Ten distinctive pgmi capabilities, grounded in code and guides |
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
| [Advanced template](docs/advanced/_index.md) | An application layer you own: one handler registry → REST+RPC+MCP+OpenAPI, per-route transaction policy, sessions and RLS, audit trails, reference gateways in five languages |
| [Advanced-template MCP gateway](docs/advanced/MCP.md) | Expose your deployed application to AI assistants |

## AI assistant support

`pgmi init` writes an `AGENTS.md` that routes agents to project skills under `.agents/skills/`, and a `CLAUDE.md` that imports it. The skills include PostgreSQL recipes whose SQL runs in pgmi's CI. They are the project's files: commit them and edit them. Any agent that reads `AGENTS.md` can use them — Claude Code, Codex, or a smaller model.

The binary carries the facts that must match its version, so agents read them on demand instead of guessing:

```bash
pgmi ai                    # Overview for AI assistants (llms.txt style)
pgmi ai contract           # Print the session API contract (views/functions)
```

The v0.14 evaluation quoted at the top gave Claude Code and Codex the same fixed tasks — add an entity with a REST endpoint and tests, audit every change to a table — in a fresh project, and checked each result against a defect checklist; most defects before v0.14 were functions left executable by every role. Method and tasks: [AI guidance in the scaffold](docs/design/ai-guidance-in-the-scaffold.md#how-success-is-measured).

`pgmi serve` additionally exposes pgmi commands as MCP tools over stdio — see the [CLI reference](docs/CLI.md#pgmi-serve). (The advanced template's [MCP gateway](docs/advanced/MCP.md) is a separate surface: it exposes your *deployed application* to AI assistants.)

## Configuration and authentication

Store connection defaults and parameters in `pgmi.yaml` next to `deploy.sql` for zero-flag deployments (`pgmi deploy .`) — see [Configuration](docs/CONFIGURATION.md).

Beyond standard PostgreSQL auth (connection strings, `PGPASSWORD`, `.pgpass`), pgmi authenticates natively to **Azure Database for PostgreSQL** (Entra ID), **Amazon RDS** (IAM), and **Google Cloud SQL** (IAM) — passwordless, using each cloud's credential chain. Commands and setup: [Connections](docs/CONNECTIONS.md).

## Contributing

Contributions welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for development
guidelines, [SUPPORT.md](SUPPORT.md) for questions and issue routing, and the
[Code of Conduct](CODE_OF_CONDUCT.md) for community standards.

## License

[Mozilla Public License 2.0](LICENSE). Template code in `internal/scaffold/templates/` is [MIT licensed](internal/scaffold/templates/LICENSE)—code you generate is yours.

Copyright 2024-2026 Alexey Evlampiev
