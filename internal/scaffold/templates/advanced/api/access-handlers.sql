/*
<pgmi-meta
    id="5d0c7a3e-61b4-4f2a-9e8d-2b7f4c1a9e60"
    idempotent="true">
  <description>
    Members, roles and invitations of the request's organization. Each handler
    declares the permission it needs (members.read or members.manage from
    data/authorization.json); the gateway refuses a caller without it before
    the body runs. The kernels refuse anything outside the request's
    organization, so another organization's ids answer 404.
  </description>
  <sortKeys>
    <key>005/005</key>
  </sortKeys>
</pgmi-meta>
*/

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000002-0001-4000-8000-000000000001',
        'path', '/members',
        'httpMethod', '^GET$',
        'name', 'list_members',
        'description', 'Current members of the organization and the roles each holds',
        'permissions', jsonb_build_array('members.read'),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'members', jsonb_build_object(
                    'type', 'array',
                    'items', jsonb_build_object(
                        'type', 'object',
                        'properties', jsonb_build_object(
                            'memberId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                            'principalId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                            'displayName', jsonb_build_object('type', 'string'),
                            'email', jsonb_build_object('type', 'string'),
                            'memberSince', jsonb_build_object('type', 'string', 'format', 'date-time'),
                            'roleIds', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'string'))
                        ),
                        'required', jsonb_build_array('memberId', 'principalId', 'roleIds')
                    )
                )
            ),
            'required', jsonb_build_array('members')
        )
    ),
    $body$
DECLARE
    v_members jsonb;
BEGIN
    SELECT coalesce(jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
               'memberId', m.membership_object_id,
               'principalId', m.principal_object_id,
               'displayName', m.display_name,
               'email', m.email,
               'memberSince', m.member_since,
               'roleIds', to_jsonb(m.role_ids))) ORDER BY m.member_since, m.membership_object_id), '[]')
    INTO v_members
    FROM membership.vw_member m
    WHERE m.organization_object_id = api.current_organization_id();

    RETURN api.json_response(200, jsonb_build_object('members', v_members));
END;
    $body$
);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000002-0002-4000-8000-000000000001',
        'path', '/members/{memberId:uuid}',
        'httpMethod', '^DELETE$',
        'name', 'remove_member',
        'description', 'End a membership and its roles; the owner cannot be removed',
        'permissions', jsonb_build_array('members.manage'),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object('removed', jsonb_build_object('type', 'boolean', 'const', true)),
            'required', jsonb_build_array('removed'))
    ),
    $body$
DECLARE
    v_member  uuid := ((request).path_params->>'memberId')::uuid;
    v_problem text;
BEGIN
    v_problem := membership.fn_end_membership_problem(v_member);
    IF v_problem = 'membership.member.not_found' THEN
        RETURN api.problem_response(404, 'Not Found', 'No such member', code => v_problem);
    ELSIF v_problem IS NOT NULL THEN
        RETURN api.problem_response(409, 'Conflict', 'The owner cannot be removed; transfer ownership first', code => v_problem);
    END IF;

    IF (membership.fn_end_membership(v_member, 'removed')).object_id IS NULL THEN
        RETURN api.problem_response(409, 'Conflict', 'The membership changed meanwhile; retry',
                                    code => 'membership.member.changed');
    END IF;
    RETURN api.json_response(200, jsonb_build_object('removed', true));
END;
    $body$
);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000002-0003-4000-8000-000000000001',
        'path', '/members/{memberId:uuid}/roles/{roleId}',
        'httpMethod', '^PUT$',
        'name', 'grant_member_role',
        'description', 'Grant a role to a member; granting a role already held changes nothing',
        'permissions', jsonb_build_array('members.manage'),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'memberId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'roleId', jsonb_build_object('type', 'string'),
                'since', jsonb_build_object('type', 'string', 'format', 'date-time')),
            'required', jsonb_build_array('memberId', 'roleId', 'since'))
    ),
    $body$
DECLARE
    v_member  uuid := ((request).path_params->>'memberId')::uuid;
    v_role    text := (request).path_params->>'roleId';
    v_problem text;
    v_granted membership.role_assignment;
BEGIN
    v_problem := membership.fn_role_problem(v_member, v_role);
    IF v_problem IN ('membership.member.not_found', 'membership.role.unknown') THEN
        RETURN api.problem_response(404, 'Not Found', 'No such member or role', code => v_problem);
    ELSIF v_problem IS NOT NULL THEN
        RETURN api.problem_response(409, 'Conflict', 'Ownership moves only by a transfer', code => v_problem);
    END IF;

    v_granted := membership.fn_grant_role(v_member, v_role);
    RETURN api.json_response(200, jsonb_build_object(
        'memberId', v_member, 'roleId', v_granted.role_id, 'since', lower(v_granted.valid_during)));
END;
    $body$
);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000002-0004-4000-8000-000000000001',
        'path', '/members/{memberId:uuid}/roles/{roleId}',
        'httpMethod', '^DELETE$',
        'name', 'revoke_member_role',
        'description', 'Revoke a role a member holds',
        'permissions', jsonb_build_array('members.manage'),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object('revoked', jsonb_build_object('type', 'boolean', 'const', true)),
            'required', jsonb_build_array('revoked'))
    ),
    $body$
DECLARE
    v_member  uuid := ((request).path_params->>'memberId')::uuid;
    v_role    text := (request).path_params->>'roleId';
    v_problem text;
BEGIN
    v_problem := membership.fn_role_problem(v_member, v_role);
    IF v_problem IN ('membership.member.not_found', 'membership.role.unknown') THEN
        RETURN api.problem_response(404, 'Not Found', 'No such member or role', code => v_problem);
    ELSIF v_problem IS NOT NULL THEN
        RETURN api.problem_response(409, 'Conflict', 'Ownership moves only by a transfer', code => v_problem);
    END IF;

    IF (membership.fn_revoke_role(v_member, v_role)).object_id IS NULL THEN
        RETURN api.problem_response(404, 'Not Found', 'The member does not hold this role');
    END IF;
    RETURN api.json_response(200, jsonb_build_object('revoked', true));
END;
    $body$
);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000002-0005-4000-8000-000000000001',
        'path', '/invitations',
        'httpMethod', '^POST$',
        'name', 'invite_member',
        'description', 'Invite an email address into the organization with a role; it is accepted by signing in with that verified address',
        'permissions', jsonb_build_array('members.manage'),
        'successStatus', 201,
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'email', jsonb_build_object('type', 'string', 'format', 'email'),
                'roleId', jsonb_build_object('type', 'string')),
            'required', jsonb_build_array('email', 'roleId')),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'invitationId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'email', jsonb_build_object('type', 'string'),
                'roleId', jsonb_build_object('type', 'string'),
                'expiresAt', jsonb_build_object('type', 'string', 'format', 'date-time')),
            'required', jsonb_build_array('invitationId', 'email', 'roleId', 'expiresAt'))
    ),
    $body$
DECLARE
    v_b       jsonb := api.content_json((request).content);
    v_email   text := v_b->>'email';
    v_role    text := v_b->>'roleId';
    v_problem text;
    v_inv     membership.invitation;
BEGIN
    v_problem := membership.fn_invitation_problem(api.current_organization_id(), v_email, v_role);
    IF v_problem = 'membership.invitation.already_member' THEN
        RETURN api.problem_response(409, 'Conflict', 'That address already belongs to a member', code => v_problem);
    ELSIF v_problem IS NOT NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Entity', 'The email or role cannot be invited', code => v_problem);
    END IF;

    v_inv := membership.fn_invite(api.current_organization_id(), v_email, v_role);
    RETURN api.json_response(201, jsonb_build_object(
        'invitationId', v_inv.object_id, 'email', v_inv.email, 'roleId', v_inv.role_id, 'expiresAt', v_inv.expires_at));
END;
    $body$
);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000002-0006-4000-8000-000000000001',
        'path', '/invitations/{invitationId:uuid}',
        'httpMethod', '^DELETE$',
        'name', 'revoke_invitation',
        'description', 'Withdraw a pending invitation',
        'permissions', jsonb_build_array('members.manage'),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object('revoked', jsonb_build_object('type', 'boolean', 'const', true)),
            'required', jsonb_build_array('revoked'))
    ),
    $body$
DECLARE
    v_invitation uuid := ((request).path_params->>'invitationId')::uuid;
BEGIN
    IF NOT EXISTS (SELECT FROM membership.invitation i
                   WHERE i.object_id = v_invitation AND i.organization_object_id = api.current_organization_id()
                     AND i.accepted_at IS NULL AND i.revoked_at IS NULL) THEN
        RETURN api.problem_response(404, 'Not Found', 'No such pending invitation');
    END IF;

    PERFORM membership.fn_revoke_invitation(v_invitation);
    RETURN api.json_response(200, jsonb_build_object('revoked', true));
END;
    $body$
);
