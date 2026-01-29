# Security Guide

pgmi handles sensitive parameters (passwords, API keys, tokens) as part of database deployments. This guide covers pgmi's security model, known threat vectors, and recommended practices for CI/CD pipelines.

## How Parameters Flow

```
CLI (--param / --params-file)
        │
        ▼
Go process (in-memory map)
        │
        ├──► pg_temp.pgmi_parameter table (session-scoped, INSERT via $1/$2)
        │
        └──► PostgreSQL session variables (set_config($1, $2, false))
        │
        ▼
Session ends → temp table dropped, session variables gone
```

Every database operation uses **parameterized queries** (`$1`, `$2` placeholders). Parameter values are never interpolated into SQL strings. This eliminates SQL injection as a vector.

## What pgmi Logs

pgmi logs **parameter counts only**, never keys or values — even in `--verbose` mode.

Note: `--verbose` also sets `client_min_messages = 'debug'` on the PostgreSQL session, which enables `RAISE DEBUG` output from SQL scripts. Ensure your SQL scripts do not leak secrets via `RAISE DEBUG`.

```
✓ Loaded 3 parameters into pg_temp.pgmi_parameter
[VERBOSE] CLI parameters override 2 value(s)
```

No parameter name, value, or hint about content ever appears in pgmi's output.

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

**Mitigation:** Use `log_statement = 'ddl'` or `'none'` on deployment targets. Most production setups already do this. If you must use `'all'`, ensure server logs are treated as sensitive and access-controlled.

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

### Acceptable: Direct `--param` in Containers

In ephemeral containers where process list isolation is guaranteed:

```bash
pgmi deploy ./migrations -d myapp \
  --param "db_admin_password=$DB_ADMIN_PASSWORD" \
  --param "api_key=$API_KEY"
```

This is fine when:
- The container runs a single process
- No other users share the host
- The container is destroyed after the pipeline completes

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
| SQL injection | None | Parameterized queries | — |
| pgmi log leakage | None | Only counts logged | — |
| Process list (`/proc`) | Medium | `--params-file` available | Use `--params-file` for secrets |
| PostgreSQL server logs | Medium | Cannot control | Set `log_statement = 'ddl'` |
| User SQL printing secrets | User-controlled | Not pgmi's domain | Review deploy scripts |
| Session variable visibility | Low | Session-scoped | Don't persist session vars |
| Shell history | Low | `--params-file` available | Use `--params-file` or env files |
