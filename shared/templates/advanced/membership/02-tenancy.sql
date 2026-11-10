/*
<pgmi-meta
    id="fc16a0f1-3ec1-4046-9ce6-6142b3c1045d"
    idempotent="false">
  <description>
    Membership structure, part 2: what a principal may do where. An
    organization (the tenant) has members: a principal's membership over a time
    range; leaving and rejoining makes a new row. Members hold system-defined
    roles over time ranges inside their membership (a temporal foreign key);
    a role grants permissions from the closed vocabulary in
    membership.permission, which the framework's handler registry references.
    Ownership is the role org.owner, held by one member at a time (an exclusion
    constraint); no kernel offers a path to zero owners. Invitations offer a
    membership to an email address without creating anyone. The audit log
    records every change with the actual and effective principal and the
    database login.
  </description>
  <sortKeys>
    <key>005/000/002</key>
  </sortKeys>
</pgmi-meta>
*/

-- Validity ranges are [from, to) with a real start; an open upper bound means
-- current. Kernels open and close them at one clock_timestamp() per statement.

CREATE TABLE membership.organization (
    object_id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    name                 text        NOT NULL CONSTRAINT organization_name_check CHECK (membership.fn_is_clean_name(name, 200)),
    slug                 text        NOT NULL CONSTRAINT organization_slug_check CHECK (membership.fn_slug_problem(slug) IS NULL),
    -- pending: created for an owner who has not accepted yet
    status               text        NOT NULL CONSTRAINT organization_status_check CHECK (status IN ('pending', 'active', 'suspended', 'archived')),
    status_changed_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by_object_id uuid        REFERENCES membership.principal (object_id),
    CONSTRAINT organization_slug_key UNIQUE (slug)
);

COMMENT ON TABLE membership.organization IS
    'Tenant. No owner column: the owner is the member holding org.owner. Never deleted; archiving ends its memberships.';

CREATE TABLE membership.organization_membership (
    object_id              uuid      PRIMARY KEY DEFAULT uuidv7(),
    organization_object_id uuid      NOT NULL REFERENCES membership.organization (object_id),
    principal_object_id    uuid      NOT NULL REFERENCES membership.principal (object_id),
    valid_during           tstzrange NOT NULL,
    created_by_object_id   uuid      REFERENCES membership.principal (object_id),
    ended_by_object_id     uuid      REFERENCES membership.principal (object_id),
    end_reason             text,
    CONSTRAINT organization_membership_valid_during_check CHECK (
        NOT isempty(valid_during) AND NOT lower_inf(valid_during) AND lower_inc(valid_during) AND NOT upper_inc(valid_during)),
    CONSTRAINT organization_membership_end_reason_check CHECK (CASE
        WHEN upper_inf(valid_during) THEN end_reason IS NULL AND ended_by_object_id IS NULL
        ELSE end_reason IN ('left', 'removed', 'organization_archived')
    END),
    CONSTRAINT organization_membership_organization_object_key UNIQUE (organization_object_id, object_id),
    CONSTRAINT organization_membership_organization_object_period_key
        UNIQUE (organization_object_id, object_id, valid_during WITHOUT OVERLAPS),
    CONSTRAINT organization_membership_principal_period_key
        UNIQUE (organization_object_id, principal_object_id, valid_during WITHOUT OVERLAPS)
);

CREATE UNIQUE INDEX uq_organization_membership_current
    ON membership.organization_membership (organization_object_id, principal_object_id) WHERE upper_inf(valid_during);
CREATE INDEX ix_organization_membership_principal ON membership.organization_membership (principal_object_id);

COMMENT ON TABLE membership.organization_membership IS
    'A principal''s membership in an organization over valid_during. Leaving closes the range; rejoining opens a new row. Never overlapping, never deleted.';

-- Rows: data/authorization.json, synced every deploy.
CREATE TABLE membership.role (
    id         text PRIMARY KEY CONSTRAINT role_id_check CHECK (membership.fn_is_dotted_code(id)),
    name       text NOT NULL CONSTRAINT role_name_check CHECK (membership.fn_is_clean_name(name, 100)),
    sort_order int  NOT NULL
);

COMMENT ON TABLE membership.role IS
    'System-defined role, the same in every organization, keyed by a stable code (org.owner, org.admin ...).';

-- Rows: data/authorization.json. Registration resolves declared permissions
-- against this table and never creates one.
CREATE TABLE membership.permission (
    id          text    PRIMARY KEY CONSTRAINT permission_id_format_check CHECK (id ~ '^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$'),
    is_platform boolean NOT NULL,
    description text    NOT NULL CONSTRAINT permission_description_check CHECK (membership.fn_is_clean_name(description, 500)),
    CONSTRAINT permission_kind_key UNIQUE (id, is_platform)
);

COMMENT ON TABLE membership.permission IS
    'Closed vocabulary of operation permissions (resource.action). An id never changes meaning once used. is_platform: an operator permission, never granted inside an organization.';

ALTER TABLE api.handler_permission
    ADD CONSTRAINT handler_permission_permission_fkey
        FOREIGN KEY (permission_id) REFERENCES membership.permission (id);

-- The composite key pins is_platform = false: a role never grants a platform permission.
CREATE TABLE membership.role_permission (
    role_id       text    NOT NULL REFERENCES membership.role (id),
    permission_id text    NOT NULL,
    is_platform   boolean NOT NULL DEFAULT false CONSTRAINT role_permission_tenant_only_check CHECK (NOT is_platform),
    PRIMARY KEY (role_id, permission_id),
    CONSTRAINT role_permission_permission_fkey
        FOREIGN KEY (permission_id, is_platform) REFERENCES membership.permission (id, is_platform)
);

CREATE INDEX ix_role_permission_permission ON membership.role_permission (permission_id, is_platform);

COMMENT ON TABLE membership.role_permission IS
    'The permissions a role grants. Organization permissions only.';

CREATE TABLE membership.role_assignment (
    object_id              uuid      PRIMARY KEY DEFAULT uuidv7(),
    organization_object_id uuid      NOT NULL,
    membership_object_id   uuid      NOT NULL,
    role_id                text      NOT NULL REFERENCES membership.role (id),
    valid_during           tstzrange NOT NULL,
    granted_by_object_id   uuid      REFERENCES membership.principal (object_id),
    revoked_by_object_id   uuid      REFERENCES membership.principal (object_id),
    CONSTRAINT role_assignment_membership_fkey
        FOREIGN KEY (organization_object_id, membership_object_id, PERIOD valid_during)
        REFERENCES membership.organization_membership (organization_object_id, object_id, PERIOD valid_during),
    CONSTRAINT role_assignment_valid_during_check CHECK (
        NOT isempty(valid_during) AND NOT lower_inf(valid_during) AND lower_inc(valid_during) AND NOT upper_inc(valid_during)),
    CONSTRAINT role_assignment_revoked_check CHECK (revoked_by_object_id IS NULL OR NOT upper_inf(valid_during)),
    CONSTRAINT role_assignment_period_key UNIQUE (membership_object_id, role_id, valid_during WITHOUT OVERLAPS),
    CONSTRAINT role_assignment_single_owner_excl
        EXCLUDE USING gist (organization_object_id WITH =, valid_during WITH &&) WHERE (role_id = 'org.owner')
);

CREATE UNIQUE INDEX uq_role_assignment_current
    ON membership.role_assignment (membership_object_id, role_id) WHERE upper_inf(valid_during);
CREATE INDEX ix_role_assignment_organization ON membership.role_assignment (organization_object_id, membership_object_id);

COMMENT ON TABLE membership.role_assignment IS
    'A role held by a membership over valid_during, which lies within the membership''s. Revoking closes the range; regaining opens a new row.';

CREATE TABLE membership.invitation (
    object_id                     uuid        PRIMARY KEY DEFAULT uuidv7(),
    organization_object_id        uuid        NOT NULL REFERENCES membership.organization (object_id),
    email                         membership.email_address NOT NULL,
    role_id                       text        NOT NULL REFERENCES membership.role (id),
    invited_by_object_id          uuid        REFERENCES membership.principal (object_id),
    created_at                    timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at                    timestamptz NOT NULL,
    accepted_at                   timestamptz,
    accepted_membership_object_id uuid,
    revoked_at                    timestamptz,
    revoked_by_object_id          uuid        REFERENCES membership.principal (object_id),
    CONSTRAINT invitation_accepted_membership_fkey
        FOREIGN KEY (organization_object_id, accepted_membership_object_id)
        REFERENCES membership.organization_membership (organization_object_id, object_id),
    CONSTRAINT invitation_expiry_check CHECK (expires_at > created_at),
    CONSTRAINT invitation_accepted_check CHECK ((accepted_at IS NULL) = (accepted_membership_object_id IS NULL)),
    CONSTRAINT invitation_single_resolution_check CHECK (accepted_at IS NULL OR revoked_at IS NULL)
);

CREATE UNIQUE INDEX uq_invitation_pending
    ON membership.invitation (organization_object_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX ix_invitation_email_pending
    ON membership.invitation (email) WHERE accepted_at IS NULL AND revoked_at IS NULL;

COMMENT ON TABLE membership.invitation IS
    'An offer of membership with an intended role to an email address. Accepted only by a person whose verified email is that address; at most one pending per organization and address.';

-- Written by the kernels only. detail carries ids and codes, never personal data.
CREATE TABLE membership.audit_event (
    object_id                     bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at                   timestamptz NOT NULL DEFAULT clock_timestamp(),
    action                        text        NOT NULL CONSTRAINT audit_event_action_check CHECK (membership.fn_is_dotted_code(action)),
    actual_principal_object_id    uuid        REFERENCES membership.principal (object_id),
    effective_principal_object_id uuid        REFERENCES membership.principal (object_id),
    active_organization_object_id uuid        REFERENCES membership.organization (object_id),
    database_login                text        NOT NULL,
    organization_object_id        uuid        REFERENCES membership.organization (object_id),
    subject_principal_object_id   uuid        REFERENCES membership.principal (object_id),
    detail                        jsonb       NOT NULL DEFAULT '{}' CONSTRAINT audit_event_detail_check CHECK (jsonb_typeof(detail) = 'object')
);

CREATE INDEX ix_audit_event_organization ON membership.audit_event (organization_object_id, occurred_at);

COMMENT ON TABLE membership.audit_event IS
    'Log of membership changes: who really did it (actual), as whom (effective), in which organization, through which database login, about what. NULL principals: the deploy.';
COMMENT ON COLUMN membership.audit_event.database_login IS
    'session_user: the login that connected. A request context cannot change it, so an operator''s SQL is told apart from a gateway request.';

SELECT pg_temp.exempt_from_fk_index(t::regclass, c, 'records who acted or was acted on; principals are never deleted and rows are read by organization')
FROM (VALUES ('membership.organization', '{created_by_object_id}'::name[]),
             ('membership.organization_membership', '{created_by_object_id,ended_by_object_id}'),
             ('membership.role_assignment', '{granted_by_object_id,revoked_by_object_id}'),
             ('membership.invitation', '{invited_by_object_id,revoked_by_object_id}'),
             ('membership.audit_event', '{actual_principal_object_id,effective_principal_object_id,subject_principal_object_id}')) v(t, c);
SELECT pg_temp.exempt_from_fk_index('membership.audit_event', '{active_organization_object_id}',
                                    'the audit trail is read by the organization it belongs to; organizations are never deleted');
SELECT pg_temp.exempt_from_fk_index('membership.invitation', '{accepted_membership_object_id}',
                                    'memberships end, they are never deleted; invitations are read by organization');
SELECT pg_temp.exempt_from_fk_index(t::regclass, '{role_id}', 'roles are reference data the deploy syncs; members are read by organization, not by role')
FROM unnest(ARRAY['membership.role_assignment', 'membership.invitation']) t;
