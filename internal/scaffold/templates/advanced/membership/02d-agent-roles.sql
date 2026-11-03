/*
<pgmi-meta
    id="d3b972e7-00ca-4d75-9a47-69092d4b8b9b"
    idempotent="false">
  <description>
    Membership structure, part 6: agent roles and the agent lifecycle. A
    connection carries an agent.* role from data/authorization.json, chosen
    at approval; its agent holds that role's permissions intersected with
    its person's. Agent roles are never a member's: a role assignment or an
    invitation never names one. A connection can be paused (by its person or
    an organization manager) and resumed; an organization can switch all its
    agents off. Existing connections get their role in
    07a-connection-roles.sql, once the catalogue holds the agent roles.
  </description>
  <sortKeys>
    <key>005/000/002/004</key>
  </sortKeys>
</pgmi-meta>
*/

ALTER TABLE membership.connection
    ADD COLUMN role_id text
        CONSTRAINT connection_role_id_check CHECK (role_id LIKE 'agent.%')
        CONSTRAINT connection_role_fkey REFERENCES membership.role (id),
    ADD COLUMN paused_at timestamptz,
    ADD COLUMN paused_by_object_id uuid CONSTRAINT connection_paused_by_fkey REFERENCES membership.principal (object_id),
    ADD CONSTRAINT connection_paused_check CHECK ((paused_at IS NULL) = (paused_by_object_id IS NULL));

COMMENT ON COLUMN membership.connection.role_id IS
    'The agent.* role chosen at approval: the agent holds its permissions intersected with its person''s, recomputed per request.';
COMMENT ON COLUMN membership.connection.paused_at IS
    'Set while paused: the agent acts as no one until resumed. paused_by_object_id is who paused it.';

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

SELECT pg_temp.exempt_from_fk_index('membership.connection', '{role_id}',
                                    'roles are reference data the deploy syncs; connections are read by person and organization');
SELECT pg_temp.exempt_from_fk_index(t::regclass, c, 'records who acted; principals are never deleted')
FROM (VALUES ('membership.connection', '{paused_by_object_id}'::name[]),
             ('membership.organization', '{agents_disabled_by_object_id}')) v(t, c);
