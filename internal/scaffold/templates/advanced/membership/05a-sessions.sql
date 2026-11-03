/*
<pgmi-meta
    id="757552db-731f-44b7-9c2c-3818c57bab11"
    idempotent="true">
  <description>
    Session kernels. api.open_login_session is the only place a person is
    provisioned: the transport calls it after the identity provider signed
    someone in, passing the verified-email and amr claims, and gets an opaque
    handle back (only its hash is stored). The sign-in counts as multi-factor
    when amr contains 'mfa'. An organization session is opened for the current
    login session in one organization the person is a current member of; a
    request opens and ends only its own person's. The framework's hook
    internal.resolve_session_context is replaced here: it resolves the two
    handles of a request into the request context and takes its snapshot. So
    is the replay pair: internal.request_log_context logs the effective and
    actual principals, the actor kind, the organization and the second
    factor, and internal.apply_request_log_context
    sets them again for api.replay_exchange, with no session. The
    kernel role owns every SECURITY DEFINER routine here but the transport's
    entry points in api.
  </description>
  <sortKeys>
    <key>005/000/005/001</key>
  </sortKeys>
</pgmi-meta>
*/

-- 32 random bytes, base64url, behind a readable prefix.
CREATE OR REPLACE FUNCTION membership.fn_new_session_token(p_prefix text)
RETURNS text LANGUAGE sql VOLATILE
SET search_path = membership, extensions, common, pg_temp AS $$
    SELECT p_prefix || '_' || translate(rtrim(encode(extensions.gen_random_bytes(32), 'base64'), '='), '+/', '-_');
$$;

CREATE OR REPLACE FUNCTION membership.fn_session_token_hash(p_token text)
RETURNS text LANGUAGE sql IMMUTABLE
SET search_path = membership, extensions, common, pg_temp AS $$
    SELECT encode(extensions.digest(p_token, 'sha256'), 'hex');
$$;

-- ============================================================================
-- Login sessions
-- ============================================================================

-- Opens a login session for a login of an enabled person; NULL otherwise.
CREATE OR REPLACE FUNCTION membership.fn_start_login_session(p_external_identity_object_id uuid, p_amr text[])
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_token text := membership.fn_new_session_token('ls');
BEGIN
    INSERT INTO membership.login_session
        (token_hash, principal_object_id, external_identity_object_id, authentication_methods, is_mfa,
         idle_expires_at, absolute_expires_at)
    SELECT membership.fn_session_token_hash(v_token), ei.user_principal_object_id, ei.object_id,
           coalesce(p_amr, '{}'), 'mfa' = ANY (coalesce(p_amr, '{}')),
           clock_timestamp() + interval '12 hours', clock_timestamp() + interval '7 days'
    FROM membership.external_identity ei
    JOIN membership.principal p ON p.object_id = ei.user_principal_object_id AND p.disabled_at IS NULL
    WHERE ei.object_id = p_external_identity_object_id;
    RETURN CASE WHEN FOUND THEN v_token END;
END $$;

-- Provisions (or links, both sides verified) the person behind a login the
-- transport authenticated, accepts their pending invitations and opens a
-- login session. Creates no organization. NULL when the login cannot be
-- provisioned or the person is disabled.
CREATE OR REPLACE FUNCTION membership.fn_open_login_session(
    p_issuer_id         text,
    p_subject           text,
    p_email             text,
    p_display_name      text,
    p_is_email_verified boolean,
    p_amr               text[])
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_principal uuid;
BEGIN
    v_principal := (membership.fn_provision_login(p_issuer_id, p_subject, p_email, p_display_name,
                                                  p_is_email_verified)).principal_object_id;
    IF v_principal IS NULL THEN
        RETURN NULL;
    END IF;
    PERFORM membership.fn_accept_pending_invitations(v_principal);
    RETURN membership.fn_start_login_session(
        (SELECT ei.object_id FROM membership.external_identity ei
          WHERE ei.issuer_id = p_issuer_id AND ei.subject = p_subject),
        p_amr);
END $$;

-- Why a login is refused, as a problem code, or NULL; the rules of
-- membership.fn_provision_login in the same order.
CREATE OR REPLACE FUNCTION membership.fn_login_problem(
    p_issuer_id text, p_subject text, p_email text, p_is_email_verified boolean)
RETURNS text
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    WITH known AS (
        SELECT ei.user_principal_object_id AS principal_object_id
        FROM membership.external_identity ei
        WHERE ei.issuer_id = p_issuer_id AND ei.subject = p_subject
    ), by_email AS (
        SELECT u.principal_object_id, u.is_email_verified
        FROM membership.user_principal u
        WHERE u.email = membership.fn_normalize_email(p_email)
    )
    SELECT CASE
        WHEN NOT coalesce(membership.fn_is_valid_subject(p_subject), false)
            THEN 'login.subject_invalid'
        WHEN NOT EXISTS (SELECT FROM known)
             AND NOT coalesce(membership.fn_is_well_formed_email(membership.fn_normalize_email(p_email)), false)
            THEN 'login.email_missing'
        WHEN NOT EXISTS (SELECT FROM known) AND EXISTS (SELECT FROM by_email)
             AND NOT (coalesce(p_is_email_verified, false) AND (SELECT is_email_verified FROM by_email))
            THEN 'login.email_unverified'
        WHEN EXISTS (SELECT FROM membership.principal p
                     WHERE p.object_id = coalesce((SELECT principal_object_id FROM known),
                                                  (SELECT principal_object_id FROM by_email))
                       AND p.disabled_at IS NOT NULL)
            THEN 'login.principal_disabled'
    END;
$$;

-- The transport's entry point after a real sign-in through an OIDC issuer
-- this deployment trusts (the oidc_issuers parameter). p_is_email_verified is
-- the email_verified claim, p_amr the amr claim. Either the session handle,
-- or a problem code: login.issuer_unknown | login.subject_invalid |
-- login.email_missing | login.email_unverified | login.principal_disabled |
-- login.refused.
CREATE OR REPLACE FUNCTION api.open_login_session(
    p_issuer_id         text,
    p_subject           text,
    p_email             text,
    p_display_name      text,
    p_is_email_verified boolean,
    p_amr               text[],
    OUT token           text,
    OUT problem         text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
BEGIN
    IF NOT EXISTS (SELECT FROM membership.identity_issuer WHERE id = p_issuer_id AND kind = 'oidc') THEN
        problem := 'login.issuer_unknown';
        RETURN;
    END IF;
    token := membership.fn_open_login_session(p_issuer_id, p_subject, p_email, p_display_name,
                                              p_is_email_verified, p_amr);
    IF token IS NULL THEN
        problem := coalesce(membership.fn_login_problem(p_issuer_id, p_subject, p_email, p_is_email_verified),
                            'login.refused');
    END IF;
END $$;

COMMENT ON FUNCTION api.open_login_session(text, text, text, text, boolean, text[]) IS
    'Transport entry point after an OIDC sign-in: provisions or links the person, accepts their pending invitations and returns an opaque login-session handle, or a problem code. The only path that provisions a person. Creates no organization.';

-- Logout: revokes the login session, and with it every organization session.
CREATE OR REPLACE FUNCTION membership.fn_close_login_session(p_token text)
RETURNS boolean
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    WITH s AS (
        UPDATE membership.login_session SET revoked_at = clock_timestamp(), revoke_reason = 'logout'
        WHERE token_hash = membership.fn_session_token_hash(p_token) AND revoked_at IS NULL
        RETURNING 1
    )
    SELECT EXISTS (SELECT FROM s);
$$;

CREATE OR REPLACE FUNCTION api.close_login_session(p_token text)
RETURNS boolean
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT membership.fn_close_login_session(p_token);
$$;

-- The live login session of a handle, of an enabled person. Idle expiry
-- slides, written at most every 5 minutes and never in a read-only transaction.
CREATE OR REPLACE FUNCTION membership.fn_resolve_login_session(p_token text)
RETURNS membership.login_session
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, internal, common, pg_temp
AS $$
DECLARE
    v_row membership.login_session;
BEGIN
    IF p_token IS NULL OR length(p_token) > 256 THEN
        RETURN NULL;
    END IF;
    SELECT l.* INTO v_row
    FROM membership.login_session l
    JOIN membership.principal p ON p.object_id = l.principal_object_id AND p.disabled_at IS NULL
    WHERE l.token_hash = membership.fn_session_token_hash(p_token)
      AND l.revoked_at IS NULL AND l.idle_expires_at > now() AND l.absolute_expires_at > now();
    IF v_row.object_id IS NOT NULL AND v_row.last_seen_at < now() - interval '5 minutes'
       AND NOT internal.transaction_is_read_only() THEN
        UPDATE membership.login_session
        SET last_seen_at = now(), idle_expires_at = least(now() + interval '12 hours', absolute_expires_at)
        WHERE object_id = (SELECT l.object_id FROM membership.login_session l
                           WHERE l.object_id = v_row.object_id
                           FOR NO KEY UPDATE SKIP LOCKED);
    END IF;
    RETURN v_row;
END $$;

-- ============================================================================
-- Organization sessions
-- ============================================================================

-- The live organization session of a handle, only within its own login
-- session, while its membership is current and the organization active.
CREATE OR REPLACE FUNCTION membership.fn_resolve_organization_session(p_token text, p_login_session_object_id uuid)
RETURNS membership.organization_session
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    SELECT w.*
    FROM membership.organization_session w
    JOIN membership.organization o ON o.object_id = w.organization_object_id AND o.status = 'active'
    JOIN membership.organization_membership m ON m.object_id = w.membership_object_id AND upper_inf(m.valid_during)
    WHERE w.token_hash = membership.fn_session_token_hash(p_token)
      AND w.login_session_object_id = p_login_session_object_id
      AND w.ended_at IS NULL
      AND p_token IS NOT NULL AND length(p_token) <= 256;
$$;

-- Opens an organization session for a live login session. problem:
-- not_a_member (an unknown organization and no current membership are
-- deliberately the same answer) | organization_unavailable.
CREATE OR REPLACE FUNCTION membership.fn_open_organization_session(
    p_login_session_object_id uuid,
    p_organization_object_id  uuid,
    OUT token                 text,
    OUT problem               text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_member uuid;
    v_status text;
BEGIN
    SELECT m.object_id, o.status INTO v_member, v_status
    FROM membership.login_session l
    JOIN membership.organization_membership m ON m.principal_object_id = l.principal_object_id
                                             AND m.organization_object_id = p_organization_object_id
                                             AND upper_inf(m.valid_during)
    JOIN membership.organization o ON o.object_id = m.organization_object_id
    WHERE l.object_id = p_login_session_object_id AND l.revoked_at IS NULL AND l.absolute_expires_at > now()
      AND (api.current_principal_id() IS NULL OR l.principal_object_id = api.current_principal_id());
    IF v_member IS NULL THEN
        problem := 'not_a_member';
        RETURN;
    END IF;
    IF v_status <> 'active' THEN
        problem := 'organization_unavailable';
        RETURN;
    END IF;
    token := membership.fn_new_session_token('os');
    INSERT INTO membership.organization_session (token_hash, login_session_object_id, organization_object_id, membership_object_id)
    VALUES (membership.fn_session_token_hash(token), p_login_session_object_id, p_organization_object_id, v_member);
END $$;

CREATE OR REPLACE FUNCTION membership.fn_end_organization_session(p_organization_session_object_id uuid)
RETURNS boolean
LANGUAGE sql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
    WITH e AS (
        UPDATE membership.organization_session w SET ended_at = clock_timestamp()
        FROM membership.login_session l
        WHERE w.object_id = p_organization_session_object_id AND w.ended_at IS NULL
          AND l.object_id = w.login_session_object_id
          AND (api.current_principal_id() IS NULL OR l.principal_object_id = api.current_principal_id())
        RETURNING 1
    )
    SELECT EXISTS (SELECT FROM e);
$$;

-- ============================================================================
-- The framework hook: a request's handles become its context
-- ============================================================================

-- Every setting is written, so nothing an earlier request in this transaction
-- left survives. The organization comes only from the organization session.
CREATE OR REPLACE FUNCTION internal.resolve_session_context(p_login_session text, p_organization_session text)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = membership, common, pg_temp
AS $$
DECLARE
    v_login membership.login_session := membership.fn_resolve_login_session(p_login_session);
    v_work  membership.organization_session;
BEGIN
    IF v_login.object_id IS NOT NULL THEN
        v_work := membership.fn_resolve_organization_session(p_organization_session, v_login.object_id);
    END IF;
    PERFORM set_config('auth.login_session_id', coalesce(v_login.object_id::text, ''), true);
    PERFORM set_config('auth.principal_id', coalesce(v_login.principal_object_id::text, ''), true);
    PERFORM set_config('auth.actual_principal_id', '', true);
    PERFORM set_config('auth.mfa', CASE WHEN v_login.is_mfa THEN 'on' ELSE '' END, true);
    PERFORM set_config('auth.organization_session_id', coalesce(v_work.object_id::text, ''), true);
    PERFORM set_config('auth.organization_id', coalesce(v_work.organization_object_id::text, ''), true);
    PERFORM membership.fn_snapshot_request_context();
END $$;

CREATE OR REPLACE FUNCTION internal.request_log_context()
RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT jsonb_strip_nulls(jsonb_build_object(
        'principalId', api.current_principal_id(),
        'actualPrincipalId', api.current_actual_principal_id(),
        'actorKind', api.current_actor_kind(),
        'organizationId', api.current_organization_id(),
        'mfa', CASE WHEN current_setting('auth.mfa', true) = 'on' THEN true END));
$$;

-- Membership and permissions are resolved live from these inputs, so a replay
-- decides with today's roles, not the ones the request had, and an agent's
-- replay with its connection as it is today: revoked, it acts as no one. The
-- logged actor kind is not an input; it follows from the actual principal.
CREATE OR REPLACE FUNCTION internal.apply_request_log_context(p_context jsonb)
RETURNS void
LANGUAGE sql AS $$
    SELECT set_config('auth.login_session_id', '', true),
           set_config('auth.organization_session_id', '', true),
           set_config('auth.principal_id', coalesce(p_context->>'principalId', ''), true),
           set_config('auth.actual_principal_id', coalesce(p_context->>'actualPrincipalId', ''), true),
           set_config('auth.organization_id', coalesce(p_context->>'organizationId', ''), true),
           set_config('auth.mfa', CASE WHEN p_context->'mfa' = 'true'::jsonb THEN 'on' ELSE '' END, true),
           set_config('auth.ctx', '', true);
$$;

SELECT pg_temp.kernel_owns(r::regprocedure, false)
FROM unnest(ARRAY[
    'membership.fn_start_login_session(uuid, text[])',
    'membership.fn_resolve_login_session(text)',
    'membership.fn_resolve_organization_session(text, uuid)']) r;

SELECT pg_temp.kernel_owns(r::regprocedure)
FROM unnest(ARRAY[
    'membership.fn_open_login_session(text, text, text, text, boolean, text[])',
    'membership.fn_login_problem(text, text, text, boolean)',
    'membership.fn_close_login_session(text)',
    'membership.fn_open_organization_session(uuid, uuid)',
    'membership.fn_end_organization_session(uuid)',
    'internal.resolve_session_context(text, text)']) r;
