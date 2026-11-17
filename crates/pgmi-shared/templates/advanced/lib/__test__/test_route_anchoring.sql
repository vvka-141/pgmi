-- ============================================================================
-- Test: routing — path templates, ranking and the registration refusals
-- ============================================================================
-- A route is a path template. A parameter takes a whole segment, a literal
-- segment outranks a parameter, and registration refuses every pair of routes
-- the ranking cannot order: two of one shape with a common method.
-- ============================================================================

CREATE FUNCTION pg_temp.route_refusal(p_metadata jsonb)
RETURNS text
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        p_metadata || jsonb_build_object('access', 'public'),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
    RETURN NULL;
EXCEPTION WHEN invalid_parameter_value THEN
    RETURN SQLERRM;
END $$;

CREATE FUNCTION pg_temp.echo_params(p_name text, p_path text, p_access text DEFAULT 'public')
RETURNS void
LANGUAGE sql AS $$
    SELECT api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', gen_random_uuid(), 'path', p_path, 'httpMethod', '^GET$',
            'name', p_name, 'access', p_access),
        $body$ BEGIN RETURN api.json_response(200, (request).path_params); END; $body$);
$$;

CREATE FUNCTION pg_temp.route_named(p_name text, p_path text, p_method text, p_floor text DEFAULT NULL)
RETURNS void
LANGUAGE sql AS $$
    SELECT api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', gen_random_uuid(), 'path', p_path, 'httpMethod', p_method,
            'name', p_name, 'access', 'public', 'minTransactionIsolation', p_floor),
        format($body$ BEGIN RETURN api.json_response(200, jsonb_build_object('route', %L)); END; $body$, p_name));
$$;

DO $$
DECLARE
    v_response api.http_response;
    v_path text;
BEGIN
    RAISE DEBUG '-> A route matches only its own path';

    PERFORM pg_temp.route_named('anchor_probe', '/anchor', '^GET$');

    v_response := api.rest_invoke('GET', '/anchor', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/anchor should route to its handler', v_response, 200);

    v_response := api.rest_invoke('GET', '/anchorage', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/anchorage must not match the /anchor route', v_response, 404);

    v_response := api.rest_invoke('GET', '/anchor/nested', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/anchor/nested must not match the /anchor route', v_response, 404);

    v_response := api.rest_invoke('GET', '/anchor?limit=10', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('/anchor?limit=10 must still route', v_response, 200);

    FOREACH v_path IN ARRAY ARRAY['/healthz', '/helloworld', '/othello/'] LOOP
        v_response := api.rest_invoke('GET', v_path, ''::extensions.hstore, NULL::bytea);
        PERFORM pg_temp.expect_status(v_path || ' must not match /health or /hello', v_response, 404);
    END LOOP;
END $$;


DO $$
DECLARE
    v_case record;
    v_error text;
BEGIN
    RAISE DEBUG '-> Registration refuses what a path template cannot be';

    FOR v_case IN
        SELECT * FROM (VALUES
            ('{"path": "/files/{name}.json", "httpMethod": "^GET$", "name": "mixed_suffix"}'::jsonb,
             '%path segment "{name}.json" mixes literal text and a parameter%'),
            ('{"path": "/files/readme.{ext}", "httpMethod": "^GET$", "name": "mixed_prefix"}',
             '%path segment "readme.{ext}" mixes literal text and a parameter%'),
            ('{"path": "/a/{id}/b/{id}", "httpMethod": "^GET$", "name": "twice"}',
             '%path parameter "id" appears twice%'),
            ('{"path": "/a/{id:uuid}/b/{id}", "httpMethod": "^GET$", "name": "twice_typed"}',
             '%path parameter "id" appears twice%'),
            ('{"path": "/a/{id:float}", "httpMethod": "^GET$", "name": "float_type"}',
             '%unknown path parameter type "float"%'),
            ('{"path": "/trailing/", "httpMethod": "^GET$", "name": "trailing"}',
             '%not in canonical form%'),
            ('{"path": "/a//b", "httpMethod": "^GET$", "name": "doubled"}',
             '%not in canonical form%'),
            ('{"path": "/a?x=1", "httpMethod": "^GET$", "name": "query"}',
             '%not in canonical form%'),
            ('{"httpMethod": "^GET$", "name": "no_path"}',
             '%requires "path"%'),
            ('{"path": "/x", "uri": "^/x$", "httpMethod": "^GET$", "name": "uri_key"}',
             '%"uri" key is not supported%'),
            ('{"path": "/x", "example": "/x", "httpMethod": "^GET$", "name": "example_key"}',
             '%"example" key is not supported%'),
            ('{"path": "/x", "version": "^2$", "httpMethod": "^GET$", "name": "version_key"}',
             '%"version" key is not supported%'),
            ('{"path": "/x", "httpMethod": "GET", "name": "unanchored_method"}',
             '%httpMethod must be anchored%')
        ) AS t(metadata, expected)
    LOOP
        v_error := pg_temp.route_refusal(v_case.metadata || jsonb_build_object('id', gen_random_uuid()));
        IF coalesce(v_error, '') NOT LIKE v_case.expected THEN
            RAISE EXCEPTION 'TEST FAILED: % must be refused with %, got %',
                v_case.metadata, v_case.expected, COALESCE(v_error, 'no error');
        END IF;
    END LOOP;
END $$;


-- /rank-a registers the literal route first, /rank-b the parameter route
-- first: either way the literal path reaches the literal route, and the
-- policy lookup resolves the same handler (the isolation floor tells them
-- apart).
DO $$
DECLARE
    v_prefix text;
    v_response api.http_response;
    v_floor text;
BEGIN
    RAISE DEBUG '-> A literal segment outranks a parameter, in either registration order';

    PERFORM pg_temp.route_named('rank_a_stats', '/rank-a/stats', '^GET$', 'read committed');
    PERFORM pg_temp.route_named('rank_a_item', '/rank-a/{id}', '^GET$');
    PERFORM pg_temp.route_named('rank_b_item', '/rank-b/{id}', '^GET$');
    PERFORM pg_temp.route_named('rank_b_stats', '/rank-b/stats', '^GET$', 'read committed');

    FOREACH v_prefix IN ARRAY ARRAY['rank_a', 'rank_b'] LOOP
        v_response := api.rest_invoke('GET', '/' || replace(v_prefix, '_', '-') || '/stats', ''::extensions.hstore, NULL::bytea);
        IF api.content_json(v_response.content)->>'route' IS DISTINCT FROM v_prefix || '_stats' THEN
            RAISE EXCEPTION 'TEST FAILED: /%/stats must reach the literal route, got %',
                v_prefix, convert_from(v_response.content, 'UTF8');
        END IF;

        v_response := api.rest_invoke('GET', '/' || replace(v_prefix, '_', '-') || '/3f2b8c1e-0000-4000-8000-000000000001', ''::extensions.hstore, NULL::bytea);
        IF api.content_json(v_response.content)->>'route' IS DISTINCT FROM v_prefix || '_item' THEN
            RAISE EXCEPTION 'TEST FAILED: /%/<id> must reach the parameter route, got %',
                v_prefix, convert_from(v_response.content, 'UTF8');
        END IF;

        SELECT transaction_isolation INTO v_floor
        FROM api.rest_route_policy('GET', '/' || replace(v_prefix, '_', '-') || '/stats');
        IF v_floor IS DISTINCT FROM 'read committed' THEN
            RAISE EXCEPTION 'TEST FAILED: rest_route_policy must resolve /%/stats to the literal route', v_prefix;
        END IF;
        SELECT transaction_isolation INTO v_floor
        FROM api.rest_route_policy('GET', '/' || replace(v_prefix, '_', '-') || '/7');
        IF v_floor IS NOT NULL THEN
            RAISE EXCEPTION 'TEST FAILED: rest_route_policy must resolve /%/7 to the parameter route', v_prefix;
        END IF;
    END LOOP;
END $$;


DO $$
DECLARE
    v_error text;
BEGIN
    RAISE DEBUG '-> Two routes of one shape need different methods';

    PERFORM pg_temp.route_named('shape_project_read', '/shape-projects/{a}', '^GET$');

    v_error := pg_temp.route_refusal(jsonb_build_object(
        'id', gen_random_uuid(), 'path', '/shape-projects/{b}', 'httpMethod', '^(GET|DELETE)$', 'name', 'shape_project_same'));
    IF coalesce(v_error, '') NOT LIKE '%shape_project_same has the shape of shape_project_read%' THEN
        RAISE EXCEPTION 'TEST FAILED: a second route of one shape with a common method must be refused, got %',
            COALESCE(v_error, 'no error');
    END IF;

    v_error := pg_temp.route_refusal(jsonb_build_object(
        'id', gen_random_uuid(), 'path', '/shape-projects/{b}', 'httpMethod', '^POST$', 'name', 'shape_project_write'));
    IF v_error IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: one shape with disjoint methods must register, got %', v_error;
    END IF;

    PERFORM pg_temp.route_named('shape_team', '/shape-teams/{teamId}', '^GET$');
    PERFORM pg_temp.route_named('shape_team_members', '/shape-teams/{teamId}/members', '^GET$');
END $$;


-- The property the ranking needs, over everything registered by now: no two
-- routes with a common method share a shape.
DO $$
DECLARE
    v_bad text;
BEGIN
    RAISE DEBUG '-> No two routes with a common method share a shape';

    SELECT string_agg(format('%s and %s (%s)', r1.route_name, r2.route_name, r1.canonical_path), '; ')
    INTO v_bad
    FROM api.rest_route r1
    JOIN api.rest_route r2
      ON r1.handler_object_id < r2.handler_object_id
     AND internal.route_shape(r1.canonical_path) = internal.route_shape(r2.canonical_path)
    WHERE EXISTS (
        SELECT 1
        FROM unnest(ARRAY['GET','POST','PUT','DELETE','PATCH','HEAD','OPTIONS']) AS m(method)
        WHERE m.method ~ r1.method_regexp AND m.method ~ r2.method_regexp);

    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: routes the ranking cannot order: %', v_bad;
    END IF;
END $$;


DO $$
DECLARE
    v_response api.http_response;
    v_body jsonb;
BEGIN
    RAISE DEBUG '-> Typed path parameters reach the handler by name, or answer 400';

    PERFORM pg_temp.echo_params('typed_project', '/typed-projects/{id:uuid}');
    PERFORM pg_temp.echo_params('typed_count', '/typed-counts/{n:int}');
    PERFORM pg_temp.echo_params('typed_name', '/typed-names/{name}/notes/{noteId:int}');
    PERFORM pg_temp.echo_params('typed_private', '/typed-private/{id:uuid}', 'authenticated');

    v_response := api.rest_invoke('GET', '/typed-projects/3f2b8c1e-0000-4000-8000-00000000000a', ''::extensions.hstore, NULL::bytea);
    IF v_response.status_code IS DISTINCT FROM 200
       OR api.content_json(v_response.content) IS DISTINCT FROM '{"id": "3f2b8c1e-0000-4000-8000-00000000000a"}'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a uuid parameter must reach the handler by name, got % %',
            v_response.status_code, convert_from(v_response.content, 'UTF8');
    END IF;

    v_response := api.rest_invoke('GET', '/typed-projects/not-a-uuid', ''::extensions.hstore, NULL::bytea);
    v_body := api.content_json(v_response.content);
    IF v_response.status_code IS DISTINCT FROM 400
       OR v_body->>'code' IS DISTINCT FROM 'pgmi.invalid_path_parameter'
       OR v_body->'invalid-params'->0->>'name' IS DISTINCT FROM 'id' THEN
        RAISE EXCEPTION 'TEST FAILED: a malformed uuid must answer 400 invalid_path_parameter naming id, got % %',
            v_response.status_code, v_body;
    END IF;

    v_response := api.rest_invoke('GET', '/typed-private/not-a-uuid', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('an anonymous call to an authenticated typed route must be 401 before the parameter check', v_response, 401);

    FOREACH v_body IN ARRAY ARRAY['"+5"', '"%205"', '"5x"', '"1.5"']::jsonb[] LOOP
        v_response := api.rest_invoke('GET', '/typed-counts/' || (v_body #>> '{}'), ''::extensions.hstore, NULL::bytea);
        PERFORM pg_temp.expect_status('int parameter ' || v_body::text, v_response, 400);
    END LOOP;

    v_response := api.rest_invoke('GET', '/typed-counts/-12', ''::extensions.hstore, NULL::bytea);
    IF api.content_json(v_response.content) IS DISTINCT FROM '{"n": "-12"}'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a negative int must pass, got % %', v_response.status_code, convert_from(v_response.content, 'UTF8');
    END IF;

    v_response := api.rest_invoke('GET', '/typed-names/a%20b+c%2Fd/notes/7', ''::extensions.hstore, NULL::bytea);
    IF api.content_json(v_response.content) IS DISTINCT FROM '{"name": "a b+c/d", "noteId": "7"}'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: text parameters must arrive percent-decoded with + kept, got %',
            convert_from(v_response.content, 'UTF8');
    END IF;
END $$;


DO $$
DECLARE
    v_before text;
    v_doc jsonb;
BEGIN
    RAISE DEBUG '-> A parameter type is part of the published contract';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-4480-4000-8000-000000000001', 'path', '/typed-docs/{id}',
            'httpMethod', '^GET$', 'name', 'typed_docs', 'access', 'public'),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
    v_before := api.catalog_version();

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-4480-4000-8000-000000000001', 'path', '/typed-docs/{id:uuid}',
            'httpMethod', '^GET$', 'name', 'typed_docs', 'access', 'public'),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
    IF api.catalog_version() IS NOT DISTINCT FROM v_before THEN
        RAISE EXCEPTION 'TEST FAILED: changing {id} to {id:uuid} must change api.catalog_version()';
    END IF;

    PERFORM pg_temp.echo_params('typed_docs_count', '/typed-docs/{id:uuid}/revisions/{n:int}');
    v_doc := api.openapi_document()->'paths';
    IF v_doc->'/typed-docs/{id}'->'get'->'parameters'->0->'schema' IS DISTINCT FROM '{"type": "string", "format": "uuid"}'::jsonb
       OR v_doc->'/typed-docs/{id}/revisions/{n}'->'get'->'parameters'->1->'schema' IS DISTINCT FROM '{"type": "integer"}'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: OpenAPI must publish typed path parameters, got % and %',
            v_doc->'/typed-docs/{id}'->'get'->'parameters',
            v_doc->'/typed-docs/{id}/revisions/{n}'->'get'->'parameters';
    END IF;
END $$;
