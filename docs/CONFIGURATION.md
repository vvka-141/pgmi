# Configuration Reference (pgmi.yaml)

## Overview

`pgmi.yaml` is an optional project-level configuration file that stores connection defaults and parameters. Place it in your project root (next to `deploy.sql`) to enable zero-flag deployments:

```bash
# Instead of:
pgmi deploy . --database myapp --param env=development

# Just:
pgmi deploy .
```

`pgmi init` generates a `pgmi.yaml` automatically for both templates.

## Coming from Other Tools

| Tool | Config File | pgmi Equivalent |
|------|-------------|-----------------|
| Flyway | `flyway.conf` | `pgmi.yaml` |
| .NET EF Core | `appsettings.json` | `pgmi.yaml` |
| Prisma | `schema.prisma` | `pgmi.yaml` |
| Liquibase | `liquibase.properties` | `pgmi.yaml` |

**Key difference:** pgmi.yaml configures the *runner* (connection, parameters, timeout). Deployment logic lives in `deploy.sql`, not in configuration.

## Schema Reference

```yaml
connection:
  host: localhost        # PostgreSQL host (default: from libpq)
  port: 5432             # PostgreSQL port (default: from libpq)
  username: postgres     # PostgreSQL user (default: from libpq)
  database: myapp        # Target database name
  sslmode: prefer        # SSL mode: disable, allow, prefer, require, verify-ca, verify-full

params:                  # Key-value parameters passed to deploy.sql
  env: development
  max_connections: "100"

timeout: 5m              # Deployment timeout (e.g., 30s, 5m, 1h)
```

All fields are optional. Missing fields fall back to built-in defaults or libpq environment variables.

## Precedence Chain

```
CLI flags → environment variables → pgmi.yaml → built-in defaults
```

Higher sources override lower ones. Example:

```yaml
# pgmi.yaml
connection:
  database: myapp
```

```bash
# Environment overrides pgmi.yaml
export PGDATABASE=staging_db

# CLI flag overrides everything
pgmi deploy . -d prod_db
```

Result: deploys to `prod_db`.

## Parameter Merging

Parameters merge from three sources (later wins):

```
pgmi.yaml params < --params-file < --param
```

Example:

```yaml
# params.env (loaded via --params-file)
env=base
log_level=info

# pgmi.yaml
params:
  env: development
  feature_flag: "true"

# CLI
# --param env=production
```

Result:
| Key | Value | Source |
|-----|-------|--------|
| `log_level` | `info` | params-file |
| `feature_flag` | `true` | pgmi.yaml |
| `env` | `production` | --param (wins) |

## Timeout Behavior

The `timeout` field in pgmi.yaml applies only when `--timeout` is not explicitly set on the command line:

```yaml
timeout: 10m   # Used unless --timeout is passed
```

```bash
pgmi deploy .              # Uses 10m from pgmi.yaml
pgmi deploy . --timeout 30m  # Uses 30m, ignores pgmi.yaml
```

If neither pgmi.yaml nor `--timeout` specifies a value, the built-in default (3 minutes) applies.

## Security Design

pgmi.yaml intentionally **excludes**:

| Field | Why Excluded | Use Instead |
|-------|-------------|-------------|
| `password` | Stored in plaintext on disk | `PGMI_CONNECTION_STRING`, `.pgpass`, env vars |
| `overwrite` | Operational safety flag | `--overwrite` CLI flag |
| `force` | Operational safety flag | `--force` CLI flag |
| `azure` | Runtime auth flag, not a project default | `--azure` CLI flag, `AZURE_*` env vars |

pgmi.yaml is safe to commit to version control. Secrets belong in environment variables, `.pgpass`, or your CI/CD secret store.

## Template Defaults

### Basic Template (`pgmi init myapp --template basic`)

```yaml
connection:
  database: myapp

params:
  env: development
```

### Advanced Template (`pgmi init myapp --template advanced`)

```yaml
connection:
  host: localhost
  port: 5432
  database: myapp
  sslmode: prefer

params:
  env: development

timeout: 5m
```

## Common Patterns

### Local Development

```yaml
# pgmi.yaml (committed)
connection:
  database: myapp_dev

params:
  env: development
```

```bash
# .env (not committed) — set connection credentials
export PGMI_CONNECTION_STRING="postgresql://dev:devpass@localhost:5432/postgres"
```

```bash
pgmi deploy .   # Connects via env var, targets myapp_dev from pgmi.yaml
```

### CI/CD Pipeline

```yaml
# pgmi.yaml (committed) — sensible defaults
connection:
  database: myapp
  sslmode: require

params:
  env: development

timeout: 5m
```

```yaml
# GitHub Actions — override per environment
- run: pgmi deploy . -d ${{ vars.DATABASE_NAME }} --param env=production --timeout 15m
  env:
    PGMI_CONNECTION_STRING: ${{ secrets.DATABASE_URL }}
```

### Multi-Environment

Use a single `pgmi.yaml` with per-environment overrides via CLI or env vars:

```yaml
# pgmi.yaml — shared defaults
connection:
  database: myapp
  sslmode: require

params:
  env: development
```

```bash
# Staging
pgmi deploy . -d myapp_staging --param env=staging

# Production
pgmi deploy . -d myapp_prod --param env=production --timeout 30m
```
