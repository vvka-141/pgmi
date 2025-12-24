-- ============================================================================
-- Test: Gateway Error Handling
-- ============================================================================
-- Validates that handlers throwing exceptions return proper error responses
-- instead of crashing. Tests the EXCEPTION WHEN OTHERS blocks in gateways.
-- ============================================================================

DO $$
DECLARE
    v_response api.http_response;
    v_mcp_response api.mcp_response;
    v_content jsonb;
    v_route_id uuid;
BEGIN
    RAISE NOTICE '→ Testing Gateway Error Handling';

    -- ========================================================================
    -- Register handlers that deliberately throw exceptions
    -- ========================================================================

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-e001-4000-8000-000000000001',
            'uri', '^/test-error-rest$',
            'httpMethod', '^GET$',
            'name', 'test_error_rest',
            'description', 'Handler that throws for testing',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RAISE EXCEPTION 'Deliberate REST test error';
END;
        $body$
    );

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-e002-4000-8000-000000000001',
            'methodName', 'test.error',
            'description', 'RPC handler that throws for testing',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RAISE EXCEPTION 'Deliberate RPC test error';
END;
        $body$
    );

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-e003-4000-8000-000000000001',
            'type', 'tool',
            'name', 'test_error_tool',
            'description', 'MCP tool that throws for testing',
            'inputSchema', '{}'::jsonb,
            'requiresAuth', false
        ),
        $body$
BEGIN
    RAISE EXCEPTION 'Deliberate MCP test error';
END;
        $body$
    );

    -- ========================================================================
    -- Test: REST error handling → 500 with error message
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/test-error-rest', ''::extensions.hstore, NULL);

    IF (v_response).status_code != 500 THEN
        RAISE EXCEPTION 'TEST FAILED: REST error should return 500, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);

    IF v_content->>'type' != 'about:blank' OR v_content->>'title' != 'Internal Server Error' THEN
        RAISE EXCEPTION 'TEST FAILED: REST error should return RFC 7807 problem format';
    END IF;

    IF v_content->>'detail' NOT LIKE '%Deliberate REST test error%' THEN
        RAISE EXCEPTION 'TEST FAILED: REST error detail should contain exception message, got: %', v_content->>'detail';
    END IF;

    RAISE NOTICE '  ✓ REST: Handler exception → 500 with error message in problem response';

    -- ========================================================================
    -- Test: RPC error handling → JSON-RPC error with code -32603
    -- ========================================================================

    v_route_id := api.rpc_resolve('test.error');
    v_response := api.rpc_invoke(
        v_route_id,
        ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.error", "id": "err-1"}', 'UTF8')
    );

    v_content := api.content_json((v_response).content);

    IF v_content->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC error should return JSON-RPC 2.0 format';
    END IF;

    IF (v_content->'error'->>'code')::int != -32603 THEN
        RAISE EXCEPTION 'TEST FAILED: RPC error code should be -32603 (Internal error), got %', v_content->'error'->>'code';
    END IF;

    IF v_content->'error'->>'message' NOT LIKE '%Deliberate RPC test error%' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC error message should contain exception text';
    END IF;

    RAISE NOTICE '  ✓ RPC: Handler exception → JSON-RPC error code -32603';

    -- ========================================================================
    -- Test: MCP tool error handling → isError: true
    -- ========================================================================

    v_mcp_response := api.mcp_call_tool('test_error_tool', '{}'::jsonb, NULL, 'err-req-1');

    IF (v_mcp_response).result->'isError' != 'true'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: MCP tool error should have isError=true';
    END IF;

    IF (v_mcp_response).request_id != 'err-req-1' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP error should preserve request_id';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements((v_mcp_response).result->'content') AS c
        WHERE c->>'text' LIKE '%Deliberate MCP test error%'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: MCP tool error content should contain exception message';
    END IF;

    RAISE NOTICE '  ✓ MCP: Handler exception → isError=true with error message';

    RAISE NOTICE '✓ Gateway Error Handling tests passed';
END $$;
