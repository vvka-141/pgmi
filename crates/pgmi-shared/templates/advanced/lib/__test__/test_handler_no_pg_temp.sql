-- ============================================================================
-- Test: no handler body calls a pg_temp function
-- ============================================================================
-- pg_temp ends with the deploy session: a body that calls into it deploys
-- green, passes every test in that session, and fails on every later call.
-- ============================================================================

DO $$
DECLARE
    v_offenders text;
BEGIN
    SELECT string_agg(h.handler_function_name, ', ' ORDER BY h.handler_function_name)
    INTO v_offenders
    FROM api.handler h
    WHERE h.deleted_at IS NULL
      AND pg_get_functiondef(h.handler_func) LIKE '%pg\_temp.%';

    IF v_offenders IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: handler bodies call pg_temp, which ends with the deploy session: %', v_offenders;
    END IF;

    RAISE NOTICE '  + no handler body calls a pg_temp function';
END $$;
