-- ============================================================================
-- Test: MCP Protocol Layer
-- ============================================================================
-- Validates the MCP protocol functions: initialize, ping, and dispatcher.
-- ============================================================================

DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
BEGIN
    RAISE NOTICE '-> Testing MCP Initialize Handshake';

    -- ========================================================================
    -- Test: Initialize with valid protocol version
    -- ========================================================================

    v_response := api.mcp_initialize('{"protocolVersion":"2024-11-05"}'::jsonb, '"init-1"'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
        RAISE EXCEPTION 'TEST FAILED: Initialize response missing jsonrpc 2.0';
    END IF;

    IF v_envelope->>'id' IS DISTINCT FROM 'init-1' THEN
        RAISE EXCEPTION 'TEST FAILED: Initialize request_id not echoed';
    END IF;

    IF v_envelope->'result'->>'protocolVersion' IS DISTINCT FROM '2024-11-05' THEN
        RAISE EXCEPTION 'TEST FAILED: Initialize missing protocolVersion in result';
    END IF;

    IF v_envelope->'result'->'serverInfo' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Initialize missing serverInfo';
    END IF;

    IF v_envelope->'result'->'capabilities' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Initialize missing capabilities';
    END IF;

    RAISE NOTICE '  + Initialize returns protocolVersion, serverInfo, capabilities';

    -- ========================================================================
    -- Test: Initialize with missing protocol version
    -- ========================================================================

    v_response := api.mcp_initialize('{}'::jsonb, '"init-2"'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->'error' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Initialize without protocolVersion should error';
    END IF;

    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32602 THEN
        RAISE EXCEPTION 'TEST FAILED: Initialize missing param should return -32602';
    END IF;

    RAISE NOTICE '  + Initialize without protocolVersion returns -32602';

    -- ========================================================================
    -- Test: Initialize negotiates an unknown version to the server's best
    -- (per the MCP lifecycle, the server suggests a version it supports rather
    -- than erroring).
    -- ========================================================================

    v_response := api.mcp_initialize('{"protocolVersion":"1999-01-01"}'::jsonb, '"init-3"'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Initialize with unknown version should negotiate, not error: %', v_envelope->'error';
    END IF;

    IF v_envelope->'result'->>'protocolVersion' IS DISTINCT FROM '2025-11-25' THEN
        RAISE EXCEPTION 'TEST FAILED: unknown version should negotiate to server best 2025-11-25, got %', v_envelope->'result'->>'protocolVersion';
    END IF;

    RAISE NOTICE '  + Initialize negotiates unknown version to server best (2025-11-25)';

    -- ========================================================================
    -- Test: a current client version (2025-06-18) completes initialize and is
    -- echoed back.
    -- ========================================================================

    v_response := api.mcp_initialize('{"protocolVersion":"2025-06-18"}'::jsonb, '"init-4"'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->'result'->>'protocolVersion' IS DISTINCT FROM '2025-06-18' THEN
        RAISE EXCEPTION 'TEST FAILED: supported version 2025-06-18 should be echoed, got %', v_envelope->'result'->>'protocolVersion';
    END IF;

    RAISE NOTICE '  + Initialize echoes a supported client version (2025-06-18)';

    RAISE NOTICE '+ MCP Initialize tests passed';
END $$;

DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
BEGIN
    RAISE NOTICE '-> Testing MCP Ping';

    v_response := api.mcp_ping('"ping-1"'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
        RAISE EXCEPTION 'TEST FAILED: Ping response missing jsonrpc 2.0';
    END IF;

    IF v_envelope->>'id' IS DISTINCT FROM 'ping-1' THEN
        RAISE EXCEPTION 'TEST FAILED: Ping request_id not echoed';
    END IF;

    IF v_envelope->'result' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Ping missing result';
    END IF;

    RAISE NOTICE '  + Ping returns empty object result';
    RAISE NOTICE '+ MCP Ping tests passed';
END $$;

DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
BEGIN
    RAISE NOTICE '-> Testing MCP Dispatcher';

    -- ========================================================================
    -- Test: Dispatcher routes initialize
    -- ========================================================================

    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"d1","method":"initialize","params":{"protocolVersion":"2024-11-05"}}'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->'result'->'serverInfo' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Dispatcher initialize routing failed';
    END IF;

    RAISE NOTICE '  + Dispatcher routes initialize';

    -- ========================================================================
    -- Test: notifications/* is a no-op even when the client supplies an id
    -- ========================================================================

    -- The dispatcher short-circuits on a missing id OR a notifications/* method.
    -- The id-less path is covered inline; this is the only coverage of the
    -- method-name path. Assert the envelope is absent, not that it carries no
    -- error: NULL->'error' IS NOT NULL is false, so that form cannot fail.
    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"d2","method":"notifications/initialized"}'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: notifications/initialized with an id must still return no envelope, got %', v_envelope;
    END IF;

    RAISE NOTICE '  + Dispatcher treats notifications/* as no-op even with an id';

    -- ========================================================================
    -- Test: Dispatcher routes ping
    -- ========================================================================

    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"d3","method":"ping"}'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->>'id' IS DISTINCT FROM 'd3' THEN
        RAISE EXCEPTION 'TEST FAILED: Dispatcher ping routing failed';
    END IF;

    RAISE NOTICE '  + Dispatcher routes ping';

    -- ========================================================================
    -- Test: Dispatcher routes tools/list
    -- ========================================================================

    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"d4","method":"tools/list"}'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->'result'->'tools' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Dispatcher tools/list missing tools array';
    END IF;

    RAISE NOTICE '  + Dispatcher routes tools/list';

    -- ========================================================================
    -- Test: Dispatcher routes resources/list
    -- ========================================================================

    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"d5","method":"resources/list"}'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->'result'->'resources' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Dispatcher resources/list missing resources array';
    END IF;

    RAISE NOTICE '  + Dispatcher routes resources/list';

    -- ========================================================================
    -- Test: Dispatcher routes prompts/list
    -- ========================================================================

    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"d6","method":"prompts/list"}'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->'result'->'prompts' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Dispatcher prompts/list missing prompts array';
    END IF;

    RAISE NOTICE '  + Dispatcher routes prompts/list';

    -- ========================================================================
    -- Test: Dispatcher returns -32601 for unknown method
    -- ========================================================================

    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"d7","method":"unknown/method"}'::jsonb);
    v_envelope := (v_response).envelope;

    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32601 THEN
        RAISE EXCEPTION 'TEST FAILED: Dispatcher unknown method should return -32601';
    END IF;

    RAISE NOTICE '  + Dispatcher returns -32601 for unknown method';

    -- ========================================================================
    -- Test: Dispatcher returns -32600 for missing jsonrpc
    -- ========================================================================

    v_response := api.mcp_handle_request('{"id":"d8","method":"ping"}'::jsonb);
    v_envelope := (v_response).envelope;

    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
        RAISE EXCEPTION 'TEST FAILED: Dispatcher missing jsonrpc should return -32600';
    END IF;

    RAISE NOTICE '  + Dispatcher returns -32600 for missing jsonrpc';

    -- ========================================================================
    -- Test: Dispatcher returns -32600 for missing method
    -- ========================================================================

    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"d9"}'::jsonb);
    v_envelope := (v_response).envelope;

    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
        RAISE EXCEPTION 'TEST FAILED: Dispatcher missing method should return -32600';
    END IF;

    RAISE NOTICE '  + Dispatcher returns -32600 for missing method';

    -- ========================================================================
    -- Test: Dispatcher returns -32600 for null request
    -- ========================================================================

    v_response := api.mcp_handle_request(NULL);
    v_envelope := (v_response).envelope;

    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
        RAISE EXCEPTION 'TEST FAILED: Dispatcher null request should return -32600';
    END IF;

    RAISE NOTICE '  + Dispatcher returns -32600 for null request';

    RAISE NOTICE '+ MCP Dispatcher tests passed';
END $$;

DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
BEGIN
    RAISE NOTICE '-> Testing MCP Full Round-Trip';

    -- Register a test tool for this test block
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-5001-4000-8000-000000000001',
            'type', 'tool',
            'name', 'test_roundtrip_tool',
            'description', 'Tool for round-trip testing',
            'inputSchema', jsonb_build_object(
                'type', 'object',
                'properties', jsonb_build_object(
                    'value', jsonb_build_object('type', 'string')
                ),
                'required', jsonb_build_array('value')
            ),
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('Echo: ' || ((request).arguments->>'value'))),
        (request).request_id
    );
END;
        $body$
    );

    -- ========================================================================
    -- Test: Initialize -> tools/list -> tools/call round trip
    -- ========================================================================

    -- Step 1: Initialize
    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"rt1","method":"initialize","params":{"protocolVersion":"2024-11-05"}}'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Round-trip initialize failed: %', v_envelope->'error';
    END IF;

    RAISE NOTICE '  + Round-trip: initialize succeeded';

    -- Step 2: tools/list
    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"rt2","method":"tools/list"}'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Round-trip tools/list failed';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_envelope->'result'->'tools') AS tool
        WHERE tool->>'name' = 'test_roundtrip_tool'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: Round-trip test_roundtrip_tool not in tools list';
    END IF;

    RAISE NOTICE '  + Round-trip: tools/list shows test_roundtrip_tool';

    -- Step 3: tools/call
    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"rt3","method":"tools/call","params":{"name":"test_roundtrip_tool","arguments":{"value":"hello"}}}'::jsonb);
    v_envelope := (v_response).envelope;

    IF v_envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: Round-trip tools/call failed: %', v_envelope->'error';
    END IF;

    IF coalesce(v_envelope->'result'->'content'->0->>'text', '') NOT LIKE 'Echo: hello%' THEN
        RAISE EXCEPTION 'TEST FAILED: Round-trip tools/call wrong result';
    END IF;

    RAISE NOTICE '  + Round-trip: tools/call returns expected result';

    RAISE NOTICE '+ MCP Full Round-Trip tests passed';
END $$;

DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
BEGIN
    RAISE NOTICE '→ Testing MCP resource templates discovery + deterministic routing';

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-a001-4000-8000-000000000001', 'type', 'resource',
            'name', 'test_templated_res', 'description', 'Test resource template',
            'uriTemplate', 'testproto:///{schema}/{table}',
            'mimeType', 'application/json', 'requiresAuth', false),
        $body$
BEGIN
    RETURN api.mcp_resource_result(
        jsonb_build_array(jsonb_build_object('uri', (request).uri, 'mimeType', 'application/json', 'text', 'ok')),
        (request).request_id);
END;
        $body$
    );

    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"rt-1","method":"resources/templates/list"}'::jsonb);
    v_envelope := (v_response).envelope;
    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_envelope->'result'->'resourceTemplates') AS rt
        WHERE rt->>'name' = 'test_templated_res' AND rt->>'uriTemplate' = 'testproto:///{schema}/{table}'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: resources/templates/list missing test_templated_res, got %', v_envelope->'result';
    END IF;
    RAISE NOTICE '  + resources/templates/list returns the test_templated_res template';

    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"rt-2","method":"resources/list"}'::jsonb);
    v_envelope := (v_response).envelope;
    IF EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_envelope->'result'->'resources') AS r
        WHERE r ? 'uriTemplate' OR (r->>'uri') IS NULL
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: resources/list must contain only concrete uri entries, got %', v_envelope->'result';
    END IF;
    RAISE NOTICE '  + resources/list contains only concrete uri entries';

    -- Deterministic routing: two overlapping templates both match mcptest:///x;
    -- the most specific (longest template) wins, consistently.
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-b001-4000-8000-000000000001', 'type', 'resource',
            'name', 'overlap_short', 'description', 'short', 'uriTemplate', 'mcptest:///{a}',
            'mimeType', 'application/json', 'requiresAuth', false),
        $body$
BEGIN
    RETURN api.mcp_resource_result(
        jsonb_build_array(jsonb_build_object('uri', (request).uri, 'mimeType', 'application/json', 'text', 'handler=short')),
        (request).request_id);
END;
        $body$
    );
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-b002-4000-8000-000000000001', 'type', 'resource',
            'name', 'overlap_long', 'description', 'long', 'uriTemplate', 'mcptest:///{aa}',
            'mimeType', 'application/json', 'requiresAuth', false),
        $body$
BEGIN
    RETURN api.mcp_resource_result(
        jsonb_build_array(jsonb_build_object('uri', (request).uri, 'mimeType', 'application/json', 'text', 'handler=long')),
        (request).request_id);
END;
        $body$
    );

    v_response := api.mcp_read_resource('mcptest:///x', NULL, '"ovl-1"'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: overlapping resource read errored: %', v_envelope->'error';
    END IF;
    IF v_envelope->'result'->'contents'->0->>'text' IS DISTINCT FROM 'handler=long' THEN
        RAISE EXCEPTION 'TEST FAILED: overlapping templates did not route to the longest (deterministic) handler, got %', v_envelope->'result';
    END IF;
    RAISE NOTICE '  + Overlapping resource templates route deterministically (longest wins)';
END $$;

DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
    v_tool jsonb;
BEGIN
    RAISE NOTICE '-> Testing MCP Registration Validation';

    -- ========================================================================
    -- Test: Tool with no inputSchema gets a default {"type":"object"}
    -- ========================================================================

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-c001-4000-8000-000000000001',
            'type', 'tool',
            'name', 'test_no_schema_tool',
            'description', 'Tool registered without inputSchema',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('ok')),
        (request).request_id
    );
END;
        $body$
    );

    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"rv1","method":"tools/list"}'::jsonb);
    v_envelope := (v_response).envelope;

    SELECT tool INTO v_tool
    FROM jsonb_array_elements(v_envelope->'result'->'tools') AS tool
    WHERE tool->>'name' = 'test_no_schema_tool';

    IF v_tool IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: test_no_schema_tool not in tools/list';
    END IF;

    IF v_tool->'inputSchema' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: tool missing inputSchema should get default';
    END IF;

    IF v_tool->'inputSchema'->>'type' IS DISTINCT FROM 'object' THEN
        RAISE EXCEPTION 'TEST FAILED: default inputSchema should have type=object, got %', v_tool->'inputSchema';
    END IF;

    RAISE NOTICE '  + Tool with no inputSchema gets default {"type":"object"}';

    -- ========================================================================
    -- Test: Tool with empty {} inputSchema is rejected by domain
    -- ========================================================================

    BEGIN
        PERFORM api.create_or_replace_mcp_handler(
            jsonb_build_object(
                'id', 'ffffffff-c002-4000-8000-000000000001',
                'type', 'tool',
                'name', 'test_empty_schema_tool',
                'description', 'Tool with empty inputSchema',
                'inputSchema', '{}'::jsonb,
                'requiresAuth', false
            ),
            $body$BEGIN RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id); END;$body$
        );
        RAISE EXCEPTION 'TEST FAILED: empty {} inputSchema should be rejected';
    EXCEPTION WHEN check_violation THEN
        RAISE NOTICE '  + Empty {} inputSchema rejected by json_schema domain';
    END;

    -- ========================================================================
    -- Test: Tool with a JSON string inputSchema is rejected by domain
    -- ========================================================================

    BEGIN
        PERFORM api.create_or_replace_mcp_handler(
            jsonb_build_object(
                'id', 'ffffffff-c003-4000-8000-000000000001',
                'type', 'tool',
                'name', 'test_string_schema_tool',
                'description', 'Tool with string inputSchema',
                'inputSchema', '"nonsense"'::jsonb,
                'requiresAuth', false
            ),
            $body$BEGIN RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id); END;$body$
        );
        RAISE EXCEPTION 'TEST FAILED: string inputSchema should be rejected';
    EXCEPTION WHEN check_violation THEN
        RAISE NOTICE '  + String inputSchema rejected by json_schema domain';
    END;

    -- ========================================================================
    -- Test: Resource with no uriTemplate is rejected
    -- ========================================================================

    BEGIN
        PERFORM api.create_or_replace_mcp_handler(
            jsonb_build_object(
                'id', 'ffffffff-c004-4000-8000-000000000001',
                'type', 'resource',
                'name', 'test_no_uri_resource',
                'description', 'Resource without uriTemplate',
                'requiresAuth', false
            ),
            $body$BEGIN RETURN api.mcp_resource_result(jsonb_build_array(jsonb_build_object('uri','x','mimeType','text/plain','text','ok')), (request).request_id); END;$body$
        );
        RAISE EXCEPTION 'TEST FAILED: resource without uriTemplate should be rejected';
    EXCEPTION WHEN invalid_parameter_value THEN
        RAISE NOTICE '  + Resource without uriTemplate rejected';
    END;

    RAISE NOTICE '+ MCP Registration Validation tests passed';
END $$;

DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
BEGIN
    RAISE NOTICE '-> Testing MCP Non-Object Request Rejection';

    v_response := api.mcp_handle_request('[{"jsonrpc":"2.0","id":"b1","method":"ping"}]'::jsonb);
    v_envelope := (v_response).envelope;

    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
        RAISE EXCEPTION 'TEST FAILED: array body should return -32600, got %', v_envelope;
    END IF;

    RAISE NOTICE '  + Array body returns -32600';

    v_response := api.mcp_handle_request('"just a string"'::jsonb);
    v_envelope := (v_response).envelope;

    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
        RAISE EXCEPTION 'TEST FAILED: scalar string body should return -32600, got %', v_envelope;
    END IF;

    RAISE NOTICE '  + Scalar string body returns -32600';

    v_response := api.mcp_handle_request('42'::jsonb);
    v_envelope := (v_response).envelope;

    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
        RAISE EXCEPTION 'TEST FAILED: numeric body should return -32600, got %', v_envelope;
    END IF;

    RAISE NOTICE '  + Numeric body returns -32600';

    v_response := api.mcp_handle_request('null'::jsonb);
    v_envelope := (v_response).envelope;

    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
        RAISE EXCEPTION 'TEST FAILED: JSON null body should return -32600, got %', v_envelope;
    END IF;

    RAISE NOTICE '  + JSON null body returns -32600';

    RAISE NOTICE '+ MCP Non-Object Request Rejection tests passed';
END $$;

-- MCP: "Requests MUST include a string or integer ID" and "Unlike base
-- JSON-RPC, the ID MUST NOT be null." The gateway used to skip this check
-- entirely and answer every one of these with a result, echoing the offending
-- value back as the response id.
DO $$
DECLARE
    v_case      record;
    v_envelope  jsonb;
BEGIN
    FOR v_case IN
        SELECT * FROM (VALUES
            ('null',    '{"jsonrpc":"2.0","id":null,"method":"ping"}'),
            ('boolean', '{"jsonrpc":"2.0","id":true,"method":"ping"}'),
            ('object',  '{"jsonrpc":"2.0","id":{"a":1},"method":"ping"}'),
            ('array',   '{"jsonrpc":"2.0","id":[1],"method":"ping"}')
        ) AS t(label, request)
    LOOP
        v_envelope := (api.mcp_handle_request(v_case.request::jsonb)).envelope;

        IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
            RAISE EXCEPTION 'TEST FAILED: % id must be an Invalid Request, got %',
                v_case.label, v_envelope;
        END IF;

        -- JSON-RPC 2.0: when the id could not be detected it MUST be Null.
        IF jsonb_typeof(v_envelope->'id') IS DISTINCT FROM 'null' THEN
            RAISE EXCEPTION 'TEST FAILED: % id response must carry a null id, got %',
                v_case.label, v_envelope;
        END IF;

        IF v_envelope ? 'result' THEN
            RAISE EXCEPTION 'TEST FAILED: % id produced a result: %',
                v_case.label, v_envelope;
        END IF;
    END LOOP;

    RAISE NOTICE '  + malformed request ids rejected with -32600 and a null id';
END $$;

-- The rejection must not swallow valid ids. Numbers stay numbers: the Go server
-- (internal/mcp) accepts any JSON number, and the two surfaces must agree.
DO $$
DECLARE
    v_case      record;
    v_envelope  jsonb;
BEGIN
    FOR v_case IN
        SELECT * FROM (VALUES
            ('string', '{"jsonrpc":"2.0","id":"ok1","method":"ping"}', '"ok1"'),
            ('integer','{"jsonrpc":"2.0","id":7,"method":"ping"}',     '7'),
            ('float',  '{"jsonrpc":"2.0","id":1.5,"method":"ping"}',   '1.5')
        ) AS t(label, request, want_id)
    LOOP
        v_envelope := (api.mcp_handle_request(v_case.request::jsonb)).envelope;

        IF NOT (v_envelope ? 'result') THEN
            RAISE EXCEPTION 'TEST FAILED: valid % id was rejected: %',
                v_case.label, v_envelope;
        END IF;
        IF v_envelope->'id' IS DISTINCT FROM v_case.want_id::jsonb THEN
            RAISE EXCEPTION 'TEST FAILED: valid % id not echoed, want % got %',
                v_case.label, v_case.want_id, v_envelope->'id';
        END IF;
    END LOOP;

    RAISE NOTICE '  + valid string/number ids still accepted and echoed';
END $$;

-- ============================================================================
-- Error objects, NULL handler results, definition shape, argument checks
-- ============================================================================
DO $$
DECLARE
    v_env jsonb;
    v_rejected boolean;
    v_bad jsonb;
BEGIN
    RAISE NOTICE '-> Testing MCP request and definition validation';

    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'f3780000-0000-4000-8000-000000000001', 'type', 'tool', 'name', 'pgmi378_needs_n',
        'description', 'needs n', 'requiresAuth', false,
        'inputSchema', '{"type":"object","properties":{"n":{"type":"integer"},"d":{"type":"string"}},"required":["n"]}'::jsonb),
        $b$BEGIN
            PERFORM ((request).arguments->>'d')::date;
            RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id);
        END;$b$);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'f3780000-0000-4000-8000-000000000002', 'type', 'tool', 'name', 'pgmi378_returns_null',
        'description', 'returns null', 'requiresAuth', false, 'inputSchema', '{"type":"object"}'::jsonb),
        $b$BEGIN RETURN NULL; END;$b$);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'f3780000-0000-4000-8000-000000000003', 'type', 'prompt', 'name', 'pgmi378_prompt',
        'description', 'needs topic', 'requiresAuth', false,
        'arguments', '[{"name":"topic","required":true}]'::jsonb),
        $b$BEGIN
            RETURN api.mcp_prompt_result(jsonb_build_array(jsonb_build_object('role', 'user',
                'content', api.mcp_text((request).arguments->>'topic'))), (request).request_id);
        END;$b$);

    -- 1. Every error object carries a message, even without params.
    v_env := (api.mcp_handle_request('{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}'::jsonb)).envelope;
    IF (v_env->'error'->>'code')::int IS DISTINCT FROM -32602 OR v_env->'error'->>'message' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: tools/call without name must be -32602 with a message, got %', v_env;
    END IF;
    v_env := (api.mcp_handle_request('{"jsonrpc":"2.0","id":2,"method":"resources/read","params":[]}'::jsonb)).envelope;
    IF (v_env->'error'->>'code')::int IS DISTINCT FROM -32602 THEN
        RAISE EXCEPTION 'TEST FAILED: non-object params must be -32602, got %', v_env;
    END IF;
    IF (api.mcp_error(-32603, NULL, '1'::jsonb)).envelope->'error'->>'message' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: api.mcp_error dropped the message';
    END IF;

    -- 2. A NULL handler result is answered, not treated as a notification.
    v_env := (api.mcp_call_tool('pgmi378_returns_null', '{}'::jsonb, NULL, '3'::jsonb)).envelope;
    IF v_env IS NULL OR (v_env->'result'->>'isError')::boolean IS DISTINCT FROM true OR v_env->'id' IS DISTINCT FROM '3'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: NULL handler result must be an isError result for id 3, got %', v_env;
    END IF;

    -- 3. Definitions must have MCP's required shape.
    FOREACH v_bad IN ARRAY ARRAY[
        '{"type":"tool","inputSchema":true}',
        '{"type":"tool","inputSchema":{"type":"string"}}',
        '{"type":"tool","inputSchema":{"type":"object"},"outputSchema":{"type":"array"}}',
        '{"type":"prompt","arguments":{"name":"x"}}',
        '{"type":"prompt","arguments":[{"required":true}]}'
    ]::jsonb[] LOOP
        v_rejected := false;
        BEGIN
            PERFORM api.create_or_replace_mcp_handler(
                v_bad || jsonb_build_object('id', 'f3780000-0000-4000-8000-000000000009', 'name', 'pgmi378_bad', 'description', 'bad'),
                $b$BEGIN RETURN NULL; END;$b$);
        EXCEPTION WHEN invalid_parameter_value OR check_violation THEN
            v_rejected := true;
        END;
        IF v_rejected IS DISTINCT FROM true THEN
            RAISE EXCEPTION 'TEST FAILED: registration accepted a malformed definition %', v_bad;
        END IF;
    END LOOP;

    -- 4. Tool arguments are validated, and the handler's own message reaches the model.
    v_env := (api.mcp_call_tool('pgmi378_needs_n', '{}'::jsonb, NULL, '4'::jsonb)).envelope;
    IF (v_env->'result'->>'isError')::boolean IS DISTINCT FROM true
       OR COALESCE(v_env->'result'->'content'->0->>'text', '') NOT LIKE '%n is required%' THEN
        RAISE EXCEPTION 'TEST FAILED: missing required argument must be a named isError result, got %', v_env;
    END IF;
    v_env := (api.mcp_call_tool('pgmi378_needs_n', '{"n":"x"}'::jsonb, NULL, '5'::jsonb)).envelope;
    IF COALESCE(v_env->'result'->'content'->0->>'text', '') NOT LIKE '%n must be integer%' THEN
        RAISE EXCEPTION 'TEST FAILED: wrongly typed argument must be named, got %', v_env;
    END IF;
    v_env := (api.mcp_call_tool('pgmi378_needs_n', '{"n":1,"d":"yesterday-ish"}'::jsonb, NULL, '6'::jsonb)).envelope;
    IF v_env->'result'->'content'->0->>'text' IS DISTINCT FROM 'A submitted date or time is malformed' THEN
        RAISE EXCEPTION 'TEST FAILED: bad input must reach the model as its class message, got %', v_env;
    END IF;

    -- 5. prompts/get enforces required arguments.
    v_env := (api.mcp_get_prompt('pgmi378_prompt', '{}'::jsonb, NULL, '7'::jsonb)).envelope;
    IF (v_env->'error'->>'code')::int IS DISTINCT FROM -32602 OR COALESCE(v_env->'error'->>'message', '') NOT LIKE '%topic%' THEN
        RAISE EXCEPTION 'TEST FAILED: missing required prompt argument must be -32602 naming it, got %', v_env;
    END IF;

    RAISE NOTICE '  + MCP errors, NULL results, definitions and arguments are validated';
END $$;

DO $$
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE '===============================================================';
    RAISE NOTICE '+ ALL MCP PROTOCOL TESTS PASSED';
    RAISE NOTICE '===============================================================';
END $$;

-- A tool that declares outputSchema must return structuredContent that fits
-- it: reference-SDK clients validate the result against the advertised schema
-- and reject one without it. Registration cannot see the body, so this is the
-- round trip that pins the pattern.
DO $$
DECLARE
    v_envelope jsonb;
    v_schema   jsonb := jsonb_build_object(
        'type', 'object',
        'properties', jsonb_build_object('n', jsonb_build_object('type', 'integer')),
        'required', jsonb_build_array('n'));
    v_listed   jsonb;
    v_missing  text;
BEGIN
    RAISE NOTICE '-> Testing outputSchema and structuredContent round trip';

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-0293-4000-8000-000000000001',
            'type', 'tool', 'name', 'declares_output',
            'description', 'Returns a count',
            'outputSchema', v_schema,
            'requiresAuth', false),
        $body$
DECLARE
    v_structured jsonb := jsonb_build_object('n', 42);
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text(v_structured::text)),
        (request).request_id, false, v_structured);
END;
        $body$);

    SELECT t->'outputSchema' INTO v_listed
    FROM jsonb_array_elements(api.mcp_list_tools()->'tools') t
    WHERE t->>'name' = 'declares_output';
    IF v_listed IS DISTINCT FROM v_schema THEN
        RAISE EXCEPTION 'TEST FAILED: tools/list must advertise the declared outputSchema, got %', v_listed;
    END IF;

    v_envelope := (api.mcp_handle_request(
        '{"jsonrpc":"2.0","id":"os1","method":"tools/call","params":{"name":"declares_output","arguments":{}}}'::jsonb)).envelope;

    IF jsonb_typeof(v_envelope->'result'->'structuredContent') IS DISTINCT FROM 'object' THEN
        RAISE EXCEPTION 'TEST FAILED: a tool with outputSchema must return structuredContent, got %', v_envelope;
    END IF;
    SELECT string_agg(k, ', ') INTO v_missing
    FROM jsonb_array_elements_text(v_schema->'required') AS k
    WHERE NOT (v_envelope->'result'->'structuredContent') ? k;
    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: structuredContent lacks required key(s) %', v_missing;
    END IF;
    IF jsonb_typeof(v_envelope->'result'->'structuredContent'->'n') IS DISTINCT FROM 'number' THEN
        RAISE EXCEPTION 'TEST FAILED: structuredContent.n must be a number, got %',
            v_envelope->'result'->'structuredContent'->'n';
    END IF;

    RAISE NOTICE '  + outputSchema is advertised and the success result carries conforming structuredContent';
END $$;
