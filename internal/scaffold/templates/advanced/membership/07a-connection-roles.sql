/*
<pgmi-meta
    id="15bb8a86-b552-44a6-a833-5e045ddda697"
    idempotent="false">
  <description>
    Membership structure, part 7: every connection has an agent role, and
    organization managers see their organization's connections. Runs after
    the seed, so the agent roles exist. A connection approved before agent
    roles existed gets the narrowest agent role covering everything its
    person holds in its organization today, so its agent keeps exactly the
    reach it had (role intersected with person); nobody re-approves. The
    select policy lets a person read their own connections and a request
    holding members.manage read its organization's.
  </description>
  <sortKeys>
    <key>005/000/007/001</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$
DECLARE
    v_caller text := current_user;
BEGIN
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_kernel_role'));
    UPDATE membership.connection c
    SET role_id = membership.fn_covering_agent_role(
        membership.fn_effective_permissions(c.principal_object_id, c.organization_object_id, false))
    WHERE c.role_id IS NULL;
    EXECUTE format('SET ROLE %I', v_caller);
END $$;

ALTER TABLE membership.connection ALTER COLUMN role_id SET NOT NULL;

DROP POLICY connection_visible ON membership.connection;
CREATE POLICY connection_visible ON membership.connection FOR SELECT
    USING (principal_object_id = (SELECT api.current_principal_id())
           OR (organization_object_id = (SELECT api.current_organization_id())
               AND (SELECT api.has_permission('members.manage')))
           OR (SELECT api.is_platform_operator()));
