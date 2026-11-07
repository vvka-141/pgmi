# pgmi

[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](https://opensource.org/licenses/MPL-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.27+-00ADD8?logo=go)](https://go.dev/)
[![CI](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml/badge.svg)](https://github.com/vvka-141/pgmi/actions/workflows/ci.yml)
[![Watch Introduction](https://img.shields.io/badge/▶_Watch-Introduction-red?logo=youtube)](https://youtu.be/0txwCsGRyyE)

**PostgreSQL deployments written in SQL, tested before they commit.**

pgmi loads your project files into one PostgreSQL session as queryable data, then
runs the `deploy.sql` **you** write. Your deployment selects migrations, loads
reference data, tests the changed database — and commits only if everything
passes. A failing test aborts the deployment transaction.

Migration frameworks decide ordering, history, and the transaction model for you.
With pgmi those decisions are SQL in your `deploy.sql` ([Why pgmi?](docs/WHY-PGMI.md)).

- **Control.** Your files are rows in session views. `deploy.sql` queries them
  and decides what runs, in what order, and inside which transaction.
- **Confidence.** `CALL pgmi_test()` runs the tests in your `__test__/`
  directories against the changed, uncommitted database. A directory's fixture
  applies to every test below it, and each test's changes roll back to a
  savepoint. Call it before `COMMIT`, and a failing test commits nothing.
- **Agent-ready.** The basic and advanced templates write an `AGENTS.md` and
  PostgreSQL skills into the project. `pgmi ai contract` prints the session API
  of the binary you run, and `pgmi serve` lets a coding agent drive pgmi over MCP.

pgmi takes no position on your architecture. `pgmi init` writes a starting
project from one of three templates. From then on the code is yours to change
or delete:

- **minimal** (the default) — a `deploy.sql` loop and one SQL file: the mechanism, bare.
- **basic** — deploy a schema with a small project: table files that run once
  (tracked in a ledger), views and functions that re-run on every deploy, and
  tests that gate the commit. Your application calls the functions by name;
  there is no API layer in the database.
- **advanced** — build your application's backend in PostgreSQL, as code you
  own, with REST, RPC and MCP routing in the database; read [the advanced-template guide](docs/advanced/README.md) before choosing it.

[What pgmi gives you, and what you own](docs/core-vs-template.md) draws the line
between pgmi and the templates; [Choose a template](docs/templates.md) compares all three.

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
pgmi deploy demo -d demo_db --param env=dev
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

✓ demo_db: 14 files loaded, 3 paths ignored in 0.09s
```

`Loaded 1 parameter(s)` is the `--param env=dev` in the command, and `[dev]` is
the template's `deploy.sql` printing it back.

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

The tests form a tree. In the first run, `./semantic/__test__/_setup.sql` is a
fixture. It runs once before `test_project.sql` and `test_task.sql`, and each
test rolls back to the fixture's state, so neither sees the other's changes.
Subdirectories of `__test__/` add their own fixtures on top of their parent's,
so one setup can branch into several scenarios. The whole tree rolls back
before the deployment commits
([hierarchical fixtures](docs/TESTING.md#hierarchical-fixtures);
[Scenario-Tree Testing in PostgreSQL](https://vvka-141.github.io/pgmi/articles/test-every-branch-before-commit/)).

> **Status:** pgmi is pre-1.0. The session API may change in a minor release. Every change is listed under "Upgrading" in the release notes, and a snapshot test makes each change deliberate. Pin the pgmi version in CI.

> **Requirements:** a deploy needs PostgreSQL 15+ (the advanced template 18+ — [compatibility matrix](docs/PRODUCTION.md#postgresql-compatibility)) and a **direct** connection or session-mode pooler. Transaction-mode poolers (PgBouncer txn mode, RDS Proxy) reassign connections between statements and destroy the session temp tables a deploy depends on — [details](docs/PRODUCTION.md#connection-requirements). The advanced template sets each request's context per transaction, so this limit applies to the deploy alone.

## The entire model in one file

`deploy.sql` is plain PostgreSQL. Your files are rows in a session view; you query them and decide what to execute. pgmi rewrites one thing before sending it: `CALL pgmi_test()` becomes the test calls, each in its own savepoint ([Testing](docs/TESTING.md)):

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

On connect, pgmi creates session-scoped temporary tables and views holding your
files and parameters, sets `pgmi.<name>` for each parameter, and takes an
advisory lock, so a second deploy of the same database is refused.

This minimal version runs every file on every deploy. pgmi keeps no history of its
own: what has run, and what may run again, is your SQL's decision too. The basic
template's `deploy.sql` records applied run-once files in a ledger table and skips
them on later deploys. If an applied file has changed, it refuses the deploy and
names the file.

Filter by directory, branch on a `--param`, skip files whose checksum already ran, load JSON/XML/CSV reference data with PostgreSQL's built-in functions — it's your SQL. See the [deploy.sql Guide](docs/DEPLOY-GUIDE.md) for patterns and the [Session API](docs/session-api.md) for the views (`pgmi_source_view` for raw path-ordered access, `pgmi_plan_view` for metadata-driven ordering).

**Why it feels different:**

- **Your SQL owns the deploy** — transactions, execution order and idempotency live in `deploy.sql`, not in the tool. pgmi retries connection attempts; your pipeline decides whether to retry a failed deploy.
- **The CLI is infrastructure-only** — connections, parameters, auth. No `--dry-run`, no `--rollback`, no orchestration flags to learn.
- **One transaction, then psql** — everything through the first top-level `COMMIT` runs in one transaction; without one, the whole file is atomic. The templates commit after the test gate and before the final banner, so a statement appended after that `COMMIT` autocommits on its own (as `CREATE INDEX CONCURRENTLY` needs) unless you group it in a `BEGIN ... COMMIT` of its own ([the execution contract](docs/DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract)).

## Why it works this way

- **Test the migrated target before commit.** The gate runs against the real target, its own data migrated, before the deployment commits; it sits after lint and rehearsal, not in their place ([Test PostgreSQL migrations before COMMIT](https://vvka-141.github.io/pgmi/articles/test-postgresql-migrations-before-commit/)).
- **Check API responses and database changes together.** In the advanced template, a deploy test calls a route, checks its response and the row it changed, then rolls back ([The Request Becomes a Transaction](https://vvka-141.github.io/pgmi/articles/request-becomes-a-transaction/)).
- **Bound lock waits and know what committed.** The example sets `lock_timeout` in the transactional head, reports which units committed before a failure, and re-runs to the same end state ([Your ALTER TABLE Is Fast. The Queue Behind It Is Not.](https://vvka-141.github.io/pgmi/articles/lock-queue-fast-is-not-safe/); [`examples/lock-safe-deploy/`](examples/lock-safe-deploy/)).

More arguments, from duplicate migration numbers to reference data: [the articles](docs/README.md#articles) and [Highlights](docs/HIGHLIGHTS.md).

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

For CI and production, use a package manager or verify the downloaded release's build provenance:

**Homebrew (macOS):**
```bash
brew install --cask vvka-141/pgmi/pgmi
```

**Direct download:** grab an archive or a Debian/Ubuntu `.deb` package from [GitHub Releases](https://github.com/vvka-141/pgmi/releases) and verify its build provenance with `gh attestation verify <downloaded-file> -R vvka-141/pgmi` (`checksums.txt` is not signed). Install a `.deb` with `sudo apt install ./pgmi_<version>_linux_amd64.deb`.

**From source** (requires Go 1.27+):
```bash
go install github.com/vvka-141/pgmi/cmd/pgmi@latest
```

Then follow the [Getting Started Guide](docs/QUICKSTART.md) for a complete walkthrough, or the [CI/CD Guide](docs/CICD.md) to deploy from a pipeline.

## Choose your path

- **Starting from nothing?** → `pgmi init myapp` writes a `deploy.sql` loop, `hello_world.sql` and a `.pgmiignore`. Build the rest your own way.
- **Just deploying SQL?** → the **basic** template: a small two-layer project, run-once tables and re-run views and functions. `pgmi init myapp --template basic`
- **Building your backend in PostgreSQL?** → read [the advanced-template guide](docs/advanced/README.md) to decide whether its runtime and maintenance model fit your application. Requires PostgreSQL 18.
- **Evaluating the approach first?** → read [Why pgmi?](docs/WHY-PGMI.md) for the approach, [Highlights](docs/HIGHLIGHTS.md) for capabilities, and [Tradeoffs](docs/TRADEOFFS.md) for limits.

Either template can be adapted for production. Choose advanced when the application needs its API, sign-in and tenancy infrastructure. [Choose a template](docs/templates.md) compares the three side by side; `pgmi templates list` shows what's available.

## When pgmi makes sense — and when it doesn't

A good fit when you need:

- **Conditional deployment logic** — different behavior per environment, feature flags, custom phases
- **Test-gated deployments** — database tests inside the deployment transaction, rollback on failure ([Testing Guide](docs/TESTING.md))
- **Explicit transaction control** — you decide where `BEGIN` and `COMMIT` go
- **Data files alongside schema** — JSON config, XML reference data, CSV seeds in the same transaction as migrations
- **Multi-cloud PostgreSQL targets** — the same `deploy.sql` works on Azure, AWS, GCP with native auth (Azure tested by hand against a real cloud, AWS and GCP not; see [connections](docs/CONNECTIONS.md))

A poor fit when:

- **Nobody on your team can review SQL/PL/pgSQL** — the deployment logic is PostgreSQL code; a coding agent can write it, but someone has to review and maintain it
- **You want tool-managed migration history** — the binary keeps no version table; each template's `deploy.sql` tracks what ran, as code you own and can change, not as a built-in
- **Your only deploy path is a transaction-mode pooler** — the deploy's session temp tables can't survive it

[Tradeoffs](docs/TRADEOFFS.md) lists the limits; [Highlights](docs/HIGHLIGHTS.md) the strengths.

## Documentation

📖 Browse the full docs as a searchable site at **<https://vvka-141.github.io/pgmi/>** (built from this `docs/` directory). The links below open the same pages on GitHub.

**Start here**

| Guide | Description |
|-------|-------------|
| [Getting Started](docs/QUICKSTART.md) | Your first deployment in 10 minutes (binary-first) |
| [deploy.sql Guide](docs/DEPLOY-GUIDE.md) | Authoring patterns: data ingestion, environment branching, multi-phase |
| [Testing](docs/TESTING.md) | Database tests with savepoint isolation and deploy gates |
| [What pgmi gives you, and what you own](docs/core-vs-template.md) | The line between pgmi core, the templates, agent guidance and application features |

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
| [Go library](docs/GO-LIBRARY.md) | Run a deploy from your own Go program |
| [Production Guide](docs/PRODUCTION.md) | Performance, rollback, monitoring, [compatibility matrix](docs/PRODUCTION.md#postgresql-compatibility) |
| [Advanced template](docs/advanced/README.md) | An application layer you own: one handler registry → REST+RPC+MCP+OpenAPI, per-route transaction policy, sessions and RLS, audit trails, reference gateways in five languages |
| [Advanced-template MCP gateway](docs/advanced/MCP.md) | Expose your deployed application to AI assistants |

## AI assistant support

Agents meet pgmi in two different roles.

**An agent that develops your database** (Claude Code, Codex, or a smaller model):

- `pgmi init --template basic` or `--template advanced` writes an `AGENTS.md`
  that routes agents to project skills under `.agents/skills/`, and a
  `CLAUDE.md` that imports it. The skills cover the pgmi workflow (deploy,
  test, debug a failed deploy, metadata), two-layer schema design, and a
  PostgreSQL recipe library (indexing, integrity, concurrency, security, time,
  analytics, change capture) whose SQL deploys in pgmi's CI. The advanced
  template adds skills for its API layer, endpoints, handlers and MCP tools.
  They are the project's files: commit them and edit them. The minimal template
  writes none.
- The binary carries the facts that must match its version, so agents read them on demand instead of guessing:

  ```bash
  pgmi ai                    # Overview for AI assistants (llms.txt style)
  pgmi ai contract           # Print the session API contract (views/functions)
  ```

- `pgmi serve` exposes pgmi's commands, deploy included, as MCP tools over
  stdio; a deploy that would drop the database must repeat its name
  ([CLI reference](docs/CLI.md#pgmi-serve)).

How the guidance is evaluated: [AI guidance in the scaffold](docs/design/ai-guidance-in-the-scaffold.md#how-success-is-measured).

**An agent that uses your application** at runtime is a separate case, and only
the advanced template serves it. Its [MCP gateway](docs/advanced/MCP.md)
exposes your deployed application's tools to AI assistants. Each agent signs
in as its own principal with a role its person chose. Each tool call is one
transaction under row-level security, and your SQL decides whether the change
commits. `pgmi serve` plays no part in this.

## Running unattended

What pgmi guarantees in CI, from a scheduler or under an agent; deploy.sql's transaction boundaries decide what a failure leaves applied:

- **Timeouts and signals cancel the running statement** on the server and roll back the open transaction: `--timeout` (default 3m) exits 16, SIGINT/SIGTERM 130.
- **Deploys never interleave.** A second deploy against the same database exits 15 at once.
- **Configuration checks run before connecting.** Path, parameter syntax, `--compat`, `pgmi.yaml` and an empty `PGMI_PARAM_*` secret exit 10, and nothing is created or changed. Required parameters (the templates' `env`) are checked by deploy.sql once connected.
- **Connecting is retried, deploying is not.** Up to four connection attempts with backoff; re-running a failed deploy is the pipeline's job.
- **`--json` reports each run:** the failing file, line, SQLSTATE and what committed, with passwords redacted.
- **Cloud authentication:** Azure and AWS tokens refresh for long deploys and never travel without TLS.
- **`--overwrite`** restores the database's settings and privileges, or refuses before dropping anything.

Also least-privilege roles and agent deploys through `pgmi serve`: [Running unattended](docs/PRODUCTION.md#running-unattended).

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
