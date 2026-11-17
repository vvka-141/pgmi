-- ============================================================================
-- Option handler_reads_views: handlers read the semantic layer, not tables
-- ============================================================================
-- Off by default; switched on in pg_temp.guard_enabled() in deploy/settings.sql.
-- The domain lives in core: tables hold the facts, views (core.vw_*) are what
-- the application reads, kernels (core.fn_*) are how it writes. A handler that
-- names a core table couples the API to storage, so a registered handler body
-- naming one fails the deploy. A declaration that only holds the row a kernel
-- returns (v_task core.task; v_task core.task%ROWTYPE) is not a read, and neither
-- is a string literal such as the problem code 'core.task.changed'.
-- ============================================================================

CREATE FUNCTION pg_temp.handler_reads_views_violation()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT DISTINCT format('%s reads core.%s', h.handler_function_name, t.relname)
    FROM api.handler h
    JOIN pg_proc p ON p.oid = h.handler_func
    CROSS JOIN LATERAL (
        SELECT regexp_replace(
                   regexp_replace(
                       regexp_replace(p.prosrc, '''(?:[^'']|'''')*''', '''''', 'g'),
                       '\mcore\.[a-z_][a-z0-9_]*(%ROWTYPE|\[\])', '', 'gi'),
                   '\m[a-z_][a-z0-9_]*\s+core\.[a-z_][a-z0-9_]*\s*(;|:=)', '', 'gi') AS src
    ) b
    JOIN pg_class t ON t.relnamespace = 'core'::regnamespace AND t.relkind IN ('r', 'p') AND NOT t.relispartition
    WHERE h.deleted_at IS NULL
      AND b.src ~* ('\mcore\.' || t.relname || '\M')
$$;

DO $$
BEGIN
    IF pg_temp.guard_enabled('handler_reads_views') THEN
        PERFORM pg_temp.enforce_guard('handler_reads_views', ARRAY(SELECT pg_temp.handler_reads_views_violation()));
    END IF;
END $$;

DO $$
DECLARE
    v_found text;
BEGIN
    CREATE TABLE core.reads_probe (id int);
    CREATE VIEW core.vw_reads_probe WITH (security_invoker = true) AS SELECT id FROM core.reads_probe;
    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'ffffffff-0451-4000-8000-000000000011', 'path', '/test-reads-table', 'httpMethod', '^GET$',
        'name', 'reads_table_probe', 'access', 'public', 'outputSchema', jsonb_build_object('type', 'object')),
        $b$BEGIN
            RETURN api.json_response(200, jsonb_build_object('n', (SELECT count(*) FROM core.reads_probe)));
        END;$b$);
    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'ffffffff-0451-4000-8000-000000000012', 'path', '/test-reads-view', 'httpMethod', '^GET$',
        'name', 'reads_view_probe', 'access', 'public', 'outputSchema', jsonb_build_object('type', 'object')),
        $b$DECLARE
            v_row core.reads_probe;
        BEGIN
            IF false THEN
                RETURN api.problem_response(409, 'Conflict', 'x', code => 'core.reads_probe.changed');
            END IF;
            RETURN api.json_response(200, jsonb_build_object('n', (SELECT count(*) FROM core.vw_reads_probe)));
        END;$b$);
    SELECT string_agg(v, '; ') INTO v_found FROM pg_temp.handler_reads_views_violation() v WHERE v LIKE '%probe%';
    IF v_found IS DISTINCT FROM 'reads_table_probe reads core.reads_probe' THEN
        RAISE EXCEPTION 'TEST FAILED: handler_reads_views must name the handler reading a core table and pass the one reading its view, named: %', v_found;
    END IF;
END $$;
