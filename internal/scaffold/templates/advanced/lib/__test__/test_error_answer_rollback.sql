-- ============================================================================
-- Test: an error answer changes nothing, and its exchange row still commits
-- ============================================================================
-- A handler that writes and then returns an error (REST/RPC status >= 400, an
-- MCP error or isError result, or a result the framework rejects) leaves no
-- write behind. The exchange log keeps the handler's real answer.
-- ============================================================================

DO $$
DECLARE
    v_response api.http_response;
    v_envelope jsonb;
    v_logged   api.http_response;
BEGIN
    RAISE NOTICE '-> Testing an error answer rolls back the handler''s writes';

    CREATE TEMP TABLE error_answer_probe (protocol text NOT NULL);

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-e557-4000-8000-000000000001', 'path', '/error-answer-probe',
            'httpMethod', '^POST$', 'name', 'error_answer_probe', 'access', 'public', 'autoLog', true),
        $body$
BEGIN
    INSERT INTO pg_temp.error_answer_probe VALUES ('rest');
    RETURN api.problem_response(409, 'Conflict', 'refused after a write', code => 'test.refused');
END;
$body$);

    v_response := api.rest_invoke('POST', '/error-answer-probe', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 409 THEN
        RAISE EXCEPTION 'TEST FAILED: the REST probe must answer 409, got %', (v_response).status_code;
    END IF;
    IF EXISTS (SELECT FROM pg_temp.error_answer_probe WHERE protocol = 'rest') THEN
        RAISE EXCEPTION 'TEST FAILED: a REST 409 answer kept the handler''s write';
    END IF;
    SELECT (response).* INTO v_logged FROM api.rest_exchange
    WHERE handler_object_id = 'ffffffff-e557-4000-8000-000000000001';
    IF NOT FOUND OR (v_logged).status_code IS DISTINCT FROM 409
       OR api.content_json((v_logged).content)->>'code' IS DISTINCT FROM 'test.refused' THEN
        RAISE EXCEPTION 'TEST FAILED: the exchange log must keep the handler''s 409, got %', v_logged;
    END IF;

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object('id', 'ffffffff-e557-4000-8000-000000000002', 'methodName', 'test.errorAnswer',
            'access', 'public', 'autoLog', true),
        $body$
BEGIN
    INSERT INTO pg_temp.error_answer_probe VALUES ('rpc');
    RETURN api.jsonrpc_error(-32602, 'refused after a write', api.content_json((request).content)->'id');
END;
$body$);

    v_response := api.rpc_invoke(api.rpc_resolve('test.errorAnswer'), ''::extensions.hstore,
        convert_to('{"jsonrpc":"2.0","method":"test.errorAnswer","id":1}', 'UTF8'));
    IF (v_response).status_code IS DISTINCT FROM 400 THEN
        RAISE EXCEPTION 'TEST FAILED: the RPC probe must answer 400, got %', (v_response).status_code;
    END IF;
    IF EXISTS (SELECT FROM pg_temp.error_answer_probe WHERE protocol = 'rpc') THEN
        RAISE EXCEPTION 'TEST FAILED: an RPC error answer kept the handler''s write';
    END IF;
    SELECT (response).* INTO v_logged FROM api.rpc_exchange
    WHERE handler_object_id = 'ffffffff-e557-4000-8000-000000000002';
    IF NOT FOUND OR (v_logged).status_code IS DISTINCT FROM 400 THEN
        RAISE EXCEPTION 'TEST FAILED: the exchange log must keep the RPC error answer, got %', v_logged;
    END IF;

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-e557-4000-8000-000000000003', 'type', 'tool', 'effect', 'write', 'reversible', true,
            'name', 'test_error_answer', 'description', 'Writes, then answers as its argument asks',
            'access', 'public',
            'inputSchema', jsonb_build_object('type', 'object',
                'properties', jsonb_build_object('answer', jsonb_build_object('type', 'string'))),
            'outputSchema', jsonb_build_object('type', 'object', 'required', jsonb_build_array('n'),
                'properties', jsonb_build_object('n', jsonb_build_object('type', 'integer')))),
        $body$
BEGIN
    INSERT INTO pg_temp.error_answer_probe VALUES ((request).arguments->>'answer');
    IF (request).arguments->>'answer' = 'refused' THEN
        RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('refused')), (request).request_id, true);
    END IF;
    IF (request).arguments->>'answer' = 'wrong-shape' THEN
        RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('{}')), (request).request_id, false, '{}');
    END IF;
    RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('{"n": 1}')), (request).request_id, false, '{"n": 1}');
END;
$body$);

    FOREACH v_envelope IN ARRAY ARRAY[
        (api.mcp_call_tool('test_error_answer', '{"answer": "refused"}', NULL, '"m1"')).envelope,
        (api.mcp_call_tool('test_error_answer', '{"answer": "wrong-shape"}', NULL, '"m2"')).envelope]
    LOOP
        IF v_envelope->'result'->'isError' IS DISTINCT FROM 'true'::jsonb THEN
            RAISE EXCEPTION 'TEST FAILED: the MCP probe must answer isError, got %', v_envelope;
        END IF;
    END LOOP;
    IF EXISTS (SELECT FROM pg_temp.error_answer_probe WHERE protocol IN ('refused', 'wrong-shape')) THEN
        RAISE EXCEPTION 'TEST FAILED: an MCP isError answer kept the tool''s write: %',
            (SELECT array_agg(protocol) FROM pg_temp.error_answer_probe);
    END IF;
    IF (SELECT count(*) FROM api.mcp_exchange WHERE mcp_name = 'test_error_answer'
          AND (response).envelope->'result'->'isError' = 'true'::jsonb) IS DISTINCT FROM 2::bigint THEN
        RAISE EXCEPTION 'TEST FAILED: both MCP error answers must be logged';
    END IF;

    v_envelope := (api.mcp_call_tool('test_error_answer', '{"answer": "ok"}', NULL, '"m3"')).envelope;
    IF v_envelope->'result'->'isError' IS DISTINCT FROM 'false'::jsonb
       OR NOT EXISTS (SELECT FROM pg_temp.error_answer_probe WHERE protocol = 'ok') THEN
        RAISE EXCEPTION 'TEST FAILED: a successful MCP answer must keep its write, got %', v_envelope;
    END IF;

    DROP TABLE pg_temp.error_answer_probe;
END $$;

DO $$
DECLARE
    v_answer   text;
    v_response api.http_response;
BEGIN
    CREATE TEMP TABLE invalid_status_probe (answer text NOT NULL);

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-e557-4000-8000-000000000004', 'path', '/invalid-status-probe',
            'httpMethod', '^POST$', 'name', 'invalid_status_probe', 'access', 'public'),
        $body$
DECLARE
    v_answer text := convert_from((request).content, 'UTF8');
    r api.http_response;
BEGIN
    INSERT INTO pg_temp.invalid_status_probe VALUES ('rest-' || v_answer);
    IF v_answer = '42' THEN
        r := (42, ''::extensions.hstore, NULL::bytea);
    END IF;
    RETURN r;
END;
$body$);

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object('id', 'ffffffff-e557-4000-8000-000000000005', 'methodName', 'test.invalidStatus',
            'access', 'public'),
        $body$
DECLARE
    v_answer text := api.content_json((request).content)->'params'->>'answer';
    r api.http_response;
BEGIN
    INSERT INTO pg_temp.invalid_status_probe VALUES ('rpc-' || v_answer);
    IF v_answer = '42' THEN
        r := (42, ''::extensions.hstore, NULL::bytea);
    END IF;
    RETURN r;
END;
$body$);

    FOREACH v_answer IN ARRAY ARRAY['null', '42'] LOOP
        v_response := api.rest_invoke('POST', '/invalid-status-probe', ''::extensions.hstore, convert_to(v_answer, 'UTF8'));
        IF (v_response).status_code IS DISTINCT FROM 500 THEN
            RAISE EXCEPTION 'TEST FAILED: a REST handler answering status % must be a 500, got %', v_answer, (v_response).status_code;
        END IF;
        v_response := api.rpc_invoke(api.rpc_resolve('test.invalidStatus'), ''::extensions.hstore,
            convert_to(format('{"jsonrpc":"2.0","method":"test.invalidStatus","params":{"answer":"%s"},"id":1}', v_answer), 'UTF8'));
        IF (v_response).status_code IS DISTINCT FROM 500 THEN
            RAISE EXCEPTION 'TEST FAILED: an RPC handler answering status % must be a 500, got %', v_answer, (v_response).status_code;
        END IF;
    END LOOP;
    IF (SELECT count(*) FROM pg_temp.invalid_status_probe) IS DISTINCT FROM 0::bigint THEN
        RAISE EXCEPTION 'TEST FAILED: a handler answering an invalid status kept its writes: %',
            (SELECT array_agg(answer) FROM pg_temp.invalid_status_probe);
    END IF;

    DROP TABLE pg_temp.invalid_status_probe;
END $$;
