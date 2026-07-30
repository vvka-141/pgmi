---
title: "CLI reference"
description: "Reference pgmi commands, connection flags, parameters, compatibility options, exit codes, and common errors."
weight: 50
---

# CLI Reference

Complete reference for all pgmi commands. Every example is copy-paste ready.

For a guided walkthrough, see [Quickstart](QUICKSTART.md).

---

## Global Flags

These flags work with every command:

| Flag | Description |
|------|-------------|
| `-v, --verbose` | Enable verbose output (also shows PostgreSQL `RAISE DEBUG` messages) |
| `--help` | Show help for any command (`-h` is `--host` on `deploy`, as in psql) |

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
| `--force` | Replace interactive confirmation with a 5-second countdown, cancellable with Ctrl+C. Without a terminal the countdown is skipped and one line is logged instead. |
| `--timeout` | Catastrophic failure protection (default: `3m`). Examples: `30s`, `5m`, `1h30m` |
| `--compat` | API compatibility version (default: latest). Pin to a specific version for stable CI/CD pipelines. |
| `--json` | Emit structured JSON to stdout after deployment, on success **and** on failure. |

#### `--json` envelope

JSON goes to stdout; the human-readable summary stays on stderr, so
`pgmi deploy . --json 2>/dev/null` yields clean JSON. Diagnostic keys are
omitted rather than emitted empty, and all values are password-redacted.

`exitCode` is always the code the process exits with — including Ctrl-C, which
reports `"status": "failed"` and `130` even if the deployment itself had already
returned cleanly.

| Field | Notes |
|-------|-------|
| `status` | `success` or `failed` |
| `exitCode` | See [Exit Codes](#exit-codes) |
| `filesLoaded`, `testMacros`, `durationMs`, `database` | Run summary |
| `executionUnits`, `unitsCommitted` | Present once deploy.sql execution begins. `executionUnits` is the total count; `unitsCommitted` is how many completed before the failure (equals `executionUnits` on success) |
| `executionMode` | `"atomic"` (head failure — nothing applied, rolled back) or `"psql"` (tail failure — earlier units already committed). Present only on failure. Derived from the unit ordinal at failure time, not from whether the script contains a COMMIT |
| `error` | Failure message. Note the key is `error`, not `message` |
| `sqlstate` | PostgreSQL error code |
| `detail`, `hint`, `where` | PostgreSQL diagnostics, when the server supplied them |
| `failedFile` | Project file that raised the error |
| `script`, `sourceLine` | The script pgmi executed, and the offending line from it |
| `line`, `column`, `scriptExpanded` | Present only when PostgreSQL reported a position (syntax errors always do). `scriptExpanded: true` means `line` refers to the macro-expanded script, not the file on disk |

```json
{
  "status": "failed",
  "exitCode": 13,
  "database": "postgres",
  "durationMs": 1165,
  "filesLoaded": 0,
  "testMacros": 0,
  "error": "execution failed: ERROR: function this_function_does_not_exist() does not exist (SQLSTATE 42883)",
  "sqlstate": "42883",
  "hint": "No function matches the given name and argument types. You might need to add explicit type casts.",
  "script": "deploy.sql",
  "sourceLine": "SELECT this_function_does_not_exist();",
  "line": 1,
  "column": 8,
  "scriptExpanded": false
}
```

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

An unsupported version is rejected as configuration — exit 10, before pgmi
connects to anything:

```bash
$ pgmi deploy . -d myapp --compat=99
FAILED myapp: failed after 0.00s
pgmi: error: invalid configuration: unsupported API version "99"; supported: [1]
```

**Best practice:** Pin `--compat` in CI/CD pipelines for stability. When upgrading pgmi, test with the new default version before updating your pinned version.

#### API Version Changelog

**Version 1** (Current)
- Initial stable API release
- Views: `pgmi_source_view`, `pgmi_plan_view`, `pgmi_parameter_view`, `pgmi_test_source_view`, `pgmi_test_directory_view`, `pgmi_source_metadata_view`
- Functions: `pgmi_test_plan()`, `pgmi_test_generate()`, `pgmi_is_sql_file()`, `pgmi_persist_test_plan()`
- Preprocessor macro: `CALL pgmi_test()`

See [Session API](session-api.md) for complete API documentation.

#### Understanding `--overwrite` Safety

The `--overwrite` flag triggers a **destructive operation**: the target database is dropped and recreated. pgmi provides safety mechanisms to prevent accidents:

**Without `--force` (interactive mode):**
```

WARNING: about to DROP and RECREATE database "myapp". This deletes all data.
Type the database name to confirm:
```
You must type the exact database name. Typos cancel the operation.

**With `--force` (countdown mode):**
```
╔═══════════════════════════════════════════════════════════════════════╗
║                                                                       ║
      ______
   .-'      '-.
  /            \
 |              |
 |,  .-.  .-.  ,|       ⚠️  DANGER: DESTRUCTIVE OPERATION ⚠️
 | )(_o/  \o_)( |  Database 'myapp' will be PERMANENTLY DELETED
 |/     /\     \|               ALL DATA WILL BE LOST
 (_     ^^     _)
  \__|IIIIII|__/
   | \IIIIII/ |
   \          /
    `--------`
║                                                                       ║
╚═══════════════════════════════════════════════════════════════════════╝

--force: DROP DATABASE "myapp" in 5s. Ctrl-C aborts.
```
The countdown line rewrites in place each second.

**Without a terminal**, both go away. The banner needs a TTY and `PGMI_NO_BANNER`
unset; the countdown needs a human who could press Ctrl-C. In CI and piped logs
`--force` prints one line and proceeds:

```
--force: dropping and recreating "myapp"
```

`--overwrite` **without** `--force` does not prompt there either — it refuses:

```
pgmi: error: approval denied: --overwrite must be confirmed at a terminal, and
this is not one; pass --force to drop and recreate "myapp" from a script or CI job
```

Exit code 12, immediately. It used to block on the prompt until `--timeout`
expired and then report exit 16, which reads as a slow database rather than a
missing flag.

**When to use `--overwrite`:**
- Local development with disposable databases
- CI/CD pipelines deploying to **ephemeral test databases** (not production!)
- Never on production or staging databases with real data

### Parameter Flags

| Flag | Description |
|------|-------------|
| `--param key=value` | Set a parameter (repeatable). Accessible in SQL via `current_setting('pgmi.key')`. Keys are lower-cased: `--param apiVersion=2` becomes `pgmi.apiversion`, and `pgmi_parameter_view.key` is `apiversion` |
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

## pgmi info

Show a project structure summary without connecting to a database.

```bash
pgmi info [path] [flags]
```

Inspects a pgmi project directory and reports file counts by directory, template type, deploy.sql presence, test coverage, and metadata usage.

| Flag | Description |
|------|-------------|
| `--json` | Emit structured JSON to stdout |

### Examples

```bash
# Inspect current directory
pgmi info

# Inspect a specific project
pgmi info ./myproject

# JSON output for scripting
pgmi info ./myproject --json
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
| `basic` | Low-ceremony, production-capable for small systems. Linear `migrations/` with `deploy.sql`. Runs on any provider. |
| `advanced` | Full reference app. 4-schema architecture, role hierarchy, metadata-driven deployment. PostgreSQL 15+; needs a role with `CREATEROLE` + `CREATE EXTENSION` (no superuser). Runs on managed cloud — see the [Production Guide](PRODUCTION.md#managed-cloud-postgresql). |

### Examples

```bash
# Create a project in the current directory
pgmi init .

# Create a named project with the basic template
pgmi init myapp

# Full reference application (more infrastructure, not a higher safety tier)
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
| `--write` | | Write metadata to files (without this flag, preview only) |
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

## pgmi serve

Exposes pgmi's commands as MCP tools over stdio (JSON-RPC 2.0), so MCP-capable
assistants (Claude Code, OpenCode) can drive pgmi natively instead of spawning a
subprocess and parsing text.

```bash
pgmi serve
```

The tools map 1:1 to existing CLI commands — `serve` adds no deployment
semantics. Connection and parameters are passed per tool call and never stored
in server state. Tools exposed: `deploy`, `init`, `metadata_plan`,
`metadata_validate`, `templates_list`, `ai_overview`, `ai_skills`, `ai_skill`,
`ai_contract`.

![pgmi serve: an MCP client speaks JSON-RPC 2.0 over stdio to the nine CLI tools, which read project files and deploy to PostgreSQL](diagrams/d09-mcp-serve.drawio.svg)

Register it with Claude Code:

```bash
claude mcp add pgmi -- pgmi serve
```

The server reads JSON-RPC from stdin, writes responses to stdout, and sends all
diagnostics to stderr. It exits cleanly on EOF or SIGINT.

**Failure handling.** A tool that fails — including one that panics — answers
with `isError: true` and the session continues; a panic also carries its stack in
`structuredContent`. A *malformed* message is different: the server replies with
a `-32700` parse error and `"id": null`, then ends the session, because the
JSON stream cannot be resynchronised after a syntax error. A client that sends a
truncated frame must restart the server.

**Protocol version.** `initialize` negotiates: the server echoes the client's
requested version when it speaks it (`2025-06-18`, `2025-03-26`, `2024-11-05`),
otherwise answers with its newest. An unsupported request is not an error — the
client decides whether the answer is acceptable.

**Structured output.** Tools returning a structured value (`deploy`, `init`,
`metadata_plan`, `metadata_validate`, `templates_list`, `ai_skills`) declare an
`outputSchema` in `tools/list` and emit matching `structuredContent`, so a client
can validate results without parsing text. The three tools that return
documents — `ai_overview`, `ai_skill`, `ai_contract` — declare no output schema
because they return text, not typed output.

**Destructive deploys need an echo-back.** `deploy` with `overwrite: true` drops
the target database, and this path auto-approves (there is no TTY to prompt). It
therefore also requires `confirmDatabaseName` to equal `database` exactly:

```json
{"name": "deploy", "arguments": {
  "path": "./myproject", "connection": "postgresql://...",
  "database": "myapp", "overwrite": true, "confirmDatabaseName": "myapp"}}
```

A missing or mismatched name is refused before anything connects, so an agent
that hallucinated a database name cannot drop it.

> This is pgmi's **own CLI** as MCP tools. It is unrelated to the advanced
> template's MCP gateway (which exposes your *deployed database* to agents — see
> [docs/advanced/MCP.md](advanced/MCP.md)).

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

Lists all embedded skills with their scope and description. `scope` tells an
agent whether a skill applies to any pgmi project (`core`), only to the
scaffolded advanced template (`advanced-template`), or only to work on pgmi
itself (`contributor`):

```
# Available pgmi Skills

Use `pgmi ai skill <name>` to get full skill content.

| Skill | Scope | Description |
|-------|-------|-------------|
| `advanced-template` | advanced-template | Advanced template framework API reference (lib/README.md) |
| `pgmi-api-architecture` | advanced-template | Advanced template: REST/RPC/MCP protocol design and HTTP architecture |
| `pgmi-debug-deploy` | core | Use when a pgmi deploy fails — map the exit code to what to inspect and how to fix it |
...

Total: 16 skills
```

### pgmi ai skill

```bash
pgmi ai skill <name>
```

Outputs the full content of a specific skill. Use this to load detailed conventions for a particular domain:

```bash
# Load SQL conventions
pgmi ai skill pgmi-sql

# Load architectural rationale
pgmi ai skill pgmi-philosophy

# Load testing patterns
pgmi ai skill pgmi-testing-review
```

### pgmi ai contract

```bash
pgmi ai contract
```

Prints the machine-readable session-API contract as JSON. Agents should query this before writing SQL against pgmi views/functions to avoid hallucinating identifiers. Output includes view names and columns, test function signatures, step types, exit codes, and preprocessor macro forms.

### pgmi ai client

```bash
pgmi ai client [lang]
```

Prints guidance for generating a typed API client from a deployment's live
OpenAPI spec (the advanced template serves it at `GET /openapi.json`). Without a
language, prints the language-agnostic doctrine (decision tree, invariants,
anti-copy directive). With a language, adds a transport-core skeleton and the
recommended generator:

```bash
pgmi ai client              # doctrine only
pgmi ai client typescript   # + openapi-typescript
pgmi ai client python       # + openapi-python-client
pgmi ai client go           # + oapi-codegen
pgmi ai client csharp       # + NSwag
pgmi ai client rust         # + openapi-generator
```

This covers the **application API** (your deployed handlers). For the **session
API** (the temp views/functions `deploy.sql` consumes), use `pgmi ai contract`.

### pgmi ai setup

```bash
pgmi ai setup [--assistant <name> | --all] [--global] [--dry-run] [--force]
              [--claude-md | --no-claude-md]
```

Materializes pgmi guidance into a coding assistant's skill directory so the
assistant learns the execution model before it edits the project. Defaults to
the Claude skill under `.claude/skills/pgmi/` (project-local, safe to commit).

| Assistant | Local target |
|-----------|-------------|
| `claude` (default) | `.claude/skills/pgmi/` |
| `agents` (aliases: `codex`, `opencode`) | `AGENTS.md` |
| `codex-skills` | `.codex/skills/pgmi/` |
| `antigravity` | `.agents/skills/pgmi/` |
| `cursor` | `.cursor/rules/pgmi.mdc` |
| `copilot` | `.github/copilot-instructions.md` |
| `windsurf` | `.windsurf/rules/pgmi.md` |
| `cline` | `.clinerules/pgmi.md` |
| `gemini` | `GEMINI.md` |

`--global` writes under your home directory instead of the project (e.g.
`~/.claude/skills/pgmi/`, `~/.codex/skills/pgmi/`, `~/.gemini/GEMINI.md`).

```bash
pgmi ai setup                        # detect .claude/, write the Claude skill
pgmi ai setup --assistant agents     # write AGENTS.md (Codex, opencode, etc.)
pgmi ai setup --assistant cursor     # write .cursor/rules/pgmi.mdc
pgmi ai setup --all                  # write one file per distinct target
pgmi ai setup --global               # write to ~/.claude/skills/pgmi/ instead
pgmi ai setup --dry-run              # print planned changes, write nothing
```

The generated skill is self-contained — it teaches the core model even with no
pgmi binary installed — and points to `pgmi ai skill <name>` for depth. Files
are stamped; re-running is idempotent and a hand-edited file is not overwritten
without `--force`. `setup` also offers a one-line managed pointer in the project
`CLAUDE.md` (`--claude-md` / `--no-claude-md` to decide non-interactively).

In non-interactive contexts (CI), pass `--assistant` explicitly.

### pgmi ai check

```bash
pgmi ai check [--assistant claude] [--global]
```

Reports whether the guidance exists and whether it matches this binary's
version. Exits non-zero when guidance is missing, stale, or hand-edited, so it
can gate CI:

```bash
pgmi ai check || pgmi ai setup --assistant claude
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
| `PGAPPNAME` | `deploy` | `application_name` reported in `pg_stat_activity` (default: `pgmi`) |
| `PGCONNECT_TIMEOUT` | `deploy` | Connection timeout in seconds (libpq convention) |
| `PGPASSFILE` | `deploy` | Path to `.pgpass` (default: `~/.pgpass` or `%APPDATA%\postgresql\pgpass.conf`) |
| `PGMI_NON_INTERACTIVE` | any | Set to `1` to disable TUI wizards |
| `CI` | any | Any non-empty value disables TUI wizards |
| `NO_COLOR` | any | Disables ANSI colors (wizards still run; accessibility signal per https://no-color.org) |
| `PGMI_NO_BANNER` | any | Any non-empty value suppresses the identity splash and the `--force` danger banner |
| `PGMI_PAGER` | any | Pager for long output; checked before `PAGER` |
| `PAGER` | any | Fallback pager when `PGMI_PAGER` is unset. Paging applies on a TTY only |
| `AZURE_TENANT_ID` | `deploy` | Azure AD tenant ID |
| `AZURE_CLIENT_ID` | `deploy` | Azure AD client ID |
| `AZURE_CLIENT_SECRET` | `deploy` | Azure AD client secret |
| `AWS_REGION` | `deploy` | AWS region for RDS IAM auth |
| `AWS_DEFAULT_REGION` | `deploy` | Fallback AWS region |

pgmi uses the [`jackc/pgx`](https://github.com/jackc/pgx) driver (Go-native, no libpq dependency). All standard `PG*` environment variables are supported.

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
| `10` | Invalid pgmi configuration — rejected before connecting; nothing was deployed |
| `11` | Database connection failed |
| `12` | User denied overwrite approval |
| `13` | SQL execution failed |
| `14` | `deploy.sql` not found |
| `15` | Concurrent deploy detected |
| `16` | Operation exceeded `--timeout` (context deadline exceeded) |
| `130` | Interrupted by SIGINT (Ctrl-C) — Unix convention 128+SIGINT |

---

## Shell Completion

Generate a completion script for your shell:

```bash
# bash
pgmi completion bash > /etc/bash_completion.d/pgmi

# zsh
pgmi completion zsh > "${fpath[1]}/_pgmi"

# fish
pgmi completion fish > ~/.config/fish/completions/pgmi.fish

# PowerShell
pgmi completion powershell | Out-String | Invoke-Expression
```

Completion covers commands, flags, template names (for `init --template`), SSL mode values, AI skill names (for `ai skill`), and assistant names (for `ai setup` / `ai check`).

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
| `permission denied for schema` | Role lacks privileges | Grant the deploy role `CREATE`/`USAGE` on the schema (or `CREATEROLE`/`CREATE EXTENSION` for advanced-template setup) |
| `current transaction is aborted` | Earlier error in transaction | Fix the root cause; check `RAISE EXCEPTION` in your SQL |
| `syntax error at or near` | Invalid SQL | Check the file path in error message, fix syntax |
| `missing required parameter` (SQLSTATE `P0001`) | Your `deploy.sql` requires a parameter you did not pass | Add `--param key=value`; this is your SQL raising, not a pgmi config error |
| `unknown parameter` (SQLSTATE `P0001`) | Parameter not declared in the template's `session.xml` | Declare it there or drop it from the command line |
| `invalid regex pattern` | Bad pattern passed to `pgmi_test()` | Fix the POSIX regex |

### Configuration Errors (Exit Code 10)

Exit 10 means **pgmi rejected the invocation before running any of your SQL**.
Every one of these is decided offline, so nothing was deployed and no database
was created, dropped, or modified.

| Error | Cause | Solution |
|-------|-------|----------|
| `database name is required` | No `-d`, no database in the connection string, no `PGDATABASE` | Add `-d mydb` |
| `invalid parameter format` | `--param` without `=` | Use `--param key=value` |
| `failed to load pgmi.yaml` | Unparseable `pgmi.yaml` | Fix the YAML syntax |
| `unsupported API version` | Invalid `--compat` value | Use `--compat=1` (currently only v1 supported) |
| `failed to read/parse params file` | `--params-file` missing or malformed | Check the path and the `KEY=VALUE` format |
| `cannot use multiple cloud authentication methods` | More than one of `--azure`/`--aws`/`--google` | Choose one |

> **Parameter errors raised by your SQL are exit 13, not exit 10.** A
> `missing required parameter` or `unknown parameter` message from the advanced
> template is a `RAISE EXCEPTION` inside `deploy.sql` — it reaches you as a SQL
> execution failure with SQLSTATE `P0001`, because by then pgmi has connected
> and handed control to your SQL. The same applies to an `invalid regex pattern`
> from `pgmi_test()`. Diagnose those with the exit-13 table above; the fix is
> usually still to add the missing `--param`.

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

Precedence for `sslcert`, `sslkey`, `sslrootcert` and `sslpassword`:

```
--sslcert etc. → sslcert= in the connection string → PGSSLCERT → pgmi.yaml
```

The connection string beats the environment, as in libpq: `PGSSLROOTCERT` is a
*default* for `sslrootcert`, not an override. `sslpassword` has no flag and no
`pgmi.yaml` field — a private-key passphrase belongs in neither argv nor a
committed file — so it comes from the connection string or `PGSSLPASSWORD`.

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
