-- ============================================================================
-- Unit Test: hello_world()
-- ============================================================================
-- This test validates the hello_world() function was created successfully
-- and returns a greeting message.
--
-- Note: Test files don't get template expansion (they're in pgmi_unittest_script,
-- not pgmi_source), so we test the behavior rather than exact string matching.
-- ============================================================================

DO $$
DECLARE
    v_greeting TEXT;
BEGIN
    -- Call the function and verify it returns a non-empty result
    v_greeting := public.hello_world();

    IF v_greeting IS NULL OR v_greeting = '' THEN
        RAISE EXCEPTION 'Test failed: hello_world() returned empty or null';
    END IF;

    -- Verify it contains expected greeting pattern
    IF v_greeting NOT LIKE 'Hello, %!' THEN
        RAISE EXCEPTION 'Test failed: Expected greeting pattern "Hello, %%!", got "%"',
            v_greeting;
    END IF;

    -- Report success with actual value
    RAISE NOTICE '✓ hello_world() returns correct greeting: "%"', v_greeting;
END $$;
