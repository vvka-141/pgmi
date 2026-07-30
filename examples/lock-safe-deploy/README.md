# Lock-safe deploy

A zero-downtime phased deployment demonstrating pgmi's execution contract:
**before your first top-level `COMMIT`, pgmi's atomic mode; after it, psql
mode.** The head of `deploy.sql` runs as one transaction; every top-level
statement after it runs in per-statement autocommit on the same session —
which is exactly what `CREATE INDEX CONCURRENTLY` requires.

Four phases, read top to bottom as they execute (`project/deploy.sql`):

1. **Atomic head** — schema changes that need only brief locks (`ADD COLUMN`
   with a constant default, `ADD CONSTRAINT ... NOT VALID`), gated by tests.
   Any failure rolls back everything.
2. **Concurrent index** — `CREATE INDEX CONCURRENTLY`, forbidden inside a
   transaction block, legal here because the statement runs in autocommit.
   Written as reap-then-`IF NOT EXISTS` so a re-run converges past a failed
   `INVALID` build without rebuilding a healthy index.
3. **Atomic backfill** — statements after the first `COMMIT` are not
   implicitly grouped, so a phase that must be atomic says so with its own
   `BEGIN ... COMMIT`.
4. **Deferred validation + second index** — `VALIDATE CONSTRAINT` scans under
   `SHARE UPDATE EXCLUSIVE` (reads and writes keep flowing), then another
   concurrent index proves the phases interleave freely.

The session's temp views (`pgmi_plan_view`, `pgmi_source_view`) survive the
head's `COMMIT` — phase 2 prints their counts to prove it.

All of it is asserted in CI (`example-lock-safe` job): both indexes end up
`VALID`, the constraint ends up validated, and a second deploy over the same
database succeeds (the tail is idempotent by construction).

## Run it

Needs a PostgreSQL server (any recent version; a throwaway container works):

```bash
docker run -d --name pgmi-example -e POSTGRES_PASSWORD=postgres -p 5440:5432 postgres:16
export PGMI_CONNECTION_STRING="postgresql://postgres:postgres@127.0.0.1:5440/postgres"

cd project
pgmi deploy . -d lock_safe_demo --force
```

Run it twice — the second deploy converges instead of failing on "index
already exists".

See [deploy.sql guide → the execution contract](../../docs/DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract)
for the full semantics, including what a mid-tail failure leaves behind.
