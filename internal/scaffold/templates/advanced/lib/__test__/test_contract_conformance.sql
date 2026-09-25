-- ============================================================================
-- Test: every declared contract element is enforced (direction A), and the
-- MCP gateway serves exactly what it advertises (direction B)
-- ============================================================================
-- Declared-but-unenforced contracts were found one at a time by unrelated
-- reviews: an advertised auth scheme the gateway never read, requiresAuth
-- satisfied by any well-formed header, an outputSchema nothing obliged the
-- handler to meet, query parameters enforced but never published. Each
-- assertion here is driven by the registry, so a route or tool added later is
-- covered without editing this file. The fixtures only guarantee that every
-- kind of declaration occurs at least once, so no arm can pass vacuously.
--
-- REST direction B (the spec lists every route, and every listed operation
-- routes) is test_openapi_agreement.sql; this file does not repeat it.

-- Structural check of a JSON value against the subset of JSON Schema the
-- template's schemas use: type, required, properties, items. NULL means it
-- conforms.
CREATE FUNCTION pg_temp.schema_violation(p_schema jsonb, p_value jsonb, p_at text DEFAULT '$')
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    v_types text[];
    v_actual text := jsonb_typeof(p_value);
    v_key text;
    v_sub jsonb;
    v_found text;
BEGIN
    IF p_schema ? 'type' THEN
        v_types := CASE jsonb_typeof(p_schema->'type')
            WHEN 'array' THEN ARRAY(SELECT jsonb_array_elements_text(p_schema->'type'))
            ELSE ARRAY[p_schema->>'type'] END;
        IF NOT (v_actual = ANY (v_types)
                OR (v_actual = 'number' AND 'integer' = ANY (v_types) AND p_value::text !~ '[.eE]')) THEN
            RETURN format('%s is %s, schema wants %s', p_at, v_actual, array_to_string(v_types, '|'));
        END IF;
    END IF;

    IF v_actual = 'object' THEN
        FOR v_key IN SELECT jsonb_array_elements_text(COALESCE(p_schema->'required', '[]')) LOOP
            IF NOT p_value ? v_key THEN
                RETURN format('%s lacks required key %s', p_at, v_key);
            END IF;
        END LOOP;
        FOR v_key, v_sub IN SELECT key, value FROM jsonb_each(COALESCE(p_schema->'properties', '{}')) LOOP
            IF p_value ? v_key THEN
                v_found := pg_temp.schema_violation(v_sub, p_value->v_key, p_at || '.' || v_key);
                IF v_found IS NOT NULL THEN RETURN v_found; END IF;
            END IF;
        END LOOP;
    ELSIF v_actual = 'array' AND p_schema ? 'items' THEN
        SELECT pg_temp.schema_violation(p_schema->'items', e.value, p_at || '[' || (e.ord - 1) || ']')
        INTO v_found
        FROM jsonb_array_elements(p_value) WITH ORDINALITY AS e(value, ord)
        WHERE pg_temp.schema_violation(p_schema->'items', e.value, p_at) IS NOT NULL
        LIMIT 1;
        RETURN v_found;
    END IF;
    RETURN NULL;
END;
$$;

-- A URL the route accepts: its probe, plus every required query parameter.
CREATE FUNCTION pg_temp.conformance_url(p_probe text, p_query jsonb)
RETURNS text
LANGUAGE sql AS $$
    SELECT p_probe || COALESCE('?' || string_agg(q->>'name' || '=x', '&'), '')
    FROM jsonb_array_elements(p_query) q
    WHERE COALESCE((q->>'required')::boolean, false)
$$;

DO $$
BEGIN
    PERFORM membership.upsert_user('test', 'contract-conformance', 'contract-conformance@example.com');

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000001', 'path', '/conformance/read',
            'httpMethod', '^GET$', 'name', 'conformance_read', 'requiresAuth', true,
            'readOnly', true,
            'produces', jsonb_build_array('application/json'),
            'outputSchema', jsonb_build_object('type', 'object',
                'required', jsonb_build_array('n'),
                'properties', jsonb_build_object('n', jsonb_build_object('type', 'integer'))),
            'query', jsonb_build_array(jsonb_build_object('name', 'format', 'required', true))),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('n', 1)); END; $body$);

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000005', 'path', '/conformance/floor',
            'httpMethod', '^GET$', 'name', 'conformance_floor', 'requiresAuth', false,
            'minTransactionIsolation', 'serializable',
            'outputSchema', jsonb_build_object('type', 'object')),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000002', 'path', '/conformance/shape',
            'httpMethod', '^GET$', 'name', 'conformance_shape', 'requiresAuth', true,
            'produces', jsonb_build_array('application/json'),
            'outputSchema', jsonb_build_object('type', 'object',
                'required', jsonb_build_array('items'),
                'properties', jsonb_build_object('items', jsonb_build_object('type', 'array',
                    'items', jsonb_build_object('type', 'string'))))),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('items', jsonb_build_array('a', 'b'))); END; $body$);

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000003', 'type', 'tool',
            'name', 'conformance_tool', 'description', 'Conformance fixture',
            'requiresAuth', true,
            'outputSchema', jsonb_build_object('type', 'object',
                'required', jsonb_build_array('n'),
                'properties', jsonb_build_object('n', jsonb_build_object('type', 'integer')))),
        $body$
DECLARE v jsonb := jsonb_build_object('n', 7);
BEGIN
    RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text(v::text)), (request).request_id, false, v);
END;
        $body$);

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000004', 'type', 'tool',
            'name', 'conformance_ro_tool', 'description', 'Read-only conformance fixture',
            'requiresAuth', false, 'readOnly', true),
        $body$ BEGIN RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id); END; $body$);

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000006', 'type', 'tool',
            'name', 'conformance_floor_tool', 'description', 'Isolation-floor conformance fixture',
            'requiresAuth', false, 'minTransactionIsolation', 'serializable'),
        $body$ BEGIN RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id); END; $body$);
END $$;

-- ----------------------------------------------------------------------------
-- A: requiresAuth means no identity, and an identity that resolves to no user,
-- are both refused
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_route  record;
    v_status int;
    v_env    jsonb;
    v_rest   int := 0;
    v_mcp    int := 0;
BEGIN
    RAISE NOTICE '-> Contract conformance: requiresAuth';

    FOR v_route IN
        SELECT r.route_name, r.probe_path, r.query_contract,
               upper((api.openapi_methods(r.method_regexp))[1]) AS method
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE h.requires_auth AND r.probe_path IS DISTINCT FROM ''
    LOOP
        PERFORM set_config('auth.idp_subject', '', true);
        v_status := (api.rest_invoke(v_route.method, pg_temp.conformance_url(v_route.probe_path, v_route.query_contract),
                                     ''::extensions.hstore, NULL::bytea)).status_code;
        IF v_status IS DISTINCT FROM 401 THEN
            RAISE EXCEPTION 'CONFORMANCE: % declares requiresAuth but answered % with no identity', v_route.route_name, v_status;
        END IF;

        PERFORM set_config('auth.idp_subject', 'test|contract-conformance-ghost', true);
        v_status := (api.rest_invoke(v_route.method, pg_temp.conformance_url(v_route.probe_path, v_route.query_contract),
                                     extensions.hstore('x-user-id', 'test|contract-conformance-ghost'), NULL::bytea)).status_code;
        IF v_status IS DISTINCT FROM 401 THEN
            RAISE EXCEPTION 'CONFORMANCE: % declares requiresAuth but answered % to an identity that resolves to no user',
                v_route.route_name, v_status;
        END IF;
        v_rest := v_rest + 1;
    END LOOP;
    PERFORM set_config('auth.idp_subject', '', true);

    FOR v_route IN
        SELECT m.mcp_name FROM api.mcp_route m
        JOIN api.handler h ON h.object_id = m.handler_object_id AND h.deleted_at IS NULL
        WHERE h.requires_auth AND h.handler_type = 'mcp_tool'
    LOOP
        FOREACH v_env IN ARRAY ARRAY[NULL::jsonb, jsonb_build_object('user_id', 'test|contract-conformance-ghost')] LOOP
            v_env := (api.mcp_call_tool(v_route.mcp_name, '{}'::jsonb, v_env, '"c1"'::jsonb)).envelope;
            IF v_env->'error' IS NULL THEN
                RAISE EXCEPTION 'CONFORMANCE: MCP tool % declares requiresAuth but ran for an unresolved caller: %',
                    v_route.mcp_name, v_env;
            END IF;
        END LOOP;
        v_mcp := v_mcp + 1;
    END LOOP;

    IF v_rest = 0 OR v_mcp = 0 THEN
        RAISE EXCEPTION 'CONFORMANCE: requiresAuth arm exercised % REST route(s) and % MCP tool(s)', v_rest, v_mcp;
    END IF;
    RAISE NOTICE '  + % REST route(s) and % MCP tool(s) refuse unresolved callers', v_rest, v_mcp;
END $$;

-- ----------------------------------------------------------------------------
-- A: every header security scheme the spec advertises is one the gateway reads
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_scheme record;
    v_route  record;
    v_status int;
    v_checked int := 0;
BEGIN
    RAISE NOTICE '-> Contract conformance: advertised security schemes';

    SELECT r.route_name, r.probe_path, r.query_contract INTO v_route
    FROM api.rest_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
    WHERE h.requires_auth AND r.probe_path IS DISTINCT FROM '' AND r.route_name = 'conformance_shape';

    FOR v_scheme IN
        SELECT key AS name, value AS scheme
        FROM jsonb_each(api.openapi_document()->'components'->'securitySchemes')
        WHERE value->>'type' = 'apiKey' AND value->>'in' = 'header'
    LOOP
        PERFORM set_config('auth.idp_subject', '', true);
        PERFORM set_config('auth.user_id', '', true);
        v_status := (api.rest_invoke('GET', pg_temp.conformance_url(v_route.probe_path, v_route.query_contract),
                        extensions.hstore(lower(v_scheme.scheme->>'name'), 'test|contract-conformance'), NULL::bytea)).status_code;
        IF v_status IS NOT DISTINCT FROM 401 THEN
            RAISE EXCEPTION 'CONFORMANCE: the spec advertises % (header %), but a valid identity sent in it was refused 401',
                v_scheme.name, v_scheme.scheme->>'name';
        END IF;
        v_checked := v_checked + 1;
    END LOOP;
    PERFORM set_config('auth.idp_subject', '', true);

    IF v_checked = 0 THEN
        RAISE EXCEPTION 'CONFORMANCE: the spec advertises no header security scheme to check';
    END IF;
    RAISE NOTICE '  + % advertised header scheme(s) are read by the gateway', v_checked;
END $$;

-- ----------------------------------------------------------------------------
-- B: every request header the gateway maps into the session is either
-- advertised as a security scheme or named here as a trust-boundary header.
-- Those are headers a trusted proxy sets and clients must never send, so
-- publishing them would invite clients to forge them. Adding one to the
-- gateway without adding it here, or to the spec, fails this arm.
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_trust_boundary text[] := ARRAY['x-user-email', 'x-tenant-id', 'authorization'];
    v_read      text[];
    v_declared  text[];
    v_undeclared text;
BEGIN
    RAISE NOTICE '-> Contract conformance: headers read by the gateway';

    SELECT array_agg(DISTINCT m[1]) INTO v_read
    FROM regexp_matches(pg_get_functiondef('api.set_auth_context(extensions.hstore)'::regprocedure),
                        'p_headers->''([a-z0-9-]+)''', 'g') AS m;

    SELECT array_agg(lower(value->>'name')) INTO v_declared
    FROM jsonb_each(api.openapi_document()->'components'->'securitySchemes')
    WHERE value->>'in' = 'header';

    SELECT string_agg(h, ', ') INTO v_undeclared
    FROM unnest(v_read) h
    WHERE NOT h = ANY (COALESCE(v_declared, '{}') || v_trust_boundary);

    IF COALESCE(cardinality(v_read), 0) = 0 THEN
        RAISE EXCEPTION 'CONFORMANCE: found no headers read by api.set_auth_context, so this arm checked nothing';
    END IF;
    IF v_undeclared IS NOT NULL THEN
        RAISE EXCEPTION 'CONFORMANCE: the gateway reads header(s) % that are neither advertised nor listed as trust-boundary headers',
            v_undeclared;
    END IF;
    RAISE NOTICE '  + % header(s) read by the gateway are all declared or trust-boundary', cardinality(v_read);
END $$;

-- ----------------------------------------------------------------------------
-- A: produces and outputSchema hold for every successful GET, and for every
-- MCP tool that declares an outputSchema and takes no required arguments
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_route    record;
    v_response api.http_response;
    v_media    text;
    v_problem  text;
    v_env      jsonb;
    v_rest     int := 0;
    v_mcp      int := 0;
BEGIN
    RAISE NOTICE '-> Contract conformance: produces and outputSchema';

    PERFORM set_config('auth.idp_subject', 'test|contract-conformance', true);
    FOR v_route IN
        SELECT r.route_name, r.probe_path, r.query_contract, h.produces, h.output_json_schema::jsonb AS schema,
               h.min_transaction_isolation, h.read_only
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE 'get' = ANY (api.openapi_methods(r.method_regexp)) AND r.probe_path IS DISTINCT FROM ''
          AND h.min_transaction_isolation IS NULL AND NOT h.read_only
    LOOP
        v_response := api.rest_invoke('GET', pg_temp.conformance_url(v_route.probe_path, v_route.query_contract),
                                      extensions.hstore('x-user-id', 'test|contract-conformance'), NULL::bytea);
        CONTINUE WHEN (v_response).status_code NOT BETWEEN 200 AND 299 OR (v_response).status_code = 204;

        v_media := lower(btrim(split_part((v_response).headers->'content-type', ';', 1)));
        IF cardinality(v_route.produces) > 0 AND NOT v_media = ANY (
                SELECT lower(p) FROM unnest(v_route.produces) p) THEN
            RAISE EXCEPTION 'CONFORMANCE: % declares produces % but answered content-type %',
                v_route.route_name, v_route.produces, (v_response).headers->'content-type';
        END IF;

        IF v_route.schema IS NOT NULL AND v_media LIKE '%json' THEN
            v_problem := pg_temp.schema_violation(v_route.schema, api.content_json((v_response).content));
            IF v_problem IS NOT NULL THEN
                RAISE EXCEPTION 'CONFORMANCE: % answered a body its outputSchema rejects: %', v_route.route_name, v_problem;
            END IF;
        END IF;
        v_rest := v_rest + 1;
    END LOOP;

    FOR v_route IN
        SELECT m.mcp_name, h.output_json_schema::jsonb AS schema
        FROM api.mcp_route m
        JOIN api.handler h ON h.object_id = m.handler_object_id AND h.deleted_at IS NULL
        WHERE h.handler_type = 'mcp_tool' AND h.output_json_schema IS NOT NULL
          AND h.min_transaction_isolation IS NULL AND NOT h.read_only
          AND jsonb_array_length(COALESCE(h.input_json_schema::jsonb->'required', '[]')) = 0
    LOOP
        v_env := (api.mcp_call_tool(v_route.mcp_name, '{}'::jsonb,
                     jsonb_build_object('user_id', 'test|contract-conformance'), '"c2"'::jsonb)).envelope;
        CONTINUE WHEN v_env->'error' IS NOT NULL OR COALESCE((v_env->'result'->>'isError')::boolean, false);
        IF v_env->'result'->'structuredContent' IS NULL THEN
            RAISE EXCEPTION 'CONFORMANCE: MCP tool % declares outputSchema but returned no structuredContent', v_route.mcp_name;
        END IF;
        v_problem := pg_temp.schema_violation(v_route.schema, v_env->'result'->'structuredContent');
        IF v_problem IS NOT NULL THEN
            RAISE EXCEPTION 'CONFORMANCE: MCP tool % returned structuredContent its outputSchema rejects: %', v_route.mcp_name, v_problem;
        END IF;
        v_mcp := v_mcp + 1;
    END LOOP;
    PERFORM set_config('auth.idp_subject', '', true);

    IF v_rest = 0 OR v_mcp = 0 THEN
        RAISE EXCEPTION 'CONFORMANCE: response-shape arm checked % REST response(s) and % MCP result(s)', v_rest, v_mcp;
    END IF;
    RAISE NOTICE '  + % REST response(s) and % MCP result(s) match their declared shape', v_rest, v_mcp;
END $$;

-- ----------------------------------------------------------------------------
-- A: required query parameters are enforced
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_route  record;
    v_status int;
    v_checked int := 0;
BEGIN
    RAISE NOTICE '-> Contract conformance: declared query parameters';

    PERFORM set_config('auth.idp_subject', 'test|contract-conformance', true);
    FOR v_route IN
        SELECT r.route_name, r.probe_path, upper((api.openapi_methods(r.method_regexp))[1]) AS method
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE r.probe_path IS DISTINCT FROM '' AND EXISTS (
            SELECT 1 FROM jsonb_array_elements(r.query_contract) q WHERE COALESCE((q->>'required')::boolean, false))
    LOOP
        v_status := (api.rest_invoke(v_route.method, v_route.probe_path,
                        extensions.hstore('x-user-id', 'test|contract-conformance'), NULL::bytea)).status_code;
        IF v_status IS DISTINCT FROM 400 THEN
            RAISE EXCEPTION 'CONFORMANCE: % declares a required query parameter but answered % without it',
                v_route.route_name, v_status;
        END IF;
        v_checked := v_checked + 1;
    END LOOP;
    PERFORM set_config('auth.idp_subject', '', true);

    IF v_checked = 0 THEN
        RAISE EXCEPTION 'CONFORMANCE: no route declares a required query parameter';
    END IF;
    RAISE NOTICE '  + % route(s) refuse a request missing a required query parameter', v_checked;
END $$;

-- ----------------------------------------------------------------------------
-- A: readOnly and minTransactionIsolation are enforced. This transaction is
-- read-write at the default isolation, so every declared floor above it and
-- every readOnly route must be refused, not run.
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_route   record;
    v_content jsonb;
    v_env     jsonb;
    v_rank    int := CASE current_setting('transaction_isolation')
                        WHEN 'serializable' THEN 3 WHEN 'repeatable read' THEN 2 ELSE 1 END;
    v_checked int := 0;
BEGIN
    RAISE NOTICE '-> Contract conformance: transaction policy';

    IF v_rank = 3 OR current_setting('transaction_read_only') = 'on' THEN
        RAISE EXCEPTION 'CONFORMANCE: this arm needs a read-write transaction below serializable';
    END IF;

    PERFORM set_config('auth.idp_subject', 'test|contract-conformance', true);
    FOR v_route IN
        SELECT r.route_name, r.probe_path, r.query_contract, h.read_only, h.min_transaction_isolation,
               upper((api.openapi_methods(r.method_regexp))[1]) AS method,
               CASE WHEN CASE h.min_transaction_isolation
                             WHEN 'serializable' THEN 3 WHEN 'repeatable read' THEN 2 ELSE 1 END > v_rank
                    THEN 'pgmi.transaction_isolation_too_weak'
                    ELSE 'pgmi.transaction_read_only_required' END AS expected
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE r.probe_path IS DISTINCT FROM ''
          AND (h.read_only OR CASE h.min_transaction_isolation
                   WHEN 'serializable' THEN 3 WHEN 'repeatable read' THEN 2 ELSE 1 END > v_rank)
    LOOP
        v_content := api.content_json((api.rest_invoke(v_route.method,
            pg_temp.conformance_url(v_route.probe_path, v_route.query_contract),
            extensions.hstore('x-user-id', 'test|contract-conformance'), NULL::bytea)).content);
        IF v_content->>'code' IS DISTINCT FROM v_route.expected THEN
            RAISE EXCEPTION 'CONFORMANCE: % declares readOnly=% / floor % but ran in a read-write % transaction: %',
                v_route.route_name, v_route.read_only, v_route.min_transaction_isolation,
                current_setting('transaction_isolation'), v_content;
        END IF;
        v_checked := v_checked + 1;
    END LOOP;

    FOR v_route IN
        SELECT m.mcp_name,
               CASE WHEN CASE h.min_transaction_isolation
                             WHEN 'serializable' THEN 3 WHEN 'repeatable read' THEN 2 ELSE 1 END > v_rank
                    THEN 'pgmi.transaction_isolation_too_weak'
                    ELSE 'pgmi.transaction_read_only_required' END AS expected
        FROM api.mcp_route m
        JOIN api.handler h ON h.object_id = m.handler_object_id AND h.deleted_at IS NULL
        WHERE h.handler_type = 'mcp_tool'
          AND (h.read_only OR CASE h.min_transaction_isolation
                   WHEN 'serializable' THEN 3 WHEN 'repeatable read' THEN 2 ELSE 1 END > v_rank)
    LOOP
        v_env := (api.mcp_call_tool(v_route.mcp_name, '{}'::jsonb,
                     jsonb_build_object('user_id', 'test|contract-conformance'), '"c3"'::jsonb)).envelope;
        IF v_env->'error'->'data'->>'code' IS DISTINCT FROM v_route.expected THEN
            RAISE EXCEPTION 'CONFORMANCE: MCP tool % declares a transaction policy but ran outside it: %', v_route.mcp_name, v_env;
        END IF;
        v_checked := v_checked + 1;
    END LOOP;
    PERFORM set_config('auth.idp_subject', '', true);

    IF v_checked = 0 THEN
        RAISE EXCEPTION 'CONFORMANCE: no route or tool declares a transaction policy to check';
    END IF;
    RAISE NOTICE '  + % route(s) and tool(s) refuse a transaction weaker than they declare', v_checked;
END $$;

-- ----------------------------------------------------------------------------
-- A: a declared request body shape is enforced before the handler runs. Driven
-- by the registry: every POST/PUT/PATCH route whose inputSchema requires a key
-- must refuse an empty object with the gateway's own message.
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_route    record;
    v_response api.http_response;
    v_detail   text;
    v_checked  int := 0;
BEGIN
    RAISE NOTICE '-> Contract conformance: declared request bodies';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0388-4000-8000-000000000001', 'path', '/conformance/write',
            'httpMethod', '^POST$', 'name', 'conformance_write', 'requiresAuth', false,
            'outputSchema', jsonb_build_object('type', 'object'),
            'inputSchema', jsonb_build_object('type', 'object',
                'required', jsonb_build_array('qty'),
                'properties', jsonb_build_object('qty', jsonb_build_object('type', 'integer')))),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$);

    PERFORM set_config('auth.idp_subject', 'test|contract-conformance', true);
    FOR v_route IN
        SELECT r.route_name, r.probe_path, r.query_contract, m.method
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        CROSS JOIN LATERAL (
            SELECT upper(x) AS method FROM unnest(api.openapi_methods(r.method_regexp)) x
            WHERE upper(x) IN ('POST', 'PUT', 'PATCH') LIMIT 1) m
        WHERE r.probe_path IS DISTINCT FROM ''
          AND jsonb_array_length(COALESCE(h.input_json_schema::jsonb->'required', '[]')) > 0
          AND h.min_transaction_isolation IS NULL AND NOT h.read_only
    LOOP
        v_response := api.rest_invoke(v_route.method, pg_temp.conformance_url(v_route.probe_path, v_route.query_contract),
            extensions.hstore(ARRAY['x-user-id', 'test|contract-conformance', 'content-type', 'application/json']),
            convert_to('{}', 'UTF8'));
        v_detail := api.content_json((v_response).content)->>'detail';
        IF (v_response).status_code IS DISTINCT FROM 400 OR coalesce(v_detail, '') NOT LIKE 'Invalid request body:%' THEN
            RAISE EXCEPTION 'CONFORMANCE: % declares required body keys but the gateway let {} through (% %)',
                v_route.route_name, (v_response).status_code, v_detail;
        END IF;
        v_checked := v_checked + 1;
    END LOOP;

    IF v_checked = 0 THEN
        RAISE EXCEPTION 'CONFORMANCE: no route declares required body keys';
    END IF;

    v_response := api.rest_invoke('POST', '/conformance/write',
        extensions.hstore('content-type', 'application/json'), convert_to('{"qty": "many"}', 'UTF8'));
    IF (v_response).status_code IS DISTINCT FROM 400 THEN
        RAISE EXCEPTION 'CONFORMANCE: a body property of the wrong type must be a 400, got %', (v_response).status_code;
    END IF;
    v_response := api.rest_invoke('POST', '/conformance/write',
        extensions.hstore('content-type', 'application/json'), convert_to('not json', 'UTF8'));
    IF (v_response).status_code IS DISTINCT FROM 400 THEN
        RAISE EXCEPTION 'CONFORMANCE: a body that is not JSON must be a 400, got %', (v_response).status_code;
    END IF;
    v_response := api.rest_invoke('POST', '/conformance/write',
        extensions.hstore('content-type', 'application/json'), convert_to('{"qty": 3}', 'UTF8'));
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'CONFORMANCE: a conforming body must reach the handler, got %', (v_response).status_code;
    END IF;
    PERFORM set_config('auth.idp_subject', '', true);

    RAISE NOTICE '  + % route(s) refuse a body missing a required key; types and non-JSON refused too', v_checked;
END $$;

-- ----------------------------------------------------------------------------
-- A: declared query value shapes are enforced
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_case record;
    v_status int;
BEGIN
    RAISE NOTICE '-> Contract conformance: declared query value shapes';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0388-4000-8000-000000000002', 'path', '/conformance/list',
            'httpMethod', '^GET$', 'name', 'conformance_list', 'requiresAuth', false,
            'outputSchema', jsonb_build_object('type', 'object'),
            'query', jsonb_build_array(
                jsonb_build_object('name', 'limit', 'schema', jsonb_build_object('type', 'integer')),
                jsonb_build_object('name', 'ratio', 'schema', jsonb_build_object('type', 'number')),
                jsonb_build_object('name', 'flag', 'schema', jsonb_build_object('type', 'boolean')),
                jsonb_build_object('name', 'format', 'schema', jsonb_build_object('type', 'string',
                    'enum', jsonb_build_array('json', 'csv'))))),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$);

    FOR v_case IN SELECT * FROM (VALUES
        ('?limit=10&ratio=-1.5e2&flag=true&format=csv', 200),
        ('?limit=ten', 400),
        ('?ratio=abc', 400),
        ('?flag=yes', 400),
        ('?format=pdf', 400)) AS c(query, want)
    LOOP
        v_status := (api.rest_invoke('GET', '/conformance/list' || v_case.query)).status_code;
        IF v_status IS DISTINCT FROM v_case.want THEN
            RAISE EXCEPTION 'CONFORMANCE: GET /conformance/list% answered %, want %', v_case.query, v_status, v_case.want;
        END IF;
    END LOOP;

    RAISE NOTICE '  + integer, number, boolean and enum query values are checked';
END $$;

-- ----------------------------------------------------------------------------
-- A: two routes that can claim one address cannot both register
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_refused boolean := false;
BEGIN
    RAISE NOTICE '-> Contract conformance: cross-overlapping routes';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0388-4000-8000-000000000003', 'path', '/conformance/shops/{shop}/orders',
            'httpMethod', '^GET$', 'name', 'conformance_shop_orders', 'requiresAuth', false,
            'outputSchema', jsonb_build_object('type', 'object')),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);

    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object('id', 'ffffffff-0388-4000-8000-000000000004', 'path', '/conformance/shops/main/{section}',
                'httpMethod', '^GET$', 'name', 'conformance_main_section', 'requiresAuth', false,
                'outputSchema', jsonb_build_object('type', 'object')),
            $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
    EXCEPTION WHEN invalid_parameter_value THEN
        v_refused := true;
    END;

    IF v_refused IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'CONFORMANCE: /conformance/shops/{shop}/orders and /conformance/shops/main/{section} both registered, though /conformance/shops/main/orders matches both';
    END IF;

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0388-4000-8000-000000000005', 'path', '/conformance/shops/{shop}/refunds',
            'httpMethod', '^GET$', 'name', 'conformance_shop_refunds', 'requiresAuth', false,
            'outputSchema', jsonb_build_object('type', 'object')),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);

    RAISE NOTICE '  + cross-overlapping routes are refused; disjoint siblings still register';
END $$;

-- ----------------------------------------------------------------------------
-- B: the MCP listings and the MCP registry agree, for an authenticated caller
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_missing   text;
    v_phantom   text;
    v_listed    int;
    v_env       jsonb;
    v_name      text;
BEGIN
    RAISE NOTICE '-> Contract conformance: MCP listings agree with the registry';

    PERFORM set_config('auth.idp_subject', 'test|contract-conformance', true);
    PERFORM set_config('auth.user_id', 'test|contract-conformance', true);

    WITH listed AS (
        SELECT 'mcp_tool' AS kind, t->>'name' AS name FROM jsonb_array_elements(api.mcp_list_tools()->'tools') t
        UNION ALL
        SELECT 'mcp_resource', t->>'name' FROM jsonb_array_elements(api.mcp_list_resources()->'resources') t
        UNION ALL
        SELECT 'mcp_resource', t->>'name' FROM jsonb_array_elements(api.mcp_list_resource_templates()->'resourceTemplates') t
        UNION ALL
        SELECT 'mcp_prompt', t->>'name' FROM jsonb_array_elements(api.mcp_list_prompts()->'prompts') t
    ), registered AS (
        SELECT h.handler_type::text AS kind, m.mcp_name AS name
        FROM api.mcp_route m
        JOIN api.handler h ON h.object_id = m.handler_object_id AND h.deleted_at IS NULL
    )
    SELECT (SELECT string_agg(kind || ':' || name, ', ') FROM (SELECT * FROM registered EXCEPT SELECT * FROM listed) x),
           (SELECT string_agg(kind || ':' || name, ', ') FROM (SELECT * FROM listed EXCEPT SELECT * FROM registered) x),
           (SELECT count(*) FROM listed)
    INTO v_missing, v_phantom, v_listed;

    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'CONFORMANCE: registered but not advertised to an authenticated caller: %', v_missing;
    END IF;
    IF v_phantom IS NOT NULL THEN
        RAISE EXCEPTION 'CONFORMANCE: advertised but not registered: %', v_phantom;
    END IF;
    IF v_listed = 0 THEN
        RAISE EXCEPTION 'CONFORMANCE: the MCP listings were empty, so agreement proved nothing';
    END IF;

    -- Every advertised tool resolves in the dispatcher: whatever the tool
    -- then answers, it must not be "not found".
    FOR v_name IN SELECT t->>'name' FROM jsonb_array_elements(api.mcp_list_tools()->'tools') t LOOP
        v_env := (api.mcp_call_tool(v_name, '{}'::jsonb,
                     jsonb_build_object('user_id', 'test|contract-conformance'), '"c4"'::jsonb)).envelope;
        IF v_env->'error'->>'message' ILIKE '%not found%' THEN
            RAISE EXCEPTION 'CONFORMANCE: tools/list advertises % but tools/call cannot find it', v_name;
        END IF;
    END LOOP;

    PERFORM set_config('auth.idp_subject', '', true);
    PERFORM set_config('auth.user_id', '', true);
    RAISE NOTICE '  + % advertised MCP entr(ies) match the registry, and every tool dispatches', v_listed;
    RAISE NOTICE '✓ Contract conformance tests passed';
END $$;
