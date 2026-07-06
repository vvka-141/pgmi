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
            USING (object_id IN (SELECT unnest(api.current_member_org_ids())))
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
            USING (organization_id IN (SELECT unnest(api.current_member_org_ids())))
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
            USING (user_object_id = (SELECT api.current_user_id()))
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
            USING (object_id = (SELECT api.current_user_id()))
    $policy$, v_customer_role);
END $$;

DO $$ BEGIN RAISE DEBUG 'membership: RLS enabled on user'; END $$;

-- ============================================================================
-- RLS on membership.user_role
-- ============================================================================
-- A customer may see only their own role assignments.

ALTER TABLE membership.user_role ENABLE ROW LEVEL SECURITY;

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    DROP POLICY IF EXISTS user_role_own_only ON membership.user_role;
    EXECUTE format($policy$
        CREATE POLICY user_role_own_only ON membership.user_role
            FOR SELECT
            TO %I
            USING (user_object_id = (SELECT api.current_user_id()))
    $policy$, v_customer_role);
END $$;

DO $$ BEGIN RAISE DEBUG 'membership: RLS enabled on user_role'; END $$;

-- ============================================================================
-- RLS on membership.role
-- ============================================================================
-- Decision: roles are a global, non-tenant catalog (object_id, name,
-- description) with no per-user/per-tenant data — public-read to the customer
-- role. RLS is still enabled so the table is not left uncovered.

ALTER TABLE membership.role ENABLE ROW LEVEL SECURITY;

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    DROP POLICY IF EXISTS role_public_read ON membership.role;
    EXECUTE format($policy$
        CREATE POLICY role_public_read ON membership.role
            FOR SELECT
            TO %I
            USING (true)
    $policy$, v_customer_role);
END $$;

DO $$ BEGIN RAISE DEBUG 'membership: RLS enabled on role'; END $$;

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
    EXECUTE format('GRANT SELECT ON membership.user_role TO %I', v_customer_role);
    EXECUTE format('GRANT SELECT ON membership.role TO %I', v_customer_role);
END $$;

-- Structural conformance (every granted table has RLS; every vw_* view is
-- security_invoker) is asserted post-deploy in
-- __test__/test_membership_rls.sql — a guard here would run at this file's
-- sort position and miss membership tables created by later files
-- (e.g. 08-api-keys.sql).

DO $$ BEGIN RAISE NOTICE '  ✓ membership RLS policies installed'; END $$;
