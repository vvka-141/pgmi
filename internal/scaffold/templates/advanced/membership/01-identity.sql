/*
<pgmi-meta
    id="5d9a3991-ec14-41ef-b30e-fcae682b58e1"
    idempotent="false">
  <description>
    Membership structure, part 1: who can act. A principal is anything that
    acts and is attributed in the audit trail: exactly one of a user (a person
    signing in through an identity provider) or a service (a machine actor with
    its own database login). A person's logins are external identities keyed by
    issuer and subject; a subject belongs to one person for good. Platform
    operators are a time-ranged status granted at deploy, never a flag.
    Identity tables carry no authorization: no roles, no organization pointers.
  </description>
  <sortKeys>
    <key>005/000/001</key>
  </sortKeys>
</pgmi-meta>
*/

-- oidc: a real identity provider, keyed by its issuer URL (the iss claim).
-- development: impersonation and test issuers, never for a real sign-in.
-- The id cannot contain '|', so a header 'issuer|subject' splits at its first pipe.
CREATE TABLE membership.identity_issuer (
    id   text PRIMARY KEY,
    kind text NOT NULL CONSTRAINT identity_issuer_kind_check CHECK (kind IN ('oidc', 'development')),
    CONSTRAINT identity_issuer_id_format_check CHECK (
        (kind = 'oidc' AND id ~ '^https?://[^\s|]+$' AND length(id) <= 255)
        OR (kind = 'development' AND id ~ '^[a-z][a-z0-9-]{1,30}$'))
);

COMMENT ON TABLE membership.identity_issuer IS
    'Identity providers an external identity may come from: oidc (issuer URL) or development (impersonation and test issuers).';

CREATE DOMAIN membership.email_address AS text
    CONSTRAINT email_address_format_check CHECK (VALUE IS NULL OR membership.fn_is_well_formed_email(VALUE));

CREATE TABLE membership.principal (
    object_id       uuid        PRIMARY KEY DEFAULT uuidv7(),
    kind            text        NOT NULL CONSTRAINT principal_kind_check CHECK (kind IN ('user', 'service')),
    display_name    text        NOT NULL CONSTRAINT principal_display_name_check CHECK (membership.fn_is_clean_name(display_name, 200)),
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    disabled_at     timestamptz,
    disabled_reason text,
    CONSTRAINT principal_disabled_reason_check CHECK (
        (disabled_at IS NULL) = (disabled_reason IS NULL)
        AND (disabled_reason IS NULL OR membership.fn_is_clean_name(disabled_reason, 500))),
    CONSTRAINT principal_object_id_kind_key UNIQUE (object_id, kind)
);

COMMENT ON TABLE membership.principal IS
    'Anything that acts and is attributed: a user (membership.user_principal) or a service (membership.service_principal). Never deleted; a disabled principal resolves to no one.';

CREATE TABLE membership.user_principal (
    principal_object_id uuid        PRIMARY KEY,
    kind                text        NOT NULL GENERATED ALWAYS AS ('user') STORED,
    email               membership.email_address NOT NULL,
    verified_email      membership.email_address,
    email_verified_at   timestamptz,
    is_email_verified   boolean     NOT NULL GENERATED ALWAYS AS (coalesce(verified_email = email, false)) STORED,
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT user_principal_principal_fkey
        FOREIGN KEY (principal_object_id, kind) REFERENCES membership.principal (object_id, kind),
    CONSTRAINT user_principal_email_key UNIQUE (email),
    CONSTRAINT user_principal_verification_check CHECK ((verified_email IS NULL) = (email_verified_at IS NULL))
);

COMMENT ON TABLE membership.user_principal IS
    'A person: email and its verification. Verification belongs to an address: a new email is unverified until an identity provider asserts it.';

CREATE TABLE membership.external_identity (
    object_id                uuid        PRIMARY KEY DEFAULT uuidv7(),
    issuer_id                text        NOT NULL REFERENCES membership.identity_issuer (id),
    subject                  text        NOT NULL CONSTRAINT external_identity_subject_check CHECK (membership.fn_is_valid_subject(subject)),
    user_principal_object_id uuid        NOT NULL REFERENCES membership.user_principal (principal_object_id),
    linked_at                timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT external_identity_issuer_subject_key UNIQUE (issuer_id, subject)
);

CREATE INDEX ix_external_identity_user ON membership.external_identity (user_principal_object_id);

COMMENT ON TABLE membership.external_identity IS
    'A login at an identity provider (issuer + subject) linked to one person for good: the pair is never handed to someone else.';

CREATE TABLE membership.service_principal (
    id                  text PRIMARY KEY CONSTRAINT service_principal_id_check CHECK (membership.fn_is_dotted_code(id)),
    principal_object_id uuid NOT NULL,
    kind                text NOT NULL GENERATED ALWAYS AS ('service') STORED,
    description         text NOT NULL CONSTRAINT service_principal_description_check CHECK (membership.fn_is_clean_name(description, 500)),
    database_role       name NOT NULL,
    CONSTRAINT service_principal_principal_key UNIQUE (principal_object_id),
    CONSTRAINT service_principal_database_role_key UNIQUE (database_role),
    CONSTRAINT service_principal_principal_fkey
        FOREIGN KEY (principal_object_id, kind) REFERENCES membership.principal (object_id, kind)
);

COMMENT ON TABLE membership.service_principal IS
    'A machine actor (a background worker) connecting as its own database login role, attributed from session_user.';

CREATE TABLE membership.platform_operator (
    object_id                uuid      PRIMARY KEY DEFAULT uuidv7(),
    user_principal_object_id uuid      NOT NULL REFERENCES membership.user_principal (principal_object_id),
    valid_during             tstzrange NOT NULL,
    note                     text      NOT NULL CONSTRAINT platform_operator_note_check CHECK (membership.fn_is_clean_name(note, 500)),
    CONSTRAINT platform_operator_valid_during_check CHECK (
        NOT isempty(valid_during) AND NOT lower_inf(valid_during) AND lower_inc(valid_during) AND NOT upper_inc(valid_during)),
    CONSTRAINT platform_operator_period_key UNIQUE (user_principal_object_id, valid_during WITHOUT OVERLAPS)
);

COMMENT ON TABLE membership.platform_operator IS
    'Platform-operator status of a person over time, synced at deploy from the platform_operators parameter. Opens platform permissions, never an organization''s data.';
