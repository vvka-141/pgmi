-- Gated inside phase 1: these invariants must hold before the deploy commits.
-- The concurrent index is created AFTER commit (phase 2), so it is deliberately
-- not asserted here.
DO $$
DECLARE
    v_status_notnull boolean;
    v_has_constraint boolean;
BEGIN
    SELECT attnotnull INTO v_status_notnull
    FROM pg_attribute
    WHERE attrelid = 'orders'::regclass AND attname = 'status';
    IF NOT COALESCE(v_status_notnull, false) THEN
        RAISE EXCEPTION 'orders.status should exist and be NOT NULL';
    END IF;

    SELECT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'orders_amount_nonneg'
    ) INTO v_has_constraint;
    IF v_has_constraint IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'orders_amount_nonneg constraint should exist (NOT VALID is fine)';
    END IF;
END $$;
