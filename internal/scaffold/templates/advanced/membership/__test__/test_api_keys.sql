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

    -- create_api_key is caller-guarded: act as Alice, who owns this org.
    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);

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

    -- create_api_key is caller-guarded: act as Alice, who owns this org.
    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);

    SELECT * INTO v_validation FROM membership.validate_api_key(NULL);
    IF v_validation.is_valid OR v_validation.reason != 'malformed key' THEN
        RAISE EXCEPTION 'NULL key should report malformed, got: %', v_validation.reason;
    END IF;

    SELECT * INTO v_validation FROM membership.validate_api_key('not-a-valid-key');
    IF v_validation.is_valid OR v_validation.reason != 'malformed key' THEN
        RAISE EXCEPTION 'Wrong prefix should report malformed, got: %', v_validation.reason;
    END IF;

    -- Structurally valid (12-hex key_id, 64-hex secret) but no such key: that is
    -- "unknown", not "malformed". A key whose segments are the wrong width never
    -- came from generate_api_key_material and is malformed — asserted below.
    SELECT * INTO v_validation FROM membership.validate_api_key(
        membership.api_key_prefix() || '_aaaaaaaaaaaa_' || repeat('b', 64)
    );
    IF v_validation.is_valid OR v_validation.reason != 'unknown key' THEN
        RAISE EXCEPTION 'Unknown key_id should report unknown key, got: %', v_validation.reason;
    END IF;

    SELECT * INTO v_validation FROM membership.validate_api_key(
        membership.api_key_prefix() || '_aaaaaaaa_notarealsecret'
    );
    IF v_validation.is_valid OR v_validation.reason != 'malformed key' THEN
        RAISE EXCEPTION 'Wrong-width segments should report malformed, got: %', v_validation.reason;
    END IF;

    -- Wrong secret, RIGHT shape: flip the last hex digit. This is the case that
    -- must reach the hash comparison — it is what an attacker guessing a secret
    -- actually sends. (Appending a character instead would change the secret's
    -- width, so the key would be rejected as malformed before any hash compare,
    -- and this test would silently stop exercising the security-critical path.)
    SELECT * INTO v_created FROM membership.create_api_key(v_alice_id, v_org_id, 'Edge case key');
    v_tampered := left(v_created.out_api_key, length(v_created.out_api_key) - 1)
        || CASE WHEN right(v_created.out_api_key, 1) = 'a' THEN 'b' ELSE 'a' END;

    SELECT * INTO v_validation FROM membership.validate_api_key(v_tampered);
    IF v_validation.is_valid OR v_validation.reason != 'invalid secret' THEN
        RAISE EXCEPTION 'Tampered secret should report invalid secret, got: %', v_validation.reason;
    END IF;

    -- A key with the wrong secret WIDTH never came from generate_api_key_material.
    SELECT * INTO v_validation FROM membership.validate_api_key(v_created.out_api_key || 'X');
    IF v_validation.is_valid OR v_validation.reason != 'malformed key' THEN
        RAISE EXCEPTION 'Over-long secret should report malformed, got: %', v_validation.reason;
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

    -- create_api_key is caller-guarded: act as Alice, who owns this org.
    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);

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
-- Test: a custom prefix — including one containing an underscore — round-trips
-- The prefix is operator-chosen and a natural one is 'acme_prod'. Splitting the
-- key on '_' made create_api_key issue keys that validate_api_key rejected as
-- malformed: auth silently and permanently broken for every key under it.
-- ============================================================================

DO $$
DECLARE
    v_alice_id uuid := current_setting('test.alice_id')::uuid;
    v_org_id uuid;
    v_created record;
    v_validation record;
    v_prefix text;
BEGIN
    RAISE DEBUG '→ Testing API key custom prefixes';

    SELECT object_id INTO STRICT v_org_id
    FROM membership.organization
    WHERE owner_user_id = v_alice_id AND is_personal = true;

    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);

    FOREACH v_prefix IN ARRAY ARRAY['pgmi', 'acme_prod', 'a_b_c_d', 'x'] LOOP
        PERFORM set_config('pgmi.api_key_prefix', v_prefix, true);

        IF membership.api_key_prefix() != v_prefix THEN
            RAISE EXCEPTION 'api_key_prefix() should read the GUC, got %', membership.api_key_prefix();
        END IF;

        SELECT * INTO v_created
        FROM membership.create_api_key(v_alice_id, v_org_id, 'prefix test ' || v_prefix);

        IF v_created.out_api_key NOT LIKE v_prefix || '\_%' ESCAPE '\' THEN
            RAISE EXCEPTION 'key should carry the configured prefix %, got %',
                v_prefix, substring(v_created.out_api_key, 1, 24);
        END IF;

        -- The whole point: a key that was issued must validate.
        SELECT * INTO v_validation FROM membership.validate_api_key(v_created.out_api_key);
        IF NOT v_validation.is_valid THEN
            RAISE EXCEPTION 'a key issued under prefix "%" must validate; got reason: %',
                v_prefix, v_validation.reason;
        END IF;
        IF v_validation.key_id != v_created.out_key_id THEN
            RAISE EXCEPTION 'prefix "%": validation resolved the wrong key_id', v_prefix;
        END IF;

        PERFORM membership.revoke_api_key(v_created.out_key_id);
    END LOOP;

    RAISE DEBUG '  ✓ keys round-trip under prefixes with and without underscores';

    -- The stricter parse must still reject genuine garbage.
    PERFORM set_config('pgmi.api_key_prefix', 'acme_prod', true);

    SELECT * INTO v_validation FROM membership.validate_api_key('acme_prod_tooshort_abc');
    IF v_validation.is_valid OR v_validation.reason != 'malformed key' THEN
        RAISE EXCEPTION 'short segments should be malformed, got %', v_validation.reason;
    END IF;

    -- A key issued under a DIFFERENT prefix must not validate under this one.
    SELECT * INTO v_created FROM membership.create_api_key(v_alice_id, v_org_id, 'other prefix');
    PERFORM set_config('pgmi.api_key_prefix', 'other', true);
    SELECT * INTO v_validation FROM membership.validate_api_key(v_created.out_api_key);
    IF v_validation.is_valid OR v_validation.reason != 'malformed key' THEN
        RAISE EXCEPTION 'a key from another prefix must not validate, got %', v_validation.reason;
    END IF;

    PERFORM set_config('pgmi.api_key_prefix', '', true);
    RAISE DEBUG '  ✓ malformed keys and foreign prefixes still rejected';
    RAISE DEBUG '✓ API key custom-prefix tests passed';
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

-- ============================================================================
-- Test: create_api_key is caller-authorized
-- create_api_key is SECURITY DEFINER, so RLS cannot confine it. Issuing a key
-- returns a working credential for the target user, so the caller — not just
-- the target — must be authorized: self-service, or an admin/owner of the org
-- provisioning for a member, or a platform superuser. A plain member must not
-- mint a peer's key, and a non-member must not mint anywhere.
-- ============================================================================

DO $$
DECLARE
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
    v_alice_id uuid := current_setting('test.alice_id')::uuid;
    v_bob_id uuid := current_setting('test.bob_id')::uuid;
    v_alice_personal uuid;
    v_team_org uuid;
    v_created record;
BEGIN
    RAISE DEBUG '→ Testing create_api_key caller authorization';

    SELECT object_id INTO STRICT v_alice_personal
    FROM membership.organization WHERE owner_user_id = v_alice_id AND is_personal = true;

    -- A shared org Alice owns; Bob joins as a plain contributor (not admin).
    INSERT INTO membership.organization (name, slug, owner_user_id, is_personal)
    VALUES ('Guard Team', 'guard-team-' || substr(gen_random_uuid()::text, 1, 8), v_alice_id, false)
    RETURNING object_id INTO v_team_org;

    INSERT INTO membership.organization_member (organization_id, user_id, role, status, joined_at)
    VALUES (v_team_org, v_bob_id, 'contributor', 'active', now());

    -- (1) Non-member: Bob does not belong to Alice's personal org. As the
    -- customer role he cannot mint a key there — neither for Alice nor for
    -- himself. Both raise P0404, indistinguishable from a missing org.
    PERFORM set_config('auth.idp_subject', 'github|bob-001', true);
    EXECUTE format('SET ROLE %I', v_customer_role);
    BEGIN
        PERFORM membership.create_api_key(v_alice_id, v_alice_personal, 'impersonate-alice');
        RESET ROLE;
        RAISE EXCEPTION 'TEST FAILED: non-member minted a key for Alice in her org';
    EXCEPTION WHEN SQLSTATE 'P0404' THEN NULL;
    END;
    BEGIN
        PERFORM membership.create_api_key(v_bob_id, v_alice_personal, 'foothold');
        RESET ROLE;
        RAISE EXCEPTION 'TEST FAILED: non-member minted a key for himself in a foreign org';
    EXCEPTION WHEN SQLSTATE 'P0404' THEN NULL;
    END;
    RESET ROLE;
    RAISE DEBUG '  ✓ non-member cannot mint keys in an org it does not belong to';

    -- (2) Plain member cannot mint a PEER's key: Bob is a contributor of the
    -- team org, but a key for Alice would impersonate her.
    PERFORM set_config('auth.idp_subject', 'github|bob-001', true);
    EXECUTE format('SET ROLE %I', v_customer_role);
    BEGIN
        PERFORM membership.create_api_key(v_alice_id, v_team_org, 'peer-impersonation');
        RESET ROLE;
        RAISE EXCEPTION 'TEST FAILED: contributor minted a peer''s key';
    EXCEPTION WHEN SQLSTATE 'P0404' THEN NULL;
    END;
    RESET ROLE;
    RAISE DEBUG '  ✓ plain member cannot mint a peer''s key';

    -- (3) Self-service: Bob may mint his OWN key in an org he belongs to.
    PERFORM set_config('auth.idp_subject', 'github|bob-001', true);
    EXECUTE format('SET ROLE %I', v_customer_role);
    SELECT * INTO v_created FROM membership.create_api_key(v_bob_id, v_team_org, 'bob self-service');
    RESET ROLE;
    IF v_created.out_key_id IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: self-service create returned no key';
    END IF;
    RAISE DEBUG '  ✓ member can mint its own key (self-service)';

    -- (4) Owner provisioning: Alice owns the team org and may mint a key for
    -- Bob, a member of it.
    PERFORM set_config('auth.idp_subject', 'google|alice-001', true);
    EXECUTE format('SET ROLE %I', v_customer_role);
    SELECT * INTO v_created FROM membership.create_api_key(v_bob_id, v_team_org, 'alice provisions bob');
    RESET ROLE;
    IF v_created.out_key_id IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: org owner could not provision a member''s key';
    END IF;
    RAISE DEBUG '  ✓ org owner can provision a member''s key';

    -- (5) Admin-role provisioning: promote Bob to admin; he may now mint a key
    -- for Alice, who is a member (owner) of the team org.
    UPDATE membership.organization_member SET role = 'admin'
    WHERE organization_id = v_team_org AND user_id = v_bob_id;

    PERFORM set_config('auth.idp_subject', 'github|bob-001', true);
    EXECUTE format('SET ROLE %I', v_customer_role);
    SELECT * INTO v_created FROM membership.create_api_key(v_alice_id, v_team_org, 'admin bob provisions alice');
    RESET ROLE;
    IF v_created.out_key_id IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: org admin could not provision a member''s key';
    END IF;
    RAISE DEBUG '  ✓ org admin can provision a member''s key';

    RAISE DEBUG '✓ create_api_key caller-authorization tests passed';
END $$;
