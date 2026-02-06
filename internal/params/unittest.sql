-- ============================================================================
-- pgmi Unit Test Framework
-- Session-scoped infrastructure for test execution from pgmi_test_plan
-- ============================================================================
-- This script creates helper functions that read from pg_temp.pgmi_test_plan,
-- which is populated by Go with embedded test content.
-- ============================================================================

-- ============================================================================
-- Parameterized View: Filtered Test Execution Plan
-- ============================================================================
CREATE OR REPLACE FUNCTION pg_temp.pgmi_unittest_pvw_plan(
    path_pattern TEXT DEFAULT '.*'
)
RETURNS TABLE(
    ordinal INT,
    step_type TEXT,
    script_path TEXT,
    directory TEXT,
    depth INT,
    pre_exec TEXT,
    script_sql TEXT,
    post_exec TEXT
)
LANGUAGE sql
STABLE
AS $$
    WITH
    matching_tests AS (
        SELECT directory
        FROM pg_temp.pgmi_test_plan
        WHERE step_type = 'test'
          AND script_path ~ path_pattern
    ),
    relevant_directories AS (
        SELECT DISTINCT p.directory
        FROM pg_temp.pgmi_test_plan p
        WHERE p.step_type IN ('fixture', 'teardown')
          AND EXISTS (
              SELECT 1
              FROM matching_tests mt
              WHERE mt.directory LIKE p.directory || '%'
          )
    ),
    filtered_plan AS (
        SELECT
            plan.ordinal AS original_order,
            plan.step_type,
            plan.script_path,
            plan.directory,
            plan.depth,
            plan.pre_exec,
            plan.script_sql,
            plan.post_exec
        FROM pg_temp.pgmi_test_plan plan
        WHERE
            (plan.step_type = 'test' AND plan.script_path ~ path_pattern)
            OR
            (plan.step_type IN ('fixture', 'teardown')
             AND plan.directory IN (SELECT directory FROM relevant_directories))
    )
    SELECT
        ROW_NUMBER() OVER (ORDER BY original_order)::INT AS ordinal,
        step_type,
        script_path,
        directory,
        depth,
        pre_exec,
        script_sql,
        post_exec
    FROM filtered_plan
    ORDER BY original_order;
$$;

COMMENT ON FUNCTION pg_temp.pgmi_unittest_pvw_plan IS
'Returns filtered test execution plan with renumbered ordinal. Includes ancestor fixtures/teardowns.';

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
    v_sql TEXT;
BEGIN
    FOR v_test IN (
        SELECT ordinal, step_type, script_path, pre_exec, script_sql, post_exec
        FROM pg_temp.pgmi_unittest_pvw_plan(path_pattern)
        ORDER BY ordinal
    )
    LOOP
        CASE v_test.step_type
            WHEN 'fixture' THEN
                PERFORM pg_temp.pgmi_plan_notice('Fixture: %s', v_test.script_path);
            WHEN 'test' THEN
                PERFORM pg_temp.pgmi_plan_notice('Testing: %s', v_test.script_path);
            WHEN 'teardown' THEN
                PERFORM pg_temp.pgmi_plan_notice('Teardown: %s', COALESCE(v_test.script_path, v_test.step_type));
        END CASE;

        -- Build combined SQL: pre_exec + script_sql + post_exec
        v_sql := '';
        IF v_test.pre_exec IS NOT NULL THEN
            v_sql := v_sql || v_test.pre_exec || E'\n';
        END IF;
        IF v_test.script_sql IS NOT NULL THEN
            v_sql := v_sql || v_test.script_sql || E'\n';
        END IF;
        IF v_test.post_exec IS NOT NULL THEN
            v_sql := v_sql || v_test.post_exec;
        END IF;

        PERFORM pg_temp.pgmi_plan_command(v_sql);
    END LOOP;
END;
$$;

COMMENT ON FUNCTION pg_temp.pgmi_plan_tests IS
'Schedules unit tests from pgmi_test_plan with optional path filtering. Uses POSIX regex.';
