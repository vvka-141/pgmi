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
    reads their own connections (policy in 06-rls.sql); only the connection
    kernels write.
  </description>
  <sortKeys>
    <key>005/000/002/002</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE TABLE membership.connection (
    object_id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    token_hash             text        NOT NULL CONSTRAINT connection_token_hash_check CHECK (length(token_hash) = 64),
    principal_object_id    uuid        NOT NULL REFERENCES membership.user_principal (principal_object_id),
    organization_object_id uuid        NOT NULL,
    membership_object_id   uuid        NOT NULL,
    client_name            text        NOT NULL
        CONSTRAINT connection_client_name_check CHECK (membership.fn_is_clean_name(client_name, 100)),
    approved_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at             timestamptz NOT NULL,
    last_used_at           timestamptz,
    revoked_at             timestamptz,
    revoked_by_object_id   uuid        REFERENCES membership.principal (object_id),
    CONSTRAINT connection_token_hash_key UNIQUE (token_hash),
    CONSTRAINT connection_membership_fkey
        FOREIGN KEY (organization_object_id, membership_object_id)
        REFERENCES membership.organization_membership (organization_object_id, object_id),
    CONSTRAINT connection_expires_check CHECK (expires_at > approved_at),
    CONSTRAINT connection_revoked_check CHECK (revoked_at IS NOT NULL OR revoked_by_object_id IS NULL)
);

CREATE INDEX ix_connection_principal ON membership.connection (principal_object_id);
CREATE INDEX ix_connection_membership ON membership.connection (organization_object_id, membership_object_id);
CREATE INDEX ix_connection_revoked_by ON membership.connection (revoked_by_object_id);

COMMENT ON TABLE membership.connection IS
    'One MCP client acting for the person who approved it, in the organization chosen at approval. Valid while not revoked, not expired, and the membership it is fixed to is current. Only the hash of its handle is stored.';

ALTER TABLE membership.connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.connection FORCE ROW LEVEL SECURITY;
SELECT pg_temp.kernel_passes('membership.connection', '{select,insert,update}');
