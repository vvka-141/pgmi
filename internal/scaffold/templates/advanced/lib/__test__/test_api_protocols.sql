-- ============================================================================
-- Test: API Protocol Integration Tests (Self-Contained)
-- ============================================================================
-- Validates that all three protocols (REST, RPC, MCP) work correctly.
-- Each test block registers its own handlers - no dependency on user code.
-- All created handlers roll back after each DO block ends.
-- ============================================================================

DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
BEGIN
    RAISE NOTICE '→ Testing REST Protocol';

    -- Register test handlers within this block
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-1001-4000-8000-000000000001',
            'uri', '^/test-hello(\?.*)?$',
            'httpMethod', '^GET$',
            'name', 'test_hello',
            'description', 'Test hello endpoint',
            'requiresAuth', false
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
            'uri', '^/test-echo(\?.*)?$',
            'httpMethod', '^POST$',
            'name', 'test_echo',
            'description', 'Test echo endpoint',
            'requiresAuth', false
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
            'uri', '^/test-health(\?.*)?$',
            'httpMethod', '^GET$',
            'name', 'test_health',
            'description', 'Test health endpoint',
            'autoLog', false,
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('status', 'healthy'));
END;
        $body$
    );

    -- ========================================================================
    -- Test: REST Hello World (without query params)
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/test-hello', ''::extensions.hstore, NULL);

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: GET /test-hello expected 200, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF v_content->>'message' != 'Hello, World!' THEN
        RAISE EXCEPTION 'TEST FAILED: GET /test-hello should return "Hello, World!", got "%"', v_content->>'message';
    END IF;

    RAISE NOTICE '  ✓ GET /test-hello returns "Hello, World!"';

    -- ========================================================================
    -- Test: REST Hello World (with query params)
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/test-hello?name=Claude', ''::extensions.hstore, NULL);

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: GET /test-hello?name=Claude expected 200, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF v_content->>'message' != 'Hello, Claude!' THEN
        RAISE EXCEPTION 'TEST FAILED: GET /test-hello?name=Claude should return "Hello, Claude!", got "%"', v_content->>'message';
    END IF;

    RAISE NOTICE '  ✓ GET /test-hello?name=Claude returns "Hello, Claude!"';

    -- ========================================================================
    -- Test: REST Echo
    -- ========================================================================

    v_response := api.rest_invoke(
        'POST',
        '/test-echo',
        ''::extensions.hstore,
        convert_to('{"test": "data"}', 'UTF8')
    );

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: REST /test-echo expected 200, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF v_content->>'method' != 'POST' THEN
        RAISE EXCEPTION 'TEST FAILED: REST /test-echo wrong method echo';
    END IF;

    RAISE NOTICE '  ✓ POST /test-echo returns 200 with echoed body';

    -- ========================================================================
    -- Test: REST Health Check
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/test-health', ''::extensions.hstore, NULL);

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: REST /test-health expected 200, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF v_content->>'status' != 'healthy' THEN
        RAISE EXCEPTION 'TEST FAILED: REST /test-health wrong status';
    END IF;

    RAISE NOTICE '  ✓ GET /test-health returns 200 with healthy status';

    -- ========================================================================
    -- Test: REST 404 Not Found
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/nonexistent-route-xyz', ''::extensions.hstore, NULL);

    IF (v_response).status_code != 404 THEN
        RAISE EXCEPTION 'TEST FAILED: REST /nonexistent-route-xyz expected 404, got %', (v_response).status_code;
    END IF;

    RAISE NOTICE '  ✓ GET /nonexistent-route-xyz returns 404';

    RAISE NOTICE '✓ REST Protocol tests passed';
END $$;

DO $$
DECLARE
    v_response api.http_response;
    v_route_id uuid;
    v_content jsonb;
BEGIN
    RAISE NOTICE '→ Testing RPC Protocol';

    -- Register test RPC handlers
    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-2001-4000-8000-000000000001',
            'methodName', 'test.sum',
            'description', 'Test sum method',
            'requiresAuth', false
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
            'requiresAuth', false
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

    -- ========================================================================
    -- Test: RPC Method Resolution
    -- ========================================================================

    v_route_id := api.rpc_resolve('test.sum');

    IF v_route_id IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: RPC test.sum not resolved';
    END IF;

    RAISE NOTICE '  ✓ RPC method test.sum resolves to UUID';

    -- ========================================================================
    -- Test: RPC test.sum
    -- ========================================================================

    v_response := api.rpc_invoke(
        v_route_id,
        ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.sum", "params": {"a": 5, "b": 3}, "id": "1"}', 'UTF8')
    );

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: RPC test.sum expected 200, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF v_content->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC response missing jsonrpc field';
    END IF;

    IF (v_content->'result'->>'result')::numeric != 8 THEN
        RAISE EXCEPTION 'TEST FAILED: RPC test.sum(5,3) expected 8, got %', v_content->'result'->>'result';
    END IF;

    RAISE NOTICE '  ✓ RPC test.sum(5,3) returns 8';

    -- ========================================================================
    -- Test: RPC test.time
    -- ========================================================================

    v_route_id := api.rpc_resolve('test.time');
    v_response := api.rpc_invoke(
        v_route_id,
        ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.time", "id": "2"}', 'UTF8')
    );

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: RPC test.time expected 200, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF v_content->'result'->>'timestamp' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: RPC test.time missing timestamp';
    END IF;

    RAISE NOTICE '  ✓ RPC test.time returns timestamp';

    -- ========================================================================
    -- Test: RPC Method Not Found
    -- ========================================================================

    v_route_id := api.rpc_resolve('nonexistent.method.xyz');

    IF v_route_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: RPC nonexistent.method.xyz should not resolve';
    END IF;

    RAISE NOTICE '  ✓ RPC nonexistent.method.xyz returns NULL';

    RAISE NOTICE '✓ RPC Protocol tests passed';
END $$;

DO $$
DECLARE
    v_response api.mcp_response;
    v_list jsonb;
BEGIN
    RAISE NOTICE '→ Testing MCP Protocol';

    -- Register test MCP handlers
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-3001-4000-8000-000000000001',
            'type', 'tool',
            'name', 'test_tool',
            'description', 'Test tool for protocol validation',
            'inputSchema', jsonb_build_object(
                'type', 'object',
                'properties', jsonb_build_object(),
                'required', jsonb_build_array()
            ),
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('Tool executed successfully')),
        (request).request_id,
        false
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
            'requiresAuth', false
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
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN (
        jsonb_build_object('messages', jsonb_build_array(
            jsonb_build_object(
                'role', 'user',
                'content', jsonb_build_object(
                    'type', 'text',
                    'text', 'Test prompt with input: ' || ((request).arguments->>'input')
                )
            )
        )),
        (request).request_id
    )::api.mcp_response;
END;
        $body$
    );

    -- ========================================================================
    -- Test: MCP Tool Invocation
    -- ========================================================================

    v_response := api.mcp_call_tool('test_tool', '{}'::jsonb, NULL, 'req-1');

    IF (v_response).request_id != 'req-1' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP tool request_id not echoed';
    END IF;

    IF (v_response).result->'isError' = 'true'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: MCP test_tool returned error';
    END IF;

    RAISE NOTICE '  ✓ MCP tool test_tool returns result';

    -- ========================================================================
    -- Test: MCP Tool Not Found
    -- ========================================================================

    v_response := api.mcp_call_tool('nonexistent_tool_xyz', '{}'::jsonb, NULL, 'req-2');

    IF (v_response).result->'isError' != 'true'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: MCP nonexistent_tool_xyz should return error';
    END IF;

    RAISE NOTICE '  ✓ MCP nonexistent tool returns error';

    -- ========================================================================
    -- Test: MCP Resource Read
    -- ========================================================================

    v_response := api.mcp_read_resource('test:///123', NULL, 'req-3');

    IF (v_response).request_id != 'req-3' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP resource request_id not echoed';
    END IF;

    RAISE NOTICE '  ✓ MCP resource read returns result';

    -- ========================================================================
    -- Test: MCP Prompt
    -- ========================================================================

    v_response := api.mcp_get_prompt(
        'test_prompt',
        '{"input": "test value"}'::jsonb,
        NULL,
        'req-4'
    );

    IF (v_response).request_id != 'req-4' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP prompt request_id not echoed';
    END IF;

    IF (v_response).result->'messages' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: MCP prompt missing messages';
    END IF;

    RAISE NOTICE '  ✓ MCP prompt returns messages';

    -- ========================================================================
    -- Test: MCP Discovery Functions
    -- ========================================================================

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

    RAISE NOTICE '  ✓ MCP list_tools returns tools array with test_tool';

    v_list := api.mcp_list_resources();
    IF v_list->'resources' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: mcp_list_resources missing resources key';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'resources') AS resource
        WHERE resource->>'name' = 'test_resource'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: test_resource not in list';
    END IF;

    RAISE NOTICE '  ✓ MCP list_resources returns resources array with test_resource';

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

    RAISE NOTICE '  ✓ MCP list_prompts returns prompts array with test_prompt';

    RAISE NOTICE '✓ MCP Protocol tests passed';
END $$;

DO $$
DECLARE
    v_rest_count int;
    v_rpc_count int;
    v_mcp_count int;
BEGIN
    RAISE NOTICE '→ Testing Handler Registry';

    -- Register one handler of each type to verify registration works
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-4001-4000-8000-000000000001',
            'uri', '^/test-registry$',
            'httpMethod', '^GET$',
            'name', 'test_registry_rest',
            'description', 'Registry test REST',
            'requiresAuth', false
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
            'requiresAuth', false
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
            'type', 'tool',
            'name', 'test_registry_tool',
            'description', 'Registry test MCP tool',
            'inputSchema', '{}'::jsonb,
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(jsonb_build_array(), (request).request_id, false);
END;
        $body$
    );

    -- ========================================================================
    -- Test: Handler Registry Contents
    -- ========================================================================

    SELECT COUNT(*) INTO v_rest_count FROM api.handler WHERE handler_type = 'rest';
    SELECT COUNT(*) INTO v_rpc_count FROM api.handler WHERE handler_type = 'rpc';
    SELECT COUNT(*) INTO v_mcp_count FROM api.handler WHERE handler_type IN ('mcp_tool', 'mcp_resource', 'mcp_prompt');

    IF v_rest_count = 0 THEN
        RAISE EXCEPTION 'TEST FAILED: No REST handlers in registry';
    END IF;

    IF v_rpc_count = 0 THEN
        RAISE EXCEPTION 'TEST FAILED: No RPC handlers in registry';
    END IF;

    IF v_mcp_count = 0 THEN
        RAISE EXCEPTION 'TEST FAILED: No MCP handlers in registry';
    END IF;

    RAISE NOTICE '  ✓ Handler registry contains REST (%), RPC (%), and MCP (%) handlers', v_rest_count, v_rpc_count, v_mcp_count;

    -- ========================================================================
    -- Test: Route Table Integrity
    -- ========================================================================

    IF NOT EXISTS (SELECT 1 FROM api.rest_route) THEN
        RAISE EXCEPTION 'TEST FAILED: No REST routes';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM api.rpc_route) THEN
        RAISE EXCEPTION 'TEST FAILED: No RPC routes';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM api.mcp_route) THEN
        RAISE EXCEPTION 'TEST FAILED: No MCP routes';
    END IF;

    RAISE NOTICE '  ✓ Route tables contain entries for all protocols';

    RAISE NOTICE '✓ Handler Registry tests passed';
END $$;

DO $$
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE '═══════════════════════════════════════════════════════════════';
    RAISE NOTICE '✓ ALL API PROTOCOL TESTS PASSED';
    RAISE NOTICE '═══════════════════════════════════════════════════════════════';
END $$;
