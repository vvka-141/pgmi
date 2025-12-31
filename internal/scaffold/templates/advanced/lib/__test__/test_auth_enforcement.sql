-- ============================================================================
-- Test: Authentication Enforcement
-- ============================================================================
-- Validates that handlers with requiresAuth (default: true) enforce
-- identity headers/context, while handlers with requiresAuth: false allow
-- unauthenticated access.
-- ============================================================================

DO $$
DECLARE
    v_response api.http_response;
    v_mcp_response api.mcp_response;
    v_content jsonb;
    v_route_id uuid;
BEGIN
    RAISE NOTICE '→ Testing Authentication Enforcement';

    -- ========================================================================
    -- Register protected handlers (requiresAuth defaults to true)
    -- ========================================================================

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-a001-4000-8000-000000000001',
            'uri', '^/test-protected-rest$',
            'httpMethod', '^GET$',
            'name', 'test_protected_rest',
            'description', 'Protected REST handler'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object(
        'user_id', current_setting('auth.user_id', true)
    ));
END;
        $body$
    );

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-a002-4000-8000-000000000001',
            'methodName', 'test.protected',
            'description', 'Protected RPC handler'
        ),
        $body$
BEGIN
    RETURN api.jsonrpc_success(
        jsonb_build_object('user_id', current_setting('auth.user_id', true)),
        api.content_json((request).content)->'id'
    );
END;
        $body$
    );

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-a003-4000-8000-000000000001',
            'type', 'tool',
            'name', 'test_protected_tool',
            'description', 'Protected MCP tool',
            'inputSchema', '{}'::jsonb
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('user_id: ' || current_setting('auth.user_id', true))),
        (request).request_id
    );
END;
        $body$
    );

    -- ========================================================================
    -- Register public handlers (requiresAuth: false)
    -- ========================================================================

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-a004-4000-8000-000000000001',
            'uri', '^/test-public-rest$',
            'httpMethod', '^GET$',
            'name', 'test_public_rest',
            'description', 'Public REST handler',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('public', true));
END;
        $body$
    );

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-a005-4000-8000-000000000001',
            'methodName', 'test.public',
            'description', 'Public RPC handler',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.jsonrpc_success(
        jsonb_build_object('public', true),
        api.content_json((request).content)->'id'
    );
END;
        $body$
    );

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-a006-4000-8000-000000000001',
            'type', 'tool',
            'name', 'test_public_tool',
            'description', 'Public MCP tool',
            'inputSchema', '{}'::jsonb,
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('public access')),
        (request).request_id
    );
END;
        $body$
    );

    -- ========================================================================
    -- Test: REST protected handler rejects unauthenticated requests
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/test-protected-rest', ''::extensions.hstore, NULL);

    IF (v_response).status_code != 401 THEN
        RAISE EXCEPTION 'TEST FAILED: Protected REST without auth should return 401, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF v_content->>'title' != 'Unauthorized' THEN
        RAISE EXCEPTION 'TEST FAILED: 401 response should have title "Unauthorized"';
    END IF;

    RAISE NOTICE '  ✓ REST: Protected handler returns 401 without x-user-id';

    -- ========================================================================
    -- Test: REST protected handler accepts authenticated requests
    -- ========================================================================

    v_response := api.rest_invoke(
        'GET',
        '/test-protected-rest',
        'x-user-id=>user123'::extensions.hstore,
        NULL
    );

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: Protected REST with auth should return 200, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF v_content->>'user_id' != 'user123' THEN
        RAISE EXCEPTION 'TEST FAILED: Session variable auth.user_id should be "user123", got "%"', v_content->>'user_id';
    END IF;

    RAISE NOTICE '  ✓ REST: Protected handler returns 200 with x-user-id, session variable set';

    -- ========================================================================
    -- Test: REST public handler accepts unauthenticated requests
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/test-public-rest', ''::extensions.hstore, NULL);

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: Public REST should return 200, got %', (v_response).status_code;
    END IF;

    RAISE NOTICE '  ✓ REST: Public handler returns 200 without auth';

    -- ========================================================================
    -- Test: RPC protected handler rejects unauthenticated requests
    -- ========================================================================

    v_route_id := api.rpc_resolve('test.protected');
    v_response := api.rpc_invoke(
        v_route_id,
        ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.protected", "id": "1"}', 'UTF8')
    );

    IF (v_response).status_code != 401 THEN
        RAISE EXCEPTION 'TEST FAILED: Protected RPC without auth should return HTTP 401, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF (v_content->'error'->>'code')::int != -32001 THEN
        RAISE EXCEPTION 'TEST FAILED: Protected RPC without auth should return JSON-RPC error -32001, got %', v_content->'error'->>'code';
    END IF;

    RAISE NOTICE '  ✓ RPC: Protected handler returns HTTP 401 with JSON-RPC error -32001';

    -- ========================================================================
    -- Test: RPC protected handler accepts authenticated requests
    -- ========================================================================

    v_response := api.rpc_invoke(
        v_route_id,
        'x-user-id=>user456'::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.protected", "id": "2"}', 'UTF8')
    );

    v_content := api.content_json((v_response).content);
    IF v_content->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Protected RPC with auth should succeed, got error: %', v_content->'error';
    END IF;

    IF v_content->'result'->>'user_id' != 'user456' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC session variable auth.user_id should be "user456"';
    END IF;

    RAISE NOTICE '  ✓ RPC: Protected handler succeeds with x-user-id, session variable set';

    -- ========================================================================
    -- Test: RPC public handler accepts unauthenticated requests
    -- ========================================================================

    v_route_id := api.rpc_resolve('test.public');
    v_response := api.rpc_invoke(
        v_route_id,
        ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.public", "id": "3"}', 'UTF8')
    );

    v_content := api.content_json((v_response).content);
    IF v_content->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Public RPC should succeed, got error: %', v_content->'error';
    END IF;

    RAISE NOTICE '  ✓ RPC: Public handler succeeds without auth';

    -- ========================================================================
    -- Test: MCP protected tool rejects unauthenticated requests
    -- ========================================================================

    -- Reset session auth variables from previous tests
    PERFORM set_config('auth.user_id', '', true);
    PERFORM set_config('auth.user_email', '', true);
    PERFORM set_config('auth.tenant_id', '', true);
    PERFORM set_config('auth.token', '', true);

    v_mcp_response := api.mcp_call_tool('test_protected_tool', '{}'::jsonb, NULL, 'req-1');

    IF (v_mcp_response).envelope->'error' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Protected MCP without context should return JSON-RPC error';
    END IF;

    IF ((v_mcp_response).envelope->'error'->>'code')::int != -32001 THEN
        RAISE EXCEPTION 'TEST FAILED: Protected MCP auth error should have code -32001, got %', (v_mcp_response).envelope->'error'->>'code';
    END IF;

    RAISE NOTICE '  ✓ MCP: Protected tool returns JSON-RPC error -32001 without context.user_id';

    -- ========================================================================
    -- Test: MCP protected tool accepts authenticated requests
    -- ========================================================================

    v_mcp_response := api.mcp_call_tool(
        'test_protected_tool',
        '{}'::jsonb,
        '{"user_id": "user789"}'::jsonb,
        'req-2'
    );

    IF (v_mcp_response).envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Protected MCP with context should succeed, got error: %', (v_mcp_response).envelope->'error';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements((v_mcp_response).envelope->'result'->'content') AS c
        WHERE c->>'text' LIKE '%user789%'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: MCP result should contain user_id from context';
    END IF;

    RAISE NOTICE '  ✓ MCP: Protected tool succeeds with context.user_id, session variable set';

    -- ========================================================================
    -- Test: MCP public tool accepts unauthenticated requests
    -- ========================================================================

    v_mcp_response := api.mcp_call_tool('test_public_tool', '{}'::jsonb, NULL, 'req-3');

    IF (v_mcp_response).envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Public MCP tool should succeed without context, got error: %', (v_mcp_response).envelope->'error';
    END IF;

    RAISE NOTICE '  ✓ MCP: Public tool succeeds without auth';

    RAISE NOTICE '✓ Authentication Enforcement tests passed';
END $$;
