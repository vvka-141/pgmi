-- ============================================================================
-- Test: agent roles and the agent lifecycle
-- ============================================================================
-- Approval names the organization and an agent role, refuses a role broader
-- than the person holds there and an organization they are no member of,
-- and is never made by an agent. An agent holds its role's permissions
-- intersected with its person's, recomputed per request. A person pauses,
-- resumes and revokes their own connections; a holder of members.manage
-- those into their organization only, and a manager's pause is lifted only
-- by a manager. The owner's switch turns every agent off; ending a
-- membership revokes the person's connections into it. Agent roles are no
-- member's, and an agent is never a member. Every change is audited with
-- the agent as its subject.
-- ============================================================================

CREATE FUNCTION pg_temp.agent_whoami(p_handle text)
RETURNS jsonb LANGUAGE sql AS $$
    SELECT (api.mcp_call_tool('al_whoami', '{}'::jsonb, jsonb_build_object('connection', p_handle), '"w"'::jsonb)).envelope;
$$;

-- The data.code of the -31001 a handle that cannot act answers, else NULL.
CREATE FUNCTION pg_temp.agent_refusal(p_handle text)
RETURNS text LANGUAGE sql AS $$
    SELECT CASE WHEN (e->>'code')::int = -31001 THEN e->'data'->>'code' END
    FROM (SELECT pg_temp.agent_whoami(p_handle)->'error' AS e) w;
$$;

CREATE FUNCTION pg_temp.agent_approve(p_headers extensions.hstore, p_organization uuid, p_role text, p_name text)
RETURNS api.http_response LANGUAGE sql AS $$
    SELECT api.rest_invoke('POST', '/connections', p_headers,
        jsonb_build_object('clientName', p_name, 'organizationId', p_organization, 'roleId', p_role));
$$;

CREATE FUNCTION pg_temp.agent_audit(p_action text, p_connection uuid)
RETURNS membership.audit_event LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_event  membership.audit_event;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    SELECT * INTO v_event FROM membership.audit_event
    WHERE action = p_action AND detail->>'connectionId' = p_connection::text
    ORDER BY object_id DESC LIMIT 1;
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN v_event;
END $$;

-- The connection's state as the organization's owner sees it.
CREATE FUNCTION pg_temp.agent_status(p_connection uuid)
RETURNS text LANGUAGE plpgsql AS $$
DECLARE
    v_status text;
BEGIN
    PERFORM pg_temp.test_act_as(current_setting('test.alice_subject'), current_setting('test.acme_org_id')::uuid);
    SELECT c.status INTO v_status FROM membership.vw_connection c WHERE c.connection_object_id = p_connection;
    PERFORM pg_temp.test_act_as(NULL);
    RETURN v_status;
END $$;

DO $$
BEGIN
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'ffffffff-a510-4000-8000-000000000001', 'type', 'tool', 'effect', 'read', 'name', 'al_whoami',
        'description', 'Who acts, with which permissions', 'access', 'authenticated'),
        $b$BEGIN
            RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('whoami')), (request).request_id,
                structured_content => jsonb_build_object(
                    'principalId', api.current_principal_id(),
                    'actualPrincipalId', api.current_actual_principal_id(),
                    'permissions', to_jsonb(api.current_permissions())));
        END;$b$);
END $$;

-- ----------------------------------------------------------------------------
-- Approval: organization and role in the body, the role never broader
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme     uuid := current_setting('test.acme_org_id')::uuid;
    v_alice_id uuid := pg_temp.test_person(current_setting('test.alice_subject'));
    v_bob      extensions.hstore := pg_temp.test_session_headers(current_setting('test.bob_subject'));
    v_alice    extensions.hstore := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme);
    v_other    uuid;
    v_response api.http_response;
    v_body     jsonb;
    v_case     record;
    v_agent    uuid;
    v_approved record;
BEGIN
    RAISE DEBUG '-> Agent lifecycle: approval';

    v_other := (membership.fn_create_organization_with_owner('Lifecycle Elsewhere', 'lifecycle-elsewhere', v_alice_id)).object_id;

    FOR v_case IN
        SELECT * FROM (VALUES (v_acme, 'agent.delegate', 'membership.connection.role_too_broad'),
                              (v_acme, 'org.member', 'membership.connection.role_unknown'),
                              (v_acme, 'agent.nothing', 'membership.connection.role_unknown'),
                              (v_other, 'agent.reader', 'membership.connection.not_member')) c(organization, role, problem)
    LOOP
        v_response := pg_temp.agent_approve(v_bob, v_case.organization, v_case.role, 'Refused');
        IF (v_response).status_code IS DISTINCT FROM 422
           OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM v_case.problem THEN
            RAISE EXCEPTION 'TEST FAILED: approval expected 422 %, got % %', v_case.problem,
                (v_response).status_code, api.content_json((v_response).content);
        END IF;
    END LOOP;

    v_response := pg_temp.agent_approve(v_bob, v_acme, 'agent.contributor', 'Bob contributor');
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 201 OR v_body->>'roleId' IS DISTINCT FROM 'agent.contributor'
       OR (v_body->>'organizationId')::uuid IS DISTINCT FROM v_acme THEN
        RAISE EXCEPTION 'TEST FAILED: Bob approves a contributor agent in Acme without an organization session, got % %',
            (v_response).status_code, v_body;
    END IF;

    IF pg_temp.agent_whoami(v_body->>'connection') ? 'error' THEN
        RAISE EXCEPTION 'TEST FAILED: Bob''s approved connection must act';
    END IF;

    -- An agent never approves or changes a connection, not even with its person's login session in hand.
    v_body := api.content_json((pg_temp.agent_approve(v_alice, v_acme, 'agent.reader', 'Alice reader')).content);
    v_agent := (pg_temp.agent_whoami(v_body->>'connection')->'result'->'structuredContent'->>'actualPrincipalId')::uuid;
    PERFORM internal.resolve_session_context(v_alice->'x-login-session', v_alice->'x-organization-session');
    PERFORM set_config('auth.actual_principal_id', v_agent::text, true), set_config('auth.ctx', '', true);
    IF api.current_actor_kind() IS DISTINCT FROM 'agent' OR api.current_login_session_id() IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the probe needs an agent context with a login session, got % %',
            api.current_actor_kind(), api.current_login_session_id();
    END IF;
    SELECT * INTO v_approved FROM membership.fn_approve_connection(v_acme, 'agent.reader', 'Agent made', 1);
    IF v_approved.problem IS DISTINCT FROM 'membership.connection.login_session_required' OR v_approved.token IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an agent must never approve a connection, got %', to_jsonb(v_approved);
    END IF;
    IF membership.fn_connection_problem((v_body->>'connectionId')::uuid, 'revoke') IS DISTINCT FROM 'membership.connection.not_found' THEN
        RAISE EXCEPTION 'TEST FAILED: an agent must never pause, resume or revoke a connection, even its own';
    END IF;
    PERFORM pg_temp.test_act_as(NULL);

    RAISE DEBUG '  + approval: role and organization in the body, never broader, never by an agent';
END $$;

-- ----------------------------------------------------------------------------
-- Agent permissions: the role's intersected with the person's, live
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme    uuid := current_setting('test.acme_org_id')::uuid;
    v_bob_id  uuid := pg_temp.test_person(current_setting('test.bob_subject'));
    v_bob     extensions.hstore := pg_temp.test_session_headers(current_setting('test.bob_subject'));
    v_alice   extensions.hstore := pg_temp.test_session_headers(current_setting('test.alice_subject'));
    v_member  uuid;
    v_handle  text;
    v_reader  text;
BEGIN
    RAISE DEBUG '-> Agent lifecycle: permissions';

    v_handle := api.content_json((pg_temp.agent_approve(v_bob, v_acme, 'agent.contributor', 'Bob perms')).content)->>'connection';
    v_reader := api.content_json((pg_temp.agent_approve(v_alice, v_acme, 'agent.reader', 'Alice perms')).content)->>'connection';
    IF pg_temp.agent_whoami(v_handle)->'result'->'structuredContent'->'permissions'
           IS DISTINCT FROM '["members.read", "project.read", "project.write", "task.read", "task.write"]'::jsonb
       OR pg_temp.agent_whoami(v_reader)->'result'->'structuredContent'->'permissions'
           IS DISTINCT FROM '["members.read", "project.read", "task.read"]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: an agent holds its role''s permissions within its person''s: % %',
            pg_temp.agent_whoami(v_handle), pg_temp.agent_whoami(v_reader);
    END IF;

    PERFORM pg_temp.test_act_as(NULL);
    PERFORM pg_temp.test_as_kernel();
    SELECT m.membership_object_id INTO STRICT v_member FROM membership.vw_member m
    WHERE m.organization_object_id = v_acme AND m.principal_object_id = v_bob_id;
    PERFORM pg_temp.test_as_owner();
    PERFORM membership.fn_revoke_role(v_member, 'org.member');
    PERFORM membership.fn_grant_role(v_member, 'org.viewer');
    IF pg_temp.agent_whoami(v_handle)->'result'->'structuredContent'->'permissions'
           IS DISTINCT FROM '["members.read", "project.read", "task.read"]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a person who loses permissions takes them from their agent at once: %',
            pg_temp.agent_whoami(v_handle);
    END IF;
    PERFORM membership.fn_revoke_role(v_member, 'org.viewer');
    IF pg_temp.agent_whoami(v_handle)->'result'->'structuredContent'->'permissions' IS DISTINCT FROM '[]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a person with no role leaves their agent no permission: %', pg_temp.agent_whoami(v_handle);
    END IF;
    PERFORM membership.fn_grant_role(v_member, 'org.member');

    RAISE DEBUG '  + agent permissions follow role and person';
END $$;

-- ----------------------------------------------------------------------------
-- Pause and resume by the person; managers within their organization only
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme      uuid := current_setting('test.acme_org_id')::uuid;
    v_bob_id    uuid := pg_temp.test_person(current_setting('test.bob_subject'));
    v_alice_id  uuid := pg_temp.test_person(current_setting('test.alice_subject'));
    v_bob       extensions.hstore := pg_temp.test_session_headers(current_setting('test.bob_subject'), v_acme);
    v_alice     extensions.hstore := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme);
    v_bob_org   uuid;
    v_body      jsonb;
    v_handle    text;
    v_id        uuid;
    v_far       uuid;
    v_own       uuid;
    v_response  api.http_response;
    v_audit     membership.audit_event;
BEGIN
    RAISE DEBUG '-> Agent lifecycle: pause, resume, managers';

    v_body := api.content_json((pg_temp.agent_approve(v_bob, v_acme, 'agent.contributor', 'Bob pausable')).content);
    v_handle := v_body->>'connection';
    v_id := (v_body->>'connectionId')::uuid;

    v_response := api.rest_invoke('POST', '/connections/' || v_id || '/pause', v_bob, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200
       OR api.content_json((v_response).content)->>'status' IS DISTINCT FROM 'paused'
       OR pg_temp.agent_refusal(v_handle) IS DISTINCT FROM 'membership.connection.paused' THEN
        RAISE EXCEPTION 'TEST FAILED: a paused agent answers -31001 membership.connection.paused, got % %', (v_response).status_code, pg_temp.agent_whoami(v_handle);
    END IF;
    v_audit := pg_temp.agent_audit('membership.connection.paused', v_id);
    IF v_audit.subject_principal_object_id IS DISTINCT FROM (pg_temp.agent_audit('membership.connection.approved', v_id)).subject_principal_object_id
       OR v_audit.subject_principal_object_id IS NULL OR v_audit.subject_principal_object_id IS NOT DISTINCT FROM v_bob_id
       OR v_audit.actor_kind IS DISTINCT FROM 'human' OR v_audit.actual_principal_object_id IS DISTINCT FROM v_bob_id THEN
        RAISE EXCEPTION 'TEST FAILED: the pause is audited as Bob acting on his agent: %', to_jsonb(v_audit);
    END IF;
    v_response := api.rest_invoke('POST', '/connections/' || v_id || '/pause', v_bob, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 409
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'membership.connection.paused' THEN
        RAISE EXCEPTION 'TEST FAILED: pausing twice answers 409 membership.connection.paused, got %', (v_response).status_code;
    END IF;
    v_response := api.rest_invoke('POST', '/connections/' || v_id || '/resume', v_bob, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 OR pg_temp.agent_whoami(v_handle) ? 'error' THEN
        RAISE EXCEPTION 'TEST FAILED: a resumed agent acts again, got % %', (v_response).status_code, pg_temp.agent_whoami(v_handle);
    END IF;
    IF (api.rest_invoke('POST', '/connections/' || v_id || '/resume', v_bob, NULL::bytea)).status_code IS DISTINCT FROM 409 THEN
        RAISE EXCEPTION 'TEST FAILED: resuming an active connection answers 409';
    END IF;

    -- A connection of Bob's into an organization Alice does not manage.
    PERFORM pg_temp.test_act_as(NULL);
    v_bob_org := (membership.fn_create_organization_with_owner('Bob Works', 'bob-works', v_bob_id)).object_id;
    v_far := (api.content_json((pg_temp.agent_approve(v_bob, v_bob_org, 'agent.reader', 'Bob far')).content)->>'connectionId')::uuid;
    v_own := (api.content_json((pg_temp.agent_approve(v_alice, pg_temp.test_organization('lifecycle-elsewhere'), 'agent.reader',
                                                      'Alice far')).content)->>'connectionId')::uuid;

    v_body := api.content_json((api.rest_invoke('GET', '/agents', v_alice, NULL::bytea)).content);
    IF (v_body->'connections' @> jsonb_build_array(jsonb_build_object('connectionId', v_id, 'principalId', v_bob_id))) IS NOT TRUE
       OR (v_body->'connections' @> jsonb_build_array(jsonb_build_object('connectionId', v_far))) IS NOT FALSE
       OR (v_body->'connections' @> jsonb_build_array(jsonb_build_object('connectionId', v_own))) IS NOT FALSE
       OR v_own IS NULL
       OR v_body->'agentsEnabled' IS DISTINCT FROM 'true'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: the manager lists Acme''s agents only: %', v_body;
    END IF;
    FOREACH v_response IN ARRAY ARRAY[
        api.rest_invoke('POST', '/agents/' || v_far || '/pause', v_alice, NULL::bytea),
        api.rest_invoke('DELETE', '/agents/' || v_far, v_alice, NULL::bytea),
        api.rest_invoke('POST', '/connections/' || v_id || '/pause', v_alice, NULL::bytea)]
    LOOP
        IF (v_response).status_code IS DISTINCT FROM 404 THEN
            RAISE EXCEPTION 'TEST FAILED: a manager reaches only the organization''s agents through /agents, got %',
                (v_response).status_code;
        END IF;
    END LOOP;

    IF (api.rest_invoke('POST', '/agents/' || v_id || '/pause', v_alice, NULL::bytea)).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: the manager pauses Bob''s Acme agent';
    END IF;
    v_response := api.rest_invoke('POST', '/connections/' || v_id || '/resume', v_bob, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 409
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'membership.connection.paused_by_manager'
       OR pg_temp.agent_refusal(v_handle) IS DISTINCT FROM 'membership.connection.paused' THEN
        RAISE EXCEPTION 'TEST FAILED: a manager''s pause is lifted only by a manager, got % %',
            (v_response).status_code, api.content_json((v_response).content);
    END IF;
    IF (api.rest_invoke('POST', '/agents/' || v_id || '/resume', v_alice, NULL::bytea)).status_code IS DISTINCT FROM 200
       OR pg_temp.agent_whoami(v_handle) ? 'error' THEN
        RAISE EXCEPTION 'TEST FAILED: the manager resumes Bob''s agent';
    END IF;
    v_response := api.rest_invoke('DELETE', '/agents/' || v_id, v_alice, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200
       OR api.content_json((v_response).content)->>'status' IS DISTINCT FROM 'revoked'
       OR pg_temp.agent_refusal(v_handle) IS DISTINCT FROM 'pgmi.connection_unknown'
       OR (pg_temp.agent_audit('membership.connection.revoked', v_id)).actual_principal_object_id IS DISTINCT FROM v_alice_id THEN
        RAISE EXCEPTION 'TEST FAILED: the manager revokes Bob''s agent for good, audited as Alice, got %', (v_response).status_code;
    END IF;
    IF (api.rest_invoke('POST', '/agents/' || v_id || '/resume', v_alice, NULL::bytea)).status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION 'TEST FAILED: a revoked connection is not found';
    END IF;

    RAISE DEBUG '  + pause and resume; managers reach their organization only';
END $$;

-- ----------------------------------------------------------------------------
-- The owner's switch, membership end, and agents never members
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme      uuid := current_setting('test.acme_org_id')::uuid;
    v_bob_id    uuid := pg_temp.test_person(current_setting('test.bob_subject'));
    v_bob       extensions.hstore := pg_temp.test_session_headers(current_setting('test.bob_subject'), v_acme);
    v_alice     extensions.hstore := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme);
    v_member    uuid;
    v_body      jsonb;
    v_handle    text;
    v_id        uuid;
    v_agent     uuid;
    v_response  api.http_response;
    v_state     text;
BEGIN
    RAISE DEBUG '-> Agent lifecycle: switch, membership end, never a member';

    v_body := api.content_json((pg_temp.agent_approve(v_bob, v_acme, 'agent.contributor', 'Bob switched')).content);
    v_handle := v_body->>'connection';
    v_id := (v_body->>'connectionId')::uuid;
    v_agent := (pg_temp.agent_whoami(v_handle)->'result'->'structuredContent'->>'actualPrincipalId')::uuid;

    PERFORM pg_temp.test_act_as(NULL);
    PERFORM pg_temp.test_as_kernel();
    SELECT m.membership_object_id INTO STRICT v_member FROM membership.vw_member m
    WHERE m.organization_object_id = v_acme AND m.principal_object_id = v_bob_id;
    PERFORM pg_temp.test_as_owner();
    PERFORM membership.fn_grant_role(v_member, 'org.admin');
    v_response := api.rest_invoke('POST', '/agents/disable', pg_temp.test_session_headers(current_setting('test.bob_subject'), v_acme),
                                  NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 403
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'membership.organization.owner_required' THEN
        RAISE EXCEPTION 'TEST FAILED: only the owner switches agents, an admin got %', (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('POST', '/agents/disable', v_alice, NULL::bytea);
    v_state := pg_temp.agent_status(v_id);
    IF (v_response).status_code IS DISTINCT FROM 200 OR api.content_json((v_response).content)->'agentsEnabled' IS DISTINCT FROM 'false'::jsonb
       OR pg_temp.agent_refusal(v_handle) IS DISTINCT FROM 'membership.connection.disabled'
       OR v_state IS DISTINCT FROM 'agents_disabled' THEN
        RAISE EXCEPTION 'TEST FAILED: the switch turns every agent off, got % %, status %',
            (v_response).status_code, pg_temp.agent_whoami(v_handle), v_state;
    END IF;
    v_response := pg_temp.agent_approve(v_bob, v_acme, 'agent.reader', 'While off');
    IF (v_response).status_code IS DISTINCT FROM 409
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'membership.connection.agents_disabled' THEN
        RAISE EXCEPTION 'TEST FAILED: no approval while the switch is off, got %', (v_response).status_code;
    END IF;
    PERFORM pg_temp.test_as_kernel();
    IF NOT EXISTS (SELECT FROM membership.audit_event
                   WHERE action = 'membership.organization.agents_disabled' AND organization_object_id = v_acme
                     AND actor_kind = 'human') THEN
        RAISE EXCEPTION 'TEST FAILED: switching agents off is audited';
    END IF;
    PERFORM pg_temp.test_as_owner();
    IF (api.rest_invoke('POST', '/agents/enable', v_alice, NULL::bytea)).status_code IS DISTINCT FROM 200
       OR pg_temp.agent_whoami(v_handle) ? 'error' THEN
        RAISE EXCEPTION 'TEST FAILED: switching agents back on restores them';
    END IF;

    PERFORM pg_temp.test_act_as(NULL);
    PERFORM membership.fn_end_membership(v_member, 'removed');
    v_state := pg_temp.agent_status(v_id);
    IF v_state IS DISTINCT FROM 'revoked'
       OR (pg_temp.agent_audit('membership.connection.revoked', v_id)).subject_principal_object_id IS DISTINCT FROM v_agent THEN
        RAISE EXCEPTION 'TEST FAILED: ending the membership revokes the person''s connection, audited on the agent: %', v_state;
    END IF;

    IF (membership.fn_add_member(v_acme, v_agent, NULL)).object_id IS NOT NULL
       OR (membership.fn_add_member(v_acme, v_bob_id, 'agent.reader')).object_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an agent is never a member, and an agent role never a member''s';
    END IF;
    v_member := (membership.fn_add_member(v_acme, v_bob_id, 'org.viewer')).object_id;
    IF membership.fn_role_problem(v_member, 'agent.reader') IS DISTINCT FROM 'membership.role.unknown'
       OR membership.fn_invitation_problem(v_acme, 'agent-invite@example.test', 'agent.reader')
          IS DISTINCT FROM 'membership.role.unknown' THEN
        RAISE EXCEPTION 'TEST FAILED: an agent role is unknown to role grants and invitations: % %',
            membership.fn_role_problem(v_member, 'agent.reader'),
            membership.fn_invitation_problem(v_acme, 'agent-invite@example.test', 'agent.reader');
    END IF;
    BEGIN
        PERFORM pg_temp.test_as_kernel();
        INSERT INTO membership.role_assignment (organization_object_id, membership_object_id, role_id, valid_during)
        SELECT m.organization_object_id, m.membership_object_id, 'agent.reader', tstzrange(clock_timestamp(), NULL)
        FROM membership.vw_member m WHERE m.organization_object_id = v_acme AND m.principal_object_id = v_bob_id;
        RAISE EXCEPTION 'TEST FAILED: the table accepted an agent role on a member';
    EXCEPTION WHEN check_violation THEN
        GET STACKED DIAGNOSTICS v_state = CONSTRAINT_NAME;
        IF v_state IS DISTINCT FROM 'role_assignment_member_role_check' THEN
            RAISE EXCEPTION 'TEST FAILED: an agent role on a member must break role_assignment_member_role_check, broke %', v_state;
        END IF;
    END;
    PERFORM pg_temp.test_as_owner();

    RAISE NOTICE '  + agent lifecycle: approval with role and organization, role within person, pause, resume, revoke, managers, the owner''s switch, membership end';
END $$;
