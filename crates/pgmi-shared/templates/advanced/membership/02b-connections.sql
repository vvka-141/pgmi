/*
<pgmi-meta
    id="eab54fa3-d8cd-4fce-bdcf-e2cef2641c72"
    idempotent="false">
  <description>
    Membership structure, part 4: connections. A connection lets one MCP
    client act for the person who approved it, in the organization that
    person chose: the person approves it while signed in on the web, inside
    an organization session, and hands the client the opaque handle. The
    connection is fixed to that membership, so it ends with the membership,
    on revocation, or at its expiry; it never names another organization and
    never provisions anyone. Only the handle's hash is stored. The person
    reads their own connections, and a request holding members.manage its
    organization's (policy in 06-rls.sql); only the connection kernels write.
    Each connection has its own agent principal, the third principal kind
    beside user and service: it never signs in and has no external identity;
    it acts only through its connection, with the connection's agent.* role
    intersected with its person's permissions. A connection is used through
    its handle, or, for an OAuth client, through the (issuer, client) the
    gateway verified, at most one live per person and client. It can be
    paused and resumed.
  </description>
  <sortKeys>
    <key>005/000/002/002</key>
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

CREATE TABLE membership.connection (
    object_id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    token_hash             text        CONSTRAINT connection_token_hash_check CHECK (length(token_hash) = 64),
    issuer_id              text        CONSTRAINT connection_issuer_fkey REFERENCES membership.identity_issuer (id),
    client_id              text
        CONSTRAINT connection_client_id_check CHECK (client_id IS NULL OR membership.fn_is_clean_name(client_id, 255)),
    principal_object_id    uuid        NOT NULL REFERENCES membership.user_principal (principal_object_id),
    agent_principal_object_id uuid     NOT NULL
        CONSTRAINT connection_agent_principal_key UNIQUE
        CONSTRAINT connection_agent_principal_fkey REFERENCES membership.agent_principal (principal_object_id),
    role_id                text        NOT NULL
        CONSTRAINT connection_role_id_check CHECK (role_id LIKE 'agent.%')
        CONSTRAINT connection_role_fkey REFERENCES membership.role (id),
    organization_object_id uuid        NOT NULL,
    membership_object_id   uuid        NOT NULL,
    client_name            text        NOT NULL
        CONSTRAINT connection_client_name_check CHECK (membership.fn_is_clean_name(client_name, 100)),
    approved_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at             timestamptz NOT NULL,
    last_used_at           timestamptz,
    revoked_at             timestamptz,
    revoked_by_object_id   uuid        REFERENCES membership.principal (object_id),
    paused_at              timestamptz,
    paused_by_object_id    uuid        CONSTRAINT connection_paused_by_fkey REFERENCES membership.principal (object_id),
    CONSTRAINT connection_token_hash_key UNIQUE (token_hash),
    CONSTRAINT connection_membership_fkey
        FOREIGN KEY (organization_object_id, membership_object_id)
        REFERENCES membership.organization_membership (organization_object_id, object_id),
    CONSTRAINT connection_expires_check CHECK (expires_at > approved_at),
    CONSTRAINT connection_revoked_check CHECK (revoked_at IS NOT NULL OR revoked_by_object_id IS NULL),
    CONSTRAINT connection_paused_check CHECK ((paused_at IS NULL) = (paused_by_object_id IS NULL)),
    CONSTRAINT connection_credential_check
        CHECK ((token_hash IS NULL) = (client_id IS NOT NULL) AND (client_id IS NULL) = (issuer_id IS NULL))
);

CREATE INDEX ix_connection_principal ON membership.connection (principal_object_id);
CREATE INDEX ix_connection_membership ON membership.connection (organization_object_id, membership_object_id);
CREATE INDEX ix_connection_revoked_by ON membership.connection (revoked_by_object_id);
CREATE UNIQUE INDEX uq_connection_client ON membership.connection (principal_object_id, issuer_id, client_id)
    WHERE client_id IS NOT NULL AND revoked_at IS NULL;

COMMENT ON TABLE membership.connection IS
    'One MCP client acting for the person who approved it, in the organization chosen at approval. Valid while not revoked, not paused, not expired, and the membership it is fixed to is current. Only the hash of its handle is stored.';
COMMENT ON COLUMN membership.connection.agent_principal_object_id IS
    'The agent that acts through this connection: the actual principal of its requests, while the approving person is the effective one.';
COMMENT ON COLUMN membership.connection.role_id IS
    'The agent.* role chosen at approval: the agent holds its permissions intersected with its person''s, recomputed per request.';
COMMENT ON COLUMN membership.connection.paused_at IS
    'Set while paused: the agent acts as no one until resumed. paused_by_object_id is who paused it.';
COMMENT ON COLUMN membership.connection.client_id IS
    'The OAuth client this connection was approved for (with issuer_id); NULL for a connection used through its handle (token_hash).';

ALTER TABLE membership.connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.connection FORCE ROW LEVEL SECURITY;
SELECT pg_temp.kernel_passes('membership.connection', '{select,insert,update}');

SELECT pg_temp.exempt_from_fk_index('membership.connection', '{role_id}',
                                    'roles are reference data the deploy syncs; connections are read by person and organization');
SELECT pg_temp.exempt_from_fk_index('membership.connection', '{paused_by_object_id}', 'records who acted; principals are never deleted');
SELECT pg_temp.exempt_from_fk_index('membership.connection', '{issuer_id}', 'issuers are reference data the deploy syncs');
