-- ============================================================================
-- Test: only a JSON-RPC 2.0 request object for the route's method runs it
-- ============================================================================
-- An empty body, a batch, a request without "jsonrpc": "2.0", without a
-- string method, with an object or array id, or naming another method is
-- -32600 Invalid Request, also when the route is resolved from the body as
-- the gateway does, and the handler never runs.
-- ============================================================================

DO $$
DECLARE
    v_body     text;
    v_response api.http_response;
BEGIN
    CREATE TEMP TABLE rpc_envelope_probe (ran int);

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object('id', 'ffffffff-0717-4000-8000-000000000001', 'methodName', 'test.envelope',
            'access', 'public'),
        $b$
BEGIN
    INSERT INTO pg_temp.rpc_envelope_probe VALUES (1);
    RETURN api.jsonrpc_success(jsonb_build_object('ran', true), api.content_json((request).content)->'id');
END;
$b$);

    FOREACH v_body IN ARRAY ARRAY[NULL, '[1, 2]', '{"id": 7, "method": "test.envelope"}',
                                  '{"jsonrpc": "1.0", "id": 7, "method": "test.envelope"}',
                                  '{"jsonrpc": "2.0", "id": 7, "method": "other.method"}',
                                  '{"jsonrpc": "2.0", "id": 7}']
    LOOP
        v_response := api.rpc_invoke(api.rpc_resolve('test.envelope'), ''::extensions.hstore, convert_to(v_body, 'UTF8'));
        IF (v_response).status_code IS DISTINCT FROM 400
           OR (api.content_json((v_response).content)->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
            RAISE EXCEPTION 'TEST FAILED: body % must be 400 / -32600, got % %', coalesce(v_body, 'NULL'),
                (v_response).status_code, api.content_json((v_response).content);
        END IF;
    END LOOP;
    FOREACH v_body IN ARRAY ARRAY[NULL, '[{"jsonrpc": "2.0", "id": 1, "method": "test.envelope"}]',
                                  '{"jsonrpc": "2.0", "id": 1}', '{"jsonrpc": "2.0", "id": 1, "method": 1}',
                                  '{"jsonrpc": "2.0", "id": {"a": 1}, "method": "test.envelope"}',
                                  '{"jsonrpc": "2.0", "id": [1], "method": "test.envelope"}']
    LOOP
        v_response := api.rpc_invoke(api.rpc_resolve(coalesce(
                CASE WHEN jsonb_typeof(v_body::jsonb) = 'object' THEN v_body::jsonb->>'method' END, '')),
            ''::extensions.hstore, convert_to(v_body, 'UTF8'));
        IF (v_response).status_code IS DISTINCT FROM 400
           OR (api.content_json((v_response).content)->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
            RAISE EXCEPTION 'TEST FAILED: body % resolved as the gateway does must be 400 / -32600, got % %', coalesce(v_body, 'NULL'),
                (v_response).status_code, api.content_json((v_response).content);
        END IF;
    END LOOP;
    IF EXISTS (SELECT FROM pg_temp.rpc_envelope_probe) THEN
        RAISE EXCEPTION 'TEST FAILED: the handler ran on an invalid request';
    END IF;

    v_response := api.rpc_invoke(api.rpc_resolve(''), ''::extensions.hstore, ''::bytea);
    IF (v_response).status_code IS DISTINCT FROM 400
       OR (api.content_json((v_response).content)->'error'->>'code')::int IS DISTINCT FROM -32700 THEN
        RAISE EXCEPTION 'TEST FAILED: an empty body is not JSON: 400 / -32700, got % %',
            (v_response).status_code, api.content_json((v_response).content);
    END IF;

    v_response := api.rpc_invoke(api.rpc_resolve('no.such.method'), ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "id": 7, "method": "no.such.method"}', 'UTF8'));
    IF (v_response).status_code IS DISTINCT FROM 404
       OR (api.content_json((v_response).content)->'error'->>'code')::int IS DISTINCT FROM -32601 THEN
        RAISE EXCEPTION 'TEST FAILED: a valid request for an unknown method must be 404 / -32601, got % %',
            (v_response).status_code, api.content_json((v_response).content);
    END IF;

    v_response := api.rpc_invoke(api.rpc_resolve('test.envelope'), ''::extensions.hstore,
        convert_to('{"jsonrpc": "2.0", "id": 7, "method": "test.envelope"}', 'UTF8'));
    IF (v_response).status_code IS DISTINCT FROM 200
       OR api.content_json((v_response).content)->'result' IS DISTINCT FROM '{"ran": true}'::jsonb
       OR api.content_json((v_response).content)->'id' IS DISTINCT FROM '7'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a well-formed request must answer 200 with its result, got %', api.content_json((v_response).content);
    END IF;

    DROP TABLE pg_temp.rpc_envelope_probe;
    RAISE NOTICE '  + JSON-RPC: only a 2.0 request object for the route''s method runs it';
END $$;
