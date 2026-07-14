-- ============================================================================
-- Test: API key lifecycle (create, validate, disable/enable, revoke)
-- ============================================================================

DO $$
DECLARE
    v_alice_id uuid := current_setting('test.alice_id')::uuid;
    v_org_id uuid;
    v_created record;
    v_validation record;
    v_resolved_user_id uuid;
BEGIN
    RAISE DEBUG '→ Testing API key lifecycle';

    SELECT object_id INTO STRICT v_org_id
    FROM membership.organization
    WHERE owner_user_id = v_alice_id AND is_personal = true;

    -- ========================================================================
    -- create_api_key returns valid material + creates user_identity
    -- ========================================================================

    SELECT * INTO v_created
    FROM membership.create_api_key(v_alice_id, v_org_id, 'Alice test key');

    IF v_created.out_api_key NOT LIKE (membership.api_key_prefix() || '\_%') ESCAPE '\' THEN
        RAISE EXCEPTION 'Key prefix mismatch: %', substring(v_created.out_api_key, 1, 16);
    END IF;
    IF length(v_created.out_key_id) < 6 THEN
        RAISE EXCEPTION 'key_id too short: %', length(v_created.out_key_id);
    END IF;
    IF v_created.out_object_id IS NULL THEN
        RAISE EXCEPTION 'object_id not returned';
    END IF;
    IF position(v_created.out_key_id IN v_created.out_api_key) = 0 THEN
        RAISE EXCEPTION 'key_id not present in full key';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM membership.user_identity
        WHERE idp_provider = 'apikey'
          AND idp_subject_id = v_created.out_key_id
          AND user_object_id = v_alice_id
    ) THEN
        RAISE EXCEPTION 'user_identity row not created for apikey provider';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM membership.api_key
        WHERE key_id = v_created.out_key_id
          AND organization_id = v_org_id
          AND user_id = v_alice_id
          AND status = 'active'
          AND display_name = 'Alice test key'
    ) THEN
        RAISE EXCEPTION 'api_key row not inserted with expected attributes';
    END IF;

    RAISE DEBUG '  ✓ Key created and user_identity registered';

    -- ========================================================================
    -- validate_api_key succeeds for active key and updates last_used_at
    -- ========================================================================

    SELECT * INTO v_validation FROM membership.validate_api_key(v_created.out_api_key);

    IF NOT v_validation.is_valid THEN
        RAISE EXCEPTION 'Active key should validate; reason: %', v_validation.reason;
    END IF;
    IF v_validation.user_id != v_alice_id
       OR v_validation.organization_id != v_org_id
       OR v_validation.key_id != v_created.out_key_id THEN
        RAISE EXCEPTION 'Validation returned wrong context';
    END IF;
    IF v_validation.reason IS NOT NULL THEN
        RAISE EXCEPTION 'reason should be NULL for valid key';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM membership.api_key
        WHERE key_id = v_created.out_key_id AND last_used_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'last_used_at not updated on successful validation';
    END IF;

    RAISE DEBUG '  ✓ Validation succeeds and updates last_used_at';

    -- ========================================================================
    -- Auth pipeline resolves the apikey identity to the owning user
    -- ========================================================================

    PERFORM set_config('auth.idp_subject', 'apikey|' || v_created.out_key_id, true);
    v_resolved_user_id := api.current_user_id();

    IF v_resolved_user_id IS NULL OR v_resolved_user_id != v_alice_id THEN
        RAISE EXCEPTION 'api.current_user_id() should resolve apikey identity to alice; got: %', v_resolved_user_id;
    END IF;

    RAISE DEBUG '  ✓ auth.idp_subject=apikey|% resolves to alice', v_created.out_key_id;

    -- View-layer: vw_user_claims includes the apikey identity
    DECLARE
        v_claims_identities jsonb;
        v_has_apikey boolean;
    BEGIN
        SELECT identities INTO v_claims_identities
        FROM membership.vw_user_claims WHERE user_id = v_alice_id;

        SELECT EXISTS (
            SELECT 1 FROM jsonb_array_elements(v_claims_identities) elem
            WHERE elem->>'provider' = 'apikey' AND elem->>'subject_id' = v_created.out_key_id
        ) INTO v_has_apikey;

        IF NOT v_has_apikey THEN
            RAISE EXCEPTION 'TEST FAILED: vw_user_claims should include apikey identity';
        END IF;
        RAISE DEBUG '  ✓ vw_user_claims includes apikey|% identity', v_created.out_key_id;
    END;

    -- View-layer: vw_current_user resolves from apikey identity
    DECLARE
        v_current record;
    BEGIN
        SELECT * INTO v_current FROM api.vw_current_user;
        IF v_current.user_id != v_alice_id THEN
            RAISE EXCEPTION 'TEST FAILED: vw_current_user should resolve to alice via apikey identity';
        END IF;
        IF v_current.email != 'alice@example.com' THEN
            RAISE EXCEPTION 'TEST FAILED: vw_current_user email mismatch';
        END IF;
        RAISE DEBUG '  ✓ vw_current_user resolves alice via apikey identity';
    END;

    -- The lifecycle functions are tenant-guarded, so they need a resolvable
    -- identity: act as Alice, an active member of the key's organization.
    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);

    -- ========================================================================
    -- Disable → validate fails → enable → validate succeeds
    -- ========================================================================

    PERFORM membership.disable_api_key(v_created.out_key_id);

    SELECT * INTO v_validation FROM membership.validate_api_key(v_created.out_api_key);
    IF v_validation.is_valid THEN
        RAISE EXCEPTION 'Disabled key should not validate';
    END IF;
    IF v_validation.reason != 'key is disabled' THEN
        RAISE EXCEPTION 'Expected "key is disabled", got: %', v_validation.reason;
    END IF;

    PERFORM membership.enable_api_key(v_created.out_key_id);

    SELECT * INTO v_validation FROM membership.validate_api_key(v_created.out_api_key);
    IF NOT v_validation.is_valid THEN
        RAISE EXCEPTION 'Re-enabled key should validate; reason: %', v_validation.reason;
    END IF;

    RAISE DEBUG '  ✓ disable/enable lifecycle works';

    -- ========================================================================
    -- Revoke permanently invalidates + removes identity + blocks re-enable
    -- ========================================================================

    PERFORM membership.revoke_api_key(v_created.out_key_id);

    SELECT * INTO v_validation FROM membership.validate_api_key(v_created.out_api_key);
    IF v_validation.is_valid THEN
        RAISE EXCEPTION 'Revoked key should not validate';
    END IF;
    IF v_validation.reason != 'key is revoked' THEN
        RAISE EXCEPTION 'Expected "key is revoked", got: %', v_validation.reason;
    END IF;

    IF EXISTS (
        SELECT 1 FROM membership.user_identity
        WHERE idp_provider = 'apikey' AND idp_subject_id = v_created.out_key_id
    ) THEN
        RAISE EXCEPTION 'user_identity should be removed after revoke';
    END IF;

    BEGIN
        PERFORM membership.enable_api_key(v_created.out_key_id);
        RAISE EXCEPTION 'enable_api_key should raise for revoked key';
    EXCEPTION WHEN SQLSTATE 'P0409' THEN
        NULL;
    END;

    RAISE DEBUG '  ✓ revoke is permanent and removes identity';

    RAISE DEBUG '✓ API key lifecycle tests passed';
END $$;

-- ============================================================================
-- Test: validate_api_key rejects malformed, unknown, and wrong-secret inputs
-- ============================================================================

DO $$
DECLARE
    v_alice_id uuid := current_setting('test.alice_id')::uuid;
    v_org_id uuid;
    v_created record;
    v_validation record;
    v_tampered text;
BEGIN
    RAISE DEBUG '→ Testing API key validation edge cases';

    SELECT object_id INTO STRICT v_org_id
    FROM membership.organization
    WHERE owner_user_id = v_alice_id AND is_personal = true;

    SELECT * INTO v_validation FROM membership.validate_api_key(NULL);
    IF v_validation.is_valid OR v_validation.reason != 'malformed key' THEN
        RAISE EXCEPTION 'NULL key should report malformed, got: %', v_validation.reason;
    END IF;

    SELECT * INTO v_validation FROM membership.validate_api_key('not-a-valid-key');
    IF v_validation.is_valid OR v_validation.reason != 'malformed key' THEN
        RAISE EXCEPTION 'Wrong prefix should report malformed, got: %', v_validation.reason;
    END IF;

    SELECT * INTO v_validation FROM membership.validate_api_key(
        membership.api_key_prefix() || '_aaaaaaaa_notarealsecret'
    );
    IF v_validation.is_valid OR v_validation.reason != 'unknown key' THEN
        RAISE EXCEPTION 'Unknown key_id should report unknown key, got: %', v_validation.reason;
    END IF;

    -- Wrong secret: generate a real key, then append garbage to its secret
    SELECT * INTO v_created FROM membership.create_api_key(v_alice_id, v_org_id, 'Edge case key');
    v_tampered := v_created.out_api_key || 'X';

    SELECT * INTO v_validation FROM membership.validate_api_key(v_tampered);
    IF v_validation.is_valid OR v_validation.reason != 'invalid secret' THEN
        RAISE EXCEPTION 'Tampered secret should report invalid secret, got: %', v_validation.reason;
    END IF;

    RAISE DEBUG '  ✓ validate rejects NULL/malformed/unknown/tampered keys';

    RAISE DEBUG '✓ API key validation edge-case tests passed';
END $$;

-- ============================================================================
-- Test: expired and not-yet-active keys are rejected
-- ============================================================================

DO $$
DECLARE
    v_alice_id uuid := current_setting('test.alice_id')::uuid;
    v_org_id uuid;
    v_created_expired record;
    v_created_future record;
    v_validation record;
BEGIN
    RAISE DEBUG '→ Testing API key expiry and activation windows';

    SELECT object_id INTO STRICT v_org_id
    FROM membership.organization
    WHERE owner_user_id = v_alice_id AND is_personal = true;

    SELECT * INTO v_created_expired
    FROM membership.create_api_key(v_alice_id, v_org_id, 'Already expired', now() - interval '1 minute');

    SELECT * INTO v_validation FROM membership.validate_api_key(v_created_expired.out_api_key);
    IF v_validation.is_valid OR v_validation.reason != 'key expired' THEN
        RAISE EXCEPTION 'Expired key should report key expired, got: %', v_validation.reason;
    END IF;

    SELECT * INTO v_created_future
    FROM membership.create_api_key(v_alice_id, v_org_id, 'Not yet active', NULL, now() + interval '1 hour');

    SELECT * INTO v_validation FROM membership.validate_api_key(v_created_future.out_api_key);
    IF v_validation.is_valid OR v_validation.reason != 'key not yet active' THEN
        RAISE EXCEPTION 'Pre-activation key should report not yet active, got: %', v_validation.reason;
    END IF;

    RAISE DEBUG '  ✓ expired and pre-activation keys rejected';

    RAISE DEBUG '✓ API key expiry tests passed';
END $$;

-- ============================================================================
-- Test: validate_api_key rejects keys belonging to inactive user/org
-- ============================================================================

DO $$
DECLARE
    v_alice_id uuid := current_setting('test.alice_id')::uuid;
    v_org_id uuid;
    v_created record;
    v_validation record;
BEGIN
    RAISE DEBUG '→ Testing API key inactive-principal rejection';

    SELECT object_id INTO STRICT v_org_id
    FROM membership.organization
    WHERE owner_user_id = v_alice_id AND is_personal = true;

    -- revoke_api_key is tenant-guarded: act as Alice, who owns these keys.
    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);

    -- Inactive organization → key rejected with 'organization is inactive'
    SELECT * INTO v_created FROM membership.create_api_key(v_alice_id, v_org_id, 'inactive-org-key');

    UPDATE membership.organization SET is_active = false WHERE object_id = v_org_id;

    SELECT * INTO v_validation FROM membership.validate_api_key(v_created.out_api_key);
    IF v_validation.is_valid THEN
        RAISE EXCEPTION 'Key for inactive organization should not validate';
    END IF;
    IF v_validation.reason != 'organization is inactive' THEN
        RAISE EXCEPTION 'Expected "organization is inactive", got: %', v_validation.reason;
    END IF;

    UPDATE membership.organization SET is_active = true WHERE object_id = v_org_id;
    PERFORM membership.revoke_api_key(v_created.out_key_id);

    RAISE DEBUG '  ✓ inactive organization → key rejected';

    -- Inactive user → key rejected with 'user is inactive'
    SELECT * INTO v_created FROM membership.create_api_key(v_alice_id, v_org_id, 'inactive-user-key');

    UPDATE membership."user" SET is_active = false WHERE object_id = v_alice_id;

    SELECT * INTO v_validation FROM membership.validate_api_key(v_created.out_api_key);
    IF v_validation.is_valid THEN
        RAISE EXCEPTION 'Key for inactive user should not validate';
    END IF;
    IF v_validation.reason != 'user is inactive' THEN
        RAISE EXCEPTION 'Expected "user is inactive", got: %', v_validation.reason;
    END IF;

    UPDATE membership."user" SET is_active = true WHERE object_id = v_alice_id;
    PERFORM membership.revoke_api_key(v_created.out_key_id);

    RAISE DEBUG '  ✓ inactive user → key rejected';

    RAISE DEBUG '✓ API key inactive-principal tests passed';
END $$;

-- ============================================================================
-- Test: lifecycle functions are tenant-scoped
-- They are SECURITY DEFINER, so RLS cannot confine them. Bob — an authenticated
-- customer session with no membership in Alice's org — must not be able to
-- disable, enable, or revoke Alice's key, and must not learn that it exists.
-- ============================================================================

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
    v_alice_id uuid := current_setting('test.alice_id')::uuid;
    v_org_id uuid;
    v_created record;
    v_fn text;
    v_status membership.api_key_status;
BEGIN
    RAISE DEBUG '→ Testing API key lifecycle tenant isolation';

    SELECT object_id INTO STRICT v_org_id
    FROM membership.organization
    WHERE owner_user_id = v_alice_id AND is_personal = true;

    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);
    SELECT * INTO v_created FROM membership.create_api_key(v_alice_id, v_org_id, 'Tenant guard key');

    -- Act as Bob through the customer role: every lifecycle call must 404.
    PERFORM set_config('auth.idp_subject', 'github|bob-001', true);
    EXECUTE format('SET ROLE %I', v_customer_role);

    FOREACH v_fn IN ARRAY ARRAY['disable_api_key', 'enable_api_key', 'revoke_api_key'] LOOP
        BEGIN
            EXECUTE format('SELECT membership.%I($1)', v_fn) USING v_created.out_key_id;
            RESET ROLE;
            RAISE EXCEPTION 'TEST FAILED: membership.% mutated a key outside the caller''s organizations', v_fn;
        EXCEPTION WHEN SQLSTATE 'P0404' THEN
            NULL;
        END;
    END LOOP;

    RESET ROLE;

    SELECT status INTO STRICT v_status
    FROM membership.api_key WHERE key_id = v_created.out_key_id;

    IF v_status != 'active' THEN
        RAISE EXCEPTION 'TEST FAILED: cross-tenant call changed key status to %', v_status;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM membership.user_identity
        WHERE idp_provider = 'apikey' AND idp_subject_id = v_created.out_key_id
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: cross-tenant revoke deleted the key identity';
    END IF;

    RAISE DEBUG '  ✓ non-member customer session cannot disable/enable/revoke another org''s key';

    -- Positive control: Alice, an active member, can manage her own key.
    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);
    EXECUTE format('SET ROLE %I', v_customer_role);
    PERFORM membership.disable_api_key(v_created.out_key_id);
    RESET ROLE;

    SELECT status INTO STRICT v_status
    FROM membership.api_key WHERE key_id = v_created.out_key_id;

    IF v_status != 'disabled' THEN
        RAISE EXCEPTION 'TEST FAILED: org member should be able to disable own key, status is %', v_status;
    END IF;

    RAISE DEBUG '  ✓ member customer session manages its own org''s key';

    RAISE DEBUG '✓ API key tenant-isolation tests passed';
END $$;
