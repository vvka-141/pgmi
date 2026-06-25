-- ============================================================================
-- Test: Entity standards reconcile (sweep + inline call)
-- ============================================================================
-- Validates that tables declaring object_id core.entity_id get created_at
-- and deleted_at columns injected by the deploy-end sweep, that the inline
-- call path works for collocated indexes, and that tables without the marker
-- are left alone. No superuser required.
-- ============================================================================

DO $$
DECLARE
    v_created_at_exists boolean;
    v_deleted_at_exists boolean;
    v_index_count int;
    v_plain_has_created boolean;
BEGIN
    RAISE NOTICE '-> Testing entity standards reconcile';

    -- ========================================================================
    -- Sweep path: object_id core.entity_id triggers column injection
    -- ========================================================================

    CREATE TABLE IF NOT EXISTS core.entity_standards_probe (
        object_id core.entity_id PRIMARY KEY DEFAULT gen_random_uuid(),
        label text NOT NULL
    );

    PERFORM pg_temp.apply_entity_standards_all();

    SELECT
        EXISTS (SELECT 1 FROM pg_attribute
                WHERE attrelid = 'core.entity_standards_probe'::regclass
                  AND attname = 'created_at' AND NOT attisdropped),
        EXISTS (SELECT 1 FROM pg_attribute
                WHERE attrelid = 'core.entity_standards_probe'::regclass
                  AND attname = 'deleted_at' AND NOT attisdropped)
    INTO v_created_at_exists, v_deleted_at_exists;

    IF NOT v_created_at_exists THEN
        RAISE EXCEPTION 'created_at not injected by entity standards sweep';
    END IF;
    IF NOT v_deleted_at_exists THEN
        RAISE EXCEPTION 'deleted_at not injected by entity standards sweep';
    END IF;

    RAISE NOTICE '  + Sweep: created_at and deleted_at injected on entity table';

    -- ========================================================================
    -- Sweep must NOT create indexes (index strategy stays with the author)
    -- ========================================================================

    SELECT count(*)::int INTO v_index_count
    FROM pg_indexes
    WHERE schemaname = 'core' AND tablename = 'entity_standards_probe';

    IF v_index_count > 1 THEN
        RAISE EXCEPTION 'Sweep created % indexes, expected only the PK', v_index_count;
    END IF;

    RAISE NOTICE '  + Sweep created no indexes beyond the PK (count=%)', v_index_count;

    -- ========================================================================
    -- Opt-out: table without object_id core.entity_id is untouched
    -- ========================================================================

    CREATE TABLE IF NOT EXISTS core.no_standards_probe (
        id serial PRIMARY KEY,
        label text NOT NULL
    );

    PERFORM pg_temp.apply_entity_standards_all();

    SELECT EXISTS (
        SELECT 1 FROM pg_attribute
        WHERE attrelid = 'core.no_standards_probe'::regclass
          AND attname = 'created_at' AND NOT attisdropped
    ) INTO v_plain_has_created;

    IF v_plain_has_created THEN
        RAISE EXCEPTION 'Sweep wrongly injected created_at on table without core.entity_id marker';
    END IF;

    RAISE NOTICE '  + Tables without core.entity_id marker are untouched';

    -- ========================================================================
    -- Idempotency: repeated calls are a no-op
    -- ========================================================================

    PERFORM pg_temp.apply_entity_table_standards('core.entity_standards_probe'::regclass);
    PERFORM pg_temp.apply_entity_table_standards('core.entity_standards_probe'::regclass);

    RAISE NOTICE '  + Repeated apply_entity_table_standards calls are idempotent';

    -- ========================================================================
    -- Inline call path: columns exist immediately for index creation
    -- ========================================================================

    CREATE TABLE core.inline_call_probe (
        object_id core.entity_id PRIMARY KEY DEFAULT gen_random_uuid(),
        label text NOT NULL
    );

    PERFORM pg_temp.apply_entity_table_standards('core.inline_call_probe');

    CREATE INDEX ix_inline_call_probe_soft_delete
        ON core.inline_call_probe(object_id)
        WHERE deleted_at IS NULL;

    RAISE NOTICE '  + Inline call: partial index on deleted_at created in same file';

    -- ========================================================================
    -- Non-superuser: functions live in pg_temp, no event trigger exists
    -- ========================================================================

    IF EXISTS (
        SELECT 1 FROM pg_event_trigger
        WHERE evtname = 'core_entity_table_standards'
    ) THEN
        RAISE EXCEPTION 'core_entity_table_standards event trigger should not exist';
    END IF;

    RAISE NOTICE '  + No event trigger present (non-superuser compatible)';

    DROP TABLE core.inline_call_probe;
    DROP TABLE core.entity_standards_probe;
    DROP TABLE core.no_standards_probe;

    RAISE NOTICE '✓ Entity standards reconcile tests passed';
END $$;
