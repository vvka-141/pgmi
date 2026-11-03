-- ============================================================================
-- Test: Authentication Enforcement
-- ============================================================================
-- Validates that handlers declared "access": "authenticated" enforce a live
-- login session (headers) or an approved connection (MCP context), while handlers declared "access": "public" allow
-- unauthenticated access.
-- ============================================================================

CREATE FUNCTION pg_temp.auth_test_principal(p_subject text)
RETURNS uuid LANGUAGE sql AS $$
    SELECT pg_temp.test_person('test|' || p_subject);
$$;

-- An MCP context for a person: a connection in an organization they own.
CREATE FUNCTION pg_temp.auth_test_mcp(p_subject text)
RETURNS jsonb LANGUAGE sql AS $$
    SELECT pg_temp.test_mcp_context('test|' || p_subject,
        coalesce(pg_temp.test_organization('auth-' || p_subject),
                 (membership.fn_create_organization_with_owner('Auth ' || p_subject, 'auth-' || p_subject,
                                                               pg_temp.auth_test_principal(p_subject))).object_id));
$$;

DO $$
DECLARE
    v_response api.http_response;
    v_mcp_response api.mcp_response;
    v_content jsonb;
    v_route_id uuid;
    v_headers extensions.hstore;
BEGIN
    RAISE NOTICE '→ Testing Authentication Enforcement';

    -- ========================================================================
    -- Register protected handlers (access: authenticated)
    -- ========================================================================

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-a001-4000-8000-000000000001',
            'path', '/test-protected-rest',
            'httpMethod', '^GET$',
            'name', 'test_protected_rest',
            'description', 'Protected REST handler',
            'access', 'authenticated'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object(
        'principalId', api.current_principal_id()
    ));
END;
        $body$
    );

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-a002-4000-8000-000000000001',
            'methodName', 'test.protected',
            'description', 'Protected RPC handler',
            'access', 'authenticated'
        ),
        $body$
BEGIN
    RETURN api.jsonrpc_success(
        jsonb_build_object('principalId', api.current_principal_id()),
        api.content_json((request).content)->'id'
    );
END;
        $body$
    );

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-a003-4000-8000-000000000001',
            'type', 'tool', 'effect', 'read',
            'name', 'test_protected_tool',
            'description', 'Protected MCP tool',
            'access', 'authenticated',
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object())
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('principal: ' || api.current_principal_id())),
        (request).request_id
    );
END;
        $body$
    );

    -- ========================================================================
    -- Register public handlers (access: public)
    -- ========================================================================

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-a004-4000-8000-000000000001',
            'path', '/test-public-rest',
            'httpMethod', '^GET$',
            'name', 'test_public_rest',
            'description', 'Public REST handler',
            'access', 'public'
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
            'access', 'public'
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
            'type', 'tool', 'effect', 'read',
            'name', 'test_public_tool',
            'description', 'Public MCP tool',
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object()),
            'access', 'public'
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

    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: Protected REST without auth should return 401, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF v_content->>'title' IS DISTINCT FROM 'Unauthorized' THEN
        RAISE EXCEPTION 'TEST FAILED: 401 response should have title "Unauthorized"';
    END IF;

    RAISE NOTICE '  ✓ REST: Protected handler returns 401 without a session';

    -- ========================================================================
    -- "access": "authenticated" gates on a live login session of an enabled
    -- person, so every positive case below needs a real person and session.
    -- ========================================================================

    PERFORM pg_temp.test_login('test', 'user123', 'authtest-user123@example.test', NULL, true);
    PERFORM pg_temp.test_login('test', 'user456', 'authtest-user456@example.test', NULL, true);
    PERFORM pg_temp.test_login('test', 'user789', 'authtest-user789@example.test', NULL, true);
    PERFORM pg_temp.test_login('test', 'alice', 'authtest-alice@example.test', NULL, true);

    -- ========================================================================
    -- Test: REST protected handler accepts authenticated requests
    -- ========================================================================

    v_response := api.rest_invoke(
        'GET',
        '/test-protected-rest',
        pg_temp.test_session_headers('test|user123'),
        NULL::bytea
    );

    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: Protected REST with auth should return 200, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF v_content->>'principalId' IS DISTINCT FROM pg_temp.auth_test_principal('user123')::text THEN
        RAISE EXCEPTION 'TEST FAILED: the handler should see user123 as its principal, got "%"', v_content->>'principalId';
    END IF;

    RAISE NOTICE '  ✓ REST: Protected handler returns 200 with a login session and sees its principal';

    -- ========================================================================
    -- Test: an identity header is not a way in: only a session handle is
    -- ========================================================================

    v_response := api.rest_invoke(
        'GET',
        '/test-protected-rest',
        'x-user-id=>test|user123, x-user-email=>authtest-user123@example.test, x-tenant-id=>00000000-0000-0000-0000-000000000001'::extensions.hstore,
        NULL::bytea
    );

    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: identity headers must NOT satisfy the auth gate, expected 401 got %', (v_response).status_code;
    END IF;

    RAISE NOTICE '  ✓ REST: x-user-id / x-user-email / x-tenant-id are ignored';

    -- ========================================================================
    -- Test: REST public handler accepts unauthenticated requests
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/test-public-rest', ''::extensions.hstore, NULL::bytea);

    IF (v_response).status_code IS DISTINCT FROM 200 THEN
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

    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: Protected RPC without auth should return HTTP 401, got %', (v_response).status_code;
    END IF;

    v_content := api.content_json((v_response).content);
    IF (v_content->'error'->>'code')::int IS DISTINCT FROM -32001 THEN
        RAISE EXCEPTION 'TEST FAILED: Protected RPC without auth should return JSON-RPC error -32001, got %', v_content->'error'->>'code';
    END IF;

    RAISE NOTICE '  ✓ RPC: Protected handler returns HTTP 401 with JSON-RPC error -32001';

    -- ========================================================================
    -- Test: RPC protected handler accepts authenticated requests
    -- ========================================================================

    v_response := api.rpc_invoke(
        v_route_id,
        pg_temp.test_session_headers('test|user456'),
        convert_to('{"jsonrpc": "2.0", "method": "test.protected", "id": "2"}', 'UTF8')
    );

    v_content := api.content_json((v_response).content);
    IF v_content->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Protected RPC with auth should succeed, got error: %', v_content->'error';
    END IF;

    IF v_content->'result'->>'principalId' IS DISTINCT FROM pg_temp.auth_test_principal('user456')::text THEN
        RAISE EXCEPTION 'TEST FAILED: the RPC handler should see user456 as its principal';
    END IF;

    RAISE NOTICE '  ✓ RPC: Protected handler succeeds with a login session and sees its principal';

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

    v_mcp_response := api.mcp_call_tool('test_protected_tool', '{}'::jsonb, NULL, '"req-1"'::jsonb);

    IF (v_mcp_response).envelope->'error' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Protected MCP without context should return JSON-RPC error';
    END IF;

    -- An anonymous caller must not learn that a protected name exists: the
    -- answer is exactly what an unknown name gets.
    IF (v_mcp_response).envelope->'error'
       IS DISTINCT FROM ((api.mcp_call_tool('test_no_such_tool', '{}'::jsonb, NULL, '"req-1"'::jsonb)).envelope->'error'
                         || jsonb_build_object('message', replace(
                                (api.mcp_call_tool('test_no_such_tool', '{}'::jsonb, NULL, '"req-1"'::jsonb)).envelope->'error'->>'message',
                                'test_no_such_tool', 'test_protected_tool'))) THEN
        RAISE EXCEPTION 'TEST FAILED: a protected tool must answer an anonymous caller exactly like an unknown one, got %',
            (v_mcp_response).envelope->'error';
    END IF;

    RAISE NOTICE '  ✓ MCP: a protected tool is indistinguishable from an unknown one without a session';

    -- ========================================================================
    -- Test: MCP protected tool accepts authenticated requests
    -- ========================================================================

    v_mcp_response := api.mcp_call_tool(
        'test_protected_tool',
        '{}'::jsonb,
        pg_temp.auth_test_mcp('user789'),
        '"req-2"'::jsonb
    );

    IF (v_mcp_response).envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Protected MCP with context should succeed, got error: %', (v_mcp_response).envelope->'error';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements((v_mcp_response).envelope->'result'->'content') AS c
        WHERE c->>'text' = 'principal: ' || pg_temp.auth_test_principal('user789')
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: the MCP handler should see user789 as its principal';
    END IF;

    RAISE NOTICE '  ✓ MCP: Protected tool succeeds with context.connection and sees its principal';

    -- ========================================================================
    -- Test: MCP public tool accepts unauthenticated requests
    -- ========================================================================

    v_mcp_response := api.mcp_call_tool('test_public_tool', '{}'::jsonb, NULL, '"req-3"'::jsonb);

    IF (v_mcp_response).envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Public MCP tool should succeed without context, got error: %', (v_mcp_response).envelope->'error';
    END IF;

    RAISE NOTICE '  ✓ MCP: Public tool succeeds without auth';

    -- ========================================================================
    -- Hardening: a handle that names no session is rejected across REST,
    -- RPC, and MCP; identity must not leak across successive calls.
    -- ========================================================================

    v_response := api.rest_invoke(
        'GET', '/test-protected-rest',
        'x-login-session=>ls_not-a-session'::extensions.hstore, NULL::bytea
    );
    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: an unknown session handle must return 401, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  ✓ REST: unknown session handle rejected with 401';

    v_route_id := api.rpc_resolve('test.protected');
    v_response := api.rpc_invoke(
        v_route_id,
        'x-login-session=>ls_not-a-session'::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "method": "test.protected", "id": "m"}', 'UTF8')
    );
    v_content := api.content_json((v_response).content);
    IF (v_content->'error'->>'code')::int IS DISTINCT FROM -32001 THEN
        RAISE EXCEPTION 'TEST FAILED: an unknown RPC session handle should return -32001, got %', v_content->'error';
    END IF;
    RAISE NOTICE '  ✓ RPC: unknown session handle rejected with -32001';

    v_mcp_response := api.mcp_call_tool(
        'test_protected_tool', '{}'::jsonb, '{"user_id": "test|user789", "connection": "cn_nope"}'::jsonb, '"req-malformed"'::jsonb
    );
    IF ((v_mcp_response).envelope->'error'->>'code')::int IS DISTINCT FROM -31001
       OR (v_mcp_response).envelope->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.connection_unknown' THEN
        RAISE EXCEPTION 'TEST FAILED: an MCP context with a user_id and an unknown connection should answer -31001 pgmi.connection_unknown, got %', (v_mcp_response).envelope->'error';
    END IF;
    RAISE NOTICE '  ✓ MCP: user_id is ignored and an unknown connection is rejected (-31001 pgmi.connection_unknown)';

    -- MCP: identity must not leak across calls. Call 1 authenticates; call 2
    -- omits context and must NOT inherit call 1's identity (proves GUC reset).
    v_mcp_response := api.mcp_call_tool(
        'test_protected_tool', '{}'::jsonb, pg_temp.auth_test_mcp('alice'), '"req-leak-1"'::jsonb
    );
    IF (v_mcp_response).envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: authenticated MCP call should succeed, got %', (v_mcp_response).envelope->'error';
    END IF;

    v_mcp_response := api.mcp_call_tool(
        'test_protected_tool', '{}'::jsonb, NULL, '"req-leak-2"'::jsonb
    );
    IF ((v_mcp_response).envelope->'error'->>'code')::int IS DISTINCT FROM -32602 THEN
        RAISE EXCEPTION 'TEST FAILED: identity leaked into a no-context MCP call (expected -32602), got %', (v_mcp_response).envelope->'error';
    END IF;
    RAISE NOTICE '  ✓ MCP: identity does not leak across calls (GUC reset verified)';

    -- ========================================================================
    -- The MCP dispatcher (mcp_handle_request) must apply the same
    -- validated, reset-first auth context to the discovery path (tools/list),
    -- and must not leak raw SQLERRM to the client.
    -- ========================================================================

    -- Discovery with a forged context must NOT reveal auth-gated tools, but
    -- must still list public ones.
    v_mcp_response := api.mcp_handle_request(
        '{"jsonrpc":"2.0","id":"disc-1","method":"tools/list"}'::jsonb,
        '{"user_id":"test|alice","organizationId":"forged"}'::jsonb
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
        pg_temp.auth_test_mcp('alice')
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
            'type', 'tool', 'effect', 'read',
            'name', 'test_throwing_tool',
            'description', 'Always raises',
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object()),
            'access', 'public'
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
    -- Disabling a person closes their open sessions at once.
    -- ========================================================================

    v_headers := pg_temp.test_session_headers('test|user123');
    v_response := api.rest_invoke('GET', '/test-protected-rest', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: setup: user123 should be served, got %', (v_response).status_code;
    END IF;
    PERFORM membership.fn_disable_principal(pg_temp.auth_test_principal('user123'), 'test lockout');
    v_response := api.rest_invoke('GET', '/test-protected-rest', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: a disabled person''s session must return 401, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  ✓ REST: a disabled person''s open session is refused with 401';

    RAISE NOTICE '✓ Authentication Enforcement tests passed';
END $$;
