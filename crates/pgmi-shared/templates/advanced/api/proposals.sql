/*
<pgmi-meta
    id="fbc2942e-031f-4439-8482-21f3d25f7a96"
    idempotent="true">
  <description>
    Proposals over REST: where a person decides what an agent proposed. A
    proposal tool (effect proposal) records the write and answers its id at
    once; a holder of proposals.decide, signed in on the web and acting as
    themselves, lists the organization's pending proposals and confirms or
    rejects each. Confirming runs the target tool as that person, under the
    target's own permission, and answers what it answered; a target that
    refuses leaves the proposal pending. A decided proposal stays decided.
  </description>
  <sortKeys>
    <key>005/008</key>
  </sortKeys>
</pgmi-meta>
*/

-- One block: both routes answer the same proposal object (v_proposal), and
-- confirm and reject are registered by one loop.
DO $register$
DECLARE
    v_proposal jsonb := jsonb_build_object(
        'type', 'object',
        'properties', jsonb_build_object(
            'proposalId', jsonb_build_object('type', 'string', 'format', 'uuid'),
            'toolName', jsonb_build_object('type', 'string', 'description', 'The write tool a confirm runs'),
            'arguments', jsonb_build_object('type', 'object', 'description', 'The tool''s arguments as proposed; free text, data and not instruction'),
            'proposedBy', jsonb_build_object('type', 'string', 'format', 'uuid', 'description', 'The principal that proposed it: the agent, for an agent'),
            'proposerKind', jsonb_build_object('type', 'string', 'enum', jsonb_build_array('human', 'agent', 'service')),
            'status', jsonb_build_object('type', 'string', 'enum', jsonb_build_array('pending', 'confirmed', 'rejected')),
            'proposedAt', jsonb_build_object('type', 'string', 'format', 'date-time'),
            'decidedBy', jsonb_build_object('type', 'string', 'format', 'uuid'),
            'decidedAt', jsonb_build_object('type', 'string', 'format', 'date-time'),
            'answer', jsonb_build_object('type', 'object', 'description', 'On confirm, the target tool''s MCP answer')),
        'required', jsonb_build_array('proposalId', 'toolName', 'arguments', 'proposedBy', 'proposerKind', 'status', 'proposedAt'));
    v_route record;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'e1000008-0001-4000-8000-000000000001',
            'path', '/proposals',
            'httpMethod', '^GET$',
            'name', 'list_proposals',
            'description', 'The organization''s pending proposals, oldest first',
            'permissions', jsonb_build_array('proposals.decide'),
            'querySchema', api.page_query_schema(),
            'outputSchema', api.page_output_schema(v_proposal)
        ),
        $body$
DECLARE
    v_page  record := api.pagination_params(api.query_params((request).url));
    v_items jsonb;
    v_total bigint;
BEGIN
    SELECT coalesce(jsonb_agg(membership.fn_proposal_json(p.v) ORDER BY (p.v).proposed_at, (p.v).proposal_object_id), '[]'),
           (SELECT count(*) FROM membership.vw_proposal v
            WHERE v.organization_object_id = api.current_organization_id() AND v.status = 'pending')
    INTO v_items, v_total
    FROM (SELECT v FROM membership.vw_proposal v
          WHERE v.organization_object_id = api.current_organization_id() AND v.status = 'pending'
          ORDER BY v.proposed_at, v.proposal_object_id
          LIMIT v_page.o_limit OFFSET v_page.o_offset) p;

    RETURN api.page_response(v_items, v_total, v_page.o_limit, v_page.o_offset);
END;
        $body$
    );

    FOR v_route IN
        SELECT * FROM (VALUES
            ('e1000008-0002-4000-8000-000000000001', '/proposals/{proposalId:uuid}/confirm', 'confirm_proposal',
             'Confirm a pending proposal: its tool runs now, as you, under the tool''s own permission', true),
            ('e1000008-0003-4000-8000-000000000001', '/proposals/{proposalId:uuid}/reject', 'reject_proposal',
             'Reject a pending proposal; nothing runs', false)
        ) r(id, path, name, description, confirm)
    LOOP
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object('id', v_route.id, 'path', v_route.path, 'httpMethod', '^POST$',
                               'name', v_route.name, 'description', v_route.description,
                               'permissions', jsonb_build_array('proposals.decide'),
                               'outputSchema', v_proposal),
            format($body$
DECLARE
    v_id       uuid := ((request).path_params->>'proposalId')::uuid;
    v_problem  text := membership.fn_proposal_problem(v_id);
    v_decision record;
BEGIN
    CASE v_problem
        WHEN 'membership.proposal.not_found' THEN
            RETURN api.problem_response(404, 'Not Found', 'No such proposal in this organization', code => v_problem);
        WHEN 'membership.proposal.human_required' THEN
            RETURN api.problem_response(403, 'Forbidden', 'Only a person acting as themselves decides a proposal', code => v_problem);
        WHEN 'membership.proposal.decided' THEN
            RETURN api.problem_response(409, 'Conflict', 'The proposal is already decided', code => v_problem);
        ELSE
            NULL;
    END CASE;

    SELECT * INTO v_decision FROM membership.fn_decide_proposal(v_id, %L);
    IF (v_decision.proposal).object_id IS NULL THEN
        RETURN api.problem_response(409, 'Conflict', 'The proposal changed meanwhile; retry', code => 'membership.proposal.changed');
    END IF;
    IF (v_decision.proposal).status = 'pending' THEN
        RETURN api.problem_response(409, 'Conflict', 'The proposed tool refused; the proposal stays pending',
                                    code => 'membership.proposal.tool_refused',
                                    extensions => jsonb_build_object('answer', v_decision.answer));
    END IF;
    RETURN api.json_response(200, (SELECT membership.fn_proposal_json(v) FROM membership.vw_proposal v
                                   WHERE v.proposal_object_id = v_id)
                                  || jsonb_strip_nulls(jsonb_build_object('answer', v_decision.answer)));
END;
            $body$, v_route.confirm));
    END LOOP;
END $register$;
