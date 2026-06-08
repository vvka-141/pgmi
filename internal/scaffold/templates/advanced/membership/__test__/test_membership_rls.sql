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
END $$;
