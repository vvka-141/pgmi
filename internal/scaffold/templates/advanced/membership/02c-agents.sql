/*
<pgmi-meta
    id="5c9867d0-b33c-4d16-a80f-f1014ad59fcc"
    idempotent="false">
  <description>
    Membership structure, part 5: agents. An approved connection gets its own
    principal, an agent: the third principal kind beside user and service. It
    never signs in and has no external identity; it acts only through its
    connection, for the person who approved it. Every connection has exactly
    one agent; connections approved before agents existed get theirs here.
    The audit log records the actor kind of every change: human, agent,
    service or system. Rows written before actor kinds existed carry none, so
    the NOT NULL constraint holds for new rows only. Only the kernels read or
    write agents.
  </description>
  <sortKeys>
    <key>005/000/002/003</key>
  </sortKeys>
</pgmi-meta>
*/

ALTER TABLE membership.principal
    DROP CONSTRAINT principal_kind_check,
    ADD CONSTRAINT principal_kind_check CHECK (kind IN ('user', 'service', 'agent'));

COMMENT ON TABLE membership.principal IS
    'Anything that acts and is attributed: a user (membership.user_principal), a service (membership.service_principal) or an agent (membership.agent_principal). Never deleted; a disabled principal resolves to no one.';

CREATE TABLE membership.agent_principal (
    principal_object_id uuid PRIMARY KEY,
    kind                text NOT NULL GENERATED ALWAYS AS ('agent') STORED,
    CONSTRAINT agent_principal_principal_fkey
        FOREIGN KEY (principal_object_id, kind) REFERENCES membership.principal (object_id, kind)
);

COMMENT ON TABLE membership.agent_principal IS
    'An MCP client acting through one approved connection (membership.connection.agent_principal_object_id) for the person who approved it. No login, no external identity.';

ALTER TABLE membership.agent_principal ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.agent_principal FORCE ROW LEVEL SECURITY;
SELECT pg_temp.kernel_passes('membership.agent_principal', '{select,insert}');

ALTER TABLE membership.connection
    ADD COLUMN agent_principal_object_id uuid,
    ADD CONSTRAINT connection_agent_principal_key UNIQUE (agent_principal_object_id),
    ADD CONSTRAINT connection_agent_principal_fkey
        FOREIGN KEY (agent_principal_object_id) REFERENCES membership.agent_principal (principal_object_id);

DO $$
DECLARE
    v_caller text := current_user;
BEGIN
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_kernel_role'));
    IF EXISTS (SELECT FROM membership.connection WHERE agent_principal_object_id IS NULL) THEN
        WITH c AS MATERIALIZED (
            SELECT object_id, client_name, uuidv7() AS agent_id
            FROM membership.connection WHERE agent_principal_object_id IS NULL
        ), p AS (
            INSERT INTO membership.principal (object_id, kind, display_name)
            SELECT agent_id, 'agent', client_name FROM c
        ), a AS (
            INSERT INTO membership.agent_principal (principal_object_id)
            SELECT agent_id FROM c
        )
        UPDATE membership.connection n SET agent_principal_object_id = c.agent_id
        FROM c WHERE n.object_id = c.object_id;
    END IF;
    EXECUTE format('SET ROLE %I', v_caller);
END $$;

ALTER TABLE membership.connection ALTER COLUMN agent_principal_object_id SET NOT NULL;

COMMENT ON COLUMN membership.connection.agent_principal_object_id IS
    'The agent that acts through this connection: the actual principal of its requests, while the approving person is the effective one.';

ALTER TABLE membership.audit_event
    ADD COLUMN actor_kind text CONSTRAINT audit_event_actor_kind_check CHECK (actor_kind IN ('human', 'agent', 'service', 'system')),
    ADD CONSTRAINT audit_event_actor_kind_not_null NOT NULL actor_kind NOT VALID;

COMMENT ON COLUMN membership.audit_event.actor_kind IS
    'What really acted: human (a person signed in), agent (an MCP client through its connection), service (a machine actor), system (no principal: the deploy, a sign-in). NULL only on rows written before actor kinds were recorded.';
