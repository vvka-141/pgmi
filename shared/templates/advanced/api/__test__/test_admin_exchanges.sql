-- ============================================================================
-- Test: GET /admin/exchanges filtered by the actual principal
-- ============================================================================
-- ?actualPrincipalId= answers only the exchanges that principal made (an
-- agent's, in this template), newest first, in any spelling of the uuid;
-- without it the page is the whole log, as before. Every row fits the route's
-- outputSchema, an MCP row (no status) and an RPC row whose route is gone too.
-- ============================================================================

DO $$
DECLARE
    v_agent    uuid := gen_random_uuid();
    v_other    uuid := gen_random_uuid();
    v_rest     uuid;
    v_rpc      uuid;
    v_mcp      uuid;
    v_newest   uuid;
    v_headers  extensions.hstore;
    v_response api.http_response;
    v_body     jsonb;
    v_total    bigint;
    v_problem  text;
BEGIN
    RAISE DEBUG '-> GET /admin/exchanges?actualPrincipalId=';

    INSERT INTO api.rest_exchange (handler_object_id, enqueued_at, context, request, response, completed_at)
    VALUES (gen_random_uuid(), now() + interval '3 hours',
            jsonb_build_object('principalId', v_other, 'actualPrincipalId', v_agent),
            ROW('GET', '/by-agent', ''::extensions.hstore, null::bytea, '{}'::jsonb)::api.rest_request,
            ROW(200, ''::extensions.hstore, null::bytea)::api.http_response, now() + interval '3 hours')
    RETURNING object_id INTO v_rest;

    INSERT INTO api.rpc_exchange (handler_object_id, enqueued_at, context, request, response, completed_at)
    VALUES (gen_random_uuid(), now() + interval '2 hours', jsonb_build_object('actualPrincipalId', v_agent),
            ROW(null::uuid, ''::extensions.hstore, null::bytea)::api.rpc_request,
            ROW(200, ''::extensions.hstore, null::bytea)::api.http_response, now() + interval '2 hours')
    RETURNING object_id INTO v_rpc;

    INSERT INTO api.mcp_exchange (handler_object_id, enqueued_at, context, mcp_type, mcp_name, request, response)
    VALUES (gen_random_uuid(), now() + interval '1 hour', jsonb_build_object('actualPrincipalId', v_agent),
            'tool', 'by_agent', ROW('{}'::jsonb, null, '{}'::jsonb, '1'::jsonb)::api.mcp_request,
            ROW('{}'::jsonb)::api.mcp_response)
    RETURNING object_id INTO v_mcp;

    INSERT INTO api.rest_exchange (handler_object_id, enqueued_at, context, request, response, completed_at)
    VALUES (gen_random_uuid(), now() + interval '4 hours',
            jsonb_build_object('principalId', v_agent, 'actualPrincipalId', v_other),
            ROW('GET', '/by-other', ''::extensions.hstore, null::bytea, '{}'::jsonb)::api.rest_request,
            ROW(200, ''::extensions.hstore, null::bytea)::api.http_response, now() + interval '4 hours')
    RETURNING object_id INTO v_newest;

    PERFORM pg_temp.test_operator(pg_temp.test_login('google', 'exchanges-operator', 'exchanges-operator@acme.com', NULL, true));
    v_headers := pg_temp.test_session_headers('google|exchanges-operator', NULL, ARRAY['mfa']);

    v_response := api.rest_invoke('GET', '/admin/exchanges?actualPrincipalId=' || upper(v_agent::text), v_headers, NULL::bytea);
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 200
       OR jsonb_path_query_array(v_body, '$.items[*].exchangeId') IS DISTINCT FROM jsonb_build_array(v_rest, v_rpc, v_mcp)
       OR (v_body->>'total')::int IS DISTINCT FROM 3 THEN
        RAISE EXCEPTION 'TEST FAILED: ?actualPrincipalId= must answer only that principal''s 3 exchanges, newest first, got % %',
            (v_response).status_code, v_body;
    END IF;

    v_problem := internal.json_schema_errors(
        internal.resolved_schema((SELECT h.output_json_schema::jsonb
                                  FROM api.rest_route r JOIN api.handler h ON h.object_id = r.handler_object_id
                                  WHERE r.route_name = 'admin_exchanges')),
        v_body);
    IF v_problem IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a REST row, an RPC row whose route is gone and an MCP row without a status must fit the outputSchema: %', v_problem;
    END IF;
    IF jsonb_path_query_first(v_body, '$.items[*] ? (@.protocol == "mcp").isError') IS DISTINCT FROM 'false'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: an MCP result without isError must list isError false, got %', v_body;
    END IF;

    v_response := api.rest_invoke('GET', '/admin/exchanges?protocol=rpc&actualPrincipalId=' || v_agent, v_headers, NULL::bytea);
    v_body := api.content_json((v_response).content);
    IF jsonb_path_query_array(v_body, '$.items[*].exchangeId') IS DISTINCT FROM jsonb_build_array(v_rpc)
       OR (v_body->>'total')::int IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'TEST FAILED: ?protocol=rpc&actualPrincipalId= must answer the one rpc exchange, got %', v_body;
    END IF;

    v_response := api.rest_invoke('GET', '/admin/exchanges?actualPrincipalId=' || gen_random_uuid(), v_headers, NULL::bytea);
    v_body := api.content_json((v_response).content);
    IF v_body->'items' IS DISTINCT FROM '[]'::jsonb OR (v_body->>'total')::int IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'TEST FAILED: an actualPrincipalId with no exchanges must answer an empty page, got %', v_body;
    END IF;

    v_response := api.rest_invoke('GET', '/admin/exchanges?actualPrincipalId=not-a-uuid', v_headers, NULL::bytea);
    PERFORM pg_temp.expect_status('a malformed actualPrincipalId must be the gateway''s 400', v_response, 400);

    v_total := (SELECT count(*) FROM api.vw_exchange);
    v_response := api.rest_invoke('GET', '/admin/exchanges?limit=4&actualPrincipalId=', v_headers, NULL::bytea);
    v_body := api.content_json((v_response).content);
    IF jsonb_path_query_array(v_body, '$.items[*].exchangeId') IS DISTINCT FROM jsonb_build_array(v_newest, v_rest, v_rpc, v_mcp)
       OR (v_body->>'total')::bigint IS DISTINCT FROM v_total THEN
        RAISE EXCEPTION 'TEST FAILED: without actualPrincipalId the page must be the whole log, newest first, got %', v_body;
    END IF;

    RAISE DEBUG '  + ?actualPrincipalId= pages one principal''s exchanges, newest first';
END $$;
