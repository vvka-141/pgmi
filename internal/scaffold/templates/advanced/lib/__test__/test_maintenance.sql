-- ============================================================================
-- Test: Exchange retention (the purge_exchanges job + admin endpoint)
-- ============================================================================

DO $$
DECLARE
    v_handler_id uuid;
    v_old_ts timestamptz := now() - interval '60 days';
    v_count_before int;
    v_count_after int;
    v_response api.http_response;
    v_body jsonb;
    v_admin_id uuid;
    v_admin_headers extensions.hstore;
BEGIN
    RAISE NOTICE '-> Testing exchange retention';

    -- Purge is for platform operators only
    v_admin_id := pg_temp.test_login('test', 'maint-admin', 'maint-admin@example.test', 'Maint Admin', true);
    PERFORM pg_temp.test_operator(v_admin_id);
    v_admin_headers := pg_temp.test_session_headers('test|maint-admin', NULL, ARRAY['mfa']);

    -- ========================================================================
    -- Setup: insert fixture exchanges with old enqueued_at
    -- ========================================================================

    SELECT object_id INTO v_handler_id
    FROM api.handler WHERE handler_function_name = 'hello_world'
    LIMIT 1;

    INSERT INTO api.rest_exchange (object_id, enqueued_at, handler_object_id, request, response, completed_at)
    SELECT gen_random_uuid(), v_old_ts - (n || ' hours')::interval,
           v_handler_id,
           ROW('GET', '/test', ''::extensions.hstore, null::bytea, '{}'::jsonb)::api.rest_request,
           ROW(200, ''::extensions.hstore, null::bytea)::api.http_response,
           v_old_ts
    FROM generate_series(1, 10) AS n;

    SELECT COUNT(*) INTO v_count_before
    FROM api.rest_exchange WHERE enqueued_at < now() - interval '30 days';

    IF coalesce(v_count_before, 0) < 10 THEN
        RAISE EXCEPTION 'Setup failed: expected >= 10 old exchanges, got %', v_count_before;
    END IF;

    RAISE NOTICE '  + Inserted 10 fixture exchanges older than 30 days';

    -- ========================================================================
    -- Test: admin purge endpoint deletes old exchanges
    -- ========================================================================

    v_response := api.rest_invoke('POST', '/admin/maintenance/purge-exchanges?retention_days=30&batch_size=100',
        v_admin_headers, null::bytea);

    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'Purge endpoint returned %, expected 200', (v_response).status_code;
    END IF;

    v_body := api.content_json((v_response).content);

    IF coalesce((v_body->>'totalDeleted')::int, 0) < 10 THEN
        RAISE EXCEPTION 'Expected >= 10 deleted, got %', v_body->>'totalDeleted';
    END IF;

    IF coalesce((v_body->'deleted'->>'rest')::int, 0) < 10 THEN
        RAISE EXCEPTION 'Expected >= 10 REST deleted, got %', v_body->'deleted'->>'rest';
    END IF;

    -- The response body is the handler's own report. Ask the table instead: a
    -- handler that returns {"totalDeleted": 10} while deleting nothing satisfies
    -- every assertion above.
    SELECT COUNT(*) INTO v_count_after
    FROM api.rest_exchange WHERE enqueued_at < now() - interval '30 days';

    IF v_count_after IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'Purge reported % deleted but % exchange(s) older than the retention window remain',
            v_body->>'totalDeleted', v_count_after;
    END IF;

    RAISE NOTICE '  + Purge endpoint deleted % exchanges (rest=%); none older than 30 days remain',
        v_body->>'totalDeleted', v_body->'deleted'->>'rest';

    -- ========================================================================
    -- Test: retention reads enqueued_at through a btree on every exchange table
    -- ========================================================================

    IF (SELECT count(*)
        FROM pg_index i
        JOIN pg_class c ON c.oid = i.indexrelid
        JOIN pg_am am ON am.oid = c.relam
        JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[0]
        WHERE i.indrelid IN ('api.rest_exchange'::regclass, 'api.rpc_exchange'::regclass, 'api.mcp_exchange'::regclass)
          AND am.amname = 'btree' AND a.attname = 'enqueued_at') IS DISTINCT FROM 3::bigint THEN
        RAISE EXCEPTION 'Each exchange table needs a btree index leading on enqueued_at';
    END IF;

    RAISE NOTICE '  + btree indexes on enqueued_at exist for all exchange tables';

    -- ========================================================================
    -- Test: validation rejects bad params
    -- ========================================================================

    v_response := api.rest_invoke('POST', '/admin/maintenance/purge-exchanges?retention_days=0',
        v_admin_headers, null::bytea);

    IF (v_response).status_code IS DISTINCT FROM 400
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'pgmi.invalid_query' THEN
        RAISE EXCEPTION 'Expected 400 pgmi.invalid_query for retention_days=0, got % %',
            (v_response).status_code, convert_from((v_response).content, 'UTF8');
    END IF;

    RAISE NOTICE '  + Validation rejects retention_days < 1';

    RAISE NOTICE '+ Exchange retention tests passed';
END $$;

-- The default window is the deploy parameter exchange_retention_days (30 in
-- the test deploy): without retention_days the endpoint keeps a 29-day-old
-- row and purges a 31-day-old one.
DO $$
DECLARE
    v_handler_id uuid := (SELECT object_id FROM api.handler WHERE handler_function_name = 'hello_world');
    v_kept uuid := gen_random_uuid();
    v_gone uuid := gen_random_uuid();
    v_response api.http_response;
BEGIN
    RAISE NOTICE '-> Testing the default retention window';

    IF api.exchange_retention() IS DISTINCT FROM interval '30 days' THEN
        RAISE EXCEPTION 'TEST FAILED: api.exchange_retention() should be the deploy default of 30 days, got %', api.exchange_retention();
    END IF;

    INSERT INTO api.rest_exchange (object_id, enqueued_at, handler_object_id, request, response, completed_at)
    VALUES (v_kept, now() - interval '29 days', v_handler_id,
            ROW('GET', '/test', ''::extensions.hstore, null::bytea, '{}'::jsonb)::api.rest_request,
            ROW(200, ''::extensions.hstore, null::bytea)::api.http_response, now()),
           (v_gone, now() - interval '31 days', v_handler_id,
            ROW('GET', '/test', ''::extensions.hstore, null::bytea, '{}'::jsonb)::api.rest_request,
            ROW(200, ''::extensions.hstore, null::bytea)::api.http_response, now());

    PERFORM pg_temp.test_operator(pg_temp.test_login('test', 'maint-default', 'maint-default@example.test', NULL, true));
    v_response := api.rest_invoke('POST', '/admin/maintenance/purge-exchanges',
        pg_temp.test_session_headers('test|maint-default', NULL, ARRAY['mfa']), null::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: default purge answered %', (v_response).status_code;
    END IF;
    IF NOT EXISTS (SELECT FROM api.rest_exchange WHERE object_id = v_kept)
       OR EXISTS (SELECT FROM api.rest_exchange WHERE object_id = v_gone) THEN
        RAISE EXCEPTION 'TEST FAILED: the default purge must keep 29 days and drop 31';
    END IF;
END $$;

-- Argument checks are IF ... RAISE: they hold with plpgsql.check_asserts off,
-- where an ASSERT would be skipped.
DO $$
DECLARE
    v_case text;
    v_state text;
BEGIN
    RAISE NOTICE '-> Testing the purge refuses bad arguments with asserts off';
    SET LOCAL plpgsql.check_asserts = off;

    FOREACH v_case IN ARRAY ARRAY[
        $q$CALL api.job_purge_exchanges(NULL)$q$,
        $q$CALL api.job_purge_exchanges(0)$q$,
        $q$CALL api.job_purge_exchanges(10, 0)$q$,
        $q$SELECT api.purge_exchanges_batch(NULL, 10)$q$,
        $q$SELECT api.purge_exchanges_batch(now() + interval '1 day', 10)$q$,
        $q$SELECT api.purge_exchanges_batch(now() - interval '1 day', NULL)$q$,
        $q$SELECT api.purge_exchanges_batch(now() - interval '1 day', 0)$q$]
    LOOP
        v_state := NULL;
        BEGIN
            EXECUTE v_case;
        EXCEPTION WHEN OTHERS THEN
            v_state := SQLSTATE;
        END;
        IF v_state IS DISTINCT FROM '22023' THEN
            RAISE EXCEPTION 'TEST FAILED: % must raise 22023, got %', v_case, coalesce(v_state, 'success');
        END IF;
    END LOOP;
END $$;

-- One batch of the job deletes only exchanges past the window, at most the
-- batch size from each protocol table.
DO $$
DECLARE
    v_handler_id uuid := (SELECT object_id FROM api.handler WHERE handler_function_name = 'hello_world');
    v_deleted    int;
    v_old        int;
    v_recent     int;
BEGIN
    RAISE DEBUG '-> Testing one batch of the purge_exchanges job';

    INSERT INTO api.rest_exchange (object_id, enqueued_at, handler_object_id, request, response, completed_at)
    SELECT gen_random_uuid(), now() - api.exchange_retention() - make_interval(hours => n), v_handler_id,
           ROW('GET', '/test', ''::extensions.hstore, null::bytea, '{}'::jsonb)::api.rest_request,
           ROW(200, ''::extensions.hstore, null::bytea)::api.http_response, now()
    FROM generate_series(1, 5) n
    UNION ALL
    SELECT gen_random_uuid(), now() - api.exchange_retention() + interval '1 hour', v_handler_id,
           ROW('GET', '/test', ''::extensions.hstore, null::bytea, '{}'::jsonb)::api.rest_request,
           ROW(200, ''::extensions.hstore, null::bytea)::api.http_response, now();

    SELECT count(*) INTO v_old FROM api.rest_exchange WHERE enqueued_at < now() - api.exchange_retention();
    SELECT count(*) INTO v_recent FROM api.rest_exchange WHERE enqueued_at >= now() - api.exchange_retention();

    v_deleted := api.job_purge_exchanges_batch(3);
    IF coalesce(v_deleted, 0) NOT BETWEEN 3 AND 9
       OR (SELECT count(*) FROM api.rest_exchange WHERE enqueued_at < now() - api.exchange_retention()) IS DISTINCT FROM v_old - 3 THEN
        RAISE EXCEPTION 'TEST FAILED: a batch of 3 must delete exactly 3 of % expired REST exchanges, reported % in all', v_old, v_deleted;
    END IF;

    WHILE api.job_purge_exchanges_batch(3) > 0 LOOP END LOOP;
    IF EXISTS (SELECT FROM api.rest_exchange WHERE enqueued_at < now() - api.exchange_retention())
       OR (SELECT count(*) FROM api.rest_exchange) IS DISTINCT FROM v_recent THEN
        RAISE EXCEPTION 'TEST FAILED: the batches must delete every expired exchange and keep the % recent ones', v_recent;
    END IF;
END $$;
