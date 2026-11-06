-- ============================================================================
-- Test: an MCP client acts as its connection's agent, for the person
-- ============================================================================
-- Approval creates the connection's agent principal. A call through the
-- connection has the person as its effective principal (row-level security
-- sees the person and the connection's organization), the agent as its actual
-- principal and actor kind agent, in the request context, the audit log,
-- the access-denial log and the exchange log (change tracking:
-- lib/__test__/test_change_tracking.sql). A person's own
-- call has actor kind human. A logged agent call replays only while its
-- connection is live, and an agent id is never an effective principal.
-- ============================================================================

CREATE FUNCTION pg_temp.mcp_whoami_agent(p_handle text)
RETURNS jsonb LANGUAGE sql AS $$
    SELECT (api.mcp_call_tool('ap_whoami', '{}'::jsonb, jsonb_build_object('connection', p_handle), '"w"'::jsonb))
           .envelope->'result'->'structuredContent';
$$;

DO $$
BEGIN
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'ffffffff-a9e0-4000-8000-000000000001', 'type', 'tool', 'effect', 'read', 'name', 'ap_whoami',
        'description', 'Who acts, for whom', 'access', 'authenticated'),
        $b$BEGIN
            RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('whoami')), (request).request_id,
                structured_content => jsonb_build_object(
                    'principalId', api.current_principal_id(),
                    'actualPrincipalId', api.current_actual_principal_id(),
                    'actorKind', api.current_actor_kind(),
                    'organizationId', api.current_organization_id(),
                    'currentUser', (SELECT u.principal_object_id FROM api.vw_current_user u),
                    'organizationsSeen', (SELECT jsonb_agg(o.object_id) FROM membership.organization o)));
        END;$b$);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'ffffffff-a9e0-4000-8000-000000000002', 'type', 'tool', 'effect', 'write', 'name', 'ap_act',
        'description', 'Grants a role', 'reversible', true, 'permissions', jsonb_build_array('members.manage'),
        'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object(
            'membershipId', jsonb_build_object('type', 'string')))),
        $b$BEGIN
            PERFORM membership.fn_grant_role(((request).arguments->>'membershipId')::uuid, 'org.viewer');
            RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('done')), (request).request_id);
        END;$b$);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'ffffffff-a9e0-4000-8000-000000000003', 'type', 'tool', 'effect', 'read', 'name', 'ap_platform',
        'description', 'Platform operation', 'permissions', jsonb_build_array('platform.operate')),
        $b$BEGIN
            RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ran')), (request).request_id);
        END;$b$);
    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'ffffffff-a9e0-4000-8000-000000000004', 'path', '/agent-probe', 'httpMethod', '^POST$',
        'name', 'agent_probe', 'access', 'authenticated', 'autoLog', true),
        $b$BEGIN
            RETURN api.json_response(200, jsonb_build_object(
                'principalId', api.current_principal_id(),
                'actualPrincipalId', api.current_actual_principal_id(),
                'actorKind', api.current_actor_kind()));
        END;$b$);
END $$;

DO $$
DECLARE
    v_acme      uuid := current_setting('test.acme_org_id')::uuid;
    v_alice_id  uuid := pg_temp.test_person(current_setting('test.alice_subject'));
    v_bob_id    uuid := pg_temp.test_person(current_setting('test.bob_subject'));
    v_web       extensions.hstore := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme);
    v_other     uuid;
    v_body      jsonb;
    v_handle    text;
    v_conn_id   uuid;
    v_agent     uuid;
    v_who       jsonb;
    v_envelope  jsonb;
    v_bob_mem   uuid;
    v_audit     membership.audit_event;
    v_denial    api.access_denial;
    v_context   jsonb;
    v_exchange  uuid;
    v_response  api.http_response;
BEGIN
    RAISE DEBUG '-> Agent principal: approval, context, RLS, every log, replay';

    v_other := (membership.fn_create_organization_with_owner('Agent Elsewhere', 'agent-elsewhere', v_alice_id)).object_id;

    -- Approval creates the agent, and the approving person is the actor.
    v_body := api.content_json((api.rest_invoke('POST', '/connections', v_web, jsonb_build_object('clientName', 'Agent probe', 'organizationId', v_acme, 'roleId', 'agent.delegate'))).content);
    v_handle := v_body->>'connection';
    v_conn_id := (v_body->>'connectionId')::uuid;
    PERFORM pg_temp.test_as_kernel();
    SELECT c.agent_principal_object_id INTO v_agent
    FROM membership.connection c
    JOIN membership.agent_principal a ON a.principal_object_id = c.agent_principal_object_id
    JOIN membership.principal p ON p.object_id = a.principal_object_id AND p.kind = 'agent'
    WHERE c.object_id = v_conn_id;
    SELECT * INTO v_audit FROM membership.audit_event
    WHERE action = 'membership.connection.approved' AND detail->>'connectionId' = v_conn_id::text;
    PERFORM pg_temp.test_as_owner();
    IF v_agent IS NULL OR v_agent IS NOT DISTINCT FROM v_alice_id THEN
        RAISE EXCEPTION 'TEST FAILED: approval must create the connection''s own agent principal, got %', v_agent;
    END IF;
    IF v_audit.actor_kind IS DISTINCT FROM 'human' OR v_audit.actual_principal_object_id IS DISTINCT FROM v_alice_id
       OR v_audit.subject_principal_object_id IS DISTINCT FROM v_agent THEN
        RAISE EXCEPTION 'TEST FAILED: the approval is audited as Alice, a human, about the agent: %', to_jsonb(v_audit);
    END IF;

    -- The context and row-level security: the person in the connection's organization.
    v_who := pg_temp.mcp_whoami_agent(v_handle);
    IF (v_who->>'principalId')::uuid IS DISTINCT FROM v_alice_id
       OR (v_who->>'actualPrincipalId')::uuid IS DISTINCT FROM v_agent
       OR v_who->>'actorKind' IS DISTINCT FROM 'agent'
       OR (v_who->>'organizationId')::uuid IS DISTINCT FROM v_acme THEN
        RAISE EXCEPTION 'TEST FAILED: an agent call is Alice (effective), her agent (actual), actor kind agent, in Acme: %', v_who;
    END IF;
    IF (v_who->>'currentUser')::uuid IS DISTINCT FROM v_alice_id
       OR (v_who->'organizationsSeen' @> to_jsonb(ARRAY[v_acme, v_other])) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: row-level security must see Alice and her organizations: %', v_who;
    END IF;

    -- The audit log: effective Alice, actual agent, actor kind agent.
    v_bob_mem := (SELECT m.object_id FROM membership.organization_membership m
                  WHERE m.organization_object_id = v_acme AND m.principal_object_id = v_bob_id AND upper_inf(m.valid_during));
    v_envelope := (api.mcp_call_tool('ap_act', jsonb_build_object('membershipId', v_bob_mem),
                                     jsonb_build_object('connection', v_handle), '"a"'::jsonb)).envelope;
    IF v_envelope ? 'error' OR (v_envelope->'result'->>'isError')::boolean THEN
        RAISE EXCEPTION 'TEST FAILED: the agent''s write tool failed: %', v_envelope;
    END IF;
    PERFORM pg_temp.test_as_kernel();
    SELECT * INTO v_audit FROM membership.audit_event
    WHERE action = 'membership.role.granted' AND subject_principal_object_id = v_bob_id
    ORDER BY object_id DESC LIMIT 1;
    PERFORM pg_temp.test_as_owner();
    IF v_audit.actual_principal_object_id IS DISTINCT FROM v_agent
       OR v_audit.effective_principal_object_id IS DISTINCT FROM v_alice_id
       OR v_audit.actor_kind IS DISTINCT FROM 'agent' THEN
        RAISE EXCEPTION 'TEST FAILED: the agent''s grant is audited as the agent acting for Alice: %', to_jsonb(v_audit);
    END IF;

    -- The access-denial log: a platform permission is refused to the agent.
    v_envelope := (api.mcp_call_tool('ap_platform', '{}'::jsonb, jsonb_build_object('connection', v_handle), '"p"'::jsonb)).envelope;
    SELECT * INTO v_denial FROM api.access_denial
    WHERE handler_object_id = 'ffffffff-a9e0-4000-8000-000000000003';
    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -31003
       OR v_denial.actual_principal_object_id IS DISTINCT FROM v_agent
       OR v_denial.effective_principal_object_id IS DISTINCT FROM v_alice_id
       OR v_denial.actor_kind IS DISTINCT FROM 'agent' THEN
        RAISE EXCEPTION 'TEST FAILED: the agent''s refusal is logged as the agent acting for Alice: % %', v_envelope, to_jsonb(v_denial);
    END IF;

    -- The exchange log context.
    SELECT e.context INTO v_context FROM api.mcp_exchange e
    WHERE e.handler_object_id = 'ffffffff-a9e0-4000-8000-000000000001'
    ORDER BY e.sequence_number DESC LIMIT 1;
    IF v_context IS DISTINCT FROM jsonb_build_object('principalId', v_alice_id, 'actualPrincipalId', v_agent,
                                                     'actorKind', 'agent', 'organizationId', v_acme) THEN
        RAISE EXCEPTION 'TEST FAILED: the exchange log keeps both principals and actor kind agent, got %', v_context;
    END IF;

    -- A person's own call is human.
    v_response := api.rest_invoke('POST', '/agent-probe', v_web, NULL::bytea);
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 200
       OR v_body IS DISTINCT FROM jsonb_build_object('principalId', v_alice_id, 'actualPrincipalId', v_alice_id, 'actorKind', 'human') THEN
        RAISE EXCEPTION 'TEST FAILED: Alice''s own call is Alice acting as herself, a human, got % %', (v_response).status_code, v_body;
    END IF;

    -- Replay re-checks the connection: live, the agent acts; revoked, no one.
    SELECT e.object_id INTO v_exchange FROM api.rest_exchange e
    WHERE e.handler_object_id = 'ffffffff-a9e0-4000-8000-000000000004'
    ORDER BY e.sequence_number DESC LIMIT 1;
    UPDATE api.rest_exchange SET context = v_context WHERE object_id = v_exchange;
    v_body := api.content_json((api.replay_exchange(v_exchange)).content);
    IF v_body IS DISTINCT FROM jsonb_build_object('principalId', v_alice_id, 'actualPrincipalId', v_agent, 'actorKind', 'agent') THEN
        RAISE EXCEPTION 'TEST FAILED: a replayed agent exchange acts as the agent for Alice while the connection is live, got %', v_body;
    END IF;
    IF (api.rest_invoke('DELETE', '/connections/' || v_conn_id, v_web, NULL::bytea)).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: Alice could not revoke her connection';
    END IF;
    v_response := api.replay_exchange(v_exchange);
    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: a revoked agent''s exchange must replay as no one (401), got % %',
            (v_response).status_code, api.content_json((v_response).content);
    END IF;

    -- An agent id names no one on its own, and no one else's agent counts.
    PERFORM set_config('auth.principal_id', v_agent::text, true),
            set_config('auth.actual_principal_id', '', true),
            set_config('auth.ctx', '', true);
    IF api.current_principal_id() IS NOT NULL OR api.current_actor_kind() IS DISTINCT FROM 'system' THEN
        RAISE EXCEPTION 'TEST FAILED: an agent is never an effective principal, got % %', api.current_principal_id(), api.current_actor_kind();
    END IF;
    v_handle := api.content_json((api.rest_invoke('POST', '/connections', v_web, jsonb_build_object('clientName', 'Agent probe 2', 'organizationId', v_acme, 'roleId', 'agent.delegate'))).content)->>'connection';
    v_agent := (pg_temp.mcp_whoami_agent(v_handle)->>'actualPrincipalId')::uuid;
    PERFORM api.rest_invoke('POST', '/agent-probe', v_web, NULL::bytea);
    PERFORM set_config('auth.actual_principal_id', v_agent::text, true);
    IF api.current_actual_principal_id() IS DISTINCT FROM v_agent OR api.current_actor_kind() IS DISTINCT FROM 'agent' THEN
        RAISE EXCEPTION 'TEST FAILED: Alice''s snapshot must not answer for her agent, got % %',
            api.current_actual_principal_id(), api.current_actor_kind();
    END IF;
    PERFORM set_config('auth.principal_id', v_bob_id::text, true),
            set_config('auth.actual_principal_id', v_agent::text, true),
            set_config('auth.organization_id', v_acme::text, true),
            set_config('auth.ctx', '', true);
    IF api.current_principal_id() IS NOT NULL OR api.current_permissions() IS DISTINCT FROM '{}'::text[] THEN
        RAISE EXCEPTION 'TEST FAILED: Alice''s agent must not act for Bob, got % %', api.current_principal_id(), api.current_permissions();
    END IF;
    PERFORM pg_temp.test_act_as(NULL);

    RAISE NOTICE '  + agent principal: an agent call is the person (effective), the agent (actual), actor kind agent, in context, RLS and every log; replay re-checks the connection';
END $$;
