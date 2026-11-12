-- ============================================================================
-- Test: proposals (an agent proposes a write, a person confirms it)
-- ============================================================================
-- A proposal tool records a pending proposal and changes nothing else; it is
-- called under its own permission, so a role that may not write may propose.
-- Only a person acting as themselves, holding proposals.decide, decides; a
-- confirm runs the target tool through the MCP path as that person, so the
-- target's own permission applies to the confirmer. A decided proposal is
-- never decided again, and a target that refuses leaves it pending.
-- ============================================================================

CREATE FUNCTION pg_temp.tp_proposal(p_id uuid)
RETURNS membership.proposal LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_row    membership.proposal;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    SELECT * INTO v_row FROM membership.proposal WHERE object_id = p_id;
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN v_row;
END $$;

CREATE FUNCTION pg_temp.tp_propose(p_context jsonb, p_title text)
RETURNS uuid LANGUAGE sql AS $$
    SELECT ((api.mcp_call_tool('tp_propose_probe', jsonb_build_object('title', p_title), p_context, '"p"')).envelope
            ->'result'->'structuredContent'->>'proposalId')::uuid;
$$;

CREATE TABLE core.proposal_probe (principal_id uuid, title text);

SELECT api.create_or_replace_mcp_handler(jsonb_build_object(
    'id', 'f4570000-0000-4000-8000-000000000001', 'type', 'tool', 'effect', 'write', 'reversible', true,
    'name', 'tp_create_probe', 'description', 'Creates a probe row as its caller',
    'permissions', jsonb_build_array('task.write'),
    'inputSchema', jsonb_build_object('type', 'object', 'required', jsonb_build_array('title'),
                                      'properties', jsonb_build_object('title', jsonb_build_object('type', 'string')))),
    $body$
BEGIN
    IF (request).arguments->>'title' = 'refuse' THEN
        RETURN api.mcp_tool_result('[{"type": "text", "text": "{\"code\": \"probe.refused\"}"}]', (request).request_id, true);
    END IF;
    INSERT INTO core.proposal_probe VALUES (api.current_principal_id(), (request).arguments->>'title');
    RETURN api.mcp_tool_result('[]', (request).request_id, false, jsonb_build_object('created', true));
END;
$body$);

SELECT api.create_or_replace_mcp_handler(jsonb_build_object(
    'id', 'f4570000-0000-4000-8000-000000000002', 'type', 'tool', 'effect', 'proposal', 'proposes', 'tp_create_probe',
    'name', 'tp_propose_probe', 'description', 'Proposes a probe row for a person to confirm',
    'permissions', jsonb_build_array('task.propose'),
    'inputSchema', jsonb_build_object('type', 'object', 'required', jsonb_build_array('title'),
                                      'properties', jsonb_build_object('title', jsonb_build_object('type', 'string')))),
    $body$
DECLARE
    v_id uuid := membership.fn_propose('tp_create_probe', (request).arguments);
BEGIN
    RETURN api.mcp_tool_result('[]', (request).request_id, v_id IS NULL, jsonb_build_object('proposalId', v_id));
END;
$body$);

SELECT api.create_or_replace_mcp_handler(jsonb_build_object(
    'id', 'f4570000-0000-4000-8000-000000000003', 'type', 'tool', 'effect', 'write', 'reversible', false,
    'name', 'tp_decide', 'description', 'An agent trying to confirm a proposal',
    'permissions', jsonb_build_array('proposals.decide'),
    'inputSchema', jsonb_build_object('type', 'object', 'required', jsonb_build_array('id'),
                                      'properties', jsonb_build_object('id', jsonb_build_object('type', 'string')))),
    $body$
DECLARE
    v_decision record;
BEGIN
    SELECT * INTO v_decision FROM membership.fn_decide_proposal((request).arguments->>'id' ?> NULL::uuid, true);
    RETURN api.mcp_tool_result('[]', (request).request_id, (v_decision.proposal).object_id IS NULL);
END;
$body$);

DO $$
DECLARE
    v_corp     uuid := current_setting('test.corp_id')::uuid;
    v_owner_id uuid := current_setting('test.owner_id')::uuid;
    v_agent    jsonb := pg_temp.test_mcp_context('test|alice-001', current_setting('test.corp_id')::uuid, 'agent.reader');
    v_delegate jsonb := pg_temp.test_mcp_context('test|owner-001', current_setting('test.corp_id')::uuid, 'agent.delegate');
    v_owner    extensions.hstore := pg_temp.test_session_headers('test|owner-001', current_setting('test.corp_id')::uuid);
    v_alice    extensions.hstore := pg_temp.test_session_headers('test|alice-001', current_setting('test.corp_id')::uuid);
    v_tool     jsonb;
    v_envelope jsonb;
    v_response api.http_response;
    v_body     jsonb;
    v_first    uuid;
    v_second   uuid;
    v_refused  uuid;
    v_row      membership.proposal;
BEGIN
    RAISE DEBUG '-> Proposals';

    SELECT jsonb_build_object('effect', r.effect, 'annotations', r.annotations) INTO v_tool
    FROM api.mcp_route r WHERE r.mcp_name = 'tp_propose_probe';
    IF v_tool->'effect' IS DISTINCT FROM '{"effect": "proposal", "proposes": "tp_create_probe"}'::jsonb
       OR v_tool->'annotations' IS DISTINCT FROM
          '{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false}'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a proposal tool publishes its effect and target, got %', v_tool;
    END IF;

    v_envelope := (api.mcp_call_tool('tp_create_probe', '{"title": "direct"}', v_agent, '"d"')).envelope;
    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -31003 THEN
        RAISE EXCEPTION 'TEST FAILED: agent.reader may not call the write tool itself, got %', v_envelope;
    END IF;

    v_first := pg_temp.tp_propose(v_agent, 'first');
    v_second := pg_temp.tp_propose(v_agent, 'second');
    v_refused := pg_temp.tp_propose(v_agent, 'refuse');
    v_row := pg_temp.tp_proposal(v_first);
    IF v_first IS NULL OR v_row.status IS DISTINCT FROM 'pending' OR v_row.tool_name IS DISTINCT FROM 'tp_create_probe'
       OR v_row.arguments IS DISTINCT FROM '{"title": "first"}'::jsonb OR v_row.proposer_kind IS DISTINCT FROM 'agent'
       OR v_row.organization_object_id IS DISTINCT FROM v_corp
       OR v_row.proposed_by_object_id IS NOT DISTINCT FROM current_setting('test.alice_id')::uuid
       OR v_row.decided_by_object_id IS NOT NULL OR v_row.decided_at IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: agent.reader records one pending proposal by its agent, got %', row_to_json(v_row);
    END IF;
    IF EXISTS (SELECT FROM core.proposal_probe) THEN
        RAISE EXCEPTION 'TEST FAILED: proposing changes nothing';
    END IF;

    v_body := api.content_json((api.rest_invoke('GET', '/proposals', v_owner, NULL::bytea)).content);
    IF (v_body->'items' @> jsonb_build_array(jsonb_build_object('proposalId', v_first, 'toolName', 'tp_create_probe',
                                                                 'status', 'pending', 'proposerKind', 'agent'))) IS NOT TRUE
       OR (v_body->>'total')::int IS DISTINCT FROM 3 THEN
        RAISE EXCEPTION 'TEST FAILED: GET /proposals lists the organization''s pending proposals, got %', v_body;
    END IF;

    v_envelope := (api.mcp_call_tool('tp_decide', jsonb_build_object('id', v_first), v_delegate, '"x"')).envelope;
    IF v_envelope->'result'->'isError' IS DISTINCT FROM 'true'::jsonb
       OR (pg_temp.tp_proposal(v_first)).status IS DISTINCT FROM 'pending' OR EXISTS (SELECT FROM core.proposal_probe) THEN
        RAISE EXCEPTION 'TEST FAILED: an agent, even one holding proposals.decide, never confirms, got %', v_envelope;
    END IF;

    v_response := api.rest_invoke('POST', '/proposals/' || v_first || '/confirm', v_alice, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 403
       OR (pg_temp.tp_proposal(v_first)).status IS DISTINCT FROM 'pending' OR EXISTS (SELECT FROM core.proposal_probe) THEN
        RAISE EXCEPTION 'TEST FAILED: a person without proposals.decide cannot confirm, got %', (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('POST', '/proposals/' || v_second || '/reject', v_owner, NULL::bytea);
    v_row := pg_temp.tp_proposal(v_second);
    IF (v_response).status_code IS DISTINCT FROM 200 OR v_row.status IS DISTINCT FROM 'rejected'
       OR v_row.decided_by_object_id IS DISTINCT FROM v_owner_id OR v_row.decided_at IS NULL
       OR api.content_json((v_response).content)->>'status' IS DISTINCT FROM 'rejected'
       OR EXISTS (SELECT FROM core.proposal_probe) THEN
        RAISE EXCEPTION 'TEST FAILED: rejecting records the decision and changes nothing, got % %',
            (v_response).status_code, row_to_json(v_row);
    END IF;

    v_response := api.rest_invoke('POST', '/proposals/' || v_first || '/confirm', v_owner, NULL::bytea);
    v_row := pg_temp.tp_proposal(v_first);
    IF (v_response).status_code IS DISTINCT FROM 200 OR v_row.status IS DISTINCT FROM 'confirmed'
       OR v_row.decided_by_object_id IS DISTINCT FROM v_owner_id
       OR api.content_json((v_response).content)->'answer'->'result'->'structuredContent' IS DISTINCT FROM '{"created": true}'::jsonb
       OR (SELECT array_agg(principal_id::text || ':' || title) FROM core.proposal_probe)
          IS DISTINCT FROM ARRAY[v_owner_id::text || ':first'] THEN
        RAISE EXCEPTION 'TEST FAILED: confirming runs the target as the confirming person, got % % %',
            (v_response).status_code, api.content_json((v_response).content), row_to_json(v_row);
    END IF;

    v_response := api.rest_invoke('POST', '/proposals/' || v_first || '/confirm', v_owner, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 409
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'membership.proposal.decided'
       OR (SELECT count(*) FROM core.proposal_probe) IS DISTINCT FROM 1::bigint THEN
        RAISE EXCEPTION 'TEST FAILED: a confirmed proposal is never confirmed again, got %', (v_response).status_code;
    END IF;
    v_response := api.rest_invoke('POST', '/proposals/' || v_second || '/confirm', v_owner, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 409 OR (pg_temp.tp_proposal(v_second)).status IS DISTINCT FROM 'rejected'
       OR (SELECT count(*) FROM core.proposal_probe) IS DISTINCT FROM 1::bigint THEN
        RAISE EXCEPTION 'TEST FAILED: a rejected proposal is never confirmed, got %', (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('POST', '/proposals/' || v_refused || '/confirm', v_owner, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 409
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'membership.proposal.tool_refused'
       OR api.content_json((v_response).content)->'answer'->'result'->'isError' IS DISTINCT FROM 'true'::jsonb
       OR (pg_temp.tp_proposal(v_refused)).status IS DISTINCT FROM 'pending' THEN
        RAISE EXCEPTION 'TEST FAILED: a target that refuses leaves the proposal pending and answers why, got % %',
            (v_response).status_code, api.content_json((v_response).content);
    END IF;

    v_response := api.rest_invoke('POST', '/proposals/' || gen_random_uuid() || '/confirm', v_owner, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 404
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'membership.proposal.not_found' THEN
        RAISE EXCEPTION 'TEST FAILED: an unknown proposal is 404, got %', (v_response).status_code;
    END IF;

    PERFORM pg_temp.test_act_as(NULL);
    RAISE NOTICE '  ✓ proposals: an agent proposes, only a person with proposals.decide confirms, once, as themselves';
END $$;

DO $$
DECLARE
    v_message text;
BEGIN
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'f4570000-0000-4000-8000-000000000011', 'type', 'tool', 'effect', 'write', 'reversible', true,
        'name', 'tp_serial_target', 'permissions', jsonb_build_array('task.write'), 'description', 'A write that needs serializable',
        'minTransactionIsolation', 'serializable'),
        $body$ BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END; $body$);
    v_message := NULL;
    BEGIN
        PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
            'id', 'f4570000-0000-4000-8000-000000000012', 'type', 'tool', 'effect', 'proposal',
            'proposes', 'tp_serial_target', 'name', 'tp_propose_serial', 'permissions', jsonb_build_array('task.write'), 'description', 'Proposes a serializable write'),
            $body$ BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END; $body$);
    EXCEPTION WHEN invalid_parameter_value THEN
        v_message := SQLERRM;
    END;
    IF coalesce(v_message, '') NOT LIKE '%"tp_propose_serial" proposes "tp_serial_target"%serializable%' THEN
        RAISE EXCEPTION 'TEST FAILED: a tool above read committed cannot be proposed, got %', COALESCE(v_message, 'no error');
    END IF;

    v_message := NULL;
    BEGIN
        PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
            'id', 'f4570000-0000-4000-8000-000000000001', 'type', 'tool', 'effect', 'write', 'reversible', true,
            'name', 'tp_create_probe', 'description', 'Creates a probe row as its caller',
            'permissions', jsonb_build_array('task.write'), 'minTransactionIsolation', 'repeatable read',
            'inputSchema', jsonb_build_object('type', 'object')),
            $body$ BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END; $body$);
    EXCEPTION WHEN invalid_parameter_value THEN
        v_message := SQLERRM;
    END;
    IF coalesce(v_message, '') NOT LIKE '%"tp_create_probe" declares minTransactionIsolation repeatable read%"tp_propose_probe"%' THEN
        RAISE EXCEPTION 'TEST FAILED: a proposed tool cannot raise its floor above read committed, got %', COALESCE(v_message, 'no error');
    END IF;

    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'f4570000-0000-4000-8000-000000000013', 'type', 'tool', 'effect', 'write', 'reversible', true,
        'name', 'tp_committed_target', 'permissions', jsonb_build_array('task.write'), 'description', 'A write at read committed',
        'minTransactionIsolation', 'read committed'),
        $body$ BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END; $body$);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'f4570000-0000-4000-8000-000000000014', 'type', 'tool', 'effect', 'proposal',
        'proposes', 'tp_committed_target', 'name', 'tp_propose_committed', 'permissions', jsonb_build_array('task.write'), 'description', 'Proposes a read committed write'),
        $body$ BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END; $body$);
END $$;

SELECT api.create_or_replace_mcp_handler(jsonb_build_object(
    'id', 'f4570000-0000-4000-8000-000000000021', 'type', 'tool', 'effect', 'write', 'reversible', true,
    'name', 'tp_operate_probe', 'description', 'A write only a platform operator may make',
    'permissions', jsonb_build_array('platform.operate')),
    $body$ BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END; $body$);

SELECT api.create_or_replace_mcp_handler(jsonb_build_object(
    'id', 'f4570000-0000-4000-8000-000000000022', 'type', 'tool', 'effect', 'proposal', 'proposes', 'tp_operate_probe',
    'name', 'tp_propose_operate', 'description', 'Proposes a platform write',
    'permissions', jsonb_build_array('task.propose')),
    $body$
DECLARE
    v_id uuid := membership.fn_propose('tp_operate_probe', (request).arguments);
BEGIN
    RETURN api.mcp_tool_result('[]', (request).request_id, v_id IS NULL, jsonb_build_object('proposalId', v_id));
END;
$body$);

DO $$
DECLARE
    v_agent    jsonb := pg_temp.test_mcp_context('test|alice-001', current_setting('test.corp_id')::uuid, 'agent.reader');
    v_owner    extensions.hstore := pg_temp.test_session_headers('test|owner-001', current_setting('test.corp_id')::uuid);
    v_id       uuid;
    v_response api.http_response;
    v_logged   jsonb;
BEGIN
    v_id := ((api.mcp_call_tool('tp_propose_operate', '{}', v_agent, '"o"')).envelope
             ->'result'->'structuredContent'->>'proposalId')::uuid;
    v_response := api.rest_invoke('POST', '/proposals/' || v_id || '/confirm', v_owner, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 409
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'membership.proposal.tool_refused'
       OR coalesce((api.content_json((v_response).content)->'answer')::text, '') NOT LIKE '%"pgmi.forbidden"%'
       OR (pg_temp.tp_proposal(v_id)).status IS DISTINCT FROM 'pending' THEN
        RAISE EXCEPTION 'TEST FAILED: a confirmer without the target''s permission gets 409 with the nested refusal, got % %',
            (v_response).status_code, api.content_json((v_response).content);
    END IF;

    SELECT api.content_json((e.response).content) INTO v_logged
    FROM api.rest_exchange e
    WHERE (e.request).url LIKE '%/proposals/' || v_id || '/confirm'
    ORDER BY e.sequence_number DESC
    LIMIT 1;
    IF coalesce((v_logged->'answer')::text, '') NOT LIKE '%"pgmi.forbidden"%' THEN
        RAISE EXCEPTION 'TEST FAILED: the confirm request''s exchange row records the nested refusal, got %', v_logged;
    END IF;
    IF EXISTS (SELECT FROM api.access_denial d JOIN api.mcp_route r ON r.handler_object_id = d.handler_object_id
               WHERE r.mcp_name = 'tp_operate_probe')
       OR EXISTS (SELECT FROM api.mcp_exchange x JOIN api.mcp_route r ON r.handler_object_id = x.handler_object_id
                  WHERE r.mcp_name = 'tp_operate_probe') THEN
        RAISE EXCEPTION 'TEST FAILED: the nested refusal rolls back with the 409, so it leaves no access_denial or mcp_exchange row; if that changed, update the README audit note';
    END IF;
END $$;
