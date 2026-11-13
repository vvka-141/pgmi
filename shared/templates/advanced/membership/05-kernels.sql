/*
<pgmi-meta
    id="23f8669b-eedc-4c42-a35a-1875d11550c2"
    idempotent="true">
  <description>
    Membership kernels: the write path into the membership tables. Rules that
    span rows live in the structure (a temporal foreign key keeps a role inside
    its membership, an exclusion constraint allows one owner at a time) or in
    these statements: a person and their principal row are inserted together,
    ownership only moves (never to nobody), the lifecycle only moves forward.
    Each kernel returns the affected row, or NULL when a rule refuses; the
    *_problem functions name why, as a problem code. Every change writes
    membership.audit_event. A kernel taking an organization, membership or
    invitation refuses one outside the request's organization, and one acting
    for a person refuses anyone but the request's own principal (a platform
    operator reaches both). Every SECURITY DEFINER routine here is owned by the
    kernel role (pg_temp.kernel_owns); membership/__test__/test_kernel_reach.sql
    calls each one the owner may call with another organization's ids.
  </description>
  <sortKeys>
    <key>005/000/005</key>
  </sortKeys>
</pgmi-meta>
*/

-- A request may act on an organization when it names no one (sign-in, the
-- deploy), works in this one, or comes from a platform operator. A person
-- signed in without an organization session reaches none.
CREATE OR REPLACE FUNCTION membership.fn_is_organization_in_reach(p_organization_object_id uuid)
RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT coalesce((r.organization_object_id IS NULL AND api.current_principal_id() IS NULL)
                    OR r.organization_object_id = p_organization_object_id
                    OR api.is_platform_operator(), false)
    FROM (SELECT api.current_organization_id() AS organization_object_id) r;
$$;

-- A request may act for a person when it names no one (sign-in, the deploy),
-- is that person, or comes from a platform operator.
CREATE OR REPLACE FUNCTION membership.fn_is_principal_in_reach(p_principal_object_id uuid)
RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT coalesce(r.principal_object_id IS NULL
                    OR r.principal_object_id = p_principal_object_id
                    OR api.is_platform_operator(), false)
    FROM (SELECT api.current_principal_id() AS principal_object_id) r;
$$;

CREATE OR REPLACE FUNCTION membership.fn_record_audit_event(
    p_action                      text,
    p_organization_object_id      uuid,
    p_subject_principal_object_id uuid,
    p_detail                      jsonb DEFAULT '{}')
RETURNS membership.audit_event
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    INSERT INTO membership.audit_event
        (action, actual_principal_object_id, effective_principal_object_id, actor_kind, active_organization_object_id,
         database_login, organization_object_id, subject_principal_object_id, detail)
    VALUES (p_action, api.current_actual_principal_id(), api.current_principal_id(), api.current_actor_kind(),
            api.current_organization_id(), session_user, p_organization_object_id, p_subject_principal_object_id, coalesce(p_detail, '{}'))
    RETURNING *;
$$;

-- ============================================================================
-- People
-- ============================================================================

-- The only way a person comes to be: principal and user row in one statement.
-- NULL when the email is malformed, or verified and already someone's verified
-- address. The display name falls back to the address's local part.
CREATE OR REPLACE FUNCTION membership.fn_create_user_principal(
    p_email             text,
    p_display_name      text,
    p_is_email_verified boolean)
RETURNS membership.user_principal
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_email text := membership.fn_normalize_email(p_email);
    v_user  membership.user_principal;
BEGIN
    IF NOT membership.fn_is_well_formed_email(v_email)
       OR (p_is_email_verified
           AND EXISTS (SELECT FROM membership.user_principal WHERE email = v_email AND is_email_verified)) THEN
        RETURN NULL;
    END IF;

    WITH p AS (
        INSERT INTO membership.principal (kind, display_name)
        VALUES ('user', left(coalesce(NULLIF(btrim(p_display_name), ''), split_part(v_email, '@', 1)), 200))
        RETURNING object_id
    )
    INSERT INTO membership.user_principal (principal_object_id, email, verified_email, email_verified_at)
    SELECT p.object_id, v_email,
           CASE WHEN p_is_email_verified THEN v_email END,
           CASE WHEN p_is_email_verified THEN clock_timestamp() END
    FROM p
    RETURNING * INTO v_user;

    PERFORM membership.fn_record_audit_event('membership.principal.created', NULL, v_user.principal_object_id);
    RETURN v_user;
END $$;

-- A machine actor connecting as its own login role, such as the background
-- jobs login, attributed from session_user. Registered outside any request or
-- by a platform operator; NULL otherwise or when the role does not exist.
-- Registering the same id for the same role again returns the existing row.
CREATE OR REPLACE FUNCTION membership.fn_register_service_principal(
    p_id            text,
    p_description   text,
    p_database_role name)
RETURNS membership.service_principal
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_service membership.service_principal;
BEGIN
    IF (api.current_principal_id() IS NOT NULL AND NOT api.is_platform_operator())
       OR to_regrole(quote_ident(p_database_role)) IS NULL THEN
        RETURN NULL;
    END IF;

    SELECT * INTO v_service FROM membership.service_principal
    WHERE id = p_id AND database_role = p_database_role;
    IF FOUND THEN
        RETURN v_service;
    END IF;

    WITH p AS (
        INSERT INTO membership.principal (kind, display_name)
        VALUES ('service', p_id)
        RETURNING object_id
    )
    INSERT INTO membership.service_principal (id, principal_object_id, description, database_role)
    SELECT p_id, p.object_id, p_description, p_database_role FROM p
    RETURNING * INTO v_service;

    PERFORM membership.fn_record_audit_event('membership.principal.created', NULL, v_service.principal_object_id,
        jsonb_build_object('serviceId', v_service.id, 'databaseRole', v_service.database_role));
    RETURN v_service;
END $$;

-- The person behind an identity-provider login, as refreshed, or NULL when
-- they may not be provisioned:
--   * the issuer is unknown, or the subject is not one (whitespace, empty);
--   * a new login's email is an existing person's verified address and the
--     login has not verified it: linking needs both sides to have verified the
--     address, or whoever can present it signs in as them. An address held
--     only unverified is no one's: the login gets a new person of its own;
--   * a new person without a usable email.
-- A known login refreshes the email, its verification and the display name.
-- Creates no organization and no membership. It stays one routine because
-- both the refresh and the first sign-in must run under the advisory locks
-- it takes first.
CREATE OR REPLACE FUNCTION membership.fn_provision_login(
    p_issuer_id         text,
    p_subject           text,
    p_email             text,
    p_display_name      text,
    p_is_email_verified boolean)
RETURNS membership.user_principal
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_principal uuid;
    v_existing  membership.user_principal;
    v_email     text := membership.fn_normalize_email(p_email);
    v_verified  boolean := coalesce(p_is_email_verified, false);
    v_name      text := NULLIF(left(btrim(p_display_name), 200), '');
BEGIN
    IF NOT EXISTS (SELECT FROM membership.identity_issuer i WHERE i.id = p_issuer_id)
       OR NOT coalesce(membership.fn_is_valid_subject(p_subject), false) THEN
        RETURN NULL;
    END IF;

    -- Two first sign-ins for the same login or the same email serialize here:
    -- the second waits, then (READ COMMITTED) finds what the first created.
    -- Each caller takes its own subject first and at most one email, so the
    -- locks cannot form a cycle.
    PERFORM pg_advisory_xact_lock(hashtextextended('membership.login.' || p_issuer_id || '|' || p_subject, 0));
    IF membership.fn_is_well_formed_email(v_email) THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('membership.email.' || v_email, 0));
    END IF;

    SELECT ei.user_principal_object_id INTO v_principal
    FROM membership.external_identity ei
    WHERE ei.issuer_id = p_issuer_id AND ei.subject = p_subject;

    IF v_principal IS NOT NULL THEN
        -- A known login's email follows the provider, verified when this
        -- sign-in verified it. Written only when it changes, and never when
        -- the result would be verified while another person holds the address
        -- verified: the address stays theirs (uq_user_principal_verified_email).
        IF membership.fn_is_well_formed_email(v_email) THEN
            UPDATE membership.user_principal u
            SET email = v_email,
                verified_email = CASE WHEN v_verified THEN v_email ELSE u.verified_email END,
                email_verified_at = CASE WHEN v_verified AND u.verified_email IS DISTINCT FROM v_email
                                         THEN clock_timestamp() ELSE u.email_verified_at END,
                updated_at = clock_timestamp()
            WHERE u.principal_object_id = v_principal
              AND (u.email <> v_email OR (v_verified AND NOT u.is_email_verified))
              AND NOT ((v_verified OR u.verified_email = v_email)
                       AND EXISTS (SELECT FROM membership.user_principal o
                                   WHERE o.email = v_email AND o.is_email_verified
                                     AND o.principal_object_id <> v_principal));
        END IF;
        UPDATE membership.principal p SET display_name = v_name
        WHERE p.object_id = v_principal AND v_name IS NOT NULL AND p.display_name <> v_name;
        RETURN (SELECT u FROM membership.user_principal u WHERE u.principal_object_id = v_principal);
    END IF;

    IF NOT membership.fn_is_well_formed_email(v_email) THEN
        RETURN NULL;
    END IF;

    SELECT * INTO v_existing FROM membership.user_principal u
    WHERE u.email = v_email AND u.is_email_verified FOR UPDATE;
    IF v_existing.principal_object_id IS NOT NULL THEN
        IF NOT v_verified THEN
            RAISE DEBUG 'fn_provision_login: refused to link %|% to an existing person: the login has not verified their email',
                p_issuer_id, p_subject;
            RETURN NULL;
        END IF;
        v_principal := v_existing.principal_object_id;
    ELSE
        v_principal := (membership.fn_create_user_principal(v_email, v_name, v_verified)).principal_object_id;
        IF v_principal IS NULL THEN
            RETURN NULL;
        END IF;
    END IF;

    INSERT INTO membership.external_identity (issuer_id, subject, user_principal_object_id)
    VALUES (p_issuer_id, p_subject, v_principal)
    ON CONFLICT (issuer_id, subject) DO NOTHING;
    IF FOUND THEN
        PERFORM membership.fn_record_audit_event('membership.identity.linked', NULL, v_principal,
                                                 jsonb_build_object('issuer', p_issuer_id));
    ELSE
        SELECT ei.user_principal_object_id INTO v_principal
        FROM membership.external_identity ei
        WHERE ei.issuer_id = p_issuer_id AND ei.subject = p_subject;
    END IF;
    RETURN (SELECT u FROM membership.user_principal u WHERE u.principal_object_id = v_principal);
END $$;

-- Platform lockout: a disabled principal resolves to no one. NULL for an
-- unknown principal or a blank reason.
CREATE OR REPLACE FUNCTION membership.fn_disable_principal(p_principal_object_id uuid, p_reason text)
RETURNS membership.principal
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_row membership.principal;
BEGIN
    IF NOT membership.fn_is_clean_name(btrim(p_reason), 500)
       OR NOT membership.fn_is_principal_in_reach(p_principal_object_id) THEN
        RETURN NULL;
    END IF;
    UPDATE membership.principal
    SET disabled_at = clock_timestamp(), disabled_reason = btrim(p_reason)
    WHERE object_id = p_principal_object_id AND disabled_at IS NULL
    RETURNING * INTO v_row;
    IF v_row.object_id IS NOT NULL THEN
        PERFORM membership.fn_record_audit_event('membership.principal.disabled', NULL, p_principal_object_id);
    END IF;
    RETURN v_row;
END $$;

CREATE OR REPLACE FUNCTION membership.fn_enable_principal(p_principal_object_id uuid)
RETURNS membership.principal
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_row membership.principal;
BEGIN
    IF NOT membership.fn_is_principal_in_reach(p_principal_object_id) THEN
        RETURN NULL;
    END IF;
    UPDATE membership.principal
    SET disabled_at = NULL, disabled_reason = NULL
    WHERE object_id = p_principal_object_id AND disabled_at IS NOT NULL
    RETURNING * INTO v_row;
    IF v_row.object_id IS NOT NULL THEN
        PERFORM membership.fn_record_audit_event('membership.principal.enabled', NULL, p_principal_object_id);
    END IF;
    RETURN v_row;
END $$;

-- ============================================================================
-- Memberships and roles
-- ============================================================================

-- The membership insert behind fn_add_member, without its checks: for a
-- kernel that has just created the organization itself.
CREATE OR REPLACE FUNCTION membership.fn_insert_member(
    p_organization_object_id uuid,
    p_principal_object_id    uuid,
    p_role_id                text)
RETURNS membership.organization_membership
LANGUAGE plpgsql
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_at     timestamptz := clock_timestamp();
    v_by     uuid := api.current_actual_principal_id();
    v_member membership.organization_membership;
BEGIN
    WITH m AS (
        INSERT INTO membership.organization_membership
            (organization_object_id, principal_object_id, valid_during, created_by_object_id)
        VALUES (p_organization_object_id, p_principal_object_id, tstzrange(v_at, NULL, '[)'), v_by)
        ON CONFLICT (organization_object_id, principal_object_id) WHERE upper_inf(valid_during) DO NOTHING
        RETURNING *
    ), r AS (
        INSERT INTO membership.role_assignment
            (organization_object_id, membership_object_id, role_id, valid_during, granted_by_object_id)
        SELECT m.organization_object_id, m.object_id, p_role_id, tstzrange(v_at, NULL, '[)'), v_by
        FROM m WHERE p_role_id IS NOT NULL
    )
    SELECT * INTO v_member FROM m;

    IF v_member.object_id IS NOT NULL THEN
        PERFORM membership.fn_record_audit_event('membership.member.added', p_organization_object_id, p_principal_object_id,
            jsonb_build_object('memberId', v_member.object_id, 'roleId', p_role_id));
    END IF;
    RETURN v_member;
END $$;

-- Opens a membership with an initial role (NULL: none). NULL when the
-- organization is out of reach, the principal is already a current member,
-- the principal is an agent or the role an agent role (an agent acts through
-- its connection, never as a member). The caller checks the organization's
-- status and the principal.
CREATE OR REPLACE FUNCTION membership.fn_add_member(
    p_organization_object_id uuid,
    p_principal_object_id    uuid,
    p_role_id                text)
RETURNS membership.organization_membership
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
BEGIN
    IF NOT membership.fn_is_organization_in_reach(p_organization_object_id)
       OR p_role_id LIKE 'agent.%'
       OR EXISTS (SELECT FROM membership.principal p WHERE p.object_id = p_principal_object_id AND p.kind = 'agent') THEN
        RETURN NULL;
    END IF;
    RETURN membership.fn_insert_member(p_organization_object_id, p_principal_object_id, p_role_id);
END $$;

-- Locks an organization's row until the transaction ends. Every kernel that
-- moves ownership or ends a membership takes it before its checks, so they
-- queue and each check reads what the one before committed.
CREATE OR REPLACE FUNCTION membership.fn_lock_organization(p_organization_object_id uuid)
RETURNS uuid
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT o.object_id FROM membership.organization o
    WHERE o.object_id = p_organization_object_id AND membership.fn_is_organization_in_reach(o.object_id)
    FOR NO KEY UPDATE;
$$;

CREATE OR REPLACE FUNCTION membership.fn_is_owner_membership(p_membership_object_id uuid)
RETURNS boolean
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT EXISTS (SELECT FROM membership.role_assignment ra
                   WHERE ra.membership_object_id = p_membership_object_id AND ra.role_id = 'org.owner'
                     AND upper_inf(ra.valid_during));
$$;

CREATE OR REPLACE FUNCTION membership.fn_end_membership_problem(p_membership_object_id uuid)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE
        WHEN NOT EXISTS (SELECT FROM membership.organization_membership m
                         WHERE m.object_id = p_membership_object_id AND upper_inf(m.valid_during)
                           AND membership.fn_is_organization_in_reach(m.organization_object_id))
            THEN 'membership.member.not_found'
        WHEN membership.fn_is_owner_membership(p_membership_object_id)
            THEN 'membership.member.owner_cannot_leave'
    END;
$$;

-- Ends a membership and its roles at one instant, and revokes the person's
-- connections into it. p_reason: left | removed. The owner's membership
-- never ends: ownership moves first.
CREATE OR REPLACE FUNCTION membership.fn_end_membership(p_membership_object_id uuid, p_reason text)
RETURNS membership.organization_membership
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_at     timestamptz;
    v_by     uuid := api.current_actual_principal_id();
    v_member membership.organization_membership;
BEGIN
    IF p_reason IS NULL OR p_reason NOT IN ('left', 'removed') THEN
        RETURN NULL;
    END IF;
    PERFORM membership.fn_lock_organization(
        (SELECT m.organization_object_id FROM membership.organization_membership m WHERE m.object_id = p_membership_object_id));
    v_at := clock_timestamp();
    IF membership.fn_end_membership_problem(p_membership_object_id) IS NOT NULL THEN
        RETURN NULL;
    END IF;
    WITH r AS (
        UPDATE membership.role_assignment
        SET valid_during = tstzrange(lower(valid_during), v_at, '[)'), revoked_by_object_id = v_by
        WHERE membership_object_id = p_membership_object_id AND upper_inf(valid_during)
    )
    UPDATE membership.organization_membership
    SET valid_during = tstzrange(lower(valid_during), v_at, '[)'), end_reason = p_reason, ended_by_object_id = v_by
    WHERE object_id = p_membership_object_id AND upper_inf(valid_during)
    RETURNING * INTO v_member;

    PERFORM membership.fn_record_audit_event('membership.member.ended', v_member.organization_object_id, v_member.principal_object_id,
        jsonb_build_object('memberId', v_member.object_id, 'reason', p_reason));
    PERFORM membership.fn_revoke_membership_connections(v_member.organization_object_id, v_member.object_id);
    RETURN v_member;
END $$;

-- Why a role cannot be granted to or revoked from a membership, or NULL.
-- org.owner moves only by a transfer; an agent role is no member's, so it is
-- unknown here.
CREATE OR REPLACE FUNCTION membership.fn_role_problem(p_membership_object_id uuid, p_role_id text)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE
        WHEN NOT EXISTS (SELECT FROM membership.organization_membership m
                         WHERE m.object_id = p_membership_object_id AND upper_inf(m.valid_during)
                           AND membership.fn_is_organization_in_reach(m.organization_object_id))
            THEN 'membership.member.not_found'
        WHEN NOT EXISTS (SELECT FROM membership.role r WHERE r.id = p_role_id) OR p_role_id LIKE 'agent.%'
            THEN 'membership.role.unknown'
        WHEN p_role_id = 'org.owner'
            THEN 'membership.role.owner_moves_by_transfer'
    END;
$$;

-- Grants a role to a current membership. granted is true when this call
-- made the assignment, false when the role was already held (assignment is
-- then the current one); both NULL when fn_role_problem refuses. It starts no
-- earlier than the membership, so a clock stepping back cannot put it outside
-- the temporal key.
CREATE OR REPLACE FUNCTION membership.fn_grant_role(
    p_membership_object_id uuid,
    p_role_id              text,
    OUT assignment         membership.role_assignment,
    OUT granted            boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
BEGIN
    IF membership.fn_role_problem(p_membership_object_id, p_role_id) IS NOT NULL THEN
        RETURN;
    END IF;
    INSERT INTO membership.role_assignment
        (organization_object_id, membership_object_id, role_id, valid_during, granted_by_object_id)
    SELECT m.organization_object_id, m.object_id, p_role_id,
           tstzrange(greatest(clock_timestamp(), lower(m.valid_during)), NULL, '[)'),
           api.current_actual_principal_id()
    FROM membership.organization_membership m
    WHERE m.object_id = p_membership_object_id
    ON CONFLICT (membership_object_id, role_id) WHERE upper_inf(valid_during) DO NOTHING
    RETURNING * INTO assignment;

    granted := assignment.object_id IS NOT NULL;
    IF granted THEN
        PERFORM membership.fn_record_audit_event('membership.role.granted', assignment.organization_object_id,
            (SELECT principal_object_id FROM membership.organization_membership WHERE object_id = p_membership_object_id),
            jsonb_build_object('memberId', p_membership_object_id, 'roleId', p_role_id));
    ELSE
        SELECT * INTO assignment FROM membership.role_assignment
        WHERE membership_object_id = p_membership_object_id AND role_id = p_role_id AND upper_inf(valid_during);
    END IF;
END $$;

CREATE OR REPLACE FUNCTION membership.fn_revoke_role(p_membership_object_id uuid, p_role_id text)
RETURNS membership.role_assignment
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_row membership.role_assignment;
BEGIN
    IF membership.fn_role_problem(p_membership_object_id, p_role_id) IS NOT NULL THEN
        RETURN NULL;
    END IF;
    UPDATE membership.role_assignment
    SET valid_during = tstzrange(lower(valid_during), clock_timestamp(), '[)'),
        revoked_by_object_id = api.current_actual_principal_id()
    WHERE membership_object_id = p_membership_object_id AND role_id = p_role_id AND upper_inf(valid_during)
    RETURNING * INTO v_row;

    IF v_row.object_id IS NOT NULL THEN
        PERFORM membership.fn_record_audit_event('membership.role.revoked', v_row.organization_object_id,
            (SELECT principal_object_id FROM membership.organization_membership WHERE object_id = p_membership_object_id),
            jsonb_build_object('memberId', p_membership_object_id, 'roleId', p_role_id));
    END IF;
    RETURN v_row;
END $$;

-- ============================================================================
-- Ownership
-- ============================================================================

CREATE OR REPLACE FUNCTION membership.fn_transfer_ownership_problem(
    p_organization_object_id uuid, p_new_owner_principal_object_id uuid)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE
        WHEN NOT membership.fn_is_organization_in_reach(p_organization_object_id)
            THEN 'membership.organization.not_found'
        WHEN NOT EXISTS (SELECT FROM membership.vw_organization_owner ow
                         WHERE ow.organization_object_id = p_organization_object_id
                           AND ow.principal_object_id = api.current_principal_id())
            THEN 'membership.ownership.not_owner'
        WHEN NOT EXISTS (SELECT FROM membership.organization o
                         WHERE o.object_id = p_organization_object_id AND o.status IN ('active', 'suspended'))
            THEN 'membership.organization.not_active'
        WHEN NOT EXISTS (SELECT FROM membership.organization_membership m
                         JOIN membership.principal p ON p.object_id = m.principal_object_id
                                                    AND p.kind = 'user' AND p.disabled_at IS NULL
                         WHERE m.organization_object_id = p_organization_object_id
                           AND m.principal_object_id = p_new_owner_principal_object_id
                           AND upper_inf(m.valid_during))
            THEN 'membership.ownership.new_owner_not_member'
        WHEN EXISTS (SELECT FROM membership.vw_organization_owner ow
                     WHERE ow.organization_object_id = p_organization_object_id
                       AND ow.principal_object_id = p_new_owner_principal_object_id)
            THEN 'membership.ownership.already_owner'
    END;
$$;

-- Moves ownership: the current owner's org.owner closes at the instant the
-- new owner's opens, so there is never zero or two owners.
-- Optionally the previous owner keeps org.admin.
CREATE OR REPLACE FUNCTION membership.fn_transfer_ownership(
    p_organization_object_id        uuid,
    p_new_owner_principal_object_id uuid,
    p_previous_owner_becomes_admin  boolean)
RETURNS membership.organization
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_at       timestamptz;
    v_by       uuid := api.current_actual_principal_id();
    v_previous uuid;
    v_new      uuid;
BEGIN
    PERFORM membership.fn_lock_organization(p_organization_object_id);
    v_at := clock_timestamp();
    IF p_previous_owner_becomes_admin IS NULL
       OR membership.fn_transfer_ownership_problem(p_organization_object_id, p_new_owner_principal_object_id) IS NOT NULL THEN
        RETURN NULL;
    END IF;
    SELECT m.object_id INTO v_new
    FROM membership.organization_membership m
    WHERE m.organization_object_id = p_organization_object_id
      AND m.principal_object_id = p_new_owner_principal_object_id AND upper_inf(m.valid_during);
    SELECT ow.membership_object_id INTO v_previous
    FROM membership.vw_organization_owner ow WHERE ow.organization_object_id = p_organization_object_id;

    UPDATE membership.role_assignment
    SET valid_during = tstzrange(lower(valid_during), v_at, '[)'), revoked_by_object_id = v_by
    WHERE membership_object_id = v_previous AND role_id = 'org.owner' AND upper_inf(valid_during);
    IF p_previous_owner_becomes_admin THEN
        INSERT INTO membership.role_assignment
            (organization_object_id, membership_object_id, role_id, valid_during, granted_by_object_id)
        VALUES (p_organization_object_id, v_previous, 'org.admin', tstzrange(v_at, NULL, '[)'), v_by)
        ON CONFLICT (membership_object_id, role_id) WHERE upper_inf(valid_during) DO NOTHING;
    END IF;
    INSERT INTO membership.role_assignment
        (organization_object_id, membership_object_id, role_id, valid_during, granted_by_object_id)
    VALUES (p_organization_object_id, v_new, 'org.owner', tstzrange(v_at, NULL, '[)'), v_by);

    PERFORM membership.fn_record_audit_event('membership.ownership.transferred', p_organization_object_id, p_new_owner_principal_object_id,
        jsonb_build_object('fromMemberId', v_previous, 'toMemberId', v_new,
                           'previousOwnerBecomesAdmin', p_previous_owner_becomes_admin));
    RETURN (SELECT o FROM membership.organization o WHERE o.object_id = p_organization_object_id);
END $$;

-- ============================================================================
-- Organizations
-- ============================================================================

CREATE OR REPLACE FUNCTION membership.fn_organization_problem(p_name text, p_slug text)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE
        WHEN NOT coalesce(membership.fn_is_clean_name(btrim(p_name), 200), false) THEN 'membership.organization.name_invalid'
        WHEN membership.fn_slug_problem(p_slug) IS NOT NULL THEN membership.fn_slug_problem(p_slug)
        WHEN EXISTS (SELECT FROM membership.organization WHERE slug = p_slug) THEN 'membership.organization.slug_taken'
    END;
$$;

-- The default path: an organization a platform operator (or the deploy)
-- creates for an owner, pending until that person accepts the owner
-- invitation with the verified address. Nobody is created for the owner.
CREATE OR REPLACE FUNCTION membership.fn_create_organization(p_name text, p_slug text, p_owner_email text)
RETURNS membership.organization
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_org membership.organization;
BEGIN
    IF membership.fn_organization_problem(p_name, p_slug) IS NOT NULL
       OR NOT coalesce(membership.fn_is_well_formed_email(membership.fn_normalize_email(p_owner_email)), false) THEN
        RETURN NULL;
    END IF;
    INSERT INTO membership.organization (name, slug, status, created_by_object_id)
    VALUES (btrim(p_name), p_slug, 'pending', api.current_actual_principal_id())
    ON CONFLICT DO NOTHING
    RETURNING * INTO v_org;
    IF v_org.object_id IS NULL THEN
        RETURN NULL;
    END IF;
    PERFORM membership.fn_record_audit_event('membership.organization.created', v_org.object_id, NULL,
                                             jsonb_build_object('status', 'pending'));
    PERFORM membership.fn_invite(v_org.object_id, p_owner_email, 'org.owner');
    RETURN v_org;
END $$;

-- The self-serve path: an active organization owned by an existing, enabled
-- person from the start.
CREATE OR REPLACE FUNCTION membership.fn_create_organization_with_owner(
    p_name                      text,
    p_slug                      text,
    p_owner_principal_object_id uuid)
RETURNS membership.organization
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_org membership.organization;
BEGIN
    IF membership.fn_organization_problem(p_name, p_slug) IS NOT NULL
       OR NOT membership.fn_is_principal_in_reach(p_owner_principal_object_id)
       OR NOT EXISTS (SELECT FROM membership.principal
                      WHERE object_id = p_owner_principal_object_id AND kind = 'user' AND disabled_at IS NULL) THEN
        RETURN NULL;
    END IF;
    INSERT INTO membership.organization (name, slug, status, created_by_object_id)
    VALUES (btrim(p_name), p_slug, 'active', api.current_actual_principal_id())
    ON CONFLICT DO NOTHING
    RETURNING * INTO v_org;
    IF v_org.object_id IS NULL THEN
        RETURN NULL;
    END IF;
    PERFORM membership.fn_record_audit_event('membership.organization.created', v_org.object_id, p_owner_principal_object_id,
                                             jsonb_build_object('status', 'active'));
    IF (membership.fn_insert_member(v_org.object_id, p_owner_principal_object_id, 'org.owner')).object_id IS NULL THEN
        RAISE EXCEPTION 'fn_create_organization_with_owner: the owner could not join %', v_org.slug;
    END IF;
    RETURN v_org;
END $$;

-- Pending becomes active only by its owner accepting; archived is final.
CREATE OR REPLACE FUNCTION membership.fn_organization_status_problem(p_organization_object_id uuid, p_status text)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE
        WHEN o.object_id IS NULL OR NOT membership.fn_is_organization_in_reach(o.object_id)
            THEN 'membership.organization.not_found'
        WHEN p_status = o.status THEN NULL
        WHEN p_status IS NULL OR p_status NOT IN ('active', 'suspended', 'archived') OR o.status = 'archived'
             OR (o.status = 'pending' AND p_status <> 'archived')
            THEN 'membership.organization.status_transition_refused'
    END
    FROM (SELECT p_organization_object_id AS id) asked
    LEFT JOIN membership.organization o ON o.object_id = asked.id;
$$;

-- Replaces an organization's name and status. Archiving ends every membership
-- and role, revokes every connection and withdraws pending invitations.
CREATE OR REPLACE FUNCTION membership.fn_update_organization(
    p_organization_object_id uuid,
    p_name                   text,
    p_status                 text)
RETURNS membership.organization
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_at  timestamptz;
    v_by  uuid := api.current_actual_principal_id();
    v_old membership.organization;
    v_org membership.organization;
BEGIN
    SELECT * INTO v_old FROM membership.organization WHERE object_id = p_organization_object_id FOR NO KEY UPDATE;
    v_at := clock_timestamp();
    IF membership.fn_organization_status_problem(p_organization_object_id, p_status) IS NOT NULL
       OR NOT coalesce(membership.fn_is_clean_name(btrim(p_name), 200), false) THEN
        RETURN NULL;
    END IF;
    UPDATE membership.organization
    SET name = btrim(p_name),
        status = p_status,
        status_changed_at = CASE WHEN p_status <> status THEN v_at ELSE status_changed_at END
    WHERE object_id = p_organization_object_id
    RETURNING * INTO v_org;

    IF v_org.status = 'archived' AND v_old.status <> 'archived' THEN
        WITH r AS (
            UPDATE membership.role_assignment
            SET valid_during = tstzrange(lower(valid_during), v_at, '[)'), revoked_by_object_id = v_by
            WHERE organization_object_id = p_organization_object_id AND upper_inf(valid_during)
        )
        UPDATE membership.organization_membership
        SET valid_during = tstzrange(lower(valid_during), v_at, '[)'),
            end_reason = 'organization_archived', ended_by_object_id = v_by
        WHERE organization_object_id = p_organization_object_id AND upper_inf(valid_during);
        UPDATE membership.invitation
        SET revoked_at = v_at, revoked_by_object_id = v_by
        WHERE organization_object_id = p_organization_object_id AND accepted_at IS NULL AND revoked_at IS NULL;
        PERFORM membership.fn_revoke_membership_connections(p_organization_object_id, NULL);
    END IF;

    PERFORM membership.fn_record_audit_event('membership.organization.updated', p_organization_object_id, NULL,
        jsonb_build_object('fromStatus', v_old.status, 'toStatus', v_org.status,
                           'renamed', v_org.name IS DISTINCT FROM v_old.name));
    RETURN v_org;
END $$;

-- ============================================================================
-- Invitations
-- ============================================================================

CREATE OR REPLACE FUNCTION membership.fn_invitation_lifetime()
RETURNS interval LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$ SELECT interval '14 days' $$;

-- Why an invitation cannot be made, or NULL. Ownership is offered only by a
-- pending organization (one without an owner yet); any other role only by an
-- active one.
CREATE OR REPLACE FUNCTION membership.fn_invitation_problem(
    p_organization_object_id uuid, p_email text, p_role_id text)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE
        WHEN o.object_id IS NULL OR NOT membership.fn_is_organization_in_reach(o.object_id)
            THEN 'membership.organization.not_found'
        WHEN NOT coalesce(membership.fn_is_well_formed_email(membership.fn_normalize_email(p_email)), false)
            THEN 'membership.invitation.email_invalid'
        WHEN NOT EXISTS (SELECT FROM membership.role r WHERE r.id = p_role_id) OR p_role_id LIKE 'agent.%'
            THEN 'membership.role.unknown'
        WHEN (o.status = 'pending') <> (p_role_id = 'org.owner') OR o.status NOT IN ('pending', 'active')
            THEN 'membership.invitation.role_not_offered'
        WHEN EXISTS (SELECT FROM membership.vw_member m
                     WHERE m.organization_object_id = o.object_id
                       AND m.email = membership.fn_normalize_email(p_email)
                       AND m.is_email_verified)
            THEN 'membership.invitation.already_member'
    END
    FROM (SELECT p_organization_object_id AS id) asked
    LEFT JOIN membership.organization o ON o.object_id = asked.id;
$$;

-- Invites an address into an organization with an intended role. A pending
-- invitation to the same address is refreshed instead of duplicated, with the
-- newest role; created says which happened. Dropped first: it once returned
-- membership.invitation, and CREATE OR REPLACE cannot change a result type
-- on a database deployed before.
DROP FUNCTION IF EXISTS membership.fn_invite(uuid, text, text);

CREATE FUNCTION membership.fn_invite(
    p_organization_object_id uuid,
    p_email                  text,
    p_role_id                text,
    OUT invitation           membership.invitation,
    OUT created              boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_id uuid;
BEGIN
    IF membership.fn_invitation_problem(p_organization_object_id, p_email, p_role_id) IS NOT NULL THEN
        RETURN;
    END IF;
    INSERT INTO membership.invitation AS i
        (organization_object_id, email, role_id, invited_by_object_id, expires_at)
    VALUES (p_organization_object_id, membership.fn_normalize_email(p_email), p_role_id,
            api.current_actual_principal_id(), clock_timestamp() + membership.fn_invitation_lifetime())
    ON CONFLICT (organization_object_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL DO UPDATE SET
        role_id              = EXCLUDED.role_id,
        invited_by_object_id = EXCLUDED.invited_by_object_id,
        created_at           = EXCLUDED.created_at,
        expires_at           = EXCLUDED.expires_at
    RETURNING i.object_id, old.object_id IS NULL INTO v_id, created;
    SELECT * INTO invitation FROM membership.invitation WHERE object_id = v_id;

    PERFORM membership.fn_record_audit_event('membership.invitation.created', p_organization_object_id, NULL,
        jsonb_build_object('invitationId', v_id, 'roleId', p_role_id));
END $$;

-- Accepts a pending invitation for a person whose VERIFIED email is its
-- address: opens the membership with the intended role (or grants it to a
-- current one), activates a pending organization whose owner this is. NULL
-- when the invitation is not pending, expired, out of reach, the person is
-- disabled, or their verified email is not the invited address.
CREATE OR REPLACE FUNCTION membership.fn_accept_invitation(p_invitation_object_id uuid, p_principal_object_id uuid)
RETURNS membership.invitation
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_inv    membership.invitation;
    v_member uuid;
BEGIN
    SELECT i.* INTO v_inv
    FROM membership.invitation i
    JOIN membership.principal p ON p.object_id = p_principal_object_id AND p.disabled_at IS NULL
    JOIN membership.user_principal u ON u.principal_object_id = p_principal_object_id
                                    AND u.email = i.email AND u.is_email_verified
    JOIN membership.organization o ON o.object_id = i.organization_object_id
                                  AND (o.status = 'pending') = (i.role_id = 'org.owner')
                                  AND o.status IN ('pending', 'active')
    WHERE i.object_id = p_invitation_object_id
      AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > clock_timestamp()
      AND membership.fn_is_organization_in_reach(i.organization_object_id)
      AND membership.fn_is_principal_in_reach(p_principal_object_id)
    FOR NO KEY UPDATE OF i;
    IF v_inv.object_id IS NULL THEN
        RETURN NULL;
    END IF;

    SELECT m.object_id INTO v_member
    FROM membership.organization_membership m
    WHERE m.organization_object_id = v_inv.organization_object_id
      AND m.principal_object_id = p_principal_object_id AND upper_inf(m.valid_during);
    IF v_member IS NULL THEN
        v_member := (membership.fn_add_member(v_inv.organization_object_id, p_principal_object_id, v_inv.role_id)).object_id;
    ELSE
        PERFORM membership.fn_grant_role(v_member, v_inv.role_id);
    END IF;

    UPDATE membership.organization SET status = 'active', status_changed_at = clock_timestamp()
    WHERE object_id = v_inv.organization_object_id AND status = 'pending';
    IF FOUND THEN
        UPDATE membership.invitation
        SET revoked_at = clock_timestamp(), revoked_by_object_id = invited_by_object_id
        WHERE organization_object_id = v_inv.organization_object_id AND role_id = 'org.owner'
          AND object_id <> v_inv.object_id AND accepted_at IS NULL AND revoked_at IS NULL;
    END IF;

    UPDATE membership.invitation
    SET accepted_at = clock_timestamp(), accepted_membership_object_id = v_member
    WHERE object_id = v_inv.object_id
    RETURNING * INTO v_inv;

    PERFORM membership.fn_record_audit_event('membership.invitation.accepted', v_inv.organization_object_id, p_principal_object_id,
        jsonb_build_object('invitationId', v_inv.object_id, 'memberId', v_member, 'roleId', v_inv.role_id));
    RETURN v_inv;
END $$;

-- Accepts every pending invitation addressed to the person's verified email.
CREATE OR REPLACE FUNCTION membership.fn_accept_pending_invitations(p_principal_object_id uuid)
RETURNS SETOF membership.invitation
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT a.*
    FROM membership.invitation i
    JOIN membership.user_principal u ON u.principal_object_id = p_principal_object_id
                                    AND u.email = i.email AND u.is_email_verified
    CROSS JOIN LATERAL membership.fn_accept_invitation(i.object_id, p_principal_object_id) a
    WHERE i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > clock_timestamp()
      AND a.object_id IS NOT NULL;
$$;

CREATE OR REPLACE FUNCTION membership.fn_revoke_invitation(p_invitation_object_id uuid)
RETURNS membership.invitation
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_inv membership.invitation;
BEGIN
    UPDATE membership.invitation
    SET revoked_at = clock_timestamp(),
        revoked_by_object_id = coalesce(api.current_actual_principal_id(), invited_by_object_id)
    WHERE object_id = p_invitation_object_id AND accepted_at IS NULL AND revoked_at IS NULL
      AND membership.fn_is_organization_in_reach(organization_object_id)
    RETURNING * INTO v_inv;
    IF v_inv.object_id IS NOT NULL THEN
        PERFORM membership.fn_record_audit_event('membership.invitation.revoked', v_inv.organization_object_id, NULL,
            jsonb_build_object('invitationId', v_inv.object_id));
    END IF;
    RETURN v_inv;
END $$;

SELECT pg_temp.kernel_owns(r::regprocedure, p_owner_executes => false)
FROM unnest(ARRAY[
    'membership.fn_record_audit_event(text, uuid, uuid, jsonb)',
    'membership.fn_create_user_principal(text, text, boolean)',
    'membership.fn_provision_login(text, text, text, text, boolean)',
    'membership.fn_lock_organization(uuid)',
    'membership.fn_is_owner_membership(uuid)',
    'membership.fn_insert_member(uuid, uuid, text)']) r;

SELECT pg_temp.kernel_owns(r::regprocedure)
FROM unnest(ARRAY[
    'membership.fn_register_service_principal(text, text, name)',
    'membership.fn_disable_principal(uuid, text)',
    'membership.fn_enable_principal(uuid)',
    'membership.fn_add_member(uuid, uuid, text)',
    'membership.fn_end_membership_problem(uuid)',
    'membership.fn_end_membership(uuid, text)',
    'membership.fn_role_problem(uuid, text)',
    'membership.fn_grant_role(uuid, text)',
    'membership.fn_revoke_role(uuid, text)',
    'membership.fn_transfer_ownership_problem(uuid, uuid)',
    'membership.fn_transfer_ownership(uuid, uuid, boolean)',
    'membership.fn_organization_problem(text, text)',
    'membership.fn_create_organization(text, text, text)',
    'membership.fn_create_organization_with_owner(text, text, uuid)',
    'membership.fn_organization_status_problem(uuid, text)',
    'membership.fn_update_organization(uuid, text, text)',
    'membership.fn_invitation_problem(uuid, text, text)',
    'membership.fn_invite(uuid, text, text)',
    'membership.fn_accept_invitation(uuid, uuid)',
    'membership.fn_accept_pending_invitations(uuid)',
    'membership.fn_revoke_invitation(uuid)']) r;
