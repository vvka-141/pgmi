/*
<pgmi-meta
    id="b84f0e27-3c61-4d9a-a5f2-6e0d1c9b7a34"
    idempotent="true">
  <description>
    The access explorer over REST, for holders of access.explore: every
    operation with the permissions and roles that open it, who in the
    request's organization reaches which operation and through what, and the
    gateway's decision simulated for one member and METHOD path. Every answer
    is pinned to the request's organization. Reads membership/10-access-explorer.sql.
  </description>
  <sortKeys>
    <key>005/007</key>
  </sortKeys>
</pgmi-meta>
*/

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000003-0001-4000-8000-000000000001',
        'path', '/access/handlers',
        'httpMethod', '^GET$',
        'name', 'list_access_handlers',
        'description', 'Every operation with its declared access, the permissions that open it and the roles granting them',
        'permissions', jsonb_build_array('access.explore'),
        'querySchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object(
            'permission', jsonb_build_object('type', 'string'),
            'role', jsonb_build_object('type', 'string'))),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'handlers', jsonb_build_object(
                    'type', 'array',
                    'items', jsonb_build_object(
                        'type', 'object',
                        'properties', jsonb_build_object(
                            'handler', jsonb_build_object('type', 'string'),
                            'kind', jsonb_build_object('type', 'string'),
                            'method', jsonb_build_object('type', 'string'),
                            'path', jsonb_build_object('type', 'string'),
                            'access', jsonb_build_object('type', 'string'),
                            'permissions', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'string')),
                            'roleIds', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'string')),
                            'platformOperation', jsonb_build_object('type', 'boolean')),
                        'required', jsonb_build_array('handler', 'kind', 'access', 'permissions', 'roleIds')))),
            'required', jsonb_build_array('handlers'))
    ),
    $body$
DECLARE
    v_q        extensions.hstore := api.query_params((request).url);
    v_handlers jsonb;
BEGIN
    SELECT coalesce(jsonb_agg(jsonb_build_object(
               'handler', a.handler_name,
               'kind', a.kind,
               'method', a.method,
               'path', a.path,
               'access', a.access,
               'permissions', to_jsonb(a.permissions),
               'roleIds', to_jsonb(a.role_ids),
               'platformOperation', a.is_platform_operation) ORDER BY a.kind, a.path, a.method), '[]')
    INTO v_handlers
    FROM membership.vw_handler_access a
    WHERE (NOT v_q ? 'permission' OR v_q -> 'permission' = ANY (a.permissions))
      AND (NOT v_q ? 'role' OR v_q -> 'role' = ANY (a.role_ids));

    RETURN api.json_response(200, jsonb_build_object('handlers', v_handlers));
END;
    $body$
);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000003-0002-4000-8000-000000000001',
        'path', '/access/paths',
        'httpMethod', '^GET$',
        'name', 'list_access_paths',
        'description', 'Who in the organization reaches which permission-guarded operation, through which role and permission',
        'permissions', jsonb_build_array('access.explore'),
        'querySchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object(
            'principalId', jsonb_build_object('type', 'string', 'format', 'uuid'),
            'handler', jsonb_build_object('type', 'string'),
            'permission', jsonb_build_object('type', 'string'),
            'role', jsonb_build_object('type', 'string'),
            'limit', jsonb_build_object('type', 'integer', 'minimum', 1, 'maximum', 2000, 'default', 500))),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'paths', jsonb_build_object(
                    'type', 'array',
                    'items', jsonb_build_object(
                        'type', 'object',
                        'properties', jsonb_build_object(
                            'principalId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                            'email', jsonb_build_object('type', 'string'),
                            'displayName', jsonb_build_object('type', 'string'),
                            'roleId', jsonb_build_object('type', 'string'),
                            'permission', jsonb_build_object('type', 'string'),
                            'handler', jsonb_build_object('type', 'string'),
                            'kind', jsonb_build_object('type', 'string'),
                            'method', jsonb_build_object('type', 'string'),
                            'path', jsonb_build_object('type', 'string')),
                        'required', jsonb_build_array('principalId', 'roleId', 'permission', 'handler')))),
            'required', jsonb_build_array('paths'))
    ),
    $body$
DECLARE
    v_q         extensions.hstore := api.query_params((request).url);
    v_principal uuid := common.try_cast(v_q -> 'principalId', NULL::uuid);
    v_limit     int := common.try_cast(coalesce(v_q -> 'limit', '500'), NULL::int);
    v_paths     jsonb;
BEGIN
    IF v_q ? 'principalId' AND v_principal IS NULL THEN
        RETURN api.problem_response(400, 'Bad Request', 'principalId must be a UUID');
    END IF;
    IF v_limit IS NULL OR v_limit NOT BETWEEN 1 AND 2000 THEN
        RETURN api.problem_response(400, 'Bad Request', 'limit must be an integer from 1 to 2000');
    END IF;

    SELECT coalesce(jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
               'principalId', p.principal_object_id,
               'email', p.principal_email,
               'displayName', p.principal_display_name,
               'roleId', p.role_id,
               'permission', p.permission_id,
               'handler', p.handler_name,
               'kind', p.kind,
               'method', p.method,
               'path', p.path)) ORDER BY p.principal_email, p.path, p.method, p.role_id, p.permission_id), '[]')
    INTO v_paths
    FROM (
        SELECT *
        FROM membership.vw_access_path p
        WHERE p.via = 'role'
          AND p.organization_object_id = api.current_organization_id()
          AND (v_principal IS NULL OR p.principal_object_id = v_principal)
          AND (NOT v_q ? 'handler' OR p.handler_name = v_q -> 'handler')
          AND (NOT v_q ? 'permission' OR p.permission_id = v_q -> 'permission')
          AND (NOT v_q ? 'role' OR p.role_id = v_q -> 'role')
        ORDER BY p.principal_email, p.path, p.method, p.role_id, p.permission_id
        LIMIT v_limit
    ) p;

    RETURN api.json_response(200, jsonb_build_object('paths', v_paths));
END;
    $body$
);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000003-0003-4000-8000-000000000001',
        'path', '/access/simulate',
        'httpMethod', '^GET$',
        'name', 'simulate_access',
        'description', 'The gateway''s decision for a member of this organization calling METHOD path, in a session with a second factor',
        'permissions', jsonb_build_array('access.explore'),
        'querySchema', jsonb_build_object('type', 'object',
            'required', jsonb_build_array('principalId', 'method', 'path'),
            'properties', jsonb_build_object(
                'principalId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'method', jsonb_build_object('type', 'string'),
                'path', jsonb_build_object('type', 'string'))),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'allowed', jsonb_build_object('type', 'boolean'),
                'reason', jsonb_build_object('type', jsonb_build_array('string', 'null'),
                    'enum', jsonb_build_array('no_route', 'unauthenticated', 'organization_required', 'permission_missing', NULL)),
                'handler', jsonb_build_object('type', jsonb_build_array('string', 'null')),
                'access', jsonb_build_object('type', jsonb_build_array('string', 'null')),
                'requiredPermissions', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'string')),
                'grantedVia', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'object'))),
            'required', jsonb_build_array('allowed', 'reason', 'requiredPermissions', 'grantedVia'))
    ),
    $body$
DECLARE
    v_q         extensions.hstore := api.query_params((request).url);
    v_principal uuid := common.try_cast(v_q -> 'principalId', NULL::uuid);
    v_path      text := v_q -> 'path';
    v_result    record;
BEGIN
    IF v_principal IS NULL THEN
        RETURN api.problem_response(400, 'Bad Request', 'principalId must be a UUID');
    END IF;
    IF left(v_path, 1) IS DISTINCT FROM '/' THEN
        RETURN api.problem_response(400, 'Bad Request', 'path must start with /');
    END IF;

    SELECT * INTO v_result
    FROM membership.fn_simulate_access(v_principal, api.current_organization_id(), v_q -> 'method', v_path);
    IF v_result.reason = 'out_of_reach' THEN
        RETURN api.problem_response(404, 'Not Found', 'No such member in this organization');
    END IF;

    RETURN api.json_response(200, jsonb_build_object(
        'allowed', v_result.allowed,
        'reason', v_result.reason,
        'handler', v_result.handler_name,
        'access', v_result.access,
        'requiredPermissions', to_jsonb(v_result.required_permissions),
        'grantedVia', v_result.granted_via));
END;
    $body$
);
