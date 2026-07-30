-- ============================================================================
-- Test: Identity enforcement
-- ============================================================================
-- Deactivation must end access, and an API key must reach only its own
-- organization. Every predicate here is an RLS anchor: api.current_user_id()
-- and api.current_member_org_ids() are what core.apply_org_rls hangs off, so a
-- gap in either is a gap in every org-scoped table at once.
-- ============================================================================

DO $$
DECLARE
    v_alice_id UUID := current_setting('test.alice_id')::UUID;
BEGIN
    RAISE NOTICE '→ Testing deactivating a user ends their session';

    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);

    IF api.current_user_id() IS DISTINCT FROM v_alice_id THEN
        RAISE EXCEPTION 'TEST FAILED: an active user must resolve, got %', api.current_user_id();
    END IF;

    UPDATE membership."user" SET is_active = false WHERE object_id = v_alice_id;

    -- 01-schema.sql states the invariant: "Inactive users cannot authenticate or
    -- access any organization." validate_api_key enforced it; the OIDC path did
    -- not, so deactivation did not end an existing session's access.
    IF api.current_user_id() IS NOT NULL THEN
        RAISE EXCEPTION
            'TEST FAILED: a deactivated user still resolves an identity (%), so deactivation does not end access',
            api.current_user_id();
    END IF;

    UPDATE membership."user" SET is_active = true WHERE object_id = v_alice_id;

    RAISE NOTICE '  ✓ deactivation ends identity resolution';
END $$;


DO $$
DECLARE
    v_alice_id UUID := current_setting('test.alice_id')::UUID;
    v_org_id UUID;
BEGIN
    RAISE NOTICE '→ Testing deactivating an organization removes it from the RLS anchor';

    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);

    INSERT INTO membership.organization (name, slug, owner_user_id, is_active)
    VALUES ('Anchor Probe Org', 'anchor-probe-org', v_alice_id, true)
    RETURNING object_id INTO v_org_id;

    INSERT INTO membership.organization_member (organization_id, user_id, role, status, joined_at)
    VALUES (v_org_id, v_alice_id, 'admin', 'active', now());

    IF NOT (v_org_id = ANY (coalesce(api.current_member_org_ids(), '{}'::uuid[]))) THEN
        RAISE EXCEPTION 'TEST FAILED: an active membership in an active org must appear in the anchor';
    END IF;

    UPDATE membership.organization SET is_active = false WHERE object_id = v_org_id;

    -- api.current_owner_org_ids() filtered is_active; this one did not, so a
    -- soft-deleted org vanished from the views while every domain row inside it
    -- stayed readable.
    IF v_org_id = ANY (api.current_member_org_ids()) THEN
        RAISE EXCEPTION
            'TEST FAILED: a deactivated organization is still in the RLS anchor, so its rows stay readable';
    END IF;

    RAISE NOTICE '  ✓ deactivating an organization removes it from the anchor';
END $$;


DO $$
DECLARE
    v_alice_id UUID := current_setting('test.alice_id')::UUID;
    v_org_a UUID;
    v_org_b UUID;
    v_key record;
    v_orgs UUID[];
BEGIN
    RAISE NOTICE '→ Testing an API key reaches only its own organization';

    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);

    INSERT INTO membership.organization (name, slug, owner_user_id, is_active)
    VALUES ('Key Org A', 'key-org-a', v_alice_id, true) RETURNING object_id INTO v_org_a;
    INSERT INTO membership.organization (name, slug, owner_user_id, is_active)
    VALUES ('Key Org B', 'key-org-b', v_alice_id, true) RETURNING object_id INTO v_org_b;

    INSERT INTO membership.organization_member (organization_id, user_id, role, status, joined_at)
    VALUES (v_org_a, v_alice_id, 'admin', 'active', now()),
           (v_org_b, v_alice_id, 'admin', 'active', now());

    SELECT * INTO v_key
    FROM membership.create_api_key(v_alice_id, v_org_a, 'probe key', NULL);

    -- Authenticate as the key, exactly as the gateway does.
    PERFORM set_config('auth.idp_subject', 'apikey|' || v_key.out_key_id, true);

    IF api.current_user_id() IS DISTINCT FROM v_alice_id THEN
        RAISE EXCEPTION 'TEST FAILED: an active key must resolve to its user, got %', api.current_user_id();
    END IF;

    v_orgs := api.current_member_org_ids();

    -- The key recorded organization_id at creation, but the credential the auth
    -- path consumes is the user_identity row, which carries no org -- so the key
    -- authorized every org its owner belonged to.
    IF NOT (v_org_a = ANY (coalesce(v_orgs, '{}'::uuid[]))) THEN
        RAISE EXCEPTION 'TEST FAILED: a key must reach its own organization';
    END IF;
    IF v_org_b = ANY (coalesce(v_orgs, '{}'::uuid[])) THEN
        RAISE EXCEPTION
            'TEST FAILED: a key issued for one organization also authorized another (%)', v_org_b;
    END IF;

    -- Disabling the key must end the session immediately. Only revoke deletes
    -- the identity row, so a disabled key used to resolve a full identity
    -- through the OIDC path, which never consults api_key state.
    PERFORM membership.disable_api_key(v_key.out_key_id);
    PERFORM set_config('auth.idp_subject', 'apikey|' || v_key.out_key_id, true);

    IF api.current_user_id() IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a disabled key still resolves an identity';
    END IF;

    -- A disabled key cannot re-enable itself: its own session no longer resolves
    -- an identity, so the tenant guard refuses. Re-enabling is the human's job.
    BEGIN
        PERFORM membership.enable_api_key(v_key.out_key_id);
        RAISE EXCEPTION 'TEST FAILED: a disabled key must not be able to re-enable itself';
    EXCEPTION WHEN SQLSTATE 'P0404' THEN
        NULL;
    END;

    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);
    PERFORM membership.enable_api_key(v_key.out_key_id);

    PERFORM set_config('auth.idp_subject', 'apikey|' || v_key.out_key_id, true);
    IF api.current_user_id() IS DISTINCT FROM v_alice_id THEN
        RAISE EXCEPTION 'TEST FAILED: re-enabling a key must restore its identity';
    END IF;

    -- Expiry has no lifecycle event to hook, so it has to be checked at
    -- resolution time rather than on a state transition.
    UPDATE membership.api_key SET expires_at = now() - interval '1 second'
    WHERE key_id = v_key.out_key_id;

    IF api.current_user_id() IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an expired key still resolves an identity';
    END IF;

    RAISE NOTICE '  ✓ keys are org-scoped, and disabled or expired keys resolve nothing';
END $$;


DO $$
DECLARE
    v_alice_id UUID := current_setting('test.alice_id')::UUID;
    v_bob_id UUID := current_setting('test.bob_id')::UUID;
    v_org UUID;
    v_key record;
    v_allowed boolean;
BEGIN
    RAISE NOTICE '→ Testing a plain member cannot revoke another user''s keys';

    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);

    INSERT INTO membership.organization (name, slug, owner_user_id, is_active)
    VALUES ('Manage Probe Org', 'manage-probe-org', v_alice_id, true) RETURNING object_id INTO v_org;

    INSERT INTO membership.organization_member (organization_id, user_id, role, status, joined_at)
    VALUES (v_org, v_alice_id, 'admin', 'active', now()),
           (v_org, v_bob_id, 'reader', 'active', now());

    SELECT * INTO v_key FROM membership.create_api_key(v_alice_id, v_org, 'owner key', NULL);

    -- Bob is a reader in the same org. can_manage_api_key required only active
    -- membership, so he could irreversibly revoke the owner's credential.
    PERFORM set_config('auth.idp_subject', 'github|bob-001', true);
    v_allowed := membership.can_manage_api_key(v_key.out_key_id);

    IF v_allowed IS DISTINCT FROM false THEN
        RAISE EXCEPTION
            'TEST FAILED: a plain reader may manage another user''s API key';
    END IF;

    RAISE NOTICE '  ✓ key management requires self, owner, or admin';
END $$;


DO $$
DECLARE
    v_alice_id UUID := current_setting('test.alice_id')::UUID;
    v_visible bigint;
BEGIN
    RAISE NOTICE '→ Testing vw_user_owned_organizations is scoped to the caller';

    PERFORM set_config('auth.idp_subject', 'github|bob-001', true);

    -- The view is named for ownership and filtered only on is_active, so it
    -- returned every active organization to every caller.
    -- IS DISTINCT FROM, not <>: this is a leak test, so a NULL must count as a
    -- difference. With <>, a row whose owner_user_id is NULL vanishes from the
    -- count, and — worse — if api.current_user_id() ever returned NULL every
    -- comparison would, so the test would pass while the view returned every
    -- organization in the database.
    SELECT count(*) INTO v_visible
    FROM membership.vw_user_owned_organizations
    WHERE owner_user_id IS DISTINCT FROM (SELECT api.current_user_id());

    IF v_visible > 0 THEN
        RAISE EXCEPTION
            'TEST FAILED: vw_user_owned_organizations exposes % organization(s) the caller does not own',
            v_visible;
    END IF;

    RAISE NOTICE '  ✓ the owned-organizations view shows only the caller''s own';
END $$;
