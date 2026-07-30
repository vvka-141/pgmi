-- ============================================================================
-- Test: Redeploy safety and logging suppression
-- ============================================================================
-- Covers three defects that share one theme: a shipped default quietly doing
-- more than it says.
--   1. autoLog=false was honored on success and ignored on failure.
--   2. Soft-deleting a handler removed it from the contract but not from
--      dispatch.
--   3. An idempotent file dropped its types with CASCADE on every deploy.
-- ============================================================================

DO $$
DECLARE
    v_response api.http_response;
    v_logged bigint;
BEGIN
    RAISE NOTICE '→ Testing autoLog=false on the error path';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-3001-4000-8000-000000000001',
            'uri', '^/secret-login$',
            'httpMethod', '^POST$',
            'name', 'secret_login_probe',
            'description', 'Raises, and must never persist its request body',
            'requiresAuth', false,
            'autoLog', false
        ),
        $body$
BEGIN
    RAISE EXCEPTION 'simulated constraint violation';
END;
$body$
    );

    v_response := api.rest_invoke(
        'POST', '/secret-login', ''::extensions.hstore,
        convert_to('{"password":"hunter2"}', 'UTF8')
    );

    IF coalesce(v_response.status_code, 0) < 400 THEN
        RAISE EXCEPTION 'TEST FAILED: the probe handler was supposed to raise, got %', v_response.status_code;
    END IF;

    SELECT count(*) INTO v_logged
    FROM api.rest_exchange
    WHERE handler_object_id = 'ffffffff-3001-4000-8000-000000000001'::uuid;

    IF v_logged IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION
            'TEST FAILED: autoLog=false route logged % exchange(s) on the error path; '
            'the request body (credentials included) was persisted', v_logged;
    END IF;

    RAISE NOTICE '  ✓ autoLog=false suppresses logging on failure as well as success';
END $$;


DO $$
DECLARE
    v_response api.http_response;
BEGIN
    RAISE NOTICE '→ Testing soft-deleted handlers are undispatchable';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-3002-4000-8000-000000000001',
            'uri', '^/retired$',
            'httpMethod', '^GET$',
            'name', 'retired_probe',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('alive', true));
END;
$body$
    );

    v_response := api.rest_invoke('GET', '/retired', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: the probe route should serve before deletion, got %',
            v_response.status_code;
    END IF;

    UPDATE api.handler SET deleted_at = now()
    WHERE object_id = 'ffffffff-3002-4000-8000-000000000001'::uuid;

    -- Before the fix this still returned 200 while the route was already gone
    -- from the OpenAPI document: invisible in the contract, still serving.
    v_response := api.rest_invoke('GET', '/retired', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION
            'TEST FAILED: a soft-deleted handler is still dispatchable (got %); it is absent '
            'from OpenAPI, so the contract and the runtime disagree', v_response.status_code;
    END IF;

    RAISE NOTICE '  ✓ soft-deleted handlers leave dispatch and the contract together';
END $$;


DO $$
DECLARE
    v_survived boolean;
BEGIN
    RAISE NOTICE '→ Testing user objects survive an encoding-types redeploy';

    CREATE TEMP TABLE encoding_dependent (payload common.utf8);

    -- Re-run the shipped definition exactly as a redeploy would. The types are
    -- documented as a public convenience, so a user column typed common.utf8 is
    -- an invited dependency -- and DROP TYPE ... CASCADE destroyed it silently.
    EXECUTE (SELECT content FROM pg_temp.pgmi_plan_view WHERE path LIKE '%/lib/common/encoding.sql');

    SELECT EXISTS (
        SELECT 1 FROM pg_attribute a
        JOIN pg_class c ON c.oid = a.attrelid
        WHERE c.relname = 'encoding_dependent' AND a.attname = 'payload' AND NOT a.attisdropped
    ) INTO v_survived;

    IF v_survived IS DISTINCT FROM true THEN
        RAISE EXCEPTION
            'TEST FAILED: redeploying encoding.sql destroyed a user column typed common.utf8';
    END IF;

    -- The casts must still work after the redeploy, which is what the drops
    -- and recreations are for.
    IF (convert_to('{"ok":true}', 'UTF8')::common.utf8::jsonb)->>'ok' IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'TEST FAILED: bytea::utf8::jsonb cast chain broken after redeploy';
    END IF;

    DROP TABLE encoding_dependent;

    RAISE NOTICE '  ✓ encoding types survive redeploy and keep their casts';
END $$;
