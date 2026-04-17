-- ============================================================================
-- Test: DDL-trigger entity standards
-- ============================================================================
-- Validates that tables declaring object_id core.entity_id get created_at
-- and deleted_at columns injected automatically, and that tables without the
-- marker are left alone (trigger does not touch them).
-- ============================================================================

DO $$
DECLARE
    v_created_at_exists boolean;
    v_deleted_at_exists boolean;
    v_index_count int;
    v_plain_has_created boolean;
BEGIN
    RAISE NOTICE '-> Testing entity standards DDL trigger';

    -- ========================================================================
    -- Opt-in: object_id core.entity_id triggers column injection
    -- ========================================================================

    CREATE TABLE IF NOT EXISTS core.entity_standards_probe (
        object_id core.entity_id PRIMARY KEY DEFAULT gen_random_uuid(),
        label text NOT NULL
    );

    SELECT
        EXISTS (SELECT 1 FROM pg_attribute
                WHERE attrelid = 'core.entity_standards_probe'::regclass
                  AND attname = 'created_at' AND NOT attisdropped),
        EXISTS (SELECT 1 FROM pg_attribute
                WHERE attrelid = 'core.entity_standards_probe'::regclass
                  AND attname = 'deleted_at' AND NOT attisdropped)
    INTO v_created_at_exists, v_deleted_at_exists;

    IF NOT v_created_at_exists THEN
        RAISE EXCEPTION 'created_at not injected by entity standards trigger';
    END IF;
    IF NOT v_deleted_at_exists THEN
        RAISE EXCEPTION 'deleted_at not injected by entity standards trigger';
    END IF;

    RAISE NOTICE '  + created_at and deleted_at injected on object_id core.entity_id table';

    -- ========================================================================
    -- Trigger must NOT create indexes (index strategy stays with the author)
    -- ========================================================================

    SELECT count(*)::int INTO v_index_count
    FROM pg_indexes
    WHERE schemaname = 'core' AND tablename = 'entity_standards_probe';

    IF v_index_count > 1 THEN
        RAISE EXCEPTION 'Trigger created % indexes, expected only the PK', v_index_count;
    END IF;

    RAISE NOTICE '  + Trigger created no indexes beyond the PK (count=%)', v_index_count;

    -- ========================================================================
    -- Opt-out: table without object_id core.entity_id is untouched
    -- ========================================================================

    CREATE TABLE IF NOT EXISTS core.no_standards_probe (
        id serial PRIMARY KEY,
        label text NOT NULL
    );

    SELECT EXISTS (
        SELECT 1 FROM pg_attribute
        WHERE attrelid = 'core.no_standards_probe'::regclass
          AND attname = 'created_at' AND NOT attisdropped
    ) INTO v_plain_has_created;

    IF v_plain_has_created THEN
        RAISE EXCEPTION 'Trigger wrongly injected created_at on table without core.entity_id marker';
    END IF;

    RAISE NOTICE '  + Tables without core.entity_id marker are untouched';

    -- ========================================================================
    -- Idempotency: re-running the trigger is a no-op (guarded by column checks)
    -- ========================================================================

    PERFORM core.apply_entity_table_standards('core.entity_standards_probe'::regclass);
    PERFORM core.apply_entity_table_standards('core.entity_standards_probe'::regclass);

    RAISE NOTICE '  + Repeated apply_entity_table_standards calls are idempotent';

    DROP TABLE core.entity_standards_probe;
    DROP TABLE core.no_standards_probe;

    RAISE NOTICE '✓ Entity standards DDL trigger tests passed';
END $$;
