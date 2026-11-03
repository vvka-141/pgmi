-- ============================================================================
-- Test: header names that collide after case-folding fail closed
-- ============================================================================
-- hstore keeps one of two keys that lowercase alike, and which one is
-- unspecified, so a client-sent X-Login-Session could win over the
-- transport's x-login-session. Both gateways answer 400 instead. The test
-- never asks which value would have won, only that the request is refused.

DO $$
DECLARE
    v_header   text;
    v_response api.http_response;
    v_route_id uuid;
BEGIN
    RAISE DEBUG '-> Header names that collide after case-folding';

    PERFORM api.create_or_replace_rpc_handler(jsonb_build_object(
        'id', 'ffffffff-ac02-4000-8000-000000000001', 'methodName', 'test.collision_probe', 'access', 'public'),
        $b$BEGIN RETURN api.jsonrpc_success('{}'::jsonb, api.content_json((request).content)->'id'); END;$b$);
    v_route_id := api.rpc_resolve('test.collision_probe');

    FOREACH v_header IN ARRAY ARRAY['x-login-session', 'x-organization-session'] LOOP
        v_response := api.rest_invoke('GET', '/health',
            extensions.hstore(ARRAY[v_header, 'transport-value', initcap(v_header), 'client-value']), NULL::bytea);
        IF (v_response).status_code IS DISTINCT FROM 400 THEN
            RAISE EXCEPTION 'TEST FAILED: REST with % in two casings answered %, want 400', v_header, (v_response).status_code;
        END IF;

        v_response := api.rpc_invoke(v_route_id,
            extensions.hstore(ARRAY[v_header, 'transport-value', upper(v_header), 'client-value']),
            convert_to('{"jsonrpc":"2.0","method":"test.collision_probe","id":1}', 'UTF8'));
        IF (v_response).status_code IS DISTINCT FROM 400
           OR (api.content_json((v_response).content)->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
            RAISE EXCEPTION 'TEST FAILED: RPC with % in two casings answered % %, want 400 -32600',
                v_header, (v_response).status_code, convert_from((v_response).content, 'UTF8');
        END IF;
    END LOOP;

    v_response := api.rest_invoke('GET', '/health', 'X-Trace-Id=>a, Accept=>application/json'::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: mixed-case names that do not collide must pass, got %', (v_response).status_code;
    END IF;

    RAISE DEBUG '  + REST and RPC answer 400 to case-colliding headers, the session headers included';
END $$;
