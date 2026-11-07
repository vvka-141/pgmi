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
- `current_setting('pgmi.key', true)`
- `RAISE EXCEPTION` with a condition, to refuse a deploy

**Honest test:** If your team would struggle to write a PL/pgSQL block that loops over a query result and calls `EXECUTE` on each row, changing the deployment logic will be slow. The templates work out of the box, but customizing them is PL/pgSQL work.

The scaffold includes skills with the deploy.sql patterns, and `pgmi ai contract` prints the installed binary's session API. Your team still needs enough PL/pgSQL to review and maintain the deployment code, whoever writes it.

---

## Migration tracking is a choice you make, not a default you inherit

There is no built-in `schema_version` table. pgmi does not track migrations, because pgmi does not decide what runs — your `deploy.sql` does.

**Basic template:** two kinds of file. A file marked run-once (`<pgmi-meta idempotent="false">`, the tables in `physical/`) runs once per database; `deploy.sql` records it in a `deployment.run_once` ledger by id, path and checksum. Every other file (the views and functions in `semantic/`) re-runs on every deploy, so it must be safe to repeat (`CREATE OR REPLACE`). When a run-once file that already ran is edited, moved, flipped to re-run or removed, the deploy fails before anything runs and names the file; a change to a table is a new run-once file. Comments, whitespace outside literals and keyword case do not count as edits. The whole mechanism is one `DO` block and a loop condition in `deploy.sql`: change it if you want a mismatch to warn instead. [`examples/apply-once-tracking`](https://github.com/vvka-141/pgmi/tree/main/examples/apply-once-tracking) shows a path-keyed variant.

**Advanced template:** a fuller PL/pgSQL tracking system recording script UUIDs, checksums, and execution history. More capable, and more code you own.

The honest tradeoff is not "pgmi has no tracking". It is that **you pick the model** — path-based? UUID-based? does a changed checksum warn, fail, or get ignored? Flyway makes that choice for you; pgmi makes you make it, in SQL you can read.

---

## A runtime error names the file, not the line

A runtime error (a constraint violation, division by zero) names the project
file but carries no line or column. One exception: when the whole file is a
single `DO` block that starts on its first line, as test files usually are,
PL/pgSQL's line inside the block is the file's line, and pgmi prints it. An exception handler in deploy.sql that
replaces the error with `RAISE EXCEPTION` loses the file too: it builds a new
error and discards the text pgmi matches on. Let the error propagate, or
re-raise with a bare `RAISE;` ([deploy.sql guide](DEPLOY-GUIDE.md#which-file-failed)).

Parse and analysis errors (syntax, unknown column) carry a position, so pgmi
prints the file, line and column, with PostgreSQL's `DETAIL`, `HINT` and
`WHERE`.

---

## CREATE INDEX CONCURRENTLY runs at top level only

A concurrent index cannot run through `EXECUTE` in a plan loop or a DO block:
PostgreSQL refuses it from any function execution context ("cannot be executed
from a function"). Write each one explicitly at the top level of deploy.sql,
after its first top-level `COMMIT` ([how](HIGHLIGHTS.md#7-create-index-concurrently-works-inside-a-deploy)).

Statements there autocommit, so earlier ones stay applied if a later one
fails. A rerun must also reap an `INVALID` index a failed build left behind;
`IF NOT EXISTS` alone does not ([making a concurrent index
re-runnable](DEPLOY-GUIDE.md#making-a-concurrent-index-re-runnable)).

---

## The default test reporter is plain notices

By default, pgmi tests produce NOTICE messages:

```
NOTICE: [pgmi] Test: ./semantic/__test__/test_task.sql
```

The default reporter has no JUnit XML, TAP, JSON report, or timing information. A test either succeeds (the suite continues) or fails (`RAISE EXCEPTION` aborts the transaction), and a full run ends with `[pgmi] Test suite passed`.

For a structured report you write or adapt a PL/pgSQL callback that receives the test event stream: `CALL pgmi_test('pattern', 'pg_temp.my_callback')` hands it every suite, fixture and test event as a `pg_temp.pgmi_test_event`. `examples/tap-reporter/` is a working TAP 14 reporter. See [Testing](TESTING.md#custom-test-callbacks) for the function signature.

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

Documentation is `README.md`, these docs, `pgmi ai`, and the agent skills the basic and advanced templates write into each project.

---

## File loading has practical limits

pgmi loads all project files into Go memory, then batch-inserts them into PostgreSQL session-scoped temporary tables. This means:

- A 100 MB project uses ~100 MB Go memory + wire transfer time + PostgreSQL storage for temp tables
- PostgreSQL temp tables use local buffers (`temp_buffers`, default 8 MB) and automatically spill to disk when data exceeds the buffer — there is no inherent RAM limitation on temp table size
- Files are loaded as text and assumed to be UTF-8
- A file with a NUL byte or invalid UTF-8 fails the deploy before pgmi connects, naming the file
- Each file is capped at 10 MiB; set `PGMI_MAX_FILE_SIZE` (bytes) to change the cap

**Practical thresholds:**

The bottleneck for large projects is INSERT throughput (parameterized row-by-row inserts) and wire transfer time, not memory:

| Scale | Works well |
|-------|-----------|
| Hundreds of SQL files, dozens of JSON/CSV files (1 KB–10 MB each) | Yes |
| Multi-gigabyte bulk data loads | No — use `COPY` or external ETL |
| Millions of CSV rows via `string_to_array` in PL/pgSQL | Slow — use `COPY` for bulk imports |

pgmi is designed for schema deployment and reference data loading, not bulk data pipelines.

---

## Deploys cannot run through a transaction-mode pooler

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

The advanced template's `deploy.sql` is a PL/pgSQL program of over a thousand
lines that handles:

- XML parameter declaration and validation
- Database role setup (owner, admin, api, kernel, and grants to an optional jobs login)
- Migration tracking with UUID-based idempotency
- Audit logging to `internal.deployment_script_execution_log`
- Test execution gating
- Five application schemas (`internal`, `core`, `api`, `common`, `membership`) plus `extensions` for extension objects

If it breaks, you debug PL/pgSQL exception handling, not framework configuration. You own this code — pgmi scaffolds it, but you maintain it. It needs PostgreSQL 18, and a new template release can need a new database: v0.14 cannot upgrade a v0.13 advanced database in place ([upgrading](advanced/UPGRADING.md)).

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
