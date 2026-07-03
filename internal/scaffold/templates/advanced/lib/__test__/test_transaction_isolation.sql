-- ============================================================================
-- Test: Transaction Isolation Enforcement (PGMI-111)
-- ============================================================================
-- Covers the isolation contract end to end within the SQL harness:
--   * registration stores the normalized floor on api.handler (A2)
--   * registration rejects unsupported floor values (A2)
--   * gateways accept when the current level satisfies the floor (A4)
--   * gateways reject with a machine-readable error when it is too weak (A4)
--   * a rejected call leaves no broken transaction behind (A4)
--
-- The pgmi test harness runs each step inside a savepoint at the deploy
-- transaction's isolation (read committed). SET TRANSACTION ISOLATION LEVEL is
-- illegal here (a query has already run), so the "actual >= required" accept
-- path for STRONGER-than-current levels is proven at the unit level by the rank
-- tests in lib/api/00-transaction-isolation.sql and, end to end at a real
-- stronger level, by the driver-level test in
-- internal/scaffold/integration_test.go (TestTemplateTransactionIsolation).
-- Here we exercise: floor satisfied (NULL / read committed) => dispatch;
-- floor too weak (repeatable read / serializable) => reject.
-- ============================================================================

DO $$
DECLARE
    v_stored text;
BEGIN
    RAISE NOTICE '→ Testing isolation floor registration (A2)';

    -- Floor normalizes to canonical form on the way in.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-7001-4000-8000-000000000001',
            'uri', '^/iso-rr$',
            'httpMethod', '^GET$',
            'name', 'iso_rr',
            'requiresAuth', false,
            'requiredTransactionIsolation', 'REPEATABLE-READ'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('ok', true));
END;
        $body$
    );

    SELECT required_transaction_isolation INTO v_stored
    FROM api.handler WHERE object_id = 'ffffffff-7001-4000-8000-000000000001';

    IF v_stored <> 'repeatable read' THEN
        RAISE EXCEPTION 'TEST FAILED: floor should normalize to "repeatable read", got "%"', v_stored;
    END IF;

    RAISE NOTICE '  ✓ requiredTransactionIsolation normalized and stored (REPEATABLE-READ -> repeatable read)';

    -- Unsupported floor is rejected at registration.
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-7002-4000-8000-000000000001',
                'uri', '^/iso-bad$',
                'httpMethod', '^GET$',
                'name', 'iso_bad',
                'requiresAuth', false,
                'requiredTransactionIsolation', 'snapshot'
            ),
            $body$
BEGIN
    RETURN api.json_response(200, '{}'::jsonb);
END;
            $body$
        );
        RAISE EXCEPTION 'TEST FAILED: unsupported isolation floor should have been rejected';
    EXCEPTION WHEN OTHERS THEN
        IF SQLERRM NOT LIKE 'unsupported transaction isolation level%' THEN
            RAISE EXCEPTION 'TEST FAILED: wrong error for unsupported floor: %', SQLERRM;
        END IF;
    END;

    RAISE NOTICE '  ✓ registration rejects unsupported isolation floor';
END $$;

DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
BEGIN
    RAISE NOTICE '→ Testing REST isolation enforcement (A4)';

    -- No floor and an equal floor are satisfied at read committed => 200.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-7101-4000-8000-000000000001',
            'uri', '^/iso-none$', 'httpMethod', '^GET$',
            'name', 'iso_none', 'requiresAuth', false
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-7102-4000-8000-000000000001',
            'uri', '^/iso-rc$', 'httpMethod', '^GET$',
            'name', 'iso_rc', 'requiresAuth', false,
            'requiredTransactionIsolation', 'read committed'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    -- Stronger floors are too weak at read committed => 428.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-7103-4000-8000-000000000001',
            'uri', '^/iso-rr-rest$', 'httpMethod', '^GET$',
            'name', 'iso_rr_rest', 'requiresAuth', false,
            'requiredTransactionIsolation', 'repeatable read'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-7104-4000-8000-000000000001',
            'uri', '^/iso-ser-rest$', 'httpMethod', '^GET$',
            'name', 'iso_ser_rest', 'requiresAuth', false,
            'requiredTransactionIsolation', 'serializable'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );

    v_response := api.rest_invoke('GET', '/iso-none', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code <> 200 THEN
        RAISE EXCEPTION 'TEST FAILED: no floor should return 200, got %', (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/iso-rc', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code <> 200 THEN
        RAISE EXCEPTION 'TEST FAILED: read committed floor should return 200 at read committed, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  ✓ satisfied floors dispatch the handler (200)';

    v_response := api.rest_invoke('GET', '/iso-rr-rest', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code <> 428 THEN
        RAISE EXCEPTION 'TEST FAILED: repeatable read floor should return 428 at read committed, got %', (v_response).status_code;
    END IF;
    v_content := api.content_json((v_response).content);
    IF v_content->>'code' <> 'pgmi.transaction_isolation_too_weak' THEN
        RAISE EXCEPTION 'TEST FAILED: REST rejection missing machine code, got %', v_content->>'code';
    END IF;

    v_response := api.rest_invoke('GET', '/iso-ser-rest', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code <> 428 THEN
        RAISE EXCEPTION 'TEST FAILED: serializable floor should return 428 at read committed, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  ✓ too-weak floors are rejected 428 with pgmi.transaction_isolation_too_weak';

    -- A rejected call leaves the session usable: the next call still works.
    v_response := api.rest_invoke('GET', '/iso-none', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code <> 200 THEN
        RAISE EXCEPTION 'TEST FAILED: session broken after a rejected call, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  ✓ rejected call leaves no broken transaction (subsequent call ok)';
END $$;

DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
    v_route_id uuid;
BEGIN
    RAISE NOTICE '→ Testing RPC isolation enforcement (A4)';

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-7201-4000-8000-000000000001',
            'methodName', 'iso.ok', 'requiresAuth', false
        ),
        $body$ BEGIN RETURN api.jsonrpc_success('{}'::jsonb, api.content_json((request).content)->'id'); END; $body$
    );
    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-7202-4000-8000-000000000001',
            'methodName', 'iso.serializable', 'requiresAuth', false,
            'requiredTransactionIsolation', 'serializable'
        ),
        $body$ BEGIN RETURN api.jsonrpc_success('{}'::jsonb, api.content_json((request).content)->'id'); END; $body$
    );

    v_route_id := api.rpc_resolve('iso.ok');
    v_response := api.rpc_invoke(v_route_id, ''::extensions.hstore,
        convert_to('{"jsonrpc":"2.0","method":"iso.ok","id":"1"}', 'UTF8'));
    IF (v_response).status_code <> 200 THEN
        RAISE EXCEPTION 'TEST FAILED: RPC no floor should return 200, got %', (v_response).status_code;
    END IF;

    v_route_id := api.rpc_resolve('iso.serializable');
    v_response := api.rpc_invoke(v_route_id, ''::extensions.hstore,
        convert_to('{"jsonrpc":"2.0","method":"iso.serializable","id":"2"}', 'UTF8'));
    IF (v_response).status_code <> 428 THEN
        RAISE EXCEPTION 'TEST FAILED: RPC serializable floor should return 428, got %', (v_response).status_code;
    END IF;
    v_content := api.content_json((v_response).content);
    IF v_content->'error'->'data'->>'code' <> 'pgmi.transaction_isolation_too_weak' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC rejection missing machine code, got %', v_content->'error'->'data'->>'code';
    END IF;
    IF v_content->>'id' <> '2' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC rejection must echo the request id, got %', v_content->>'id';
    END IF;
    RAISE NOTICE '  ✓ RPC too-weak floor rejected 428 with machine code and echoed id';
END $$;

DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
BEGIN
    RAISE NOTICE '→ Testing MCP isolation enforcement (A4)';

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-7301-4000-8000-000000000001',
            'type', 'tool', 'name', 'iso_tool_ok', 'requiresAuth', false,
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object())
        ),
        $body$ BEGIN RETURN api.mcp_tool_result(jsonb_build_array(), (request).request_id); END; $body$
    );
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-7302-4000-8000-000000000001',
            'type', 'tool', 'name', 'iso_tool_ser', 'requiresAuth', false,
            'requiredTransactionIsolation', 'serializable',
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object())
        ),
        $body$ BEGIN RETURN api.mcp_tool_result(jsonb_build_array(), (request).request_id); END; $body$
    );

    v_response := api.mcp_call_tool('iso_tool_ok', '{}'::jsonb, NULL, '"req-1"'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->'result' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: MCP no floor should dispatch (result present)';
    END IF;

    v_response := api.mcp_call_tool('iso_tool_ser', '{}'::jsonb, NULL, '"req-2"'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->'error' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: MCP serializable floor should reject with an error envelope';
    END IF;
    IF (v_envelope->'error'->>'code')::int <> -32600 THEN
        RAISE EXCEPTION 'TEST FAILED: MCP rejection should use code -32600, got %', v_envelope->'error'->>'code';
    END IF;
    IF v_envelope->'error'->'data'->>'code' <> 'pgmi.transaction_isolation_too_weak' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP rejection missing machine code, got %', v_envelope->'error'->'data'->>'code';
    END IF;
    IF v_envelope->>'id' <> 'req-2' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP rejection must echo the request id, got %', v_envelope->>'id';
    END IF;
    RAISE NOTICE '  ✓ MCP too-weak floor rejected (-32600) with machine code and echoed id';
END $$;

DO $$
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE '✓ ALL TRANSACTION ISOLATION TESTS PASSED';
END $$;
