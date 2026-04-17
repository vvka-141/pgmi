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
-- Override by editing this function — validate_api_key and generate_api_key_material
-- both read from it, so changing it here changes both sides atomically.

CREATE OR REPLACE FUNCTION membership.api_key_prefix()
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT COALESCE(
        NULLIF(current_setting('pgmi.api_key_prefix', true), ''),
        'pgmi'
    );
$$;

COMMENT ON FUNCTION membership.api_key_prefix IS
    'Prefix segment of API keys. Reads the pgmi.api_key_prefix GUC or defaults to "pgmi". Format: {prefix}_{key_id}_{secret}.';

-- ============================================================================
-- Constant-time String Comparison
-- ============================================================================
-- Eliminates the timing side-channel in hash comparisons. Plain `=` on text
-- short-circuits on the first differing byte, leaking the length of the match
-- prefix. This helper XORs every byte and accumulates differences so execution
-- time is independent of where inputs diverge.

CREATE OR REPLACE FUNCTION membership.eq_constant_time(a text, b text)
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

COMMENT ON FUNCTION membership.eq_constant_time(text, text) IS
    'Constant-time equality compare for hex/text secrets. Runtime is independent of where inputs diverge, eliminating the timing side-channel present in plain `=`.';

-- ============================================================================
-- API Key Status Enum
-- ============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'api_key_status' AND typnamespace = 'membership'::regnamespace) THEN
        CREATE TYPE membership.api_key_status AS ENUM ('active', 'disabled', 'revoked');
    END IF;
END $$;

-- ============================================================================
-- API Key Table
-- ============================================================================
-- object_id core.entity_id opts this table into the DDL-trigger entity
-- standards: created_at and deleted_at are injected automatically.

CREATE TABLE IF NOT EXISTS membership.api_key (
    object_id core.entity_id PRIMARY KEY DEFAULT gen_random_uuid(),

    organization_id uuid NOT NULL REFERENCES membership.organization(object_id),
    user_id uuid NOT NULL REFERENCES membership."user"(object_id),

    key_id text NOT NULL,
    key_hash text NOT NULL,
    display_name text NOT NULL,
    status membership.api_key_status NOT NULL DEFAULT 'active',
    activated_at timestamptz,
    expires_at timestamptz,
    last_used_at timestamptz,

    CONSTRAINT uq_api_key_key_id UNIQUE (key_id),
    CONSTRAINT ck_api_key_display_name_not_empty CHECK (length(trim(display_name)) > 0),
    CONSTRAINT ck_api_key_key_id_not_empty CHECK (length(key_id) >= 6),
    CONSTRAINT ck_api_key_key_hash_not_empty CHECK (length(key_hash) = 64)
);

-- Fail fast if the entity-standards DDL trigger did not inject created_at /
-- deleted_at (partial indexes below reference deleted_at).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_attribute
        WHERE attrelid = 'membership.api_key'::regclass
          AND attname = 'deleted_at' AND NOT attisdropped
    ) THEN
        RAISE EXCEPTION 'membership.api_key missing deleted_at — core_entity_table_standards event trigger did not fire'
            USING HINT = 'Verify lib/core/entity-standards.sql ran successfully and deployment connection has superuser.';
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS ix_api_key_org
    ON membership.api_key(organization_id)
    WHERE deleted_at IS NULL AND status != 'revoked';

CREATE INDEX IF NOT EXISTS ix_api_key_user
    ON membership.api_key(user_id)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE membership.api_key IS
    'API keys for machine-to-machine authentication. The full key is shown only at creation; only its SHA-256 hash is persisted.';

COMMENT ON COLUMN membership.api_key.key_id IS
    'Short identifier (8 alphanumeric) stored unhashed for O(1) lookup. Part of the full key: {prefix}_{key_id}_{secret}.';

COMMENT ON COLUMN membership.api_key.key_hash IS
    'SHA-256 hex of the full API key.';

COMMENT ON COLUMN membership.api_key.status IS
    'Key lifecycle: active (usable), disabled (temporarily blocked, reversible), revoked (permanent).';

COMMENT ON COLUMN membership.api_key.activated_at IS
    'When the key becomes valid. NULL = immediately active upon creation.';

COMMENT ON COLUMN membership.api_key.expires_at IS
    'When the key expires. NULL = no expiry.';

COMMENT ON COLUMN membership.api_key.last_used_at IS
    'Last successful validation timestamp. Updated on each use.';

-- ============================================================================
-- RLS Policies
-- ============================================================================

ALTER TABLE membership.api_key ENABLE ROW LEVEL SECURITY;

-- Table owner bypasses RLS, so SECURITY DEFINER functions (create_api_key,
-- revoke_api_key, etc.) work without explicit owner policies. Customer-role
-- callers only need SELECT on keys within their visible orgs — mutations go
-- through the SECURITY DEFINER functions, not direct DML.
DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    DROP POLICY IF EXISTS api_key_customer_select ON membership.api_key;

    EXECUTE format($policy$
        CREATE POLICY api_key_customer_select ON membership.api_key
            FOR SELECT TO %I
            USING (organization_id = ANY(api.current_member_org_ids()))
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
            encode(extensions.gen_random_bytes(6), 'base64') AS raw_id,
            encode(extensions.gen_random_bytes(32), 'hex') AS secret
    ),
    c_key_id AS (
        SELECT
            substring(regexp_replace(c_raw.raw_id, '[^A-Za-z0-9]', '', 'g'), 1, 8) AS key_id,
            c_raw.secret
        FROM c_raw
    ),
    c_full_key AS (
        SELECT
            c_key_id.key_id,
            membership.api_key_prefix() || '_' || c_key_id.key_id || '_' || c_key_id.secret AS full_key
        FROM c_key_id
    )
    SELECT
        c_full_key.key_id,
        c_full_key.full_key,
        encode(extensions.digest(c_full_key.full_key, 'sha256'), 'hex') AS key_hash
    FROM c_full_key;
$$;

COMMENT ON FUNCTION membership.generate_api_key_material IS
    'Generate API key components: key_id (8 alphanumeric chars), full key ({prefix}_{id}_{secret}), and SHA-256 hash.';

-- ============================================================================
-- Create API Key
-- ============================================================================
-- SECURITY DEFINER so the caller does not need direct writes on api_key or
-- user_identity. Validates membership (owner or active member) before issuing.

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
    IF p_raw_key IS NULL OR position('_' IN p_raw_key) = 0
       OR split_part(p_raw_key, '_', 1) != v_prefix THEN
        RETURN QUERY SELECT false, NULL::uuid, NULL::uuid, NULL::text, 'malformed key'::text;
        RETURN;
    END IF;

    v_parts := string_to_array(p_raw_key, '_');
    IF array_length(v_parts, 1) != 3 THEN
        RETURN QUERY SELECT false, NULL::uuid, NULL::uuid, NULL::text, 'malformed key'::text;
        RETURN;
    END IF;

    v_key_id := v_parts[2];

    SELECT c_key.* INTO v_key
    FROM membership.api_key c_key
    WHERE c_key.key_id = v_key_id
      AND c_key.deleted_at IS NULL;

    IF NOT FOUND THEN
        RETURN QUERY SELECT false, NULL::uuid, NULL::uuid, NULL::text, 'unknown key'::text;
        RETURN;
    END IF;

    v_computed_hash := encode(extensions.digest(p_raw_key, 'sha256'), 'hex');
    IF NOT membership.eq_constant_time(v_computed_hash, v_key.key_hash) THEN
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

CREATE OR REPLACE FUNCTION membership.disable_api_key(p_key_id text)
RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = membership, pg_temp
AS $$
BEGIN
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
    'Temporarily disable an API key (reversible). Raises if key is revoked.';

CREATE OR REPLACE FUNCTION membership.enable_api_key(p_key_id text)
RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = membership, pg_temp
AS $$
BEGIN
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
    'Re-enable a disabled API key. Raises if key is revoked.';

CREATE OR REPLACE FUNCTION membership.revoke_api_key(p_key_id text)
RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = membership, pg_temp
AS $$
BEGIN
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
    'Permanently revoke an API key and remove its matching user_identity row. Irreversible.';

-- ============================================================================
-- Grants
-- ============================================================================
-- upsert-style lifecycle functions are SECURITY DEFINER and execute as the
-- owner regardless of the caller. Grant EXECUTE to the API role so the
-- customer-facing REST/RPC handlers can issue and revoke keys on behalf of
-- their users; the RLS policies above scope visibility to the caller's orgs.

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    EXECUTE format('GRANT SELECT ON membership.api_key TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON membership.api_key TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON membership.api_key TO %I', v_customer_role);

    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.api_key_prefix() TO %I, %I, %I', v_admin_role, v_api_role, v_customer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.generate_api_key_material() TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.create_api_key(uuid,uuid,text,timestamptz,timestamptz) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.validate_api_key(text) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.disable_api_key(text) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.enable_api_key(text) TO %I, %I', v_admin_role, v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.revoke_api_key(text) TO %I', v_admin_role);
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ membership.api_key - key table with hashed secrets';
    RAISE NOTICE '  ✓ membership.api_key_prefix() - centralized prefix helper';
    RAISE NOTICE '  ✓ membership.generate_api_key_material() - key generation';
    RAISE NOTICE '  ✓ membership.create_api_key() - issue key + user_identity';
    RAISE NOTICE '  ✓ membership.validate_api_key() - validate for auth';
    RAISE NOTICE '  ✓ membership.disable_api_key() / enable_api_key() / revoke_api_key()';
END $$;
