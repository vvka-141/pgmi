-- ============================================================================
-- Test: OpenAPI 3.1 generator
-- ============================================================================

DO $$
DECLARE
    v_doc jsonb;
    v_paths jsonb;
    v_response api.http_response;
BEGIN
    RAISE NOTICE '-> Testing OpenAPI 3.1 generator';

    v_doc := api.openapi_document();

    -- ========================================================================
    -- Basic structure
    -- ========================================================================

    IF v_doc->>'openapi' != '3.1.0' THEN
        RAISE EXCEPTION 'openapi version should be 3.1.0, got %', v_doc->>'openapi';
    END IF;

    IF v_doc->'info'->>'title' IS NULL THEN
        RAISE EXCEPTION 'info.title should not be null';
    END IF;

    IF v_doc->'paths' IS NULL THEN
        RAISE EXCEPTION 'paths should not be null';
    END IF;

    IF v_doc->'components'->'securitySchemes'->'bearerAuth' IS NULL THEN
        RAISE EXCEPTION 'bearerAuth security scheme should be defined';
    END IF;

    RAISE NOTICE '  + Document structure valid (3.1.0, info, paths, components)';

    -- ========================================================================
    -- REST routes appear in paths
    -- ========================================================================

    v_paths := v_doc->'paths';

    IF v_paths->'/openapi.json' IS NULL THEN
        RAISE EXCEPTION '/openapi.json path missing from spec. Paths: %', (SELECT jsonb_object_keys(v_paths) LIMIT 10);
    END IF;

    IF v_paths->'/openapi.json'->'get' IS NULL THEN
        RAISE EXCEPTION '/openapi.json GET operation missing';
    END IF;

    IF v_paths->'/openapi.json'->'get'->>'operationId' != 'openapi_spec' THEN
        RAISE EXCEPTION 'operationId should be openapi_spec, got %',
            v_paths->'/openapi.json'->'get'->>'operationId';
    END IF;

    RAISE NOTICE '  + /openapi.json path present with GET operation';

    -- ========================================================================
    -- Auth requirement: openapi_spec has no security (requiresAuth=false)
    -- ========================================================================

    IF v_paths->'/openapi.json'->'get'->'security' IS NOT NULL THEN
        RAISE EXCEPTION '/openapi.json should not have security requirement';
    END IF;

    RAISE NOTICE '  + No-auth handler omits security requirement';

    -- ========================================================================
    -- Handlers with requires_auth have security requirement
    -- ========================================================================

    IF v_paths->'/me' IS NOT NULL AND v_paths->'/me'->'get'->'security' IS NULL THEN
        RAISE EXCEPTION '/me should have security requirement';
    END IF;

    RAISE NOTICE '  + Auth handlers include security requirement';

    -- ========================================================================
    -- Handler served via REST gateway
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/openapi.json');

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'GET /openapi.json returned status %, expected 200', (v_response).status_code;
    END IF;

    RAISE NOTICE '  + GET /openapi.json returns 200 via gateway';

    -- ========================================================================
    -- Path conversion utilities
    -- ========================================================================

    IF api.openapi_path('^/hello(\?.*)?$') != '/hello' THEN
        RAISE EXCEPTION 'openapi_path failed: got %', api.openapi_path('^/hello(\?.*)?$');
    END IF;

    IF api.openapi_methods('^GET$') != ARRAY['get'] THEN
        RAISE EXCEPTION 'openapi_methods failed for ^GET$';
    END IF;

    RAISE NOTICE '  + Path/method conversion utilities work';

    RAISE NOTICE '✓ OpenAPI 3.1 generator tests passed';
END $$;
