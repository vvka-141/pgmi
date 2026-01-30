-- ============================================================================
-- pgmi Unit Test Framework
-- Session-scoped infrastructure for automatic test discovery and execution
-- ============================================================================
-- This script creates the pg_temp.pgmi_unittest_* objects that pgmi
-- automatically populates during session initialization. Test files are
-- identified by directory pattern: /__test__/
-- NOTE: This pattern must match pgmi.TestDirectoryPattern constant in pkg/pgmi/constants.go
-- ============================================================================

-- ============================================================================
-- Clean up any existing unittest objects
-- ============================================================================
DROP TABLE IF EXISTS pg_temp.pgmi_unittest_script CASCADE;
DROP SEQUENCE IF EXISTS pg_temp.pgmi_unittest_ordinal_seq CASCADE;
DROP SEQUENCE IF EXISTS pg_temp.pgmi_unittest_savepoint_seq CASCADE;

-- ============================================================================
-- Unit Test Script Table
-- ============================================================================
CREATE TEMP TABLE pg_temp.pgmi_unittest_script
(
    path TEXT NOT NULL PRIMARY KEY,
    name TEXT NOT NULL,
    directory TEXT NOT NULL,
    depth INTEGER NOT NULL,
    content TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    is_setup BOOLEAN GENERATED ALWAYS AS (name ~* '^_setup\.p?sql$') STORED,
    CONSTRAINT chk_unittest_path_format CHECK (path ~ '^\./'),
    CONSTRAINT chk_unittest_name_format CHECK (name ~ '[^/]+$'),
    CONSTRAINT chk_unittest_directory_format CHECK (directory ~ '^\./(?:[^/]+/)*$'),
    CONSTRAINT chk_unittest_depth CHECK (depth >= 0),
    CONSTRAINT chk_unittest_size_bytes CHECK (size_bytes >= 0),
    CONSTRAINT chk_unittest_path_directory_match CHECK (path = directory || name),
    CONSTRAINT chk_unittest_name_not_empty CHECK (name != ''),
    CONSTRAINT chk_unittest_directory_ends_slash CHECK (directory ~ '/$')
);

COMMENT ON TABLE pg_temp.pgmi_unittest_script IS
'Stores unit test scripts with metadata for execution planning. Automatically populated by pgmi from files in __test__/ directories.';

-- Allow access from any role context
GRANT SELECT ON TABLE pg_temp.pgmi_unittest_script TO PUBLIC;

COMMENT ON COLUMN pg_temp.pgmi_unittest_script.path IS
'Full path to script file (e.g., ./tests/users/test-001.sql)';

COMMENT ON COLUMN pg_temp.pgmi_unittest_script.name IS
'Filename without directory (e.g., test-001.sql)';

COMMENT ON COLUMN pg_temp.pgmi_unittest_script.directory IS
'Directory path ending with slash (e.g., ./tests/users/)';

COMMENT ON COLUMN pg_temp.pgmi_unittest_script.depth IS
'Directory nesting level (0 for root)';

COMMENT ON COLUMN pg_temp.pgmi_unittest_script.content IS
'SQL script content to be executed';

COMMENT ON COLUMN pg_temp.pgmi_unittest_script.size_bytes IS
'Size of the script content in bytes';

COMMENT ON COLUMN pg_temp.pgmi_unittest_script.is_setup IS
'True if this is a setup script (_setup.sql)';

-- ============================================================================
-- Enhanced Directory-Level Aggregation View
-- ============================================================================
CREATE OR REPLACE TEMP VIEW pg_temp.pgmi_unittest_vw_directory AS
WITH directory_base AS (
    SELECT DISTINCT directory, depth
    FROM pg_temp.pgmi_unittest_script
),
min_depth AS (
    SELECT MIN(depth) AS root_depth
    FROM directory_base
)
SELECT
    d.directory AS directory_path,
    d.depth AS directory_depth,
    -- Hierarchy metadata
    d.depth = md.root_depth AS is_root_directory,
    (
        SELECT db.directory
        FROM directory_base db
        WHERE d.directory LIKE db.directory || '%'
          AND db.directory != d.directory
          AND db.depth = d.depth - 1
        LIMIT 1
    ) AS parent_directory_path,
    -- Subdirectories
    ARRAY(
        SELECT DISTINCT us.directory
        FROM pg_temp.pgmi_unittest_script us
        WHERE us.directory LIKE d.directory || '%'
          AND us.directory != d.directory
          AND us.depth = d.depth + 1
        ORDER BY us.directory
    ) AS immediate_subdirectories,
    -- Setup script information
    (
        SELECT us.path
        FROM pg_temp.pgmi_unittest_script us
        WHERE us.directory = d.directory
          AND us.is_setup = true
        LIMIT 1
    ) AS setup_script_path,
    EXISTS(
        SELECT 1
        FROM pg_temp.pgmi_unittest_script us
        WHERE us.directory = d.directory
          AND us.is_setup = true
    ) AS has_setup,
    -- Test scripts
    ARRAY(
        SELECT us.path
        FROM pg_temp.pgmi_unittest_script us
        WHERE us.directory = d.directory
          AND us.is_setup = false
        ORDER BY us.path
    ) AS test_script_paths,
    (
        SELECT COUNT(*)::int
        FROM pg_temp.pgmi_unittest_script us
        WHERE us.directory = d.directory
          AND us.is_setup = false
    ) AS test_script_count,
    -- Subdirectory count
    (
        SELECT COUNT(DISTINCT us.directory)::int
        FROM pg_temp.pgmi_unittest_script us
        WHERE us.directory LIKE d.directory || '%'
          AND us.directory != d.directory
          AND us.depth = d.depth + 1
    ) AS immediate_subdirectory_count
FROM directory_base d
CROSS JOIN min_depth md;

COMMENT ON VIEW pg_temp.pgmi_unittest_vw_directory IS
'Aggregates directory-level metadata for unit test execution planning with hierarchy information';

-- ============================================================================
-- Sequences for Ordinals and Savepoints
-- ============================================================================
CREATE TEMP SEQUENCE pg_temp.pgmi_unittest_ordinal_seq;
CREATE TEMP SEQUENCE pg_temp.pgmi_unittest_savepoint_seq;

COMMENT ON SEQUENCE pg_temp.pgmi_unittest_ordinal_seq IS
'Generates sequential execution order numbers for test steps';

COMMENT ON SEQUENCE pg_temp.pgmi_unittest_savepoint_seq IS
'Generates unique IDs for savepoint names';

-- ============================================================================
-- Helper Function: Generate Unique Savepoint IDs
-- ============================================================================
CREATE OR REPLACE FUNCTION pg_temp.pgmi_unittest_generate_savepoint_id(
    context_path text,
    savepoint_category text DEFAULT NULL
)
RETURNS text
LANGUAGE sql
AS $$
    SELECT format(
        'sp_%s_%s%s',
        lpad(nextval('pg_temp.pgmi_unittest_savepoint_seq')::text, 4, '0'),
        left(md5(context_path), 6),
        CASE
            WHEN savepoint_category IS NOT NULL
            THEN '_' || savepoint_category
            ELSE ''
        END
    );
$$;

COMMENT ON FUNCTION pg_temp.pgmi_unittest_generate_savepoint_id IS
'Generates unique, traceable savepoint IDs using sequence and path hash. Format: sp_0001_a1b2c3_fixture';

-- ============================================================================
-- Helper Function: Execute Test Script by Path
-- ============================================================================
CREATE OR REPLACE FUNCTION pg_temp.pgmi_unittest_script_exec(
    script_path text
)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    v_content text;
BEGIN
    -- Retrieve script content
    SELECT content INTO STRICT v_content
    FROM pg_temp.pgmi_unittest_script
    WHERE path = script_path;

    -- Execute the script
    EXECUTE v_content;

EXCEPTION
    WHEN NO_DATA_FOUND THEN
        RAISE EXCEPTION 'Test script not found: %', script_path;
    WHEN OTHERS THEN
        RAISE EXCEPTION 'Error executing test script %: %', script_path, SQLERRM;
END;
$$;

COMMENT ON FUNCTION pg_temp.pgmi_unittest_script_exec IS
'Executes a test script by retrieving its content from pg_temp.pgmi_unittest_script table';

-- ============================================================================
-- Helper Function: Reset All Sequences Before Test Execution
-- ============================================================================
CREATE OR REPLACE FUNCTION pg_temp.pgmi_unittest_reset_sequences()
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    ALTER SEQUENCE pg_temp.pgmi_unittest_ordinal_seq RESTART WITH 1;
    ALTER SEQUENCE pg_temp.pgmi_unittest_savepoint_seq RESTART WITH 1;
END;
$$;

COMMENT ON FUNCTION pg_temp.pgmi_unittest_reset_sequences IS
'Resets ordinal and savepoint sequences before generating a new test execution plan';

-- ============================================================================
-- Main Function: Generate Test Execution Plan
-- ============================================================================
CREATE OR REPLACE FUNCTION pg_temp.pgmi_unittest_pvw_script(
    root_directory text DEFAULT NULL
)
RETURNS TABLE(
    execution_order int,
    step_type text,
    script_path text,
    script_directory text,
    savepoint_id text,
    executable_sql text
)
LANGUAGE plpgsql
AS $$
DECLARE
    v_root_dir text;
    v_dir_info record;
    v_test_path text;
    v_subdir text;
    v_setup_savepoint text;
    v_test_savepoint text;
BEGIN
    -- ========================================================================
    -- STEP 1: Handle NULL root - find all root directories and recurse
    -- ========================================================================
    IF root_directory IS NULL THEN
        FOR v_root_dir IN
            SELECT directory_path
            FROM pg_temp.pgmi_unittest_vw_directory
            WHERE is_root_directory = true
            ORDER BY directory_path
        LOOP
            RETURN QUERY
            SELECT * FROM pg_temp.pgmi_unittest_pvw_script(v_root_dir);
        END LOOP;

        RETURN;
    END IF;

    -- ========================================================================
    -- STEP 2: Get directory information from view
    -- ========================================================================
    SELECT * INTO v_dir_info
    FROM pg_temp.pgmi_unittest_vw_directory
    WHERE directory_path = root_directory;

    -- If directory doesn't exist, return empty
    IF NOT FOUND THEN
        RETURN;
    END IF;

    -- ========================================================================
    -- STEP 3: If setup script exists, yield 'setup' record
    -- ========================================================================
    IF v_dir_info.has_setup THEN
        v_setup_savepoint := pg_temp.pgmi_unittest_generate_savepoint_id(
            v_dir_info.setup_script_path,
            'setup'
        );

        RETURN QUERY
        SELECT
            nextval('pg_temp.pgmi_unittest_ordinal_seq')::int,
            'setup'::text,
            v_dir_info.setup_script_path,
            root_directory,
            v_setup_savepoint,
            format(
                'SAVEPOINT %I; SELECT pg_temp.pgmi_unittest_script_exec(%L);',
                v_setup_savepoint,
                v_dir_info.setup_script_path
            );
    END IF;

    -- ========================================================================
    -- STEP 4: Yield 'test' records for all test scripts in directory
    -- ========================================================================
    IF v_dir_info.test_script_count > 0 THEN
        FOREACH v_test_path IN ARRAY v_dir_info.test_script_paths
        LOOP
            v_test_savepoint := pg_temp.pgmi_unittest_generate_savepoint_id(v_test_path, 'test');

            RETURN QUERY
            SELECT
                nextval('pg_temp.pgmi_unittest_ordinal_seq')::int,
                'test'::text,
                v_test_path,
                root_directory,
                v_test_savepoint,
                format(
                    'SAVEPOINT %I; SELECT pg_temp.pgmi_unittest_script_exec(%L); ROLLBACK TO SAVEPOINT %I;',
                    v_test_savepoint,
                    v_test_path,
                    v_test_savepoint
                );
        END LOOP;
    END IF;

    -- ========================================================================
    -- STEP 5: Recursively process immediate subdirectories
    -- ========================================================================
    IF v_dir_info.immediate_subdirectory_count > 0 THEN
        FOREACH v_subdir IN ARRAY v_dir_info.immediate_subdirectories
        LOOP
            RETURN QUERY
            SELECT * FROM pg_temp.pgmi_unittest_pvw_script(v_subdir);
        END LOOP;
    END IF;

    -- ========================================================================
    -- STEP 6: If setup script exists, yield 'teardown' record
    -- ========================================================================
    IF v_dir_info.has_setup THEN
        RETURN QUERY
        SELECT
            nextval('pg_temp.pgmi_unittest_ordinal_seq')::int,
            'teardown'::text,
            v_dir_info.setup_script_path,
            root_directory,
            v_setup_savepoint,
            format(
                'ROLLBACK TO SAVEPOINT %I;',
                v_setup_savepoint
            );
    END IF;

    RETURN;
END;
$$;

COMMENT ON FUNCTION pg_temp.pgmi_unittest_pvw_script IS
'Generates ordered execution plan for unit tests with setup/teardown lifecycle management';

-- ============================================================================
-- Populate Test Scripts from pgmi_source
-- ============================================================================
-- Move test files from pg_temp.pgmi_source into pg_temp.pgmi_unittest_script
-- Test files are identified by directory pattern: /__test__/
-- NOTE: This pattern must match pgmi.TestDirectoryPattern constant in pkg/pgmi/constants.go
INSERT INTO pg_temp.pgmi_unittest_script (path, name, directory, depth, content, size_bytes)
SELECT path, name, directory, depth, content, size_bytes
FROM pg_temp.pgmi_source
WHERE is_sql_file
  AND directory ~ '/__tests?__/'
ORDER BY path;

-- Remove test files from pgmi_source to prevent accidental execution during deployment
DELETE FROM pg_temp.pgmi_source
WHERE is_sql_file
  AND directory ~ '/__tests?__/';

-- ============================================================================
-- Materialize Unittest Execution Plan
-- ============================================================================
-- Reset sequences before generating the plan
SELECT pg_temp.pgmi_unittest_reset_sequences();

-- Create the materialized execution plan table with correct ordering
-- Embed actual SQL content instead of relying on pgmi_unittest_script_exec()
CREATE TEMPORARY TABLE pg_temp.pgmi_unittest_plan AS
SELECT
    p.execution_order,
    p.step_type,
    p.script_path,
    p.script_directory,
    p.savepoint_id,
    -- For setup and test steps, replace the script_exec() call with embedded content
    CASE
        WHEN p.step_type = 'setup' THEN
            format('SAVEPOINT %I; %s', p.savepoint_id, s.content)
        WHEN p.step_type = 'test' THEN
            format('SAVEPOINT %I; %s ROLLBACK TO SAVEPOINT %I;', p.savepoint_id, s.content, p.savepoint_id)
        ELSE
            -- teardown stays as-is (just the savepoint rollback)
            p.executable_sql
    END AS executable_sql
FROM pg_temp.pgmi_unittest_pvw_script() p
LEFT JOIN pg_temp.pgmi_unittest_script s ON p.script_path = s.path
ORDER BY p.execution_order;

-- Create index for efficient sequential access
CREATE INDEX idx_pgmi_unittest_plan_order
ON pg_temp.pgmi_unittest_plan(execution_order);

-- Drop the raw table to prevent misuse
DROP TABLE IF EXISTS pg_temp.pgmi_unittest_script CASCADE;

COMMENT ON TABLE pg_temp.pgmi_unittest_plan IS
'Materialized unittest execution plan with correct ordering from directory traversal. Use this table (NOT pgmi_unittest_script) for test execution.';

-- Allow access from any role context
GRANT SELECT ON TABLE pg_temp.pgmi_unittest_plan TO PUBLIC;

-- ============================================================================
-- Parameterized View: Filtered Unittest Execution Plan
-- ============================================================================
CREATE OR REPLACE FUNCTION pg_temp.pgmi_unittest_pvw_plan(
    path_pattern TEXT DEFAULT '.*'
)
RETURNS TABLE(
    execution_order INT,
    step_type TEXT,
    script_path TEXT,
    script_directory TEXT,
    savepoint_id TEXT,
    executable_sql TEXT
)
LANGUAGE sql
STABLE
AS $$
    WITH
    -- Find all tests matching the pattern
    matching_tests AS (
        SELECT script_directory
        FROM pg_temp.pgmi_unittest_plan
        WHERE step_type = 'test'
          AND script_path ~ path_pattern
    ),
    -- Find all directories that are ancestors of (or equal to) matching test directories
    relevant_directories AS (
        SELECT DISTINCT p.script_directory
        FROM pg_temp.pgmi_unittest_plan p
        WHERE p.step_type IN ('setup', 'teardown')
          AND EXISTS (
              SELECT 1
              FROM matching_tests mt
              WHERE mt.script_directory LIKE p.script_directory || '%'
          )
    ),
    -- Filter plan to include only relevant scripts
    filtered_plan AS (
        SELECT
            plan.execution_order AS original_order,
            plan.step_type,
            plan.script_path,
            plan.script_directory,
            plan.savepoint_id,
            plan.executable_sql
        FROM pg_temp.pgmi_unittest_plan plan
        WHERE
            -- Include matching tests
            (plan.step_type = 'test' AND plan.script_path ~ path_pattern)
            OR
            -- Include setup/teardown from relevant directories
            (plan.step_type IN ('setup', 'teardown')
             AND plan.script_directory IN (SELECT script_directory FROM relevant_directories))
    )
    -- Return with renumbered execution_order (1,2,3,...)
    SELECT
        ROW_NUMBER() OVER (ORDER BY original_order)::INT AS execution_order,
        step_type,
        script_path,
        script_directory,
        savepoint_id,
        executable_sql
    FROM filtered_plan
    ORDER BY original_order;
$$;

COMMENT ON FUNCTION pg_temp.pgmi_unittest_pvw_plan IS
'Returns filtered unittest execution plan based on path pattern with renumbered execution order (1,2,3,...). Automatically includes ancestor setup/teardown scripts required by matching tests. Use this view for custom test execution workflows or query it to preview which tests will run.';

-- ============================================================================
-- Helper Function: Execute Tests with Pattern Filtering
-- ============================================================================
CREATE OR REPLACE FUNCTION pg_temp.pgmi_plan_tests(
    path_pattern TEXT DEFAULT '.*'
)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    v_test RECORD;
BEGIN
    FOR v_test IN (
        SELECT execution_order, step_type, script_path, executable_sql
        FROM pg_temp.pgmi_unittest_pvw_plan(path_pattern)
        ORDER BY execution_order
    )
    LOOP
        CASE v_test.step_type
            WHEN 'setup' THEN
                PERFORM pg_temp.pgmi_plan_notice('Setup: %s', v_test.script_path);
            WHEN 'test' THEN
                PERFORM pg_temp.pgmi_plan_notice('Testing: %s', v_test.script_path);
            WHEN 'teardown' THEN
                PERFORM pg_temp.pgmi_plan_notice('Teardown: %s', v_test.script_path);
        END CASE;

        PERFORM pg_temp.pgmi_plan_command(v_test.executable_sql);
    END LOOP;
END;
$$;

COMMENT ON FUNCTION pg_temp.pgmi_plan_tests IS
'Executes unit tests from pgmi_unittest_plan with optional path filtering using POSIX regular expressions (~ operator). Default pattern ''.*'' matches all tests. Examples: pgmi_plan_tests() runs all tests, pgmi_plan_tests(''.*/pre-deployment/.*'') runs only pre-deployment tests, pgmi_plan_tests(''.*_(integration|e2e)\\.sql$'') runs integration/E2E tests. Setup/teardown scripts automatically execute for matching test directories.';
