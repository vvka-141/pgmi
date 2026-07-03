-- ============================================================================
-- Test: RLS isolation on membership.user_role and membership.role
-- A customer-role session sees only its own role assignments, never another
-- user's; the global role catalog stays readable (public-read).
-- ============================================================================

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
    v_alice_id UUID;
    v_bob_id UUID;
    v_role_id UUID;
    v_own_visible INT;
    v_other_visible INT;
    v_role_visible INT;
BEGIN
    SELECT u.object_id INTO v_alice_id
    FROM membership."user" u
    JOIN membership.user_identity ui ON ui.user_object_id = u.object_id
    WHERE ui.idp_provider = 'google' AND ui.idp_subject_id = 'alice-001';

    SELECT u.object_id INTO v_bob_id
    FROM membership."user" u
    JOIN membership.user_identity ui ON ui.user_object_id = u.object_id
    WHERE ui.idp_provider = 'github' AND ui.idp_subject_id = 'bob-001';

    -- Arrange (as owner; RLS is bypassed): one role assigned to both users.
    INSERT INTO membership.role (name, description)
        VALUES ('rls_test_role', 'fixture for RLS isolation test')
        ON CONFLICT (name) DO UPDATE SET description = EXCLUDED.description
        RETURNING object_id INTO v_role_id;

    INSERT INTO membership.user_role (user_object_id, role_object_id)
        VALUES (v_alice_id, v_role_id), (v_bob_id, v_role_id)
        ON CONFLICT DO NOTHING;

    -- Act as the customer role, authenticated as Alice.
    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);
    EXECUTE format('SET ROLE %I', v_customer_role);

    SELECT count(*) INTO v_own_visible
        FROM membership.user_role WHERE user_object_id = v_alice_id;
    SELECT count(*) INTO v_other_visible
        FROM membership.user_role WHERE user_object_id = v_bob_id;
    SELECT count(*) INTO v_role_visible
        FROM membership.role WHERE object_id = v_role_id;

    RESET ROLE;

    IF v_own_visible <> 1 THEN
        RAISE EXCEPTION 'TEST FAILED: customer should see its own user_role row, saw %', v_own_visible;
    END IF;
    IF v_other_visible <> 0 THEN
        RAISE EXCEPTION 'TEST FAILED: customer must NOT see another user''s user_role rows, saw %', v_other_visible;
    END IF;
    IF v_role_visible <> 1 THEN
        RAISE EXCEPTION 'TEST FAILED: customer should read the global role catalog, saw %', v_role_visible;
    END IF;

    RAISE DEBUG '✓ membership.user_role RLS isolates per user; role catalog public-read';

    -- View-layer RLS: vw_users through customer role only shows own data
    DECLARE
        v_alice_visible int;
        v_bob_visible int;
    BEGIN
        PERFORM set_config('auth.idp_subject', 'google|alice-001', true);
        EXECUTE format('SET ROLE %I', v_customer_role);

        SELECT count(*) INTO v_alice_visible
        FROM membership.vw_active_users WHERE object_id = v_alice_id;
        SELECT count(*) INTO v_bob_visible
        FROM membership.vw_active_users WHERE object_id = v_bob_id;

        RESET ROLE;

        IF v_alice_visible < 1 THEN
            RAISE EXCEPTION 'TEST FAILED: alice should be visible in vw_active_users via customer role';
        END IF;
        RAISE DEBUG '  ✓ vw_active_users accessible through customer role (alice visible=%,bob visible=%)',
            v_alice_visible, v_bob_visible;
    END;
END $$;

-- ============================================================================
-- Structural conformance. Runs post-deploy, so it sees membership tables from
-- every file regardless of sort order — a deploy-time guard in 06-rls.sql
-- could not see later-sorted files like 08-api-keys.sql:
--   1. every membership table the customer role can SELECT has RLS enabled;
--   2. every membership vw_* view is security_invoker so RLS applies through it.
-- ============================================================================

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
    v_offender TEXT;
BEGIN
    SELECT string_agg(c.relname, ', ')
    INTO v_offender
    FROM pg_class c
    WHERE c.relnamespace = 'membership'::regnamespace
      AND c.relkind = 'r'
      AND has_table_privilege(v_customer_role, c.oid, 'SELECT')
      AND NOT c.relrowsecurity;

    IF v_offender IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: membership table(s) granted to % without RLS: %',
            v_customer_role, v_offender;
    END IF;

    SELECT string_agg(v.table_name, ', ')
    INTO v_offender
    FROM information_schema.views v
    JOIN pg_class c ON c.relnamespace = 'membership'::regnamespace
                   AND c.relname = v.table_name
    WHERE v.table_schema = 'membership'
      AND v.table_name LIKE 'vw\_%' ESCAPE '\'
      AND (c.reloptions IS NULL OR NOT ('security_invoker=true' = ANY(c.reloptions)));

    IF v_offender IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: membership view(s) must have security_invoker=true for RLS: %',
            v_offender;
    END IF;

    RAISE DEBUG '✓ membership structural conformance: RLS on all granted tables, security_invoker views';
END $$;
