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

## No migration tracking out of the box

The basic template executes files alphabetically every time. There is no built-in `schema_version` table or migration history.

**Basic template:** Every deployment re-runs all files. Your SQL must be idempotent (`CREATE OR REPLACE`, `IF NOT EXISTS`, `ON CONFLICT DO NOTHING`).

**Advanced template:** Includes a 450-line PL/pgSQL tracking system that records script UUIDs, checksums, and execution history. You own and maintain this code.

**Roll your own:** You can implement tracking in deploy.sql with a few lines — see [DEPLOY-GUIDE.md](DEPLOY-GUIDE.md#idempotent-migrations-with-tracking). The tradeoff is that every tracking system makes assumptions (path-based? UUID-based? checksum-based?) that Flyway and Liquibase don't force you to think about.

---

## Debugging is raw PostgreSQL errors

When a migration fails, pgmi shows:

```
execution failed: ERROR: relation "users" does not exist (SQLSTATE 42P01)
```

What it doesn't show: the `Detail`, `Hint`, and `Where` fields from PostgreSQL's error response. pgmi doesn't parse SQL, track line numbers, or maintain source maps.

**Mitigation:** Use exception blocks in deploy.sql to capture which file failed:

```sql
BEGIN
    EXECUTE v_file.content;
EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'Failed on %: %', v_file.path, SQLERRM;
END;
```

See [DEPLOY-GUIDE.md](DEPLOY-GUIDE.md#error-context-with-exception-blocks) for the full pattern.

---

## CREATE INDEX CONCURRENTLY

`CREATE INDEX CONCURRENTLY` cannot run inside a transaction block. This is a PostgreSQL constraint, not a pgmi limitation — the same issue affects Flyway, Liquibase, Prisma, Goose, and Drizzle.

pgmi's workaround: structure deploy.sql so that concurrent indexes are top-level statements outside any transaction block. After `COMMIT`, pgmi's temp tables still exist (session-scoped), and each subsequent top-level statement runs in autocommit mode:

```sql
-- Phase 1: transactional migrations
BEGIN;
-- ... migrations (excluding concurrent index files) ...
COMMIT;

-- Phase 2: concurrent indexes as top-level statements (autocommit mode)
-- These CANNOT be inside a DO block — DO creates an implicit transaction context.
-- Write them explicitly; pgmi's temp tables are still available for queries.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_email ON users(email);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_order_date ON orders(created_at);
```

Note: because top-level SQL has no procedural constructs, you cannot dynamically iterate `pgmi_source_view` for concurrent indexes. Write them explicitly in deploy.sql, or use a separate pgmi deployment phase.

See [DEPLOY-GUIDE.md](DEPLOY-GUIDE.md#create-index-concurrently-workaround) for the complete pattern.

---

## No structured test output

pgmi tests produce NOTICE messages:

```
NOTICE: [pgmi] Test: ./__test__/test_user_crud.sql
```

There is no JUnit XML, TAP protocol, JSON report, pass/fail summary, or timing information. The test either succeeds (continues) or fails (`RAISE EXCEPTION` aborts the transaction).

**The callback mechanism** (`CALL pgmi_test('pattern', 'my_callback')`) is extensible — you can write a PL/pgSQL function that receives test events and produces structured output. See [TESTING.md](TESTING.md#custom-test-callbacks) for the function signature.

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

See [CONNECTIONS.md](CONNECTIONS.md#connection-pooler-compatibility) for details.

---

## The advanced template is a real program

The advanced template's `deploy.sql` is ~450 lines of PL/pgSQL that handles:

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

See [WHY-PGMI.md](WHY-PGMI.md) for when pgmi's approach makes sense.

---

## See also

- [WHY-PGMI.md](WHY-PGMI.md) — Philosophy and comparison with other tools
- [DEPLOY-GUIDE.md](DEPLOY-GUIDE.md) — Patterns that mitigate these limitations
- [CONNECTIONS.md](CONNECTIONS.md) — Connection pooler details
- [TESTING.md](TESTING.md) — Test callback extensibility
