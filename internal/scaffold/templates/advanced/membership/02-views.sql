/*
<pgmi-meta
    id="b1000001-0002-4000-8000-000000000001"
    idempotent="true">
  <description>
    Membership query views: active users, organizations, memberships, invitations
  </description>
  <sortKeys>
    <key>005/000/002</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE VIEW membership.vw_active_users AS
SELECT object_id, email, display_name, email_verified, created_at, updated_at
FROM membership."user"
WHERE is_active = true;

CREATE OR REPLACE VIEW membership.vw_active_organizations AS
SELECT object_id, name, slug, owner_user_id, is_personal, created_at, updated_at
FROM membership.organization
WHERE is_active = true;

CREATE OR REPLACE VIEW membership.vw_user_owned_organizations AS
SELECT o.object_id, o.name, o.slug, o.owner_user_id, o.is_personal, o.created_at
FROM membership.organization o
WHERE o.is_active = true;

CREATE OR REPLACE VIEW membership.vw_user_memberships AS
SELECT
    om.object_id,
    om.organization_id,
    om.user_id,
    om.role,
    om.status,
    om.invited_at,
    om.joined_at,
    o.name AS organization_name,
    o.slug AS organization_slug,
    o.is_personal
FROM membership.organization_member om
JOIN membership.organization o ON o.object_id = om.organization_id
WHERE o.is_active = true;

CREATE OR REPLACE VIEW membership.vw_active_memberships AS
SELECT * FROM membership.vw_user_memberships
WHERE status = 'active';

CREATE OR REPLACE VIEW membership.vw_pending_invitations AS
SELECT * FROM membership.vw_user_memberships
WHERE status = 'pending';

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA membership TO %I', v_customer_role);
END $$;
