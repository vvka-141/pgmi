---
title: "Production"
description: "Plan pgmi production deployments, rollback strategies, connection requirements, observability, and CI/CD patterns."
weight: 120
---

# Production Guide

This guide covers considerations for running pgmi in production environments: performance, rollback strategies, monitoring, and operational patterns.

> **Example layout.** The examples on this page assume SQL files under `migrations/`, `extensions/`, `setup/` and `rollback/`. The scaffolded templates use `physical/` and `semantic/` instead. Change the path in a `WHERE` to your project's folder: a filter that matches no folder selects nothing and runs nothing, without an error.

## Connection requirements

pgmi requires a **direct PostgreSQL connection** or a connection pooler in **session mode**.

pgmi uses session-scoped temporary tables (`pg_temp`) that exist only for the lifetime of a single database connection. Connection poolers in transaction or statement mode reassign connections between operations, destroying the temporary tables mid-deployment.

| Pooler Mode | Compatible | Why |
|-------------|-----------|-----|
| Session | Yes | Connection stays with one backend for the entire session |
| Transaction | **No** | Backend may change between transactions — `pg_temp` state lost |
| Statement | **No** | Backend may change between statements — `pg_temp` state lost |

This applies to PgBouncer, Pgpool-II, AWS RDS Proxy, Azure PgBouncer, and any other connection pooler. Direct connections are always safe. If you use a pooler, either configure session mode for pgmi deployments or bypass the pooler with a direct connection string.

Deploy with a password over `sslmode=verify-full` and an `sslrootcert`: the libpq default `prefer` falls back to plaintext, which an attacker on the path can force, and then the password and every `PGMI_PARAM_*` value cross the wire unencrypted. `--json` records what the session negotiated in `tls`.

---

## PostgreSQL compatibility

The minimum PostgreSQL version depends on which layer you use:

| Layer | Min PostgreSQL | What drives the floor |
|-------|----------------|-----------------------|
| pgmi core / CLI | 15+ | Session-scoped temp tables and a session-mode connection (no transaction pooler). Set deliberately in the Go core. |
| basic template | 15+ | Plain SQL tables, views and functions — no extensions, roles, or version-specific syntax. |
| advanced template | 18+ | `deploy.sql` refuses an older server before any schema work. The template relies on PostgreSQL 18 features: `uuidv7()`, temporal keys (`WITHOUT OVERLAPS`, `PERIOD` foreign keys), virtual generated columns and `RETURNING OLD`. Also needs the `uuid-ossp` / `pgcrypto` / `pg_trgm` / `hstore` / `btree_gist` extensions, and a role with `CREATEROLE` + `CREATEDB`; `deploy.sql` grants itself the roles it creates. **No superuser required** (tested in CI). |
| API / MCP features | 18+ | Ship inside the advanced template, so they share its floor. |

The 15+ figure applies to the CLI and the basic scaffold (set in the Go core — see the [core minimum](#connection-requirements)). The advanced template raises the floor to **18+**, and its `deploy.sql` checks it first: on an older server it fails with `This project's code was written for PostgreSQL 18; this server runs <version>. pgmi itself works on PostgreSQL 15 and newer: adapt the code to your version, then deploy again.` and exit code 13, before it creates anything.

Both floors are tested, not asserted. CI runs the core suite and the basic
template against PostgreSQL 15, and everything against 18, so a construct that
raises either floor fails the build. A server below 15 is refused before any
schema work with `invalid configuration: this server reports PostgreSQL
<version>, and pgmi requires 15 or newer` and exit code 10. 15 is the oldest
release PostgreSQL supports once 14 reaches end of life on 2026-11-12; a
project on an older server stays on pgmi v0.13.

---

## Managed cloud PostgreSQL

The **basic** template works on PostgreSQL 15+; the **advanced** template requires 18+ (see the [compatibility matrix](#postgresql-compatibility) above). Where they differ further is what the **advanced template** requires from the deployment connection — **none of which is superuser**:

1. **`CREATEROLE`** — the advanced template creates the `<db>_owner`, `<db>_admin`, `<db>_api` and `<db>_kernel` roles. Verify that the provider's deployment role can create them.
2. **`CREATE EXTENSION`** for `uuid-ossp`, `pgcrypto`, `pg_trgm`, `hstore`, `btree_gist`. Extension availability and the role allowed to install each one vary by provider and service tier.
3. **`CREATE SCHEMA`** — the template creates five application schemas (`internal`, `core`, `api`, `common`, `membership`) plus `extensions` for extension objects.

A superuser or the provider's admin creates that login once:

```sql
CREATE ROLE myapp_deploy LOGIN CREATEROLE CREATEDB NOSUPERUSER NOBYPASSRLS PASSWORD '...';
```

The deploy grants itself the roles it creates, so no further grant is needed. Keep the login's password off the command line: put it in the connection string from a secret or in an environment variable ([Connect with a password](CONNECTIONS.md#connect-with-a-password)).

### Grants a provider makes on its own

Some providers grant their own roles on every new schema and table, through event triggers they own, and keep a schema of their own in every database. Databricks Lakebase does both: `databricks_reader_<id>`, `databricks_writer_<id>`, `databricks_superuser` and `databricks_gateway` get grants on each new schema and table, three functions in `public` stay executable by `PUBLIC`, and every database holds a schema `__db_system`. The advanced template's guards hold grants to an exact list, so on such a provider they fail the deploy until it names the provider's roles and schemas:

```text
provider_role_pattern=databricks\_%
provider_schemas=["__db_system"]
```

With these set, every deploy revokes `CREATE` from the provider's roles on the template's schemas and `extensions`: a role that can create objects in a schema on a `search_path` can shadow a function called there and run its code as a `SECURITY DEFINER` caller. It also revokes their `TRUNCATE`, `REFERENCES`, `TRIGGER` and `MAINTAIN` on the template's tables, which act on a whole table past row-level security (Lakebase grants all four to `databricks_superuser`). The guards then ignore the provider's `USAGE` on schemas (and anything it holds on `public`, which no template `search_path` names), its `SELECT`, `INSERT`, `UPDATE` and `DELETE` on membership tables, event-trigger functions another role keeps in `public` (they cannot be called directly), and the listed schemas. Its `CREATE` that survives the revoke, its `TRUNCATE`, `TRIGGER`, `REFERENCES` or `MAINTAIN` on a membership table, any other routine, and grants to `PUBLIC` or to the template's roles still fail the deploy; a pattern that matches one of the template's roles is refused. Row-level security binds a provider role unless it has `BYPASSRLS`: Lakebase's reader sees only what a table's policies give it, which for the template's own tables is nothing, while `databricks_superuser`, its admin group, bypasses row-level security by design and reads every row. The refusal log (`api.access_denial`) and the exchange log (`api.exchange` and its `rest_`, `rpc_` and `mcp_exchange` children) hold every organization's rows, so they are forced to row-level security with one policy for the owner: the provider's reader sees none of their rows.

Entity lifecycle standards (`created_at` / `deleted_at` on tables marked `object_id core.entity_id`) are enforced by a **deploy-end sweep over `pg_temp` functions** — no DDL event trigger, no superuser. The reconcile machinery lives in `pg_temp` and disappears at session end.

| Provider | Basic template | Advanced template | Verification |
|----------|----------------|-------------------|--------------|
| Self-hosted / Docker / Kubernetes | Tested | Tested | CI covers PostgreSQL 15 for core/basic and 18 for everything. |
| AWS RDS for PostgreSQL | Expected | Capability-dependent | Not continuously tested against the managed service. Verify role and extension capabilities. |
| AWS Aurora PostgreSQL | Expected | Capability-dependent | Not continuously tested against the managed service. Verify role and extension capabilities. |
| Azure Database for PostgreSQL — Flexible Server | Expected | Capability-dependent | Not continuously tested against the managed service. Verify role and extension capabilities. |
| Azure Cosmos DB for PostgreSQL (formerly Citus) | Expected | Not validated for sharded tables | Citus-specific semantics need project-level validation. |
| Google Cloud SQL for PostgreSQL | Expected | Capability-dependent | Not continuously tested against the managed service. Verify role and extension capabilities. |
| Databricks Lakebase | Tested by hand | Tested by hand, with `provider_role_pattern` and `provider_schemas` | Manual runs in 2026-11, not in CI. See [Databricks Lakebase](LAKEBASE.md). |
| Google AlloyDB | Expected | Capability-dependent | Not continuously tested against the managed service. Verify role and extension capabilities. |
| Supabase | Expected | Not supported | No PostgreSQL 18 offering found, and the advanced template refuses older servers. |
| Neon | Expected | Capability-dependent | Not continuously tested against the managed service. Verify role and extension capabilities. |
| Railway / Render / Fly.io managed PostgreSQL | Expected | Capability-dependent | Not continuously tested against these services. Verify role and extension capabilities. |

`Tested` means the repository exercises the combination in CI. `Expected` means
the core requirements are standard PostgreSQL features, but the provider is not
part of continuous integration. `Capability-dependent` means the advanced
template works only when the service offers PostgreSQL 18 and the actual deploy
role can create roles and schemas and install all five required extensions. Verify those capabilities on the selected
service and tier before production use.

---

## Deployment strategies

### Single-transaction deployment

Transactional changes succeed or fail together. This provides the strongest
atomicity, but holds locks longer. Sequence advances and effects outside the
transaction are not covered.

```sql
-- deploy.sql
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file
        ORDER BY p.execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

COMMIT;
```

**When to use:**
- Small deployments (< 10 files)
- All changes are quick (no long-running DDL)
- You need all-or-nothing semantics

**Tradeoffs:**
- Locks held until all migrations complete
- Long-running migrations block other operations
- Failure in any file rolls back everything

### Error context: which file failed

pgmi names the failing file and line from the error PostgreSQL raised (`failedFile`, `line` in `--json`), and the `RAISE NOTICE` in the loop above shows how far the deploy got. Do not wrap `EXECUTE` in an exception handler for diagnostics: a handler that re-raises with a new message hides the file and turns every SQLSTATE into `P0001`. If a handler is needed for another reason, end it with a bare `RAISE;`.

### Committing in phases

pgmi's [execution contract](DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract)
makes this first-class, and no external orchestration is involved. Everything
through your first top-level transaction terminator is one atomic unit; after
it, each top-level statement autocommits, exactly as in psql:

```sql
-- deploy.sql
BEGIN;
-- schema changes that must land together
COMMIT;

-- from here on, each top-level statement commits on its own
CREATE INDEX CONCURRENTLY idx_users_email ON users(email);

BEGIN;
-- anything that must be atomic again says so
COMMIT;
```

Two constraints still apply. Both are PostgreSQL's, not pgmi's:

1. **Transaction control needs the tail, not a procedure.** `COMMIT` raises
   `invalid transaction termination` whenever a transaction block surrounds it —
   in a `DO` block *and* in a `CALL`ed procedure alike. That is always the case
   in the atomic head, so a plan loop there commits once when the head ends,
   never per file. Run the same `DO` block at top level in the tail and `COMMIT`
   works: outside a transaction block, PostgreSQL 11+ lets both `DO` and `CALL`
   end and start transactions. Procedures buy you nothing the tail doesn't.
2. **`EXECUTE` runs statements in a function context.** `CREATE INDEX
   CONCURRENTLY`, `VACUUM`, and `CREATE DATABASE` are refused there whatever the
   transaction state — `CREATE INDEX CONCURRENTLY cannot be executed from a
   function`. Write those at top level in the tail, never through the plan loop.

**Recommended default:** idempotent migrations in the atomic head. If a deploy
fails, fix it and redeploy. Reach for the tail when a statement genuinely cannot
run inside a transaction, or when a data migration is too large for one — and
write everything after the first `COMMIT` idempotently, because a later failure
leaves the already-committed statements applied.

### Phased deployment

Different handling for different phases.

```sql
-- deploy.sql
DO $$
DECLARE
    v_file RECORD;
BEGIN
    -- Phase 1: Extensions
    RAISE NOTICE '=== Phase 1: Extensions ===';
    FOR v_file IN (
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND p.path LIKE './extensions/%'
        ORDER BY p.execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;

    -- Phase 2: Migrations
    RAISE NOTICE '=== Phase 2: Migrations ===';
    FOR v_file IN (
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND p.path LIKE './migrations/%'
        ORDER BY p.execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;

    -- Phase 3: Idempotent setup
    RAISE NOTICE '=== Phase 3: Setup ===';
    FOR v_file IN (
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND p.path LIKE './setup/%'
        ORDER BY p.execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

**When to use:**
- Production deployments with mixed requirements
- Extensions and DDL that need different handling
- Clear separation between migration types

## Lock management

### Understanding PostgreSQL locks

DDL operations acquire locks that can block reads and writes:

| Operation | Lock type | Blocks |
|-----------|-----------|--------|
| `CREATE TABLE` | AccessExclusiveLock | Everything on new table |
| `ALTER TABLE ADD COLUMN` | AccessExclusiveLock | All operations on table |
| `CREATE INDEX` | ShareLock | Writes (not reads) |
| `CREATE INDEX CONCURRENTLY` | ShareUpdateExclusiveLock | Other DDL only |

### Lock timeout strategy

Set aggressive lock timeouts to fail fast rather than queue indefinitely:

```sql
-- In your migration files
SET lock_timeout = '5s';  -- Fail if lock not acquired in 5 seconds

ALTER TABLE users ADD COLUMN phone TEXT;

RESET lock_timeout;
```

Or at the start of deploy.sql:

```sql
SET lock_timeout = '10s';
-- ... migrations ...
RESET lock_timeout;
```

### Concurrent index creation

For large tables, use `CONCURRENTLY` to avoid blocking.

**It does not belong in a migration file.** Migration files reach PostgreSQL
through `EXECUTE v_file.content` inside a `DO` block, and `CREATE INDEX
CONCURRENTLY cannot be executed from a function` — the transaction state is
irrelevant. Write it at top level in `deploy.sql`, after your first `COMMIT`,
where pgmi's execution contract puts you in psql mode (per-statement
autocommit):

```sql
-- deploy.sql: Handle concurrent index separately
-- First, run regular migrations in a transaction (the atomic head)
BEGIN;
-- ... regular migrations ...
COMMIT;

-- Then run the concurrent index (psql mode: autocommit, outside any transaction).
-- Reap a previous failed build first: it leaves an INVALID index, and
-- IF NOT EXISTS matches on name alone, so it would skip the rebuild forever.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_index
        WHERE indexrelid = to_regclass('idx_users_email') AND NOT indisvalid
    ) THEN
        DROP INDEX idx_users_email;
    END IF;
END $$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email ON users(email);

-- Finally, continue with remaining work — atomic because it says so
BEGIN;
-- ... remaining migrations ...
COMMIT;
```

After the first `COMMIT`, statements are not implicitly grouped: any later phase that must be atomic writes its own `BEGIN ... COMMIT`, and a mid-script failure keeps the already-autocommitted statements applied — write them idempotently. The `DO` block above is what makes this one idempotent; see [making a concurrent index re-runnable](DEPLOY-GUIDE.md#making-a-concurrent-index-re-runnable) for why neither guard works alone, and the [deploy.sql guide](DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract) for the full contract.

## Rollback strategies

pgmi has no rollback command and cannot undo a deployment after `COMMIT`.
PostgreSQL aborts an uncommitted transaction when a statement or test fails;
reverting a committed change takes a new forward deploy.

### Automatic rollback (transaction-based)

If you use single-transaction deployment, PostgreSQL rolls back automatically on any error:

```sql
-- deploy.sql
BEGIN;
-- ... all migrations via EXECUTE v_file.content ...
COMMIT;
-- If any migration fails before COMMIT, its transactional changes roll back
```

### Compensating transactions

pgmi has no rollback command. `pgmi.rollback` below is a parameter your
`deploy.sql` reads, and running undo scripts is a new forward deploy: it
commits like any other. For each migration, write a matching undo script:

```sql
-- migrations/002_add_email_column.sql
ALTER TABLE users ADD COLUMN email TEXT;

-- rollback/002_add_email_column.sql
ALTER TABLE users DROP COLUMN IF EXISTS email;
```

Then let `deploy.sql` keep a ledger of applied migrations, so the normal run
applies each one once and the undo run reverts only what the ledger shows
applied:

```sql
-- deploy.sql with an undo mode
BEGIN;

CREATE TABLE IF NOT EXISTS public.applied_migration (
    path text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

DO $$
DECLARE
    v_rollback BOOLEAN := COALESCE(current_setting('pgmi.rollback', true), 'false')::boolean;
    v_file RECORD;
BEGIN
    IF v_rollback THEN
        FOR v_file IN (
            SELECT m.path, u.content
            FROM public.applied_migration m
            JOIN pg_temp.pgmi_source_view u
              ON u.path = './rollback/' || substr(m.path, length('./migrations/') + 1)
            ORDER BY m.path DESC
        )
        LOOP
            RAISE NOTICE 'Undoing: %', v_file.path;
            EXECUTE v_file.content;
            DELETE FROM public.applied_migration WHERE path = v_file.path;
        END LOOP;
    ELSE
        FOR v_file IN (
            SELECT p.path, p.content
            FROM pg_temp.pgmi_plan_view p
            JOIN pg_temp.pgmi_source_view s ON s.path = p.path
            WHERE s.is_sql_file AND p.path LIKE './migrations/%'
              AND NOT EXISTS (SELECT 1 FROM public.applied_migration m WHERE m.path = p.path)
            ORDER BY p.execution_order
        )
        LOOP
            RAISE NOTICE 'Executing: %', v_file.path;
            EXECUTE v_file.content;
            INSERT INTO public.applied_migration (path) VALUES (v_file.path);
        END LOOP;
    END IF;
END $$;

CALL pgmi_test();
COMMIT;
```

The undo run reverses path order, so the sample assumes migrations run in path
order (no sort keys). `CALL pgmi_test()` runs in both modes, so the tests must
hold after an undo too, and it needs at least one test file under a
`__test__/` directory.
Usage:
```bash
pgmi deploy . -d mydb --param rollback=true
```

### Which file failed

pgmi names the failing file (and, for syntax errors, the line) without an
exception block, as long as the original error reaches it. A handler that
re-raises with `RAISE EXCEPTION` replaces that error; if you catch, re-raise
with a bare `RAISE;`. See [Which file failed?](DEPLOY-GUIDE.md#which-file-failed).

**Note:** a deploy without top-level `COMMIT` is all-or-nothing — if any
migration fails, the entire transaction rolls back. For true partial progress,
see [Committing in phases](#committing-in-phases).

**Important:** PL/pgSQL does not support direct SAVEPOINT commands. If you need savepoint-based isolation (like the test framework provides), use top-level SQL outside of DO blocks, or use `BEGIN...EXCEPTION...END` blocks which create implicit savepoints for error recovery.

## Monitoring and observability

### Deployment progress

pgmi outputs PostgreSQL `RAISE NOTICE` messages. Capture them in your CI/CD:

```bash
set -o pipefail   # the pipeline's status is pgmi's, not tee's
pgmi deploy . -d mydb 2>&1 | tee deployment.log
```

### Custom progress tracking

Add notices in deploy.sql:

```sql
DO $$
DECLARE
    v_file RECORD;
    v_total INT;
    v_count INT := 0;
BEGIN
    SELECT count(*) INTO v_total FROM pg_temp.pgmi_plan_view;

    FOR v_file IN (
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file
        ORDER BY p.execution_order
    )
    LOOP
        v_count := v_count + 1;
        RAISE NOTICE '[%/%] Executing: %', v_count, v_total, v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

### Audit logging

Log deployments to a table for historical tracking:

```sql
-- deploy.sql: Audit logging
DO $$
DECLARE
    v_deployment_id UUID := gen_random_uuid();
    v_file RECORD;
    v_env TEXT := COALESCE(current_setting('pgmi.env', true), 'unknown');
    v_files_count INT;
BEGIN
    SELECT count(*) INTO v_files_count FROM pg_temp.pgmi_plan_view;

    -- Record deployment start
    INSERT INTO audit.deployments (id, started_at, env, files_count)
    VALUES (v_deployment_id, now(), v_env, v_files_count);

    FOR v_file IN (
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file
        ORDER BY p.execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;

        -- Log file execution
        INSERT INTO audit.deployment_files (deployment_id, file_path, executed_at)
        VALUES (v_deployment_id, v_file.path, now());
    END LOOP;

    -- Record deployment completion
    UPDATE audit.deployments SET completed_at = now() WHERE id = v_deployment_id;
END $$;
```

## Audit and compliance

### Advanced template: runtime security stack

> **Scope: advanced template only.** This is SQL that `pgmi init --template advanced` copied into your project, not behaviour of the pgmi binary.

Deployments scaffolded from the [advanced template](advanced/README.md) carry
a layered runtime defense for the APIs they serve — opaque session handles
resolved in the database, a request context only the api role can state,
membership, and row-level security:

![The layered security stack: identity providers sign people in to login sessions, the session handles resolve through membership into the request context, and row-level security ends the chain](diagrams/d13-security-stack.drawio.svg)

### Advanced template: built-in tracking

> **Scope: advanced template only.** This is SQL that `pgmi init --template advanced` copied into your project, not behaviour of the pgmi binary.

The [advanced template](advanced/README.md) maintains a persistent execution log in `internal.deployment_script_execution_log`:

| Column | Description |
|--------|-------------|
| `deployment_script_object_id` | UUID from `<pgmi-meta>` (or auto-generated from path) |
| `file_path` | File path at execution time |
| `idempotent` | Whether script was re-runnable |
| `deployment_script_content_checksum` | Content hash at execution time |
| `pgmi_checksum` | Normalized content hash (comments, whitespace, keyword case ignored) |
| `sort_key` | Execution ordering key |
| `xact_id` | PostgreSQL transaction ID (correlates with WAL) |
| `executed_at` | Timestamp |
| `executed_by` | Database role that ran the script |

Non-idempotent scripts are skipped on subsequent deployments. One that was edited, moved, re-sorted or removed after it ran refuses the deploy before any file runs. The advanced template reads this history through `internal.vw_deployment` (one row per deploy: when, login role, environment, scripts run, new and changed) and `internal.vw_deployed_script` (one row per script: path, first and last run, run count, checksum).

### Basic template: a run-once ledger

> **Scope: basic template only.** This describes the `deploy.sql` that `pgmi init --template basic` copied into your project, not behaviour of the pgmi binary.

The basic template records each run-once file (`<pgmi-meta idempotent="false">`, its `physical/` tables) in `deployment.run_once`: id, path, checksum and when it ran. Those files are skipped afterwards; every other file re-runs on every deploy (`CREATE OR REPLACE`). A run-once file edited, moved, flipped to re-run or removed after it ran refuses the deploy before any file runs. It keeps no per-deploy history; add that in deploy.sql if you need it — see the [Audit logging](#audit-logging) section below.

### Session transparency

During deployment, all state is queryable — files (`pgmi_source_view`), parameters (`pgmi_parameter_view`), execution plan (`pgmi_plan_view`), test plan (`pgmi_test_plan()`). This enables runtime inspection and debugging, though session state does not persist after the connection ends.

---

## Performance considerations

### Timeout configuration

Set appropriate timeouts for your deployment size:

```bash
# Small deployments (default 3 minutes)
pgmi deploy . -d mydb

# Large deployments
pgmi deploy . -d mydb --timeout 30m

# Via pgmi.yaml
# timeout: 30m
```

### Statement timeout

For individual long-running statements, use PostgreSQL's `statement_timeout`:

```sql
-- In migration file
SET statement_timeout = '10min';
-- Long-running operation
CREATE INDEX idx_large_table ON large_table(column);
RESET statement_timeout;
```

### Connection pooling

See [Connection Requirements](#connection-requirements) above. Use a direct connection or session-mode pooler for deployments:

```bash
# Direct connection for deployment (bypasses pooler)
PGMI_CONNECTION_STRING="postgresql://deployer:${DB_PASSWORD}@db-server:5432/mydb" pgmi deploy .

# Application traffic goes through pooler
# postgresql://user:pass@pgbouncer:6432/mydb
```

## Running unattended

What `pgmi deploy` guarantees when nobody is watching: in CI, from a
scheduler, or under an agent. pgmi handles stopping, concurrency, the checks
before connecting, connection retries and the run's record. deploy.sql
decides the transaction boundaries, so it decides what a failure leaves
applied.

**Timeouts and signals cancel the running statement.**

- `--timeout` (default 3m; `0` means no limit; `timeout:` in `pgmi.yaml`)
  bounds the whole deploy, the autocommit tail included. When it runs out,
  pgmi sends PostgreSQL a cancel request for the running statement, waits up
  to two seconds for the server to stop it, and exits 16. A query is not left
  running on the server after pgmi has gone.
- SIGINT and SIGTERM (the signal a CI job cancel sends) do the same and exit
  130. A second signal quits at once.
- Either way the open transaction rolls back. What deploy.sql already
  committed, such as statements in the autocommit tail after its first
  `COMMIT`, stays applied
  ([the execution contract](DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract)).
- An `--overwrite` drop and re-create runs to completion even if a timeout or
  signal arrives meanwhile, so a deploy never leaves the database dropped and
  not re-created.

**Two deploys never interleave.** Each deploy takes a session advisory lock
per database before it runs anything. A second deploy against the same
database does not wait: it exits 15 at once. `--overwrite` refuses a database
another deploy holds, and two deploys that both create the same missing
database also exit 15. Queue deploys in the CI system
([CI/CD](CICD.md#concurrent-deploys-and-timeouts)).

**Configuration checks run before connecting.** The project path, parameter
syntax, `--compat` and `pgmi.yaml` are checked before pgmi opens a
connection. So are an empty `PGMI_PARAM_<NAME>` (what an unset CI secret
becomes), a malformed `PGMI_MAX_FILE_SIZE`, and an `sslmode` that would send
a cloud token without TLS. Any of these exits 10; a missing `deploy.sql` is
caught then too and exits 14. In none of these cases is a database created,
dropped or changed. Required parameters, such as the templates' `env`, are
deploy.sql's to check once connected: a missing one exits 13 (SQLSTATE 22023),
after a new target database was created.

**Connecting is retried; deploying is not.** A connection that fails with a
transient error (connection exceptions, too many connections, the server
starting up, network errors) is retried three times, up to four attempts in
all, with exponential backoff from 100 ms. Each retry is printed on stderr.
The count is fixed: no flag or setting changes it. The Google Cloud SQL
connector does not retry. pgmi never retries a deploy or a statement:
re-running a failed deploy is the pipeline's job, and retrying inside a
deploy (a serialization failure, a lock timeout) is deploy.sql's.

**`--json` reports each run's result.** With `--json`, pgmi prints one JSON
object on stdout for a success and for a failure, including failures before
the deploy starts. It gives the exit code, SQLSTATE, the failing file and
line, the phase (`deploy` or `test`), and `unitsCommitted`: how many of
deploy.sql's execution units (the transactional head, then each statement of
the autocommit tail) committed before the failure, which tells you whether a
re-run starts from a clean slate. It also names where the connection came
from (`connectionSource`), which `pgmi.yaml` values an environment variable
replaced (`overrides`), and the pgmi, contract and server versions. Keep it
as a build artifact: a database table written by deploy.sql loses the
failed run's rows when the transaction rolls back, so this is often the only
record of a failure ([Reading the result](CICD.md#reading-the-result)).

**pgmi redacts connection passwords and SQL `PASSWORD '...'` literals from
its output.** That covers every error message and the JSON record, whose
target never carries the user, password or token. A secret your SQL expands
inside `EXECUTE` can still reach an error's context: catch errors around such
statements and raise your own message
([Security](SECURITY.md#your-sql-controls-its-own-logging)). Where secrets go
in a pipeline: the [environment reference](CICD.md#environment-reference).

**Cloud authentication supports long deploys.** With `--azure` or `--aws`, pgmi gets
a fresh token for each new connection and caps a connection's lifetime at its
token's, so a deploy can outlast the token (15 minutes for RDS). It refuses
an `sslmode` that could send the token in plain text.

**`--overwrite` restores what it drops.** The re-created database keeps its
encoding and locale, owner, connection limit, per-database and per-role
settings, comment and privileges. If the deploying role could not restore
one of them, pgmi refuses before dropping anything (exit 10). It never drops
the maintenance database or a template. Without a terminal, `--overwrite`
needs `--force`, or it exits 12 instead of waiting for an answer.

**A least-privilege role can deploy.** pgmi connects to the target first and
uses the maintenance database only to create a target that does not exist.
For an existing database, pgmi itself needs `CONNECT` and `TEMPORARY` on it
(its session tables live in `pg_temp`; PUBLIC has both by default, so grant
`TEMPORARY` back if you revoked it); the role still needs whatever privileges
your deploy.sql uses. Give a
production deploy role no `CREATEDB`, so a mistyped `-d` fails instead of
creating an empty database ([CI/CD](#cicd)).

**Agent deploys through `pgmi serve`.** `pgmi serve` exposes deploy over MCP. A deploy with
`overwrite` must also repeat the resolved database name in
`confirmDatabaseName`, checked before connecting; a second deploy to a database another one holds
exits 15, and `serve` does not read a project's `.env`
([pgmi serve](CLI.md#pgmi-serve)).

## CI/CD

The complete pipeline pattern lives in [CI/CD](CICD.md): install a pinned,
checksum-verified binary, which also pins the session contract; use a direct
connection from the CI secret store; pass `--compat 1`; and use `--force` only to bypass an
interactive confirmation. Provider authentication examples are in
[Connections](CONNECTIONS.md).

Give the production deploy role no `CREATEDB`. pgmi creates a missing target
database, so a mistyped `-d` would otherwise deploy into a new, empty database
and exit 0. See [Security](SECURITY.md#required-permissions).

## Deployment gates

Use pgmi's exit codes for pipeline control:

```bash
#!/bin/bash
pgmi deploy . -d mydb
exit_code=$?

case $exit_code in
    0)  echo "Deployment successful" ;;
    10) echo "Configuration error"; exit 1 ;;
    11) echo "Connection failed"; exit 1 ;;
    13) echo "SQL execution failed"; exit 1 ;;
    *)  echo "Unexpected error: $exit_code"; exit 1 ;;
esac
```

See [CLI Reference](CLI.md) for all exit codes.

## Multi-database deployments

### Sequential deployment

Deploy to multiple databases in sequence:

```bash
for db in db1 db2 db3; do
    echo "Deploying to $db..."
    pgmi deploy . -d $db --param env=prod || exit 1
done
```

### Parallel deployment (with caution)

```bash
pids=()
for db in db1 db2 db3; do
    pgmi deploy . -d "$db" --param env=prod &
    pids+=($!)
done
rc=0
for pid in "${pids[@]}"; do
    wait "$pid" || rc=1   # a bare wait returns 0 whatever the deploys did
done
exit $rc
```

**Warning:** Parallel deployment requires that migrations don't depend on cross-database state.

## Disaster recovery

### Pre-deployment backup

Always backup before production deployments:

```bash
pg_dump -Fc mydb > backup_$(date +%Y%m%d_%H%M%S).dump
pgmi deploy . -d mydb
```

### Point-in-time recovery

If using PostgreSQL's WAL archiving, note the LSN before deployment:

```sql
SELECT pg_current_wal_lsn();
-- Deploy
-- If rollback needed, restore to this LSN
```

### Blue-green deployments

Deploy to a standby database, then switch:

```bash
# 1. Deploy to blue (standby) with tests gating the commit
pgmi deploy . -d mydb_blue --param env=prod

# 2. If deployment succeeds (tests passed), switch traffic (application config or DNS)

# 3. Blue becomes production, green becomes standby
```

Tests run as part of deployment via `CALL pgmi_test()` in your deploy.sql. If any test fails, the deployment rolls back and traffic stays on the current production database.

## Next steps

- [Security Guide](SECURITY.md) — Secrets handling in CI/CD
- [Testing Guide](TESTING.md) — Pre-deployment testing patterns
- [Configuration Reference](CONFIGURATION.md) — Timeout configuration and precedence
