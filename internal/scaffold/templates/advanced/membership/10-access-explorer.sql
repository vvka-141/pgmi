/*
<pgmi-meta
    id="7a3e9c52-0b4d-4f61-9a28-c5e1d7f04b93"
    idempotent="true">
  <description>
    The access explorer: read-only views over the chain person, membership,
    role, permission, handler, from four starting points (handler, role,
    permission, person), each showing the path and not just yes or no; and
    membership.fn_simulate_access, which predicts the gateway's decision for
    a person, an organization and METHOD URL with the gateway's own routing
    (internal.rest_route_lookup) and decision (internal.access_denial_reason).
    The views are security_invoker, so a request sees the rows the forced
    membership policies let it see. Served by api/access-explorer.sql.
  </description>
  <sortKeys>
    <key>005/000/010</key>
  </sortKeys>
</pgmi-meta>
*/

SELECT core.ensure_view('membership.vw_effective_permission', $view$
CREATE OR REPLACE VIEW membership.vw_effective_permission
WITH (security_invoker = true) AS
SELECT m.organization_object_id,
       m.principal_object_id,
       m.object_id AS membership_object_id,
       ra.role_id,
       rp.permission_id
FROM membership.organization_membership m
JOIN membership.organization o ON o.object_id = m.organization_object_id AND o.status = 'active'
JOIN membership.principal p ON p.object_id = m.principal_object_id AND p.disabled_at IS NULL
JOIN membership.role_assignment ra ON ra.membership_object_id = m.object_id AND upper_inf(ra.valid_during)
JOIN membership.role_permission rp ON rp.role_id = ra.role_id
WHERE upper_inf(m.valid_during)
$view$);

COMMENT ON VIEW membership.vw_effective_permission IS
    'Each organization permission a person holds now, and the role it comes through: a current role of a current membership in an active organization, for a person who is not disabled.';

SELECT core.ensure_view('membership.vw_handler_operation', $view$
CREATE OR REPLACE VIEW membership.vw_handler_operation
WITH (security_invoker = true) AS
SELECT h.object_id AS handler_object_id,
       h.handler_function_name AS handler_name,
       h.handler_type::text AS kind,
       coalesce(upper(array_to_string(api.openapi_methods(r.method_regexp), ',')), '') AS method,
       coalesce(nullif(r.canonical_path, ''), rp.method_name, mr.mcp_name, '') AS path,
       h.access
FROM api.handler h
LEFT JOIN api.rest_route r ON r.handler_object_id = h.object_id
LEFT JOIN api.rpc_route rp ON rp.handler_object_id = h.object_id
LEFT JOIN api.mcp_route mr ON mr.handler_object_id = h.object_id
WHERE h.deleted_at IS NULL
$view$);

COMMENT ON VIEW membership.vw_handler_operation IS
    'Every live handler as an operation: protocol, HTTP methods and path (or RPC method, or MCP name), and its declared access.';

SELECT core.ensure_view('membership.vw_handler_access', $view$
CREATE OR REPLACE VIEW membership.vw_handler_access
WITH (security_invoker = true) AS
SELECT o.handler_object_id, o.handler_name, o.kind, o.method, o.path, o.access,
       coalesce((SELECT array_agg(hp.permission_id ORDER BY hp.permission_id)
                 FROM api.handler_permission hp WHERE hp.handler_object_id = o.handler_object_id), '{}') AS permissions,
       coalesce((SELECT array_agg(DISTINCT rp.role_id ORDER BY rp.role_id)
                 FROM api.handler_permission hp
                 JOIN membership.role_permission rp ON rp.permission_id = hp.permission_id
                 WHERE hp.handler_object_id = o.handler_object_id), '{}') AS role_ids,
       EXISTS (SELECT FROM api.handler_permission hp
               JOIN membership.permission p ON p.id = hp.permission_id AND p.is_platform
               WHERE hp.handler_object_id = o.handler_object_id) AS is_platform_operation
FROM membership.vw_handler_operation o
$view$);

COMMENT ON VIEW membership.vw_handler_access IS
    'By handler: its declared access, the permissions that open it (any one suffices), the roles granting one of them, and whether a platform permission opens it.';

SELECT core.ensure_view('membership.vw_role_access', $view$
CREATE OR REPLACE VIEW membership.vw_role_access
WITH (security_invoker = true) AS
SELECT r.id AS role_id, r.name AS role_name, r.sort_order,
       rp.permission_id,
       o.handler_object_id, o.handler_name, o.kind, o.method, o.path
FROM membership.role r
JOIN membership.role_permission rp ON rp.role_id = r.id
LEFT JOIN api.handler_permission hp ON hp.permission_id = rp.permission_id
LEFT JOIN membership.vw_handler_operation o ON o.handler_object_id = hp.handler_object_id
$view$);

COMMENT ON VIEW membership.vw_role_access IS
    'By role: each permission it grants and each handler that permission opens; the handler columns are NULL for a permission no handler declares (one the application checks in SQL or RLS).';

SELECT core.ensure_view('membership.vw_permission', $view$
CREATE OR REPLACE VIEW membership.vw_permission
WITH (security_invoker = true) AS
SELECT p.id AS permission_id, p.is_platform, p.description,
       coalesce((SELECT array_agg(o.handler_name ORDER BY o.handler_name)
                 FROM api.handler_permission hp
                 JOIN membership.vw_handler_operation o ON o.handler_object_id = hp.handler_object_id
                 WHERE hp.permission_id = p.id), '{}') AS handler_names,
       coalesce((SELECT array_agg(r.id ORDER BY r.sort_order)
                 FROM membership.role_permission rp JOIN membership.role r ON r.id = rp.role_id
                 WHERE rp.permission_id = p.id), '{}') AS role_ids,
       (SELECT count(DISTINCT e.principal_object_id)::int
        FROM membership.vw_effective_permission e WHERE e.permission_id = p.id) AS holder_count
FROM membership.permission p
$view$);

COMMENT ON VIEW membership.vw_permission IS
    'By permission: the handlers it guards, the roles granting it, and how many people hold it through a current role, counted over the rows the reader may see.';

SELECT core.ensure_view('membership.vw_access_path', $view$
CREATE OR REPLACE VIEW membership.vw_access_path
WITH (security_invoker = true) AS
SELECT e.organization_object_id, e.principal_object_id, u.email AS principal_email,
       p.display_name AS principal_display_name, e.membership_object_id,
       'role'::text AS via, e.role_id, e.permission_id,
       o.handler_object_id, o.handler_name, o.kind, o.method, o.path
FROM membership.vw_effective_permission e
JOIN membership.principal p ON p.object_id = e.principal_object_id
LEFT JOIN membership.user_principal u ON u.principal_object_id = e.principal_object_id
JOIN api.handler_permission hp ON hp.permission_id = e.permission_id
JOIN membership.vw_handler_operation o ON o.handler_object_id = hp.handler_object_id
UNION ALL
SELECT NULL, up.principal_object_id, up.email, up.display_name, NULL,
       'platform', NULL, pm.id,
       o.handler_object_id, o.handler_name, o.kind, o.method, o.path
FROM membership.vw_user_principal up
JOIN membership.permission pm ON pm.is_platform
JOIN api.handler_permission hp ON hp.permission_id = pm.id
JOIN membership.vw_handler_operation o ON o.handler_object_id = hp.handler_object_id
WHERE up.disabled_at IS NULL AND up.is_platform_operator
$view$);

COMMENT ON VIEW membership.vw_access_path IS
    'By person: every path to a permission-guarded handler, through a role in an organization (via = role) or as a platform operator (via = platform, organization NULL, in a session with a second factor when platform_mfa_required). Handlers declared public, authenticated or organization are open to everyone they admit and do not appear.';

-- What the simulation needs from the membership tables, for one person and
-- one organization the request may look at: the request's own person, a
-- current member of an organization in reach, or anyone for a platform
-- operator or a context that names no one. Out of reach, every column is NULL.
CREATE OR REPLACE FUNCTION membership.fn_simulation_context(
    p_principal_object_id    uuid,
    p_organization_object_id uuid,
    OUT is_principal_active  boolean,
    OUT has_organization     boolean,
    OUT permissions          text[],
    OUT role_ids             text[])
LANGUAGE plpgsql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_member boolean;
BEGIN
    v_member := EXISTS (SELECT FROM membership.organization_membership m
                        WHERE m.principal_object_id = p_principal_object_id
                          AND m.organization_object_id = p_organization_object_id
                          AND upper_inf(m.valid_during));
    IF NOT (membership.fn_is_principal_in_reach(p_principal_object_id)
            OR (v_member AND membership.fn_is_organization_in_reach(p_organization_object_id))) THEN
        RETURN;
    END IF;

    is_principal_active := EXISTS (SELECT FROM membership.principal p
                                   WHERE p.object_id = p_principal_object_id AND p.disabled_at IS NULL);
    has_organization := is_principal_active AND v_member
        AND EXISTS (SELECT FROM membership.organization o
                    WHERE o.object_id = p_organization_object_id AND o.status = 'active');
    permissions := CASE WHEN is_principal_active
                        THEN membership.fn_effective_permissions(p_principal_object_id,
                                 CASE WHEN has_organization THEN p_organization_object_id END, true)
                        ELSE '{}' END;
    role_ids := coalesce((SELECT array_agg(ra.role_id ORDER BY ra.role_id)
                          FROM membership.role_assignment ra
                          JOIN membership.organization_membership m ON m.object_id = ra.membership_object_id
                          WHERE has_organization AND upper_inf(ra.valid_during) AND upper_inf(m.valid_during)
                            AND m.principal_object_id = p_principal_object_id
                            AND m.organization_object_id = p_organization_object_id), '{}');
END $$;

SELECT pg_temp.kernel_owns('membership.fn_simulation_context(uuid, uuid)');

CREATE OR REPLACE FUNCTION membership.fn_simulate_access(
    p_principal_object_id    uuid,
    p_organization_object_id uuid,
    p_method                 text,
    p_url                    text,
    OUT allowed              boolean,
    OUT reason               text,
    OUT handler_name         text,
    OUT access               text,
    OUT required_permissions text[],
    OUT granted_via          jsonb)
LANGUAGE plpgsql STABLE
SET search_path = membership, api, internal, common, pg_temp
AS $$
DECLARE
    v_method  text := upper(trim(p_method));
    v_handler uuid;
    v_context record;
BEGIN
    allowed := false;
    required_permissions := '{}';
    granted_via := '[]';

    SELECT * INTO v_context FROM membership.fn_simulation_context(p_principal_object_id, p_organization_object_id);
    IF v_context.is_principal_active IS NULL THEN
        allowed := NULL;
        reason := 'out_of_reach';
        RETURN;
    END IF;

    v_handler := internal.rest_route_lookup(CASE WHEN v_method = 'HEAD' THEN 'GET' ELSE v_method END,
                                            api.canonical_path(api.url_path(trim(p_url))));
    IF v_handler IS NULL THEN
        reason := 'no_route';
        RETURN;
    END IF;

    SELECT h.handler_function_name, h.access INTO handler_name, access
    FROM api.handler h WHERE h.object_id = v_handler;
    required_permissions := coalesce((SELECT array_agg(hp.permission_id ORDER BY hp.permission_id)
                                      FROM api.handler_permission hp WHERE hp.handler_object_id = v_handler), '{}');

    reason := internal.access_denial_reason(v_handler, access, v_context.is_principal_active,
                                            v_context.has_organization, v_context.permissions);
    allowed := reason IS NULL;

    granted_via := coalesce((
        SELECT jsonb_agg(jsonb_build_object('via', g.via, 'role', g.role_id, 'permission', g.permission_id)
                         ORDER BY g.via, g.role_id, g.permission_id)
        FROM (
            SELECT 'role' AS via, rp.role_id, rp.permission_id
            FROM membership.role_permission rp
            WHERE rp.role_id = ANY (v_context.role_ids) AND rp.permission_id = ANY (required_permissions)
            UNION ALL
            SELECT 'platform', NULL, p.id
            FROM membership.permission p
            WHERE p.is_platform AND p.id = ANY (required_permissions) AND p.id = ANY (v_context.permissions)
        ) g), '[]');
END $$;

COMMENT ON FUNCTION membership.fn_simulate_access(uuid, uuid, text, text) IS
    'Predicts the REST gateway: would this person, working in this organization in a session with a second factor, be allowed METHOD URL? Returns the handler, its permissions and the granting paths, or the reason: no_route | out_of_reach | unauthenticated | organization_required | permission_missing.';
