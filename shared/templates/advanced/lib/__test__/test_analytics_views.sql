-- ============================================================================
-- Test: the analytics views report every handler once with its route and
-- drift, and every exchange with its replay statement
-- ============================================================================

-- Handler and route views
DO $$
DECLARE
    v_handler_count int;
    v_route_count int;
    v_info record;
BEGIN
    -- vw_handler_info: function_exists and definition_drifted
    SELECT count(*) INTO v_handler_count FROM api.vw_handler_info;
    IF coalesce(v_handler_count, 0) < 1 THEN
        RAISE EXCEPTION 'vw_handler_info should have at least 1 row (demo handlers registered)';
    END IF;

    SELECT function_exists, definition_drifted, has_rest_route, handler_function_name
    INTO v_info
    FROM api.vw_handler_info
    WHERE handler_function_name = 'hello_world';

    -- SELECT ... INTO is not STRICT: no row leaves every field NULL, and each
    -- assertion below would then compare against NULL and skip.
    IF NOT FOUND THEN
        RAISE EXCEPTION 'vw_handler_info has no row for hello_world';
    END IF;

    IF v_info.function_exists IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'hello_world function_exists should be true';
    END IF;
    IF v_info.definition_drifted IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'hello_world definition_drifted should be false (freshly deployed)';
    END IF;
    IF v_info.has_rest_route IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'hello_world should have a REST route';
    END IF;

    -- vw_handler_info: schema coverage columns
    SELECT has_output_schema INTO v_info
    FROM api.vw_handler_info
    WHERE handler_function_name = 'hello_world';

    IF NOT FOUND THEN
        RAISE EXCEPTION 'vw_handler_info has no row for hello_world';
    END IF;
    IF v_info.has_output_schema IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'hello_world should have output schema';
    END IF;

    -- vw_handler_summary: single row with counts
    DECLARE
        v_summary record;
    BEGIN
        SELECT total_handlers, rest_handlers INTO v_summary FROM api.vw_handler_summary;

        IF NOT FOUND THEN
            RAISE EXCEPTION 'vw_handler_summary returned no row';
        END IF;
        IF coalesce(v_summary.total_handlers, 0) < 1 THEN
            RAISE EXCEPTION 'vw_handler_summary total_handlers should be >= 1';
        END IF;
        IF coalesce(v_summary.rest_handlers, 0) < 1 THEN
            RAISE EXCEPTION 'vw_handler_summary rest_handlers should be >= 1';
        END IF;
    END;

    -- vw_route_info: unified route view
    SELECT count(*) INTO v_route_count FROM api.vw_route_info;
    IF coalesce(v_route_count, 0) < 1 THEN
        RAISE EXCEPTION 'vw_route_info should have at least 1 route';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM api.vw_route_info
        WHERE route_type = 'rest' AND handler_function_name = 'hello_world'
    ) THEN
        RAISE EXCEPTION 'vw_route_info should show hello_world under rest';
    END IF;

    -- vw_route_summary: single row
    DECLARE
        v_rsummary record;
    BEGIN
        SELECT total_routes, rest_routes INTO v_rsummary FROM api.vw_route_summary;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'vw_route_summary returned no row';
        END IF;
        IF coalesce(v_rsummary.total_routes, 0) < 1 THEN
            RAISE EXCEPTION 'vw_route_summary total_routes should be >= 1';
        END IF;
    END;
END $$;

-- Exchange views
DO $$
DECLARE
    v_response api.http_response;
    v_exchange_count int;
    v_summary record;
    v_replay text;
BEGIN
    -- Generate a REST exchange by invoking hello_world (with auth).
    -- "access": "authenticated" needs a live login session of an enabled person.
    PERFORM pg_temp.test_login('test', 'admin', 'analytics-admin@example.test', NULL, true);

    v_response := api.rest_invoke('GET', '/hello?name=ViewTest',
        pg_temp.test_session_headers('test|admin'), null::bytea);
    PERFORM pg_temp.expect_status('hello_world invocation failed', v_response, 200);

    -- vw_rest_exchange_info: exchange visible with replay_sql
    SELECT count(*) INTO v_exchange_count FROM api.vw_rest_exchange_info;
    IF coalesce(v_exchange_count, 0) < 1 THEN
        RAISE EXCEPTION 'vw_rest_exchange_info should have at least 1 row after invocation';
    END IF;

    SELECT replay_sql INTO v_replay
    FROM api.vw_rest_exchange_info
    WHERE request_url LIKE '%/hello%'
    ORDER BY enqueued_at DESC LIMIT 1;

    IF coalesce(v_replay, '') NOT LIKE '%api.replay_exchange(%' THEN
        RAISE EXCEPTION 'vw_rest_exchange_info replay_sql should call api.replay_exchange, got: %', v_replay;
    END IF;

    -- vw_exchange_summary: counts updated
    SELECT total_exchanges, rest_exchanges, error_count
    INTO v_summary FROM api.vw_exchange_summary;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'vw_exchange_summary returned no row';
    END IF;
    IF coalesce(v_summary.total_exchanges, 0) < 1 THEN
        RAISE EXCEPTION 'vw_exchange_summary total_exchanges should be >= 1';
    END IF;
    IF coalesce(v_summary.rest_exchanges, 0) < 1 THEN
        RAISE EXCEPTION 'vw_exchange_summary rest_exchanges should be >= 1';
    END IF;

    -- vw_exchange_stats: at least one row with REST protocol
    IF NOT EXISTS (
        SELECT 1 FROM api.vw_exchange_stats
        WHERE protocol = 'rest' AND _grp_protocol = 0
    ) THEN
        RAISE EXCEPTION 'vw_exchange_stats should have a rest protocol row';
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'api' AND table_name LIKE 'vw\_%exchange%' AND column_name LIKE '%pending%'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: an exchange view still has a pending column; the log is written with its response';
    END IF;
END $$;

DO $$
DECLARE
    v_row record;
BEGIN
    RAISE DEBUG '-> vw_handler_info: one row per handler, its route as values';

    IF (SELECT count(*) FROM api.vw_handler_info) IS DISTINCT FROM (SELECT count(*) FROM api.handler)
       OR (SELECT count(DISTINCT object_id) FROM api.vw_handler_info) IS DISTINCT FROM (SELECT count(*) FROM api.handler) THEN
        RAISE EXCEPTION 'TEST FAILED: vw_handler_info must have exactly one row per handler';
    END IF;

    SELECT rest_path, rest_method_regexp, is_unrouted INTO v_row
    FROM api.vw_handler_info WHERE handler_function_name = 'hello_world';
    IF v_row.rest_path IS DISTINCT FROM '/hello' OR v_row.rest_method_regexp IS DISTINCT FROM '^GET$'
       OR v_row.is_unrouted IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'TEST FAILED: hello_world must show /hello, ^GET$ and a route, got %', v_row;
    END IF;

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-4481-4000-8000-000000000001', 'path', '/unrouted-probe',
            'httpMethod', '^GET$', 'name', 'unrouted_probe', 'access', 'public'),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
    DELETE FROM api.rest_route WHERE handler_object_id = 'ffffffff-4481-4000-8000-000000000001';

    SELECT rest_path, is_unrouted INTO v_row
    FROM api.vw_handler_info WHERE object_id = 'ffffffff-4481-4000-8000-000000000001';
    IF v_row.is_unrouted IS DISTINCT FROM true OR v_row.rest_path IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a handler without a route must show is_unrouted and no rest_path, got %', v_row;
    END IF;
END $$;
