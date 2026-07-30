-- Lock-safe phased deploy. One program, four phases you read top to bottom.
--
-- The execution contract in one sentence: before your first top-level COMMIT,
-- pgmi's atomic mode; after it, psql mode (each top-level statement runs in
-- its own autocommit, and an explicit BEGIN ... COMMIT is a real transaction).

-- ============================ Phase 1: one transaction ======================
-- Fast, safe schema changes + gated tests. Atomic: any failure rolls back
-- everything, nothing below this COMMIT runs.
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    -- is_sql_file, not just the directory: pgmi_plan_view carries every loaded
    -- file, so migrations/001_orders.sql.bak matches the LIKE and executes as a
    -- migration — silently, exit 0.
    FOR v_file IN (
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND p.path LIKE './migrations/%'
        ORDER BY p.execution_order
    )
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

SAVEPOINT _tests;
CALL pgmi_test();
ROLLBACK TO SAVEPOINT _tests;

COMMIT;

-- ============================ Phase 2: concurrent index =====================
-- psql mode from here on. The temp views are session-scoped, so the plan is
-- still queryable even though the transaction is gone — and each statement
-- runs in autocommit, which is exactly what CREATE INDEX CONCURRENTLY needs.
--
-- A mid-phase failure stops the deploy but keeps the statements that already
-- ran, so everything below is written to be re-runnable.

SET lock_timeout = '3s';

DO $$
BEGIN
    RAISE NOTICE '[phase 2] plan survived COMMIT: % source file(s), % planned step(s)',
        (SELECT count(*) FROM pg_temp.pgmi_source_view),
        (SELECT count(*) FROM pg_temp.pgmi_plan_view);
END $$;

-- Idempotent, and cheap when there is nothing to do. A failed CIC leaves an
-- INVALID index that IF NOT EXISTS would skip over by name, so reap that
-- leftover first — and only that one, never a blanket sweep: an in-flight CIC
-- in another session is INVALID too. CIC blocks neither reads nor writes.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_index
        WHERE indexrelid = to_regclass('idx_orders_customer') AND NOT indisvalid
    ) THEN
        DROP INDEX idx_orders_customer;
    END IF;
END $$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_orders_customer ON orders (customer_id);

-- ============================ Phase 3: atomic backfill ======================
-- Statements after the first COMMIT are not implicitly grouped: a phase that
-- must be atomic says so with its own BEGIN ... COMMIT.
BEGIN;
UPDATE orders SET status = 'archived'
WHERE status = 'pending' AND created_at < now() - interval '10 years';
COMMIT;

-- ============================ Phase 4: deferred work ========================
-- The scan we deferred from phase 1. SHARE UPDATE EXCLUSIVE: reads and writes
-- continue while it validates the existing rows.
ALTER TABLE orders VALIDATE CONSTRAINT orders_amount_nonneg;

-- A second concurrent index, proving phases interleave freely.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_index
        WHERE indexrelid = to_regclass('idx_orders_status') AND NOT indisvalid
    ) THEN
        DROP INDEX idx_orders_status;
    END IF;
END $$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_orders_status ON orders (status);
