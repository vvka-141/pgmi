/*
<pgmi-meta
    id="6d62bcce-81e0-4bea-ba43-203cee540730"
    idempotent="false">
  <description>
    Core foundation structure: the core.entity_id domain that opts a table into
    entity lifecycle standards, and the fingerprint table behind
    core.ensure_view.
  </description>
  <sortKeys>
    <key>003/000</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE DOMAIN core.entity_id AS uuid;

COMMENT ON DOMAIN core.entity_id IS
    'Opt-in marker for entity lifecycle standards. Declare a column "object_id core.entity_id" in your CREATE TABLE and the deploy-end sweep injects created_at and deleted_at columns automatically. Call pg_temp.apply_entity_table_standards(regclass) inline if you need the columns immediately for indexes. Works with plain and partitioned tables alike.';

CREATE TABLE internal.view_fingerprint (
    view_name   text PRIMARY KEY,
    statement_md5 text NOT NULL,
    stored_definition text NOT NULL,
    stored_options text[]
);

COMMENT ON TABLE internal.view_fingerprint IS
    'What core.ensure_view last installed for each view: the requested statement, and the definition and options PostgreSQL stored. An unchanged view is skipped.';

SELECT pg_temp.exempt_from_rls('internal.view_fingerprint', 'deploy bookkeeping: written by core.ensure_view at deploy');
