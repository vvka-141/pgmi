/*
<pgmi-meta
    id="b1000001-0004-4000-8000-000000000001"
    idempotent="true">
  <description>
    User claims view: computes aggregate claims for all users (administrative)
  </description>
  <sortKeys>
    <key>005/000/004</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE VIEW membership.vw_user_claims AS
SELECT
    u.object_id AS user_id,
    u.email,
    u.display_name,
    u.email_verified,
    u.is_active,
    COALESCE(array_agg(DISTINCT r.name) FILTER (WHERE r.name IS NOT NULL), '{}') AS roles,
    (SELECT COALESCE(array_agg(sub.organization_id), '{}')
     FROM (SELECT DISTINCT om2.organization_id FROM membership.organization_member om2
           WHERE om2.user_id = u.object_id AND om2.status = 'active' LIMIT 1000) sub) AS member_org_ids,
    (SELECT COALESCE(array_agg(sub.object_id), '{}')
     FROM (SELECT DISTINCT o2.object_id FROM membership.organization o2
           WHERE o2.owner_user_id = u.object_id AND o2.is_active LIMIT 1000) sub) AS owner_org_ids,
    COALESCE(
        jsonb_agg(DISTINCT jsonb_build_object('provider', ui.idp_provider, 'subject_id', ui.idp_subject_id))
        FILTER (WHERE ui.object_id IS NOT NULL),
        '[]'::jsonb
    ) AS identities
FROM membership."user" u
LEFT JOIN membership.user_role ur ON ur.user_object_id = u.object_id
LEFT JOIN membership.role r ON r.object_id = ur.role_object_id
LEFT JOIN membership.user_identity ui ON ui.user_object_id = u.object_id
GROUP BY u.object_id, u.email, u.display_name, u.email_verified, u.is_active;

DO $$
DECLARE
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON membership.vw_user_claims TO %I', v_admin_role);
END $$;
