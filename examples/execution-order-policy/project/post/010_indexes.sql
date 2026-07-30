/*
<pgmi-meta
    id="dc49d826-d8b7-4f32-a6d9-8760dbb73b0e"
    idempotent="true">
  <description>Post-load: secondary index and validated foreign key</description>
  <sortKeys>
    <key>300/010</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE INDEX IF NOT EXISTS price_sku_ix ON catalog.price (sku);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'price_sku_fk'
          AND conrelid = 'catalog.price'::regclass
    ) THEN
        ALTER TABLE catalog.price
            ADD CONSTRAINT price_sku_fk
            FOREIGN KEY (sku) REFERENCES catalog.product (sku) NOT VALID;
    END IF;

    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'price_sku_fk'
          AND conrelid = 'catalog.price'::regclass
          AND NOT convalidated
    ) THEN
        ALTER TABLE catalog.price VALIDATE CONSTRAINT price_sku_fk;
    END IF;
END $$;
