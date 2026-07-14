# pgmi - AI Assistant Guide

> PostgreSQL-native execution fabric for database deployments. Minimal interference, maximum empowerment.

## What is pgmi?

pgmi loads SQL files and parameters into PostgreSQL session-scoped temporary tables, then executes a user-provided `deploy.sql` that orchestrates deployment using PostgreSQL's procedural languages.

**Key insight:** pgmi is an execution fabric, NOT a migration framework. Transaction control, execution order, retry logic, idempotency - all belong in your SQL, not CLI flags.

## Quick Start for AI Assistants

```bash
# Initialize a project
pgmi init myproject --template basic

# Deploy to database. Tests are not a separate command: deploy.sql calls
# CALL pgmi_test(), so they run inside the same transaction and a failing
# test rolls the whole deployment back.
pgmi deploy ./myproject --connection "postgresql://user:pass@host/db"
```

There is no `--test`, `--skip-tests`, or `--dry-run` flag, and no parameter pgmi
interprets to control any of them. What runs is whatever `deploy.sql` executes.

## Core Concepts

### Session-Scoped Tables

pgmi creates temporary tables in `pg_temp` schema:

| View | Purpose |
|------|---------|
| `pgmi_source_view` | All SQL files with path, content, metadata (excludes deploy.sql and `__test__/`) |
| `pgmi_plan_view` | Execution order derived from `<pgmi-meta>` sortKeys |
| `pgmi_parameter_view` | CLI parameters (`--param key=value`) |
| `pgmi_test_source_view` | Test files from `__test__/` directories |
| `pgmi_test_directory_view` | Test directory hierarchy |
| `pgmi_source_metadata_view` | Parsed `<pgmi-meta>` blocks |

All names end in `_view` — they are the stable public API. Do not query the
`_pgmi_*` internal tables directly; they are implementation details.

### deploy.sql Pattern

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

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

## Available Skills

Use `pgmi ai skill <name>` to get detailed guidance:

| Skill | Use When |
|-------|----------|
| `pgmi-sql` | Writing SQL/PL/pgSQL or deploy.sql |
| `pgmi-debug-deploy` | A deploy failed — map the exit code to a diagnosis |
| `pgmi-philosophy` | Understanding architectural decisions |
| `pgmi-system-design` | Designing features the pgmi way (physical/logical/API layering) |
| `pgmi-templates` | Creating or modifying scaffold templates |
| `pgmi-testing-review` | Writing, organizing, or debugging tests |
| `pgmi-postgres-review` | Writing SQL with correctness and performance guidance |
| `pgmi-metadata-system` | Working with `<pgmi-meta>` blocks, sortKeys, execution ordering |
| `pgmi-test-architecture` | Organizing `__test__/` directories and test strategy |
| `postgresql-patterns` | EXECUTE, format(), composite types, dynamic SQL |
| `pgmi-api-architecture` | REST/RPC/MCP protocol design (advanced template) |
| `pgmi-handler-patterns` | Writing REST/RPC handler bodies — the four-phase defensive doctrine (advanced template) |
| `pgmi-endpoint-quickstart` | End-to-end recipe: add an entity + REST endpoint + test (advanced template) |
| `pgmi-mcp` | MCP handler implementation (advanced template) |

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
  _setup.sql           # REQUIRED name for fixtures
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
  --connection STRING    PostgreSQL connection string (URI or ADO.NET)
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
  --params-file PATH     Load from .env file (repeatable, later wins)

Workflow:
  --overwrite            Drop and recreate the database
  --force                Non-interactive 5s countdown (CI/CD)
  --timeout DURATION     Catastrophic failure timeout (default 3m)
  --compat VERSION       Pin session interface version
  --json                 Emit structured JSON to stdout after deployment
```

### pgmi init \[path\]

```
  -t, --template NAME    basic (default) or advanced
```

### pgmi metadata \<subcommand\> \<path\>

```
  scaffold [--write] [--idempotent=BOOL]  Generate <pgmi-meta> blocks
  validate [--json]                       Check XML validity + uniqueness
  plan [--json]                           Show execution order from sortKeys
```

### pgmi ai

```
  (no subcommand)        Overview (this document)
  skills                 List embedded skills
  skill <name>           Print skill content
  client [lang]          API client guidance (typescript, python, go, csharp, rust)
  contract               Session API contract (views, functions)
  setup [--assistant X]  Write guidance (claude, agents, --all)
  check                  Report if guidance is current
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
semantics; connection and parameters are passed per call, never stored.

```
  pgmi serve             Start the MCP server (reads JSON-RPC on stdin)
  claude mcp add pgmi -- pgmi serve     Register with Claude Code
```

Tools: `deploy`, `init`, `metadata_plan`, `metadata_validate`, `templates_list`,
`ai_overview`, `ai_skills`, `ai_skill`, `ai_contract`. Results are structured
JSON (the same data as each command's `--json` output). Diagnostics go to
stderr; the server exits cleanly on EOF or SIGINT.

### Global flags

```
  -v, --verbose          Verbose output (sets client_min_messages = 'debug')
  -h, --help             Help for any command
```

## Common Questions

**"Why no `--dry-run`?"** — deploy.sql controls transactions. Use `--param preview=true` in your SQL,
then `RAISE EXCEPTION 'preview: rolling back'` to abort. You control what "dry run" means.

**"Why no `--rollback`?"** — Rollback strategy belongs in deploy.sql. pgmi doesn't know whether you
want a full rollback, partial undo, or compensating migrations — your SQL decides.

**"Why no `pgmi test` command?"** — Tests run via `CALL pgmi_test()` inside deploy.sql.
The CLI never decides what SQL to run; your deploy.sql orchestrates everything including tests.

## Learn More

- `pgmi ai skill pgmi-sql` - Complete SQL conventions
- `pgmi ai skill pgmi-philosophy` - Architectural principles
- `pgmi ai skill pgmi-templates` - Production template guide
- `pgmi ai client [lang]` - Consuming the API from code? Client guidance for TypeScript, Python, Go, C#, Rust
