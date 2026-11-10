/*
<pgmi-meta
    id="3f0c6d2a-8b1e-4c57-9a43-5d2e71b08c64"
    idempotent="false">
  <description>
    Membership structure, part 8: connections by OAuth client. An MCP client
    that signs in at the identity provider presents an access token; the
    gateway verifies it and hands the database the token's issuer, subject
    and client id. The first call from a (person, client) pair is a pending
    connection with a short pairing code; the person approves it on the web
    with that code, choosing organization and agent role, and the pair then
    acts through an approved connection that has no handle
    (membership.connection.issuer_id, client_id). Only the kernels read or
    write pending connections.
  </description>
  <sortKeys>
    <key>005/000/002/005</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE TABLE membership.pending_connection (
    object_id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    principal_object_id uuid        NOT NULL REFERENCES membership.user_principal (principal_object_id),
    issuer_id           text        NOT NULL REFERENCES membership.identity_issuer (id),
    client_id           text        NOT NULL
        CONSTRAINT pending_connection_client_id_check CHECK (membership.fn_is_clean_name(client_id, 255)),
    pairing_code        text        NOT NULL
        CONSTRAINT pending_connection_pairing_code_check CHECK (pairing_code ~ '^[2-9A-HJKMNP-TV-Z]{6}$'),
    requested_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at          timestamptz NOT NULL,
    CONSTRAINT pending_connection_client_key UNIQUE (principal_object_id, issuer_id, client_id),
    CONSTRAINT pending_connection_pairing_code_key UNIQUE (principal_object_id, pairing_code),
    CONSTRAINT pending_connection_expires_check CHECK (expires_at > requested_at)
);

COMMENT ON TABLE membership.pending_connection IS
    'An OAuth client that called for a person who has not approved it yet. The pairing code goes only to that same (issuer, subject, client) and is approved only by its person, before it expires; a later call after expiry issues a new one.';

ALTER TABLE membership.pending_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.pending_connection FORCE ROW LEVEL SECURITY;
SELECT pg_temp.kernel_passes('membership.pending_connection', '{select,insert,update,delete}');

SELECT pg_temp.exempt_from_fk_index('membership.pending_connection', '{issuer_id}', 'issuers are reference data the deploy syncs');
