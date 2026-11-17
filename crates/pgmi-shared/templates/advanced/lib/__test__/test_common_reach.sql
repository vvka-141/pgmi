-- ============================================================================
-- Test: every pinned search_path names common
-- ============================================================================
-- common.try_cast and the ?> operators live in common. A routine that pins a
-- search_path without it cannot use ?>, and neither can a handler body the
-- routine dispatches to: the body runs under the dispatcher's search_path.
-- ============================================================================

CREATE FUNCTION pg_temp.search_path_without_common()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT p.oid::regprocedure::text || ' (' || cfg || ')'
    FROM pg_proc p
    JOIN pg_temp.application_schema() s ON s.oid = p.pronamespace
    CROSS JOIN LATERAL unnest(p.proconfig) cfg
    WHERE cfg LIKE 'search_path=%'
      AND NOT ('common' = ANY (string_to_array(replace(substr(cfg, 13), ' ', ''), ',')));
$$;

DO $$
DECLARE
    v_bad text;
BEGIN
    RAISE NOTICE '-> Every pinned search_path names common';

    SELECT string_agg(r, E'\n  ' ORDER BY r) INTO v_bad FROM pg_temp.search_path_without_common() r;
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION E'TEST FAILED: routines pin a search_path without common:\n  %', v_bad;
    END IF;

    CREATE FUNCTION internal.reach_probe() RETURNS int LANGUAGE sql
    SET search_path = internal, pg_temp AS $f$ SELECT 1 $f$;
    SELECT string_agg(r, ', ') INTO v_bad FROM pg_temp.search_path_without_common() r;
    IF coalesce(v_bad, '') NOT LIKE '%reach_probe() (search_path=internal, pg_temp)' THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name a planted routine without common, named: %', v_bad;
    END IF;
    DROP FUNCTION internal.reach_probe();

    IF ('42' ?> 0) IS DISTINCT FROM 42 OR to_regoperator('common.?>(text,date)') IS NULL
       OR to_regoperator('api.?>(text,integer)') IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the ?> operators must live in common, with a date overload';
    END IF;
END $$;
