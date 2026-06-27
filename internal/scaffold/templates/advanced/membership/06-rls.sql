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
            USING (user_object_id = api.current_user_id())
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

-- Regression guard: every membership table the customer role can SELECT must
-- have RLS enabled, so a future table added to the blanket grant cannot leak.
DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
    v_unprotected TEXT;
BEGIN
    SELECT string_agg(c.relname, ', ')
    INTO v_unprotected
    FROM pg_class c
    WHERE c.relnamespace = 'membership'::regnamespace
      AND c.relkind = 'r'
      AND has_table_privilege(v_customer_role, c.oid, 'SELECT')
      AND NOT c.relrowsecurity;

    IF v_unprotected IS NOT NULL THEN
        RAISE EXCEPTION 'membership table(s) granted to % without RLS: %', v_customer_role, v_unprotected;
    END IF;
END $$;

DO $$
DECLARE
    v_unprotected text;
BEGIN
    SELECT string_agg(v.table_name, ', ')
    INTO v_unprotected
    FROM information_schema.views v
    JOIN pg_class c ON c.relnamespace = 'membership'::regnamespace
                   AND c.relname = v.table_name
    WHERE v.table_schema = 'membership'
      AND v.table_name LIKE 'vw\_%' ESCAPE '\'
      AND (c.reloptions IS NULL OR NOT ('security_invoker=true' = ANY(c.reloptions)));

    IF v_unprotected IS NOT NULL THEN
        RAISE EXCEPTION 'membership view(s) must have security_invoker=true for RLS: %', v_unprotected;
    END IF;
END $$;

DO $$ BEGIN RAISE NOTICE '  ✓ membership RLS policies installed'; END $$;
