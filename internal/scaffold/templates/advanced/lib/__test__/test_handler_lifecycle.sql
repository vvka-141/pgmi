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

    RAISE NOTICE '  + Handler invoked';

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
    -- Test: handler.accepts enforcement -> 415 on unsupported request body type
    -- ========================================================================

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'dddddddd-0050-4000-8000-000000000001',
            'uri', '^/test-accepts$',
            'httpMethod', '^POST$',
            'name', 'test_accepts',
            'description', 'Handler that only accepts application/json bodies',
            'autoLog', false,
            'requiresAuth', false,
            'accepts', jsonb_build_array('application/json')
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('ok', true));
END;
        $body$
    );

    v_response := api.rest_invoke(
        'POST', '/test-accepts',
        extensions.hstore('content-type', 'application/json; charset=utf-8'),
        convert_to('{}', 'UTF8')
    );
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'Content-Type application/json should be accepted (200), got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  + Content-Type: application/json -> 200 (declared accepts)';

    v_response := api.rest_invoke(
        'POST', '/test-accepts',
        extensions.hstore('content-type', 'application/xml'),
        convert_to('<x/>', 'UTF8')
    );
    IF (v_response).status_code != 415 THEN
        RAISE EXCEPTION 'Content-Type application/xml should return 415, got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  + Content-Type: application/xml -> 415 Unsupported Media Type';

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

    -- ========================================================================
    -- PGMI-31: media-range Accept must not false-406, and a superstring
    -- subtype must not false-accept.
    -- ========================================================================

    -- Accept: application/* matches a JSON-producing handler (no false 406).
    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        'accept=>application/*'::extensions.hstore,
        NULL::bytea
    );
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'Accept: application/* should match application/json (200), got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  + Accept: application/* -> 200 (media range, no false 406)';

    -- Accept: application/json-patch+json must NOT match application/json.
    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        'accept=>application/json-patch+json'::extensions.hstore,
        NULL::bytea
    );
    IF (v_response).status_code != 406 THEN
        RAISE EXCEPTION 'Accept: application/json-patch+json must NOT match application/json (expected 406), got %', (v_response).status_code;
    END IF;
    RAISE NOTICE '  + Accept: application/json-patch+json -> 406 (no false accept)';

    RAISE NOTICE '+ Content Negotiation tests passed';
END $$;

-- ============================================================================
-- Test: REST route resolution strips query string before regex match.
-- Patterns no longer need '(\?.*)?$' boilerplate.
-- ============================================================================

DO $$
DECLARE
    v_handler_id uuid := 'ffffffff-c004-4000-8000-000000000001';
    v_response api.http_response;
BEGIN
    RAISE NOTICE '-> Testing REST query-string-aware routing';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_handler_id,
            'uri', '^/qs-probe$',  -- No '(\?.*)?$' needed; gateway strips query string
            'httpMethod', '^GET$',
            'name', 'qs_probe',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('matched', true));
END;
        $body$
    );

    v_response := api.rest_invoke('GET', '/qs-probe', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'route should match bare path: %', (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/qs-probe?foo=bar&baz=quux', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'route should match path with query string (gateway strips it): %', (v_response).status_code;
    END IF;

    RAISE NOTICE '  + REST routes match path-only; query string ignored';
END $$;

-- ============================================================================
-- Test: handler registration rejects names that risk identifier truncation
-- ============================================================================

DO $$
DECLARE
    v_caught boolean := false;
BEGIN
    RAISE NOTICE '-> Testing handler name validation';

    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-c005-4000-8000-000000000001',
                'uri', '^/long$',
                'httpMethod', '^GET$',
                -- 60 chars - over the 49 limit
                'name', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
                'requiresAuth', false
            ),
            'BEGIN RETURN api.json_response(200, ''{}''::jsonb); END;'
        );
    EXCEPTION WHEN OTHERS THEN
        IF SQLERRM ~ 'invalid handler name' THEN
            v_caught := true;
        ELSE
            RAISE;
        END IF;
    END;

    IF NOT v_caught THEN
        RAISE EXCEPTION 'expected handler-name validation error for 60-char name';
    END IF;

    -- Empty name (rejected by IS NULL check or regex)
    v_caught := false;
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-c006-4000-8000-000000000001',
                'uri', '^/empty$',
                'httpMethod', '^GET$',
                'name', '',
                'requiresAuth', false
            ),
            'BEGIN RETURN api.json_response(200, ''{}''::jsonb); END;'
        );
    EXCEPTION WHEN OTHERS THEN
        IF SQLERRM ~ 'handler name|invalid handler name' THEN
            v_caught := true;
        ELSE
            RAISE;
        END IF;
    END;

    IF NOT v_caught THEN
        RAISE EXCEPTION 'expected validation error for empty handler name';
    END IF;

    RAISE NOTICE '  + Handler name validation rejects oversized and empty names';
END $$;

DO $$
DECLARE
    v_id uuid := 'ffffffff-c007-4000-8000-000000000001';
    v_alpha_exists boolean;
    v_beta_exists boolean;
    v_registered text;
BEGIN
    RAISE NOTICE '-> Testing handler rename drops the orphaned function';

    -- Register under the first name
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_id, 'uri', '^/rename-test$', 'httpMethod', '^GET$',
            'name', 'rename_alpha', 'requiresAuth', false
        ),
        'BEGIN RETURN api.json_response(200, ''{}''::jsonb); END;'
    );

    SELECT EXISTS(
        SELECT 1 FROM pg_proc
        WHERE pronamespace = 'api'::regnamespace AND proname = 'rename_alpha'
          AND proargtypes[0] = 'api.rest_request'::regtype
    ) INTO v_alpha_exists;
    IF NOT v_alpha_exists THEN
        RAISE EXCEPTION 'rename test setup: api.rename_alpha should exist after first registration';
    END IF;

    -- Re-register the SAME id under a new name
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_id, 'uri', '^/rename-test$', 'httpMethod', '^GET$',
            'name', 'rename_beta', 'requiresAuth', false
        ),
        'BEGIN RETURN api.json_response(200, ''{}''::jsonb); END;'
    );

    SELECT EXISTS(
        SELECT 1 FROM pg_proc
        WHERE pronamespace = 'api'::regnamespace AND proname = 'rename_alpha'
          AND proargtypes[0] = 'api.rest_request'::regtype
    ) INTO v_alpha_exists;
    SELECT EXISTS(
        SELECT 1 FROM pg_proc
        WHERE pronamespace = 'api'::regnamespace AND proname = 'rename_beta'
          AND proargtypes[0] = 'api.rest_request'::regtype
    ) INTO v_beta_exists;

    IF v_alpha_exists THEN
        RAISE EXCEPTION 'orphaned function api.rename_alpha was not dropped on rename';
    END IF;
    IF NOT v_beta_exists THEN
        RAISE EXCEPTION 'replacement function api.rename_beta should exist after rename';
    END IF;

    SELECT handler_function_name INTO v_registered FROM api.handler WHERE object_id = v_id;
    IF v_registered <> 'rename_beta' THEN
        RAISE EXCEPTION 'registry should point at rename_beta, got %', v_registered;
    END IF;

    RAISE NOTICE '  + Renaming a handler drops the old function and leaves no orphan';
END $$;

DO $$
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE '===============================================================';
    RAISE NOTICE '+ ALL HANDLER LIFECYCLE AND CONTENT NEGOTIATION TESTS PASSED';
    RAISE NOTICE '===============================================================';
END $$;
