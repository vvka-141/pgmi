-- ============================================================================
-- Test: Analytics views (handler, route, exchange)
-- ============================================================================

DO $$
DECLARE
    v_handler_count int;
    v_route_count int;
    v_info record;
BEGIN
    RAISE NOTICE '-> Testing analytics views';

    -- ========================================================================
    -- vw_handler_info: function_exists and definition_drifted
    -- ========================================================================

    SELECT count(*) INTO v_handler_count FROM api.vw_handler_info;
    IF v_handler_count < 1 THEN
        RAISE EXCEPTION 'vw_handler_info should have at least 1 row (demo handlers registered)';
    END IF;

    SELECT function_exists, definition_drifted, has_rest_route, handler_function_name
    INTO v_info
    FROM api.vw_handler_info
    WHERE handler_function_name = 'hello_world';

    IF NOT v_info.function_exists THEN
        RAISE EXCEPTION 'hello_world function_exists should be true';
    END IF;
    IF v_info.definition_drifted THEN
        RAISE EXCEPTION 'hello_world definition_drifted should be false (freshly deployed)';
    END IF;
    IF NOT v_info.has_rest_route THEN
        RAISE EXCEPTION 'hello_world should have a REST route';
    END IF;

    RAISE NOTICE '  + vw_handler_info: function_exists=true, definition_drifted=false for hello_world';

    -- ========================================================================
    -- vw_handler_info: schema coverage columns
    -- ========================================================================

    SELECT has_output_schema INTO v_info
    FROM api.vw_handler_info
    WHERE handler_function_name = 'hello_world';

    IF NOT v_info.has_output_schema THEN
        RAISE EXCEPTION 'hello_world should have output schema';
    END IF;

    RAISE NOTICE '  + vw_handler_info: schema coverage columns present';

    -- ========================================================================
    -- vw_handler_summary: single row with counts
    -- ========================================================================

    DECLARE
        v_summary record;
    BEGIN
        SELECT total_handlers, rest_handlers INTO v_summary FROM api.vw_handler_summary;

        IF v_summary.total_handlers < 1 THEN
            RAISE EXCEPTION 'vw_handler_summary total_handlers should be >= 1';
        END IF;
        IF v_summary.rest_handlers < 1 THEN
            RAISE EXCEPTION 'vw_handler_summary rest_handlers should be >= 1';
        END IF;

        RAISE NOTICE '  + vw_handler_summary: total=%, rest=%', v_summary.total_handlers, v_summary.rest_handlers;
    END;

    -- ========================================================================
    -- vw_route_info: unified route view
    -- ========================================================================

    SELECT count(*) INTO v_route_count FROM api.vw_route_info;
    IF v_route_count < 1 THEN
        RAISE EXCEPTION 'vw_route_info should have at least 1 route';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM api.vw_route_info
        WHERE route_type = 'rest' AND handler_function_name = 'hello_world'
    ) THEN
        RAISE EXCEPTION 'vw_route_info should show hello_world under rest';
    END IF;

    RAISE NOTICE '  + vw_route_info: % routes, hello_world found under rest', v_route_count;

    -- ========================================================================
    -- vw_route_summary: single row
    -- ========================================================================

    DECLARE
        v_rsummary record;
    BEGIN
        SELECT total_routes, rest_routes INTO v_rsummary FROM api.vw_route_summary;
        IF v_rsummary.total_routes < 1 THEN
            RAISE EXCEPTION 'vw_route_summary total_routes should be >= 1';
        END IF;
        RAISE NOTICE '  + vw_route_summary: total=%, rest=%', v_rsummary.total_routes, v_rsummary.rest_routes;
    END;

    RAISE NOTICE '+ Handler/route analytics view tests passed';
END $$;

DO $$
DECLARE
    v_response api.http_response;
    v_exchange_count int;
    v_summary record;
    v_replay text;
BEGIN
    RAISE NOTICE '-> Testing exchange analytics views';

    -- ========================================================================
    -- Generate a REST exchange by invoking hello_world (with auth)
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/hello?name=ViewTest',
        ('x-user-id=>test|admin')::extensions.hstore, null::bytea);
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'hello_world invocation failed: %', (v_response).status_code;
    END IF;

    -- ========================================================================
    -- vw_rest_exchange_info: exchange visible with replay_sql
    -- ========================================================================

    SELECT count(*) INTO v_exchange_count FROM api.vw_rest_exchange_info;
    IF v_exchange_count < 1 THEN
        RAISE EXCEPTION 'vw_rest_exchange_info should have at least 1 row after invocation';
    END IF;

    SELECT replay_sql INTO v_replay
    FROM api.vw_rest_exchange_info
    WHERE request_url LIKE '%/hello%'
    ORDER BY enqueued_at DESC LIMIT 1;

    IF v_replay IS NULL OR v_replay NOT LIKE '%rest_invoke%' THEN
        RAISE EXCEPTION 'vw_rest_exchange_info replay_sql should contain rest_invoke, got: %', v_replay;
    END IF;

    RAISE NOTICE '  + vw_rest_exchange_info: % exchanges, replay_sql generated', v_exchange_count;

    -- ========================================================================
    -- vw_exchange_summary: counts updated
    -- ========================================================================

    SELECT total_exchanges, rest_exchanges, error_count
    INTO v_summary FROM api.vw_exchange_summary;

    IF v_summary.total_exchanges < 1 THEN
        RAISE EXCEPTION 'vw_exchange_summary total_exchanges should be >= 1';
    END IF;
    IF v_summary.rest_exchanges < 1 THEN
        RAISE EXCEPTION 'vw_exchange_summary rest_exchanges should be >= 1';
    END IF;

    RAISE NOTICE '  + vw_exchange_summary: total=%, rest=%, errors=%',
        v_summary.total_exchanges, v_summary.rest_exchanges, v_summary.error_count;

    -- ========================================================================
    -- vw_exchange_stats: at least one row with REST protocol
    -- ========================================================================

    IF NOT EXISTS (
        SELECT 1 FROM api.vw_exchange_stats
        WHERE protocol = 'rest' AND _grp_protocol = 0
    ) THEN
        RAISE EXCEPTION 'vw_exchange_stats should have a rest protocol row';
    END IF;

    RAISE NOTICE '  + vw_exchange_stats: REST protocol row present';

    RAISE NOTICE '+ Exchange analytics view tests passed';
END $$;
