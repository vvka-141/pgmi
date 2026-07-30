/*
<pgmi-meta
    id="427c2342-20b6-41a8-97eb-cc3f3ac93d10"
    idempotent="true">
  <description>Seed category reference data (added on a parallel branch)</description>
  <sortKeys>
    <key>200/010</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE TABLE IF NOT EXISTS catalog.category (
    name TEXT PRIMARY KEY
);

INSERT INTO catalog.category (name) VALUES
    ('Machines'),
    ('Grinders'),
    ('Accessories')
ON CONFLICT (name) DO NOTHING;
