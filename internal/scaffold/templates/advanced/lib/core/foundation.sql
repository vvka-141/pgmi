/*
<pgmi-meta
    id="85c16de9-a1cc-491b-88e6-4db887f684c8"
    idempotent="true">
  <description>
    Core schema foundation: entity hierarchy with identity and lifecycle management
  </description>
  <sortKeys>
    <key>003/000</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing entity hierarchy'; END $$;

-- ============================================================================
-- Entity Base Table (Identity Only)
-- ============================================================================

CREATE TABLE IF NOT EXISTS core.entity (
    object_id UUID PRIMARY KEY DEFAULT extensions.gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT is_abstract CHECK (false) NO INHERIT
);

COMMENT ON TABLE core.entity IS
    'Abstract base: provides object identity and creation timestamp. Parent for all entities.';

COMMENT ON COLUMN core.entity.object_id IS
    'Unique identifier for the entity';

COMMENT ON COLUMN core.entity.created_at IS
    'Entity creation timestamp';

-- ============================================================================
-- Managed Entity (Adds Soft-Delete Lifecycle)
-- ============================================================================

CREATE TABLE IF NOT EXISTS core.managed_entity (
    deleted_at TIMESTAMPTZ,

    CONSTRAINT is_abstract CHECK (false) NO INHERIT
) INHERITS (core.entity);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'core.managed_entity'::regclass
        AND contype = 'p'
    ) THEN
        ALTER TABLE core.managed_entity ADD PRIMARY KEY (object_id);
    END IF;
END $$;

COMMENT ON TABLE core.managed_entity IS
    'Abstract base for managed entities with soft-delete lifecycle. Inherits identity from core.entity.';

COMMENT ON COLUMN core.managed_entity.deleted_at IS
    'Soft deletion timestamp (NULL if active). Garbage collection cleans marked entries.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ core.entity - abstract base with identity';
    RAISE NOTICE '  ✓ core.managed_entity - abstract base with soft-delete lifecycle';
END $$;
