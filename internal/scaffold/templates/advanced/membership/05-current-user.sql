/*
<pgmi-meta
    id="b1000001-0005-4000-8000-000000000001"
    idempotent="true">
  <description>
    Session-based current user context: reads auth.idp_subject session variable,
    provides current user functions for RLS evaluation
  </description>
  <sortKeys>
    <key>005/000/005</key>
  </sortKeys>
</pgmi-meta>
*/

-- ============================================================================
-- IdP Subject Parsing
-- ============================================================================

CREATE OR REPLACE FUNCTION api.current_idp_subject()
RETURNS TEXT
LANGUAGE sql STABLE PARALLEL SAFE
AS $$
    SELECT NULLIF(current_setting('auth.idp_subject', true), '');
$$;

COMMENT ON FUNCTION api.current_idp_subject() IS
    'Reads the auth.idp_subject session variable set by the gateway. Returns NULL when unauthenticated.';

CREATE OR REPLACE FUNCTION api.parse_idp_provider(p_subject TEXT)
RETURNS TEXT
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
    SELECT split_part(p_subject, '|', 1);
$$;

COMMENT ON FUNCTION api.parse_idp_provider(TEXT) IS
    'Extracts the provider name from a provider|subject_id string.';

CREATE OR REPLACE FUNCTION api.parse_idp_subject_id(p_subject TEXT)
RETURNS TEXT
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
    SELECT split_part(p_subject, '|', 2);
$$;

COMMENT ON FUNCTION api.parse_idp_subject_id(TEXT) IS
    'Extracts the subject ID from a provider|subject_id string.';

-- Inline tests for pure functions
DO $$
BEGIN
    IF api.parse_idp_provider('google|12345') != 'google' THEN
        RAISE EXCEPTION 'TEST FAILED: parse_idp_provider';
    END IF;
    IF api.parse_idp_subject_id('google|12345') != '12345' THEN
        RAISE EXCEPTION 'TEST FAILED: parse_idp_subject_id';
    END IF;
END $$;

-- ============================================================================
-- Current User Resolution
-- ============================================================================

CREATE OR REPLACE FUNCTION api.current_user_id()
RETURNS UUID
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, api, pg_temp
AS $$
    SELECT u.object_id
    FROM membership.user_identity ui
    JOIN membership."user" u ON u.object_id = ui.user_object_id
    WHERE ui.idp_provider = api.parse_idp_provider(api.current_idp_subject())
      AND ui.idp_subject_id = api.parse_idp_subject_id(api.current_idp_subject());
$$;

COMMENT ON FUNCTION api.current_user_id() IS
    'Resolves the session identity to a membership.user UUID. SECURITY DEFINER — used as RLS anchor.';

CREATE OR REPLACE FUNCTION api.is_authenticated()
RETURNS BOOLEAN
LANGUAGE sql STABLE
AS $$
    SELECT api.current_user_id() IS NOT NULL;
$$;

COMMENT ON FUNCTION api.is_authenticated() IS
    'Returns true if the session has a valid identity resolved to a user.';

CREATE OR REPLACE FUNCTION api.current_member_org_ids()
RETURNS UUID[]
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, api, pg_temp
AS $$
    SELECT COALESCE(array_agg(om.organization_id), '{}')
    FROM membership.organization_member om
    WHERE om.user_id = api.current_user_id() AND om.status = 'active';
$$;

COMMENT ON FUNCTION api.current_member_org_ids() IS
    'Returns org IDs where the current user is an active member. Primary RLS anchor for org-scoped policies.';

CREATE OR REPLACE FUNCTION api.current_owner_org_ids()
RETURNS UUID[]
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = membership, api, pg_temp
AS $$
    SELECT COALESCE(array_agg(o.object_id), '{}')
    FROM membership.organization o
    WHERE o.owner_user_id = api.current_user_id() AND o.is_active = true;
$$;

COMMENT ON FUNCTION api.current_owner_org_ids() IS
    'Returns org IDs owned by the current user. Used for owner-level access checks.';

-- ============================================================================
-- Current User View
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_current_user AS
SELECT
    u.object_id AS user_id,
    u.email,
    u.display_name,
    u.email_verified,
    api.current_member_org_ids() AS member_org_ids,
    api.current_owner_org_ids() AS owner_org_ids
FROM membership."user" u
WHERE u.object_id = api.current_user_id();

COMMENT ON VIEW api.vw_current_user IS
    'Current session user profile with org memberships. Reads the auth.idp_subject session variable.';

-- ============================================================================
-- Performance Indexes for RLS
-- ============================================================================

CREATE INDEX IF NOT EXISTS ix_user_identity_provider_subject
    ON membership.user_identity(idp_provider, idp_subject_id);

CREATE INDEX IF NOT EXISTS ix_org_member_user_status
    ON membership.organization_member(user_id, status)
    WHERE status = 'active';

-- ============================================================================
-- Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.vw_current_user TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_current_user TO %I', v_customer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.current_idp_subject() TO %I', v_customer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.current_user_id() TO %I', v_customer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.is_authenticated() TO %I', v_customer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.current_member_org_ids() TO %I', v_customer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.current_owner_org_ids() TO %I', v_customer_role);
END $$;

DO $$ BEGIN RAISE NOTICE '  ✓ current user context installed'; END $$;
