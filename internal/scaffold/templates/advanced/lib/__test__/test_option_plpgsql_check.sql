-- ============================================================================
-- Option plpgsql_check: every PL/pgSQL routine passes plpgsql_check
-- ============================================================================
-- Off by default; switched on in pg_temp.guard_enabled() in deploy.sql.
-- PostgreSQL checks a PL/pgSQL body's SQL only when a statement runs, so a
-- misspelt column in a branch no test reaches ships. plpgsql_check reads every
-- statement of every routine of an application schema (a trigger function
-- against the first table it is attached to); any error fails the deploy.
-- The extension is created here, inside the test's savepoint, and rolled back
-- with it. Creating it needs a superuser deploy login and the extension
-- installed on the server; without either the option is skipped with a
-- warning. Off, this file does nothing: it needs the extension to prove
-- itself.
-- ============================================================================

CREATE FUNCTION pg_temp.plpgsql_check_ready()
RETURNS boolean
LANGUAGE plpgsql AS $$
DECLARE
    v_role text := current_user;
BEGIN
    IF to_regproc('extensions.plpgsql_check_function_tb') IS NOT NULL THEN
        RETURN true;
    END IF;
    RESET ROLE;
    BEGIN
        CREATE EXTENSION plpgsql_check SCHEMA extensions;
    EXCEPTION WHEN undefined_file OR insufficient_privilege OR feature_not_supported THEN
        EXECUTE format('SET ROLE %I', v_role);
        RETURN false;
    END;
    EXECUTE format('SET ROLE %I', v_role);
    RETURN true;
END $$;

CREATE FUNCTION pg_temp.plpgsql_check_violation()
RETURNS SETOF text
LANGUAGE plpgsql AS $$
DECLARE
    v_routine record;
BEGIN
    FOR v_routine IN
        SELECT p.oid, format('%s.%I(%s)', s.name, p.proname, pg_get_function_identity_arguments(p.oid)) AS name,
               COALESCE((SELECT t.tgrelid FROM pg_trigger t WHERE t.tgfoid = p.oid ORDER BY t.oid LIMIT 1), 0) AS relid
        FROM pg_proc p
        JOIN pg_temp.application_schema() s ON s.oid = p.pronamespace
        JOIN pg_language l ON l.oid = p.prolang AND l.lanname = 'plpgsql'
        WHERE p.prorettype IS DISTINCT FROM 'event_trigger'::regtype
          AND (p.prorettype IS DISTINCT FROM 'trigger'::regtype OR EXISTS (SELECT FROM pg_trigger t WHERE t.tgfoid = p.oid))
          AND NOT EXISTS (SELECT FROM pg_depend d
                          WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
    LOOP
        RETURN QUERY
            SELECT v_routine.name || ': ' || c.message
            FROM extensions.plpgsql_check_function_tb(v_routine.oid, v_routine.relid) c
            WHERE c.level = 'error';
    END LOOP;
END $$;

DO $$
DECLARE
    v_found text;
BEGIN
    IF NOT pg_temp.guard_enabled('plpgsql_check') THEN
        RETURN;
    END IF;
    IF NOT pg_temp.plpgsql_check_ready() THEN
        RAISE WARNING 'Deploy guard plpgsql_check skipped: the extension cannot be created (not installed on the server, or the deploy login is not a superuser)';
        RETURN;
    END IF;
    PERFORM pg_temp.enforce_guard('plpgsql_check', ARRAY(SELECT pg_temp.plpgsql_check_violation()));

    CREATE FUNCTION core.plpgsql_check_probe() RETURNS uuid LANGUAGE plpgsql AS $f$
    BEGIN
        RETURN (SELECT no_such_column FROM membership.organization LIMIT 1);
    END $f$;
    SELECT string_agg(v, ', ') INTO v_found FROM pg_temp.plpgsql_check_violation() v WHERE v LIKE 'core.plpgsql_check_probe()%';
    IF coalesce(v_found, '') NOT LIKE '%no_such_column%' THEN
        RAISE EXCEPTION 'TEST FAILED: plpgsql_check must name a routine reading a missing column, named: %', v_found;
    END IF;
END $$;
