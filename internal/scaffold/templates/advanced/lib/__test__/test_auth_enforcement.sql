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
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object())
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
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object()),
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

    v_response := api.rest_invoke('GET', '/test-protected-rest', ''::extensions.hstore, NULL::bytea);

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
        'x-user-id=>test|user123'::extensions.hstore,
        NULL::bytea
    );

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: Protected REST with auth should return 200, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF v_content->>'user_id' != 'test|user123' THEN
        RAISE EXCEPTION 'TEST FAILED: Session variable auth.user_id should be "test|user123", got "%"', v_content->>'user_id';
    END IF;

    RAISE NOTICE '  ✓ REST: Protected handler returns 200 with x-user-id, session variable set';

    -- ========================================================================
    -- Test: unprefixed user-id header is NOT accepted as auth
    -- (standardized on x-user-id; no alias fallback)
    -- ========================================================================

    v_response := api.rest_invoke(
        'GET',
        '/test-protected-rest',
        'user-id=>userZZZ'::extensions.hstore,
        NULL::bytea
    );

    IF (v_response).status_code != 401 THEN
        RAISE EXCEPTION 'TEST FAILED: user-id alias must NOT satisfy auth gate, expected 401 got %', (v_response).status_code;
    END IF;

    RAISE NOTICE '  ✓ REST: user-id header alone is rejected (alias removed)';

    -- ========================================================================
    -- Test: REST public handler accepts unauthenticated requests
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/test-public-rest', ''::extensions.hstore, NULL::bytea);

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
        'x-user-id=>test|user456'::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.protected", "id": "2"}', 'UTF8')
    );

    v_content := api.content_json((v_response).content);
    IF v_content->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Protected RPC with auth should succeed, got error: %', v_content->'error';
    END IF;

    IF v_content->'result'->>'user_id' != 'test|user456' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC session variable auth.user_id should be "test|user456"';
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

    v_mcp_response := api.mcp_call_tool('test_protected_tool', '{}'::jsonb, NULL, '"req-1"'::jsonb);

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
        '{"user_id": "test|user789"}'::jsonb,
        '"req-2"'::jsonb
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

    v_mcp_response := api.mcp_call_tool('test_public_tool', '{}'::jsonb, NULL, '"req-3"'::jsonb);

    IF (v_mcp_response).envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Public MCP tool should succeed without context, got error: %', (v_mcp_response).envelope->'error';
    END IF;

    RAISE NOTICE '  ✓ MCP: Public tool succeeds without auth';

    -- ========================================================================
    -- Hardening (PGMI-16): present-but-malformed identities are rejected across
    -- REST, RPC, and MCP; identity must not leak across successive calls.
    -- ========================================================================

    -- REST: malformed x-user-id (no provider|subject pipe) → 401
    v_response := api.rest_invoke(
        'GET', '/test-protected-rest',
        'x-user-id=>alice'::extensions.hstore, NULL::bytea
    );
    IF (v_response).status_code != 401 THEN
        RAISE EXCEPTION 'TEST FAILED: malformed x-user-id must return 401, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  ✓ REST: malformed x-user-id (no pipe) rejected with 401';

    -- REST: empty-provider identity (leading pipe) → 401 (provider must be non-empty)
    v_response := api.rest_invoke(
        'GET', '/test-protected-rest',
        'x-user-id=>|alice'::extensions.hstore, NULL::bytea
    );
    IF (v_response).status_code != 401 THEN
        RAISE EXCEPTION 'TEST FAILED: leading-pipe x-user-id must return 401, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  ✓ REST: empty-provider x-user-id (|subject) rejected with 401';

    -- RPC: malformed x-user-id → -32001
    v_route_id := api.rpc_resolve('test.protected');
    v_response := api.rpc_invoke(
        v_route_id,
        'x-user-id=>alice'::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.protected", "id": "m"}', 'UTF8')
    );
    v_content := api.content_json((v_response).content);
    IF (v_content->'error'->>'code')::int IS DISTINCT FROM -32001 THEN
        RAISE EXCEPTION 'TEST FAILED: malformed RPC x-user-id should return -32001, got %', v_content->'error';
    END IF;
    RAISE NOTICE '  ✓ RPC: malformed x-user-id (no pipe) rejected with -32001';

    -- MCP: malformed user_id (no pipe) → -32001
    v_mcp_response := api.mcp_call_tool(
        'test_protected_tool', '{}'::jsonb, '{"user_id": "alice"}'::jsonb, '"req-malformed"'::jsonb
    );
    IF ((v_mcp_response).envelope->'error'->>'code')::int IS DISTINCT FROM -32001 THEN
        RAISE EXCEPTION 'TEST FAILED: malformed MCP user_id should return -32001, got %', (v_mcp_response).envelope->'error';
    END IF;
    RAISE NOTICE '  ✓ MCP: malformed user_id (no pipe) rejected with -32001';

    -- MCP: identity must not leak across calls. Call 1 authenticates; call 2
    -- omits context and must NOT inherit call 1's identity (proves GUC reset).
    v_mcp_response := api.mcp_call_tool(
        'test_protected_tool', '{}'::jsonb, '{"user_id": "test|alice"}'::jsonb, '"req-leak-1"'::jsonb
    );
    IF (v_mcp_response).envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: authenticated MCP call should succeed, got %', (v_mcp_response).envelope->'error';
    END IF;

    v_mcp_response := api.mcp_call_tool(
        'test_protected_tool', '{}'::jsonb, NULL, '"req-leak-2"'::jsonb
    );
    IF ((v_mcp_response).envelope->'error'->>'code')::int IS DISTINCT FROM -32001 THEN
        RAISE EXCEPTION 'TEST FAILED: identity leaked into a no-context MCP call (expected -32001), got %', (v_mcp_response).envelope->'error';
    END IF;
    RAISE NOTICE '  ✓ MCP: identity does not leak across calls (GUC reset verified)';

    -- ========================================================================
    -- PGMI-26: the MCP dispatcher (mcp_handle_request) must apply the same
    -- validated, reset-first auth context to the discovery path (tools/list),
    -- and must not leak raw SQLERRM to the client.
    -- ========================================================================

    PERFORM set_config('auth.user_id', '', true);

    -- Discovery with a forged, malformed identity (no provider|subject pipe)
    -- must NOT reveal auth-gated tools, but must still list public ones.
    v_mcp_response := api.mcp_handle_request(
        '{"jsonrpc":"2.0","id":"disc-1","method":"tools/list"}'::jsonb,
        '{"user_id":"alice"}'::jsonb
    );
    IF EXISTS (
        SELECT 1 FROM jsonb_array_elements((v_mcp_response).envelope->'result'->'tools') AS t
        WHERE t->>'name' = 'test_protected_tool'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: forged context exposed auth-gated tool via dispatcher tools/list';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements((v_mcp_response).envelope->'result'->'tools') AS t
        WHERE t->>'name' = 'test_public_tool'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: dispatcher tools/list hid a public tool';
    END IF;
    RAISE NOTICE '  ✓ MCP dispatcher: forged context does not expose auth-gated tools in tools/list';

    -- Identity must not bleed into a later context-less discovery request.
    v_mcp_response := api.mcp_handle_request(
        '{"jsonrpc":"2.0","id":"disc-2","method":"tools/call","params":{"name":"test_protected_tool","arguments":{}}}'::jsonb,
        '{"user_id":"test|alice"}'::jsonb
    );
    v_mcp_response := api.mcp_handle_request(
        '{"jsonrpc":"2.0","id":"disc-3","method":"tools/list"}'::jsonb,
        NULL
    );
    IF EXISTS (
        SELECT 1 FROM jsonb_array_elements((v_mcp_response).envelope->'result'->'tools') AS t
        WHERE t->>'name' = 'test_protected_tool'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: identity bled into a context-less dispatcher tools/list';
    END IF;
    RAISE NOTICE '  ✓ MCP dispatcher: identity does not bleed into context-less discovery';

    -- A failing handler invoked through the dispatcher must not surface raw
    -- error detail (table/column names, SQLERRM) to the client.
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-a007-4000-8000-000000000001',
            'type', 'tool',
            'name', 'test_throwing_tool',
            'description', 'Always raises',
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object()),
            'requiresAuth', false
        ),
        $body$
BEGIN
    RAISE EXCEPTION 'pgmi_secret_detail_marker in handler';
END;
        $body$
    );

    v_mcp_response := api.mcp_handle_request(
        '{"jsonrpc":"2.0","id":"err-1","method":"tools/call","params":{"name":"test_throwing_tool","arguments":{}}}'::jsonb,
        NULL
    );
    IF (v_mcp_response).envelope::text LIKE '%pgmi_secret_detail_marker%' THEN
        RAISE EXCEPTION 'TEST FAILED: raw handler error detail leaked to MCP client: %', (v_mcp_response).envelope;
    END IF;
    RAISE NOTICE '  ✓ MCP dispatcher: failing handler does not leak raw SQLERRM to client';

    -- ========================================================================
    -- PGMI-33: the gateway JIT-provisions a membership.user row for a first-time
    -- authenticated identity, so api.current_user_id() resolves and /me +
    -- /organizations work without any out-of-band row creation.
    -- ========================================================================

    v_response := api.rest_invoke(
        'GET', '/me',
        'x-user-id=>jittest|user-pgmi33, x-user-email=>jit-pgmi33@example.com'::extensions.hstore,
        NULL::bytea
    );
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: /me for a freshly-authenticated identity should be 200 (JIT-provisioned), got %', (v_response).status_code;
    END IF;
    v_content := api.content_json((v_response).content);
    IF v_content->>'email' != 'jit-pgmi33@example.com' THEN
        RAISE EXCEPTION 'TEST FAILED: /me returned wrong/absent user, got %', v_content;
    END IF;
    RAISE NOTICE '  ✓ REST: /me JIT-provisions a first-time identity (200, current_user_id resolves)';

    v_response := api.rest_invoke(
        'GET', '/organizations',
        'x-user-id=>jittest|user-pgmi33, x-user-email=>jit-pgmi33@example.com'::extensions.hstore,
        NULL::bytea
    );
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: /organizations should be 200, got %', (v_response).status_code;
    END IF;
    v_content := api.content_json((v_response).content);
    IF jsonb_array_length(v_content->'organizations') < 1 THEN
        RAISE EXCEPTION 'TEST FAILED: /organizations should list the auto-created personal org, got %', v_content;
    END IF;
    RAISE NOTICE '  ✓ REST: /organizations returns the JIT-provisioned personal org';

    -- Idempotent: a repeat request must not error on duplicate provisioning.
    v_response := api.rest_invoke(
        'GET', '/me',
        'x-user-id=>jittest|user-pgmi33, x-user-email=>jit-pgmi33@example.com'::extensions.hstore,
        NULL::bytea
    );
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: repeated /me should stay 200 (idempotent provisioning), got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  ✓ REST: JIT provisioning is idempotent across repeated requests';

    RAISE NOTICE '✓ Authentication Enforcement tests passed';
END $$;
