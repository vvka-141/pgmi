-- ============================================================================
-- Test: Route anchoring — semantic routing proof
-- ============================================================================
-- Routes must not catch each other's traffic. Registration enforces:
--   1. Self-consistency: a route's canonical_path must match its own regex
--   2. Non-overlap: no route's canonical_path may match another route's regex
--      (when their HTTP methods intersect)
--   3. httpMethod must be ^-anchored and $-terminated
-- ============================================================================

DO $$
DECLARE
    v_response api.http_response;
BEGIN
    RAISE NOTICE '→ Testing anchored routes match only their own path';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-2001-4000-8000-000000000001',
            'uri', '^/anchor$',
            'httpMethod', '^GET$',
            'name', 'anchor_probe',
            'description', 'Anchoring probe endpoint',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('matched', true));
END;
$body$
    );

    v_response := api.rest_invoke('GET', '/anchor', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/anchor should route to its handler, got %', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/anchorage', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION '/anchorage must not match /anchor route, got %', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/anchor/nested', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION '/anchor/nested must not match /anchor route, got %', v_response.status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/anchor?limit=10', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/anchor?limit=10 must still route, got %', v_response.status_code;
    END IF;

    RAISE NOTICE '  ✓ anchored routes match only their own path';
END $$;


DO $$
DECLARE
    v_raised boolean;
BEGIN
    RAISE NOTICE '→ Testing registration rejects inconsistent and unanchored patterns';

    v_raised := false;
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-2002-4000-8000-000000000001',
                'path', '/test-alpha',
                'uri', '^/test-beta$',
                'httpMethod', '^GET$',
                'name', 'self_consistency_probe',
                'requiresAuth', false
            ),
            $body$
BEGIN
    RETURN api.json_response(200, '{}'::jsonb);
END;
$body$
        );
    EXCEPTION WHEN invalid_parameter_value THEN
        v_raised := true;
    END;

    IF v_raised IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'registration must reject a route whose canonical path does not match its own uri';
    END IF;

    v_raised := false;
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-2003-4000-8000-000000000001',
                'uri', '^/method-probe$',
                'httpMethod', 'GET',
                'name', 'method_probe',
                'requiresAuth', false
            ),
            $body$
BEGIN
    RETURN api.json_response(200, '{}'::jsonb);
END;
$body$
        );
    EXCEPTION WHEN invalid_parameter_value THEN
        v_raised := true;
    END;

    IF v_raised IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'registration must reject an unanchored httpMethod';
    END IF;

    RAISE NOTICE '  ✓ registration rejects inconsistent and unanchored patterns';
END $$;


DO $$
DECLARE
    v_raised boolean;
BEGIN
    RAISE NOTICE '→ Testing route overlap detection at registration time';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-3001-4000-8000-000000000001',
            'uri', '^/overlap/.*$',
            'httpMethod', '^GET$',
            'name', 'overlap_catchall',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.json_response(200, '{}'::jsonb);
END;
$body$
    );

    v_raised := false;
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-3001-4000-8000-000000000002',
                'uri', '^/overlap/specific$',
                'httpMethod', '^GET$',
                'name', 'overlap_specific',
                'requiresAuth', false
            ),
            $body$
BEGIN
    RETURN api.json_response(200, '{}'::jsonb);
END;
$body$
        );
    EXCEPTION WHEN invalid_parameter_value THEN
        v_raised := true;
    END;

    IF v_raised IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'registration must reject a route whose canonical path is caught by another route''s regex';
    END IF;

    RAISE NOTICE '  ✓ overlapping routes are rejected at registration time';
END $$;


DO $$
DECLARE
    v_raised boolean;
BEGIN
    RAISE NOTICE '→ Testing example requirement for path+uri parameterized routes';

    v_raised := false;
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-3002-4000-8000-000000000001',
                'path', '/items/{itemId}',
                'uri', '^/items/([0-9]+)$',
                'httpMethod', '^GET$',
                'name', 'example_required_probe',
                'requiresAuth', false
            ),
            $body$
BEGIN
    RETURN api.json_response(200, '{}'::jsonb);
END;
$body$
        );
    EXCEPTION WHEN invalid_parameter_value THEN
        v_raised := true;
    END;

    IF v_raised IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'registration must require "example" when both path and uri are declared with parameters';
    END IF;

    v_raised := false;
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-3002-4000-8000-000000000002',
                'path', '/items/{itemId}',
                'uri', '^/items/([0-9]+)$',
                'example', '/items/abc',
                'httpMethod', '^GET$',
                'name', 'example_mismatch_probe',
                'requiresAuth', false
            ),
            $body$
BEGIN
    RETURN api.json_response(200, '{}'::jsonb);
END;
$body$
        );
    EXCEPTION WHEN invalid_parameter_value THEN
        v_raised := true;
    END;

    IF v_raised IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'registration must reject an example URL that does not match the uri regex';
    END IF;

    RAISE NOTICE '  ✓ example requirement enforced for path+uri parameterized routes';
END $$;


DO $$
DECLARE
    v_overlap record;
    v_bad text;
BEGIN
    RAISE NOTICE '→ Testing no route''s canonical path matches another route''s regex';

    SELECT string_agg(
        format('%s (%s) caught by %s (%s)',
            r1.route_name, r1_path, r2.route_name, r2.address_regexp),
        '; '
    )
    INTO v_bad
    FROM (
        SELECT r.route_name, r.address_regexp, r.handler_object_id,
               r.canonical_path AS opath
        FROM api.rest_route r
    ) r1
    CROSS JOIN api.rest_route r2
    CROSS JOIN LATERAL (
        SELECT CASE
            WHEN r1.opath NOT LIKE '%{%' THEN r1.opath
            ELSE NULL
        END AS test_path
    ) paths(r1_path)
    WHERE r1.handler_object_id IS DISTINCT FROM r2.handler_object_id
      AND paths.r1_path IS NOT NULL
      AND paths.r1_path ~ r2.address_regexp;

    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION 'route overlap detected: %', v_bad;
    END IF;

    RAISE NOTICE '  ✓ no shipped route catches another route''s canonical path';
END $$;


DO $$
DECLARE
    v_response api.http_response;
BEGIN
    RAISE NOTICE '→ Testing that a longer path containing a route does not match it';

    IF EXISTS (SELECT 1 FROM api.rest_route WHERE address_regexp = '^/health$') THEN
        v_response := api.rest_invoke('GET', '/healthz', ''::extensions.hstore, NULL::bytea);
        IF v_response.status_code IS DISTINCT FROM 404 THEN
            RAISE EXCEPTION '/healthz must not be served by the /health route, got %',
                v_response.status_code;
        END IF;

        v_response := api.rest_invoke('GET', '/helloworld', ''::extensions.hstore, NULL::bytea);
        IF v_response.status_code IS DISTINCT FROM 404 THEN
            RAISE EXCEPTION '/helloworld must not be served by the /hello route, got %',
                v_response.status_code;
        END IF;

        v_response := api.rest_invoke('GET', '/othello/', ''::extensions.hstore, NULL::bytea);
        IF v_response.status_code IS DISTINCT FROM 404 THEN
            RAISE EXCEPTION '/othello/ must not be served by the /hello route, got %',
                v_response.status_code;
        END IF;
    END IF;

    RAISE NOTICE '  ✓ /healthz, /helloworld and /othello/ all fall through to 404';
END $$;
