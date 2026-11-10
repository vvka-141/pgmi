/*
<pgmi-meta
    id="c41b66e1-9e38-4150-9f1f-ce93647daa48"
    idempotent="true">
  <description>
    Membership read models: the current owner of each organization, the
    current members with their roles, people with their logins, and the
    caller's own profile (api.vw_current_user). security_invoker throughout, so
    they show what the reader's role may see of the tables.
  </description>
  <sortKeys>
    <key>005/000/004</key>
  </sortKeys>
</pgmi-meta>
*/

SELECT core.ensure_view('membership.vw_organization_owner', $view$
CREATE OR REPLACE VIEW membership.vw_organization_owner
WITH (security_invoker = true) AS
SELECT ra.organization_object_id, ra.membership_object_id, m.principal_object_id, lower(ra.valid_during) AS owner_since
FROM membership.role_assignment ra
JOIN membership.organization_membership m ON m.object_id = ra.membership_object_id
WHERE ra.role_id = 'org.owner' AND upper_inf(ra.valid_during)
$view$);

COMMENT ON VIEW membership.vw_organization_owner IS
    'The current owner of each organization: the member holding org.owner now.';

SELECT core.ensure_view('membership.vw_member', $view$
CREATE OR REPLACE VIEW membership.vw_member
WITH (security_invoker = true) AS
SELECT m.object_id AS membership_object_id,
       m.organization_object_id,
       o.slug AS organization_slug,
       o.status AS organization_status,
       m.principal_object_id,
       p.kind AS principal_kind,
       p.display_name,
       u.email,
       lower(m.valid_during) AS member_since,
       coalesce(array_agg(ra.role_id ORDER BY r.sort_order) FILTER (WHERE ra.role_id IS NOT NULL), '{}') AS role_ids,
       u.is_email_verified
FROM membership.organization_membership m
JOIN membership.organization o ON o.object_id = m.organization_object_id
JOIN membership.principal p ON p.object_id = m.principal_object_id
LEFT JOIN membership.user_principal u ON u.principal_object_id = m.principal_object_id
LEFT JOIN membership.role_assignment ra ON ra.membership_object_id = m.object_id AND upper_inf(ra.valid_during)
LEFT JOIN membership.role r ON r.id = ra.role_id
WHERE upper_inf(m.valid_during)
GROUP BY m.object_id, o.object_id, p.object_id, u.principal_object_id
$view$);

COMMENT ON VIEW membership.vw_member IS
    'Current memberships with the roles each holds now.';

SELECT core.ensure_view('membership.vw_user_principal', $view$
CREATE OR REPLACE VIEW membership.vw_user_principal
WITH (security_invoker = true) AS
SELECT p.object_id AS principal_object_id,
       p.display_name,
       u.email,
       u.is_email_verified,
       p.created_at,
       p.disabled_at,
       coalesce((SELECT jsonb_agg(jsonb_build_object('issuer', ei.issuer_id, 'subject', ei.subject) ORDER BY ei.linked_at)
                 FROM membership.external_identity ei WHERE ei.user_principal_object_id = p.object_id), '[]') AS logins,
       EXISTS (SELECT FROM membership.platform_operator po
               WHERE po.user_principal_object_id = p.object_id AND upper_inf(po.valid_during)) AS is_platform_operator
FROM membership.principal p
JOIN membership.user_principal u ON u.principal_object_id = p.object_id
$view$);

COMMENT ON VIEW membership.vw_user_principal IS
    'People with their logins and platform-operator status.';

SELECT core.ensure_view('membership.vw_connection', $view$
CREATE OR REPLACE VIEW membership.vw_connection
WITH (security_invoker = true) AS
SELECT c.object_id AS connection_object_id,
       c.organization_object_id,
       c.principal_object_id,
       p.display_name AS principal_display_name,
       c.agent_principal_object_id,
       c.client_name,
       c.role_id,
       CASE WHEN c.revoked_at IS NOT NULL THEN 'revoked'
            WHEN c.expires_at <= now() THEN 'expired'
            WHEN NOT coalesce(upper_inf(m.valid_during), false) THEN 'ended'
            WHEN o.status IS DISTINCT FROM 'active' THEN 'organization_inactive'
            WHEN o.agents_disabled_at IS NOT NULL THEN 'agents_disabled'
            WHEN c.paused_at IS NOT NULL THEN 'paused'
            ELSE 'active' END AS status,
       c.approved_at,
       c.expires_at,
       c.last_used_at,
       c.paused_at,
       c.paused_by_object_id,
       c.revoked_at,
       c.client_id
FROM membership.connection c
LEFT JOIN membership.organization_membership m ON m.object_id = c.membership_object_id
LEFT JOIN membership.organization o ON o.object_id = c.organization_object_id
LEFT JOIN membership.principal p ON p.object_id = c.principal_object_id
$view$);

COMMENT ON VIEW membership.vw_connection IS
    'Connections with their state: active, paused, agents_disabled (the owner switched the organization''s agents off), organization_inactive, ended (the membership ended), expired or revoked. Only active ones act.';

-- One connection as the API answers it, the same in every list and after every change.
CREATE OR REPLACE FUNCTION membership.fn_connection_json(c membership.vw_connection)
RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_strip_nulls(jsonb_build_object(
        'connectionId', c.connection_object_id,
        'organizationId', c.organization_object_id,
        'principalId', c.principal_object_id,
        'principalName', c.principal_display_name,
        'clientName', c.client_name,
        'clientId', c.client_id,
        'roleId', c.role_id,
        'status', c.status,
        'approvedAt', c.approved_at,
        'expiresAt', c.expires_at,
        'lastUsedAt', c.last_used_at,
        'pausedAt', c.paused_at));
$$;

SELECT core.ensure_view('api.vw_current_user', $view$
CREATE OR REPLACE VIEW api.vw_current_user
WITH (security_invoker = true) AS
SELECT p.object_id AS principal_object_id,
       p.display_name,
       u.email,
       u.is_email_verified,
       api.current_organization_id() AS organization_id
FROM membership.principal p
JOIN membership.user_principal u ON u.principal_object_id = p.object_id
WHERE p.object_id = api.current_principal_id()
$view$);

COMMENT ON VIEW api.vw_current_user IS
    'The calling person''s profile and the organization of the request.';

DO $$
BEGIN
    EXECUTE format('GRANT SELECT ON membership.vw_organization_owner, membership.vw_member, membership.vw_user_principal TO %I',
                   pg_temp.deployment_setting('database_kernel_role'));
END $$;
