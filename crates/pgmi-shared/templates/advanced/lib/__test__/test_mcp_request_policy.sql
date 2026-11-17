-- ============================================================================
-- Test: MCP refusals before dispatch (api.mcp_request_policy)
-- ============================================================================
-- The gateway forwards MCP-Protocol-Version, Mcp-Method and Mcp-Name with the
-- body; the policy answers the refusal, the era and the transaction. Asserts
-- codes, ids and data.header, never message prose.
-- ============================================================================

DO $$
DECLARE
    v_meta constant jsonb := '{"io.modelcontextprotocol/protocolVersion": "2026-07-28",
                               "io.modelcontextprotocol/clientCapabilities": {}}';
    v_call jsonb;
    v_case record;
    v_row record;
    v_count int := 0;
BEGIN
    RAISE NOTICE '-> Testing MCP header, version and _meta refusals before dispatch';

    v_call := jsonb_build_object('jsonrpc', '2.0', 'id', 1, 'method', 'tools/call',
                                 'params', jsonb_build_object('name', 'list_tasks', '_meta', v_meta));

    FOR v_case IN
        SELECT * FROM (VALUES
            ('matching',
             v_call, '2026-07-28', 'tools/call', 'list_tasks', NULL::int, NULL::text, true),
            ('base64 name',
             v_call, '2026-07-28', 'tools/call', '=?base64?bGlzdF90YXNrcw==?=', NULL, NULL, true),
            ('base64 method',
             v_call, '2026-07-28', '=?base64?dG9vbHMvY2FsbA==?=', 'list_tasks', NULL, NULL, true),
            ('no method',
             v_call, '2026-07-28', NULL, 'list_tasks', -32020, 'Mcp-Method', true),
            ('other method',
             v_call, '2026-07-28', 'tools/list', 'list_tasks', -32020, 'Mcp-Method', true),
            ('no name',
             v_call, '2026-07-28', 'tools/call', NULL, -32020, 'Mcp-Name', true),
            ('other name',
             v_call, '2026-07-28', 'tools/call', 'other', -32020, 'Mcp-Name', true),
            ('base64 of another name',
             v_call, '2026-07-28', 'tools/call', '=?base64?b3RoZXI=?=', -32020, 'Mcp-Name', true),
            ('not base64',
             v_call, '2026-07-28', 'tools/call', '=?base64?***?=', -32020, 'Mcp-Name', true),
            ('base64 that is not UTF-8',
             v_call, '2026-07-28', 'tools/call', '=?base64?/w==?=', -32020, 'Mcp-Name', true),
            ('base64 with bad padding',
             v_call, '2026-07-28', 'tools/call', '=?base64?bGlzdF90YXNrcw?=', -32020, 'Mcp-Name', true),
            ('a header version the server does not speak',
             v_call, '2026-09-01', 'tools/call', 'list_tasks', -32022, NULL, true),
            ('no _meta',
             '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'::jsonb, '2026-07-28', 'tools/list', NULL,
             -32020, 'MCP-Protocol-Version', true),
            ('a batch',
             jsonb_build_array(v_call), '2026-07-28', 'tools/list', NULL, -32020, 'Mcp-Method', true),
            ('legacy header, modern body',
             v_call, '2025-11-25', 'tools/call', 'list_tasks', -32020, 'MCP-Protocol-Version', true),
            ('no header, modern body',
             v_call, NULL, NULL, NULL, -32020, 'MCP-Protocol-Version', true),
            ('a header that is not a date',
             v_call, 'draft', NULL, NULL, -32022, NULL, true),
            ('an unknown pre-2026 header',
             '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'::jsonb, '2023-01-01', NULL, NULL, -32022, NULL, true),
            ('an unsupported version in header and _meta',
             jsonb_set(v_call, '{params,_meta,io.modelcontextprotocol/protocolVersion}', '"2099-01-01"'),
             '2099-01-01', 'tools/call', 'list_tasks', -32022, NULL, true),
            ('no clientCapabilities',
             v_call #- '{params,_meta,io.modelcontextprotocol/clientCapabilities}',
             '2026-07-28', 'tools/call', 'list_tasks', -32602, NULL, true),
            ('server/discover needs no clientCapabilities',
             '{"jsonrpc":"2.0","id":1,"method":"server/discover",
               "params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}'::jsonb,
             '2026-07-28', 'server/discover', NULL, NULL, NULL, true),
            ('resources/read names its uri',
             jsonb_build_object('jsonrpc', '2.0', 'id', 1, 'method', 'resources/read',
                                'params', jsonb_build_object('uri', 'note://a', '_meta', v_meta)),
             '2026-07-28', 'resources/read', 'note://a', NULL, NULL, true),
            ('resources/read under another uri',
             jsonb_build_object('jsonrpc', '2.0', 'id', 1, 'method', 'resources/read',
                                'params', jsonb_build_object('uri', 'note://a', '_meta', v_meta)),
             '2026-07-28', 'resources/read', 'note://b', -32020, 'Mcp-Name', true),
            ('prompts/get without its name',
             jsonb_build_object('jsonrpc', '2.0', 'id', 1, 'method', 'prompts/get',
                                'params', jsonb_build_object('name', 'p', '_meta', v_meta)),
             '2026-07-28', 'prompts/get', NULL, -32020, 'Mcp-Name', true),
            ('a notification',
             '{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}'::jsonb,
             '2026-07-28', 'notifications/cancelled', NULL, NULL, NULL, true),
            ('a notification without its method',
             '{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}'::jsonb,
             '2026-07-28', NULL, NULL, -32020, 'Mcp-Method', true),
            ('a legacy request',
             '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_tasks"}}'::jsonb,
             '2025-11-25', NULL, NULL, NULL, NULL, false),
            ('a legacy request without a header',
             '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_tasks"}}'::jsonb,
             NULL, NULL, NULL, NULL, NULL, false)
        ) AS c(label, request, protocol_version, mcp_method, mcp_name, code, header, modern)
    LOOP
        SELECT * INTO STRICT v_row
        FROM api.mcp_request_policy(v_case.request::text, NULL, v_case.protocol_version, v_case.mcp_method, v_case.mcp_name);

        IF (v_row.refusal->'error'->>'code')::int IS DISTINCT FROM v_case.code
           OR v_row.refusal->'error'->'data'->>'header' IS DISTINCT FROM v_case.header
           OR v_row.modern IS DISTINCT FROM v_case.modern THEN
            RAISE EXCEPTION 'TEST FAILED: %: want code %, header %, modern %; got modern %, refusal %',
                v_case.label, v_case.code, v_case.header, v_case.modern, v_row.modern, v_row.refusal;
        END IF;
        IF v_row.refusal IS NOT NULL
           AND (v_row.refusal->>'jsonrpc' IS DISTINCT FROM '2.0'
                OR v_row.refusal->'id' IS DISTINCT FROM COALESCE(v_case.request->'id', 'null')) THEN
            RAISE EXCEPTION 'TEST FAILED: %: the refusal must echo the request id, got %', v_case.label, v_row.refusal;
        END IF;
        v_count := v_count + 1;
    END LOOP;
    IF v_count IS DISTINCT FROM 28 THEN
        RAISE EXCEPTION 'TEST FAILED: expected 28 cases, ran %', v_count;
    END IF;

    -- -32022 names what the server speaks: every version for an unknown header,
    -- the 2026-07-28 era only for an unsupported _meta version.
    SELECT refusal INTO STRICT v_row
    FROM api.mcp_request_policy('{"jsonrpc":"2.0","id":5,"method":"tools/list"}', NULL, '2023-01-01', NULL, NULL);
    IF v_row.refusal->'error'->'data' IS DISTINCT FROM jsonb_build_object(
           'supported', to_jsonb(api.mcp_legacy_versions() || api.mcp_supported_versions()),
           'requested', '2023-01-01') THEN
        RAISE EXCEPTION 'TEST FAILED: an unknown header must list every version, got %', v_row.refusal;
    END IF;
    SELECT refusal INTO STRICT v_row
    FROM api.mcp_request_policy(jsonb_set(v_call, '{params,_meta,io.modelcontextprotocol/protocolVersion}', '"2099-01-01"')::text,
                                NULL, '2099-01-01', 'tools/call', 'list_tasks');
    IF v_row.refusal->'error'->'data' IS DISTINCT FROM '{"supported": ["2026-07-28"], "requested": "2099-01-01"}'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: an unsupported _meta version must list the 2026-07-28 era, got %', v_row.refusal;
    END IF;

    -- Legacy initialize negotiates from the same list.
    IF (api.mcp_initialize('{"protocolVersion":"1999-01-01"}', '1')).envelope->'result'->>'protocolVersion'
       IS DISTINCT FROM (api.mcp_legacy_versions())[cardinality(api.mcp_legacy_versions())] THEN
        RAISE EXCEPTION 'TEST FAILED: initialize must negotiate the newest of api.mcp_legacy_versions()';
    END IF;

    RAISE DEBUG '  + -32020 with data.header, -32022 with supported and requested, -32602, the era';
END $$;

-- resources/read routes by params.uri alone, as the dispatcher does: a stray
-- params.name must not send the policy to another handler, or to none.
DO $$
DECLARE
    v_row  record;
    v_text text;
BEGIN
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0719-4000-8000-000000000001', 'type', 'resource', 'name', 'policy_doc',
            'description', 'A document read at serializable', 'uriTemplate', 'policydoc://{id}',
            'access', 'public', 'minTransactionIsolation', 'serializable'),
        $b$BEGIN RETURN api.mcp_resource_result('[]', (request).request_id); END;$b$);

    SELECT * INTO STRICT v_row FROM api.mcp_request_policy(
        '{"jsonrpc": "2.0", "id": 1, "method": "resources/read", "params": {"uri": "policydoc://7", "name": "x"}}',
        NULL, NULL, NULL, NULL);
    IF v_row.transaction_isolation IS DISTINCT FROM 'serializable' OR v_row.handler_name IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: resources/read with a stray name must resolve the uri''s policy, got % %',
            v_row.transaction_isolation, v_row.handler_name;
    END IF;

    FOREACH v_text IN ARRAY ARRAY['{"jsonrpc": "2.0", "id": "a\u0000b", "method": "ping"}',
                                  '{"jsonrpc": "2.0", "id": "a\ud800b", "method": "ping"}', 'not json']
    LOOP
        SELECT * INTO STRICT v_row FROM api.mcp_request_policy(v_text, NULL, NULL, NULL, NULL);
        IF (v_row.refusal->'error'->>'code')::int IS DISTINCT FROM -32700 OR v_row.refusal->'id' IS DISTINCT FROM 'null'::jsonb THEN
            RAISE EXCEPTION 'TEST FAILED: a body jsonb refuses (%) must be a -32700 refusal, got %', v_text, v_row.refusal;
        END IF;
    END LOOP;
END $$;
