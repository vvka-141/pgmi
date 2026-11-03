/*
<pgmi-meta
    id="0b7e5d21-4c8a-4f63-a1d9-6e2f8c3b7a54"
    idempotent="true">
  <description>
    The self-serve path for organizations: any signed-in person creates an
    active organization and owns it. Organizations are invitation-only by
    default (a platform operator creates one with POST /admin/organizations
    and invites its owner), so this handler deploys with env=dev alone. A
    project that wants self-serve sign-up everywhere moves this file out of
    dev/ into api/; there is no flag.
  </description>
  <sortKeys>
    <key>005/006</key>
  </sortKeys>
</pgmi-meta>
*/

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0008-4000-8000-000000000001',
        'path', '/organizations',
        'httpMethod', '^POST$',
        'name', 'create_organization',
        'description', 'Create an active organization owned by the signed-in person',
        'access', 'authenticated',
        'successStatus', 201,
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'name', jsonb_build_object('type', 'string'),
                'slug', jsonb_build_object('type', 'string')),
            'required', jsonb_build_array('name', 'slug')),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'id', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'name', jsonb_build_object('type', 'string'),
                'slug', jsonb_build_object('type', 'string'),
                'status', jsonb_build_object('type', 'string', 'const', 'active')),
            'required', jsonb_build_array('id', 'name', 'slug', 'status'))
    ),
    $body$
DECLARE
    v_b       jsonb := api.content_json((request).content);
    v_name    text := v_b->>'name';
    v_slug    text := v_b->>'slug';
    v_problem text;
    v_org     membership.organization;
BEGIN
    IF api.current_organization_id() IS NOT NULL THEN
        RETURN api.problem_response(409, 'Conflict',
            'A request acts in one organization; create a new one without x-organization-session',
            code => 'organization_session_open');
    END IF;
    v_problem := membership.fn_organization_problem(v_name, v_slug);
    IF v_problem = 'membership.organization.slug_taken' THEN
        RETURN api.problem_response(409, 'Conflict', 'The slug is taken', code => v_problem);
    ELSIF v_problem IS NOT NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Entity', 'The name or slug is not valid', code => v_problem);
    END IF;

    v_org := membership.fn_create_organization_with_owner(v_name, v_slug, api.current_principal_id());
    RETURN api.json_response(201, jsonb_build_object(
        'id', v_org.object_id, 'name', v_org.name, 'slug', v_org.slug, 'status', v_org.status));
END;
    $body$
);
