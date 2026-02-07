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
