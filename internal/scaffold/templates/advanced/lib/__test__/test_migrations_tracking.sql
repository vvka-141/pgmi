-- ============================================================================
-- Test: Deployment Tracking Infrastructure
-- ============================================================================
-- Validates that deployment tracking is correctly set up:
--   - Tracking table exists and has correct structure
--   - At least one script was tracked
--   - Role hierarchy is in place
-- ============================================================================

DO $$
DECLARE
    v_table_exists BOOLEAN;
    v_has_tracked_scripts BOOLEAN;
    v_owner_role TEXT;
BEGIN
    RAISE DEBUG '-> Testing deployment tracking infrastructure';

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
    RAISE DEBUG '  Tracking table exists';

    -- Test 2: At least one script was tracked (lib/ files execute before tests)
    SELECT EXISTS (
        SELECT 1 FROM internal.deployment_script_execution_log
        WHERE file_path LIKE './lib/%'
    ) INTO v_has_tracked_scripts;

    IF NOT v_has_tracked_scripts THEN
        RAISE EXCEPTION 'TEST FAILED: No lib/ scripts tracked in deployment log';
    END IF;
    RAISE DEBUG '  Scripts tracked correctly';

    -- Test 3: Owner role exists
    v_owner_role := current_database()::TEXT || '_owner';
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = v_owner_role) THEN
        RAISE EXCEPTION 'TEST FAILED: Owner role % does not exist', v_owner_role;
    END IF;
    RAISE DEBUG '  Owner role exists: %', v_owner_role;

    -- Test 4: Internal schema owned by owner role
    IF (SELECT nspowner::regrole::TEXT FROM pg_namespace WHERE nspname = 'internal') != v_owner_role THEN
        RAISE EXCEPTION 'TEST FAILED: Internal schema not owned by owner role';
    END IF;
    RAISE DEBUG '  Internal schema owned by owner role';

    RAISE DEBUG 'Deployment tracking test passed';
END $$;
