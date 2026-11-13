-- ============================================================================
-- Test: declared schemas are enforced at every depth, for MCP tool arguments
-- and REST request bodies; a missing resource answers in its client's era
-- ============================================================================
-- The database is the only validator: type (a name or a list), enum,
-- required, additionalProperties: false, through properties, items and
-- #/$defs. A legacy-era resources/read of an unknown URI answers -32002, a
-- 2026-07-28 one -32602, both with data.uri.
-- ============================================================================

DO $$
DECLARE
    v_schema jsonb := '{
        "type": "object", "additionalProperties": false, "required": ["kind", "owner"],
        "$defs": {"person": {"type": "object", "required": ["name"],
                             "properties": {"name": {"type": "string"}, "age": {"type": ["integer", "null"]}}}},
        "properties": {
            "kind": {"type": "string", "enum": ["a", "b"]},
            "owner": {"$ref": "#/$defs/person"},
            "tags": {"type": "array", "items": {"type": "string"}}
        }}';
    v_envelope jsonb;
    v_text     text;
    v_response api.http_response;
    v_body     jsonb;
BEGIN
    RAISE NOTICE '-> Testing schema enforcement at every depth';

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-e570-4000-8000-000000000001', 'type', 'tool', 'effect', 'read',
            'name', 'test_schema_depth', 'description', 'Answers ok when its arguments pass the schema',
            'access', 'public', 'inputSchema', v_schema),
        $body$
BEGIN
    RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id);
END;
$body$);

    v_envelope := (api.mcp_call_tool('test_schema_depth',
        '{"kind": "a", "owner": {"name": "Ann", "age": null}, "tags": ["x"]}', NULL, '"s0"')).envelope;
    IF v_envelope->'result'->'isError' IS DISTINCT FROM 'false'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: conforming arguments must reach the tool, got %', v_envelope;
    END IF;

    FOR v_text, v_envelope IN SELECT c.want, (api.mcp_call_tool('test_schema_depth', c.args::jsonb, NULL, '"s1"')).envelope
        FROM (VALUES
            ('extra is not allowed',            '{"kind": "a", "owner": {"name": "Ann"}, "extra": 1}'),
            ('kind must be one of "a", "b"',    '{"kind": "c", "owner": {"name": "Ann"}}'),
            ('owner.name is required',          '{"kind": "a", "owner": {}}'),
            ('owner.age must be integer or null', '{"kind": "a", "owner": {"name": "Ann", "age": 1.5}}'),
            ('tags[1] must be string',          '{"kind": "a", "owner": {"name": "Ann"}, "tags": ["x", 2]}')
        ) AS c(want, args)
    LOOP
        IF v_envelope->'result'->'isError' IS DISTINCT FROM 'true'::jsonb
           OR coalesce(v_envelope->'result'->'content'->0->>'text', '') NOT LIKE '%' || v_text || '%' THEN
            RAISE EXCEPTION 'TEST FAILED: the tool must refuse with "%", got %', v_text, v_envelope;
        END IF;
    END LOOP;

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-e570-4000-8000-000000000002', 'path', '/schema-depth-probe',
            'httpMethod', '^POST$', 'name', 'schema_depth_probe', 'access', 'public',
            'inputSchema', (v_schema - '$defs') || jsonb_build_object('properties',
                (v_schema->'properties') || jsonb_build_object('owner', v_schema->'$defs'->'person')),
            'outputSchema', '{"type": "object"}'::jsonb),
        $body$
BEGIN
    RETURN api.json_response(200, '{}'::jsonb);
END;
$body$);
    v_response := api.rest_invoke('POST', '/schema-depth-probe', ''::extensions.hstore,
        '{"kind": "c", "owner": {"name": 7}}'::jsonb);
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 400 OR v_body->>'code' IS DISTINCT FROM 'pgmi.invalid_body'
       OR (SELECT jsonb_agg(p->>'name' ORDER BY p->>'name') FROM jsonb_array_elements(v_body->'invalid-params') p)
          IS DISTINCT FROM '["kind", "owner.name"]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a REST body must be refused for an enum and a nested type, naming both, got % %',
            (v_response).status_code, v_body;
    END IF;

    v_envelope := (api.mcp_handle_request(
        '{"jsonrpc": "2.0", "id": "r1", "method": "resources/read", "params": {"uri": "zz://nope"}}'::jsonb)).envelope;
    IF v_envelope->'error'->'code' IS DISTINCT FROM '-32002'::jsonb
       OR v_envelope->'error'->'data'->>'uri' IS DISTINCT FROM 'zz://nope' THEN
        RAISE EXCEPTION 'TEST FAILED: a legacy-era missing resource answers -32002 with data.uri, got %', v_envelope;
    END IF;
    v_envelope := (api.mcp_handle_request(jsonb_build_object('jsonrpc', '2.0', 'id', 'r2', 'method', 'resources/read',
        'params', jsonb_build_object('uri', 'zz://nope',
            '_meta', jsonb_build_object('io.modelcontextprotocol/protocolVersion', '2026-07-28',
                                        'io.modelcontextprotocol/clientCapabilities', '{}'::jsonb))))).envelope;
    IF v_envelope->'error'->'code' IS DISTINCT FROM '-32602'::jsonb
       OR v_envelope->'error'->'data'->>'uri' IS DISTINCT FROM 'zz://nope' THEN
        RAISE EXCEPTION 'TEST FAILED: a 2026-07-28 missing resource answers -32602 with data.uri, got %', v_envelope;
    END IF;
END $$;

-- RPC: the declared accepts, produces and inputSchema (of params) are
-- enforced before the handler runs, as for REST.
DO $$
DECLARE
    v_id       uuid := 'f6370000-0000-4000-8000-000000000001';
    v_response api.http_response;
    v_body     jsonb;
    v_ran      boolean;
    v_call     bytea := convert_to('{"jsonrpc": "2.0", "id": 9, "method": "test.schema_probe", "params": {"x": "notint"}}', 'UTF8');
BEGIN
    PERFORM api.create_or_replace_rpc_handler(jsonb_build_object(
        'id', v_id, 'methodName', 'test.schema_probe', 'access', 'public',
        'inputSchema', jsonb_build_object('type', 'object', 'required', jsonb_build_array('x'),
                                          'properties', jsonb_build_object('x', jsonb_build_object('type', 'integer')))),
        $body$
BEGIN
    PERFORM set_config('test.rpc_probe_ran', 'yes', true);
    RETURN api.jsonrpc_success(jsonb_build_object('ok', true), api.content_json((request).content)->'id');
END;
$body$);

    v_response := api.rpc_invoke(v_id, ''::extensions.hstore, v_call);
    v_body := api.content_json((v_response).content);
    v_ran := coalesce(current_setting('test.rpc_probe_ran', true) = 'yes', false);
    IF (v_response).status_code IS DISTINCT FROM 400
       OR v_body->'error'->'code' IS DISTINCT FROM '-32602'::jsonb
       OR v_body->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.invalid_body'
       OR v_body->'error'->'data'->'invalid-params'->0->>'name' IS DISTINCT FROM 'x'
       OR v_body->'id' IS DISTINCT FROM '9'::jsonb
       OR v_ran IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: RPC params breaking the inputSchema must be 400 -32602 naming x, the handler not run, got % % (ran: %)',
            (v_response).status_code, v_body, current_setting('test.rpc_probe_ran', true);
    END IF;

    v_response := api.rpc_invoke(v_id, extensions.hstore('Content-Type', 'text/xml'),
        convert_to('{"jsonrpc": "2.0", "id": 9, "method": "test.schema_probe", "params": {"x": 1}}', 'UTF8'));
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 415
       OR v_body->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.unsupported_media_type' THEN
        RAISE EXCEPTION 'TEST FAILED: an RPC body of an undeclared content type must be 415, got % %', (v_response).status_code, v_body;
    END IF;

    v_response := api.rpc_invoke(v_id, extensions.hstore('accept', 'text/html'),
        convert_to('{"jsonrpc": "2.0", "id": 9, "method": "test.schema_probe", "params": {"x": 1}}', 'UTF8'));
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 406
       OR v_body->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.not_acceptable' THEN
        RAISE EXCEPTION 'TEST FAILED: an RPC Accept matching nothing produced must be 406, got % %', (v_response).status_code, v_body;
    END IF;

    v_ran := coalesce(current_setting('test.rpc_probe_ran', true) = 'yes', false);
    IF v_ran IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: a refused RPC call ran its handler';
    END IF;

    v_response := api.rpc_invoke(v_id, ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "id": 9, "method": "test.schema_probe", "params": {"x": 1}}', 'UTF8'));
    v_ran := coalesce(current_setting('test.rpc_probe_ran', true) = 'yes', false);
    IF (v_response).status_code IS DISTINCT FROM 200 OR v_ran IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: valid RPC params must reach the handler, got %', (v_response).status_code;
    END IF;
    RAISE DEBUG '  + RPC enforces accepts, produces and the inputSchema of params';
END $$;

-- REST: a #/components/schemas/ reference below the root is followed, and the
-- boolean schema false refuses any value.
DO $$
DECLARE
    v_response api.http_response;
    v_body     jsonb;
BEGIN
    PERFORM api.create_or_replace_schema_component('TestNestedOwner',
        '{"type": "object", "required": ["name"], "properties": {"name": {"type": "string", "maxLength": 3}}}');
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-e570-4000-8000-000000000003', 'path', '/schema-ref-probe',
            'httpMethod', '^POST$', 'name', 'schema_ref_probe', 'access', 'public',
            'inputSchema', '{"type": "object", "properties": {
                "owner": {"$ref": "#/components/schemas/TestNestedOwner"}, "legacy": false}}'::jsonb,
            'outputSchema', '{"type": "object"}'::jsonb),
        $body$
BEGIN
    RETURN api.json_response(200, '{}'::jsonb);
END;
$body$);

    v_response := api.rest_invoke('POST', '/schema-ref-probe', ''::extensions.hstore,
        '{"owner": {"name": "Annabel"}, "legacy": 1}'::jsonb);
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 400
       OR (SELECT jsonb_agg(p->>'name' ORDER BY p->>'name') FROM jsonb_array_elements(v_body->'invalid-params') p)
          IS DISTINCT FROM '["legacy", "owner.name"]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a REST body must be checked through a nested component and against false, got % %',
            (v_response).status_code, v_body;
    END IF;

    v_response := api.rest_invoke('POST', '/schema-ref-probe', ''::extensions.hstore, '{"owner": {"name": "Ann"}}'::jsonb);
    PERFORM pg_temp.expect_status('a conforming REST body must reach the handler', v_response, 200);
    RAISE DEBUG '  + REST follows nested component references and refuses false';
END $$;
