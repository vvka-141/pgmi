-- ============================================================================
-- Test: MCP error codes stay out of the JSON-RPC reserved range
-- ============================================================================
-- MCP 2026-07-28 reserves -32768..-32000; pgmi answers there only with the
-- spec's own codes (-32700, -32600..-32603, -32020..-32022). Its own codes
-- are -31001 (the client cannot act: an unknown handle, which the gateway
-- answers 401, or a connection that cannot act yet), -31003 (no permission,
-- no organization) and -31000 (from the gateway: retries exhausted, no
-- database connection). The isolation floor is -32603 with data.code.
-- ============================================================================

DO $$
DECLARE
    v_context  jsonb;
    v_stray    text;
    v_envelope jsonb;
BEGIN
    PERFORM pg_temp.test_act_as(NULL);
    v_context := pg_temp.test_mcp_context('test|error-codes',
        (membership.fn_create_organization_with_owner('error-codes', 'error-codes',
            pg_temp.test_login('test', 'error-codes', 'error-codes@example.test'))).object_id);
    IF v_context->>'connection' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the fixture connection was not approved';
    END IF;
    RAISE DEBUG '-> MCP error codes';

    SELECT string_agg(format('%s in %s', m[1], p.oid::regprocedure), ', ' ORDER BY p.oid::regprocedure::text) INTO v_stray
    FROM pg_proc p
    JOIN pg_namespace n ON n.oid = p.pronamespace AND n.nspname IN ('api', 'internal')
    CROSS JOIN LATERAL regexp_matches(p.prosrc, 'mcp_error\(\s*(-?\d+)', 'g') m
    WHERE p.proname ~ 'mcp'
      AND m[1]::int BETWEEN -32768 AND -32000
      AND m[1]::int NOT IN (-32700, -32600, -32601, -32602, -32603, -32020, -32021, -32022);
    IF v_stray IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: MCP answers with codes in the reserved range: %', v_stray;
    END IF;

    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'ffffffff-ec00-4000-8000-000000000001', 'type', 'tool', 'effect', 'read', 'name', 'ec_platform',
        'description', 'Platform operation', 'permissions', jsonb_build_array('platform.operate')),
        $b$BEGIN RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ran')), (request).request_id); END;$b$);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'ffffffff-ec00-4000-8000-000000000002', 'type', 'tool', 'effect', 'read', 'name', 'ec_serializable',
        'description', 'Needs serializable', 'access', 'public', 'minTransactionIsolation', 'serializable'),
        $b$BEGIN RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ran')), (request).request_id); END;$b$);

    v_envelope := (api.mcp_call_tool('ec_platform', '{}'::jsonb, v_context, '"f"'::jsonb)).envelope;
    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -31003
       OR v_envelope->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.forbidden'
       OR v_envelope->'id' IS DISTINCT FROM '"f"'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a refused permission must answer -31003 pgmi.forbidden, got %', v_envelope;
    END IF;

    v_envelope := (api.mcp_call_tool('ec_serializable', '{}'::jsonb, NULL, '"i"'::jsonb)).envelope;
    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32603
       OR v_envelope->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.transaction_isolation_too_weak' THEN
        RAISE EXCEPTION 'TEST FAILED: the isolation floor must answer -32603 pgmi.transaction_isolation_too_weak, got %', v_envelope;
    END IF;

    v_envelope := (api.mcp_handle_request(
        '{"jsonrpc": "2.0", "id": "u", "method": "tools/call", "params": {"name": "ec_platform", "arguments": {}}}'::jsonb,
        '{"connection": "cn_unknown"}'::jsonb)).envelope;
    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -31001
       OR v_envelope->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.connection_unknown'
       OR v_envelope->'id' IS DISTINCT FROM '"u"'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: an unknown handle must answer -31001 pgmi.connection_unknown, got %', v_envelope;
    END IF;

    RAISE NOTICE '  + MCP error codes: -31003 refusals, -31001 unknown handle, -32603 isolation floor, nothing else in the reserved range';
END $$;

-- Version negotiation: a version the server does not speak is -32022 listing
-- what it does speak, so the client can retry with one of them.
DO $$
DECLARE
    v_list    constant jsonb := '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}';
    v_meta    constant jsonb := '{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {"_meta": {"io.modelcontextprotocol/protocolVersion": "2025-11-25"}}}';
    v_case    record;
    v_refusal jsonb;
BEGIN
    FOR v_case IN SELECT * FROM (VALUES
        ('unknown header',            v_list, '1999-01-01', -32022, to_jsonb(api.mcp_legacy_versions() || api.mcp_supported_versions())),
        ('future header',             v_list, '2027-01-01', -32022, to_jsonb(api.mcp_legacy_versions() || api.mcp_supported_versions())),
        ('legacy header and _meta',   v_meta, '2025-11-25', -32022, to_jsonb(api.mcp_supported_versions())),
        ('legacy header, other _meta', jsonb_set(v_meta, '{params,_meta,io.modelcontextprotocol/protocolVersion}', '"2025-06-18"'),
                                      '2025-11-25', -32020, NULL::jsonb)) c(label, request, header, code, supported)
    LOOP
        v_refusal := internal.mcp_request_refusal(v_case.request, v_case.header, NULL, NULL);
        IF (v_refusal->'error'->>'code')::int IS DISTINCT FROM v_case.code
           OR (v_case.supported IS NOT NULL AND v_refusal->'error'->'data'->'supported' IS DISTINCT FROM v_case.supported) THEN
            RAISE EXCEPTION 'TEST FAILED: % must be % listing %, got %', v_case.label, v_case.code, v_case.supported, v_refusal;
        END IF;
    END LOOP;

    v_refusal := internal.mcp_meta_refusal(v_meta);
    IF (v_refusal->'error'->>'code')::int IS DISTINCT FROM -32022
       OR v_refusal->'error'->'data'->'supported' IS DISTINCT FROM to_jsonb(api.mcp_supported_versions()) THEN
        RAISE EXCEPTION 'TEST FAILED: a body-only legacy _meta version must be -32022 listing the 2026-07-28 era, got %', v_refusal;
    END IF;
END $$;
