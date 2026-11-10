/*
<pgmi-meta
    id="d99b700b-417b-46e4-b225-f9e9d98c789f"
    idempotent="true">
  <description>
    The request context: who the request acts for (the effective principal),
    who really acts (the actual principal) and as what kind of actor, in which
    organization, with which permissions. Inputs are transaction-local
    settings (auth.principal_id, auth.actual_principal_id, auth.organization_id,
    auth.mfa and the session ids) that the gateway sets
    from the request's sessions (internal.resolve_session_context, replaced
    here) or that a trusted SQL session sets directly. Any connection can SET
    auth.*, so they count only from a login that is a member of the api role
    (the gateways, the admin login, the deploy); from any other login there is
    no principal, no organization and no permission. The organization is one
    per request and holds only through a current membership in an active
    organization. An actual principal other than the effective one counts
    only as the agent of a live connection of that person, which also fixes
    the organization; anything else gives no principal at all. The actor kind
    follows from the actual principal: human, agent, service, or system when
    there is none. Platform permissions need a second factor (the login
    session's amr claim) unless platform_mfa_required=false. The context is
    resolved once per request into a snapshot the typed readers return while
    its inputs are unchanged. This file also replaces the framework's
    permission hook.
  </description>
  <sortKeys>
    <key>005/000/003</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION internal.is_platform_permission(p_permission_id text)
RETURNS boolean
LANGUAGE sql STABLE
SET search_path = membership, common, pg_temp
AS $$
    SELECT p.is_platform FROM membership.permission p WHERE p.id = p_permission_id;
$$;

-- session_user, not current_user: inside SECURITY DEFINER code current_user is
-- the owner. The role name and the MFA switch are fixed at deploy.
DO $$
BEGIN
    EXECUTE format($fn$
        CREATE OR REPLACE FUNCTION membership.fn_is_trusted_context()
        RETURNS boolean LANGUAGE sql STABLE AS $body$
            SELECT pg_has_role(session_user, %L, 'MEMBER');
        $body$
    $fn$, pg_temp.deployment_setting('database_api_role'));

    EXECUTE format($fn$
        CREATE OR REPLACE FUNCTION membership.fn_is_platform_mfa_required()
        RETURNS boolean LANGUAGE sql IMMUTABLE AS $body$
            SELECT %L::boolean;
        $body$
    $fn$, pg_temp.deployment_setting('platform_mfa_required'));
END $$;

COMMENT ON FUNCTION membership.fn_is_trusted_context() IS
    'The trust gate: whether this connection''s auth.* settings count. True only for a login that is a member of the api role.';

-- The key of the inputs a snapshot was taken from; NULL from an untrusted
-- connection, so no reader there ever returns a snapshot it set itself.
CREATE OR REPLACE FUNCTION membership.fn_request_context_key()
RETURNS text LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN membership.fn_is_trusted_context() THEN concat_ws('|', 'v2',
        coalesce(current_setting('auth.principal_id', true), ''),
        coalesce(current_setting('auth.actual_principal_id', true), ''),
        coalesce(current_setting('auth.organization_id', true), ''),
        coalesce(current_setting('auth.mfa', true), ''),
        coalesce(current_setting('auth.login_session_id', true), ''),
        coalesce(current_setting('auth.organization_session_id', true), '')) END;
$$;

CREATE OR REPLACE FUNCTION membership.fn_is_platform_operator(p_principal_object_id uuid)
RETURNS boolean
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT EXISTS (SELECT FROM membership.platform_operator po
                   JOIN membership.principal p ON p.object_id = po.user_principal_object_id AND p.disabled_at IS NULL
                   WHERE po.user_principal_object_id = p_principal_object_id
                     AND upper_inf(po.valid_during));
$$;

-- The permissions a principal holds in an organization: its current roles'
-- there, in an active organization, plus every platform permission while it
-- is a platform operator in a session that satisfied MFA.
CREATE OR REPLACE FUNCTION membership.fn_effective_permissions(
    p_principal_object_id    uuid,
    p_organization_object_id uuid,
    p_mfa                    boolean)
RETURNS text[]
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT coalesce(array_agg(DISTINCT s.permission_id ORDER BY s.permission_id), '{}')
    FROM (
        SELECT rp.permission_id
        FROM membership.organization_membership m
        JOIN membership.organization o ON o.object_id = m.organization_object_id AND o.status = 'active'
        JOIN membership.role_assignment ra ON ra.membership_object_id = m.object_id AND upper_inf(ra.valid_during)
        JOIN membership.role_permission rp ON rp.role_id = ra.role_id
        WHERE m.principal_object_id = p_principal_object_id
          AND m.organization_object_id = p_organization_object_id
          AND upper_inf(m.valid_during)
        UNION
        SELECT p.id
        FROM membership.permission p
        WHERE p.is_platform AND p_mfa AND membership.fn_is_platform_operator(p_principal_object_id)
    ) s;
$$;

-- An agent's permissions: its connection's role's intersected with its
-- person's in the connection's organization, never a platform permission.
-- A person who loses a permission takes it from their agents at once.
CREATE OR REPLACE FUNCTION membership.fn_agent_permissions(p_connection membership.connection)
RETURNS text[]
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT coalesce(array_agg(rp.permission_id ORDER BY rp.permission_id), '{}')
    FROM membership.role_permission rp
    WHERE rp.role_id = p_connection.role_id
      AND rp.permission_id = ANY (membership.fn_effective_permissions(
              p_connection.principal_object_id, p_connection.organization_object_id, false));
$$;

-- Everything authorization derives from, resolved live. All empty from an
-- untrusted connection, or when the principal is unknown or disabled. The
-- organization holds only through a current membership in an active one;
-- for an agent it is its connection's, and the connection must be live.
-- PL/pgSQL: SECURITY DEFINER is never inlined, and PL/pgSQL keeps its plans.
CREATE OR REPLACE FUNCTION membership.fn_resolve_request_context(
    OUT principal_object_id        uuid,
    OUT actual_principal_object_id uuid,
    OUT actor_kind                 text,
    OUT organization_object_id     uuid,
    OUT is_platform_operator       boolean,
    OUT permissions                text[])
LANGUAGE plpgsql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_mfa        boolean;
    v_kind       text;
    v_actual     uuid;
    v_connection membership.connection;
BEGIN
    actor_kind := 'system';
    is_platform_operator := false;
    permissions := '{}';
    IF NOT membership.fn_is_trusted_context() THEN
        RETURN;
    END IF;
    SELECT p.object_id, p.kind INTO principal_object_id, v_kind
    FROM membership.principal p
    WHERE p.object_id = common.try_cast(NULLIF(current_setting('auth.principal_id', true), ''), NULL::uuid)
      AND p.kind <> 'agent' AND p.disabled_at IS NULL;
    IF principal_object_id IS NULL THEN
        RETURN;
    END IF;
    v_actual := coalesce(common.try_cast(NULLIF(current_setting('auth.actual_principal_id', true), ''), NULL::uuid),
                         principal_object_id);

    IF v_actual IS DISTINCT FROM principal_object_id THEN
        v_connection := membership.fn_live_connection(v_actual);
        IF v_connection.principal_object_id IS DISTINCT FROM principal_object_id THEN
            principal_object_id := NULL;
            RETURN;
        END IF;
        actual_principal_object_id := v_actual;
        actor_kind := 'agent';
        organization_object_id := v_connection.organization_object_id;
        permissions := membership.fn_agent_permissions(v_connection);
        RETURN;
    END IF;

    actual_principal_object_id := principal_object_id;
    actor_kind := CASE v_kind WHEN 'user' THEN 'human' ELSE 'service' END;
    SELECT m.organization_object_id INTO organization_object_id
    FROM membership.organization_membership m
    JOIN membership.organization o ON o.object_id = m.organization_object_id AND o.status = 'active'
    WHERE m.principal_object_id = fn_resolve_request_context.principal_object_id
      AND m.organization_object_id = common.try_cast(NULLIF(current_setting('auth.organization_id', true), ''), NULL::uuid)
      AND upper_inf(m.valid_during);
    v_mfa := NOT membership.fn_is_platform_mfa_required() OR current_setting('auth.mfa', true) = 'on';
    is_platform_operator := v_mfa AND membership.fn_is_platform_operator(principal_object_id);
    permissions := membership.fn_effective_permissions(principal_object_id, organization_object_id, v_mfa);
END $$;

-- Resolves the context once and keeps it, with the key of its inputs, in
-- transaction-local settings. The readers return it while the key matches and
-- resolve live otherwise, so membership is re-checked on every request.
CREATE OR REPLACE FUNCTION membership.fn_snapshot_request_context()
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v record;
BEGIN
    PERFORM set_config('auth.ctx', '', true);
    SELECT * INTO v FROM membership.fn_resolve_request_context();
    PERFORM set_config('auth.ctx_principal_id', coalesce(v.principal_object_id::text, ''), true);
    PERFORM set_config('auth.ctx_actual_principal_id', coalesce(v.actual_principal_object_id::text, ''), true);
    PERFORM set_config('auth.ctx_actor_kind', v.actor_kind, true);
    PERFORM set_config('auth.ctx_organization_id', coalesce(v.organization_object_id::text, ''), true);
    PERFORM set_config('auth.ctx_is_platform_operator', v.is_platform_operator::text, true);
    PERFORM set_config('auth.ctx_permissions', v.permissions::text, true);
    PERFORM set_config('auth.ctx', coalesce(membership.fn_request_context_key(), ''), true);
END $$;

-- ============================================================================
-- The typed readers: what the gateways, RLS policies, kernels and handlers call
-- ============================================================================

CREATE OR REPLACE FUNCTION api.current_principal_id()
RETURNS uuid LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN current_setting('auth.ctx', true) = membership.fn_request_context_key()
                THEN NULLIF(current_setting('auth.ctx_principal_id', true), '')::uuid
                ELSE (membership.fn_resolve_request_context()).principal_object_id END;
$$;

-- Who really acts: the agent of a connection, otherwise the effective principal.
CREATE OR REPLACE FUNCTION api.current_actual_principal_id()
RETURNS uuid LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN current_setting('auth.ctx', true) = membership.fn_request_context_key()
                THEN NULLIF(current_setting('auth.ctx_actual_principal_id', true), '')::uuid
                ELSE (membership.fn_resolve_request_context()).actual_principal_object_id END;
$$;

-- human | agent | service | system (the request names no principal).
CREATE OR REPLACE FUNCTION api.current_actor_kind()
RETURNS text LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN current_setting('auth.ctx', true) = membership.fn_request_context_key()
                THEN current_setting('auth.ctx_actor_kind', true)
                ELSE (membership.fn_resolve_request_context()).actor_kind END;
$$;

CREATE OR REPLACE FUNCTION api.is_authenticated()
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT api.current_principal_id() IS NOT NULL;
$$;

-- The one organization of the request; NULL means org-scoped RLS matches nothing.
CREATE OR REPLACE FUNCTION api.current_organization_id()
RETURNS uuid LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN current_setting('auth.ctx', true) = membership.fn_request_context_key()
                THEN NULLIF(current_setting('auth.ctx_organization_id', true), '')::uuid
                ELSE (membership.fn_resolve_request_context()).organization_object_id END;
$$;

CREATE OR REPLACE FUNCTION api.current_permissions()
RETURNS text[] LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN current_setting('auth.ctx', true) = membership.fn_request_context_key()
                THEN current_setting('auth.ctx_permissions', true)::text[]
                ELSE (membership.fn_resolve_request_context()).permissions END;
$$;

CREATE OR REPLACE FUNCTION api.has_permission(p_permission text)
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT p_permission = ANY (api.current_permissions());
$$;

-- A platform operator in a session that satisfied MFA.
CREATE OR REPLACE FUNCTION api.is_platform_operator()
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN current_setting('auth.ctx', true) = membership.fn_request_context_key()
                THEN current_setting('auth.ctx_is_platform_operator', true) = 'true'
                ELSE (membership.fn_resolve_request_context()).is_platform_operator END;
$$;

CREATE OR REPLACE FUNCTION api.current_login_session_id()
RETURNS uuid LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN membership.fn_is_trusted_context()
                THEN NULLIF(current_setting('auth.login_session_id', true), '')::uuid END;
$$;

CREATE OR REPLACE FUNCTION api.current_organization_session_id()
RETURNS uuid LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN membership.fn_is_trusted_context()
                THEN NULLIF(current_setting('auth.organization_session_id', true), '')::uuid END;
$$;

DO $$
BEGIN
    EXECUTE format('GRANT EXECUTE ON ROUTINE api.current_principal_id(), api.current_actual_principal_id(), api.current_actor_kind(), '
                   'api.is_authenticated(), api.current_organization_id(), api.current_permissions(), '
                   'api.has_permission(text), api.is_platform_operator(), api.current_login_session_id(), '
                   'api.current_organization_session_id(), internal.transaction_is_read_only() TO %I',
                   pg_temp.deployment_setting('database_kernel_role'));
END $$;

SELECT pg_temp.kernel_owns('membership.fn_is_platform_operator(uuid)', false);
SELECT pg_temp.kernel_owns('membership.fn_effective_permissions(uuid, uuid, boolean)', false);
SELECT pg_temp.kernel_owns('membership.fn_agent_permissions(membership.connection)', false);
SELECT pg_temp.kernel_owns('membership.fn_resolve_request_context()');
SELECT pg_temp.kernel_owns('membership.fn_snapshot_request_context()', false);
