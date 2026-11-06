-- ============================================================================
-- Test: Transaction Policy — read-only routes and resolve-then-open
-- ============================================================================
-- Covers the policy contract within the SQL harness:
--   * registration stores readOnly on api.handler (default false)
--   * api.rest_route_policy / api.rpc_route_policy / api.mcp_request_policy
--     resolve max(floor, requested) + read-only + deferrable
--   * gateways reject a read-only route dispatched in a read-write transaction
--     (pgmi.transaction_read_only_required), fail closed
--   * a READ ONLY transaction dispatches a read-only route and the gateway
--     skips its own writes (exchange logging)
--   * OpenAPI advertises x-pgmi-read-only and the derived x-pgmi-replica-safe
--   * flipping readOnly changes api.catalog_version()
--
-- The harness runs at the deploy transaction's isolation (read committed) and
-- cannot raise it, so the "open at the resolved level" path at real stronger
-- levels — including SERIALIZABLE READ ONLY DEFERRABLE — is proven by the
-- driver-level test in internal/scaffold/transaction_policy_test.go.
-- transaction_read_only CAN be turned on mid-transaction (only the reverse is
-- restricted), which is what the accept-path block below relies on; the
-- savepoint rollback around each test file reverts it.
-- ============================================================================

DO $$
DECLARE
    v_read_only boolean;
BEGIN
    RAISE NOTICE '→ Testing readOnly registration';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-8001-4000-8000-000000000001',
            'path', '/policy-ro', 'httpMethod', '^GET$',
            'name', 'policy_ro', 'access', 'public',
            'readOnly', true, 'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );

    SELECT read_only INTO v_read_only
    FROM api.handler WHERE object_id = 'ffffffff-8001-4000-8000-000000000001';
    IF v_read_only IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'TEST FAILED: readOnly=true should be stored on api.handler';
    END IF;

    SELECT read_only INTO v_read_only
    FROM api.vw_handler_info WHERE object_id = 'ffffffff-8001-4000-8000-000000000001';
    IF v_read_only IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'TEST FAILED: read_only should surface in vw_handler_info';
    END IF;

    -- Default is read-write.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-8002-4000-8000-000000000001',
            'path', '/policy-rw', 'httpMethod', '^GET$',
            'name', 'policy_rw', 'access', 'public'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    SELECT read_only INTO v_read_only
    FROM api.handler WHERE object_id = 'ffffffff-8002-4000-8000-000000000001';
    IF v_read_only IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'TEST FAILED: readOnly should default to false';
    END IF;

    RAISE NOTICE '  ✓ readOnly stored (default false) and surfaced by discovery';
END $$;

DO $$
DECLARE
    v_policy record;
BEGIN
    RAISE NOTICE '→ Testing resolve-then-open policy lookups';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-8101-4000-8000-000000000001',
            'path', '/policy-rr', 'httpMethod', '^GET$',
            'name', 'policy_rr', 'access', 'public',
            'readOnly', true, 'autoLog', false, 'minTransactionIsolation', 'repeatable read'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-8102-4000-8000-000000000001',
            'path', '/policy-ser', 'httpMethod', '^GET$',
            'name', 'policy_ser', 'access', 'public',
            'readOnly', true, 'autoLog', false, 'minTransactionIsolation', 'serializable'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );

    -- Caller sends nothing → the floor, read-only, not deferrable below serializable.
    SELECT * INTO v_policy FROM api.rest_route_policy('GET', '/policy-rr');
    IF NOT FOUND THEN
        RAISE EXCEPTION 'TEST FAILED: policy lookup resolved zero rows — every assertion below would compare against NULL and skip';
    END IF;
    IF v_policy.transaction_isolation IS DISTINCT FROM 'repeatable read'
       OR v_policy.transaction_read_only IS DISTINCT FROM true
       OR v_policy.transaction_deferrable IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'TEST FAILED: /policy-rr with no request should resolve (repeatable read, ro, not deferrable), got (%, %, %)',
            v_policy.transaction_isolation, v_policy.transaction_read_only, v_policy.transaction_deferrable;
    END IF;

    -- Weaker request → still the floor (downgrade impossible).
    SELECT * INTO v_policy FROM api.rest_route_policy('GET', '/policy-rr', 'read committed');
    IF NOT FOUND THEN
        RAISE EXCEPTION 'TEST FAILED: policy lookup resolved zero rows — every assertion below would compare against NULL and skip';
    END IF;
    IF v_policy.transaction_isolation IS DISTINCT FROM 'repeatable read' THEN
        RAISE EXCEPTION 'TEST FAILED: requesting weaker than the floor must yield the floor, got %',
            v_policy.transaction_isolation;
    END IF;

    -- Stronger request → escalation; serializable + read-only → deferrable.
    SELECT * INTO v_policy FROM api.rest_route_policy('GET', '/policy-rr', 'SERIALIZABLE');
    IF NOT FOUND THEN
        RAISE EXCEPTION 'TEST FAILED: policy lookup resolved zero rows — every assertion below would compare against NULL and skip';
    END IF;
    IF v_policy.transaction_isolation IS DISTINCT FROM 'serializable'
       OR v_policy.transaction_deferrable IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'TEST FAILED: escalated serializable read-only should be deferrable, got (%, %)',
            v_policy.transaction_isolation, v_policy.transaction_deferrable;
    END IF;

    -- Serializable floor + read-only → deferrable without any request.
    SELECT * INTO v_policy FROM api.rest_route_policy('GET', '/policy-ser');
    IF NOT FOUND THEN
        RAISE EXCEPTION 'TEST FAILED: policy lookup resolved zero rows — every assertion below would compare against NULL and skip';
    END IF;
    IF v_policy.transaction_isolation IS DISTINCT FROM 'serializable'
       OR v_policy.transaction_read_only IS DISTINCT FROM true
       OR v_policy.transaction_deferrable IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'TEST FAILED: /policy-ser should resolve SERIALIZABLE READ ONLY DEFERRABLE, got (%, %, %)',
            v_policy.transaction_isolation, v_policy.transaction_read_only, v_policy.transaction_deferrable;
    END IF;

    -- Unknown route → zero rows (proxy opens at requested/default; gateway answers 404).
    IF EXISTS (SELECT 1 FROM api.rest_route_policy('GET', '/no-such-route')) THEN
        RAISE EXCEPTION 'TEST FAILED: unknown route must resolve to zero rows';
    END IF;

    -- RPC lookup keyed by method name.
    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-8103-4000-8000-000000000001',
            'methodName', 'policy.report', 'access', 'public',
            'readOnly', true, 'autoLog', false, 'minTransactionIsolation', 'repeatable read'
        ),
        $body$ BEGIN RETURN api.jsonrpc_success('{}'::jsonb, api.content_json((request).content)->'id'); END; $body$
    );
    SELECT * INTO v_policy FROM api.rpc_route_policy('policy.report');
    IF NOT FOUND THEN
        RAISE EXCEPTION 'TEST FAILED: policy lookup resolved zero rows — every assertion below would compare against NULL and skip';
    END IF;
    IF v_policy.transaction_isolation IS DISTINCT FROM 'repeatable read'
       OR v_policy.transaction_read_only IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'TEST FAILED: rpc_route_policy should resolve the registered policy';
    END IF;

    -- MCP: one call takes the raw JSON-RPC request.
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-8104-4000-8000-000000000001',
            'type', 'tool', 'effect', 'read', 'name', 'policy_tool', 'access', 'public',
            'minTransactionIsolation', 'serializable',
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object())
        ),
        $body$ BEGIN RETURN api.mcp_tool_result(jsonb_build_array(), (request).request_id); END; $body$
    );
    SELECT * INTO v_policy FROM api.mcp_request_policy(
        jsonb_build_object('method', 'tools/call',
                           'params', jsonb_build_object('name', 'policy_tool'))::text);
    IF NOT FOUND THEN
        RAISE EXCEPTION 'TEST FAILED: policy lookup resolved zero rows — every assertion below would compare against NULL and skip';
    END IF;
    IF v_policy.transaction_isolation IS DISTINCT FROM 'serializable'
       OR v_policy.transaction_read_only IS DISTINCT FROM false
       OR v_policy.transaction_deferrable IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'TEST FAILED: mcp_request_policy should resolve the tool policy, got (%, %, %)',
            v_policy.transaction_isolation, v_policy.transaction_read_only, v_policy.transaction_deferrable;
    END IF;

    -- Non-dispatch methods and unknown names resolve to the no-policy row,
    -- still honoring a requested escalation.
    SELECT * INTO v_policy FROM api.mcp_request_policy(
        jsonb_build_object('method', 'tools/list')::text, 'serializable');
    IF NOT FOUND THEN
        RAISE EXCEPTION 'TEST FAILED: policy lookup resolved zero rows — every assertion below would compare against NULL and skip';
    END IF;
    IF v_policy.transaction_isolation IS DISTINCT FROM 'serializable'
       OR v_policy.transaction_read_only IS DISTINCT FROM false
       OR v_policy.transaction_deferrable IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'TEST FAILED: non-dispatch method should resolve request-only policy';
    END IF;
    SELECT * INTO v_policy FROM api.mcp_request_policy(
        jsonb_build_object('method', 'tools/call',
                           'params', jsonb_build_object('name', 'no_such_tool'))::text);
    IF NOT FOUND THEN
        RAISE EXCEPTION 'TEST FAILED: policy lookup resolved zero rows — every assertion below would compare against NULL and skip';
    END IF;
    IF v_policy.transaction_isolation IS NOT NULL
       OR v_policy.transaction_read_only IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'TEST FAILED: unknown tool should resolve the no-policy row';
    END IF;

    RAISE NOTICE '  ✓ rest/rpc/mcp policy lookups resolve max(floor, requested) + READ ONLY [DEFERRABLE]';
END $$;

-- The policy names the handler and the x-* annotations of its inputSchema
-- properties: what the gateway's interceptors select on.
DO $$
DECLARE
    v_schema jsonb := '{"type": "object", "properties": {
        "text": {"type": "string", "x-embed": true},
        "text_embedding": {"type": "array"},
        "tag": {"type": "string", "x-source": "catalog", "x-embed": false}}}';
    v_expected jsonb := '{"text": {"x-embed": true}, "tag": {"x-source": "catalog", "x-embed": false}}';
    v_policy record;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-8105-4000-8000-000000000001', 'path', '/policy-annotated',
                           'httpMethod', '^POST$', 'name', 'policy_annotated', 'access', 'public',
                           'inputSchema', v_schema),
        $body$ BEGIN RETURN api.json_response(200, '{}'); END; $body$);
    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object('id', 'ffffffff-8105-4000-8000-000000000002', 'methodName', 'policy.annotated',
                           'access', 'public', 'inputSchema', v_schema),
        $body$ BEGIN RETURN api.jsonrpc_success('{}'::jsonb, api.content_json((request).content)->'id'); END; $body$);
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-8105-4000-8000-000000000003', 'type', 'tool', 'effect', 'read',
                           'name', 'policy_annotated', 'access', 'public', 'inputSchema', v_schema),
        $body$ BEGIN RETURN api.mcp_tool_result(jsonb_build_array(), (request).request_id); END; $body$);

    SELECT * INTO STRICT v_policy FROM api.rest_route_policy('POST', '/policy-annotated');
    IF v_policy.input_annotations IS DISTINCT FROM v_expected OR v_policy.handler_name IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: rest_route_policy annotations: got % (handler %)',
            v_policy.input_annotations, v_policy.handler_name;
    END IF;
    SELECT * INTO STRICT v_policy FROM api.rpc_route_policy('policy.annotated');
    IF v_policy.input_annotations IS DISTINCT FROM v_expected OR v_policy.handler_name IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: rpc_route_policy annotations: got %', v_policy.input_annotations;
    END IF;
    SELECT * INTO STRICT v_policy FROM api.mcp_request_policy(
        '{"method": "tools/call", "params": {"name": "policy_annotated"}}');
    IF v_policy.input_annotations IS DISTINCT FROM v_expected OR v_policy.handler_name IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: mcp_request_policy annotations: got %', v_policy.input_annotations;
    END IF;

    SELECT * INTO STRICT v_policy FROM api.rest_route_policy('GET', '/policy-rr');
    IF v_policy.input_annotations IS NOT NULL OR v_policy.handler_name IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a route without annotations must name its handler and return NULL annotations, got %',
            v_policy.input_annotations;
    END IF;
    SELECT * INTO STRICT v_policy FROM api.mcp_request_policy('{"method": "tools/list"}');
    IF v_policy.input_annotations IS NOT NULL OR v_policy.handler_name IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a method without a handler must return no handler and no annotations';
    END IF;

    RAISE NOTICE '  ✓ policy lookups name the handler and its inputSchema annotations';
END $$;

DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
    v_route_id uuid;
    v_mcp api.mcp_response;
    v_envelope jsonb;
BEGIN
    RAISE NOTICE '→ Testing read-only enforcement (fail closed in a read-write transaction)';

    -- No isolation floor on these routes, so the read-only check is what fires.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-8201-4000-8000-000000000001',
            'path', '/ro-rest', 'httpMethod', '^GET$',
            'name', 'ro_rest', 'access', 'public', 'readOnly', true, 'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    v_response := api.rest_invoke('GET', '/ro-rest', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 428 THEN
        RAISE EXCEPTION 'TEST FAILED: read-only route in a read-write tx should return 428, got %',
            (v_response).status_code;
    END IF;
    v_content := api.content_json((v_response).content);
    IF v_content->>'code' IS DISTINCT FROM 'pgmi.transaction_read_only_required'
       OR v_content->'requiredReadOnly' IS DISTINCT FROM 'true'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: REST rejection missing machine code, got %', v_content->>'code';
    END IF;

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-8202-4000-8000-000000000001',
            'methodName', 'ro.rpc', 'access', 'public', 'readOnly', true, 'autoLog', false
        ),
        $body$ BEGIN RETURN api.jsonrpc_success('{}'::jsonb, api.content_json((request).content)->'id'); END; $body$
    );
    v_route_id := api.rpc_resolve('ro.rpc');
    v_response := api.rpc_invoke(v_route_id, ''::extensions.hstore,
        convert_to('{"jsonrpc":"2.0","method":"ro.rpc","id":"7"}', 'UTF8'));
    IF (v_response).status_code IS DISTINCT FROM 428 THEN
        RAISE EXCEPTION 'TEST FAILED: RPC read-only route should return 428, got %', (v_response).status_code;
    END IF;
    v_content := api.content_json((v_response).content);
    IF v_content->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.transaction_read_only_required'
       OR v_content->'error'->'data'->'requiredReadOnly' IS DISTINCT FROM 'true'::jsonb
       OR v_content->>'id' IS DISTINCT FROM '7' THEN
        RAISE EXCEPTION 'TEST FAILED: RPC rejection must carry the machine code and echo the id';
    END IF;

    RAISE NOTICE '  ✓ REST and RPC reject 428/-32600 with pgmi.transaction_read_only_required';
END $$;

DO $$
DECLARE
    v_doc jsonb;
    v_op jsonb;
    v_v1 text;
    v_v2 text;
BEGIN
    RAISE NOTICE '→ Testing OpenAPI advertisement and catalog version';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-8301-4000-8000-000000000001',
            'path', '/oa-ro-rr', 'httpMethod', '^GET$',
            'name', 'oa_ro_rr', 'access', 'public',
            'readOnly', true, 'autoLog', false, 'minTransactionIsolation', 'repeatable read'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-8302-4000-8000-000000000001',
            'path', '/oa-ro-ser', 'httpMethod', '^GET$',
            'name', 'oa_ro_ser', 'access', 'public',
            'readOnly', true, 'autoLog', false, 'minTransactionIsolation', 'serializable'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-8303-4000-8000-000000000001',
            'path', '/oa-rw', 'httpMethod', '^GET$',
            'name', 'oa_rw', 'access', 'public'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );

    v_doc := api.openapi_document();

    v_op := v_doc->'paths'->'/oa-ro-rr'->'get';
    IF (v_op->>'x-pgmi-read-only')::boolean IS DISTINCT FROM true
       OR (v_op->>'x-pgmi-replica-safe')::boolean IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'TEST FAILED: read-only repeatable-read route should advertise read-only + replica-safe, got %',
            v_op;
    END IF;

    -- Serializable is unavailable on a hot standby: read-only but primary-only.
    v_op := v_doc->'paths'->'/oa-ro-ser'->'get';
    IF (v_op->>'x-pgmi-read-only')::boolean IS DISTINCT FROM true
       OR (v_op->>'x-pgmi-replica-safe')::boolean IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'TEST FAILED: read-only serializable route must advertise replica-safe FALSE, got %',
            v_op;
    END IF;

    v_op := v_doc->'paths'->'/oa-rw'->'get';
    IF v_op ? 'x-pgmi-read-only' OR v_op ? 'x-pgmi-replica-safe' THEN
        RAISE EXCEPTION 'TEST FAILED: a read-write route must not carry read-only extensions';
    END IF;

    -- Flipping readOnly is a contract change: the catalog version must move.
    v_v1 := api.catalog_version();
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-8303-4000-8000-000000000001',
            'path', '/oa-rw', 'httpMethod', '^GET$',
            'name', 'oa_rw', 'access', 'public',
            'readOnly', true, 'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    v_v2 := api.catalog_version();
    IF (v_v1 = v_v2) IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: flipping readOnly must change api.catalog_version()';
    END IF;

    RAISE NOTICE '  ✓ x-pgmi-read-only + x-pgmi-replica-safe advertised; readOnly participates in the ETag';
END $$;

-- Keep this block LAST: it turns the transaction read-only, and everything
-- after it in this file would inherit that until the file's savepoint rolls
-- back. Turning read-only ON mid-transaction is legal (only the reverse is
-- restricted), which is exactly what lets the accept path run in the harness.
DO $$
DECLARE
    v_response api.http_response;
    v_logged int;
BEGIN
    RAISE NOTICE '→ Testing read-only accept path (READ ONLY transaction dispatches)';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-8401-4000-8000-000000000001',
            'path', '/ro-accept', 'httpMethod', '^GET$',
            'name', 'ro_accept', 'access', 'public', 'readOnly', true, 'autoLog', false
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );

    PERFORM set_config('transaction_read_only', 'on', true);

    v_response := api.rest_invoke('GET', '/ro-accept', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: read-only route in a READ ONLY tx should dispatch (200), got %',
            (v_response).status_code;
    END IF;

    -- Exchange logging is a write; the gateway must have skipped it.
    SELECT count(*) INTO v_logged
    FROM api.rest_exchange
    WHERE handler_object_id = 'ffffffff-8401-4000-8000-000000000001';
    IF v_logged IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'TEST FAILED: exchange logging must be skipped in a READ ONLY transaction';
    END IF;

    RAISE NOTICE '  ✓ READ ONLY transaction dispatches the route; gateway writes are skipped';
END $$;

DO $$
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE '✓ ALL TRANSACTION POLICY TESTS PASSED';
END $$;
