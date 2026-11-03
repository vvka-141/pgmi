/*
<pgmi-meta
    id="a8a9fc7b-146e-4be7-8353-c51d3b6ced2b"
    idempotent="true">
  <description>
    Connections over REST: how a person lets an MCP client act for them, and
    how an organization keeps its agents in hand. Signed in on the web, the
    person approves a connection for a client by name, naming the
    organization it acts in and its agent role (agent.* in
    data/authorization.json, never broader than what the person holds
    there); the answer carries the handle once, and the client sends it as
    X-Connection to the MCP gateway. An OAuth client needs no handle: its
    first call answers a pairing code, and the person approves it with
    POST /connections/approve, naming organization and agent role the same
    way. The person lists, pauses, resumes and
    revokes their own connections under /connections. Under /agents, a
    holder of members.manage lists, pauses, resumes and revokes the
    connections into the request's organization, and its owner switches
    every agent off and on. The handle is never shown again.
  </description>
  <sortKeys>
    <key>005/004</key>
  </sortKeys>
</pgmi-meta>
*/

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000004-0001-4000-8000-000000000001',
        'path', '/connections',
        'httpMethod', '^POST$',
        'name', 'approve_connection',
        'description', 'Approve a connection for an MCP client in one of your organizations, with an agent role; returns its handle once',
        'access', 'authenticated',
        'successStatus', 201,
        'autoLog', false,
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'clientName', jsonb_build_object('type', 'string', 'minLength', 1, 'maxLength', 100),
                'organizationId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'roleId', jsonb_build_object('type', 'string'),
                'validDays', jsonb_build_object('type', 'integer', 'minimum', 1, 'maximum', 365, 'default', 90)),
            'required', jsonb_build_array('clientName', 'organizationId', 'roleId')),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'connectionId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'connection', jsonb_build_object('type', 'string'),
                'organizationId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'clientName', jsonb_build_object('type', 'string'),
                'roleId', jsonb_build_object('type', 'string'),
                'expiresAt', jsonb_build_object('type', 'string', 'format', 'date-time')),
            'required', jsonb_build_array('connectionId', 'connection', 'organizationId', 'clientName', 'roleId', 'expiresAt'))
    ),
    $body$
DECLARE
    v_b         jsonb := api.content_json((request).content);
    v_malformed jsonb := api.malformed_params(v_b, '{"organizationId": "uuid", "validDays": "integer"}');
    v_approved  record;
BEGIN
    IF v_malformed IS NOT NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Content', 'organizationId must be a uuid, validDays an integer',
                                    invalid_params => v_malformed);
    END IF;

    SELECT * INTO v_approved FROM membership.fn_approve_connection(
        common.try_cast(v_b->>'organizationId', NULL::uuid), v_b->>'roleId', v_b->>'clientName',
        coalesce(common.try_cast(v_b->>'validDays', NULL::int), 90));
    CASE v_approved.problem
        WHEN 'membership.connection.login_session_required' THEN
            RETURN api.problem_response(403, 'Forbidden', 'Only a person signed in on the web approves a connection',
                                        code => v_approved.problem);
        WHEN 'membership.connection.client_name_invalid' THEN
            RETURN api.problem_response(422, 'Unprocessable Content', 'clientName must be 1 to 100 characters without surrounding spaces',
                                        code => v_approved.problem);
        WHEN 'membership.connection.validity_invalid' THEN
            RETURN api.problem_response(422, 'Unprocessable Content', 'validDays must be between 1 and 365',
                                        code => v_approved.problem);
        WHEN 'membership.connection.not_member' THEN
            RETURN api.problem_response(422, 'Unprocessable Content', 'organizationId names no active organization you are a member of',
                                        code => v_approved.problem);
        WHEN 'membership.connection.role_unknown' THEN
            RETURN api.problem_response(422, 'Unprocessable Content', 'roleId names no agent role',
                                        code => v_approved.problem);
        WHEN 'membership.connection.role_too_broad' THEN
            RETURN api.problem_response(422, 'Unprocessable Content', 'The agent role grants a permission you do not hold in that organization',
                                        code => v_approved.problem);
        WHEN 'membership.connection.agents_disabled' THEN
            RETURN api.problem_response(409, 'Conflict', 'The organization''s owner has switched its agents off',
                                        code => v_approved.problem);
        WHEN 'membership.connection.refused' THEN
            RETURN api.problem_response(409, 'Conflict', 'The membership changed meanwhile; retry', code => v_approved.problem);
        ELSE
            NULL;
    END CASE;

    RETURN api.json_response(201, jsonb_build_object(
        'connectionId', v_approved.connection_object_id,
        'connection', v_approved.token,
        'organizationId', common.try_cast(v_b->>'organizationId', NULL::uuid),
        'clientName', v_b->>'clientName',
        'roleId', v_b->>'roleId',
        'expiresAt', v_approved.expires_at));
END;
    $body$
);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000004-000c-4000-8000-000000000001',
        'path', '/connections/approve',
        'httpMethod', '^POST$',
        'name', 'approve_pending_connection',
        'description', 'Approve the OAuth client that showed you this pairing code, in one of your organizations, with an agent role',
        'access', 'authenticated',
        'successStatus', 201,
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'pairingCode', jsonb_build_object('type', 'string', 'minLength', 1, 'maxLength', 20),
                'organizationId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'roleId', jsonb_build_object('type', 'string'),
                'validDays', jsonb_build_object('type', 'integer', 'minimum', 1, 'maximum', 365, 'default', 90)),
            'required', jsonb_build_array('pairingCode', 'organizationId', 'roleId')),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'connectionId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'organizationId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'clientId', jsonb_build_object('type', 'string'),
                'clientName', jsonb_build_object('type', 'string'),
                'roleId', jsonb_build_object('type', 'string'),
                'expiresAt', jsonb_build_object('type', 'string', 'format', 'date-time')),
            'required', jsonb_build_array('connectionId', 'organizationId', 'clientId', 'clientName', 'roleId', 'expiresAt'))
    ),
    $body$
DECLARE
    v_b         jsonb := api.content_json((request).content);
    v_malformed jsonb := api.malformed_params(v_b, '{"organizationId": "uuid", "validDays": "integer"}');
    v_org       uuid := common.try_cast(v_b->>'organizationId', NULL::uuid);
    v_approved  record;
BEGIN
    IF v_malformed IS NOT NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Content', 'organizationId must be a uuid, validDays an integer',
                                    invalid_params => v_malformed);
    END IF;

    SELECT * INTO v_approved FROM membership.fn_approve_connection_by_code(
        v_b->>'pairingCode', v_org, v_b->>'roleId', coalesce(common.try_cast(v_b->>'validDays', NULL::int), 90));
    CASE v_approved.problem
        WHEN 'membership.connection.login_session_required' THEN
            RETURN api.problem_response(403, 'Forbidden', 'Only a person signed in on the web approves a connection',
                                        code => v_approved.problem);
        WHEN 'membership.connection.pairing_code_unknown' THEN
            RETURN api.problem_response(422, 'Unprocessable Content', 'pairingCode names no client waiting for you; it may have expired',
                                        code => v_approved.problem);
        WHEN 'membership.connection.validity_invalid' THEN
            RETURN api.problem_response(422, 'Unprocessable Content', 'validDays must be between 1 and 365',
                                        code => v_approved.problem);
        WHEN 'membership.connection.not_member' THEN
            RETURN api.problem_response(422, 'Unprocessable Content', 'organizationId names no active organization you are a member of',
                                        code => v_approved.problem);
        WHEN 'membership.connection.role_unknown' THEN
            RETURN api.problem_response(422, 'Unprocessable Content', 'roleId names no agent role',
                                        code => v_approved.problem);
        WHEN 'membership.connection.role_too_broad' THEN
            RETURN api.problem_response(422, 'Unprocessable Content', 'The agent role grants a permission you do not hold in that organization',
                                        code => v_approved.problem);
        WHEN 'membership.connection.agents_disabled' THEN
            RETURN api.problem_response(409, 'Conflict', 'The organization''s owner has switched its agents off',
                                        code => v_approved.problem);
        WHEN 'membership.connection.refused' THEN
            RETURN api.problem_response(409, 'Conflict', 'The membership changed meanwhile; retry', code => v_approved.problem);
        ELSE
            IF v_approved.problem IS NOT NULL THEN
                RETURN api.problem_response(422, 'Unprocessable Content', 'The connection cannot be approved',
                                            code => v_approved.problem);
            END IF;
    END CASE;

    RETURN api.json_response(201, jsonb_build_object(
        'connectionId', v_approved.connection_object_id,
        'organizationId', v_org,
        'clientId', v_approved.client_id,
        'clientName', v_approved.client_name,
        'roleId', v_b->>'roleId',
        'expiresAt', v_approved.expires_at));
END;
    $body$
);

DO $register$
DECLARE
    v_connection jsonb := jsonb_build_object(
        'type', 'object',
        'properties', jsonb_build_object(
            'connectionId', jsonb_build_object('type', 'string', 'format', 'uuid'),
            'organizationId', jsonb_build_object('type', 'string', 'format', 'uuid'),
            'principalId', jsonb_build_object('type', 'string', 'format', 'uuid'),
            'principalName', jsonb_build_object('type', 'string'),
            'clientName', jsonb_build_object('type', 'string'),
            'clientId', jsonb_build_object('type', 'string'),
            'roleId', jsonb_build_object('type', 'string'),
            'status', jsonb_build_object('type', 'string', 'enum', jsonb_build_array(
                'active', 'paused', 'agents_disabled', 'organization_inactive', 'ended', 'expired', 'revoked')),
            'approvedAt', jsonb_build_object('type', 'string', 'format', 'date-time'),
            'expiresAt', jsonb_build_object('type', 'string', 'format', 'date-time'),
            'lastUsedAt', jsonb_build_object('type', 'string', 'format', 'date-time'),
            'pausedAt', jsonb_build_object('type', 'string', 'format', 'date-time')),
        'required', jsonb_build_array('connectionId', 'organizationId', 'principalId', 'clientName', 'roleId', 'status',
                                      'approvedAt', 'expiresAt'));
    v_route record;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'e1000004-0002-4000-8000-000000000001',
            'path', '/connections',
            'httpMethod', '^GET$',
            'name', 'list_connections',
            'description', 'The signed-in person''s connections in every organization, newest first, without their handles',
            'access', 'authenticated',
            'outputSchema', jsonb_build_object(
                'type', 'object',
                'properties', jsonb_build_object('connections', jsonb_build_object('type', 'array', 'items', v_connection)),
                'required', jsonb_build_array('connections'))
        ),
        $body$
DECLARE
    v_connections jsonb;
BEGIN
    SELECT coalesce(jsonb_agg(membership.fn_connection_json(c) ORDER BY c.approved_at DESC, c.connection_object_id), '[]')
    INTO v_connections
    FROM membership.vw_connection c
    WHERE c.principal_object_id = api.current_principal_id();

    RETURN api.json_response(200, jsonb_build_object('connections', v_connections));
END;
        $body$
    );

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'e1000004-0006-4000-8000-000000000001',
            'path', '/agents',
            'httpMethod', '^GET$',
            'name', 'list_agents',
            'description', 'Every connection into the organization, newest first, and whether its agents are switched on',
            'permissions', jsonb_build_array('members.manage'),
            'outputSchema', jsonb_build_object(
                'type', 'object',
                'properties', jsonb_build_object(
                    'agentsEnabled', jsonb_build_object('type', 'boolean'),
                    'connections', jsonb_build_object('type', 'array', 'items', v_connection)),
                'required', jsonb_build_array('agentsEnabled', 'connections'))
        ),
        $body$
DECLARE
    v_connections jsonb;
BEGIN
    SELECT coalesce(jsonb_agg(membership.fn_connection_json(c) ORDER BY c.approved_at DESC, c.connection_object_id), '[]')
    INTO v_connections
    FROM membership.vw_connection c
    WHERE c.organization_object_id = api.current_organization_id();

    RETURN api.json_response(200, jsonb_build_object(
        'agentsEnabled', (SELECT o.agents_disabled_at IS NULL FROM membership.organization o
                          WHERE o.object_id = api.current_organization_id()),
        'connections', v_connections));
END;
        $body$
    );

    -- Pause, resume and revoke, once for the person's own connections and
    -- once for a manager's organization: the same kernel and problem codes,
    -- only the routes, the access and which connections the route may name
    -- differ. Another person's or another organization's connection is 404.
    FOR v_route IN
        SELECT * FROM (VALUES
            ('e1000004-0004-4000-8000-000000000001', '/connections/{connectionId:uuid}/pause', '^POST$', 'pause_connection',
             'Pause one of your connections: its agent acts as no one until resumed', NULL, 'pause',
             'c.principal_object_id = api.current_principal_id()'),
            ('e1000004-0005-4000-8000-000000000001', '/connections/{connectionId:uuid}/resume', '^POST$', 'resume_connection',
             'Resume one of your paused connections', NULL, 'resume',
             'c.principal_object_id = api.current_principal_id()'),
            ('e1000004-0003-4000-8000-000000000001', '/connections/{connectionId:uuid}', '^DELETE$', 'revoke_connection',
             'Revoke one of your connections for good; its next MCP request has no identity', NULL, 'revoke',
             'c.principal_object_id = api.current_principal_id()'),
            ('e1000004-0008-4000-8000-000000000001', '/agents/{connectionId:uuid}/pause', '^POST$', 'pause_agent',
             'Pause a connection into the organization: its agent acts as no one until resumed', 'members.manage', 'pause',
             'c.organization_object_id = api.current_organization_id()'),
            ('e1000004-0009-4000-8000-000000000001', '/agents/{connectionId:uuid}/resume', '^POST$', 'resume_agent',
             'Resume a paused connection into the organization', 'members.manage', 'resume',
             'c.organization_object_id = api.current_organization_id()'),
            ('e1000004-0007-4000-8000-000000000001', '/agents/{connectionId:uuid}', '^DELETE$', 'revoke_agent',
             'Revoke a connection into the organization for good', 'members.manage', 'revoke',
             'c.organization_object_id = api.current_organization_id()')
        ) r(id, path, method, name, description, permission, operation, scope)
    LOOP
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object('id', v_route.id, 'path', v_route.path, 'httpMethod', v_route.method,
                               'name', v_route.name, 'description', v_route.description, 'outputSchema', v_connection)
            || CASE WHEN v_route.permission IS NULL THEN jsonb_build_object('access', 'authenticated')
                    ELSE jsonb_build_object('permissions', jsonb_build_array(v_route.permission)) END,
            format($body$
DECLARE
    v_id      uuid := ((request).path_params->>'connectionId')::uuid;
    v_problem text := CASE WHEN EXISTS (SELECT FROM membership.vw_connection c
                                        WHERE c.connection_object_id = v_id AND %1$s)
                           THEN membership.fn_connection_problem(v_id, %2$L)
                           ELSE 'membership.connection.not_found' END;
    v_row     membership.connection;
BEGIN
    CASE v_problem
        WHEN 'membership.connection.not_found' THEN
            RETURN api.problem_response(404, 'Not Found', 'No such live connection', code => v_problem);
        WHEN 'membership.connection.paused' THEN
            RETURN api.problem_response(409, 'Conflict', 'The connection is already paused', code => v_problem);
        WHEN 'membership.connection.not_paused' THEN
            RETURN api.problem_response(409, 'Conflict', 'The connection is not paused', code => v_problem);
        WHEN 'membership.connection.paused_by_manager' THEN
            RETURN api.problem_response(409, 'Conflict', 'An organization manager paused this connection; only a manager resumes it',
                                        code => v_problem);
        ELSE
            NULL;
    END CASE;

    v_row := membership.fn_%2$s_connection(v_id);
    IF v_row.object_id IS NULL THEN
        RETURN api.problem_response(409, 'Conflict', 'The connection changed meanwhile; retry', code => 'membership.connection.changed');
    END IF;
    RETURN api.json_response(200, (SELECT membership.fn_connection_json(c) FROM membership.vw_connection c
                                   WHERE c.connection_object_id = v_id));
END;
            $body$, v_route.scope, v_route.operation));
    END LOOP;

    FOR v_route IN
        SELECT * FROM (VALUES
            ('e1000004-000a-4000-8000-000000000001', '/agents/disable', 'disable_agents',
             'Switch every agent of the organization off; owner only', false),
            ('e1000004-000b-4000-8000-000000000001', '/agents/enable', 'enable_agents',
             'Switch the organization''s agents back on; owner only', true)
        ) r(id, path, name, description, enabled)
    LOOP
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', v_route.id, 'path', v_route.path, 'httpMethod', '^POST$', 'name', v_route.name,
                'description', v_route.description,
                'permissions', jsonb_build_array('members.manage'),
                'outputSchema', jsonb_build_object(
                    'type', 'object',
                    'properties', jsonb_build_object(
                        'organizationId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                        'agentsEnabled', jsonb_build_object('type', 'boolean')),
                    'required', jsonb_build_array('organizationId', 'agentsEnabled'))),
            format($body$
DECLARE
    v_org     uuid := api.current_organization_id();
    v_problem text := membership.fn_organization_agents_problem(v_org);
    v_updated membership.organization;
BEGIN
    IF v_problem = 'membership.organization.owner_required' THEN
        RETURN api.problem_response(403, 'Forbidden', 'Only the organization''s owner switches its agents', code => v_problem);
    ELSIF v_problem IS NOT NULL THEN
        RETURN api.problem_response(404, 'Not Found', 'No such organization', code => v_problem);
    END IF;
    v_updated := membership.fn_set_organization_agents(v_org, %L);
    RETURN api.json_response(200, jsonb_build_object(
        'organizationId', v_updated.object_id, 'agentsEnabled', v_updated.agents_disabled_at IS NULL));
END;
            $body$, v_route.enabled));
    END LOOP;
END $register$;
