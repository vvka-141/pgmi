-- ============================================================================
-- Test: Soft-delete filtering through views
-- ============================================================================

DO $$
DECLARE
    v_user_id uuid;
    v_org_id uuid;
BEGIN
    RAISE NOTICE '-> Testing soft-delete view filtering';

    -- ========================================================================
    -- Setup: create a test user + org for soft-delete testing
    -- ========================================================================

    v_user_id := membership.upsert_user('softdel', 'sd-001', 'softdel@example.com', 'SoftDel User', true);

    SELECT object_id INTO STRICT v_org_id
    FROM membership.organization
    WHERE owner_user_id = v_user_id AND is_personal = true;

    -- ========================================================================
    -- Active user visible in vw_active_users
    -- ========================================================================

    IF NOT EXISTS (
        SELECT 1 FROM membership.vw_active_users WHERE object_id = v_user_id
    ) THEN
        RAISE EXCEPTION 'Active user should be in vw_active_users';
    END IF;

    RAISE NOTICE '  + Active user visible in vw_active_users';

    -- ========================================================================
    -- Deactivate user -> invisible in vw_active_users, still in raw table
    -- ========================================================================

    UPDATE membership."user" SET is_active = false WHERE object_id = v_user_id;

    IF EXISTS (
        SELECT 1 FROM membership.vw_active_users WHERE object_id = v_user_id
    ) THEN
        RAISE EXCEPTION 'Deactivated user should NOT be in vw_active_users';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM membership."user" WHERE object_id = v_user_id
    ) THEN
        RAISE EXCEPTION 'Deactivated user should still exist in raw table';
    END IF;

    RAISE NOTICE '  + Deactivated user filtered from vw_active_users, still in raw table';

    UPDATE membership."user" SET is_active = true WHERE object_id = v_user_id;

    -- ========================================================================
    -- Active org visible in vw_active_organizations
    -- ========================================================================

    IF NOT EXISTS (
        SELECT 1 FROM membership.vw_active_organizations WHERE object_id = v_org_id
    ) THEN
        RAISE EXCEPTION 'Active org should be in vw_active_organizations';
    END IF;

    RAISE NOTICE '  + Active org visible in vw_active_organizations';

    -- ========================================================================
    -- Deactivate org -> invisible in vw_active_organizations
    -- ========================================================================

    UPDATE membership.organization SET is_active = false WHERE object_id = v_org_id;

    IF EXISTS (
        SELECT 1 FROM membership.vw_active_organizations WHERE object_id = v_org_id
    ) THEN
        RAISE EXCEPTION 'Deactivated org should NOT be in vw_active_organizations';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM membership.organization WHERE object_id = v_org_id
    ) THEN
        RAISE EXCEPTION 'Deactivated org should still exist in raw table';
    END IF;

    RAISE NOTICE '  + Deactivated org filtered from vw_active_organizations, still in raw table';

    -- ========================================================================
    -- Deactivated org cascades to memberships view
    -- ========================================================================

    IF EXISTS (
        SELECT 1 FROM membership.vw_active_memberships
        WHERE user_id = v_user_id AND organization_id = v_org_id
    ) THEN
        RAISE EXCEPTION 'Membership of deactivated org should NOT appear in vw_active_memberships';
    END IF;

    RAISE NOTICE '  + Deactivated org membership filtered from vw_active_memberships';

    UPDATE membership.organization SET is_active = true WHERE object_id = v_org_id;

    RAISE NOTICE '+ Soft-delete view filtering tests passed';
END $$;
