-- ============================================================================
-- Test Setup: Create Alice and Bob test users
-- ============================================================================

DO $$
DECLARE
    v_alice_id UUID;
    v_bob_id UUID;
BEGIN
    RAISE DEBUG '→ Setting up membership test fixtures';

    v_alice_id := api.upsert_user('google', 'alice-001', 'alice@example.com', 'Alice', true);
    PERFORM set_config('test.alice_id', v_alice_id::TEXT, true);

    v_bob_id := api.upsert_user('github', 'bob-001', 'bob@example.com', 'Bob', true);
    PERFORM set_config('test.bob_id', v_bob_id::TEXT, true);

    RAISE DEBUG '  ✓ Alice: % (google|alice-001)', v_alice_id;
    RAISE DEBUG '  ✓ Bob: % (github|bob-001)', v_bob_id;
END $$;
