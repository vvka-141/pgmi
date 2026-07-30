-- Metadata-only since PG11: a non-volatile default does not rewrite the table.
-- Still takes ACCESS EXCLUSIVE for a moment, so it belongs in the short phase-1
-- transaction, never behind a long-running query.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'pending';

-- The safe road to a validated CHECK: add it NOT VALID here (no table scan, quick
-- lock), then VALIDATE in phase 2 under SHARE UPDATE EXCLUSIVE (concurrent reads
-- and writes keep flowing).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'orders_amount_nonneg') THEN
        ALTER TABLE orders ADD CONSTRAINT orders_amount_nonneg CHECK (amount >= 0) NOT VALID;
    END IF;
END $$;
