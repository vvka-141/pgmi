# pgmi - AI Assistant Guide

> PostgreSQL-native deployment driver. You write the deployment in SQL and PL/pgSQL; pgmi handles connection, parameters, and exit codes.

## What is pgmi?

pgmi loads SQL files and parameters into PostgreSQL session-scoped temporary tables, then executes a user-provided `deploy.sql` that orchestrates deployment using PostgreSQL's procedural languages.

**Key insight:** pgmi is an execution fabric, NOT a migration framework. Transaction control, execution order, retry logic, idempotency - all belong in your SQL, not CLI flags.

## Quick Start for AI Assistants

```bash
# Initialize a project
pgmi init myproject --template basic

# Deploy to database. Tests are not a separate command: deploy.sql calls
# CALL pgmi_test(), so they run inside the same transaction and a failing
# test rolls the whole deployment back. pgmi reads the connection string from
# PGMI_CONNECTION_STRING or DATABASE_URL, which keeps the password out of argv.
export PGMI_CONNECTION_STRING="postgresql://user:pass@host/db"
pgmi deploy ./myproject
```

There is no `--test`, `--skip-tests`, or `--dry-run` flag, and no parameter pgmi
interprets to control any of them. What runs is whatever `deploy.sql` executes.

## Core Concepts

### Session-Scoped Tables

pgmi creates temporary tables in `pg_temp` schema:

| View | Purpose |
|------|---------|
| `pgmi_source_view` | All project files with path, content, metadata, `is_sql_file` (excludes deploy.sql and `__test__/`) |
| `pgmi_plan_view` | All project files in execution order, derived from `<pgmi-meta>` sortKeys |
| `pgmi_parameter_view` | CLI parameters (`--param key=value`) |
| `pgmi_runtime_view` | One row: `pgmi_version`, `contract_version` of the running binary |
| `pgmi_test_source_view` | Test files from `__test__/` directories |
| `pgmi_test_directory_view` | Test directory hierarchy |
| `pgmi_source_metadata_view` | Parsed `<pgmi-meta>` blocks |

All names end in `_view` — they are the public API. Do not query the
`_pgmi_*` internal tables directly; they are implementation details. Before
pgmi 1.0 the public API may change in a minor release; every change is listed
under Upgrading in that release's notes. `pgmi ai contract` prints the exact
columns of the binary you run.

### What Gets Loaded

Every file under the project path enters the session, not just SQL. Five things
never arrive:

| Never loaded | Note |
|---|---|
| The **root** `deploy.sql` | pgmi executes it. A nested `./nested/deploy.sql` **does** load, with `is_sql_file` true — a plan loop will happily execute it |
| `__test__/`, `__tests__/` | Go to `pgmi_test_source_view`, not `pgmi_source_view` |
| Any path segment starting with `.` | `.git`, `.venv`, `.claude`, `.env`. SQL you park in a dot-directory is invisible |
| `node_modules/`, `__pycache__/` | Exact names only — pgmi's own `__test__` is not affected |
| Paths matched by `.pgmiignore` | Root file, `.gitignore` syntax; `pgmi deploy --verbose` lists what each rule ignored |

Everything else is read as text. A binary file (one with a NUL byte) or one
over the size limit (10 MiB; `PGMI_MAX_FILE_SIZE` overrides it) fails the
deploy with exit 10 before pgmi connects. A Python `venv/` or a build output
folder inside the project is the usual cause (a venv's symlinks fail the deploy
too): add it to `.pgmiignore`, or name the venv `.venv`.

`is_sql_file` is true only for `.sql .ddl .dml .dql .dcl .psql .pgsql
.plpgsql` — `001.sql.bak` and `notes.txt` load with `is_sql_file` false,
which is what makes the guard below matter.

### deploy.sql Pattern

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    -- pgmi_plan_view carries EVERY loaded file, not only SQL — README.md,
    -- pgmi.yaml and editor leftovers (001.sql~, .bak) are all in it.
    -- is_sql_file is the execution guard; without it a stale backup of a
    -- migration runs as a migration.
    FOR v_file IN (
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND p.path LIKE './migrations/%'
        ORDER BY p.execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

### Execution Contract: atomic head, then psql tail

**Before your first top-level `COMMIT`, atomic mode; after it, psql mode.**

- **Head** — everything through the first top-level transaction terminator
  (`COMMIT`, `END`, `ROLLBACK`, and their `WORK`/`TRANSACTION`/`AND CHAIN`
  forms, and `ABORT`) is one transaction. A deploy.sql with no top-level
  terminator is entirely atomic. The scaffolded templates are **not** that
  shape: they open with `BEGIN`, `COMMIT` after the test gate, and keep the
  DONE banner in the tail — so anything you append to one autocommits.
- **Tail** — every top-level statement after it autocommits on its own, as in
  psql. An explicit `BEGIN ... COMMIT` forms a real transaction.

```sql
BEGIN;
-- migrations + CALL pgmi_test(): all-or-nothing
COMMIT;

-- psql mode from here: this cannot run inside a transaction
CREATE INDEX CONCURRENTLY idx_users_email ON users(email);
```

Two rules follow. Statements between mid-file `COMMIT`s are **not** implicitly
grouped — a later phase that must be atomic writes its own `BEGIN ... COMMIT`.
And a mid-tail failure keeps earlier autocommitted statements applied, so write
tail statements idempotently. A concurrent index needs both guards: a failed
build leaves an `INVALID` index that `IF NOT EXISTS` skips over by name, so
drop that leftover in a `DO` block first, then
`CREATE INDEX CONCURRENTLY IF NOT EXISTS`.

`CREATE INDEX CONCURRENTLY`, `VACUUM` and `CREATE DATABASE` are refused inside
`EXECUTE` whatever the transaction state (`25001 ... cannot be executed from a
function`). Put them at top level in the tail, never in a plan loop.

### Parameters

Parameters are passed with `--param key=value` and read back with
`current_setting('pgmi.key', true)`. pgmi does not interpret them — they mean
whatever your `deploy.sql` decides they mean.

```sql
-- Access parameters with defaults
v_env := COALESCE(current_setting('pgmi.env', true), 'development');

-- Conditional logic based on parameters (this branch exists because deploy.sql
-- wrote it, not because pgmi knows what "env" is)
IF v_env <> 'production' THEN
    PERFORM seed_dev_data();
END IF;
```

## Key Differentiators

Ten distinctive capabilities are grounded in the implementation and guides.
The three most relevant to agents:

1. **Tests gate the deploy transaction** — `CALL pgmi_test()` runs inside the
   deployment transaction; a failing test aborts the commit and rolls back its
   transactional schema and data changes. Sequence advances and effects outside
   the transaction are not rolled back.
2. **Reformat-proof checksums** — `pgmi_checksum` strips comments, case-folds,
   and collapses whitespace before hashing, so reformatting a file doesn't
   trigger a checksum mismatch (the most common Flyway support-thread class).
3. **Agent-native tooling** — the binary you're already calling (`pgmi ai`,
   `pgmi ai contract`, `pgmi serve`) embeds machine-readable guidance and a
   session API contract. No docs site required.

Full list with honest competitor lines and code links:
[Highlights](https://vvka-141.github.io/pgmi/docs/highlights/)

## Project Skills

`pgmi init` writes the project's agent guidance: `AGENTS.md` and skills in
`.agents/skills/`. Both templates get `pgmi-workflow`, `pgmi-sql`,
`pgmi-testing`, `pgmi-metadata`, `pgmi-debug-deploy`, `pgmi-design`,
`pgmi-layers` and the PostgreSQL recipes `postgres-analytics`,
`postgres-change-capture`, `postgres-concurrency`, `postgres-indexing`,
`postgres-integrity`, `postgres-security` and `postgres-time`. The advanced
template adds `pgmi-api-layer`, `pgmi-endpoint`, `pgmi-handlers`, `pgmi-api`
and `pgmi-mcp`. They belong to the project: read them there, and edit them as its
conventions change. An advanced-template project also carries reference
gateways in `gateways/`.

## SQL Conventions

### Table Names: Singular

```sql
-- CORRECT: Singular table names
CREATE TABLE account (...);
CREATE TABLE "user" (...);  -- Quote reserved words

-- WRONG: Plural names
CREATE TABLE accounts (...);
```

### Test Fixtures: `_setup.sql`

```
__test__/
  _setup.sql           # the fixture name — _setup.psql also works, nothing else does
  test_something.sql   # Test files
```

### Dollar-Quoting

```sql
-- Always use dollar-quoting for embedded SQL
DO $outer$
BEGIN
    EXECUTE $sql$SELECT * FROM users$sql$;
END $outer$;
```

### JSON Keys: camelCase

```sql
-- PostgreSQL identifiers: snake_case
-- JSON keys: camelCase
jsonb_build_object(
    'httpMethod', '^GET$',
    'autoLog', true
)
```

## CLI Reference

### pgmi deploy \<path\>

Run deploy.sql against a target database.

```
Connection:
  --connection STRING    PostgreSQL connection string (URI, host=... dbname=..., or ADO.NET)
  --host STRING          Server host ($PGHOST, default: localhost)
  -p, --port INT         Server port ($PGPORT, default: 5432)
  -U, --username STRING  PostgreSQL user ($PGUSER or OS user)
  -d, --database STRING  Target database ($PGDATABASE)
  --sslmode MODE         disable|allow|prefer|require|verify-ca|verify-full

Cloud auth (no password needed):
  --azure                Azure Entra ID (DefaultAzureCredential)
  --azure-tenant-id ID   Azure AD tenant (overrides $AZURE_TENANT_ID)
  --azure-client-id ID   Azure AD app/client ID
  --aws                  AWS IAM database authentication
  --aws-region REGION    AWS region for RDS ($AWS_REGION)
  --google               Google Cloud SQL IAM authentication
  --google-instance NAME project:region:instance (required with --google)

TLS:
  --sslcert PATH         Client certificate
  --sslkey PATH          Client private key
  --sslrootcert PATH     Root CA certificate

Parameters:
  --param KEY=VALUE      Pass parameter (repeatable, available as current_setting('pgmi.key'))
  --params-file PATH     Load from .env file (repeatable, later wins; - reads stdin)
  PGMI_PARAM_<NAME>      Environment variable read as parameter <name>; use for secrets

Workflow:
  --overwrite            Drop and recreate the database
  --force                Non-interactive 5s countdown (CI/CD)
  --timeout DURATION     Catastrophic failure timeout (default 3m)
  --compat VERSION       Refuse a binary that does not ship this session API version
  --json                 Emit structured JSON to stdout after deployment
```

### pgmi init \[path\]

```
  -t, --template NAME    basic (default) or advanced
```

### pgmi metadata \<subcommand\> \<path\>

```
  scaffold [--write] [--sort-key PREFIX] [--idempotent=BOOL]  Generate <pgmi-meta> blocks
  validate [--json]                       Check XML validity + uniqueness
  plan [--json]                           Show execution order from sortKeys
```

### pgmi ai

```
  (no subcommand)        Overview (this document)
  contract               Session API contract (views, functions)
```

### pgmi info \[path\]

```
  --json                 Emit structured JSON to stdout
```

Read-only project introspection (no database required). Shows file counts,
template type, deploy.sql/pgmi.yaml presence, test coverage, metadata usage.

### pgmi templates

```
  list                   List available templates
  describe <name>        Template details and structure
```

### pgmi serve

Run pgmi as an MCP (Model Context Protocol) server over stdio. MCP-capable
assistants use pgmi's commands natively as tools instead of spawning a
subprocess and parsing text. Tools map 1:1 to CLI commands — no new deployment
semantics. `deploy` resolves like `pgmi deploy` (arguments, then environment,
then pgmi.yaml; only `path` is required); nothing is stored between calls.

```
  pgmi serve             Start the MCP server (reads JSON-RPC on stdin)
  claude mcp add pgmi -- pgmi serve     Register with Claude Code
```

Tools: `deploy`, `init`, `metadata_plan`, `metadata_validate`, `templates_list`,
`ai_overview`, `ai_contract`. Results are structured
JSON (the same data as each command's `--json` output). Diagnostics go to
stderr; the server exits cleanly on EOF or SIGINT.

### Global flags

```
  -v, --verbose          Verbose output (sets client_min_messages = 'debug')
      --help             Help for any command (-h is --host on deploy)
```

## Common Questions

**"Why no `--dry-run`?"** — deploy.sql controls transactions. Pass `--param preview=true`, and in deploy.sql
check it and `RAISE EXCEPTION 'preview: rolling back'` before the first top-level `COMMIT`. Everything up to
that `COMMIT` is one transaction and rolls back. Statements after it autocommit, and the exception cannot undo
them. The scaffolded templates `COMMIT` after the test gate. You control what "dry run" means.

**"Why no `--rollback`?"** — Rollback strategy belongs in deploy.sql. pgmi doesn't know whether you
want a full rollback, partial undo, or compensating migrations — your SQL decides.

**"Why no `pgmi test` command?"** — Tests run via `CALL pgmi_test()` inside deploy.sql.
The CLI never decides what SQL to run; your deploy.sql orchestrates everything including tests.

## Learn More

- `.agents/skills/` in a scaffolded project — SQL, testing and design conventions
- `pgmi ai contract` — the exact session API of this binary
- [Documentation](https://vvka-141.github.io/pgmi/docs/readme/)
