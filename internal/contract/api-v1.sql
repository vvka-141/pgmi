-- ============================================================================
-- PGMI Session API v1
-- ============================================================================
-- Creates public views and functions for deploy.sql to use.
-- Executed by Go after schema.sql and file loading.
--
-- This file provides the stable public API that deploy.sql depends on.
-- Internal tables (_pgmi_*) may change; these views provide abstraction.
--
-- PUBLIC VIEWS:
--   pgmi_source_view          - Source files (SELECT * FROM _pgmi_source)
--   pgmi_parameter_view       - CLI parameters
--   pgmi_test_source_view     - Test file content
--   pgmi_test_directory_view  - Test directory hierarchy
--   pgmi_source_metadata_view - Parsed <pgmi-meta> blocks
--   pgmi_plan_view            - Execution order with multi-phase support
--
-- PUBLIC FUNCTIONS:
--   pgmi_test_generate()      - Generate SQL for test execution
-- ============================================================================

-- §pgmi_source_view ──────────────────────────────────────────────────────────
CREATE TEMP VIEW pgmi_source_view AS
SELECT * FROM pg_temp._pgmi_source;

COMMENT ON VIEW pg_temp.pgmi_source_view IS
    'Public view of source files. Use this in deploy.sql instead of _pgmi_source.';

GRANT SELECT ON pg_temp.pgmi_source_view TO PUBLIC;


-- §pgmi_parameter_view ───────────────────────────────────────────────────────
CREATE TEMP VIEW pgmi_parameter_view AS
SELECT * FROM pg_temp._pgmi_parameter;

COMMENT ON VIEW pg_temp.pgmi_parameter_view IS
    'Public view of CLI parameters. Use this in deploy.sql instead of _pgmi_parameter.';

GRANT SELECT ON pg_temp.pgmi_parameter_view TO PUBLIC;


-- §pgmi_test_source_view ─────────────────────────────────────────────────────
CREATE TEMP VIEW pgmi_test_source_view AS
SELECT * FROM pg_temp._pgmi_test_source;

COMMENT ON VIEW pg_temp.pgmi_test_source_view IS
    'Public view of test file content. Use this in deploy.sql instead of _pgmi_test_source.';

GRANT SELECT ON pg_temp.pgmi_test_source_view TO PUBLIC;


-- §pgmi_test_directory_view ──────────────────────────────────────────────────
CREATE TEMP VIEW pgmi_test_directory_view AS
SELECT * FROM pg_temp._pgmi_test_directory;

COMMENT ON VIEW pg_temp.pgmi_test_directory_view IS
    'Public view of test directory hierarchy. Use this instead of _pgmi_test_directory.';

GRANT SELECT ON pg_temp.pgmi_test_directory_view TO PUBLIC;


-- §pgmi_source_metadata_view ─────────────────────────────────────────────────
CREATE TEMP VIEW pgmi_source_metadata_view AS
SELECT * FROM pg_temp._pgmi_source_metadata;

COMMENT ON VIEW pg_temp.pgmi_source_metadata_view IS
    'Public view of script metadata. Use this instead of _pgmi_source_metadata.';

GRANT SELECT ON pg_temp.pgmi_source_metadata_view TO PUBLIC;


-- §pgmi_plan_view ────────────────────────────────────────────────────────────
-- Joins: _pgmi_source LEFT JOIN _pgmi_source_metadata
-- Purpose: Provides execution order for deploy.sql to iterate
-- Key behavior: UNNEST(sort_keys) means files with N sort keys appear N times
-- Order: sort_key ASC, path ASC (deterministic)
CREATE OR REPLACE TEMP VIEW pgmi_plan_view AS
SELECT
    -- File identity
    s.path,
    s.content,
    s.pgmi_checksum AS checksum,

    -- Metadata (with fallback for files without metadata)
    -- Fallback uses MD5 hash cast to UUID (built-in, no extension required)
    -- Note: Not RFC 4122 compliant, but consistent with deploy.sql and available during session init
    md5(s.path::bytea)::uuid AS generic_id,
    m.id,  -- NULL for files without metadata
    COALESCE(m.idempotent, true) AS idempotent,
    COALESCE(m.description, '') AS description,

    -- UNNEST sort keys: each key becomes a separate execution entry
    unnested.sort_key,

    -- Assign sequential execution order (deterministic tie-breaking with path)
    ROW_NUMBER() OVER (ORDER BY unnested.sort_key, s.path) AS execution_order

FROM pg_temp._pgmi_source s
LEFT JOIN pg_temp._pgmi_source_metadata m ON s.path = m.path

-- CROSS JOIN LATERAL: For each file, expand sort_keys array
-- If no metadata: use path as fallback sort key
CROSS JOIN LATERAL UNNEST(
    COALESCE(
        NULLIF(m.sort_keys, '{}'),  -- Use metadata sort keys if present
        ARRAY[s.path]               -- Fallback: lexicographic path order
    )
) AS unnested(sort_key)

ORDER BY unnested.sort_key, s.path;

COMMENT ON VIEW pg_temp.pgmi_plan_view IS
    'Execution plan with multi-phase support via UNNEST(sort_keys).
     Files with multiple sort keys execute multiple times at different stages.
     Order: sort_key ASC, path ASC (deterministic).
     Files without metadata use path as sort key (lexicographic order).';

GRANT SELECT ON pg_temp.pgmi_plan_view TO PUBLIC;


-- §pgmi_test_generate ────────────────────────────────────────────────────────
-- Generates SQL for test execution with savepoint isolation.
-- Uses parallel arrays instead of hstore (no extension dependency).
-- Called by Go preprocessor to expand pgmi_test() macro.
CREATE OR REPLACE FUNCTION pg_temp.pgmi_test_generate(
    p_pattern TEXT DEFAULT NULL,
    p_callback TEXT DEFAULT 'pg_temp.pgmi_test_callback'
) RETURNS TEXT
LANGUAGE plpgsql AS $$
DECLARE
    v_sql TEXT := '';
    v_step RECORD;
    v_sp_counter INT := 0;
    v_dir_paths TEXT[] := '{}';
    v_dir_sps TEXT[] := '{}';
    v_test_paths TEXT[] := '{}';
    v_test_sps TEXT[] := '{}';
    v_idx INT;
    v_sp_name TEXT;
    v_tsp_name TEXT;
    v_callback TEXT;
    v_last_ordinal INT := 0;
BEGIN
    v_callback := COALESCE(p_callback, 'pg_temp.pgmi_test_callback');

    -- Suite start
    v_sql := v_sql || format(
        'SELECT %s(ROW(''suite_start'', NULL, '''', 0, 0, NULL)::pg_temp.pgmi_test_event);',
        v_callback
    ) || E'\n';

    FOR v_step IN SELECT * FROM pg_temp.pgmi_test_plan(p_pattern)
    LOOP
        v_last_ordinal := v_step.ordinal;

        CASE v_step.step_type
            WHEN 'fixture' THEN
                -- Create directory savepoint
                v_sp_counter := v_sp_counter + 1;
                v_sp_name := format('__pgmi_d%s__', v_sp_counter);
                v_dir_paths := array_append(v_dir_paths, v_step.directory);
                v_dir_sps := array_append(v_dir_sps, v_sp_name);

                v_sql := v_sql || format('SAVEPOINT %I;', v_sp_name) || E'\n';
                v_sql := v_sql || format(
                    'SELECT %s(ROW(''fixture_start'', %L, %L, %s, %s, NULL)::pg_temp.pgmi_test_event);',
                    v_callback, v_step.script_path, v_step.directory, v_step.depth, v_step.ordinal
                ) || E'\n';
                v_sql := v_sql || format(
                    'DO $__pgmi__$ BEGIN EXECUTE (SELECT content FROM pg_temp._pgmi_test_source WHERE path = %L); END $__pgmi__$;',
                    v_step.script_path
                ) || E'\n';
                v_sql := v_sql || format(
                    'SELECT %s(ROW(''fixture_end'', %L, %L, %s, %s, NULL)::pg_temp.pgmi_test_event);',
                    v_callback, v_step.script_path, v_step.directory, v_step.depth, v_step.ordinal
                ) || E'\n';

            WHEN 'test' THEN
                -- Ensure dir savepoint exists
                v_idx := array_position(v_dir_paths, v_step.directory);
                IF v_idx IS NULL THEN
                    v_sp_counter := v_sp_counter + 1;
                    v_sp_name := format('__pgmi_d%s__', v_sp_counter);
                    v_dir_paths := array_append(v_dir_paths, v_step.directory);
                    v_dir_sps := array_append(v_dir_sps, v_sp_name);
                    v_sql := v_sql || format('SAVEPOINT %I;', v_sp_name) || E'\n';
                END IF;

                -- Ensure test savepoint exists for this directory
                v_idx := array_position(v_test_paths, v_step.directory);
                IF v_idx IS NULL THEN
                    v_sp_counter := v_sp_counter + 1;
                    v_tsp_name := format('__pgmi_t%s__', v_sp_counter);
                    v_test_paths := array_append(v_test_paths, v_step.directory);
                    v_test_sps := array_append(v_test_sps, v_tsp_name);
                    v_sql := v_sql || format('SAVEPOINT %I;', v_tsp_name) || E'\n';
                ELSE
                    v_tsp_name := v_test_sps[v_idx];
                END IF;

                v_sql := v_sql || format(
                    'SELECT %s(ROW(''test_start'', %L, %L, %s, %s, NULL)::pg_temp.pgmi_test_event);',
                    v_callback, v_step.script_path, v_step.directory, v_step.depth, v_step.ordinal
                ) || E'\n';
                v_sql := v_sql || format(
                    'DO $__pgmi__$ BEGIN EXECUTE (SELECT content FROM pg_temp._pgmi_test_source WHERE path = %L); END $__pgmi__$;',
                    v_step.script_path
                ) || E'\n';
                v_sql := v_sql || format(
                    'SELECT %s(ROW(''test_end'', %L, %L, %s, %s, NULL)::pg_temp.pgmi_test_event);',
                    v_callback, v_step.script_path, v_step.directory, v_step.depth, v_step.ordinal
                ) || E'\n';
                v_sql := v_sql || format(
                    'SELECT %s(ROW(''rollback'', %L, %L, %s, %s, NULL)::pg_temp.pgmi_test_event);',
                    v_callback, v_step.script_path, v_step.directory, v_step.depth, v_step.ordinal
                ) || E'\n';
                v_sql := v_sql || format('ROLLBACK TO SAVEPOINT %I;', v_tsp_name) || E'\n';

            WHEN 'teardown' THEN
                v_idx := array_position(v_dir_paths, v_step.directory);
                v_sp_name := CASE WHEN v_idx IS NOT NULL THEN v_dir_sps[v_idx] END;

                v_sql := v_sql || format(
                    'SELECT %s(ROW(''teardown_start'', NULL, %L, %s, %s, NULL)::pg_temp.pgmi_test_event);',
                    v_callback, v_step.directory, v_step.depth, v_step.ordinal
                ) || E'\n';
                IF v_sp_name IS NOT NULL THEN
                    v_sql := v_sql || format('ROLLBACK TO SAVEPOINT %I;', v_sp_name) || E'\n';
                    v_sql := v_sql || format('RELEASE SAVEPOINT %I;', v_sp_name) || E'\n';
                END IF;
                v_sql := v_sql || format(
                    'SELECT %s(ROW(''teardown_end'', NULL, %L, %s, %s, NULL)::pg_temp.pgmi_test_event);',
                    v_callback, v_step.directory, v_step.depth, v_step.ordinal
                ) || E'\n';
        END CASE;
    END LOOP;

    -- Suite end
    v_sql := v_sql || format(
        'SELECT %s(ROW(''suite_end'', NULL, '''', 0, %s, NULL)::pg_temp.pgmi_test_event);',
        v_callback, v_last_ordinal
    );

    RETURN v_sql;
END;
$$;

COMMENT ON FUNCTION pg_temp.pgmi_test_generate IS
'Generates SQL for test execution with savepoint isolation.
Parameters:
  p_pattern  - POSIX regex to filter tests (NULL = all tests)
  p_callback - Function to call for test lifecycle events (default: pgmi_test_callback)
Returns: SQL string ready for EXECUTE that runs the test suite.
Used by: Go preprocessor to expand pgmi_test() macro calls.';
