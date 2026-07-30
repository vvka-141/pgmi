/*
<pgmi-meta
    id="71e9acee-5281-4f53-897f-1bd38c36f5d4"
    idempotent="true">
  <description>Seed product reference data</description>
  <sortKeys>
    <key>200/010</key>
  </sortKeys>
</pgmi-meta>
*/

INSERT INTO catalog.product (sku, name) VALUES
    ('ESP-1001', 'Espresso machine'),
    ('GRD-2001', 'Burr grinder'),
    ('KTL-3001', 'Gooseneck kettle'),
    ('FLT-4001', 'Paper filters (100)'),
    ('MUG-5001', 'Stoneware mug')
ON CONFLICT (sku) DO UPDATE SET name = EXCLUDED.name;
