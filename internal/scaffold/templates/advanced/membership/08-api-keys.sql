/*
<pgmi-meta
    id="b1000001-0008-4000-8000-000000000001"
    idempotent="true">
  <description>
    API key authentication for machine-to-machine access (agents, MCP clients).
    Each key creates a matching user_identity row with provider='apikey' so
    the existing JWT/OIDC auth resolution pipeline handles API keys unchanged.
    No quota enforcement — add a tier model if you need per-org limits.
  </description>
  <sortKeys>
    <key>005/000/008</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing API key authentication'; END $$;

-- ============================================================================
-- API Key Prefix Helper
-- ============================================================================
-- Centralized prefix used in the full key format: {prefix}_{key_id}_{secret}.
-- Override by editing this function, or by setting the pgmi.api_key_prefix GUC —
-- validate_api_key and generate_api_key_material both read from it, so changing
-- it here changes both sides atomically.
--
-- The prefix may contain underscores ('acme_prod'). validate_api_key parses from
-- the right, anchoring on the fixed-width key_id and secret, so the prefix is
-- simply whatever precedes them.

CREATE OR REPLACE FUNCTION membership.api_key_prefix()
RETURNS text
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT COALESCE(
        NULLIF(current_setting('pgmi.api_key_prefix', true), ''),
        'pgmi'
    );
$$;

COMMENT ON FUNCTION membership.api_key_prefix IS
    'Prefix segment of API keys. Reads the pgmi.api_key_prefix GUC or defaults to "pgmi". Format: {prefix}_{key_id}_{secret}.';

-- ============================================================================
-- Hash-safe String Comparison
-- ============================================================================
-- Best-effort timing-leak-resistant equality for SHA-256 hashes. Plain `=` on
-- text short-circuits on the first differing byte, leaking the length of the
-- match prefix; this helper XORs every byte and accumulates differences so the
-- loop does not short-circuit. PL/pgSQL cannot guarantee true constant time
-- (JIT, scheduling, branch prediction, caches add variation), so this is named
-- eq_hash_safe, not eq_constant_time: it compares hashes, so any residual
-- timing leak reveals at most hash-prefix similarity, never raw key bytes.

DROP FUNCTION IF EXISTS membership.eq_constant_time(text, text);

CREATE OR REPLACE FUNCTION membership.eq_hash_safe(a text, b text)
RETURNS boolean
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE AS $$
DECLARE
    v_a bytea := convert_to(a, 'UTF8');
    v_b bytea := convert_to(b, 'UTF8');
    v_la int := octet_length(v_a);
    v_lb int := octet_length(v_b);
    v_len int := GREATEST(v_la, v_lb);
    v_diff int := v_la # v_lb;
    i int;
BEGIN
    FOR i IN 0 .. v_len - 1 LOOP
        v_diff := v_diff
            | (CASE WHEN i < v_la THEN get_byte(v_a, i) ELSE 0 END
               # CASE WHEN i < v_lb THEN get_byte(v_b, i) ELSE 0 END);
    END LOOP;
    RETURN v_diff = 0;
END;
$$;

COMMENT ON FUNCTION membership.eq_hash_safe(text, text) IS
    'Hash-safe equality compare for SHA-256 hashes. Avoids the early-exit of plain `=`; not a true constant-time guarantee (PL/pgSQL cannot deliver one), but because it compares hashes any residual timing leak reveals at most hash-prefix similarity, not raw key bytes.';


-- ============================================================================
-- RLS Policies
-- ============================================================================

ALTER TABLE membership.api_key ENABLE ROW LEVEL SECURITY;

-- Table owner bypasses RLS, so SECURITY DEFINER functions (create_api_key,
-- revoke_api_key, etc.) work without explicit owner policies. This policy scopes
-- direct SELECT only; it does NOT constrain the SECURITY DEFINER bodies below,
-- which must carry their own tenant guard (membership.can_manage_api_key).
-- Customer-role callers only need SELECT on keys within their visible orgs —
-- mutations go through the SECURITY DEFINER functions, not direct DML.
DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    DROP POLICY IF EXISTS api_key_customer_select ON membership.api_key;

    EXECUTE format($policy$
        CREATE POLICY api_key_customer_select ON membership.api_key
            FOR SELECT TO %I
            USING (organization_id IN (SELECT unnest(api.current_member_org_ids())))
    $policy$, v_customer_role);
END $$;

-- ============================================================================
-- Key Material Generation
-- ============================================================================

CREATE OR REPLACE FUNCTION membership.generate_api_key_material()
RETURNS TABLE (
    key_id text,
    full_key text,
    key_hash text
)
LANGUAGE sql VOLATILE AS $$
    WITH c_raw AS (
        SELECT
            encode(extensions.gen_random_bytes(6), 'hex') AS key_id,
            encode(extensions.gen_random_bytes(32), 'hex') AS secret
    ),
    c_full_key AS (
        SELECT
            c_raw.key_id,
            membership.api_key_prefix() || '_' || c_raw.key_id || '_' || c_raw.secret AS full_key
        FROM c_raw
    )
    SELECT
        c_full_key.key_id,
        c_full_key.full_key,
        encode(extensions.digest(c_full_key.full_key, 'sha256'), 'hex') AS key_hash
    FROM c_full_key;
$$;

COMMENT ON FUNCTION membership.generate_api_key_material IS
    'Generate API key components: key_id (12 hex chars, uniform length), full key ({prefix}_{id}_{secret}), and SHA-256 hash.';

-- ============================================================================
-- Create API Key
-- ============================================================================
-- SECURITY DEFINER so the caller does not need direct writes on api_key or
-- user_identity. RLS cannot confine the body (it runs as the table owner), so
-- membership.can_create_api_key guards the CALLER first; the argument checks
-- inside create_api_key then validate the TARGET user and organization.

-- A caller may mint a key: for itself in an org it actively belongs to; for any
-- member of an org it owns or admins; or, as a platform superuser, anywhere. A
-- plain reader/contributor cannot mint a peer's key — that would hand over a
-- working credential and impersonate the peer. Fail-closed for identity-less
-- sessions. Unauthorized callers get the same P0404 as a missing org, so this is
-- not a cross-tenant existence oracle.
CREATE OR REPLACE FUNCTION membership.can_create_api_key(p_user_id uuid, p_organization_id uuid)
RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = membership, api, pg_temp
AS $$
    -- COALESCE to false: get_member_role and current_user_id return NULL for a
    -- non-member/identity-less caller, so the bare OR-chain would yield NULL and
    -- IF NOT NULL would silently skip the guard. Fail closed.
    SELECT COALESCE(
        api.current_user_is_admin()
        OR (
            p_user_id = api.current_user_id()
            AND p_organization_id = ANY (api.current_member_org_ids())
        )
        OR membership.is_organization_owner(api.current_user_id(), p_organization_id)
        OR membership.get_member_role(api.current_user_id(), p_organization_id) = 'admin',
        false
    );
$$;

COMMENT ON FUNCTION membership.can_create_api_key(uuid, uuid) IS
    'True when the current identity may issue a key for p_user_id in p_organization_id: platform superuser, self-service for an org it belongs to, or an admin/owner of the org provisioning for a member. Caller guard for create_api_key.';

CREATE OR REPLACE FUNCTION membership.create_api_key(
    p_user_id uuid,
    p_organization_id uuid,
    p_display_name text,
    p_expires_at timestamptz DEFAULT NULL,
    p_activated_at timestamptz DEFAULT NULL
)
RETURNS TABLE (
    out_api_key text,
    out_key_id text,
    out_object_id uuid
)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = membership, api, extensions, pg_temp
AS $$
DECLARE
    v_key_material record;
    v_object_id uuid;
    v_retry int;
BEGIN
    IF NOT membership.can_create_api_key(p_user_id, p_organization_id) THEN
        RAISE EXCEPTION 'Organization not found: %', p_organization_id
            USING ERRCODE = 'P0404';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM membership."user" c_user
        WHERE c_user.object_id = p_user_id AND c_user.is_active
    ) THEN
        RAISE EXCEPTION 'User not found or inactive: %', p_user_id
            USING ERRCODE = 'P0404';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM membership.organization c_org
        WHERE c_org.object_id = p_organization_id AND c_org.is_active
    ) THEN
        RAISE EXCEPTION 'Organization not found or inactive: %', p_organization_id
            USING ERRCODE = 'P0404';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM membership.organization c_org
        WHERE c_org.object_id = p_organization_id
          AND c_org.owner_user_id = p_user_id
          AND c_org.is_active
        UNION ALL
        SELECT 1 FROM membership.organization_member c_member
        WHERE c_member.organization_id = p_organization_id
          AND c_member.user_id = p_user_id
          AND c_member.status = 'active'
    ) THEN
        RAISE EXCEPTION 'User is not an active member of organization'
            USING ERRCODE = 'P0403';
    END IF;

    FOR v_retry IN 1..3 LOOP
        SELECT * INTO v_key_material FROM membership.generate_api_key_material();

        INSERT INTO membership.api_key (
            organization_id, user_id,
            key_id, key_hash, display_name,
            activated_at, expires_at
        ) VALUES (
            p_organization_id, p_user_id,
            v_key_material.key_id, v_key_material.key_hash, trim(p_display_name),
            p_activated_at, p_expires_at
        )
        ON CONFLICT (key_id) DO NOTHING
        RETURNING membership.api_key.object_id INTO v_object_id;

        IF FOUND THEN
            EXIT;
        END IF;
    END LOOP;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'Failed to generate unique key_id after retries'
            USING ERRCODE = 'P0500';
    END IF;

    INSERT INTO membership.user_identity (user_object_id, idp_provider, idp_subject_id)
    VALUES (p_user_id, 'apikey', v_key_material.key_id);

    RAISE DEBUG 'Created API key % for user % in org %',
        v_key_material.key_id, p_user_id, p_organization_id;

    RETURN QUERY SELECT v_key_material.full_key, v_key_material.key_id, v_object_id;
END;
$$;

COMMENT ON FUNCTION membership.create_api_key IS
    'Issue an API key for a user in an organization. Returns the plaintext key (shown only at creation) and creates a matching user_identity row for auth resolution.';

-- ============================================================================
-- Validate API Key
-- ============================================================================
-- Invoked by the gateway before auth context is established, so SECURITY
-- DEFINER is required. Updates last_used_at on success to avoid a second
-- round-trip per request.

CREATE OR REPLACE FUNCTION membership.validate_api_key(p_raw_key text)
RETURNS TABLE (
    is_valid boolean,
    user_id uuid,
    organization_id uuid,
    key_id text,
    reason text
)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = membership, extensions, pg_temp
AS $$
DECLARE
    v_prefix text := membership.api_key_prefix();
    v_parts text[];
    v_key_id text;
    v_computed_hash text;
    v_key membership.api_key%ROWTYPE;
BEGIN
    -- Strip the prefix literally, then parse what remains. Do not match the
    -- prefix with a pattern: it is operator-chosen and a natural one contains an
    -- underscore ('acme_prod'), which a split-on-'_' parse rejected as malformed
    -- — create_api_key issued keys validate_api_key would never accept, silently
    -- and permanently breaking auth for every key under that prefix.
    --
    -- key_id is matched at the width 01-schema.sql declares (>= 6), NOT at the
    -- width generate_api_key_material happens to emit today (12). key_id width is
    -- a property of when a key was issued; pinning the parse to the current
    -- generator strands every key already in the table. The secret stays pinned
    -- at exactly 64 hex so a key with a tampered secret reaches the hash
    -- comparison instead of dying at the parse.
    IF p_raw_key IS NULL THEN
        RETURN QUERY SELECT false, NULL::uuid, NULL::uuid, NULL::text, 'malformed key'::text;
        RETURN;
    END IF;

    IF left(p_raw_key, length(v_prefix) + 1) IS DISTINCT FROM v_prefix || '_' THEN
        RETURN QUERY SELECT false, NULL::uuid, NULL::uuid, NULL::text, 'malformed key'::text;
        RETURN;
    END IF;

    v_parts := regexp_match(
        substr(p_raw_key, length(v_prefix) + 2),
        '^([0-9a-f]{6,})_([0-9a-f]{64})$'
    );

    IF v_parts IS NULL THEN
        RETURN QUERY SELECT false, NULL::uuid, NULL::uuid, NULL::text, 'malformed key'::text;
        RETURN;
    END IF;

    v_key_id := v_parts[1];

    SELECT c_key.* INTO v_key
    FROM membership.api_key c_key
    WHERE c_key.key_id = v_key_id
      AND c_key.deleted_at IS NULL;

    IF NOT FOUND THEN
        RETURN QUERY SELECT false, NULL::uuid, NULL::uuid, NULL::text, 'unknown key'::text;
        RETURN;
    END IF;

    v_computed_hash := encode(extensions.digest(p_raw_key, 'sha256'), 'hex');
    IF NOT membership.eq_hash_safe(v_computed_hash, v_key.key_hash) THEN
        RETURN QUERY SELECT false, NULL::uuid, NULL::uuid, v_key_id, 'invalid secret'::text;
        RETURN;
    END IF;

    IF v_key.status != 'active' THEN
        RETURN QUERY SELECT false, v_key.user_id, v_key.organization_id, v_key_id,
            ('key is ' || v_key.status::text)::text;
        RETURN;
    END IF;

    IF v_key.activated_at IS NOT NULL AND v_key.activated_at > now() THEN
        RETURN QUERY SELECT false, v_key.user_id, v_key.organization_id, v_key_id,
            'key not yet active'::text;
        RETURN;
    END IF;

    IF v_key.expires_at IS NOT NULL AND v_key.expires_at <= now() THEN
        RETURN QUERY SELECT false, v_key.user_id, v_key.organization_id, v_key_id,
            'key expired'::text;
        RETURN;
    END IF;

    -- Reject when the owning user or organization has been deactivated.
    -- Without this check, deactivated principals retain valid credentials
    -- until each key is individually revoked — a privilege-escalation path.
    IF NOT EXISTS (
        SELECT 1 FROM membership."user" u
        WHERE u.object_id = v_key.user_id AND u.is_active
    ) THEN
        RETURN QUERY SELECT false, v_key.user_id, v_key.organization_id, v_key_id,
            'user is inactive'::text;
        RETURN;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM membership.organization o
        WHERE o.object_id = v_key.organization_id AND o.is_active
    ) THEN
        RETURN QUERY SELECT false, v_key.user_id, v_key.organization_id, v_key_id,
            'organization is inactive'::text;
        RETURN;
    END IF;

    UPDATE membership.api_key c_key
    SET last_used_at = now()
    WHERE c_key.key_id = v_key_id;

    RETURN QUERY SELECT true, v_key.user_id, v_key.organization_id, v_key_id, NULL::text;
END;
$$;

COMMENT ON FUNCTION membership.validate_api_key IS
    'Validate an API key and return the user/org context. Updates last_used_at on success.';

-- ============================================================================
-- Lifecycle: disable, enable, revoke
-- ============================================================================
-- The lifecycle functions are SECURITY DEFINER and run as the table owner, so
-- RLS never constrains them. Without this guard any authenticated session could
-- mutate any key in the cluster by key_id alone. Fail-closed: a session with no
-- resolvable identity manages nothing. Platform superusers cross tenants; every
-- other caller is confined to the orgs it is an active member of.

CREATE OR REPLACE FUNCTION membership.can_manage_api_key(p_key_id text)
RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = membership, api, pg_temp
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM membership.api_key c_api_key
        WHERE c_api_key.key_id = p_key_id
          AND c_api_key.deleted_at IS NULL
          AND (
              api.current_user_is_admin()
              -- Mere membership was enough, so any reader could irreversibly
              -- revoke the org owner's keys. Mirror can_create_api_key: minting
              -- and destroying a credential are the same level of authority.
              OR c_api_key.user_id = api.current_user_id()
              OR membership.is_organization_owner(api.current_user_id(), c_api_key.organization_id)
              OR membership.get_member_role(api.current_user_id(), c_api_key.organization_id) = 'admin'
          )
    );
$$;

COMMENT ON FUNCTION membership.can_manage_api_key(text) IS
    'True when the current identity may manage the key: platform superuser, the key''s own owner, or an admin/owner of the key''s organization. Tenant guard for the lifecycle functions.';

CREATE OR REPLACE FUNCTION membership.disable_api_key(p_key_id text)
RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = membership, api, pg_temp
AS $$
BEGIN
    -- Same 'not found' for "no such key" and "not yours" — a distinct error
    -- would turn this into a cross-tenant key-existence oracle.
    IF NOT membership.can_manage_api_key(p_key_id) THEN
        RAISE EXCEPTION 'API key not found: %', p_key_id USING ERRCODE = 'P0404';
    END IF;

    UPDATE membership.api_key c_api_key
    SET status = 'disabled'
    WHERE c_api_key.key_id = p_key_id
      AND c_api_key.deleted_at IS NULL
      AND c_api_key.status = 'active';

    IF NOT FOUND THEN
        IF EXISTS (
            SELECT 1 FROM membership.api_key c_api_key
            WHERE c_api_key.key_id = p_key_id AND c_api_key.deleted_at IS NULL AND c_api_key.status = 'revoked'
        ) THEN
            RAISE EXCEPTION 'Cannot disable a revoked key' USING ERRCODE = 'P0409';
        ELSIF EXISTS (
            SELECT 1 FROM membership.api_key c_api_key
            WHERE c_api_key.key_id = p_key_id AND c_api_key.deleted_at IS NULL AND c_api_key.status = 'disabled'
        ) THEN
            RETURN;
        ELSE
            RAISE EXCEPTION 'API key not found: %', p_key_id USING ERRCODE = 'P0404';
        END IF;
    END IF;
END;
$$;

COMMENT ON FUNCTION membership.disable_api_key IS
    'Temporarily disable an API key (reversible). Tenant-guarded: raises P0404 unless the caller is a platform superuser or an active member of the key''s organization. Raises P0409 if the key is revoked.';

CREATE OR REPLACE FUNCTION membership.enable_api_key(p_key_id text)
RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = membership, api, pg_temp
AS $$
BEGIN
    IF NOT membership.can_manage_api_key(p_key_id) THEN
        RAISE EXCEPTION 'API key not found: %', p_key_id USING ERRCODE = 'P0404';
    END IF;

    UPDATE membership.api_key c_api_key
    SET status = 'active'
    WHERE c_api_key.key_id = p_key_id
      AND c_api_key.deleted_at IS NULL
      AND c_api_key.status = 'disabled';

    IF NOT FOUND THEN
        IF EXISTS (
            SELECT 1 FROM membership.api_key c_api_key
            WHERE c_api_key.key_id = p_key_id AND c_api_key.deleted_at IS NULL AND c_api_key.status = 'revoked'
        ) THEN
            RAISE EXCEPTION 'Cannot enable a revoked key (revocation is permanent)' USING ERRCODE = 'P0409';
        ELSIF EXISTS (
            SELECT 1 FROM membership.api_key c_api_key
            WHERE c_api_key.key_id = p_key_id AND c_api_key.deleted_at IS NULL AND c_api_key.status = 'active'
        ) THEN
            RETURN;
        ELSE
            RAISE EXCEPTION 'API key not found: %', p_key_id USING ERRCODE = 'P0404';
        END IF;
    END IF;
END;
$$;

COMMENT ON FUNCTION membership.enable_api_key IS
    'Re-enable a disabled API key. Tenant-guarded: raises P0404 unless the caller is a platform superuser or an active member of the key''s organization. Raises P0409 if the key is revoked.';

CREATE OR REPLACE FUNCTION membership.revoke_api_key(p_key_id text)
RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = membership, api, pg_temp
AS $$
BEGIN
    IF NOT membership.can_manage_api_key(p_key_id) THEN
        RAISE EXCEPTION 'API key not found: %', p_key_id USING ERRCODE = 'P0404';
    END IF;

    UPDATE membership.api_key c_api_key
    SET status = 'revoked'
    WHERE c_api_key.key_id = p_key_id
      AND c_api_key.deleted_at IS NULL
      AND c_api_key.status != 'revoked';

    IF NOT FOUND THEN
        IF EXISTS (
            SELECT 1 FROM membership.api_key c_api_key
            WHERE c_api_key.key_id = p_key_id AND c_api_key.deleted_at IS NULL AND c_api_key.status = 'revoked'
        ) THEN
            RETURN;
        ELSE
            RAISE EXCEPTION 'API key not found: %', p_key_id USING ERRCODE = 'P0404';
        END IF;
    END IF;

    DELETE FROM membership.user_identity c_identity
    WHERE c_identity.idp_provider = 'apikey'
      AND c_identity.idp_subject_id = p_key_id;
END;
$$;

COMMENT ON FUNCTION membership.revoke_api_key IS
    'Permanently revoke an API key and remove its matching user_identity row. Irreversible. Tenant-guarded: raises P0404 unless the caller is a platform superuser or an active member of the key''s organization.';

-- ============================================================================
-- Grants
-- ============================================================================
-- The lifecycle functions are SECURITY DEFINER and execute as the owner
-- regardless of the caller. Grant EXECUTE to the API role so the customer-facing
-- REST/RPC handlers can issue, disable, and revoke keys on behalf of their users;
-- membership.can_manage_api_key — not RLS — is what confines each caller to its
-- own organizations. The customer role is granted the same four so a direct
-- customer session can self-serve; it is named here rather than inherited from
-- PUBLIC, because deploy.sql revokes PUBLIC across this schema once every file
-- has run. validate_api_key stays off that list: it resolves a raw key to its
-- owner and belongs to the gateway, not to the sessions it authenticates.

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    -- Read-only privileges across the board. Mutations MUST flow through the
    -- SECURITY DEFINER lifecycle functions (create/disable/enable/revoke) so
    -- the membership and quota checks cannot be bypassed by direct DML — even
    -- by the admin role. Idempotent against earlier deploys that granted
    -- INSERT/UPDATE/DELETE to admin.
    EXECUTE format('GRANT SELECT ON membership.api_key TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON membership.api_key TO %I', v_admin_role);
    EXECUTE format('REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON membership.api_key FROM %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON membership.api_key TO %I', v_customer_role);

    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.api_key_prefix() TO %I, %I, %I', v_admin_role, v_api_role, v_customer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.generate_api_key_material() TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.create_api_key(uuid,uuid,text,timestamptz,timestamptz) TO %I, %I, %I', v_admin_role, v_api_role, v_customer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.validate_api_key(text) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.disable_api_key(text) TO %I, %I, %I', v_admin_role, v_api_role, v_customer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.enable_api_key(text) TO %I, %I, %I', v_admin_role, v_api_role, v_customer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.revoke_api_key(text) TO %I, %I, %I', v_admin_role, v_api_role, v_customer_role);
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ membership.api_key - key table with hashed secrets';
    RAISE NOTICE '  ✓ membership.api_key_prefix() - centralized prefix helper';
    RAISE NOTICE '  ✓ membership.generate_api_key_material() - key generation';
    RAISE NOTICE '  ✓ membership.can_create_api_key() - caller guard for key issuance';
    RAISE NOTICE '  ✓ membership.create_api_key() - issue key + user_identity';
    RAISE NOTICE '  ✓ membership.validate_api_key() - validate for auth';
    RAISE NOTICE '  ✓ membership.can_manage_api_key() - tenant guard for the lifecycle functions';
    RAISE NOTICE '  ✓ membership.disable_api_key() / enable_api_key() / revoke_api_key()';
END $$;
