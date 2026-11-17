/*
<pgmi-meta
    id="7b3e9c41-2d58-4f0a-b6e1-9a4c5d8f2e17"
    idempotent="true">
  <description>
    organization.transfer_ownership, a JSON-RPC action. Ownership is no
    resource a client could PUT: one step closes the owner's org.owner, opens
    the new owner's and may leave the previous owner org.admin, so it is a
    named action over one kernel, membership.fn_transfer_ownership.
  </description>
  <sortKeys>
    <key>005/006</key>
  </sortKeys>
</pgmi-meta>
*/

SELECT api.create_or_replace_rpc_handler(
    jsonb_build_object(
        'id', 'e1000006-0001-4000-8000-000000000001',
        'methodName', 'organization.transfer_ownership',
        'description', 'Make another member the owner of the request''s organization; only the current owner may',
        'permissions', jsonb_build_array('members.manage'),
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'additionalProperties', false,
            'required', jsonb_build_array('newOwnerId'),
            'properties', jsonb_build_object(
                'newOwnerId', jsonb_build_object('type', 'string', 'format', 'uuid',
                    'description', 'The new owner''s principalId, from GET /members'),
                'previousOwnerBecomesAdmin', jsonb_build_object('type', 'boolean',
                    'description', 'Keep the previous owner as org.admin; default true'))),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'organizationId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'ownerId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'previousOwnerIsAdmin', jsonb_build_object('type', 'boolean')),
            'required', jsonb_build_array('organizationId', 'ownerId', 'previousOwnerIsAdmin'))
    ),
    $body$
DECLARE
    v_id         jsonb := api.content_json((request).content)->'id';
    v_params     jsonb := api.content_json((request).content)->'params';
    v_org        uuid := api.current_organization_id();
    v_new_owner  uuid := (v_params->>'newOwnerId') ?> NULL::uuid;
    v_keep_admin boolean := coalesce((v_params->'previousOwnerBecomesAdmin')::boolean, true);
    v_problem    text;
    v_moved      membership.organization;
BEGIN
    IF v_new_owner IS NULL THEN
        RETURN api.jsonrpc_error(-32011, 'Invalid params', v_id, jsonb_build_object('invalid-params',
            jsonb_build_array(jsonb_build_object('name', 'newOwnerId', 'reason', 'invalid'))));
    END IF;

    v_problem := membership.fn_transfer_ownership_problem(v_org, v_new_owner);
    IF v_problem IS NULL THEN
        v_moved := membership.fn_transfer_ownership(v_org, v_new_owner, v_keep_admin);
        IF v_moved.object_id IS NULL THEN
            v_problem := membership.fn_transfer_ownership_problem(v_org, v_new_owner);
        END IF;
    END IF;
    IF v_problem = 'membership.ownership.not_owner' THEN
        RETURN api.jsonrpc_error(-32010, 'Only the organization''s owner can transfer its ownership', v_id,
            jsonb_build_object('code', v_problem));
    ELSIF v_problem = 'membership.ownership.new_owner_not_member' THEN
        RETURN api.jsonrpc_error(-32011, 'Invalid params', v_id, jsonb_build_object('invalid-params',
            jsonb_build_array(jsonb_build_object('name', 'newOwnerId', 'reason', 'not_found'))));
    ELSIF v_problem IS NOT NULL OR v_moved.object_id IS NULL THEN
        RETURN api.jsonrpc_error(-32010, 'Ownership cannot move', v_id,
            jsonb_build_object('code', coalesce(v_problem, 'membership.ownership.refused')));
    END IF;

    RETURN api.jsonrpc_success(jsonb_build_object(
        'organizationId', v_org, 'ownerId', v_new_owner, 'previousOwnerIsAdmin', v_keep_admin), v_id);
END;
    $body$
);
