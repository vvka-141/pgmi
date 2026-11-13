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
        'querySchema', api.page_query_schema(),
        'outputSchema', api.page_output_schema(jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'memberId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'principalId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'displayName', jsonb_build_object('type', 'string'),
                'email', jsonb_build_object('type', 'string'),
                'isEmailVerified', jsonb_build_object('type', 'boolean'),
                'memberSince', jsonb_build_object('type', 'string', 'format', 'date-time'),
                'roleIds', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'string'))
            ),
            'required', jsonb_build_array('memberId', 'principalId', 'roleIds')
        ))
    ),
    $body$
DECLARE
    v_page  record := api.pagination_params(api.query_params((request).url));
    v_items jsonb;
    v_total bigint;
BEGIN
    -- One CTE for the count and the page: vw_member aggregates each member's
    -- roles, and the CTE computes it once where two queries computed it twice.
    WITH member AS (
        SELECT m FROM membership.vw_member m WHERE m.organization_object_id = api.current_organization_id()
    )
    SELECT coalesce(jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
               'memberId', (p.m).membership_object_id,
               'principalId', (p.m).principal_object_id,
               'displayName', (p.m).display_name,
               'email', (p.m).email,
               'isEmailVerified', (p.m).is_email_verified,
               'memberSince', (p.m).member_since,
               'roleIds', to_jsonb((p.m).role_ids))) ORDER BY (p.m).member_since, (p.m).membership_object_id), '[]'),
           (SELECT count(*) FROM member)
    INTO v_items, v_total
    FROM (SELECT m FROM member
          ORDER BY (m).member_since, (m).membership_object_id
          LIMIT v_page.o_limit OFFSET v_page.o_offset) p;

    RETURN api.page_response(v_items, v_total, v_page.o_limit, v_page.o_offset);
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
        'description', 'Grant a role to a member: 201 when the grant is new, 200 when the role was already held (nothing changes)',
        'permissions', jsonb_build_array('members.manage'),
        'successStatus', jsonb_build_array(201, 200),
        'noLocation', 'a PUT creates at its own URI, which a 201 without Location names',
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
    v_granted record;
BEGIN
    v_problem := membership.fn_role_problem(v_member, v_role);
    IF v_problem IN ('membership.member.not_found', 'membership.role.unknown') THEN
        RETURN api.problem_response(404, 'Not Found', 'No such member or role', code => v_problem);
    ELSIF v_problem IS NOT NULL THEN
        RETURN api.problem_response(409, 'Conflict', 'Ownership moves only by a transfer', code => v_problem);
    END IF;

    SELECT * INTO v_granted FROM membership.fn_grant_role(v_member, v_role);
    RETURN api.json_response(CASE WHEN v_granted.granted THEN 201 ELSE 200 END, jsonb_build_object(
        'memberId', v_member, 'roleId', (v_granted.assignment).role_id, 'since', lower((v_granted.assignment).valid_during)));
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
        RETURN api.problem_response(404, 'Not Found', 'The member does not hold this role', code => 'membership.role.not_held');
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
        'successStatus', jsonb_build_array(201, 200),
        'noLocation', 'no route reads one invitation',
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
    v_result  record;
BEGIN
    v_problem := membership.fn_invitation_problem(api.current_organization_id(), v_email, v_role);
    IF v_problem IS NOT NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Content', 'The email or role cannot be invited', code => v_problem,
            invalid_params => CASE v_problem
                WHEN 'membership.invitation.email_invalid' THEN jsonb_build_array(api.invalid_param('email', 'invalid'))
                WHEN 'membership.invitation.already_member' THEN jsonb_build_array(api.invalid_param('email', 'duplicate'))
                WHEN 'membership.role.unknown' THEN jsonb_build_array(api.invalid_param('roleId', 'not_found'))
                WHEN 'membership.invitation.role_not_offered' THEN jsonb_build_array(api.invalid_param('roleId', 'not_offered'))
            END);
    END IF;

    SELECT * INTO v_result FROM membership.fn_invite(api.current_organization_id(), v_email, v_role);
    RETURN api.json_response(CASE WHEN v_result.created THEN 201 ELSE 200 END, jsonb_build_object(
        'invitationId', (v_result.invitation).object_id, 'email', (v_result.invitation).email,
        'roleId', (v_result.invitation).role_id, 'expiresAt', (v_result.invitation).expires_at));
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
        RETURN api.problem_response(404, 'Not Found', 'No such pending invitation', code => 'membership.invitation.not_found');
    END IF;

    PERFORM membership.fn_revoke_invitation(v_invitation);
    RETURN api.json_response(200, jsonb_build_object('revoked', true));
END;
    $body$
);
