/*
<pgmi-meta
    id="6205c4d2-aaa0-4bcd-856e-2c34f49e955b"
    idempotent="false">
  <description>
    Membership structure, part 3: sessions. A login session is browser-wide:
    the transport opens it (api.open_login_session) after the identity
    provider signed a person in through one of their logins, and keeps its
    opaque handle in an httpOnly cookie. Only the handle's hash is stored. It
    records the provider's authentication methods (the amr claim) and whether
    they include a second factor, which platform permissions require. An
    organization session belongs to one browser tab and is fixed to one
    membership, so to one organization, for its whole life; it is valid only
    within its own login session. Both tables are forced to row-level
    security with no policy but the kernel role's: only the session kernels
    touch them.
  </description>
  <sortKeys>
    <key>005/000/002/001</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE TABLE membership.login_session (
    object_id                   uuid        PRIMARY KEY DEFAULT uuidv7(),
    token_hash                  text        NOT NULL CONSTRAINT login_session_token_hash_check CHECK (length(token_hash) = 64),
    principal_object_id         uuid        NOT NULL REFERENCES membership.user_principal (principal_object_id),
    external_identity_object_id uuid        NOT NULL REFERENCES membership.external_identity (object_id),
    authentication_methods      text[]      NOT NULL DEFAULT '{}',
    is_mfa                      boolean     NOT NULL,
    created_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_seen_at                timestamptz NOT NULL DEFAULT clock_timestamp(),
    idle_expires_at             timestamptz NOT NULL,
    absolute_expires_at         timestamptz NOT NULL,
    revoked_at                  timestamptz,
    revoke_reason               text,
    CONSTRAINT login_session_token_hash_key UNIQUE (token_hash),
    CONSTRAINT login_session_revoked_check CHECK ((revoked_at IS NULL) = (revoke_reason IS NULL))
);

CREATE INDEX ix_login_session_principal ON membership.login_session (principal_object_id);
CREATE INDEX ix_login_session_identity ON membership.login_session (external_identity_object_id);

COMMENT ON TABLE membership.login_session IS
    'Browser-wide session opened after the identity provider signed a person in. Idle 12 hours, absolute 7 days, revocable. Only the hash of its handle is stored.';

CREATE TABLE membership.organization_session (
    object_id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    token_hash              text        NOT NULL CONSTRAINT organization_session_token_hash_check CHECK (length(token_hash) = 64),
    login_session_object_id uuid        NOT NULL REFERENCES membership.login_session (object_id),
    organization_object_id  uuid        NOT NULL,
    membership_object_id    uuid        NOT NULL,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    ended_at                timestamptz,
    CONSTRAINT organization_session_token_hash_key UNIQUE (token_hash),
    CONSTRAINT organization_session_membership_fkey
        FOREIGN KEY (organization_object_id, membership_object_id)
        REFERENCES membership.organization_membership (organization_object_id, object_id)
);

CREATE INDEX ix_organization_session_login ON membership.organization_session (login_session_object_id);
CREATE INDEX ix_organization_session_membership ON membership.organization_session (organization_object_id, membership_object_id);

COMMENT ON TABLE membership.organization_session IS
    'One browser tab working in one organization, fixed to one membership: it ends with that membership, its login session, or the tab. Switching organization opens another.';

ALTER TABLE membership.login_session ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.login_session FORCE ROW LEVEL SECURITY;
ALTER TABLE membership.organization_session ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership.organization_session FORCE ROW LEVEL SECURITY;

SELECT pg_temp.kernel_passes('membership.login_session', '{select,insert,update}');
SELECT pg_temp.kernel_passes('membership.organization_session', '{select,insert,update}');
