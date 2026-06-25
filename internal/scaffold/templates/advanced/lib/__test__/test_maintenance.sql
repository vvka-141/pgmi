-- ============================================================================
-- Test: Exchange retention (purge procedure + admin endpoint)
-- ============================================================================

DO $$
DECLARE
    v_handler_id uuid;
    v_old_ts timestamptz := now() - interval '60 days';
    v_count_before int;
    v_count_after int;
    v_response api.http_response;
    v_body jsonb;
BEGIN
    RAISE NOTICE '-> Testing exchange retention';

    -- ========================================================================
    -- Setup: insert fixture exchanges with old enqueued_at
    -- ========================================================================

    SELECT object_id INTO v_handler_id
    FROM api.handler WHERE handler_function_name = 'hello_world'
    LIMIT 1;

    INSERT INTO api.rest_exchange (object_id, enqueued_at, handler_object_id, request, response, completed_at)
    SELECT gen_random_uuid(), v_old_ts - (n || ' hours')::interval,
           v_handler_id,
           ROW('GET', '/test', ''::extensions.hstore, null::bytea)::api.rest_request,
           ROW(200, ''::extensions.hstore, null::bytea)::api.http_response,
           v_old_ts
    FROM generate_series(1, 10) AS n;

    SELECT COUNT(*) INTO v_count_before
    FROM api.rest_exchange WHERE enqueued_at < now() - interval '30 days';

    IF v_count_before < 10 THEN
        RAISE EXCEPTION 'Setup failed: expected >= 10 old exchanges, got %', v_count_before;
    END IF;

    RAISE NOTICE '  + Inserted 10 fixture exchanges older than 30 days';

    -- ========================================================================
    -- Test: admin purge endpoint deletes old exchanges
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/admin/maintenance/purge-exchanges?retention_days=30&batch_size=100',
        ('x-user-id=>test|admin')::extensions.hstore, null::bytea);

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'Purge endpoint returned %, expected 200', (v_response).status_code;
    END IF;

    v_body := api.content_json((v_response).content);

    IF (v_body->>'totalDeleted')::int < 10 THEN
        RAISE EXCEPTION 'Expected >= 10 deleted, got %', v_body->>'totalDeleted';
    END IF;

    IF (v_body->'deleted'->>'rest')::int < 10 THEN
        RAISE EXCEPTION 'Expected >= 10 REST deleted, got %', v_body->'deleted'->>'rest';
    END IF;

    RAISE NOTICE '  + Purge endpoint deleted % exchanges (rest=%)',
        v_body->>'totalDeleted', v_body->'deleted'->>'rest';

    -- ========================================================================
    -- Test: procedure exists with expected signature
    -- ========================================================================

    IF NOT EXISTS (
        SELECT 1 FROM pg_proc p
        JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname = 'api' AND p.proname = 'purge_exchanges'
          AND p.prokind = 'p'
    ) THEN
        RAISE EXCEPTION 'api.purge_exchanges PROCEDURE not found';
    END IF;

    RAISE NOTICE '  + api.purge_exchanges procedure exists';

    -- ========================================================================
    -- Test: BRIN indexes exist
    -- ========================================================================

    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE indexname = 'bx_rest_exchange_enqueued'
    ) THEN
        RAISE EXCEPTION 'BRIN index bx_rest_exchange_enqueued not found';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE indexname = 'bx_rpc_exchange_enqueued'
    ) THEN
        RAISE EXCEPTION 'BRIN index bx_rpc_exchange_enqueued not found';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE indexname = 'bx_mcp_exchange_enqueued'
    ) THEN
        RAISE EXCEPTION 'BRIN index bx_mcp_exchange_enqueued not found';
    END IF;

    RAISE NOTICE '  + BRIN indexes on enqueued_at exist for all exchange tables';

    -- ========================================================================
    -- Test: validation rejects bad params
    -- ========================================================================

    v_response := api.rest_invoke('GET', '/admin/maintenance/purge-exchanges?retention_days=0',
        ('x-user-id=>test|admin')::extensions.hstore, null::bytea);

    IF (v_response).status_code != 422 THEN
        RAISE EXCEPTION 'Expected 422 for retention_days=0, got %', (v_response).status_code;
    END IF;

    RAISE NOTICE '  + Validation rejects retention_days < 1';

    RAISE NOTICE '+ Exchange retention tests passed';
END $$;
