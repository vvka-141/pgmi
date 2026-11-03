---
title: "Security"
description: "Handle secrets, permissions, parameters, and CI/CD security boundaries in pgmi deployments."
weight: 110
---

# Security Guide

pgmi handles sensitive parameters (passwords, API keys, tokens) as part of database deployments. This guide covers pgmi's security model, known threat vectors, and recommended practices for CI/CD pipelines.

## Runtime security stack (advanced template)

> **Scope: advanced template only.** This is SQL that `pgmi init --template advanced` copied into your project, not behaviour of the pgmi binary.

This page covers pgmi core's deployment-time security. Projects scaffolded
from the [advanced template](advanced/_index.md) additionally layer a runtime
defense for the APIs they serve. The database resolves every caller from
opaque handles: a login session the transport opened after an OIDC sign-in, an
organization session per browser tab, or a connection a person approved for an
MCP client. No identity or organization is taken from a header. One access
decision serves REST, RPC and MCP, and forced row-level security ends the
chain. What the transport must do is in the
[login transport contract](advanced/LOGIN-TRANSPORT.md):

![The layered security stack: identity providers sign people in to login sessions, the session handles resolve through membership into the request context, and row-level security ends the chain](diagrams/d13-security-stack.drawio.svg)

## Required Permissions

| Operation | Minimum Privilege |
|-----------|------------------|
| `pgmi deploy` (existing database) | `CONNECT` and `TEMPORARY` on the target database (both granted to `PUBLIC` by default; the advanced template revokes them from `PUBLIC`, so a deploy role that is neither superuser nor the database owner needs them granted) |
| `pgmi deploy` (new database) | additionally `CREATEDB` and `CONNECT` on the maintenance database |
| `pgmi deploy --overwrite` | additionally `CONNECT` on the maintenance database, ownership of the target database (to drop it), and `CREATEDB` (to recreate it) |
| DDL in migrations | Depends on your SQL — typically schema owner or `CREATE` on target schema |
| Advanced template role setup | `CREATEROLE` + `CREATEDB`, with `createrole_self_grant = 'set, inherit'` on the role (no superuser) |

pgmi itself only needs to: connect, create temp tables (automatic for any role), set session variables, and execute your deploy.sql. The actual permissions depend on what your SQL does.

**Keep `CREATEDB` away from the production deploy role.** pgmi creates a
missing target database, so with `CREATEDB` a mistyped `-d prodd` creates an
empty `prodd`, deploys into it and exits 0, leaving the real database
untouched. Without it, the same typo fails with "database does not exist".
Grant `CREATEDB` only where creating databases is intended, such as
development and CI. Pipelines that keep it can check `"created"` in
`pgmi deploy --json` output.

**Deploying to an existing database touches only that database.** pgmi connects
to the target first and reaches for a maintenance database (`postgres` by
default, see [`maintenance_database`](CONFIGURATION.md#maintenance_database))
only when the target does not exist and has to be created — so a CI role
granted `CONNECT` on nothing but its own application database is sufficient for
the common case. `--overwrite` always needs the maintenance connection, because
a database cannot drop itself.

---

## How Parameters Flow

![Secrets as parameters: a CI-generated params file flows into one PostgreSQL session where your SQL reads current_setting('pgmi.*'); the command line, pgmi's logs, and disk are never on the path](diagrams/d08-secrets-flow.drawio.svg)

Every pgmi-internal database operation uses **parameterized queries** (`$1`, `$2` placeholders). Parameter values are never interpolated into SQL strings by pgmi itself. This eliminates SQL injection in pgmi's own code; your `deploy.sql` and application SQL are your responsibility.

## What pgmi Logs

The **pgmi core CLI** logs **parameter counts only**, never keys or values — even in `--verbose` mode.

```
[VERBOSE] Loaded 3 parameters from file (total: 3)
[VERBOSE] CLI parameters override 1 value(s)
[VERBOSE] Loading parameters into pg_temp._pgmi_parameter
Loaded 4 parameters
```

No parameter name, value, or hint about content ever appears in **pgmi's own (core CLI) output**.

### Connection passwords in error messages

An error message can quote back the connection string it failed on — `net/url`
does exactly that when a password contains an unescaped `@`, and again when one
contains a bare `%`. pgmi redacts the password before that text reaches stderr,
`--json`, MCP `structuredContent`, or the connection wizard's on-screen error,
covering the URI form (`postgresql://user:secret@host/db`, including a `secret`
that itself contains `@`), the keyword form (`password=`, `sslpassword=`), and
the quoted ADO.NET form (`Password='p ass'`). The host, user and failure reason
are preserved — a redacted message still has to be diagnosable.

### Your SQL controls its own logging

This guarantee covers only pgmi core. Your `deploy.sql` and template SQL run with full access to the parameters and can print whatever they choose — `RAISE NOTICE`, audit tables, debug logs. pgmi does not redact those for you.

- `--verbose` sets `client_min_messages = 'debug'` on the session, enabling `RAISE DEBUG` output from your SQL. Ensure your scripts do not leak secrets via `RAISE DEBUG`.
- **Redact by default.** When logging parameters from SQL, mask secret-like keys. The advanced template's `deploy.sql` masks keys matching `(password|secret|token|key|credential|auth)`; follow the same pattern in your own scripts.
- A password reaching the server via `ALTER ROLE ... PASSWORD` can land in the PostgreSQL server log under `log_statement = ddl`/`all` — set `log_statement` accordingly.
- **A failing top-level statement has its source line echoed back**, psql-style, with a caret under the error position:

  ```
  pgmi: error: execution failed: ERROR: unrecognized role option "totally_bogus_keyword" (SQLSTATE 42601)
  LOCATION: deploy.sql line 1, column 42
  LINE 1: CREATE ROLE app LOGIN PASSWORD 'hunter2' TOTALLY_BOGUS_KEYWORD;
                                                   ^
  ```

  This is pgmi's own output, so `log_statement = 'none'` does not suppress it — a secret written **literally** into `deploy.sql` reaches stderr, `--json` and CI logs on any error on that line. Suppressing the line is not the fix; it is what makes a syntax error diagnosable.

  **Parameters are not exposed this way.** A value passed via `--params-file` reaches SQL through `current_setting('pgmi.key')`, evaluated server-side, so it is never part of the statement text pgmi holds or echoes — including when that statement fails. Nor is it exposed when your SQL interpolates it with `format(... %L ...)` inside `EXECUTE`: PostgreSQL reports that context as `at EXECUTE` without the expanded statement. Pinned by `TestParameterValuesNeverReachDeployErrors`.

## Threat Model

### Process List Exposure

**Risk: Medium** — applies to all CLI tools that accept arguments.

```bash
# Visible to any user via ps aux or /proc/<pid>/cmdline
pgmi deploy . --param api_key=sk-live-abc123
```

**Mitigation:** Pass secrets through a `PGMI_PARAM_*` environment variable, stdin, or a params file, never `--param`:

```bash
export PGMI_PARAM_API_KEY="$API_KEY"          # becomes current_setting('pgmi.api_key')
pgmi deploy . -d mydb
```

A process's environment is readable only by the same user and root (`/proc/<pid>/environ`); its arguments are readable by everyone. Values are read into memory and never logged. See [Pipeline Patterns](#pipeline-patterns) below.

### PostgreSQL Server Logs

**Risk: Medium** — depends on server configuration.

pgmi sets session variables with `set_config($1, $2, false)`, sending each value as a bind parameter. PostgreSQL writes bind parameter values to its log, in full by default, whenever it logs the statement: under `log_statement = 'all'`, under `log_min_duration_statement` / `log_min_duration_sample` (including `0`, which logs every statement), and through auto_explain. This is PostgreSQL's behavior, not pgmi's.

**Mitigation:** Set `log_parameter_max_length = 0` (and `auto_explain.log_parameter_max_length = 0` if auto_explain is loaded) on servers that receive secrets. Statements are still logged, without their parameter values. `log_statement = 'ddl'` alone does not cover the duration logs. Otherwise treat server logs as sensitive and access-controlled.

> **Role passwords are a sharper case.** `CREATE/ALTER ROLE … PASSWORD '…'` is **DDL**, so the cleartext password is written to the server log under `log_statement = 'ddl'` *or* `'all'` — `--params-file` does not change this (the value is in the SQL regardless of how it reached pgmi). The advanced template sets role passwords this way. For role-password deployments:
> - Set `log_statement = 'none'` for the deployment window, or wrap the role DDL in `SET LOCAL log_statement = 'none';` (changing `log_statement` is itself superuser-gated, or needs a `GRANT SET ON PARAMETER log_statement` to the deploy role on PostgreSQL 15+).
> - Or pass a **pre-hashed SCRAM verifier** instead of a cleartext password: `ALTER ROLE x PASSWORD 'SCRAM-SHA-256$4096:…'`. PostgreSQL stores the verifier verbatim, so cleartext never transits the wire or the log. (This is what `psql \password` produces client-side.)

### User SQL Leaking Secrets

**Risk: User-controlled** — pgmi cannot and should not prevent this.

Nothing stops `deploy.sql` from doing:

```sql
RAISE NOTICE 'Key: %', current_setting('pgmi.api_key');
```

This would print the value to stdout. pgmi's philosophy is that the user's SQL drives everything — pgmi provides the infrastructure, not guardrails around SQL content.

**Mitigation:** Treat `deploy.sql` and all executed scripts as security-sensitive code. Review them like you would application code that handles secrets.

### Session Variable Visibility

**Risk: Low** — requires same-session access.

Parameters are accessible via `current_setting('pgmi.key')` and `SHOW ALL` within the deployment session. Since pgmi uses a single dedicated connection, this is only a concern if your SQL intentionally exposes session variables (e.g., writing them to a persistent table).

### Shell History

**Risk: Low** — mostly relevant to interactive use, not pipelines.

```bash
# This ends up in ~/.bash_history
pgmi deploy . --param secret=hunter2
```

**Mitigation:** Use a `PGMI_PARAM_*` variable, stdin or `--params-file`, or prefix commands with a space (most shells skip history for space-prefixed commands).

## Pipeline Patterns

### Recommended: `PGMI_PARAM_*` environment variables

Every environment variable named `PGMI_PARAM_<NAME>` becomes parameter `<name>`, read with `current_setting('pgmi.<name>', true)` like any other. Map secrets straight from the CI secret store; nothing touches disk, nothing reaches argv:

```yaml
- name: Deploy database
  env:
    PGMI_CONNECTION_STRING: ${{ secrets.DATABASE_URL }}
    PGMI_PARAM_DB_ADMIN_PASSWORD: ${{ secrets.DB_ADMIN_PASSWORD }}
    PGMI_PARAM_API_KEY: ${{ secrets.API_KEY }}
  run: pgmi deploy ./migrations -d myapp --param env=prod --force
```

- **Only the prefix is read.** CI tokens, cloud credentials and the rest of the runner's environment never reach the session.
- **An empty value is refused (exit 10).** GitHub Actions expands a missing or misspelled secret to an empty string, and `current_setting` would hand deploy.sql `''`, which passes an `IS NULL` check. pgmi names the variable and stops instead.
- **pgmi does not decide what is required.** An unset variable is a missing parameter; deploy.sql's own required-parameter check raises, with your message. Secrets get no defaults.
- Precedence: `pgmi.yaml` < `PGMI_PARAM_*` < `--params-file` < `--param`.
- A project `.env` may set `PGMI_PARAM_*` for local work; real environment variables win over it.

### Stdin: `--params-file -`

For a secret manager that prints `KEY=VALUE` lines, pipe it in. Nothing is written to disk:

```bash
./fetch-secrets | pgmi deploy ./migrations -d myapp --params-file - --force
```

`-` can appear once among the `--params-file` values and keeps its position in the "later file wins" order. With stdin taken by the parameters, an `--overwrite` confirmation cannot be answered, so pass `--force`. On Linux and macOS, process substitution works too: `--params-file <(./fetch-secrets)`.

### Fallback: a params file

When the values must come from a file, create it private and remove it on every exit path. A plain `rm` after `pgmi deploy` never runs when the deploy fails, because CI shells run with `-e`:

```bash
params=$(mktemp)
trap 'rm -f "$params"' EXIT
printf 'db_admin_password=%s\napi_key=%s\n' "$DB_ADMIN_PASSWORD" "$API_KEY" > "$params"
pgmi deploy ./migrations -d myapp --params-file "$params"
```

`mktemp` creates the file readable only by you; a fixed path such as `/tmp/params.env` survives the job on a shared runner.

### Never: secrets in `--param`

`--param db_admin_password=$DB_ADMIN_PASSWORD` puts the expanded value in argv, visible in `ps` to every user on the host and often in CI logs. Use one of the three patterns above; they cost the same.

### Azure Entra ID: Eliminating Secrets

Managed Identity eliminates the secret-handling problem entirely — no passwords in env vars, no temp files, no process list exposure:

```bash
pgmi deploy . -d mydb \
  --host myserver.postgres.database.azure.com \
  --azure
```

With `--azure` or `--aws`, pgmi refuses `sslmode` `disable`, `allow` and `prefer` (exit 10), so the token never travels without TLS. Azure defaults to `verify-full`; AWS defaults to `verify-full` with the RDS CA bundle in `--sslrootcert`, otherwise `require`. See [TLS with token authentication](CONNECTIONS.md#tls-with-token-authentication).

Service Principal still requires `AZURE_CLIENT_SECRET` as an env var. Pass any additional secrets with the [pipeline patterns](#pipeline-patterns) above.

## PostgreSQL Server Hardening

For deployments handling sensitive parameters, ensure your PostgreSQL server is configured appropriately:

| Setting | Recommended Value | Reason |
|---------|-------------------|--------|
| `log_parameter_max_length` | `0` | Keeps bind parameter values, which carry pgmi's parameters, out of every statement log |
| `auto_explain.log_parameter_max_length` | `0` | The same for auto_explain, if loaded |
| `log_statement` | `'ddl'` or `'none'` | `'all'` logs every statement; role-password DDL needs `'none'` (above) |
| `ssl` | `on` | Encrypts parameter values in transit between pgmi and PostgreSQL |

## Summary

| Vector | Risk | pgmi's Control | Your Action |
|--------|------|----------------|-------------|
| SQL injection (pgmi internals) | None | Parameterized queries | Review your own SQL for injection |
| pgmi core log leakage | None | Core CLI logs counts only | Redact secrets in your own `RAISE NOTICE`/audit logging |
| Process list (`/proc`) | Medium | `PGMI_PARAM_*`, stdin and `--params-file` available | Never put a secret in `--param` |
| PostgreSQL server logs | Medium | Cannot control | `log_parameter_max_length = 0`; `none` or SCRAM for role passwords |
| User SQL printing secrets | User-controlled | Not pgmi's domain | Review deploy scripts |
| Session variable visibility | Low | Session-scoped | Don't persist session vars |
| Shell history | Low | `PGMI_PARAM_*`, stdin and `--params-file` available | Keep secrets out of typed commands |
| Azure Entra ID (MI) | None | Token-based, no secrets | Use `--azure` with Managed Identity |
| IAM token sent without TLS | None | Plaintext-capable sslmodes refused with `--azure`/`--aws` | Give AWS the RDS CA bundle for `verify-full` |
