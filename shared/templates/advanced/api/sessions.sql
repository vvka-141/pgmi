/*
<pgmi-meta
    id="35cadbc4-3906-4edb-a251-6cd1fb625d12"
    idempotent="true">
  <description>
    Organization sessions over REST. A browser tab that signed in (it has a
    login session) opens an organization session in one organization the
    person is a current member of, and sends its handle as
    x-organization-session on every request; that fixes the request's
    organization. Switching organization opens another session. DELETE ends
    the tab's current one.
  </description>
  <sortKeys>
    <key>005/003</key>
  </sortKeys>
</pgmi-meta>
*/

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0006-4000-8000-000000000001',
        'path', '/organization-sessions',
        'httpMethod', '^POST$',
        'name', 'open_organization_session',
        'description', 'Open an organization session for this tab in an organization the signed-in person is a member of',
        'access', 'authenticated',
        'autoLog', false,
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'organizationId', jsonb_build_object('type', 'string', 'format', 'uuid')),
            'required', jsonb_build_array('organizationId')),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'organizationSession', jsonb_build_object('type', 'string'),
                'organizationId', jsonb_build_object('type', 'string', 'format', 'uuid')),
            'required', jsonb_build_array('organizationSession', 'organizationId'))
    ),
    $body$
DECLARE
    v_organization uuid := common.try_cast(api.content_json((request).content)->>'organizationId', NULL::uuid);
    v_opened record;
BEGIN
    IF v_organization IS NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Content', 'organizationId must be a UUID',
                                    code => 'membership.organization_session.organization_id_invalid',
                                    invalid_params => jsonb_build_array(api.invalid_param('organizationId', 'must be a uuid')));
    END IF;
    SELECT * INTO v_opened FROM membership.fn_open_organization_session(api.current_login_session_id(), v_organization);
    IF v_opened.problem = 'membership.organization_session.not_member' THEN
        RETURN api.problem_response(422, 'Unprocessable Content', 'No such organization among yours', code => v_opened.problem,
                                    invalid_params => jsonb_build_array(api.invalid_param('organizationId', 'not_found')));
    ELSIF v_opened.problem IS NOT NULL THEN
        RETURN api.problem_response(409, 'Conflict', 'The organization is not active', code => v_opened.problem);
    END IF;
    RETURN api.json_response(200, jsonb_build_object(
        'organizationSession', v_opened.token,
        'organizationId', v_organization));
END;
    $body$
);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0007-4000-8000-000000000001',
        'path', '/organization-sessions/current',
        'httpMethod', '^DELETE$',
        'name', 'end_organization_session',
        'description', 'End this tab''s organization session',
        'access', 'authenticated',
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object('ended', jsonb_build_object('type', 'boolean', 'const', true)),
            'required', jsonb_build_array('ended'))
    ),
    $body$
BEGIN
    IF api.current_organization_session_id() IS NULL
       OR NOT membership.fn_end_organization_session(api.current_organization_session_id()) THEN
        RETURN api.problem_response(404, 'Not Found', 'This request has no organization session', code => 'membership.organization_session.not_open');
    END IF;
    RETURN api.json_response(200, jsonb_build_object('ended', true));
END;
    $body$
);
