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
pgmi deploy . --database myapp --param env=dev

# Just:
pgmi deploy .
```

The basic and advanced templates ship a `pgmi.yaml`. The minimal template writes none, unless you set up the connection in the `pgmi init` wizard, which writes one for any template.

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
  env: dev
  max_connections: "100"

timeout: 5m              # Deployment timeout (e.g., 30s, 5m, 1h)
```

All fields are optional. Missing fields fall back to built-in defaults or libpq environment variables. Unknown keys are an error, not a silent fallback — a typo like `usernmae:` fails the load rather than quietly deploying against a default.

### `auth_method`

Setting `auth_method` is equivalent to passing `--azure`, `--aws`, or `--google`;
it is what turns cloud authentication on for a project without a flag. Only
`azure`, `aws`, and `google` are accepted; any other value is refused with exit
10 before pgmi connects. Only a `--azure`, `--aws` or `--google` flag overrides
it, as every flag overrides `pgmi.yaml`. A detail flag (`--aws-region`,
`--azure-tenant-id`, `--azure-client-id`, `--google-instance`) refines the
method `pgmi.yaml` chose instead of replacing it.

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
role granted `CONNECT` and `TEMPORARY` on nothing but its own application
database can deploy to it (PUBLIC has both by default). Set this when your cluster has no `postgres` database, or when the role may
only reach a different administrative database:

```yaml
connection:
  database: myapp
  maintenance_database: admin_db
```

## `.pgmiignore`

`pgmi.yaml` configures how pgmi connects; `.pgmiignore` configures what it
loads. It sits at the project root, uses `.gitignore` syntax, and adds to the
built-in exclusions (dot-paths, `node_modules/`, `__pycache__/`). It only
decides what enters the session; `deploy.sql` still decides what runs. Syntax,
the `deploy.sql` rule and how ignored paths are reported:
[what gets loaded](DEPLOY-GUIDE.md#pgmiignore).

## Precedence Chain

```
CLI flags → --connection string → environment variables → pgmi.yaml → built-in defaults
```

Higher sources override lower ones; every variable and what outranks it is
listed in the [environment reference](CICD.md#environment-reference). A parameter written into the connection
string outranks its environment variable, matching libpq — `PGSSLROOTCERT` is a
*default* for `sslrootcert`, not an override of it. Otherwise a stale variable in
a CI environment could swap the trust anchor out from under a `verify-full`
connection string.

**`--connection` replaces the `connection:` block in `pgmi.yaml`; it does not
merge with it.** The chain above is per-field for flags and environment
variables, but a connection string is one value. Every field it does not name
comes from its PG* variable (`PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`,
`PGAPPNAME`, `PGCONNECT_TIMEOUT`, `PGSSLMODE`), as in libpq, then from the
built-in default (`localhost:5432`), never from the `pgmi.yaml` value. The one
exception is the database: a string without one takes `PGDATABASE`, then
`connection.database` from `pgmi.yaml`; with neither, the deploy refuses with
exit 10. A database name carries no trust policy, so filling it is safe.

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
# The string names only a host, so port, username and sslmode come from PG*
# variables or the built-in defaults — not from pgmi.yaml.
pgmi deploy . --connection "postgresql://other.host"
# → with no PG* set: port 5432, sslmode prefer; the target database is myapp
```

This is deliberate: merging `sslmode: require` from `pgmi.yaml` into a string
pointing at a *different* host would apply one host's trust policy to another.
Name the fields you need in the string, or use `-d` and the granular flags,
which do compose. Pinned by `TestResolveConnectionParams_ConnectionStringReplacesProjectConfig`
and `TestResolveConnectionString_FillsOmittedFromEnvironment`.

pgmi reads no libpq service files: `service=` in a connection string and a set
`PGSERVICE` are refused with exit 10 rather than ignored.

**A dbname in the string is the target.** A dbname in
`PGMI_CONNECTION_STRING` or `DATABASE_URL` is an *environment* source, so it
outranks `connection.database` in `pgmi.yaml` — it replaces the target rather
than only supplying the maintenance database. A string ending in `/postgres`
therefore deploys into `postgres`. Either name the target in the string, or keep
`/postgres` and pass `-d <target>`, which outranks both. See
[the two-database pattern](CLI.md#the-two-database-pattern). The
`connection: from PGMI_CONNECTION_STRING (host:port/database)` line on stderr
names the target, and every `PG*` variable or connection string that replaces a
`pgmi.yaml` value prints one line, e.g. `database: from PGDATABASE (staging_db), not pgmi.yaml (myapp)`,
`database: from PGMI_CONNECTION_STRING (postgres), not pgmi.yaml (myapp)`
or `sslmode: from PGSSLMODE (disable), not pgmi.yaml (verify-full)`. The same
holds for `sslcert`, `sslkey` and `sslrootcert`. A set `PGOPTIONS`, which pgx applies to every
session, prints `session options: from PGOPTIONS` (the name only).

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
# whitespace around key and value is stripped
  spaced  =  trimmed
# surrounding " or ' are removed; single quotes keep everything literal
quoted="has spaces"
secret='abc$Word1 # kept'
# only the FIRST = splits; the value keeps the rest
literal=a=b
# # is only a comment at the start of a line
hash=value#notacomment
# an export prefix is ignored
export shell=ok
```

A params file means what the project `.env` means, or it is refused. Where a
`.env` file would read a line differently, `--params-file` exits 10 naming the
line, never the value: an unquoted value with ` #` (the `.env` reads a
comment) or `$` (it expands a variable), a double-quoted value with `\` or `$`
(it decodes escapes and expands), and an unterminated quote or text after the
closing one. Put such a value in single quotes. A comment belongs on its own
line.

A key becomes the session variable `pgmi.<key>`, so it must be a PostgreSQL
simple identifier: **a letter or underscore first**, then letters, digits or
underscores, 63 characters maximum. `api_key` and `_private` are fine;
`api-key`, `api.key` and `1abc` are rejected before pgmi connects (exit 10).

The same rules apply to `--param key=value`, which also splits on the first
`=` only.

## Parameter Merging

Parameters merge from four sources (later wins):

```
pgmi.yaml params < PGMI_PARAM_* environment variables < --params-file < --param
```

`PGMI_PARAM_<NAME>=value` becomes parameter `<name>`; no other environment
variable is read, and an empty value exits 10 (a CI secret that failed to map).
`--params-file -` reads the same `KEY=VALUE` format from stdin. For secrets, see
[Security: pipeline patterns](SECURITY.md#pipeline-patterns).

Keys compare case-insensitively, because the session stores them lower-cased:
`Env` from `pgmi.yaml` and `--param env=prod` are one parameter, and the later
source wins. Two keys differing only in case inside one source — one
`pgmi.yaml` `params:` block, the `PGMI_PARAM_*` variables, or one params file —
have no defined order, so pgmi rejects them before connecting (exit 10).

Example:

```yaml
# prod.params (loaded via --params-file)
env=base
log_level=info

# pgmi.yaml
params:
  env: dev
  feature_flag: "true"

# CLI
# --param env=prod
```

Result:
| Key | Value | Source |
|-----|-------|--------|
| `log_level` | `info` | params-file |
| `feature_flag` | `true` | pgmi.yaml |
| `env` | `prod` | --param (wins) |

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

timeout: 3m
```

`env` is required and deliberately not set: pass `--param env=dev` locally and
`PGMI_PARAM_ENV` in CI, so a deploy that forgets it fails instead of seeding
demo data.

### Advanced Template (`pgmi init myapp --template advanced`)

```yaml
connection:
  database: mydb
  host: localhost
  port: 5432
  sslmode: prefer

timeout: 5m
```

`env` (`dev`, `test` or `prod`) is required and deliberately absent: pass
`--param env=dev` for a local database, or put `env` and
`database_admin_password` in a params file. The database remembers its `env` and
refuses a redeploy that names another.

## Common Patterns

### Local Development

```yaml
# pgmi.yaml (committed)
connection:
  database: myapp_dev

params:
  env: dev
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

`.env` is loaded from the **project path** (the deploy argument), not the directory you launch `pgmi` from — `pgmi deploy path/to/app` reads `path/to/app/.env`. A `.env` in your shell's current directory is ignored.

A real environment variable wins over the same key in `.env`, but only key by
key. A `PGMI_CONNECTION_STRING` set only in `.env` outranks the pipeline's real
`PGHOST`, `PGDATABASE` and the rest, because a connection string outranks
`PG*` variables. pgmi names what `.env` applied: the source line reads
`connection: from PGMI_CONNECTION_STRING (set by ./.env) (...)`, one line lists
the keys `.env` set (names only, never values), and `--json` carries them as
`dotenvKeys`.

### CI/CD Pipeline

```yaml
# pgmi.yaml (committed) — sensible defaults
connection:
  database: myapp
  sslmode: require

timeout: 5m
```

```yaml
# GitHub Actions — override per environment
- run: pgmi deploy . -d ${{ vars.DATABASE_NAME }} --param env=prod --timeout 15m
  env:
    PGMI_CONNECTION_STRING: ${{ secrets.DATABASE_URL }}
```

With a connection string, `connection.sslmode` from `pgmi.yaml` does not
apply: put `?sslmode=verify-full` in the secret itself. pgmi prints a line
when the yaml value is dropped, and when `PGSSLMODE` replaces it.

### Multi-Environment

Use a single `pgmi.yaml` with per-environment overrides via CLI or env vars:

```yaml
# pgmi.yaml — shared defaults; env is passed per deploy, never committed
connection:
  database: myapp
  sslmode: require
```

```bash
# Local
pgmi deploy . -d myapp_dev --param env=dev

# Staging
pgmi deploy . -d myapp_test --param env=test

# Production
pgmi deploy . -d myapp_prod --param env=prod --timeout 30m
```
