-- ============================================================================
-- Test: OpenAPI 3.1 generator
-- ============================================================================

DO $$
DECLARE
    v_doc jsonb;
    v_paths jsonb;
    v_response api.http_response;
BEGIN
    RAISE NOTICE '-> Testing OpenAPI 3.1 generator';

    v_doc := api.openapi_document();

    -- ========================================================================
    -- Basic structure
    -- ========================================================================

    IF v_doc->>'openapi' IS DISTINCT FROM '3.1.0' THEN
        RAISE EXCEPTION 'openapi version should be 3.1.0, got %', v_doc->>'openapi';
    END IF;

    IF v_doc->'info'->>'title' IS NULL THEN
        RAISE EXCEPTION 'info.title should not be null';
    END IF;

    IF v_doc->'paths' IS NULL THEN
        RAISE EXCEPTION 'paths should not be null';
    END IF;

    IF v_doc->'components'->'securitySchemes'->'apiKeyAuth' IS NULL THEN
        RAISE EXCEPTION 'apiKeyAuth security scheme should be defined';
    END IF;

    IF v_doc->'components'->'securitySchemes'->'apiKeyAuth'->>'type' IS DISTINCT FROM 'apiKey' THEN
        RAISE EXCEPTION 'apiKeyAuth type should be apiKey, got %',
            v_doc->'components'->'securitySchemes'->'apiKeyAuth'->>'type';
    END IF;

    IF v_doc->'components'->'securitySchemes'->'apiKeyAuth'->>'name' IS DISTINCT FROM 'x-user-id' THEN
        RAISE EXCEPTION 'apiKeyAuth name should be x-user-id, got %',
            v_doc->'components'->'securitySchemes'->'apiKeyAuth'->>'name';
    END IF;

    IF v_doc->'components'->'securitySchemes'->'apiKeyAuth'->>'in' IS DISTINCT FROM 'header' THEN
        RAISE EXCEPTION 'apiKeyAuth in should be header, got %',
            v_doc->'components'->'securitySchemes'->'apiKeyAuth'->>'in';
    END IF;

    RAISE NOTICE '  + Document structure valid (3.1.0, info, paths, components)';

    -- ========================================================================
    -- REST routes appear in paths
    -- ========================================================================

    v_paths := v_doc->'paths';

    IF v_paths->'/openapi.json' IS NULL THEN
        RAISE EXCEPTION '/openapi.json path missing from spec. Paths: %', (SELECT jsonb_object_keys(v_paths) LIMIT 10);
    END IF;

    IF v_paths->'/openapi.json'->'get' IS NULL THEN
        RAISE EXCEPTION '/openapi.json GET operation missing';
    END IF;

    IF v_paths->'/openapi.json'->'get'->>'operationId' IS DISTINCT FROM 'openapi_spec' THEN
        RAISE EXCEPTION 'operationId should be openapi_spec, got %',
            v_paths->'/openapi.json'->'get'->>'operationId';
    END IF;

    RAISE NOTICE '  + /openapi.json path present with GET operation';

    -- ========================================================================
    -- Auth requirement: openapi_spec has no security (requiresAuth=false)
    -- ========================================================================

    IF v_paths->'/openapi.json'->'get'->'security' IS NOT NULL THEN
        RAISE EXCEPTION '/openapi.json should not have security requirement';
    END IF;

    RAISE NOTICE '  + No-auth handler omits security requirement';

    -- ========================================================================
    -- Handlers with requires_auth have security requirement
    -- ========================================================================

    IF v_paths->'/me' IS NOT NULL AND v_paths->'/me'->'get'->'security' IS NULL THEN
        RAISE EXCEPTION '/me should have security requirement';
    END IF;

    IF v_paths->'/me' IS NOT NULL
       AND v_paths->'/me'->'get'->'security'->0 ? 'apiKeyAuth' IS DISTINCT FROM true THEN
        RAISE EXCEPTION '/me security should reference apiKeyAuth, got %',
            v_paths->'/me'->'get'->'security';
    END IF;

    RAISE NOTICE '  + Auth handlers include apiKeyAuth security requirement';

    -- ========================================================================
    -- Response codes: every operation documents gateway error responses
    -- ========================================================================

    IF NOT (v_paths->'/openapi.json'->'get'->'responses' ? '404') THEN
        RAISE EXCEPTION 'operation should document 404 response';
    END IF;
    IF NOT (v_paths->'/openapi.json'->'get'->'responses' ? '405') THEN
        RAISE EXCEPTION 'operation should document 405 response';
    END IF;

    IF v_paths->'/me' IS NOT NULL THEN
        IF NOT (v_paths->'/me'->'get'->'responses' ? '401') THEN
            RAISE EXCEPTION 'authenticated operation should document 401 response';
        END IF;
    END IF;

    IF v_paths->'/openapi.json'->'get'->'responses' ? '401' THEN
        RAISE EXCEPTION 'unauthenticated operation should NOT document 401 response';
    END IF;

    RAISE NOTICE '  + Operations document gateway error responses (404, 405, 401 conditional)';

    -- ========================================================================
    -- Handler served via REST gateway
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/openapi.json');

    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'GET /openapi.json returned status %, expected 200', (v_response).status_code;
    END IF;

    RAISE NOTICE '  + GET /openapi.json returns 200 via gateway';

    -- ========================================================================
    -- Path template and method conversion utilities
    -- ========================================================================

    IF api.path_template_to_regex('/hello') IS DISTINCT FROM '^/hello$' THEN
        RAISE EXCEPTION 'path_template_to_regex(/hello) failed: got %',
            api.path_template_to_regex('/hello');
    END IF;

    IF api.path_template_to_regex('/orgs/{orgId}/users/{userId}') IS DISTINCT FROM '^/orgs/([^/]+)/users/([^/]+)$' THEN
        RAISE EXCEPTION 'path_template_to_regex multi-param failed: got %',
            api.path_template_to_regex('/orgs/{orgId}/users/{userId}');
    END IF;

    IF api.path_template_params('/orgs/{orgId}/users/{userId}') IS DISTINCT FROM ARRAY['orgId', 'userId'] THEN
        RAISE EXCEPTION 'path_template_params failed: got %',
            api.path_template_params('/orgs/{orgId}/users/{userId}');
    END IF;

    IF api.openapi_methods('^GET$') IS DISTINCT FROM ARRAY['get'] THEN
        RAISE EXCEPTION 'openapi_methods failed for ^GET$';
    END IF;

    RAISE NOTICE '  + Path template and method conversion utilities work';

    -- ========================================================================
    -- Schema coverage: REST examples have output schemas
    -- ========================================================================

    IF EXISTS (
        SELECT 1 FROM api.vw_handler_info
        WHERE has_rest_route AND NOT has_output_schema
          AND handler_function_name NOT LIKE 'openapi_%'
    ) THEN
        RAISE EXCEPTION 'REST handlers without output schema: %',
            (SELECT string_agg(handler_function_name, ', ')
             FROM api.vw_handler_info
             WHERE has_rest_route AND NOT has_output_schema
               AND handler_function_name NOT LIKE 'openapi_%');
    END IF;

    RAISE NOTICE '  + Schema coverage: all REST handlers declare output schema';

    -- ========================================================================
    -- OpenAPI document includes declared schemas (not fallback object)
    -- ========================================================================

    IF v_paths->'/hello'->'get'->'responses'->'200'->'content'->'application/json'->'schema'->>'type' = 'object'
       AND v_paths->'/hello'->'get'->'responses'->'200'->'content'->'application/json'->'schema'->'properties' IS NULL THEN
        RAISE EXCEPTION '/hello response schema should have properties (not bare object fallback)';
    END IF;

    RAISE NOTICE '  + Declared schemas appear in OpenAPI document';

    RAISE NOTICE '✓ OpenAPI 3.1 generator tests passed';
END $$;

DO $$
DECLARE
    v_doc jsonb;
    v_op jsonb;
BEGIN
    RAISE NOTICE '-> Testing OpenAPI transaction-isolation extension';

    -- A route that declares an isolation floor must advertise it in the spec so
    -- a preloading client can open the transaction at the right level on the
    -- FIRST call, instead of learning it reactively from a 428.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000001',
            'uri', '^/iso-openapi-test$',
            'httpMethod', '^GET$',
            'name', 'iso_openapi_test',
            'requiresAuth', false,
            'autoLog', false,
            'minTransactionIsolation', 'serializable'
        ),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
    );

    v_doc := api.openapi_document();
    v_op := v_doc->'paths'->'/iso-openapi-test'->'get';

    IF v_op IS NULL THEN
        RAISE EXCEPTION 'iso-openapi-test route missing from spec';
    END IF;

    IF v_op->>'x-pgmi-min-transaction-isolation' IS DISTINCT FROM 'serializable' THEN
        RAISE EXCEPTION 'operation should advertise x-pgmi-min-transaction-isolation=serializable, got %',
            v_op->>'x-pgmi-min-transaction-isolation';
    END IF;

    RAISE NOTICE '  + Route with a floor advertises x-pgmi-min-transaction-isolation';

    -- A route with no floor must NOT carry the key (absent, not null).
    IF (v_doc->'paths'->'/openapi.json'->'get') ? 'x-pgmi-min-transaction-isolation' THEN
        RAISE EXCEPTION 'floorless route should omit x-pgmi-min-transaction-isolation';
    END IF;

    RAISE NOTICE '  + Floorless route omits the extension';
    RAISE NOTICE '✓ OpenAPI transaction-isolation extension tests passed';
END $$;

-- ============================================================================
-- Test: multi-parameter routes emit valid OpenAPI
-- A path may not repeat a template variable name. Every capture group must get
-- a distinct {name}, and each must be declared in the operation's parameters.
-- ============================================================================

DO $$
DECLARE
    v_doc jsonb;
    v_op jsonb;
    v_params jsonb;
    v_path text;
    v_names text[];
    v_declared boolean;
BEGIN
    RAISE NOTICE '-> Testing OpenAPI multi-parameter path templating';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000002',
            'path', '/orgs/{orgId}/users/{userId}',
            'httpMethod', '^GET$',
            'name', 'multi_param_openapi_test',
            'requiresAuth', false,
            'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
    );

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000003',
            'uri', '^/things/([^/]+)/parts/([^/]+)$',
            'httpMethod', '^GET$',
            'name', 'positional_param_openapi_test',
            'requiresAuth', false,
            'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
    );

    v_doc := api.openapi_document();

    IF v_doc->'paths'->'/orgs/{orgId}/users/{userId}' IS NULL THEN
        RAISE EXCEPTION 'declared multi-param route missing; paths: %',
            (SELECT string_agg(k, ', ') FROM jsonb_object_keys(v_doc->'paths') k);
    END IF;

    IF v_doc->'paths'->'/things/{p1}/parts/{p2}' IS NULL THEN
        RAISE EXCEPTION 'positional fallback route missing; paths: %',
            (SELECT string_agg(k, ', ') FROM jsonb_object_keys(v_doc->'paths') k);
    END IF;

    RAISE NOTICE '  + Each capture group gets a distinct template variable';

    -- No path may repeat a template variable name (the actual defect).
    FOR v_path IN SELECT jsonb_object_keys(v_doc->'paths')
    LOOP
        SELECT ARRAY(SELECT m[1] FROM regexp_matches(v_path, '\{([^}]+)\}', 'g') AS m)
        INTO v_names;

        IF cardinality(v_names) IS DISTINCT FROM cardinality(ARRAY(SELECT DISTINCT unnest(v_names))) THEN
            RAISE EXCEPTION 'path % repeats a template variable: %', v_path, v_names;
        END IF;
    END LOOP;

    RAISE NOTICE '  + No path in the document repeats a template variable';

    -- Every {var} in a path is backed by a matching parameters entry.
    FOR v_path IN SELECT jsonb_object_keys(v_doc->'paths')
    LOOP
        SELECT ARRAY(SELECT m[1] FROM regexp_matches(v_path, '\{([^}]+)\}', 'g') AS m)
        INTO v_names;

        CONTINUE WHEN cardinality(v_names) = 0;

        FOR v_op IN SELECT value FROM jsonb_each(v_doc->'paths'->v_path)
        LOOP
            v_params := COALESCE(v_op->'parameters', '[]'::jsonb);

            SELECT bool_and(
                EXISTS (
                    SELECT 1 FROM jsonb_array_elements(v_params) p
                    WHERE p->>'name' = n
                      AND p->>'in' = 'path'
                      AND (p->'required')::boolean
                )
            )
            INTO v_declared
            FROM unnest(v_names) AS n;

            IF v_declared IS DISTINCT FROM true THEN
                RAISE EXCEPTION 'operation on % does not declare every path variable %; parameters: %',
                    v_path, v_names, v_params;
            END IF;
        END LOOP;
    END LOOP;

    RAISE NOTICE '  + Every path variable has a required in:path parameters entry';
    RAISE NOTICE '✓ OpenAPI multi-parameter tests passed';
END $$;

-- ============================================================================
-- Test: path-template registration derives regex and canonical_path correctly
-- ============================================================================

DO $$
DECLARE
    v_route record;
    v_response api.http_response;
    v_raised boolean;
BEGIN
    RAISE NOTICE '-> Testing path-template registration';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000004',
            'path', '/items/{itemId}',
            'httpMethod', '^GET$',
            'name', 'path_template_test',
            'requiresAuth', false,
            'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );

    SELECT canonical_path, address_regexp INTO v_route
    FROM api.rest_route WHERE route_name = 'path_template_test';

    IF v_route.canonical_path IS DISTINCT FROM '/items/{itemId}' THEN
        RAISE EXCEPTION 'canonical_path should be /items/{itemId}, got %', v_route.canonical_path;
    END IF;

    IF v_route.address_regexp IS DISTINCT FROM '^/items/([^/]+)$' THEN
        RAISE EXCEPTION 'address_regexp should be ^/items/([^/]+)$, got %', v_route.address_regexp;
    END IF;

    v_response := api.rest_invoke('GET', '/items/42', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION '/items/42 should match path-template route, got %', v_response.status_code;
    END IF;

    RAISE NOTICE '  + path template derives regex and canonical_path';

    v_raised := false;
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-0b11-4000-8000-000000000005',
                'httpMethod', '^GET$',
                'name', 'no_path_no_uri_test',
                'requiresAuth', false
            ),
            $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
        );
    EXCEPTION WHEN invalid_parameter_value THEN
        v_raised := true;
    END;

    IF v_raised IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'registering without path or uri should have been rejected';
    END IF;

    RAISE NOTICE '  + registration rejects handler with neither path nor uri';
    RAISE NOTICE '✓ path-template registration tests passed';
END $$;

-- ============================================================================
-- Test: the contract is cacheable (ETag / 304) and every response stamps the
-- catalog version. Caching an API contract is only safe if the client can tell
-- cheaply when it went stale — otherwise a cached route table serves a false 404
-- for a route a later deploy added.
-- ============================================================================

DO $$
DECLARE
    v_response api.http_response;
    v_etag text;
    v_etag_after text;
    v_version text;
BEGIN
    RAISE NOTICE '-> Testing OpenAPI cacheability (ETag / 304 / catalog version)';

    -- 200 carries a strong ETag and a revalidate directive.
    v_response := api.rest_invoke('GET', '/openapi.json');
    v_etag := api.header((v_response).headers, 'ETag');

    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: GET /openapi.json returned %', (v_response).status_code;
    END IF;
    IF v_etag IS NULL OR v_etag !~ '^"[0-9a-f]{64}"$' THEN
        RAISE EXCEPTION 'TEST FAILED: /openapi.json must return a strong quoted ETag, got %', v_etag;
    END IF;
    IF api.header((v_response).headers, 'Cache-Control') IS DISTINCT FROM 'no-cache' THEN
        RAISE EXCEPTION 'TEST FAILED: contract must be revalidated, got cache-control=%',
            api.header((v_response).headers, 'Cache-Control');
    END IF;

    RAISE NOTICE '  + GET /openapi.json returns a strong ETag and cache-control: no-cache';

    -- If-None-Match with that ETag → 304, empty body.
    v_response := api.rest_invoke('GET', '/openapi.json',
        extensions.hstore('if-none-match', v_etag));

    IF (v_response).status_code IS DISTINCT FROM 304 THEN
        RAISE EXCEPTION 'TEST FAILED: matching If-None-Match must return 304, got %',
            (v_response).status_code;
    END IF;
    IF COALESCE(octet_length((v_response).content), 0) IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'TEST FAILED: a 304 must carry no body, got % bytes',
            octet_length((v_response).content);
    END IF;

    RAISE NOTICE '  + If-None-Match with the current ETag returns 304 with an empty body';

    -- A stale tag must NOT short-circuit: the client gets the new document.
    v_response := api.rest_invoke('GET', '/openapi.json',
        extensions.hstore('if-none-match', '"0000000000000000000000000000000000000000000000000000000000000000"'));

    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: a stale If-None-Match must return 200, got %',
            (v_response).status_code;
    END IF;

    RAISE NOTICE '  + A stale If-None-Match returns the full document';

    -- Every response carries the catalog version, and it matches the ETag.
    v_response := api.rest_invoke('GET', '/openapi.json');
    v_version := api.header((v_response).headers, 'x-pgmi-catalog-version');
    IF v_version IS NULL OR '"' || v_version || '"' IS DISTINCT FROM v_etag THEN
        RAISE EXCEPTION 'TEST FAILED: x-pgmi-catalog-version (%) must match the ETag (%)',
            v_version, v_etag;
    END IF;

    -- ...including on an ordinary route the client was calling anyway. That is the
    -- point: staleness is learned passively, without polling the spec.
    v_response := api.rest_invoke('GET', '/health');
    IF api.header((v_response).headers, 'x-pgmi-catalog-version') IS DISTINCT FROM v_version THEN
        RAISE EXCEPTION 'TEST FAILED: an ordinary REST response must stamp the same catalog version';
    END IF;

    RAISE NOTICE '  + Every response stamps x-pgmi-catalog-version, matching the ETag';

    -- The ETag must change when the registry changes — and only then. A digest
    -- over handler def_hash would MISS this: the body is identical, only the uri
    -- changed.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000005',
            'uri', '^/etag-probe$', 'httpMethod', '^GET$',
            'name', 'etag_probe', 'requiresAuth', false, 'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
    );

    v_etag_after := api.header((api.rest_invoke('GET', '/openapi.json')).headers, 'ETag');
    IF v_etag_after = v_etag THEN
        RAISE EXCEPTION 'TEST FAILED: adding a route must change the ETag';
    END IF;

    RAISE NOTICE '  + Registering a route changes the ETag';

    -- Same handler body, different uri: the published contract changed, so the
    -- ETag must change too.
    v_etag := v_etag_after;
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000005',
            'uri', '^/etag-probe-renamed$', 'httpMethod', '^GET$',
            'name', 'etag_probe', 'requiresAuth', false, 'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
    );

    v_etag_after := api.header((api.rest_invoke('GET', '/openapi.json')).headers, 'ETag');
    IF v_etag_after = v_etag THEN
        RAISE EXCEPTION 'TEST FAILED: changing a route uri must change the ETag (an unchanged '
            'handler body must not be mistaken for an unchanged contract)';
    END IF;

    RAISE NOTICE '  + Changing only a route uri still changes the ETag';

    -- And it is stable when nothing changed: two reads, same tag.
    IF api.header((api.rest_invoke('GET', '/openapi.json')).headers, 'ETag') IS DISTINCT FROM v_etag_after THEN
        RAISE EXCEPTION 'TEST FAILED: the ETag must be stable across reads when the registry is unchanged';
    END IF;

    RAISE NOTICE '  + The ETag is stable while the registry is unchanged';
    RAISE NOTICE '✓ OpenAPI cacheability tests passed';
END $$;

DO $$
DECLARE
    v_response api.http_response;
    v_html text;
BEGIN
    RAISE NOTICE '-> Testing API explorer endpoint';

    v_response := api.rest_invoke('GET', '/docs');

    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'GET /docs returned %, expected 200', (v_response).status_code;
    END IF;

    v_html := convert_from((v_response).content, 'UTF8');

    IF coalesce(v_html, '') NOT LIKE '%api-reference%' THEN
        RAISE EXCEPTION 'GET /docs should contain Scalar API reference script tag';
    END IF;

    IF coalesce(v_html, '') NOT LIKE '%/openapi.json%' THEN
        RAISE EXCEPTION 'GET /docs should reference /openapi.json';
    END IF;

    RAISE NOTICE '  + GET /docs returns HTML with Scalar API reference';
    RAISE NOTICE '✓ API explorer tests passed';
END $$;
