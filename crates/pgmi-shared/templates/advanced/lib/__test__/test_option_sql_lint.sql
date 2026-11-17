-- ============================================================================
-- Option sql_lint: routine and view bodies avoid four known traps
-- ============================================================================
-- Off by default; switched on in pg_temp.guard_enabled() in deploy/settings.sql.
-- Each rule names SQL that is legal and always a mistake:
--   NULL comparison   x = NULL, x <> NULL: always NULL, never true
--   NOT IN (SELECT    one NULL from the subquery and nothing matches
--   swallowed error   WHEN OTHERS THEN NULL hides every failure
--   money             locale-dependent formatting and rounding
-- Bodies are read with comments and quoted literals removed; views as
-- PostgreSQL stored them.
-- ============================================================================

CREATE FUNCTION pg_temp.sql_lint_rule()
RETURNS TABLE (rule text, pattern text)
LANGUAGE sql IMMUTABLE AS $$
    VALUES ('NULL comparison', '[<!][>=]\s*NULL\M|\m(WHERE|AND|OR|WHEN|ON|IF)\s+\(*\s*[a-z_][a-z0-9_.]*\s*=\s*NULL\M'),
           ('NOT IN (SELECT', '\mNOT\s+IN\s*\(\s*SELECT\M|\mNOT\s*\(\s*[a-z_][a-z0-9_.]*\s+IN\s*\(\s*SELECT\M'),
           ('swallowed error', '\mWHEN\s+OTHERS\s+THEN\s+NULL\s*;'),
           ('money', '::\s*money\M')
$$;

CREATE FUNCTION pg_temp.sql_lint_violation()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    WITH body AS (
        SELECT format('%s.%I(%s)', s.name, p.proname, pg_get_function_identity_arguments(p.oid)) AS name,
               regexp_replace(regexp_replace(regexp_replace(p.prosrc, '/\*.*?\*/', ' ', 'g'), '--[^\n]*', ' ', 'g'),
                              '''([^'']|'''')*''', '''''', 'g') AS src
        FROM pg_proc p
        JOIN pg_temp.application_schema() s ON s.oid = p.pronamespace
        JOIN pg_language l ON l.oid = p.prolang AND l.lanname IN ('sql', 'plpgsql')
        WHERE NOT EXISTS (SELECT FROM pg_depend d
                          WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
        UNION ALL
        SELECT format('%s.%I', s.name, c.relname), pg_get_viewdef(c.oid)
        FROM pg_class c
        JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
        WHERE c.relkind IN ('v', 'm')
    )
    SELECT b.name || ': ' || r.rule
    FROM body b
    JOIN pg_temp.sql_lint_rule() r ON b.src ~* r.pattern
$$;

DO $$
BEGIN
    IF pg_temp.guard_enabled('sql_lint') THEN
        PERFORM pg_temp.enforce_guard('sql_lint', ARRAY(SELECT pg_temp.sql_lint_violation()));
    END IF;
END $$;

DO $$
DECLARE
    v_found text;
BEGIN
    CREATE FUNCTION core.sql_lint_probe(p_id uuid) RETURNS int LANGUAGE plpgsql AS $f$
    DECLARE
        v_n int := NULL;
    BEGIN
        -- x = NULL in a comment is not a finding
        SELECT count(*) INTO v_n FROM membership.organization o
        WHERE o.object_id = NULL OR o.object_id NOT IN (SELECT m.organization_object_id FROM membership.organization_membership m);
        PERFORM 'WHERE x = NULL'::text, 1::numeric::money;
        RETURN v_n;
    EXCEPTION WHEN OTHERS THEN NULL;
    END $f$;
    CREATE VIEW core.vw_sql_lint_probe WITH (security_invoker = true) AS
        SELECT o.object_id FROM membership.organization o
        WHERE o.object_id NOT IN (SELECT m.organization_object_id FROM membership.organization_membership m);
    CREATE FUNCTION core.sql_lint_clean_probe(p_id uuid) RETURNS uuid LANGUAGE plpgsql AS $f$
    DECLARE
        v_id uuid := NULL;
    BEGIN
        UPDATE membership.organization SET display_name = display_name WHERE object_id IS NULL;
        v_id := p_id;
        RETURN v_id;
    END $f$;

    SELECT string_agg(v, '; ' ORDER BY v COLLATE "C") INTO v_found FROM pg_temp.sql_lint_violation() v WHERE v LIKE 'core.%lint%';
    IF v_found IS DISTINCT FROM 'core.sql_lint_probe(p_id uuid): NOT IN (SELECT; core.sql_lint_probe(p_id uuid): NULL comparison; '
                                'core.sql_lint_probe(p_id uuid): money; core.sql_lint_probe(p_id uuid): swallowed error; '
                                'core.vw_sql_lint_probe: NOT IN (SELECT' THEN
        RAISE EXCEPTION 'TEST FAILED: sql_lint must name each trap once and pass the clean routine, named: %', v_found;
    END IF;
END $$;
