/*
<pgmi-meta
    id="2cc1c534-c92c-4e57-a78b-17a5b4943a32"
    idempotent="false">
  <description>
    Row-level security on every membership table. The tables holding people
    and organizations are forced, so the owner, which runs every handler, is
    held to the policies too: a request reads itself, its organization's
    members, roles, invitations and audit trail, and the organizations it
    belongs to; a platform operator reads all of it. No request writes them:
    every write, and every read across organizations, goes through a kernel,
    which runs as the kernel role and passes by its own policy. The
    authorization vocabulary and the identity issuers are reference data the
    deploy syncs, so they are exempt from forcing; the kernel reads them.
    Policies wrap the readers as (SELECT ...), one InitPlan per query.
  </description>
  <sortKeys>
    <key>005/000/006</key>
  </sortKeys>
</pgmi-meta>
*/

ALTER TABLE membership.identity_issuer ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.role ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.permission ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.role_permission ENABLE ROW LEVEL SECURITY;

SELECT pg_temp.exempt_from_rls(t::regclass, 'reference data synced by the deploy; holds no organization rows')
FROM unnest(ARRAY['membership.identity_issuer', 'membership.role', 'membership.permission',
                  'membership.role_permission']) t;

ALTER TABLE membership.principal ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.principal FORCE ROW LEVEL SECURITY;
ALTER TABLE membership.user_principal ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.user_principal FORCE ROW LEVEL SECURITY;
ALTER TABLE membership.external_identity ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.external_identity FORCE ROW LEVEL SECURITY;
ALTER TABLE membership.service_principal ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.service_principal FORCE ROW LEVEL SECURITY;
ALTER TABLE membership.platform_operator ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.platform_operator FORCE ROW LEVEL SECURITY;
ALTER TABLE membership.organization ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.organization FORCE ROW LEVEL SECURITY;
ALTER TABLE membership.organization_membership ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.organization_membership FORCE ROW LEVEL SECURITY;
ALTER TABLE membership.role_assignment ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.role_assignment FORCE ROW LEVEL SECURITY;
ALTER TABLE membership.invitation ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.invitation FORCE ROW LEVEL SECURITY;
ALTER TABLE membership.audit_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.audit_event FORCE ROW LEVEL SECURITY;

CREATE POLICY principal_visible ON membership.principal FOR SELECT
    USING (object_id = (SELECT api.current_principal_id())
           OR EXISTS (SELECT FROM membership.organization_membership m
                      WHERE m.principal_object_id = principal.object_id
                        AND m.organization_object_id = (SELECT api.current_organization_id())
                        AND upper_inf(m.valid_during))
           OR (SELECT api.is_platform_operator()));

CREATE POLICY user_principal_visible ON membership.user_principal FOR SELECT
    USING (EXISTS (SELECT FROM membership.principal p WHERE p.object_id = user_principal.principal_object_id));

CREATE POLICY external_identity_visible ON membership.external_identity FOR SELECT
    USING (user_principal_object_id = (SELECT api.current_principal_id()) OR (SELECT api.is_platform_operator()));

CREATE POLICY service_principal_visible ON membership.service_principal FOR SELECT
    USING ((SELECT api.is_platform_operator()));

CREATE POLICY platform_operator_visible ON membership.platform_operator FOR SELECT
    USING (user_principal_object_id = (SELECT api.current_principal_id()) OR (SELECT api.is_platform_operator()));

CREATE POLICY organization_visible ON membership.organization FOR SELECT
    USING (object_id = (SELECT api.current_organization_id())
           OR EXISTS (SELECT FROM membership.organization_membership m
                      WHERE m.organization_object_id = organization.object_id
                        AND m.principal_object_id = (SELECT api.current_principal_id())
                        AND upper_inf(m.valid_during))
           OR (SELECT api.is_platform_operator()));

CREATE POLICY organization_membership_visible ON membership.organization_membership FOR SELECT
    USING (organization_object_id = (SELECT api.current_organization_id())
           OR principal_object_id = (SELECT api.current_principal_id())
           OR (SELECT api.is_platform_operator()));

CREATE POLICY role_assignment_visible ON membership.role_assignment FOR SELECT
    USING (organization_object_id = (SELECT api.current_organization_id())
           OR EXISTS (SELECT FROM membership.organization_membership m
                      WHERE m.object_id = role_assignment.membership_object_id
                        AND m.principal_object_id = (SELECT api.current_principal_id()))
           OR (SELECT api.is_platform_operator()));

CREATE POLICY invitation_visible ON membership.invitation FOR SELECT
    USING (organization_object_id = (SELECT api.current_organization_id()) OR (SELECT api.is_platform_operator()));

CREATE POLICY connection_visible ON membership.connection FOR SELECT
    USING (principal_object_id = (SELECT api.current_principal_id())
           OR (organization_object_id = (SELECT api.current_organization_id())
               AND (SELECT api.has_permission('members.manage')))
           OR (SELECT api.is_platform_operator()));

CREATE POLICY audit_event_visible ON membership.audit_event FOR SELECT
    USING (organization_object_id = (SELECT api.current_organization_id()) OR (SELECT api.is_platform_operator()));

SELECT pg_temp.kernel_passes(t::regclass, ops)
FROM (VALUES
    ('membership.identity_issuer',         '{select}'::text[]),
    ('membership.role',                    '{select}'),
    ('membership.permission',              '{select}'),
    ('membership.role_permission',         '{select}'),
    ('membership.principal',               '{select,insert,update}'),
    ('membership.user_principal',          '{select,insert,update}'),
    ('membership.external_identity',       '{select,insert}'),
    ('membership.service_principal',       '{select,insert}'),
    ('membership.platform_operator',       '{select,insert,update}'),
    ('membership.organization',            '{select,insert,update}'),
    ('membership.organization_membership', '{select,insert,update}'),
    ('membership.role_assignment',         '{select,insert,update}'),
    ('membership.invitation',              '{select,insert,update}'),
    ('membership.audit_event',             '{select,insert}')) v(t, ops);
