-- Identical to project/deploy.sql except the LAST tail statement references
-- a non-existent column.  This makes the deploy fail mid-tail while earlier
-- tail units (the first CIC, the backfill, the constraint validation) survive.

BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
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

SET lock_timeout = '3s';

DO $$
BEGIN
    RAISE NOTICE '[phase 2] plan survived COMMIT: % source file(s), % planned step(s)',
        (SELECT count(*) FROM pg_temp.pgmi_source_view),
        (SELECT count(*) FROM pg_temp.pgmi_plan_view);
END $$;

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

BEGIN;
UPDATE orders SET status = 'archived'
WHERE status = 'pending' AND created_at < now() - interval '10 years';
COMMIT;

ALTER TABLE orders VALIDATE CONSTRAINT orders_amount_nonneg;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_index
        WHERE indexrelid = to_regclass('idx_orders_status') AND NOT indisvalid
    ) THEN
        DROP INDEX idx_orders_status;
    END IF;
END $$;

-- Deliberate error: column does not exist.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_orders_status ON orders (nonexistent_column);
