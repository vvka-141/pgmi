-- ============================================================================
-- Option view_column_comments: every core view and column says what it is
-- ============================================================================
-- Off by default; switched on in pg_temp.guard_enabled() in deploy/settings.sql.
-- The views of core are what the application, its reports and an assistant
-- read; a comment on the view and on each column is their documentation, kept
-- in the catalog beside them. A core view or column without one fails.
-- ============================================================================

CREATE FUNCTION pg_temp.view_column_comments_violation()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT format('core.%I', c.relname)
    FROM pg_class c
    WHERE c.relnamespace = 'core'::regnamespace AND c.relkind IN ('v', 'm')
      AND obj_description(c.oid, 'pg_class') IS NULL
    UNION ALL
    SELECT format('core.%I.%I', c.relname, a.attname)
    FROM pg_class c
    JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
    WHERE c.relnamespace = 'core'::regnamespace AND c.relkind IN ('v', 'm')
      AND col_description(c.oid, a.attnum) IS NULL
$$;

DO $$
BEGIN
    IF pg_temp.guard_enabled('view_column_comments') THEN
        PERFORM pg_temp.enforce_guard('view_column_comments', ARRAY(SELECT pg_temp.view_column_comments_violation()));
    END IF;
END $$;

DO $$
DECLARE
    v_found text;
BEGIN
    CREATE VIEW core.vw_comment_probe WITH (security_invoker = true) AS SELECT 1 AS documented, 2 AS undocumented;
    COMMENT ON COLUMN core.vw_comment_probe.documented IS 'A column with its comment.';
    SELECT string_agg(v, '; ' ORDER BY v) INTO v_found FROM pg_temp.view_column_comments_violation() v WHERE v LIKE 'core.vw_comment_probe%';
    IF v_found IS DISTINCT FROM 'core.vw_comment_probe; core.vw_comment_probe.undocumented' THEN
        RAISE EXCEPTION 'TEST FAILED: view_column_comments must name the view and its uncommented column, named: %', v_found;
    END IF;
END $$;
