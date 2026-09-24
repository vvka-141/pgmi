-- ============================================================================
-- Test: exchange logs never store credentials
-- ============================================================================
-- rest_exchange / rpc_exchange keep the request and response for replay and
-- audit. A bearer token, raw API key or session cookie persisted there is a
-- live credential readable by anyone with log access, so the logged copy must
-- drop them -- on the success path and on the error path, in any header case.
-- ============================================================================

DO $$
DECLARE
    v_headers extensions.hstore := extensions.hstore(ARRAY[
        'authorization', 'Bearer pgmi_abcdef123456_secret',
        'Cookie', 'session=SECRET',
        'X-Api-Key', 'pgmi_raw_key',
        'proxy-authorization', 'Basic c2VjcmV0',
        'x-trace-id', 'keep-me'
    ]);
    v_logged extensions.hstore;
    v_route_id uuid;
BEGIN
    RAISE NOTICE '→ Testing that exchange logs drop credential headers';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-c001-4000-8000-000000000001',
            'uri', '^/redaction-ok$',
            'httpMethod', '^GET$',
            'name', 'redaction_ok',
            'requiresAuth', false,
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
            'uri', '^/redaction-fail$',
            'httpMethod', '^GET$',
            'name', 'redaction_fail',
            'requiresAuth', false,
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
            'requiresAuth', false,
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
                   WHERE lower(h.key) IN ('authorization', 'cookie', 'x-api-key', 'proxy-authorization')) THEN
            RAISE EXCEPTION 'TEST FAILED: exchange log stored credential headers: %', v_logged;
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
