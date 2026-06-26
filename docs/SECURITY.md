---
title: "Security"
weight: 110
---

# Security Guide

pgmi handles sensitive parameters (passwords, API keys, tokens) as part of database deployments. This guide covers pgmi's security model, known threat vectors, and recommended practices for CI/CD pipelines.

> **API keys**: the advanced template ships a machine-to-machine API key subsystem (`membership.api_key`, SHA-256-hashed, hash-safe compare, SECURITY DEFINER lifecycle). See [API-KEYS.md](./API-KEYS.md).

## Required Permissions

| Operation | Minimum Privilege |
|-----------|------------------|
| `pgmi deploy` (new database) | `CREATEDB` on the PostgreSQL cluster |
| `pgmi deploy` (existing database) | `CREATE` on `pg_temp` schema (granted by default to all roles) |
| DDL in migrations | Depends on your SQL — typically schema owner or `CREATE` on target schema |
| Advanced template role setup | Superuser (initial setup only) |

pgmi itself only needs to: connect, create temp tables (automatic for any role), set session variables, and execute your deploy.sql. The actual permissions depend on what your SQL does.

---

## How Parameters Flow

```
CLI (--param / --params-file)
        │
        ▼
Go process (in-memory map)
        │
        ├──► pg_temp.pgmi_parameter_view (session-scoped, INSERT via $1/$2)
        │
        └──► PostgreSQL session variables (set_config($1, $2, false))
        │
        ▼
Session ends → temp table dropped, session variables gone
```

Every pgmi-internal database operation uses **parameterized queries** (`$1`, `$2` placeholders). Parameter values are never interpolated into SQL strings by pgmi itself. This eliminates SQL injection in pgmi's own code; your `deploy.sql` and application SQL are your responsibility.

## What pgmi Logs

The **pgmi core CLI** logs **parameter counts only**, never keys or values — even in `--verbose` mode.

```
✓ Loaded 3 parameters into pg_temp._pgmi_parameter
[VERBOSE] CLI parameters override 2 value(s)
```

No parameter name, value, or hint about content ever appears in **pgmi's own (core CLI) output**.

### Your SQL controls its own logging

This guarantee covers only pgmi core. Your `deploy.sql` and template SQL run with full access to the parameters and can print whatever they choose — `RAISE NOTICE`, audit tables, debug logs. pgmi does not redact those for you.

- `--verbose` sets `client_min_messages = 'debug'` on the session, enabling `RAISE DEBUG` output from your SQL. Ensure your scripts do not leak secrets via `RAISE DEBUG`.
- **Redact by default.** When logging parameters from SQL, mask secret-like keys. The advanced template's `deploy.sql` masks keys matching `(password|secret|token|key|credential|auth)`; follow the same pattern in your own scripts.
- A password reaching the server via `ALTER ROLE ... PASSWORD` can land in the PostgreSQL server log under `log_statement = ddl`/`all` — set `log_statement` accordingly.

## Threat Model

### Process List Exposure

**Risk: Medium** — applies to all CLI tools that accept arguments.

```bash
# Visible to any user via ps aux or /proc/<pid>/cmdline
pgmi deploy . --param api_key=sk-live-abc123
```

**Mitigation:** Use `--params-file` instead of `--param` for secrets:

```bash
pgmi deploy . --params-file /tmp/secrets.env
```

The file contents are read into memory and never logged. See [Pipeline Patterns](#pipeline-patterns) below.

### PostgreSQL Server Logs

**Risk: Medium** — depends on server configuration.

pgmi sets session variables using `set_config($1, $2, false)`. If the PostgreSQL server has `log_statement = 'all'`, the resolved parameter values will appear in server logs. This is PostgreSQL's behavior, not pgmi's.

**Mitigation:** For pgmi's `set_config()` calls, `log_statement = 'ddl'` is sufficient — `set_config` is a function call, not DDL, so it won't appear in the log. If you must use `'all'`, ensure server logs are treated as sensitive and access-controlled.

> **Role passwords are a sharper case.** `CREATE/ALTER ROLE … PASSWORD '…'` is **DDL**, so the cleartext password is written to the server log under `log_statement = 'ddl'` *or* `'all'` — `--params-file` does not change this (the value is in the SQL regardless of how it reached pgmi). The advanced template sets role passwords this way. For role-password deployments:
> - Set `log_statement = 'none'` for the deployment window, or wrap the role DDL in `SET LOCAL log_statement = 'none';` (requires the superuser the advanced template already uses for role setup).
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

**Mitigation:** Use `--params-file`, or prefix commands with a space (most shells skip history for space-prefixed commands).

## Pipeline Patterns

### Recommended: `--params-file` with Environment Variables

```bash
# Secrets injected by pipeline (GitHub Actions, GitLab CI, Azure DevOps, etc.)
# Write to a temp file, deploy, clean up

cat > "$RUNNER_TEMP/params.env" <<EOF
db_admin_password=$DB_ADMIN_PASSWORD
api_key=$API_KEY
environment=production
EOF

pgmi deploy ./migrations -d myapp --params-file "$RUNNER_TEMP/params.env"
rm -f "$RUNNER_TEMP/params.env"
```

This avoids process list exposure entirely. The file exists only for the duration of the deployment.

### Exception: Direct `--param` only in fully isolated ephemeral containers

`--params-file` is the default for secrets. Passing them inline with `--param` is a
**deliberate risk-acceptance**, justified only when process-list exposure is provably
moot — and even then, prefer a file when it costs nothing:

```bash
pgmi deploy ./migrations -d myapp \
  --param "db_admin_password=$DB_ADMIN_PASSWORD" \
  --param "api_key=$API_KEY"
```

Acceptable **only** when all of these hold:
- The container runs a single process
- No other users share the host
- The container is destroyed after the pipeline completes

If in doubt, use `--params-file`. Never use a hardcoded secret literal on the command line.

### GitHub Actions Example

```yaml
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Deploy database
        env:
          DB_ADMIN_PASSWORD: ${{ secrets.DB_ADMIN_PASSWORD }}
          API_KEY: ${{ secrets.API_KEY }}
        run: |
          cat > "$RUNNER_TEMP/params.env" <<EOF
          db_admin_password=$DB_ADMIN_PASSWORD
          api_key=$API_KEY
          environment=production
          EOF

          pgmi deploy ./migrations \
            --connection "${{ secrets.DATABASE_URL }}" \
            -d myapp \
            --params-file "$RUNNER_TEMP/params.env"

          rm -f "$RUNNER_TEMP/params.env"
```

### GitLab CI Example

```yaml
deploy:
  stage: deploy
  script:
    - |
      cat > /tmp/params.env <<EOF
      db_admin_password=$DB_ADMIN_PASSWORD
      api_key=$API_KEY
      environment=production
      EOF

      pgmi deploy ./migrations \
        --connection "$DATABASE_URL" \
        -d myapp \
        --params-file /tmp/params.env

      rm -f /tmp/params.env
```

### Azure Entra ID: Eliminating Secrets

Managed Identity eliminates the secret-handling problem entirely — no passwords in env vars, no temp files, no process list exposure:

```bash
pgmi deploy . -d mydb \
  --host myserver.postgres.database.azure.com \
  --azure --sslmode require
```

Service Principal still requires `AZURE_CLIENT_SECRET` as an env var. The same `--params-file` patterns from above apply if you need to pass additional secrets.

## PostgreSQL Server Hardening

For deployments handling sensitive parameters, ensure your PostgreSQL server is configured appropriately:

| Setting | Recommended Value | Reason |
|---------|-------------------|--------|
| `log_statement` | `'ddl'` or `'none'` | Prevents parameter values from appearing in server logs via `set_config()` calls |
| `log_min_duration_statement` | `-1` or a high value | Prevents slow query logs from capturing parameter-setting statements |
| `ssl` | `on` | Encrypts parameter values in transit between pgmi and PostgreSQL |

## Summary

| Vector | Risk | pgmi's Control | Your Action |
|--------|------|----------------|-------------|
| SQL injection (pgmi internals) | None | Parameterized queries | Review your own SQL for injection |
| pgmi core log leakage | None | Core CLI logs counts only | Redact secrets in your own `RAISE NOTICE`/audit logging |
| Process list (`/proc`) | Medium | `--params-file` available | Use `--params-file` for secrets |
| PostgreSQL server logs | Medium | Cannot control | `ddl` for pgmi params; `none` or SCRAM for role passwords |
| User SQL printing secrets | User-controlled | Not pgmi's domain | Review deploy scripts |
| Session variable visibility | Low | Session-scoped | Don't persist session vars |
| Shell history | Low | `--params-file` available | Use `--params-file` or env files |
| Azure Entra ID (MI) | None | Token-based, no secrets | Use `--azure` with Managed Identity |
