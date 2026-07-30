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

    IF v_created_at_exists IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'created_at not injected by entity standards sweep';
    END IF;
    IF v_deleted_at_exists IS DISTINCT FROM true THEN
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

    IF v_plain_has_created IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'Sweep wrongly injected created_at on table without core.entity_id marker';
    END IF;

    RAISE NOTICE '  + Tables without core.entity_id marker are untouched';

    -- ========================================================================
    -- Idempotency: repeated calls are a no-op
    -- ========================================================================
    -- "It did not raise" is not idempotence. The sweep runs on every deploy, so
    -- a re-apply that reset created_at would silently rewrite lifecycle
    -- timestamps on every redeploy of every project built on this template.
    -- Snapshot a real row and the column set, re-apply twice, compare.

    DECLARE
        v_probe_id uuid;
        v_created_before timestamptz;
        v_created_after timestamptz;
        v_columns_before text;
        v_columns_after text;
    BEGIN
        INSERT INTO core.entity_standards_probe (label)
        VALUES ('idempotency probe') RETURNING object_id INTO v_probe_id;

        SELECT created_at INTO STRICT v_created_before
        FROM core.entity_standards_probe WHERE object_id = v_probe_id;

        SELECT string_agg(attname || ':' || atttypid::regtype::text, ',' ORDER BY attnum)
        INTO v_columns_before
        FROM pg_attribute
        WHERE attrelid = 'core.entity_standards_probe'::regclass
          AND attnum > 0 AND NOT attisdropped;

        PERFORM pg_temp.apply_entity_table_standards('core.entity_standards_probe'::regclass);
        PERFORM pg_temp.apply_entity_table_standards('core.entity_standards_probe'::regclass);

        SELECT created_at INTO STRICT v_created_after
        FROM core.entity_standards_probe WHERE object_id = v_probe_id;

        SELECT string_agg(attname || ':' || atttypid::regtype::text, ',' ORDER BY attnum)
        INTO v_columns_after
        FROM pg_attribute
        WHERE attrelid = 'core.entity_standards_probe'::regclass
          AND attnum > 0 AND NOT attisdropped;

        IF v_created_after IS DISTINCT FROM v_created_before THEN
            RAISE EXCEPTION 'apply_entity_table_standards rewrote created_at on re-apply: % -> %',
                v_created_before, v_created_after;
        END IF;
        IF v_columns_after IS DISTINCT FROM v_columns_before THEN
            RAISE EXCEPTION 'apply_entity_table_standards changed the column set on re-apply: % -> %',
                v_columns_before, v_columns_after;
        END IF;

        DELETE FROM core.entity_standards_probe WHERE object_id = v_probe_id;
    END;

    RAISE NOTICE '  + Repeated apply_entity_table_standards calls leave rows and columns untouched';

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
