-- ============================================================================
-- Test: MCP identity through approved connections
-- ============================================================================
-- An MCP caller is an existing person acting through a connection that
-- person approved on the web, in the organization they were working in. The
-- MCP context's other keys never count: not an identity, not an email, not
-- an organization, not a login or organization session. Nothing on the MCP
-- path provisions anyone. A handle that names no live connection (unknown,
-- revoked, expired, a disabled person, an ended membership) gives no identity
-- and answers -31001 pgmi.connection_unknown, which the gateway sends as 401.
-- ============================================================================

CREATE FUNCTION pg_temp.mcp_whoami(p_context jsonb)
RETURNS jsonb LANGUAGE sql AS $$
    SELECT (api.mcp_call_tool('mi_whoami', '{}'::jsonb, p_context, '"mi"'::jsonb)).envelope;
$$;

-- The data.code of the -31001 a context that cannot act answers, else NULL.
CREATE FUNCTION pg_temp.mcp_refusal(p_envelope jsonb)
RETURNS text LANGUAGE sql AS $$
    SELECT CASE WHEN (p_envelope->'error'->>'code')::int = -31001 THEN p_envelope->'error'->'data'->>'code' END;
$$;

CREATE FUNCTION pg_temp.mcp_identity_count()
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_count  bigint;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    SELECT (SELECT count(*) FROM membership.principal) + (SELECT count(*) FROM membership.external_identity)
         + (SELECT count(*) FROM membership.login_session) + (SELECT count(*) FROM membership.connection)
    INTO v_count;
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN v_count;
END $$;

DO $$
BEGIN
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'ffffffff-1d00-4000-8000-000000000001', 'type', 'tool', 'effect', 'read', 'name', 'mi_whoami',
        'description', 'Who the request acts as', 'access', 'authenticated'),
        $b$BEGIN
            RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('whoami')), (request).request_id,
                structured_content => jsonb_build_object(
                    'principalId', api.current_principal_id(),
                    'organizationId', api.current_organization_id(),
                    'permissions', to_jsonb(api.current_permissions()),
                    'loginSessionId', api.current_login_session_id()));
        END;$b$);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'ffffffff-1d00-4000-8000-000000000002', 'type', 'tool', 'effect', 'read', 'name', 'mi_platform',
        'description', 'Platform operation', 'permissions', jsonb_build_array('platform.operate')),
        $b$BEGIN
            RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ran')), (request).request_id);
        END;$b$);
END $$;

-- ----------------------------------------------------------------------------
-- No identity from anything but an approved connection; no provisioning
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme     uuid := current_setting('test.acme_org_id')::uuid;
    v_alice    extensions.hstore := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme);
    v_before   bigint;
    v_context  jsonb;
    v_envelope jsonb;
    v_list     jsonb;
BEGIN
    RAISE DEBUG '-> MCP identity: approved connections only, no provisioning';

    v_before := pg_temp.mcp_identity_count();
    FOREACH v_context IN ARRAY ARRAY[
        NULL::jsonb,
        '{}'::jsonb,
        '{"user_id": "https://idp.example.com|mcp-newcomer", "user_email": "mcp-newcomer@example.test", "user_email_verified": true}'::jsonb,
        jsonb_build_object('user_id', current_setting('test.alice_subject'), 'tenant_id', v_acme, 'organizationId', v_acme),
        jsonb_build_object('loginSession', v_alice->'x-login-session', 'organizationSession', v_alice->'x-organization-session')]
    LOOP
        v_envelope := pg_temp.mcp_whoami(v_context);
        IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32602 THEN
            RAISE EXCEPTION 'TEST FAILED: MCP context % gave an identity: %', v_context, v_envelope;
        END IF;
        v_list := (api.mcp_handle_request('{"jsonrpc":"2.0","id":"l","method":"tools/list"}'::jsonb, v_context)).envelope;
        IF v_list->'result'->'tools' IS NULL OR v_list->'result'->'tools' @> '[{"name": "mi_whoami"}]' THEN
            RAISE EXCEPTION 'TEST FAILED: tools/list with context % lists a tool that needs an identity: %', v_context, v_list;
        END IF;
    END LOOP;
    FOREACH v_context IN ARRAY ARRAY[
        '{"connection": "cn_unknown"}'::jsonb,
        '{"connection": 42}'::jsonb,
        '{"connection": ""}'::jsonb,
        jsonb_build_object('connection', repeat('x', 300))]
    LOOP
        v_envelope := pg_temp.mcp_whoami(v_context);
        v_list := (api.mcp_handle_request('{"jsonrpc":"2.0","id":"l","method":"tools/list"}'::jsonb, v_context)).envelope;
        IF pg_temp.mcp_refusal(v_envelope) IS DISTINCT FROM 'pgmi.connection_unknown'
           OR pg_temp.mcp_refusal(v_list) IS DISTINCT FROM 'pgmi.connection_unknown'
           OR v_envelope->'id' IS DISTINCT FROM '"mi"'::jsonb OR v_list->'id' IS DISTINCT FROM '"l"'::jsonb THEN
            RAISE EXCEPTION 'TEST FAILED: unknown handle % must answer -31001 pgmi.connection_unknown, got % and %',
                v_context, v_envelope, v_list;
        END IF;
    END LOOP;
    IF pg_temp.mcp_identity_count() IS DISTINCT FROM v_before
       OR pg_temp.test_person('https://idp.example.com|mcp-newcomer') IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an MCP request provisioned a person, a login, a session or a connection';
    END IF;

    RAISE DEBUG '  + no context, an unknown handle, user_id/email, tenant or session handles give no identity and provision no one';
END $$;

-- ----------------------------------------------------------------------------
-- Approve on the web, act through MCP in the approved organization only
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme       uuid := current_setting('test.acme_org_id')::uuid;
    v_alice_id   uuid := pg_temp.test_person(current_setting('test.alice_subject'));
    v_other      uuid;
    v_web        extensions.hstore;
    v_other_web  extensions.hstore;
    v_response   api.http_response;
    v_body       jsonb;
    v_handle     text;
    v_envelope   jsonb;
    v_approved   record;
BEGIN
    RAISE DEBUG '-> MCP identity: approval, organization from the server';

    v_other := (membership.fn_create_organization_with_owner('Other Works', 'other-works', v_alice_id)).object_id;
    PERFORM pg_temp.test_operator(v_alice_id);
    v_web := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme, ARRAY['pwd', 'mfa']);
    v_other_web := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_other, ARRAY['pwd', 'mfa']);

    v_response := api.rest_invoke('POST', '/connections', v_web, '{"clientName": "Desktop assistant"}'::jsonb);
    IF (v_response).status_code IS DISTINCT FROM 400 THEN
        RAISE EXCEPTION 'TEST FAILED: approval without organizationId and roleId expected 400, got %', (v_response).status_code;
    END IF;
    v_response := api.rest_invoke('POST', '/connections', v_web, jsonb_build_object('clientName', ' padded ', 'organizationId', v_acme, 'roleId', 'agent.reader'));
    IF (v_response).status_code IS DISTINCT FROM 422 THEN
        RAISE EXCEPTION 'TEST FAILED: a padded client name expected 422, got %', (v_response).status_code;
    END IF;
    PERFORM pg_temp.test_act_as(current_setting('test.alice_subject'), v_acme, true);
    SELECT * INTO v_approved FROM membership.fn_approve_connection(v_acme, 'agent.reader', 'From SQL', 30);
    PERFORM pg_temp.test_act_as(NULL);
    IF v_approved.problem IS DISTINCT FROM 'membership.connection.login_session_required' OR v_approved.token IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: approval without a login session must be refused, got %', to_jsonb(v_approved);
    END IF;

    v_response := api.rest_invoke('POST', '/connections', v_web - 'x-organization-session'::text,
        jsonb_build_object('clientName', 'Desktop assistant', 'organizationId', v_acme, 'roleId', 'agent.delegate', 'validDays', 30));
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 201 OR (v_body->>'organizationId')::uuid IS DISTINCT FROM v_acme
       OR coalesce(v_body->>'connection', '') NOT LIKE 'cn\_%'
       OR (coalesce((v_body->>'expiresAt')::timestamptz BETWEEN now() + interval '29 days' AND now() + interval '31 days', false)) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: POST /connections expected 201 in Acme for 30 days, got %: %', (v_response).status_code, v_body;
    END IF;
    v_handle := v_body->>'connection';

    v_envelope := pg_temp.mcp_whoami(jsonb_build_object(
        'connection', v_handle,
        'organizationId', v_other, 'tenant_id', v_other, 'organization_id', v_other,
        'organizationSession', v_other_web->'x-organization-session',
        'loginSession', v_other_web->'x-login-session'));
    IF (v_envelope->'result'->'structuredContent'->>'principalId')::uuid IS DISTINCT FROM v_alice_id
       OR (v_envelope->'result'->'structuredContent'->>'organizationId')::uuid IS DISTINCT FROM v_acme
       OR v_envelope->'result'->'structuredContent'->'loginSessionId' IS DISTINCT FROM 'null'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: the connection must act as Alice in Acme whatever organization the client sends: %', v_envelope;
    END IF;
    IF ((v_envelope->'result'->'structuredContent'->'permissions') ? 'members.manage') IS NOT TRUE
       OR (v_envelope->'result'->'structuredContent'->'permissions') ? 'platform.operate' THEN
        RAISE EXCEPTION 'TEST FAILED: the connection holds the person''s organization permissions and no platform permission: %',
            v_envelope->'result'->'structuredContent'->'permissions';
    END IF;
    v_envelope := (api.mcp_call_tool('mi_platform', '{}'::jsonb, jsonb_build_object('connection', v_handle), '"p"'::jsonb)).envelope;
    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -31003
       OR v_envelope->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.forbidden' THEN
        RAISE EXCEPTION 'TEST FAILED: a connection must not use a platform permission, got %', v_envelope;
    END IF;

    v_response := api.rest_invoke('GET', '/connections', v_web, NULL::bytea);
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 200
       OR jsonb_array_length(v_body->'connections') IS DISTINCT FROM 1
       OR v_body->'connections'->0->>'status' IS DISTINCT FROM 'active'
       OR v_body->'connections'->0->>'lastUsedAt' IS NULL
       OR v_body::text LIKE '%' || v_handle || '%' THEN
        RAISE EXCEPTION 'TEST FAILED: GET /connections must list the active connection, its last use and never its handle: %', v_body;
    END IF;
    v_response := api.rest_invoke('GET', '/connections',
        pg_temp.test_session_headers(current_setting('test.bob_subject'), v_acme), NULL::bytea);
    IF api.content_json((v_response).content)->'connections' IS DISTINCT FROM '[]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: another person sees Alice''s connections: %', api.content_json((v_response).content);
    END IF;

    RAISE DEBUG '  + approved on the web, the connection acts as the person in the approved organization only';
END $$;

-- ----------------------------------------------------------------------------
-- Revocation, expiry, a disabled person and an ended membership break it
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme     uuid := current_setting('test.acme_org_id')::uuid;
    v_alice    extensions.hstore := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme);
    v_bob      extensions.hstore := pg_temp.test_session_headers(current_setting('test.bob_subject'), v_acme);
    v_bob_id   uuid := pg_temp.test_person(current_setting('test.bob_subject'));
    v_response api.http_response;
    v_body     jsonb;
    v_handle   text;
    v_id       uuid;
BEGIN
    RAISE DEBUG '-> MCP identity: revoke, expire, disable, end membership';

    v_body := api.content_json((api.rest_invoke('POST', '/connections', v_bob, jsonb_build_object('clientName', 'Bob CLI', 'organizationId', v_acme, 'roleId', 'agent.contributor'))).content);
    v_handle := v_body->>'connection';
    v_id := (v_body->>'connectionId')::uuid;
    IF pg_temp.mcp_whoami(jsonb_build_object('connection', v_handle))->'result'->'structuredContent'->>'principalId'
       IS DISTINCT FROM v_bob_id::text THEN
        RAISE EXCEPTION 'TEST FAILED: Bob''s new connection does not act as Bob';
    END IF;

    v_response := api.rest_invoke('DELETE', '/connections/' || v_id, v_alice, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 404
       OR pg_temp.mcp_whoami(jsonb_build_object('connection', v_handle)) ? 'error' THEN
        RAISE EXCEPTION 'TEST FAILED: Alice revoked Bob''s connection: %', (v_response).status_code;
    END IF;
    v_response := api.rest_invoke('DELETE', '/connections/' || v_id, v_bob, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200
       OR pg_temp.mcp_refusal(pg_temp.mcp_whoami(jsonb_build_object('connection', v_handle))) IS DISTINCT FROM 'pgmi.connection_unknown' THEN
        RAISE EXCEPTION 'TEST FAILED: a revoked connection must give no identity (DELETE answered %)', (v_response).status_code;
    END IF;
    IF (api.rest_invoke('DELETE', '/connections/' || v_id, v_bob, NULL::bytea)).status_code IS DISTINCT FROM 404
       OR (api.rest_invoke('DELETE', '/connections/not-a-uuid', v_bob, NULL::bytea)).status_code IS DISTINCT FROM 400 THEN
        RAISE EXCEPTION 'TEST FAILED: revoking twice answers 404, a malformed id 400';
    END IF;

    v_handle := api.content_json((api.rest_invoke('POST', '/connections', v_bob, jsonb_build_object('clientName', 'Bob expiring', 'organizationId', v_acme, 'roleId', 'agent.contributor'))).content)->>'connection';
    PERFORM pg_temp.test_as_kernel();
    UPDATE membership.connection SET approved_at = now() - interval '2 days', expires_at = now() - interval '1 second'
    WHERE token_hash = membership.fn_session_token_hash(v_handle);
    PERFORM pg_temp.test_as_owner();
    IF pg_temp.mcp_refusal(pg_temp.mcp_whoami(jsonb_build_object('connection', v_handle))) IS DISTINCT FROM 'pgmi.connection_unknown' THEN
        RAISE EXCEPTION 'TEST FAILED: an expired connection must give no identity';
    END IF;
    IF (api.content_json((api.rest_invoke('GET', '/connections', v_bob, NULL::bytea)).content)->'connections'
           @> '[{"clientName": "Bob expiring", "status": "expired"}, {"clientName": "Bob CLI", "status": "revoked"}]') IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: GET /connections must report revoked and expired connections';
    END IF;

    v_handle := api.content_json((api.rest_invoke('POST', '/connections', v_bob, jsonb_build_object('clientName', 'Bob disabled', 'organizationId', v_acme, 'roleId', 'agent.contributor'))).content)->>'connection';
    PERFORM membership.fn_disable_principal(v_bob_id, 'test');
    IF pg_temp.mcp_refusal(pg_temp.mcp_whoami(jsonb_build_object('connection', v_handle))) IS DISTINCT FROM 'pgmi.connection_unknown' THEN
        RAISE EXCEPTION 'TEST FAILED: a disabled person''s connection must give no identity';
    END IF;
    PERFORM membership.fn_enable_principal(v_bob_id);
    IF pg_temp.mcp_whoami(jsonb_build_object('connection', v_handle)) ? 'error' THEN
        RAISE EXCEPTION 'TEST FAILED: enabling the person again restores the connection';
    END IF;

    PERFORM pg_temp.test_as_kernel();
    PERFORM membership.fn_end_membership(m.object_id, 'removed')
    FROM membership.organization_membership m
    WHERE m.organization_object_id = v_acme AND m.principal_object_id = v_bob_id AND upper_inf(m.valid_during);
    PERFORM pg_temp.test_as_owner();
    PERFORM membership.fn_add_member(v_acme, v_bob_id, 'org.member');
    IF pg_temp.mcp_refusal(pg_temp.mcp_whoami(jsonb_build_object('connection', v_handle))) IS DISTINCT FROM 'pgmi.connection_unknown' THEN
        RAISE EXCEPTION 'TEST FAILED: a connection must end with its membership, even when the person rejoins';
    END IF;

    RAISE DEBUG '  + revoked, expired, disabled-person and ended-membership connections give no identity: pgmi.connection_unknown';
    RAISE NOTICE '  + MCP identity: only an approved, live connection acts as its person, in the approved organization; nothing provisions';
END $$;
