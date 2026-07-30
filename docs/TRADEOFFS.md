---
title: "Trade-offs"
description: "Review pgmi's limits, required PL/pgSQL fluency, PostgreSQL-only scope, and when another tool fits better."
weight: 170
---

# Honest Limitations

pgmi trades framework-managed complexity for SQL-level control. That trade has real costs. This page lists them honestly so you can decide if they apply to your team and project.

---

## PL/pgSQL expertise required

pgmi's `deploy.sql` is a PL/pgSQL program, not configuration. Your team needs to be comfortable with:

- `FOR v_file IN (SELECT ...) LOOP ... END LOOP`
- `EXECUTE v_file.content`
- `BEGIN ... EXCEPTION WHEN OTHERS THEN ... END`
- `current_setting('pgmi.key', true)`

**Honest test:** If your team would struggle to write a PL/pgSQL function that loops over a query result, calls `EXECUTE` on each row, and handles exceptions — pgmi's power is inaccessible. The basic template works out of the box, but customizing deployment logic requires PL/pgSQL fluency.

This is an intentional constraint, not an oversight. pgmi's flexibility comes from giving you a real programming language (PL/pgSQL) instead of a configuration DSL.

---

## Migration tracking is a choice you make, not a default you inherit

There is no built-in `schema_version` table. pgmi does not track migrations, because pgmi does not decide what runs — your `deploy.sql` does.

**Basic template, default:** every deployment re-runs every file. Your SQL must be idempotent (`CREATE OR REPLACE`, `IF NOT EXISTS`, `ON CONFLICT DO NOTHING`). Nothing to drift.

**Basic template, apply-once:** `deploy.sql` ships with a three-line tracking block — a `_migration` ledger, a `NOT EXISTS` filter, an `INSERT` after each file. Uncomment the lines marked `(A)`, `(B)`, `(C)` and you have Flyway's semantics. The template README compares both models side by side.

**Advanced template:** a fuller PL/pgSQL tracking system recording script UUIDs, checksums, and execution history. More capable, and more code you own.

The honest tradeoff is not "pgmi has no tracking". It is that **you pick the model** — path-based? UUID-based? does a changed checksum warn, fail, or get ignored? Flyway makes that choice for you; pgmi makes you make it, in three lines you can read.

---

## Debugging is raw PostgreSQL errors

When a migration fails, pgmi shows:

```
execution failed: ERROR: relation "users" does not exist (SQLSTATE 42P01)
```

pgmi surfaces PostgreSQL's `DETAIL`, `HINT`, and `WHERE` fields when the server
sends them (see `pkg/pgmi/errors.go` `FormatError`). What it can't tell you is
*which project file* the failing statement came from: pgmi doesn't parse SQL,
track line numbers, or maintain source maps.

**Mitigation:** Wrap execution in exception blocks in deploy.sql to enrich the
error with the failing file path:

```sql
BEGIN
    EXECUTE v_file.content;
EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'Failed on %: %', v_file.path, SQLERRM;
END;
```

See [deploy.sql guide](DEPLOY-GUIDE.md#error-context-with-exception-blocks) for the full pattern.

---

## CREATE INDEX CONCURRENTLY

`CREATE INDEX CONCURRENTLY` cannot run inside a transaction block. This is a PostgreSQL constraint, not a pgmi limitation — the same issue affects Flyway, Liquibase, Prisma, Goose, and Drizzle.

pgmi's execution contract handles it directly: **before your first top-level `COMMIT`, pgmi's atomic mode; after it, psql mode.** The head of deploy.sql (through the first top-level transaction terminator) runs as one transaction; every top-level statement after it runs per-statement autocommit on the same session — which is exactly what `CREATE INDEX CONCURRENTLY` needs. pgmi's temp views survive the `COMMIT` (session-scoped) and stay queryable:

```sql
-- Phase 1: transactional migrations (atomic head)
BEGIN;
-- ... migrations ...
COMMIT;

-- Phase 2: concurrent indexes, psql mode (per-statement autocommit)
CREATE INDEX CONCURRENTLY idx_user_email ON users(email);

-- Phase 3: an atomic backfill says so explicitly
BEGIN;
UPDATE users SET email_normalized = lower(email) WHERE email_normalized IS NULL;
COMMIT;

-- Phase 4: more concurrent work
CREATE INDEX CONCURRENTLY idx_order_date ON orders(created_at);
```

The trade-offs to know:

- After the first `COMMIT`, statements are **not** implicitly grouped — a later atomic phase writes its own `BEGIN ... COMMIT`. A mid-tail failure keeps earlier autocommitted statements applied, so tail statements should be idempotent. For a concurrent index that means reaping an `INVALID` leftover before `CREATE INDEX CONCURRENTLY IF NOT EXISTS` — `IF NOT EXISTS` alone matches on name and would skip the wreckage forever; see [making a concurrent index re-runnable](DEPLOY-GUIDE.md#making-a-concurrent-index-re-runnable).
- Concurrent index statements cannot go through `EXECUTE` inside a DO block — PostgreSQL refuses `CREATE INDEX CONCURRENTLY` from any function execution context ("cannot be executed from a function"), even after a `COMMIT`. Write them explicitly at top level; there are no loops or variables there.

See [deploy.sql guide](DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract) for the full contract and `examples/lock-safe-deploy/` for a runnable project.

---

## No structured test output

pgmi tests produce NOTICE messages:

```
NOTICE: [pgmi] Test: ./__test__/test_user_crud.sql
```

There is no JUnit XML, TAP protocol, JSON report, pass/fail summary, or timing information. The test either succeeds (continues) or fails (`RAISE EXCEPTION` aborts the transaction).

**The callback mechanism** (`CALL pgmi_test('pattern', 'pg_temp.my_callback')`) is extensible — you can write a PL/pgSQL function that receives test events and produces structured output. See [Testing](TESTING.md#custom-test-callbacks) for the function signature.

---

## No GUI, no IDE plugin, no ecosystem

pgmi is a CLI tool. There is no:

- VS Code extension
- IntelliJ/DataGrip plugin
- Maven or Gradle plugin
- Spring Boot starter
- Jenkins plugin
- Commercial support or training programs
- Web dashboard

Documentation is `README.md`, these docs, `pgmi ai skills`, and the embedded AI documentation.

---

## File loading has practical limits

pgmi loads all project files into Go memory, then batch-inserts them into PostgreSQL session-scoped temporary tables. This means:

- A 100 MB project uses ~100 MB Go memory + wire transfer time + PostgreSQL storage for temp tables
- PostgreSQL temp tables use local buffers (`temp_buffers`, default 8 MB) and automatically spill to disk when data exceeds the buffer — there is no inherent RAM limitation on temp table size
- Files are loaded as text and assumed to be UTF-8
- Binary files are loaded but not useful (pgmi won't corrupt them, but PL/pgSQL can't process binary data meaningfully)

**Practical thresholds:**

The bottleneck for large projects is INSERT throughput (parameterized row-by-row inserts) and wire transfer time, not memory:

| Scale | Works well |
|-------|-----------|
| Hundreds of SQL files, dozens of JSON/CSV files (1 KB–10 MB each) | Yes |
| Multi-gigabyte bulk data loads | No — use `COPY` or external ETL |
| Millions of CSV rows via `string_to_array` in PL/pgSQL | Slow — use `COPY` for bulk imports |

pgmi is designed for schema deployment and reference data loading, not bulk data pipelines.

---

## Connection poolers are incompatible

pgmi requires session-scoped temporary tables that survive for the entire deployment. Connection poolers in transaction or statement mode reassign backends between operations, destroying the temp tables.

| Pooler | Session mode | Transaction mode | Statement mode |
|--------|-------------|------------------|----------------|
| PgBouncer | Works | Breaks | Breaks |
| Pgpool-II | Works | Breaks | N/A |
| AWS RDS Proxy | Works (pinned) | Breaks | N/A |
| Azure PgBouncer | Works | Breaks | Breaks |

**Solution:** Use the direct PostgreSQL endpoint (port 5432) for pgmi deployments, not the pooled endpoint (port 6432). Your application traffic continues to use the pooler as usual.

See [Connections](CONNECTIONS.md#connection-pooler-compatibility) for details.

---

## The advanced template is a real program

> **Scope: advanced template only.** This is SQL that `pgmi init --template advanced` copied into your project, not behaviour of the pgmi binary.

The advanced template's `deploy.sql` is several hundred lines of PL/pgSQL that
handles:

- XML parameter declaration and validation
- Database role setup (owner, writer, reader, deployer)
- Migration tracking with UUID-based idempotency
- Audit logging to `internal.deployment_script_execution_log`
- Test execution gating
- 4-schema architecture setup

If it breaks, you debug PL/pgSQL exception handling, not framework configuration. You own this code — pgmi scaffolds it, but you maintain it.

For teams comfortable with PL/pgSQL, this is a feature. For teams that want a tool to handle complexity, this is a cost.

---

## Who should use pgmi

**Good fit:**
- Teams fluent in SQL/PL/pgSQL who want deployment logic in the database's native language
- Projects that need conditional deployment, data ingestion, or custom transaction strategies
- Multi-cloud PostgreSQL deployments (same `deploy.sql` works everywhere)
- Teams that value transparency — every piece of deployment state is queryable SQL

**Not a good fit:**
- Teams that prefer framework-managed migrations with zero SQL beyond DDL
- Projects that need multi-database support (pgmi is PostgreSQL-only)
- Organizations that require GUI tools, commercial support, or enterprise ecosystem integrations

See [Why pgmi](WHY-PGMI.md) for when pgmi's approach makes sense.

---

## See also

- [Why pgmi](WHY-PGMI.md) — Philosophy and comparison with other tools
- [Design records](design/why-execution-fabric.md) — the decisions behind these trade-offs, with rejected alternatives
- [deploy.sql guide](DEPLOY-GUIDE.md) — Patterns that mitigate these limitations
- [Connections](CONNECTIONS.md) — Connection pooler details
- [Testing](TESTING.md) — Test callback extensibility
