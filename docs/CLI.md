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
| `-v, --verbose` | Enable verbose output (also shows PostgreSQL `RAISE DEBUG` messages from deploy.sql and tests, not from pgmi's own session setup; other server DEBUG messages are not shown; a `SET client_min_messages` in deploy.sql still wins; for `deploy` lists each ignored path with the rule that excluded it) |
| `--help` | Show help for any command (`-h` is `--host` on `deploy`, as in psql) |

---

## pgmi deploy

Execute a database deployment.

```bash
pgmi deploy <project_path> [flags]
```

pgmi connects to PostgreSQL, loads your project files into session temp tables, then runs `deploy.sql` which directly executes your files.

Notices go to stderr. `NOTICE` and `INFO` lines print the bare message; a
`WARNING` prints as `WARNING:  message`, with the English label whatever
`lc_messages` is, so CI logs can grep for it. `DETAIL:` and `HINT:` follow as
in psql. `--verbose` prefixes every line with the elapsed time.

### Connection Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--connection` | `$PGMI_CONNECTION_STRING` or `$DATABASE_URL` | Full connection string: PostgreSQL URI, libpq keyword/value (`host=... dbname=...`), or ADO.NET. The flag is mutually exclusive with granular flags; the environment variables are ignored whenever `-h`, `-p`, `-U` or `--sslmode` is given. |
| `--host` | `$PGHOST` or `localhost` | PostgreSQL server host |
| `-p, --port` | `$PGPORT` or `5432` | PostgreSQL server port |
| `-U, --username` | `$PGUSER` or OS user | PostgreSQL user |
| `-d, --database` | `$PGDATABASE` or from connection string | Target database name |
| `--sslmode` | `$PGSSLMODE` or `prefer` | SSL mode: `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full`. With `--azure`/`--aws` only `require` and stronger are accepted, and the default is `verify-full` (AWS: `require` without `--sslrootcert`); see [TLS with token authentication](CONNECTIONS.md#tls-with-token-authentication) |
| `--sslcert` | `$PGSSLCERT` | Path to client SSL certificate file |
| `--sslkey` | `$PGSSLKEY` | Path to client SSL private key file |
| `--sslrootcert` | `$PGSSLROOTCERT` | Path to root CA certificate for server verification |

### Deployment Flags

| Flag | Description |
|------|-------------|
| `--overwrite` | Drop and recreate the target database before deploying. **Local development only.** |
| `--force` | Replace interactive confirmation with a 5-second countdown, cancellable with Ctrl+C. Without a terminal the countdown is skipped and one line is logged instead. |
| `--timeout` | Catastrophic failure protection (default: `3m`). Examples: `30s`, `5m`, `1h30m` |
| `--compat` | Session API version deploy.sql was written against (default: latest). Refuses a version the binary does not ship; before 1.0 it does not freeze columns, so pin the binary too. |
| `--json` | Emit structured JSON to stdout after deployment, on success **and** on failure. |

#### `--json` envelope

JSON goes to stdout; the human-readable summary stays on stderr, so
`pgmi deploy . --json 2>/dev/null` yields clean JSON. Diagnostic keys are
omitted rather than emitted empty, and all values are password-redacted.

`exitCode` is always the code the process exits with — including Ctrl-C, which
reports `"status": "failed"` and `130` even if the deployment itself had already
returned cleanly.

The human failure line says the same: `✗ myapp: failed after 0.05s — nothing
committed`, `(unit 3 of 4 failed; 2 earlier unit(s) already committed)` after a
mid-file `COMMIT`, and `created database "myapp", left empty` when this deploy
created it.

| Field | Notes |
|-------|-------|
| `status` | `success` or `failed` |
| `exitCode` | See [Exit Codes](#exit-codes) |
| `schemaVersion` | Version of this JSON shape (`1`). Raised when a key is renamed or removed, not when one is added |
| `pgmiVersion` | The binary, as `pgmi version` prints it |
| `contractVersion` | Session API version applied (`--compat`, or the latest). `null` when `--compat` names one the binary does not ship |
| `serverVersion` | PostgreSQL `server_version` of the target. `null` when the deploy failed before connecting |
| `tls` | The deploy session's transport: `{"enabled", "version", "sslmode"}`, e.g. `{"enabled": true, "version": "TLS 1.3", "sslmode": "verify-full"}`. `version` is `null` over plaintext; `sslmode` is the effective one, `prefer` by default. `null` when the deploy failed before connecting |
| `target` | `{"host", "port", "database"}` — never the user, password or token. `null` when the connection could not be resolved |
| `connectionSource` | Where the connection came from: `--connection`, `PGMI_CONNECTION_STRING` or `DATABASE_URL` for a connection string; otherwise the layer that supplied the host: `flags`, `PGHOST`, `pgmi.yaml` or `defaults`. `null` only when the run failed before the connection was resolved |
| `overrides` | Each `pgmi.yaml` connection value another source replaced, as `{field, from, value, notFrom, notValue}`, e.g. `{"field": "sslmode", "from": "PGSSLMODE", "value": "disable", "notFrom": "pgmi.yaml", "notValue": "verify-full"}`. An empty `value` means the yaml value was dropped: a connection string never takes `sslmode` from `pgmi.yaml`. Never a password. `[]` when nothing was replaced |
| `dotenvKeys` | The keys the project `.env` set, sorted, names only: those not already in the real environment. `[]` when it set none |
| `startedAt`, `finishedAt` | RFC 3339 UTC timestamps |
| `filesLoaded`, `testMacros`, `durationMs`, `database` | Run summary. These and the keys down to `recreated` are always present: `null` (`ignoredPaths`: `[]`) when the run stopped before loading the project, e.g. a mistyped path (exit 10) |
| `pathsIgnored` | Paths left out by the built-in exclusions and `.pgmiignore`; an ignored directory counts once ([what gets loaded](DEPLOY-GUIDE.md#what-gets-loaded)) |
| `ignoredPaths` | Each of those paths as `{path, rule}`, `rule` being `built-in` or `.pgmiignore` |
| `created` | `true` when this deploy created the target database. On a routine deploy that usually means a mistyped `-d` |
| `recreated` | `true` when `--overwrite` dropped the target and created it again; after a failure it is left empty, its old data gone |
| `executionUnits`, `unitsCommitted` | Present once deploy.sql execution begins. `executionUnits` is the total count; `unitsCommitted` is how many committed: a unit inside an open `BEGIN` that the failure rolled back does not count (equals `executionUnits` on success) |
| `executionMode` | `"atomic"` (nothing committed, everything rolled back) or `"psql"` (earlier units already committed). Present only on failure |
| `error` | Failure message. Note the key is `error`, not `message` |
| `notices`, `noticesTruncated` | The deploy's notices, on success and on failure: the first 50, up to 200 `WARNING`s from the middle and the last 150, each as `{severity, message}`, plus `detail` and `hint` when the notice has them; `noticesTruncated` counts the rest and is absent when nothing was dropped. stderr still prints every one |
| `sqlstate` | PostgreSQL error code |
| `detail`, `hint`, `where` | PostgreSQL diagnostics, when the server supplied them |
| `failedFile` | The file that raised the error: a project file, found by the text PostgreSQL was executing (or a `Failed in <path>:` message), or `deploy.sql` when the failure was placed there. Absent when a handler in deploy.sql re-raised with `RAISE EXCEPTION` and a different message. Only SQL files are considered, so an editor backup such as `#task.sql#` is never named |
| `failedFileCandidates` | Every SQL file holding the failing text, when more than one does (a copied file); `failedFile` is the first of them |
| `phase` | `"test"` when the failure is in a test or fixture under `__test__/`, `"deploy"` for any other failure of deploy.sql |
| `script`, `sourceLine` | The file the failure was placed in (a project or test file, or `deploy.sql`), and the offending line from it. `where` names a project file instead of quoting its text |
| `line`, `column`, `scriptExpanded` | Present when the failure has a line: always for a parse error, inside a function body too; for a runtime error raised in a DO block (a test file, after any comment header, or `deploy.sql` when the failing part holds one DO block), the line inside it, `column` 0. `scriptExpanded: true` means `line` refers to the macro-expanded script, not the file on disk |

The provenance keys (`schemaVersion` through `finishedAt`) are present on every
run, success or failure. A failed deploy rolls back, so this envelope is often
the only record of it: keep it as CI output.

```json
{
  "status": "failed",
  "exitCode": 13,
  "schemaVersion": 1,
  "pgmiVersion": "0.14.0",
  "contractVersion": "1",
  "serverVersion": "18.1",
  "tls": {"enabled": true, "version": "TLS 1.3", "sslmode": "verify-full"},
  "target": {"host": "db.internal", "port": 5432, "database": "postgres"},
  "connectionSource": "PGMI_CONNECTION_STRING",
  "overrides": [],
  "dotenvKeys": [],
  "startedAt": "2026-10-31T14:02:11.402Z",
  "finishedAt": "2026-10-31T14:02:12.567Z",
  "database": "postgres",
  "created": false,
  "durationMs": 1165,
  "filesLoaded": 0,
  "pathsIgnored": 0,
  "ignoredPaths": [],
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

The `--compat` flag names the session API version your `deploy.sql` was written against. A binary that does not ship that version refuses the deploy (exit 10) instead of running it against a different one.

**Before pgmi 1.0 the session API may change in a minor release** — a view, column or function added, renamed or removed — without a new contract version. Every such change is listed under **Upgrading** in that release's notes. `--compat` does not freeze columns across pgmi releases; pinning the pgmi binary does.

**Currently supported versions:**

| Version | Status | Notes |
|---------|--------|-------|
| `1` | **Current / Latest** | Pre-1.0: changes are listed in each release's Upgrading section. v0.14 renamed `pgmi_plan_view.checksum` to `pgmi_checksum` inside contract 1 |

**When to use `--compat`:**

```bash
# CI/CD pipelines: name the version, and pin the pgmi binary (that pins the columns)
pgmi deploy . -d myapp --compat=1

# Local development: use latest (default, no flag needed)
pgmi deploy . -d myapp
```

**What the API version controls:**
- Session views: `pg_temp.pgmi_source_view`, `pg_temp.pgmi_plan_view`, `pg_temp.pgmi_parameter_view`, `pg_temp.pgmi_runtime_view`, etc.
- Public functions: `pg_temp.pgmi_test_plan()`, `pg_temp.pgmi_test_generate()`
- Column names and types in views

**What it does NOT control:**
- CLI flags and behavior (CLI versioning is separate)
- How the views and the execution contract behave (plan ordering, which files load, the atomic head and psql tail). That follows the binary version, so pin the binary too
- Internal tables (`_pgmi_*`) — these are implementation details

**Error handling:**

An unsupported version is rejected as configuration — exit 10, before pgmi
connects to anything:

```bash
$ pgmi deploy . -d myapp --compat=99
FAILED myapp: failed after 0.00s
pgmi: error: unsupported API version "99"; supported: 1
```

**Best practice:** In CI/CD, pin the pgmi binary and pass `--compat`. Before moving the binary pin, read the Upgrading section of every release in between.

#### API Version Changelog

**Version 1** (Current)
- Changes within v1 before pgmi 1.0 are listed in the release notes' Upgrading sections
- Views: `pgmi_source_view`, `pgmi_plan_view`, `pgmi_parameter_view`, `pgmi_runtime_view`, `pgmi_test_source_view`, `pgmi_test_directory_view`, `pgmi_source_metadata_view`
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

The recreate keeps the database's encoding, locale and locale provider (libc,
ICU with its rules, or builtin), owner, connection limit, per-database and
per-role-in-database settings, comment and privileges. A deploy already running
against the database is never ended: `--overwrite` refuses with exit 15 before it
drops, and so does a second `--overwrite` of the same database while the first
runs ([the remaining window](CONNECTIONS.md#the---overwrite-lifecycle)). Once `DROP DATABASE` starts,
Ctrl-C, `--timeout` and an MCP cancel no longer interrupt it: pgmi finishes the
drop and the recreate (bounded at 60 seconds), then stops. Cancelling never
leaves the database missing or recreated without its owner and privileges.

The deploying role must be able to put all of that back. Before anything is
dropped, `--overwrite` checks that it can become the owner, set each setting
(some, like `log_min_duration_statement`, only a superuser may set), set other
roles' settings, and restore the comment and privileges. If it cannot, it
refuses with exit 10 and names each item; the database is untouched. A
restore that still fails after the drop is attempted for every item, and the
deploy exits 13 listing each one that failed.

**When to use `--overwrite`:**
- Local development with disposable databases
- CI/CD pipelines deploying to **ephemeral test databases** (not production!)
- Never on production or staging databases with real data

### Parameter Flags

| Flag | Description |
|------|-------------|
| `--param key=value` | Set a parameter (repeatable). Accessible in SQL via `current_setting('pgmi.key')`. Keys are lower-cased: `--param apiVersion=2` becomes `pgmi.apiversion`, and `pgmi_parameter_view.key` is `apiversion` |
| `--params-file path` | Load parameters from `.env` file (repeatable, later files override earlier ones, `Env` in a later file overrides `env` in an earlier one). Two keys differing only in case inside one file exit 10. `-` reads stdin, at most once |

Environment variables named `PGMI_PARAM_<NAME>` are parameters too: `PGMI_PARAM_DB_PASSWORD=x` becomes `current_setting('pgmi.db_password')`. No other environment variable is read. An empty `PGMI_PARAM_*` value exits 10. Precedence: `--param` > `--params-file` (later wins) > `PGMI_PARAM_*` > `pgmi.yaml`. Secrets belong in the environment, stdin or a params file, never in `--param` — see [Security](SECURITY.md#pipeline-patterns).

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

# Connection string without the password (it comes from PGPASSWORD or .pgpass)
pgmi deploy . --connection "postgresql://user@localhost:5432/postgres" -d myapp

# .pgpass file (PostgreSQL standard)
# ~/.pgpass format: hostname:port:database:username:password
```

As libpq does, pgmi ignores a password file that group or others can read
(`chmod 0600 ~/.pgpass`), with a warning on stderr. Windows makes no such check.

### Examples

```bash
# Deploy (creates the database if new, deploys incrementally if it exists)
pgmi deploy ./myproject -d myapp

# Recreate database for local development (shows 5-second countdown)
pgmi deploy ./myproject -d myapp_dev --overwrite --force

# Full connection string from the environment (never put the password on argv)
PGMI_CONNECTION_STRING="postgresql://postgres:${DB_PASSWORD}@db.example.com:5432/postgres" pgmi deploy ./myproject -d myapp

# Pin to specific API version for CI/CD stability
pgmi deploy ./myproject -d myapp --compat=1

# With parameters
pgmi deploy ./myproject -d myapp --param env=prod --param version=2.1.0

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
  --host myserver.postgres.database.azure.com

# Azure Entra ID with Service Principal (secret via AZURE_CLIENT_SECRET)
pgmi deploy ./myproject -d myapp --azure \
  --host myserver.postgres.database.azure.com \
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

It also shows which pgmi version and template created the project, read from the first line `pgmi init` writes into `deploy.sql` (`-- Scaffolded by pgmi 0.12.2 from the advanced template.`). Use it to tell whether a project's template files predate a fix. pgmi never changes your files based on it.

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
pgmi init [target_path] [flags]
```

Creates a ready-to-deploy project. Without `--template` (and without a TTY to run the wizard) it writes the **minimal** project: `deploy.sql`, `hello_world.sql` and a `.pgmiignore` that keeps parameter files and backup copies out of the session, nothing else. The `basic` and `advanced` templates add a directory layout and a README, and also write agent guidance the project owns: `AGENTS.md` (a routing table for AI assistants), `CLAUDE.md` (which imports `AGENTS.md`) and `.agents/skills/` in the [Agent Skills](https://agentskills.io/specification) format. Commit them and edit them as your conventions change; they point to `pgmi ai contract` for facts that change with the pgmi version.

| Flag | Default | Description |
|------|---------|-------------|
| `-t, --template` | `minimal` | Template to use (`minimal`, `basic` or `advanced`). On a TTY without `-t`, the wizard asks and preselects `minimal` |

Use `pgmi templates list` to see all available templates with descriptions.

### Templates

| Template | Purpose |
|----------|---------|
| `minimal` | Three files and no opinions: `deploy.sql` runs every SQL file in path order with an `Executing:` notice per file, `hello_world.sql` raises a notice, and `.pgmiignore` keeps parameter files and backup copies out of the session. No tables, ledger, tests, parameters, `pgmi.yaml` or agent skills. For building your own deployment from nothing. |
| `basic` | Conventional database development, with the application outside PostgreSQL. Two layers: run-once `physical/` tables and re-run `semantic/` views and functions. PostgreSQL 15+, runs on any provider. |
| `advanced` | Full reference app. Five application schemas (`internal`, `core`, `api`, `common`, `membership`) plus `extensions` for extension objects, role hierarchy, metadata-driven deployment. PostgreSQL 18+; needs a role with `CREATEROLE` + `CREATEDB` (no superuser). Runs on managed cloud — see the [Production Guide](PRODUCTION.md#managed-cloud-postgresql). |

[Choose a template](templates.md) compares the three side by side.

### Examples

```bash
# Create a project in the current directory
pgmi init .

# Create the minimal project: deploy.sql, hello_world.sql and .pgmiignore
pgmi init myapp

# Two layers with tests and agent guidance
pgmi init myapp --template basic

# Full reference application (more infrastructure, not a higher safety tier)
pgmi init myapp --template advanced

# See available templates
pgmi templates list
```

---

## pgmi config

Launch the interactive connection wizard and save its result to `pgmi.yaml`.

```bash
pgmi config [path]
```

If `path` is omitted, pgmi writes `pgmi.yaml` in the current directory. The
wizard supports local PostgreSQL, Azure Entra ID, AWS IAM, and Google Cloud SQL
authentication.

```bash
# Configure the current directory
pgmi config

# Configure an existing project
pgmi config ./myproject
```

The command requires an interactive terminal; without one it exits 2. In CI, write `pgmi.yaml` directly
or use PostgreSQL environment variables instead. If `pgmi.yaml` already exists,
pgmi asks before replacing its connection settings; an unparseable file also
requires explicit confirmation before it is overwritten.

See [Configuration](CONFIGURATION.md) for the file schema and precedence rules.

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
| `--sort-key` | | Sort-key prefix, e.g. `005/core/`. Without it, the prefix the files with metadata in the same directory share |

Each new block gets a random id and the sort key `<prefix>/<file name>`. The
prefix decides where the file runs, so it comes from you, not from the path:
layers do not run in alphabetical order (`core` before `api`), and an advanced
project's keys belong in its numbered bands. Point the command at one layer's
directory and name its prefix:

```bash
# Preview what would be generated
pgmi metadata scaffold ./myproject/core --sort-key 005/core/

# Write metadata to files
pgmi metadata scaffold ./myproject/core --sort-key 005/core/ --write
pgmi metadata scaffold ./myproject/api --sort-key 006/api/ --write

# New files next to ones that already have metadata take their prefix
pgmi metadata scaffold ./myproject --write
```

A file with no `--sort-key` and no sibling prefix to copy, or whose siblings
use more than one prefix, stops the command before anything is written (exit 2).

A directory inside a project is scanned from the project root (the nearest
directory above it holding `deploy.sql`), so paths, suggested keys and
`.pgmiignore` are the same as for `pgmi metadata scaffold <project>`. Only the
files under the directory you name are changed, and `--write` names each one.

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

Show, without a database, the rows `pgmi_plan_view` will hold at deploy time:
every loaded non-test file once per sort key (its path when it has none), in
execution order. Non-SQL files are included and marked (`isSqlFile: false` in
`--json`), as they are in the view. The listing goes to stdout.

```bash
pgmi metadata plan <project_path> [flags]
```

`--json` prints `{"totalFiles", "plan": [{"executionOrder", "path", "sortKey",
"id", "idempotent", "description", "isSqlFile"}]}`. `metadata validate --json`
prints `totalFiles`, `filesWithMetadata`, `filesWithoutMetadata`,
`validationPassed`, `duplicateIds` and `warnings`. Both exit 10 on invalid
metadata or duplicate ids, as `pgmi deploy` does, and with `--json` the output
then carries `error` and `exitCode`. Every file with an invalid `<pgmi-meta>`
block is reported in one run; `metadata validate --json` lists them in
`errors`, one entry per file.

`validate` warns, without failing, about a SQL file whose `<pgmi-meta>` sits
outside a `/* */` block comment. A `-- <pgmi-meta>` line comment is never read,
so the file has no metadata and the advanced template's deploy refuses it.

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
semantics. Nothing from one tool call is stored in server state. Tools exposed: `deploy`, `init`, `metadata_plan`,
`metadata_validate`, `templates_list`, `ai_overview`, `ai_contract`.

![pgmi serve: an MCP client speaks JSON-RPC 2.0 over stdio to the seven CLI tools, which read project files and deploy to PostgreSQL](diagrams/d09-mcp-serve.drawio.svg)

Register it with Claude Code:

```bash
claude mcp add pgmi -- pgmi serve
```

The server reads JSON-RPC from stdin, writes responses to stdout, and sends all
diagnostics to stderr. It exits cleanly on EOF or SIGINT.

**Failure handling.** A tool that fails — including one that panics — answers
with `isError: true` and the session continues; a panic also carries its stack in
`structuredContent`. A malformed message gets a `-32700` parse error with
`"id": null`, and the server keeps reading from the next line.

**`deploy` resolves like `pgmi deploy`.** Only `path` is required. The
arguments map to flags — `connection` to `--connection`, `database` to `-d`,
`timeout` to `--timeout`, `paramsFile` (a path or an array of paths) to
`--params-file`, `params` to `--param` — and whatever they leave out comes from
the server's environment (`PGMI_CONNECTION_STRING`, `DATABASE_URL`, `PG*`) and
then the project's `pgmi.yaml` (`connection`, `params`, `timeout`), in the same
order as the CLI. Two `params` keys differing only in case exit 10. One
difference: `serve` does not read the project's `.env`, because it is
long-lived and loading one project's `.env` into its environment would carry
those credentials into the next project's deploy. Put the password in the
server's environment, `.pgpass`, or a params file rather than in tool
arguments, which end up in transcripts.

**Concurrency and cancellation.** Each `tools/call` runs on its own, so `ping`
is answered while a deploy runs, and `notifications/cancelled` stops the call
(a cancelled deploy rolls back; no response is sent for it, per the spec).
Deploys to different databases run side by side, each with its own `notices`;
a second deploy to a database another deploy holds gets `isError` with exit
code 15.
The `deploy` result carries the same fields as `pgmi deploy --json`, `notices`
included: the first 50, up to 200 `WARNING`s from the middle, and the last
150, each as `{severity, message}`, plus `detail` and `hint` when the notice has them. `noticesTruncated` counts the rest. The
severity is PostgreSQL's unlocalized (English) one, whatever `lc_messages` is.
The text block is a one-line summary plus the `WARNING`s, on success and,
after the exit code, on failure; every notice is in `structuredContent` once.

**Protocol version.** `pgmi serve` speaks both MCP eras and picks one per
request:

- **2026-07-28 (stateless).** A request whose `_meta` names
  `io.modelcontextprotocol/protocolVersion` is served without a handshake.
  `server/discover` returns the supported versions (`2026-07-28`), the `tools`
  capability, `ttlMs`, `cacheScope` and `serverInfo`. A request naming another
  version gets `-32022` with `data.supported` and `data.requested`. One that
  omits `io.modelcontextprotocol/clientCapabilities` gets `-32602`. Results
  carry `resultType: "complete"`: pgmi tools answer in a single round trip,
  because the request is the transaction boundary, so `pgmi serve` never
  returns `input_required`, and name the server in
  `_meta["io.modelcontextprotocol/serverInfo"]`. `initialize` and `ping` belong
  to the earlier revisions: on a 2026-07-28 request they are `-32601`.
- **Earlier revisions.** `initialize` negotiates: the server echoes the
  client's requested version when it speaks it (`2025-11-25`, `2025-06-18`,
  `2024-11-05`; never `2025-03-26`, whose JSON-RPC batches pgmi does not
  implement), otherwise answers with its newest. An
  unsupported request is not an error — the client decides whether the answer
  is acceptable.

**Structured output.** Tools returning a structured value (`deploy`, `init`,
`metadata_plan`, `metadata_validate`, `templates_list`) declare an
`outputSchema` in `tools/list` and emit matching `structuredContent`, so a client
can validate results without parsing text. The two tools that return
documents — `ai_overview`, `ai_contract` — declare no output schema
because they return text, not typed output. A successful `deploy` answers a
text summary: one line, then each notice as `SEVERITY: message`; its full
record (notices, `ignoredPaths`, timings) is in `structuredContent`. A failed
call's text ends with its exit code and, when known, its phase. `verbose: true` means what `-v` means on the CLI:
`RAISE DEBUG` from deploy.sql and tests reaches the notices.

**Tool annotations.** Every tool declares `readOnlyHint`, `destructiveHint`,
`idempotentHint` and `openWorldHint`. The `ai_*`, `templates_list` and
`metadata_*` tools are read-only, so a client need not ask before calling them;
`deploy` is destructive and reaches a database.

**Destructive deploys need an echo-back.** `deploy` with `overwrite: true` drops
the target database, and this path auto-approves (there is no TTY to prompt). It
therefore also requires `confirmDatabaseName` to equal the resolved target
database exactly:

```json
{"name": "deploy", "arguments": {
  "path": "./myproject", "connection": "postgresql://...",
  "database": "myapp", "overwrite": true, "confirmDatabaseName": "myapp"}}
```

A missing or mismatched name is refused before anything connects, with exit
code 12, as an unconfirmed `--overwrite` is on the CLI, so an agent that
hallucinated a database name cannot drop it.

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
- Core concepts (session tables, deploy.sql pattern, execution contract)
- Quick start commands
- Key SQL conventions

The rest of the agent guidance belongs to the project: `pgmi init` writes
`AGENTS.md` and skills in `.agents/skills/`, and the advanced template adds
reference gateways in `gateways/`.

### pgmi ai contract

```bash
pgmi ai contract
```

Prints the machine-readable session-API contract as JSON. Agents should query this before writing SQL against pgmi views/functions to avoid hallucinating identifiers. Output includes view names and columns, test function signatures, step types, exit codes, and preprocessor macro forms.

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
| `PGMI_PARAM_<NAME>` | `deploy`, `serve` | Deploy parameter `<name>` (lower-cased); empty value exits 10 |
| `PGMI_NON_INTERACTIVE` | any | Set to `1` to disable TUI wizards |
| `CI` | any | Any non-empty value disables TUI wizards |
| `NO_COLOR` | any | Disables ANSI colors (wizards still run; accessibility signal per https://no-color.org). A stderr that is no terminal (a CI log, a pipe, `TERM=dumb`) gets no colors or glyphs either: the summary line reads `ok` or `FAILED` |
| `PGMI_NO_BANNER` | any | Any non-empty value suppresses the identity splash and the `--force` danger banner |
| `PGMI_PAGER` | any | Pager for long output; checked before `PAGER` |
| `PAGER` | any | Fallback pager when `PGMI_PAGER` is unset. Paging applies on a TTY only |
| `AZURE_TENANT_ID` | `deploy` | Azure AD tenant ID |
| `AZURE_CLIENT_ID` | `deploy` | Azure AD client ID |
| `AZURE_CLIENT_SECRET` | `deploy` | Azure AD client secret |
| `AWS_REGION` | `deploy` | AWS region for RDS IAM auth |
| `AWS_DEFAULT_REGION` | `deploy` | Fallback AWS region |

pgmi uses the [`jackc/pgx`](https://github.com/jackc/pgx) driver (Go-native, no libpq dependency), so not every libpq variable works: `PGSERVICE` is refused, and `PGHOSTADDR` and `PGSSLCRL` are ignored. The driver also reads `PGOPTIONS`, `PGTZ`, `PGTARGETSESSIONATTRS`, `PGREQUIREAUTH` and `PGCHANNELBINDING`. The full list, with which variables are secrets, what outranks each one and the cloud SDK variables, is the [environment reference](CICD.md#environment-reference).

### Precedence

```
CLI flags  >  environment variables  >  pgmi.yaml  >  built-in defaults
```

A `PG*` variable or `pgmi.yaml` value does not override a field the connection string names; per-setting rules are in the [environment reference](CICD.md#precedence).

Unless you typed it (`--connection`, `-h`), deploy says on stderr where the connection came from (`connection: from PGMI_CONNECTION_STRING (host:port/database)`, `connection: from pgmi.yaml (...)`, `connection: defaults (localhost:5432/...)`) and names that source in connection and configuration errors. A refused connection also names the ways to point pgmi elsewhere: `--connection`, `PGMI_CONNECTION_STRING`, `pgmi.yaml`. `-h`, `-p` or `-U` next to either variable is refused (exit 10) rather than merged or used alone, so a password in `DATABASE_URL` is never sent to the host named by `-h` and a deploy never falls back to localhost. `--sslmode` replaces the string's sslmode and is announced; `-d` picks the target database.

---

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General error |
| `2` | CLI usage error (invalid arguments or flags) |
| `3` | Panic or unexpected system error |
| `10` | Invalid pgmi configuration — rejected before any database is created, dropped or deployed to |
| `11` | Database connection failed |
| `12` | User denied overwrite approval |
| `13` | SQL execution failed |
| `14` | `deploy.sql` not found |
| `15` | Concurrent deploy detected |
| `16` | Operation exceeded `--timeout` (context deadline exceeded) |
| `130` | Interrupted by SIGINT (Ctrl-C) or SIGTERM |

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

Completion covers commands, flags, template names (for `init --template`), and SSL mode values.

---

## Common Error Messages

### Connection Errors (Exit Code 11)

| Error | Cause | Solution |
|-------|-------|----------|
| `connection refused` | PostgreSQL not running or wrong port | Check `pg_isready -h <host> -p <port>` |
| `password authentication failed` | Wrong credentials | Verify username/password, check `pg_hba.conf` |
| `database "X" does not exist` | Wrong target name | pgmi creates a missing target database itself when the role has `CREATEDB`; without it, see `permission denied to create database` under exit 13. Check the `-d` name and the connection string |
| `SSL connection required` | Server requires SSL | Add `?sslmode=require` to connection string |
| `no pg_hba.conf entry` | Client IP not allowed | Add entry to `pg_hba.conf` or use SSH tunnel |

### SQL Execution Errors (Exit Code 13)

| Error | Cause | Solution |
|-------|-------|----------|
| `relation "X" does not exist` | Table/view not found | Check execution order, ensure dependencies run first |
| `function "X" does not exist` | Missing function | Run schema files before files that call functions |
| `permission denied to create database` | The target does not exist and the role lacks `CREATEDB` | Check the `-d` name, or grant the role `CREATEDB` |
| `permission denied for schema` | Role lacks privileges | Grant the deploy role `CREATE`/`USAGE` on the schema (or `CREATEROLE` + `CREATEDB` for advanced-template setup) |
| `current transaction is aborted` | Earlier error in transaction | Fix the root cause; check `RAISE EXCEPTION` in your SQL |
| `syntax error at or near` | Invalid SQL | Check the file path in error message, fix syntax |
| `Missing required parameter(s)` (SQLSTATE `22023`) | Your `deploy.sql` requires a parameter you did not pass | Set `PGMI_PARAM_<NAME>`, or `--param key=value` for a non-secret; this is your SQL raising, not a pgmi config error |
| `Unknown parameters: ... These are not declared in session.xml` (SQLSTATE `22023`) | Parameter not declared in the template's `session.xml` | Declare it there or drop it from the command line |
| `invalid regex pattern` | Bad pattern passed to `pgmi_test()` | Fix the POSIX regex |

### Configuration Errors (Exit Code 10)

Exit 10 means **pgmi rejected the invocation before running any of your SQL**.
Nothing was deployed and no database was created, dropped, or modified. Every
case is decided offline except the server version check, which runs right
after connecting, before any create or drop.

| Error | Cause | Solution |
|-------|-------|----------|
| `database name is required` | No `-d`, no database in the connection string, no `PGDATABASE` | Add `-d mydb` |
| `invalid parameter format` | `--param` without `=` | Use `--param key=value` |
| `failed to load pgmi.yaml` | Unparseable `pgmi.yaml` | Fix the YAML syntax |
| `unsupported API version` | Invalid `--compat` value | Use `--compat=1` (currently only v1 supported) |
| `failed to read/parse params file` | `--params-file` missing or malformed | Check the path and the `KEY=VALUE` format |
| `environment variable PGMI_PARAM_X is set but empty` | A CI secret mapped to it is missing or misspelled | Fix the secret mapping, or unset the variable |
| `--params-file - (stdin) given 2 times` | `-` repeated | Read stdin once |
| `cannot use multiple cloud authentication methods` | More than one of `--azure`/`--aws`/`--google` | Choose one |

> **Parameter errors raised by your SQL are exit 13, not exit 10.** A
> `Missing required parameter(s)` or `Unknown parameters` message from a
> template is a `RAISE EXCEPTION` inside `deploy.sql` — it reaches you as a SQL
> execution failure with SQLSTATE `22023`, because by then pgmi has connected
> and handed control to your SQL. The same applies to an `invalid regex pattern`
> from `pgmi_test()`. Diagnose those with the exit-13 table above; the fix is
> usually still to add the missing `--param`.

### File Errors (Exit Code 14)

| Error | Cause | Solution |
|-------|-------|----------|
| `deploy.sql not found` | Missing orchestrator | Run `pgmi init` or create deploy.sql manually |

### Debugging Tips

1. **Add `--verbose`** to see DEBUG-level PostgreSQL notices
2. **Check the file path** in error messages — pgmi names the failing file and line (see [Which file failed?](DEPLOY-GUIDE.md#which-file-failed))
3. **Use `RAISE NOTICE`** in your SQL to trace execution flow

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
  --param env=prod \
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
