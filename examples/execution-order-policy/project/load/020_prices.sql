/*
<pgmi-meta
    id="786c2dde-b2a2-4902-8d15-5cfc861b5fcf"
    idempotent="true">
  <description>Seed price reference data</description>
  <sortKeys>
    <key>200/020</key>
  </sortKeys>
</pgmi-meta>
*/

INSERT INTO catalog.price (sku, valid_from, amount_cents) VALUES
    ('ESP-1001', DATE '2026-07-01', 64900),
    ('GRD-2001', DATE '2026-07-01', 17900),
    ('KTL-3001', DATE '2026-07-01',  5400),
    ('FLT-4001', DATE '2026-07-01',   900),
    ('MUG-5001', DATE '2026-07-01',  1800)
ON CONFLICT (sku, valid_from) DO UPDATE SET amount_cents = EXCLUDED.amount_cents;
