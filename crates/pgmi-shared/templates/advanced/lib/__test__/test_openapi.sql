-- ============================================================================
-- Test: the generated OpenAPI 3.1 document describes every route correctly,
-- revalidates cheaply, and never serves one caller's document to another
-- ============================================================================

-- ############################################################################
-- DOCUMENT - the generated document is OpenAPI 3.1 and describes every route
-- ############################################################################
DO $$
DECLARE
    v_doc jsonb;
    v_paths jsonb;
    v_response api.http_response;
BEGIN
    PERFORM pg_temp.test_login('test', 'openapi-reader', 'openapi-reader@example.test', NULL, true);
    PERFORM pg_temp.test_act_as('test|openapi-reader');
    v_doc := api.openapi_document();
    PERFORM pg_temp.test_act_as(NULL);

    -- Basic structure
    IF v_doc->>'openapi' IS DISTINCT FROM '3.1.0' THEN
        RAISE EXCEPTION 'openapi version should be 3.1.0, got %', v_doc->>'openapi';
    END IF;

    IF v_doc->'info'->>'title' IS NULL THEN
        RAISE EXCEPTION 'info.title should not be null';
    END IF;

    IF v_doc->'paths' IS NULL THEN
        RAISE EXCEPTION 'paths should not be null';
    END IF;

    IF v_doc->'components'->'securitySchemes'->'loginSession' IS DISTINCT FROM
           v_doc->'components'->'securitySchemes'->'loginSession'
           || '{"type": "apiKey", "in": "cookie", "name": "__Host-session"}'::jsonb
       OR v_doc->'components'->'securitySchemes'->'organizationSession' IS DISTINCT FROM
           v_doc->'components'->'securitySchemes'->'organizationSession'
           || '{"type": "apiKey", "in": "header", "name": "x-organization-session"}'::jsonb
       OR (SELECT count(*) FROM jsonb_object_keys(v_doc->'components'->'securitySchemes')) IS DISTINCT FROM 2::bigint THEN
        RAISE EXCEPTION 'the security schemes should be exactly the session cookie and the organization header, got %',
            v_doc->'components'->'securitySchemes';
    END IF;

    -- A write carrying the session cookie needs X-CSRF-Token: required on
    -- every unsafe operation of a non-public route, optional on a public one.
    IF EXISTS (
        SELECT FROM jsonb_each(v_doc->'paths') p(path, ops)
        CROSS JOIN LATERAL jsonb_each(p.ops) o(method, op)
        WHERE (o.method IN ('post', 'put', 'patch', 'delete'))
              IS DISTINCT FROM EXISTS (SELECT FROM jsonb_array_elements(o.op->'parameters') prm
                                       WHERE prm->>'name' = 'X-CSRF-Token' AND prm->>'in' = 'header'
                                         AND (prm->'required') = to_jsonb(o.op ? 'security')))
    THEN
        RAISE EXCEPTION 'every unsafe operation, and only those, should declare the X-CSRF-Token header, required when it has security';
    END IF;
    IF NOT EXISTS (SELECT FROM jsonb_each(v_doc->'paths') p(path, ops)
                   CROSS JOIN LATERAL jsonb_each(p.ops) o(method, op)
                   WHERE o.method IN ('post', 'put', 'patch', 'delete')) THEN
        RAISE EXCEPTION 'the document should hold an unsafe operation for the X-CSRF-Token check to mean anything';
    END IF;

    -- REST routes appear in paths
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

    -- Auth requirement: openapi_spec has no security (access public)
    IF v_paths->'/openapi.json'->'get'->'security' IS NOT NULL THEN
        RAISE EXCEPTION '/openapi.json should not have security requirement';
    END IF;

    -- Non-public handlers have security requirement
    IF v_paths->'/me'->'get'->'security' IS DISTINCT FROM '[{"loginSession": []}]'::jsonb THEN
        RAISE EXCEPTION '/me should require the login session, got %', v_paths->'/me'->'get'->'security';
    END IF;

    -- Response codes: every operation documents gateway error responses
    IF (v_paths->'/openapi.json'->'get'->'responses' ? '404') IS NOT TRUE THEN
        RAISE EXCEPTION 'operation should document 404 response';
    END IF;
    IF (v_paths->'/openapi.json'->'get'->'responses' ? '405') IS NOT TRUE THEN
        RAISE EXCEPTION 'operation should document 405 response';
    END IF;

    IF v_paths->'/me' IS NOT NULL THEN
        IF (v_paths->'/me'->'get'->'responses' ? '401') IS NOT TRUE THEN
            RAISE EXCEPTION 'authenticated operation should document 401 response';
        END IF;
    END IF;

    IF v_paths->'/openapi.json'->'get'->'responses' ? '401' THEN
        RAISE EXCEPTION 'unauthenticated operation should NOT document 401 response';
    END IF;

    -- Handler served via REST gateway
    v_response := api.rest_invoke('GET', '/openapi.json');

    PERFORM pg_temp.expect_status('GET /openapi.json', v_response, 200);

    -- Path template and method conversion utilities
    IF api.path_template_to_regex('/hello') IS DISTINCT FROM '^/hello$' THEN
        RAISE EXCEPTION 'path_template_to_regex(/hello) failed: got %',
            api.path_template_to_regex('/hello');
    END IF;

    IF api.path_template_to_regex('/orgs/{orgId}/users/{userId}') IS DISTINCT FROM '^/orgs/([^/]+)/users/([^/]+)$' THEN
        RAISE EXCEPTION 'path_template_to_regex multi-param failed: got %',
            api.path_template_to_regex('/orgs/{orgId}/users/{userId}');
    END IF;

    IF api.path_template_param_spec('/orgs/{orgId:uuid}/users/{userId:int}')
       IS DISTINCT FROM '[{"name": "orgId", "type": "uuid"}, {"name": "userId", "type": "int"}]'::jsonb THEN
        RAISE EXCEPTION 'path_template_param_spec failed: got %',
            api.path_template_param_spec('/orgs/{orgId:uuid}/users/{userId:int}');
    END IF;

    IF api.openapi_methods('^GET$') IS DISTINCT FROM ARRAY['get'] THEN
        RAISE EXCEPTION 'openapi_methods failed for ^GET$';
    END IF;

    -- Schema coverage: REST examples have output schemas
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

    -- OpenAPI document includes declared schemas (not fallback object)
    IF (v_paths->'/hello'->'get'->'responses'->'200'->'content'->'application/json'->'schema'->>'type' = 'object') IS NOT FALSE
       AND v_paths->'/hello'->'get'->'responses'->'200'->'content'->'application/json'->'schema'->'properties' IS NULL THEN
        RAISE EXCEPTION '/hello response schema should have properties (not bare object fallback)';
    END IF;

    IF NOT EXISTS (SELECT FROM jsonb_array_elements(coalesce(v_paths->'/hello'->'get'->'parameters', '[]')) p
                   WHERE p->>'name' = 'name' AND p->>'in' = 'query' AND p->'schema'->>'type' = 'string') THEN
        RAISE EXCEPTION 'TEST FAILED: GET /hello must declare its optional query parameter name, got %',
            v_paths->'/hello'->'get'->'parameters';
    END IF;
END $$;

-- ############################################################################
-- ISOLATION - a route's isolation floor is advertised as an extension
-- ############################################################################
DO $$
DECLARE
    v_doc jsonb;
    v_op jsonb;
BEGIN
    -- A route that declares an isolation floor must advertise it in the spec so
    -- a preloading client can open the transaction at the right level on the
    -- FIRST call, instead of learning it reactively from a 428.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000001',
            'path', '/iso-openapi-test',
            'httpMethod', '^GET$',
            'name', 'iso_openapi_test',
            'access', 'public',
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

    -- A route with no floor must NOT carry the key (absent, not null).
    IF (v_doc->'paths'->'/openapi.json'->'get') ? 'x-pgmi-min-transaction-isolation' THEN
        RAISE EXCEPTION 'floorless route should omit x-pgmi-min-transaction-isolation';
    END IF;
END $$;

-- ############################################################################
-- MULTI-PARAMETER - routes with several path variables emit valid OpenAPI
-- A path may not repeat a template variable name. Every variable must be
-- declared in the operation's parameters.
-- ############################################################################
DO $$
DECLARE
    v_doc jsonb;
    v_op jsonb;
    v_params jsonb;
    v_path text;
    v_names text[];
    v_declared boolean;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000002',
            'path', '/orgs/{orgId}/users/{userId}',
            'httpMethod', '^GET$',
            'name', 'multi_param_openapi_test',
            'access', 'public',
            'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
    );

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000003',
            'path', '/things/{thingId}/parts/{partId}',
            'httpMethod', '^GET$',
            'name', 'second_multi_param_openapi_test',
            'access', 'public',
            'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
    );

    v_doc := api.openapi_document();

    IF v_doc->'paths'->'/orgs/{orgId}/users/{userId}' IS NULL THEN
        RAISE EXCEPTION 'declared multi-param route missing; paths: %',
            (SELECT string_agg(k, ', ') FROM jsonb_object_keys(v_doc->'paths') k);
    END IF;

    IF v_doc->'paths'->'/things/{thingId}/parts/{partId}' IS NULL THEN
        RAISE EXCEPTION 'second multi-param route missing; paths: %',
            (SELECT string_agg(k, ', ') FROM jsonb_object_keys(v_doc->'paths') k);
    END IF;

    -- No path may repeat a template variable name (the actual defect).
    FOR v_path IN SELECT jsonb_object_keys(v_doc->'paths')
    LOOP
        SELECT ARRAY(SELECT m[1] FROM regexp_matches(v_path, '\{([^}]+)\}', 'g') AS m)
        INTO v_names;

        IF cardinality(v_names) IS DISTINCT FROM cardinality(ARRAY(SELECT DISTINCT unnest(v_names))) THEN
            RAISE EXCEPTION 'path % repeats a template variable: %', v_path, v_names;
        END IF;
    END LOOP;

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
END $$;

-- ############################################################################
-- PATH TEMPLATES - registration derives the regex and canonical_path
-- ############################################################################
DO $$
DECLARE
    v_route record;
    v_response api.http_response;
    v_raised boolean;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000004',
            'path', '/items/{itemId}',
            'httpMethod', '^GET$',
            'name', 'path_template_test',
            'access', 'public',
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
    PERFORM pg_temp.expect_status('/items/42 should match path-template route', v_response, 200);

    v_raised := false;
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-0b11-4000-8000-000000000005',
                'httpMethod', '^GET$',
                'name', 'no_path_test',
                'access', 'public'
            ),
            $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
        );
    EXCEPTION WHEN invalid_parameter_value THEN
        v_raised := true;
    END;

    IF v_raised IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'registering without path should have been rejected';
    END IF;
END $$;

-- ############################################################################
-- CACHING - the contract is cacheable (ETag / 304) and every response stamps
-- the catalog version
-- Caching an API contract is only safe if the client can tell
-- cheaply when it went stale — otherwise a cached route table serves a false 404
-- for a route a later deploy added.
-- ############################################################################
DO $$
DECLARE
    v_response api.http_response;
    v_etag text;
    v_etag_after text;
    v_version text;
BEGIN
    -- 200 carries a strong ETag and a revalidate directive.
    v_response := api.rest_invoke('GET', '/openapi.json');
    v_etag := api.header((v_response).headers, 'ETag');

    PERFORM pg_temp.expect_status('GET /openapi.json', v_response, 200);
    IF v_etag IS NULL OR (v_etag !~ '^"[0-9a-f]{64}-anon"$') IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: /openapi.json must return a strong quoted ETag, got %', v_etag;
    END IF;
    IF api.header((v_response).headers, 'Cache-Control') IS DISTINCT FROM 'no-cache' THEN
        RAISE EXCEPTION 'TEST FAILED: contract must be revalidated, got cache-control=%',
            api.header((v_response).headers, 'Cache-Control');
    END IF;

    -- If-None-Match with that ETag → 304, empty body.
    v_response := api.rest_invoke('GET', '/openapi.json',
        extensions.hstore('if-none-match', v_etag));

    PERFORM pg_temp.expect_status('matching If-None-Match must return 304', v_response, 304);
    IF COALESCE(octet_length((v_response).content), 0) IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'TEST FAILED: a 304 must carry no body, got % bytes',
            octet_length((v_response).content);
    END IF;

    -- A stale tag must NOT short-circuit: the client gets the new document.
    v_response := api.rest_invoke('GET', '/openapi.json',
        extensions.hstore('if-none-match', '"0000000000000000000000000000000000000000000000000000000000000000"'));

    PERFORM pg_temp.expect_status('a stale If-None-Match must return 200', v_response, 200);

    -- Every response carries the catalog version, and the ETag is that version
    -- plus the caller class.
    v_response := api.rest_invoke('GET', '/openapi.json');
    v_version := api.header((v_response).headers, 'x-pgmi-catalog-version');
    IF v_version IS NULL OR '"' || v_version || '-anon"' IS DISTINCT FROM v_etag THEN
        RAISE EXCEPTION 'TEST FAILED: x-pgmi-catalog-version (%) must match the ETag (%)',
            v_version, v_etag;
    END IF;

    -- ...including on an ordinary route the client was calling anyway. That is the
    -- point: staleness is learned passively, without polling the spec.
    v_response := api.rest_invoke('GET', '/health');
    IF api.header((v_response).headers, 'x-pgmi-catalog-version') IS DISTINCT FROM v_version THEN
        RAISE EXCEPTION 'TEST FAILED: an ordinary REST response must stamp the same catalog version';
    END IF;

    -- The ETag must change when the registry changes — and only then. A digest
    -- over handler def_hash would MISS this: the body is identical, only the uri
    -- changed.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000005',
            'path', '/etag-probe', 'httpMethod', '^GET$',
            'name', 'etag_probe', 'access', 'public', 'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
    );

    v_etag_after := api.header((api.rest_invoke('GET', '/openapi.json')).headers, 'ETag');
    IF (v_etag_after = v_etag) IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: adding a route must change the ETag';
    END IF;

    -- Same handler body, different uri: the published contract changed, so the
    -- ETag must change too.
    v_etag := v_etag_after;
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0b11-4000-8000-000000000005',
            'path', '/etag-probe-renamed', 'httpMethod', '^GET$',
            'name', 'etag_probe', 'access', 'public', 'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$
    );

    v_etag_after := api.header((api.rest_invoke('GET', '/openapi.json')).headers, 'ETag');
    IF (v_etag_after = v_etag) IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: changing a route uri must change the ETag (an unchanged '
            'handler body must not be mistaken for an unchanged contract)';
    END IF;

    -- And it is stable when nothing changed: two reads, same tag.
    IF api.header((api.rest_invoke('GET', '/openapi.json')).headers, 'ETag') IS DISTINCT FROM v_etag_after THEN
        RAISE EXCEPTION 'TEST FAILED: the ETag must be stable across reads when the registry is unchanged';
    END IF;
END $$;

-- ############################################################################
-- EXPLORER - GET /docs serves the API reference page over openapi.json
-- ############################################################################
DO $$
DECLARE
    v_response api.http_response;
    v_html text;
BEGIN
    v_response := api.rest_invoke('GET', '/docs');

    PERFORM pg_temp.expect_status('GET /docs', v_response, 200);

    v_html := convert_from((v_response).content, 'UTF8');

    IF coalesce(v_html, '') NOT LIKE '%api-reference%' THEN
        RAISE EXCEPTION 'GET /docs should contain Scalar API reference script tag';
    END IF;

    IF coalesce(v_html, '') NOT LIKE '%openapi.json%' THEN
        RAISE EXCEPTION 'GET /docs should reference openapi.json';
    END IF;
END $$;

-- ############################################################################
-- VALIDATOR - an ETag carries the caller class
-- The document is filtered by caller, and the 304 is decided inside the
-- handler, where Vary cannot help: a validator obtained while authenticated
-- must not revalidate an anonymous request, or the anonymous caller keeps the
-- authenticated document, admin routes included.
-- ############################################################################
DO $$
DECLARE
    v_response api.http_response;
    v_etag     text;
    v_html     text;
BEGIN
    PERFORM pg_temp.test_login('test', 'openapi-etag', 'openapi-etag@example.test', NULL, true);
    v_response := api.rest_invoke('GET', '/openapi.json',
        pg_temp.test_session_headers('test|openapi-etag'), NULL::bytea);
    v_etag := (v_response).headers->'etag';
    IF v_etag IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: /openapi.json must carry an ETag';
    END IF;

    v_response := api.rest_invoke('GET', '/openapi.json',
        extensions.hstore('if-none-match', v_etag), NULL::bytea);
    PERFORM pg_temp.expect_status('an authenticated ETag must not revalidate an anonymous request', v_response, 200);

    v_response := api.rest_invoke('GET', '/openapi.json',
        extensions.hstore('if-none-match', (v_response).headers->'etag'), NULL::bytea);
    PERFORM pg_temp.expect_status('an anonymous ETag must still revalidate an anonymous request', v_response, 304);

    v_html := convert_from((api.rest_invoke('GET', '/docs')).content, 'UTF8');
    IF (v_html !~ 'api-reference@[0-9]+\.[0-9]+\.[0-9]+/') IS NOT FALSE OR (v_html !~ 'integrity="sha384-') IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: /docs must load a version-pinned script with SRI';
    END IF;

    IF (v_html !~ 'data-url="openapi\.json"') IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: /docs must name openapi.json relative to itself, so it works under any mount prefix';
    END IF;
    IF api.openapi_document()->'servers' IS DISTINCT FROM '[{"url": "/api"}]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: servers must name the gateway mount (api_base_path, /api by default), got %',
            api.openapi_document()->'servers';
    END IF;
END $$;
