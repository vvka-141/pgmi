/*
<pgmi-meta
    id="b1000001-0006-4000-8000-000000000001"
    idempotent="true">
  <description>
    Row-Level Security policies for membership tables, targeting customer role
  </description>
  <sortKeys>
    <key>005/000/006</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing membership RLS policies'; END $$;

-- ============================================================================
-- RLS on membership.organization
-- ============================================================================

ALTER TABLE membership.organization ENABLE ROW LEVEL SECURITY;

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    DROP POLICY IF EXISTS org_member_access ON membership.organization;
    EXECUTE format($policy$
        CREATE POLICY org_member_access ON membership.organization
            FOR SELECT
            TO %I
            USING (object_id = ANY(api.current_member_org_ids()))
    $policy$, v_customer_role);
END $$;

DO $$ BEGIN RAISE DEBUG 'membership: RLS enabled on organization'; END $$;

-- ============================================================================
-- RLS on membership.organization_member
-- ============================================================================

ALTER TABLE membership.organization_member ENABLE ROW LEVEL SECURITY;

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    DROP POLICY IF EXISTS org_member_see_own_org ON membership.organization_member;
    EXECUTE format($policy$
        CREATE POLICY org_member_see_own_org ON membership.organization_member
            FOR SELECT
            TO %I
            USING (organization_id = ANY(api.current_member_org_ids()))
    $policy$, v_customer_role);
END $$;

DO $$ BEGIN RAISE DEBUG 'membership: RLS enabled on organization_member'; END $$;

-- ============================================================================
-- RLS on membership.user_identity
-- ============================================================================

ALTER TABLE membership.user_identity ENABLE ROW LEVEL SECURITY;

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    DROP POLICY IF EXISTS identity_own_only ON membership.user_identity;
    EXECUTE format($policy$
        CREATE POLICY identity_own_only ON membership.user_identity
            FOR SELECT
            TO %I
            USING (user_object_id = api.current_user_id())
    $policy$, v_customer_role);
END $$;

DO $$ BEGIN RAISE DEBUG 'membership: RLS enabled on user_identity'; END $$;

-- ============================================================================
-- RLS on membership."user"
-- ============================================================================

ALTER TABLE membership."user" ENABLE ROW LEVEL SECURITY;

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    DROP POLICY IF EXISTS user_see_self ON membership."user";
    EXECUTE format($policy$
        CREATE POLICY user_see_self ON membership."user"
            FOR SELECT
            TO %I
            USING (object_id = api.current_user_id())
    $policy$, v_customer_role);
END $$;

DO $$ BEGIN RAISE DEBUG 'membership: RLS enabled on user'; END $$;

-- ============================================================================
-- Customer role table grants (RLS restricts actual visibility)
-- ============================================================================

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    EXECUTE format('GRANT SELECT ON membership.organization TO %I', v_customer_role);
    EXECUTE format('GRANT SELECT ON membership.organization_member TO %I', v_customer_role);
    EXECUTE format('GRANT SELECT ON membership.user_identity TO %I', v_customer_role);
    EXECUTE format('GRANT SELECT ON membership."user" TO %I', v_customer_role);
END $$;

DO $$ BEGIN RAISE NOTICE '  ✓ membership RLS policies installed'; END $$;
