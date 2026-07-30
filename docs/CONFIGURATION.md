---
title: "Configuration"
description: "Configure pgmi.yaml, environment variables, parameters, and precedence rules for deployments."
weight: 70
---

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
  maintenance_database: postgres   # Database used only to CREATE or DROP the target (default: postgres)
  sslmode: prefer        # SSL mode: disable, allow, prefer, require, verify-ca, verify-full
  sslcert: /path/to/client.crt    # Client SSL certificate path
  sslkey: /path/to/client.key     # Client SSL private key path
  sslrootcert: /path/to/ca.crt    # Root CA certificate path

  auth_method: azure     # Cloud auth: azure, aws, or google (omit for password auth)
  azure_tenant_id: 00000000-0000-0000-0000-000000000000
  azure_client_id: 11111111-1111-1111-1111-111111111111
  aws_region: us-east-1
  google_instance: my-project:us-central1:my-instance

params:                  # Key-value parameters passed to deploy.sql
  env: development
  max_connections: "100"

timeout: 5m              # Deployment timeout (e.g., 30s, 5m, 1h)
```

All fields are optional. Missing fields fall back to built-in defaults or libpq environment variables. Unknown keys are an error, not a silent fallback — a typo like `usernmae:` fails the load rather than quietly deploying against a default.

### `auth_method`

Setting `auth_method` is equivalent to passing `--azure`, `--aws`, or `--google`;
it is what turns cloud authentication on for a project without a flag. Only
`azure`, `aws`, and `google` are recognised — any other value leaves the
connection on password authentication.

```yaml
connection:
  host: myserver.postgres.database.azure.com
  database: myapp
  username: deploy@contoso.com
  auth_method: azure
  azure_tenant_id: 00000000-0000-0000-0000-000000000000
```

The companion keys only supply defaults; the matching flag or environment
variable outranks them (`--azure-tenant-id` > `$AZURE_TENANT_ID` >
`azure_tenant_id`, and for AWS `--aws-region` > `$AWS_REGION` >
`$AWS_DEFAULT_REGION` > `aws_region`).

**There is deliberately no key for the Azure client secret.** It is read only
from `$AZURE_CLIENT_SECRET`, so a committed `pgmi.yaml` cannot carry it.

See [Connections](CONNECTIONS.md#azure-entra-id) for what each provider needs.

### `maintenance_database`

The database pgmi connects to in order to `CREATE` or `DROP` the target, since
neither can be done from inside the target itself. Defaults to `postgres`.

pgmi connects to the **target** database first. The maintenance database is
dialed only when the target does not exist, or when `--overwrite` is used — so a
role granted `CONNECT` on nothing but its own application database can deploy to
it. Set this when your cluster has no `postgres` database, or when the role may
only reach a different administrative database:

```yaml
connection:
  database: myapp
  maintenance_database: admin_db
```

## Precedence Chain

```
CLI flags → --connection string → environment variables → pgmi.yaml → built-in defaults
```

Higher sources override lower ones. A parameter written into the connection
string outranks its environment variable, matching libpq — `PGSSLROOTCERT` is a
*default* for `sslrootcert`, not an override of it. Otherwise a stale variable in
a CI environment could swap the trust anchor out from under a `verify-full`
connection string.

**`--connection` replaces the `connection:` block in `pgmi.yaml`; it does not
merge with it.** The chain above is per-field for flags and environment
variables, but a connection string is one value. Supplying it leaves every field
it does not name at the built-in default, not at the `pgmi.yaml` value:

```yaml
# pgmi.yaml
connection:
  host: db.internal
  port: 5434
  username: deployer
  database: myapp
  sslmode: require
```

```bash
# The string names only a host, so port, username, sslmode AND database
# come from the built-in defaults — not from pgmi.yaml.
pgmi deploy . --connection "postgresql://other.host"
# → port 5432, sslmode prefer, and the target database is postgres, not myapp
```

This is deliberate: merging `sslmode: require` from `pgmi.yaml` into a string
pointing at a *different* host would apply one host's trust policy to another.
Name the fields you need in the string, or use `-d` and the granular flags,
which do compose. Pinned by `TestResolveConnectionParams_ConnectionStringReplacesProjectConfig`.

**The database name follows the same rule, and it surprises people.** A dbname in
`PGMI_CONNECTION_STRING` or `DATABASE_URL` is an *environment* source, so it
outranks `connection.database` in `pgmi.yaml` — it replaces the target rather
than only supplying the maintenance database. A string ending in `/postgres`
therefore deploys into `postgres`. Either name the target in the string, or keep
`/postgres` and pass `-d <target>`, which outranks both. See
[the two-database pattern](CLI.md#the-two-database-pattern).

Example:

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

## Parameter file format

`--params-file` reads `KEY=VALUE` lines:

```bash
# comments and blank lines are ignored

plain=value
  spaced  =  trimmed        # whitespace around key and value is stripped
quoted="has spaces"         # surrounding " or ' are removed
literal=a=b                 # only the FIRST = splits; the value keeps the rest
hash=value#notacomment      # # is only a comment at the start of a line
```

A key becomes the session variable `pgmi.<key>`, so it must be a PostgreSQL
simple identifier: **a letter or underscore first**, then letters, digits or
underscores, 63 characters maximum. `api_key` and `_private` are fine;
`api-key`, `api.key` and `1abc` are rejected before pgmi connects (exit 10).

`export KEY=value` is **not** supported — the key would be `export KEY`. Drop
the `export` if you are reusing a shell-sourced file.

The same rules apply to `--param key=value`, which also splits on the first
`=` only.

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
| `sslpassword` | Key passphrase is a secret | `PGSSLPASSWORD` env var |
| `overwrite` | Operational safety flag | `--overwrite` CLI flag |
| `force` | Operational safety flag | `--force` CLI flag |

pgmi.yaml is safe to commit to version control. Secrets belong in environment variables, `.pgpass`, or your CI/CD secret store.

## Template Defaults

### Basic Template (`pgmi init myapp --template basic`)

```yaml
connection:
  database: mydb
  host: localhost
  port: 5432
  sslmode: prefer

params:
  env: development

timeout: 3m
```

### Advanced Template (`pgmi init myapp --template advanced`)

```yaml
connection:
  database: mydb
  host: localhost
  port: 5432
  sslmode: prefer

params:
  env: dev

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
# .env (not committed) — credentials only; the target comes from pgmi.yaml
export PGHOST=localhost
export PGPORT=5432
export PGUSER=dev
export PGPASSWORD=devpass
```

```bash
pgmi deploy .   # Credentials from the environment, target myapp_dev from pgmi.yaml
```

A connection string works too, but it carries a database name, and that name is
an environment source — so it **replaces** `connection.database` rather than
complementing it:

```bash
export PGMI_CONNECTION_STRING="postgresql://dev:devpass@localhost:5432/myapp_dev"
pgmi deploy .

# Or keep the maintenance database in the string and name the target on the
# command line, which outranks both:
export PGMI_CONNECTION_STRING="postgresql://dev:devpass@localhost:5432/postgres"
pgmi deploy . -d myapp_dev
```

`.env` is loaded from the **project path** (the deploy argument), not the directory you launch `pgmi` from — `pgmi deploy path/to/app` reads `path/to/app/.env`. A `.env` in your shell's current directory is ignored. Real environment variables always take precedence over `.env` values.

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
