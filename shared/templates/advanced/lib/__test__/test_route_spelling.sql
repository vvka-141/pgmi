-- ============================================================================
-- Test: Route spelling tolerance
-- ============================================================================
-- The funnel must be liberal: every valid spelling of a path must reach the
-- correct handler. If this file is red, the canonicalization layer
-- is broken or has been bypassed.
-- ============================================================================

-- spelling tolerance for fixed routes
DO $$
DECLARE
    v_response api.http_response;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-3001-4000-8000-000000000001',
            'path', '/spell-probe',
            'httpMethod', '^GET$',
            'name', 'spell_probe',
            'description', 'Spelling tolerance probe',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('reached', true));
END;
$body$
    );

    v_response := api.rest_invoke('GET', '/spell-probe', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/spell-probe (canonical)', v_response, 200);

    v_response := api.rest_invoke('GET', '/spell-probe/', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/spell-probe/ (trailing slash)', v_response, 200);

    v_response := api.rest_invoke('GET', 'spell-probe', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('spell-probe (no leading slash)', v_response, 200);

    v_response := api.rest_invoke('GET', '//spell-probe', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('//spell-probe (duplicate leading slash)', v_response, 200);

    v_response := api.rest_invoke('GET', '/spell-probe//', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/spell-probe// (duplicate trailing slashes)', v_response, 200);

    v_response := api.rest_invoke('GET', '/public/../spell-probe', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/public/../spell-probe (dot-segments)', v_response, 200);

    v_response := api.rest_invoke('GET', '/s%70ell-probe', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/s%70ell-probe (unreserved percent-encoding)', v_response, 200);

    v_response := api.rest_invoke('GET', '/s%70ell-%70robe', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/s%70ell-%70robe (multiple unreserved decodes)', v_response, 200);

    v_response := api.rest_invoke('GET', '/public/%2e%2e/spell-probe', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/public/%2e%2e/spell-probe (encoded dot-segments)', v_response, 200);
END $$;

-- query string preservation across spellings
DO $$
DECLARE
    v_response api.http_response;
    v_params extensions.hstore;
BEGIN
    v_response := api.rest_invoke('GET', '/spell-probe?a=1&b=2', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/spell-probe?a=1&b=2', v_response, 200);

    v_response := api.rest_invoke('GET', '/spell-probe?b=2&a=1', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/spell-probe?b=2&a=1', v_response, 200);

    v_params := api.query_params('/spell-probe?a=1&b=2');
    IF v_params->'a' IS DISTINCT FROM '1' OR v_params->'b' IS DISTINCT FROM '2' THEN
        RAISE EXCEPTION 'query_params(?a=1&b=2): expected a=1,b=2, got %', v_params;
    END IF;

    v_params := api.query_params('/spell-probe?b=2&a=1');
    IF v_params->'a' IS DISTINCT FROM '1' OR v_params->'b' IS DISTINCT FROM '2' THEN
        RAISE EXCEPTION 'query_params(?b=2&a=1): expected a=1,b=2, got %', v_params;
    END IF;
END $$;

-- spelling tolerance for parameterized routes
DO $$
DECLARE
    v_response api.http_response;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-3002-4000-8000-000000000001',
            'path', '/spell/{n}',
            'httpMethod', '^GET$',
            'name', 'spell_param_probe',
            'description', 'Parameterized spelling probe',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('reached', true));
END;
$body$
    );

    v_response := api.rest_invoke('GET', '/spell/42', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/spell/42', v_response, 200);

    v_response := api.rest_invoke('GET', '/spell/42/', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/spell/42/ (trailing slash)', v_response, 200);

    v_response := api.rest_invoke('GET', 'spell/42', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('spell/42 (no leading slash)', v_response, 200);
END $$;
