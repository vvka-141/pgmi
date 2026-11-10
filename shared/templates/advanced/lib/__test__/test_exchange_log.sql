-- ============================================================================
-- Test: the exchange log finds a request by its id, stores its context, and
-- replays it as its caller inside a rollback
-- ============================================================================
-- A logged request keeps no credential, so replay cannot re-present one: the
-- gateway stores what internal.request_log_context() returns, and
-- api.replay_exchange applies it (internal.apply_request_log_context), runs
-- the request through the same dispatch and rolls back everything it did.
-- ============================================================================

DO $$
DECLARE
    v_owner     uuid;
    v_org       uuid;
    v_headers   extensions.hstore;
    v_rid       text := md5(random()::text);
    v_response  api.http_response;
    v_exchange  api.vw_exchange;
    v_body      jsonb;
    v_rows      bigint;
    v_before    text;
BEGIN
    RAISE NOTICE '-> Testing the exchange log: request id, context, replay';

    v_owner := pg_temp.test_login('test', 'replay-owner', 'replay-owner@example.test', NULL, true);
    v_org := (membership.fn_create_organization_with_owner('Replay', 'replay-org', v_owner)).object_id;

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-e001-4000-8000-000000000001',
            'path', '/replay-probe',
            'httpMethod', '^POST$',
            'name', 'replay_probe',
            'access', 'authenticated',
            'autoLog', true
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object(
        'principalId', api.current_principal_id(),
        'organizationId', api.current_organization_id()));
END;
$body$
    );
    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-e001-4000-8000-000000000002',
            'methodName', 'replay.probe',
            'access', 'authenticated',
            'autoLog', true
        ),
        $body$
BEGIN
    RETURN api.jsonrpc_success(jsonb_build_object('principalId', api.current_principal_id()),
                               api.content_json((request).content)->'id');
END;
$body$
    );

    v_headers := pg_temp.test_session_headers('test|replay-owner', v_org);
    v_response := api.rest_invoke('POST', '/replay-probe',
        v_headers || extensions.hstore(ARRAY['x-request-id', v_rid, 'referer', 'https://app.test/invite?code=SECRET']),
        NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: the probe answered %', (v_response).status_code;
    END IF;

    SELECT * INTO v_exchange FROM api.vw_exchange WHERE request_id = v_rid;
    IF NOT FOUND OR v_exchange.protocol IS DISTINCT FROM 'rest' OR v_exchange.response_status IS DISTINCT FROM 200
       OR v_exchange.handler_object_id IS DISTINCT FROM 'ffffffff-e001-4000-8000-000000000001' THEN
        RAISE EXCEPTION 'TEST FAILED: api.vw_exchange must find the REST exchange by its x-request-id, got %', v_exchange;
    END IF;
    IF v_exchange.context IS DISTINCT FROM jsonb_build_object('principalId', v_owner, 'actualPrincipalId', v_owner,
                                                              'actorKind', 'human', 'organizationId', v_org) THEN
        RAISE EXCEPTION 'TEST FAILED: the logged context must be both principals, the actor kind and the organization, got %', v_exchange.context;
    END IF;
    IF EXISTS (SELECT FROM api.rest_exchange WHERE object_id = v_exchange.exchange_id
               AND ((request).headers ?| ARRAY['x-login-session', 'x-organization-session', 'referer'])) THEN
        RAISE EXCEPTION 'TEST FAILED: a session handle or the referer was logged';
    END IF;

    -- Replay acts as the logged caller although the logged headers carry no
    -- session, and leaves nothing behind: no new exchange row, no context.
    PERFORM pg_temp.test_act_as(NULL);
    v_before := current_setting('auth.principal_id', true);
    SELECT count(*) INTO v_rows FROM api.rest_exchange WHERE handler_object_id = 'ffffffff-e001-4000-8000-000000000001';

    v_response := api.replay_exchange(v_exchange.exchange_id);
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 200
       OR v_body IS DISTINCT FROM jsonb_build_object('principalId', v_owner, 'organizationId', v_org) THEN
        RAISE EXCEPTION 'TEST FAILED: replay must answer as the logged caller, got % %', (v_response).status_code, v_body;
    END IF;
    IF (SELECT count(*) FROM api.rest_exchange WHERE handler_object_id = 'ffffffff-e001-4000-8000-000000000001') IS DISTINCT FROM v_rows THEN
        RAISE EXCEPTION 'TEST FAILED: replay must roll back its own exchange row';
    END IF;
    IF current_setting('auth.principal_id', true) IS DISTINCT FROM v_before THEN
        RAISE EXCEPTION 'TEST FAILED: replay must roll back the context it applied';
    END IF;

    -- RPC: found by the request id, replayed as its caller.
    v_rid := md5(random()::text);
    PERFORM api.rpc_invoke(api.rpc_resolve('replay.probe'),
        v_headers || extensions.hstore('x-request-id', v_rid),
        convert_to('{"jsonrpc":"2.0","method":"replay.probe","id":7}', 'UTF8'));
    SELECT * INTO v_exchange FROM api.vw_exchange WHERE request_id = v_rid;
    IF NOT FOUND OR v_exchange.protocol IS DISTINCT FROM 'rpc' THEN
        RAISE EXCEPTION 'TEST FAILED: api.vw_exchange must find the RPC exchange by its x-request-id';
    END IF;
    PERFORM pg_temp.test_act_as(NULL);
    v_body := api.content_json((api.replay_exchange(v_exchange.exchange_id)).content);
    IF v_body->'result'->>'principalId' IS DISTINCT FROM v_owner::text OR v_body->'id' IS DISTINCT FROM '7'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: RPC replay must answer as the logged caller, got %', v_body;
    END IF;

    -- The admin endpoint replays the same way; the gateway role cannot call
    -- api.replay_exchange at all.
    PERFORM pg_temp.test_operator(pg_temp.test_login('test', 'replay-operator', 'replay-operator@example.test', NULL, true));
    v_response := api.rest_invoke('POST', '/admin/exchanges/' || v_exchange.exchange_id || '/replay',
        pg_temp.test_session_headers('test|replay-operator', NULL, ARRAY['mfa']), NULL::bytea);
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 200 OR (v_body->>'status')::int IS DISTINCT FROM 200
       OR (v_body->>'body')::jsonb->'result'->>'principalId' IS DISTINCT FROM v_owner::text THEN
        RAISE EXCEPTION 'TEST FAILED: POST /admin/exchanges/{id}/replay must answer the replayed response, got % %',
            (v_response).status_code, v_body;
    END IF;
    v_response := api.rest_invoke('POST', '/admin/exchanges/' || gen_random_uuid() || '/replay',
        pg_temp.test_session_headers('test|replay-operator', NULL, ARRAY['mfa']), NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION 'TEST FAILED: replaying an unknown exchange must answer 404, got %', (v_response).status_code;
    END IF;
    IF has_function_privilege(pg_temp.deployment_setting('database_api_role'), 'api.replay_exchange(uuid)', 'EXECUTE') THEN
        RAISE EXCEPTION 'TEST FAILED: the gateway role must not execute api.replay_exchange';
    END IF;
END $$;

-- MCP: a tool error (result.isError) counts as an error in every exchange
-- view, and replay runs a logged tool call as its caller and keeps nothing.
DO $$
DECLARE
    v_owner     uuid := pg_temp.test_login('test', 'replay-mcp', 'replay-mcp@example.test', NULL, true);
    v_org       uuid;
    v_context   jsonb;
    v_exchange  uuid;
    v_envelope  jsonb;
    v_rows      bigint;
    v_logged    bigint;
    v_errors    bigint;
BEGIN
    PERFORM pg_temp.test_act_as(NULL);
    v_org := (membership.fn_create_organization_with_owner('Replay MCP', 'replay-mcp-org', v_owner)).object_id;
    IF v_org IS NULL THEN
        RAISE EXCEPTION 'TEST SETUP: the organization was not created';
    END IF;
    CREATE TABLE core.replay_mcp_probe (principal_id uuid);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'f6410000-0000-4000-8000-000000000001', 'type', 'tool', 'effect', 'write', 'reversible', true,
        'name', 'replay_mcp_probe', 'description', 'Records who called it', 'access', 'authenticated'),
        $body$
BEGIN
    INSERT INTO core.replay_mcp_probe VALUES (api.current_principal_id());
    RETURN api.mcp_tool_result('[]', (request).request_id, false,
                               jsonb_build_object('principalId', api.current_principal_id()));
END;
$body$);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'f6410000-0000-4000-8000-000000000002', 'type', 'tool', 'effect', 'read',
        'name', 'replay_mcp_failing', 'description', 'Always fails', 'access', 'public'),
        $body$ BEGIN RAISE EXCEPTION 'probe failure'; END; $body$);

    SELECT error_count INTO v_errors FROM api.vw_exchange_summary;
    v_envelope := (api.mcp_call_tool('replay_mcp_failing', '{}', NULL, '"f1"')).envelope;
    SELECT object_id INTO v_exchange FROM api.mcp_exchange WHERE mcp_name = 'replay_mcp_failing'
    ORDER BY sequence_number DESC LIMIT 1;
    IF v_envelope->'result'->'isError' IS DISTINCT FROM 'true'::jsonb
       OR (SELECT is_error FROM api.vw_mcp_exchange_info WHERE exchange_id = v_exchange) IS NOT TRUE
       OR (SELECT error_count FROM api.vw_exchange_summary) IS DISTINCT FROM v_errors + 1
       OR NOT EXISTS (SELECT FROM api.vw_exchange_stats WHERE protocol = 'mcp_tool' AND is_error
                      AND _grp_protocol = 0 AND _grp_error = 0 AND exchange_count > 0) THEN
        RAISE EXCEPTION 'TEST FAILED: a tool answer with isError must count as an error in every exchange view, got %', v_envelope;
    END IF;

    v_context := pg_temp.test_mcp_context('test|replay-mcp', v_org);
    v_envelope := (api.mcp_call_tool('replay_mcp_probe', '{}', v_context, '"m1"')).envelope;
    IF coalesce(v_envelope::text, '') NOT LIKE '%' || v_owner || '%' THEN
        RAISE EXCEPTION 'TEST FAILED: the probe tool must answer as its caller, got %', v_envelope;
    END IF;
    SELECT object_id INTO v_exchange FROM api.mcp_exchange WHERE mcp_name = 'replay_mcp_probe'
    ORDER BY sequence_number DESC LIMIT 1;
    IF (SELECT replay_sql FROM api.vw_mcp_exchange_info WHERE exchange_id = v_exchange)
       IS DISTINCT FROM format('SELECT * FROM api.replay_exchange(%L);', v_exchange) THEN
        RAISE EXCEPTION 'TEST FAILED: the MCP view''s replay_sql must call api.replay_exchange';
    END IF;

    PERFORM pg_temp.test_act_as(NULL);
    SELECT count(*) INTO v_rows FROM core.replay_mcp_probe;
    SELECT count(*) INTO v_logged FROM api.mcp_exchange;
    v_envelope := api.content_json((api.replay_exchange(v_exchange)).content);
    IF v_envelope->>'id' IS DISTINCT FROM 'm1' OR coalesce(v_envelope::text, '') NOT LIKE '%' || v_owner || '%'
       OR v_envelope->'result'->'isError' IS DISTINCT FROM 'false'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: MCP replay must answer as the logged caller, got %', v_envelope;
    END IF;
    IF (SELECT count(*) FROM core.replay_mcp_probe) IS DISTINCT FROM v_rows
       OR (SELECT count(*) FROM api.mcp_exchange) IS DISTINCT FROM v_logged THEN
        RAISE EXCEPTION 'TEST FAILED: MCP replay must roll back its writes and its own exchange row';
    END IF;
    RAISE NOTICE '  ✓ MCP: tool errors count as errors; replay runs as the caller and keeps nothing';
END $$;

-- An application that keeps more context replaces internal.request_log_context;
-- the gateway stores whatever it returns.
DO $$
DECLARE
    v_context jsonb;
BEGIN
    RAISE NOTICE '-> Testing an overridden internal.request_log_context() is stored';

    CREATE OR REPLACE FUNCTION internal.request_log_context()
    RETURNS jsonb LANGUAGE sql STABLE AS $f$ SELECT '{"principalId": null, "tenantHint": "replay-override"}'::jsonb $f$;

    PERFORM api.rest_invoke('POST', '/replay-probe', pg_temp.test_session_headers('test|replay-owner'), NULL::bytea);
    SELECT context INTO v_context FROM api.rest_exchange
    WHERE handler_object_id = 'ffffffff-e001-4000-8000-000000000001'
    ORDER BY sequence_number DESC LIMIT 1;
    IF v_context->>'tenantHint' IS DISTINCT FROM 'replay-override' THEN
        RAISE EXCEPTION 'TEST FAILED: the overridden request_log_context() must be stored, got %', v_context;
    END IF;
END $$;

-- The redacted-header list is extendable: an application header named by
-- internal.application_unloggable_headers() is dropped like a credential.
DO $$
DECLARE
    v_rid text := md5(random()::text);
BEGIN
    RAISE NOTICE '-> Testing the application can extend the redacted headers';

    CREATE OR REPLACE FUNCTION internal.application_unloggable_headers()
    RETURNS text[] LANGUAGE sql STABLE AS $f$ SELECT '{x-device-secret}'::text[] $f$;

    PERFORM api.rest_invoke('POST', '/replay-probe',
        pg_temp.test_session_headers('test|replay-owner')
            || extensions.hstore(ARRAY['X-Device-Secret', 'SECRET', 'x-request-id', v_rid]), NULL::bytea);
    IF NOT EXISTS (SELECT FROM api.rest_exchange WHERE (request).headers OPERATOR(extensions.->) 'x-request-id' = v_rid) THEN
        RAISE EXCEPTION 'TEST FAILED: the probe call was not logged';
    END IF;
    IF EXISTS (SELECT FROM api.rest_exchange WHERE (request).headers OPERATOR(extensions.->) 'x-request-id' = v_rid
               AND strpos((request).headers::text, 'SECRET') > 0) THEN
        RAISE EXCEPTION 'TEST FAILED: a header the application named unloggable was logged';
    END IF;
END $$;

DO $$
DECLARE
    v_left text;
BEGIN
    SELECT string_agg(name, ', ') INTO v_left FROM (
        SELECT c.oid::regclass::text AS name FROM pg_class c WHERE c.relname LIKE '%inbound_queue%'
        UNION ALL
        SELECT p.oid::regprocedure::text FROM pg_proc p WHERE p.proname LIKE '%inbound_queue%'
        UNION ALL
        SELECT t.oid::regtype::text FROM pg_type t WHERE t.typname LIKE '%inbound_queue%') o;
    IF v_left IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: objects still named inbound_queue: %', v_left;
    END IF;
END $$;
