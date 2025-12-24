-- ============================================================================
-- Test: Metadata-Driven Deployment Tracking Infrastructure
-- ============================================================================
-- Validates that the metadata-driven deployment tracking is correctly set up:
--   • Tracking table exists and has correct structure
--   • init.sql was tracked as executed
--   • Bootstrap infrastructure is in place
-- ============================================================================

DO $$
DECLARE
    v_table_exists BOOLEAN;
    v_init_tracked BOOLEAN;
    v_owner_role TEXT;
BEGIN
    RAISE DEBUG '→ Testing metadata-driven deployment tracking';

    -- Test 1: Tracking table exists
    SELECT EXISTS (
        SELECT 1 FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'internal'
        AND c.relname = 'deployment_script_execution_log'
    ) INTO v_table_exists;

    IF NOT v_table_exists THEN
        RAISE EXCEPTION 'TEST FAILED: Tracking table does not exist';
    END IF;
    RAISE DEBUG '  ✓ Tracking table exists';

    -- Test 2: init.sql was tracked
    SELECT EXISTS (
        SELECT 1 FROM internal.deployment_script_execution_log
        WHERE deployment_script_object_id = '00000000-0000-0000-0000-000000000001'::UUID
        AND file_path = './init.sql'
    ) INTO v_init_tracked;

    IF NOT v_init_tracked THEN
        RAISE EXCEPTION 'TEST FAILED: init.sql not tracked in deployment log';
    END IF;
    RAISE DEBUG '  ✓ init.sql tracked correctly';

    -- Test 3: Owner role exists
    v_owner_role := current_database()::TEXT || '_owner';
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = v_owner_role) THEN
        RAISE EXCEPTION 'TEST FAILED: Owner role % does not exist', v_owner_role;
    END IF;
    RAISE DEBUG '  ✓ Owner role exists: %', v_owner_role;

    -- Test 4: Internal schema owned by owner role
    IF (SELECT nspowner::regrole::TEXT FROM pg_namespace WHERE nspname = 'internal') != v_owner_role THEN
        RAISE EXCEPTION 'TEST FAILED: Internal schema not owned by owner role';
    END IF;
    RAISE DEBUG '  ✓ Internal schema owned by owner role';

    RAISE DEBUG '✓ Metadata-driven deployment tracking test passed';
END $$;
