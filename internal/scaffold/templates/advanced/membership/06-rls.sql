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

DROP POLICY IF EXISTS org_member_access ON membership.organization;
CREATE POLICY org_member_access ON membership.organization
    FOR SELECT
    TO current_user
    USING (object_id = ANY(api.current_member_org_ids()));

-- ============================================================================
-- RLS on membership.organization_member
-- ============================================================================

ALTER TABLE membership.organization_member ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS org_member_see_own_org ON membership.organization_member;
CREATE POLICY org_member_see_own_org ON membership.organization_member
    FOR SELECT
    TO current_user
    USING (organization_id = ANY(api.current_member_org_ids()));

-- ============================================================================
-- RLS on membership.user_identity
-- ============================================================================

ALTER TABLE membership.user_identity ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS identity_own_only ON membership.user_identity;
CREATE POLICY identity_own_only ON membership.user_identity
    FOR SELECT
    TO current_user
    USING (user_object_id = api.current_user_id());

-- ============================================================================
-- RLS on membership."user"
-- ============================================================================

ALTER TABLE membership."user" ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS user_see_self ON membership."user";
CREATE POLICY user_see_self ON membership."user"
    FOR SELECT
    TO current_user
    USING (object_id = api.current_user_id());

-- ============================================================================
-- Customer role table grants (RLS restricts actual visibility)
-- ============================================================================

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.pgmi_get_param('database_customer_role');
BEGIN
    EXECUTE format('GRANT SELECT ON membership.organization TO %I', v_customer_role);
    EXECUTE format('GRANT SELECT ON membership.organization_member TO %I', v_customer_role);
    EXECUTE format('GRANT SELECT ON membership.user_identity TO %I', v_customer_role);
    EXECUTE format('GRANT SELECT ON membership."user" TO %I', v_customer_role);
END $$;

DO $$ BEGIN RAISE NOTICE '  ✓ membership RLS policies installed'; END $$;
