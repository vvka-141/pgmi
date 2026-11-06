-- ============================================================================
-- Test: every exchange-log read the template ships is served by an index
-- ============================================================================
-- With sequential scans disabled, EXPLAIN of each lookup must name an index
-- scan on every exchange table it reads: by exchange id, by x-request-id
-- through api.vw_exchange, the newest-first page of GET /admin/exchanges
-- (a Merge Append of backward index scans, no Sort) and that page filtered by
-- actualPrincipalId (the expression index on each table). The exact index set on
-- the three tables is pinned so an index without a reader cannot creep back.
-- Each seeded row has its own principal, so the generic plan's estimate of
-- one principal's share holds on a database whose log already has few.
-- ============================================================================

CREATE FUNCTION pg_temp.exchange_plan_nodes(p_query text)
RETURNS TABLE (node_type text, relation text, index_name text)
LANGUAGE plpgsql AS $$
DECLARE
    v_plan jsonb;
BEGIN
    EXECUTE 'EXPLAIN (FORMAT JSON) ' || p_query INTO v_plan;
    RETURN QUERY
    SELECT n->>'Node Type', n->>'Relation Name', n->>'Index Name'
    FROM jsonb_path_query(v_plan, 'strict $.**') n
    WHERE jsonb_typeof(n) = 'object' AND n ? 'Node Type';
END $$;

CREATE FUNCTION pg_temp.assert_exchange_index_scans(p_label text, p_query text, p_tables text[])
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_bad text;
BEGIN
    SELECT string_agg(format('%s on %s', node_type, relation), ', ')
    INTO v_bad
    FROM pg_temp.exchange_plan_nodes(p_query)
    WHERE relation LIKE '%exchange' AND node_type NOT IN ('Index Scan', 'Index Only Scan', 'Bitmap Heap Scan');
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: % reads the exchange log without an index: %', p_label, v_bad;
    END IF;

    SELECT string_agg(t, ', ')
    INTO v_bad
    FROM unnest(p_tables) t
    WHERE NOT EXISTS (SELECT FROM pg_temp.exchange_plan_nodes(p_query) WHERE relation = t);
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: % does not read %', p_label, v_bad;
    END IF;
END $$;

DO $$
DECLARE
    v_rest_id uuid;
    v_rpc_id uuid;
    v_mcp_id uuid;
    v_extra text;
    v_principal text := gen_random_uuid()::text;
    v_filtered text;
BEGIN
    INSERT INTO api.rest_exchange (handler_object_id, context, request, response, completed_at)
    SELECT gen_random_uuid(), jsonb_build_object('actualPrincipalId', gen_random_uuid()),
           ROW('GET', '/idx', extensions.hstore('x-request-id', 'idx-rest-' || n), null::bytea, '{}'::jsonb)::api.rest_request,
           ROW(200, ''::extensions.hstore, null::bytea)::api.http_response, now()
    FROM generate_series(1, 200) n;

    INSERT INTO api.rpc_exchange (handler_object_id, context, request, response, completed_at)
    SELECT gen_random_uuid(), jsonb_build_object('actualPrincipalId', gen_random_uuid()),
           ROW(null::uuid, extensions.hstore('x-request-id', 'idx-rpc-' || n), null::bytea)::api.rpc_request,
           ROW(200, ''::extensions.hstore, null::bytea)::api.http_response, now()
    FROM generate_series(1, 200) n;

    INSERT INTO api.mcp_exchange (handler_object_id, context, mcp_type, mcp_name, request, response)
    SELECT gen_random_uuid(), jsonb_build_object('actualPrincipalId', gen_random_uuid()), 'tool', 'idx',
           ROW('{}'::jsonb, null, '{}'::jsonb, to_jsonb(n))::api.mcp_request,
           ROW('{}'::jsonb)::api.mcp_response
    FROM generate_series(1, 200) n;

    SELECT object_id INTO v_rest_id FROM api.rest_exchange LIMIT 1;
    SELECT object_id INTO v_rpc_id FROM api.rpc_exchange LIMIT 1;
    SELECT object_id INTO v_mcp_id FROM api.mcp_exchange LIMIT 1;

    ANALYZE api.rest_exchange, api.rpc_exchange, api.mcp_exchange;
    SET LOCAL enable_seqscan = off;

    PERFORM pg_temp.assert_exchange_index_scans('rest_exchange by object_id',
        format('SELECT * FROM api.rest_exchange WHERE object_id = %L', v_rest_id), '{rest_exchange}');
    PERFORM pg_temp.assert_exchange_index_scans('rpc_exchange by object_id',
        format('SELECT * FROM api.rpc_exchange WHERE object_id = %L', v_rpc_id), '{rpc_exchange}');
    PERFORM pg_temp.assert_exchange_index_scans('mcp_exchange by object_id',
        format('SELECT * FROM api.mcp_exchange WHERE object_id = %L', v_mcp_id), '{mcp_exchange}');

    PERFORM pg_temp.assert_exchange_index_scans('api.vw_exchange by request_id',
        $q$SELECT * FROM api.vw_exchange WHERE request_id = 'idx-rest-1'$q$, '{rest_exchange,rpc_exchange}');

    PERFORM pg_temp.assert_exchange_index_scans('the GET /admin/exchanges page',
        $q$SELECT protocol, exchange_id, enqueued_at FROM api.vw_exchange ORDER BY enqueued_at DESC LIMIT 20 OFFSET 20$q$,
        '{rest_exchange,rpc_exchange,mcp_exchange}');

    IF EXISTS (SELECT FROM pg_temp.exchange_plan_nodes(
                   $q$SELECT protocol, exchange_id, enqueued_at FROM api.vw_exchange ORDER BY enqueued_at DESC LIMIT 20$q$)
               WHERE node_type = 'Sort') THEN
        RAISE EXCEPTION 'TEST FAILED: the GET /admin/exchanges page sorts the log instead of merging index scans';
    END IF;

    EXECUTE $q$PREPARE pg_temp_exchanges_by_actual_principal(text, text) AS
        SELECT protocol, exchange_id, enqueued_at FROM api.vw_exchange
        WHERE ($1 IS NULL OR protocol = $1) AND context->>'actualPrincipalId' = $2
        ORDER BY enqueued_at DESC LIMIT 20 OFFSET 20$q$;
    SET LOCAL plan_cache_mode = force_generic_plan;
    v_filtered := format('EXECUTE pg_temp_exchanges_by_actual_principal(NULL, %L)', v_principal);
    PERFORM pg_temp.assert_exchange_index_scans('the GET /admin/exchanges page by actualPrincipalId',
        v_filtered, '{rest_exchange,rpc_exchange,mcp_exchange}');
    SELECT string_agg(t, ', ') INTO v_extra
    FROM unnest(ARRAY['rest', 'rpc', 'mcp']) t
    WHERE NOT EXISTS (SELECT FROM pg_temp.exchange_plan_nodes(v_filtered)
                      WHERE relation = t || '_exchange' AND index_name = 'ix_' || t || '_exchange_actual_principal');
    IF v_extra IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the page by actualPrincipalId does not use ix_*_exchange_actual_principal on: %', v_extra;
    END IF;
    IF EXISTS (SELECT FROM pg_temp.exchange_plan_nodes(v_filtered) WHERE node_type = 'Sort') THEN
        RAISE EXCEPTION 'TEST FAILED: the page by actualPrincipalId sorts the log instead of merging index scans';
    END IF;
    EXECUTE $q$PREPARE pg_temp_exchange_count_by_actual_principal(text, text) AS
        SELECT count(*) FROM api.vw_exchange
        WHERE ($1 IS NULL OR protocol = $1) AND context->>'actualPrincipalId' = $2$q$;
    PERFORM pg_temp.assert_exchange_index_scans('the GET /admin/exchanges total by actualPrincipalId',
        format('EXECUTE pg_temp_exchange_count_by_actual_principal(NULL, %L)', v_principal),
        '{rest_exchange,rpc_exchange,mcp_exchange}');
    EXECUTE 'DEALLOCATE pg_temp_exchanges_by_actual_principal';
    EXECUTE 'DEALLOCATE pg_temp_exchange_count_by_actual_principal';

    SELECT string_agg(coalesce(a.indexname, '-') || ' / ' || coalesce(e.name, '-'), ', ')
    INTO v_extra
    FROM (SELECT indexname FROM pg_indexes
          WHERE schemaname = 'api' AND tablename IN ('rest_exchange', 'rpc_exchange', 'mcp_exchange')) a
    FULL JOIN unnest(ARRAY[
        'uq_rest_exchange_seq', 'uq_rpc_exchange_seq', 'uq_mcp_exchange_seq',
        'uq_rest_exchange_object_id', 'uq_rpc_exchange_object_id', 'uq_mcp_exchange_object_id',
        'ix_rest_exchange_enqueued', 'ix_rpc_exchange_enqueued', 'ix_mcp_exchange_enqueued',
        'ix_rest_exchange_request_id', 'ix_rpc_exchange_request_id',
        'ix_rest_exchange_actual_principal', 'ix_rpc_exchange_actual_principal',
        'ix_mcp_exchange_actual_principal']) e(name) ON e.name = a.indexname
    WHERE a.indexname IS NULL OR e.name IS NULL;
    IF v_extra IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: exchange indexes differ from the expected set (actual / expected): %', v_extra;
    END IF;

    RAISE NOTICE '  + exchange log lookups by id, request id, actual principal and the admin page use indexes';
END $$;
