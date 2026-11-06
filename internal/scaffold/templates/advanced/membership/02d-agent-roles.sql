/*
<pgmi-meta
    id="d3b972e7-00ca-4d75-9a47-69092d4b8b9b"
    idempotent="false">
  <description>
    Membership structure, part 6: agent roles. A connection carries an
    agent.* role from data/authorization.json (membership.connection.role_id).
    Agent roles are never a member's: a role assignment or an invitation
    never names one. An organization can switch all its agents off.
  </description>
  <sortKeys>
    <key>005/000/002/004</key>
  </sortKeys>
</pgmi-meta>
*/

ALTER TABLE membership.role_assignment
    ADD CONSTRAINT role_assignment_member_role_check CHECK (role_id NOT LIKE 'agent.%');
ALTER TABLE membership.invitation
    ADD CONSTRAINT invitation_member_role_check CHECK (role_id NOT LIKE 'agent.%');

ALTER TABLE membership.organization
    ADD COLUMN agents_disabled_at timestamptz,
    ADD COLUMN agents_disabled_by_object_id uuid
        CONSTRAINT organization_agents_disabled_by_fkey REFERENCES membership.principal (object_id),
    ADD CONSTRAINT organization_agents_disabled_check
        CHECK ((agents_disabled_at IS NULL) = (agents_disabled_by_object_id IS NULL));

COMMENT ON COLUMN membership.organization.agents_disabled_at IS
    'Set while the owner has switched every agent of the organization off: no connection into it acts.';

SELECT pg_temp.exempt_from_fk_index('membership.organization', '{agents_disabled_by_object_id}',
                                    'records who acted; principals are never deleted');
