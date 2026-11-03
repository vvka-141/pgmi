-- ============================================================================
-- Test: OpenAPI document agrees with the router
-- ============================================================================
-- Its own file on purpose. test_openapi.sql registers fixture routes in earlier
-- DO blocks that live until that file's savepoint unwinds, so running these
-- checks there measured the fixtures too. Per-file isolation makes them
-- measure the routes the project actually ships.

-- ============================================================================
-- The document must be TRUE, not merely well-formed
-- ============================================================================
-- Everything above checks the document's own consistency: structure, security
-- requirements, schema coverage, template variables, ETag. All of it passes
-- just as happily if the document describes an API that does not exist.
--
-- These two arms compare the derivation against the runtime it claims to
-- describe -- the "one declaration, many derivations" contract. Nothing else
-- in this suite sends a published path through the router.
DO $$
DECLARE
    v_undocumented text;
    v_unregistered text;
    v_routes       int;
    v_operations   int;
BEGIN
    -- The document is filtered by caller; completeness is a claim about what
    -- an authenticated caller sees.
    PERFORM pg_temp.test_login('test', 'openapi-agreement', 'openapi-agreement@example.test', NULL, true);
    PERFORM pg_temp.test_act_as('test|openapi-agreement');

    -- Arm 1: every registered REST route is published, and nothing is
    -- published that is not registered. A route missing from the document is
    -- an endpoint clients cannot discover; a path in the document with no
    -- route behind it is a promise the gateway will answer with 404.
    SELECT string_agg(r.route_name || ' (' || coalesce(r.canonical_path, 'canonical_path IS NULL') || ')', ', ')
    INTO v_undocumented
    FROM api.rest_route r
    WHERE r.canonical_path IS NULL
       OR NOT EXISTS (
           SELECT 1 FROM jsonb_object_keys(api.openapi_document()->'paths') AS k(path)
           WHERE k.path = r.canonical_path
       );

    IF v_undocumented IS NOT NULL THEN
        RAISE EXCEPTION 'route(s) registered but absent from the OpenAPI document: %', v_undocumented;
    END IF;

    SELECT string_agg(k.path, ', ') INTO v_unregistered
    FROM jsonb_object_keys(api.openapi_document()->'paths') AS k(path)
    WHERE NOT EXISTS (SELECT 1 FROM api.rest_route r WHERE r.canonical_path = k.path);

    IF v_unregistered IS NOT NULL THEN
        RAISE EXCEPTION 'OpenAPI path(s) with no registered route behind them: %', v_unregistered;
    END IF;

    SELECT count(*) INTO v_routes FROM api.rest_route;
    SELECT count(*) INTO v_operations
    FROM jsonb_each(api.openapi_document()->'paths') p,
         jsonb_each(p.value) op
    WHERE op.key IN ('get', 'post', 'put', 'patch', 'delete', 'head', 'options');

    -- Set equality on paths alone would miss a lost method on a shared path
    -- (GET /x published, POST /x dropped).
    IF v_operations IS DISTINCT FROM v_routes THEN
        RAISE EXCEPTION 'OpenAPI documents % operation(s) for % registered route(s)',
            v_operations, v_routes;
    END IF;

    PERFORM pg_temp.test_act_as(NULL);

    RAISE NOTICE '  + every registered route is published, and every published path is registered';
END $$;

-- Arm 3: every status the gateway derives from a route's metadata is published
-- on its operation, with the shared Problem body, and a body the gateway checks
-- is a required body. A status the gateway can send but the document omits is
-- one a generated client cannot handle.
DO $$
DECLARE
    v_missing text;
BEGIN
    PERFORM pg_temp.test_act_as('test|openapi-agreement');
    WITH op AS (
        SELECT h.handler_function_name AS name, m.method, r.success_status, h.access, h.read_only,
               h.min_transaction_isolation, h.input_json_schema, r.query_schema, r.path_params,
               api.openapi_document()->'paths'->r.canonical_path->m.method AS operation
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        CROSS JOIN LATERAL unnest(api.openapi_methods(r.method_regexp)) AS m(method)
    ),
    expected AS (
        SELECT op.name, op.method, op.operation, x.status
        FROM op CROSS JOIN LATERAL (VALUES
            (op.success_status::text, true),
            ('400', op.method IN ('post', 'put', 'patch') OR op.query_schema IS NOT NULL
                    OR EXISTS (SELECT FROM jsonb_array_elements(op.path_params) p WHERE p->>'type' IN ('uuid', 'int'))),
            ('401', op.access IS DISTINCT FROM 'public'),
            ('403', op.access = 'permission'),
            ('428', op.read_only OR op.min_transaction_isolation IS NOT NULL)
        ) AS x(status, applies)
        WHERE x.applies
    )
    SELECT string_agg(format('%s %s: %s', upper(method), name, problem), '; ' ORDER BY name, method, problem)
    INTO v_missing
    FROM (
        SELECT name, method, 'no ' || status AS problem FROM expected
        WHERE NOT coalesce(operation->'responses' ? status, false)
        UNION ALL
        SELECT op.name, op.method, r.key || ' without the Problem body'
        FROM op CROSS JOIN LATERAL jsonb_each(op.operation->'responses') r
        WHERE r.key ~ '^4' AND r.value->'content'->'application/problem+json'->'schema'->>'$ref'
                               IS DISTINCT FROM '#/components/schemas/Problem'
        UNION ALL
        SELECT name, method, 'requestBody.required missing' FROM op
        WHERE input_json_schema IS NOT NULL AND method IN ('post', 'put', 'patch')
          AND operation->'requestBody'->'required' IS DISTINCT FROM 'true'::jsonb
        UNION ALL
        SELECT name, method, 'more than one 2xx' FROM op
        WHERE (SELECT count(*) FROM jsonb_object_keys(operation->'responses') k WHERE k ~ '^2') IS DISTINCT FROM 1::bigint
    ) problems;
    PERFORM pg_temp.test_act_as(NULL);

    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'OpenAPI omits what the gateway answers: %', v_missing;
    END IF;
END $$;

-- Arm 4: every $ref in the document resolves to a published component, and
-- every registered component is used.
DO $$
DECLARE
    v_doc jsonb;
    v_dangling text;
    v_unused text;
BEGIN
    PERFORM pg_temp.test_act_as('test|openapi-agreement');
    v_doc := api.openapi_document();
    PERFORM pg_temp.test_act_as(NULL);

    SELECT string_agg(DISTINCT ref #>> '{}', ', ') INTO v_dangling
    FROM jsonb_path_query(v_doc, 'strict $.**."$ref"') AS ref
    WHERE NOT v_doc->'components'->'schemas' ? regexp_replace(ref #>> '{}', '^#/components/schemas/', '');
    IF v_dangling IS NOT NULL THEN
        RAISE EXCEPTION 'OpenAPI references schemas it does not publish: %', v_dangling;
    END IF;

    SELECT string_agg(sc.name, ', ' ORDER BY sc.name) INTO v_unused
    FROM api.schema_component sc
    WHERE NOT EXISTS (
        SELECT FROM jsonb_path_query(v_doc - 'components', 'strict $.**."$ref"') AS ref
        WHERE ref #>> '{}' = '#/components/schemas/' || sc.name)
      AND NOT EXISTS (
        SELECT FROM api.schema_component other
        CROSS JOIN LATERAL jsonb_path_query(other.schema::jsonb, 'strict $.**."$ref"') AS ref
        WHERE other.name IS DISTINCT FROM sc.name AND ref #>> '{}' = '#/components/schemas/' || sc.name);
    IF v_unused IS NOT NULL THEN
        RAISE EXCEPTION 'schema component(s) no operation uses: %', v_unused;
    END IF;
END $$;

-- Arm 5: the catalog version digests every registry column the generator
-- reads, so the ETag cannot stay put while the document changes. The check
-- reads both functions' source; a planted column proves it can fail.
CREATE FUNCTION pg_temp.columns_missing_from_digest(p_generator text, p_digest text)
RETURNS SETOF text
LANGUAGE sql IMMUTABLE AS $$
    SELECT DISTINCT m[1]
    FROM regexp_matches(p_generator, '\m((?:r|h|hp|sc)\.[a-z_]+)\M', 'g') AS m
    EXCEPT
    SELECT DISTINCT m[1]
    FROM regexp_matches(p_digest, '\m((?:r|h|hp|sc)\.[a-z_]+)\M', 'g') AS m;
$$;

DO $$
DECLARE
    v_missing text;
BEGIN
    SELECT string_agg(c, ', ' ORDER BY c) INTO v_missing
    FROM pg_temp.columns_missing_from_digest(
        (SELECT prosrc FROM pg_proc WHERE oid = 'api.openapi_document()'::regprocedure),
        (SELECT prosrc FROM pg_proc WHERE oid = 'internal.catalog_digest()'::regprocedure)) AS c;
    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'api.openapi_document() reads % but internal.catalog_digest() does not digest it', v_missing;
    END IF;

    IF NOT EXISTS (SELECT FROM pg_temp.columns_missing_from_digest('SELECT r.planted_column FROM api.rest_route r',
                                                                  'SELECT r.canonical_path FROM api.rest_route r')) THEN
        RAISE EXCEPTION 'the digest guard does not see a planted generator column';
    END IF;
END $$;

DO $$
DECLARE
    v_path     text;
    v_method   text;
    v_status   int;
    v_checked  int := 0;
    v_ops      text[];
    v_response api.http_response;
BEGIN
    -- Arm 2: a published path, sent to the router, must reach a handler.
    -- 404 means no route matched and 405 means the documented verb is not
    -- accepted; both make the document a lie. 401/403 are fine -- the route
    -- matched and the gateway enforced auth, which is the point of /me.
    --
    -- A path with {variables} is sent as the route's probe URL, which
    -- registration derived from its own capture groups or its declared
    -- example, so the substituted value satisfies that route and no other.
    --
    -- The operations are read as an authenticated caller, so auth-gated routes
    -- are covered too, and then sent anonymously, so no admin handler runs.
    PERFORM pg_temp.test_act_as('test|openapi-agreement');
    SELECT array_agg(upper(op.key) || ' ' || COALESCE(NULLIF(r.probe_path, ''), p.key) ORDER BY p.key, op.key)
    INTO v_ops
    FROM jsonb_each(api.openapi_document()->'paths') p
    CROSS JOIN LATERAL jsonb_each(p.value) op
    LEFT JOIN LATERAL (
        SELECT rr.probe_path FROM api.rest_route rr
        WHERE rr.canonical_path = p.key ORDER BY rr.sequence_number DESC LIMIT 1) r ON true
    WHERE op.key IN ('get', 'post', 'put', 'patch', 'delete');
    PERFORM pg_temp.test_act_as(NULL);

    FOR v_method, v_path IN
        SELECT split_part(o, ' ', 1), split_part(o, ' ', 2) FROM unnest(v_ops) AS o
    LOOP
        v_response := api.rest_invoke(v_method, v_path);
        v_status := (v_response).status_code;
        v_checked := v_checked + 1;

        -- A handler may answer 404 for a probe value that names no row; only
        -- the router's own 404 means the path did not resolve.
        IF (v_status = 405) IS NOT FALSE OR (v_status IS NOT DISTINCT FROM 404
               AND coalesce(api.content_json((v_response).content)->>'detail', '') LIKE 'No route matches%') THEN
            RAISE EXCEPTION 'OpenAPI publishes % % but the gateway answers % -- the document describes a route that does not resolve',
                v_method, v_path, v_status;
        END IF;
    END LOOP;

    IF (v_checked = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'no published operations were exercised -- the round-trip check ran on nothing';
    END IF;

    -- OPTIONS answers every published path, before identity, so a CORS
    -- preflight to any documented operation can succeed.
    FOR v_path IN SELECT DISTINCT split_part(o, ' ', 2) FROM unnest(v_ops) AS o LOOP
        v_status := (api.rest_invoke('OPTIONS', v_path)).status_code;
        IF v_status IS DISTINCT FROM 204 AND v_status NOT BETWEEN 200 AND 299 THEN
            RAISE EXCEPTION 'OpenAPI publishes % but OPTIONS on it answers %', v_path, v_status;
        END IF;
    END LOOP;

    RAISE NOTICE '  + all % published operation(s) resolve to a handler, and every path answers OPTIONS', v_checked;
    RAISE NOTICE '✓ OpenAPI-to-router agreement tests passed';
END $$;
