/*
<pgmi-meta
    id="85c16de9-a1cc-491b-88e6-4db887f684c8"
    idempotent="true">
  <description>
    Core foundation: entity_id domain type. Tables that declare object_id as
    core.entity_id opt into lifecycle standards applied by the DDL trigger in
    entity-standards.sql (created_at, deleted_at columns).
  </description>
  <sortKeys>
    <key>003/000</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing core entity foundation'; END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_type
        WHERE typname = 'entity_id' AND typnamespace = 'core'::regnamespace
    ) THEN
        CREATE DOMAIN core.entity_id AS uuid;
    END IF;
END $$;

COMMENT ON DOMAIN core.entity_id IS
    'Opt-in marker for entity lifecycle standards. Declare a column "object_id core.entity_id" in your CREATE TABLE and the DDL event trigger (core_entity_table_standards) injects created_at and deleted_at columns automatically. Works with plain and partitioned tables alike.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ core.entity_id - domain marker for entity tables';
END $$;
