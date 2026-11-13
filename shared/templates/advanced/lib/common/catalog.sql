/*
<pgmi-meta
    id="34e1a3e3-38ba-4ccb-8f7e-e9b29e443bf8"
    idempotent="true">
  <description>
    Catalog questions the deploy-time helpers and the core.* setup functions
    ask, defined before lib/core so both work in and outside a deploy.
  </description>
  <sortKeys>
    <key>001/003</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION common.has_column(p_table regclass, p_column name)
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (SELECT FROM pg_attribute
                   WHERE attrelid = p_table AND attname = p_column AND attnum > 0 AND NOT attisdropped)
$$;

COMMENT ON FUNCTION common.has_column(regclass, name) IS
    'Whether the table has a live (not dropped) column of that name.';
