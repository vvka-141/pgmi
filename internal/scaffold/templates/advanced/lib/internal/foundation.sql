/*
<pgmi-meta
    id="3e8b4f27-3d72-4dbb-9291-d62e6723bbd3"
    idempotent="true">
  <description>
    Internal schema foundation: test script generation utility
  </description>
  <sortKeys>
    <key>002/000</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION internal.pvw_unittest_script(
    path_pattern TEXT DEFAULT NULL
)
RETURNS TABLE (
    execution_order INT,
    step_type TEXT,
    script_path TEXT,
    content TEXT
)
LANGUAGE sql
STABLE
AS $$
    SELECT s.execution_order, s.step_type, s.script_path, s.content
    FROM internal.unittest_script s
    WHERE path_pattern IS NULL
       OR s.step_type = 'test' AND s.script_path ~ path_pattern
       OR s.step_type IN ('setup', 'teardown') AND EXISTS (
            SELECT 1 FROM internal.unittest_script t
            WHERE t.step_type = 'test'
              AND t.script_path ~ path_pattern
              AND t.script_directory LIKE s.script_directory || '%'
          )
    ORDER BY s.execution_order;
$$;

COMMENT ON FUNCTION internal.pvw_unittest_script IS
'Parameterized view: returns test scripts filtered by path pattern (POSIX regex). Includes ancestor fixtures for matching tests.';

CREATE OR REPLACE FUNCTION internal.generate_test_script(
    path_pattern TEXT DEFAULT NULL
)
RETURNS TEXT
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_script RECORD;
    v_result TEXT := '';
    v_count INT := 0;
BEGIN
    v_result := E'-- Generated Test Script\n';
    v_result := v_result || format('-- Generated: %s', NOW()) || E'\n';
    IF path_pattern IS NOT NULL THEN
        v_result := v_result || format('-- Filter: %s', path_pattern) || E'\n';
    END IF;
    v_result := v_result || E'\nBEGIN;\n\n';

    FOR v_script IN SELECT * FROM internal.pvw_unittest_script(path_pattern)
    LOOP
        v_count := v_count + 1;
        v_result := v_result || format(
            E'DO $__notice__$ BEGIN RAISE NOTICE %L; END $__notice__$;\n',
            format('→ [%s] %s: %s', v_script.execution_order, initcap(v_script.step_type), v_script.script_path)
        );
        v_result := v_result || v_script.content || E'\n\n';
    END LOOP;

    IF v_count = 0 THEN
        v_result := v_result || E'-- No tests found\n\n';
    END IF;

    v_result := v_result || E'ROLLBACK;\n';

    RETURN v_result;
END;
$$;

COMMENT ON FUNCTION internal.generate_test_script IS
'Generates executable test script. Run via: psql -d mydb -tA -c "SELECT internal.generate_test_script();" | psql -d mydb';
