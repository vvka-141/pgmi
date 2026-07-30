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

    RAISE NOTICE '  + every registered route is published, and every published path is registered';
END $$;

DO $$
DECLARE
    v_path     text;
    v_method   text;
    v_status   int;
    v_checked  int := 0;
BEGIN
    -- Arm 2: a published path, sent to the router, must reach a handler.
    -- 404 means no route matched and 405 means the documented verb is not
    -- accepted; both make the document a lie. 401/403 are fine -- the route
    -- matched and the gateway enforced auth, which is the point of /me.
    --
    -- Only paths WITHOUT {variables}: a concrete value for a variable has to
    -- satisfy that route's own capture group, and this test ships to users
    -- whose routes may capture (\d+) or a slug. Guessing a substitution would
    -- fail their deploy for a route that is perfectly correct. Parameterized
    -- paths are covered by arm 1 plus the capture-group and in:path parameter
    -- checks above.
    FOR v_path, v_method IN
        SELECT p.key, upper(op.key)
        FROM jsonb_each(api.openapi_document()->'paths') p,
             jsonb_each(p.value) op
        WHERE op.key IN ('get', 'post', 'put', 'patch', 'delete')
          AND p.key NOT LIKE '%{%'
        ORDER BY p.key, op.key
    LOOP
        v_status := (api.rest_invoke(v_method, v_path)).status_code;
        v_checked := v_checked + 1;

        IF v_status IN (404, 405) THEN
            RAISE EXCEPTION 'OpenAPI publishes % % but the gateway answers % -- the document describes a route that does not resolve',
                v_method, v_path, v_status;
        END IF;
    END LOOP;

    IF v_checked = 0 THEN
        RAISE EXCEPTION 'no unparameterized paths were exercised -- the round-trip check ran on nothing';
    END IF;

    RAISE NOTICE '  + all % published unparameterized operation(s) resolve to a handler', v_checked;
    RAISE NOTICE '✓ OpenAPI-to-router agreement tests passed';
END $$;
