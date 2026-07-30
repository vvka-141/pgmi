/*
<pgmi-meta
    id="c0285423-1f30-480e-895e-67cffaed3b67"
    idempotent="true">
  <description>Volume discount table (added in a feature branch)</description>
  <sortKeys>
    <key>200/015</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE TABLE IF NOT EXISTS catalog.discount (
    sku          TEXT NOT NULL,
    min_quantity INTEGER NOT NULL,
    percent_off  NUMERIC(5,2) NOT NULL,
    PRIMARY KEY (sku, min_quantity)
);
