-- ============================================================================
-- Test: MCP callers from a verified OAuth identity, paired by code
-- ============================================================================
-- The gateway hands the database the issuer, subject and client id of an
-- access token it verified. Only a person who signed in on the web through
-- that issuer is admitted. The first call of an unknown (person, client) is a
-- pending connection with a pairing code, the same code for every call until
-- it expires; only its own person approves it, signed in on the web, with an
-- organization and an agent role no broader than theirs. Then the same
-- identity acts as that connection's agent. Paused and switched-off
-- connections refuse with their own code; an expired one pairs again and its
-- approval revokes the old one. A person has one live connection per client.
-- ============================================================================

CREATE FUNCTION pg_temp.oc_context(p_subject text, p_client text, p_issuer text DEFAULT 'https://idp.example.com')
RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_build_object('client', jsonb_build_object('issuer', p_issuer, 'subject', p_subject, 'clientId', p_client));
$$;

CREATE FUNCTION pg_temp.oc_call(p_context jsonb)
RETURNS jsonb LANGUAGE sql AS $$
    SELECT (api.mcp_handle_request(
        '{"jsonrpc": "2.0", "id": "oc", "method": "tools/call", "params": {"name": "oc_whoami", "arguments": {}}}'::jsonb,
        p_context)).envelope;
$$;

CREATE FUNCTION pg_temp.oc_refusal(p_envelope jsonb)
RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN (p_envelope->'error'->>'code')::int = -31001 AND p_envelope->'id' = '"oc"'::jsonb
                THEN p_envelope->'error'->'data'->>'code' END;
$$;

-- {carol} stands for Carol's principal, so rows a kept database already
-- holds never count.
CREATE FUNCTION pg_temp.oc_sql(p_sql text)
RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT replace(p_sql, '{carol}', '(SELECT user_principal_object_id FROM membership.external_identity
                                       WHERE issuer_id = ''https://idp.example.com'' AND subject = ''carol-oauth'')');
$$;

CREATE FUNCTION pg_temp.oc_rows(p_sql text)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_sql    text := pg_temp.oc_sql(p_sql);
    v_count  bigint;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    EXECUTE v_sql INTO v_count;
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN v_count;
END $$;

CREATE FUNCTION pg_temp.oc_kernel(p_sql text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_sql    text := pg_temp.oc_sql(p_sql);
BEGIN
    PERFORM pg_temp.test_as_kernel();
    EXECUTE v_sql;
    EXECUTE format('SET ROLE %I', v_caller);
END $$;

CREATE FUNCTION pg_temp.oc_approve(p_headers extensions.hstore, p_code text, p_organization uuid, p_role text)
RETURNS api.http_response LANGUAGE sql AS $$
    SELECT api.rest_invoke('POST', '/connections/approve', p_headers,
        jsonb_build_object('pairingCode', p_code, 'organizationId', p_organization, 'roleId', p_role));
$$;

DO $$
DECLARE
    v_carol uuid := pg_temp.test_login('https://idp.example.com', 'carol-oauth', 'carol@acme.com', 'Carol Ruiz', true);
BEGIN
    PERFORM membership.fn_add_member(current_setting('test.acme_org_id')::uuid, v_carol, 'org.member');
    PERFORM pg_temp.test_login('https://idp.example.com', 'dave-oauth', 'dave@example.com', 'Dave Ng', true);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'ffffffff-a512-4000-8000-000000000001', 'type', 'tool', 'effect', 'read', 'name', 'oc_whoami',
        'description', 'Who acts', 'access', 'authenticated'),
        $b$BEGIN
            RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('whoami')), (request).request_id,
                structured_content => jsonb_build_object(
                    'principalId', api.current_principal_id(),
                    'actualPrincipalId', api.current_actual_principal_id(),
                    'actorKind', api.current_actor_kind(),
                    'organizationId', api.current_organization_id(),
                    'permissions', to_jsonb(api.current_permissions())));
        END;$b$);
END $$;

-- ----------------------------------------------------------------------------
-- Not admitted: no web sign-in through that issuer, a development issuer, a
-- malformed client id, a disabled person; nothing is written
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_handle  text := pg_temp.test_mcp_context(current_setting('test.alice_subject'), current_setting('test.acme_org_id')::uuid)->>'connection';
    v_before  bigint := pg_temp.oc_rows('SELECT (SELECT count(*) FROM membership.pending_connection) + (SELECT count(*) FROM membership.connection) + (SELECT count(*) FROM membership.principal)');
    v_context jsonb;
    v_dave    uuid := pg_temp.test_person('https://idp.example.com|dave-oauth');
BEGIN
    PERFORM membership.fn_disable_principal(v_dave, 'test');
    FOREACH v_context IN ARRAY ARRAY[
        pg_temp.oc_context('nobody-here', 'desktop-client'),
        pg_temp.oc_context('carol-oauth', 'desktop-client', 'https://other-idp.example.com'),
        pg_temp.oc_context('carol-oauth', 'desktop-client', 'https://unknown.example.com'),
        pg_temp.oc_context('alice-api-test', 'desktop-client', 'google'),
        pg_temp.oc_context('carol-oauth', ' padded'),
        pg_temp.oc_context('carol-oauth', ''),
        pg_temp.oc_context('carol-oauth', NULL),
        pg_temp.oc_context(NULL, 'desktop-client'),
        pg_temp.oc_context('dave-oauth', 'desktop-client'),
        pg_temp.oc_context('nobody-here', 'desktop-client') || jsonb_build_object('connection', v_handle)]
    LOOP
        IF pg_temp.oc_refusal(pg_temp.oc_call(v_context)) IS DISTINCT FROM 'membership.connection.not_admitted' THEN
            RAISE EXCEPTION 'TEST FAILED: context % must be not admitted, got %', v_context, pg_temp.oc_call(v_context);
        END IF;
    END LOOP;
    PERFORM membership.fn_enable_principal(v_dave);
    IF pg_temp.oc_rows('SELECT (SELECT count(*) FROM membership.pending_connection) + (SELECT count(*) FROM membership.connection) + (SELECT count(*) FROM membership.principal)')
       IS DISTINCT FROM v_before THEN
        RAISE EXCEPTION 'TEST FAILED: an identity that is not admitted wrote a pending connection or a principal';
    END IF;
    RAISE DEBUG '  + unknown, development, malformed and disabled identities are not admitted; a handle beside them is ignored';
END $$;

-- ----------------------------------------------------------------------------
-- Pending: one code per (person, client), repeated until approved, for every
-- method; only its own person approves it
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme     uuid := current_setting('test.acme_org_id')::uuid;
    v_carol    extensions.hstore := pg_temp.test_session_headers('https://idp.example.com|carol-oauth');
    v_bob      extensions.hstore := pg_temp.test_session_headers(current_setting('test.bob_subject'));
    v_envelope jsonb := pg_temp.oc_call(pg_temp.oc_context('carol-oauth', 'desktop-client'));
    v_code     text := v_envelope->'error'->'data'->>'pairingCode';
    v_other    text;
    v_response api.http_response;
    v_approved record;
BEGIN
    IF pg_temp.oc_refusal(v_envelope) IS DISTINCT FROM 'membership.connection.pending'
       OR (v_code ~ '^[2-9A-HJKMNP-TV-Z]{6}$') IS NOT TRUE
       OR strpos(v_envelope->'error'->>'message', v_code) IS NOT DISTINCT FROM 0
       OR (v_envelope->'error'->'data'->>'expiresAt')::timestamptz NOT BETWEEN now() + interval '59 minutes' AND now() + interval '61 minutes' THEN
        RAISE EXCEPTION 'TEST FAILED: a first call must answer -31001 membership.connection.pending with a code valid for an hour: %', v_envelope;
    END IF;
    IF pg_temp.oc_call(pg_temp.oc_context('carol-oauth', 'desktop-client'))->'error'->'data'->>'pairingCode' IS DISTINCT FROM v_code
       OR (api.mcp_handle_request('{"jsonrpc": "2.0", "id": "oc", "method": "tools/list"}'::jsonb,
                                  pg_temp.oc_context('carol-oauth', 'desktop-client'))).envelope->'error'->'data'->>'pairingCode'
          IS DISTINCT FROM v_code
       OR (api.mcp_call_tool('oc_whoami', '{}'::jsonb, pg_temp.oc_context('carol-oauth', 'desktop-client'), '"t"'::jsonb)).envelope
              ->'error'->'data'->>'pairingCode' IS DISTINCT FROM v_code THEN
        RAISE EXCEPTION 'TEST FAILED: every call and method of a pending client must answer the same code';
    END IF;
    v_other := pg_temp.oc_call(pg_temp.oc_context('carol-oauth', 'cli-client'))->'error'->'data'->>'pairingCode';
    IF v_other IS NULL OR v_other IS NOT DISTINCT FROM v_code
       OR pg_temp.oc_rows('SELECT count(*) FROM membership.pending_connection WHERE principal_object_id = {carol}')
          IS DISTINCT FROM 2 THEN
        RAISE EXCEPTION 'TEST FAILED: each client of a person gets its own pending connection and code';
    END IF;

    v_response := pg_temp.oc_approve(v_bob, v_code, v_acme, 'agent.reader');
    IF (v_response).status_code IS DISTINCT FROM 422
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'membership.connection.pairing_code_unknown' THEN
        RAISE EXCEPTION 'TEST FAILED: another person must not approve Carol''s code: % %', (v_response).status_code,
            api.content_json((v_response).content);
    END IF;
    PERFORM pg_temp.test_act_as('https://idp.example.com|carol-oauth', v_acme, true);
    SELECT * INTO v_approved FROM membership.fn_approve_connection_by_code(v_code, v_acme, 'agent.reader', 30);
    PERFORM pg_temp.test_act_as(NULL);
    IF v_approved.problem IS DISTINCT FROM 'membership.connection.login_session_required' OR v_approved.connection_object_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: approval without a login session must be refused: %', to_jsonb(v_approved);
    END IF;
    FOR v_approved IN
        SELECT * FROM (VALUES ('ZZZZZZ', v_acme, 'agent.reader', 422, 'membership.connection.pairing_code_unknown'),
                              (v_code, v_acme, 'agent.delegate', 422, 'membership.connection.role_too_broad'),
                              (v_code, v_acme, 'org.member', 422, 'membership.connection.role_unknown'),
                              (v_code, gen_random_uuid(), 'agent.reader', 422, 'membership.connection.not_member')) t(code, org, role, status, problem)
    LOOP
        v_response := pg_temp.oc_approve(v_carol, v_approved.code, v_approved.org, v_approved.role);
        IF (v_response).status_code IS DISTINCT FROM v_approved.status
           OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM v_approved.problem THEN
            RAISE EXCEPTION 'TEST FAILED: approval % expected % %, got % %', to_jsonb(v_approved), v_approved.status,
                v_approved.problem, (v_response).status_code, api.content_json((v_response).content);
        END IF;
    END LOOP;
    IF pg_temp.oc_rows('SELECT count(*) FROM membership.pending_connection WHERE principal_object_id = {carol}')
          IS DISTINCT FROM 2
       OR pg_temp.oc_rows('SELECT count(*) FROM membership.connection WHERE principal_object_id = {carol}')
          IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'TEST FAILED: a refused approval must leave the pending connection and make no connection';
    END IF;
    PERFORM set_config('test.oc_code', v_code, true);
    RAISE DEBUG '  + a pending client answers one code everywhere; only its person approves, within their role';
END $$;


CREATE FUNCTION pg_temp.oc_value(p_sql text)
RETURNS text LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_sql    text := pg_temp.oc_sql(p_sql);
    v_value  text;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    EXECUTE v_sql INTO v_value;
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN v_value;
END $$;

-- ----------------------------------------------------------------------------
-- Approved: the same identity acts as the connection's agent, role ∩ person
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme     uuid := current_setting('test.acme_org_id')::uuid;
    v_carol_id uuid := pg_temp.test_person('https://idp.example.com|carol-oauth');
    v_carol    extensions.hstore := pg_temp.test_session_headers('https://idp.example.com|carol-oauth');
    v_response api.http_response;
    v_body     jsonb;
    v_agent    text;
    v_expected text;
    v_who      jsonb;
BEGIN
    v_response := pg_temp.oc_approve(v_carol, lower(current_setting('test.oc_code')), v_acme, 'agent.reader');
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 201 OR v_body->>'clientId' IS DISTINCT FROM 'desktop-client'
       OR v_body->>'clientName' IS DISTINCT FROM 'desktop-client' OR v_body->>'roleId' IS DISTINCT FROM 'agent.reader'
       OR v_body ? 'connection' THEN
        RAISE EXCEPTION 'TEST FAILED: approving the code expected 201 for desktop-client without a handle, got % %',
            (v_response).status_code, v_body;
    END IF;
    v_agent := pg_temp.oc_value(format(
        'SELECT c.agent_principal_object_id FROM membership.connection c
         JOIN membership.principal a ON a.object_id = c.agent_principal_object_id AND a.kind = %L
         WHERE c.object_id = %L AND c.token_hash IS NULL AND c.issuer_id = %L AND c.organization_object_id = %L',
        'agent', v_body->>'connectionId', 'https://idp.example.com', v_acme));
    v_expected := pg_temp.oc_value(format(
        'SELECT coalesce(jsonb_agg(rp.permission_id ORDER BY rp.permission_id), %L)::text FROM membership.role_permission rp
         WHERE rp.role_id = %L AND rp.permission_id = ANY (membership.fn_effective_permissions(%L, %L, false))',
        '[]', 'agent.reader', v_carol_id, v_acme));
    IF v_agent IS NULL OR v_expected IS NULL OR v_expected IS NOT DISTINCT FROM '[]'
       OR pg_temp.oc_rows('SELECT count(*) FROM membership.pending_connection WHERE principal_object_id = {carol} AND client_id = ''desktop-client''') IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'TEST FAILED: approval must make an agent connection without a handle and consume the code (agent %, expected %)',
            v_agent, v_expected;
    END IF;

    v_who := pg_temp.oc_call(pg_temp.oc_context('carol-oauth', 'desktop-client'))->'result'->'structuredContent';
    IF v_who->>'principalId' IS DISTINCT FROM v_carol_id::text OR v_who->>'actualPrincipalId' IS DISTINCT FROM v_agent
       OR v_who->>'actorKind' IS DISTINCT FROM 'agent' OR v_who->>'organizationId' IS DISTINCT FROM v_acme::text
       OR (SELECT jsonb_agg(p ORDER BY p) FROM jsonb_array_elements_text(v_who->'permissions') p)::text IS DISTINCT FROM v_expected THEN
        RAISE EXCEPTION 'TEST FAILED: the approved client must act as Carol''s agent in Acme with role ∩ person %: %', v_expected, v_who;
    END IF;
    IF pg_temp.oc_value(format('SELECT last_used_at IS NOT NULL FROM membership.connection WHERE object_id = %L',
                               v_body->>'connectionId')) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'TEST FAILED: a call through the client must record the connection''s last use';
    END IF;
    IF api.content_json((pg_temp.oc_approve(v_carol, current_setting('test.oc_code'), v_acme, 'agent.reader')).content)->>'code'
       IS DISTINCT FROM 'membership.connection.pairing_code_unknown' THEN
        RAISE EXCEPTION 'TEST FAILED: a code approves once';
    END IF;
    IF (api.content_json((api.rest_invoke('GET', '/connections', v_carol, NULL::bytea)).content)->'connections')
           @> '[{"clientId": "desktop-client", "status": "active", "roleId": "agent.reader"}]' IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: GET /connections must list the client''s connection';
    END IF;

    PERFORM set_config('test.oc_connection', v_body->>'connectionId', true);
    RAISE DEBUG '  + the approved client acts as its person''s agent, role intersected with the person';
END $$;

-- ----------------------------------------------------------------------------
-- One live connection per (person, client), enforced by the index
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_constraint text;
BEGIN
    PERFORM pg_temp.oc_kernel(format(
        'WITH a AS (INSERT INTO membership.principal (kind, display_name) VALUES (%L, %L) RETURNING object_id),
              b AS (INSERT INTO membership.agent_principal (principal_object_id) SELECT object_id FROM a)
         INSERT INTO membership.connection (issuer_id, client_id, principal_object_id, organization_object_id,
             membership_object_id, agent_principal_object_id, role_id, client_name, expires_at)
         SELECT c.issuer_id, c.client_id, c.principal_object_id, c.organization_object_id, c.membership_object_id,
                a.object_id, c.role_id, c.client_name, c.expires_at
         FROM membership.connection c, a WHERE c.object_id = %L',
        'agent', 'second', current_setting('test.oc_connection')));
    RAISE EXCEPTION 'TEST FAILED: a second live connection for one person and client was accepted';
EXCEPTION WHEN unique_violation THEN
    GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
    IF v_constraint IS DISTINCT FROM 'uq_connection_client' THEN
        RAISE EXCEPTION 'TEST FAILED: expected uq_connection_client, got %', v_constraint;
    END IF;
    RAISE DEBUG '  + a second live connection for one person and client violates uq_connection_client';
END $$;

-- ----------------------------------------------------------------------------
-- Paused and switched off refuse with their own code; resumed acts again
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme     uuid := current_setting('test.acme_org_id')::uuid;
    v_id       text := current_setting('test.oc_connection');
    v_carol    extensions.hstore := pg_temp.test_session_headers('https://idp.example.com|carol-oauth');
    v_alice    extensions.hstore := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme);
    v_context  jsonb := pg_temp.oc_context('carol-oauth', 'desktop-client');
    v_envelope jsonb;
BEGIN
    IF (api.rest_invoke('POST', '/connections/' || v_id || '/pause', v_carol, NULL::bytea)).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: Carol pauses her client''s connection';
    END IF;
    v_envelope := pg_temp.oc_call(v_context);
    IF pg_temp.oc_refusal(v_envelope) IS DISTINCT FROM 'membership.connection.paused' OR v_envelope->'error'->'data' ? 'pairingCode'
       OR pg_temp.oc_rows('SELECT count(*) FROM membership.pending_connection WHERE principal_object_id = {carol} AND client_id = ''desktop-client''') IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'TEST FAILED: a paused connection must refuse with membership.connection.paused and no new code: %', v_envelope;
    END IF;
    PERFORM api.rest_invoke('POST', '/connections/' || v_id || '/resume', v_carol, NULL::bytea);
    IF pg_temp.oc_call(v_context)->'result'->'structuredContent'->>'actorKind' IS DISTINCT FROM 'agent' THEN
        RAISE EXCEPTION 'TEST FAILED: a resumed connection acts again';
    END IF;

    IF (api.rest_invoke('POST', '/agents/disable', v_alice, NULL::bytea)).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: the owner switches the organization''s agents off';
    END IF;
    IF pg_temp.oc_refusal(pg_temp.oc_call(v_context)) IS DISTINCT FROM 'membership.connection.disabled' THEN
        RAISE EXCEPTION 'TEST FAILED: switched-off agents must refuse with membership.connection.disabled: %', pg_temp.oc_call(v_context);
    END IF;
    PERFORM api.rest_invoke('POST', '/agents/enable', v_alice, NULL::bytea);
    IF pg_temp.oc_call(v_context) ? 'error' THEN
        RAISE EXCEPTION 'TEST FAILED: switched back on, the client acts again';
    END IF;
    RAISE DEBUG '  + paused and switched-off connections refuse with their own code, never a pairing code';
END $$;

-- ----------------------------------------------------------------------------
-- Expiry: a stale code renews on the next call and the old one no longer
-- approves; an expired connection pairs again and its approval revokes it
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme    uuid := current_setting('test.acme_org_id')::uuid;
    v_carol   extensions.hstore := pg_temp.test_session_headers('https://idp.example.com|carol-oauth');
    v_old     text;
    v_new     jsonb;
    v_code    text;
    v_body    jsonb;
BEGIN
    v_old := pg_temp.oc_call(pg_temp.oc_context('carol-oauth', 'cli-client'))->'error'->'data'->>'pairingCode';
    PERFORM pg_temp.oc_kernel('UPDATE membership.pending_connection SET requested_at = now() - interval ''2 hours'',
                                   expires_at = now() - interval ''1 second'' WHERE principal_object_id = {carol} AND client_id = ''cli-client''');
    IF api.content_json((pg_temp.oc_approve(v_carol, v_old, v_acme, 'agent.reader')).content)->>'code'
       IS DISTINCT FROM 'membership.connection.pairing_code_unknown' THEN
        RAISE EXCEPTION 'TEST FAILED: an expired code must not approve';
    END IF;
    v_new := pg_temp.oc_call(pg_temp.oc_context('carol-oauth', 'cli-client'))->'error'->'data';
    IF v_new->>'code' IS DISTINCT FROM 'membership.connection.pending' OR (v_new->>'expiresAt')::timestamptz <= now()
       OR pg_temp.oc_rows('SELECT count(*) FROM membership.pending_connection WHERE principal_object_id = {carol} AND client_id = ''cli-client''') IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'TEST FAILED: the first call after expiry renews the pending connection in place: %', v_new;
    END IF;

    PERFORM pg_temp.oc_kernel(format('UPDATE membership.connection SET approved_at = now() - interval ''2 days'',
                                          expires_at = now() - interval ''1 second'' WHERE object_id = %L',
                                     current_setting('test.oc_connection')));
    v_code := pg_temp.oc_call(pg_temp.oc_context('carol-oauth', 'desktop-client'))->'error'->'data'->>'pairingCode';
    IF v_code IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an expired connection must pair again';
    END IF;
    v_body := api.content_json((pg_temp.oc_approve(v_carol, v_code, v_acme, 'agent.contributor')).content);
    IF v_body->>'connectionId' IS NULL
       OR pg_temp.oc_value(format('SELECT revoked_at IS NOT NULL FROM membership.connection WHERE object_id = %L',
                                  current_setting('test.oc_connection'))) IS DISTINCT FROM 'true'
       OR pg_temp.oc_rows('SELECT count(*) FROM membership.connection WHERE principal_object_id = {carol} AND client_id = ''desktop-client'' AND revoked_at IS NULL')
          IS DISTINCT FROM 1
       OR pg_temp.oc_call(pg_temp.oc_context('carol-oauth', 'desktop-client'))->'result'->'structuredContent'->>'actorKind'
          IS DISTINCT FROM 'agent' THEN
        RAISE EXCEPTION 'TEST FAILED: re-approval must revoke the expired connection and act through the new one: %', v_body;
    END IF;
    RAISE DEBUG '  + expired codes renew, expired connections pair again and are revoked on approval';
END $$;

-- ----------------------------------------------------------------------------
-- Pairing codes: the alphabet only, six characters, not repeating
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_bad      bigint;
    v_distinct bigint;
BEGIN
    SELECT count(*) FILTER (WHERE c !~ '^[2-9A-HJKMNP-TV-Z]{6}$' OR c ~ '[01ILOU]'), count(DISTINCT c)
    INTO v_bad, v_distinct
    FROM (SELECT membership.fn_new_pairing_code() AS c FROM generate_series(1, 500)) g;
    IF v_bad IS DISTINCT FROM 0::bigint OR v_distinct < 495 THEN
        RAISE EXCEPTION 'TEST FAILED: pairing codes: % outside the alphabet, % distinct of 500', v_bad, v_distinct;
    END IF;
    RAISE NOTICE '  + MCP OAuth clients: admitted only as a web-signed-in person, paired by code, acting as the approved agent';
END $$;
