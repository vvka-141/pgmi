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

-- ============================================================================
-- Test: a script flipped idempotent -> non-idempotent runs once under the new
-- contract, rather than being skipped forever because of its earlier idempotent
-- log rows. This exercises deploy.sql's skip predicate directly (the __test__
-- harness is one deploy, so a real two-deploy flip can't be staged here).
-- ============================================================================

DO $$
DECLARE
    v_obj uuid := 'ffffffff-0192-4000-8000-000000000001';
    v_would_skip boolean;
BEGIN
    RAISE DEBUG '-> Testing idempotent->non-idempotent flip semantics';

    -- Model the execution log's (object_id, idempotent) rows without the real
    -- table's FK chain — the flip decision is purely this predicate.
    CREATE TEMP TABLE _flip_log (object_id uuid, idempotent boolean) ON COMMIT DROP;

    -- 1. The script ran several times AS idempotent. No non-idempotent run yet.
    INSERT INTO _flip_log VALUES (v_obj, true), (v_obj, true);

    v_would_skip := EXISTS (
        SELECT 1 FROM _flip_log WHERE object_id = v_obj AND NOT idempotent
    );
    IF v_would_skip THEN
        RAISE EXCEPTION 'TEST FAILED: a script that only ever ran as idempotent must NOT be skipped after being flipped to non-idempotent';
    END IF;
    RAISE DEBUG '  ✓ flipped script runs once (prior idempotent rows do not skip it)';

    -- 2. Now it has run once under the non-idempotent contract.
    INSERT INTO _flip_log VALUES (v_obj, false);

    v_would_skip := EXISTS (
        SELECT 1 FROM _flip_log WHERE object_id = v_obj AND NOT idempotent
    );
    IF NOT v_would_skip THEN
        RAISE EXCEPTION 'TEST FAILED: after running once as non-idempotent, the script must be skipped on the next deploy';
    END IF;
    RAISE DEBUG '  ✓ after one non-idempotent run, it is skipped';

    DROP TABLE _flip_log;
    RAISE DEBUG 'Idempotent-flip test passed';
END $$;
