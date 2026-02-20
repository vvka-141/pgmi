-- ============================================================================
-- pgmi Session Foundation Tests
-- ============================================================================
-- Validates complete session initialization including:
-- - File loading into pg_temp.pgmi_source_view
-- - Test file separation into pg_temp.pgmi_test_source (dropped after materialization)
-- - Execution plan materialization in pg_temp.pgmi_test_plan
-- - Multi-level directory traversal correctness
-- ============================================================================

DO $$
BEGIN
    RAISE NOTICE '========================================';
    RAISE NOTICE 'Session Foundation Tests';
    RAISE NOTICE '========================================';
END $$;

-- TEST 1: File Separation (pgmi_source_view should have NO test files)
DO $$
DECLARE
    v_test_files_in_source INT;
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE 'TEST 1: File Separation (pgmi_source vs pgmi_test_plan)';

    -- Check: pgmi_source should have NO test files (they should have been moved to unittest tables)
    SELECT COUNT(*) INTO v_test_files_in_source
    FROM pg_temp.pgmi_source_view
    WHERE directory ~ '/__test__/';

    IF v_test_files_in_source > 0 THEN
        RAISE EXCEPTION '✗ FAILED: Found % test files in pgmi_source (should be 0)', v_test_files_in_source;
    END IF;

    RAISE NOTICE '✓ PASSED: File separation correct (no test files in pgmi_source)';
END $$;

-- TEST 2: File Counts Match Expectations
DO $$
DECLARE
    v_regular_file_count INT;
    v_test_step_count INT;
    v_expected_regular INT := 3; -- 2 migrations + 1 setup file (deploy.sql excluded)
    v_expected_test_steps INT := 6;  -- 2 _setup + 4 test files
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE 'TEST 2: File Counts';

    -- Count regular files (excluding deploy.sql which is not in pgmi_source)
    SELECT COUNT(*) INTO v_regular_file_count
    FROM pg_temp.pgmi_source_view;

    -- Count test steps in unittest plan (excludes teardown steps)
    SELECT COUNT(*) INTO v_test_step_count
    FROM pg_temp.pgmi_test_plan()
    WHERE step_type IN ('fixture', 'test');

    IF v_regular_file_count != v_expected_regular THEN
        RAISE EXCEPTION '✗ FAILED: Expected % regular files, found %', v_expected_regular, v_regular_file_count;
    END IF;

    IF v_test_step_count != v_expected_test_steps THEN
        RAISE EXCEPTION '✗ FAILED: Expected % test steps (setup+test), found %', v_expected_test_steps, v_test_step_count;
    END IF;

    RAISE NOTICE '✓ PASSED: File counts correct (% regular, % test steps)', v_regular_file_count, v_test_step_count;
END $$;

-- TEST 3: Path Normalization
DO $$
DECLARE
    v_invalid_paths INT;
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE 'TEST 3: Path Normalization';

    -- Check: All paths should use Unix-style separators and start with ./
    SELECT COUNT(*) INTO v_invalid_paths
    FROM pg_temp.pgmi_source_view
    WHERE path !~ '^\./';

    IF v_invalid_paths > 0 THEN
        RAISE EXCEPTION '✗ FAILED: Found % files with invalid path format', v_invalid_paths;
    END IF;

    RAISE NOTICE '✓ PASSED: All paths correctly normalized';
END $$;

-- TEST 4: Depth Calculation
DO $$
DECLARE
    v_invalid_depth INT;
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE 'TEST 4: Depth Calculation';

    -- Verify depth matches directory structure
    -- migrations/*.sql should have depth=1
    -- setup/*.sql should have depth=1
    SELECT COUNT(*) INTO v_invalid_depth
    FROM pg_temp.pgmi_source_view
    WHERE (directory = './migrations/' AND depth != 1)
       OR (directory = './setup/' AND depth != 1);

    IF v_invalid_depth > 0 THEN
        RAISE EXCEPTION '✗ FAILED: Found % files with incorrect depth', v_invalid_depth;
    END IF;

    RAISE NOTICE '✓ PASSED: Depth calculations correct';
END $$;

-- TEST 5: Test File Detection (directory pattern matching)
DO $$
DECLARE
    v_test_steps INT;
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE 'TEST 5: Test File Detection';

    -- All items in unittest_plan should have __test__ in their path
    SELECT COUNT(*) INTO v_test_steps
    FROM pg_temp.pgmi_test_plan()
    WHERE directory !~ '/__test__/';

    IF v_test_steps > 0 THEN
        RAISE EXCEPTION '✗ FAILED: Found % unittest plan entries without /__test__/ in path', v_test_steps;
    END IF;

    RAISE NOTICE '✓ PASSED: Test file detection correct';
END $$;

-- TEST 6: Setup File Detection
DO $$
DECLARE
    v_setup_count INT;
    v_expected_setup INT := 2; -- __test__/_setup.sql + __test__/auth/_setup.sql
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE 'TEST 6: Setup File Detection';

    -- Count setup steps in plan
    SELECT COUNT(*) INTO v_setup_count
    FROM pg_temp.pgmi_test_plan()
    WHERE step_type = 'fixture';

    IF v_setup_count != v_expected_setup THEN
        RAISE EXCEPTION '✗ FAILED: Expected % setup files, found %', v_expected_setup, v_setup_count;
    END IF;

    RAISE NOTICE '✓ PASSED: Setup file detection correct (found %)', v_setup_count;
END $$;

-- TEST 7: Multi-Level Execution Order
DO $$
DECLARE
    v_ordinal TEXT[];
    v_expected_order TEXT[];
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE 'TEST 7: Multi-Level Execution Order';

    -- Get actual execution order (only setup and test, not teardown)
    SELECT array_agg(script_path ORDER BY ordinal)
    INTO v_ordinal
    FROM pg_temp.pgmi_test_plan()
    WHERE step_type IN ('fixture', 'test');

    -- Expected order based on directory traversal (depth-first)
    v_expected_order := ARRAY[
        './__test__/_setup.sql',                     -- Level 0 setup
        './__test__/test_basic.sql',                 -- Level 0 test
        './__test__/auth/_setup.sql',                -- Level 1 setup
        './__test__/auth/test_login.sql',            -- Level 1 test
        './__test__/auth/oauth/test_google.sql',     -- Level 2 test
        './__test__/billing/test_stripe.sql'         -- Level 1 test (billing sibling)
    ];

    IF v_ordinal != v_expected_order THEN
        RAISE EXCEPTION E'✗ FAILED: Execution order incorrect\nExpected: %\nGot: %',
            array_to_string(v_expected_order, ', '),
            array_to_string(v_ordinal, ', ');
    END IF;

    RAISE NOTICE '✓ PASSED: Multi-level execution order correct';
END $$;

-- TEST 8: Teardown Structure
-- Every directory with tests gets a teardown (to clean up entry savepoints)
DO $$
DECLARE
    v_fixture_count INT;
    v_teardown_count INT;
    v_directories_with_tests INT;
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE 'TEST 8: Teardown Structure';

    -- Count fixtures and teardowns
    SELECT
        COUNT(*) FILTER (WHERE step_type = 'fixture'),
        COUNT(*) FILTER (WHERE step_type = 'teardown')
    INTO v_fixture_count, v_teardown_count
    FROM pg_temp.pgmi_test_plan();

    -- Count unique directories with tests
    SELECT COUNT(DISTINCT directory)
    INTO v_directories_with_tests
    FROM pg_temp.pgmi_test_plan()
    WHERE step_type = 'test';

    -- Every directory with tests should have a teardown
    IF v_teardown_count != v_directories_with_tests THEN
        RAISE EXCEPTION '✗ FAILED: Expected % teardowns (one per directory with tests), got %',
            v_directories_with_tests, v_teardown_count;
    END IF;

    -- Verify each fixture has corresponding teardown with same directory
    IF EXISTS (
        SELECT 1
        FROM pg_temp.pgmi_test_plan() fixture_step
        WHERE fixture_step.step_type = 'fixture'
          AND NOT EXISTS (
              SELECT 1
              FROM pg_temp.pgmi_test_plan() teardown_step
              WHERE teardown_step.step_type = 'teardown'
                AND teardown_step.directory = fixture_step.directory
          )
    ) THEN
        RAISE EXCEPTION '✗ FAILED: Found fixture without matching teardown';
    END IF;

    RAISE NOTICE '✓ PASSED: Teardown structure correct (% fixtures, % teardowns, % test directories)',
        v_fixture_count, v_teardown_count, v_directories_with_tests;
END $$;

-- TEST 9: Checksum Integrity
DO $$
DECLARE
    v_invalid_checksums INT;
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE 'TEST 9: Checksum Integrity';

    -- Verify checksums are valid hex strings
    SELECT COUNT(*) INTO v_invalid_checksums
    FROM pg_temp.pgmi_source_view
    WHERE checksum !~ '^[a-fA-F0-9]{32,64}$'
       OR pgmi_checksum !~ '^[a-fA-F0-9]{32,64}$';

    IF v_invalid_checksums > 0 THEN
        RAISE EXCEPTION '✗ FAILED: Found % files with invalid checksums', v_invalid_checksums;
    END IF;

    RAISE NOTICE '✓ PASSED: Checksum integrity verified';
END $$;

-- TEST 10: Constraint Validation
DO $$
DECLARE
    v_constraint_violations INT := 0;
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE 'TEST 10: Constraint Validation';

    -- This test verifies that all pgmi_source constraints are satisfied
    -- If we got this far without errors, all constraints passed
    -- Let's explicitly verify a few critical ones

    -- Verify path = directory || name
    SELECT COUNT(*) INTO v_constraint_violations
    FROM pg_temp.pgmi_source_view
    WHERE path != directory || name;

    IF v_constraint_violations > 0 THEN
        RAISE EXCEPTION '✗ FAILED: Found % files with path != directory || name', v_constraint_violations;
    END IF;

    -- Verify content size matches size_bytes
    SELECT COUNT(*) INTO v_constraint_violations
    FROM pg_temp.pgmi_source_view
    WHERE octet_length(content) != size_bytes;

    IF v_constraint_violations > 0 THEN
        RAISE EXCEPTION '✗ FAILED: Found % files with content size mismatch', v_constraint_violations;
    END IF;

    RAISE NOTICE '✓ PASSED: All pgmi_source constraints satisfied';
END $$;

-- Final Summary
DO $$
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE '========================================';
    RAISE NOTICE '✓ ALL TESTS PASSED';
    RAISE NOTICE '========================================';
END $$;
