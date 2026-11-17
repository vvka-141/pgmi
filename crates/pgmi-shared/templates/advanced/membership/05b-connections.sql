/*
<pgmi-meta
    id="d66d13f4-04bd-452a-b11e-d41958d52062"
    idempotent="true">
  <description>
    Connection kernels: how an MCP caller comes to be a person's agent. A
    person approves a connection while signed in on the web (a login
    session), naming the organization it acts in and its agent role; the
    role must not grant anything the person lacks there. The client presents
    the returned handle on every MCP request, and the framework's hook
    internal.resolve_connection_context, replaced here, resolves it into that
    person (the effective principal), the connection's agent (the actual
    principal), that organization and the agent's permissions: its role's
    intersected with the person's, recomputed per request. An OAuth client
    needs no handle: the gateway verifies its access token and the hook
    internal.resolve_client_context, replaced here, resolves the token's
    issuer, subject and client id the same way, or answers why it cannot
    act yet; the first call of an unknown client makes a pending connection
    its person approves on the web with a pairing code. Nothing on the MCP
    path provisions anyone: an unknown, revoked, expired or paused handle, a
    disabled person, an ended membership or an organization whose agents are
    switched off gives the request no identity. Only a person acting as
    themselves approves, pauses, resumes or revokes; an agent never changes
    an agent. The person manages their own connections, a holder of
    members.manage those of the request's organization, and the owner
    switches all of the organization's agents off and on. Every lifecycle
    event is audited with the agent as its subject. A connection never
    carries a second factor, so it never holds a platform permission. The
    kernel role owns every SECURITY DEFINER routine here.
  </description>
  <sortKeys>
    <key>005/000/005/002</key>
  </sortKeys>
</pgmi-meta>
*/

-- ============================================================================
-- APPROVAL - a person signed in on the web approves a connection with an agent role
-- ============================================================================

-- Why a person cannot give an agent this role in this organization, or NULL:
-- membership.connection.role_unknown (not an agent role) | membership.connection.role_too_broad (it
-- grants a permission the person does not hold there).
CREATE OR REPLACE FUNCTION membership.fn_agent_role_problem(
    p_principal_object_id    uuid,
    p_organization_object_id uuid,
    p_role_id                text)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE
        WHEN p_role_id IS NULL OR p_role_id NOT LIKE 'agent.%'
             OR NOT EXISTS (SELECT FROM membership.role r WHERE r.id = p_role_id)
            THEN 'membership.connection.role_unknown'
        WHEN EXISTS (SELECT rp.permission_id FROM membership.role_permission rp WHERE rp.role_id = p_role_id
                     EXCEPT
                     SELECT unnest(membership.fn_effective_permissions(p_principal_object_id, p_organization_object_id, false)))
            THEN 'membership.connection.role_too_broad'
    END;
$$;

CREATE OR REPLACE FUNCTION membership.fn_record_connection_event(p_action text, p_connection membership.connection)
RETURNS membership.audit_event
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT membership.fn_record_audit_event(p_action, p_connection.organization_object_id,
        p_connection.agent_principal_object_id,
        jsonb_build_object('connectionId', p_connection.object_id, 'principalId', p_connection.principal_object_id,
                           'roleId', p_connection.role_id));
$$;

-- The person's current membership of an active organization, or NULL.
CREATE OR REPLACE FUNCTION membership.fn_find_membership(p_principal_object_id uuid, p_organization_object_id uuid)
RETURNS uuid
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT m.object_id
    FROM membership.organization_membership m
    JOIN membership.organization o ON o.object_id = m.organization_object_id AND o.status = 'active'
    WHERE m.principal_object_id = p_principal_object_id AND m.organization_object_id = p_organization_object_id
      AND upper_inf(m.valid_during);
$$;

-- Why the request cannot approve a connection with these choices, or NULL:
-- membership.connection.login_session_required (only a person signed in on the web,
-- acting as themselves, approves; a request through a connection has no
-- login session, so an agent never approves) | membership.connection.client_name_invalid |
-- membership.connection.validity_invalid | membership.connection.not_member | membership.connection.role_unknown |
-- membership.connection.paused_by_manager (a manager paused one of the person's agents
-- there) | membership.connection.role_too_broad | membership.connection.agents_disabled.
CREATE OR REPLACE FUNCTION membership.fn_connection_approval_problem(
    p_organization_object_id uuid,
    p_role_id                text,
    p_client_name            text,
    p_valid_days             int)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE
        WHEN api.current_login_session_id() IS NULL OR r.person IS NULL OR api.current_actor_kind() IS DISTINCT FROM 'human'
             OR api.current_actual_principal_id() IS DISTINCT FROM r.person
            THEN 'membership.connection.login_session_required'
        WHEN NOT coalesce(membership.fn_is_clean_name(p_client_name, 100), false)
            THEN 'membership.connection.client_name_invalid'
        WHEN p_valid_days IS NULL OR p_valid_days NOT BETWEEN 1 AND 365
            THEN 'membership.connection.validity_invalid'
        WHEN membership.fn_find_membership(r.person, p_organization_object_id) IS NULL
            THEN 'membership.connection.not_member'
        WHEN EXISTS (SELECT FROM membership.connection c
                     WHERE c.principal_object_id = r.person AND c.organization_object_id = p_organization_object_id
                       AND c.revoked_at IS NULL AND c.paused_at IS NOT NULL
                       AND c.paused_by_object_id IS DISTINCT FROM r.person)
            THEN 'membership.connection.paused_by_manager'
        ELSE coalesce(membership.fn_agent_role_problem(r.person, p_organization_object_id, p_role_id),
                      CASE WHEN EXISTS (SELECT FROM membership.organization o
                                        WHERE o.object_id = p_organization_object_id AND o.agents_disabled_at IS NOT NULL)
                           THEN 'membership.connection.agents_disabled' END)
    END
    FROM (SELECT api.current_principal_id() AS person) r;
$$;

-- Starts a connection with an agent role fixed to a current membership of an
-- enabled person in an active organization; no row otherwise, or when the
-- role is not one the person may give (membership.fn_agent_role_problem).
-- Without p_client_id it returns the handle the client presents; with
-- p_issuer_id and p_client_id it belongs to that OAuth client and has none.
CREATE OR REPLACE FUNCTION membership.fn_start_connection(
    p_membership_object_id uuid,
    p_role_id              text,
    p_client_name          text,
    p_valid_days           int,
    p_issuer_id            text DEFAULT NULL,
    p_client_id            text DEFAULT NULL)
RETURNS TABLE (connection_object_id uuid, token text, expires_at timestamptz)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_token text := CASE WHEN p_client_id IS NULL THEN membership.fn_new_session_token('cn') END;
    v_row   membership.connection;
BEGIN
    WITH m AS MATERIALIZED (
        SELECT m.principal_object_id, m.organization_object_id, m.object_id, uuidv7() AS agent_id
        FROM membership.organization_membership m
        JOIN membership.organization o ON o.object_id = m.organization_object_id AND o.status = 'active'
        JOIN membership.user_principal u ON u.principal_object_id = m.principal_object_id
        JOIN membership.principal p ON p.object_id = m.principal_object_id AND p.disabled_at IS NULL
        WHERE m.object_id = p_membership_object_id AND upper_inf(m.valid_during)
          AND membership.fn_agent_role_problem(m.principal_object_id, m.organization_object_id, p_role_id) IS NULL
    ), p AS (
        INSERT INTO membership.principal (object_id, kind, display_name)
        SELECT agent_id, 'agent', p_client_name FROM m
    ), a AS (
        INSERT INTO membership.agent_principal (principal_object_id)
        SELECT agent_id FROM m
    )
    INSERT INTO membership.connection AS c
        (token_hash, issuer_id, client_id, principal_object_id, organization_object_id, membership_object_id,
         agent_principal_object_id, role_id, client_name, expires_at)
    SELECT membership.fn_session_token_hash(v_token), p_issuer_id, p_client_id, m.principal_object_id,
           m.organization_object_id, m.object_id, m.agent_id, p_role_id, p_client_name,
           clock_timestamp() + make_interval(days => p_valid_days)
    FROM m
    RETURNING c.* INTO v_row;
    IF v_row.object_id IS NULL THEN
        RETURN;
    END IF;
    PERFORM membership.fn_record_connection_event('membership.connection.approved', v_row);
    RETURN QUERY SELECT v_row.object_id, v_token, v_row.expires_at;
END $$;

-- Approves a connection for the request's own person in an organization they
-- are a current member of, with an agent role no broader than what they hold
-- there; the answer carries the handle once.
-- problem: membership.fn_connection_approval_problem | membership.connection.refused.
CREATE OR REPLACE FUNCTION membership.fn_approve_connection(
    p_organization_object_id uuid,
    p_role_id                text,
    p_client_name            text,
    p_valid_days             int,
    OUT connection_object_id uuid,
    OUT token                text,
    OUT expires_at           timestamptz,
    OUT problem              text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_started record;
BEGIN
    problem := membership.fn_connection_approval_problem(p_organization_object_id, p_role_id, p_client_name, p_valid_days);
    IF problem IS NOT NULL THEN
        RETURN;
    END IF;
    SELECT s.* INTO v_started
    FROM membership.fn_start_connection(
        membership.fn_find_membership(api.current_principal_id(), p_organization_object_id),
        p_role_id, p_client_name, p_valid_days) s;
    IF v_started.token IS NULL THEN
        problem := 'membership.connection.refused';
        RETURN;
    END IF;
    connection_object_id := v_started.connection_object_id;
    token := v_started.token;
    expires_at := v_started.expires_at;
END $$;

-- ============================================================================
-- PAIRING - an OAuth client waits for its person to approve the code it shows
-- ============================================================================

-- A new pairing code: 6 characters from the 30 that cannot be misread (no 0,
-- O, 1, I, L, U), each from a cryptographically random byte below 240, so
-- every character is equally likely.
CREATE OR REPLACE FUNCTION membership.fn_new_pairing_code()
RETURNS text
LANGUAGE plpgsql VOLATILE
SET search_path = membership, extensions, common, pg_temp
AS $$
DECLARE
    v_bytes bytea := extensions.gen_random_bytes(32);
    v_code  text;
BEGIN
    SELECT string_agg(substr('23456789ABCDEFGHJKMNPQRSTVWXYZ', r.b % 30 + 1, 1), '' ORDER BY r.i)
    INTO v_code
    FROM (SELECT get_byte(v_bytes, i) AS b, i FROM generate_series(0, 31) i
          WHERE get_byte(v_bytes, i) < 240 ORDER BY i LIMIT 6) r;
    IF length(v_code) IS DISTINCT FROM 6 THEN
        RAISE EXCEPTION 'pairing code: too few usable random bytes';
    END IF;
    RETURN v_code;
END $$;

-- Approves the pending connection whose pairing code the request's own
-- person read from their MCP client. The pending row is locked first, so two
-- approvals of one code make one connection; a code is only ever its own
-- person's. The client's previous connection, whatever its state, is revoked
-- first: a person has one per client.
-- problem: membership.connection.login_session_required | membership.connection.pairing_code_unknown
-- (no live code of this person) | the rest of membership.fn_connection_approval_problem |
-- membership.connection.refused.
CREATE OR REPLACE FUNCTION membership.fn_approve_connection_by_code(
    p_pairing_code           text,
    p_organization_object_id uuid,
    p_role_id                text,
    p_valid_days             int,
    OUT connection_object_id uuid,
    OUT client_id            text,
    OUT client_name          text,
    OUT expires_at           timestamptz,
    OUT problem              text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_pending membership.pending_connection;
    v_name    text;
    v_started record;
    v_revoked int;
BEGIN
    SELECT p.* INTO v_pending
    FROM membership.pending_connection p
    WHERE p.principal_object_id = api.current_principal_id()
      AND p.pairing_code = upper(btrim(p_pairing_code))
      AND p.expires_at > clock_timestamp()
    FOR UPDATE;
    v_name := btrim(left(v_pending.client_id, 100));
    problem := membership.fn_connection_approval_problem(p_organization_object_id, p_role_id, coalesce(v_name, 'unknown'), p_valid_days);
    -- A missing login session stays first: the request is not a person who
    -- could hold a code. Past that, no live code outranks every other
    -- problem: there is no client to approve, and the checks ran on a
    -- placeholder name.
    IF problem IS DISTINCT FROM 'membership.connection.login_session_required' AND v_pending.object_id IS NULL THEN
        problem := 'membership.connection.pairing_code_unknown';
    END IF;
    IF problem IS NOT NULL THEN
        RETURN;
    END IF;

    WITH r AS (
        UPDATE membership.connection c
        SET revoked_at = clock_timestamp(), revoked_by_object_id = api.current_actual_principal_id()
        WHERE c.principal_object_id = v_pending.principal_object_id AND c.issuer_id = v_pending.issuer_id
          AND c.client_id = v_pending.client_id AND c.revoked_at IS NULL
        RETURNING c
    )
    -- count() makes every revoked row record its event; the number is unused.
    SELECT count((membership.fn_record_connection_event('membership.connection.revoked', r.c)).object_id)::int
    INTO v_revoked
    FROM r;

    SELECT s.* INTO v_started
    FROM membership.fn_start_connection(
        membership.fn_find_membership(v_pending.principal_object_id, p_organization_object_id),
        p_role_id, v_name, p_valid_days, v_pending.issuer_id, v_pending.client_id) s;
    IF v_started.connection_object_id IS NULL THEN
        problem := 'membership.connection.refused';
        RETURN;
    END IF;
    DELETE FROM membership.pending_connection p WHERE p.object_id = v_pending.object_id;
    connection_object_id := v_started.connection_object_id;
    client_id := v_pending.client_id;
    client_name := v_name;
    expires_at := v_started.expires_at;
END $$;

-- ============================================================================
-- PAUSE, RESUME, REVOKE - who may change a connection, and the changes
-- ============================================================================

DROP FUNCTION IF EXISTS membership.fn_is_connection_managed(uuid);

-- Whether the request works in this organization and holds members.manage
-- there, which lets it manage the organization's agents.
CREATE OR REPLACE FUNCTION membership.fn_request_manages_members(p_organization_object_id uuid)
RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT coalesce(api.current_organization_id() = p_organization_object_id AND api.has_permission('members.manage'), false);
$$;

-- A request may change a connection when it names no one (the deploy), or is
-- a person acting as themselves who approved it, holds members.manage in its
-- organization, or is a platform operator.
CREATE OR REPLACE FUNCTION membership.fn_is_connection_in_reach(p_principal_object_id uuid, p_organization_object_id uuid)
RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT coalesce(r.principal_object_id IS NULL
                    OR (api.current_actor_kind() = 'human'
                        AND (r.principal_object_id = p_principal_object_id
                             OR membership.fn_request_manages_members(p_organization_object_id)
                             OR api.is_platform_operator())), false)
    FROM (SELECT api.current_principal_id() AS principal_object_id) r;
$$;

-- Why a connection cannot be paused, resumed or revoked by this request, or
-- NULL: membership.connection.not_found (none, or out of reach) |
-- membership.connection.revoked (pause or resume of a revoked one; a repeated
-- revoke is no problem) |
-- membership.connection.paused (pause) | membership.connection.not_paused (resume) |
-- membership.connection.paused_by_manager (resume or revoke by its person of a pause a manager made).
CREATE OR REPLACE FUNCTION membership.fn_connection_problem(p_connection_object_id uuid, p_operation text)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE
        WHEN c.object_id IS NULL
             OR NOT membership.fn_is_connection_in_reach(c.principal_object_id, c.organization_object_id)
            THEN 'membership.connection.not_found'
        WHEN c.revoked_at IS NOT NULL AND p_operation IN ('pause', 'resume')
            THEN 'membership.connection.revoked'
        WHEN c.revoked_at IS NOT NULL
            THEN NULL
        WHEN p_operation = 'pause' AND c.paused_at IS NOT NULL
            THEN 'membership.connection.paused'
        WHEN p_operation = 'resume' AND c.paused_at IS NULL
            THEN 'membership.connection.not_paused'
        WHEN p_operation IN ('resume', 'revoke') AND c.paused_at IS NOT NULL
             AND c.paused_by_object_id IS DISTINCT FROM api.current_principal_id()
             AND api.current_principal_id() IS NOT NULL
             AND NOT membership.fn_request_manages_members(c.organization_object_id) AND NOT api.is_platform_operator()
            THEN 'membership.connection.paused_by_manager'
    END
    FROM (SELECT p_connection_object_id AS id) asked
    LEFT JOIN membership.connection c ON c.object_id = asked.id;
$$;

CREATE OR REPLACE FUNCTION membership.fn_pause_connection(p_connection_object_id uuid)
RETURNS membership.connection
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_row membership.connection;
BEGIN
    IF membership.fn_connection_problem(p_connection_object_id, 'pause') IS NOT NULL THEN
        RETURN NULL;
    END IF;
    UPDATE membership.connection c
    SET paused_at = clock_timestamp(), paused_by_object_id = coalesce(api.current_principal_id(), c.principal_object_id)
    WHERE c.object_id = p_connection_object_id AND c.paused_at IS NULL AND c.revoked_at IS NULL
    RETURNING c.* INTO v_row;
    IF v_row.object_id IS NOT NULL THEN
        PERFORM membership.fn_record_connection_event('membership.connection.paused', v_row);
    END IF;
    RETURN v_row;
END $$;

CREATE OR REPLACE FUNCTION membership.fn_resume_connection(p_connection_object_id uuid)
RETURNS membership.connection
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_row membership.connection;
BEGIN
    IF membership.fn_connection_problem(p_connection_object_id, 'resume') IS NOT NULL THEN
        RETURN NULL;
    END IF;
    UPDATE membership.connection c
    SET paused_at = NULL, paused_by_object_id = NULL
    WHERE c.object_id = p_connection_object_id AND c.paused_at IS NOT NULL AND c.revoked_at IS NULL
    RETURNING c.* INTO v_row;
    IF v_row.object_id IS NOT NULL THEN
        PERFORM membership.fn_record_connection_event('membership.connection.resumed', v_row);
    END IF;
    RETURN v_row;
END $$;

CREATE OR REPLACE FUNCTION membership.fn_revoke_connection(p_connection_object_id uuid)
RETURNS membership.connection
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_row membership.connection;
BEGIN
    IF membership.fn_connection_problem(p_connection_object_id, 'revoke') IS NOT NULL THEN
        RETURN NULL;
    END IF;
    UPDATE membership.connection c
    SET revoked_at = clock_timestamp(), revoked_by_object_id = api.current_actual_principal_id()
    WHERE c.object_id = p_connection_object_id AND c.revoked_at IS NULL
    RETURNING c.* INTO v_row;
    IF v_row.object_id IS NOT NULL THEN
        PERFORM membership.fn_record_connection_event('membership.connection.revoked', v_row);
        RETURN v_row;
    END IF;
    -- Revoked already: a repeated revoke answers the row as it is.
    SELECT c.* INTO v_row FROM membership.connection c
    WHERE c.object_id = p_connection_object_id AND c.revoked_at IS NOT NULL;
    RETURN v_row;
END $$;

-- Revokes the live connections of a membership that ended, or of every
-- membership of an organization (p_membership_object_id NULL). Called by the
-- kernels that end memberships; returns how many.
CREATE OR REPLACE FUNCTION membership.fn_revoke_membership_connections(
    p_organization_object_id uuid,
    p_membership_object_id   uuid)
RETURNS int
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    WITH r AS (
        UPDATE membership.connection c
        SET revoked_at = clock_timestamp(), revoked_by_object_id = api.current_actual_principal_id()
        WHERE c.organization_object_id = p_organization_object_id
          AND (p_membership_object_id IS NULL OR c.membership_object_id = p_membership_object_id)
          AND c.revoked_at IS NULL
        RETURNING c
    )
    -- count() makes every revoked row record its event.
    SELECT count((membership.fn_record_connection_event('membership.connection.revoked', r.c)).object_id)::int
    FROM r;
$$;

-- ============================================================================
-- AGENT SWITCH - the owner turns every agent of an organization off and on
-- ============================================================================

-- Why this request cannot switch an organization's agents off or on, or NULL:
-- membership.organization.not_found | membership.organization.owner_required.
CREATE OR REPLACE FUNCTION membership.fn_organization_agents_problem(p_organization_object_id uuid)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE
        WHEN NOT EXISTS (SELECT FROM membership.organization o
                         WHERE o.object_id = p_organization_object_id
                           AND membership.fn_is_organization_in_reach(o.object_id))
            THEN 'membership.organization.not_found'
        WHEN api.current_principal_id() IS NOT NULL
             AND (api.current_actor_kind() IS DISTINCT FROM 'human'
                  OR NOT EXISTS (SELECT FROM membership.vw_organization_owner ow
                                 WHERE ow.organization_object_id = p_organization_object_id
                                   AND ow.principal_object_id = api.current_principal_id()))
            THEN 'membership.organization.owner_required'
    END;
$$;

-- The owner's switch: false turns every agent of the organization off (no
-- connection into it acts), true turns them back on. Switching to the state
-- it is in changes nothing and records nothing.
CREATE OR REPLACE FUNCTION membership.fn_set_organization_agents(p_organization_object_id uuid, p_enabled boolean)
RETURNS membership.organization
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_org membership.organization;
BEGIN
    IF p_enabled IS NULL OR membership.fn_organization_agents_problem(p_organization_object_id) IS NOT NULL THEN
        RETURN NULL;
    END IF;
    UPDATE membership.organization o
    SET agents_disabled_at = CASE WHEN p_enabled THEN NULL ELSE clock_timestamp() END,
        agents_disabled_by_object_id = CASE WHEN p_enabled THEN NULL ELSE api.current_actual_principal_id() END
    WHERE o.object_id = p_organization_object_id AND (o.agents_disabled_at IS NULL) IS DISTINCT FROM p_enabled
    RETURNING o.* INTO v_org;
    IF v_org.object_id IS NULL THEN
        RETURN (SELECT o FROM membership.organization o WHERE o.object_id = p_organization_object_id);
    END IF;
    PERFORM membership.fn_record_audit_event(
        CASE WHEN p_enabled THEN 'membership.organization.agents_enabled' ELSE 'membership.organization.agents_disabled' END,
        p_organization_object_id, NULL);
    RETURN v_org;
END $$;

-- ============================================================================
-- RESOLUTION - a handle or an OAuth client becomes a live connection, or why not
-- ============================================================================

-- The live connection of an agent: not revoked, paused or expired, its
-- membership current, its organization active with agents switched on, its
-- person and its agent enabled. Asked on every request the agent makes, a
-- replay included.
CREATE OR REPLACE FUNCTION membership.fn_live_connection(p_agent_principal_object_id uuid)
RETURNS membership.connection
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT c.*
    FROM membership.connection c
    JOIN membership.principal p ON p.object_id = c.principal_object_id AND p.disabled_at IS NULL
    JOIN membership.principal a ON a.object_id = c.agent_principal_object_id AND a.disabled_at IS NULL
    JOIN membership.organization o ON o.object_id = c.organization_object_id
                                  AND o.status = 'active' AND o.agents_disabled_at IS NULL
    JOIN membership.organization_membership m ON m.object_id = c.membership_object_id AND upper_inf(m.valid_during)
    WHERE c.agent_principal_object_id = p_agent_principal_object_id
      AND c.revoked_at IS NULL AND c.paused_at IS NULL AND c.expires_at > now();
$$;

-- Records that a live connection acted: at most once a minute, never in a
-- read-only transaction, never waiting on a concurrent request that holds
-- the row.
CREATE OR REPLACE FUNCTION membership.fn_touch_connection(p_connection membership.connection)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, internal, common, pg_temp
AS $$
BEGIN
    IF p_connection.object_id IS NOT NULL
       AND (p_connection.last_used_at IS NULL OR p_connection.last_used_at < now() - interval '1 minute')
       AND NOT internal.transaction_is_read_only() THEN
        UPDATE membership.connection SET last_used_at = now()
        WHERE object_id = (SELECT l.object_id FROM membership.connection l
                           WHERE l.object_id = p_connection.object_id
                           FOR NO KEY UPDATE SKIP LOCKED);
    END IF;
END $$;

-- Why a connection that is not live cannot act now, as {code, message}:
-- membership.connection.paused | membership.connection.disabled (its
-- organization is inactive or has its agents switched off, or its agent is
-- disabled). NULL when it never acts again: revoked, expired, its membership
-- ended or its person disabled.
CREATE OR REPLACE FUNCTION membership.fn_connection_refusal(p_connection membership.connection)
RETURNS jsonb
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT CASE s.state
               WHEN 'membership.connection.paused' THEN jsonb_build_object('code', s.state,
                   'message', 'This client''s connection is paused; its person or an organization manager resumes it on the web')
               WHEN 'membership.connection.disabled' THEN jsonb_build_object('code', s.state,
                   'message', 'This client''s connection cannot act now: its organization or its agent is switched off')
           END
    FROM (
        SELECT CASE
                   WHEN p_connection.object_id IS NULL OR p_connection.revoked_at IS NOT NULL
                        OR p_connection.expires_at <= now() OR NOT coalesce(upper_inf(m.valid_during), false)
                        OR p.disabled_at IS NOT NULL OR p.object_id IS NULL THEN NULL
                   WHEN o.status IS DISTINCT FROM 'active' OR o.agents_disabled_at IS NOT NULL
                        OR a.disabled_at IS NOT NULL THEN 'membership.connection.disabled'
                   WHEN p_connection.paused_at IS NOT NULL THEN 'membership.connection.paused'
                   ELSE 'membership.connection.disabled'
               END AS state
        FROM (SELECT) one
        LEFT JOIN membership.organization_membership m ON m.object_id = p_connection.membership_object_id
        LEFT JOIN membership.organization o ON o.object_id = p_connection.organization_object_id
        LEFT JOIN membership.principal p ON p.object_id = p_connection.principal_object_id
        LEFT JOIN membership.principal a ON a.object_id = p_connection.agent_principal_object_id
    ) s;
$$;

-- The live connection of a handle (membership.fn_live_connection), or why
-- the connection it names cannot act now (membership.fn_connection_refusal).
-- Both NULL: no connection acts on this handle, now or later.
CREATE OR REPLACE FUNCTION membership.fn_resolve_connection(
    p_token text,
    OUT connection membership.connection,
    OUT refusal    jsonb)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, internal, common, pg_temp
AS $$
DECLARE
    v_held membership.connection;
BEGIN
    IF p_token IS NULL OR length(p_token) > 256 THEN
        RETURN;
    END IF;
    SELECT c.* INTO v_held
    FROM membership.connection c
    WHERE c.token_hash = membership.fn_session_token_hash(p_token);
    connection := membership.fn_live_connection(v_held.agent_principal_object_id);
    IF connection.object_id IS NULL THEN
        refusal := membership.fn_connection_refusal(v_held);
        RETURN;
    END IF;
    PERFORM membership.fn_touch_connection(connection);
END $$;

-- The connection an OAuth client acts through for a person, from the
-- identity the gateway verified in its access token. The person is the one
-- that issuer and subject signed in as on the web; nobody is provisioned.
-- An unknown pair of person and client gets a pending connection with a
-- pairing code, valid for an hour and renewed by the first call after it
-- expires; concurrent first calls meet in INSERT ... ON CONFLICT and share
-- one code. A connection that no longer acts for good (expired, membership
-- ended) is paired again; approval revokes it. refusal, when no connection
-- acts: {code, message} with code membership.connection.not_admitted (no such enabled
-- person, or an issuer or client id this database does not take) |
-- membership.connection.pending (plus pairingCode and expiresAt, for this caller only) |
-- membership.connection.paused | membership.connection.disabled (the organization is inactive or
-- has its agents switched off, or the agent is disabled).
CREATE OR REPLACE FUNCTION membership.fn_resolve_connection_by_client(
    p_issuer_id  text,
    p_subject    text,
    p_client_id  text,
    OUT connection membership.connection,
    OUT refusal    jsonb)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, internal, common, pg_temp
AS $$
DECLARE
    v_person  uuid;
    v_held    membership.connection;
    v_pending membership.pending_connection;
BEGIN
    SELECT ei.user_principal_object_id INTO v_person
    FROM membership.external_identity ei
    JOIN membership.identity_issuer i ON i.id = ei.issuer_id AND i.kind = 'oidc'
    JOIN membership.principal p ON p.object_id = ei.user_principal_object_id AND p.disabled_at IS NULL
    WHERE ei.issuer_id = p_issuer_id AND ei.subject = p_subject
      AND coalesce(membership.fn_is_clean_name(p_client_id, 255), false);
    IF v_person IS NULL THEN
        refusal := jsonb_build_object('code', 'membership.connection.not_admitted',
            'message', 'This sign-in is not admitted here: sign in on the web first, as the same person');
        RETURN;
    END IF;

    SELECT c.* INTO v_held
    FROM membership.connection c
    WHERE c.principal_object_id = v_person AND c.issuer_id = p_issuer_id AND c.client_id = p_client_id
      AND c.revoked_at IS NULL;
    IF v_held.object_id IS NOT NULL THEN
        connection := membership.fn_live_connection(v_held.agent_principal_object_id);
        IF connection.object_id IS NOT NULL THEN
            PERFORM membership.fn_touch_connection(connection);
            RETURN;
        END IF;
        refusal := membership.fn_connection_refusal(v_held);
        IF refusal IS NOT NULL THEN
            RETURN;
        END IF;
    END IF;

    INSERT INTO membership.pending_connection AS p (principal_object_id, issuer_id, client_id, pairing_code, expires_at)
    VALUES (v_person, p_issuer_id, p_client_id, membership.fn_new_pairing_code(), clock_timestamp() + interval '1 hour')
    ON CONFLICT (principal_object_id, issuer_id, client_id) DO UPDATE
        SET pairing_code = excluded.pairing_code, requested_at = excluded.requested_at, expires_at = excluded.expires_at
        WHERE p.expires_at <= clock_timestamp();
    SELECT p.* INTO STRICT v_pending
    FROM membership.pending_connection p
    WHERE p.principal_object_id = v_person AND p.issuer_id = p_issuer_id AND p.client_id = p_client_id;
    refusal := jsonb_build_object('code', 'membership.connection.pending', 'pairingCode', v_pending.pairing_code,
        'expiresAt', v_pending.expires_at,
        'message', format('Approve this client on the web with pairing code %s', v_pending.pairing_code));
END $$;

-- ============================================================================
-- HOOKS - a resolved connection becomes the request context
-- ============================================================================

-- Makes a connection the request's context: the person acted for by the
-- connection's agent, in the connection's organization; nobody when NULL.
-- Every setting is written, so nothing an earlier request in this
-- transaction left survives.
CREATE OR REPLACE FUNCTION membership.fn_apply_connection_context(p_connection membership.connection)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
BEGIN
    PERFORM membership.fn_set_request_inputs(
        p_principal_id            => p_connection.principal_object_id,
        p_actual_principal_id     => p_connection.agent_principal_object_id,
        p_organization_id         => p_connection.organization_object_id,
        p_mfa                     => false,
        p_login_session_id        => NULL,
        p_organization_session_id => NULL);
    PERFORM membership.fn_snapshot_request_context();
END $$;

-- The framework hooks, replaced here: a connection handle, or an OAuth
-- client's verified identity, becomes the request's context.
CREATE OR REPLACE FUNCTION internal.resolve_connection_context(p_connection text)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_resolved record;
BEGIN
    SELECT * INTO v_resolved FROM membership.fn_resolve_connection(p_connection);
    PERFORM membership.fn_apply_connection_context(v_resolved.connection);
    RETURN v_resolved.refusal;
END $$;

CREATE OR REPLACE FUNCTION internal.resolve_client_context(p_issuer_id text, p_subject text, p_client_id text)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_resolved record;
BEGIN
    SELECT * INTO v_resolved FROM membership.fn_resolve_connection_by_client(p_issuer_id, p_subject, p_client_id);
    PERFORM membership.fn_apply_connection_context(v_resolved.connection);
    RETURN v_resolved.refusal;
END $$;

SELECT pg_temp.kernel_owns(r::regprocedure, p_owner_executes => false)
FROM unnest(ARRAY[
    'membership.fn_record_connection_event(text, membership.connection)',
    'membership.fn_find_membership(uuid, uuid)',
    'membership.fn_connection_approval_problem(uuid, text, text, int)',
    'membership.fn_start_connection(uuid, text, text, int, text, text)',
    'membership.fn_revoke_membership_connections(uuid, uuid)',
    'membership.fn_live_connection(uuid)',
    'membership.fn_connection_refusal(membership.connection)',
    'membership.fn_touch_connection(membership.connection)',
    'membership.fn_resolve_connection(text)',
    'membership.fn_resolve_connection_by_client(text, text, text)',
    'membership.fn_apply_connection_context(membership.connection)']) r;

SELECT pg_temp.kernel_owns(r::regprocedure)
FROM unnest(ARRAY[
    'membership.fn_agent_role_problem(uuid, uuid, text)',
    'membership.fn_approve_connection(uuid, text, text, int)',
    'membership.fn_approve_connection_by_code(text, uuid, text, int)',
    'membership.fn_connection_problem(uuid, text)',
    'membership.fn_pause_connection(uuid)',
    'membership.fn_resume_connection(uuid)',
    'membership.fn_revoke_connection(uuid)',
    'membership.fn_organization_agents_problem(uuid)',
    'membership.fn_set_organization_agents(uuid, boolean)',
    'internal.resolve_connection_context(text)',
    'internal.resolve_client_context(text, text, text)']) r;
