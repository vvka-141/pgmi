-- ============================================================================
-- Test: exchange logs never store credentials
-- ============================================================================
-- rest_exchange / rpc_exchange keep the request and response for replay and
-- audit. A bearer token, raw API key or session cookie persisted there is a
-- live credential readable by anyone with log access, so the logged copy must
-- drop them -- on the success path and on the error path, in any header case.
-- The Referer goes too: a page address can carry a one-time code.
-- ============================================================================

DO $$
DECLARE
    v_headers extensions.hstore := extensions.hstore(ARRAY[
        'authorization', 'Bearer pgmi_abcdef123456_secret',
        'Cookie', 'session=SECRET',
        'X-Api-Key', 'pgmi_raw_key',
        'proxy-authorization', 'Basic c2VjcmV0',
        'x-login-session', 'ls_SECRET',
        'x-organization-session', 'os_SECRET',
        'Referer', 'https://app.test/invite?code=SECRET',
        'X-Connection', 'cn_SECRET',
        'x-auth-token', 'SECRET',
        'X-Access-Token', 'SECRET',
        'x-refresh-token', 'SECRET',
        'X-CSRF-Token', 'SECRET',
        'x-xsrf-token', 'SECRET',
        'x-trace-id', 'keep-me'
    ]);
    v_logged extensions.hstore;
    v_route_id uuid;
BEGIN
    RAISE NOTICE '→ Testing that exchange logs drop credential headers';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-c001-4000-8000-000000000001',
            'path', '/redaction-ok',
            'httpMethod', '^GET$',
            'name', 'redaction_ok',
            'access', 'public',
            'autoLog', true
        ),
        $body$
BEGIN
    RETURN (200, extensions.hstore(ARRAY['content-type', 'application/json', 'set-cookie', 'session=ISSUED']),
            convert_to('{}', 'UTF8'))::api.http_response;
END;
$body$
    );

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-c001-4000-8000-000000000002',
            'path', '/redaction-fail',
            'httpMethod', '^GET$',
            'name', 'redaction_fail',
            'access', 'public',
            'autoLog', true
        ),
        $body$
BEGIN
    RAISE EXCEPTION 'deliberate failure';
END;
$body$
    );

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-c001-4000-8000-000000000003',
            'methodName', 'redaction.probe',
            'access', 'public',
            'autoLog', true
        ),
        $body$
BEGIN
    RETURN api.jsonrpc_success(jsonb_build_object('ok', true), '1'::jsonb);
END;
$body$
    );

    PERFORM api.rest_invoke('GET', '/redaction-ok', v_headers, NULL::bytea);
    PERFORM api.rest_invoke('GET', '/redaction-fail', v_headers, NULL::bytea);
    v_route_id := api.rpc_resolve('redaction.probe');
    PERFORM api.rpc_invoke(v_route_id, v_headers,
        convert_to('{"jsonrpc":"2.0","method":"redaction.probe","id":1}', 'UTF8'));

    FOR v_logged IN
        SELECT (request).headers FROM api.rest_exchange
        WHERE handler_object_id IN ('ffffffff-c001-4000-8000-000000000001', 'ffffffff-c001-4000-8000-000000000002')
        UNION ALL
        SELECT (request).headers FROM api.rpc_exchange
        WHERE handler_object_id = 'ffffffff-c001-4000-8000-000000000003'
    LOOP
        IF EXISTS (SELECT 1 FROM extensions.each(v_logged) h
                   WHERE lower(h.key) IN ('authorization', 'cookie', 'x-api-key', 'proxy-authorization',
                                          'x-login-session', 'x-organization-session', 'referer',
                                          'x-connection', 'x-auth-token', 'x-access-token',
                                          'x-refresh-token', 'x-csrf-token', 'x-xsrf-token')) THEN
            RAISE EXCEPTION 'TEST FAILED: exchange log stored credential headers: %', v_logged;
        END IF;
        IF strpos(v_logged::text, 'SECRET') > 0 THEN
            RAISE EXCEPTION 'TEST FAILED: a credential value reached the exchange log: %', v_logged;
        END IF;
        IF (v_logged -> 'x-trace-id') IS DISTINCT FROM 'keep-me' THEN
            RAISE EXCEPTION 'TEST FAILED: redaction dropped a non-credential header: %', v_logged;
        END IF;
    END LOOP;

    IF (SELECT count(*) FROM api.rest_exchange
        WHERE handler_object_id IN ('ffffffff-c001-4000-8000-000000000001', 'ffffffff-c001-4000-8000-000000000002'))
       + (SELECT count(*) FROM api.rpc_exchange
          WHERE handler_object_id = 'ffffffff-c001-4000-8000-000000000003') IS DISTINCT FROM 3 THEN
        RAISE EXCEPTION 'TEST FAILED: expected 3 logged exchanges (REST ok, REST error, RPC)';
    END IF;

    IF EXISTS (SELECT 1 FROM api.rest_exchange
               WHERE handler_object_id = 'ffffffff-c001-4000-8000-000000000001'
                 AND (response).headers ? 'set-cookie') THEN
        RAISE EXCEPTION 'TEST FAILED: exchange log stored the issued session cookie';
    END IF;

    RAISE NOTICE '  ✓ credentials are dropped from logged requests and responses, other headers kept';
END $$;


DO $$
DECLARE
    v_logged  jsonb;
    v_context jsonb;
BEGIN
    PERFORM pg_temp.test_act_as(NULL);
    v_context := pg_temp.test_mcp_context('test|redaction',
        (membership.fn_create_organization_with_owner('redaction', 'redaction',
            pg_temp.test_login('test', 'redaction', 'redaction@example.test'))).object_id);
    IF v_context->>'connection' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the fixture connection was not approved';
    END IF;
    RAISE NOTICE '→ Testing that the MCP exchange log drops credentials from the context';

    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'ffffffff-c001-4000-8000-000000000010', 'type', 'tool', 'effect', 'read', 'name', 'redaction_mcp_probe',
        'description', 'redaction probe', 'access', 'public', 'inputSchema', '{"type":"object"}'::jsonb),
        $body$BEGIN RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id); END;$body$);

    PERFORM api.mcp_call_tool('redaction_mcp_probe', '{}'::jsonb,
        v_context || '{"client": "redaction-probe", "loginSession": "ls_SECRET", "organizationSession": "os_SECRET", "token": "Bearer SECRET", "api_key": "pgmi_raw"}'::jsonb, '"r-1"'::jsonb);

    SELECT to_jsonb((request).context) INTO v_logged
    FROM api.mcp_exchange WHERE mcp_name = 'redaction_mcp_probe'
    ORDER BY enqueued_at DESC LIMIT 1;

    IF v_logged IS NULL OR v_logged ?| ARRAY['token', 'api_key', 'connection', 'loginSession', 'organizationSession'] THEN
        RAISE EXCEPTION 'TEST FAILED: the MCP exchange log kept a credential from the context: %', v_logged;
    END IF;
    IF v_logged->>'client' IS DISTINCT FROM 'redaction-probe' THEN
        RAISE EXCEPTION 'TEST FAILED: the MCP exchange log dropped a non-credential key, got %', v_logged;
    END IF;

    RAISE NOTICE '  ✓ MCP exchange log keeps other keys and drops connection and session handles, tokens and keys';
END $$;
