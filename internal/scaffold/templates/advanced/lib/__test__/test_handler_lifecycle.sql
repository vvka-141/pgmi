-- ============================================================================
-- Test: Handler Lifecycle and Content Negotiation
-- ============================================================================
-- Validates the full handler round-trip with transactional rollback:
--   • Handler registration
--   • Request invocation
--   • Queue logging verification
--   • Content negotiation (Accept header enforcement)
-- ============================================================================

DO $$
DECLARE
    v_handler_id uuid := 'ffffffff-0001-4000-8000-000000000001';
    v_response api.http_response;
    v_queue_count int;
BEGIN
    RAISE NOTICE '-> Testing Handler Lifecycle';

    -- ========================================================================
    -- Test: Register handler, invoke, verify queue logging
    -- ========================================================================

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_handler_id,
            'uri', '^/test-lifecycle(\?.*)?$',
            'httpMethod', '^GET$',
            'name', 'test_lifecycle',
            'description', 'Lifecycle test handler',
            'autoLog', true,
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('lifecycle', 'ok'));
END;
        $body$
    );

    RAISE NOTICE '  + Handler registered';

    -- Invoke handler
    v_response := api.rest_invoke('GET', '/test-lifecycle', ''::extensions.hstore, NULL::bytea);

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'Handler invocation failed: %', (v_response).status_code;
    END IF;

    RAISE NOTICE '  + Handler invoked successfully';

    -- Verify queue logging
    SELECT COUNT(*) INTO v_queue_count
    FROM api.inbound_queue
    WHERE handler_object_id = v_handler_id;

    IF v_queue_count != 1 THEN
        RAISE EXCEPTION 'Expected 1 queue entry, got %', v_queue_count;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM api.rest_exchange
        WHERE handler_object_id = v_handler_id
    ) THEN
        RAISE EXCEPTION 'REST exchange not logged';
    END IF;

    RAISE NOTICE '  + Queue logging verified';

    RAISE NOTICE '+ Handler Lifecycle tests passed';
END $$;

DO $$
DECLARE
    v_handler_id uuid := 'ffffffff-0002-4000-8000-000000000001';
    v_response api.http_response;
BEGIN
    RAISE NOTICE '-> Testing Content Negotiation';

    -- Register a handler for content negotiation tests
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_handler_id,
            'uri', '^/test-content-nego(\?.*)?$',
            'httpMethod', '^GET$',
            'name', 'test_content_nego',
            'description', 'Content negotiation test handler',
            'autoLog', false,
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('nego', 'ok'));
END;
        $body$
    );

    -- ========================================================================
    -- Test: Accept: application/json -> 200 (matches default produces)
    -- ========================================================================

    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        'accept=>application/json'::extensions.hstore,
        NULL::bytea
    );
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'Accept: application/json should return 200, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  + Accept: application/json -> 200';

    -- ========================================================================
    -- Test: Accept: */* -> 200 (wildcard)
    -- ========================================================================

    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        'accept=>*/*'::extensions.hstore,
        NULL::bytea
    );
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'Accept: */* should return 200, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  + Accept: */* -> 200';

    -- ========================================================================
    -- Test: Accept: application/xml -> 406 (not supported)
    -- ========================================================================

    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        'accept=>application/xml'::extensions.hstore,
        NULL::bytea
    );
    IF (v_response).status_code != 406 THEN
        RAISE EXCEPTION 'Accept: application/xml should return 406, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  + Accept: application/xml -> 406 Not Acceptable';

    -- ========================================================================
    -- Test: No Accept header -> 200 (no preference)
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/test-content-nego', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'No Accept header should return 200, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  + No Accept header -> 200';

    -- ========================================================================
    -- Test: Accept with multiple types including json -> 200
    -- ========================================================================

    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        '"accept"=>"text/html, application/json, text/plain"'::extensions.hstore,
        NULL::bytea
    );
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'Accept with json in list should return 200, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  + Accept: text/html, application/json, text/plain -> 200';

    RAISE NOTICE '+ Content Negotiation tests passed';
END $$;

DO $$
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE '===============================================================';
    RAISE NOTICE '+ ALL HANDLER LIFECYCLE AND CONTENT NEGOTIATION TESTS PASSED';
    RAISE NOTICE '===============================================================';
END $$;
