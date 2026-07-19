/*
<pgmi-meta
    id="5f5822c9-6001-4563-a692-87001c0b9de2"
    idempotent="true">
  <description>Catalog schema: product and price</description>
  <sortKeys>
    <key>100/010</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE SCHEMA IF NOT EXISTS catalog;

CREATE TABLE IF NOT EXISTS catalog.product (
    sku  TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

-- Bulk-load target: no secondary index, no foreign key yet.
-- Both arrive in ./post/ after the load; the smoke check in ./checks/
-- is the integrity gate in between.
CREATE TABLE IF NOT EXISTS catalog.price (
    sku          TEXT NOT NULL,
    valid_from   DATE NOT NULL,
    amount_cents INTEGER NOT NULL,
    PRIMARY KEY (sku, valid_from)
);
