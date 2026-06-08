-- ============================================================================
-- Test: Gateway Error Handling
-- ============================================================================
-- Validates that handlers throwing exceptions:
-- 1. Return sanitized errors to clients (security: hide internal details)
-- 2. Log full error details to exchange tables (debugging: engineers can troubleshoot)
-- Tests the EXCEPTION WHEN OTHERS blocks in gateways.
-- ============================================================================

DO $$
DECLARE
    v_response api.http_response;
    v_mcp_response api.mcp_response;
    v_content jsonb;
    v_route_id uuid;
    v_exchange_detail text;
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
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object()),
            'requiresAuth', false
        ),
        $body$
BEGIN
    RAISE EXCEPTION 'Deliberate MCP test error';
END;
        $body$
    );

    -- ========================================================================
    -- Test: REST error handling
    -- Client receives: sanitized error (no internal details exposed)
    -- Exchange table: full error logged for debugging
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/test-error-rest', ''::extensions.hstore, NULL::bytea);

    IF (v_response).status_code != 500 THEN
        RAISE EXCEPTION 'TEST FAILED: REST error should return 500, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);

    IF v_content->>'type' != 'about:blank' OR v_content->>'title' != 'Internal Server Error' THEN
        RAISE EXCEPTION 'TEST FAILED: REST error should return RFC 7807 problem format';
    END IF;

    -- Client should receive sanitized error (not exposing internal details)
    IF v_content->>'detail' LIKE '%Deliberate REST test error%' THEN
        RAISE EXCEPTION 'TEST FAILED: REST error detail should NOT expose internal exception to clients';
    END IF;

    IF v_content->>'detail' != 'An internal error occurred' THEN
        RAISE EXCEPTION 'TEST FAILED: REST error detail should be sanitized, got: %', v_content->>'detail';
    END IF;

    -- Full error should be logged in exchange table for debugging
    SELECT api.content_json((response).content)->>'detail'
    INTO v_exchange_detail
    FROM api.rest_exchange
    WHERE handler_object_id = 'ffffffff-e001-4000-8000-000000000001'::uuid
    ORDER BY enqueued_at DESC LIMIT 1;

    IF v_exchange_detail NOT LIKE '%Deliberate REST test error%' THEN
        RAISE EXCEPTION 'TEST FAILED: REST exchange table should contain full error for debugging, got: %', v_exchange_detail;
    END IF;

    RAISE NOTICE '  ✓ REST: Client gets sanitized error, exchange table has full error for debugging';

    -- ========================================================================
    -- Test: RPC error handling
    -- Client receives: sanitized error
    -- Exchange table: full error logged for debugging
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

    -- Client should receive sanitized error
    IF v_content->'error'->>'message' LIKE '%Deliberate RPC test error%' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC error should NOT expose internal exception to clients';
    END IF;

    IF v_content->'error'->>'message' != 'Internal error' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC error message should be sanitized, got: %', v_content->'error'->>'message';
    END IF;

    -- Full error should be logged in exchange table for debugging
    SELECT api.content_json((response).content)->'error'->>'message'
    INTO v_exchange_detail
    FROM api.rpc_exchange
    WHERE handler_object_id = 'ffffffff-e002-4000-8000-000000000001'::uuid
    ORDER BY enqueued_at DESC LIMIT 1;

    IF v_exchange_detail NOT LIKE '%Deliberate RPC test error%' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC exchange table should contain full error for debugging, got: %', v_exchange_detail;
    END IF;

    RAISE NOTICE '  ✓ RPC: Client gets sanitized error, exchange table has full error for debugging';

    -- ========================================================================
    -- Test: MCP tool error handling
    -- Client receives: sanitized error
    -- Exchange table: full error logged for debugging
    -- ========================================================================

    v_mcp_response := api.mcp_call_tool('test_error_tool', '{}'::jsonb, NULL, '"err-req-1"'::jsonb);

    IF (v_mcp_response).envelope->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP error should be JSON-RPC 2.0 format';
    END IF;

    -- MCP spec: tool *execution* failures MUST use result.isError=true, NOT
    -- the JSON-RPC error envelope. Reflects the C1 fix from Wave A.
    IF (v_mcp_response).envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Tool execution failure must NOT use JSON-RPC error channel (got %)', (v_mcp_response).envelope->'error';
    END IF;

    IF ((v_mcp_response).envelope->'result'->>'isError')::boolean IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: MCP tool execution failure must set result.isError=true';
    END IF;

    IF (v_mcp_response).envelope->>'id' != 'err-req-1' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP error should preserve request_id in envelope.id';
    END IF;

    -- Client should receive sanitized error via result.content (NOT envelope.error)
    IF (v_mcp_response).envelope->'result'->'content'->0->>'text' LIKE '%Deliberate MCP test error%' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP error should NOT expose internal exception, got: %',
            (v_mcp_response).envelope->'result'->'content'->0->>'text';
    END IF;

    IF (v_mcp_response).envelope->'result'->'content'->0->>'text' != 'Tool execution failed' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP error should be sanitized to "Tool execution failed", got: %',
            (v_mcp_response).envelope->'result'->'content'->0->>'text';
    END IF;

    -- Full error should be logged in exchange table for debugging
    SELECT (response).envelope->'result'->'content'->0->>'text'
    INTO v_exchange_detail
    FROM api.mcp_exchange
    WHERE handler_object_id = 'ffffffff-e003-4000-8000-000000000001'::uuid
    ORDER BY enqueued_at DESC LIMIT 1;

    IF v_exchange_detail NOT LIKE '%Deliberate MCP test error%' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP exchange table should contain full error for debugging, got: %', v_exchange_detail;
    END IF;

    RAISE NOTICE '  ✓ MCP: Client gets sanitized error, exchange table has full error for debugging';

    -- ========================================================================
    -- Test: constraint violations map to 4xx (not a blanket 500)
    -- An uncaught unique_violation is semantically 409 Conflict.
    -- ========================================================================

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-e004-4000-8000-000000000001',
            'uri', '^/test-conflict-rest$',
            'httpMethod', '^GET$',
            'name', 'test_conflict_rest',
            'description', 'Handler that raises unique_violation',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RAISE unique_violation USING MESSAGE = 'duplicate key value for test';
END;
        $body$
    );

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-e005-4000-8000-000000000001',
            'methodName', 'test.conflict',
            'description', 'RPC handler that raises unique_violation',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RAISE unique_violation USING MESSAGE = 'duplicate key value for test';
END;
        $body$
    );

    v_response := api.rest_invoke('GET', '/test-conflict-rest', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code != 409 THEN
        RAISE EXCEPTION 'TEST FAILED: REST unique_violation should map to 409, got %', (v_response).status_code;
    END IF;
    v_content := api.content_json((v_response).content);
    IF v_content->>'detail' LIKE '%duplicate key value for test%' THEN
        RAISE EXCEPTION 'TEST FAILED: REST 409 must not leak the raw SQLERRM to clients';
    END IF;
    RAISE NOTICE '  ✓ REST: unique_violation maps to sanitized 409 Conflict';

    v_route_id := api.rpc_resolve('test.conflict');
    v_response := api.rpc_invoke(
        v_route_id,
        ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.conflict", "id": "conf-1"}', 'UTF8')
    );
    IF (v_response).status_code != 409 THEN
        RAISE EXCEPTION 'TEST FAILED: RPC unique_violation should map to HTTP 409, got %', (v_response).status_code;
    END IF;
    v_content := api.content_json((v_response).content);
    IF (v_content->'error'->>'code')::int != -32602 THEN
        RAISE EXCEPTION 'TEST FAILED: RPC client-error should use code -32602, got %', v_content->'error'->>'code';
    END IF;
    IF v_content->'error'->>'message' LIKE '%duplicate key value for test%' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC 409 must not leak the raw SQLERRM to clients';
    END IF;
    RAISE NOTICE '  ✓ RPC: unique_violation maps to sanitized HTTP 409 / -32602';

    RAISE NOTICE '✓ Gateway Error Handling tests passed';
END $$;
