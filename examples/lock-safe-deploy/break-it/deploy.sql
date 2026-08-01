-- Identical to project/deploy.sql except the LAST tail statement references
-- a non-existent column.  This makes the deploy fail mid-tail while earlier
-- tail units (the first CIC, the backfill, the constraint validation) survive.

-- ============================ Phase 1: one transaction ======================
-- Fast, safe schema changes + gated tests. Atomic: any failure rolls back
-- everything, nothing below this COMMIT runs.
BEGIN;

-- These statements take ACCESS EXCLUSIVE, which conflicts with every reader.
-- Bound the WAIT so a busy table makes the deploy fail in milliseconds instead
-- of parking a lock request that everything else then queues behind. The tail
-- below is idempotent, so a failed acquisition is a cheap re-run.
--
-- SET LOCAL, not SET: this bound belongs to the transaction holding the locks.
-- The lock itself is held until COMMIT regardless — keep this phase short.
SET LOCAL lock_timeout = '250ms';

-- Reaping a failed concurrent build is the one step in the tail that needs a
-- strong lock: a plain DROP INDEX takes ACCESS EXCLUSIVE on the table. Defined
-- here, called from the tail -- pg_temp objects belong to the session, so they
-- outlive the COMMIT below.
CREATE FUNCTION pg_temp.reap_invalid_index(p_index text, p_table regclass)
RETURNS boolean AS $fn$
DECLARE
    v_index oid := to_regclass(p_index);
BEGIN
    IF v_index IS NULL THEN
        RETURN false;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = v_index AND NOT indisvalid) THEN
        RETURN false;
    END IF;

    -- Bound the wait: this is the one ACCESS EXCLUSIVE request in the tail, and
    -- a busy table must not be able to rebuild the queue phase 1 avoided.
    SET LOCAL lock_timeout = '250ms';
    EXECUTE format('LOCK TABLE %s IN ACCESS EXCLUSIVE MODE', p_table);

    -- Re-check while holding it. An index being built by a CONCURRENTLY in
    -- another session is also indisvalid, and may have become valid since the
    -- probe above -- dropping it then would destroy healthy work.
    IF EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = v_index AND NOT indisvalid) THEN
        RAISE NOTICE 'reaping INVALID index %', p_index;
        EXECUTE format('DROP INDEX %s', v_index::regclass);
        RETURN true;
    END IF;
    RETURN false;
END;
$fn$ LANGUAGE plpgsql;

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

-- Session-level here: SET LOCAL would expire with each autocommitted statement.
-- Longer than phase 1's bound on purpose — these operations take SHARE UPDATE
-- EXCLUSIVE, so waiting a little costs readers and writers nothing.
SET lock_timeout = '3s';

DO $$
BEGIN
    RAISE NOTICE '[phase 2] plan survived COMMIT: % source file(s), % planned step(s)',
        (SELECT count(*) FROM pg_temp.pgmi_source_view),
        (SELECT count(*) FROM pg_temp.pgmi_plan_view);
END $$;

SELECT pg_temp.reap_invalid_index('idx_orders_customer', 'orders');

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
SELECT pg_temp.reap_invalid_index('idx_orders_status', 'orders');

-- Deliberate error: column does not exist.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_orders_status ON orders (nonexistent_column);
