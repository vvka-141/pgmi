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

    IF v_doc->>'openapi' != '3.1.0' THEN
        RAISE EXCEPTION 'openapi version should be 3.1.0, got %', v_doc->>'openapi';
    END IF;

    IF v_doc->'info'->>'title' IS NULL THEN
        RAISE EXCEPTION 'info.title should not be null';
    END IF;

    IF v_doc->'paths' IS NULL THEN
        RAISE EXCEPTION 'paths should not be null';
    END IF;

    IF v_doc->'components'->'securitySchemes'->'bearerAuth' IS NULL THEN
        RAISE EXCEPTION 'bearerAuth security scheme should be defined';
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

    IF v_paths->'/openapi.json'->'get'->>'operationId' != 'openapi_spec' THEN
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

    RAISE NOTICE '  + Auth handlers include security requirement';

    -- ========================================================================
    -- Handler served via REST gateway
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/openapi.json');

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'GET /openapi.json returned status %, expected 200', (v_response).status_code;
    END IF;

    RAISE NOTICE '  + GET /openapi.json returns 200 via gateway';

    -- ========================================================================
    -- Path conversion utilities
    -- ========================================================================

    IF api.openapi_path('^/hello(\?.*)?$') != '/hello' THEN
        RAISE EXCEPTION 'openapi_path failed: got %', api.openapi_path('^/hello(\?.*)?$');
    END IF;

    IF api.openapi_path('^/orgs/([^/]+)/users/(\d+)$') != '/orgs/{p1}/users/{p2}' THEN
        RAISE EXCEPTION 'undeclared path params must be named positionally, got %',
            api.openapi_path('^/orgs/([^/]+)/users/(\d+)$');
    END IF;

    IF api.openapi_path('^/orgs/([^/]+)/users/(\d+)$', ARRAY['orgId', 'userId'])
       != '/orgs/{orgId}/users/{userId}' THEN
        RAISE EXCEPTION 'declared path params must name the template variables, got %',
            api.openapi_path('^/orgs/([^/]+)/users/(\d+)$', ARRAY['orgId', 'userId']);
    END IF;

    IF api.openapi_methods('^GET$') != ARRAY['get'] THEN
        RAISE EXCEPTION 'openapi_methods failed for ^GET$';
    END IF;

    RAISE NOTICE '  + Path/method conversion utilities work';

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
            'requiredTransactionIsolation', 'serializable'
        ),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
    );

    v_doc := api.openapi_document();
    v_op := v_doc->'paths'->'/iso-openapi-test'->'get';

    IF v_op IS NULL THEN
        RAISE EXCEPTION 'iso-openapi-test route missing from spec';
    END IF;

    IF v_op->>'x-pgmi-transaction-isolation' IS DISTINCT FROM 'serializable' THEN
        RAISE EXCEPTION 'operation should advertise x-pgmi-transaction-isolation=serializable, got %',
            v_op->>'x-pgmi-transaction-isolation';
    END IF;

    RAISE NOTICE '  + Route with a floor advertises x-pgmi-transaction-isolation';

    -- A route with no floor must NOT carry the key (absent, not null).
    IF (v_doc->'paths'->'/openapi.json'->'get') ? 'x-pgmi-transaction-isolation' THEN
        RAISE EXCEPTION 'floorless route should omit x-pgmi-transaction-isolation';
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
            'uri', '^/orgs/([^/]+)/users/(\d+)$',
            'httpMethod', '^GET$',
            'name', 'multi_param_openapi_test',
            'requiresAuth', false,
            'autoLog', false,
            'pathParams', jsonb_build_array('orgId', 'userId')
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

    -- Declared names appear in the template, in order.
    IF v_doc->'paths'->'/orgs/{orgId}/users/{userId}' IS NULL THEN
        RAISE EXCEPTION 'declared multi-param route missing; paths: %',
            (SELECT string_agg(k, ', ') FROM jsonb_object_keys(v_doc->'paths') k);
    END IF;

    -- Undeclared groups fall back to positional names — still distinct.
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

        IF cardinality(v_names) != cardinality(ARRAY(SELECT DISTINCT unnest(v_names))) THEN
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

            IF NOT v_declared THEN
                RAISE EXCEPTION 'operation on % does not declare every path variable %; parameters: %',
                    v_path, v_names, v_params;
            END IF;
        END LOOP;
    END LOOP;

    RAISE NOTICE '  + Every path variable has a required in:path parameters entry';
    RAISE NOTICE '✓ OpenAPI multi-parameter tests passed';
END $$;

-- ============================================================================
-- Test: pathParams miscount is rejected at registration, not at request time
-- ============================================================================

DO $$
DECLARE
    v_raised boolean := false;
BEGIN
    RAISE NOTICE '-> Testing pathParams registration guard';

    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-0b11-4000-8000-000000000004',
                'uri', '^/orgs/([^/]+)/users/([^/]+)$',
                'httpMethod', '^GET$',
                'name', 'bad_path_params_test',
                'requiresAuth', false,
                'autoLog', false,
                'pathParams', jsonb_build_array('orgId')
            ),
            $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
        );
    EXCEPTION WHEN invalid_parameter_value THEN
        v_raised := true;
    END;

    IF NOT v_raised THEN
        RAISE EXCEPTION 'registering 1 pathParam for a 2-group route should have been rejected';
    END IF;

    RAISE NOTICE '  + pathParams/capture-group miscount rejected at registration';
    RAISE NOTICE '✓ pathParams guard tests passed';
END $$;

DO $$
DECLARE
    v_response api.http_response;
    v_html text;
BEGIN
    RAISE NOTICE '-> Testing API explorer endpoint';

    v_response := api.rest_invoke('GET', '/docs');

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'GET /docs returned %, expected 200', (v_response).status_code;
    END IF;

    v_html := convert_from((v_response).content, 'UTF8');

    IF v_html NOT LIKE '%api-reference%' THEN
        RAISE EXCEPTION 'GET /docs should contain Scalar API reference script tag';
    END IF;

    IF v_html NOT LIKE '%/openapi.json%' THEN
        RAISE EXCEPTION 'GET /docs should reference /openapi.json';
    END IF;

    RAISE NOTICE '  + GET /docs returns HTML with Scalar API reference';
    RAISE NOTICE '✓ API explorer tests passed';
END $$;
