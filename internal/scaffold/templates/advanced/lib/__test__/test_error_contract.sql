-- ============================================================================
-- Test: the error contract
-- ============================================================================
-- A handler's uncaught SQLSTATE answers the same status and pgmi.* code over
-- REST and RPC; anything not listed, class 22 included, is a 500. HEAD error
-- responses carry no content. Registration refuses readOnly where the exchange
-- log could not be written. Framework problem codes all start with pgmi.
-- ============================================================================

DO $$
DECLARE
    v_case record;
    v_response api.http_response;
    v_content jsonb;
    v_envelope jsonb;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-e101-4000-8000-000000000001',
            'path', '/error-contract/{state}', 'httpMethod', '^(GET|DELETE)$',
            'name', 'error_contract_rest', 'access', 'public', 'autoLog', false),
        $body$
BEGIN
    IF (request).path_params->>'state' = '22012' THEN
        PERFORM 1 / 0;
    END IF;
    RAISE EXCEPTION 'raised by the error contract test' USING ERRCODE = (request).path_params->>'state';
END;
        $body$);

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-e102-4000-8000-000000000001',
            'methodName', 'error_contract.raise', 'access', 'public', 'autoLog', false),
        $body$
DECLARE
    v_state text := api.content_json((request).content)->'params'->>'state';
BEGIN
    IF v_state = '22012' THEN
        PERFORM 1 / 0;
    END IF;
    RAISE EXCEPTION 'raised by the error contract test' USING ERRCODE = v_state;
END;
        $body$);

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-e103-4000-8000-000000000001',
            'type', 'tool', 'effect', 'read', 'name', 'error_contract_tool', 'access', 'public',
            'description', 'Raises the SQLSTATE it is given',
            'inputSchema', jsonb_build_object('type', 'object',
                'properties', jsonb_build_object('state', jsonb_build_object('type', 'string')))),
        $body$
BEGIN
    IF (request).arguments->>'state' = '22012' THEN
        PERFORM 1 / 0;
    END IF;
    RAISE EXCEPTION 'raised by the error contract test' USING ERRCODE = (request).arguments->>'state';
END;
        $body$);

    FOR v_case IN
        SELECT * FROM (VALUES
            ('22012', 'GET',    500, NULL,                         -32603),
            ('2201B', 'GET',    500, NULL,                         -32603),
            ('0A000', 'GET',    500, NULL,                         -32603),
            ('22P02', 'GET',    400, 'pgmi.malformed_value',       -32602),
            ('22021', 'GET',    400, 'pgmi.invalid_encoding',      -32602),
            ('22P05', 'GET',    400, 'pgmi.invalid_encoding',      -32602),
            ('22001', 'GET',    422, 'pgmi.value_too_long',        -32602),
            ('23505', 'GET',    409, 'pgmi.already_exists',        -32602),
            ('23P01', 'GET',    409, 'pgmi.conflict',              -32602),
            ('55000', 'GET',    409, 'pgmi.invalid_state',         -32602),
            ('23503', 'GET',    400, 'pgmi.reference_not_found',   -32602),
            ('23503', 'DELETE', 409, 'pgmi.resource_in_use',       NULL),
            ('42501', 'GET',    500, NULL,                         -32603),
            ('P0002', 'GET',    404, 'pgmi.not_found',             -32602)
        ) AS t(sqlstate, method, status, code, rpc_code)
    LOOP
        v_response := api.rest_invoke(v_case.method, '/error-contract/' || v_case.sqlstate);
        v_content := api.content_json((v_response).content);
        IF (v_response).status_code IS DISTINCT FROM v_case.status
           OR v_content->>'code' IS DISTINCT FROM v_case.code
           OR v_content::text LIKE '%raised by the error contract test%' THEN
            RAISE EXCEPTION 'TEST FAILED: REST % % should answer % %, got % %',
                v_case.method, v_case.sqlstate, v_case.status, v_case.code, (v_response).status_code, v_content;
        END IF;

        CONTINUE WHEN v_case.rpc_code IS NULL;

        v_response := api.rpc_invoke(api.rpc_resolve('error_contract.raise'), ''::extensions.hstore,
            convert_to(jsonb_build_object('jsonrpc', '2.0', 'method', 'error_contract.raise', 'id', 1,
                'params', jsonb_build_object('state', v_case.sqlstate))::text, 'UTF8'));
        v_content := api.content_json((v_response).content);
        IF (v_response).status_code IS DISTINCT FROM v_case.status
           OR (v_content->'error'->>'code')::int IS DISTINCT FROM v_case.rpc_code
           OR v_content->'error'->'data'->>'code' IS DISTINCT FROM v_case.code
           OR v_content::text LIKE '%raised by the error contract test%' THEN
            RAISE EXCEPTION 'TEST FAILED: RPC % should answer % / % %, got % %',
                v_case.sqlstate, v_case.status, v_case.rpc_code, v_case.code, (v_response).status_code, v_content;
        END IF;
    END LOOP;

    v_envelope := (api.mcp_call_tool('error_contract_tool', '{"state": "22012"}', NULL, '"ec-1"')).envelope;
    IF v_envelope->'result'->>'isError' IS DISTINCT FROM 'true'
       OR v_envelope->'result'->'content'->0->>'text' IS DISTINCT FROM 'Tool execution failed' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP division by zero should be the generic tool error, got %', v_envelope;
    END IF;
    v_envelope := (api.mcp_call_tool('error_contract_tool', '{"state": "23505"}', NULL, '"ec-2"')).envelope;
    IF v_envelope->'result'->'content'->0->>'text' IS DISTINCT FROM 'Resource already exists' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP unique violation should name the conflict, got %', v_envelope;
    END IF;
END $$;

-- HEAD: the status survives, the content does not, on every error path.
DO $$
DECLARE
    v_response api.http_response;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-e104-4000-8000-000000000001',
            'path', '/error-contract-post-only', 'httpMethod', '^POST$',
            'name', 'error_contract_post_only', 'access', 'public', 'autoLog', false),
        $body$ BEGIN RETURN api.json_response(200, '{}'); END; $body$);

    v_response := api.rest_invoke('HEAD', '/error-contract-nowhere');
    IF (v_response).status_code IS DISTINCT FROM 404 OR (v_response).content IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: HEAD to an unknown path should be 404 without content, got % %',
            (v_response).status_code, octet_length((v_response).content);
    END IF;
    IF (coalesce(((v_response).headers->'content-length')::int, 0) = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: HEAD keeps the content-length the body would have had, got %', (v_response).headers;
    END IF;

    v_response := api.rest_invoke('HEAD', '/error-contract-post-only');
    IF (v_response).status_code IS DISTINCT FROM 405 OR (v_response).content IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: HEAD to a POST-only path should be 405 without content, got % %',
            (v_response).status_code, octet_length((v_response).content);
    END IF;

    v_response := api.rest_invoke('HEAD', '/error-contract/22012');
    IF (v_response).status_code IS DISTINCT FROM 500 OR (v_response).content IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: HEAD to a failing handler should be 500 without content, got % %',
            (v_response).status_code, octet_length((v_response).content);
    END IF;
END $$;

-- readOnly runs in a READ ONLY transaction, which cannot write the exchange
-- log: a readOnly route is unlogged unless it says "autoLog": true, which is
-- refused, as is any readOnly MCP handler.
DO $$
DECLARE
    v_case record;
    v_refused boolean;
BEGIN
    FOR v_case IN
        SELECT * FROM (VALUES
            ('rest', jsonb_build_object('id', 'ffffffff-e105-4000-8000-000000000001',
                'path', '/error-contract-ro', 'httpMethod', '^GET$', 'name', 'error_contract_ro',
                'access', 'public', 'readOnly', true, 'autoLog', true)),
            ('rpc', jsonb_build_object('id', 'ffffffff-e106-4000-8000-000000000001',
                'methodName', 'error_contract.ro', 'access', 'public', 'readOnly', true, 'autoLog', true)),
            ('mcp', jsonb_build_object('id', 'ffffffff-e107-4000-8000-000000000001',
                'type', 'tool', 'effect', 'read', 'name', 'error_contract_ro_tool', 'description', 'refused',
                'access', 'public', 'readOnly', true))
        ) AS t(kind, metadata)
    LOOP
        v_refused := false;
        BEGIN
            CASE v_case.kind
                WHEN 'rest' THEN PERFORM api.create_or_replace_rest_handler(v_case.metadata,
                    $body$ BEGIN RETURN api.json_response(200, '{}'); END; $body$);
                WHEN 'rpc' THEN PERFORM api.create_or_replace_rpc_handler(v_case.metadata,
                    $body$ BEGIN RETURN api.jsonrpc_success('{}', NULL); END; $body$);
                ELSE PERFORM api.create_or_replace_mcp_handler(v_case.metadata,
                    $body$ BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END; $body$);
            END CASE;
        EXCEPTION WHEN invalid_parameter_value THEN
            v_refused := SQLERRM LIKE '%readOnly%';
        END;
        IF v_refused IS DISTINCT FROM true THEN
            RAISE EXCEPTION 'TEST FAILED: a logged readOnly % handler must be refused at registration', v_case.kind;
        END IF;
    END LOOP;
END $$;

DO $$
DECLARE
    v_logged boolean[];
BEGIN
    PERFORM api.create_or_replace_rest_handler(jsonb_build_object('id', 'ffffffff-e108-4000-8000-000000000001',
        'path', '/error-contract-ro-default', 'httpMethod', '^GET$', 'name', 'error_contract_ro_default',
        'access', 'public', 'readOnly', true),
        $body$ BEGIN RETURN api.json_response(200, '{}'); END; $body$);
    PERFORM api.create_or_replace_rpc_handler(jsonb_build_object('id', 'ffffffff-e109-4000-8000-000000000001',
        'methodName', 'error_contract.ro_default', 'access', 'public', 'readOnly', true),
        $body$ BEGIN RETURN api.jsonrpc_success('{}', NULL); END; $body$);
    v_logged := ARRAY[
        (SELECT auto_log FROM api.rest_route WHERE handler_object_id = 'ffffffff-e108-4000-8000-000000000001'),
        (SELECT auto_log FROM api.rpc_route WHERE handler_object_id = 'ffffffff-e109-4000-8000-000000000001')];
    IF v_logged IS DISTINCT FROM ARRAY[false, false] THEN
        RAISE EXCEPTION 'TEST FAILED: a readOnly route without autoLog must register unlogged, got rest/rpc auto_log %', v_logged;
    END IF;
END $$;

-- Framework problem codes start with pgmi.; the application's are
-- <schema>.<entity>.<reason> (test_response_contract.sql).
-- The check reads the framework's source, so a new gateway refusal with a bare
-- code fails here.
CREATE FUNCTION pg_temp.bare_problem_codes(p_source text)
RETURNS SETOF text
LANGUAGE sql IMMUTABLE AS $$
    SELECT DISTINCT m[1]
    FROM regexp_matches(p_source, $re$(?:code\s*=>\s*|'code',\s*)'([^']*)'$re$, 'g') AS m
    WHERE m[1] !~ '^pgmi\.';
$$;

DO $$
DECLARE
    v_bare text;
BEGIN
    SELECT string_agg(s.path || ': ' || c, ', ' ORDER BY s.path, c) INTO v_bare
    FROM pg_temp.pgmi_source_view s
    CROSS JOIN LATERAL pg_temp.bare_problem_codes(
        regexp_replace(s.content, '--[^\n]*', '', 'g')) AS c
    WHERE s.path LIKE './lib/%' AND s.is_sql_file;
    IF v_bare IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: framework problem codes must start with pgmi.: %', v_bare;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_temp.bare_problem_codes(
            $src$ RETURN api.problem_response(409, 'Conflict', code => 'already_there'); $src$)) THEN
        RAISE EXCEPTION 'TEST FAILED: the problem-code check does not see a planted bare code';
    END IF;
END $$;

-- Malformed or schema-invalid input is the gateway's 400, in the same shape as
-- a query violation: a pgmi.* code and invalid-params.
DO $$
DECLARE
    v_response api.http_response;
    v_body jsonb;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-e108-4000-8000-000000000001',
            'path', '/error-contract-body', 'httpMethod', '^POST$',
            'name', 'error_contract_body', 'access', 'public', 'autoLog', false, 'successStatus', 201,
            'noLocation', 'a fixture',
            'inputSchema', jsonb_build_object('type', 'object', 'required', jsonb_build_array('name'),
                'properties', jsonb_build_object('name', jsonb_build_object('type', 'string'),
                                                 'size', jsonb_build_object('type', 'integer')))),
        $body$ BEGIN RETURN api.json_response(201, '{}'); END; $body$);

    v_response := api.rest_invoke('POST', '/error-contract-body', 'content-type=>application/json'::extensions.hstore,
                                  convert_to('{"size": 1.5}', 'UTF8'));
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 400
       OR v_body->>'code' IS DISTINCT FROM 'pgmi.invalid_body'
       OR v_body->'invalid-params' IS DISTINCT FROM
          '[{"name": "name", "reason": "required"}, {"name": "size", "reason": "invalid"}]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a schema-invalid body should be 400 pgmi.invalid_body with invalid-params, got % %',
            (v_response).status_code, v_body;
    END IF;

    v_response := api.rest_invoke('POST', '/error-contract-body', 'content-type=>application/json'::extensions.hstore,
                                  convert_to('{not json', 'UTF8'));
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 400 OR v_body->>'code' IS DISTINCT FROM 'pgmi.malformed_body' THEN
        RAISE EXCEPTION 'TEST FAILED: a body that is not JSON should be 400 pgmi.malformed_body, got % %',
            (v_response).status_code, v_body;
    END IF;
END $$;
