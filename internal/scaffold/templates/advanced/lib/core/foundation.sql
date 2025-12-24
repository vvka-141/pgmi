/*
<pgmi-meta
    id="85c16de9-a1cc-491b-88e6-4db887f684c8"
    idempotent="true">
  <description>
    Core schema foundation: managed object base table with UUID identity
  </description>
  <sortKeys>
    <key>003/000</key>
  </sortKeys>
</pgmi-meta>
*/
-- ============================================================================
-- Managed Object Base Table
-- ============================================================================

DO $$
BEGIN
    RAISE NOTICE '→ Installing managed object infrastructure';
END $$;

CREATE TABLE IF NOT EXISTS core.managed_object (
    object_id UUID PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,

    CONSTRAINT is_abstract CHECK (false) NO INHERIT
);

COMMENT ON TABLE core.managed_object IS
    'Abstract base table for all managed entities. Provides object identity and lifecycle metadata with soft-deletion support.';

COMMENT ON COLUMN core.managed_object.object_id IS
    'Unique identifier for the entity';

COMMENT ON COLUMN core.managed_object.created_at IS
    'Entity creation timestamp';

COMMENT ON COLUMN core.managed_object.deleted_at IS
    'Soft deletion timestamp (NULL if active)';

DO $$
BEGIN
    RAISE NOTICE '  ✓ core.managed_object - abstract base table with lifecycle tracking';
END $$;


