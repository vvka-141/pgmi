-- ============================================================================
-- Test: a handler's isolation floor is enforced by every gateway
-- ============================================================================
-- Covers the isolation contract end to end within the SQL harness:
--   * registration stores the normalized floor on api.handler
--   * registration rejects unsupported floor values
--   * gateways accept when the current level satisfies the floor
--   * gateways reject with a machine-readable error when it is too weak
--   * a rejected call leaves no broken transaction behind
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

-- ############################################################################
-- REGISTRATION - the floor is stored normalized; an unsupported one is refused
-- ############################################################################
DO $$
DECLARE
    v_stored text;
BEGIN
    -- Floor normalizes to canonical form on the way in.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-7001-4000-8000-000000000001',
            'path', '/iso-rr',
            'httpMethod', '^GET$',
            'name', 'iso_rr',
            'access', 'public',
            'minTransactionIsolation', 'REPEATABLE-READ'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('ok', true));
END;
        $body$
    );

    SELECT min_transaction_isolation INTO v_stored
    FROM api.handler WHERE object_id = 'ffffffff-7001-4000-8000-000000000001';

    IF v_stored IS DISTINCT FROM 'repeatable read' THEN
        RAISE EXCEPTION 'TEST FAILED: floor should normalize to "repeatable read", got "%"', v_stored;
    END IF;

    -- Unsupported floor is rejected at registration.
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-7002-4000-8000-000000000001',
                'path', '/iso-bad',
                'httpMethod', '^GET$',
                'name', 'iso_bad',
                'access', 'public',
                'minTransactionIsolation', 'snapshot'
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
END $$;

-- ############################################################################
-- REST - a floor above the current level is refused with a machine-readable
-- error and leaves no broken transaction
-- ############################################################################
DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
BEGIN
    -- No floor and an equal floor are satisfied at read committed => 200.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-7101-4000-8000-000000000001',
            'path', '/iso-none', 'httpMethod', '^GET$',
            'name', 'iso_none', 'access', 'public'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-7102-4000-8000-000000000001',
            'path', '/iso-rc', 'httpMethod', '^GET$',
            'name', 'iso_rc', 'access', 'public',
            'minTransactionIsolation', 'read committed'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    -- Stronger floors are too weak at read committed => 428.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-7103-4000-8000-000000000001',
            'path', '/iso-rr-rest', 'httpMethod', '^GET$',
            'name', 'iso_rr_rest', 'access', 'public',
            'minTransactionIsolation', 'repeatable read'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-7104-4000-8000-000000000001',
            'path', '/iso-ser-rest', 'httpMethod', '^GET$',
            'name', 'iso_ser_rest', 'access', 'public',
            'minTransactionIsolation', 'serializable'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );

    v_response := api.rest_invoke('GET', '/iso-none', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('no floor should return 200', v_response, 200);

    v_response := api.rest_invoke('GET', '/iso-rc', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('read committed floor should return 200 at read committed', v_response, 200);

    v_response := api.rest_invoke('GET', '/iso-rr-rest', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('repeatable read floor should return 428 at read committed', v_response, 428);
    v_content := api.content_json((v_response).content);
    IF v_content->>'code' IS DISTINCT FROM 'pgmi.transaction_isolation_too_weak'
       OR v_content->>'requiredIsolation' IS DISTINCT FROM 'repeatable read' THEN
        RAISE EXCEPTION 'TEST FAILED: REST rejection missing machine code or requiredIsolation, got %', v_content;
    END IF;

    v_response := api.rest_invoke('GET', '/iso-ser-rest', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('serializable floor should return 428 at read committed', v_response, 428);

    -- A rejected call leaves the session usable: the next call still works.
    v_response := api.rest_invoke('GET', '/iso-none', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('session broken after a rejected call', v_response, 200);
END $$;

-- ############################################################################
-- RPC - the same refusal through the RPC gateway
-- ############################################################################
DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
    v_route_id uuid;
BEGIN
    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-7201-4000-8000-000000000001',
            'methodName', 'iso.ok', 'access', 'public'
        ),
        $body$ BEGIN RETURN api.jsonrpc_success('{}'::jsonb, api.content_json((request).content)->'id'); END; $body$
    );
    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-7202-4000-8000-000000000001',
            'methodName', 'iso.serializable', 'access', 'public',
            'minTransactionIsolation', 'serializable'
        ),
        $body$ BEGIN RETURN api.jsonrpc_success('{}'::jsonb, api.content_json((request).content)->'id'); END; $body$
    );

    v_route_id := api.rpc_resolve('iso.ok');
    v_response := api.rpc_invoke(v_route_id, ''::extensions.hstore,
        convert_to('{"jsonrpc":"2.0","method":"iso.ok","id":"1"}', 'UTF8'));
    PERFORM pg_temp.expect_status('RPC no floor should return 200', v_response, 200);

    v_route_id := api.rpc_resolve('iso.serializable');
    v_response := api.rpc_invoke(v_route_id, ''::extensions.hstore,
        convert_to('{"jsonrpc":"2.0","method":"iso.serializable","id":"2"}', 'UTF8'));
    PERFORM pg_temp.expect_status('RPC serializable floor should return 428', v_response, 428);
    v_content := api.content_json((v_response).content);
    IF v_content->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.transaction_isolation_too_weak'
       OR v_content->'error'->'data'->>'requiredIsolation' IS DISTINCT FROM 'serializable' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC rejection missing machine code or requiredIsolation, got %', v_content;
    END IF;
    IF v_content->>'id' IS DISTINCT FROM '2' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC rejection must echo the request id, got %', v_content->>'id';
    END IF;
END $$;

-- ############################################################################
-- MCP - the same refusal through the MCP gateway
-- ############################################################################
DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
BEGIN
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-7301-4000-8000-000000000001',
            'type', 'tool', 'effect', 'read', 'name', 'iso_tool_ok', 'access', 'public',
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object())
        ),
        $body$ BEGIN RETURN api.mcp_tool_result(jsonb_build_array(), (request).request_id); END; $body$
    );
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-7302-4000-8000-000000000001',
            'type', 'tool', 'effect', 'read', 'name', 'iso_tool_ser', 'access', 'public',
            'minTransactionIsolation', 'serializable',
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
    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32603 THEN
        RAISE EXCEPTION 'TEST FAILED: MCP rejection should use code -32603, got %', v_envelope->'error'->>'code';
    END IF;
    IF v_envelope->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.transaction_isolation_too_weak'
       OR v_envelope->'error'->'data'->>'requiredIsolation' IS DISTINCT FROM 'serializable' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP rejection missing machine code or requiredIsolation, got %', v_envelope;
    END IF;
    IF v_envelope->>'id' IS DISTINCT FROM 'req-2' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP rejection must echo the request id, got %', v_envelope->>'id';
    END IF;
END $$;

DO $$
BEGIN
END $$;
