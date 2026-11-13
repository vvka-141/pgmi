-- ============================================================================
-- Test: REST, RPC and MCP each dispatch a registered handler and answer in
-- their protocol's shape
-- ============================================================================
-- Each block registers its own handlers, so nothing here depends on project
-- code. They live until this file's savepoint rolls back, so later blocks can
-- see earlier blocks' handlers.
-- ============================================================================

-- ############################################################################
-- REST - handlers answer with their status and body, an unknown path is 404,
-- and the gateway finalizes the response headers
-- ############################################################################
DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-1001-4000-8000-000000000001',
            'path', '/test-hello',
            'httpMethod', '^GET$',
            'name', 'test_hello',
            'description', 'Test hello endpoint',
            'access', 'public'
        ),
        $body$
DECLARE
    v_name text;
BEGIN
    v_name := COALESCE(api.query_params((request).url)->'name', 'World');
    RETURN api.json_response(200, jsonb_build_object(
        'message', 'Hello, ' || v_name || '!'
    ));
END;
        $body$
    );

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-1002-4000-8000-000000000001',
            'path', '/test-echo',
            'httpMethod', '^POST$',
            'name', 'test_echo',
            'description', 'Test echo endpoint',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object(
        'method', (request).method,
        'url', (request).url,
        'body', api.content_json((request).content)
    ));
END;
        $body$
    );

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-1003-4000-8000-000000000001',
            'path', '/test-health',
            'httpMethod', '^GET$',
            'name', 'test_health',
            'description', 'Test health endpoint',
            'autoLog', false,
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('status', 'healthy'));
END;
        $body$
    );

    -- REST Hello World (without query params)
    v_response := api.rest_invoke('GET', '/test-hello', ''::extensions.hstore, NULL::bytea);

    PERFORM pg_temp.expect_status('GET /test-hello', v_response, 200);

    v_content := api.content_json((v_response).content);
    IF v_content->>'message' IS DISTINCT FROM 'Hello, World!' THEN
        RAISE EXCEPTION 'TEST FAILED: GET /test-hello should return "Hello, World!", got "%"', v_content->>'message';
    END IF;

    -- REST Hello World (with query params)
    v_response := api.rest_invoke('GET', '/test-hello?name=Claude', ''::extensions.hstore, NULL::bytea);

    PERFORM pg_temp.expect_status('GET /test-hello?name=Claude', v_response, 200);

    v_content := api.content_json((v_response).content);
    IF v_content->>'message' IS DISTINCT FROM 'Hello, Claude!' THEN
        RAISE EXCEPTION 'TEST FAILED: GET /test-hello?name=Claude should return "Hello, Claude!", got "%"', v_content->>'message';
    END IF;

    -- REST Echo
    v_response := api.rest_invoke(
        'POST',
        '/test-echo',
        ''::extensions.hstore,
        convert_to('{"test": "data"}', 'UTF8')
    );

    PERFORM pg_temp.expect_status('REST /test-echo', v_response, 200);

    v_content := api.content_json((v_response).content);
    IF v_content->>'method' IS DISTINCT FROM 'POST' THEN
        RAISE EXCEPTION 'TEST FAILED: REST /test-echo wrong method echo';
    END IF;

    -- REST Health Check
    v_response := api.rest_invoke('GET', '/test-health', ''::extensions.hstore, NULL::bytea);

    PERFORM pg_temp.expect_status('REST /test-health', v_response, 200);

    v_content := api.content_json((v_response).content);
    IF v_content->>'status' IS DISTINCT FROM 'healthy' THEN
        RAISE EXCEPTION 'TEST FAILED: REST /test-health wrong status';
    END IF;

    -- REST 404 Not Found
    v_response := api.rest_invoke('GET', '/nonexistent-route-xyz', ''::extensions.hstore, NULL::bytea);

    PERFORM pg_temp.expect_status('REST /nonexistent-route-xyz', v_response, 404);

    -- Response Header Finalization
    -- Guards internal.finalize_response_headers: the content-type default
    -- fires only when a handler returns headers WITHOUT content-type (the
    -- api.json_response builder always sets one, so this needs a handler
    -- that constructs api.http_response bare), registered responseHeaders
    -- merge lowercased, and the x-include-schema directive never reaches
    -- the wire.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-1004-4000-8000-000000000001',
            'path', '/test-bare-headers',
            'httpMethod', '^GET$',
            'name', 'test_bare_headers',
            'description', 'Returns a response with no headers at all',
            'access', 'public',
            'responseHeaders', jsonb_build_object(
                'X-Custom-Header', 'custom-value',
                'x-include-schema', 'false'
            )
        ),
        $body$
BEGIN
    RETURN (200, NULL, convert_to('{"ok":true}', 'UTF8'))::api.http_response;
END;
        $body$
    );

    v_response := api.rest_invoke('GET', '/test-bare-headers', ''::extensions.hstore, NULL::bytea);

    PERFORM pg_temp.expect_status('/test-bare-headers', v_response, 200);
    IF (v_response).headers->'content-type' IS DISTINCT FROM 'application/json; charset=utf-8' THEN
        RAISE EXCEPTION 'TEST FAILED: bare response should get JSON content-type default, got %',
            (v_response).headers->'content-type';
    END IF;
    IF (v_response).headers->'x-custom-header' IS DISTINCT FROM 'custom-value' THEN
        RAISE EXCEPTION 'TEST FAILED: registered responseHeaders should merge lowercased, got %',
            (v_response).headers->'x-custom-header';
    END IF;
    IF (v_response).headers ? 'x-include-schema' THEN
        RAISE EXCEPTION 'TEST FAILED: x-include-schema directive must not appear on the wire';
    END IF;
    IF (v_response).headers->'content-length' IS DISTINCT FROM octet_length((v_response).content)::text THEN
        RAISE EXCEPTION 'TEST FAILED: content-length should match body size, got %',
            (v_response).headers->'content-length';
    END IF;
    IF coalesce((v_response).headers ? 'x-route-id', false) IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'TEST FAILED: x-route-id should be stamped on REST responses';
    END IF;
END $$;

-- ############################################################################
-- RPC - a method resolves and answers a JSON-RPC result; an unknown one does
-- not resolve; a notification runs nothing and answers 202
-- ############################################################################
DO $$
DECLARE
    v_response api.http_response;
    v_route_id uuid;
    v_content jsonb;
BEGIN
    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-2001-4000-8000-000000000001',
            'methodName', 'test.sum',
            'description', 'Test sum method',
            'access', 'public'
        ),
        $body$
DECLARE
    v_params jsonb;
    v_a numeric;
    v_b numeric;
BEGIN
    v_params := api.content_json((request).content)->'params';
    v_a := (v_params->>'a')::numeric;
    v_b := (v_params->>'b')::numeric;
    RETURN api.jsonrpc_success(
        jsonb_build_object('result', v_a + v_b),
        api.content_json((request).content)->'id'
    );
END;
        $body$
    );

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-2002-4000-8000-000000000001',
            'methodName', 'test.time',
            'description', 'Test time method',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.jsonrpc_success(
        jsonb_build_object('timestamp', now()),
        api.content_json((request).content)->'id'
    );
END;
        $body$
    );

    -- RPC Method Resolution
    v_route_id := api.rpc_resolve('test.sum');

    IF v_route_id IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: RPC test.sum not resolved';
    END IF;

    -- RPC test.sum
    v_response := api.rpc_invoke(
        v_route_id,
        ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.sum", "params": {"a": 5, "b": 3}, "id": "1"}', 'UTF8')
    );

    PERFORM pg_temp.expect_status('RPC test.sum', v_response, 200);

    v_content := api.content_json((v_response).content);
    IF v_content->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC response missing jsonrpc field';
    END IF;

    IF (v_content->'result'->>'result')::numeric IS DISTINCT FROM 8 THEN
        RAISE EXCEPTION 'TEST FAILED: RPC test.sum(5,3) expected 8, got %', v_content->'result'->>'result';
    END IF;

    -- RPC test.time
    v_route_id := api.rpc_resolve('test.time');
    v_response := api.rpc_invoke(
        v_route_id,
        ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.time", "id": "2"}', 'UTF8')
    );

    PERFORM pg_temp.expect_status('RPC test.time', v_response, 200);

    v_content := api.content_json((v_response).content);
    IF v_content->'result'->>'timestamp' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: RPC test.time missing timestamp';
    END IF;

    -- RPC Method Not Found
    v_route_id := api.rpc_resolve('nonexistent.method.xyz');

    IF v_route_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: RPC nonexistent.method.xyz should not resolve';
    END IF;
END $$;

DO $$
DECLARE
    v_response api.http_response;
    v_before bigint;
    v_after bigint;
BEGIN
    CREATE TEMP TABLE rpc_notification_probe (ran int);

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object('id', 'ffffffff-2003-4000-8000-000000000001', 'methodName', 'test.notify',
            'access', 'public'),
        $body$
BEGIN
    INSERT INTO pg_temp.rpc_notification_probe VALUES (1);
    RETURN api.jsonrpc_success(jsonb_build_object('ran', true), api.content_json((request).content)->'id');
END;
        $body$
    );

    SELECT count(*) INTO v_before FROM pg_temp.rpc_notification_probe;
    v_response := api.rpc_invoke(api.rpc_resolve('test.notify'), ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.notify", "params": {}}', 'UTF8'));
    SELECT count(*) INTO v_after FROM pg_temp.rpc_notification_probe;

    IF (v_response).status_code IS DISTINCT FROM 202 OR (v_response).content IS DISTINCT FROM ''::bytea
       OR coalesce((v_response).headers ? 'content-type', true) THEN
        RAISE EXCEPTION 'TEST FAILED: a notification must answer 202 with no body, got % %',
            (v_response).status_code, convert_from(coalesce((v_response).content, ''::bytea), 'UTF8');
    END IF;
    IF v_after IS DISTINCT FROM v_before THEN
        RAISE EXCEPTION 'TEST FAILED: a notification ran its method (% rows before, % after)', v_before, v_after;
    END IF;

    v_response := api.rpc_invoke(api.rpc_resolve('test.notify'), ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.notify", "id": null}', 'UTF8'));
    SELECT count(*) INTO v_after FROM pg_temp.rpc_notification_probe;

    IF (v_response).status_code IS DISTINCT FROM 200
       OR api.content_json((v_response).content)->'result' IS DISTINCT FROM '{"ran": true}'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a request with "id": null must be dispatched, got % %',
            (v_response).status_code, api.content_json((v_response).content);
    END IF;
    IF v_after IS DISTINCT FROM v_before + 1 THEN
        RAISE EXCEPTION 'TEST FAILED: a request with "id": null did not run its method';
    END IF;

    DROP TABLE pg_temp.rpc_notification_probe;
END $$;

-- ############################################################################
-- MCP - tools, resources and prompts answer JSON-RPC 2.0 envelopes under the
-- request id, and the lists name what is registered
-- ############################################################################
DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
    v_list jsonb;
BEGIN
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-3001-4000-8000-000000000001',
            'type', 'tool', 'effect', 'read',
            'name', 'test_tool',
            'description', 'Test tool for protocol validation',
            'inputSchema', jsonb_build_object(
                'type', 'object',
                'properties', jsonb_build_object(),
                'required', jsonb_build_array()
            ),
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('Tool executed')),
        (request).request_id
    );
END;
        $body$
    );

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-3002-4000-8000-000000000001',
            'type', 'resource',
            'name', 'test_resource',
            'description', 'Test resource',
            'uriTemplate', 'test:///{id}',
            'mimeType', 'application/json',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.mcp_resource_result(
        jsonb_build_array(jsonb_build_object(
            'uri', (request).uri,
            'mimeType', 'application/json',
            'text', '{"resource": "data"}'
        )),
        (request).request_id
    );
END;
        $body$
    );

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-3003-4000-8000-000000000001',
            'type', 'prompt',
            'name', 'test_prompt',
            'description', 'Test prompt',
            'arguments', jsonb_build_array(
                jsonb_build_object('name', 'input', 'description', 'Test input', 'required', true)
            ),
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.mcp_prompt_result(
        jsonb_build_array(
            jsonb_build_object(
                'role', 'user',
                'content', jsonb_build_object(
                    'type', 'text',
                    'text', 'Test prompt with input: ' || ((request).arguments->>'input')
                )
            )
        ),
        (request).request_id
    );
END;
        $body$
    );

    -- MCP Tool Invocation (JSON-RPC 2.0 Compliance)
    v_response := api.mcp_call_tool('test_tool', '{}'::jsonb, NULL, '"req-1"'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP tool response missing jsonrpc 2.0';
    END IF;

    IF v_envelope->>'id' IS DISTINCT FROM 'req-1' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP tool request_id not echoed';
    END IF;

    IF v_envelope->'result' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: MCP test_tool missing result field';
    END IF;

    IF v_envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: MCP test_tool should not have error field';
    END IF;

    -- MCP Tool Not Found (JSON-RPC 2.0 Error Format)
    v_response := api.mcp_call_tool('nonexistent_tool_xyz', '{}'::jsonb, NULL, '"req-2"'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP error response missing jsonrpc 2.0';
    END IF;

    IF v_envelope->'error' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: MCP nonexistent_tool_xyz should have error object';
    END IF;

    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32602 THEN
        RAISE EXCEPTION 'TEST FAILED: MCP unknown tool should use code -32602 (Invalid params)';
    END IF;

    -- MCP Resource Read (JSON-RPC 2.0 Compliance)
    v_response := api.mcp_read_resource('test:///123', NULL, '"req-3"'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP resource response missing jsonrpc 2.0';
    END IF;

    IF v_envelope->>'id' IS DISTINCT FROM 'req-3' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP resource request_id not echoed';
    END IF;

    IF v_envelope->'result'->'contents' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: MCP resource missing contents in result';
    END IF;

    -- resource dispatch matches a stored regex, never rebuilds one

    -- The acceptance criterion, asserted against the deployed function body:
    -- uri_template_to_regex must not appear in the dispatch path at all.
    IF pg_get_functiondef('internal.mcp_dispatch(text,text,jsonb,jsonb,jsonb,boolean)'::regprocedure)
       ~ 'uri_template_to_regex' THEN
        RAISE EXCEPTION 'TEST FAILED: mcp_dispatch still converts uri_template per request';
    END IF;

    -- Every resource route carries the derived regex, and it is the one the
    -- converter produces — a stale value would mis-route, not merely slow down.
    IF EXISTS (
        SELECT 1 FROM api.mcp_route
        WHERE mcp_type = 'resource'
          AND uri_regexp IS DISTINCT FROM api.uri_template_to_regex(uri_template)
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: a resource route has a stale or missing uri_regexp';
    END IF;

    -- The trigger, not the registration function, is what guarantees that: prove
    -- it holds for direct DML too (admin_role is granted UPDATE on this table).
    UPDATE api.mcp_route SET uri_template = 'test:///{id}/extra'
    WHERE mcp_name = 'test_resource';

    IF (SELECT uri_regexp FROM api.mcp_route WHERE mcp_name = 'test_resource')
       IS DISTINCT FROM api.uri_template_to_regex('test:///{id}/extra') THEN
        RAISE EXCEPTION 'TEST FAILED: direct UPDATE of uri_template left uri_regexp stale';
    END IF;

    UPDATE api.mcp_route SET uri_template = 'test:///{id}'
    WHERE mcp_name = 'test_resource';

    -- MCP Prompt (JSON-RPC 2.0 Compliance)
    v_response := api.mcp_get_prompt(
        'test_prompt',
        '{"input": "test value"}'::jsonb,
        NULL,
        '"req-4"'::jsonb
    );
    v_envelope := (v_response).envelope;

    IF v_envelope->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP prompt response missing jsonrpc 2.0';
    END IF;

    IF v_envelope->>'id' IS DISTINCT FROM 'req-4' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP prompt request_id not echoed';
    END IF;

    IF v_envelope->'result'->'messages' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: MCP prompt missing messages in result';
    END IF;

    -- MCP Discovery Functions
    v_list := api.mcp_list_tools();
    IF v_list->'tools' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: mcp_list_tools missing tools key';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'tools') AS tool
        WHERE tool->>'name' = 'test_tool'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: test_tool not in list';
    END IF;

    v_list := api.mcp_list_resources();
    IF v_list->'resources' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: mcp_list_resources missing resources key';
    END IF;

    -- test_resource is templated (test:///{id}); it belongs to
    -- resources/templates/list, not resources/list.
    v_list := api.mcp_list_resource_templates();
    IF v_list->'resourceTemplates' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: mcp_list_resource_templates missing resourceTemplates key';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'resourceTemplates') AS resource
        WHERE resource->>'name' = 'test_resource' AND resource->>'uriTemplate' = 'test:///{id}'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: test_resource not in resource templates list';
    END IF;

    v_list := api.mcp_list_prompts();
    IF v_list->'prompts' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: mcp_list_prompts missing prompts key';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'prompts') AS prompt
        WHERE prompt->>'name' = 'test_prompt'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: test_prompt not in list';
    END IF;
END $$;

-- ############################################################################
-- REGISTRY - every protocol has handlers and routes
-- ############################################################################
DO $$
DECLARE
    v_rest_count int;
    v_rpc_count int;
    v_mcp_count int;
BEGIN
    -- Register one handler of each type to verify registration works
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-4001-4000-8000-000000000001',
            'path', '/test-registry',
            'httpMethod', '^GET$',
            'name', 'test_registry_rest',
            'description', 'Registry test REST',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, '{}'::jsonb);
END;
        $body$
    );

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-4002-4000-8000-000000000001',
            'methodName', 'test.registry',
            'description', 'Registry test RPC',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.jsonrpc_success('{}'::jsonb, NULL);
END;
        $body$
    );

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-4003-4000-8000-000000000001',
            'type', 'tool', 'effect', 'read',
            'name', 'test_registry_tool',
            'description', 'Registry test MCP tool',
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object()),
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(jsonb_build_array(), (request).request_id);
END;
        $body$
    );

    -- Handler Registry Contents
    SELECT COUNT(*) INTO v_rest_count FROM api.handler WHERE handler_type = 'rest';
    SELECT COUNT(*) INTO v_rpc_count FROM api.handler WHERE handler_type = 'rpc';
    SELECT COUNT(*) INTO v_mcp_count FROM api.handler WHERE handler_type IN ('mcp_tool', 'mcp_resource', 'mcp_prompt');

    IF (v_rest_count = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: No REST handlers in registry';
    END IF;

    IF (v_rpc_count = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: No RPC handlers in registry';
    END IF;

    IF (v_mcp_count = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: No MCP handlers in registry';
    END IF;

    -- Route Table Integrity
    IF NOT EXISTS (SELECT 1 FROM api.rest_route) THEN
        RAISE EXCEPTION 'TEST FAILED: No REST routes';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM api.rpc_route) THEN
        RAISE EXCEPTION 'TEST FAILED: No RPC routes';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM api.mcp_route) THEN
        RAISE EXCEPTION 'TEST FAILED: No MCP routes';
    END IF;
END $$;

DO $$
BEGIN
END $$;
