/*
<pgmi-meta
    id="0f1b2a3e-613d-4716-976f-819db1752d9c"
    idempotent="true">
  <description>Catalog invariants: structure always, data when present</description>
  <sortKeys>
    <key>150/000</key>
    <key>400/000</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$
DECLARE
    v_product_count   BIGINT;
    v_price_count     BIGINT;
    v_bad_price_count BIGINT;
    v_orphan_count    BIGINT;
BEGIN
    IF to_regclass('catalog.product') IS NULL OR to_regclass('catalog.price') IS NULL THEN
        RAISE EXCEPTION 'smoke: catalog tables are missing';
    END IF;

    SELECT count(*) INTO v_product_count FROM catalog.product;
    SELECT count(*) INTO v_price_count   FROM catalog.price;

    SELECT count(*) INTO v_bad_price_count
    FROM catalog.price
    WHERE amount_cents <= 0;

    IF v_bad_price_count > 0 THEN
        RAISE EXCEPTION 'smoke: % price rows with non-positive amounts', v_bad_price_count;
    END IF;

    SELECT count(*) INTO v_orphan_count
    FROM catalog.price p
    WHERE NOT EXISTS (SELECT 1 FROM catalog.product pr WHERE pr.sku = p.sku);

    IF v_orphan_count > 0 THEN
        RAISE EXCEPTION 'smoke: % price rows reference unknown products', v_orphan_count;
    END IF;

    RAISE NOTICE 'smoke: ok (% products, % prices)', v_product_count, v_price_count;
END $$;
