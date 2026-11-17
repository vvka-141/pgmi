-- ============================================================================
-- Test: every declared contract element is enforced (direction A), and the
-- MCP gateway serves exactly what it advertises (direction B)
-- ============================================================================
-- A contract can be declared and not enforced: an advertised auth scheme the
-- gateway never reads, declared access satisfied by any well-formed header, an
-- outputSchema nothing obliges the handler to meet, query parameters enforced
-- but never published. Each assertion here is driven by the registry, so a route or tool added later is
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
CREATE FUNCTION pg_temp.conformance_url(p_probe text, p_query_schema jsonb)
RETURNS text
LANGUAGE sql AS $$
    SELECT p_probe || COALESCE('?' || string_agg(r || '=' || COALESCE(v.p->'enum'->>0,
               CASE v.p->>'type' WHEN 'integer' THEN COALESCE(v.p->>'minimum', '1')
                                 WHEN 'number' THEN COALESCE(v.p->>'minimum', '1')
                                 WHEN 'boolean' THEN 'true'
                                 ELSE CASE v.p->>'format' WHEN 'uuid' THEN '00000000-0000-4000-8000-000000000000'
                                                          WHEN 'date' THEN '2026-01-01'
                                                          WHEN 'date-time' THEN '2026-01-01T00:00:00Z'
                                                          ELSE 'x' END END), '&'), '')
    FROM jsonb_array_elements_text(COALESCE(p_query_schema->'required', '[]')) r
    CROSS JOIN LATERAL (SELECT CASE WHEN p_query_schema->'properties'->r->>'type' = 'array'
                                    THEN p_query_schema->'properties'->r->'items'
                                    ELSE p_query_schema->'properties'->r END AS p) v
$$;

-- Session headers for a caller who may call every route: a platform operator
-- with a second factor, in an organization it owns.
CREATE FUNCTION pg_temp.conformance_caller()
RETURNS extensions.hstore
LANGUAGE plpgsql AS $$
DECLARE
    v_caller uuid := pg_temp.test_person('test|contract-conformance');
BEGIN
    PERFORM pg_temp.test_act_as(NULL);
    IF current_setting('test.conformance_operator', true) IS DISTINCT FROM 'yes' THEN
        PERFORM pg_temp.test_operator(v_caller);
        PERFORM set_config('test.conformance_operator', 'yes', true);
    END IF;
    RETURN pg_temp.test_session_headers('test|contract-conformance',
               coalesce(pg_temp.test_organization('conformance-org'),
                        (membership.fn_create_organization_with_owner('Conformance', 'conformance-org', v_caller)).object_id),
               ARRAY['pwd', 'mfa']);
END $$;

-- An MCP context for the same caller: a connection in the organization it owns.
CREATE FUNCTION pg_temp.conformance_mcp_context()
RETURNS jsonb
LANGUAGE plpgsql AS $$
DECLARE
    v_caller uuid := pg_temp.test_person('test|contract-conformance');
BEGIN
    RETURN pg_temp.test_mcp_context('test|contract-conformance',
               coalesce(pg_temp.test_organization('conformance-org'),
                        (membership.fn_create_organization_with_owner('Conformance', 'conformance-org', v_caller)).object_id));
END $$;

DO $$
BEGIN
    PERFORM pg_temp.test_login('test', 'contract-conformance', 'contract-conformance@example.test', NULL, true);

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000001', 'path', '/conformance/read',
            'httpMethod', '^GET$', 'name', 'conformance_read', 'access', 'authenticated',
            'readOnly', true, 'autoLog', false,
            'produces', jsonb_build_array('application/json'),
            'outputSchema', jsonb_build_object('type', 'object',
                'required', jsonb_build_array('n'),
                'properties', jsonb_build_object('n', jsonb_build_object('type', 'integer'))),
            'querySchema', jsonb_build_object('type', 'object', 'required', jsonb_build_array('format'),
                'properties', jsonb_build_object('format', jsonb_build_object('type', 'string')))),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('n', 1)); END; $body$);

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000005', 'path', '/conformance/floor',
            'httpMethod', '^GET$', 'name', 'conformance_floor', 'access', 'public',
            'minTransactionIsolation', 'serializable',
            'outputSchema', jsonb_build_object('type', 'object')),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000002', 'path', '/conformance/shape',
            'httpMethod', '^GET$', 'name', 'conformance_shape', 'access', 'authenticated',
            'produces', jsonb_build_array('application/json'),
            'outputSchema', jsonb_build_object('type', 'object',
                'required', jsonb_build_array('items'),
                'properties', jsonb_build_object('items', jsonb_build_object('type', 'array',
                    'items', jsonb_build_object('type', 'string'))))),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('items', jsonb_build_array('a', 'b'))); END; $body$);

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000003', 'type', 'tool', 'effect', 'read',
            'name', 'conformance_tool', 'description', 'Conformance fixture',
            'access', 'authenticated',
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
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000006', 'type', 'tool', 'effect', 'read',
            'name', 'conformance_floor_tool', 'description', 'Isolation-floor conformance fixture',
            'access', 'public', 'minTransactionIsolation', 'serializable'),
        $body$ BEGIN RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id); END; $body$);

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0305-4000-8000-000000000007', 'type', 'tool', 'effect', 'read',
            'name', 'conformance_guarded_tool', 'description', 'Permission-guarded conformance fixture',
            'permissions', jsonb_build_array('task.read')),
        $body$ BEGIN RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id); END; $body$);
END $$;

-- ----------------------------------------------------------------------------
-- A: a non-public access declaration means no session, and a handle that names
-- no live session, are both refused
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_route  record;
    v_status int;
    v_env    jsonb;
    v_rest   int := 0;
    v_mcp    int := 0;
BEGIN
    FOR v_route IN
        SELECT r.route_name, r.probe_path, r.query_schema::jsonb AS query_schema,
               upper((api.openapi_methods(r.method_regexp))[1]) AS method
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE h.access IS DISTINCT FROM 'public' AND r.probe_path IS DISTINCT FROM ''
    LOOP
        PERFORM pg_temp.test_act_as(NULL);
        v_status := (api.rest_invoke(v_route.method, pg_temp.conformance_url(v_route.probe_path, v_route.query_schema),
                                     ''::extensions.hstore, NULL::bytea)).status_code;
        IF v_status IS DISTINCT FROM 401 THEN
            RAISE EXCEPTION 'CONFORMANCE: % is not public but answered % with no identity', v_route.route_name, v_status;
        END IF;

        v_status := (api.rest_invoke(v_route.method, pg_temp.conformance_url(v_route.probe_path, v_route.query_schema),
                                     'x-login-session=>ls_ghost'::extensions.hstore, NULL::bytea)).status_code;
        IF v_status IS DISTINCT FROM 401 THEN
            RAISE EXCEPTION 'CONFORMANCE: % is not public but answered % to a handle that names no session',
                v_route.route_name, v_status;
        END IF;
        v_rest := v_rest + 1;
    END LOOP;
    PERFORM pg_temp.test_act_as(NULL);

    FOR v_route IN
        SELECT m.mcp_name FROM api.mcp_route m
        JOIN api.handler h ON h.object_id = m.handler_object_id AND h.deleted_at IS NULL
        WHERE h.access IS DISTINCT FROM 'public' AND h.handler_type = 'mcp_tool'
    LOOP
        FOREACH v_env IN ARRAY ARRAY[NULL::jsonb, '{"connection": "cn_ghost"}'::jsonb] LOOP
            v_env := (api.mcp_call_tool(v_route.mcp_name, '{}'::jsonb, v_env, '"c1"'::jsonb)).envelope;
            IF v_env->'error' IS NULL THEN
                RAISE EXCEPTION 'CONFORMANCE: MCP tool % is not public but ran for an unresolved caller: %',
                    v_route.mcp_name, v_env;
            END IF;
        END LOOP;
        v_mcp := v_mcp + 1;
    END LOOP;

    IF (v_rest = 0) IS NOT FALSE OR (v_mcp = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'CONFORMANCE: declared-access arm exercised % REST route(s) and % MCP tool(s)', v_rest, v_mcp;
    END IF;
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
    v_org uuid;
    v_headers extensions.hstore;
BEGIN
    SELECT r.route_name, r.probe_path, r.query_schema::jsonb AS query_schema INTO v_route
    FROM api.rest_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
    WHERE h.access IS DISTINCT FROM 'public' AND r.probe_path IS DISTINCT FROM '' AND r.route_name = 'conformance_shape';

    v_org := (membership.fn_create_organization_with_owner('Conformance', 'conformance-org',
                  pg_temp.test_person('test|contract-conformance'))).object_id;
    v_headers := pg_temp.test_session_headers('test|contract-conformance', v_org);
    v_status := (api.rest_invoke('GET', pg_temp.conformance_url(v_route.probe_path, v_route.query_schema),
                    v_headers, NULL::bytea)).status_code;
    IF v_status IS NOT DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'CONFORMANCE: a valid login and organization session were refused 401';
    END IF;

    -- The __Host-session cookie reaches the database as x-login-session: the
    -- gateway derives that header from it and drops any client-sent copy.
    FOR v_scheme IN
        SELECT key AS name, value AS scheme,
               CASE WHEN value->>'in' = 'cookie' AND value->>'name' = '__Host-session' THEN 'x-login-session'
                    ELSE lower(value->>'name') END AS header
        FROM jsonb_each(api.openapi_document()->'components'->'securitySchemes')
        WHERE value->>'type' = 'apiKey' AND value->>'in' IN ('header', 'cookie')
    LOOP
        IF (v_headers ? v_scheme.header) IS NOT TRUE THEN
            RAISE EXCEPTION 'CONFORMANCE: the spec advertises % (%), but a signed-in request does not carry it',
                v_scheme.name, v_scheme.scheme->>'name';
        END IF;
        v_status := (api.rest_invoke('GET', pg_temp.conformance_url(v_route.probe_path, v_route.query_schema),
                        v_headers - v_scheme.header, NULL::bytea)).status_code;
        IF (v_scheme.header = 'x-login-session') IS NOT FALSE AND v_status IS DISTINCT FROM 401 THEN
            RAISE EXCEPTION 'CONFORMANCE: the spec advertises % (%), but the gateway does not read it (% without it)',
                v_scheme.name, v_scheme.scheme->>'name', v_status;
        END IF;
        v_checked := v_checked + 1;
    END LOOP;

    IF (v_checked = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'CONFORMANCE: the spec advertises no session security scheme to check';
    END IF;
END $$;

-- ----------------------------------------------------------------------------
-- B: every request header the gateway maps into the request context is
-- advertised as a security scheme, or named here as a trust-boundary header (one
-- a trusted proxy sets and clients must never send, so publishing it would
-- invite forgery; x-login-session is one, derived from the advertised
-- __Host-session cookie). Adding one to the gateway without adding it here, or
-- to the spec, fails this arm.
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_trust_boundary text[] := '{x-login-session}';
    v_read      text[];
    v_declared  text[];
    v_undeclared text;
BEGIN
    SELECT array_agg(DISTINCT m[1]) INTO v_read
    FROM regexp_matches(pg_get_functiondef('api.set_auth_context(extensions.hstore)'::regprocedure),
                        'p_headers->''([a-z0-9-]+)''', 'g') AS m;

    SELECT array_agg(lower(value->>'name')) INTO v_declared
    FROM jsonb_each(api.openapi_document()->'components'->'securitySchemes')
    WHERE value->>'in' = 'header';

    SELECT string_agg(h, ', ') INTO v_undeclared
    FROM unnest(v_read) h
    WHERE NOT h = ANY (COALESCE(v_declared, '{}') || v_trust_boundary);

    IF (COALESCE(cardinality(v_read), 0) = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'CONFORMANCE: found no headers read by api.set_auth_context, so this arm checked nothing';
    END IF;
    IF v_undeclared IS NOT NULL THEN
        RAISE EXCEPTION 'CONFORMANCE: the gateway reads header(s) % that are neither advertised nor listed as trust-boundary headers',
            v_undeclared;
    END IF;
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
    PERFORM pg_temp.test_act_as('test|contract-conformance');
    FOR v_route IN
        SELECT r.route_name, r.probe_path, r.query_schema::jsonb AS query_schema, h.produces, h.output_json_schema::jsonb AS schema,
               h.min_transaction_isolation, h.read_only
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE 'get' = ANY (api.openapi_methods(r.method_regexp)) AND r.probe_path IS DISTINCT FROM ''
          AND h.min_transaction_isolation IS NULL AND NOT h.read_only
    LOOP
        v_response := api.rest_invoke('GET', pg_temp.conformance_url(v_route.probe_path, v_route.query_schema),
                                      pg_temp.test_session_headers('test|contract-conformance'), NULL::bytea);
        CONTINUE WHEN (v_response).status_code NOT BETWEEN 200 AND 299 OR (v_response).status_code = 204;

        v_media := lower(btrim(split_part((v_response).headers->'content-type', ';', 1)));
        IF cardinality(v_route.produces) > 0 AND (v_media = ANY (
                SELECT lower(p) FROM unnest(v_route.produces) p)) IS NOT TRUE THEN
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
                     pg_temp.conformance_mcp_context(), '"c2"'::jsonb)).envelope;
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
    PERFORM pg_temp.test_act_as(NULL);

    IF (v_rest = 0) IS NOT FALSE OR (v_mcp = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'CONFORMANCE: response-shape arm checked % REST response(s) and % MCP result(s)', v_rest, v_mcp;
    END IF;
END $$;

-- ----------------------------------------------------------------------------
-- A: required query parameters are enforced
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_route  record;
    v_status int;
    v_checked int := 0;
    v_headers extensions.hstore;
BEGIN
    -- The query is checked only after access.
    v_headers := pg_temp.conformance_caller();
    PERFORM pg_temp.test_act_as('test|contract-conformance');
    FOR v_route IN
        SELECT r.route_name, r.probe_path, upper((api.openapi_methods(r.method_regexp))[1]) AS method
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE r.probe_path IS DISTINCT FROM '' AND EXISTS (
            SELECT 1 FROM jsonb_array_elements(r.query_schema->'required'))
    LOOP
        v_status := (api.rest_invoke(v_route.method, v_route.probe_path, v_headers, NULL::bytea)).status_code;
        IF v_status IS DISTINCT FROM 400 THEN
            RAISE EXCEPTION 'CONFORMANCE: % declares a required query parameter but answered % without it',
                v_route.route_name, v_status;
        END IF;
        v_checked := v_checked + 1;
    END LOOP;
    PERFORM pg_temp.test_act_as(NULL);

    IF (v_checked = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'CONFORMANCE: no route declares a required query parameter';
    END IF;
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
    IF (v_rank = 3) IS NOT FALSE OR (current_setting('transaction_read_only') = 'on') IS NOT FALSE THEN
        RAISE EXCEPTION 'CONFORMANCE: this arm needs a read-write transaction below serializable';
    END IF;

    PERFORM pg_temp.test_act_as('test|contract-conformance');
    FOR v_route IN
        SELECT r.route_name, r.probe_path, r.query_schema::jsonb AS query_schema, h.read_only, h.min_transaction_isolation,
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
            pg_temp.conformance_url(v_route.probe_path, v_route.query_schema),
            pg_temp.test_session_headers('test|contract-conformance'), NULL::bytea)).content);
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
                    THEN 'pgmi.transaction_isolation_too_weak' END AS expected
        FROM api.mcp_route m
        JOIN api.handler h ON h.object_id = m.handler_object_id AND h.deleted_at IS NULL
        WHERE h.handler_type = 'mcp_tool'
          AND (CASE h.min_transaction_isolation
                   WHEN 'serializable' THEN 3 WHEN 'repeatable read' THEN 2 ELSE 1 END > v_rank)
    LOOP
        v_env := (api.mcp_call_tool(v_route.mcp_name, '{}'::jsonb,
                     pg_temp.conformance_mcp_context(), '"c3"'::jsonb)).envelope;
        IF v_env->'error'->'data'->>'code' IS DISTINCT FROM v_route.expected THEN
            RAISE EXCEPTION 'CONFORMANCE: MCP tool % declares a transaction policy but ran outside it: %', v_route.mcp_name, v_env;
        END IF;
        v_checked := v_checked + 1;
    END LOOP;
    PERFORM pg_temp.test_act_as(NULL);

    IF (v_checked = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'CONFORMANCE: no route or tool declares a transaction policy to check';
    END IF;
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
    v_headers  extensions.hstore;
BEGIN
    -- The body is checked only after access.
    v_headers := pg_temp.conformance_caller() || extensions.hstore('content-type', 'application/json');

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0388-4000-8000-000000000001', 'path', '/conformance/write',
            'httpMethod', '^POST$', 'name', 'conformance_write', 'access', 'public',
            'outputSchema', jsonb_build_object('type', 'object'),
            'inputSchema', jsonb_build_object('type', 'object',
                'required', jsonb_build_array('qty'),
                'properties', jsonb_build_object('qty', jsonb_build_object('type', 'integer')))),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$);

    PERFORM pg_temp.test_act_as('test|contract-conformance');
    FOR v_route IN
        SELECT r.route_name, r.probe_path, r.query_schema::jsonb AS query_schema, m.method
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        CROSS JOIN LATERAL (
            SELECT upper(x) AS method FROM unnest(api.openapi_methods(r.method_regexp)) x
            WHERE upper(x) IN ('POST', 'PUT', 'PATCH') LIMIT 1) m
        WHERE r.probe_path IS DISTINCT FROM ''
          AND jsonb_array_length(COALESCE(h.input_json_schema::jsonb->'required', '[]')) > 0
          AND h.min_transaction_isolation IS NULL AND NOT h.read_only
    LOOP
        v_response := api.rest_invoke(v_route.method, pg_temp.conformance_url(v_route.probe_path, v_route.query_schema),
            v_headers, convert_to('{}', 'UTF8'));
        v_detail := api.content_json((v_response).content)->>'detail';
        IF (v_response).status_code IS DISTINCT FROM 400 OR coalesce(v_detail, '') NOT LIKE 'Invalid request body:%' THEN
            RAISE EXCEPTION 'CONFORMANCE: % declares required body keys but the gateway let {} through (% %)',
                v_route.route_name, (v_response).status_code, v_detail;
        END IF;
        v_checked := v_checked + 1;
    END LOOP;

    IF (v_checked = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'CONFORMANCE: no route declares required body keys';
    END IF;

    v_response := api.rest_invoke('POST', '/conformance/write',
        extensions.hstore('content-type', 'application/json'), convert_to('{"qty": "many"}', 'UTF8'));
    PERFORM pg_temp.expect_status('CONFORMANCE: a body property of the wrong type must be a 400', v_response, 400);
    v_response := api.rest_invoke('POST', '/conformance/write',
        extensions.hstore('content-type', 'application/json'), convert_to('not json', 'UTF8'));
    PERFORM pg_temp.expect_status('CONFORMANCE: a body that is not JSON must be a 400', v_response, 400);
    v_response := api.rest_invoke('POST', '/conformance/write',
        extensions.hstore('content-type', 'application/json'), convert_to('{"qty": 3}', 'UTF8'));
    PERFORM pg_temp.expect_status('CONFORMANCE: a conforming body must reach the handler', v_response, 200);
    PERFORM pg_temp.test_act_as(NULL);
END $$;

-- ----------------------------------------------------------------------------
-- A: a declared locationTemplate is a Location a GET answers, published as a
-- response header; registration refuses one the response cannot fill.
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_route    record;
    v_response api.http_response;
    v_message  text;
    v_case     jsonb;
    v_checked  int := 0;
    v_base     text := rtrim(api.openapi_server_url(), '/');
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0486-4000-8000-000000000001', 'path', '/conformance/things',
            'httpMethod', '^POST$', 'name', 'conformance_create_thing', 'access', 'public',
            'successStatus', 201, 'locationTemplate', '/conformance/things/{thingId}',
            'outputSchema', jsonb_build_object('type', 'object',
                'required', jsonb_build_array('thingId'),
                'properties', jsonb_build_object('thingId', jsonb_build_object('type', 'string')))),
        $body$ BEGIN RETURN api.json_response(201, jsonb_build_object('thingId', 'a b/c')); END; $body$);
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0486-4000-8000-000000000002', 'path', '/conformance/things/{thingId}',
            'httpMethod', '^GET$', 'name', 'conformance_read_thing', 'access', 'public',
            'outputSchema', jsonb_build_object('type', 'object')),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('thingId', (request).path_params->>'thingId')); END; $body$);

    v_response := api.rest_invoke('POST', '/conformance/things', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 201
       OR (v_response).headers->'location' IS DISTINCT FROM v_base || '/conformance/things/a%20b%2Fc' THEN
        RAISE EXCEPTION 'CONFORMANCE: a 201 with a locationTemplate must answer Location %/conformance/things/a%%20b%%2Fc under the mount, got % %',
            v_base, (v_response).status_code, (v_response).headers->'location';
    END IF;
    v_response := api.rest_invoke('GET', substr((v_response).headers->'location', length(v_base) + 1), ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200
       OR api.content_json((v_response).content)->>'thingId' IS DISTINCT FROM 'a b/c' THEN
        RAISE EXCEPTION 'CONFORMANCE: GET on a declared Location must reach the resource, got %', (v_response).status_code;
    END IF;

    PERFORM pg_temp.conformance_caller();
    PERFORM pg_temp.test_act_as('test|contract-conformance', pg_temp.test_organization('conformance-org'), true);
    FOR v_route IN
        SELECT r.route_name, r.location_template,
               api.openapi_document()->'paths'->r.canonical_path
                   ->lower((api.openapi_methods(r.method_regexp))[1])->'responses'->'201'->'headers' AS headers
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE r.location_template IS NOT NULL
    LOOP
        IF internal.rest_route_lookup('GET',
               regexp_replace(v_route.location_template, '\{[^}]+\}', '00000000-0000-4000-8000-000000000000', 'g')) IS NULL THEN
            RAISE EXCEPTION 'CONFORMANCE: % declares Location % but no GET route answers it', v_route.route_name, v_route.location_template;
        END IF;
        IF v_route.headers->'Location' IS NULL THEN
            RAISE EXCEPTION 'CONFORMANCE: % declares a locationTemplate but OpenAPI publishes no Location header for its 201',
                v_route.route_name;
        END IF;
        v_checked := v_checked + 1;
    END LOOP;
    PERFORM pg_temp.test_act_as(NULL);

    IF NOT EXISTS (SELECT FROM api.rest_route WHERE route_name = 'approve_connection' AND location_template IS NOT NULL)
       OR (v_checked < 2) IS NOT FALSE THEN
        RAISE EXCEPTION 'CONFORMANCE: POST /connections and the fixture must declare Location, % route(s) do', v_checked;
    END IF;

    FOREACH v_case IN ARRAY ARRAY[
        '{"locationTemplate": "/conformance/things/{missing}"}',
        '{"locationTemplate": "/conformance/things/{thingId}", "successStatus": 200}',
        '{"locationTemplate": "/conformance/things/x{thingId}"}']::jsonb[]
    LOOP
        v_message := NULL;
        BEGIN
            PERFORM api.create_or_replace_rest_handler(
                jsonb_build_object('id', 'ffffffff-0486-4000-8000-000000000003', 'path', '/conformance/bad-location',
                    'httpMethod', '^POST$', 'name', 'conformance_bad_location', 'access', 'public',
                    'successStatus', 201,
                    'outputSchema', jsonb_build_object('type', 'object',
                        'properties', jsonb_build_object('thingId', jsonb_build_object('type', 'string')))) || v_case,
                $body$ BEGIN RETURN api.json_response((SELECT 201), '{}'::jsonb); END; $body$);
        EXCEPTION WHEN invalid_parameter_value THEN
            GET STACKED DIAGNOSTICS v_message = MESSAGE_TEXT;
        END;
        IF coalesce(v_message, '') NOT LIKE '%locationTemplate%' THEN
            RAISE EXCEPTION 'CONFORMANCE: registration must refuse %, got %', v_case, coalesce(v_message, 'no error');
        END IF;
    END LOOP;
END $$;

-- ----------------------------------------------------------------------------
-- A: a body route that narrows accepts refuses another media type, and one
-- with an inputSchema accepts application/json alone by default
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_route   record;
    v_status  int;
    v_checked int := 0;
    v_headers extensions.hstore;
BEGIN
    v_headers := pg_temp.conformance_caller() || extensions.hstore('content-type', 'text/plain');
    PERFORM pg_temp.test_act_as('test|contract-conformance');
    FOR v_route IN
        SELECT r.route_name, r.probe_path, r.query_schema::jsonb AS query_schema, m.method
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        CROSS JOIN LATERAL (
            SELECT upper(x) AS method FROM unnest(api.openapi_methods(r.method_regexp)) x
            WHERE upper(x) IN ('POST', 'PUT', 'PATCH') LIMIT 1) m
        WHERE r.probe_path IS DISTINCT FROM ''
          AND NOT api.accept_matches(array_to_string(h.accepts, ', '), ARRAY['text/plain'])
          AND h.min_transaction_isolation IS NULL AND NOT h.read_only
    LOOP
        v_status := (api.rest_invoke(v_route.method, pg_temp.conformance_url(v_route.probe_path, v_route.query_schema),
            v_headers, convert_to('{}', 'UTF8'))).status_code;
        IF v_status IS DISTINCT FROM 415 THEN
            RAISE EXCEPTION 'CONFORMANCE: % does not accept text/plain but answered %', v_route.route_name, v_status;
        END IF;
        v_checked := v_checked + 1;
    END LOOP;
    PERFORM pg_temp.test_act_as(NULL);

    IF (v_checked = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'CONFORMANCE: no body route narrows accepts';
    END IF;
END $$;

-- ----------------------------------------------------------------------------
-- A: declared query value shapes are enforced
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_case record;
    v_status int;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0388-4000-8000-000000000002', 'path', '/conformance/list',
            'httpMethod', '^GET$', 'name', 'conformance_list', 'access', 'public',
            'outputSchema', jsonb_build_object('type', 'object'),
            'querySchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object(
                'limit', jsonb_build_object('type', 'integer'),
                'ratio', jsonb_build_object('type', 'number'),
                'flag', jsonb_build_object('type', 'boolean'),
                'format', jsonb_build_object('type', 'string', 'enum', jsonb_build_array('json', 'csv'))))),
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
END $$;

-- ----------------------------------------------------------------------------
-- A: two routes that can claim one address are ranked, never left to order
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_response api.http_response;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0388-4000-8000-000000000003', 'path', '/conformance/shops/{shop}/orders',
            'httpMethod', '^GET$', 'name', 'conformance_shop_orders', 'access', 'public',
            'outputSchema', jsonb_build_object('type', 'object')),
        $body$ BEGIN RETURN api.json_response(200, '{"route": "shop_orders"}'::jsonb); END; $body$);
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0388-4000-8000-000000000004', 'path', '/conformance/shops/main/{section}',
            'httpMethod', '^GET$', 'name', 'conformance_main_section', 'access', 'public',
            'outputSchema', jsonb_build_object('type', 'object')),
        $body$ BEGIN RETURN api.json_response(200, '{"route": "main_section"}'::jsonb); END; $body$);

    v_response := api.rest_invoke('GET', '/conformance/shops/main/orders', ''::extensions.hstore, NULL::bytea);
    IF api.content_json(v_response.content)->>'route' IS DISTINCT FROM 'main_section' THEN
        RAISE EXCEPTION 'CONFORMANCE: /conformance/shops/main/orders matches both routes and must go to the one literal earlier, got %',
            convert_from(v_response.content, 'UTF8');
    END IF;
    v_response := api.rest_invoke('GET', '/conformance/shops/north/orders', ''::extensions.hstore, NULL::bytea);
    IF api.content_json(v_response.content)->>'route' IS DISTINCT FROM 'shop_orders' THEN
        RAISE EXCEPTION 'CONFORMANCE: /conformance/shops/north/orders must reach the shop route, got %',
            convert_from(v_response.content, 'UTF8');
    END IF;
END $$;

-- ----------------------------------------------------------------------------
-- B: the MCP listings and the MCP registry agree, for a platform operator
-- acting in an organization it owns, who holds every permission
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_missing   text;
    v_phantom   text;
    v_listed    int;
    v_env       jsonb;
    v_name      text;
BEGIN
    PERFORM pg_temp.conformance_caller();
    PERFORM pg_temp.test_act_as('test|contract-conformance', pg_temp.test_organization('conformance-org'), true);

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
    SELECT (SELECT string_agg(x.kind || ':' || x.name
                              || coalesce(format(' (requires %s; the caller holds %s)', p.required,
                                                 coalesce(nullif(array_to_string(api.current_permissions(), ', '), ''), 'none')), ''),
                              ', ')
            FROM (SELECT * FROM registered EXCEPT SELECT * FROM listed) x
            LEFT JOIN LATERAL (
                SELECT string_agg(hp.permission_id, ', ' ORDER BY hp.permission_id) AS required
                FROM api.mcp_route m
                JOIN api.handler h ON h.object_id = m.handler_object_id AND h.deleted_at IS NULL
                JOIN api.handler_permission hp ON hp.handler_object_id = h.object_id
                WHERE m.mcp_name = x.name AND h.handler_type::text = x.kind
            ) p ON true),
           (SELECT string_agg(kind || ':' || name, ', ') FROM (SELECT * FROM listed EXCEPT SELECT * FROM registered) x),
           (SELECT count(*) FROM listed)
    INTO v_missing, v_phantom, v_listed;

    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'CONFORMANCE: registered but not advertised to a caller holding every permission: %', v_missing;
    END IF;
    IF v_phantom IS NOT NULL THEN
        RAISE EXCEPTION 'CONFORMANCE: advertised but not registered: %', v_phantom;
    END IF;
    IF (v_listed = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'CONFORMANCE: the MCP listings were empty, so agreement proved nothing';
    END IF;

    -- Every advertised tool resolves in the dispatcher: whatever the tool
    -- then answers, it must not be "not found".
    FOR v_name IN SELECT t->>'name' FROM jsonb_array_elements(api.mcp_list_tools()->'tools') t LOOP
        v_env := (api.mcp_call_tool(v_name, '{}'::jsonb,
                     pg_temp.conformance_mcp_context(), '"c4"'::jsonb)).envelope;
        IF v_env->'error'->>'message' ILIKE '%not found%' THEN
            RAISE EXCEPTION 'CONFORMANCE: tools/list advertises % but tools/call cannot find it', v_name;
        END IF;
    END LOOP;

    PERFORM pg_temp.test_act_as(NULL);
END $$;

-- ----------------------------------------------------------------------------
-- The database's 401 carries its code like every other refusal
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_response api.http_response := api.rest_invoke('GET', '/me', ''::extensions.hstore, NULL::bytea);
BEGIN
    IF (v_response).status_code IS DISTINCT FROM 401
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'pgmi.unauthenticated' THEN
        RAISE EXCEPTION 'CONFORMANCE: an anonymous call to an authenticated route must answer 401 pgmi.unauthenticated, got % %',
            (v_response).status_code, api.content_json((v_response).content);
    END IF;
END $$;
