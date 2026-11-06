/*
<pgmi-meta
    id="f1250e50-1adf-437c-978f-3b3866a17b78"
    idempotent="false">
  <description>
    Encoding wrapper types (utf8, latin1, win1252). The casts and converters
    that use them live in encoding.sql.
  </description>
  <sortKeys>
    <key>001/000</key>
  </sortKeys>
</pgmi-meta>
*/

-- Composite types, not domains: CREATE CAST needs a type we own, and
-- PostgreSQL ignores casts to a domain.
CREATE TYPE common.utf8 AS (v text);
CREATE TYPE common.latin1 AS (v text);
CREATE TYPE common.win1252 AS (v text);

COMMENT ON TYPE common.utf8 IS
    'UTF-8 decoded text wrapper. Enables cast chaining: my_bytea::utf8::jsonb';
COMMENT ON TYPE common.latin1 IS
    'Latin-1 (ISO-8859-1) decoded text wrapper. Enables cast chaining: my_bytea::latin1::text';
COMMENT ON TYPE common.win1252 IS
    'Windows-1252 decoded text wrapper. Enables cast chaining: my_bytea::win1252::text';
