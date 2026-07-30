-- ============================================================================
-- Test: Route spelling tolerance
-- ============================================================================
-- The funnel must be liberal: every valid spelling of a path must reach the
-- correct handler. If this file is red, the canonicalization layer
-- is broken or has been bypassed.
-- ============================================================================

DO $$
DECLARE
    v_response api.http_response;
BEGIN
    RAISE NOTICE '→ Testing spelling tolerance for fixed routes';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-3001-4000-8000-000000000001',
            'uri', '^/spell-probe$',
            'httpMethod', '^GET$',
            'name', 'spell_probe',
            'description', 'Spelling tolerance probe',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('reached', true));
END;
$body$
    );

    v_response := api.rest_invoke('GET', '/spell-probe', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/spell-probe (canonical) → %, expected 200', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/spell-probe/', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/spell-probe/ (trailing slash) → %, expected 200', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', 'spell-probe', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'spell-probe (no leading slash) → %, expected 200', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '//spell-probe', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '//spell-probe (duplicate leading slash) → %, expected 200', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/spell-probe//', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/spell-probe// (duplicate trailing slashes) → %, expected 200', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/public/../spell-probe', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/public/../spell-probe (dot-segments) → %, expected 200', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/s%70ell-probe', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/s%%70ell-probe (unreserved percent-encoding) → %, expected 200', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/s%70ell-%70robe', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/s%%70ell-%%70robe (multiple unreserved decodes) → %, expected 200', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/public/%2e%2e/spell-probe', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/public/%%2e%%2e/spell-probe (encoded dot-segments) → %, expected 200', v_response.status_code;
    END IF;

    RAISE NOTICE '  ✓ all path spellings reach the handler';
END $$;


DO $$
DECLARE
    v_response api.http_response;
    v_params extensions.hstore;
BEGIN
    RAISE NOTICE '→ Testing query string preservation across spellings';

    v_response := api.rest_invoke('GET', '/spell-probe?a=1&b=2', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/spell-probe?a=1&b=2 → %, expected 200', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/spell-probe?b=2&a=1', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/spell-probe?b=2&a=1 → %, expected 200', v_response.status_code;
    END IF;

    v_params := api.query_params('/spell-probe?a=1&b=2');
    IF v_params->'a' IS DISTINCT FROM '1' OR v_params->'b' IS DISTINCT FROM '2' THEN
        RAISE EXCEPTION 'query_params(?a=1&b=2): expected a=1,b=2, got %', v_params;
    END IF;

    v_params := api.query_params('/spell-probe?b=2&a=1');
    IF v_params->'a' IS DISTINCT FROM '1' OR v_params->'b' IS DISTINCT FROM '2' THEN
        RAISE EXCEPTION 'query_params(?b=2&a=1): expected a=1,b=2, got %', v_params;
    END IF;

    RAISE NOTICE '  ✓ query parameters parsed regardless of key order';
END $$;


DO $$
DECLARE
    v_response api.http_response;
BEGIN
    RAISE NOTICE '→ Testing spelling tolerance for parameterized routes';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-3002-4000-8000-000000000001',
            'uri', '^/spell/([0-9]+)$',
            'httpMethod', '^GET$',
            'name', 'spell_param_probe',
            'description', 'Parameterized spelling probe',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('reached', true));
END;
$body$
    );

    v_response := api.rest_invoke('GET', '/spell/42', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/spell/42 → %, expected 200', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/spell/42/', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/spell/42/ (trailing slash) → %, expected 200', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', 'spell/42', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'spell/42 (no leading slash) → %, expected 200', v_response.status_code;
    END IF;

    RAISE NOTICE '  ✓ parameterized routes tolerate spelling variants';
END $$;
